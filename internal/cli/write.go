// `ktx write` — the I/O command that persists a tool_call record to the
// database (Decisions 1, 2, 7, 8, 9). It reads a flat JSON tool_call from
// stdin, resolves the session identity (caller-provided or ppid-based), and
// persists the record. Output is the persisted record ID as raw JSON on
// stdout — no --format flag (I/O primitive, Decision 2/8).
//
// Two payload shapes share one command via event_type (Decision 9):
//   - tool_call (default): a full flat tool_call record.
//   - session_close: just session_id + timestamp; sets ended_at on the
//     session row (sidecar close path, D1 revised).
//
// Session resolution when session_id is absent follows Decision 1 revised:
// ppid file lookup at $XDG_RUNTIME_DIR/ktx/sessions/<ppid>.json, with
// liveness + PID-reuse guards. The resolution logic lives in the db package
// package; this file owns the I/O (stdin, stdout, errors, dispatch).
//
// Task 5 migration: the verb registers via dispatch.Register with
// NeedsDB=true (dispatcher opens the DB), NeedsStdin=true (dispatcher
// exposes os.Stdin via ctx.Stdin), and PassthroughStdout=true (verb
// writes raw JSON, no envelope wrapping). The legacy runWriteWith
// remains the test seam and still opens its own DB so existing Task 3
// tests pass unchanged.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runWriteWith is the testable core of `ktx write` (Task 1-3 seam).
// stdin/stdout/stderr are injected so the write flow can be exercised
// without touching os.Stdin or os.Stdout. The body opens the database,
// delegates to writeLogic for the actual work, and translates the
// returned error into an exit-code + stderr envelope.
func runWriteWith(args []string, opts Options, stdin io.Reader, stdout, stderr io.Writer) int {
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx write: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	db, err := dbpkg.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx write: open database: %v", err), opts.Format)
		return ExitInternal
	}
	defer db.Close()

	_, err = writeLogic(args, stdin, db, stdout)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// writeLogic is the shared core of `ktx write`. Both runWriteWith (the
// legacy test seam) and the dispatch wrapper writeCommandFunc call it.
// It assumes the database is already opened and reads from stdin
// directly. Returns (any, error) so the dispatcher can map the error
// envelope through its own printError + exitCodeOf pipeline.
//
// Step-by-step:
//  1. Parse --help/-h.
//  2. If --help, render printWriteHelp and return.
//  3. Reject trailing positionals (input is read from stdin).
//  4. Read stdin body; reject empty body and malformed JSON.
//  5. Dispatch on event_type: tool_call or session_close.
func writeLogic(args []string, stdin io.Reader, db *dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx write", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx write: %v", err)
	}
	if showHelp {
		printWriteHelp(stdout)
		return nil, nil
	}
	if len(fs.Args()) > 0 {
		return nil, usageError("ktx write: unexpected argument %q; input is read from stdin", fs.Arg(0))
	}

	body, err := io.ReadAll(stdin)
	if err != nil {
		return nil, internalError("ktx write: read stdin: %v", err)
	}
	if len(body) == 0 {
		return nil, usageError("ktx write: no input on stdin")
	}

	var tc dbpkg.ToolCall
	if err := json.Unmarshal(body, &tc); err != nil {
		return nil, usageError("ktx write: parse JSON: %v", err)
	}

	eventType := tc.EventType
	if eventType == "" {
		eventType = "tool_call"
	}

	switch eventType {
	case "tool_call":
		return writeToolCallLogic(&tc, db, stdout)
	case "session_close":
		return writeCloseLogic(&tc, db, stdout)
	default:
		return nil, usageError("ktx write: unknown event_type %q (want %q or %q)", eventType, "tool_call", "session_close")
	}
}

// writeToolCallLogic validates the required fields (id, tool,
// timestamp), resolves the session id, fills in the content hash if
// missing, and persists the record via db.PersistToolCall. The record
// ID (tc.ID) is printed as raw JSON.
func writeToolCallLogic(tc *dbpkg.ToolCall, db *dbpkg.DB, stdout io.Writer) (any, error) {
	// Required structural fields (Decision 9). id, tool, and timestamp are
	// meaningless if empty; input, output, and content_hash are accepted as
	// supplied (including empty strings, e.g. a tool with no output).
	if tc.ID == "" {
		return nil, usageError("ktx write: missing required field: id")
	}
	if tc.Tool == "" {
		return nil, usageError("ktx write: missing required field: tool")
	}
	if tc.Timestamp == "" {
		return nil, usageError("ktx write: missing required field: timestamp")
	}

	// Resolve session id: caller-provided (sidecar) or ppid-based (adapter).
	sessionID, err := db.ResolveSessionID(tc.SessionID, tc.Timestamp)
	if err != nil {
		if errors.Is(err, dbpkg.ErrNotFound) {
			return nil, notFoundError("ktx write: session %q not found", tc.SessionID)
		}
		return nil, internalError("ktx write: resolve session: %v", err)
	}
	tc.SessionID = sessionID

	if tc.ContentHash == "" && tc.Output != "" {
		tc.ContentHash = dbpkg.ContentHash(tc.Output)
	}

	if err := db.PersistToolCall(tc); err != nil {
		return nil, internalError("ktx write: persist tool_call: %v", err)
	}

	printID(stdout, tc.ID)
	return nil, nil
}

// writeCloseLogic handles the session_close payload (Decision 9):
// sets ended_at on the session row via db.CloseSession. The
// session_id is printed as raw JSON so the caller can confirm which
// session was closed.
func writeCloseLogic(tc *dbpkg.ToolCall, db *dbpkg.DB, stdout io.Writer) (any, error) {
	if tc.SessionID == "" {
		return nil, usageError("ktx write: session_close requires session_id")
	}
	if tc.Timestamp == "" {
		return nil, usageError("ktx write: session_close requires timestamp")
	}

	if err := db.CloseSession(tc.SessionID, tc.Timestamp); err != nil {
		if errors.Is(err, dbpkg.ErrNotFound) {
			return nil, notFoundError("ktx write: session %q not found", tc.SessionID)
		}
		return nil, internalError("ktx write: close session: %v", err)
	}

	printID(stdout, tc.SessionID)
	return nil, nil
}

// writeCommandFunc is the dispatch wrapper. The dispatcher already
// opened the DB (NeedsDB=true), exposed os.Stdin via ctx.Stdin
// (NeedsStdin=true), and parsed --help/--format. The verb receives a
// *DispatchContext carrying the populated DB, stdin, and a clean
// positional Args tail. The verb's only responsibility is to apply
// writeLogic and surface the typed error to the dispatcher's
// printError + exitCodeOf pipeline.
func writeCommandFunc(ctx *DispatchContext) (any, error) {
	return writeLogic(ctx.Args, ctx.Stdin, ctx.DB, ctx.Stdout)
}

// init registers `ktx write` with the dispatch module. After init
// runs, the verb is reachable through runWith (the legacy
// commandIndex entry in cli.go is removed in Task 5).
func init() {
	Register("write", RegisterOptions{
		Description:       "persist a tool_call record from sidecar input",
		NeedsDB:           true,
		NeedsStdin:        true,
		PassthroughStdout: true,
		Help:              printWriteHelp,
	}, writeCommandFunc)
}

// printID writes the record ID as raw JSON (a JSON string) to stdout. ktx
// write is an I/O primitive (Decision 2/8): the output is always raw JSON,
// never text-formatted, regardless of --format.
func printID(w io.Writer, id string) {
	_ = json.NewEncoder(w).Encode(id)
}

// printWriteHelp writes the `ktx write` help. Used both as the
// dispatcher's Help hook (renderHelp invokes it on --help) and from
// writeLogic when runWriteWith is invoked directly with --help.
func printWriteHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx write — persist a tool_call record from sidecar input")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx write < stdin")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Reads a flat JSON tool_call from stdin and persists it to the database.")
	fmt.Fprintln(w, "Prints the persisted record ID as raw JSON to stdout.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Input fields (Decision 9):")
	fmt.Fprintln(w, "  Required:")
	fmt.Fprintln(w, "    id            record ID (16-char lowercase alpha nanoid)")
	fmt.Fprintln(w, "    session_id    session ID (auto-created via ppid lookup if absent)")
	fmt.Fprintln(w, "    tool          tool name")
	fmt.Fprintln(w, "    input         tool input")
	fmt.Fprintln(w, "    output        tool output")
	fmt.Fprintln(w, "    content_hash  SHA-256 of output")
	fmt.Fprintln(w, "    timestamp     event timestamp (caller-supplied string)")
	fmt.Fprintln(w, "  Optional:")
	fmt.Fprintln(w, "    tokens_in, tokens_out, cost, latency_ms, model, provider (M2)")
	fmt.Fprintln(w, "    event_type    \"tool_call\" (default) or \"session_close\"")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "When session_id is absent, ktx resolves the session by ppid file lookup")
	fmt.Fprintln(w, "at $XDG_RUNTIME_DIR/ktx/sessions/<ppid>.json and reuses it if the parent")
	fmt.Fprintln(w, "process is alive and the start-time matches; otherwise it closes the old")
	fmt.Fprintln(w, "session and opens a new one.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output: raw JSON (the record ID). No --format flag.")
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}