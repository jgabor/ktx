package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestSortableTimestamp(t *testing.T) {
	want := time.Date(2026, 8, 20, 12, 34, 56, 789, time.UTC)
	got, ok := sortableTimestamp("2026-08-20T14:34:56.000000789+02:00").(int64)
	if !ok || !time.Unix(0, got).Equal(want) {
		t.Fatalf("sortable timestamp = %v, want %s", got, want)
	}
	if got := sortableTimestamp("malformed"); got != nil {
		t.Fatalf("malformed sortable timestamp = %v, want nil", got)
	}
}

func TestPersistedToolCallTimestampFormsMatch(t *testing.T) {
	d := openMemoryDB(t)
	text := "2026-08-20T14:34:56.000000789+02:00"
	seedSessionAndCalls(t, d, []callFixture{{id: "sortablepersistx", output: "ok", timestamp: text}})
	var storedText string
	var sortable int64
	if err := d.db.QueryRow(`SELECT timestamp, timestamp_unix_nano FROM tool_calls WHERE id = 'sortablepersistx'`).Scan(&storedText, &sortable); err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, storedText)
	if err != nil || !parsed.Equal(time.Unix(0, sortable)) {
		t.Fatalf("persisted timestamps differ: text=%q sortable=%d err=%v", storedText, sortable, err)
	}
}

func TestSortableTimestampMigration(t *testing.T) {
	path := t.TempDir() + "/historical.db"
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`DROP INDEX tool_calls_timestamp_unix_nano_idx; DROP INDEX session_telemetry_runtime_time_idx; ALTER TABLE tool_calls DROP COLUMN timestamp_unix_nano; ALTER TABLE session_telemetry DROP COLUMN captured_at_unix_nano; PRAGMA user_version = 14`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO sessions (id) VALUES ('migrationtimexx'); INSERT INTO tool_calls (id, session_id, output, timestamp) VALUES ('validtimestampxx','migrationtimexx','kept','2026-08-20T14:34:56+02:00'), ('badtimestampxxxx','migrationtimexx','also kept','bad')`); err != nil {
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
	var valid, malformed sql.NullInt64
	if err := d.db.QueryRow(`SELECT max(CASE WHEN id='validtimestampxx' THEN timestamp_unix_nano END), max(CASE WHEN id='badtimestampxxxx' THEN timestamp_unix_nano END) FROM tool_calls`).Scan(&valid, &malformed); err != nil {
		t.Fatal(err)
	}
	if !valid.Valid || malformed.Valid {
		t.Fatalf("migrated sortable timestamps valid=%v malformed=%v", valid, malformed)
	}
	var output string
	if err := d.db.QueryRow(`SELECT output FROM tool_calls WHERE id='badtimestampxxxx'`).Scan(&output); err != nil || output != "also kept" {
		t.Fatalf("retained evidence output=%q err=%v", output, err)
	}
}

func TestStatsWindowSelectorsUseTimeIndexes(t *testing.T) {
	d := openMemoryDB(t)
	assertPlanUsesIndex(t, d.db, `SELECT id FROM tool_calls WHERE timestamp_unix_nano BETWEEN ? AND ? AND output IS NOT NULL`, "tool_calls_timestamp_unix_nano_idx")
	assertPlanUsesIndex(t, d.db, `SELECT tokens_input FROM session_telemetry WHERE captured_at_unix_nano BETWEEN ? AND ? AND runtime = ? AND runtime_version = ?`, "session_telemetry_runtime_time_idx", "opencode", "1.18.5")
}

func assertPlanUsesIndex(t *testing.T, database *sql.DB, query, index string, extra ...any) {
	t.Helper()
	args := []any{int64(0), int64(1)}
	args = append(args, extra...)
	rows, err := database.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var detail string
	for rows.Next() {
		var id, parent, unused int
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, index) {
			return
		}
	}
	t.Fatalf("query plan did not use %s; last detail: %s", index, detail)
}
