//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package db

import (
	"errors"
	"os"
	"syscall"
)

func tryLockWriterFile(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errWriterLockBusy
		}
		return err
	}
}

func unlockWriterFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
