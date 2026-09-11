package experiment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// An archive keeps one directory handle for the entire interpretation. Checks
// of path strings or symlink metadata do not authorize a later pathname open:
// every read and replacement still goes through this root.
type archive struct{ *os.Root }

type archiveAccessError struct{ err error }

func (e *archiveAccessError) Error() string { return "archive access: " + e.err.Error() }
func (e *archiveAccessError) Unwrap() error { return e.err }

func archiveError(err error) error {
	if err == nil || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return err
	}
	return &archiveAccessError{err}
}

func unsafeArchiveAccess(err error) bool {
	var access *archiveAccessError
	return errors.As(err, &access)
}

func openArchive(directory string) (*archive, error) {
	directory = filepath.Clean(directory)
	parent, err := os.OpenRoot(filepath.Dir(directory))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	root, err := parent.OpenRoot(filepath.Base(directory))
	if err != nil {
		return nil, archiveError(err)
	}
	return &archive{root}, nil
}

func archiveRelative(name string) error {
	if !filepath.IsLocal(name) {
		return &archiveAccessError{fmt.Errorf("path must be archive-relative: %q", name)}
	}
	return nil
}

func (a *archive) open(name string) (*os.File, error) {
	if err := archiveRelative(name); err != nil {
		return nil, err
	}
	f, err := openArchiveRegular(a.Root, filepath.FromSlash(name))
	return f, archiveError(err)
}

// OpenArchiveFile opens a regular file confined to the selected archive. It
// does not impose an evidence-validity contract on discovery or inspection.
func OpenArchiveFile(directory, name string) (*os.File, error) {
	a, err := openArchive(directory)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	return a.open(name)
}

func (a *archive) readFile(name string) ([]byte, error) {
	f, err := a.open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (a *archive) hash(name string) (string, error) {
	f, err := a.open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (a *archive) stat(name string) (fs.FileInfo, error) {
	if err := archiveRelative(name); err != nil {
		return nil, err
	}
	info, err := a.Stat(name)
	if err != nil {
		return nil, archiveError(err)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return nil, &archiveAccessError{fmt.Errorf("unsupported archive file type %s: %q", info.Mode().Type(), name)}
	}
	return info, nil
}

func (a *archive) directoryHash(name string) (string, error) {
	if err := archiveRelative(name); err != nil {
		return "", err
	}
	var paths []string
	if err := fs.WalkDir(archiveTreeFS{a}, name, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != name {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return "", archiveError(err)
	}
	sort.Strings(paths)
	digest := sha256.New()
	for _, path := range paths {
		info, err := a.Lstat(path)
		if err != nil {
			return "", archiveError(err)
		}
		relative, _ := filepath.Rel(name, path)
		relative = filepath.ToSlash(relative)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			// Tree hashes retain link text, not target bytes. Do not follow a
			// link merely to hash it; this is also the producer's hash format.
			target, err := a.Readlink(path)
			if err != nil {
				return "", archiveError(err)
			}
			fmt.Fprintf(digest, "link\x00%s\x00%s\x00", relative, target)
		case info.Mode().IsRegular():
			file, err := a.open(path)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(digest, "file\x00%s\x00", relative)
			_, copyErr := io.Copy(digest, file)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return "", err
			}
			digest.Write([]byte{0})
		case !info.IsDir():
			return "", &archiveAccessError{fmt.Errorf("unsupported archive file type %s: %q", info.Mode().Type(), path)}
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// WalkDir does not follow tree-entry symlinks. All directory opens still need
// confinement and descriptor type checks if an entry changes during the walk.
type archiveTreeFS struct{ a *archive }

func (f archiveTreeFS) Open(name string) (fs.File, error) {
	if err := archiveRelative(name); err != nil {
		return nil, err
	}
	file, err := openArchiveNode(f.a.Root, name, true)
	return file, archiveError(err)
}

func (f archiveTreeFS) Stat(name string) (fs.FileInfo, error) { return f.a.stat(name) }

func (a *archive) writeSummary(summary Summary) error {
	data, err := MarshalJSON(summary)
	if err != nil {
		return err
	}
	name := ".summary-" + rand.Text()
	f, err := a.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = a.Remove(name) }()
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := a.Rename(name, "summary.json"); err != nil {
		return err
	}
	dir, err := a.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
