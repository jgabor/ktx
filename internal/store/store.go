// Package store owns the ktx database: path resolution, ID generation, and
// the on-disk SQLite schema.
//
// Task 1 implements only what the CLI scaffolding needs to locate the
// database and produce record IDs: DefaultPath, ResolvePath, and NewID.
// Open and the schema migration are stubbed and filled in by Task 2
// (sessions + tool_calls tables, per Decision 7).
//
// Dependencies are pinned here so the choice is explicit and survives
// `go mod tidy` before Task 2 wires up the schema:
//
//   - github.com/matoous/go-nanoid/v2 — 16-char lowercase alpha IDs (D3)
//   - modernc.org/sqlite              — pure-Go SQLite driver, no CGO (D6)
package store

import (
	"errors"
	"os"
	"path/filepath"

	gonanoid "github.com/matoous/go-nanoid/v2"

	// Register the pure-Go SQLite driver as "sqlite". No CGO, matching the
	// vision's "no new runtimes" principle. Task 2 opens the database via
	// database/sql with this driver.
	_ "modernc.org/sqlite"
)

// ID alphabet and length per Decision 3: 16-char lowercase alpha, ~75 bits
// entropy, no ambiguous characters (0/O, 1/l).
const (
	idAlphabet = "abcdefghijklmnopqrstuvwxyz"
	idLength   = 16
)

// ErrNotImplemented is returned by stubbed store operations until Task 2
// fills in the schema and persistence layer.
var ErrNotImplemented = errors.New("store: not implemented")

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

// Open opens the database at path and ensures the schema is migrated.
// Stubbed for Task 1; Task 2 implements the sessions + tool_calls schema.
func Open(path string) (*DB, error) {
	return nil, ErrNotImplemented
}

// DB is a handle to the ktx database. Task 2 populates its fields and
// methods (sessions, tool_calls, roll-up counters).
type DB struct {
	// placeholder; Task 2 fills this in.
}
