// Table-driven tests for `ktx bench` (internal/cli/bench.go).
//
// Coverage:
//
//   - An empty bench_runs table queried via runBenchReportImpl emits
//     a JSON envelope on stdout where `headline` is null and `rows`
//     is []. The test calls the impl directly (not the runBenchReport
//     wrapper) per the spec: the wrapper adds the database-open +
//     format-arg parsing, but the envelope shape is the impl's
//     responsibility.
//
// Companion cases assert: list mode with a single row carries a
// non-null headline and a one-element rows array; single-row mode
// emits the BenchRun directly; not-found id returns a not-found
// error envelope (mapped to exit 2 by the wrapper).
package cli

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// benchTestDB returns an in-memory *dbpkg.DB with the full schema
// migrated. The bench impl does not touch the filesystem; an
// in-memory database is the cleanest fixture.
func benchTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestRunBenchReportImpl_EmptyEnvelope covers: an empty
// bench_runs table produces a JSON envelope with `headline: null`
// and `rows: []`. The envelope is the only output on stdout; the
// implementation does not write to stderr on success.
//
// The test calls runBenchReportImpl directly (per the spec) so the
// envelope shape and headline/rows computation are exercised
// without the runBenchReport wrapper's database-open + flag-parse
// surface.
func TestRunBenchReportImpl_EmptyEnvelope(t *testing.T) {
	t.Run("empty bench_runs → headline=null, rows=[]", func(t *testing.T) {
		d := benchTestDB(t)
		var stdout, stderr bytes.Buffer

		err := runBenchReportImpl(&stdout, &stderr, d, nil, FormatJSON, "")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		// Decode the JSON envelope. The shape is the
		// contract: {headline, methodology_version,
		// rows}. The headline for an empty table with no
		// filter is null (the absence-marker contract).
		var env struct {
			Headline           *float64 `json:"headline"`
			MethodologyVersion *string  `json:"methodology_version"`
			Rows               []any    `json:"rows"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env.Headline != nil {
			t.Errorf("headline = %v, want null", *env.Headline)
		}
		if env.Rows == nil {
			t.Errorf("rows = nil, want []")
		}
		if len(env.Rows) != 0 {
			t.Errorf("rows = %d, want 0", len(env.Rows))
		}
		// methodology_version for an empty table with no
		// filter is null (the absence-marker contract:
		// "the absence marker").
		if env.MethodologyVersion != nil {
			t.Errorf("methodology_version = %v, want null", *env.MethodologyVersion)
		}
		// stderr should be empty on success.
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty, got %q", stderr.String())
		}
	})

	t.Run("companion: filter on empty table → rows=[], filter surfaces in envelope", func(t *testing.T) {
		// When --methodology is set against an empty table,
		// methodology_version falls back to the filter
		// string (per the spec).
		d := benchTestDB(t)
		var stdout, stderr bytes.Buffer

		err := runBenchReportImpl(&stdout, &stderr, d, nil, FormatJSON, "v1")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		var env struct {
			Headline           *float64 `json:"headline"`
			MethodologyVersion *string  `json:"methodology_version"`
			Rows               []any    `json:"rows"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env.Headline != nil {
			t.Errorf("headline = %v, want null", *env.Headline)
		}
		if env.Rows == nil || len(env.Rows) != 0 {
			t.Errorf("rows = %v, want []", env.Rows)
		}
		if env.MethodologyVersion == nil || *env.MethodologyVersion != "v1" {
			t.Errorf("methodology_version = %v, want v1 (filter fallback)", env.MethodologyVersion)
		}
	})
}

// TestRunBenchReportImpl_NonEmpty covers the list-mode shape with
// data. Companion to the empty-table case: a single row's headline
// is the row's cost / max(score, 1e-9), the rows array carries the
// row directly, methodology_version is the row's value.
func TestRunBenchReportImpl_NonEmpty(t *testing.T) {
	t.Run("list mode with one row → headline=row's value, rows=[B]", func(t *testing.T) {
		d := benchTestDB(t)
		cost := 0.000825
		if err := d.InsertBenchRun(&dbpkg.BenchRun{
			ID:                 "ailjmiiapmzoetub",
			Agent:              "anthropic/claude-sonnet-4-20250514",
			Model:              "claude-sonnet-4-20250514",
			Task:               "addition_v1",
			Score:              1.0,
			TokensIn:           150,
			TokensOut:          25,
			Cost:               &cost,
			MethodologyVersion: "v1",
			Timestamp:          1718928000,
		}); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}

		var stdout, stderr bytes.Buffer
		err := runBenchReportImpl(&stdout, &stderr, d, nil, FormatJSON, "")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		var env struct {
			Headline           *float64         `json:"headline"`
			MethodologyVersion *string          `json:"methodology_version"`
			Rows               []map[string]any `json:"rows"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env.Headline == nil {
			t.Fatalf("headline = nil, want 0.000825")
		}
		if *env.Headline != 0.000825 {
			t.Errorf("headline = %v, want 0.000825", *env.Headline)
		}
		if env.MethodologyVersion == nil || *env.MethodologyVersion != "v1" {
			t.Errorf("methodology_version = %v, want v1", env.MethodologyVersion)
		}
		if len(env.Rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(env.Rows))
		}
		row := env.Rows[0]
		if row["id"] != "ailjmiiapmzoetub" {
			t.Errorf("row.id = %v, want ailjmiiapmzoetub", row["id"])
		}
	})

	t.Run("single-row mode emits the BenchRun directly", func(t *testing.T) {
		d := benchTestDB(t)
		cost := 0.000825
		if err := d.InsertBenchRun(&dbpkg.BenchRun{
			ID:                 "ailjmiiapmzoetub",
			Agent:              "anthropic/claude-sonnet-4-20250514",
			Model:              "claude-sonnet-4-20250514",
			Task:               "addition_v1",
			Score:              1.0,
			TokensIn:           150,
			TokensOut:          25,
			Cost:               &cost,
			MethodologyVersion: "v1",
			Timestamp:          1718928000,
		}); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}

		var stdout, stderr bytes.Buffer
		err := runBenchReportImpl(&stdout, &stderr, d, []string{"ailjmiiapmzoetub"}, FormatJSON, "")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		// Single-row mode emits the BenchRun directly
		// (no rows array, no headline envelope).
		var row struct {
			ID                 string   `json:"id"`
			Cost               *float64 `json:"cost"`
			MethodologyVersion string   `json:"methodology_version"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &row); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if row.ID != "ailjmiiapmzoetub" {
			t.Errorf("id = %q, want ailjmiiapmzoetub", row.ID)
		}
		if row.Cost == nil || *row.Cost != 0.000825 {
			t.Errorf("cost = %v, want 0.000825", row.Cost)
		}
	})

	t.Run("single-row mode not-found returns a *Error{CodeNotFound}", func(t *testing.T) {
		d := benchTestDB(t)
		var stdout, stderr bytes.Buffer

		err := runBenchReportImpl(&stdout, &stderr, d, []string{"nonexistentrowid"}, FormatJSON, "")
		if err == nil {
			t.Fatalf("runBenchReportImpl: nil error; want not-found")
		}
		// The error must be a *Error{CodeNotFound} so the
		// dispatcher adapter maps it to ExitNotFound (exit 2).
		// We assert on the public exit-code mapping via
		// exitCodeOf (defined in errors.go) and the message
		// content. The notFoundError constructor in this
		// package creates a fresh *Error{CodeNotFound}
		// without wrapping dbpkg.ErrNotFound; that is the
		// public contract — the wrapper adapter inspects
		// the *Error's Code field, not the chain.
		if got := exitCodeOf(err); got != ExitNotFound {
			t.Errorf("exitCodeOf(err) = %d, want %d (ExitNotFound)", got, ExitNotFound)
		}
		// The error message includes the missing id so the
		// user can grep for the offending record.
		if !strings.Contains(err.Error(), "nonexistentrowid") {
			t.Errorf("err.Error() = %q, want to mention the missing id", err.Error())
		}
		_ = stderr
	})
}

// TestRunBenchWith_Dispatch covers the bench subcommand dispatcher
// surface: --help, unknown subcommand, no subcommand. The bench
// run/report specific paths are covered by runBenchReportImpl
// (above) and the runBenchRun flow (out of scope here —
// bench run requires a published methodology and an agent binary).
func TestRunBenchWith_Dispatch(t *testing.T) {
	t.Run("no args prints help and exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx bench") {
			t.Errorf("stdout missing ktx bench header: %q", stdout.String())
		}
	})

	t.Run("--help prints help and exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchWith([]string{"--help"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx bench") {
			t.Errorf("stdout missing ktx bench header: %q", stdout.String())
		}
	})

	t.Run("unknown subcommand exits 1 (usage)", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchWith([]string{"nonexistent-subcommand"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "nonexistent-subcommand") {
			t.Errorf("stderr missing subcommand name: %q", stderr.String())
		}
	})
}

// TestCaptureStreamForBenchRun covers the recording contract's
// capture helper. The helper persists a RecordedStream row
// linked to a session + bench_run, with content_hash recomputed from
// stream_text. The test seeds an in-memory DB with a session + bench_run,
// calls the helper directly (no subprocess), and verifies the row
// round-trips with all 7 columns populated and content_hash integrity.
func TestCaptureStreamForBenchRun(t *testing.T) {
	t.Run("persists recorded_streams row with correct content_hash", func(t *testing.T) {
		d := benchTestDB(t)

		// Seed session + bench_run so the FKs have valid targets.
		sessionID := mustNewIDBench(t)
		if err := d.InsertSession(&dbpkg.Session{
			ID:         sessionID,
			Agent:      "opencode",
			StartedAt:  "2026-06-30T00:00:00Z",
			LastSeenAt: "2026-06-30T00:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		benchID := mustNewIDBench(t)
		if err := d.InsertBenchRun(&dbpkg.BenchRun{
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

		streamBytes := []byte(`{"type":"text","part":{"text":"4"}}`)
		if err := captureStreamForBenchRun(d, sessionID, benchID, streamBytes); err != nil {
			t.Fatalf("captureStreamForBenchRun: %v", err)
		}

		// Re-read via GetRecordedStreamBySession and verify all fields.
		got, err := d.GetRecordedStreamBySession(sessionID)
		if err != nil {
			t.Fatalf("GetRecordedStreamBySession: %v", err)
		}
		if got.SessionID != sessionID {
			t.Errorf("SessionID = %q, want %q", got.SessionID, sessionID)
		}
		if got.BenchRunID != benchID {
			t.Errorf("BenchRunID = %q, want %q", got.BenchRunID, benchID)
		}
		if got.StreamText != string(streamBytes) {
			t.Errorf("StreamText = %q, want %q", got.StreamText, string(streamBytes))
		}
		// content_hash must equal SHA-256 of stream_text (integrity).
		recomputed := dbpkg.ContentHash(got.StreamText)
		if got.ContentHash != recomputed {
			t.Errorf("ContentHash mismatch: persisted %q, recomputed %q", got.ContentHash, recomputed)
		}
		if got.StreamFormat != "opencode-jsonl-1" {
			t.Errorf("StreamFormat = %q, want opencode-jsonl-1", got.StreamFormat)
		}
		if got.CapturedAt == "" {
			t.Error("CapturedAt is empty; want RFC3339Nano timestamp")
		}
	})
}

// mustNewIDBench is the bench_test.go equivalent of mustNewID in
// internal/db/persist_test.go — generates a nanoid for test fixtures.
func mustNewIDBench(t *testing.T) string {
	t.Helper()
	id, err := dbpkg.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}

// TestRunBenchReplay_Integration covers the --replay codepath. Seeds an in-memory DB with a session + bench_runs row +
// recorded_streams row whose stream_text is a known-good JSONL fixture,
// then invokes runBenchReplay directly (NOT the runBenchRun wrapper;
// the wrapper would spawn a subprocess). Asserts:
//   - integrity=PASS (content_hash matches)
//   - parser_regression=PASS (replayed tool_use count == seeded session.tool_call_count)
//   - ZERO subprocess calls
//   - exit code 0 on PASS
func TestRunBenchReplay_Integration(t *testing.T) {
	d := benchTestDB(t)

	// Seed a session with tool_call_count=0 (the fixture had 0
	// tools — the model answered "4" without invoking any tools).
	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID:         sessionID,
		Agent:      "opencode",
		StartedAt:  "2026-06-30T15:55:51Z",
		LastSeenAt: "2026-06-30T15:55:51Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Seed a bench_runs row with the evidence metrics.
	benchID := mustNewIDBench(t)
	originalCost := 0.002597825
	if err := d.InsertBenchRun(&dbpkg.BenchRun{
		ID:                 benchID,
		Agent:              "neuralwatt/glm-5.2-short-fast-flex",
		Model:              "glm-5.2-short-fast-flex",
		Task:               "addition_v1",
		Score:              1.0,
		TokensIn:           41,
		TokensOut:          2,
		Cost:               &originalCost,
		MethodologyVersion: "v1",
		Timestamp:          1782834992,
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}

	// Seed a recorded_streams row with a known-good JSONL stream.
	// The stream has no tool_use events (the model answered directly).
	streamText := `{"type":"step_start","part":{}}
{"type":"text","part":{"text":"4"}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2},"cost":0.002597825,"model":"glm-5.2-short-fast-flex","provider":"neuralwatt"}}`
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   benchID,
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   "2026-06-30T15:56:32Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	// Call runBenchReplay directly via a test wrapper that accepts
	// a pre-opened *dbpkg.DB. The public runBenchReplay opens its
	// own DB from the path, so we test the replay LOGIC via a
	// direct invocation pattern.
	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}

	// Decode the JSON consistency envelope.
	var env struct {
		Replay           map[string]any  `json:"replay"`
		Integrity        map[string]any  `json:"integrity"`
		ParserRegression map[string]any  `json:"parser_regression"`
		CostDrift        map[string]any  `json:"cost_drift"`
		Consistency      string          `json:"consistency"`
		OriginalBenchRun json.RawMessage `json:"original_bench_run"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
	}

	// integrity=PASS: content_hash of stream_text matches stored hash.
	integrityPass, _ := env.Integrity["pass"].(bool)
	if !integrityPass {
		t.Errorf("integrity.pass = false, want true; stored_hash=%v recomputed_hash=%v",
			env.Integrity["stored_hash"], env.Integrity["recomputed_hash"])
	}

	// parser_regression=PASS: re-parsed tool_use count (0) matches
	// the session's tool_call_count (0).
	parserPass, _ := env.ParserRegression["pass"].(bool)
	if !parserPass {
		t.Errorf("parser_regression.pass = false, want true; replayed=%v original=%v",
			env.ParserRegression["replayed_tool_use_count"],
			env.ParserRegression["original_tool_call_count"])
	}

	// cost_drift: should be NONE (no drift) since the stream's
	// step_finish.cost matches the original bench_runs.cost.
	driftDetected, _ := env.CostDrift["detected"].(bool)
	if driftDetected {
		t.Errorf("cost_drift.detected = true, want false (stream cost matches original)")
	}

	// consistency=PASS.
	if env.Consistency != "PASS" {
		t.Errorf("consistency = %q, want PASS", env.Consistency)
	}
}

// TestRunBenchReplay_NotFound covers the not-found contract:
// --replay with a session-id that has no recorded_streams row exits 2.
func TestRunBenchReplay_NotFound(t *testing.T) {
	d := benchTestDB(t)

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, "zzzzzzzzzzzzzzzz", &stdout, &stderr, FormatJSON)
	if code != ExitNotFound {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found") && !strings.Contains(stderr.String(), "no recorded stream") {
		t.Errorf("stderr missing not-found message: %q", stderr.String())
	}
}

// TestRunBenchReplay_KtxRunSession covers --replay on a session
// captured via `ktx run --record-stream` (bench_run_id is NULL). The
// replay must process without a panic, skip the bench_run lookup, and
// emit a reduced report with capture_source="ktx_run" and
// original_bench_run=null. The A/B compression, integrity, and
// parser_regression checks still run normally.
func TestRunBenchReplay_KtxRunSession(t *testing.T) {
	d := benchTestDB(t)

	// Seed a session with one tool_use event in the stream (so
	// parser_regression and the compression A/B have something to
	// measure). tool_call_count=1 must match the stream's tool_use
	// count for parser_regression=PASS.
	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID:            sessionID,
		Agent:         "opencode",
		StartedAt:     "2026-07-08T12:00:00Z",
		LastSeenAt:    "2026-07-08T12:00:00Z",
		ToolCallCount: 1,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Seed a recorded_streams row with bench_run_id="" (NULL — the
	// ktx-run --record-stream path). The stream carries one
	// tool_use event with a compressible output.
	streamText := `{"type":"tool_use","timestamp":"2026-07-08T12:00:01Z","part":{"type":"tool","tool":"bash","callID":"call_001","state":{"input":{"command":"echo hello world"},"output":"hello world\n"}}}`
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   "", // NULL — ktx-run capture
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   "2026-07-08T12:00:02Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	// Re-read to verify the NULL round-tripped.
	got, err := d.GetRecordedStreamBySession(sessionID)
	if err != nil {
		t.Fatalf("GetRecordedStreamBySession: %v", err)
	}
	if got.BenchRunID != "" {
		t.Fatalf("BenchRunID = %q, want empty (NULL round-trip)", got.BenchRunID)
	}

	// Run the replay — must not panic on the NULL bench_run_id.
	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}

	// Decode the JSON envelope.
	var env struct {
		Replay           map[string]any   `json:"replay"`
		Integrity        map[string]any   `json:"integrity"`
		ParserRegression map[string]any   `json:"parser_regression"`
		CostDrift        map[string]any   `json:"cost_drift"`
		Consistency      string           `json:"consistency"`
		OriginalBenchRun *json.RawMessage `json:"original_bench_run"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
	}

	// capture_source must be "ktx_run" (not "bench_run").
	captureSource, _ := env.Replay["capture_source"].(string)
	if captureSource != "ktx_run" {
		t.Errorf("replay.capture_source = %q, want \"ktx_run\"", captureSource)
	}

	// original_bench_run must be null (no bench_runs row).
	if env.OriginalBenchRun != nil && string(*env.OriginalBenchRun) != "null" {
		t.Errorf("original_bench_run = %s, want null", string(*env.OriginalBenchRun))
	}

	// cost_drift must report comparison="unavailable" (no original
	// to compare against).
	comparison, _ := env.CostDrift["comparison"].(string)
	if comparison != "unavailable" {
		t.Errorf("cost_drift.comparison = %q, want \"unavailable\"", comparison)
	}

	// integrity=PASS: content_hash matches.
	integrityPass, _ := env.Integrity["pass"].(bool)
	if !integrityPass {
		t.Errorf("integrity.pass = false, want true")
	}

	// parser_regression=PASS: re-parsed tool_use count (1) matches
	// the session's tool_call_count (1).
	parserPass, _ := env.ParserRegression["pass"].(bool)
	if !parserPass {
		t.Errorf("parser_regression.pass = false, want true; replayed=%v original=%v",
			env.ParserRegression["replayed_tool_use_count"],
			env.ParserRegression["original_tool_call_count"])
	}

	// consistency must be PASS (integrity and parser_regression
	// both pass, and cost_drift is "unavailable" not "detected").
	if env.Consistency != "PASS" {
		t.Errorf("consistency = %q, want PASS", env.Consistency)
	}
}

// seedReplayForCompression seeds an in-memory DB with a minimal
// session + bench_run + recorded_stream carrying a single tool_use
// event whose output is the given string and whose exit code makes
// is_error the given value. Returns the session-id. The caller
// controls cwd (for ktx.toml) and XDG_CONFIG_HOME.
//
// The OpenCode tool_use event is built via json.Marshal so all JSON
// escaping is correct (including ANSI ESC control chars, which must
// be \u001b-escaped, and embedded newlines). The parser's OpenCode
// branch derives is_error from state.metadata.exit.
func seedReplayForCompression(t *testing.T, d *dbpkg.DB, output string, isErr bool) string {
	t.Helper()
	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID:            sessionID,
		Agent:         "opencode",
		StartedAt:     "2026-07-01T12:00:00Z",
		LastSeenAt:    "2026-07-01T12:00:00Z",
		ToolCallCount: 1,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	benchID := mustNewIDBench(t)
	if err := d.InsertBenchRun(&dbpkg.BenchRun{
		ID:                 benchID,
		Agent:              "opencode",
		Model:              "test-model",
		Task:               "compression_v1",
		Score:              1.0,
		MethodologyVersion: "v1",
		Timestamp:          1782834992,
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}
	exit := 0
	if isErr {
		exit = 1
	}
	// Build the OpenCode tool_use event via json.Marshal so \n, ESC,
	// quotes, and backslashes are all escaped correctly. The parser
	// re-reads output/state.metadata.exit from this shape.
	event := map[string]any{
		"type": "tool_use",
		"part": map[string]any{
			"type":   "tool",
			"tool":   "bash",
			"callID": "call1",
			"state": map[string]any{
				"input":    "echo",
				"output":   output,
				"time":     map[string]any{"start": 1, "end": 2},
				"metadata": map[string]any{"exit": exit},
			},
		},
		"timestamp": "2026-07-01T12:00:00Z",
	}
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal tool_use event: %v", err)
	}
	streamText := string(b)
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   benchID,
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   "2026-07-01T12:01:00Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}
	return sessionID
}

// itoa wraps strconv.Itoa for the bench_test fixture (also used by
// hook_test.go's TestHook_RealSessionEndToEnd within the cli test
// package).
func itoa(n int) string { return strconv.Itoa(n) }

// TestRunBenchReplay_Compression covers: a ktx.toml that
// configures compression stages makes the replay envelope include a
// 'compression' section with baseline_bytes, compressed_bytes, ratio,
// bytes_saved, and pipeline. The pipeline shrinks the tool_use output,
// so ratio < 1.0 and bytes_saved > 0.
func TestRunBenchReplay_Compression(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:2"]
`)

	output := "line 1\nline 2\nline 3\nline 4\nline 5\n" // 5 lines, head:2 → 2 lines
	sessionID := seedReplayForCompression(t, d, output, false)

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}

	var env struct {
		Compression map[string]any `json:"compression"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("decode JSON: %v\nstdout=%s", err, stdout.String())
	}
	if env.Compression == nil {
		t.Fatal("compression section missing from envelope")
	}
	baseline, _ := env.Compression["baseline_bytes"].(float64)
	compressed, _ := env.Compression["compressed_bytes"].(float64)
	ratio, _ := env.Compression["ratio"].(float64)
	saved, _ := env.Compression["bytes_saved"].(float64)
	pipeline, _ := env.Compression["pipeline"].([]any)

	if baseline != float64(len(output)) {
		t.Errorf("baseline_bytes = %v, want %d", baseline, len(output))
	}
	if compressed >= baseline || compressed <= 0 {
		t.Errorf("compressed_bytes = %v, want 0 < compressed < %v", compressed, baseline)
	}
	if ratio >= 1.0 || ratio <= 0 {
		t.Errorf("ratio = %v, want 0 < ratio < 1.0 (pipeline shrinks)", ratio)
	}
	if saved <= 0 {
		t.Errorf("bytes_saved = %v, want > 0", saved)
	}
	if len(pipeline) != 1 || pipeline[0] != "head:2" {
		t.Errorf("pipeline = %v, want [head:2]", pipeline)
	}
}

func TestMeasureReplayCompression_ReportsResolvedPipelineRuns(t *testing.T) {
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:1"]

[pipeline.search]
stages = ["head:2"]

[[route]]
tool = "grep"
profile = "search"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	section := measureReplayCompression([]*dbpkg.ToolCall{
		{Tool: "read", Output: "read 1\nread 2\nread 3\n"},
		{Tool: "grep", Output: "match 1\nmatch 2\nmatch 3\n"},
	}, cfg)
	raw, err := json.Marshal(section["pipeline_runs"])
	if err != nil {
		t.Fatal(err)
	}
	var runs []struct {
		Profile string   `json:"profile"`
		Route   string   `json:"route"`
		Stages  []string `json:"stages"`
		Calls   int64    `json:"calls"`
	}
	if err := json.Unmarshal(raw, &runs); err != nil {
		t.Fatalf("decode pipeline_runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("pipeline_runs = %s, want two resolved runs", raw)
	}
	want := []struct {
		profile string
		route   string
		stage   string
	}{
		{profile: "default", route: "default_fallback", stage: "head:1"},
		{profile: "search", route: "matched", stage: "head:2"},
	}
	for i := range want {
		if runs[i].Profile != want[i].profile || runs[i].Route != want[i].route || len(runs[i].Stages) != 1 || runs[i].Stages[0] != want[i].stage || runs[i].Calls != 1 {
			t.Errorf("pipeline_runs[%d] = %+v, want profile=%q route=%q stages=[%s] calls=1", i, runs[i], want[i].profile, want[i].route, want[i].stage)
		}
	}
}

func TestMeasureReplayCompression_BoundsPipelineRuns(t *testing.T) {
	cfg := &config.Config{Profiles: make(map[string]config.Profile)}
	calls := make([]*dbpkg.ToolCall, replayPipelineRunLimit+1)
	for i := range calls {
		tool := "tool-" + strconv.Itoa(i)
		profile := "profile-" + strconv.Itoa(i)
		cfg.Profiles[profile] = config.Profile{Stages: []string{"head:1"}}
		cfg.Routes = append(cfg.Routes, config.RouteRule{Tool: tool, Profile: profile})
		calls[i] = &dbpkg.ToolCall{Tool: tool, Output: "line 1\nline 2\n"}
	}

	section := measureReplayCompression(calls, cfg)
	runs, ok := section["pipeline_runs"].([]replayPipelineRun)
	if !ok {
		t.Fatalf("pipeline_runs type = %T", section["pipeline_runs"])
	}
	if len(runs) != replayPipelineRunLimit {
		t.Errorf("pipeline_runs = %d, want hard limit %d", len(runs), replayPipelineRunLimit)
	}
	if section["pipeline_runs_truncated"] != true || section["pipeline_runs_unreported_calls"] != int64(1) {
		t.Errorf("truncation = %v unreported_calls = %v, want true and 1", section["pipeline_runs_truncated"], section["pipeline_runs_unreported_calls"])
	}
}

func TestRunBenchReplay_MalformedConfigFails(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	sessionID := seedReplayForCompression(t, d, "line 1\nline 2\n", false)
	hookWriteUserConfig(t, "[pipeline.default\n")

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d; stdout=%q stderr=%q", code, ExitUsage, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "ktx.toml") {
		t.Errorf("stdout=%q stderr=%q, want no result and a config diagnostic", stdout.String(), stderr.String())
	}
}

// TestRunBenchReplay_CompressionIdentity covers: with no
// ktx.toml (passthrough), the compression section reports ratio=1.0,
// bytes_saved=0, and an empty pipeline.
func TestRunBenchReplay_CompressionIdentity(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	output := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	sessionID := seedReplayForCompression(t, d, output, false)

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}

	var env struct {
		Compression map[string]any `json:"compression"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("decode JSON: %v\nstdout=%s", err, stdout.String())
	}
	baseline, _ := env.Compression["baseline_bytes"].(float64)
	compressed, _ := env.Compression["compressed_bytes"].(float64)
	ratio, _ := env.Compression["ratio"].(float64)
	saved, _ := env.Compression["bytes_saved"].(float64)
	pipeline, _ := env.Compression["pipeline"].([]any)

	if baseline != float64(len(output)) {
		t.Errorf("baseline_bytes = %v, want %d", baseline, len(output))
	}
	if compressed != baseline {
		t.Errorf("compressed_bytes = %v, want %v (passthrough: compressed == baseline)", compressed, baseline)
	}
	if ratio != 1.0 {
		t.Errorf("ratio = %v, want 1.0 (passthrough)", ratio)
	}
	if saved != 0 {
		t.Errorf("bytes_saved = %v, want 0 (passthrough)", saved)
	}
	if len(pipeline) != 0 {
		t.Errorf("pipeline = %v, want [] (passthrough)", pipeline)
	}
}

// TestRunBenchReplay_CompressionFailureGate covers: a recorded
// tool_call with is_error=true is NOT compressed (failure gate); its
// compressed_bytes contribution equals the original, so the aggregate
// ratio is 1.0 even when a compression pipeline is configured.
func TestRunBenchReplay_CompressionFailureGate(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:2"]
`)

	output := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	sessionID := seedReplayForCompression(t, d, output, true) // is_error=true

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}

	var env struct {
		Compression map[string]any `json:"compression"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("decode JSON: %v\nstdout=%s", err, stdout.String())
	}
	baseline, _ := env.Compression["baseline_bytes"].(float64)
	compressed, _ := env.Compression["compressed_bytes"].(float64)
	ratio, _ := env.Compression["ratio"].(float64)

	if baseline != float64(len(output)) {
		t.Errorf("baseline_bytes = %v, want %d", baseline, len(output))
	}
	if compressed != baseline {
		t.Errorf("compressed_bytes = %v, want %v (failure gate: is_error → original, not compressed)", compressed, baseline)
	}
	if ratio != 1.0 {
		t.Errorf("ratio = %v, want 1.0 (failure gate skips compression)", ratio)
	}
}

// TestRunBenchReplay_RealWorldPipeline covers: a recorded
// session replayed with the real-world pipeline configured
// reports ratio < 1.0 and bytes_saved > 0 (the pipeline actually
// reduced context). Companion to TestHook_RealSessionEndToEnd, which
// exercises the live hook path; this proves the A/B measurement
// quantifies the same savings off-line.
func TestRunBenchReplay_RealWorldPipeline(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "grep-v:^debug:", "keep-ends:50:20", "token-budget:2000"]
`)

	// A verbose build-log-style output: ANSI codes, debug noise, many
	// lines, over the token budget. JSON-escaping is handled by
	// seedReplayForCompression (it replaces real \n with \n escapes).
	var sb strings.Builder
	sb.WriteString("\x1b[31merror\x1b[0m: build started\n")
	for i := 0; i < 100; i++ {
		sb.WriteString("debug: tracing step ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteByte('\n')
	}
	for i := 0; i < 40; i++ {
		sb.WriteString("compile: unit ")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(" ok\n")
	}
	sb.WriteString("\x1b[31mfatal\x1b[0m: linker error at end\n")
	verbose := sb.String()

	sessionID := seedReplayForCompression(t, d, verbose, false)

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}

	var env struct {
		Compression map[string]any `json:"compression"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("decode JSON: %v\nstdout=%s", err, stdout.String())
	}
	baseline, _ := env.Compression["baseline_bytes"].(float64)
	compressed, _ := env.Compression["compressed_bytes"].(float64)
	ratio, _ := env.Compression["ratio"].(float64)
	saved, _ := env.Compression["bytes_saved"].(float64)

	if baseline != float64(len(verbose)) {
		t.Errorf("baseline_bytes = %v, want %d", baseline, len(verbose))
	}
	if ratio >= 1.0 {
		t.Errorf("ratio = %v, want < 1.0 (real-world pipeline should reduce context)", ratio)
	}
	if saved <= 0 {
		t.Errorf("bytes_saved = %v, want > 0", saved)
	}
	if compressed >= baseline {
		t.Errorf("compressed_bytes = %v, want < %v (baseline)", compressed, baseline)
	}
}

// TestRunBenchReplay_CompressionEmptyStream covers the edge: a recorded
// stream with no tool_use events yields baseline=0, compressed=0,
// ratio=1.0 (the baseline==0 guard), pipeline=[].
func TestRunBenchReplay_CompressionEmptyStream(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:2"]
`)

	// A stream with no tool_use events (text + step_finish only).
	streamText := `{"type":"step_start","part":{}}
{"type":"text","part":{"text":"4"}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2}}}`
	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID: sessionID, Agent: "opencode", StartedAt: "2026-07-01T12:00:00Z",
		LastSeenAt: "2026-07-01T12:00:00Z", ToolCallCount: 0,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	benchID := mustNewIDBench(t)
	if err := d.InsertBenchRun(&dbpkg.BenchRun{
		ID: benchID, Agent: "opencode", Model: "m", Task: "t", Score: 1.0,
		MethodologyVersion: "v1", Timestamp: 1782834992,
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID: rsID, SessionID: sessionID, BenchRunID: benchID,
		StreamText: streamText, ContentHash: dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1", CapturedAt: "2026-07-01T12:01:00Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}

	var env struct {
		Compression map[string]any `json:"compression"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	baseline, _ := env.Compression["baseline_bytes"].(float64)
	ratio, _ := env.Compression["ratio"].(float64)
	if baseline != 0 {
		t.Errorf("baseline_bytes = %v, want 0 (no tool_use events)", baseline)
	}
	if ratio != 1.0 {
		t.Errorf("ratio = %v, want 1.0 (empty stream guard)", ratio)
	}
}

// TestExtractStreamMetrics_CacheTelemetry covers the stream-side cache
// parsing contract that bench_run.go uses to populate BenchRun.CacheRead /
// CacheWrite. Cache counts are per-request deltas — SUM matches the
// input/output/cost aggregation. An emitted zero is preserved (non-nil);
// absent stays nil. A cache metric present on only some step_finish events
// drops to nil (completeness gate).
func TestExtractStreamMetrics_CacheTelemetry(t *testing.T) {
	t.Run("per-request SUM + emitted zero preserved", func(t *testing.T) {
		// Two step_finish events: cache.read = 100+15232 = 15332,
		// cache.write = 500+1500 = 2000. The SUM aggregation
		// recovers these totals; MAX would give 15232 and 1500
		// (wrong for non-zero first events).
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"cache":{"read":100,"write":500}}}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":15232,"write":1500}}}}`
		sm := extractStreamMetrics([]byte(stream))
		if !sm.hasData {
			t.Fatal("hasData=false, want true (step_finish events present)")
		}
		if sm.cacheRead == nil || *sm.cacheRead != 15332 {
			t.Errorf("cacheRead = %v, want pointer to 15332 (SUM of per-request counts)", sm.cacheRead)
		}
		if sm.cacheWrite == nil || *sm.cacheWrite != 2000 {
			t.Errorf("cacheWrite = %v, want pointer to 2000", sm.cacheWrite)
		}
	})

	t.Run("MAX would give wrong answer for non-zero first event", func(t *testing.T) {
		// cache.read=100 on first event, 200 on second.
		// SUM=300, MAX=200 (wrong). Locks SUM, not MAX.
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":1,"cache":{"read":100,"write":50}}}}
{"type":"step_finish","part":{"tokens":{"input":20,"output":2,"cache":{"read":200,"write":100}}}}`
		sm := extractStreamMetrics([]byte(stream))
		if sm.cacheRead == nil || *sm.cacheRead != 300 {
			t.Errorf("cacheRead = %v, want 300 (SUM: 100+200), not 200 (MAX)", sm.cacheRead)
		}
		if sm.cacheWrite == nil || *sm.cacheWrite != 150 {
			t.Errorf("cacheWrite = %v, want 150 (SUM: 50+100), not 100 (MAX)", sm.cacheWrite)
		}
	})

	t.Run("multi-step cost and cache SUM", func(t *testing.T) {
		// Two step_finish events: cost SUM = 0.01+0.02, cache.read
		// SUM = 100+15232 = 15332, cache.write SUM = 100+100 = 200.
		// If cost used MAX it would give 0.02 (wrong); if cache
		// used MAX it would give 15232 (wrong). Both use SUM.
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":1,"cache":{"read":100,"write":100}},"cost":0.01}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":15232,"write":100}},"cost":0.02}}`
		sm := extractStreamMetrics([]byte(stream))
		if !sm.hasData {
			t.Fatal("hasData=false, want true")
		}
		wantCost := 0.01 + 0.02
		if sm.cost == nil || *sm.cost != wantCost {
			t.Errorf("cost = %v, want %v (SUM of per-step deltas)", sm.cost, wantCost)
		}
		if sm.cacheRead == nil || *sm.cacheRead != 15332 {
			t.Errorf("cacheRead = %v, want 15332 (SUM of per-request counts)", sm.cacheRead)
		}
		if sm.cacheWrite == nil || *sm.cacheWrite != 200 {
			t.Errorf("cacheWrite = %v, want 200 (SUM of per-request counts)", sm.cacheWrite)
		}
	})

	t.Run("absent cache leaves nil pointers", func(t *testing.T) {
		// step_finish with no part.tokens.cache: an older runtime
		// that does not emit cache telemetry. The absent value
		// is NOT the same as an emitted zero.
		stream := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2},"cost":0.01}}`
		sm := extractStreamMetrics([]byte(stream))
		if !sm.hasData {
			t.Fatal("hasData=false, want true")
		}
		if sm.cacheRead != nil {
			t.Errorf("cacheRead = %v, want nil (cache telemetry absent)", *sm.cacheRead)
		}
		if sm.cacheWrite != nil {
			t.Errorf("cacheWrite = %v, want nil (cache telemetry absent)", *sm.cacheWrite)
		}
	})

	t.Run("emitted zero on every event preserved as 0", func(t *testing.T) {
		// cache.read=0 on every step_finish (cold cache): emitted
		// zero must round-trip as a non-nil pointer to 0.
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"cache":{"read":0,"write":0}}}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":0,"write":0}}}}`
		sm := extractStreamMetrics([]byte(stream))
		if sm.cacheRead == nil || *sm.cacheRead != 0 {
			t.Errorf("cacheRead = %v, want pointer to 0 (emitted zero preserved)", sm.cacheRead)
		}
		if sm.cacheWrite == nil || *sm.cacheWrite != 0 {
			t.Errorf("cacheWrite = %v, want pointer to 0 (emitted zero preserved)", sm.cacheWrite)
		}
	})

	t.Run("partial cache evidence leaves nil (completeness gate)", func(t *testing.T) {
		// First step_finish carries cache.read=100 and cache.write=50;
		// the second carries neither. The completeness gate drops
		// both pointers to nil — a SUM from a subset of events would
		// misrepresent the attempt. Mirrors experiment.ParseStream's
		// `sawCacheRead && CacheReadEvents == steps` guard.
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":1,"cache":{"read":100,"write":50}}}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2}}}`
		sm := extractStreamMetrics([]byte(stream))
		if !sm.hasData {
			t.Fatal("hasData=false, want true")
		}
		if sm.cacheRead != nil {
			t.Errorf("cacheRead = %v, want nil (incomplete — one step_finish lacks cache.read)", *sm.cacheRead)
		}
		if sm.cacheWrite != nil {
			t.Errorf("cacheWrite = %v, want nil (incomplete — one step_finish lacks cache.write)", *sm.cacheWrite)
		}
	})

	t.Run("explicit JSON null treated as absent, not emitted zero", func(t *testing.T) {
		// Explicit JSON null for cache.read, cache.write, and cost
		// must be treated as absent (nil pointers), not emitted zero.
		// This matches experiment.ParseStream where *float64 pointers
		// unmarshal null to nil. Without this guard, null would be
		// indistinguishable from a real 0 measurement.
		stream := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":null,"write":null}},"cost":null}}`
		sm := extractStreamMetrics([]byte(stream))
		if !sm.hasData {
			t.Fatal("hasData=false, want true")
		}
		if sm.cacheRead != nil {
			t.Errorf("cacheRead = %v, want nil (JSON null is absent, not zero)", *sm.cacheRead)
		}
		if sm.cacheWrite != nil {
			t.Errorf("cacheWrite = %v, want nil (JSON null is absent, not zero)", *sm.cacheWrite)
		}
		if sm.cost != nil {
			t.Errorf("cost = %v, want nil (JSON null is absent, not zero)", *sm.cost)
		}
	})

	t.Run("null cache on one event triggers completeness gate", func(t *testing.T) {
		// First step_finish: cache.read=100. Second: cache.read=null.
		// The null is absent (not zero), so cacheReadEvents=1 < steps=2
		// → completeness gate drops the pointer to nil.
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":1,"cache":{"read":100,"write":50}}}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":null,"write":null}}}}`
		sm := extractStreamMetrics([]byte(stream))
		if sm.cacheRead != nil {
			t.Errorf("cacheRead = %v, want nil (null on second event fails completeness gate)", *sm.cacheRead)
		}
		if sm.cacheWrite != nil {
			t.Errorf("cacheWrite = %v, want nil (null on second event fails completeness gate)", *sm.cacheWrite)
		}
	})
}

func TestExtractStreamMetrics_UsageCompleteness(t *testing.T) {
	t.Run("complete explicit zeros remain observations", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}`
		sm := extractStreamMetrics([]byte(stream))
		if !sm.completeTokens() || !sm.completeReportedCost() || sm.tokensIn == nil || *sm.tokensIn != 0 || sm.tokensOut == nil || *sm.tokensOut != 0 || sm.reasoning == nil || *sm.reasoning != 0 || sm.cost == nil || *sm.cost != 0 {
			t.Fatalf("explicit-zero metrics = %#v", sm)
		}
	})
	tests := []struct {
		name, first, second, field string
	}{
		{"partial input including zero", `"input":0,"output":1,"reasoning":0`, `"output":2,"reasoning":0`, "input"},
		{"partial output including zero", `"input":1,"output":0,"reasoning":0`, `"input":2,"reasoning":0`, "output"},
		{"partial reasoning including zero", `"input":1,"output":1,"reasoning":0`, `"input":2,"output":2`, "reasoning"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := `{"type":"step_finish","part":{"tokens":{` + test.first + `,"cache":{"read":0,"write":0}},"cost":0}}` + "\n" +
				`{"type":"step_finish","part":{"tokens":{` + test.second + `,"cache":{"read":0,"write":0}},"cost":0}}`
			sm := extractStreamMetrics([]byte(stream))
			if sm.completeTokens() {
				t.Fatal("completeTokens=true for partial token stream")
			}
			switch test.field {
			case "input":
				if sm.tokensIn != nil {
					t.Fatalf("tokensIn = %v, want nil", *sm.tokensIn)
				}
			case "output":
				if sm.tokensOut != nil {
					t.Fatalf("tokensOut = %v, want nil", *sm.tokensOut)
				}
			case "reasoning":
				if sm.reasoning != nil {
					t.Fatalf("reasoning = %v, want nil", *sm.reasoning)
				}
			}
		})
	}
	t.Run("partial cost including zero", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}` + "\n" +
			`{"type":"step_finish","part":{"tokens":{"input":2,"output":2,"reasoning":0,"cache":{"read":0,"write":0}}}}`
		if sm := extractStreamMetrics([]byte(stream)); sm.cost != nil || !sm.completeTokens() || sm.completeReportedCost() {
			t.Fatalf("partial cost metrics = %#v, want complete tokens and incomplete reported cost", sm)
		}
	})
}

// TestRunBenchReplay_CacheDrift covers the cost_drift envelope's cache
// fields. Seed a bench_run with cache telemetry and a matching recorded
// stream; assert replayed_cache_read / original_cache_read round-trip, and
// that cache-bearing original_bench_run carries the columns verbatim.
// The fixture uses a single step_finish so every field (tokens_in,
// tokens_out, cost, cache_read, cache_write) matches exactly — PASS
// genuinely means all fields match.
func TestRunBenchReplay_CacheDrift(t *testing.T) {
	d := benchTestDB(t)

	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID:         sessionID,
		Agent:      "opencode",
		StartedAt:  "2026-06-30T15:55:51Z",
		LastSeenAt: "2026-06-30T15:55:51Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	benchID := mustNewIDBench(t)
	originalCost := 0.002597825
	originalCacheRead := int64(15232)
	originalCacheWrite := int64(1500)
	if err := d.InsertBenchRun(&dbpkg.BenchRun{
		ID:                 benchID,
		Agent:              "neuralwatt/glm-5.2-short-fast-flex",
		Model:              "glm-5.2-short-fast-flex",
		Task:               "addition_v1",
		Score:              1.0,
		TokensIn:           41,
		TokensOut:          2,
		Cost:               &originalCost,
		MethodologyVersion: "v1",
		Timestamp:          1782834992,
		CacheRead:          &originalCacheRead,
		CacheWrite:         &originalCacheWrite,
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}

	// Single step_finish: all fields (tokens_in, tokens_out, cost,
	// cache_read, cache_write) match the original bench_run row → PASS.
	streamText := `{"type":"step_start","part":{}}
{"type":"text","part":{"text":"4"}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":15232,"write":1500}},"cost":0.002597825,"model":"glm-5.2-short-fast-flex","provider":"neuralwatt"}}`
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   benchID,
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   "2026-06-30T15:56:32Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}

	var env struct {
		CostDrift        map[string]any  `json:"cost_drift"`
		OriginalBenchRun json.RawMessage `json:"original_bench_run"`
		Consistency      string          `json:"consistency"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
	}

	// cost_drift.detected = false: all fields match.
	driftDetected, _ := env.CostDrift["detected"].(bool)
	if driftDetected {
		t.Errorf("cost_drift.detected = true, want false (all fields match)")
	}

	// The envelope retains both replayed and original cache values
	// even when no drift, so PASS is diagnosable.
	replayedCacheRead, ok := env.CostDrift["replayed_cache_read"].(float64)
	if !ok {
		t.Errorf("cost_drift.replayed_cache_read missing or wrong type: %T", env.CostDrift["replayed_cache_read"])
	} else if replayedCacheRead != 15232 {
		t.Errorf("cost_drift.replayed_cache_read = %v, want 15232", replayedCacheRead)
	}
	originalCacheReadJSON, ok := env.CostDrift["original_cache_read"].(float64)
	if !ok {
		t.Errorf("cost_drift.original_cache_read missing or wrong type: %T", env.CostDrift["original_cache_read"])
	} else if originalCacheReadJSON != 15232 {
		t.Errorf("cost_drift.original_cache_read = %v, want 15232", originalCacheReadJSON)
	}

	// original_bench_run carries cache columns verbatim.
	var obr struct {
		CacheRead  *int64 `json:"cache_read"`
		CacheWrite *int64 `json:"cache_write"`
	}
	if err := json.Unmarshal(env.OriginalBenchRun, &obr); err != nil {
		t.Fatalf("decode original_bench_run: %v", err)
	}
	if obr.CacheRead == nil || *obr.CacheRead != 15232 {
		t.Errorf("original_bench_run.cache_read = %v, want 15232", obr.CacheRead)
	}
	if obr.CacheWrite == nil || *obr.CacheWrite != 1500 {
		t.Errorf("original_bench_run.cache_write = %v, want 1500", obr.CacheWrite)
	}

	if env.Consistency != "PASS" {
		t.Errorf("consistency = %q, want PASS", env.Consistency)
	}

	// Legacy replay_* keys (replay_tokens_in, replay_tokens_out,
	// replay_cost, original_tokens_in, original_tokens_out,
	// original_cost) must be preserved — they predate cache telemetry
	// and downstream consumers depend on them. Cache fields use the
	// replayed_* / original_* vocabulary. This protects against
	// accidental renaming.
	if _, ok := env.CostDrift["replay_tokens_in"]; !ok {
		t.Errorf("cost_drift.replay_tokens_in missing (legacy key must be preserved)")
	}
	if _, ok := env.CostDrift["replay_tokens_out"]; !ok {
		t.Errorf("cost_drift.replay_tokens_out missing (legacy key must be preserved)")
	}
	if _, ok := env.CostDrift["replay_cost"]; !ok {
		t.Errorf("cost_drift.replay_cost missing (legacy key must be preserved)")
	}
	if _, ok := env.CostDrift["original_tokens_in"]; !ok {
		t.Errorf("cost_drift.original_tokens_in missing (legacy key must be preserved)")
	}
	if _, ok := env.CostDrift["original_tokens_out"]; !ok {
		t.Errorf("cost_drift.original_tokens_out missing (legacy key must be preserved)")
	}
	if _, ok := env.CostDrift["original_cost"]; !ok {
		t.Errorf("cost_drift.original_cost missing (legacy key must be preserved)")
	}
	// No replayed_tokens_in / replayed_tokens_out / replayed_cost —
	// these were never introduced; the legacy keys are the canonical ones.
	if _, ok := env.CostDrift["replayed_tokens_in"]; ok {
		t.Errorf("cost_drift.replayed_tokens_in present — must NOT exist (legacy replay_tokens_in is canonical)")
	}
	if _, ok := env.CostDrift["replayed_cost"]; ok {
		t.Errorf("cost_drift.replayed_cost present — must NOT exist (legacy replay_cost is canonical)")
	}
}

// no original bench_run to compare against, but replayed_cache_read /
// replayed_cache_write surface in the "unavailable comparison" envelope.
func TestRunBenchReplay_CacheAbsentKtxRun(t *testing.T) {
	d := benchTestDB(t)

	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID:         sessionID,
		Agent:      "opencode",
		StartedAt:  "2026-06-30T15:55:51Z",
		LastSeenAt: "2026-06-30T15:55:51Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// A recorded_stream with no parent bench_run (captures were
	// taken via `ktx run --record-stream`).
	streamText := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":15232,"write":1500}},"cost":0.01,"model":"glm-5.2-short-fast-flex","provider":"neuralwatt"}}`
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   "", // ktx-run path: no bench_run
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   "2026-06-30T15:56:32Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}

	var env struct {
		CostDrift   map[string]any `json:"cost_drift"`
		Consistency string         `json:"consistency"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
	}

	// ktx-run path: detected=false, comparison=unavailable.
	detected, _ := env.CostDrift["detected"].(bool)
	if detected {
		t.Errorf("cost_drift.detected = true, want false (ktx-run path)")
	}
	comparison, _ := env.CostDrift["comparison"].(string)
	if comparison != "unavailable" {
		t.Errorf("cost_drift.comparison = %q, want unavailable", comparison)
	}

	// replayed_cache_read / replayed_cache_write surface even on the
	// ktx-run path (the stream is the only source, the original is nil).
	replayedCacheRead, ok := env.CostDrift["replayed_cache_read"].(float64)
	if !ok {
		t.Errorf("cost_drift.replayed_cache_read missing or wrong type: %T", env.CostDrift["replayed_cache_read"])
	} else if replayedCacheRead != 15232 {
		t.Errorf("cost_drift.replayed_cache_read = %v, want 15232", replayedCacheRead)
	}
	replayedCacheWrite, ok := env.CostDrift["replayed_cache_write"].(float64)
	if !ok {
		t.Errorf("cost_drift.replayed_cache_write missing or wrong type: %T", env.CostDrift["replayed_cache_write"])
	} else if replayedCacheWrite != 1500 {
		t.Errorf("cost_drift.replayed_cache_write = %v, want 1500", replayedCacheWrite)
	}

	if env.Consistency != "PASS" {
		t.Errorf("consistency = %q, want PASS", env.Consistency)
	}

	// Legacy replay_* keys preserved on the ktx-run path too.
	if _, ok := env.CostDrift["replay_tokens_in"]; !ok {
		t.Errorf("cost_drift.replay_tokens_in missing (legacy key must be preserved on ktx-run path)")
	}
	if _, ok := env.CostDrift["replay_tokens_out"]; !ok {
		t.Errorf("cost_drift.replay_tokens_out missing (legacy key must be preserved on ktx-run path)")
	}
	if _, ok := env.CostDrift["replay_cost"]; !ok {
		t.Errorf("cost_drift.replay_cost missing (legacy key must be preserved on ktx-run path)")
	}
}

// TestRunBenchReplay_CacheMismatch covers cache-only drift: tokens,
// tokens_out, and cost all match, but cache_read differs → DRIFT. The
// envelope carries both sides for diagnosis.
func TestRunBenchReplay_CacheMismatch(t *testing.T) {
	d := benchTestDB(t)

	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID:         sessionID,
		Agent:      "opencode",
		StartedAt:  "2026-06-30T15:55:51Z",
		LastSeenAt: "2026-06-30T15:55:51Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	benchID := mustNewIDBench(t)
	originalCost := 0.002597825
	originalCacheRead := int64(15232)
	originalCacheWrite := int64(1500)
	if err := d.InsertBenchRun(&dbpkg.BenchRun{
		ID:                 benchID,
		Agent:              "neuralwatt/glm-5.2-short-fast-flex",
		Model:              "glm-5.2-short-fast-flex",
		Task:               "addition_v1",
		Score:              1.0,
		TokensIn:           41,
		TokensOut:          2,
		Cost:               &originalCost,
		MethodologyVersion: "v1",
		Timestamp:          1782834992,
		CacheRead:          &originalCacheRead,
		CacheWrite:         &originalCacheWrite,
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}

	// tokens/cost match, but cache_read=9999 ≠ 15232 → DRIFT.
	streamText := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":9999,"write":1500}},"cost":0.002597825,"model":"glm-5.2-short-fast-flex","provider":"neuralwatt"}}`
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   benchID,
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   "2026-06-30T15:56:32Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}

	var env struct {
		CostDrift   map[string]any `json:"cost_drift"`
		Consistency string         `json:"consistency"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
	}

	driftDetected, _ := env.CostDrift["detected"].(bool)
	if !driftDetected {
		t.Errorf("cost_drift.detected = false, want true (cache_read mismatch: 9999 ≠ 15232)")
	}
	if env.Consistency != "DRIFT" {
		t.Errorf("consistency = %q, want DRIFT", env.Consistency)
	}

	// Envelope carries both sides for diagnosis.
	replayedCacheRead, ok := env.CostDrift["replayed_cache_read"].(float64)
	if !ok || replayedCacheRead != 9999 {
		t.Errorf("cost_drift.replayed_cache_read = %v, want 9999", env.CostDrift["replayed_cache_read"])
	}
	originalCR, ok := env.CostDrift["original_cache_read"].(float64)
	if !ok || originalCR != 15232 {
		t.Errorf("cost_drift.original_cache_read = %v, want 15232", env.CostDrift["original_cache_read"])
	}
}

// TestRunBenchReplay_CacheNilVsZero covers the absent-vs-emitted-zero
// distinction on cache drift. A bench_run with cache_read=nil (absent —
// pre-cache runtime) and a stream emitting cache_read=0 (cold cache) is
// a drift: absent ≠ emitted zero. The comparison must not treat nil and
// 0 as equal.
func TestRunBenchReplay_CacheNilVsZero(t *testing.T) {
	d := benchTestDB(t)

	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID:         sessionID,
		Agent:      "opencode",
		StartedAt:  "2026-06-30T15:55:51Z",
		LastSeenAt: "2026-06-30T15:55:51Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// bench_run has cache_read=nil (absent — pre-cache runtime).
	// Stream emits cache_read=0 (cold cache). Absent ≠ emitted
	// zero → DRIFT.
	benchID := mustNewIDBench(t)
	originalCost := 0.002597825
	if err := d.InsertBenchRun(&dbpkg.BenchRun{
		ID:                 benchID,
		Agent:              "neuralwatt/glm-5.2-short-fast-flex",
		Model:              "glm-5.2-short-fast-flex",
		Task:               "addition_v1",
		Score:              1.0,
		TokensIn:           41,
		TokensOut:          2,
		Cost:               &originalCost,
		MethodologyVersion: "v1",
		Timestamp:          1782834992,
		// CacheRead / CacheWrite left nil (absent — pre-cache runtime)
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}

	streamText := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":0,"write":0}},"cost":0.002597825,"model":"glm-5.2-short-fast-flex","provider":"neuralwatt"}}`
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   benchID,
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   "2026-06-30T15:56:32Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}

	var env struct {
		CostDrift   map[string]any `json:"cost_drift"`
		Consistency string         `json:"consistency"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
	}

	driftDetected, _ := env.CostDrift["detected"].(bool)
	if !driftDetected {
		t.Errorf("cost_drift.detected = false, want true (nil ≠ emitted zero on cache_read)")
	}
	if env.Consistency != "DRIFT" {
		t.Errorf("consistency = %q, want DRIFT", env.Consistency)
	}

	// original_cache_read must be null (nil pointer), replayed_cache_read must be 0.
	if env.CostDrift["original_cache_read"] != nil {
		t.Errorf("original_cache_read = %v, want null (absent)", env.CostDrift["original_cache_read"])
	}
	replayedCacheRead, ok := env.CostDrift["replayed_cache_read"].(float64)
	if !ok {
		t.Errorf("cost_drift.replayed_cache_read missing or wrong type: %T", env.CostDrift["replayed_cache_read"])
	} else if replayedCacheRead != 0 {
		t.Errorf("cost_drift.replayed_cache_read = %v, want 0 (emitted zero)", replayedCacheRead)
	}
}

// TestRunBenchReplay_MultiStepCacheSUM covers multi-step cache SUM
// semantics end-to-end through the replay path. Two step_finish events
// with cache.read=100 and cache.read=15200 SUM to 15300 (matching the
// bench_run row). MAX would give 15200 ≠ 15300 → DRIFT, but SUM
// correctly produces PASS.
func TestRunBenchReplay_MultiStepCacheSUM(t *testing.T) {
	d := benchTestDB(t)

	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID:         sessionID,
		Agent:      "opencode",
		StartedAt:  "2026-06-30T15:55:51Z",
		LastSeenAt: "2026-06-30T15:55:51Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	benchID := mustNewIDBench(t)
	originalCost := 0.03
	originalCacheRead := int64(15300) // SUM of 100 + 15200
	originalCacheWrite := int64(1500) // SUM of 500 + 1000
	if err := d.InsertBenchRun(&dbpkg.BenchRun{
		ID:                 benchID,
		Agent:              "neuralwatt/glm-5.2-short-fast-flex",
		Model:              "glm-5.2-short-fast-flex",
		Task:               "addition_v1",
		Score:              1.0,
		TokensIn:           41, // SUM of 10 + 31
		TokensOut:          2,  // SUM of 1 + 1
		Cost:               &originalCost,
		MethodologyVersion: "v1",
		Timestamp:          1782834992,
		CacheRead:          &originalCacheRead,
		CacheWrite:         &originalCacheWrite,
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}

	// Two step_finish events: every field SUMs to the bench_run row.
	// If MAX were used, cache_read=15200 ≠ 15300 → DRIFT (wrong).
	streamText := `{"type":"step_start","part":{}}
{"type":"step_finish","part":{"tokens":{"input":10,"output":1,"cache":{"read":100,"write":500}},"cost":0.01,"model":"glm-5.2-short-fast-flex","provider":"neuralwatt"}}
{"type":"step_finish","part":{"tokens":{"input":31,"output":1,"cache":{"read":15200,"write":1000}},"cost":0.02,"model":"glm-5.2-short-fast-flex","provider":"neuralwatt"}}`
	rsID := mustNewIDBench(t)
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   benchID,
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   "2026-06-30T15:56:32Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}

	var env struct {
		CostDrift   map[string]any `json:"cost_drift"`
		Consistency string         `json:"consistency"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
	}

	// SUM: all fields match the bench_run row → PASS.
	driftDetected, _ := env.CostDrift["detected"].(bool)
	if driftDetected {
		t.Errorf("cost_drift.detected = true, want false (SUM matches: 100+15200=15300, not MAX=15200)")
	}
	if env.Consistency != "PASS" {
		t.Errorf("consistency = %q, want PASS", env.Consistency)
	}

	// Verify the replayed SUM values surface in the envelope.
	replayedCacheRead, ok := env.CostDrift["replayed_cache_read"].(float64)
	if !ok || replayedCacheRead != 15300 {
		t.Errorf("replayed_cache_read = %v, want 15300 (SUM of 100+15200)", env.CostDrift["replayed_cache_read"])
	}
}
