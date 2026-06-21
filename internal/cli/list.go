// `ktx list` — the QUERY command that lists persisted sessions in a
// human-friendly form (Decisions 2, 4, 7, 8).
//
// `ktx list` is a query command (Decision 2), not an I/O primitive. The
// default output is a JSON array of session records (Decision 4); the
// `--format text` view is a simple human-readable table.
//
// M1 scope is the minimal list: every session in the database, ordered by
// started_at (oldest first). M2 (Task 5) extends the surface with a
// per-session M2 rollup: TOKENS (sum of tokens_in + tokens_out per call,
// summed across the session), COST, and MODEL. The rollup is computed
// in-db from tool_calls (see db.SessionM2Rollups); the text view
// uses the explicit "-" marker when the session has no non-NULL M2 data
// (AC6), and the JSON view emits a top-level m2 object with the rollup
// fields when at least one call has non-NULL M2 data (AC7).
//
// Database path resolution (Decision 8): KTX_DB env > default. ktx list
// does not take a --path flag; per-invocation override lives on `ktx db`.
//
// Task 6 migration: the verb registers via dispatch.Register with
// NeedsDB=true (dispatcher opens the DB) and FormatDefault=FormatJSON.
// The legacy runListWith remains the test seam (Task 3 surface) and
// still opens its own DB so existing Task 3 tests pass unchanged.
// The shared core is listLogic, returning (any, error); the dispatcher
// routes the typed error through its printError + exitCodeOf pipeline.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// runListWith is the testable core of `ktx list` (Task 3 seam).
// stdout/stderr are injected so the list flow can be exercised without
// touching os.Stdout. The body parses --format, opens the database,
// delegates to listLogic for the actual work, and translates the
// returned error into an exit-code + stderr envelope.
func runListWith(args []string, opts Options, stdout, stderr io.Writer) int {
	// Subcommand-local --format. The dispatch module's runRegisteredVerb
	// already extracts --format from the args when the verb is invoked
	// through dispatch; for direct runListWith callers (Task 3 seam),
	// extract it here so the call sites have identical semantics.
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}

	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx list: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := dbpkg.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx list: open database: %v", err), format)
		return ExitInternal
	}
	defer db.Close()

	_, err = listLogic(rest, format, db, stdout)
	if err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// listLogic is the shared core of `ktx list`. Both runListWith (the
// legacy test seam) and the dispatch wrapper listCommandFunc call it.
// The caller has already resolved --format and opened the database.
//
// Step-by-step:
//
//  1. Parse --help/-h.
//  2. If --help, render printListHelp and return.
//  3. Validate --format is json or text.
//  4. Reject trailing positionals.
//  5. db.ListSessions() + db.SessionM2Rollups().
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

	// Per-session M2 rollup (Task 5). Sessions without any non-NULL M2
	// data are absent from the map; the inspect surface treats a missing
	// key as "no data" (AC6). The query is read-only and runs against
	// the same DB handle; failure here is an internal error rather than
	// a usage error because the schema is fixed and the input is empty.
	rollups, err := db.SessionM2Rollups()
	if err != nil {
		return nil, internalError("ktx list: session m2 rollups: %v", err)
	}

	switch format {
	case FormatText:
		printListText(stdout, sessions, rollups)
	default:
		// JSON default. Build a wrapper slice that embeds the
		// dbpkg.Session struct (so its fields are flattened into the
		// output object) and adds the optional m2 pointer (Task 5,
		// AC7). The wrapper avoids touching the Session struct in
		// internal/db/persist.go (Task 5 scope) while keeping the JSON
		// output shape exactly the same plus a top-level m2 object
		// when present. omitempty on M2 means a session with no
		// non-NULL M2 data does not emit the field at all (AC7
		// permits omission or null; omission is the chosen signal
		// because it matches the "absent" semantic).
		rows := make([]sessionListRow, len(sessions))
		for i, s := range sessions {
			rows[i].Session = s
			if r, ok := rollups[s.ID]; ok {
				roll := r
				rows[i].M2 = &roll
			}
		}
		if err := json.NewEncoder(stdout).Encode(rows); err != nil {
			return nil, internalError("ktx list: write JSON: %v", err)
		}
	}
	return nil, nil
}

// sessionListRow is the JSON encoding shape for a single `ktx list`
// session row (Task 5, AC7). The dbpkg.Session fields are embedded so
// the JSON output is a flat object with the M1 fields plus an
// optional m2 pointer; omitempty on M2 omits the field entirely
// when the session has no non-NULL M2 data.
//
// The wrapper type is local to the cli package so the on-disk
// dbpkg.Session shape (in internal/db/persist.go, off-limits for Task 5)
// is not modified.
type sessionListRow struct {
	dbpkg.Session
	M2 *dbpkg.SessionM2 `json:"m2,omitempty"`
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
// runs, the verb is reachable through runWith (the legacy
// commandIndex entry in cli.go is removed in Task 6).
func init() {
	Register("list", RegisterOptions{
		Description:   "list all sessions",
		NeedsDB:       true,
		FormatDefault: FormatJSON,
		Help:          printListHelp,
	}, listCommandFunc)
}

// m2NoData is the explicit "no data" marker for the text list view
// (Task 5, AC6). It is visually distinct from a zero total — "0" /
// "0.0000" means the underlying rollup is non-NULL and sums to zero;
// "-" means no underlying call has non-NULL M2 data.
const m2NoData = "-"

// printListText writes a human-readable table of sessions. tabwriter
// gives tidy column alignment without pulling in a table library; the
// "min" widths are zero so the columns are sized by content. EndedAt
// shows "-" for an open session, which is the meaningful signal a
// reader wants at a glance (closed == finished, "-" == still running).
//
// M2 columns (Task 5): TOKENS, COST, MODEL are appended after
// TOOL_CALLS. A session with no non-NULL M2 data shows "-" for all
// three (AC6). A session with partial M2 data shows the available
// sub-rollup values and "-" for any sub-rollup where every underlying
// call's value is NULL. The rollup logic is in db.SessionM2Rollups
// (Task 5); this renderer is a pure presentation layer.
func printListText(w io.Writer, sessions []dbpkg.Session, rollups map[string]dbpkg.SessionM2) {
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
			tokens = m2TokensCell(r)
			cost = m2CostCell(r)
			model = m2ModelCell(r)
		} else {
			tokens = m2NoData
			cost = m2NoData
			model = m2NoData
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			s.ID, s.Agent, s.StartedAt, ended, s.ToolCallCount,
			tokens, cost, model)
	}
	tw.Flush()
}

// m2TokensCell renders the TOKENS column value for a session rollup.
// A nil pointer (no underlying call had both tokens_in and tokens_out
// non-NULL) renders as "-" (AC6); a non-nil pointer to 0 renders as
// "0" to preserve the absent-vs-zero distinction.
func m2TokensCell(r dbpkg.SessionM2) string {
	if r.Tokens == nil {
		return m2NoData
	}
	return fmt.Sprintf("%d", *r.Tokens)
}

// m2CostCell renders the COST column value for a session rollup.
// Four decimal places match the JSON default's float64 precision
// and the per-call M2 section in printGetText; a session rollup of
// zero renders as "0.0000" so the "0" total is visually distinct
// from "-" (AC6).
func m2CostCell(r dbpkg.SessionM2) string {
	if r.Cost == nil {
		return m2NoData
	}
	return fmt.Sprintf("%.4f", *r.Cost)
}

// m2ModelCell renders the MODEL column value for a session rollup.
// A nil pointer renders as "-"; a non-nil pointer renders the model
// name verbatim. MIN() over a non-empty set returns a non-NULL
// model string in the rollup, so the empty-string edge case is
// not reachable through the normal query path.
func m2ModelCell(r dbpkg.SessionM2) string {
	if r.Model == nil {
		return m2NoData
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
	fmt.Fprintln(w, "  json (default)  a JSON array of session records on stdout (M2 sessions include a top-level m2 object with tokens/cost/model rollup)")
	fmt.Fprintln(w, "  text            a tab-aligned table (id, agent, started, ended, tool_calls, tokens, cost, model; \"-\" means no M2 data)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  list printed (may be empty)")
	fmt.Fprintln(w, "  1  usage error (unknown flag, unexpected argument)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "M1+M2 scope: all sessions, no filtering. The M2 cost-publication rollup")
	fmt.Fprintln(w, "(tokens, cost, model) is shown per session when at least one underlying")
	fmt.Fprintln(w, "tool_call has a non-NULL M2 field; sessions without M2 data show \"-\".")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx get   fetch a single record by ID")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}