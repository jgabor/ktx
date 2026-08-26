package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const writerLockRetryDelay = 5 * time.Millisecond

const maxReportedLockHolders = 8

var (
	errWriterLockBusy          = errors.New("writer lock busy")
	errCoordinationUnavailable = errors.New("database coordination unavailable")
)

type coordinationMode uint8

const (
	coordinationShared coordinationMode = iota
	coordinationExclusive
)

func (m coordinationMode) String() string {
	if m == coordinationExclusive {
		return "exclusive"
	}
	return "shared"
}

// LockHolder is bounded ownership information observed for a coordination timeout.
type LockHolder struct {
	PID     int
	Mode    string
	Command string
}

// BlockerCommandLabel returns the closed label for a supported ktx command.
// It never returns argument or payload text.
func BlockerCommandLabel(argv []string) string {
	if len(argv) < 2 {
		return "unknown"
	}
	args := argv[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--quiet", "-q":
			args = args[1:]
		case "--format":
			if len(args) < 2 {
				return "unknown"
			}
			args = args[2:]
		default:
			if strings.HasPrefix(args[0], "--format=") ||
				strings.HasPrefix(args[0], "--quiet=") || strings.HasPrefix(args[0], "-q=") {
				args = args[1:]
				continue
			}
			return "unknown"
		}
	}
	if len(args) == 0 {
		return "unknown"
	}
	switch args[0] {
	case "write", "compress", "hook", "run", "get", "read", "list", "stats", "decompress", "bench", "doctor":
		return args[0]
	case "telemetry":
		if len(args) > 1 && (args[1] == "record" || args[1] == "list") {
			return "telemetry " + args[1]
		}
	case "db":
		nested := args[1:]
		if len(nested) > 0 && nested[0] == "--path" {
			if len(nested) < 2 {
				return "unknown"
			}
			nested = nested[2:]
		} else if len(nested) > 0 && strings.HasPrefix(nested[0], "--path=") {
			nested = nested[1:]
		}
		if len(nested) > 0 && (nested[0] == "migrate" || nested[0] == "report") {
			return "db " + nested[0]
		}
	}
	return "unknown"
}

type coordinationTimeoutError struct {
	RequestedMode              coordinationMode
	OwnershipSnapshotAvailable bool
	LockHolders                []LockHolder
	err                        error
}

func (e *coordinationTimeoutError) Error() string { return e.err.Error() }
func (e *coordinationTimeoutError) Unwrap() error { return e.err }

func newCoordinationTimeout(mode coordinationMode, file *os.File, path string, cause error) error {
	holders, available := observeLockHolders(file, path, mode)
	return &coordinationTimeoutError{
		RequestedMode:              mode,
		OwnershipSnapshotAvailable: available,
		LockHolders:                holders,
		err:                        fmt.Errorf("%w: %w", errCoordinationUnavailable, cause),
	}
}

// writerCoordinator coordinates supported shared and exclusive access to one
// database file. A short admission lock prevents new readers from passing a
// waiting writer. The operating system releases both locks when a process exits.
type writerCoordinator struct {
	path         string
	mu           chan struct{}
	gateAcquired func()
}

func newWriterCoordinator(path string) (*writerCoordinator, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return nil, err
	}
	return &writerCoordinator{path: canonical, mu: make(chan struct{}, 1)}, nil
}

type coordinationLease struct{ file *os.File }

func (c *writerCoordinator) acquire(ctx context.Context, create bool, mode coordinationMode) (*coordinationLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, newCoordinationTimeout(mode, nil, c.path, err)
	}
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
		err = tryLockWriterGate(file, mode)
		if err == nil {
			if c.gateAcquired != nil {
				c.gateAcquired()
			}
			break
		}
		if !errors.Is(err, errWriterLockBusy) {
			_ = file.Close()
			return nil, fmt.Errorf("db: acquire coordination gate for %q: %w", c.path, err)
		}
		timer := time.NewTimer(writerLockRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			timeout := newCoordinationTimeout(mode, file, c.path, ctx.Err())
			_ = file.Close()
			return nil, timeout
		}
	}

	for {
		err = tryLockWriterFile(file, mode)
		if err == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = unlockWriterFile(file)
				_ = unlockWriterGate(file)
				timeout := newCoordinationTimeout(mode, file, c.path, ctxErr)
				_ = file.Close()
				return nil, timeout
			}
			break
		}
		if !errors.Is(err, errWriterLockBusy) {
			_ = unlockWriterGate(file)
			_ = file.Close()
			return nil, fmt.Errorf("db: acquire coordination for %q: %w", c.path, err)
		}
		timer := time.NewTimer(writerLockRetryDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			timeout := newCoordinationTimeout(mode, file, c.path, ctx.Err())
			_ = unlockWriterGate(file)
			_ = file.Close()
			return nil, timeout
		}
	}
	if err := unlockWriterGate(file); err != nil {
		_ = unlockWriterFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("db: release coordination gate for %q: %w", c.path, err)
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

	if ctxErr := ctx.Err(); ctxErr != nil {
		return newCoordinationTimeout(coordinationExclusive, nil, c.path, ctxErr)
	}
	select {
	case c.mu <- struct{}{}:
		defer func() { <-c.mu }()
	case <-ctx.Done():
		return newCoordinationTimeout(coordinationExclusive, nil, c.path, ctx.Err())
	}

	lease, err := c.acquire(ctx, create, coordinationExclusive)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lease.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("db: release writer coordination for %q: %w", c.path, closeErr)
		}
	}()
	if err := auditDatabaseAccess("exclusive_coordination", c.path); err != nil {
		return err
	}

	return fn()
}

// IsDatabaseUnavailable reports whether err exhausted SQLite locking or
// supported-process coordination.
func IsDatabaseUnavailable(err error) bool {
	return IsSQLiteLockError(err) || errors.Is(err, errCoordinationUnavailable)
}
