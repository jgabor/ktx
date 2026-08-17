// `ktx get` — the QUERY command that fetches a persisted record by nanoid
// ID and prints it in a human-readable form.
//
// `ktx get` is the query-layer counterpart to the I/O primitive `ktx read`:
// both address the same records by the same 16-char nanoid (tool_calls
// checked first, sessions second). The difference is the
// output contract:
//
//   - ktx read (I/O):   raw JSON, always. The I/O surface is mechanical;
//     reversibility demands byte-faithful round-trips.
//   - ktx get  (query): JSON default with --format text for humans. The
//     query layer is allowed to add formatting; this is
//     where the agent's "show me" ergonomics live.
//
// The text format is a one-line-per-field summary (key: value).
//
// Database path resolution: KTX_DB env > default. ktx get
// does not take a --path flag; per-invocation override lives on `ktx db`.
//
// JSON is the default output format.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runGetWith is the testable core of `ktx get`.
// stdout/stderr are injected so the get flow can be exercised without
// touching os.Stdout. The body parses --format, opens the database,
// delegates to getLogic for the actual work, and translates the
// returned error into an exit-code + stderr envelope.
func runGetWith(args []string, opts Options, stdout, stderr io.Writer) int {
	// Subcommand-local --format. The dispatch module's runRegisteredVerb
	// already extracts --format from the args when the verb is invoked
	// through dispatch; for direct runGetWith callers,
	// extract it here so the call sites have identical semantics.
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}

	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx get: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := openQueryDatabase(path, "get")
	if err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
	}
	defer db.Close()

	_, err = getLogic(rest, format, db, stdout)
	if err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// getLogic reads and renders one record from an open database. The caller
// resolves the output format before invoking it.
//
// Step-by-step:
//
//  1. Parse --help/-h.
//  2. If --help, render printGetHelp and return.
//  3. Validate --format is json or text.
//  4. Validate exactly one positional (the ID).
//  5. db.GetRecord(id): tool_calls first, then sessions.
//  6. Switch on format: text → printGetText, default → json.Encode.
//
// Anything that fails becomes a typed *Error (usage / not-found /
// internal) so the caller can map it to the right exit code.
func getLogic(args []string, format string, db *dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx get", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx get: %v", err)
	}
	if showHelp {
		printGetHelp(stdout)
		return nil, nil
	}
	if format != FormatJSON && format != FormatText {
		return nil, usageError("ktx get: invalid --format %q: want %q or %q", format, FormatJSON, FormatText)
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return nil, usageError("ktx get: missing required argument: <id>")
	}
	if len(rest) > 1 {
		return nil, usageError("ktx get: unexpected argument %q; ktx get takes a single ID", rest[1])
	}
	id := rest[0]

	record, err := db.GetRecord(id)
	if err != nil {
		if errors.Is(err, dbpkg.ErrNotFound) {
			return nil, notFoundError("ktx get: record %q not found", id)
		}
		return nil, internalError("ktx get: get record: %v", err)
	}

	switch format {
	case FormatText:
		printGetText(stdout, record)
	default:
		// JSON default. The record is either *dbpkg.ToolCall or
		// *dbpkg.Session, both already JSON-tagged; pass it through
		// directly. json.NewEncoder writes a trailing newline, which
		// keeps stdout pipe-friendly.
		if err := json.NewEncoder(stdout).Encode(record); err != nil {
			return nil, internalError("ktx get: write JSON: %v", err)
		}
	}
	return nil, nil
}

// getCommandFunc is the dispatch wrapper. The dispatcher already opened
// the DB read-only, parsed --help/--format, and resolved the
// format. The verb receives a *DispatchContext carrying the populated
// DB, the resolved format, and a clean positional Args tail. The verb's
// only responsibility is to apply getLogic and surface the typed
// error to the dispatcher's printError + exitCodeOf pipeline.
func getCommandFunc(ctx *DispatchContext) (any, error) {
	return getLogic(ctx.Args, ctx.Format, ctx.DB, ctx.Stdout)
}

// init registers `ktx get` with the dispatch module. After init runs,
// the verb is reachable through runWith.
func init() {
	Register("get", RegisterOptions{
		Description:    "query a record by ID (formatted)",
		DatabaseAccess: DatabaseReadOnly,
		FormatDefault:  FormatJSON,
		Help:           printGetHelp,
	}, getCommandFunc)
}

// printGetText writes a one-line-per-field summary of the record. The
// field set is determined by the record type so tool_call and session
// each get a faithful (not generic) text view. Multi-line fields
// (input, output) are rendered on a single line — the query layer's
// text format is for skim-reading, not for inspecting large payloads.
//
// For tool_call records, a cost-publication section is appended
// below the main fields when at least one cost pointer is non-nil.
// The section is silent when all cost fields are NULL:
// no "0" / "n/a" / blank placeholders — absence is signalled
// by the section's non-existence. Pointer discipline is preserved
// end-to-end: a nil pointer is not coerced to 0 just because the
// renderer is rendering a column.
func printGetText(w io.Writer, record any) {
	switch r := record.(type) {
	case *dbpkg.ToolCall:
		fmt.Fprintf(w, "id:           %s\n", r.ID)
		fmt.Fprintf(w, "session_id:   %s\n", r.SessionID)
		fmt.Fprintf(w, "tool:         %s\n", r.Tool)
		fmt.Fprintf(w, "input:        %s\n", r.Input)
		fmt.Fprintf(w, "output:       %s\n", r.Output)
		fmt.Fprintf(w, "content_hash: %s\n", r.ContentHash)
		fmt.Fprintf(w, "timestamp:    %s\n", r.Timestamp)
		fmt.Fprintf(w, "event_type:   %s\n", r.EventType)
		// Cost-publication section. Rendered only when at
		// least one cost pointer is non-nil. Field name column is padded
		// to 12 chars after the 2-space indent, matching the main
		// section's visual alignment.
		if r.TokensIn != nil || r.TokensOut != nil || r.Cost != nil ||
			r.LatencyMs != nil || r.Model != nil || r.Provider != nil {
			fmt.Fprintln(w, "")
			fmt.Fprintln(w, "Costs:")
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
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Latency evidence:")
		fmt.Fprintf(w, "  state:        %s\n", r.LatencyState)
		fmt.Fprintf(w, "  unit:         %s\n", r.LatencyUnit)
		fmt.Fprintf(w, "  authority:    %s\n", r.LatencyAuthority)
	case *dbpkg.Session:
		fmt.Fprintf(w, "id:              %s\n", r.ID)
		fmt.Fprintf(w, "agent:           %s\n", r.Agent)
		fmt.Fprintf(w, "started_at:      %s\n", r.StartedAt)
		fmt.Fprintf(w, "ended_at:        %s\n", r.EndedAt)
		fmt.Fprintf(w, "last_seen_at:    %s\n", r.LastSeenAt)
		fmt.Fprintf(w, "tool_call_count: %d\n", r.ToolCallCount)
	default:
		// Defensive: GetRecord only returns *ToolCall or *Session. If a
		// Unknown record variants fall back to JSON so
		// the user gets something rather than silence.
		_ = json.NewEncoder(w).Encode(record)
	}
}

// printGetHelp writes the `ktx get` help. Mirrors `ktx read --help` style
// so the two related commands present consistently. Used both as the
// dispatcher's Help hook (renderHelp invokes it on --help) and from
// getLogic when runGetWith is invoked directly with --help.
func printGetHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx get — query a record by ID (formatted)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx get [--format text|json] <id>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Fetches a record (tool_call or session) from the database by its 16-char")
	fmt.Fprintln(w, "nanoid ID. tool_calls are checked first; sessions are checked second")
	fmt.Fprintln(w, "(both tables are addressed by nanoid ID).")
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
