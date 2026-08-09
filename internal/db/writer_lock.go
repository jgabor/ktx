package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const writerLockRetryDelay = 5 * time.Millisecond

var errWriterLockBusy = errors.New("writer lock busy")

// writerCoordinator serializes supported writers for one database file. The
// operating system releases its lock when a process exits.
type writerCoordinator struct {
	path string
	mu   sync.Mutex
}

func newWriterCoordinator(path string) (*writerCoordinator, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return nil, err
	}
	return &writerCoordinator{path: canonical}, nil
}

func (c *writerCoordinator) withLock(ctx context.Context, create bool, fn func() error) (err error) {
	if c == nil {
		return fn()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(c.path, flags, 0o666)
	if err != nil {
		return fmt.Errorf("db: open writer coordination target %q: %w", c.path, err)
	}
	defer file.Close()

	for {
		err = tryLockWriterFile(file)
		if err == nil {
			break
		}
		if !errors.Is(err, errWriterLockBusy) {
			return fmt.Errorf("db: acquire writer coordination for %q: %w", c.path, err)
		}
		timer := time.NewTimer(writerLockRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	defer func() {
		unlockErr := unlockWriterFile(file)
		if err == nil && unlockErr != nil {
			err = fmt.Errorf("db: release writer coordination for %q: %w", c.path, unlockErr)
		}
	}()

	return fn()
}
