package experiment

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jgabor/ktx/internal/db"
)

// SQLite opens sidecars and coordinates/migrates by pathname. Never give it
// an archive pathname, even read-only: copy through confined descriptors into
// a private directory, then let normal readiness operate only on that copy.
func (a *archive) openDatabase(name string) (*db.DB, func(), error) {
	cleanup := func() {}
	main, err := a.open(name)
	if err != nil {
		return nil, cleanup, err
	}
	defer main.Close()
	physical, err := a.databaseName(main)
	if err != nil {
		return nil, cleanup, err
	}
	directory, err := os.MkdirTemp("", "ktx-archive-db-")
	if err != nil {
		return nil, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(directory) }
	path := filepath.Join(directory, "archive.db")
	copyFile := func(file *os.File, destination string) error {
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, file)
		return errors.Join(copyErr, out.Close())
	}
	if err = copyFile(main, path); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		file, openErr := a.open(physical + suffix)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			cleanup()
			return nil, func() {}, openErr
		}
		switch suffix {
		case "-journal":
			// A hot rollback journal can name an arbitrary super-journal.
			// Closed owned archives may retain an empty/zeroed PERSIST journal,
			// but recovery of an interrupted rollback transaction is not safe
			// through SQLite's host filesystem and must not be attempted here.
			var header [1]byte
			n, readErr := file.Read(header[:])
			if readErr != nil && readErr != io.EOF {
				err = readErr
			}
			if n != 0 && header[0] != 0 {
				err = &archiveAccessError{fmt.Errorf("unsupported archive database rollback journal with nonzero header: %q", physical+suffix)}
			}
		case "-wal":
			err = copyFile(file, path+suffix)
		case "-shm":
			// The WAL index is derived; SQLite rebuilds it on the private copy.
		}
		closeErr := file.Close()
		if err = errors.Join(err, closeErr); err != nil {
			cleanup()
			return nil, func() {}, err
		}
	}
	database, err := db.OpenQuery(path)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return database, cleanup, nil
}
