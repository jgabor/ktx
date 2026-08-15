//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package logging

import (
	"errors"
	"os"
	"syscall"
	"time"
)

var errLockBusy = errors.New("logging lock busy")

func acquire(file *os.File, limit time.Duration) (time.Duration, error) {
	started := time.Now()
	deadline := started.Add(limit)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return time.Since(started), nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			return time.Since(started), err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return time.Since(started), errLockBusy
		}
		if remaining > lockPollInterval {
			remaining = lockPollInterval
		}
		time.Sleep(remaining)
	}
}

func release(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
