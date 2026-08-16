//go:build !linux

package db

import "os"

func observeLockHolders(*os.File, string, coordinationMode) ([]LockHolder, bool) {
	return nil, false
}
