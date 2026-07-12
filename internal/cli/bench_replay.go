package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	parserpkg "github.com/jgabor/ktx/internal/parser"
)

// runBenchReplay implements `ktx bench run --replay <session-id>`. It
// loads the recorded JSONL stream
// from recorded_streams by session_id, re-parses it through
// parser.ParseEvent (parser_regression check), re-aggregates tokens/cost
// via extractStreamMetrics (cost_drift check), and emits a JSON
// consistency envelope comparing replay-derived metrics to the original
// bench_runs row — with ZERO agent calls.
//
// Exit codes: 0 on PASS or DRIFT (drift is informational); 2 on
// not-found (session-id has no recorded_streams row); 4 on internal
// failure (FAIL consistency or database error).
func runBenchReplay(sessionID string, opts Options, stdout, stderr io.Writer, format string) int {
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx bench run --replay: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := dbpkg.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx bench run --replay: open database: %v", err), format)
		return ExitInternal
	}
	defer db.Close()
	return runBenchReplayImpl(db, sessionID, stdout, stderr, format)
}

// runBenchReplayImpl is the testable core of runBenchReplay. It takes
// a pre-opened *dbpkg.DB so integration tests can seed an in-memory DB
// and invoke the replay logic directly without filesystem or subprocess
// overhead. The public runBenchReplay opens the DB and delegates here.
func runBenchReplayImpl(db *dbpkg.DB, sessionID string, stdout, stderr io.Writer, format string) int {
	// Pre-flight: verify recorded_streams table exists. A missing table
	// signals an old database; the user should run any ktx command to
	// trigger the migration.
	hasTable, err := db.HasRecordedStreamsTable()
	if err != nil {
		printError(stderr, internalError("ktx bench run --replay: recorded_streams check: %v", err), format)
		return ExitInternal
	}
	if !hasTable {
		printError(stderr, notFoundError("ktx bench run --replay: recorded_streams table is missing (old database; run any ktx command to apply the migration)"), format)
		return ExitNotFound
	}

	// Load the recorded stream by session_id (the public --replay arg).
	rs, err := db.GetRecordedStreamBySession(sessionID)
	if err != nil {
		if errors.Is(err, dbpkg.ErrNotFound) {
			printError(stderr, notFoundError("ktx bench run --replay: no recorded stream for session %q", sessionID), format)
			return ExitNotFound
		}
		printError(stderr, internalError("ktx bench run --replay: load recorded stream: %v", err), format)
		return ExitInternal
	}

	// Load the original session for tool_call_count (parser_regression
	// check) and the original bench_runs row for comparison.
	session, err := db.GetSession(rs.SessionID)
	if err != nil {
		printError(stderr, internalError("ktx bench run --replay: load session: %v", err), format)
		return ExitInternal
	}

	// When bench_run_id is empty (NULL — the stream was captured via
	// `ktx run --record-stream`, which has no bench_runs parent),
	// skip the original bench_run load and the cost-drift comparison.
	// The replay still runs integrity + parser_regression + A/B
	// compression, but the envelope carries null for
	// original_bench_run and a capture_source marker so consumers can
	// tell the two capture paths apart.
	var originalBR *dbpkg.BenchRun
	if rs.BenchRunID != "" {
		br, err := db.QueryBenchRun(rs.BenchRunID)
		if err != nil {
			printError(stderr, internalError("ktx bench run --replay: load bench_run: %v", err), format)
			return ExitInternal
		}
		originalBR = &br
	}

	// (a) Stream integrity: recompute content_hash and compare.
	recomputedHash := dbpkg.ContentHash(rs.StreamText)
	integrityPass := recomputedHash == rs.ContentHash

	// (b) Parser regression: re-parse stream_text through parser.ParseEvent
	// and count tool_use events; compare to the original session's
	// tool_call_count. Since parser.ParseEvent is pure and content_hash
	// guarantees byte-passthrough, a mismatch can ONLY mean a parser code
	// change between capture and replay.
	//
	// The parsed tool_calls are also collected: the A/B
	// compression measurement reuses them below — zero agent calls,
	// the stream is already in memory.
	replayedToolUseCount := 0
	var parsedToolCalls []*dbpkg.ToolCall
	for _, line := range bytes.Split([]byte(rs.StreamText), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		tc, ok := parserpkg.ParseEvent(line)
		if ok && tc != nil {
			replayedToolUseCount++
			parsedToolCalls = append(parsedToolCalls, tc)
		}
	}
	parserRegressionPass := int64(replayedToolUseCount) == session.ToolCallCount

	// (c) Cost drift: re-aggregate tokens/cost via extractStreamMetrics
	// and compare to the original bench_runs row. Skipped entirely when
	// the stream has no bench_run parent (ktx-run --record-stream
	// captures) — there is no original cost to compare against, so
	// costDriftDetected stays false and the cost_drift envelope reports
	// the reduced "no original bench_run to compare against" shape.
	streamMetrics := extractStreamMetrics([]byte(rs.StreamText))
	costDriftDetected := false
	var replayCost *float64
	var replayTokensIn, replayTokensOut int64
	if streamMetrics.hasData {
		replayTokensIn = streamMetrics.tokensIn
		replayTokensOut = streamMetrics.tokensOut
		if streamMetrics.cost != nil {
			v := *streamMetrics.cost
			replayCost = &v
		}
	}
	if originalBR != nil {
		if originalBR.Cost != nil && replayCost != nil {
			costDriftDetected = *originalBR.Cost != *replayCost
		} else if originalBR.Cost != nil && replayCost == nil {
			costDriftDetected = true
		}
	}

	// Determine overall consistency.
	consistency := "PASS"
	if !integrityPass || !parserRegressionPass {
		consistency = "FAIL"
	} else if costDriftDetected {
		consistency = "DRIFT"
	}

	// (d) A/B compression measurement: run each recorded
	// tool_use output through compressPipeline (timeout=0, off-line)
	// and aggregate baseline_bytes (sum len original) vs
	// compressed_bytes (sum len compressed). Reuses compressPipeline
	// (DRY) so the failure gate, the anti-worsening gate, and the
	// conservative-on-failure invariants all apply identically to
	// the live hook path. Failure-gated
	// calls (is_error=true) count toward baseline_bytes only
	// (compressPipeline returns the original for them). Zero agent
	// calls — the recorded stream is already parsed above.
	compressionSection := measureReplayCompression(parsedToolCalls)

	// Build the output envelope. capture_source distinguishes the
	// two capture paths: "bench_run" (ktx bench run, originalBR is
	// non-nil) and "ktx_run" (ktx run --record-stream, originalBR is
	// nil — no bench_runs row to compare against).
	captureSource := "ktx_run"
	if originalBR != nil {
		captureSource = "bench_run"
	}
	envelope := map[string]any{
		"replay": map[string]any{
			"session_id":     rs.SessionID,
			"bench_run_id":   rs.BenchRunID,
			"stream_format":  rs.StreamFormat,
			"captured_at":    rs.CapturedAt,
			"capture_source": captureSource,
		},
		"integrity":          map[string]any{"pass": integrityPass, "recomputed_hash": recomputedHash, "stored_hash": rs.ContentHash},
		"parser_regression":  map[string]any{"pass": parserRegressionPass, "replayed_tool_use_count": replayedToolUseCount, "original_tool_call_count": session.ToolCallCount},
		"original_bench_run": originalBR,
		"compression":        compressionSection,
		"consistency":        consistency,
	}

	if originalBR == nil {
		// No bench_run to compare against — the stream was captured
		// via `ktx run --record-stream`. Report replay-derived
		// tokens/cost (if any) without an original to compare.
		envelope["cost_drift"] = map[string]any{
			"detected":          false,
			"comparison":        "unavailable",
			"reason":            "no original bench_run to compare against — session captured via ktx run --record-stream",
			"replay_tokens_in":  replayTokensIn,
			"replay_tokens_out": replayTokensOut,
			"replay_cost":       replayCost,
		}
	} else if costDriftDetected {
		delta := 0.0
		if replayCost != nil && originalBR.Cost != nil {
			delta = *replayCost - *originalBR.Cost
		}
		costDrift := map[string]any{
			"detected":            true,
			"replay_tokens_in":    replayTokensIn,
			"original_tokens_in":  originalBR.TokensIn,
			"replay_tokens_out":   replayTokensOut,
			"original_tokens_out": originalBR.TokensOut,
			"replay_cost":         replayCost,
			"original_cost":       originalBR.Cost,
			"delta":               delta,
		}
		envelope["cost_drift"] = costDrift
	} else {
		envelope["cost_drift"] = map[string]any{"detected": false}
	}

	switch format {
	case FormatText:
		fmt.Fprintf(stdout, "replay: %s (integrity=%v, parser=%v, cost_drift=%v)\n",
			consistency, integrityPass, parserRegressionPass, costDriftDetected)
		fmt.Fprintf(stdout, "session_id: %s\n", rs.SessionID)
		fmt.Fprintf(stdout, "bench_run_id: %s\n", rs.BenchRunID)
		fmt.Fprintf(stdout, "replayed_tool_use_count: %d\n", replayedToolUseCount)
		fmt.Fprintf(stdout, "original_tool_call_count: %d\n", session.ToolCallCount)
		// A/B compression summary.
		if baseline, ok := compressionSection["baseline_bytes"].(int64); ok {
			compressed, _ := compressionSection["compressed_bytes"].(int64)
			ratio, _ := compressionSection["ratio"].(float64)
			saved, _ := compressionSection["bytes_saved"].(int64)
			fmt.Fprintf(stdout, "compression: baseline=%d compressed=%d ratio=%.4f saved=%d bytes\n",
				baseline, compressed, ratio, saved)
		}
	default:
		if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
			printError(stderr, internalError("ktx bench run --replay: write JSON: %v", err), format)
			return ExitInternal
		}
	}

	if consistency == "FAIL" {
		printError(stderr, internalError("ktx bench run --replay: consistency=FAIL (integrity=%v, parser_regression=%v)", integrityPass, parserRegressionPass), format)
		return ExitInternal
	}
	return ExitOK
}

// measureReplayCompression runs each recorded tool_use output through
// compressPipeline (timeout=0, off-line) and aggregates an A/B
// comparison: baseline_bytes (sum of original output lengths) vs
// compressed_bytes (sum of compressed output lengths), plus the
// ratio (compressed/baseline), bytes_saved (baseline-compressed), and
// the configured pipeline (cfg.PipelineDefault as a []string).
//
// Failure-gated calls (is_error=true on the tool_call) are counted in
// baseline_bytes only — compressPipeline returns the original output
// for them (the pre-pipeline gate skips compression on failure), so
// their compressed_bytes contribution equals the original length (the
// pipeline did not help). The anti-worsening gate ensures
// compressed_bytes <= baseline per call, so the aggregate ratio is in
// [0.0, 1.0]. Zero agent calls: the measurement reuses the recorded
// stream's parsed tool_calls and compressPipeline's off-line path.
//
// stderr is io.Discard so stage warnings do not pollute the replay
// envelope — the numbers are the data; diagnostics belong to a direct
// `ktx compress` invocation. A broken ktx.toml surfaces per-call as a
// compressPipeline error; the call is counted as passthrough (no
// compression) so the measurement still completes.
func measureReplayCompression(toolCalls []*dbpkg.ToolCall) map[string]any {
	var baseline, compressed int64
	for _, tc := range toolCalls {
		baseline += int64(len(tc.Output))
		out, _, _, _, err := compressPipeline(tc, nil, io.Discard, false)
		if err != nil {
			// compressPipeline could not run (e.g. a broken
			// ktx.toml); count the original length — no
			// compression. The measurement still completes.
			compressed += int64(len(tc.Output))
			continue
		}
		compressed += int64(len(out))
	}

	ratio := 1.0
	if baseline > 0 {
		ratio = float64(compressed) / float64(baseline)
	}

	// The configured pipeline (what stages are in play). Empty when
	// no ktx.toml exists (the passthrough path) — matches the AC's
	// ratio=1.0, bytes_saved=0, pipeline=[] for the passthrough case.
	pipeline := []string{}
	if cfg, err := config.Load(); err == nil && cfg != nil && len(cfg.PipelineDefault) > 0 {
		pipeline = cfg.PipelineDefault
	}

	return map[string]any{
		"baseline_bytes":   baseline,
		"compressed_bytes": compressed,
		"ratio":            ratio,
		"bytes_saved":      baseline - compressed,
		"pipeline":         pipeline,
	}
}

// captureStreamForBenchRun persists the captured JSONL stream into the
// recorded_streams table (recording contract). Called by runBenchRun
// after InsertBenchRun and QueryBenchRun succeed, so bench_run_id and
// session_id are both known-valid. The streamBytes are the agent's stdout
// captured by runBenchRun (snapshotted before any scanner consumed the
// buffer). Returns nil on success; the caller soft-fails on error (stderr
// warning, bench run still exits 0).
func captureStreamForBenchRun(db *dbpkg.DB, sessionID, benchRunID string, streamBytes []byte) error {
	rsID, err := dbpkg.NewID()
	if err != nil {
		return fmt.Errorf("generate recorded_stream id: %w", err)
	}
	streamText := string(streamBytes)
	rs := &dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   benchRunID,
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	return db.InsertRecordedStream(rs)
}
