//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package db

import (
	"errors"
	"os"
	"syscall"
)

func tryLockWriterFile(file *os.File, mode coordinationMode) error {
	operation := syscall.LOCK_SH
	if mode == coordinationExclusive {
		operation = syscall.LOCK_EX
	}
	for {
		err := syscall.Flock(int(file.Fd()), operation|syscall.LOCK_NB)
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

func tryLockWriterGate(file *os.File, mode coordinationMode) error {
	lockType := int16(syscall.F_RDLCK)
	if mode == coordinationExclusive {
		lockType = syscall.F_WRLCK
	}
	err := syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &syscall.Flock_t{
		Type:   lockType,
		Whence: 0,
		Start:  1,
		Len:    1,
	})
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN) {
		return errWriterLockBusy
	}
	return err
}

func unlockWriterGate(file *os.File) error {
	return syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &syscall.Flock_t{
		Type:   syscall.F_UNLCK,
		Whence: 0,
		Start:  1,
		Len:    1,
	})
}

func unlockWriterFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
