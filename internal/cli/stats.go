// `ktx stats` — per-tool-call statistics that rank tool/command
// groups by delivered tokens so users know which commands need
// profiles or tighter compression.
//
// `ktx stats` is a query command. The default output is a JSON
// array; `--format text` renders a human-readable table. `--limit N`
// caps the number of groups shown (default 20).
//
// The query surfaces both compressed and uncompressed tool calls:
// uncompressed calls appear with delivered tokens equal to raw tokens.
// Groups with strictly fewer than 2 calls or strictly fewer than 50
// delivered tokens are excluded as trivial.
//
// Binary name extraction reuses router.BinaryFromInput so the
// extraction logic stays in one place.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/router"
)

// statsDefaultLimit is the default maximum number of groups
// returned by `ktx stats` when --limit is not specified.
const statsDefaultLimit = 20

// statsMinCalls is the minimum number of calls a group must
// have to be included in the output.
const statsMinCalls = 2

// statsMinDeliveredTokens is the minimum delivered tokens a
// group must have to be included.
const statsMinDeliveredTokens = 50

// statsGroup is one ranked row of the stats output. It
// represents an aggregated view of tool calls grouped by tool name
// and extracted binary name.
type statsGroup struct {
	Tool                 string `json:"tool"`
	Command              string `json:"command"`
	Calls                int    `json:"calls"`
	TotalRawTokens       int    `json:"total_raw_tokens"`
	TotalDeliveredTokens int    `json:"total_delivered_tokens"`
}

func init() {
	Register("stats", RegisterOptions{
		Description:   "per-tool-call statistics ranked by delivered tokens",
		NeedsDB:       true,
		FormatDefault: FormatJSON,
		Help:          printStatsHelp,
	}, statsCommandFunc)
}

// statsCommandFunc is the dispatch wrapper. The dispatcher
// already opened the DB, parsed --help/--format, and resolved the
// format.
func statsCommandFunc(ctx *DispatchContext) (any, error) {
	return statsLogic(ctx.Args, ctx.Format, ctx.DB, ctx.Stdout)
}

// statsLogic reads, aggregates, and renders the stats groups.
func statsLogic(args []string, format string, db *dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	var limit int
	fs := flag.NewFlagSet("ktx stats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")
	fs.IntVar(&limit, "limit", statsDefaultLimit, "maximum number of groups to show")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx stats: %v", err)
	}
	if showHelp {
		printStatsHelp(stdout)
		return nil, nil
	}
	if format != FormatJSON && format != FormatText {
		return nil, usageError("ktx stats: invalid --format %q: want %q or %q", format, FormatJSON, FormatText)
	}
	if len(fs.Args()) > 0 {
		return nil, usageError("ktx stats: unexpected argument %q", fs.Arg(0))
	}
	if limit <= 0 {
		return nil, usageError("ktx stats: --limit must be positive, got %d", limit)
	}

	rows, err := db.StatsRows()
	if err != nil {
		return nil, internalError("ktx stats: %v", err)
	}

	groups := aggregateStats(rows)
	sortStats(groups)

	if len(groups) > limit {
		groups = groups[:limit]
	}

	if format == FormatText {
		printStatsText(stdout, groups)
	} else {
		_ = json.NewEncoder(stdout).Encode(groups)
	}
	return nil, nil
}

// aggregateStats groups raw rows by (tool, binary name) and
// computes per-group totals. The 4-chars-per-token heuristic
// matches the token-budget stage's formula.
func aggregateStats(rows []dbpkg.StatsRow) []statsGroup {
	type key struct {
		tool, command string
	}
	m := map[key]*statsGroup{}

	for _, r := range rows {
		binary := router.BinaryFromInput(r.Input)
		k := key{tool: r.Tool, command: binary}

		g, ok := m[k]
		if !ok {
			g = &statsGroup{Tool: r.Tool, Command: binary}
			m[k] = g
		}
		g.Calls++
		g.TotalRawTokens += (r.RawBytes + 3) / 4
		g.TotalDeliveredTokens += (r.DeliveredBytes + 3) / 4
	}

	result := make([]statsGroup, 0, len(m))
	for _, g := range m {
		if g.Calls < statsMinCalls {
			continue
		}
		if g.TotalDeliveredTokens < statsMinDeliveredTokens {
			continue
		}
		result = append(result, *g)
	}
	return result
}

// sortStats sorts groups by total delivered tokens descending.
// Groups with equal delivered tokens are sorted by tool, then
// command, for deterministic output.
func sortStats(groups []statsGroup) {
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].TotalDeliveredTokens != groups[j].TotalDeliveredTokens {
			return groups[i].TotalDeliveredTokens > groups[j].TotalDeliveredTokens
		}
		if groups[i].Tool != groups[j].Tool {
			return groups[i].Tool < groups[j].Tool
		}
		return groups[i].Command < groups[j].Command
	})
}

// printStatsText writes a human-readable table of stats groups.
func printStatsText(w io.Writer, groups []statsGroup) {
	if len(groups) == 0 {
		fmt.Fprintln(w, "no stats")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tCOMMAND\tCALLS\tRAW_TOK\tDELIVERED_TOK")
	for _, g := range groups {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\n",
			g.Tool, g.Command, g.Calls, g.TotalRawTokens, g.TotalDeliveredTokens)
	}
	tw.Flush() //nolint:errcheck // best-effort formatting; errors are not actionable
}

// printStatsHelp writes the `ktx stats` help.
func printStatsHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx stats — per-tool-call statistics ranked by delivered tokens")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx stats [--format text|json] [--limit N]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Lists tool/command groups ranked by total delivered tokens (descending).")
	fmt.Fprintln(w, "Uncompressed tool calls are included with delivered tokens equal to raw tokens.")
	fmt.Fprintln(w, "Groups with fewer than 2 calls or fewer than 50 delivered tokens are excluded.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --limit N   maximum number of groups to show (default 20)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  a JSON array of objects on stdout")
	fmt.Fprintln(w, "  text            a tab-aligned table")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  stats listed (may be empty)")
	fmt.Fprintln(w, "  1  usage error (unknown flag, invalid --limit)")
	fmt.Fprintln(w, "  4  internal error (database failure)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx list   list all sessions in the database")
	fmt.Fprintln(w, "  ktx get    query a single record by ID")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
