package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	// The deadline covers all configured busy waits and retry backoffs, with
	// room for the transaction itself and scheduler delay.
	writeTimeout     = 10 * time.Second
	writeMaxAttempts = 8
	// Eight driver waits plus deterministic backoff total at most 8.36s before
	// scheduling overhead. Ordinary writes use writeTimeout, with one final
	// context-unaware driver wait able to outlive that deadline.
	sqliteBusyTimeoutMS = 1000
)

func writeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), writeTimeout)
}

type transactionStarter interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// withWriteTx retries only transactions that definitely did not commit. The
// pinned modernc driver rolls back after a failed Commit; that makes its
// BUSY/LOCKED commit errors safe to retry without duplicating writes.
func withWriteTx(ctx context.Context, database transactionStarter, fn func(*sql.Tx) error) error {
	var lastErr error
	for attempt := 0; attempt < writeMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			lastErr = err
			if !isSQLiteLockError(err) || !waitWriteRetry(ctx, attempt) {
				return err
			}
			continue
		}

		if err := fn(tx); err != nil {
			lastErr = err
			_ = tx.Rollback()
			if !isSQLiteLockError(err) || !waitWriteRetry(ctx, attempt) {
				return err
			}
			continue
		}

		err = tx.Commit()
		if err == nil {
			return nil
		}
		lastErr = err
		if isSQLiteLockError(err) && waitWriteRetry(ctx, attempt) {
			continue
		}
		// Non-lock commit failures can be ambiguous. Never replay those.
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("db: write retries exhausted: %w", lastErr)
}

// IsSQLiteLockError reports whether err wraps a SQLite lock failure.
// Callers use it to render the stable database-availability contract without
// exposing driver-specific diagnostics.
func IsSQLiteLockError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

func isSQLiteLockError(err error) bool { return IsSQLiteLockError(err) }

func waitWriteRetry(ctx context.Context, attempt int) bool {
	delay := time.Duration(attempt+1) * 10 * time.Millisecond
	if delay > 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
