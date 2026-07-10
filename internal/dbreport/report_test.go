package dbreport

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testSchema = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT, started_at TEXT, ended_at TEXT, last_seen_at TEXT, tool_call_count INTEGER DEFAULT 0);
CREATE TABLE tool_calls (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), tool TEXT, input TEXT, output TEXT, content_hash TEXT, timestamp TEXT, event_type TEXT DEFAULT 'tool_call', tokens_in INTEGER, tokens_out INTEGER, cost REAL, latency_ms INTEGER, model TEXT, provider TEXT, exit_code INTEGER, is_error INTEGER);
CREATE TABLE compressed (id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES tool_calls(id), output TEXT, content_hash TEXT, pipeline TEXT, timestamp TEXT);
CREATE TABLE recorded_streams (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), bench_run_id TEXT, stream_text TEXT, content_hash TEXT, stream_format TEXT, captured_at TEXT);
`

func TestEmptyReportGoldenAndReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Generate(path, false, time.Date(2026, 7, 10, 12, 34, 56, 789000000, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := MarshalJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(filepath.Join("testdata", "empty-report.golden.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	actualHash := fmt.Sprintf("%x\n", sha256.Sum256(actual))
	if actualHash != string(expected) {
		t.Fatalf("report golden mismatch: got %s", actualHash)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("read-only report changed database bytes")
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var count int
	if err := check.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bench_runs'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("report created bench_runs table")
	}
}

func TestReportAggregatesProductEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sessions VALUES
('s1','unknown','2026-07-01T00:00:00Z','2026-07-01T00:01:00Z','2026-07-01T00:01:00Z',2),
('s2','opencode','2026-07-02T00:00:00Z',NULL,'2026-07-02T00:00:01Z',1)`); err != nil {
		t.Fatal(err)
	}
	hash := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	if _, err := database.Exec(`INSERT INTO tool_calls
(id,session_id,tool,input,output,content_hash,timestamp,tokens_in,tokens_out,cost,latency_ms,model,provider,exit_code,is_error)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?),(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?),(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"t1", "s1", "bash", "x", "héllo", hash("héllo"), "2026-07-01T00:00:01Z", 10, 2, .25, 10, "model-a", "provider-a", 0, 0,
		"t2", "s1", "bash", "y", "ERROR", hash("ERROR"), "2026-07-01T00:00:02Z", nil, nil, nil, 30, nil, nil, 1, 1,
		"t3", "s2", "read", "z", "data", hash("data"), "2026-07-02T00:00:01Z", nil, nil, nil, nil, nil, "provider-b", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO compressed VALUES
('c1','t1','hi',?,'["head:1"]','2026-07-01T00:00:03Z'),
('c2','t1','h',?,'["head:1"]','2026-07-01T00:00:04Z'),
('c3','t2','ERROR',?,'[]','2026-07-01T00:00:05Z')`, hash("hi"), hash("h"), hash("ERROR")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO recorded_streams VALUES ('r1','s1',NULL,?,?,?,?)`, `{"event":"done"}`+"\n", hash(`{"event":"done"}`+"\n"), "jsonl-1", "2026-07-01T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Generate(path, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	overview := report["overview"].(map[string]any)
	if asInt64(overview["total_sessions"]) != 2 || asInt64(overview["total_tool_calls"]) != 3 || asInt64(overview["total_output_bytes"]) != 15 {
		t.Fatalf("overview = %#v", overview)
	}
	compression := report["compression"].(map[string]any)["overview"].(map[string]any)
	if asInt64(compression["artifact_count"]) != 3 || asInt64(compression["unique_source_count"]) != 2 || asInt64(compression["recompressed_source_count"]) != 1 {
		t.Fatalf("compression = %#v", compression)
	}
	if asInt64(compression["unique_source_bytes"]) != 11 || asInt64(compression["source_bytes_across_artifacts"]) != 17 || asInt64(compression["compressed_bytes"]) != 8 {
		t.Fatalf("compression byte metrics = %#v", compression)
	}
	failures := report["failure_metrics"].(map[string]any)
	if asInt64(failures["unknown_status_calls"]) != 1 {
		t.Fatalf("failure metrics = %#v", failures)
	}
	integrity := report["integrity"].(map[string]any)["tool_calls"].(map[string]int)
	if integrity["mismatch_count"] != 0 {
		t.Fatalf("integrity = %#v", integrity)
	}
}
