// Table-driven tests for `ktx bench` (internal/cli/bench.go).
//
// Acceptance coverage:
//
//   - AC 5 (Task 3): an empty bench_runs table queried via
//     runBenchReportImpl emits a JSON envelope on stdout where
//     `headline` is null and `rows` is []. The test calls the impl
//     directly (not the runBenchReport wrapper) per the spec: the
//     wrapper adds the database-open + format-arg parsing, but the
//     envelope shape is the impl's responsibility.
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

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// benchTestDB returns an in-memory *dbpkg.DB with the full schema
// migrated. The bench impl does not touch the filesystem; an
// in-memory database is the cleanest fixture.
func benchTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestRunBenchReportImpl_EmptyEnvelope covers AC 5: an empty
// bench_runs table produces a JSON envelope with `headline: null`
// and `rows: []`. The envelope is the only output on stdout; the
// implementation does not write to stderr on success.
//
// The test calls runBenchReportImpl directly (per the spec) so the
// envelope shape and headline/rows computation are exercised
// without the runBenchReport wrapper's database-open + flag-parse
// surface.
func TestRunBenchReportImpl_EmptyEnvelope(t *testing.T) {
	t.Run("AC5: empty bench_runs → headline=null, rows=[]", func(t *testing.T) {
		d := benchTestDB(t)
		var stdout, stderr bytes.Buffer

		err := runBenchReportImpl(&stdout, &stderr, d, nil, FormatJSON, "")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		// Decode the JSON envelope. The shape is the
		// plan-spec contract: {headline, methodology_version,
		// rows}. The headline for an empty table with no
		// filter is null (the AC6 contract).
		var env struct {
			Headline           *float64  `json:"headline"`
			MethodologyVersion *string   `json:"methodology_version"`
			Rows               []any     `json:"rows"`
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
		// filter is null (the AC6 contract: "the absence
		// marker").
		if env.MethodologyVersion != nil {
			t.Errorf("methodology_version = %v, want null", *env.MethodologyVersion)
		}
		// stderr should be empty on success.
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty, got %q", stderr.String())
		}
	})

	t.Run("AC5 companion: filter on empty table → rows=[], filter surfaces in envelope", func(t *testing.T) {
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
// data. Companion to AC 5: a single row's headline is the
// row's cost / max(score, 1e-9), the rows array carries the row
// directly, methodology_version is the row's value.
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
			Headline           *float64  `json:"headline"`
			MethodologyVersion *string   `json:"methodology_version"`
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
			ID                string  `json:"id"`
			Cost              *float64 `json:"cost"`
			MethodologyVersion string  `json:"methodology_version"`
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
// (above) and the runBenchRun flow (not in scope for Task 3 —
// bench run requires a methodology file and an agent binary).
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

// TestCaptureStreamForBenchRun covers the D20 recording contract's
// capture helper (plan Task 3). The helper persists a RecordedStream row
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
		// content_hash must equal SHA-256 of stream_text (D9 integrity).
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
// internal/db/persist_test.go — generates a D3 nanoid for test fixtures.
func mustNewIDBench(t *testing.T) string {
	t.Helper()
	id, err := dbpkg.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}

// TestRunBenchReplay_Integration covers the D20 --replay codepath (plan
// Tasks 4+5). Seeds an in-memory DB with a session + bench_runs row +
// recorded_streams row whose stream_text is a known-good JSONL fixture,
// then invokes runBenchReplay directly (NOT the runBenchRun wrapper;
// the wrapper would spawn a subprocess). Asserts:
//   - integrity=PASS (content_hash matches)
//   - parser_regression=PASS (replayed tool_use count == seeded session.tool_call_count)
//   - ZERO subprocess calls
//   - exit code 0 on PASS
func TestRunBenchReplay_Integration(t *testing.T) {
	d := benchTestDB(t)

	// Seed a session with tool_call_count=0 (the D16 fixture had 0
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

	// Seed a bench_runs row with the D16-evidence metrics.
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
		Replay           map[string]any `json:"replay"`
		Integrity        map[string]any `json:"integrity"`
		ParserRegression map[string]any `json:"parser_regression"`
		CostDrift        map[string]any `json:"cost_drift"`
		Consistency      string         `json:"consistency"`
	 OriginalBenchRun  json.RawMessage `json:"original_bench_run"`
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

// TestRunBenchReplay_NotFound covers the D4 not-found contract:
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
		ID:             sessionID,
		Agent:          "opencode",
		StartedAt:      "2026-07-08T12:00:00Z",
		LastSeenAt:     "2026-07-08T12:00:00Z",
		ToolCallCount:  1,
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
		Replay           map[string]any `json:"replay"`
		Integrity        map[string]any `json:"integrity"`
		ParserRegression map[string]any `json:"parser_regression"`
		CostDrift        map[string]any `json:"cost_drift"`
		Consistency      string         `json:"consistency"`
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
		ID:             sessionID,
		Agent:          "opencode",
		StartedAt:      "2026-07-01T12:00:00Z",
		LastSeenAt:     "2026-07-01T12:00:00Z",
		ToolCallCount:  1,
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
				"input":  "echo",
				"output": output,
				"time":   map[string]any{"start": 1, "end": 2},
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

// TestRunBenchReplay_Compression covers Task 4 AC 1: a ktx.toml that
// configures compression stages makes the replay envelope include a
// 'compression' section with baseline_bytes, compressed_bytes, ratio,
// bytes_saved, and pipeline. The pipeline shrinks the tool_use output,
// so ratio < 1.0 and bytes_saved > 0.
func TestRunBenchReplay_Compression(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	hookWriteFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:2"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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

// TestRunBenchReplay_CompressionIdentity covers Task 4 AC 2: with no
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

// TestRunBenchReplay_CompressionD15Gate covers Task 4 AC 4: a recorded
// tool_call with is_error=true is NOT compressed (D15 gate); its
// compressed_bytes contribution equals the original, so the aggregate
// ratio is 1.0 even when a compression pipeline is configured.
func TestRunBenchReplay_CompressionD15Gate(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	hookWriteFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:2"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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
		t.Errorf("compressed_bytes = %v, want %v (D15 gate: is_error → original, not compressed)", compressed, baseline)
	}
	if ratio != 1.0 {
		t.Errorf("ratio = %v, want 1.0 (D15 gate skips compression)", ratio)
	}
}

// TestRunBenchReplay_RealWorldPipeline covers Task 5 AC 2: a recorded
// session replayed with the real-world Phase 2 pipeline configured
// reports ratio < 1.0 and bytes_saved > 0 (the pipeline actually
// reduced context). Companion to TestHook_RealSessionEndToEnd, which
// exercises the live hook path; this proves the A/B measurement
// quantifies the same savings off-line.
func TestRunBenchReplay_RealWorldPipeline(t *testing.T) {
	d := benchTestDB(t)
	hookChdirTemp(t)
	hookWriteFile(t, "ktx.toml", `[pipeline.default]
stages = ["strip-ansi", "grep-v:^debug:", "keep-ends:50:20", "token-budget:2000"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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
	hookWriteFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:2"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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
