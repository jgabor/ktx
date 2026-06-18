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
// the two-table M1 schema.
//
// Column definitions match the task contract exactly: NOT NULL and DEFAULT
// appear only where Decision 7/9 specify them (session_id, tool_call_count,
// event_type). The tool_calls.session_id REFERENCES sessions(id) clause is
// enforced because Open enables foreign_keys per connection.
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
		event_type   TEXT DEFAULT 'tool_call'
	)`,
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
// every open, so re-opening an existing database is a no-op.
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

// migrate runs the schema statements in a transaction so the database is
// never left with a partial schema.
func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, stmt := range schemaStatements {
		if _, err := tx.Exec(stmt); err != nil {
			tx.Rollback()
			return err
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
