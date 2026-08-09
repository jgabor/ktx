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

type coordinationMode uint8

const (
	coordinationShared coordinationMode = iota
	coordinationExclusive
)

// writerCoordinator coordinates supported shared and exclusive access to one
// database file. The operating system releases its lock when a process exits.
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

type coordinationLease struct{ file *os.File }

func (c *writerCoordinator) acquire(ctx context.Context, create bool, mode coordinationMode) (*coordinationLease, error) {
	flags := os.O_RDONLY
	if mode == coordinationExclusive {
		flags = os.O_RDWR
	}
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(c.path, flags, 0o666)
	if err != nil {
		return nil, fmt.Errorf("db: open coordination target %q: %w", c.path, err)
	}

	for {
		err = tryLockWriterFile(file, mode)
		if err == nil {
			break
		}
		if !errors.Is(err, errWriterLockBusy) {
			_ = file.Close()
			return nil, fmt.Errorf("db: acquire coordination for %q: %w", c.path, err)
		}
		timer := time.NewTimer(writerLockRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		}
	}
	return &coordinationLease{file: file}, nil
}

func (l *coordinationLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return errors.Join(unlockWriterFile(l.file), l.file.Close())
}

func (c *writerCoordinator) withLock(ctx context.Context, create bool, fn func() error) (err error) {
	if c == nil {
		return fn()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	lease, err := c.acquire(ctx, create, coordinationExclusive)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lease.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("db: release writer coordination for %q: %w", c.path, closeErr)
		}
	}()

	return fn()
}
