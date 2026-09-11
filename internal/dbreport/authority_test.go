package dbreport

import (
	"bytes"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/db"
)

func authorityFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authority.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(testSchema + `INSERT INTO sessions(id,agent,started_at) VALUES ('s','agent','2026-07-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	return path, database
}

func TestReportLatencyAuthority(t *testing.T) {
	path, database := authorityFixture(t)
	// No affinity: even integral reals and numeric text must not become measurements.
	if _, err := database.Exec(`ALTER TABLE tool_calls RENAME COLUMN latency_ms TO old_latency_ms; ALTER TABLE tool_calls ADD COLUMN latency_ms;`); err != nil {
		t.Fatal(err)
	}
	for i, evidence := range []struct {
		value                  any
		state, unit, authority string
	}{
		{0, "measured", "milliseconds", "runtime"},
		{20, "measured", "milliseconds", "runtime"},
		{1000, "unknown", "milliseconds", "unknown"},
		{0, "unknown", "milliseconds", "unknown"},
		{1000, "malformed", "milliseconds", "runtime"},
		{1000, "measured", "seconds", "runtime"},
		{1000, "measured", "milliseconds", "unknown"},
		{nil, "measured", "milliseconds", "runtime"},
		{-1, "measured", "milliseconds", "runtime"},
		{1.5, "measured", "milliseconds", "runtime"},
		{float64(0), "measured", "milliseconds", "runtime"},
		{math.Inf(1), "measured", "milliseconds", "runtime"},
		{math.Inf(-1), "measured", "milliseconds", "runtime"},
		{math.NaN(), "measured", "milliseconds", "runtime"},
		{"1", "measured", "milliseconds", "runtime"},
		{"bad", "measured", "milliseconds", "runtime"},
		{[]byte("1"), "measured", "milliseconds", "runtime"},
	} {
		if _, err := database.Exec(`INSERT INTO tool_calls(id,session_id,tool,timestamp,latency_ms,latency_state,latency_unit,latency_authority) VALUES (?,'s','read','2026-07-01T00:00:01Z',?,?,?,?)`, fmt.Sprint(i), evidence.value, evidence.state, evidence.unit, evidence.authority); err != nil {
			t.Fatal(err)
		}
	}
	assertDatabaseUnchanged(t, path)
	report, err := Generate(path, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	measurement := report["measurement_coverage"].(map[string]any)
	latency := measurement["coverage"].(map[string]any)["latency"].(map[string]any)
	if asInt64(latency["measured_calls"]) != 2 || latency["coverage_pct"] != percentage(2, 17) {
		t.Errorf("latency coverage = %v", latency)
	}
	dist := measurement["latency_ms_distribution"].(map[string]any)
	if dist["count"] != 2 || dist["min"] != float64(0) || dist["max"] != float64(20) || dist["average"] != float64(10) || dist["p50"] != float64(10) || dist["p95"] != float64(19) {
		t.Errorf("distribution = %v", dist)
	}
	for _, group := range []string{"tool_metrics", "agent_metrics"} {
		rows := report[group].([]map[string]any)
		if len(rows) != 1 || asFloat(rows[0]["average_latency_ms"]) != 10 {
			t.Errorf("%s = %v", group, rows)
		}
	}
	if _, err := MarshalJSON(report); err != nil {
		t.Fatalf("report is not finite JSON: %v", err)
	}
}

func TestReportUnknownLatencyIsNotMeasuredZero(t *testing.T) {
	for _, test := range []struct {
		name, state, authority string
		value                  any
		wantMean               any
		wantCount              int
	}{
		{"unknown missing", "unknown", "unknown", nil, nil, 0},
		{"unknown numeric", "unknown", "unknown", 1000, nil, 0},
		{"unknown zero", "unknown", "unknown", 0, nil, 0},
		{"measured zero", "measured", "runtime", 0, float64(0), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, database := authorityFixture(t)
			if _, err := database.Exec(`INSERT INTO tool_calls(id,session_id,tool,timestamp,latency_ms,latency_state,latency_unit,latency_authority) VALUES ('call','s','read','2026-07-01T00:00:01Z',?,?,'milliseconds',?)`, test.value, test.state, test.authority); err != nil {
				t.Fatal(err)
			}
			assertDatabaseUnchanged(t, path)
			report, err := Generate(path, false, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			measurement := report["measurement_coverage"].(map[string]any)
			coverage := measurement["coverage"].(map[string]any)["latency"].(map[string]any)
			if asInt64(coverage["measured_calls"]) != int64(test.wantCount) || coverage["coverage_pct"] != float64(100*test.wantCount) {
				t.Errorf("coverage = %v", coverage)
			}
			dist := measurement["latency_ms_distribution"].(map[string]any)
			if dist["count"] != test.wantCount {
				t.Errorf("distribution = %v", dist)
			}
			for _, key := range []string{"min", "max", "p50", "p95", "average"} {
				if dist[key] != test.wantMean {
					t.Errorf("%s = %v, want %v", key, dist[key], test.wantMean)
				}
			}
			for _, group := range []string{"tool_metrics", "agent_metrics"} {
				rows := report[group].([]map[string]any)
				if len(rows) != 1 || rows[0]["average_latency_ms"] != test.wantMean {
					t.Errorf("%s = %v, want mean %v", group, rows, test.wantMean)
				}
			}
		})
	}
}

func TestStoredOutcomeTypesRemainReadable(t *testing.T) {
	path, database := authorityFixture(t)
	// No affinity: preserve the actual stored type, including real 0/1.
	if _, err := database.Exec(`ALTER TABLE tool_calls RENAME COLUMN is_error TO old_is_error; ALTER TABLE tool_calls ADD COLUMN is_error;`); err != nil {
		t.Fatal(err)
	}
	values := []any{nil, int64(2), 0.5, float64(0), float64(1), []byte("1"), "1", "bad", int64(0), int64(1)}
	for i, value := range values {
		if _, err := database.Exec(`INSERT INTO tool_calls(id,session_id,tool,input,output,content_hash,timestamp,is_error,tokens_in,tokens_out,cost,latency_ms,model,provider,exit_code,runtime,producer) VALUES (?,'s','read','input','output','hash','2026-07-01T00:00:01Z',?,10,0,0.25,42,'model','provider',1,'runtime','write')`, fmt.Sprint(i), value); err != nil {
			t.Fatal(err)
		}
	}
	assertDatabaseUnchanged(t, path)
	reader, err := db.OpenQuery(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i := range values {
		got, err := reader.GetToolCall(fmt.Sprint(i))
		if err != nil {
			t.Errorf("value %v: %v", values[i], err)
			continue
		}
		tokensIn, tokensOut, latency, exitCode := int64(10), int64(0), int64(42), int64(1)
		cost, model, provider, runtime, producer := 0.25, "model", "provider", "runtime", db.ProducerWrite
		want := db.ToolCall{
			ID: fmt.Sprint(i), SessionID: "s", Tool: "read", Input: "input", Output: "output", ContentHash: "hash",
			Timestamp: "2026-07-01T00:00:01Z", EventType: "tool_call", TokensIn: &tokensIn, TokensOut: &tokensOut,
			Cost: &cost, LatencyMs: &latency, Model: &model, Provider: &provider, ExitCode: &exitCode,
			Runtime: &runtime, Producer: &producer, LatencyState: "unknown", LatencyUnit: "milliseconds", LatencyAuthority: "unknown",
			IsError: got.IsError, // Assert the outcome separately below.
		}
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("lost record: %+v", got)
		}
		if i < len(values)-2 {
			if got.IsError != nil {
				t.Errorf("value %#v became %v", values[i], *got.IsError)
			}
		} else if got.IsError == nil || *got.IsError != (i == len(values)-1) {
			t.Errorf("valid outcome %#v = %v", values[i], got.IsError)
		}
	}
	report, err := Generate(path, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	failures := report["failure_metrics"].(map[string]any)
	if asInt64(failures["unknown_status_calls"]) != 8 || asInt64(failures["error_calls"]) != 1 || asInt64(failures["successful_calls"]) != 1 || asFloat(failures["error_rate_pct_of_known"]) != 50 {
		t.Errorf("failures = %v", failures)
	}
	coverage := report["measurement_coverage"].(map[string]any)["coverage"].(map[string]any)["error_status"].(map[string]any)
	if asInt64(coverage["measured_calls"]) != 2 || asFloat(coverage["coverage_pct"]) != 20 {
		t.Errorf("outcome coverage = %v", coverage)
	}
	for _, group := range []string{"tool_metrics", "agent_metrics", "daily_activity"} {
		rows := report[group].([]map[string]any)
		if len(rows) != 1 || asInt64(rows[0]["error_calls"]) != 1 {
			t.Errorf("%s = %v", group, rows)
		}
	}
}

func TestIntegrityIterationFailureRejectsReport(t *testing.T) {
	path, database := authorityFixture(t)
	if _, err := database.Exec(fmt.Sprintf(`DROP TABLE recorded_streams;
CREATE VIEW recorded_streams AS
SELECT 'first' AS id, 's' AS session_id, NULL AS bench_run_id, 'body' AS stream_text, '%s' AS content_hash, 'text' AS stream_format, '2026-07-01T00:00:00Z' AS captured_at
UNION ALL
SELECT 'second', 's', NULL, 'body', json_extract('invalid-json','$'), 'text', '2026-07-01T00:00:00Z';`, db.ContentHash("body"))); err != nil {
		t.Fatal(err)
	}
	// Prove this is an iteration error after a successfully scanned row.
	rows, err := database.Query(`SELECT content_hash, CAST(stream_text AS BLOB) FROM recorded_streams`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("missing first row")
	}
	var hash string
	var body []byte
	if err := rows.Scan(&hash, &body); err != nil {
		t.Fatal(err)
	}
	if hash != db.ContentHash(string(body)) {
		t.Fatal("first row must have a valid hash")
	}
	if rows.Next() || rows.Err() == nil {
		t.Fatal("expected second-row iteration error")
	}
	rows.Close()
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if counts, err := extractIntegrity(tx); err == nil || counts != nil {
		t.Errorf("partial integrity returned: %v, %v", counts, err)
	}
	report, err := Generate(path, false, time.Now())
	if err == nil || report != nil || !strings.Contains(err.Error(), "integrity:") {
		t.Fatalf("Generate = %v, %v; want integrity error and no report", report, err)
	}
}

func assertDatabaseUnchanged(t *testing.T, path string) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Error("read-only operation changed persisted evidence")
		}
	})
}
