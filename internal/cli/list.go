// `ktx list` — the QUERY command that lists persisted sessions in a
// human-friendly form (Decisions 2, 4, 7, 8).
//
// `ktx list` is a query command (Decision 2), not an I/O primitive. The
// default output is a JSON array of session records (Decision 4); the
// `--format text` view is a simple human-readable table.
//
// M1 scope is the minimal list: every session in the database, ordered by
// started_at (oldest first). No cost/token filters (those are M2), no
// tool_call join (M3 may add per-session tool_call listing behind a flag
// — for M1 the wrapper/adapter persistence path gives ktx list the
// tool_call_count roll-up field on the session row, which is enough to
// answer "did this session see activity?").
//
// Database path resolution (Decision 8): KTX_DB env > default. ktx list
// does not take a --path flag; per-invocation override lives on `ktx db`.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/jgabor/ktx/internal/store"
)

func runList(args []string, opts Options) int {
	return runListWith(args, opts, os.Stdout, os.Stderr)
}

// runListWith is the testable core of `ktx list`. stdout/stderr are
// injected so the list flow can be exercised without touching os.Stdout.
func runListWith(args []string, opts Options, stdout, stderr io.Writer) int {
	var showHelp bool
	// Subcommand-local --format. See runGetWith for the rationale: the
	// global --format only parses from the front of args, so we accept
	// it here too via extractFormat. The subcommand's value takes
	// precedence over the global one when both are set.
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}

	fs := flag.NewFlagSet("ktx list", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(rest); err != nil {
		printError(stderr, usageError("ktx list: %v", err), format)
		return ExitUsage
	}
	if showHelp {
		printListHelp(stdout)
		return ExitOK
	}
	if format != FormatJSON && format != FormatText {
		printError(stderr, usageError("ktx list: invalid --format %q: want %q or %q", format, FormatJSON, FormatText), format)
		return ExitUsage
	}
	if len(fs.Args()) > 0 {
		printError(stderr, usageError("ktx list: unexpected argument %q", fs.Arg(0)), format)
		return ExitUsage
	}

	path, err := store.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx list: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := store.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx list: open database: %v", err), format)
		return ExitInternal
	}
	defer db.Close()

	sessions, err := db.ListSessions()
	if err != nil {
		printError(stderr, internalError("ktx list: list sessions: %v", err), format)
		return ExitInternal
	}

	switch format {
	case FormatText:
		printListText(stdout, sessions)
	default:
		// JSON default. The slice is non-nil even when empty
		// (ListSessions returns []Session{} not nil) so json.NewEncoder
		// produces "[]" rather than "null" for the empty case.
		if err := json.NewEncoder(stdout).Encode(sessions); err != nil {
			printError(stderr, internalError("ktx list: write JSON: %v", err), format)
			return ExitInternal
		}
	}
	return ExitOK
}

// printListText writes a human-readable table of sessions. tabwriter
// gives tidy column alignment without pulling in a table library; the
// "min" widths are zero so the columns are sized by content. EndedAt
// shows "-" for an open session, which is the meaningful signal a
// reader wants at a glance (closed == finished, "-" == still running).
func printListText(w io.Writer, sessions []store.Session) {
	if len(sessions) == 0 {
		fmt.Fprintln(w, "no sessions")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tAGENT\tSTARTED\tENDED\tTOOL_CALLS")
	for _, s := range sessions {
		ended := s.EndedAt
		if ended == "" {
			ended = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
			s.ID, s.Agent, s.StartedAt, ended, s.ToolCallCount)
	}
	tw.Flush()
}

// printListHelp writes the `ktx list` help. Mirrors `ktx get --help` style
// so the two query commands present consistently.
func printListHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx list — list sessions in the database (formatted)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx list [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Lists every session in the database, ordered by started_at (oldest first).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  a JSON array of session records on stdout")
	fmt.Fprintln(w, "  text            a tab-aligned table (id, agent, started, ended, tool_calls)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  list printed (may be empty)")
	fmt.Fprintln(w, "  1  usage error (unknown flag, unexpected argument)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "M1 scope: all sessions, no filtering. Cost/token filters and per-session")
	fmt.Fprintln(w, "tool_call joins are post-M1 (M2/M3).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx get   fetch a single record by ID")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
