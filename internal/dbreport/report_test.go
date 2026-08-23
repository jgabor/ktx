package dbreport

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const testSchema = `
CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT, agent_attribution_authority TEXT NOT NULL DEFAULT 'unknown', started_at TEXT, ended_at TEXT, last_seen_at TEXT, tool_call_count INTEGER DEFAULT 0);
CREATE TABLE tool_calls (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), tool TEXT, input TEXT, output TEXT, content_hash TEXT, timestamp TEXT, timestamp_unix_nano INTEGER, event_type TEXT DEFAULT 'tool_call', tokens_in INTEGER, tokens_out INTEGER, cost REAL, latency_ms INTEGER, model TEXT, provider TEXT, exit_code INTEGER, is_error INTEGER, runtime TEXT, latency_state TEXT NOT NULL DEFAULT 'unknown', latency_unit TEXT NOT NULL DEFAULT 'milliseconds', latency_authority TEXT NOT NULL DEFAULT 'unknown', producer TEXT);
CREATE TABLE compressed (id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES tool_calls(id), output TEXT, content_hash TEXT, pipeline TEXT, timestamp TEXT, timestamp_unix_nano INTEGER);
CREATE TABLE recorded_streams (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), bench_run_id TEXT, stream_text TEXT, content_hash TEXT, stream_format TEXT, captured_at TEXT);
PRAGMA user_version = 16;
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
	if _, err := database.Exec(`INSERT INTO sessions (id,agent,started_at,ended_at,last_seen_at,tool_call_count) VALUES
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
	if _, err := database.Exec(`INSERT INTO compressed (id,source_id,output,content_hash,pipeline,timestamp) VALUES
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

func TestReportOutcomeMetrics(t *testing.T) {
	tests := []struct {
		name                               string
		states                             []any
		successful, failed, unknown, known int64
		wantErrorRate, wantCoverage        *float64
	}{
		{name: "empty"},
		{name: "all unknown", states: []any{nil, 2, -1}, unknown: 3, wantCoverage: float64Ptr(0)},
		{name: "all known without errors", states: []any{0, 0}, successful: 2, known: 2, wantErrorRate: float64Ptr(0), wantCoverage: float64Ptr(100)},
		{name: "mixed aggregate", states: []any{0, 0, 1, nil, 7}, successful: 2, failed: 1, unknown: 2, known: 3, wantErrorRate: float64Ptr(33.33), wantCoverage: float64Ptr(60)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := createOutcomeFixture(t, test.states)
			report, err := Generate(path, false, time.Date(2026, 7, 10, 12, 34, 56, 789000000, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			if report["report_schema_version"] != SchemaVersion || SchemaVersion != 3 {
				t.Fatalf("report_schema_version = %v, want 3", report["report_schema_version"])
			}
			failures := report["failure_metrics"].(map[string]any)
			assertExactKeys(t, failures, "error_calls", "successful_calls", "unknown_status_calls", "known_status_calls", "total_tool_calls", "error_rate_pct_of_known", "outcome_coverage_pct", "exit_codes")
			if got := asInt64(failures["successful_calls"]); got != test.successful {
				t.Errorf("successful_calls = %d, want %d", got, test.successful)
			}
			if got := asInt64(failures["error_calls"]); got != test.failed {
				t.Errorf("error_calls = %d, want %d", got, test.failed)
			}
			if got := asInt64(failures["unknown_status_calls"]); got != test.unknown {
				t.Errorf("unknown_status_calls = %d, want %d", got, test.unknown)
			}
			if got := asInt64(failures["known_status_calls"]); got != test.known {
				t.Errorf("known_status_calls = %d, want %d", got, test.known)
			}
			total := int64(len(test.states))
			if got := asInt64(failures["total_tool_calls"]); got != total {
				t.Errorf("total_tool_calls = %d, want %d", got, total)
			}
			if test.known != test.successful+test.failed || total != test.known+test.unknown {
				t.Fatal("invalid test reconciliation")
			}
			assertOptionalFloat(t, failures["error_rate_pct_of_known"], test.wantErrorRate)
			assertOptionalFloat(t, failures["outcome_coverage_pct"], test.wantCoverage)

			coverage := report["measurement_coverage"].(map[string]any)["coverage"].(map[string]any)["error_status"].(map[string]any)
			if got := asInt64(coverage["measured_calls"]); got != test.known {
				t.Errorf("error-status measured_calls = %d, want %d", got, test.known)
			}
			assertOptionalFloat(t, coverage["coverage_pct"], test.wantCoverage)
			if len(test.states) > 0 {
				tool := report["tool_metrics"].([]map[string]any)[0]
				if got := asInt64(tool["unknown_status_calls"]); got != test.unknown {
					t.Errorf("tool unknown_status_calls = %d, want %d", got, test.unknown)
				}
			}
		})
	}
}

func TestReportOutcomeStorageClasses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(strings.Replace(testSchema, "is_error INTEGER", "is_error", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sessions (id,agent,started_at,ended_at,last_seen_at,tool_call_count) VALUES ('s1','unknown','2026-07-01T00:00:00Z',NULL,'2026-07-01T00:00:00Z',10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO tool_calls (id,session_id,tool,input,output,content_hash,timestamp,is_error) VALUES
('integer-zero-a','s1','bash','','','','2026-07-01T00:00:00Z',0),
('integer-zero-b','s1','read','','','','2026-07-01T00:00:00Z',0),
('integer-one','s1','bash','','','','2026-07-01T00:00:00Z',1),
('real-zero','s1','bash','','','','2026-07-01T00:00:00Z',0.0),
('real-one','s1','bash','','','','2026-07-01T00:00:00Z',1.0),
('real-other','s1','read','','','','2026-07-01T00:00:00Z',0.5),
('integer-other','s1','read','','','','2026-07-01T00:00:00Z',7),
('null','s1','read','','','','2026-07-01T00:00:00Z',NULL),
('text','s1','read','','','','2026-07-01T00:00:00Z','1'),
('blob','s1','read','','','','2026-07-01T00:00:00Z',x'01')`); err != nil {
		t.Fatal(err)
	}
	rows, err := database.Query(`SELECT id, typeof(is_error) FROM tool_calls ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	storedTypes := map[string]string{}
	for rows.Next() {
		var id, storageType string
		if err := rows.Scan(&id, &storageType); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		storedTypes[id] = storageType
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantTypes := map[string]string{
		"integer-zero-a": "integer", "integer-zero-b": "integer", "integer-one": "integer",
		"real-zero": "real", "real-one": "real", "real-other": "real", "integer-other": "integer",
		"null": "null", "text": "text", "blob": "blob",
	}
	if fmt.Sprint(storedTypes) != fmt.Sprint(wantTypes) {
		t.Fatalf("stored outcome types = %v, want %v", storedTypes, wantTypes)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := Generate(path, false, time.Date(2026, 7, 10, 12, 34, 56, 789000000, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	failures := report["failure_metrics"].(map[string]any)
	if got := []int64{asInt64(failures["successful_calls"]), asInt64(failures["error_calls"]), asInt64(failures["unknown_status_calls"]), asInt64(failures["known_status_calls"])}; fmt.Sprint(got) != "[2 1 7 3]" {
		t.Errorf("failure counts = %v, want [2 1 7 3]", got)
	}
	assertOptionalFloat(t, failures["error_rate_pct_of_known"], float64Ptr(33.33))
	assertOptionalFloat(t, failures["outcome_coverage_pct"], float64Ptr(30))
	coverage := report["measurement_coverage"].(map[string]any)["coverage"].(map[string]any)["error_status"].(map[string]any)
	if got := asInt64(coverage["measured_calls"]); got != 3 {
		t.Errorf("measured_calls = %d, want 3", got)
	}
	assertOptionalFloat(t, coverage["coverage_pct"], float64Ptr(30))

	tools := report["tool_metrics"].([]map[string]any)
	toolOutcomes := map[string][]int64{}
	for _, tool := range tools {
		toolOutcomes[asString(tool["tool"])] = []int64{asInt64(tool["error_calls"]), asInt64(tool["unknown_status_calls"])}
	}
	if got := fmt.Sprint(toolOutcomes); got != "map[bash:[1 2] read:[0 5]]" {
		t.Errorf("grouped tool outcomes = %s, want map[bash:[1 2] read:[0 5]]", got)
	}
	agents := report["agent_metrics"].([]map[string]any)
	if got := asInt64(agents[0]["error_calls"]); got != 1 {
		t.Errorf("agent error_calls = %d, want 1", got)
	}
	daily := report["daily_activity"].([]map[string]any)
	if got := asInt64(daily[0]["error_calls"]); got != 1 {
		t.Errorf("daily error_calls = %d, want 1", got)
	}
}

func createOutcomeFixture(t *testing.T, states []any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO sessions (id,agent,started_at,ended_at,last_seen_at,tool_call_count) VALUES ('s1','unknown','2026-07-01T00:00:00Z',NULL,'2026-07-01T00:00:00Z',?)`, len(states)); err != nil {
		t.Fatal(err)
	}
	for index, state := range states {
		if _, err := database.Exec(`INSERT INTO tool_calls (id,session_id,tool,input,output,content_hash,timestamp,is_error) VALUES (?,?,?,?,?,?,?,?)`, fmt.Sprintf("t%d", index), "s1", "bash", "", "", "", "2026-07-01T00:00:00Z", state); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func float64Ptr(value float64) *float64 { return &value }

func assertOptionalFloat(t *testing.T, got any, want *float64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("value = %v, want null", got)
		}
		return
	}
	value, ok := got.(float64)
	if !ok || math.Abs(value-*want) > 1e-9 {
		t.Errorf("value = %v, want %v", got, *want)
	}
}

func assertExactKeys(t *testing.T, value map[string]any, expected ...string) {
	t.Helper()
	actual := make([]string, 0, len(value))
	for key := range value {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("keys = %v, want %v", actual, expected)
	}
}
