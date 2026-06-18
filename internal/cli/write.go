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
// liveness + PID-reuse guards. The resolution logic lives in the store
// package; this file owns the I/O (stdin, stdout, errors, dispatch).
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jgabor/ktx/internal/store"
)

func runWrite(args []string, opts Options) int {
	return runWriteWith(args, opts, os.Stdin, os.Stdout, os.Stderr)
}

func runWriteWith(args []string, opts Options, stdin io.Reader, stdout, stderr io.Writer) int {
	var showHelp bool
	fs := flag.NewFlagSet("ktx write", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		printError(stderr, usageError("ktx write: %v", err), opts.Format)
		return ExitUsage
	}
	if showHelp {
		printWriteHelp(stdout)
		return ExitOK
	}
	if len(fs.Args()) > 0 {
		printError(stderr, usageError("ktx write: unexpected argument %q; input is read from stdin", fs.Arg(0)), opts.Format)
		return ExitUsage
	}

	body, err := io.ReadAll(stdin)
	if err != nil {
		printError(stderr, internalError("ktx write: read stdin: %v", err), opts.Format)
		return ExitInternal
	}
	if len(body) == 0 {
		printError(stderr, usageError("ktx write: no input on stdin"), opts.Format)
		return ExitUsage
	}

	var tc store.ToolCall
	if err := json.Unmarshal(body, &tc); err != nil {
		printError(stderr, usageError("ktx write: parse JSON: %v", err), opts.Format)
		return ExitUsage
	}

	eventType := tc.EventType
	if eventType == "" {
		eventType = "tool_call"
	}

	// Database path resolution (Decision 8): KTX_DB env > default. ktx write
	// does not take a --path flag; per-invocation override lives on `ktx db`.
	path, err := store.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx write: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	db, err := store.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx write: open database: %v", err), opts.Format)
		return ExitInternal
	}
	defer db.Close()

	switch eventType {
	case "tool_call":
		return writeToolCall(&tc, db, opts, stdout, stderr)
	case "session_close":
		return writeClose(&tc, db, opts, stdout, stderr)
	default:
		printError(stderr, usageError("ktx write: unknown event_type %q (want %q or %q)", eventType, "tool_call", "session_close"), opts.Format)
		return ExitUsage
	}
}

// writeToolCall validates the required fields, resolves the session id, and
// persists the record. The record ID (tc.ID) is printed as raw JSON.
func writeToolCall(tc *store.ToolCall, db *store.DB, opts Options, stdout, stderr io.Writer) int {
	// Required structural fields (Decision 9). id, tool, and timestamp are
	// meaningless if empty; input, output, and content_hash are accepted as
	// supplied (including empty strings, e.g. a tool with no output).
	if tc.ID == "" {
		printError(stderr, usageError("ktx write: missing required field: id"), opts.Format)
		return ExitUsage
	}
	if tc.Tool == "" {
		printError(stderr, usageError("ktx write: missing required field: tool"), opts.Format)
		return ExitUsage
	}
	if tc.Timestamp == "" {
		printError(stderr, usageError("ktx write: missing required field: timestamp"), opts.Format)
		return ExitUsage
	}

	// Resolve session id: caller-provided (sidecar) or ppid-based (adapter).
	sessionID, err := db.ResolveSessionID(tc.SessionID, tc.Timestamp)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			printError(stderr, notFoundError("ktx write: session %q not found", tc.SessionID), opts.Format)
			return ExitNotFound
		}
		printError(stderr, internalError("ktx write: resolve session: %v", err), opts.Format)
		return ExitInternal
	}
	tc.SessionID = sessionID

	if err := db.PersistToolCall(tc); err != nil {
		printError(stderr, internalError("ktx write: persist tool_call: %v", err), opts.Format)
		return ExitInternal
	}

	printID(stdout, tc.ID)
	return ExitOK
}

// writeClose handles the session_close payload (Decision 9): sets ended_at on
// the session row. The session_id is printed as raw JSON so the caller can
// confirm which session was closed.
func writeClose(tc *store.ToolCall, db *store.DB, opts Options, stdout, stderr io.Writer) int {
	if tc.SessionID == "" {
		printError(stderr, usageError("ktx write: session_close requires session_id"), opts.Format)
		return ExitUsage
	}
	if tc.Timestamp == "" {
		printError(stderr, usageError("ktx write: session_close requires timestamp"), opts.Format)
		return ExitUsage
	}

	if err := db.CloseSession(tc.SessionID, tc.Timestamp); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			printError(stderr, notFoundError("ktx write: session %q not found", tc.SessionID), opts.Format)
			return ExitNotFound
		}
		printError(stderr, internalError("ktx write: close session: %v", err), opts.Format)
		return ExitInternal
	}

	printID(stdout, tc.SessionID)
	return ExitOK
}

// printID writes the record ID as raw JSON (a JSON string) to stdout. ktx
// write is an I/O primitive (Decision 2/8): the output is always raw JSON,
// never text-formatted, regardless of --format.
func printID(w io.Writer, id string) {
	_ = json.NewEncoder(w).Encode(id)
}

// printWriteHelp writes the `ktx write` help.
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
