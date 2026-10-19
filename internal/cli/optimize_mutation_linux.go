package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type optimizeTargetMutation struct {
	path  string
	root  *os.Root
	roots []*os.Root
	locks []*os.File
	raw   []byte
	info  os.FileInfo
}

func beginOptimizeTarget(path string, create bool) (m *optimizeTargetMutation, returnedErr error) {
	m = &optimizeTargetMutation{path: path}
	defer func() {
		if returnedErr != nil {
			m.close()
		}
	}()
	parent := filepath.Dir(path)
	nearest, err := optimizeNearestDirectory(parent)
	if err != nil {
		return m, err
	}
	open := func(name string) (*os.Root, error) {
		root, err := os.OpenRoot(name)
		if err != nil {
			return nil, err
		}
		m.roots = append(m.roots, root)
		f, err := root.Open(".")
		if err != nil {
			return nil, err
		}
		m.locks = append(m.locks, f)
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				return root, nil
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
				return nil, err
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("configuration update is busy; retry after other mutations finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	ancestor, err := open(nearest)
	if err != nil {
		return m, err
	}
	if nearest != parent {
		if !create {
			return m, nil
		}
		relative, err := filepath.Rel(nearest, parent)
		if err != nil {
			return m, err
		}
		if err := ancestor.MkdirAll(relative, 0o700); err != nil {
			return m, err
		}
		if err := optimizeNoSymlinkPath(parent); err != nil {
			return m, err
		}
		m.root, err = open(parent)
		if err != nil {
			return m, err
		}
	} else {
		m.root = ancestor
	}
	return m, nil
}

// optimizeNearestDirectory returns the closest existing ancestor of parent
// after checking that it is a plain directory reached without symlinks.
func optimizeNearestDirectory(parent string) (string, error) {
	nearest := parent
	for {
		info, err := os.Lstat(nearest)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("target parent is not a plain directory")
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		next := filepath.Dir(nearest)
		if next == nearest {
			return "", err
		}
		nearest = next
	}
	if err := optimizeNoSymlinkPath(nearest); err != nil {
		return "", err
	}
	return nearest, nil
}

// optimizeTargetFileSafe accepts only targets that apply can replace and
// rollback can restore exactly.
func optimizeTargetFileSafe(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return fmt.Errorf("target must be a singly linked, owned regular file without special mode bits")
	}
	return nil
}

// checkOptimizeTarget applies the mutation path and file rules without
// locking, creating, or writing anything, so screening can refuse targets
// that a later apply would refuse.
func checkOptimizeTarget(path string) error {
	parent := filepath.Dir(path)
	nearest, err := optimizeNearestDirectory(parent)
	if err != nil || nearest != parent {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target is a symlink")
	}
	return optimizeTargetFileSafe(info)
}

func (m *optimizeTargetMutation) close() {
	for i := len(m.locks) - 1; i >= 0; i-- {
		_ = m.locks[i].Close()
	}
	for i := len(m.roots) - 1; i >= 0; i-- {
		_ = m.roots[i].Close()
	}
}

func (m *optimizeTargetMutation) snapshot() ([]byte, os.FileInfo, error) {
	if m.root == nil {
		if _, err := os.Lstat(filepath.Dir(m.path)); !os.IsNotExist(err) {
			return nil, nil, conflictError("target parent appeared during preview; retry")
		}
		return nil, nil, nil
	}
	if err := optimizeNoSymlinkPath(filepath.Dir(m.path)); err != nil {
		return nil, nil, err
	}
	held, err := m.root.Stat(".")
	if err != nil {
		return nil, nil, err
	}
	current, err := os.Lstat(filepath.Dir(m.path))
	if err != nil || !os.SameFile(held, current) {
		return nil, nil, conflictError("captured target directory changed during mutation")
	}
	f, err := m.root.OpenFile(filepath.Base(m.path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if err := optimizeTargetFileSafe(info); err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, optimizeConfigLimit+1))
	if err == nil && len(data) > optimizeConfigLimit {
		err = fmt.Errorf("target exceeds configuration bound")
	}
	return data, info, err
}

func (m *optimizeTargetMutation) read() ([]byte, os.FileInfo, error) {
	var err error
	m.raw, m.info, err = m.snapshot()
	return m.raw, m.info, err
}

func (m *optimizeTargetMutation) unchanged() error {
	data, info, err := m.snapshot()
	if err != nil {
		return err
	}
	if (info == nil) != (m.info == nil) || !bytes.Equal(data, m.raw) {
		return conflictError("target changed during mutation; no overwrite")
	}
	if info != nil && (!os.SameFile(info, m.info) || info.Mode() != m.info.Mode() || optimizeFileGroup(info) != optimizeFileGroup(m.info)) {
		return conflictError("target identity or mode changed during mutation; no overwrite")
	}
	return nil
}

func (m *optimizeTargetMutation) publish(data []byte, mode os.FileMode, group int, hooks optimizeMutationHooks) error {
	return optimizeAtomicRootWrite(m.root, filepath.Base(m.path), data, mode, group, m.unchanged, hooks)
}

func (m *optimizeTargetMutation) remove(hooks optimizeMutationHooks) error {
	if hooks.beforePublish != nil {
		if err := hooks.beforePublish(); err != nil {
			return err
		}
	}
	if err := m.unchanged(); err != nil {
		return err
	}
	if err := m.root.Remove(filepath.Base(m.path)); err != nil {
		return err
	}
	if err := optimizeSyncRoot(m.root); err != nil {
		return err
	}
	if hooks.afterPublish != nil {
		return hooks.afterPublish()
	}
	return nil
}

func optimizeFileGroup(info os.FileInfo) int { return int(info.Sys().(*syscall.Stat_t).Gid) }

func optimizeNewGroup(m *optimizeTargetMutation) int {
	if m.root != nil {
		if info, err := m.root.Stat("."); err == nil && info.Mode()&os.ModeSetgid != 0 {
			return optimizeFileGroup(info)
		}
	}
	return os.Getegid()
}
