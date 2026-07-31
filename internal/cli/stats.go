// `ktx stats` ranks tool/command groups by delivered tokens. Its summary
// subcommand emits fixed-shape window aggregates, while breakdown owns the
// bounded diagnostic delivery groups.
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
// Command dimensions use a fixed reporting vocabulary so raw arguments cannot
// become group labels.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jgabor/ktx/internal/cache/reuse"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/router"
)

// statsDefaultLimit is the default maximum number of groups
// returned by `ktx stats` when --limit is not specified.
const (
	statsDefaultLimit           = 20
	statsDefaultWindow          = 30 * time.Minute
	statsSummarySchemaVersion   = 4
	statsBreakdownSchemaVersion = 1
	statsBreakdownDetailedLimit = 100
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

// statsParsedArgs carries parsed stats flags through the dispatcher.
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
				return nil, nil, fmt.Errorf("ktx stats: --window requires a duration (for example 30m)")
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
		return 0, fmt.Errorf("ktx stats: invalid --window %q: %v", value, err)
	}
	if window <= 0 {
		return 0, fmt.Errorf("ktx stats: --window must be positive, got %q", value)
	}
	return window, nil
}

func init() {
	Register("stats", RegisterOptions{
		Description:    "per-tool-call statistics ranked by delivered tokens",
		DatabaseAccess: DatabaseReadOnly,
		FormatDefault:  FormatJSON,
		Help:           printStatsHelp,
		ParseArgs:      parseStatsArgs,
	}, statsCommandFunc)
}

// statsCommandFunc is the dispatch wrapper. The dispatcher already
// opened the DB, parsed --help/--format, and extracted stats flags via
// ParseArgs.
func statsCommandFunc(ctx *DispatchContext) (any, error) {
	parsed := statsParsedArgs{Limit: statsDefaultLimit, Window: statsDefaultWindow}
	if parsed, ok := ctx.ParsedArgs.(statsParsedArgs); ok {
		if len(ctx.Args) > 0 {
			switch ctx.Args[0] {
			case "summary":
				if parsed.LimitSet {
					return nil, usageError("ktx stats summary: --limit is only valid for grouped stats")
				}
				return statsSummaryLogic(ctx.Args[1:], ctx.Format, parsed.Window, time.Now(), ctx.DB, ctx.Stdout)
			case "breakdown":
				if parsed.LimitSet {
					return nil, usageError("ktx stats breakdown: --limit is only valid for grouped stats")
				}
				return statsBreakdownLogic(ctx.Args[1:], ctx.Format, parsed.Window, time.Now(), ctx.DB, ctx.Stdout)
			}
		}
		if parsed.WindowSet {
			return nil, usageError("ktx stats: --window requires the summary or breakdown subcommand")
		}
		return statsLogic(ctx.Args, ctx.Format, parsed.Limit, ctx.DB, ctx.Stdout)
	}
	return statsLogic(ctx.Args, ctx.Format, parsed.Limit, ctx.DB, ctx.Stdout)
}

type statsSummary struct {
	SchemaVersion int                    `json:"schema_version"`
	GeneratedAt   string                 `json:"generated_at"`
	Window        statsSummaryWindow     `json:"window"`
	Calls         statsSummaryCalls      `json:"calls"`
	Tokens        statsSummaryTokens     `json:"tokens"`
	CacheReuse    statsSummaryCacheReuse `json:"cache_reuse"`
}

type statsBreakdown struct {
	SchemaVersion     int                    `json:"schema_version"`
	GeneratedAt       string                 `json:"generated_at"`
	Window            statsSummaryWindow     `json:"window"`
	Calls             statsSummaryCalls      `json:"calls"`
	Tokens            statsSummaryTokens     `json:"tokens"`
	DeliveryBreakdown statsBreakdownDelivery `json:"delivery_breakdown"`
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

type statsBreakdownDelivery struct {
	DetailedGroupLimit    int                   `json:"detailed_group_limit"`
	DistinctGroups        int                   `json:"distinct_groups"`
	OmittedDistinctGroups int                   `json:"omitted_distinct_groups"`
	Groups                []statsBreakdownGroup `json:"groups"`
	Other                 statsBreakdownTotals  `json:"other"`
}

type statsBreakdownGroup struct {
	Tool                   string  `json:"tool"`
	Command                *string `json:"command"`
	Subcommand             *string `json:"subcommand"`
	Profile                *string `json:"profile"`
	Route                  *string `json:"route"`
	Disposition            string  `json:"disposition"`
	RawOutputSizeBand      string  `json:"raw_output_size_band"`
	Calls                  int     `json:"calls"`
	WithCompressedArtifact int     `json:"with_compressed_artifact"`
	RawEstimated           int     `json:"raw_estimated"`
	DeliveredEstimated     int     `json:"delivered_estimated"`
}

type statsBreakdownTotals struct {
	Calls                  int `json:"calls"`
	WithCompressedArtifact int `json:"with_compressed_artifact"`
	RawEstimated           int `json:"raw_estimated"`
	DeliveredEstimated     int `json:"delivered_estimated"`
}

type statsSummaryCacheReuse struct {
	ContractVersion        string                         `json:"contract_version"`
	Runtime                string                         `json:"runtime"`
	RuntimeVersion         string                         `json:"runtime_version"`
	ContributingModelSteps int                            `json:"contributing_model_steps"`
	UncachedInput          statsSummaryIntegerMeasurement `json:"uncached_input"`
	CacheRead              statsSummaryIntegerMeasurement `json:"cache_read"`
	CacheWrite             statsSummaryIntegerMeasurement `json:"cache_write"`
	LogicalInput           statsSummaryIntegerMeasurement `json:"logical_input"`
	CachedInputShare       statsSummaryShareMeasurement   `json:"cached_input_share"`
	Formula                string                         `json:"formula"`
	Unit                   string                         `json:"unit"`
}

type statsSummaryIntegerMeasurement struct {
	State string   `json:"state"`
	Value *big.Int `json:"value"`
}

type statsSummaryShareMeasurement struct {
	State  string   `json:"state"`
	Value  *float64 `json:"value"`
	Ratio  *string  `json:"ratio"`
	Reason *string  `json:"reason"`
}

type statsSummaryIntegerAccumulator struct {
	value     big.Int
	missing   bool
	malformed bool
}

const (
	statsSummaryMeasured      = "measured"
	statsSummaryMissing       = "missing"
	statsSummaryMalformed     = "malformed"
	statsSummaryNotApplicable = "not_applicable"
)

func statsSummaryLogic(args []string, format string, window time.Duration, now time.Time, db *dbpkg.DB, stdout io.Writer) (any, error) {
	showHelp, err := validateStatsWindowCommand("ktx stats summary", args, format, window, stdout, printStatsSummaryHelp)
	if err != nil || showHelp {
		return nil, err
	}

	now = now.UTC()
	from := now.Add(-window)
	rows, err := db.StatsSummaryRows(from, now)
	if err != nil {
		return nil, internalError("ktx stats summary: %v", err)
	}
	cacheRows, err := db.StatsSummaryCacheRows(from, now, reuse.RequiredRuntime, reuse.RequiredRuntimeVersion)
	if err != nil {
		return nil, internalError("ktx stats summary: %v", err)
	}
	windowSummary, calls, tokens := aggregateStatsWindow(rows, window, now)
	summary := statsSummary{
		SchemaVersion: statsSummarySchemaVersion,
		GeneratedAt:   now.Format(time.RFC3339Nano),
		Window:        windowSummary,
		Calls:         calls,
		Tokens:        tokens,
		CacheReuse:    aggregateStatsSummaryCache(cacheRows),
	}

	if format == FormatText {
		printStatsSummaryText(stdout, summary)
	} else {
		_ = json.NewEncoder(stdout).Encode(summary)
	}
	return nil, nil
}

func statsBreakdownLogic(args []string, format string, window time.Duration, now time.Time, db *dbpkg.DB, stdout io.Writer) (any, error) {
	showHelp, err := validateStatsWindowCommand("ktx stats breakdown", args, format, window, stdout, printStatsBreakdownHelp)
	if err != nil || showHelp {
		return nil, err
	}

	now = now.UTC()
	from := now.Add(-window)
	rows, err := db.StatsSummaryRows(from, now)
	if err != nil {
		return nil, internalError("ktx stats breakdown: %v", err)
	}
	windowSummary, calls, tokens := aggregateStatsWindow(rows, window, now)
	breakdown := statsBreakdown{
		SchemaVersion:     statsBreakdownSchemaVersion,
		GeneratedAt:       now.Format(time.RFC3339Nano),
		Window:            windowSummary,
		Calls:             calls,
		Tokens:            tokens,
		DeliveryBreakdown: aggregateStatsBreakdown(rows),
	}

	if format == FormatText {
		printStatsBreakdownText(stdout, breakdown)
	} else {
		_ = json.NewEncoder(stdout).Encode(breakdown)
	}
	return nil, nil
}

func validateStatsWindowCommand(command string, args []string, format string, window time.Duration, stdout io.Writer, printHelp func(io.Writer)) (bool, error) {
	var showHelp bool
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")
	if err := fs.Parse(args); err != nil {
		return false, usageError("%s: %v", command, err)
	}
	if showHelp {
		printHelp(stdout)
		return true, nil
	}
	if len(fs.Args()) > 0 {
		return false, usageError("%s: unexpected argument %q", command, fs.Arg(0))
	}
	if format != FormatJSON && format != FormatText {
		return false, usageError("%s: invalid --format %q: want %q or %q", command, format, FormatJSON, FormatText)
	}
	if window <= 0 {
		return false, usageError("%s: --window must be positive", command)
	}
	return false, nil
}

func aggregateStatsWindow(rows []dbpkg.StatsSummaryRow, window time.Duration, now time.Time) (statsSummaryWindow, statsSummaryCalls, statsSummaryTokens) {
	from := now.Add(-window)
	windowSummary := statsSummaryWindow{
		Seconds: window.Seconds(),
		From:    from.Format(time.RFC3339Nano),
		To:      now.Format(time.RFC3339Nano),
	}
	var calls statsSummaryCalls
	var tokens statsSummaryTokens
	for _, row := range rows {
		calls.TotalWithOutput++
		if row.HasCompressedArtifact {
			calls.WithCompressedArtifact++
		}
		tokens.RawEstimated += estimatedTokens(row.RawChars)
		tokens.DeliveredEstimated += estimatedTokens(row.DeliveredChars)
	}
	seconds := window.Seconds()
	tokens.RawEstimatedPerSecond = float64(tokens.RawEstimated) / seconds
	tokens.DeliveredEstimatedPerSecond = float64(tokens.DeliveredEstimated) / seconds
	if tokens.RawEstimated > 0 {
		reduction := float64(tokens.RawEstimated-tokens.DeliveredEstimated) / float64(tokens.RawEstimated) * 100
		tokens.ReductionPercent = &reduction
	}
	return windowSummary, calls, tokens
}

func estimatedTokens(characters int) int {
	return (characters + 3) / 4
}

func aggregateStatsBreakdown(rows []dbpkg.StatsSummaryRow) statsBreakdownDelivery {
	type key struct {
		tool, command, subcommand, profile, route, disposition, sizeBand string
	}
	groupsByKey := make(map[key]*statsBreakdownGroup)
	for _, row := range rows {
		command, subcommand := statsReportingDimensions(row.Input)
		groupKey := key{
			tool: row.Tool, command: command, subcommand: subcommand,
			profile: row.Profile, route: row.Route, disposition: row.Disposition, sizeBand: statsBreakdownSizeBand(row.RawChars),
		}
		group := groupsByKey[groupKey]
		if group == nil {
			group = &statsBreakdownGroup{
				Tool: groupKey.tool, Command: optionalStatsBreakdownString(groupKey.command), Subcommand: optionalStatsBreakdownString(groupKey.subcommand),
				Profile: optionalStatsBreakdownString(groupKey.profile), Route: optionalStatsBreakdownString(groupKey.route),
				Disposition: groupKey.disposition, RawOutputSizeBand: groupKey.sizeBand,
			}
			groupsByKey[groupKey] = group
		}
		group.Calls++
		if row.HasCompressedArtifact {
			group.WithCompressedArtifact++
		}
		group.RawEstimated += estimatedTokens(row.RawChars)
		group.DeliveredEstimated += estimatedTokens(row.DeliveredChars)
	}

	groups := make([]statsBreakdownGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].DeliveredEstimated != groups[j].DeliveredEstimated {
			return groups[i].DeliveredEstimated > groups[j].DeliveredEstimated
		}
		left := statsBreakdownSortKey(groups[i])
		right := statsBreakdownSortKey(groups[j])
		return left < right
	})

	breakdown := statsBreakdownDelivery{
		DetailedGroupLimit: statsBreakdownDetailedLimit,
		DistinctGroups:     len(groups),
		Groups:             make([]statsBreakdownGroup, min(len(groups), statsBreakdownDetailedLimit)),
	}
	copy(breakdown.Groups, groups)
	breakdown.OmittedDistinctGroups = len(groups) - len(breakdown.Groups)
	for _, group := range groups[len(breakdown.Groups):] {
		breakdown.Other.Calls += group.Calls
		breakdown.Other.WithCompressedArtifact += group.WithCompressedArtifact
		breakdown.Other.RawEstimated += group.RawEstimated
		breakdown.Other.DeliveredEstimated += group.DeliveredEstimated
	}
	return breakdown
}

func statsReportingDimensions(input string) (string, string) {
	command := router.BinaryFromInput(input)
	switch command {
	case "cat", "find", "grep", "ls", "rg":
		return command, ""
	case "git":
	default:
		return "", ""
	}

	raw, ok := router.CommandFromInput(input)
	if !ok {
		return command, ""
	}
	for _, field := range strings.Fields(raw)[1:] {
		switch field {
		case "diff", "log", "status":
			return command, field
		}
	}
	return command, ""
}

func statsBreakdownSortKey(group statsBreakdownGroup) string {
	return strings.Join([]string{
		group.Tool, statsBreakdownString(group.Command), statsBreakdownString(group.Subcommand),
		statsBreakdownString(group.Profile), statsBreakdownString(group.Route), group.Disposition, group.RawOutputSizeBand,
	}, "\x00")
}

func optionalStatsBreakdownString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func statsBreakdownString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func statsBreakdownSizeBand(characters int) string {
	switch {
	case characters == 0:
		return "empty"
	case characters <= 1024:
		return "1-1k"
	case characters <= 10240:
		return "1k-10k"
	case characters <= 102400:
		return "10k-100k"
	default:
		return "100k+"
	}
}

func aggregateStatsSummaryCache(rows []dbpkg.StatsSummaryCacheRow) statsSummaryCacheReuse {
	var uncachedInput, cacheRead, cacheWrite statsSummaryIntegerAccumulator
	for _, row := range rows {
		uncachedInput.add(row.UncachedInput)
		cacheRead.add(row.CacheRead)
		cacheWrite.add(row.CacheWrite)
	}

	uncachedInputTotal := uncachedInput.measurement(len(rows))
	cacheReadTotal := cacheRead.measurement(len(rows))
	cacheWriteTotal := cacheWrite.measurement(len(rows))
	logicalInput := unavailableStatsSummaryInteger(aggregateStatsSummaryState(uncachedInputTotal.State, cacheReadTotal.State, cacheWriteTotal.State))
	share := unavailableStatsSummaryShare(logicalInput.State)
	if uncachedInputTotal.State == statsSummaryMeasured && cacheReadTotal.State == statsSummaryMeasured && cacheWriteTotal.State == statsSummaryMeasured {
		logical := new(big.Int).Add(uncachedInputTotal.Value, cacheReadTotal.Value)
		logical.Add(logical, cacheWriteTotal.Value)
		logicalInput = statsSummaryIntegerMeasurement{State: statsSummaryMeasured, Value: logical}
		if logical.Sign() > 0 {
			ratio := new(big.Rat).SetFrac(cacheReadTotal.Value, logical)
			value, _ := ratio.Float64()
			ratioText := ratio.RatString()
			share = statsSummaryShareMeasurement{State: statsSummaryMeasured, Value: &value, Ratio: &ratioText}
		} else {
			reason := "logical_input_not_positive"
			share = statsSummaryShareMeasurement{State: statsSummaryNotApplicable, Reason: &reason}
		}
	}

	return statsSummaryCacheReuse{
		ContractVersion:        reuse.RequiredContractVersion,
		Runtime:                reuse.RequiredRuntime,
		RuntimeVersion:         reuse.RequiredRuntimeVersion,
		ContributingModelSteps: len(rows),
		UncachedInput:          uncachedInputTotal,
		CacheRead:              cacheReadTotal,
		CacheWrite:             cacheWriteTotal,
		LogicalInput:           logicalInput,
		CachedInputShare:       share,
		Formula:                "cache_read / logical_input",
		Unit:                   "token_fraction",
	}
}

func (a *statsSummaryIntegerAccumulator) add(measurement dbpkg.IntegerMeasurement) {
	switch measurement.State {
	case dbpkg.MeasurementMeasured:
		if measurement.Value == nil || *measurement.Value < 0 {
			a.malformed = true
			return
		}
		a.value.Add(&a.value, big.NewInt(*measurement.Value))
	case dbpkg.MeasurementMalformed:
		a.malformed = true
	default:
		a.missing = true
	}
}

func (a *statsSummaryIntegerAccumulator) measurement(count int) statsSummaryIntegerMeasurement {
	state := statsSummaryMeasured
	switch {
	case a.malformed:
		state = statsSummaryMalformed
	case count == 0 || a.missing:
		state = statsSummaryMissing
	}
	if state != statsSummaryMeasured {
		return unavailableStatsSummaryInteger(state)
	}
	return statsSummaryIntegerMeasurement{State: state, Value: new(big.Int).Set(&a.value)}
}

func aggregateStatsSummaryState(states ...string) string {
	result := statsSummaryMeasured
	for _, state := range states {
		if state == statsSummaryMalformed {
			return statsSummaryMalformed
		}
		if state != statsSummaryMeasured {
			result = statsSummaryMissing
		}
	}
	return result
}

func unavailableStatsSummaryInteger(state string) statsSummaryIntegerMeasurement {
	return statsSummaryIntegerMeasurement{State: state}
}

func unavailableStatsSummaryShare(state string) statsSummaryShareMeasurement {
	return statsSummaryShareMeasurement{State: state}
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
		binary, _ := statsReportingDimensions(r.Input)
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
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WINDOW_FROM\tWINDOW_TO\tCALLS\tCOMPRESSED\tRAW_TOK\tDELIVERED_TOK\tRAW_TPS\tDELIVERED_TPS\tREDUCTION_PCT\tMODEL_STEPS\tUNCACHED_INPUT\tCACHE_READ\tCACHE_WRITE\tLOGICAL_INPUT\tCACHED_INPUT_PCT")
	fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%.4f\t%.4f\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
		summary.Window.From, summary.Window.To,
		summary.Calls.TotalWithOutput, summary.Calls.WithCompressedArtifact,
		summary.Tokens.RawEstimated, summary.Tokens.DeliveredEstimated,
		summary.Tokens.RawEstimatedPerSecond, summary.Tokens.DeliveredEstimatedPerSecond,
		formatStatsReduction(summary.Tokens.ReductionPercent), summary.CacheReuse.ContributingModelSteps,
		formatStatsSummaryInteger(summary.CacheReuse.UncachedInput),
		formatStatsSummaryInteger(summary.CacheReuse.CacheRead),
		formatStatsSummaryInteger(summary.CacheReuse.CacheWrite),
		formatStatsSummaryInteger(summary.CacheReuse.LogicalInput),
		formatStatsSummarySharePercent(summary.CacheReuse.CachedInputShare))
	tw.Flush() //nolint:errcheck // best-effort formatting; errors are not actionable
}

func printStatsBreakdownText(w io.Writer, breakdown statsBreakdown) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WINDOW_FROM\tWINDOW_TO\tCALLS\tCOMPRESSED\tRAW_TOK\tDELIVERED_TOK\tRAW_TPS\tDELIVERED_TPS\tREDUCTION_PCT")
	fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%.4f\t%.4f\t%s\n",
		breakdown.Window.From, breakdown.Window.To,
		breakdown.Calls.TotalWithOutput, breakdown.Calls.WithCompressedArtifact,
		breakdown.Tokens.RawEstimated, breakdown.Tokens.DeliveredEstimated,
		breakdown.Tokens.RawEstimatedPerSecond, breakdown.Tokens.DeliveredEstimatedPerSecond,
		formatStatsReduction(breakdown.Tokens.ReductionPercent))
	tw.Flush() //nolint:errcheck // best-effort formatting; errors are not actionable

	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tCOMMAND\tSUBCOMMAND\tPROFILE\tROUTE\tDISPOSITION\tSIZE_BAND\tCALLS\tCOMPRESSED\tRAW_TOK\tDELIVERED_TOK")
	for _, group := range breakdown.DeliveryBreakdown.Groups {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\n",
			group.Tool, formatStatsBreakdownOptional(group.Command), formatStatsBreakdownOptional(group.Subcommand),
			formatStatsBreakdownOptional(group.Profile), formatStatsBreakdownOptional(group.Route), group.Disposition,
			group.RawOutputSizeBand, group.Calls, group.WithCompressedArtifact, group.RawEstimated, group.DeliveredEstimated)
	}
	if breakdown.DeliveryBreakdown.Other.Calls > 0 {
		other := breakdown.DeliveryBreakdown.Other
		fmt.Fprintf(tw, "other\t-\t-\t-\t-\t-\t-\t%d\t%d\t%d\t%d\n",
			other.Calls, other.WithCompressedArtifact, other.RawEstimated, other.DeliveredEstimated)
	}
	tw.Flush() //nolint:errcheck // best-effort formatting; errors are not actionable
}

func formatStatsReduction(reduction *float64) string {
	if reduction == nil {
		return "-"
	}
	return strconv.FormatFloat(*reduction, 'f', 2, 64)
}

func formatStatsBreakdownOptional(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}

func formatStatsSummaryInteger(measurement statsSummaryIntegerMeasurement) string {
	if measurement.State == statsSummaryMeasured && measurement.Value != nil {
		return measurement.Value.String()
	}
	return measurement.State
}

func formatStatsSummarySharePercent(measurement statsSummaryShareMeasurement) string {
	if measurement.State == statsSummaryMeasured && measurement.Value != nil {
		return strconv.FormatFloat(*measurement.Value*100, 'f', 2, 64)
	}
	return measurement.State
}

// printStatsHelp writes the `ktx stats` help.
func printStatsHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx stats — per-tool-call statistics ranked by delivered tokens")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  ktx stats [--format text|json] [--limit N]")
	fmt.Fprintln(w, "  ktx stats summary [--window DURATION] [--format text|json]")
	fmt.Fprintln(w, "  ktx stats breakdown [--window DURATION] [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Lists tool/command groups ranked by total delivered tokens (descending).")
	fmt.Fprintln(w, "Uncompressed tool calls are included with delivered tokens equal to raw tokens.")
	fmt.Fprintln(w, "Groups with fewer than 2 calls or fewer than 50 delivered tokens are excluded.")
	fmt.Fprintln(w, "Summary emits fixed-shape window aggregates; breakdown emits detailed delivery groups.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --limit N   maximum number of groups to show (default 20)")
	fmt.Fprintln(w, "  --window DURATION  trailing window (default 30m; summary and breakdown only)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  structured JSON on stdout")
	fmt.Fprintln(w, "  text            tab-aligned output")
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
	fmt.Fprintln(w, "ktx stats summary — fixed-shape trailing-window aggregates")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx stats summary [--window DURATION] [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Counts calls with non-NULL output and an RFC3339 timestamp in the inclusive")
	fmt.Fprintln(w, "[now-window, now] interval. Rates use the full configured window duration.")
	fmt.Fprintln(w, "Each call uses its earliest retained compressed artifact; equal timestamps use")
	fmt.Fprintln(w, "the lexicographically smallest artifact ID. Calls without one use raw output.")
	fmt.Fprintln(w, "The fixed-shape response contains window, call, token, and cache aggregates only.")
	fmt.Fprintln(w, "Use 'ktx stats breakdown' for diagnostic delivery groups.")
	fmt.Fprintln(w, "Accepted OpenCode 1.18.5 model steps use their captured_at timestamp in the")
	fmt.Fprintln(w, "same interval. Cache token totals require every contributing value to be")
	fmt.Fprintln(w, "measured. The cached-input share is cache_read / logical_input; it is not a provider hit rate.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --window DURATION  positive Go duration (default 30m)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  schema_version 4 JSON object")
	fmt.Fprintln(w, "  text            one tab-aligned summary row")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
}

func printStatsBreakdownHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx stats breakdown — trailing-window delivery diagnostics")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx stats breakdown [--window DURATION] [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Counts calls with non-NULL output and an RFC3339 timestamp in the inclusive")
	fmt.Fprintln(w, "[now-window, now] interval. Rates use the full configured window duration.")
	fmt.Fprintln(w, "Each call uses its earliest retained compressed artifact; equal timestamps use")
	fmt.Fprintln(w, "the lexicographically smallest artifact ID. Calls without one use raw output.")
	fmt.Fprintln(w, "Reports up to 100 token-weighted disposition groups; omitted groups reconcile")
	fmt.Fprintln(w, "through exact other totals. Model-step telemetry is not queried or emitted.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --window DURATION  positive Go duration (default 30m)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  schema_version 1 JSON object")
	fmt.Fprintln(w, "  text            headline row and detailed group table")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
}
