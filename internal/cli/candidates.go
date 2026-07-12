// `ktx candidates` — the discovery command that ranks tool/command
// groups by delivered tokens so users know which commands need
// profiles or tighter compression.
//
// `ktx candidates` is a query command. The default output is a JSON
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

// candidatesDefaultLimit is the default maximum number of groups
// returned by `ktx candidates` when --limit is not specified.
const candidatesDefaultLimit = 20

// candidatesMinCalls is the minimum number of calls a group must
// have to be included in the output.
const candidatesMinCalls = 2

// candidatesMinDeliveredTokens is the minimum delivered tokens a
// group must have to be included.
const candidatesMinDeliveredTokens = 50

// candidateGroup is one ranked row of the candidates output. It
// represents an aggregated view of tool calls grouped by tool name
// and extracted binary name.
type candidateGroup struct {
	Tool                 string `json:"tool"`
	Command              string `json:"command"`
	Calls                int    `json:"calls"`
	TotalRawTokens       int    `json:"total_raw_tokens"`
	TotalDeliveredTokens int    `json:"total_delivered_tokens"`
}

func init() {
	Register("candidates", RegisterOptions{
		Description:   "rank tool/command groups by delivered token waste",
		NeedsDB:       true,
		FormatDefault: FormatJSON,
		Help:          printCandidatesHelp,
	}, candidatesCommandFunc)
}

// candidatesCommandFunc is the dispatch wrapper. The dispatcher
// already opened the DB, parsed --help/--format, and resolved the
// format.
func candidatesCommandFunc(ctx *DispatchContext) (any, error) {
	return candidatesLogic(ctx.Args, ctx.Format, ctx.DB, ctx.Stdout)
}

// candidatesLogic reads, aggregates, and renders the candidate groups.
func candidatesLogic(args []string, format string, db *dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	var limit int
	fs := flag.NewFlagSet("ktx candidates", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")
	fs.IntVar(&limit, "limit", candidatesDefaultLimit, "maximum number of groups to show")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx candidates: %v", err)
	}
	if showHelp {
		printCandidatesHelp(stdout)
		return nil, nil
	}
	if format != FormatJSON && format != FormatText {
		return nil, usageError("ktx candidates: invalid --format %q: want %q or %q", format, FormatJSON, FormatText)
	}
	if len(fs.Args()) > 0 {
		return nil, usageError("ktx candidates: unexpected argument %q", fs.Arg(0))
	}
	if limit <= 0 {
		return nil, usageError("ktx candidates: --limit must be positive, got %d", limit)
	}

	rows, err := db.CandidateRows()
	if err != nil {
		return nil, internalError("ktx candidates: %v", err)
	}

	groups := aggregateCandidates(rows)
	sortCandidates(groups)

	if len(groups) > limit {
		groups = groups[:limit]
	}

	if format == FormatText {
		printCandidatesText(stdout, groups)
	} else {
		_ = json.NewEncoder(stdout).Encode(groups)
	}
	return nil, nil
}

// aggregateCandidates groups raw rows by (tool, binary name) and
// computes per-group totals. The 4-chars-per-token heuristic
// matches the token-budget stage's formula.
func aggregateCandidates(rows []dbpkg.CandidateRow) []candidateGroup {
	type key struct {
		tool, command string
	}
	m := map[key]*candidateGroup{}

	for _, r := range rows {
		binary := router.BinaryFromInput(r.Input)
		k := key{tool: r.Tool, command: binary}

		g, ok := m[k]
		if !ok {
			g = &candidateGroup{Tool: r.Tool, Command: binary}
			m[k] = g
		}
		g.Calls++
		g.TotalRawTokens += (r.RawBytes + 3) / 4
		g.TotalDeliveredTokens += (r.DeliveredBytes + 3) / 4
	}

	result := make([]candidateGroup, 0, len(m))
	for _, g := range m {
		if g.Calls < candidatesMinCalls {
			continue
		}
		if g.TotalDeliveredTokens < candidatesMinDeliveredTokens {
			continue
		}
		result = append(result, *g)
	}
	return result
}

// sortCandidates sorts groups by total delivered tokens descending.
// Groups with equal delivered tokens are sorted by tool, then
// command, for deterministic output.
func sortCandidates(groups []candidateGroup) {
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

// printCandidatesText writes a human-readable table of candidate groups.
func printCandidatesText(w io.Writer, groups []candidateGroup) {
	if len(groups) == 0 {
		fmt.Fprintln(w, "no candidates")
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

// printCandidatesHelp writes the `ktx candidates` help.
func printCandidatesHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx candidates — rank tool/command groups by delivered token waste")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx candidates [--format text|json] [--limit N]")
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
	fmt.Fprintln(w, "  0  candidates listed (may be empty)")
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
