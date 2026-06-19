// Package store owns the ktx database: path resolution, ID generation, and
// the on-disk SQLite schema.
//
// The database is a single SQLite file (Decision 6) with two tables
// (Decision 7): sessions (aggregate, INSERT on open + UPDATE roll-up) and
// tool_calls (append-only, INSERT only). IDs are 16-char lowercase alpha
// nanoids (Decision 3). Content hashes are SHA-256 of the output field,
// stored as a separate column for verification without being the primary key
// (Decision 3).
//
// Dependencies are pinned here so the choice is explicit:
//
//   - github.com/matoous/go-nanoid/v2 — 16-char lowercase alpha IDs (D3)
//   - modernc.org/sqlite              — pure-Go SQLite driver, no CGO (D6)
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	gonanoid "github.com/matoous/go-nanoid/v2"

	// Register the pure-Go SQLite driver as "sqlite". No CGO, matching the
	// vision's "no new runtimes" principle. Open connects via database/sql
	// with this driver.
	_ "modernc.org/sqlite"
)

// ID alphabet and length per Decision 3: 16-char lowercase alpha, ~75 bits
// entropy, no ambiguous characters (0/O, 1/l).
const (
	idAlphabet = "abcdefghijklmnopqrstuvwxyz"
	idLength   = 16
)

// sqliteDriver is the registered name of the modernc.org/sqlite driver.
const sqliteDriver = "sqlite"

// schemaStatements creates the ktx tables per Decision 7. The statements are
// idempotent (CREATE TABLE IF NOT EXISTS), so Open runs them on every call
// without a migration framework; a versioned migration layer is YAGNI for
// the two-table M1/M2 schema.
//
// Column definitions match the task contract exactly: NOT NULL and DEFAULT
// appear only where Decision 7/9 specify them (session_id, tool_call_count,
// event_type). The tool_calls.session_id REFERENCES sessions(id) clause is
// enforced because Open enables foreign_keys per connection.
//
// The M2 cost-publication columns (tokens_in, tokens_out, cost, latency_ms,
// model, provider) are part of the CREATE TABLE so fresh databases include
// them from the start; pre-M2 databases pick them up via the in-place
// ALTER TABLE step in migrate(). The M2 columns are nullable and have no
// DEFAULT — NULL is the proper "field absent" marker so cost reconstruction
// can distinguish "caller did not supply" from "caller supplied zero".
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS sessions (
		id              TEXT PRIMARY KEY,
		agent           TEXT,
		started_at      TEXT,
		ended_at        TEXT,
		last_seen_at    TEXT,
		tool_call_count INTEGER DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS tool_calls (
		id           TEXT PRIMARY KEY,
		session_id   TEXT NOT NULL REFERENCES sessions(id),
		tool         TEXT,
		input        TEXT,
		output       TEXT,
		content_hash TEXT,
		timestamp    TEXT,
		event_type   TEXT DEFAULT 'tool_call',
		tokens_in    INTEGER,
		tokens_out   INTEGER,
		cost         REAL,
		latency_ms   INTEGER,
		model        TEXT,
		provider     TEXT
	)`,
}

// m2Column enumerates a single M2 cost-publication column on tool_calls.
// The list is the single source of truth for the migration: it drives both
// the columns added to the CREATE TABLE statement (above) and the
// ALTER TABLE ADD COLUMN step in migrate() for pre-M2 databases.
//
// Order matters only for deterministic output (doctor reports missing
// columns in the order they appear here). Types match the D9 flat-JSON
// payload so callers can persist numbers and strings without a SQLite
// type-coercion surprise.
type m2Column struct {
	Name string
	Type string
}

// m2Columns are the M2 cost-publication columns on tool_calls. Each entry
// pairs the column name with its SQL type for the ALTER TABLE ADD COLUMN
// statement. Idempotency is handled by checking PRAGMA table_info before
// altering — SQLite has no IF NOT EXISTS for ADD COLUMN, so a column
// existence check is the only pure-Go mechanism. PRAGMA table_info runs
// inside the migration transaction without issue; the query is read-only
// and the connection's schema view is consistent within a transaction.
var m2Columns = []m2Column{
	{Name: "tokens_in", Type: "INTEGER"},
	{Name: "tokens_out", Type: "INTEGER"},
	{Name: "cost", Type: "REAL"},
	{Name: "latency_ms", Type: "INTEGER"},
	{Name: "model", Type: "TEXT"},
	{Name: "provider", Type: "TEXT"},
}

// DefaultPath resolves the default database path per Decision 8:
// $XDG_DATA_HOME/ktx/ktx.db. When XDG_DATA_HOME is unset, it falls back to
// ~/.local/share/ktx/ktx.db (XDG base directory specification).
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

// ResolvePath applies the database path precedence from Decision 8:
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

// NewID returns a fresh 16-character lowercase alphabetic nanoid (D3).
// Collision-safe for billions of records; no ambiguous characters.
func NewID() (string, error) {
	return gonanoid.Generate(idAlphabet, idLength)
}

// ContentHash returns the SHA-256 hex digest of output (Decision 3). The
// content hash is stored as a separate field on each tool_call row for
// verification without being the primary key.
func ContentHash(output string) string {
	sum := sha256.Sum256([]byte(output))
	return hex.EncodeToString(sum[:])
}

// Open opens (or creates) the ktx database at path and ensures the schema is
// migrated. The parent directory is created if missing (Decision 6). Foreign
// keys are enforced so the tool_calls.session_id REFERENCES clause is
// meaningful, not decorative (Decision 7).
//
// The schema migration is idempotent: CREATE TABLE IF NOT EXISTS runs on
// every open, so re-opening an existing database is a no-op. The M2
// in-place migration (ALTER TABLE ADD COLUMN for tokens_in/cost/etc.) is
// equally idempotent — the column-existence check via PRAGMA table_info
// makes a second run on a fully migrated database a no-op, and a
// partial-set database (e.g. only tokens_in added) is forward-fixed on
// the next open. No versioned migration framework is needed for the
// additive M2 column set (per the plan: SQLite ALTER TABLE, no custom
// framework).
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("store: open: path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: open: create directory for %q: %w", path, err)
	}

	// modernc.org/sqlite reads _pragma query params and applies them per
	// connection. foreign_keys(1) enforces the tool_calls -> sessions FK on
	// every pooled connection, not just the first.
	dsn := "file:" + path + "?_pragma=foreign_keys(1)"

	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate %q: %w", path, err)
	}

	return &DB{db: db}, nil
}

// migrate runs the schema statements and the M2 in-place column migration
// inside a single transaction so the database is never left with a partial
// schema. Fresh databases get the M2 columns from the CREATE TABLE itself;
// pre-M2 databases pick them up via ALTER TABLE ADD COLUMN. The same
// transaction also forward-fixes a partial M2 set: any M2 column absent
// from tool_calls is added; any M2 column already present is skipped. A
// re-open of a fully migrated database is therefore a pure no-op (all
// PRAGMA checks find the column, no ALTER runs).
//
// The column-existence check uses PRAGMA table_info inside the migration
// transaction. PRAGMA table_info is a read query that returns one row per
// column on the connected schema view; running it inside a transaction is
// safe because the transaction has not yet altered the schema and the
// connection's schema cache is consistent within the transaction.
func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful Commit

	for _, stmt := range schemaStatements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: create schema: %w", err)
		}
	}

	have, err := tableColumns(tx, "tool_calls")
	if err != nil {
		return fmt.Errorf("store: inspect tool_calls: %w", err)
	}
	for _, col := range m2Columns {
		if _, ok := have[col.Name]; ok {
			continue
		}
		stmt := "ALTER TABLE tool_calls ADD COLUMN " + col.Name + " " + col.Type
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("store: add m2 column %q: %w", col.Name, err)
		}
	}

	return tx.Commit()
}

// DB is a handle to the ktx database. The underlying *sql.DB is unexported
// because callers reach the database through methods, not by reaching into
// the handle. Persistence and query methods are added by Tasks 3-7.
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
