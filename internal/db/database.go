// Package db owns database lifecycle, persistence, and schema evolution.
// the on-disk SQLite schema.
//
// The database is a single SQLite file with five tables
// (the core schema, the bench_runs table, the compressed table, and
// the recorded_streams table):
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
// ktx-run-captured stream can carry no bench parent). IDs are 16-char
// lowercase alpha nanoids.
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

	// Register the pure-Go SQLite driver used by database/sql.
	_ "modernc.org/sqlite"
)

// IDs use 16 lowercase letters, providing roughly 75 bits of entropy without
// ambiguous numeric characters.
const (
	idAlphabet = "abcdefghijklmnopqrstuvwxyz"
	idLength   = 16
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

	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
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
	database, err := sql.Open(sqliteDriver, uri)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		return nil, err
	}
	if _, err := database.Exec("PRAGMA query_only = ON"); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
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
