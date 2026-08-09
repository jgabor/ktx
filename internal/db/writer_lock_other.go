//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package db

import (
	"errors"
	"os"
)

var errWriterLockUnsupported = errors.New("writer coordination is unsupported on this platform")

func tryLockWriterFile(*os.File, coordinationMode) error { return errWriterLockUnsupported }

func unlockWriterFile(*os.File) error { return nil }
