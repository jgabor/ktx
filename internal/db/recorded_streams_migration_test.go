// Schema migration tests for the `recorded_streams` table (D20 plan Task 1).
//
// Covers the D20 plan Task 1 acceptance:
//   - fresh-DB has `recorded_streams` with all 7 columns
//   - pre-D20 DB (M3+M4 era) gains `recorded_streams` on next open
//   - re-open is a no-op (idempotency)
//   - FK enforcement: inserting a recorded_streams row with a valid
//     session_id + bench_run_id succeeds; inserting one referencing a
//     missing session or bench_run fails (foreign_keys=1 enforced by
//     Open's DSN)
//
// No ALTER TABLE column-add loop test — D20's "no ALTER TABLE" clause
// is read literally per resolved judgment call 1. The CREATE TABLE IF
// NOT EXISTS alone handles the fresh-DB and pre-D20 self-heal paths.
//
// The tests use the existing in-memory seams (OpenMemory,
// OpenWithoutMigration, raw *sql.DB) so no filesystem fixture is
// required. Table-driven subtests follow the project's convention
// (see compressed_migration_test.go, db_test.go).
package db

import (
	"database/sql"
	"errors"
	"testing"
)

// expectedRecordedStreamsColumns mirrors the CREATE TABLE column order
// in open.go so the schema-vs-intent check is one source of truth away
// from the definition.
var expectedRecordedStreamsColumns = []string{
	"id", "session_id", "bench_run_id", "stream_text", "content_hash", "stream_format", "captured_at",
}

// TestRecordedStreamsMigration_FreshDB covers the "fresh database
// created by the D20 binary" leg: OpenMemory runs migrate() which
// includes the `CREATE TABLE IF NOT EXISTS recorded_streams` statement,
// so the table and all 7 columns must be present from the start (no
// ALTER needed).
func TestRecordedStreamsMigration_FreshDB(t *testing.T) {
	t.Run("recorded_streams table exists with all 7 columns", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		have, err := tableColumns(d.db, "recorded_streams")
		if err != nil {
			t.Fatalf("tableColumns(recorded_streams): %v", err)
		}
		for _, want := range expectedRecordedStreamsColumns {
			if _, ok := have[want]; !ok {
				t.Errorf("recorded_streams column %q missing (have %v)", want, sortedKeys(have))
			}
		}
		if got := len(have); got != len(expectedRecordedStreamsColumns) {
			t.Errorf("recorded_streams has %d columns, want %d", got, len(expectedRecordedStreamsColumns))
		}
	})

	t.Run("session_id index exists", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		var name string
		err = d.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name='recorded_streams_session_id_idx'`,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Fatal("recorded_streams_session_id_idx index missing after OpenMemory")
		}
		if err != nil {
			t.Fatalf("sqlite_master lookup for session_id index: %v", err)
		}
		if name != "recorded_streams_session_id_idx" {
			t.Errorf("index name = %q, want recorded_streams_session_id_idx", name)
		}
	})

	t.Run("bench_run_id index exists", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		var name string
		err = d.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name='recorded_streams_bench_run_id_idx'`,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Fatal("recorded_streams_bench_run_id_idx index missing after OpenMemory")
		}
		if err != nil {
			t.Fatalf("sqlite_master lookup for bench_run_id index: %v", err)
		}
		if name != "recorded_streams_bench_run_id_idx" {
			t.Errorf("index name = %q, want recorded_streams_bench_run_id_idx", name)
		}
	})
}

// TestRecordedStreamsMigration_PreD20Database covers the "M3+M4-era
// database opened by the D20 binary gains recorded_streams" leg. We
// build a pre-D20 fixture exactly as M1+M2+M3+M4 would have left it
// (sessions, tool_calls with all M2+D15 columns, bench_runs,
// compressed), then run migrate() against it. The recorded_streams
// table must appear with all 7 columns and existing tables/rows must
// be untouched.
func TestRecordedStreamsMigration_PreD20Database(t *testing.T) {
	t.Run("M3+M4-era DB gains recorded_streams table with all 7 columns", func(t *testing.T) {
		raw, err := sql.Open(sqliteDriver, "file::memory:?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw sqlite: %v", err)
		}
		t.Cleanup(func() { _ = raw.Close() })

		// Build the pre-D20 schema exactly as M1+M2+M3+M4 would have:
		// sessions, tool_calls (with M2 + D15 columns inlined),
		// bench_runs, compressed. No recorded_streams table — that
		// is what migrate() must add.
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
				provider     TEXT,
				exit_code    INTEGER,
				is_error     INTEGER
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
			`CREATE TABLE compressed (
				id           TEXT PRIMARY KEY,
				source_id    TEXT NOT NULL REFERENCES tool_calls(id),
				output       TEXT,
				content_hash TEXT,
				pipeline     TEXT,
				timestamp    TEXT
			)`,
		} {
			if _, err := raw.Exec(stmt); err != nil {
				t.Fatalf("create pre-D20 table: %v", err)
			}
		}

		// Seed one row in each existing table so the
		// "existing records are intact" assertion has real data
		// to check against.
		sessionID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO sessions (id, agent, started_at, last_seen_at) VALUES (?, ?, ?, ?)`,
			sessionID, "test", "2026-06-30T00:00:00Z", "2026-06-30T00:00:00Z",
		); err != nil {
			t.Fatalf("seed sessions: %v", err)
		}
		tcID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO tool_calls (id, session_id, tool, input, output, content_hash, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			tcID, sessionID, "bash", "echo hi", "hi\n", "deadbeef", "2026-06-30T00:00:01Z",
		); err != nil {
			t.Fatalf("seed tool_calls: %v", err)
		}
		benchID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO bench_runs (id, agent, model, task, score, methodology_version, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			benchID, "opencode", "glm-5.2-short-fast-flex", "addition_v1", 1.0, "v1", 1782834992,
		); err != nil {
			t.Fatalf("seed bench_runs: %v", err)
		}

		// Run migrate() against the pre-D20 fixture. This is the
		// "D20 binary opens an M3+M4-era database" path.
		if err := migrate(raw); err != nil {
			t.Fatalf("migrate pre-D20 DB: %v", err)
		}

		// recorded_streams must now exist with all 7 columns.
		have, err := tableColumns(raw, "recorded_streams")
		if err != nil {
			t.Fatalf("tableColumns(recorded_streams) after migrate: %v", err)
		}
		for _, want := range expectedRecordedStreamsColumns {
			if _, ok := have[want]; !ok {
				t.Errorf("recorded_streams column %q missing after migrate (have %v)", want, sortedKeys(have))
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
			t.Errorf("bench_runs.score after migrate = %v, want 1.0", gotBenchScore)
		}
	})
}

// TestRecordedStreamsMigration_IdempotentReopen covers the "re-open a
// fully migrated database" leg: migrate() on an already-migrated
// OpenMemory() DB is a no-op — the recorded_streams table and its
// columns are unchanged.
func TestRecordedStreamsMigration_IdempotentReopen(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory (first open): %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Capture the column set after the first migration.
	before, err := tableColumns(d.db, "recorded_streams")
	if err != nil {
		t.Fatalf("tableColumns before re-migrate: %v", err)
	}

	// Re-run migrate() on the same database — this simulates a
	// ktx re-open of an already-migrated DB.
	if err := migrate(d.db); err != nil {
		t.Fatalf("migrate (second open): %v", err)
	}

	// The column set must be unchanged.
	after, err := tableColumns(d.db, "recorded_streams")
	if err != nil {
		t.Fatalf("tableColumns after re-migrate: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("column count changed: before=%d after=%d", len(before), len(after))
	}
	for _, col := range expectedRecordedStreamsColumns {
		if _, ok := after[col]; !ok {
			t.Errorf("column %q vanished after re-migrate", col)
		}
	}
}

// TestRecordedStreamsMigration_ForeignKeyEnforcement covers the FK
// enforcement leg: a recorded_streams INSERT referencing a valid
// session_id + bench_run_id succeeds; a row referencing a missing
// session_id OR a missing bench_run_id is rejected by the FK constraint.
//
// The FK enforcement is defense-in-depth on top of the pre-check in
// InsertRecordedStream — the pre-check gives a typed error, the FK
// catches races (impossible in a single-connection transaction but
// defensible as a SQL-level guard).
func TestRecordedStreamsMigration_ForeignKeyEnforcement(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Seed a session and a bench_run so FKs have valid targets.
	sessionID := mustNewID(t)
	if err := d.InsertSession(&Session{
		ID:         sessionID,
		Agent:      "test",
		StartedAt:  "2026-06-30T00:00:00Z",
		LastSeenAt: "2026-06-30T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	benchID := mustNewID(t)
	if err := d.InsertBenchRun(&BenchRun{
		ID:                 benchID,
		Agent:              "opencode",
		Model:              "test-model",
		Task:               "test-task",
		Score:              1.0,
		MethodologyVersion: "v1",
		Timestamp:          1782834992,
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}

	t.Run("valid session + bench_run succeeds", func(t *testing.T) {
		rsID := mustNewID(t)
		err := d.InsertRecordedStream(&RecordedStream{
			ID:           rsID,
			SessionID:    sessionID,
			BenchRunID:   benchID,
			StreamText:   `{"type":"text"}`,
			ContentHash:  ContentHash(`{"type":"text"}`),
			StreamFormat: "opencode-jsonl-1",
			CapturedAt:   "2026-06-30T00:00:00Z",
		})
		if err != nil {
			t.Fatalf("InsertRecordedStream valid FKs: %v", err)
		}
	})

	t.Run("missing session_id rejected by FK", func(t *testing.T) {
		rsID := mustNewID(t)
		err := d.InsertRecordedStream(&RecordedStream{
			ID:           rsID,
			SessionID:    "zzzzzzzzzzzzzzzz",
			BenchRunID:   benchID,
			StreamText:   "",
			ContentHash:  "",
			StreamFormat: "opencode-jsonl-1",
			CapturedAt:   "2026-06-30T00:00:00Z",
		})
		if !errors.Is(err, ErrRecordedStreamSourceNotFound) {
			t.Fatalf("InsertRecordedStream missing session: err = %v, want ErrRecordedStreamSourceNotFound", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ErrRecordedStreamSourceNotFound does not wrap ErrNotFound; CLI exit-code mapping would break")
		}
	})

	t.Run("missing bench_run_id rejected by FK", func(t *testing.T) {
		rsID := mustNewID(t)
		err := d.InsertRecordedStream(&RecordedStream{
			ID:           rsID,
			SessionID:    sessionID,
			BenchRunID:   "zzzzzzzzzzzzzzzz",
			StreamText:   "",
			ContentHash:  "",
			StreamFormat: "opencode-jsonl-1",
			CapturedAt:   "2026-06-30T00:00:00Z",
		})
		if !errors.Is(err, ErrRecordedStreamSourceNotFound) {
			t.Fatalf("InsertRecordedStream missing bench_run: err = %v, want ErrRecordedStreamSourceNotFound", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ErrRecordedStreamSourceNotFound does not wrap ErrNotFound; CLI exit-code mapping would break")
		}
	})
}
