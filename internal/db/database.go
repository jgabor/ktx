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
	"os"
	"path/filepath"
	"regexp"

	// Register the pure-Go SQLite driver as "sqlite". No CGO, matching the
	// vision's "no new runtimes" principle. Open connects via database/sql
	// with this driver.
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
// The schema migration is idempotent: CREATE TABLE IF NOT EXISTS runs on
// every open, so re-opening an existing database is a no-op. The
// in-place cost-publication migration (ALTER TABLE ADD COLUMN for tokens_in/cost/etc.) is
// equally idempotent — the column-existence check via PRAGMA table_info
// makes a second run on a fully migrated database a no-op, and a
// partial-set database (e.g. only tokens_in added) is forward-fixed on
// the next open. No versioned migration framework is needed for the
// additive cost-publication column set (SQLite ALTER TABLE, no custom
// framework).
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
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"

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

// OpenWithoutMigration wraps an externally-managed *sql.DB in a *DB
// without applying the schema migration. The seam exists for two
// use cases:
//
//   - Test fixtures that need to construct a database with a specific
//     schema (e.g. an unmigrated fixture missing bench_runs, or a partial
//     cost-publication fixture missing one additive column). The dispatch layer's
//     doctor tests exercise this path: opening a raw *sql.DB with
//     core + cost-publication columns but no bench_runs table, wrapping it in *DB
//     without a migration, and asserting the doctor reports the
//     bench_runs check as a warning. Running Open would create
//     bench_runs via CREATE TABLE IF NOT EXISTS and defeat the
//     fixture; OpenWithoutMigration preserves the caller's schema
//     verbatim.
//
//   - Future production code paths that need to read or write against
//     a database whose schema is already established by an out-of-band
//     process (a sidecar migration tool, a separately-managed
//     connection pool, a benchmark harness pre-creating the
//     schema). The seam gives those callers a clean entry point
//     without exposing the *DB struct's unexported field via
//     reflection or unsafe.
//
// The caller owns three responsibilities that Open handles
// implicitly:
//
//  1. The *sql.DB connection lifecycle: OpenWithoutMigration does
//     not call sql.Open or Close. The returned *DB's Close closes
//     the underlying pool — the caller should not also close the
//     sql.DB handle.
//  2. The foreign_keys pragma. Open applies _pragma=foreign_keys(1)
//     via the DSN so the tool_calls -> sessions FK is enforced on
//     every pooled connection. OpenWithoutMigration does not
//     override the caller's DSN; if the caller's DSN did not set
//     the pragma, the FK is not enforced. Tests that depend on FK
//     enforcement should use `file::memory:?_pragma=foreign_keys(1)`
//     or run `PRAGMA foreign_keys = ON` on the connection before
//     wrapping.
//  3. Schema validity. The returned *DB does not check that the
//     schema satisfies the core + cost-publication + bench_runs contract; methods that read
//     columns (PersistToolCall, GetToolCall, HasBenchRunsTable,
//     etc.) surface query errors against an incompatible schema as
//     ordinary errors.
//
// OpenWithoutMigration does not touch the filesystem and does not
// require a path; the schema lives entirely in the caller's
// *sql.DB. The function is exported (production-reusable) rather
// than build-tag-scoped because the seam is small, the contract is
// documented, and future deepening work (D-side reorganisations,
// the db file re-cut) can adopt it without an internal fork.
func OpenWithoutMigration(sqlDB *sql.DB) (*DB, error) {
	if sqlDB == nil {
		return nil, errors.New("db: open without migration: sqlDB is nil")
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("db: open without migration: ping: %w", err)
	}
	return &DB{db: sqlDB}, nil
}

// OpenMemory returns a *DB backed by an in-memory SQLite database with the
// full schema migrated and foreign_keys=1 enforced. The seam exists for
// tests (and any future in-process tooling) that need a real ktx DB
// without touching the filesystem: no path resolution, no MkdirAll, no
// XDG_DATA_HOME probing. The returned *DB has the same surface as Open
// (Close, PersistToolCall, GetRecord, etc.) so tests can exercise the
// full persistence path with bytes.Buffer / httptest isolation rather
// than a t.TempDir() SQLite file.
//
// Each call returns a fresh database: the modernc.org/sqlite driver
// treats `file::memory:?cache=shared` as connection-scoped, so two
// OpenMemory calls do not share state. The DSN's _pragma=foreign_keys(1)
// applies per connection, so foreign_keys=1 holds on every pooled
// connection — the same FK guarantee Open provides on file-backed DBs.
//
// Production code paths do not use this seam; Open remains the only
// way ktx opens the user's database on disk.
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
