// D15 failure-signal schema migration tests (Decision 15 AC1).
package db

import (
	"database/sql"
	"testing"
)

// TestOpen_D15FailureSignalMigration covers AC1: a pre-D15 database
// (tool_calls without exit_code/is_error) picks up the columns on open,
// existing rows have NULL for both, and a second migrate is a no-op.
func TestOpen_D15FailureSignalMigration(t *testing.T) {
	t.Run("pre-D15 schema gains exit_code and is_error idempotently", func(t *testing.T) {
		path := t.TempDir() + "/pre-d15.db"
		raw, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw sqlite: %v", err)
		}
		t.Cleanup(func() { _ = raw.Close() })

		if _, err := raw.Exec(`CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			agent TEXT,
			started_at TEXT,
			ended_at TEXT,
			last_seen_at TEXT,
			tool_call_count INTEGER DEFAULT 0
		)`); err != nil {
			t.Fatalf("create sessions: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE tool_calls (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id),
			tool TEXT,
			input TEXT,
			output TEXT,
			content_hash TEXT,
			timestamp TEXT,
			event_type TEXT DEFAULT 'tool_call',
			tokens_in INTEGER,
			tokens_out INTEGER,
			cost REAL,
			latency_ms INTEGER,
			model TEXT,
			provider TEXT
		)`); err != nil {
			t.Fatalf("create tool_calls: %v", err)
		}

		sessionID := mustNewID(t)
		tcID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO sessions (id, agent, started_at, last_seen_at) VALUES (?, ?, ?, ?)`,
			sessionID, "test", "2026-06-26T00:00:00Z", "2026-06-26T00:00:00Z",
		); err != nil {
			t.Fatalf("seed session: %v", err)
		}
		if _, err := raw.Exec(
			`INSERT INTO tool_calls (id, session_id, tool, input, output, content_hash, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			tcID, sessionID, "bash", "ls", "out\n", ContentHash("out\n"), "2026-06-26T00:00:01Z",
		); err != nil {
			t.Fatalf("seed tool_call: %v", err)
		}
		_ = raw.Close()

		d, err := Open(path)
		if err != nil {
			t.Fatalf("Open pre-D15 db: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })

		for _, col := range failureSignalColumns {
			var present int
			if err := d.db.QueryRow(
				"SELECT COUNT(*) FROM pragma_table_info('tool_calls') WHERE name = ?",
				col.Name,
			).Scan(&present); err != nil {
				t.Fatalf("pragma_table_info tool_calls.%s: %v", col.Name, err)
			}
			if present != 1 {
				t.Fatalf("tool_calls column %q missing after D15 migration", col.Name)
			}
		}

		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall pre-D15 row: %v", err)
		}
		if got.ExitCode != nil {
			t.Errorf("ExitCode = %v, want nil (existing row NULL = absent)", got.ExitCode)
		}
		if got.IsError != nil {
			t.Errorf("IsError = %v, want nil (existing row NULL = absent)", got.IsError)
		}

		if err := migrate(d.db); err != nil {
			t.Fatalf("second migrate: %v", err)
		}
		for _, col := range failureSignalColumns {
			var present int
			if err := d.db.QueryRow(
				"SELECT COUNT(*) FROM pragma_table_info('tool_calls') WHERE name = ?",
				col.Name,
			).Scan(&present); err != nil {
				t.Fatalf("pragma_table_info after re-migrate: %v", err)
			}
			if present != 1 {
				t.Fatalf("column %q duplicated or missing after idempotent migrate", col.Name)
			}
		}
	})
}
