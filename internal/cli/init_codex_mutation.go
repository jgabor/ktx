package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jgabor/ktx/internal/durablefile"
)

var codexWriteDocument = durablefile.WriteChecked

// A directory lease survives hooks.json replacement and needs no lock file.
// Every participating mutation reads only after acquiring it. External editors
// must cooperate for full lost-update protection; unchanged detects observed
// edits, not writes in the final check-to-rename/remove window.
type codexMutation struct {
	target string
	parent string
	dir    *os.File
	info   os.FileInfo
	raw    []byte
}

func beginCodexMutation(target string, create bool) (*codexMutation, error) {
	parent := filepath.Dir(target)
	if create {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return nil, err
		}
	}
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, err
	}
	dir, err := os.Open(canonical)
	if err != nil {
		return nil, err
	}
	if err := lockCodexDirectory(dir); err != nil {
		_ = dir.Close()
		return nil, err
	}
	m := &codexMutation{target: filepath.Join(canonical, filepath.Base(target)), parent: parent, dir: dir}
	m.raw, m.info, err = readCodexSnapshot(m.target)
	if err == nil {
		err = m.unchanged()
	}
	if err != nil {
		m.close()
		return nil, err
	}
	return m, nil
}

func (m *codexMutation) close() { _ = m.dir.Close() }

func readCodexSnapshot(target string) ([]byte, os.FileInfo, error) {
	f, err := openCodexDocument(target)
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
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("hooks.json must be a regular file")
	}
	raw, err := io.ReadAll(f)
	return raw, info, err
}

func (m *codexMutation) unchanged() error {
	conflict := func() error {
		return fmt.Errorf("hooks.json or its directory changed during update; retry after other editors finish")
	}
	held, err := m.dir.Stat()
	if err != nil {
		return err
	}
	current, err := os.Stat(m.parent)
	if err != nil || !os.SameFile(held, current) {
		return conflict()
	}
	current, err = os.Stat(filepath.Dir(m.target))
	if err != nil || !os.SameFile(held, current) {
		return conflict()
	}
	raw, info, err := readCodexSnapshot(m.target)
	if err != nil {
		return conflict()
	}
	if (info == nil) != (m.info == nil) {
		return conflict()
	}
	if info != nil && (!os.SameFile(info, m.info) || info.Mode() != m.info.Mode() || !info.ModTime().Equal(m.info.ModTime()) || !bytes.Equal(raw, m.raw)) {
		return conflict()
	}
	return nil
}

func (m *codexMutation) publish(data []byte) error {
	return codexWriteDocument(m.target, data, 0o644, m.unchanged)
}

func (m *codexMutation) remove() error {
	if err := m.unchanged(); err != nil {
		return err
	}
	return os.Remove(m.target)
}
