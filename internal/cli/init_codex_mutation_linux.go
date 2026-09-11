package cli

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func lockCodexDirectory(dir *os.File) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hooks.json update is busy; retry after other editors finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func openCodexDocument(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
