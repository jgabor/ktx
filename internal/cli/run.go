// `ktx run` — the wrapper integration path (Decisions 1, 4, 10). Spawns an
// agent subprocess, captures its stdout, parses newline-delimited JSON
// events for tool-call shapes, persists each tool event via the internal
// store function, and transparently passes the agent's stdout through to
// ktx's stdout.
//
// The wrapper is a transparent proxy (D1):
//
//   - agent stdout passes through to ktx's stdout untouched
//   - agent stderr inherits the wrapper's stderr
//   - one-line run summary to stderr; --quiet suppresses it
//   - exit code matches the child's exit code
//
// Session lifecycle (D1 revised, D7):
//
//   - Open: wrapper creates a session row before spawning the child,
//     setting agent = first positional arg (the binary name, e.g.
//     "opencode"). This overrides the hook path's "unknown" default.
//   - Identity: KTX_SESSION_ID exported into the child env (D10) so any
//     runtime hook noops and the child does not double-record.
//   - Close: immediate, on child process exit (D1 revised). Wrapper sets
//     ended_at and releases the session file's invariant for the next
//     call.
//
// Event parsing:
//
//	The agent is expected to emit newline-delimited JSON events on
//	stdout. Each event is parsed for a tool-call shape; non-tool events
//	(text, step lifecycle) and non-JSON lines are passed through
//	without persistence. Parsing is best-effort and schema-flexible: the
//	OpenCode "tool_use" shape (top.type == "tool_use", part.type ==
//	"tool", part.state.input/output) is recognised, and a flat fallback
//	(top-level id/tool/name/input/args/output/result/timestamp/time)
//	supports other runtimes.
//
// M2 extraction (Task 4):
//
//	The parser populates the M2 pointer fields on the persisted
//	tool_calls row when the source event carries them. OpenCode's
//	tool_use event exposes latency via part.state.time.start/end
//	(-> latency_ms) and identifies the runtime as provider="opencode"
//	(runtime-known single exception). tokens_in / tokens_out / cost /
//	model live on the separate step_finish event (a step-aggregate)
//	and do NOT appear on tool_use; the parser reads them defensively
//	in state.metadata so a future runtime that places them there
//	would round-trip. A missing M2 field is not a parse failure —
//	the persisted row carries NULL and the wrapper moves on.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jgabor/ktx/internal/store"
)

// maxAgentLine caps a single emitted JSON line. Agent tool outputs (e.g.,
// file listings) can be tens of MB; we keep enough headroom for PoC without
// reserving memory we don't need. Larger lines are skipped (passthrough).
const maxAgentLine = 64 * 1024 * 1024

func runRun(args []string, opts Options) int {
	return runRunWith(args, opts, os.Stdout, os.Stderr)
}

// runRunWith is the testable core of `ktx run`. stdout/stderr are injected
// so the run flow can be exercised without touching os.Stdout/Stderr.
func runRunWith(args []string, opts Options, stdout, stderr io.Writer) int {
	var showHelp, runQuiet bool
	fs := flag.NewFlagSet("ktx run", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")
	fs.BoolVar(&runQuiet, "quiet", false, "suppress run summary")
	fs.BoolVar(&runQuiet, "q", false, "suppress run summary (shorthand)")

	if err := fs.Parse(args); err != nil {
		printError(stderr, usageError("ktx run: %v", err), opts.Format)
		return ExitUsage
	}
	if showHelp {
		printRunHelp(stdout)
		return ExitOK
	}
	if runQuiet {
		opts.Quiet = true
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printError(stderr, usageError("ktx run: missing agent command; usage: ktx run [flags] <agent> [args...]"), opts.Format)
		return ExitUsage
	}

	// The global flag parser stops at the first non-flag (the subcommand
	// verb), so ktx flags that appear AFTER `run` (e.g., `ktx run
	// opencode run --format json "..." --quiet`) only reach the wrapper
	// through filterKtxFlags. Agent flags (anything else) pass through
	// untouched. The post-agent --quiet is honored alongside the
	// pre-agent --quiet handled by the run FlagSet above.
	postAgentQuiet, agentHelp, agentArgs := filterKtxFlags(rest[1:])
	if postAgentQuiet {
		opts.Quiet = true
	}
	if agentHelp {
		printRunHelp(stdout)
		return ExitOK
	}
	agent := rest[0]

	return executeRun(agent, agentArgs, opts, stdout, stderr)
}

// filterKtxFlags removes ktx-recognised flags from args. The recognised set
// is small and stable: --quiet/-q and --help/-h. Other flags and all
// positional arguments pass through to the agent. We intentionally do NOT
// recognise --format here — --format is a global flag and the wrapper
// always emits the run summary as JSON regardless of format.
//
// Both bare and --flag=value forms are accepted so the post-agent scan
// behaves like the global parser (stdlib flag.BoolVar). The pre-agent
// form is consumed by the run FlagSet; this helper is the post-agent
// fallback for users who type `ktx run <agent> ... --quiet`.
func filterKtxFlags(args []string) (quiet, showHelp bool, rest []string) {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "-q", "--quiet":
			quiet = true
		case "-h", "--help":
			showHelp = true
		default:
			rest = append(rest, a)
		}
	}
	return
}

// executeRun is the runtime body of `ktx run`. It opens the session,
// spawns the agent, parses tool events, persists them, closes the
// session, and emits the run summary. It returns the exit code that ktx
// run itself should report (the child's exit code, or an internal error
// code if session open fails).
func executeRun(agent string, agentArgs []string, opts Options, stdout, stderr io.Writer) int {
	start := time.Now()

	// Open session before spawning the child so any early child output
	// can be associated with the right session_id.
	path, err := store.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx run: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	db, err := store.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx run: open database: %v", err), opts.Format)
		return ExitInternal
	}
	defer db.Close()

	startedAt := start.UTC().Format(time.RFC3339Nano)
	// The session-file key is the wrapper's own PID (Decision 1 revised
	// inverted for the wrapper path): the child sees ktx run as its
	// parent, so a future hook on the child would look up the wrapper by
	// its ppid. KTX_SESSION_ID makes the hook noop in any case (D10).
	sessionID, err := db.OpenNamedSession(agent, os.Getpid(), startedAt)
	if err != nil {
		printError(stderr, internalError("ktx run: open session: %v", err), opts.Format)
		return ExitInternal
	}

	// Spawn the child with KTX_SESSION_ID exported (D10). The child
	// inherits the wrapper's stdin and stderr (the agent can read input
	// and write diagnostics directly); only stdout is intercepted for
	// tee + parse. Signal forwarding: SIGINT/SIGTERM received by the
	// wrapper are propagated to the child via context cancellation.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cmd := exec.CommandContext(ctx, agent, agentArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "KTX_SESSION_ID="+sessionID)

	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		printError(stderr, internalError("ktx run: stdout pipe: %v", err), opts.Format)
		return ExitInternal
	}

	if err := cmd.Start(); err != nil {
		// A missing agent binary is a user error (wrong name) rather than
		// a ktx internal fault; classify as usage so the exit code matches
		// the project's exit-code contract (Decision 4: 1 = usage).
		printError(stderr, usageError("ktx run: spawn %q: %v", agent, err), opts.Format)
		return ExitUsage
	}

	// Tee stdout: read line by line, write to caller's stdout
	// (passthrough), parse each line for tool events. Persistence is
	// inline so each tool_call is committed before the next line is
	// parsed — partial runs are crash-safe.
	toolCalls := teeAndParse(stdoutR, stdout, db, sessionID, stderr)

	waitErr := cmd.Wait()

	// Close the session immediately on child process exit (D1 revised:
	// wrapper closes immediately on child process exit). Close failure
	// is logged but does not fail the user's command — the agent ran,
	// and the user gets its exit code.
	endedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.CloseSession(sessionID, endedAt); err != nil {
		fmt.Fprintf(stderr, "ktx: warning: close session %q: %v\n", sessionID, err)
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

	if !opts.Quiet {
		writeRunSummary(stderr, toolCalls, time.Since(start), exitCode)
	}

	return exitCode
}

// teeAndParse reads from r, writes each line to stdout (transparent
// passthrough), and persists any tool-shaped event to db. Returns the
// number of tool_calls persisted. teeAndParse never returns an error:
// line read errors are logged but do not abort the tee; persist errors
// are logged per-call but do not abort the tee.
//
// Persistence is inline (synchronous) so each tool_call is committed
// before the next line is read. A persist failure is logged to stderr
// and the next line is processed.
func teeAndParse(r io.Reader, stdout io.Writer, db *store.DB, sessionID string, stderr io.Writer) int {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxAgentLine)
	var persisted int
	for scanner.Scan() {
		line := scanner.Bytes()
		// Passthrough: write the line as-is, then a newline so the
		// caller's stdout stays in sync with what the agent emitted
		// (scanner.Bytes() strips the trailing newline).
		_, _ = stdout.Write(line)
		_, _ = stdout.Write([]byte{'\n'})

		ev, ok := parseToolEvent(line)
		if !ok {
			continue
		}

		tc, err := buildToolCall(ev, sessionID)
		if err != nil {
			fmt.Fprintf(stderr, "ktx: warning: build tool_call: %v\n", err)
			continue
		}
		if err := db.PersistToolCall(tc); err != nil {
			// Don't fail the run; record the error so the user sees
			// it. The summary will reflect the partial count.
			fmt.Fprintf(stderr, "ktx: warning: persist tool_call: %v\n", err)
			continue
		}
		persisted++
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(stderr, "ktx: warning: read agent stdout: %v\n", err)
	}
	return persisted
}

// buildToolCall materialises a parsed event into the store's ToolCall
// shape (Decision 9). ID is always generated fresh — Decision 3
// mandates 16-char lowercase alpha nanoids, and runtime-supplied IDs
// (e.g., OpenCode's callID) do not necessarily match. Cross-referencing
// runtime IDs against ktx records is a post-M1 enhancement (Decision 9
// reserves the callID field shape but M1 does not store it).
//
// content_hash is derived from output. timestamp is normalised to
// RFC3339Nano so downstream readers do not need to disambiguate
// milliseconds vs seconds vs ISO strings.
//
// The M2 fields (TokensIn, TokensOut, Cost, LatencyMs, Model, Provider)
// are pointer-copied from the parsed event: a nil pointer round-trips
// to SQL NULL / JSON null (field absent in source), a non-nil pointer
// to zero round-trips to SQL 0 / JSON 0 (source emitted zero). The
// convention matches the store.ToolCall shape (Task 3) and preserves
// the reconstructability requirement from the cost-mechanics decision:
// 'absent' is not conflated with 'zero'.
func buildToolCall(ev *parsedEvent, sessionID string) (*store.ToolCall, error) {
	id, err := store.NewID()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}
	ts := ev.Timestamp
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339Nano)
	} else {
		ts = normaliseTimestamp(ts)
	}
	return &store.ToolCall{
		ID:          id,
		SessionID:   sessionID,
		Tool:        ev.Tool,
		Input:       ev.Input,
		Output:      ev.Output,
		ContentHash: store.ContentHash(ev.Output),
		Timestamp:   ts,
		EventType:   "tool_call",
		TokensIn:    ev.TokensIn,
		TokensOut:   ev.TokensOut,
		Cost:        ev.Cost,
		LatencyMs:   ev.LatencyMs,
		Model:       ev.Model,
		Provider:    ev.Provider,
	}, nil
}

// parsedEvent is the result of a successful parseToolEvent.
//
// The M2 fields (TokensIn, TokensOut, Cost, LatencyMs, Model, Provider)
// use pointer types so the absent-vs-zero distinction survives into
// buildToolCall: nil = "field absent in the source event" (round-trips
// to SQL NULL and JSON null); a non-nil pointer to 0 = "source emitted
// the field with value zero" (round-trips to SQL 0 and JSON 0).
//
// This mirrors the persistence-side convention in store.ToolCall
// (Task 3): the parse layer hands buildToolCall pointers that the
// struct boundary copies straight onto the store shape. A missing M2
// field in the source event is NOT a parse failure — the wrapper
// records the tool call with NULL M2 columns and moves on.
type parsedEvent struct {
	ID        string
	Tool      string
	Input     string
	Output    string
	Timestamp string

	// M2 cost-publication fields. nil = absent in the source event.
	// Populated by parseToolEvent when the event carries the field;
	// buildToolCall copies the pointer through to the persisted row.
	TokensIn  *int64
	TokensOut *int64
	Cost      *float64
	LatencyMs *int64
	Model     *string
	Provider  *string
}

// parseToolEvent returns (event, true) if line looks like a tool call,
// (nil, false) otherwise. Malformed JSON or non-tool events return
// (nil, false) so the caller can passthrough unchanged.
//
// Two schemas are recognised:
//
//  1. OpenCode: top.type == "tool_use" and part.type == "tool". The
//     tool name, input, and output live in part.state. M2 extraction
//     from this shape:
//       - latency_ms: computed from part.state.time.end - .start when
//         both are present (OpenCode always emits both on a completed
//         state). If only one is present, latency_ms stays nil — a
//         missing field is not a parse failure.
//       - provider: defaulted to "opencode" (runtime-known single
//         exception; the wrapper knows its agent). Documented as the
//         one place "don't guess" yields to "wrapper knows".
//       - tokens_in / tokens_out / cost / model: the OpenCode runtime
//         does NOT emit these on tool_use events; they live on the
//         separate step_finish event (a step-aggregate, not per-tool).
//         The parser defensively reads part.state.metadata and the
//         top-level event for these fields so a future OpenCode shape
//         or a custom adapter that does emit them would round-trip.
//         Real OpenCode 1.17.8 events leave these nil — documented in
//         .agentera/verification/m4-task-4-acceptance.md.
//
//  2. Flat fallback: a top-level object with tool/name and (input|args)
//     and (output|result) fields. Optional id/timestamp/time aliases.
//     M2 fields are read defensively from the top level so a runtime
//     that emits M2 fields flat (no nesting) would also round-trip.
//
// Unknown event types ("text", "step_start", "step_finish", etc.) are
// treated as non-tool events and skipped without error.
func parseToolEvent(line []byte) (*parsedEvent, bool) {
	if len(line) == 0 {
		return nil, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(line, &top); err != nil {
		return nil, false
	}
	if len(top) == 0 {
		return nil, false
	}

	var eventType string
	_ = json.Unmarshal(top["type"], &eventType)

	// OpenCode schema: tool_use events nest the tool details under
	// "part". Only events with part.type == "tool" are tool calls.
	if eventType == "tool_use" {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(top["part"], &part); err != nil {
			return nil, false
		}
		var partType string
		_ = json.Unmarshal(part["type"], &partType)
		if partType != "tool" {
			return nil, false
		}
		ev := &parsedEvent{}
		_ = json.Unmarshal(part["tool"], &ev.Tool)
		_ = json.Unmarshal(part["callID"], &ev.ID)
		if ev.ID == "" {
			_ = json.Unmarshal(part["id"], &ev.ID)
		}
		var state map[string]json.RawMessage
		if err := json.Unmarshal(part["state"], &state); err == nil {
			ev.Input = jsonValueToString(state["input"])
			ev.Output = jsonValueToString(state["output"])
			ev.LatencyMs = latencyFromState(state["time"])
			// Defensive M2 reads inside state.metadata. OpenCode's
			// bash/edit/etc. tools populate state.metadata with tool-
			// specific output (output, exit, description, truncated)
			// but not with tokens/cost/model — those live on
			// step_finish events. The reads below would surface them
			// if a future runtime or custom adapter adds them.
			var md map[string]json.RawMessage
			if err := json.Unmarshal(state["metadata"], &md); err == nil {
				ev.TokensIn = int64PtrFromRaw(md["tokens_in"], md["tokensIn"], md["input_tokens"])
				ev.TokensOut = int64PtrFromRaw(md["tokens_out"], md["tokensOut"], md["output_tokens"])
				ev.Cost = float64PtrFromRaw(md["cost"], md["cost_usd"])
				ev.Model = stringPtrFromRaw(md["model"], md["model_id"])
				ev.Provider = stringPtrFromRaw(md["provider"], md["provider_id"])
			}
		}
		_ = json.Unmarshal(top["timestamp"], &ev.Timestamp)
		// Runtime-known default for the OpenCode adapter: the wrapper
		// knows its agent. If the event later carries a provider field
		// (none today), the defensive read above wins.
		if ev.Provider == nil {
			s := "opencode"
			ev.Provider = &s
		}
		if ev.Tool == "" {
			return nil, false
		}
		return ev, true
	}

	// Flat fallback. Treat any JSON object with a tool/name field as a
	// tool event; recognise optional id/input/args/output/result/time
	// aliases. Known non-tool event types ("text", "step_start",
	// "step_finish") are rejected so non-tool noise does not get
	// recorded.
	switch eventType {
	case "text", "step_start", "step_finish", "":
		// "" allowed: not every runtime emits a type field.
	default:
		// Unknown explicit type — refuse to guess.
		return nil, false
	}

	var tool string
	_ = json.Unmarshal(top["tool"], &tool)
	if tool == "" {
		_ = json.Unmarshal(top["name"], &tool)
	}
	if tool == "" {
		return nil, false
	}

	ev := &parsedEvent{Tool: tool}
	_ = json.Unmarshal(top["id"], &ev.ID)
	ev.Input = jsonValueToString(firstNonEmptyRaw(top["input"], top["args"], top["parameters"]))
	ev.Output = jsonValueToString(firstNonEmptyRaw(top["output"], top["result"], top["content"]))
	_ = json.Unmarshal(top["timestamp"], &ev.Timestamp)
	if ev.Timestamp == "" {
		_ = json.Unmarshal(top["time"], &ev.Timestamp)
	}
	// Defensive M2 reads on the flat shape so a runtime that emits
	// M2 fields at the top level (no nesting) round-trips through the
	// wrapper. Real OpenCode events reach the OpenCode branch above
	// and never exercise this path; flat-fallback runtimes that do
	// not emit M2 fields leave the pointers nil.
	ev.LatencyMs = latencyFromState(firstNonEmptyRaw(top["time"], top["duration"]))
	ev.TokensIn = int64PtrFromRaw(top["tokens_in"], top["tokensIn"], top["input_tokens"])
	ev.TokensOut = int64PtrFromRaw(top["tokens_out"], top["tokensOut"], top["output_tokens"])
	ev.Cost = float64PtrFromRaw(top["cost"], top["cost_usd"])
	ev.Model = stringPtrFromRaw(top["model"], top["model_id"])
	ev.Provider = stringPtrFromRaw(top["provider"], top["provider_id"])
	return ev, true
}

// firstNonEmptyRaw returns the first non-empty RawMessage in the list.
func firstNonEmptyRaw(values ...json.RawMessage) json.RawMessage {
	for _, v := range values {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

// latencyFromState returns the millisecond delta between state.time.end
// and state.time.start when both are present and numeric. A missing
// start or end, a non-numeric value, or an end-before-start returns nil
// — the wrapper records the tool_call with NULL latency_ms rather than
// guess. OpenCode's tool_use event always emits both fields on a
// completed state, so a successful extraction is the common case for
// real OpenCode events; the nil return is reserved for malformed or
// partial inputs.
func latencyFromState(raw json.RawMessage) *int64 {
	if len(raw) == 0 {
		return nil
	}
	var tm struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	}
	if err := json.Unmarshal(raw, &tm); err != nil {
		return nil
	}
	if tm.Start == 0 || tm.End == 0 || tm.End < tm.Start {
		return nil
	}
	delta := tm.End - tm.Start
	return &delta
}

// int64PtrFromRaw returns the first non-empty RawMessage that parses as
// a JSON number, as a pointer to int64. Empty input, malformed JSON,
// or non-numeric content returns nil — the same 'missing field is not
// a parse failure' rule the wrapper applies to top-level M2 fields.
func int64PtrFromRaw(values ...json.RawMessage) *int64 {
	for _, v := range values {
		if len(v) == 0 {
			continue
		}
		var n int64
		if err := json.Unmarshal(v, &n); err == nil {
			return &n
		}
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			i := int64(f)
			return &i
		}
	}
	return nil
}

// float64PtrFromRaw mirrors int64PtrFromRaw for floating-point fields.
func float64PtrFromRaw(values ...json.RawMessage) *float64 {
	for _, v := range values {
		if len(v) == 0 {
			continue
		}
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			return &f
		}
		var n int64
		if err := json.Unmarshal(v, &n); err == nil {
			val := float64(n)
			return &val
		}
	}
	return nil
}

// stringPtrFromRaw returns the first non-empty RawMessage that parses
// as a non-empty JSON string, as a pointer to string. Empty input,
// malformed JSON, missing values, or empty strings yield nil — the
// 'absent vs empty string' distinction matters at the store boundary
// (an empty string would round-trip as a non-NULL "" column rather
// than a NULL column).
func stringPtrFromRaw(values ...json.RawMessage) *string {
	for _, v := range values {
		if len(v) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			return &s
		}
	}
	return nil
}

// jsonValueToString returns a string representation of a JSON value.
// Strings are returned unquoted; other types (objects, arrays, numbers,
// booleans) are re-encoded to JSON text. Empty input yields an empty
// string so callers do not have to guard against nil.
func jsonValueToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return string(raw)
}

// normaliseTimestamp converts an integer Unix timestamp (seconds or
// milliseconds) to RFC3339Nano. Strings are returned as-is so the caller
// can supply an ISO-8601 timestamp directly. The seconds-vs-ms
// disambiguation uses magnitude: anything >= 1e10 is milliseconds
// (modern Unix ms is ~1.7e12, modern Unix seconds is ~1.7e9; the
// crossover at year ~2286 in seconds vs ~1970 in milliseconds makes
// this safe for any realistic runtime).
func normaliseTimestamp(ts string) string {
	if ts == "" {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
		if n >= 1e10 {
			return time.UnixMilli(n).UTC().Format(time.RFC3339Nano)
		}
		return time.Unix(n, 0).UTC().Format(time.RFC3339Nano)
	}
	return ts
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
func printRunHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx run — wrap an agent invocation and record the session")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx run [flags] <agent> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Spawns <agent> as a child process with KTX_SESSION_ID exported into")
	fmt.Fprintln(w, "the environment. The agent's stdout passes through to ktx's stdout")
	fmt.Fprintln(w, "untouched. Each newline-delimited JSON event is parsed; tool events")
	fmt.Fprintln(w, "are persisted to the database as tool_calls (Decision 10).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "When the child exits, the session is closed immediately (ended_at set).")
	fmt.Fprintln(w, "ktx run's exit code matches the child's exit code.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -q, --quiet    suppress the one-line run summary on stderr")
	fmt.Fprintln(w, "  -h, --help     show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Note: --quiet/-q may also appear after the agent command; the wrapper")
	fmt.Fprintln(w, "recognises it anywhere on its command line. Other flags pass through")
	fmt.Fprintln(w, "to the agent.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Example:")
	fmt.Fprintln(w, "  ktx run opencode run --format json \"explain closures in JavaScript\"")
	fmt.Fprintln(w, "  ktx --quiet run opencode run --format json --model anthropic/claude-sonnet-4-20250514 \"fix the bug\"")
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
