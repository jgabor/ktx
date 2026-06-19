// `ktx get` — the QUERY command that fetches a persisted record by nanoid
// ID and prints it in a human-friendly form (Decisions 2, 4, 7, 8).
//
// `ktx get` is the query-layer counterpart to the I/O primitive `ktx read`:
// both address the same records by the same 16-char nanoid (tool_calls
// checked first, sessions second; Decision 7). The difference is the
// output contract:
//
//   - ktx read (I/O):   raw JSON, always. The I/O surface is mechanical;
//     reversibility demands byte-faithful round-trips.
//   - ktx get  (query): JSON default with --format text for humans. The
//     query layer is allowed to add formatting; this is
//     where the agent's "show me" ergonomics live.
//
// Today, --format text is a one-line-per-field summary (key: value), which
// keeps the get-vs-read difference minimal but visible. Future M3/M4 query
// commands (ktx bench, ktx compress) can layer richer formatting on the
// same `text` convention without changing the I/O surface.
//
// Database path resolution (Decision 8): KTX_DB env > default. ktx get
// does not take a --path flag; per-invocation override lives on `ktx db`.
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

func runGet(args []string, opts Options) int {
	return runGetWith(args, opts, os.Stdout, os.Stderr)
}

// runGetWith is the testable core of `ktx get`. stdout/stderr are
// injected so the get flow can be exercised without touching os.Stdout.
func runGetWith(args []string, opts Options, stdout, stderr io.Writer) int {
	var showHelp bool
	// Subcommand-local --format. The global --format is already in
	// opts.Format (parsed by the dispatcher from the front of args), but
	// the stdlib flag package stops at the first non-flag, so flags
	// appearing AFTER a positional (e.g. `ktx get <id> --format text`)
	// are not seen by the global parser. extractFormat pulls --format
	// out of the args (in either position); the subcommand's value
	// takes precedence over the global one when both are set.
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}

	fs := flag.NewFlagSet("ktx get", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(rest); err != nil {
		printError(stderr, usageError("ktx get: %v", err), format)
		return ExitUsage
	}
	if showHelp {
		printGetHelp(stdout)
		return ExitOK
	}
	if format != FormatJSON && format != FormatText {
		printError(stderr, usageError("ktx get: invalid --format %q: want %q or %q", format, FormatJSON, FormatText), format)
		return ExitUsage
	}

	rest = fs.Args()
	if len(rest) == 0 {
		printError(stderr, usageError("ktx get: missing required argument: <id>"), format)
		return ExitUsage
	}
	if len(rest) > 1 {
		printError(stderr, usageError("ktx get: unexpected argument %q; ktx get takes a single ID", rest[1]), format)
		return ExitUsage
	}
	id := rest[0]

	path, err := store.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx get: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := store.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx get: open database: %v", err), format)
		return ExitInternal
	}
	defer db.Close()

	record, err := db.GetRecord(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			printError(stderr, notFoundError("ktx get: record %q not found", id), format)
			return ExitNotFound
		}
		printError(stderr, internalError("ktx get: get record: %v", err), format)
		return ExitInternal
	}

	switch format {
	case FormatText:
		printGetText(stdout, record)
	default:
		// JSON default. The record is either *store.ToolCall or
		// *store.Session, both already JSON-tagged; pass it through
		// directly. json.NewEncoder writes a trailing newline, which
		// keeps stdout pipe-friendly.
		if err := json.NewEncoder(stdout).Encode(record); err != nil {
			printError(stderr, internalError("ktx get: write JSON: %v", err), format)
			return ExitInternal
		}
	}
	return ExitOK
}

// printGetText writes a one-line-per-field summary of the record. The
// field set is determined by the record type so tool_call and session
// each get a faithful (not generic) text view. Multi-line fields
// (input, output) are rendered on a single line — the query layer's
// text format is for skim-reading, not for inspecting large payloads.
//
// For tool_call records, an M2 cost-publication section is appended
// below the M1 fields when at least one M2 pointer is non-nil
// (Task 5, AC3). The section is silent when all M2 fields are NULL
// (AC4): no "0" / "n/a" / blank placeholders — absence is signalled
// by the section's non-existence. Pointer discipline is preserved
// end-to-end: a nil pointer is not coerced to 0 just because the
// renderer is rendering a column.
func printGetText(w io.Writer, record any) {
	switch r := record.(type) {
	case *store.ToolCall:
		fmt.Fprintf(w, "id:           %s\n", r.ID)
		fmt.Fprintf(w, "session_id:   %s\n", r.SessionID)
		fmt.Fprintf(w, "tool:         %s\n", r.Tool)
		fmt.Fprintf(w, "input:        %s\n", r.Input)
		fmt.Fprintf(w, "output:       %s\n", r.Output)
		fmt.Fprintf(w, "content_hash: %s\n", r.ContentHash)
		fmt.Fprintf(w, "timestamp:    %s\n", r.Timestamp)
		fmt.Fprintf(w, "event_type:   %s\n", r.EventType)
		// M2 cost-publication section (Task 5). Rendered only when at
		// least one M2 pointer is non-nil. Field name column is padded
		// to 12 chars after the 2-space indent, matching the M1
		// section's visual alignment.
		if r.TokensIn != nil || r.TokensOut != nil || r.Cost != nil ||
			r.LatencyMs != nil || r.Model != nil || r.Provider != nil {
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "M2:")
			if r.TokensIn != nil {
				fmt.Fprintf(w, "  tokens_in:    %d\n", *r.TokensIn)
			}
			if r.TokensOut != nil {
				fmt.Fprintf(w, "  tokens_out:   %d\n", *r.TokensOut)
			}
			if r.Cost != nil {
				fmt.Fprintf(w, "  cost:         %.4f\n", *r.Cost)
			}
			if r.LatencyMs != nil {
				fmt.Fprintf(w, "  latency_ms:   %d\n", *r.LatencyMs)
			}
			if r.Model != nil {
				fmt.Fprintf(w, "  model:        %s\n", *r.Model)
			}
			if r.Provider != nil {
				fmt.Fprintf(w, "  provider:     %s\n", *r.Provider)
			}
		}
	case *store.Session:
		fmt.Fprintf(w, "id:              %s\n", r.ID)
		fmt.Fprintf(w, "agent:           %s\n", r.Agent)
		fmt.Fprintf(w, "started_at:      %s\n", r.StartedAt)
		fmt.Fprintf(w, "ended_at:        %s\n", r.EndedAt)
		fmt.Fprintf(w, "last_seen_at:    %s\n", r.LastSeenAt)
		fmt.Fprintf(w, "tool_call_count: %d\n", r.ToolCallCount)
	default:
		// Defensive: GetRecord only returns *ToolCall or *Session. If a
		// future store change adds a new variant, fall back to JSON so
		// the user gets something rather than silence.
		_ = json.NewEncoder(w).Encode(record)
	}
}

// printGetHelp writes the `ktx get` help. Mirrors `ktx read --help` style
// so the two related commands present consistently.
func printGetHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx get — query a record by ID (formatted)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx get [--format text|json] <id>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Fetches a record (tool_call or session) from the database by its 16-char")
	fmt.Fprintln(w, "nanoid ID. tool_calls are checked first; sessions are checked second")
	fmt.Fprintln(w, "(Decision 7: both tables are addressed by nanoid ID).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  the record as stored, one JSON object on stdout")
	fmt.Fprintln(w, "  text            one-line-per-field summary, keyed by field name")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  record found and printed")
	fmt.Fprintln(w, "  1  usage error (missing/invalid argument, unknown flag)")
	fmt.Fprintln(w, "  2  record not found")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx read   raw JSON read of a record (I/O primitive, no --format)")
	fmt.Fprintln(w, "  ktx list   list all sessions in the database")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
