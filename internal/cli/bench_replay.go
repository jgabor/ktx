package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
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
	db, err := openDatabase(path)
	if err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
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
	parsedToolCalls := parseReplayToolCalls(rs.StreamText)
	replayedToolUseCount := len(parsedToolCalls)
	parserRegressionPass := int64(replayedToolUseCount) == session.ToolCallCount

	// (c) Cost drift: re-aggregate tokens/cost/cache via
	// extractStreamMetrics and compare to the original bench_runs row.
	// Every evidence field is compared: tokens_in, tokens_out, cost,
	// cache_read, cache_write. Nil-vs-explicit-zero on cache (and
	// cost) is a drift — absent ≠ emitted zero. Any mismatch produces
	// DRIFT. Skipped entirely when the stream has no bench_run parent
	// (ktx-run --record-stream captures) — there is no original to
	// compare against, so costDriftDetected stays false and the
	// cost_drift envelope reports the reduced "no original bench_run
	// to compare against" shape with replayed-side values only.
	streamMetrics := extractStreamMetrics([]byte(rs.StreamText))
	costDriftDetected := false
	var replayCost *float64
	var replayTokensIn, replayTokensOut int64
	var replayCacheRead, replayCacheWrite *int64
	if streamMetrics.hasData {
		if streamMetrics.tokensIn != nil {
			replayTokensIn = *streamMetrics.tokensIn
		}
		if streamMetrics.tokensOut != nil {
			replayTokensOut = *streamMetrics.tokensOut
		}
		if streamMetrics.cost != nil {
			v := *streamMetrics.cost
			replayCost = &v
		}
		if streamMetrics.cacheRead != nil {
			v := *streamMetrics.cacheRead
			replayCacheRead = &v
		}
		if streamMetrics.cacheWrite != nil {
			v := *streamMetrics.cacheWrite
			replayCacheWrite = &v
		}
	}
	if originalBR != nil {
		if replayTokensIn != originalBR.TokensIn ||
			replayTokensOut != originalBR.TokensOut ||
			!float64PtrEqual(replayCost, originalBR.Cost) ||
			!int64PtrEqual(replayCacheRead, originalBR.CacheRead) ||
			!int64PtrEqual(replayCacheWrite, originalBR.CacheWrite) {
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
	cfg, err := config.Load()
	if err != nil {
		printError(stderr, usageError("ktx bench run --replay: %v", err), format)
		return ExitUsage
	}
	compressionSection := measureReplayCompression(parsedToolCalls, cfg)

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
		// tokens/cost/cache (if any) without an original to compare.
		// Legacy keys (replay_tokens_in / replay_tokens_out /
		// replay_cost) are preserved for stream consumers; cache
		// fields use the replayed_* / original_* vocabulary.
		envelope["cost_drift"] = map[string]any{
			"detected":             false,
			"comparison":           "unavailable",
			"reason":               "no original bench_run to compare against — session captured via ktx run --record-stream",
			"replay_tokens_in":     replayTokensIn,
			"replay_tokens_out":    replayTokensOut,
			"replay_cost":          replayCost,
			"replayed_cache_read":  replayCacheRead,
			"replayed_cache_write": replayCacheWrite,
		}
	} else {
		// Always carry both replayed and original values so drift
		// is diagnosable even when detected=false (PASS). The delta
		// is the cost difference (negative = replay cheaper).
		// Legacy keys (replay_tokens_in / replay_tokens_out /
		// replay_cost, original_tokens_in / original_tokens_out /
		// original_cost) are preserved for stream consumers; cache
		// fields use the replayed_* / original_* vocabulary.
		delta := 0.0
		if replayCost != nil && originalBR.Cost != nil {
			delta = *replayCost - *originalBR.Cost
		}
		envelope["cost_drift"] = map[string]any{
			"detected":             costDriftDetected,
			"replay_tokens_in":     replayTokensIn,
			"original_tokens_in":   originalBR.TokensIn,
			"replay_tokens_out":    replayTokensOut,
			"original_tokens_out":  originalBR.TokensOut,
			"replay_cost":          replayCost,
			"original_cost":        originalBR.Cost,
			"delta":                delta,
			"replayed_cache_read":  replayCacheRead,
			"original_cache_read":  originalBR.CacheRead,
			"replayed_cache_write": replayCacheWrite,
			"original_cache_write": originalBR.CacheWrite,
		}
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
		if runs, ok := compressionSection["pipeline_runs"].([]replayPipelineRun); ok {
			for _, run := range runs {
				fmt.Fprintf(stdout, "pipeline_run: profile=%s route=%s calls=%d stages=%v\n",
					run.Profile, run.Route, run.Calls, run.Stages)
			}
		}
		if truncated, _ := compressionSection["pipeline_runs_truncated"].(bool); truncated {
			unreported, _ := compressionSection["pipeline_runs_unreported_calls"].(int64)
			fmt.Fprintf(stdout, "pipeline_runs_truncated: true unreported_calls=%d\n", unreported)
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

type replayPipelineRun struct {
	Profile string   `json:"profile"`
	Route   string   `json:"route"`
	Stages  []string `json:"stages"`
	Calls   int64    `json:"calls"`
}

const replayPipelineRunLimit = 100

// measureReplayCompression runs each recorded tool_use output against one
// caller-supplied configuration snapshot through the same resolved pipeline
// path as hooks and explicit compression. It aggregates an A/B
// comparison: baseline_bytes (sum of original output lengths) vs
// compressed_bytes (sum of compressed output lengths), plus the
// ratio (compressed/baseline), bytes_saved (baseline-compressed), the legacy
// configured-default pipeline projection, and the authoritative profile/route/
// retained-stage runs.
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
// stderr is io.Discard so stage warnings do not pollute the replay envelope.
// The caller rejects an invalid configuration before measurement begins.
func measureReplayCompression(toolCalls []*dbpkg.ToolCall, cfg *config.Config) map[string]any {
	if cfg == nil {
		cfg = &config.Config{}
	}
	var baseline, compressed int64
	runs := []replayPipelineRun{}
	runIndexes := make(map[string]int)
	var unreportedRunCalls int64
	for _, tc := range toolCalls {
		baseline += int64(len(tc.Output))
		execution, err := compressPipelineWithConfig(tc, cfg, nil, io.Discard, false)
		if err != nil {
			compressed += int64(len(tc.Output))
			continue
		}
		compressed += int64(len(execution.output))

		key := execution.pipelineRecord()
		if i, ok := runIndexes[key]; ok {
			runs[i].Calls++
			continue
		}
		if len(runs) == replayPipelineRunLimit {
			unreportedRunCalls++
			continue
		}
		runIndexes[key] = len(runs)
		runs = append(runs, replayPipelineRun{
			Profile: execution.profile,
			Route:   execution.route,
			Stages:  append([]string{}, execution.stages...),
			Calls:   1,
		})
	}

	ratio := 1.0
	if baseline > 0 {
		ratio = float64(compressed) / float64(baseline)
	}

	// Preserve the legacy configured-default field for existing database replay
	// consumers. pipeline_runs is the authoritative execution report.
	configuredDefault := []string{}
	configuredDefault = append(configuredDefault, cfg.PipelineDefault...)

	return map[string]any{
		"baseline_bytes":                 baseline,
		"compressed_bytes":               compressed,
		"ratio":                          ratio,
		"bytes_saved":                    baseline - compressed,
		"pipeline":                       configuredDefault,
		"pipeline_runs":                  runs,
		"pipeline_runs_truncated":        unreportedRunCalls > 0,
		"pipeline_runs_unreported_calls": unreportedRunCalls,
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

// float64PtrEqual reports whether two nullable float64 pointers hold
// equal values. nil == nil (both absent); nil ≠ non-nil (absent vs
// emitted value) is a mismatch — the absent-vs-zero distinction that
// cost drift detection relies on.
func float64PtrEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// int64PtrEqual reports whether two nullable int64 pointers hold equal
// values. nil == nil (both absent); nil ≠ non-nil (absent vs emitted
// zero) is a mismatch — the absent-vs-zero distinction that cache
// drift detection relies on.
func int64PtrEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
