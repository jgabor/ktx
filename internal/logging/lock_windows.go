//go:build windows

package logging

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

var errLockBusy = errors.New("logging lock busy")

func acquire(file *os.File, limit time.Duration) (time.Duration, error) {
	started := time.Now()
	deadline := started.Add(limit)
	for {
		err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
		if err == nil {
			return time.Since(started), nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}
