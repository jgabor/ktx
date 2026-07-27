// `ktx list` — the QUERY command that lists persisted sessions in a
// human-friendly form.
//
// `ktx list` is a query command, not an I/O primitive. The
// default output is a JSON array of session records; the
// `--format text` view is a simple human-readable table.
//
// The scope is the minimal list: every session in the database, ordered by
// started_at (oldest first). A per-session cost rollup extends the surface with
// a rollup: TOKENS (sum of tokens_in + tokens_out per call,
// summed across the session), COST, and MODEL. The rollup is computed
// in-db from tool_calls (see db.SessionCostsRollups); the text view
// uses the explicit "-" marker when the session has no non-NULL cost data, and the JSON view emits a top-level costs object with the rollup
// fields when at least one call has non-NULL cost data.
//
// Database path resolution: KTX_DB env > default. ktx list
// does not take a --path flag; per-invocation override lives on `ktx db`.
//
// JSON is the default output format.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runListWith executes `ktx list` with injected output streams.
// stdout/stderr are injected so the list flow can be exercised without
// touching os.Stdout. The body parses --format, opens the database,
// delegates to listLogic for the actual work, and translates the
// returned error into an exit-code + stderr envelope.
func runListWith(args []string, opts Options, stdout, stderr io.Writer) int {
	// Subcommand-local --format. The dispatch module's runRegisteredVerb
	// already extracts --format from the args when the verb is invoked
	// through dispatch; for direct runListWith callers,
	// extract it here so the call sites have identical semantics.
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}

	target, err := dbpkg.ResolveTarget("")
	if err != nil {
		printError(stderr, internalError("ktx list: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := openDatabase(target, opts.ExpectedRuntimeSHA256)
	if err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
	}
	defer db.Close()

	_, err = listLogic(rest, format, db, stdout)
	if err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// listLogic reads and renders sessions from an open database. The caller
// resolves the output format before invoking it.
//
// Step-by-step:
//
//  1. Parse --help/-h.
//  2. If --help, render printListHelp and return.
//  3. Validate --format is json or text.
//  4. Reject trailing positionals.
//  5. db.ListSessions() + db.SessionCostsRollups().
//  6. Switch on format: text → printListText, default → json envelope.
func listLogic(args []string, format string, db *dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx list", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx list: %v", err)
	}
	if showHelp {
		printListHelp(stdout)
		return nil, nil
	}
	if format != FormatJSON && format != FormatText {
		return nil, usageError("ktx list: invalid --format %q: want %q or %q", format, FormatJSON, FormatText)
	}
	if len(fs.Args()) > 0 {
		return nil, usageError("ktx list: unexpected argument %q", fs.Arg(0))
	}

	sessions, err := db.ListSessions()
	if err != nil {
		return nil, internalError("ktx list: list sessions: %v", err)
	}

	// Per-session cost rollup. Sessions without any non-NULL cost
	// data are absent from the map; the inspect surface treats a missing
	// key as "no data". The query is read-only and runs against
	// the same DB handle; failure here is an internal error rather than
	// a usage error because the schema is fixed and the input is empty.
	rollups, err := db.SessionCostsRollups()
	if err != nil {
		return nil, internalError("ktx list: session cost rollups: %v", err)
	}

	switch format {
	case FormatText:
		printListText(stdout, sessions, rollups)
	default:
		// JSON default. Build a wrapper slice that embeds the
		// dbpkg.Session struct (so its fields are flattened into the
		// output object) and adds the optional costs pointer.
		// The wrapper avoids touching the Session struct in
		// internal/db/records.go while keeping the JSON
		// output shape exactly the same plus a top-level costs object
		// when present. omitempty on the Costs field means a session with no
		// non-NULL cost data does not emit the field at all; omission is
		// the chosen signal because it matches the "absent" semantic.
		rows := make([]sessionListRow, len(sessions))
		for i, s := range sessions {
			rows[i].Session = s
			if r, ok := rollups[s.ID]; ok {
				roll := r
				rows[i].Costs = &roll
			}
		}
		if err := json.NewEncoder(stdout).Encode(rows); err != nil {
			return nil, internalError("ktx list: write JSON: %v", err)
		}
	}
	return nil, nil
}

// sessionListRow is the JSON encoding shape for a single `ktx list`
// session row. The dbpkg.Session fields are embedded so
// the JSON output is a flat object with the base fields plus an
// optional costs pointer; omitempty on the Costs field omits it entirely
// when the session has no non-NULL cost data.
//
// The wrapper type is local to the cli package so the on-disk
// dbpkg.Session shape (in internal/db/records.go)
// is not modified.
type sessionListRow struct {
	dbpkg.Session
	Costs *dbpkg.SessionCosts `json:"costs,omitempty"`
}

// listCommandFunc is the dispatch wrapper. The dispatcher already
// opened the DB (NeedsDB=true), parsed --help/--format, and resolved
// the format. The verb receives a *DispatchContext carrying the
// populated DB, the resolved format, and a clean positional Args
// tail. The verb's only responsibility is to apply listLogic and
// surface the typed error to the dispatcher's printError + exitCodeOf
// pipeline.
func listCommandFunc(ctx *DispatchContext) (any, error) {
	return listLogic(ctx.Args, ctx.Format, ctx.DB, ctx.Stdout)
}

// init registers `ktx list` with the dispatch module. After init
// runs, the verb is reachable through runWith.
func init() {
	Register("list", RegisterOptions{
		Description:   "list all sessions",
		NeedsDB:       true,
		FormatDefault: FormatJSON,
		Help:          printListHelp,
	}, listCommandFunc)
}

// costsNoData is the explicit "no data" marker for the text list view.
// It is visually distinct from a zero total — "0" /
// "0.0000" means the underlying rollup is non-NULL and sums to zero;
// "-" means no underlying call has non-NULL cost data.
const costsNoData = "-"

// printListText writes a human-readable table of sessions. tabwriter
// gives tidy column alignment without pulling in a table library; the
// "min" widths are zero so the columns are sized by content. EndedAt
// shows "-" for an open session, which is the meaningful signal a
// reader wants at a glance (closed == finished, "-" == still running).
//
// Cost columns: TOKENS, COST, MODEL are appended after
// TOOL_CALLS. A session with no non-NULL cost data shows "-" for all
// three. A session with partial cost data shows the available
// sub-rollup values and "-" for any sub-rollup where every underlying
// call's value is NULL. The rollup logic is in db.SessionCostsRollups;
// this renderer is a pure presentation layer.
func printListText(w io.Writer, sessions []dbpkg.Session, rollups map[string]dbpkg.SessionCosts) {
	if len(sessions) == 0 {
		fmt.Fprintln(w, "no sessions")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tAGENT\tSTARTED\tENDED\tTOOL_CALLS\tTOKENS\tCOST\tMODEL")
	for _, s := range sessions {
		ended := s.EndedAt
		if ended == "" {
			ended = "-"
		}
		var tokens, cost, model string
		if r, ok := rollups[s.ID]; ok {
			tokens = costsTokensCell(r)
			cost = costsCostCell(r)
			model = costsModelCell(r)
		} else {
			tokens = costsNoData
			cost = costsNoData
			model = costsNoData
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			s.ID, s.Agent, s.StartedAt, ended, s.ToolCallCount,
			tokens, cost, model)
	}
	_ = tw.Flush()
}

// costsTokensCell renders the TOKENS column value for a session rollup.
// A nil pointer (no underlying call had both tokens_in and tokens_out
// non-NULL) renders as "-"; a non-nil pointer to 0 renders as
// "0" to preserve the absent-vs-zero distinction.
func costsTokensCell(r dbpkg.SessionCosts) string {
	if r.Tokens == nil {
		return costsNoData
	}
	return fmt.Sprintf("%d", *r.Tokens)
}

// costsCostCell renders the COST column value for a session rollup.
// Four decimal places match the JSON default's float64 precision
// and the per-call costs section in printGetText; a session rollup of
// zero renders as "0.0000" so the "0" total is visually distinct
// from "-".
func costsCostCell(r dbpkg.SessionCosts) string {
	if r.Cost == nil {
		return costsNoData
	}
	return fmt.Sprintf("%.4f", *r.Cost)
}

// costsModelCell renders the MODEL column value for a session rollup.
// A nil pointer renders as "-"; a non-nil pointer renders the model
// name verbatim. MIN() over a non-empty set returns a non-NULL
// model string in the rollup, so the empty-string edge case is
// not reachable through the normal query path.
func costsModelCell(r dbpkg.SessionCosts) string {
	if r.Model == nil {
		return costsNoData
	}
	return *r.Model
}

// printListHelp writes the `ktx list` help. Mirrors `ktx get --help` style
// so the two query commands present consistently. Used both as the
// dispatcher's Help hook (renderHelp invokes it on --help) and from
// listLogic when runListWith is invoked directly with --help.
func printListHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx list — list sessions in the database (formatted)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx list [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Lists every session in the database, ordered by started_at (oldest first).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  a JSON array of session records on stdout (sessions with cost data include a top-level costs object with tokens/cost/model rollup)")
	fmt.Fprintln(w, "  text            a tab-aligned table (id, agent, started, ended, tool_calls, tokens, cost, model; \"-\" means no cost data)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  list printed (may be empty)")
	fmt.Fprintln(w, "  1  usage error (unknown flag, unexpected argument)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Scope: all sessions, no filtering. The cost-publication rollup")
	fmt.Fprintln(w, "(tokens, cost, model) is shown per session when at least one underlying")
	fmt.Fprintln(w, "tool_call has a non-NULL cost field; sessions without cost data show \"-\".")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx get   fetch a single record by ID")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
