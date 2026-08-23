package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/durablefile"
	parserpkg "github.com/jgabor/ktx/internal/parser"
	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/pipeline/stages"
)

func replayFixturePipelineV1() []string {
	return []string{"strip-ansi", "dedup-blank", "normalize"}
}

type replayFixtureSource struct {
	streamSessionID string
	streamBenchID   sql.NullString
	streamText      string
	streamHash      string
	streamFormat    string
	sessionID       sql.NullString
	toolCallCount   sql.NullInt64
	benchID         sql.NullString
	agent           sql.NullString
	model           sql.NullString
	task            sql.NullString
	score           sql.NullFloat64
	tokensIn        sql.NullInt64
	tokensOut       sql.NullInt64
	cost            sql.NullFloat64
	methodology     sql.NullString
	cacheRead       sql.NullInt64
	cacheWrite      sql.NullInt64
}

func invalidReplayFixtureSource(message string, args ...any) *Error {
	err := usageError("ktx bench export: "+message, args...)
	err.Extra = map[string]any{
		"failure_class": "invalid-replay-fixture-source",
		"correction":    "version 1 export requires one benchmark-parented recorded stream whose session, benchmark, hash, parser count, and usage evidence are consistent",
	}
	return err
}

func invalidReplayFixtureDestination(sourcePath string) *Error {
	return sourceDatabaseDestinationError("ktx bench export", "invalid-replay-fixture-destination", sourcePath)
}

func runBenchExport(args []string, opts Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ktx bench export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output string
	fs.StringVar(&output, "output", "-", "write the canonical fixture to this file, or - for stdout")
	if err := fs.Parse(args); err != nil {
		printError(stderr, usageError("ktx bench export: %v", err), opts.Format)
		return ExitUsage
	}
	if fs.NArg() != 1 {
		printError(stderr, usageError("ktx bench export: usage: ktx bench export [--output <path|->] <session-id>"), opts.Format)
		return ExitUsage
	}
	if output == "" {
		printError(stderr, usageError("ktx bench export: --output must be a file path or - for stdout"), opts.Format)
		return ExitUsage
	}

	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx bench export: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	aliasesSource, err := destinationAliasesSource(path, output)
	if err != nil {
		printError(stderr, internalError("ktx bench export: compare output with source database: %v", err), opts.Format)
		return ExitInternal
	}
	if aliasesSource {
		printError(stderr, invalidReplayFixtureDestination(path), opts.Format)
		return ExitUsage
	}
	database, err := dbpkg.OpenQuery(path)
	if err != nil {
		printError(stderr, internalError("ktx bench export: open source database read-only: %v", err), opts.Format)
		return ExitInternal
	}
	defer database.Close()

	fixtureBytes, err := exportReplayFixture(database.DB(), fs.Arg(0))
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	if output == "-" {
		if _, err := stdout.Write(fixtureBytes); err != nil {
			printError(stderr, internalError("ktx bench export: write stdout: %v", err), opts.Format)
			return ExitInternal
		}
		return ExitOK
	}
	if err := writeFileAtomically(output, fixtureBytes); err != nil {
		printError(stderr, internalError("ktx bench export: write %q: %v", output, err), opts.Format)
		return ExitInternal
	}
	fmt.Fprintf(stderr, "ktx: exported replay fixture to %s\n", output)
	return ExitOK
}

func exportReplayFixture(database *sql.DB, sessionID string) ([]byte, error) {
	source, err := loadReplayFixtureSource(database, sessionID)
	if err != nil {
		return nil, err
	}
	fixture, err := replayFixtureFromSource(source)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		return nil, internalError("ktx bench export: encode fixture: %v", err)
	}
	data = append(data, '\n')
	if _, err := decodeReplayFixture(bytes.NewReader(data)); err != nil {
		return nil, internalError("ktx bench export: generated fixture failed validation: %v", err)
	}
	return data, nil
}

func loadReplayFixtureSource(database *sql.DB, sessionID string) (*replayFixtureSource, error) {
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, internalError("ktx bench export: begin read-only snapshot: %v", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT rs.session_id, rs.bench_run_id, rs.stream_text, rs.content_hash, rs.stream_format,
		       s.id, s.tool_call_count,
		       br.id, br.agent, br.model, br.task, br.score, br.tokens_in, br.tokens_out,
		       br.cost, br.methodology_version, br.cache_read, br.cache_write
		FROM recorded_streams AS rs
		LEFT JOIN sessions AS s ON s.id = rs.session_id
		LEFT JOIN bench_runs AS br ON br.id = rs.bench_run_id
		WHERE rs.session_id = ?`, sessionID)
	if err != nil {
		return nil, invalidReplayFixtureSource("source database cannot provide supported benchmark-parented evidence: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, internalError("ktx bench export: query source snapshot: %v", err)
		}
		return nil, notFoundError("ktx bench export: no recorded stream for session %q", sessionID)
	}

	var source replayFixtureSource
	if err := rows.Scan(
		&source.streamSessionID, &source.streamBenchID, &source.streamText, &source.streamHash, &source.streamFormat,
		&source.sessionID, &source.toolCallCount,
		&source.benchID, &source.agent, &source.model, &source.task, &source.score, &source.tokensIn, &source.tokensOut,
		&source.cost, &source.methodology, &source.cacheRead, &source.cacheWrite,
	); err != nil {
		return nil, invalidReplayFixtureSource("source evidence is malformed: %v", err)
	}
	if rows.Next() {
		return nil, invalidReplayFixtureSource("session %q has multiple recorded streams; version 1 requires exactly one authoritative benchmark-parented stream", sessionID)
	}
	if err := rows.Err(); err != nil {
		return nil, internalError("ktx bench export: query source snapshot: %v", err)
	}
	if err := rows.Close(); err != nil {
		return nil, internalError("ktx bench export: close source query: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, internalError("ktx bench export: close source snapshot: %v", err)
	}
	return &source, nil
}

func replayFixtureFromSource(source *replayFixtureSource) (*replayFixture, error) {
	if !source.sessionID.Valid || source.sessionID.String != source.streamSessionID {
		return nil, invalidReplayFixtureSource("recorded stream has an inconsistent session link; version 1 requires a linked session comparison")
	}
	if !source.streamBenchID.Valid || source.streamBenchID.String == "" {
		return nil, invalidReplayFixtureSource("recorded stream is parentless; version 1 supports only benchmark-parented sources")
	}
	if !source.benchID.Valid || source.benchID.String != source.streamBenchID.String {
		return nil, invalidReplayFixtureSource("recorded stream has an inconsistent benchmark link; version 1 requires an existing benchmark parent")
	}
	if source.streamFormat != "opencode-jsonl-1" {
		return nil, invalidReplayFixtureSource("stream format %q is unsupported; version 1 requires opencode-jsonl-1", source.streamFormat)
	}
	if got := dbpkg.ContentHash(source.streamText); got != source.streamHash {
		return nil, invalidReplayFixtureSource("stream content hash mismatch: computed %s, source has %s", got, source.streamHash)
	}
	if !source.toolCallCount.Valid || source.toolCallCount.Int64 < 0 {
		return nil, invalidReplayFixtureSource("linked session has invalid parser comparison evidence")
	}

	toolCalls := parseReplayToolCalls(source.streamText)
	if int64(len(toolCalls)) != source.toolCallCount.Int64 {
		return nil, invalidReplayFixtureSource("parser evidence mismatch: stream has %d tool uses, linked session records %d", len(toolCalls), source.toolCallCount.Int64)
	}
	metrics := extractStreamMetrics([]byte(source.streamText))
	if !source.agent.Valid || source.agent.String == "" || !source.model.Valid || source.model.String == "" ||
		!source.task.Valid || source.task.String == "" || !source.methodology.Valid || source.methodology.String == "" ||
		!source.score.Valid || !finite(source.score.Float64) || !source.tokensIn.Valid || !source.tokensOut.Valid {
		return nil, invalidReplayFixtureSource("benchmark parent is missing required methodology, agent, model, task, score, or token evidence")
	}
	if nullableInt64OrZero(metrics.tokensIn) != source.tokensIn.Int64 || nullableInt64OrZero(metrics.tokensOut) != source.tokensOut.Int64 ||
		!nullableFloat64Equal(metrics.cost, source.cost) || !nullableInt64Equal(metrics.cacheRead, source.cacheRead) || !nullableInt64Equal(metrics.cacheWrite, source.cacheWrite) {
		return nil, invalidReplayFixtureSource("benchmark usage linkage mismatch: stream-derived tokens, cost, or cache evidence differs from its benchmark parent")
	}

	version := 1
	stream := source.streamText
	toolUseCount := source.toolCallCount.Int64
	score := source.score.Float64
	compression := measureReplayFixtureCompression(toolCalls)
	return &replayFixture{
		Version: &version,
		Benchmark: &replayFixtureBenchmark{
			MethodologyVersion: source.methodology.String, Agent: source.agent.String,
			Model: source.model.String, Task: source.task.String, Score: &score,
		},
		Stream: &replayFixtureStream{Format: source.streamFormat, Content: &stream, SHA256: source.streamHash},
		Expected: &replayFixtureExpectations{
			Parser: &replayFixtureParser{ToolUseCount: &toolUseCount},
			Usage: &replayFixtureUsage{
				TokensIn:   optionalNullable[int64]{Present: true, Value: cloneInt64(metrics.tokensIn)},
				TokensOut:  optionalNullable[int64]{Present: true, Value: cloneInt64(metrics.tokensOut)},
				Cost:       optionalNullable[float64]{Present: true, Value: cloneFloat64(metrics.cost)},
				CacheRead:  optionalNullable[int64]{Present: true, Value: cloneInt64(metrics.cacheRead)},
				CacheWrite: optionalNullable[int64]{Present: true, Value: cloneInt64(metrics.cacheWrite)},
			},
			Compression: compression,
		},
	}, nil
}

func parseReplayToolCalls(stream string) []*dbpkg.ToolCall {
	var calls []*dbpkg.ToolCall
	for _, line := range bytes.Split([]byte(stream), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if call, ok := parserpkg.ParseEvent(line); ok && call != nil {
			calls = append(calls, call)
		}
	}
	return calls
}

func measureReplayFixtureCompression(toolCalls []*dbpkg.ToolCall) *replayFixtureCompression {
	registry := pipeline.NewRegistry()
	stages.RegisterBuiltins(registry)
	pipelineIdentity := replayFixturePipelineV1()
	runner := pipeline.Runner{Registry: registry, Pipeline: pipelineIdentity, Warn: io.Discard}
	var baseline, compressed int64
	for _, call := range toolCalls {
		baseline += int64(len(call.Output))
		if call.IsError != nil && *call.IsError {
			compressed += int64(len(call.Output))
			continue
		}
		compressed += int64(len(runner.Run([]byte(call.Output)).Out))
	}
	ratio := 1.0
	if baseline > 0 {
		ratio = float64(compressed) / float64(baseline)
	}
	saved := baseline - compressed
	return &replayFixtureCompression{
		Pipeline: pipelineIdentity, BaselineBytes: &baseline, CompressedBytes: &compressed,
		BytesSaved: &saved, Ratio: &ratio,
	}
}

func nullableInt64OrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func nullableInt64Equal(value *int64, source sql.NullInt64) bool {
	return (value == nil && !source.Valid) || (value != nil && source.Valid && *value == source.Int64)
}

func nullableFloat64Equal(value *float64, source sql.NullFloat64) bool {
	return (value == nil && !source.Valid) || (value != nil && source.Valid && *value == source.Float64)
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func writeFileAtomically(path string, data []byte) error {
	return durablefile.Write(path, data, 0o644)
}
