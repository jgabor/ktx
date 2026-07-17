package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
	methodologypkg "github.com/jgabor/ktx/internal/methodology"
	"github.com/jgabor/ktx/internal/usage"
)

// methodology is the parsed subset of a published methodology the bench
// runner needs. The full YAML schema has many fields (scoring.epsilon,
// trials.non_determinism_handling, etc.) that are not consumed at
// run-time — v1 only needs the version string, the scoring function
// name, the task fields (identifier + prompt + expected_output), and
// the model fields (id + provider). Adding more fields later extends
// the parser, not the struct.
type methodology = methodologypkg.Spec

func loadMethodology(version string) (*methodology, string, error) {
	return methodologypkg.Load(version)
}

// runBenchRun is the headline producer. The flow:
//
//  1. Parse positional args (agent, model, task).
//  2. Load the embedded methodology; bail with ExitNotFound when the
//     requested version is not published.
//  3. Validate that the supplied (agent, model, task) match the
//     methodology's pinned inputs. The methodology definition is the
//     reproducibility contract — the CLI rejects a run whose inputs
//     drift from the published methodology. (v1 ships single-task;
//     the (agent, model, task) triple is pinned in the
//     methodology file. A drift is treated as a usage error so the
//     user knows the headline they would produce does not match the
//     published one.)
//  4. Open the database (or fall through to internal db.Open),
//     pre-flight check for the bench_runs table presence.
//  5. Invoke `ktx run <agent> <task.prompt>` as a subprocess. The
//     subprocess's stdout is captured for scoring; its stderr is
//     captured for the wrapper run summary. KTX_SESSION_ID is
//     exported so any runtime hook noops.
//  6. Score the agent's output (last text event's content, falling
//     back to the last non-empty line of stdout) against
//     task.expected_output.
//  7. Find the wrapper's session in the database (most recent session
//     whose agent matches and is not older than the wrapper's start
//     time), aggregate tokens/cost/model from tool_calls.
//  8. Derivation fills bench_runs.cost when the tool_calls'
//     SUM(cost) is NULL but tokens + model are present and the cache
//     has the price.
//  9. Insert the bench_runs row with all 14 columns populated.
//  10. Print the headline JSON envelope to stdout and a one-line
//     summary to stderr (wrapper convention).
//
// Exit codes: 0 on success, 2 (not-found) on missing methodology or
// task definition, 4 (internal) on database / subprocess failures.
func runBenchRun(args []string, opts Options, stdout, stderr io.Writer) int {
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}
	if format != FormatJSON && format != FormatText {
		printError(stderr, usageError("ktx bench run: invalid --format %q: want %q or %q", format, FormatJSON, FormatText), format)
		return ExitUsage
	}

	fs := flag.NewFlagSet("ktx bench run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var replaySessionID string
	var replayFile string
	var methodologyVersion string
	fs.StringVar(&replaySessionID, "replay", "", "replay a recorded session by session-id; makes (agent, model, task) optional")
	fs.StringVar(&replayFile, "replay-file", "", "evaluate a portable replay fixture offline by explicit file path")
	fs.StringVar(&methodologyVersion, "methodology", "", "methodology version (e.g., 'v1', 'v2-tooluse'); defaults to v1")
	if err := fs.Parse(rest); err != nil {
		printError(stderr, usageError("ktx bench run: %v", err), format)
		return ExitUsage
	}
	rest = fs.Args()
	if replaySessionID != "" && replayFile != "" {
		printError(stderr, usageError("ktx bench run: --replay and --replay-file are mutually exclusive; choose a session-id or a fixture path"), format)
		return ExitUsage
	}

	// --replay dispatches to the replay path before positional-argument
	// validation. The
	// (agent, model, task) triple is read from the referenced
	// bench_runs row, not from the CLI.
	if replaySessionID != "" {
		if len(rest) > 0 {
			printError(stderr, usageError("ktx bench run --replay does not accept positional args; the (agent, model, task) triple is read from the referenced bench_runs row"), format)
			return ExitUsage
		}
		return runBenchReplay(replaySessionID, opts, stdout, stderr, format)
	}
	if replayFile != "" {
		if len(rest) > 0 {
			printError(stderr, usageError("ktx bench run --replay-file does not accept positional args; pass the fixture path as the flag value"), format)
			return ExitUsage
		}
		return runBenchReplayFile(replayFile, stdout, stderr, format)
	}

	if len(rest) != 3 {
		printError(stderr, usageError("ktx bench run: usage: ktx bench run <agent> <model> <task>"), format)
		return ExitUsage
	}
	agentArg, modelArg, taskArg := rest[0], rest[1], rest[2]

	_, m, exitCode := loadMethodologyOrFail(format, methodologyVersion, stderr)
	if exitCode != ExitOK {
		return exitCode
	}

	// Validate the CLI inputs against the methodology's pinned triple.
	// A drift is a usage error so the user sees the headline does not
	// match the published methodology (reproducibility contract).
	if agentArg != "opencode" { // v1: agent is the binary name; methodology pins opencode 1.17.8
		printError(stderr, usageError("ktx bench run: agent %q does not match methodology pinned agent %q", agentArg, "opencode"), format)
		return ExitUsage
	}
	if modelArg != m.ModelID {
		printError(stderr, usageError("ktx bench run: model %q does not match methodology pinned model %q", modelArg, m.ModelID), format)
		return ExitUsage
	}
	if taskArg != m.TaskIdentifier {
		printError(stderr, usageError("ktx bench run: task %q does not match methodology pinned task %q", taskArg, m.TaskIdentifier), format)
		return ExitUsage
	}

	// Open the database. The dbpkg.Open call applies the bench_runs
	// migration if the database predates the bench_runs table (forward-fix).
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx bench run: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := dbpkg.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx bench run: open database: %v", err), format)
		return ExitInternal
	}
	defer db.Close()

	// Pre-flight: verify the bench_runs table exists. Doctor would
	// surface this as a warning; bench run surfaces it as a hard error
	// because the run cannot proceed without the table. Missing inputs
	// are a hard failure.
	hasTable, err := db.HasBenchRunsTable()
	if err != nil {
		printError(stderr, internalError("ktx bench run: bench_runs check: %v", err), format)
		return ExitInternal
	}
	if !hasTable {
		printError(stderr, notFoundError("ktx bench run: bench_runs table is missing (old database; run any ktx command to apply the migration)"), format)
		return ExitNotFound
	}

	// Invoke the wrapper subprocess. The wrapper's stdout (agent's
	// output, transparent proxy) is captured for scoring; its
	// stderr (the run summary) is captured for diagnostics. We do NOT
	// pass the wrapper's stdout through to bench run's stdout —
	// bench run's stdout is the headline JSON envelope, and the
	// user explicitly asked for the headline (not the agent's
	// response). The agent's response is queryable via `ktx get` on
	// the underlying tool_calls.
	wrapperStart := time.Now().UTC()
	exe, err := os.Executable()
	if err != nil {
		printError(stderr, internalError("ktx bench run: resolve ktx binary: %v", err), format)
		return ExitInternal
	}
	modelSpec := m.ModelProvider + "/" + m.ModelID
	cmd := exec.Command(exe, "run", agentArg, "run", "--format", "json", "-m", modelSpec, m.TaskPrompt)
	var agentStdout, agentStderr bytes.Buffer
	cmd.Stdout = &agentStdout
	cmd.Stderr = &agentStderr
	cmd.Env = os.Environ() // KTX_SESSION_ID is exported by the wrapper itself.
	if err := cmd.Run(); err != nil {
		// A non-zero exit from the wrapper is informational, not a
		// bench run failure — the agent may have failed to run but
		// the bench infrastructure still persists a row with score=0
		// and the partial aggregation. The headline is meaningful
		// either way. Surface the wrapper's stderr in the bench
		// run's stderr so the user can diagnose.
		fmt.Fprintf(stderr, "ktx: warning: wrapper exited with error: %v\n", err)
		if agentStderr.Len() > 0 {
			fmt.Fprintf(stderr, "--- wrapper stderr ---\n%s--- end wrapper stderr ---\n", agentStderr.String())
		}
	}

	// Find the wrapper's session. The wrapper creates a session for
	// the agent invocation; we find it by listing sessions and
	// matching the most-recent session whose started_at is after our
	// wrapperStart. This assumes bench run is the only concurrent writer to
	// the benchmark database.
	sessionID, agentName, err := findWrapperSession(db, agentArg, wrapperStart)
	if err != nil {
		printError(stderr, internalError("ktx bench run: find wrapper session: %v", err), format)
		return ExitInternal
	}
	if sessionID == "" {
		// Wrapper did not create a session — most likely the wrapper
		// failed before reaching OpenNamedSession (e.g. spawn error
		// for a non-existent agent). Surface a clear error so the
		// user can fix the agent path.
		printError(stderr, internalError("ktx bench run: wrapper did not create a session; check that agent %q is on PATH", agentArg), format)
		return ExitInternal
	}

	// Aggregate tokens/cost/model from the wrapper's tool_calls.
	tokensIn, tokensOut, sumCost, aggModel, aggProvider, err := db.SummarizeBenchToolCalls(sessionID)
	if err != nil {
		printError(stderr, internalError("ktx bench run: aggregate tool_calls: %v", err), format)
		return ExitInternal
	}
	// Snapshot the agent's stdout BEFORE any scanner reads it.
	// extractAgentOutput and extractStreamMetrics both consume the
	// bytes via bufio.Scanner; a *bytes.Buffer's read offset advances
	// to EOF on the first scan, so the second scanner would see
	// no bytes. Snapshotting once keeps both extractions faithful to
	// the captured stream.
	agentStdoutBytes := agentStdout.Bytes()
	streamMetrics := extractStreamMetrics(agentStdoutBytes)
	// OpenCode 1.17.8 step_finish carries tokens/cost on the
	// stream's step-aggregate event, NOT on the tool_call rows the
	// wrapper persists from tool_use events. SummarizeBenchToolCalls
	// returns zeros / NULL in that gap; streamMetrics — when it has
	// seen any step_finish events — is the authoritative fallback
	// for filling the bench_runs row's tokens/cost fields.
	if streamMetrics.hasData {
		if tokensIn == 0 && streamMetrics.tokensIn != nil {
			tokensIn = *streamMetrics.tokensIn
		}
		if tokensOut == 0 && streamMetrics.tokensOut != nil {
			tokensOut = *streamMetrics.tokensOut
		}
		// The complete stream value is the runtime-reported cost for
		// the model requests and therefore wins even when it is zero.
		// Tool-call aggregation is only a fallback for runtimes without
		// step-level cost evidence.
		if streamMetrics.cost != nil {
			v := *streamMetrics.cost
			sumCost = &v
		}
	}
	// Prefer the wrapper's tool_calls values when non-empty; fall
	// back to the methodology-pinned model and the user-supplied
	// agent name. The bench_runs row is the headline record, not a
	// re-summary of the tool_calls.
	if aggModel == "" {
		if streamMetrics.model != "" {
			aggModel = streamMetrics.model
		} else {
			aggModel = m.ModelID
		}
	}
	if aggProvider == "" {
		if streamMetrics.provider != "" {
			aggProvider = streamMetrics.provider
		} else {
			aggProvider = m.ModelProvider
		}
	}
	_ = agentName // agentName is the wrapper's session.agent; not needed for the bench row.

	// Score the agent's output. v1 = exact_match against expected.
	actualOutput := extractAgentOutput(agentStdoutBytes)
	score := scoreExactMatch(actualOutput, m.TaskExpectedOutput)

	// Build the BenchRun. The Cost pointer is populated from the
	// SUM(cost) of the underlying tool_calls when present; otherwise
	// it stays nil and deriveBenchCost fills it from the cache at
	// write time.
	br := &dbpkg.BenchRun{
		ID:                 "",                           // populated below
		Agent:              aggProvider + "/" + aggModel, // bench_runs stores provider/model as the agent identifier
		Model:              aggModel,
		Task:               m.TaskIdentifier,
		Score:              score,
		TokensIn:           tokensIn,
		TokensOut:          tokensOut,
		Cost:               sumCost,
		MethodologyVersion: m.Version,
		Timestamp:          time.Now().UTC().Unix(),
	}
	if streamMetrics.hasData && !streamMetrics.completeTokens() {
		br.SkipEstimatedAPICost = true
	}
	// streamMetrics carries cache telemetry as nullable pointers
	// (absent stays nil; emitted zero is non-nil). The bench_runs
	// row replicates them verbatim — no fall-through to tool_calls
	// aggregation, which has no cache columns.
	if streamMetrics.cacheRead != nil {
		v := *streamMetrics.cacheRead
		br.CacheRead = &v
	}
	if streamMetrics.cacheWrite != nil {
		v := *streamMetrics.cacheWrite
		br.CacheWrite = &v
	}
	if streamMetrics.reasoning != nil {
		v := *streamMetrics.reasoning
		br.Reasoning = &v
	}
	if br.Agent == "" || br.Agent == "/" {
		// Fallback when neither tool_calls nor the methodology
		// supplied values: the bench row records the CLI-supplied
		// agent name verbatim.
		br.Agent = agentArg
	}

	id, err := dbpkg.NewID()
	if err != nil {
		printError(stderr, internalError("ktx bench run: generate id: %v", err), format)
		return ExitInternal
	}
	br.ID = id

	if err := db.InsertBenchRun(br); err != nil {
		printError(stderr, internalError("ktx bench run: persist bench_run: %v", err), format)
		return ExitInternal
	}

	// Re-fetch the persisted row so the output reflects the
	// post-derivation cost (the in-memory br.Cost pointer may have
	// been filled by deriveBenchCost before the INSERT, but the
	// stored row is the source of truth).
	persisted, err := db.QueryBenchRun(id)
	if err != nil {
		printError(stderr, internalError("ktx bench run: refetch bench_run: %v", err), format)
		return ExitInternal
	}

	// Recording contract: persist the captured JSONL stream so
	// `ktx bench run --replay <session-id>` can re-parse it later
	// without re-invoking the agent. The capture is non-fatal: if it
	// fails (e.g. FK rejection, disk full), a stderr warning is
	// emitted and the bench run still exits 0 with the headline —
	// --replay simply won't be available for this session.
	// agentStdoutBytes was snapshotted at line 1049 before any
	// scanner consumed the buffer; sessionID is from findWrapperSession.
	if err := captureStreamForBenchRun(db, sessionID, persisted.ID, agentStdoutBytes); err != nil {
		fmt.Fprintf(stderr, "ktx: warning: failed to record stream for --replay: %v\n", err)
	}

	// Compute the headline. cost_per_correct = cost_USD /
	// max(score, 1e-9). When score=0 the headline is a very large
	// number — the "cost is the contract made loud" signal.
	headline := computeHeadline(persisted.Cost, persisted.Score)

	// JSON envelope per the output block: {headline, rows,
	// methodology_version}. One row for `bench run` (the just-inserted
	// bench_run); `rows` is an array for `bench report`'s
	// multi-row view.
	envelope := map[string]any{
		"headline":            headline,
		"rows":                []dbpkg.BenchRun{persisted},
		"methodology_version": persisted.MethodologyVersion,
	}

	switch format {
	case FormatText:
		// Text rendering: one-line headline with methodology
		// reference, plus a key-value summary of the row.
		fmt.Fprintf(stdout, "headline: %.6f USD/correct (methodology %s)\n", headline, persisted.MethodologyVersion)
		fmt.Fprintf(stdout, "row id: %s\n", persisted.ID)
		fmt.Fprintf(stdout, "agent: %s\n", persisted.Agent)
		fmt.Fprintf(stdout, "model: %s\n", persisted.Model)
		fmt.Fprintf(stdout, "task: %s\n", persisted.Task)
		fmt.Fprintf(stdout, "score: %.4f\n", persisted.Score)
		if persisted.Cost != nil {
			fmt.Fprintf(stdout, "cost: %.6f\n", *persisted.Cost)
		} else {
			fmt.Fprintln(stdout, "cost: null")
		}
		fmt.Fprintf(stdout, "tokens_in: %d\n", persisted.TokensIn)
		fmt.Fprintf(stdout, "tokens_out: %d\n", persisted.TokensOut)
	default:
		// JSON default. The envelope is the output shape.
		if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
			printError(stderr, internalError("ktx bench run: write JSON: %v", err), format)
			return ExitInternal
		}
	}

	// Wrapper convention: one-line summary to stderr. The summary
	// carries the headline and methodology reference so a glance at
	// stderr surfaces the headline without parsing JSON.
	costStr := "null"
	if persisted.Cost != nil {
		costStr = fmt.Sprintf("%.6f", *persisted.Cost)
	}
	fmt.Fprintf(stderr, "{\"summary\":{\"bench_run\":%q,\"methodology\":%q,\"headline\":%.6f,\"score\":%.4f,\"cost\":%s,\"tokens_in\":%d,\"tokens_out\":%d}}\n",
		persisted.ID, persisted.MethodologyVersion, headline, persisted.Score, costStr, persisted.TokensIn, persisted.TokensOut)

	return ExitOK
}

// loadMethodologyOrFail wraps loadMethodology with the CLI error
// envelope. Returns (path, methodology, exit_code). The exit code is
// non-zero when load fails; the caller propagates it.
func loadMethodologyOrFail(format, version string, stderr io.Writer) (string, *methodology, int) {
	m, path, err := loadMethodology(version)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			printError(stderr, notFoundError("ktx bench run: methodology %q is not embedded", path), format)
			return path, nil, ExitNotFound
		default:
			printError(stderr, internalError("ktx bench run: %v", err), format)
			return path, nil, ExitInternal
		}
	}
	return path, m, ExitOK
}

// findWrapperSession locates the wrapper's session in the database.
// The wrapper creates a session for each invocation; bench run finds
// it by listing sessions and matching the most recent whose agent
// matches agentArg and whose started_at is on or after wrapperStart.
//
// The match assumes the wrapper is the only writer to the benchmark database.
func findWrapperSession(db *dbpkg.DB, agentArg string, wrapperStart time.Time) (sessionID, agentName string, err error) {
	sessions, err := db.ListSessions()
	if err != nil {
		return "", "", err
	}
	// Pick the most recent session whose started_at is >= wrapperStart
	// (or close enough) and whose agent matches the binary name. The
	// started_at field is an RFC3339Nano string; we compare by string
	// ordering (RFC3339Nano is lexicographically sortable).
	startStr := wrapperStart.Format("2006-01-02T15:04:05.000000000Z07:00")
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		if s.StartedAt < startStr {
			continue
		}
		// The wrapper stores the binary name as the agent field;
		// match it to the CLI-supplied agentArg.
		if s.Agent != agentArg {
			continue
		}
		return s.ID, s.Agent, nil
	}
	// Fallback: most-recent session regardless of started_at (handles
	// clock skew where the wrapper started before bench run's
	// wrapperStart observation).
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		if s.Agent == agentArg {
			return s.ID, s.Agent, nil
		}
	}
	return "", "", nil
}

// extractAgentOutput returns the agent's final response from the
// captured stdout. The wrapper's stdout is the agent's verbatim
// output (transparent proxy); bench run captures it for scoring.
//
// The extractor handles two shapes:
//
//  1. JSONL events: each line is a JSON object with a "type" field.
//     For OpenCode 1.17.8 the answer lives at part.text on the final
//     `text` event (see tools/bench/fixtures/opencode-tool-use.jsonl
//     line 5: {"type":"text",...,"part":{"type":"text","text":...}}).
//     The earlier top-level `content` alias is preserved as a
//     fallback for the flat-fallback shape and for tests that
//     pre-date the OpenCode fixture.
//  2. Plain text: each line is just text. The last non-empty line
//     is the agent's response.
//
// The agent's response is then compared against the methodology's
// expected_output via exact_match scoring (v1).
//
// The input is a []byte snapshot taken before any scanner reads
// the buffer; once a bufio.Scanner has advanced a *bytes.Buffer's
// read offset to EOF, a second pass over the same buffer would see
// no bytes. The caller therefore snapshots agentStdout.Bytes() once
// and passes the snapshot to both extractAgentOutput and
// extractStreamMetrics so neither call invalidates the other.
func extractAgentOutput(stdout []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var lastText string
	var lastNonEmpty string
	for scanner.Scan() {
		line := scanner.Bytes()
		text := strings.TrimRight(string(line), "\r")
		if text == "" {
			continue
		}
		lastNonEmpty = text
		// Try to parse as JSON to detect a text event.
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(line, &probe); err == nil {
			var eventType string
			_ = json.Unmarshal(probe["type"], &eventType)
			if eventType == "text" {
				// OpenCode nests the answer under part.text;
				// the flat-fallback shape uses top-level content.
				var content string
				if raw := probe["content"]; len(raw) > 0 {
					_ = json.Unmarshal(raw, &content)
				}
				if content == "" && len(probe["part"]) > 0 {
					var part map[string]json.RawMessage
					if err := json.Unmarshal(probe["part"], &part); err == nil {
						for _, k := range []string{"text", "content"} {
							if raw := part[k]; len(raw) > 0 {
								if err := json.Unmarshal(raw, &content); err == nil && content != "" {
									break
								}
							}
						}
					}
				}
				if content != "" {
					lastText = content
				}
			}
		}
	}
	if lastText != "" {
		return lastText
	}
	return lastNonEmpty
}

// scoreExactMatch is the v1 scorer (scoring.function = exact_match).
// Returns 1.0 when actual equals expected after whitespace trimming,
// 0.0 otherwise. The comparison is byte-exact post-trim; the v1
// methodology documents that "temperature>0 runs are not bit-
// reproducible; exact-match scoring will reject any token-level variance."
func scoreExactMatch(actual, expected string) float64 {
	if strings.TrimSpace(actual) == strings.TrimSpace(expected) {
		return 1.0
	}
	return 0.0
}

// streamMetrics carries per-step-enrichment evidence extracted from
// the captured JSONL stream that `ktx run` teed to bench run's
// captured agent stdout. OpenCode 1.17.8 emits
// tokens_in/tokens_out/cost/model on the step_finish event (a
// step-aggregate), not on tool_use (per-tool). The wrapper persists
// only tool_use events as tool_call rows, so those rows are
// NULL-by-design against real OpenCode (see parser.go:150-158). This
// struct surfaces the stream-side evidence; bench run uses it to
// populate bench_runs row fields that would otherwise stay 0/NULL via
// SummarizeBenchToolCalls.
type streamMetrics struct {
	tokensIn   *int64
	tokensOut  *int64
	cost       *float64
	model      string
	provider   string
	cacheRead  *int64
	cacheWrite *int64
	reasoning  *int64
	hasData    bool
}

func (sm streamMetrics) completeTokens() bool {
	return sm.tokensIn != nil && sm.tokensOut != nil && sm.reasoning != nil && sm.cacheRead != nil && sm.cacheWrite != nil
}

func (sm streamMetrics) completeReportedCost() bool {
	return sm.cost != nil
}

// extractStreamMetrics parses the captured JSONL stream looking for
// step_finish events (the OpenCode step-aggregate). Token, cost,
// cache, and reasoning aggregation and the completeness gate are
// delegated to the shared usage package so the live benchmark path
// and the experiment path apply one interpretation to the same
// recorded stream.
//
// model and provider are command-specific metadata extracted here, not
// in the shared package: the first non-empty part.model /
// part.provider on any step_finish wins. They do not participate in
// the completeness gate.
//
// When the stream carries no step_finish events (e.g., a flat-fallback
// runtime that emits only tool calls), streamMetrics.hasData=false
// and the caller falls through to the tool_calls-based aggregation
// path.
//
// The input is a []byte snapshot taken before any scanner reads the
// buffer (see extractAgentOutput's doc for why — once a bufio.
// Scanner consumes a *bytes.Buffer, the read offset is at EOF).
func extractStreamMetrics(stdout []byte) streamMetrics {
	var sm streamMetrics
	agg := &usage.Aggregator{}
	usage.ReadDelimitedLines(bytes.NewReader(stdout), usage.MaxRecordSize, func(line []byte) bool {
		agg.AddLine(line)
		// model / provider are CLI-local metadata; extract them
		// from the same scan so the stream is read exactly once.
		extractModelProvider(line, &sm)
		return false
	})
	ev := agg.Result()
	// Integer evidence (tokens, cache, reasoning) is shared as
	// exact *int64 from the usage package; this caller passes it
	// through without conversion. An explicit zero is preserved
	// (non-nil pointer to 0); a completeness failure, an absent
	// field, or a fractional/precision-losing value stays nil.
	sm.hasData = ev.HasData
	sm.tokensIn = ev.TokensIn
	sm.tokensOut = ev.TokensOut
	sm.cacheRead = ev.CacheRead
	sm.cacheWrite = ev.CacheWrite
	sm.reasoning = ev.Reasoning
	// cost is *float64 in the shared result; pass through.
	sm.cost = ev.Cost
	return sm
}

// extractModelProvider reads the first non-empty part.model and
// part.provider from a step_finish line into sm. They are
// command-specific metadata and do not affect usage-evidence
// aggregation or completeness. Malformed and non-step_finish lines
// are ignored.
func extractModelProvider(line []byte, sm *streamMetrics) {
	var event struct {
		Type string `json:"type"`
		Part struct {
			Model    string `json:"model"`
			Provider string `json:"provider"`
		} `json:"part"`
	}
	if json.Unmarshal(line, &event) != nil {
		return
	}
	if event.Type != "step_finish" {
		return
	}
	if sm.model == "" && event.Part.Model != "" {
		sm.model = event.Part.Model
	}
	if sm.provider == "" && event.Part.Provider != "" {
		sm.provider = event.Part.Provider
	}
}

// computeHeadline computes the headline number:
//
//	cost_per_correct = cost_USD / max(score, 1e-9)
//
// The epsilon prevents division by zero when score=0. The 1e-9
// guard is documented in methodology-v1.yaml.
// Returns 0 when cost is nil (the bench row's NULL cost path — the
// headline reflects partial data, soft-fail).
func computeHeadline(cost *float64, score float64) float64 {
	if cost == nil {
		return 0
	}
	denom := score
	if denom < benchEpsilon {
		denom = benchEpsilon
	}
	return *cost / denom
}
