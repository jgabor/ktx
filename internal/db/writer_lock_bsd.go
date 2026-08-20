//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package db

import (
	"errors"
	"os"
	"syscall"
)

func tryLockWriterFile(file *os.File, mode coordinationMode) error {
	return tryLockWriterRange(file, mode, 0)
}

func unlockWriterFile(file *os.File) error {
	return unlockWriterRange(file, 0)
}

func tryLockWriterGate(file *os.File, mode coordinationMode) error {
	return tryLockWriterRange(file, mode, 1)
}

func unlockWriterGate(file *os.File) error {
	return unlockWriterRange(file, 1)
}

func tryLockWriterRange(file *os.File, mode coordinationMode, start int64) error {
	lockType := int16(syscall.F_RDLCK)
	if mode == coordinationExclusive {
		lockType = syscall.F_WRLCK
	}
	err := syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &syscall.Flock_t{
		Type: lockType, Whence: 0, Start: start, Len: 1,
	})
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN) {
		return errWriterLockBusy
	}
	return err
}

func unlockWriterRange(file *os.File, start int64) error {
	return syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &syscall.Flock_t{
		Type: syscall.F_UNLCK, Whence: 0, Start: start, Len: 1,
	})
}
