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
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/router"
)

// statsDefaultLimit is the default maximum number of groups
// returned by `ktx stats` when --limit is not specified.
const (
	statsDefaultLimit  = 20
	statsDefaultWindow = 30 * time.Minute
)

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

// statsParsedArgs carries the parsed --limit value from ParseArgs
// through the dispatcher into statsCommandFunc.
type statsParsedArgs struct {
	Limit     int
	LimitSet  bool
	Window    time.Duration
	WindowSet bool
}

// parseStatsArgs pre-extracts --limit from args before the
// dispatcher's shared flag set sees it. The shared flag set only
// knows --help/-h and --quiet/-q; an unrecognized --limit would
// trigger "flag provided but not defined." This mirrors how
// extractFormat handles --format: scan, extract, return the rest.
func parseStatsArgs(args []string) ([]string, any, error) {
	limit := statsDefaultLimit
	window := statsDefaultWindow
	var limitSet, windowSet bool
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--limit" && i+1 < len(args) {
			parsed, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, nil, fmt.Errorf("ktx stats: invalid --limit %q: %v", args[i+1], err)
			}
			limit = parsed
			limitSet = true
			i++ // skip the value
			continue
		}
		if strings.HasPrefix(a, "--limit=") {
			val := strings.TrimPrefix(a, "--limit=")
			parsed, err := strconv.Atoi(val)
			if err != nil {
				return nil, nil, fmt.Errorf("ktx stats: invalid --limit %q: %v", val, err)
			}
			limit = parsed
			limitSet = true
			continue
		}
		if a == "--window" {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("ktx stats summary: --window requires a duration (for example 30m)")
			}
			parsed, err := parseStatsWindow(args[i+1])
			if err != nil {
				return nil, nil, err
			}
			window = parsed
			windowSet = true
			i++
			continue
		}
		if strings.HasPrefix(a, "--window=") {
			parsed, err := parseStatsWindow(strings.TrimPrefix(a, "--window="))
			if err != nil {
				return nil, nil, err
			}
			window = parsed
			windowSet = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, statsParsedArgs{Limit: limit, LimitSet: limitSet, Window: window, WindowSet: windowSet}, nil
}

func parseStatsWindow(value string) (time.Duration, error) {
	window, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("ktx stats summary: invalid --window %q: %v", value, err)
	}
	if window <= 0 {
		return 0, fmt.Errorf("ktx stats summary: --window must be positive, got %q", value)
	}
	return window, nil
}

func init() {
	Register("stats", RegisterOptions{
		Description:   "per-tool-call statistics ranked by delivered tokens",
		NeedsDB:       true,
		FormatDefault: FormatJSON,
		Help:          printStatsHelp,
		ParseArgs:     parseStatsArgs,
	}, statsCommandFunc)
}

// statsCommandFunc is the dispatch wrapper. The dispatcher already
// opened the DB, parsed --help/--format, and extracted --limit via
// ParseArgs. The limit value arrives via ctx.ParsedArgs.
func statsCommandFunc(ctx *DispatchContext) (any, error) {
	parsed := statsParsedArgs{Limit: statsDefaultLimit, Window: statsDefaultWindow}
	if parsed, ok := ctx.ParsedArgs.(statsParsedArgs); ok {
		if len(ctx.Args) > 0 && ctx.Args[0] == "summary" {
			if parsed.LimitSet {
				return nil, usageError("ktx stats summary: --limit is only valid for grouped stats")
			}
			return statsSummaryLogic(ctx.Args[1:], ctx.Format, parsed.Window, time.Now(), ctx.DB, ctx.Stdout)
		}
		if parsed.WindowSet {
			return nil, usageError("ktx stats: --window requires the summary subcommand")
		}
		return statsLogic(ctx.Args, ctx.Format, parsed.Limit, ctx.DB, ctx.Stdout)
	}
	return statsLogic(ctx.Args, ctx.Format, parsed.Limit, ctx.DB, ctx.Stdout)
}

type statsSummary struct {
	SchemaVersion int                `json:"schema_version"`
	GeneratedAt   string             `json:"generated_at"`
	Window        statsSummaryWindow `json:"window"`
	Calls         statsSummaryCalls  `json:"calls"`
	Tokens        statsSummaryTokens `json:"tokens"`
}

type statsSummaryWindow struct {
	Seconds float64 `json:"seconds"`
	From    string  `json:"from"`
	To      string  `json:"to"`
}

type statsSummaryCalls struct {
	TotalWithOutput        int `json:"total_with_output"`
	WithCompressedArtifact int `json:"with_compressed_artifact"`
}

type statsSummaryTokens struct {
	RawEstimated                int      `json:"raw_estimated"`
	DeliveredEstimated          int      `json:"delivered_estimated"`
	RawEstimatedPerSecond       float64  `json:"raw_estimated_per_second"`
	DeliveredEstimatedPerSecond float64  `json:"delivered_estimated_per_second"`
	ReductionPercent            *float64 `json:"reduction_percent"`
}

func statsSummaryLogic(args []string, format string, window time.Duration, now time.Time, db *dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx stats summary", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")
	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx stats summary: %v", err)
	}
	if showHelp {
		printStatsSummaryHelp(stdout)
		return nil, nil
	}
	if len(fs.Args()) > 0 {
		return nil, usageError("ktx stats summary: unexpected argument %q", fs.Arg(0))
	}
	if format != FormatJSON && format != FormatText {
		return nil, usageError("ktx stats summary: invalid --format %q: want %q or %q", format, FormatJSON, FormatText)
	}
	if window <= 0 {
		return nil, usageError("ktx stats summary: --window must be positive")
	}

	now = now.UTC()
	from := now.Add(-window)
	rows, err := db.StatsSummaryRows(from, now)
	if err != nil {
		return nil, internalError("ktx stats summary: %v", err)
	}

	summary := statsSummary{
		SchemaVersion: 1,
		GeneratedAt:   now.Format(time.RFC3339Nano),
		Window: statsSummaryWindow{
			Seconds: window.Seconds(),
			From:    from.Format(time.RFC3339Nano),
			To:      now.Format(time.RFC3339Nano),
		},
	}
	for _, row := range rows {
		summary.Calls.TotalWithOutput++
		if row.HasCompressedArtifact {
			summary.Calls.WithCompressedArtifact++
		}
		summary.Tokens.RawEstimated += estimatedTokens(row.RawChars)
		summary.Tokens.DeliveredEstimated += estimatedTokens(row.DeliveredChars)
	}
	seconds := window.Seconds()
	summary.Tokens.RawEstimatedPerSecond = float64(summary.Tokens.RawEstimated) / seconds
	summary.Tokens.DeliveredEstimatedPerSecond = float64(summary.Tokens.DeliveredEstimated) / seconds
	if summary.Tokens.RawEstimated > 0 {
		reduction := float64(summary.Tokens.RawEstimated-summary.Tokens.DeliveredEstimated) / float64(summary.Tokens.RawEstimated) * 100
		summary.Tokens.ReductionPercent = &reduction
	}

	if format == FormatText {
		printStatsSummaryText(stdout, summary)
	} else {
		_ = json.NewEncoder(stdout).Encode(summary)
	}
	return nil, nil
}

func estimatedTokens(characters int) int {
	return (characters + 3) / 4
}

// statsLogic reads, aggregates, and renders the stats groups.
func statsLogic(args []string, format string, limit int, db *dbpkg.DB, stdout io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx stats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

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
		g.TotalRawTokens += estimatedTokens(r.RawBytes)
		g.TotalDeliveredTokens += estimatedTokens(r.DeliveredBytes)
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

func printStatsSummaryText(w io.Writer, summary statsSummary) {
	reduction := "-"
	if summary.Tokens.ReductionPercent != nil {
		reduction = strconv.FormatFloat(*summary.Tokens.ReductionPercent, 'f', 2, 64)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WINDOW_FROM\tWINDOW_TO\tCALLS\tCOMPRESSED\tRAW_TOK\tDELIVERED_TOK\tRAW_TPS\tDELIVERED_TPS\tREDUCTION_PCT")
	fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%.4f\t%.4f\t%s\n",
		summary.Window.From, summary.Window.To,
		summary.Calls.TotalWithOutput, summary.Calls.WithCompressedArtifact,
		summary.Tokens.RawEstimated, summary.Tokens.DeliveredEstimated,
		summary.Tokens.RawEstimatedPerSecond, summary.Tokens.DeliveredEstimatedPerSecond,
		reduction)
	tw.Flush() //nolint:errcheck // best-effort formatting; errors are not actionable
}

// printStatsHelp writes the `ktx stats` help.
func printStatsHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx stats — per-tool-call statistics ranked by delivered tokens")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  ktx stats [--format text|json] [--limit N]")
	fmt.Fprintln(w, "  ktx stats summary [--window DURATION] [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Lists tool/command groups ranked by total delivered tokens (descending).")
	fmt.Fprintln(w, "Uncompressed tool calls are included with delivered tokens equal to raw tokens.")
	fmt.Fprintln(w, "Groups with fewer than 2 calls or fewer than 50 delivered tokens are excluded.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --limit N   maximum number of groups to show (default 20)")
	fmt.Fprintln(w, "  --window DURATION  trailing summary window (default 30m; summary only)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  a JSON array of objects on stdout")
	fmt.Fprintln(w, "  text            a tab-aligned table")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  stats listed (may be empty)")
	fmt.Fprintln(w, "  1  usage error (unknown flag, invalid --limit or --window)")
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

func printStatsSummaryHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx stats summary — versioned trailing-window token summary")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx stats summary [--window DURATION] [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Counts calls with non-NULL output and an RFC3339 timestamp in the inclusive")
	fmt.Fprintln(w, "[now-window, now] interval. Rates use the full configured window duration.")
	fmt.Fprintln(w, "Each call uses its latest retained compressed artifact; equal timestamps use")
	fmt.Fprintln(w, "the lexicographically greatest artifact ID. Calls without one use raw output.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --window DURATION  positive Go duration (default 30m)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  schema_version 1 JSON object")
	fmt.Fprintln(w, "  text            one tab-aligned summary row")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
}
