package durablefile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var renameFile = os.Rename

// Write replaces path with complete data using a temporary file in the same
// directory. Existing file permissions are retained; perm applies to new files.
func Write(path string, data []byte, perm fs.FileMode) error {
	return WriteChecked(path, data, perm, nil)
}

// WriteChecked calls check after staging and syncing the complete replacement,
// immediately before rename. Callers coordinating a read/merge/write hold their
// lease across this call. The check alone is not a filesystem compare-and-swap.
func WriteChecked(path string, data []byte, perm fs.FileMode, check func() error) error {
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}

	temporary, err := os.CreateTemp(filepath.Dir(path), ".ktx-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	fail := func(err error) error {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(perm); err != nil {
		return fail(err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fail(err)
	}
	if err := temporary.Sync(); err != nil {
		return fail(err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	if err := renameFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}
