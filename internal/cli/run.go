// `ktx run` — the wrapper integration path. Spawns an agent subprocess,
// captures its stdout, parses newline-delimited JSON events for tool-call
// shapes and identifiable OpenCode model steps, persists each evidence kind in
// its own table via the internal db.Open function, and
// transparently passes the agent's stdout through to ktx's stdout.
//
// The wrapper is a transparent proxy:
//
//   - agent stdout preserves ScanLines normalization (CRLF to LF, final LF added)
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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/evidence"
	"github.com/jgabor/ktx/internal/parser"
	"github.com/jgabor/ktx/internal/telemetry"
	telemetryopencode "github.com/jgabor/ktx/internal/telemetry/opencode"
)

// maxAgentLine bounds event parsing, not forwarding. Larger lines are
// forwarded completely without parsing, with a diagnostic.
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
	Format            string
	Quiet             bool
	RecordStream      bool
	CollectionID      string
	completionReceipt io.Writer
}

// runReceiptEnv selects the private descriptor supplied by bench, not a
// caller-selected session. The wrapper remains the session identity owner.
const runReceiptEnv = "KTX_INTERNAL_RUN_RECEIPT"

func withoutRunReceipt(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, runReceiptEnv+"=") {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// runRunWith executes `ktx run` with injected output streams.
// stdout/stderr are injected so the run flow can be exercised without
// touching os.Stdout/Stderr. The body parses the run flag set and
// invokes executeRun, returning the child's exit code directly.
func runRunWith(args []string, opts Options, stdout, stderr io.Writer) int {
	return runDispatchWithOptions("run", args, opts, stdout, stderr)
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

// executeRun is the runtime body of `ktx run`. It opens the session,
// spawns the agent, parses tool events, persists them, closes the
// session, and emits the run summary. It returns the exit code that ktx
// run itself should report (the child's exit code, or an internal error
// code if session open fails).
//
// When opts.RecordStream is set (the --record-stream opt-in), the
// wrapper captures the agent's stdout into a private spool alongside the
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
	// tee + parse. On Linux, either SIGINT or SIGTERM starts a TERM grace
	// period for the owned child/group, followed by KILL and pipe settlement.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Let stdout return EPIPE instead of terminating the wrapper before
	// it can report delivery failure and reap its child. No signal loop is
	// needed: the write error is the authority, not the notification.
	brokenPipe := make(chan os.Signal, 1)
	signal.Notify(brokenPipe, syscall.SIGPIPE)
	defer signal.Stop(brokenPipe)

	// runCommandFunc / runRunWith pass agentArgs that still includes the
	// agent binary name at position 0. exec.CommandContext
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
	if err := configureRunProcess(cmd); err != nil {
		fmt.Fprintf(stderr, "ktx: warning: retaining inherited process group: %v\n", err)
	}
	restoreProcess, err := prepareRunSubreaper(cmd)
	if err != nil {
		printError(stderr, internalError("ktx run: prepare descendant ownership: %v", err), opts.Format)
		return ExitInternal
	}
	defer func() {
		if err := restoreProcess(); err != nil {
			fmt.Fprintf(stderr, "ktx: warning: restore process state: %v\n", err)
		}
	}()
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	cmd.Env = append(withoutRunReceipt(os.Environ()), "KTX_SESSION_ID="+sessionID)
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
	settleProcess := superviseRunProcess(ctx, cmd, stdoutR)

	// Tee stdout: read line by line, write to caller's stdout
	// (passthrough), parse each line for tool events. Persistence is
	// inline so each tool_call is committed before the next line is
	// parsed — partial runs are crash-safe.
	//
	// Optional recording failure must not stop ordinary stdout forwarding.
	var stream *recordingSpool
	teeOut := stdout
	if opts.RecordStream {
		stream, err = newRecordingSpool()
		if err != nil {
			fmt.Fprintf(stderr, "ktx: warning: record stream unavailable: %v\n", err)
		} else {
			defer stream.close()
			teeOut = io.MultiWriter(stdout, stream)
		}
	}
	toolCalls, collectionErr := teeAndParseRuntimeCollection(stdoutR, teeOut, db, sessionID, stderr, filepath.Base(agent), opts.CollectionID)
	shutdown := settleProcess(collectionErr)
	collectionErr = errors.Join(collectionErr, shutdown.err)

	waitErr := cmd.Wait()

	// Close the session immediately on child process exit
	// (wrapper closes immediately on child process exit). Close failure
	// is logged but does not fail the user's command — the agent ran,
	// and the user gets its exit code.
	continuityEnded := time.Now().UTC()
	continuityState := "stopped"
	if ctx.Err() != nil || shutdown.interrupted || collectionErr != nil {
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
	var recordingChildExit *exec.ExitError
	if stream != nil {
		err := stream.finish()
		if err == nil && collectionErr == nil && continuityState == "stopped" && (waitErr == nil || errors.As(waitErr, &recordingChildExit)) {
			err = stream.complete(sessionID)
			if err == nil {
				err = stream.publish(db, "")
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "ktx: warning: record stream: %v; private owner location %s\n", err, stream.path)
		}
	}
	if continuityState == "interrupted" {
		fmt.Fprintln(stderr, "ktx: run interrupted; committed evidence retained, complete stream recording unavailable")
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
	var childExit *exec.ExitError
	if opts.completionReceipt != nil && continuityState == "stopped" && (waitErr == nil || errors.As(waitErr, &childExit)) {
		if _, err := io.WriteString(opts.completionReceipt, sessionID); err != nil {
			fmt.Fprintf(stderr, "ktx: warning: write completed session receipt: %v\n", err)
		}
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
	if len(ctx.Args) == 0 {
		return nil, usageError("ktx run: missing agent command; usage: ktx run [flags] <agent> [args...]")
	}
	agent := ctx.Args[0]

	opts := runOptions{
		Format:       ctx.Format,
		Quiet:        ctx.Quiet,
		RecordStream: parsed.recordStream,
		CollectionID: parsed.collectionID,
	}
	if parsed.quietSet {
		opts.Quiet = parsed.quiet
	}
	if os.Getenv(runReceiptEnv) == "1" {
		receipt := os.NewFile(3, "run-completion-receipt")
		defer receipt.Close()
		opts.completionReceipt = receipt
	}

	exitCode := executeRun(agent, ctx.Args, opts, ctx.Stdout, ctx.Stderr)
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
	quiet        bool
	quietSet     bool
	showHelp     bool
}

// parseRunDispatchArgs owns wrapper flags on both sides of the executable.
// The first -- ends wrapper parsing and is removed; every later token is
// literal child argv. Boolean values must be attached, and collection accepts
// only =id. No child option's value arity is inferred.
func parseRunDispatchArgs(args []string) ([]string, any, error) {
	var parsed runParsedArgs
	rest := make([]string, 0, len(args))
	for i, arg := range args {
		if arg == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		name, value, explicit := strings.Cut(arg, "=")
		// Keep the shared flag parser's existing single-dash long names and
		// double-dash short names before the executable, without adding new
		// post-executable aliases.
		if len(rest) == 0 {
			switch name {
			case "-quiet", "--q":
				name = "--quiet"
			case "-help", "--h":
				name = "--help"
			}
		}
		switch name {
		case "--quiet", "-q", "--help", "-h":
			b := true
			if explicit {
				var err error
				b, err = strconv.ParseBool(value)
				if err != nil {
					return nil, nil, fmt.Errorf("invalid value %q for %s: want a boolean", value, name)
				}
			}
			if name == "--quiet" || name == "-q" {
				parsed.quiet, parsed.quietSet = b, true
			} else {
				parsed.showHelp = b
			}
			continue
		case "--record-stream":
			parsed.recordStream = true
			if explicit {
				b, _, err := matchRecordStreamFlag(arg)
				if err != nil {
					return nil, nil, err
				}
				parsed.recordStream = b
			}
			continue
		case "--collection":
			if explicit {
				if parsed.collectionID != "" || !dbpkg.IDPattern.MatchString(value) {
					return nil, nil, fmt.Errorf("--collection requires one 16-letter lowercase ID")
				}
				parsed.collectionID = value
				continue
			}
		}
		if len(rest) == 0 && strings.HasPrefix(arg, "-") && arg != "-" {
			return nil, nil, fmt.Errorf("flag provided but not defined: %s", name)
		}
		rest = append(rest, arg)
	}
	return rest, parsed, nil
}

// teeAndParse reads from r, writes each line to stdout (transparent
// passthrough), and persists any tool-shaped event to db. Returns the
// number of tool_calls persisted. The count-only helpers log forwarding
// failures; the subprocess owner uses the error-returning collection helper.
// Persist errors are logged per-call but do not abort the tee.
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
	count, err := teeAndParseRuntimeCollection(r, stdout, db, sessionID, stderr, runtimeName, "")
	if err != nil {
		fmt.Fprintf(stderr, "ktx: warning: %v\n", err)
	}
	return count
}

func teeAndParseRuntimeCollection(r io.Reader, stdout io.Writer, db *dbpkg.DB, sessionID string, stderr io.Writer, runtimeName, collectionID string) (int, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	var persisted int
	var telemetryCollector *telemetryopencode.Collector
	if runtimeName == telemetryopencode.Runtime {
		telemetryCollector = telemetryopencode.NewCollector()
	}
	for {
		line, oversized, err := forwardAgentLine(reader, stdout)
		if err == io.EOF {
			break
		}
		if err != nil {
			return persisted, err
		}
		if oversized {
			fmt.Fprintf(stderr, "ktx: warning: agent stdout event parsing skipped: line exceeds %d bytes; output forwarded\n", maxAgentLine)
			continue
		}

		if telemetryCollector != nil {
			err := telemetryCollector.CollectStream(line, func(observation *telemetry.Observation, capturedAt string) error {
				_, persistErr := db.PersistSessionTelemetry(sessionID, capturedAt, observation)
				return persistErr
			})
			switch {
			case err == nil:
			case errors.Is(err, telemetryopencode.ErrDuplicate), errors.Is(err, telemetryopencode.ErrUnsupported), errors.Is(err, telemetryopencode.ErrInvalid):
				// Rejected and duplicate model-step records do not affect tool evidence.
			default:
				fmt.Fprintf(stderr, "ktx: warning: persist session telemetry: %v\n", err)
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
	return persisted, nil
}

// forwardAgentLine streams a ScanLines-normalized line using fixed-size reads.
// Only the bounded event candidate is retained; oversized output keeps flowing.
// A trailing CR is held across fragments so CRLF and final-CR behavior does
// not depend on read boundaries. EOF means no line, not an unterminated line.
func forwardAgentLine(r *bufio.Reader, out io.Writer) ([]byte, bool, error) {
	var event []byte
	var oversized, seen, pendingCR bool
	write := func(p []byte) error {
		if len(p) == 0 {
			return nil
		}
		n, err := out.Write(p)
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return fmt.Errorf("write agent stdout: %w", err)
		}
		if !oversized {
			if len(p) > maxAgentLine-len(event) {
				oversized, event = true, nil
			} else {
				if len(event)+len(p) > cap(event) {
					buf := make([]byte, len(event), min(maxAgentLine, max(2*cap(event), len(event)+len(p))))
					copy(buf, event)
					event = buf
				}
				event = append(event, p...)
			}
		}
		return nil
	}
	for {
		part, readErr := r.ReadSlice('\n')
		if len(part) == 0 && !seen && errors.Is(readErr, io.EOF) {
			return nil, false, io.EOF
		}
		seen = seen || len(part) > 0
		final := !errors.Is(readErr, bufio.ErrBufferFull)
		readFailed := final && readErr != nil && !errors.Is(readErr, io.EOF)
		if readErr == nil {
			part = part[:len(part)-1]
		}
		if pendingCR && (len(part) > 0 || !final || readFailed) {
			if err := write([]byte{'\r'}); err != nil {
				return nil, oversized, err
			}
		}
		pendingCR = !readFailed && len(part) > 0 && part[len(part)-1] == '\r'
		if pendingCR {
			part = part[:len(part)-1]
		}
		if err := write(part); err != nil {
			return nil, oversized, err
		}
		if !final {
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, oversized, fmt.Errorf("read agent stdout: %w", readErr)
		}
		// The terminator is forwarded but is not part of the event candidate.
		n, err := out.Write([]byte{'\n'})
		if err == nil && n != 1 {
			err = io.ErrShortWrite
		}
		if err != nil {
			return nil, oversized, fmt.Errorf("write agent stdout: %w", err)
		}
		return event, oversized, nil
	}
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
	fmt.Fprintln(w, "with CRLF normalized to LF and a final newline added when absent.")
	fmt.Fprintln(w, "Newline-delimited JSON events are parsed; tool events")
	fmt.Fprintln(w, "are persisted as tool_calls and identifiable OpenCode model steps as")
	fmt.Fprintln(w, "separate session telemetry.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "When the child exits, the session is closed immediately (ended_at set).")
	fmt.Fprintln(w, "ktx run's exit code matches the child's unless forwarding or collection fails.")
	fmt.Fprintln(w, "Forwarding failure stops the direct child, returns exit 4, and does not")
	fmt.Fprintln(w, "publish an incomplete stream recording. Already persisted evidence remains.")
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
	fmt.Fprintln(w, "Wrapper flags above work before or after the agent, until the first --.")
	fmt.Fprintln(w, "That -- is removed; all remaining tokens are literal child arguments.")
	fmt.Fprintln(w, "Use a second -- if the child itself needs that token.")
	fmt.Fprintln(w, "Booleans are bare (true) or use =value; they never consume the next token.")
	fmt.Fprintln(w, "--quiet/--help accept Go boolean forms; --record-stream accepts true, false, 1, 0.")
	fmt.Fprintln(w, "The last boolean wins, including false; --collection requires exactly one =id.")
	fmt.Fprintln(w, "Other options after the agent pass through; their value arity is not inferred.")
	fmt.Fprintln(w, "For a child value such as --quiet, use: ktx run <agent> -- --prompt --quiet")
	fmt.Fprintln(w, "--format belongs to ktx only before run; after the agent it belongs to the child.")
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
	fmt.Fprintln(w, "  with the same newline normalization. Lines above 64 MiB are forwarded")
	fmt.Fprintln(w, "  completely, but event parsing is skipped with a stderr warning.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
