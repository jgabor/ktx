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
	"math"
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
	statsSummarySchemaVersion   = 9
	statsBreakdownSchemaVersion = 3
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
	SchemaVersion              int                                    `json:"schema_version"`
	Window                     statsSummaryWindow                     `json:"window"`
	Calls                      statsSummaryCalls                      `json:"calls"`
	Tokens                     statsSummaryCompactTokens              `json:"tokens"`
	PromptCacheTokenHitRatePct statsSummaryPromptCacheTokenHitRatePct `json:"prompt_cache_token_hit_rate_pct"`
	Latency                    statsLatency                           `json:"latency"`
}

type statsBreakdown struct {
	SchemaVersion     int                    `json:"schema_version"`
	GeneratedAt       string                 `json:"generated_at"`
	Window            statsSummaryWindow     `json:"window"`
	Calls             statsSummaryCalls      `json:"calls"`
	Outcomes          statsOutcomeTotals     `json:"outcomes"`
	Tokens            statsSummaryTokens     `json:"tokens"`
	DeliveryBreakdown statsBreakdownDelivery `json:"delivery_breakdown"`
	Latency           statsLatency           `json:"latency"`
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

type statsOutcomeTotals struct {
	SuccessfulCalls     int      `json:"successful_calls"`
	ErrorCalls          int      `json:"error_calls"`
	UnknownCalls        int      `json:"unknown_calls"`
	KnownCalls          int      `json:"known_calls"`
	TotalCalls          int      `json:"total_calls"`
	ErrorRatePctOfKnown *float64 `json:"error_rate_pct_of_known"`
	OutcomeCoveragePct  *float64 `json:"outcome_coverage_pct"`
}

type statsSummaryTokens struct {
	RawEstimated                int      `json:"raw_estimated"`
	DeliveredEstimated          int      `json:"delivered_estimated"`
	RawEstimatedPerSecond       float64  `json:"raw_estimated_per_second"`
	DeliveredEstimatedPerSecond float64  `json:"delivered_estimated_per_second"`
	ReductionPercent            *float64 `json:"reduction_percent"`
}

type statsSummaryCompactTokens struct {
	RawEstimated       int `json:"raw_estimated"`
	DeliveredEstimated int `json:"delivered_estimated"`
}

type statsSummaryPromptCacheTokenHitRatePct struct {
	State                  string   `json:"state"`
	Value                  *float64 `json:"value"`
	ContributingModelSteps int      `json:"contributing_model_steps"`
}

type statsLatency struct {
	State            string                   `json:"state"`
	Unit             string                   `json:"unit"`
	Authority        string                   `json:"authority"`
	MeasuredCalls    int                      `json:"measured_calls"`
	UnknownCalls     int                      `json:"unknown_calls"`
	MalformedCalls   int                      `json:"malformed_calls"`
	TotalCalls       int                      `json:"total_calls"`
	CoveragePct      *float64                 `json:"coverage_pct"`
	ContributingFrom *string                  `json:"contributing_from"`
	ContributingTo   *string                  `json:"contributing_to"`
	Distribution     statsLatencyDistribution `json:"distribution_ms"`
}

type statsLatencyDistribution struct {
	Count   int      `json:"count"`
	Min     *float64 `json:"min"`
	P50     *float64 `json:"p50"`
	P95     *float64 `json:"p95"`
	Max     *float64 `json:"max"`
	Average *float64 `json:"average"`
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
	windowSummary, calls, _, tokens := aggregateStatsWindow(rows, window, now)
	summary := statsSummary{
		SchemaVersion: statsSummarySchemaVersion,
		Window:        windowSummary,
		Calls:         calls,
		Tokens: statsSummaryCompactTokens{
			RawEstimated:       tokens.RawEstimated,
			DeliveredEstimated: tokens.DeliveredEstimated,
		},
		PromptCacheTokenHitRatePct: aggregateStatsSummaryPromptCacheTokenHitRatePct(cacheRows),
		Latency:                    aggregateStatsLatency(rows),
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
	windowSummary, calls, outcomes, tokens := aggregateStatsWindow(rows, window, now)
	breakdown := statsBreakdown{
		SchemaVersion:     statsBreakdownSchemaVersion,
		GeneratedAt:       now.Format(time.RFC3339Nano),
		Window:            windowSummary,
		Calls:             calls,
		Outcomes:          outcomes,
		Tokens:            tokens,
		DeliveryBreakdown: aggregateStatsBreakdown(rows),
		Latency:           aggregateStatsLatency(rows),
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

func aggregateStatsWindow(rows []dbpkg.StatsSummaryRow, window time.Duration, now time.Time) (statsSummaryWindow, statsSummaryCalls, statsOutcomeTotals, statsSummaryTokens) {
	from := now.Add(-window)
	windowSummary := statsSummaryWindow{
		Seconds: window.Seconds(),
		From:    from.Format(time.RFC3339Nano),
		To:      now.Format(time.RFC3339Nano),
	}
	var calls statsSummaryCalls
	var outcomes statsOutcomeTotals
	var tokens statsSummaryTokens
	for _, row := range rows {
		calls.TotalWithOutput++
		outcomes.TotalCalls++
		switch {
		case row.IsError == nil:
			outcomes.UnknownCalls++
		case *row.IsError:
			outcomes.ErrorCalls++
		default:
			outcomes.SuccessfulCalls++
		}
		if row.HasCompressedArtifact {
			calls.WithCompressedArtifact++
		}
		tokens.RawEstimated += estimatedTokens(row.RawChars)
		tokens.DeliveredEstimated += estimatedTokens(row.DeliveredChars)
	}
	outcomes.KnownCalls = outcomes.SuccessfulCalls + outcomes.ErrorCalls
	if outcomes.KnownCalls > 0 {
		errorRate := float64(outcomes.ErrorCalls) / float64(outcomes.KnownCalls) * 100
		outcomes.ErrorRatePctOfKnown = &errorRate
	}
	if outcomes.TotalCalls > 0 {
		coverage := float64(outcomes.KnownCalls) / float64(outcomes.TotalCalls) * 100
		outcomes.OutcomeCoveragePct = &coverage
	}
	seconds := window.Seconds()
	tokens.RawEstimatedPerSecond = float64(tokens.RawEstimated) / seconds
	tokens.DeliveredEstimatedPerSecond = float64(tokens.DeliveredEstimated) / seconds
	if tokens.RawEstimated > 0 {
		reduction := float64(tokens.RawEstimated-tokens.DeliveredEstimated) / float64(tokens.RawEstimated) * 100
		tokens.ReductionPercent = &reduction
	}
	return windowSummary, calls, outcomes, tokens
}

func aggregateStatsLatency(rows []dbpkg.StatsSummaryRow) statsLatency {
	result := statsLatency{State: "unavailable", Unit: dbpkg.LatencyUnitMilliseconds, Authority: dbpkg.LatencyAuthorityRuntime}
	values := make([]float64, 0, len(rows))
	var first, last string
	for _, row := range rows {
		result.TotalCalls++
		switch row.LatencyState {
		case dbpkg.LatencyStateMeasured:
			if row.LatencyMs == nil || row.LatencyAuthority != dbpkg.LatencyAuthorityRuntime {
				result.MalformedCalls++
				continue
			}
			result.MeasuredCalls++
			values = append(values, float64(*row.LatencyMs))
			if first == "" || row.Timestamp < first {
				first = row.Timestamp
			}
			if last == "" || row.Timestamp > last {
				last = row.Timestamp
			}
		case dbpkg.LatencyStateMalformed:
			result.MalformedCalls++
		default:
			result.UnknownCalls++
		}
	}
	if result.TotalCalls > 0 {
		coverage := float64(result.MeasuredCalls) / float64(result.TotalCalls) * 100
		result.CoveragePct = &coverage
	}
	result.Distribution.Count = len(values)
	if len(values) == 0 {
		return result
	}
	result.State = "measured"
	result.ContributingFrom, result.ContributingTo = &first, &last
	sort.Float64s(values)
	min, max := values[0], values[len(values)-1]
	p50, p95 := statsPercentile(values, .5), statsPercentile(values, .95)
	total := 0.0
	for _, value := range values {
		total += value
	}
	average := math.Round(total/float64(len(values))*100) / 100
	result.Distribution.Min, result.Distribution.P50 = &min, &p50
	result.Distribution.P95, result.Distribution.Max = &p95, &max
	result.Distribution.Average = &average
	return result
}

func statsPercentile(values []float64, fraction float64) float64 {
	position := float64(len(values)-1) * fraction
	lower, upper := int(math.Floor(position)), int(math.Ceil(position))
	value := values[lower]
	if lower != upper {
		value += (values[upper] - value) * (position - float64(lower))
	}
	return math.Round(value*100) / 100
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

func aggregateStatsSummaryPromptCacheTokenHitRatePct(rows []dbpkg.StatsSummaryCacheRow) statsSummaryPromptCacheTokenHitRatePct {
	var uncachedInput, cacheRead, cacheWrite statsSummaryIntegerAccumulator
	for _, row := range rows {
		uncachedInput.add(row.UncachedInput)
		cacheRead.add(row.CacheRead)
		cacheWrite.add(row.CacheWrite)
	}

	result := statsSummaryPromptCacheTokenHitRatePct{State: statsSummaryMissing, ContributingModelSteps: len(rows)}
	state := aggregateStatsSummaryState(
		uncachedInput.state(len(rows)),
		cacheRead.state(len(rows)),
		cacheWrite.state(len(rows)),
	)
	if state != statsSummaryMeasured {
		result.State = state
		return result
	}

	logicalInput := new(big.Int).Add(&uncachedInput.value, &cacheRead.value)
	logicalInput.Add(logicalInput, &cacheWrite.value)
	if logicalInput.Sign() == 0 {
		result.State = statsSummaryNotApplicable
		return result
	}
	percentage := new(big.Rat).SetFrac(&cacheRead.value, logicalInput)
	percentage.Mul(percentage, big.NewRat(100, 1))
	value, _ := percentage.Float64()
	result.State = statsSummaryMeasured
	result.Value = &value
	return result
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

func (a *statsSummaryIntegerAccumulator) state(count int) string {
	switch {
	case a.malformed:
		return statsSummaryMalformed
	case count == 0 || a.missing:
		return statsSummaryMissing
	}
	return statsSummaryMeasured
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
	fmt.Fprintln(tw, "WINDOW_SECONDS\tWINDOW_FROM\tWINDOW_TO\tCALLS\tCOMPRESSED\tRAW_TOK\tDELIVERED_TOK\tPROMPT_CACHE_TOKEN_HIT_RATE_PCT\tCONTRIBUTING_MODEL_STEPS\tLATENCY_STATE\tLATENCY_UNIT\tLATENCY_AUTHORITY\tLATENCY_MEASURED\tLATENCY_UNKNOWN\tLATENCY_MALFORMED\tLATENCY_COVERAGE_PCT\tLATENCY_FROM\tLATENCY_TO\tLATENCY_MIN\tLATENCY_P50\tLATENCY_P95\tLATENCY_MAX\tLATENCY_AVERAGE")
	fields := []string{
		strconv.FormatFloat(summary.Window.Seconds, 'f', -1, 64),
		summary.Window.From,
		summary.Window.To,
		strconv.Itoa(summary.Calls.TotalWithOutput),
		strconv.Itoa(summary.Calls.WithCompressedArtifact),
		strconv.Itoa(summary.Tokens.RawEstimated),
		strconv.Itoa(summary.Tokens.DeliveredEstimated),
		formatStatsSummaryPromptCacheTokenHitRatePct(summary.PromptCacheTokenHitRatePct),
		strconv.Itoa(summary.PromptCacheTokenHitRatePct.ContributingModelSteps),
	}
	fields = append(fields, formatStatsLatencyText(summary.Latency)...)
	fmt.Fprintln(tw, strings.Join(fields, "\t"))
	tw.Flush() //nolint:errcheck // best-effort formatting; errors are not actionable
}

func printStatsBreakdownText(w io.Writer, breakdown statsBreakdown) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WINDOW_FROM\tWINDOW_TO\tCALLS\tCOMPRESSED\tSUCCESSFUL\tERROR\tUNKNOWN\tKNOWN\tERROR_PCT_KNOWN\tOUTCOME_COVERAGE_PCT\tRAW_TOK\tDELIVERED_TOK\tRAW_TPS\tDELIVERED_TPS\tREDUCTION_PCT\tLATENCY_STATE\tLATENCY_UNIT\tLATENCY_AUTHORITY\tLATENCY_MEASURED\tLATENCY_UNKNOWN\tLATENCY_MALFORMED\tLATENCY_COVERAGE_PCT\tLATENCY_FROM\tLATENCY_TO\tLATENCY_MIN\tLATENCY_P50\tLATENCY_P95\tLATENCY_MAX\tLATENCY_AVERAGE")
	fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%d\t%d\t%.4f\t%.4f\t%s\t%s\n",
		breakdown.Window.From, breakdown.Window.To,
		breakdown.Calls.TotalWithOutput, breakdown.Calls.WithCompressedArtifact,
		breakdown.Outcomes.SuccessfulCalls, breakdown.Outcomes.ErrorCalls, breakdown.Outcomes.UnknownCalls, breakdown.Outcomes.KnownCalls,
		formatStatsPercent(breakdown.Outcomes.ErrorRatePctOfKnown), formatStatsPercent(breakdown.Outcomes.OutcomeCoveragePct),
		breakdown.Tokens.RawEstimated, breakdown.Tokens.DeliveredEstimated,
		breakdown.Tokens.RawEstimatedPerSecond, breakdown.Tokens.DeliveredEstimatedPerSecond,
		formatStatsReduction(breakdown.Tokens.ReductionPercent), strings.Join(formatStatsLatencyText(breakdown.Latency), "\t"))
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

func formatStatsLatencyText(latency statsLatency) []string {
	value := func(v *float64) string {
		if v == nil {
			return "-"
		}
		return strconv.FormatFloat(*v, 'f', -1, 64)
	}
	text := func(v *string) string {
		if v == nil {
			return "-"
		}
		return *v
	}
	return []string{
		latency.State, latency.Unit, latency.Authority,
		strconv.Itoa(latency.MeasuredCalls), strconv.Itoa(latency.UnknownCalls), strconv.Itoa(latency.MalformedCalls),
		value(latency.CoveragePct), text(latency.ContributingFrom), text(latency.ContributingTo),
		value(latency.Distribution.Min), value(latency.Distribution.P50), value(latency.Distribution.P95),
		value(latency.Distribution.Max), value(latency.Distribution.Average),
	}
}

func formatStatsReduction(reduction *float64) string {
	if reduction == nil {
		return "-"
	}
	return strconv.FormatFloat(*reduction, 'f', 2, 64)
}

func formatStatsPercent(value *float64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatFloat(*value, 'f', 2, 64)
}

func formatStatsBreakdownOptional(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}

func formatStatsSummaryPromptCacheTokenHitRatePct(hitRate statsSummaryPromptCacheTokenHitRatePct) string {
	if hitRate.State != statsSummaryMeasured || hitRate.Value == nil {
		return hitRate.State
	}
	return strconv.FormatFloat(*hitRate.Value, 'f', -1, 64)
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
	fmt.Fprintln(w, "ktx stats summary — compact trailing-window aggregates")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx stats summary [--window DURATION] [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Counts calls with non-NULL output and an RFC3339 timestamp in the inclusive")
	fmt.Fprintln(w, "[now-window, now] interval.")
	fmt.Fprintln(w, "Each call uses its earliest retained compressed artifact; equal timestamps use")
	fmt.Fprintln(w, "the lexicographically smallest artifact ID. Calls without one use raw output.")
	fmt.Fprintln(w, "The closed response contains window, call totals, estimated token totals, and")
	fmt.Fprintln(w, "prompt_cache_token_hit_rate_pct. In percentage units it is 0 to 100 when measured;")
	fmt.Fprintln(w, "measured zero stays 0, and unavailable states keep their state with a null value.")
	fmt.Fprintln(w, "contributing_model_steps is a count. The rate pools all accepted models and providers")
	fmt.Fprintln(w, "for the supported runtime contract, weighting them by tokens rather than averaging rates.")
	fmt.Fprintln(w, "This is token-weighted runtime reporting,")
	fmt.Fprintln(w, "not a provider request hit rate or cache-internal claim.")
	fmt.Fprintln(w, "Latency reports measured, unknown, and malformed call counts, runtime authority,")
	fmt.Fprintln(w, "measured coverage and contributing bounds, and measured-only millisecond distributions.")
	fmt.Fprintln(w, "When no measured latency exists, state is unavailable and distribution values are null.")
	fmt.Fprintln(w, "Use 'ktx stats breakdown' for delivery diagnostics.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --window DURATION  positive Go duration (default 30m)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  schema_version 9 JSON object")
	fmt.Fprintln(w, "  text            one literal header and row; no separate schema version")
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
	fmt.Fprintln(w, "Aggregate outcomes report known-denominator error rate and full-population coverage.")
	fmt.Fprintln(w, "is_error=false is success, true is error, and missing/invalid is unknown;")
	fmt.Fprintln(w, "exit_code is diagnostic only. Known is success+error; total is known+unknown.")
	fmt.Fprintln(w, "Empty percentages are null/-; all-unknown is null/- error and 0 coverage.")
	fmt.Fprintln(w, "Detailed groups do not report per-group outcome rates.")
	fmt.Fprintln(w, "Latency reports measured, unknown, and malformed call counts, runtime authority,")
	fmt.Fprintln(w, "measured coverage and contributing bounds, and measured-only millisecond distributions.")
	fmt.Fprintln(w, "When no measured latency exists, state is unavailable and distribution values are null.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --window DURATION  positive Go duration (default 30m)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  schema_version 3 JSON object")
	fmt.Fprintln(w, "  text            same current headline fields plus groups; no separate schema version")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
}
