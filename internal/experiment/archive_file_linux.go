package experiment

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openArchiveRegular(root *os.Root, name string) (*os.File, error) {
	return openArchiveNode(root, name, false)
}

func openArchiveNode(root *os.Root, name string, directory bool) (*os.File, error) {
	// Diagnose devices/sockets without opening them. This is not the
	// confinement or type authority: both are enforced again on the open.
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() && (!directory || !info.IsDir()) {
		return nil, fmt.Errorf("unsupported archive file type %s: %q", info.Mode().Type(), name)
	}
	// Root enforces confinement during open, including concurrent symlink
	// replacement. NONBLOCK prevents a substituted FIFO from hanging inspection;
	// the descriptor's type, not an earlier stat, controls whether we read it.
	f, err := root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err == nil && !info.Mode().IsRegular() && (!directory || !info.IsDir()) {
		err = fmt.Errorf("unsupported archive file type %s: %q", info.Mode().Type(), name)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// SQLite resolves a database symlink before deriving its sidecar names. Use
// the pinned descriptor's location, not a second resolution of the alias.
func (a *archive) databaseName(file *os.File) (string, error) {
	dir, err := a.Open(".")
	if err != nil {
		return "", err
	}
	defer dir.Close()
	rootName, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", dir.Fd()))
	if err != nil {
		return "", err
	}
	fileName, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return "", err
	}
	name, err := filepath.Rel(rootName, fileName)
	if err != nil {
		return "", err
	}
	return name, archiveRelative(name)
}
