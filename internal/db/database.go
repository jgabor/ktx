// Package db owns database lifecycle, persistence, and schema evolution.
// the on-disk SQLite schema.
//
// The database is a single SQLite file with core, benchmark, reversible
// artifact, recorded-stream, and optional session-telemetry tables:
// sessions (aggregate, INSERT on open + UPDATE roll-up),
// tool_calls (append-only, INSERT only), bench_runs (the benchmark
// headline store; one row per `ktx bench run` invocation; same
// nanoid-keyed shape as the other tables), compressed (the
// reversibility layer; one row per `ktx compress` invocation;
// references tool_calls.id via source_id FK), and recorded_streams (the
// recording contract; one row per `ktx bench run` invocation, or per
// opt-in `ktx run --record-stream`; holds the verbatim JSONL stream the
// agent emitted — the authoritative cost evidence; references
// sessions.id via a NOT NULL FK and bench_runs.id via a NULLABLE FK so a
// ktx-run-captured stream can carry no bench parent). session_telemetry keeps
// accepted model-step observations separate from those evidence families and
// associates them to sessions by foreign key. IDs are 16-char lowercase alpha
// nanoids where a table has a standalone ID.
// Content hashes are SHA-256 of the output field, stored as a separate
// column for verification without being the primary key.
//
// The compressed table is the reversibility layer: it
// holds compressed artifacts SEPARATE from tool_calls (which the bench
// cost-aggregates — injecting zero-cost non-evidence rows would
// corrupt the cost-summed evidence set). compressed.source_id
// REFERENCES tool_calls(id); with foreign_keys=1 enforced by Open's
// DSN, a compressed row cannot point at a non-existent tool_call.
// `ktx decompress <id>` resolves source_id and returns the source's
// original output bytes verbatim — reversal via retained originals at
// the pipeline boundary, not per-stage inversion.
//
// Dependencies are pinned here so the choice is explicit:
//
//   - github.com/matoous/go-nanoid/v2 — 16-char lowercase alpha IDs
//   - modernc.org/sqlite              — pure-Go SQLite driver, no CGO
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	// Register the pure-Go SQLite driver used by database/sql.
	_ "modernc.org/sqlite"
)

// IDs use 16 lowercase letters, providing roughly 75 bits of entropy without
// ambiguous numeric characters.
const (
	idAlphabet = "abcdefghijklmnopqrstuvwxyz"
	idLength   = 16

	// A hook opens its database before its configured pipeline timeout starts.
	// Two one-second driver busy waits keep startup bounded near the default
	// two-second critical-path budget while allowing a lock that outlives one
	// busy wait to clear. The attempt limit also bounds context-unaware driver
	// waits.
	startupRetryDeadline    = 2 * time.Second
	startupRetryMaxAttempts = 2
)

// sqliteDriver is the registered name of the modernc.org/sqlite driver.
const sqliteDriver = "sqlite"

func DefaultPath() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "ktx", "ktx.db"), nil
}

// ResolvePath applies the database path precedence:
//
//  1. override (the --path flag value, empty when absent)
//  2. KTX_DB environment variable
//  3. DefaultPath ($XDG_DATA_HOME/ktx/ktx.db)
//
// The --path flag is per-invocation only; nothing is persisted.
func ResolvePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if envPath := os.Getenv("KTX_DB"); envPath != "" {
		return envPath, nil
	}
	return DefaultPath()
}

// IDPattern is the canonical regex for ktx IDs. Exported so
// the cli layer can validate sampled IDs without re-encoding the
// alphabet/length contract. Lives next to the schema / open helpers
// because the pattern is the on-the-wire contract for the IDs the
// schema and the open path produce together.
var IDPattern = regexp.MustCompile(`^[a-z]{16}$`)

// Open opens (or creates) the ktx database at path and ensures the schema is
// migrated. The parent directory is created if missing. Foreign
// keys are enforced so the tool_calls.session_id REFERENCES clause is
// meaningful, not decorative.
//
// PRAGMA user_version gates migration so a current-schema open performs no
// write transaction. Stale schemas are migrated and versioned atomically.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("db: open: path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("db: open: create directory for %q: %w", path, err)
	}

	// modernc.org/sqlite reads _pragma query params and applies them per
	// connection. foreign_keys(1) enforces the tool_calls -> sessions FK on
	// every pooled connection, not just the first.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(%d)&_pragma=synchronous(NORMAL)&_txlock=immediate", path, sqliteBusyTimeoutMS)

	db, err := openSQLiteWithRetry(dsn, 0, startupLockRetryPolicy())
	if err != nil {
		return nil, fmt.Errorf("db: open %q: %w", path, err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: migrate %q: %w", path, err)
	}

	return &DB{db: db}, nil
}

// OpenReadOnly opens an existing SQLite database with a single connection.
// SQLite's mode=ro is the write-enforcement boundary; query_only adds defense
// in depth. The caller owns the returned pool and must close it.
func OpenReadOnly(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: absolute}).String() + fmt.Sprintf("?mode=ro&_pragma=busy_timeout(%d)", sqliteBusyTimeoutMS)
	database, err := openSQLiteWithRetry(uri, 1, startupLockRetryPolicy())
	if err != nil {
		return nil, err
	}
	if _, err := database.Exec("PRAGMA query_only = ON"); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

type sqliteLockRetryPolicy struct {
	deadline    time.Duration
	maxAttempts int
	retryDelay  func(int) time.Duration
}

func startupLockRetryPolicy() sqliteLockRetryPolicy {
	return sqliteLockRetryPolicy{
		deadline:    startupRetryDeadline,
		maxAttempts: startupRetryMaxAttempts,
		retryDelay:  func(int) time.Duration { return 0 },
	}
}

// openSQLiteWithRetry retries only physical-connection startup failures caused
// by SQLite's BUSY or LOCKED codes. Each failed pool is closed before another
// connection can be opened, so its connection and any partially applied
// pragmas cannot outlive the retry.
func openSQLiteWithRetry(dsn string, maxOpenConns int, policy sqliteLockRetryPolicy) (*sql.DB, error) {
	if policy.maxAttempts < 1 {
		policy.maxAttempts = 1
	}
	deadline := time.Now().Add(policy.deadline)
	for attempt := 0; attempt < policy.maxAttempts; attempt++ {
		database, err := sql.Open(sqliteDriver, dsn)
		if err != nil {
			if database != nil {
				_ = database.Close()
			}
			return nil, err
		}
		if maxOpenConns > 0 {
			database.SetMaxOpenConns(maxOpenConns)
		}
		err = database.Ping()
		if err == nil {
			return database, nil
		}
		_ = database.Close()
		if !isSQLiteLockError(err) || !waitSQLiteLockRetry(policy, deadline, attempt) {
			return nil, err
		}
	}
	return nil, errors.New("db: sqlite startup retry exhausted without an error")
}

// retrySQLiteLock retries only lock errors and returns the original error when
// no retry is permitted. The deadline prevents another retry from starting
// after the startup budget; maxAttempts bounds driver busy waits that do not
// observe a Go context.
func retrySQLiteLock(policy sqliteLockRetryPolicy, attempt func() error) error {
	if policy.maxAttempts < 1 {
		policy.maxAttempts = 1
	}
	deadline := time.Now().Add(policy.deadline)
	for i := 0; i < policy.maxAttempts; i++ {
		err := attempt()
		if err == nil || !isSQLiteLockError(err) {
			return err
		}
		if !waitSQLiteLockRetry(policy, deadline, i) {
			return err
		}
	}
	return errors.New("db: sqlite lock retry exhausted without an error")
}

func waitSQLiteLockRetry(policy sqliteLockRetryPolicy, deadline time.Time, attempt int) bool {
	if attempt+1 == policy.maxAttempts || time.Now().After(deadline) {
		return false
	}
	delay := policy.retryDelay(attempt)
	if delay <= 0 {
		return true
	}
	if time.Now().Add(delay).After(deadline) {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	return true
}

// OpenWithoutMigration wraps an existing *sql.DB without changing its
// schema or connection settings. The returned DB owns the underlying pool;
// callers remain responsible for establishing the schema and pragmas before
// wrapping it.
func OpenWithoutMigration(sqlDB *sql.DB) (*DB, error) {
	if sqlDB == nil {
		return nil, errors.New("db: open without migration: sqlDB is nil")
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("db: open without migration: ping: %w", err)
	}
	return &DB{db: sqlDB}, nil
}

// OpenMemory returns a fresh, migrated in-memory database with foreign-key
// enforcement enabled. The caller must close it.
func OpenMemory() (*DB, error) {
	dsn := "file::memory:?_pragma=foreign_keys(1)"

	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open memory: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: open memory: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: migrate memory: %w", err)
	}

	return &DB{db: db}, nil
}

// DB is a handle to the ktx database. The underlying *sql.DB is unexported
// because callers reach the database through methods, not by reaching into
// the handle.
type DB struct {
	db *sql.DB
}

// Close closes the database handle, releasing the underlying SQLite
// connection pool. It is safe to call on a nil *DB.
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// DB returns the underlying *sql.DB handle. Production callers should
// prefer the typed methods on *DB (GetToolCall, PersistToolCall,
// InsertCompressed, …); this escape hatch exists for failure-mode
// tests that need to simulate out-of-band corruption (e.g. deleting a
// source tool_call row with FKs temporarily disabled to verify the
// compressed-row not-found path — a tool_calls INSERT-only contract violation simulation). The
// returned handle is the live pool; callers must not Close it (use
// Close on the enclosing *DB instead).
func (d *DB) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}
