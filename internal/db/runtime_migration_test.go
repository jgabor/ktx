// Runtime column schema migration tests (the runtime column
// added to tool_calls).
package db

import (
	"database/sql"
	"testing"
)

// TestOpen_RuntimeMigration covers the contract: an older
// database without runtime picks up the column on open,
// existing rows have NULL for runtime, and a second migrate is a no-op.
func TestOpen_RuntimeMigration(t *testing.T) {
	t.Run("unmigrated schema gains runtime idempotently", func(t *testing.T) {
		path := t.TempDir() + "/pre-runtime.db"
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
			provider TEXT,
			exit_code INTEGER,
			is_error INTEGER
		)`); err != nil {
			t.Fatalf("create tool_calls: %v", err)
		}

		sessionID := mustNewID(t)
		tcID := mustNewID(t)
		if _, err := raw.Exec(
			`INSERT INTO sessions (id, agent, started_at, last_seen_at) VALUES (?, ?, ?, ?)`,
			sessionID, "test", "2026-07-11T00:00:00Z", "2026-07-11T00:00:00Z",
		); err != nil {
			t.Fatalf("seed session: %v", err)
		}
		if _, err := raw.Exec(
			`INSERT INTO tool_calls (id, session_id, tool, input, output, content_hash, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			tcID, sessionID, "bash", "ls", "out\n", ContentHash("out\n"), "2026-07-11T00:00:01Z",
		); err != nil {
			t.Fatalf("seed tool_call: %v", err)
		}
		_ = raw.Close()

		d, err := Open(path)
		if err != nil {
			t.Fatalf("Open older db: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })

		for _, col := range runtimeColumns {
			var present int
			if err := d.db.QueryRow(
				"SELECT COUNT(*) FROM pragma_table_info('tool_calls') WHERE name = ?",
				col.Name,
			).Scan(&present); err != nil {
				t.Fatalf("pragma_table_info tool_calls.%s: %v", col.Name, err)
			}
			if present != 1 {
				t.Fatalf("tool_calls column %q missing after runtime migration", col.Name)
			}
		}

		var runtime sql.NullString
		if err := d.db.QueryRow(
			"SELECT runtime FROM tool_calls WHERE id = ?",
			tcID,
		).Scan(&runtime); err != nil {
			t.Fatalf("select runtime: %v", err)
		}
		if runtime.Valid {
			t.Errorf("runtime = %q, want NULL (existing row absent)", runtime.String)
		}

		if err := migrate(d.db); err != nil {
			t.Fatalf("second migrate: %v", err)
		}
		for _, col := range runtimeColumns {
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
