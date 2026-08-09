package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenConfiguresBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeout.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	assertBusyTimeout(t, database.db, 1000)

	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	assertBusyTimeout(t, readOnly, 1000)
}

func TestWriteCapablePoolsUseOneConnection(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		database, err := Open(filepath.Join(t.TempDir(), "ktx.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		assertProcessLocalPool(t, database)
	})

	t.Run("open existing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ktx.db")
		initialized, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := initialized.Close(); err != nil {
			t.Fatal(err)
		}
		database, err := OpenExisting(path)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		assertProcessLocalPool(t, database)
	})

	t.Run("memory", func(t *testing.T) {
		database, err := OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		assertProcessLocalPool(t, database)
	})

	t.Run("without migration", func(t *testing.T) {
		raw, err := sql.Open(sqliteDriver, "file::memory:?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		database, err := OpenWithoutMigration(raw)
		if err != nil {
			_ = raw.Close()
			t.Fatal(err)
		}
		defer database.Close()
		assertProcessLocalPool(t, database)
	})
}

func assertProcessLocalPool(t *testing.T, database *DB) {
	t.Helper()
	if got := database.DB().Stats().MaxOpenConnections; got != processLocalMaxOpenConns {
		t.Fatalf("max open connections = %d, want %d", got, processLocalMaxOpenConns)
	}
}

func assertBusyTimeout(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("busy_timeout = %d, want %d", got, want)
	}
}

func TestOpenReadOnly(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"report ?#1.db", "100%&ready=.db"} {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join(dir, "fixture.db")
			fixture, err := Open(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			if err := os.Rename(source, path); err != nil {
				t.Fatal(err)
			}

			readOnly, err := OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer readOnly.Close()
			if _, err := readOnly.Exec("CREATE TABLE forbidden (id INTEGER)"); err == nil {
				t.Fatal("write to read-only database succeeded")
			}
		})
	}

	t.Run("nonexistent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing ?#%.db")
		if database, err := OpenReadOnly(path); err == nil {
			database.Close()
			t.Fatal("opening nonexistent database succeeded")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("nonexistent database was created: %v", err)
		}
	})
}

func TestOpenExistingDoesNotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "ktx.db")
	if database, err := OpenExisting(path); err == nil {
		database.Close()
		t.Fatal("OpenExisting created a missing database")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database was created: %v", err)
	}
}

func TestOpenQuery(t *testing.T) {
	t.Run("missing database is an ephemeral empty current schema", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "ktx.db")
		database, err := OpenQuery(path)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		sessions, err := database.ListSessions()
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 0 {
			t.Fatalf("sessions = %d, want 0", len(sessions))
		}
		if _, err := database.db.Exec("CREATE TABLE forbidden (id INTEGER)"); err == nil {
			t.Fatal("write through missing-database query handle succeeded")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("query created missing database: %v", err)
		}
	})

	t.Run("current database is immutable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "current.db")
		created, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := created.Close(); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		database, err := OpenQuery(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.db.Exec("CREATE TABLE forbidden (id INTEGER)"); err == nil {
			t.Fatal("write through current-database query handle succeeded")
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before) {
			t.Fatal("read-only query changed database bytes")
		}
	})

	for _, test := range []struct {
		name    string
		version int
		check   func(error) bool
	}{
		{name: "older schema requires migration", version: MaxSupportedSchema() - 1, check: func(err error) bool {
			var target *SchemaTooOldError
			return errors.As(err, &target) && target.Actual == MaxSupportedSchema()-1 && target.Required == MaxSupportedSchema()
		}},
		{name: "newer schema requires newer binary", version: MaxSupportedSchema() + 1, check: func(err error) bool {
			var target *SchemaTooNewError
			return errors.As(err, &target) && target.Actual == MaxSupportedSchema()+1 && target.Maximum == MaxSupportedSchema()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "versioned.db")
			raw, err := sql.Open(sqliteDriver, "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec("PRAGMA user_version = " + fmt.Sprint(test.version)); err != nil {
				raw.Close()
				t.Fatal(err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			database, err := OpenQuery(path)
			if database != nil {
				database.Close()
			}
			if err == nil || !test.check(err) {
				t.Fatalf("OpenQuery error = %v", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("rejected query changed database bytes")
			}
		})
	}
}

// TestOpenMemory_BackendAndForeignKeys covers the first three legs of the
// OpenMemory acceptance contract: the seam returns a *DB backed by an
// in-memory SQLite, foreign_keys is enforced per connection (verified by
// querying PRAGMA foreign_keys directly through the unexported *sql.DB
// handle), and the full schema is migrated (sessions + tool_calls +
// bench_runs tables present with their PRIMARY KEY columns). Each leg
// is asserted in its own t.Run subtest so a failure pinpoints which
// guarantee regressed.
func TestOpenMemory_BackendAndForeignKeys(t *testing.T) {
	t.Run("returns *DB without file path", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		if d == nil {
			t.Fatal("OpenMemory returned nil *DB")
		}
		t.Cleanup(func() { d.Close() })
	})

	t.Run("foreign_keys=1 is enforced", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// The seam must enable foreign_keys per connection, not just on
		// the first pooled connection. Querying PRAGMA foreign_keys on
		// the underlying *sql.DB exercises whichever connection
		// database/sql picks from the pool; a non-1 value means the
		// _pragma=foreign_keys(1) DSN parameter was dropped.
		var got int
		if err := d.db.QueryRow("PRAGMA foreign_keys").Scan(&got); err != nil {
			t.Fatalf("PRAGMA foreign_keys: %v", err)
		}
		if got != 1 {
			t.Fatalf("foreign_keys=%d, want 1", got)
		}
	})

	t.Run("schema migrated: sessions + tool_calls + bench_runs", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Verify each table exists and has at least its PRIMARY KEY
		// column. We do not assert the full column list here — that
		// is covered by TestOpenMemory_CostColumns below. Three tables
		// with their PKs proves the schemaStatements ran.
		for _, table := range []string{"sessions", "tool_calls", "bench_runs"} {
			row := d.db.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='table' AND name = ?",
				table,
			)
			var name string
			if err := row.Scan(&name); err != nil {
				if err == sql.ErrNoRows {
					t.Fatalf("table %q missing after OpenMemory migrate", table)
				}
				t.Fatalf("sqlite_master lookup for %q: %v", table, err)
			}
		}

		// PRIMARY KEY sanity: each table must have at least one
		// PRIMARY KEY column. PRAGMA table_info reports pk>0 for PK
		// columns; 0 for non-PK columns. A MAX(pk) query against the
		// PRAGMA result confirms at least one column carries pk>=1.
		// Table names are hard-coded above so the inline form is
		// safe (no untrusted input).
		for _, table := range []string{"sessions", "tool_calls", "bench_runs"} {
			rows, err := d.db.Query("PRAGMA table_info(" + table + ")")
			if err != nil {
				t.Fatalf("PRAGMA table_info(%q): %v", table, err)
			}
			var maxPK int
			for rows.Next() {
				var cid int
				var name, ctype string
				var notnull, pk int
				var dfltValue any
				if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
					rows.Close()
					t.Fatalf("scan table_info(%q): %v", table, err)
				}
				if pk > maxPK {
					maxPK = pk
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate table_info(%q): %v", table, err)
			}
			if maxPK < 1 {
				t.Fatalf("table %q has no PRIMARY KEY column", table)
			}
		}
	})

	t.Run("migration is idempotent on re-open", func(t *testing.T) {
		// OpenMemory creates a fresh database per call (the
		// modernc.org/sqlite driver treats file::memory:?cache=shared
		// as connection-scoped per call), so 're-open' here means
		// calling migrate a second time on the same *DB. A second
		// migrate must be a pure no-op — no errors, no extra rows
		// from CREATE TABLE IF NOT EXISTS, no duplicate columns from
		// the ALTER TABLE loop.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Call migrate via the unexported path the production code
		// uses. If migrate were not exported, the test could not
		// exercise idempotency directly; it is unexported but
		// accessible inside the package via a re-call from a helper
		// or via direct invocation here. Re-invoking OpenMemory on
		// the same *DB is not supported, so we invoke migrate again
		// through the package's exported machinery — there is none,
		// so we assert via the schema-mutation invariants instead.
		before := tableRowCount(t, d.db, "sessions")
		if err := migrate(d.db); err != nil {
			t.Fatalf("second migrate: %v", err)
		}
		after := tableRowCount(t, d.db, "sessions")
		if before != after {
			t.Fatalf("idempotent migrate altered row count: %d -> %d", before, after)
		}
	})
}

// TestOpenMemory_CostColumns covers the cost-publication + bench_runs column migration:
// OpenMemory must produce a database with every additive column from the
// costColumns and benchRunsColumns lists. The migrate() loop is the only
// place those columns are added; if migrate is skipped or the lists
// diverge from the CREATE TABLE, the round-trips silently lose data.
func TestOpenMemory_CostColumns(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	for _, col := range costColumns {
		var present int
		if err := d.db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('tool_calls') WHERE name = ?",
			col.Name,
		).Scan(&present); err != nil {
			t.Fatalf("pragma_table_info tool_calls.%s: %v", col.Name, err)
		}
		if present != 1 {
			t.Fatalf("tool_calls column %q missing after OpenMemory", col.Name)
		}
	}

	for _, col := range benchRunsColumns {
		var present int
		if err := d.db.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('bench_runs') WHERE name = ?",
			col.Name,
		).Scan(&present); err != nil {
			t.Fatalf("pragma_table_info bench_runs.%s: %v", col.Name, err)
		}
		if present != 1 {
			t.Fatalf("bench_runs column %q missing after OpenMemory", col.Name)
		}
	}
}

// tableRowCount returns the row count of table via db. Used by the
// idempotency subtest to assert that a second migrate does not create
// duplicate CREATE TABLE artefacts (table_rowid would shift, but a
// CREATE TABLE IF NOT EXISTS is a no-op on existing tables — so the
// count stays the same).
func tableRowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestOpenWithoutMigration_PreM3Fixture covers the contract: the seam
// returns a *DB over an externally-managed *sql.DB without applying the
// migration. The test builds an older schema (core + cost-publication
// columns, no bench_runs table) directly, wraps it in *DB via
// OpenWithoutMigration, inserts a tool_call, and asserts
// HasBenchRunsTable reports false — proving the migration did NOT
// create bench_runs.
//
// The seam is also exercised by the dispatch layer's doctor tests
// (see internal/cli/dispatch_namespace_test.go), where the
// unmigrated fixture drives the doctor's degraded status. This unit
// test pins down the seam's contract at the db package boundary.
func TestOpenWithoutMigration_PreM3Fixture(t *testing.T) {
	t.Run("wraps older schema and leaves bench_runs absent", func(t *testing.T) {
		// Build the older schema directly: core sessions +
		// core + cost-publication tool_calls. Intentionally skip
		// bench_runs so the migration, if it ran, would create the
		// table and the HasBenchRunsTable() == false assertion would
		// fail.
		raw, err := sql.Open(sqliteDriver, "file::memory:?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw sqlite: %v", err)
		}
		t.Cleanup(func() { _ = raw.Close() })

		if _, err := raw.Exec(`CREATE TABLE sessions (
			id              TEXT PRIMARY KEY,
			agent           TEXT,
			started_at      TEXT,
			ended_at        TEXT,
			last_seen_at    TEXT,
			tool_call_count INTEGER DEFAULT 0
		)`); err != nil {
			t.Fatalf("create sessions: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE tool_calls (
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
			provider     TEXT,
			exit_code    INTEGER,
			is_error     INTEGER,
			runtime      TEXT
		)`); err != nil {
			t.Fatalf("create tool_calls: %v", err)
		}

		d, err := OpenWithoutMigration(raw)
		if err != nil {
			t.Fatalf("OpenWithoutMigration: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })

		// HasBenchRunsTable reports false on an unmigrated fixture.
		has, err := d.HasBenchRunsTable()
		if err != nil {
			t.Fatalf("HasBenchRunsTable: %v", err)
		}
		if has {
			t.Error("HasBenchRunsTable = true; want false (migration should not have run)")
		}

		// Insert a tool_call against the older schema. The seam
		// must accept writes against the caller's existing schema
		// without bootstrapping missing tables. The FK on
		// tool_calls.session_id requires the parent session row to
		// exist first.
		sessionID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO sessions (id, agent, started_at, last_seen_at) VALUES (?, ?, ?, ?)`,
			sessionID, "test", "2026-06-21T00:00:00Z", "2026-06-21T00:00:00Z",
		); err != nil {
			t.Fatalf("seed sessions row: %v", err)
		}
		tcID := mustNewID(t)
		if err := d.PersistToolCall(&ToolCall{
			ID: tcID, SessionID: sessionID, Tool: "Bash",
			Input: "in", Output: "out", Timestamp: "2026-06-21T00:00:01Z",
		}); err != nil {
			t.Fatalf("PersistToolCall against older schema: %v", err)
		}

		// The tool_call row round-trips; the seam operates on the
		// caller's schema without modification.
		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		if got.SessionID != sessionID {
			t.Errorf("GetToolCall session = %q, want %q", got.SessionID, sessionID)
		}
	})

	t.Run("nil sqlDB returns an error", func(t *testing.T) {
		// The seam's preconditions: a nil *sql.DB is a programming
		// error and must surface immediately rather than panic on
		// the first method call (when the receiver would dereference
		// a nil pool). A clear error at the seam saves a confusing
		// nil-pointer deref downstream.
		d, err := OpenWithoutMigration(nil)
		if err == nil {
			t.Fatal("OpenWithoutMigration(nil) returned nil error; want non-nil")
		}
		if d != nil {
			t.Errorf("OpenWithoutMigration(nil) returned non-nil *DB; want nil")
		}
	})

	t.Run("closed sqlDB fails on Ping", func(t *testing.T) {
		// The seam Pings the connection before constructing *DB so
		// the caller learns early that the connection is unusable.
		// A pre-closed pool surfaces a Ping error and prevents the
		// caller from receiving a *DB that would fail on the first
		// real query.
		raw, err := sql.Open(sqliteDriver, "file::memory:?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw sqlite: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Fatalf("close raw sqlite: %v", err)
		}

		d, err := OpenWithoutMigration(raw)
		if err == nil {
			t.Fatal("OpenWithoutMigration(closed) returned nil error; want non-nil")
		}
		if d != nil {
			t.Errorf("OpenWithoutMigration(closed) returned non-nil *DB; want nil")
		}
		if !errors.Is(err, err) || err == nil {
			// Surface the underlying error type — Ping returns a
			// sql.ErrConnDone-class error, but we don't pin the
			// exact sentinel; the contract is "non-nil error".
			t.Logf("OpenWithoutMigration(closed) error: %v", err)
		}
	})
}
