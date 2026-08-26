// `ktx run` — the wrapper integration path. Spawns an agent subprocess,
// captures its stdout, parses newline-delimited JSON events for tool-call
// shapes and identifiable OpenCode model steps, persists each evidence kind in
// its own table via the internal db.Open function, and
// transparently passes the agent's stdout through to ktx's stdout.
//
// The wrapper is a transparent proxy:
//
//   - agent stdout passes through to ktx's stdout untouched
//   - agent stderr inherits the wrapper's stderr
//   - one-line run summary to stderr; --quiet suppresses it
//   - exit code matches the child's exit code
//
// Session lifecycle:
//
//   - Open: wrapper creates a session row before spawning the child,
//     setting agent = first positional arg (the binary name, e.g.
//     "opencode"). This overrides the hook path's "unknown" default.
//   - Identity: KTX_SESSION_ID exported into the child env so any
//     runtime hook noops and the child does not double-record.
//   - Close: immediate, on child process exit. Wrapper sets
//     ended_at and releases the session file's invariant for the next
//     call.
//
// Event parsing:
//
//	The agent is expected to emit newline-delimited JSON events on
//	stdout. Each line is forwarded to internal/parser.ParseEvent,
//	which is the single source of truth for the ktx tool_call shape. OpenCode
//	step_finish records use the pinned telemetry normalizer and remain separate.
//	ParseEvent returns (tool_call, true) for tool-shaped events
//	(OpenCode tool_use and the flat fallback) and (nil, false) for
//	non-tool events (text, step lifecycle, malformed JSON, empty lines).
//	The non-tool signal is passthrough: teeAndParse writes the line
//	to the caller's stdout and moves on. The parser package is
//	module-internal and reused by hook processing
//	(same JSON payload shape).
//
// executeRun owns the transparent subprocess proxy, event persistence, and
// session lifecycle. Both CLI entry points forward the child's exit status.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/evidence"
	"github.com/jgabor/ktx/internal/parser"
	telemetryopencode "github.com/jgabor/ktx/internal/telemetry/opencode"
)

// maxAgentLine caps a single emitted JSON line. Agent tool outputs (e.g.,
// file listings) can be tens of MB; the limit leaves headroom without
// reserving memory we don't need. Larger lines are skipped (passthrough).
const maxAgentLine = 64 * 1024 * 1024

// runCmd is the subprocess constructor used by executeRun to spawn the
// agent. It matches the exec.CommandContext signature so a test-only
// seam can substitute a fake constructor without touching real OS
// processes. The package-level variable preserves production behavior
// (real exec.CommandContext) — tests reassign it for the duration of a
// single test via t.Cleanup, never at init time. Concurrent tests that
// override runCmd must serialise their overrides; the seam is single-
// threaded within a single goroutine but the variable itself is shared.
var runCmd = exec.CommandContext

type runOptions struct {
	Format       string
	Quiet        bool
	RecordStream bool
	CollectionID string
}

// runRunWith executes `ktx run` with injected output streams.
// stdout/stderr are injected so the run flow can be exercised without
// touching os.Stdout/Stderr. The body parses the run flag set and
// invokes executeRun, returning the child's exit code directly.
func runRunWith(args []string, opts Options, stdout, stderr io.Writer) int {
	return runDispatchWithOptions("run", args, opts, stdout, stderr)
}

func extractCollectionFlag(args []string) ([]string, string, error) {
	rest := make([]string, 0, len(args))
	var id string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--collection=") {
			rest = append(rest, arg)
			continue
		}
		value := strings.TrimPrefix(arg, "--collection=")
		if id != "" || !dbpkg.IDPattern.MatchString(value) {
			return nil, "", fmt.Errorf("--collection requires one 16-letter lowercase ID")
		}
		id = value
	}
	return rest, id, nil
}

// matchRecordStreamFlag parses the --record-stream[=value] form and
// returns matched=true when the flag matches and an error for an unsupported
// value. The bare form
// (--record-stream) is handled by the caller's switch; this helper
// only covers the =value form so the caller can stay a simple switch
// for the bare flags. Returns matched=false when the arg is not a
// --record-stream flag.
func matchRecordStreamFlag(a string) (value, matched bool, err error) {
	const prefix = "--record-stream="
	if !strings.HasPrefix(a, prefix) {
		return false, false, nil
	}
	val := strings.TrimPrefix(a, prefix)
	switch val {
	case "true", "1":
		return true, true, nil
	case "false", "0":
		return false, true, nil
	default:
		return false, true, fmt.Errorf("invalid value %q for --record-stream: want true, false, 1, or 0", val)
	}
}

// filterKtxFlags removes ktx-recognised flags from args. The recognised set
// is small and stable: --quiet/-q, --help/-h, and --record-stream. Other
// flags and all positional arguments pass through to the agent. We
// intentionally do NOT recognise --format here — --format is a global
// flag and the wrapper always emits the run summary as JSON regardless
// of format.
//
// Both bare and --flag=value forms are accepted so the post-agent scan
// behaves like the global parser (stdlib flag.BoolVar). The pre-agent
// form is consumed by parseRunArgs; this helper is the post-agent
// fallback for users who type `ktx run <agent> ... --quiet`.
//
// Returns the owned values, passthrough arguments, and any malformed owned
// flag error.
func filterKtxFlags(args []string) (quiet, showHelp, recordStream bool, rest []string, err error) {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "-q", "--quiet":
			quiet = true
		case "-h", "--help":
			showHelp = true
		case "--record-stream":
			if a == "--record-stream" {
				recordStream = true
				continue
			}
			value, _, parseErr := matchRecordStreamFlag(a)
			if parseErr != nil {
				return false, false, false, nil, parseErr
			}
			recordStream = value
		default:
			rest = append(rest, a)
		}
	}
	return quiet, showHelp, recordStream, rest, nil
}

// executeRun is the runtime body of `ktx run`. It opens the session,
// spawns the agent, parses tool events, persists them, closes the
// session, and emits the run summary. It returns the exit code that ktx
// run itself should report (the child's exit code, or an internal error
// code if session open fails).
//
// When opts.RecordStream is set (the --record-stream opt-in), the
// wrapper captures the agent's stdout into a bytes.Buffer alongside the
// transparent passthrough (io.MultiWriter), and persists the captured
// JSONL into the recorded_streams table after the session closes —
// with an empty (NULL) bench_run_id, since ktx run has no bench_runs
// parent. The capture reuses the same tee path as bench's
// captureStreamForBenchRun (recording contract); --replay can then
// re-parse the session without re-invoking the agent. The capture is
// soft-fail: a persist error is logged as a warning and the run still
// exits with the child's exit code (the agent already ran).
func executeRun(agent string, agentArgs []string, opts runOptions, stdout, stderr io.Writer) int {
	start := time.Now()

	// Open session before spawning the child so any early child output
	// can be associated with the right session_id.
	target, err := dbpkg.ResolveTarget("")
	if err != nil {
		printError(stderr, internalError("ktx run: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	db, err := openDatabase(target, "run")
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	defer db.Close()

	startedAt := start.UTC().Format(time.RFC3339Nano)
	// The session-file key is the wrapper's own PID: the child sees ktx
	// run as its parent, so a hook on the child looks up the
	// wrapper by its ppid. KTX_SESSION_ID makes the hook noop in any case.
	sessionID, err := db.OpenNamedSession(agent, os.Getpid(), startedAt)
	if err != nil {
		printError(stderr, internalError("ktx run: open session: %v", err), opts.Format)
		return ExitInternal
	}

	continuityStarted := time.Now().UTC()
	var interval *dbpkg.CollectionInterval
	if opts.CollectionID == "" {
		interval, err = db.OpenCollectionInterval([]string{"run"}, continuityStarted.Format(time.RFC3339Nano))
	} else {
		interval, err = db.EnsureCollectionInterval(opts.CollectionID, []string{"run"}, continuityStarted.Format(time.RFC3339Nano))
	}
	if err != nil {
		printError(stderr, internalError("ktx run: open continuity interval: %v", err), opts.Format)
		return ExitInternal
	}
	observation := dbpkg.IngressObservation{
		IntervalID: interval.ID,
		Ingress:    "run",
		InstanceID: sessionID,
		Authority:  dbpkg.IngressProcessAuthority,
	}
	observation.State = "started"
	observation.ObservedAt = continuityStarted.Format(time.RFC3339Nano)
	if err := db.RetainIngressObservation(observation); err != nil {
		printError(stderr, internalError("ktx run: retain continuity start: %v", err), opts.Format)
		return ExitInternal
	}
	continuityFinished := false
	finishContinuity := func(state string, ended time.Time) {
		if continuityFinished {
			return
		}
		continuityFinished = true
		observation.State = state
		observation.ObservedAt = ended.Format(time.RFC3339Nano)
		if err := db.RetainIngressObservation(observation); err != nil {
			fmt.Fprintf(stderr, "ktx: warning: retain run continuity %s: %v\n", state, err)
			return
		}
		result, err := db.EvaluateContinuity(interval.ID, continuityStarted, ended)
		if err != nil {
			fmt.Fprintf(stderr, "ktx: warning: evaluate run continuity: %v\n", err)
		} else if state == "stopped" && !result.Continuous {
			fmt.Fprintln(stderr, "ktx: warning: run continuity closed with a coverage gap")
		}
	}
	defer func() { finishContinuity("interrupted", time.Now().UTC()) }()

	// Spawn the child with KTX_SESSION_ID exported. The child
	// inherits the wrapper's stdin and stderr (the agent can read input
	// and write diagnostics directly); only stdout is intercepted for
	// tee + parse. Signal forwarding: SIGINT/SIGTERM received by the
	// wrapper are propagated to the child via context cancellation.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// runCommandFunc / runRunWith pass agentArgs that still includes the
	// agent binary name at position 0 (filterKtxFlags returns ctx.Args
	// unchanged when no quiet/help flags are present). exec.CommandContext
	// already uses `agent` as argv[0]; if we also pass agentArgs[0] ==
	// agent as the first variadic arg, Go's exec appends it as argv[1]
	// and the child process receives a doubled agent name at the front
	// of its argv. That broke `ktx run opencode run --format json ...`:
	// opencode saw argv=[opencode, opencode, run, ...], treated the
	// doubled "opencode" as the default `[project]` positional, and
	// emitted its top-level help instead of running the `run` subcommand.
	// Strip the leading agent name defensively so executeRun works
	// regardless of which caller hands it the args slice.
	agentArgsTail := agentArgs
	if len(agentArgsTail) > 0 && agentArgsTail[0] == agent {
		agentArgsTail = agentArgsTail[1:]
	}
	cmd := runCmd(ctx, agent, agentArgsTail...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "KTX_SESSION_ID="+sessionID)
	if opts.CollectionID != "" {
		cmd.Env = append(cmd.Env, "KTX_COLLECTION_ID="+opts.CollectionID, "KTX_DB="+target.Path)
	}

	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		printError(stderr, internalError("ktx run: stdout pipe: %v", err), opts.Format)
		return ExitInternal
	}

	if err := cmd.Start(); err != nil {
		// A missing agent binary is a user error (wrong name) rather than
		// a ktx internal fault; classify as usage so the exit code matches
		// the project's exit-code contract (1 = usage).
		printError(stderr, usageError("ktx run: spawn %q: %v", agent, err), opts.Format)
		return ExitUsage
	}

	// Tee stdout: read line by line, write to caller's stdout
	// (passthrough), parse each line for tool events. Persistence is
	// inline so each tool_call is committed before the next line is
	// parsed — partial runs are crash-safe.
	//
	// When --record-stream is set, the tee also writes to a
	// bytes.Buffer (via io.MultiWriter) so the full JSONL stream is
	// captured for the recorded_streams persist after session close.
	var streamBuf bytes.Buffer
	teeOut := stdout
	if opts.RecordStream {
		teeOut = io.MultiWriter(stdout, &streamBuf)
	}
	toolCalls, collectionErr := teeAndParseRuntimeCollection(stdoutR, teeOut, db, sessionID, stderr, filepath.Base(agent), opts.CollectionID)

	waitErr := cmd.Wait()

	// Close the session immediately on child process exit
	// (wrapper closes immediately on child process exit). Close failure
	// is logged but does not fail the user's command — the agent ran,
	// and the user gets its exit code.
	continuityEnded := time.Now().UTC()
	continuityState := "stopped"
	if ctx.Err() != nil {
		continuityState = "interrupted"
	}
	finishContinuity(continuityState, continuityEnded)
	endedAt := continuityEnded.Format(time.RFC3339Nano)
	if err := db.CloseSession(sessionID, endedAt); err != nil {
		fmt.Fprintf(stderr, "ktx: warning: close session %q: %v\n", sessionID, err)
	}

	// Persist the captured stream into recorded_streams when
	// --record-stream was passed (recording contract, opt-in for
	// the wrapper path). bench_run_id is empty (SQL NULL) because ktx
	// run has no bench_runs parent — the bench path
	// (captureStreamForBenchRun) is the only caller that supplies a
	// non-empty bench_run_id. Soft-fail: a persist error is logged as a
	// warning and the run still exits with the child's exit code.
	if opts.RecordStream {
		if err := captureStreamForRun(db, sessionID, streamBuf.Bytes()); err != nil {
			fmt.Fprintf(stderr, "ktx: warning: record stream: %v\n", err)
		}
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// Wait() failed for a non-exit reason (rare; e.g., I/O
			// error after a successful Start). Treat as internal.
			exitCode = ExitInternal
		}
	}
	if collectionErr != nil {
		printError(stderr, internalError("ktx run: collection failed closed: %v", collectionErr), opts.Format)
		exitCode = ExitInternal
	}

	if !opts.Quiet {
		writeRunSummary(stderr, toolCalls, time.Since(start), exitCode)
	}

	return exitCode
}

// captureStreamForRun persists the captured JSONL stream into the
// recorded_streams table for a `ktx run --record-stream` invocation
// (recording contract, opt-in wrapper path). Mirrors
// captureStreamForBenchRun but passes an empty bench_run_id (which
// InsertRecordedStream maps to SQL NULL) since ktx run has no
// bench_runs parent. The captured stream enables `ktx bench run
// --replay <session-id>` on a wrapper-captured session without
// re-invoking the agent.
func captureStreamForRun(db *dbpkg.DB, sessionID string, streamBytes []byte) error {
	rsID, err := dbpkg.NewID()
	if err != nil {
		return fmt.Errorf("generate recorded_stream id: %w", err)
	}
	streamText := string(streamBytes)
	rs := &dbpkg.RecordedStream{
		ID:           rsID,
		SessionID:    sessionID,
		BenchRunID:   "",
		StreamText:   streamText,
		ContentHash:  dbpkg.ContentHash(streamText),
		StreamFormat: "opencode-jsonl-1",
		CapturedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	return db.InsertRecordedStream(rs)
}

// runCommandFunc adapts parsed run arguments to executeRun. executeRun owns
// session database lifetime; child statuses are forwarded unchanged.
func runCommandFunc(ctx *DispatchContext) (any, error) {
	parsed, ok := ctx.ParsedArgs.(runParsedArgs)
	if !ok {
		return nil, internalError("ktx run: command arguments were not parsed")
	}
	// The dispatcher already consumed --help and --quiet; ctx.Args
	// contains only positional args. We still need to scan for
	// post-agent --quiet (e.g. `ktx run <agent> ... --quiet`),
	// --record-stream, and --help because the dispatcher does not see
	// those (they are in the agent's args, not the verb's).
	postAgentQuiet, agentHelp, postAgentRecordStream, agentArgs, err := filterKtxFlags(ctx.Args)
	if err != nil {
		return nil, usageError("ktx run: %v", err)
	}
	if agentHelp {
		printRunHelp(ctx.Stdout)
		return nil, nil
	}

	if len(ctx.Args) == 0 {
		return nil, usageError("ktx run: missing agent command; usage: ktx run [flags] <agent> [args...]")
	}
	agent := ctx.Args[0]

	opts := runOptions{
		Format:       ctx.Format,
		Quiet:        ctx.Quiet || postAgentQuiet,
		RecordStream: parsed.recordStream || postAgentRecordStream,
		CollectionID: parsed.collectionID,
	}

	exitCode := executeRun(agent, agentArgs, opts, ctx.Stdout, ctx.Stderr)
	if exitCode == ExitOK {
		return nil, nil
	}
	return nil, exitWith(exitCode)
}

// The run command is a transparent stdout proxy. It owns database setup inside
// executeRun because its session lifetime is coupled to the child process.
func init() {
	Register("run", RegisterOptions{
		Description:       "wrap an agent invocation and record the session",
		PassthroughStdout: true,
		ParseArgs:         parseRunDispatchArgs,
		Help:              printRunHelp,
	}, runCommandFunc)
}

// runParsedArgs carries command-specific flags to runCommandFunc.
type runParsedArgs struct {
	recordStream bool
	collectionID string
}

// parseRunDispatchArgs extracts ktx flags placed before the agent command.
func parseRunDispatchArgs(args []string) ([]string, any, error) {
	var collectionID string
	var err error
	args, collectionID, err = extractCollectionFlag(args)
	if err != nil {
		return nil, nil, err
	}
	var recordStream bool
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--record-stream" {
			recordStream = true
			continue
		}
		if val, ok, err := matchRecordStreamFlag(a); err != nil {
			return nil, nil, err
		} else if ok {
			recordStream = val
			continue
		}
		rest = append(rest, a)
	}
	return rest, runParsedArgs{recordStream: recordStream, collectionID: collectionID}, nil
}

// teeAndParse reads from r, writes each line to stdout (transparent
// passthrough), and persists any tool-shaped event to db. Returns the
// number of tool_calls persisted. teeAndParse never returns an error:
// line read errors are logged but do not abort the tee; persist errors
// are logged per-call but do not abort the tee.
//
// The JSON -> tool_call translation lives in internal/parser
// (parser.ParseEvent). The wrapper adds sessionID after parsing and
// hands the populated *db.ToolCall to db.PersistToolCall. Non-tool
// events (text, step lifecycle, malformed JSON, empty lines) are
// passed through: ParseEvent returns (nil, false) and the wrapper
// writes the line to stdout without persisting.
//
// Persistence is inline (synchronous) so each tool_call is committed
// before the next line is read. A persist failure is logged to stderr
// and the next line is processed.
func teeAndParse(r io.Reader, stdout io.Writer, db *dbpkg.DB, sessionID string, stderr io.Writer) int {
	return teeAndParseRuntime(r, stdout, db, sessionID, stderr, "")
}

func teeAndParseRuntime(r io.Reader, stdout io.Writer, db *dbpkg.DB, sessionID string, stderr io.Writer, runtimeName string) int {
	count, _ := teeAndParseRuntimeCollection(r, stdout, db, sessionID, stderr, runtimeName, "")
	return count
}

func teeAndParseRuntimeCollection(r io.Reader, stdout io.Writer, db *dbpkg.DB, sessionID string, stderr io.Writer, runtimeName, collectionID string) (int, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxAgentLine)
	var persisted int
	var telemetryCollector *telemetryopencode.Collector
	if runtimeName == telemetryopencode.Runtime {
		telemetryCollector = telemetryopencode.NewCollector()
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		// Passthrough: write the line as-is, then a newline so the
		// caller's stdout stays in sync with what the agent emitted
		// (scanner.Bytes() strips the trailing newline).
		_, _ = stdout.Write(line)
		_, _ = stdout.Write([]byte{'\n'})

		if telemetryCollector != nil {
			observation, err := telemetryCollector.AddStream(line)
			switch {
			case err == nil:
				capturedAt := time.Now().UTC().Format(time.RFC3339Nano)
				if _, persistErr := db.PersistSessionTelemetry(sessionID, capturedAt, observation); persistErr != nil {
					fmt.Fprintf(stderr, "ktx: warning: persist session telemetry: %v\n", persistErr)
				}
			case errors.Is(err, telemetryopencode.ErrDuplicate), errors.Is(err, telemetryopencode.ErrUnsupported), errors.Is(err, telemetryopencode.ErrInvalid):
				// Rejected and duplicate model-step records do not affect tool evidence.
			default:
				fmt.Fprintf(stderr, "ktx: warning: normalize session telemetry: %v\n", err)
			}
		}

		tc, nc, ok := parser.ParseEventWithCorrelation(line)
		if !ok {
			continue
		}
		// ParseEvent returns a fully-populated *db.ToolCall except for
		// SessionID, which the parser does not know. The wrapper fills
		// it before persisting.
		tc.SessionID = sessionID
		if collectionID != "" {
			if !nc.Trusted || nc.Runtime != telemetryopencode.Runtime {
				return persisted, dbpkg.ErrContinuityInvalid
			}
			src, err := correlation.Resolve(correlation.NativeTuple{Runtime: nc.Runtime, SessionID: nc.SessionID, CallID: nc.CallID}, correlation.Trusted(correlation.ProvenanceOpenCodeWrapper))
			if err != nil {
				return persisted, err
			}
			err = db.RetainRuntimeSourceObservation(dbpkg.RuntimeSourceObservation{
				CollectionID: collectionID, SessionID: sessionID, SourceID: src.ID,
				LatencyMs: tc.LatencyMs, LatencyState: tc.LatencyState, LatencyUnit: tc.LatencyUnit,
				LatencyAuthority: tc.LatencyAuthority, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}, tc.Output)
			if err != nil {
				return persisted, err
			}
			persisted++
			continue
		}
		producer := dbpkg.ProducerRun
		tc.Producer = &producer

		// Resolve deterministic identity from the native correlation
		// tuple the parser extracted. For OpenCode tool_use events the
		// parser recognizes the native schema and sets Trusted=true; the
		// wrapper is a recognized parser path, so provenance is trusted.
		// Flat-fallback events are untrusted and receive a fresh NanoID.
		var prov correlation.Provenance
		if nc.Trusted {
			prov = correlation.Trusted(correlation.ProvenanceOpenCodeWrapper)
		} else {
			prov = correlation.Untrusted()
		}
		src, err := correlation.Resolve(
			correlation.NativeTuple{Runtime: nc.Runtime, SessionID: nc.SessionID, CallID: nc.CallID},
			prov)
		if err != nil {
			fmt.Fprintf(stderr, "ktx: warning: resolve identity: %v\n", err)
			continue
		}
		tc.ID = src.ID
		// Normalize runtime consistently with the hook path so that
		// equivalent cross-ingress events have the same authoritative
		// evidence (runtime is an equality field in PersistToolCallOnce).
		if nc.Runtime != "" {
			rt := nc.Runtime
			tc.Runtime = &rt
		}

		if _, err := evidence.PersistSource(db, src, tc); err != nil {
			if errors.Is(err, dbpkg.ErrToolCallConflict) {
				fmt.Fprintf(stderr, "ktx: warning: tool_call conflict for source %q: %v\n", tc.ID, err)
			} else {
				fmt.Fprintf(stderr, "ktx: warning: persist tool_call: %v\n", err)
			}
			continue
		}
		persisted++
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(stderr, "ktx: warning: read agent stdout: %v\n", err)
	}
	return persisted, nil
}

// writeRunSummary emits the one-line run summary to w. The summary uses
// the JSON shape: {"summary":{"tool_calls":N,"duration_ms":N,"exit_code":N}}.
// json.NewEncoder writes a trailing newline so the summary is a clean
// single line on stderr.
func writeRunSummary(w io.Writer, toolCalls int, duration time.Duration, exitCode int) {
	summary := map[string]any{
		"summary": map[string]any{
			"tool_calls":  toolCalls,
			"duration_ms": duration.Milliseconds(),
			"exit_code":   exitCode,
		},
	}
	_ = json.NewEncoder(w).Encode(summary)
}

// printRunHelp writes the `ktx run` help. Mirrors the help style of
// `ktx write` and `ktx read` so the surface presents consistently.
// Used as the dispatcher's Help hook (renderHelp invokes it on
// --help) and from runRunWith when called directly with --help.
func printRunHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx run — wrap an agent invocation and record the session")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx run [flags] <agent> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Spawns <agent> as a child process with KTX_SESSION_ID exported into")
	fmt.Fprintln(w, "the environment. The agent's stdout passes through to ktx's stdout")
	fmt.Fprintln(w, "untouched. Each newline-delimited JSON event is parsed; tool events")
	fmt.Fprintln(w, "are persisted as tool_calls and identifiable OpenCode model steps as")
	fmt.Fprintln(w, "separate session telemetry.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "When the child exits, the session is closed immediately (ended_at set).")
	fmt.Fprintln(w, "ktx run's exit code matches the child's exit code.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -q, --quiet    suppress the one-line run summary on stderr")
	fmt.Fprintln(w, "  -h, --help     show this help")
	fmt.Fprintln(w, "      --record-stream  persist the agent's JSONL stdout into the")
	fmt.Fprintln(w, "                        recorded_streams table (enables")
	fmt.Fprintln(w, "                        `ktx bench run --replay <session-id>`)")
	fmt.Fprintln(w, "      --collection=<id>  explicitly let the supported OpenCode hook own")
	fmt.Fprintln(w, "                         transformed source evidence while this wrapper")
	fmt.Fprintln(w, "                         owns lifecycle and runtime timing")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Note: --quiet/-q, --record-stream, and --collection=<id> may also appear after the agent")
	fmt.Fprintln(w, "command; the wrapper recognises them anywhere on its command line.")
	fmt.Fprintln(w, "Other flags pass through to the agent.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Example:")
	fmt.Fprintln(w, "  KTX_DB=\"$PWD/.ktx-example.db\" ktx run opencode run --format json \"explain closures in JavaScript\"")
	fmt.Fprintln(w, "  This selects an operator-owned project path; verify that it is disposable before running.")
	fmt.Fprintln(w, "  KTX_DB=\"$PWD/.ktx-example.db\" ktx --quiet run opencode run --format json --model anthropic/claude-sonnet-4-20250514 \"fix the bug\"")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Event parsing:")
	fmt.Fprintln(w, "  OpenCode --format json emits \"tool_use\" events that the wrapper")
	fmt.Fprintln(w, "  maps to tool_calls rows. A flat fallback (id/tool/input/output at")
	fmt.Fprintln(w, "  the top level, with aliases like name/args/result/time) supports")
	fmt.Fprintln(w, "  other runtimes. Non-tool events and non-JSON lines pass through")
	fmt.Fprintln(w, "  unchanged.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
