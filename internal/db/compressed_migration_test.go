// Schema migration tests for the `compressed` table.
//
// Covers the migration acceptance:
//   - fresh-DB has `compressed` with all columns
//   - older DB gains `compressed` on next open
//   - re-open is a no-op (idempotency)
//   - partial-column forward-fix (hand-built fixture via
//     OpenWithoutMigration with only `id` + `source_id`)
//   - FK enforcement: inserting a compressed row with a valid
//     source_id succeeds; inserting one referencing a missing
//     tool_call fails (foreign_keys=1 enforced by Open's DSN)
//
// The tests use the existing in-memory seams (OpenMemory, OpenWithoutMigration,
// raw *sql.DB) so no filesystem fixture is required. Table-driven subtests
// follow the project's convention (see db_test.go, persist_test.go).
package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// expectedCompressedColumns mirrors compressedColumns in schema.go (by name)
// so the schema-vs-intent check is one source of truth away from the CREATE
// TABLE. Order matches the CREATE TABLE column order; doctor and the
// partial-forward-fix path both rely on the order being deterministic.
var expectedCompressedColumns = []string{
	"id", "source_id", "output", "content_hash", "pipeline", "timestamp", "timestamp_unix_nano", "producer",
}

func TestSchema15To16BackfillsCompressedTimestampsWithoutChangingEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema15.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := seedSessionAndCalls(t, d, []callFixture{{id: "migrationtimesrc", output: "original"}})[0]
	seedCompressedWithID(t, d, "aaaaaaaaaaaaaaaa", sourceID, "a", "2026-07-21T12:00:00.1Z")
	seedCompressedWithID(t, d, "bbbbbbbbbbbbbbbb", sourceID, "b", "2026-07-21T12:00:00.100Z")
	seedCompressedWithID(t, d, "cccccccccccccccc", sourceID, "c", "malformed")
	if _, err := d.DB().Exec(`ALTER TABLE compressed DROP COLUMN timestamp_unix_nano; PRAGMA user_version = 15`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rows, err := d.DB().Query(`SELECT timestamp, timestamp_unix_nano FROM compressed ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var timestamps []string
	var sortable []sql.NullInt64
	for rows.Next() {
		var text string
		var value sql.NullInt64
		if err := rows.Scan(&text, &value); err != nil {
			t.Fatal(err)
		}
		timestamps = append(timestamps, text)
		sortable = append(sortable, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(sortable) != 3 || !sortable[0].Valid || !sortable[1].Valid || sortable[0].Int64 != sortable[1].Int64 || sortable[2].Valid {
		t.Fatalf("sortable timestamps = %#v, want equal valid values then NULL", sortable)
	}
	wantText := []string{"2026-07-21T12:00:00.1Z", "2026-07-21T12:00:00.100Z", "malformed"}
	for i := range wantText {
		if timestamps[i] != wantText[i] {
			t.Fatalf("timestamp[%d] = %q, want %q", i, timestamps[i], wantText[i])
		}
	}
}

// TestCompressedMigration_FreshDB covers the "fresh database created by
// the current binary" leg: OpenMemory runs migrate() which includes the
// `CREATE TABLE IF NOT EXISTS compressed` statement, so the table and
// all columns must be present from the start (no ALTER needed).
func TestCompressedMigration_FreshDB(t *testing.T) {
	t.Run("compressed table exists with all columns", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		have, err := tableColumns(d.db, "compressed")
		if err != nil {
			t.Fatalf("tableColumns(compressed): %v", err)
		}
		for _, want := range expectedCompressedColumns {
			if _, ok := have[want]; !ok {
				t.Errorf("compressed column %q missing (have %v)", want, sortedKeys(have))
			}
		}
		if got := len(have); got != len(expectedCompressedColumns) {
			t.Errorf("compressed has %d columns, want %d", got, len(expectedCompressedColumns))
		}
	})

	t.Run("compressed_source_id_idx index exists", func(t *testing.T) {
		// The index is the `ktx decompress` lookup accelerator. A
		// missing index degrades but does not break the reversal
		// primitive (GetSourceOutput still scans); we assert
		// presence so a regression of the schemaStatements slice
		// surfaces immediately.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		var name string
		err = d.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name='compressed_source_id_idx'`,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Fatal("compressed_source_id_idx index missing after OpenMemory")
		}
		if err != nil {
			t.Fatalf("sqlite_master lookup for index: %v", err)
		}
		if name != "compressed_source_id_idx" {
			t.Errorf("index name = %q, want compressed_source_id_idx", name)
		}
	})
}

// TestCompressedMigration_PreM4Database covers the "older database
// opened by the current binary gains compressed" leg. We build an
// unmigrated fixture exactly as the older schema would have left it
// (sessions, tool_calls with all cost-publication columns, bench_runs),
// then run migrate()
// against it. The compressed table must appear with all columns
// and existing tables/rows must be untouched.
func TestCompressedMigration_PreM4Database(t *testing.T) {
	t.Run("unmigrated DB gains compressed table with all columns", func(t *testing.T) {
		raw, err := sql.Open(sqliteDriver, "file::memory:?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw sqlite: %v", err)
		}
		t.Cleanup(func() { _ = raw.Close() })

		// Build the older schema exactly as the prior version would have:
		// sessions, tool_calls (with cost-publication columns inlined), bench_runs.
		// No compressed table — that is what migrate() must add.
		for _, stmt := range []string{
			`CREATE TABLE sessions (
				id              TEXT PRIMARY KEY,
				agent           TEXT,
				started_at      TEXT,
				ended_at        TEXT,
				last_seen_at    TEXT,
				tool_call_count INTEGER DEFAULT 0
			)`,
			`CREATE TABLE tool_calls (
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
			`CREATE TABLE bench_runs (
				id                  TEXT PRIMARY KEY,
				agent               TEXT,
				model               TEXT,
				task                TEXT,
				score               REAL,
				tokens_in           INTEGER,
				tokens_out          INTEGER,
				cost                REAL,
				methodology_version TEXT,
				timestamp           INTEGER
			)`,
		} {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("create older-schema table: %v", err)
			}
		}

		// Seed one row in each existing table so the
		// "existing records are intact" assertion has real data
		// to check against.
		sessionID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO sessions (id, agent, started_at, last_seen_at) VALUES (?, ?, ?, ?)`,
			sessionID, "test", "2026-06-24T00:00:00Z", "2026-06-24T00:00:00Z",
		); err != nil {
			t.Fatalf("seed sessions: %v", err)
		}
		tcID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO tool_calls (id, session_id, tool, input, output, content_hash, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			tcID, sessionID, "bash", "echo hi", "hi\n", "deadbeef", "2026-06-24T00:00:01Z",
		); err != nil {
			t.Fatalf("seed tool_calls: %v", err)
		}
		benchID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO bench_runs (id, agent, model, task, score, methodology_version, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			benchID, "opencode", "claude-sonnet-4-20250514", "task-1", 1.0, "v1", 1719187200,
		); err != nil {
			t.Fatalf("seed bench_runs: %v", err)
		}

		// Run migrate() against the unmigrated fixture. This is the
		// "current binary opens an older database" path.
		if err := migrate(raw); err != nil {
			t.Fatalf("migrate unmigrated DB: %v", err)
		}

		// compressed must now exist with all columns.
		have, err := tableColumns(raw, "compressed")
		if err != nil {
			t.Fatalf("tableColumns(compressed) after migrate: %v", err)
		}
		for _, want := range expectedCompressedColumns {
			if _, ok := have[want]; !ok {
				t.Errorf("compressed column %q missing after migrate (have %v)", want, sortedKeys(have))
			}
		}

		// Existing rows must be intact.
		var gotTool string
		if err := raw.QueryRow(`SELECT tool FROM tool_calls WHERE id = ?`, tcID).Scan(&gotTool); err != nil {
			t.Fatalf("tool_calls row vanished after migrate: %v", err)
		}
		if gotTool != "bash" {
			t.Errorf("tool_calls.tool after migrate = %q, want bash", gotTool)
		}
		var gotBenchScore float64
		if err := raw.QueryRow(`SELECT score FROM bench_runs WHERE id = ?`, benchID).Scan(&gotBenchScore); err != nil {
			t.Fatalf("bench_runs row vanished after migrate: %v", err)
		}
		if gotBenchScore != 1.0 {
			t.Errorf("bench_runs.score after migrate = %g, want 1.0", gotBenchScore)
		}
	})

	t.Run("re-open (re-migrate) is a no-op", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		before, err := tableColumns(d.db, "compressed")
		if err != nil {
			t.Fatalf("tableColumns before re-migrate: %v", err)
		}
		// A second migrate must not duplicate columns or error.
		if err := migrate(d.db); err != nil {
			t.Fatalf("second migrate: %v", err)
		}
		after, err := tableColumns(d.db, "compressed")
		if err != nil {
			t.Fatalf("tableColumns after re-migrate: %v", err)
		}
		if len(before) != len(after) {
			t.Fatalf("re-migrate altered compressed column count: %d -> %d", len(before), len(after))
		}
	})
}

// TestCompressedMigration_PartialForwardFix covers the partial-column
// forward-fix path: a `compressed` table exists with only `id` and
// `source_id` (the pathological "buggy prior migration created a
// partial table" case). migrate() must add the other 4 columns on the
// next open via ALTER TABLE ADD COLUMN, matching the
// partial-forward-fix contract.
func TestCompressedMigration_PartialForwardFix(t *testing.T) {
	raw, err := sql.Open(sqliteDriver, "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	// Build only sessions + tool_calls (compressed.source_id FK needs
	// tool_calls to exist) + a partial compressed with id + source_id.
	for _, stmt := range []string{
		`CREATE TABLE sessions (
			id              TEXT PRIMARY KEY,
			agent           TEXT,
			started_at      TEXT,
			ended_at        TEXT,
			last_seen_at    TEXT,
			tool_call_count INTEGER DEFAULT 0
		)`,
		`CREATE TABLE tool_calls (
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
		`CREATE TABLE bench_runs (
			id                  TEXT PRIMARY KEY,
			agent               TEXT,
			model               TEXT,
			task                TEXT,
			score               REAL,
			tokens_in           INTEGER,
			tokens_out          INTEGER,
			cost                REAL,
			methodology_version TEXT,
			timestamp           INTEGER
		)`,
		// Partial compressed: only id + source_id. The other 4
		// columns (output, content_hash, pipeline, timestamp) are
		// missing — migrate() must add them.
		`CREATE TABLE compressed (
			id        TEXT PRIMARY KEY,
			source_id TEXT NOT NULL REFERENCES tool_calls(id)
		)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("create partial-compressed fixture: %v", err)
		}
	}

	// Wrap via OpenWithoutMigration so we hold the caller's schema
	// verbatim until we explicitly call migrate().
	d, err := OpenWithoutMigration(raw)
	if err != nil {
		t.Fatalf("OpenWithoutMigration: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	if err := migrate(d.db); err != nil {
		t.Fatalf("migrate partial-compressed: %v", err)
	}

	have, err := tableColumns(d.db, "compressed")
	if err != nil {
		t.Fatalf("tableColumns after migrate: %v", err)
	}
	// All columns must now be present.
	for _, want := range expectedCompressedColumns {
		if _, ok := have[want]; !ok {
			t.Errorf("compressed column %q missing after forward-fix (have %v)", want, sortedKeys(have))
		}
	}
}

// TestCompressedMigration_ForeignKeyEnforcement covers the FK leg of
// the contract: with foreign_keys=1 (enforced by Open's DSN), a
// compressed row cannot point at a non-existent tool_call.
func TestCompressedMigration_ForeignKeyEnforcement(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Seed a real session + tool_call so the FK has a valid target.
	sessionID := mustNewID(t)
	if err := d.InsertSession(&Session{
		ID:         sessionID,
		Agent:      "test",
		StartedAt:  "2026-06-24T00:00:00Z",
		LastSeenAt: "2026-06-24T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	realToolCallID := mustNewID(t)
	if err := d.PersistToolCall(&ToolCall{
		ID:        realToolCallID,
		SessionID: sessionID,
		Tool:      "bash",
		Output:    "hi\n",
		Timestamp: "2026-06-24T00:00:01Z",
	}); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}

	t.Run("compressed row with valid source_id inserts", func(t *testing.T) {
		cid := mustNewID(t)
		_, err := d.db.Exec(
			`INSERT INTO compressed (id, source_id, output, content_hash, pipeline, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			cid, realToolCallID, "hi\n", "hash", "[]", "2026-06-24T00:00:02Z",
		)
		if err != nil {
			t.Fatalf("insert compressed with valid source_id: %v", err)
		}
	})

	t.Run("compressed row with unknown source_id is rejected (FK)", func(t *testing.T) {
		cid := mustNewID(t)
		_, err := d.db.Exec(
			`INSERT INTO compressed (id, source_id, output, content_hash, pipeline, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			cid, "zzzzzzzzzzzzzzzz", "out", "hash", "[]", "2026-06-24T00:00:03Z",
		)
		if err == nil {
			t.Fatal("insert compressed with unknown source_id succeeded; want FK rejection")
		}
		// We don't pin the exact SQLite FK error string; the
		// contract is "non-nil error". The modernc.org/sqlite
		// driver surfaces it as a sqlite-specific error variant.
		if !errors.Is(err, err) { // trivially true; documents intent
			t.Logf("FK rejection error: %v", err)
		}
	})
}
