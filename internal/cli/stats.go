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
	"crypto/sha256"
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
	"github.com/jgabor/ktx/internal/cachemetric"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/router"
)

// statsDefaultLimit is the default maximum number of groups
// returned by `ktx stats` when --limit is not specified.
const (
	statsDefaultLimit           = 20
	statsDefaultWindow          = 30 * time.Minute
	statsSummarySchemaVersion   = 7
	statsBreakdownSchemaVersion = 2
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
	SchemaVersion                 int                                       `json:"schema_version"`
	GeneratedAt                   string                                    `json:"generated_at"`
	Window                        statsSummaryWindow                        `json:"window"`
	Calls                         statsSummaryCalls                         `json:"calls"`
	Outcomes                      statsOutcomeTotals                        `json:"outcomes"`
	Tokens                        statsSummaryTokens                        `json:"tokens"`
	CacheReuse                    statsSummaryCacheReuse                    `json:"cache_reuse"`
	PromptCacheTokenHitRate       statsSummaryPromptCacheTokenHitRate       `json:"prompt_cache_token_hit_rate"`
	RuntimeReportedCost           statsSummaryRuntimeReportedCost           `json:"runtime_reported_cost_usd"`
	APIEquivalentEstimatedAPICost statsSummaryAPIEquivalentEstimatedAPICost `json:"api_equivalent_estimated_cost_usd"`
}

type statsBreakdown struct {
	SchemaVersion     int                    `json:"schema_version"`
	GeneratedAt       string                 `json:"generated_at"`
	Window            statsSummaryWindow     `json:"window"`
	Calls             statsSummaryCalls      `json:"calls"`
	Outcomes          statsOutcomeTotals     `json:"outcomes"`
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
	ContractVersion          string                              `json:"contract_version"`
	Runtime                  string                              `json:"runtime"`
	RuntimeVersion           string                              `json:"runtime_version"`
	EvidenceScope            string                              `json:"evidence_scope"`
	RequestedWindowCoverage  statsSummaryRequestedWindowCoverage `json:"requested_window_coverage"`
	FirstContributingCapture *string                             `json:"first_contributing_capture"`
	LastContributingCapture  *string                             `json:"last_contributing_capture"`
	ObservedSpanSeconds      *float64                            `json:"observed_span_seconds"`
	ContributingSessions     int                                 `json:"contributing_sessions"`
	ContributingModelSteps   int                                 `json:"contributing_model_steps"`
	UncachedInput            statsSummaryIntegerMeasurement      `json:"uncached_input"`
	CacheRead                statsSummaryIntegerMeasurement      `json:"cache_read"`
	CacheWrite               statsSummaryIntegerMeasurement      `json:"cache_write"`
	LogicalInput             statsSummaryIntegerMeasurement      `json:"logical_input"`
	CachedInputShare         statsSummaryShareMeasurement        `json:"cached_input_share"`
	Formula                  string                              `json:"formula"`
	Unit                     string                              `json:"unit"`
}

type statsSummaryPromptCacheTokenHitRate struct {
	State          string                                    `json:"state"`
	Value          *float64                                  `json:"value"`
	Ratio          *string                                   `json:"ratio"`
	Reason         *string                                   `json:"reason"`
	Kind           string                                    `json:"kind"`
	Unit           string                                    `json:"unit"`
	Formula        string                                    `json:"formula"`
	Source         string                                    `json:"source"`
	Runtime        string                                    `json:"runtime"`
	RuntimeVersion string                                    `json:"runtime_version"`
	Provider       *string                                   `json:"provider"`
	Model          *string                                   `json:"model"`
	RawBuckets     statsSummaryPromptCacheTokenRawBuckets    `json:"raw_buckets"`
	LogicalInput   statsSummaryIntegerMeasurement            `json:"logical_input"`
	TemporalScope  statsSummaryPromptCacheTokenTemporalScope `json:"temporal_scope"`
}

type statsSummaryPromptCacheTokenRawBuckets struct {
	UncachedInput statsSummaryIntegerMeasurement `json:"uncached_input"`
	CacheRead     statsSummaryIntegerMeasurement `json:"cache_read"`
	CacheWrite    statsSummaryIntegerMeasurement `json:"cache_write"`
}

type statsSummaryPromptCacheTokenTemporalScope struct {
	Scope                    string                              `json:"scope"`
	RequestedWindowCoverage  statsSummaryRequestedWindowCoverage `json:"requested_window_coverage"`
	FirstContributingCapture *string                             `json:"first_contributing_capture"`
	LastContributingCapture  *string                             `json:"last_contributing_capture"`
	ObservedSpanSeconds      *float64                            `json:"observed_span_seconds"`
	ContributingSessions     int                                 `json:"contributing_sessions"`
	ContributingModelSteps   int                                 `json:"contributing_model_steps"`
}

type statsSummaryRuntimeIdentity struct {
	Provider string
	Model    string
}

type statsSummaryCacheIdentityGroup struct {
	Identity statsSummaryRuntimeIdentity
	Rows     []dbpkg.StatsSummaryCacheRow
}

type statsSummaryCacheIdentityPartition struct {
	Groups            []statsSummaryCacheIdentityGroup
	MissingModelSteps int
}

type statsSummaryRuntimeReportedCost struct {
	State            string                                    `json:"state"`
	Value            *float64                                  `json:"value"`
	Unit             string                                    `json:"unit"`
	Formula          string                                    `json:"formula"`
	Source           string                                    `json:"source"`
	BillingSemantics string                                    `json:"billing_semantics"`
	Reasons          []string                                  `json:"reasons"`
	TemporalScope    statsSummaryPromptCacheTokenTemporalScope `json:"temporal_scope"`
	RuntimeContracts []statsSummaryRuntimeContractPopulation   `json:"runtime_contracts"`
}

type statsSummaryRuntimeContractPopulation struct {
	Runtime                string `json:"runtime"`
	RuntimeVersion         string `json:"runtime_version"`
	ContributingModelSteps int    `json:"contributing_model_steps"`
}

type statsSummaryAPIEquivalentEstimatedAPICost struct {
	statsSummaryAPIEquivalentCostView
	Provider       *string                                          `json:"provider"`
	Model          *string                                          `json:"model"`
	IdentityGroups []statsSummaryAPIEquivalentEstimatedAPICostGroup `json:"identity_groups"`
}

type statsSummaryAPIEquivalentEstimatedAPICostGroup struct {
	statsSummaryAPIEquivalentCostView
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type statsSummaryAPIEquivalentCostView struct {
	State          string                                    `json:"state"`
	Value          *float64                                  `json:"value"`
	MicroUSD       *string                                   `json:"micro_usd"`
	Unit           string                                    `json:"unit"`
	Formula        string                                    `json:"formula"`
	Source         string                                    `json:"source"`
	Aggregation    string                                    `json:"aggregation"`
	Components     statsSummaryAPIEquivalentCostComponents   `json:"components"`
	Pricing        statsSummaryPricingSnapshot               `json:"pricing"`
	RateSelections []statsSummaryAPIEquivalentRateSelection  `json:"rate_selections"`
	TemporalScope  statsSummaryPromptCacheTokenTemporalScope `json:"temporal_scope"`
	Reasons        []string                                  `json:"reasons"`
}

type statsSummaryAPIEquivalentCostComponents struct {
	UncachedInput statsSummaryIntegerMeasurement `json:"uncached_input"`
	CacheRead     statsSummaryIntegerMeasurement `json:"cache_read"`
	CacheWrite    statsSummaryIntegerMeasurement `json:"cache_write"`
	Output        statsSummaryIntegerMeasurement `json:"output"`
	Reasoning     statsSummaryIntegerMeasurement `json:"reasoning"`
}

type statsSummaryPricingSnapshot struct {
	State        string   `json:"state"`
	Provider     *string  `json:"provider"`
	Model        *string  `json:"model"`
	Path         *string  `json:"path"`
	RecordSHA256 *string  `json:"record_sha256"`
	SourceURL    *string  `json:"source_url"`
	RetrievedAt  *string  `json:"retrieved_at"`
	Reasons      []string `json:"reasons"`
}

type statsSummaryAPIEquivalentRateSelection struct {
	Basis                   string                                  `json:"basis"`
	ContextTierSize         *int64                                  `json:"context_tier_size"`
	InputUSDPerMillion      float64                                 `json:"input_usd_per_million"`
	OutputUSDPerMillion     float64                                 `json:"output_usd_per_million"`
	CacheReadUSDPerMillion  *float64                                `json:"cache_read_usd_per_million"`
	CacheWriteUSDPerMillion *float64                                `json:"cache_write_usd_per_million"`
	ReasoningUSDPerMillion  float64                                 `json:"reasoning_usd_per_million"`
	ReasoningRatePolicy     string                                  `json:"reasoning_rate_policy"`
	ContributingModelSteps  int                                     `json:"contributing_model_steps"`
	Components              statsSummaryAPIEquivalentCostComponents `json:"components"`
	MicroUSD                string                                  `json:"micro_usd"`
}

type statsSummaryRequestedWindowCoverage struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Scope  string `json:"scope"`
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

type statsSummaryCostAccumulator struct {
	value     big.Rat
	missing   bool
	malformed bool
}

const (
	statsSummaryMeasured                              = "measured"
	statsSummaryUnavailable                           = "unavailable"
	statsSummaryMissing                               = "missing"
	statsSummaryMalformed                             = "malformed"
	statsSummaryNotApplicable                         = "not_applicable"
	statsSummaryPromptPrefixCacheEvidenceScope        = "provider_prompt_prefix_cache_tokens"
	statsSummaryRequestedWindowCoverageUnavailable    = "unavailable"
	statsSummaryCollectorLifecycleEvidenceUnavailable = "collector_lifecycle_evidence_unavailable"
	statsSummaryObservedSubsetScope                   = "observed_subset"
	statsSummaryRuntimeIdentityMissing                = "runtime_emitted_identity_missing"
	statsSummaryRuntimeIdentityMixed                  = "runtime_emitted_identity_mixed"
	statsSummaryNoContributingModelSteps              = "no_contributing_model_steps"
	statsSummaryUnsupportedRuntimeOrVersion           = "unsupported_runtime_or_version"
	statsSummaryRequiredTokenBucketMissing            = "required_token_bucket_missing"
	statsSummaryRequiredTokenBucketMalformed          = "required_token_bucket_malformed"
	statsSummaryLogicalInputNotPositive               = "logical_input_not_positive"
	statsSummaryPromptCacheTokenHitRateKind           = cachemetric.PromptCacheTokenHitRateKind
	statsSummaryPromptCacheTokenHitRateSource         = "runtime_emitted_model_step_telemetry"
	statsSummaryPromptCacheTokenHitRateFormula        = cachemetric.PromptCacheTokenHitRateFormula
	statsSummaryRuntimeReportedCostSource             = "runtime_emitted_model_step_telemetry"
	statsSummaryRuntimeReportedCostMissing            = "runtime_reported_cost_missing"
	statsSummaryRuntimeReportedCostMalformed          = "runtime_reported_cost_malformed"
	statsSummaryRuntimeReportedCostNonfinite          = "runtime_reported_cost_nonfinite"
	statsSummaryAPIEquivalentCostSource               = "runtime_emitted_model_step_telemetry_and_retained_pricing"
	statsSummaryAPIEquivalentCostFormula              = "(uncached_input*input_price + cache_read*cache_read_price + cache_write*cache_write_price + output*output_price + reasoning*reasoning_price)/1000000"
	statsSummaryAPIEquivalentCostAggregation          = "round_each_model_step_to_micro_usd_then_sum"
	statsSummaryPricingSnapshotUnavailable            = "pricing_snapshot_unavailable"
	statsSummaryTokenMalformedSuffix                  = "_malformed"
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
	cacheRows, err := db.StatsSummaryCacheRows(from, now)
	if err != nil {
		return nil, internalError("ktx stats summary: %v", err)
	}
	supportedCacheRows, unsupportedCacheRows := partitionStatsSummaryCacheRuntimeContract(cacheRows)
	var prices *dbpkg.Prices
	var pricesErr error
	if len(supportedCacheRows) > 0 {
		prices, pricesErr = dbpkg.LoadPrices()
	}
	windowSummary, calls, outcomes, tokens := aggregateStatsWindow(rows, window, now)
	summary := statsSummary{
		SchemaVersion:                 statsSummarySchemaVersion,
		GeneratedAt:                   now.Format(time.RFC3339Nano),
		Window:                        windowSummary,
		Calls:                         calls,
		Outcomes:                      outcomes,
		Tokens:                        tokens,
		CacheReuse:                    aggregateStatsSummaryCache(supportedCacheRows),
		PromptCacheTokenHitRate:       aggregateStatsSummaryPromptCacheTokenHitRate(supportedCacheRows, len(unsupportedCacheRows)),
		RuntimeReportedCost:           aggregateStatsSummaryRuntimeReportedCost(cacheRows),
		APIEquivalentEstimatedAPICost: aggregateStatsSummaryAPIEquivalentEstimatedAPICost(supportedCacheRows, len(unsupportedCacheRows), prices, pricesErr),
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
	sessions := make(map[string]struct{}, len(rows))
	var firstCapture, lastCapture time.Time
	var hasCapture bool
	for _, row := range rows {
		uncachedInput.add(row.UncachedInput)
		cacheRead.add(row.CacheRead)
		cacheWrite.add(row.CacheWrite)
		sessions[row.SessionID] = struct{}{}
		if !hasCapture || row.CapturedAt.Before(firstCapture) {
			firstCapture = row.CapturedAt
		}
		if !hasCapture || row.CapturedAt.After(lastCapture) {
			lastCapture = row.CapturedAt
		}
		hasCapture = true
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

	cache := statsSummaryCacheReuse{
		ContractVersion: reuse.RequiredContractVersion,
		Runtime:         reuse.RequiredRuntime,
		RuntimeVersion:  reuse.RequiredRuntimeVersion,
		EvidenceScope:   statsSummaryPromptPrefixCacheEvidenceScope,
		RequestedWindowCoverage: statsSummaryRequestedWindowCoverage{
			State:  statsSummaryRequestedWindowCoverageUnavailable,
			Reason: statsSummaryCollectorLifecycleEvidenceUnavailable,
			Scope:  statsSummaryObservedSubsetScope,
		},
		ContributingSessions:   len(sessions),
		ContributingModelSteps: len(rows),
		UncachedInput:          uncachedInputTotal,
		CacheRead:              cacheReadTotal,
		CacheWrite:             cacheWriteTotal,
		LogicalInput:           logicalInput,
		CachedInputShare:       share,
		Formula:                "cache_read / logical_input",
		Unit:                   "token_fraction",
	}
	if hasCapture {
		first := firstCapture.UTC().Format(time.RFC3339Nano)
		last := lastCapture.UTC().Format(time.RFC3339Nano)
		span := lastCapture.Sub(firstCapture).Seconds()
		cache.FirstContributingCapture = &first
		cache.LastContributingCapture = &last
		cache.ObservedSpanSeconds = &span
	}
	return cache
}

func partitionStatsSummaryCacheRuntimeContract(rows []dbpkg.StatsSummaryCacheRow) (supported, unsupported []dbpkg.StatsSummaryCacheRow) {
	for _, row := range rows {
		if row.Runtime == reuse.RequiredRuntime && row.RuntimeVersion == reuse.RequiredRuntimeVersion {
			supported = append(supported, row)
			continue
		}
		unsupported = append(unsupported, row)
	}
	return supported, unsupported
}

func aggregateStatsSummaryPromptCacheTokenHitRate(rows []dbpkg.StatsSummaryCacheRow, unsupportedModelSteps int) statsSummaryPromptCacheTokenHitRate {
	cache := aggregateStatsSummaryCache(rows)
	rate := newStatsSummaryPromptCacheTokenHitRate(cache)
	if len(rows) == 0 {
		if unsupportedModelSteps > 0 {
			rate.Reason = statsSummaryString(statsSummaryUnsupportedRuntimeOrVersion)
		} else {
			rate.Reason = statsSummaryString(statsSummaryNoContributingModelSteps)
		}
		return rate
	}

	partition := partitionStatsSummaryCacheIdentity(rows)
	identity, identityReason := partition.aggregateIdentity()
	if identityReason != "" {
		rate.Reason = statsSummaryString(identityReason)
		return rate
	}

	cache = aggregateStatsSummaryCache(partition.Groups[0].Rows)
	rate.Provider = statsSummaryString(identity.Provider)
	rate.Model = statsSummaryString(identity.Model)
	rate.RawBuckets = statsSummaryPromptCacheTokenRawBuckets{
		UncachedInput: cache.UncachedInput,
		CacheRead:     cache.CacheRead,
		CacheWrite:    cache.CacheWrite,
	}
	rate.LogicalInput = cache.LogicalInput
	rate.TemporalScope = statsSummaryPromptCacheTokenTemporalScopeFromCache(cache)

	switch {
	case cache.UncachedInput.State == statsSummaryMalformed || cache.CacheRead.State == statsSummaryMalformed || cache.CacheWrite.State == statsSummaryMalformed:
		rate.Reason = statsSummaryString(statsSummaryRequiredTokenBucketMalformed)
	case cache.UncachedInput.State != statsSummaryMeasured || cache.CacheRead.State != statsSummaryMeasured || cache.CacheWrite.State != statsSummaryMeasured:
		rate.Reason = statsSummaryString(statsSummaryRequiredTokenBucketMissing)
	case cache.LogicalInput.Value == nil || cache.LogicalInput.Value.Sign() <= 0:
		rate.Reason = statsSummaryString(statsSummaryLogicalInputNotPositive)
	default:
		_, ratio, measured := cachemetric.PromptCacheTokenHitRate(cache.UncachedInput.Value, cache.CacheRead.Value, cache.CacheWrite.Value)
		if !measured {
			rate.Reason = statsSummaryString(statsSummaryLogicalInputNotPositive)
			return rate
		}
		value, _ := ratio.Float64()
		ratioText := ratio.RatString()
		rate.State = statsSummaryMeasured
		rate.Value = &value
		rate.Ratio = &ratioText
		rate.Reason = nil
	}
	return rate
}

func newStatsSummaryPromptCacheTokenHitRate(cache statsSummaryCacheReuse) statsSummaryPromptCacheTokenHitRate {
	return statsSummaryPromptCacheTokenHitRate{
		State:          statsSummaryUnavailable,
		Kind:           statsSummaryPromptCacheTokenHitRateKind,
		Unit:           "token_fraction",
		Formula:        statsSummaryPromptCacheTokenHitRateFormula,
		Source:         statsSummaryPromptCacheTokenHitRateSource,
		Runtime:        reuse.RequiredRuntime,
		RuntimeVersion: reuse.RequiredRuntimeVersion,
		RawBuckets: statsSummaryPromptCacheTokenRawBuckets{
			UncachedInput: unavailableStatsSummaryInteger(statsSummaryUnavailable),
			CacheRead:     unavailableStatsSummaryInteger(statsSummaryUnavailable),
			CacheWrite:    unavailableStatsSummaryInteger(statsSummaryUnavailable),
		},
		LogicalInput:  unavailableStatsSummaryInteger(statsSummaryUnavailable),
		TemporalScope: statsSummaryPromptCacheTokenTemporalScopeFromCache(cache),
	}
}

func statsSummaryPromptCacheTokenTemporalScopeFromCache(cache statsSummaryCacheReuse) statsSummaryPromptCacheTokenTemporalScope {
	return statsSummaryPromptCacheTokenTemporalScope{
		Scope:                    cache.RequestedWindowCoverage.Scope,
		RequestedWindowCoverage:  cache.RequestedWindowCoverage,
		FirstContributingCapture: cache.FirstContributingCapture,
		LastContributingCapture:  cache.LastContributingCapture,
		ObservedSpanSeconds:      cache.ObservedSpanSeconds,
		ContributingSessions:     cache.ContributingSessions,
		ContributingModelSteps:   cache.ContributingModelSteps,
	}
}

func aggregateStatsSummaryRuntimeReportedCost(rows []dbpkg.StatsSummaryCacheRow) statsSummaryRuntimeReportedCost {
	cache := aggregateStatsSummaryCache(rows)
	result := statsSummaryRuntimeReportedCost{
		State:            statsSummaryUnavailable,
		Unit:             "usd",
		Formula:          "sum(runtime_reported_cost)",
		Source:           statsSummaryRuntimeReportedCostSource,
		BillingSemantics: statsSummaryUnavailable,
		Reasons:          []string{},
		TemporalScope:    statsSummaryPromptCacheTokenTemporalScopeFromCache(cache),
		RuntimeContracts: statsSummaryRuntimeContractPopulations(rows),
	}
	var total statsSummaryCostAccumulator
	for _, row := range rows {
		total.add(row.Cost)
	}
	switch {
	case len(rows) == 0:
		result.Reasons = []string{statsSummaryNoContributingModelSteps}
	case total.malformed:
		result.State = statsSummaryMalformed
		result.Reasons = []string{statsSummaryRuntimeReportedCostMalformed}
	case total.missing:
		result.State = statsSummaryMissing
		result.Reasons = []string{statsSummaryRuntimeReportedCostMissing}
	default:
		value, _ := total.value.Float64()
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			result.State = statsSummaryUnavailable
			result.Reasons = []string{statsSummaryRuntimeReportedCostNonfinite}
			return result
		}
		result.State = statsSummaryMeasured
		result.Value = &value
	}
	return result
}

func statsSummaryRuntimeContractPopulations(rows []dbpkg.StatsSummaryCacheRow) []statsSummaryRuntimeContractPopulation {
	type contract struct{ runtime, version string }
	counts := map[contract]int{}
	for _, row := range rows {
		counts[contract{runtime: row.Runtime, version: row.RuntimeVersion}]++
	}
	contracts := make([]statsSummaryRuntimeContractPopulation, 0, len(counts))
	for key, count := range counts {
		contracts = append(contracts, statsSummaryRuntimeContractPopulation{
			Runtime: key.runtime, RuntimeVersion: key.version, ContributingModelSteps: count,
		})
	}
	sort.Slice(contracts, func(i, j int) bool {
		if contracts[i].Runtime != contracts[j].Runtime {
			return contracts[i].Runtime < contracts[j].Runtime
		}
		return contracts[i].RuntimeVersion < contracts[j].RuntimeVersion
	})
	return contracts
}

func aggregateStatsSummaryAPIEquivalentEstimatedAPICost(rows []dbpkg.StatsSummaryCacheRow, unsupportedModelSteps int, prices *dbpkg.Prices, pricesErr error) statsSummaryAPIEquivalentEstimatedAPICost {
	cache := aggregateStatsSummaryCache(rows)
	result := statsSummaryAPIEquivalentEstimatedAPICost{
		statsSummaryAPIEquivalentCostView: newStatsSummaryAPIEquivalentCostView(cache),
		IdentityGroups:                    []statsSummaryAPIEquivalentEstimatedAPICostGroup{},
	}
	if len(rows) == 0 {
		if unsupportedModelSteps > 0 {
			result.Reasons = []string{statsSummaryUnsupportedRuntimeOrVersion}
		} else {
			result.Reasons = []string{statsSummaryNoContributingModelSteps}
		}
		result.Pricing = unavailableStatsSummaryPricingSnapshot(result.Reasons)
		return result
	}

	partition := partitionStatsSummaryCacheIdentity(rows)
	for _, group := range partition.Groups {
		view := aggregateStatsSummaryAPIEquivalentEstimatedAPICostGroup(group.Identity, group.Rows, prices, pricesErr)
		result.IdentityGroups = append(result.IdentityGroups, statsSummaryAPIEquivalentEstimatedAPICostGroup{
			statsSummaryAPIEquivalentCostView: view,
			Provider:                          group.Identity.Provider,
			Model:                             group.Identity.Model,
		})
	}

	blockers := []string{}
	if unsupportedModelSteps > 0 {
		blockers = append(blockers, statsSummaryUnsupportedRuntimeOrVersion)
	}
	if partition.MissingModelSteps > 0 {
		blockers = append(blockers, statsSummaryRuntimeIdentityMissing)
	}
	if len(partition.Groups) > 1 {
		blockers = append(blockers, statsSummaryRuntimeIdentityMixed)
	}
	if len(blockers) > 0 {
		result.Reasons = orderStatsSummaryAPIEquivalentCostReasons(blockers)
		result.Pricing = unavailableStatsSummaryPricingSnapshot(result.Reasons)
		return result
	}

	group := result.IdentityGroups[0]
	result.statsSummaryAPIEquivalentCostView = group.statsSummaryAPIEquivalentCostView
	result.Provider = statsSummaryString(group.Provider)
	result.Model = statsSummaryString(group.Model)
	return result
}

func newStatsSummaryAPIEquivalentCostView(cache statsSummaryCacheReuse) statsSummaryAPIEquivalentCostView {
	return statsSummaryAPIEquivalentCostView{
		State:          statsSummaryUnavailable,
		Unit:           "usd",
		Formula:        statsSummaryAPIEquivalentCostFormula,
		Source:         statsSummaryAPIEquivalentCostSource,
		Aggregation:    statsSummaryAPIEquivalentCostAggregation,
		Components:     unavailableStatsSummaryAPIEquivalentCostComponents(statsSummaryUnavailable),
		Pricing:        unavailableStatsSummaryPricingSnapshot(nil),
		RateSelections: []statsSummaryAPIEquivalentRateSelection{},
		TemporalScope:  statsSummaryPromptCacheTokenTemporalScopeFromCache(cache),
		Reasons:        []string{},
	}
}

func aggregateStatsSummaryAPIEquivalentEstimatedAPICostGroup(identity statsSummaryRuntimeIdentity, rows []dbpkg.StatsSummaryCacheRow, prices *dbpkg.Prices, pricesErr error) statsSummaryAPIEquivalentCostView {
	cache := aggregateStatsSummaryCache(rows)
	result := newStatsSummaryAPIEquivalentCostView(cache)
	result.Components = aggregateStatsSummaryAPIEquivalentCostComponents(rows)
	pricing, price, priceAvailable := statsSummaryPricingSnapshotForIdentity(identity, prices, pricesErr)
	result.Pricing = pricing

	totalMicroUSD := big.NewInt(0)
	rateAccumulators := map[string]*statsSummaryAPIEquivalentRateAccumulator{}
	reasons := []string{}
	for _, row := range rows {
		estimate, stepReasons := statsSummaryAPIEquivalentCostForRow(row, price, priceAvailable)
		reasons = append(reasons, stepReasons...)
		if estimate == nil || estimate.Cost == nil || estimate.MicroUSD == nil {
			continue
		}
		totalMicroUSD.Add(totalMicroUSD, estimate.MicroUSD)
		key := statsSummaryAPIEquivalentRateSelectionKey(estimate.RateSelection)
		accumulator := rateAccumulators[key]
		if accumulator == nil {
			accumulator = &statsSummaryAPIEquivalentRateAccumulator{selection: *estimate.RateSelection}
			rateAccumulators[key] = accumulator
		}
		accumulator.add(row, estimate.MicroUSD)
	}
	if len(reasons) > 0 {
		result.Reasons = orderStatsSummaryAPIEquivalentCostReasons(reasons)
		return result
	}
	value, ok := dbpkg.APIEquivalentCostUSD(totalMicroUSD)
	if !ok {
		result.Reasons = []string{string(dbpkg.APIEquivalentCostNonfinite)}
		return result
	}
	microUSD := totalMicroUSD.String()
	result.State = statsSummaryMeasured
	result.Value = value
	result.MicroUSD = &microUSD
	result.RateSelections = statsSummaryAPIEquivalentRateSelections(rateAccumulators)
	return result
}

func statsSummaryPricingSnapshotForIdentity(identity statsSummaryRuntimeIdentity, prices *dbpkg.Prices, pricesErr error) (statsSummaryPricingSnapshot, dbpkg.Price, bool) {
	path := (*string)(nil)
	if prices != nil && prices.Path() != "" {
		path = statsSummaryString(prices.Path())
	}
	result := statsSummaryPricingSnapshot{
		State:    statsSummaryUnavailable,
		Provider: statsSummaryString(identity.Provider),
		Model:    statsSummaryString(identity.Model),
		Path:     path,
		Reasons:  []string{statsSummaryPricingSnapshotUnavailable},
	}
	if prices == nil || pricesErr != nil {
		return result, dbpkg.Price{}, false
	}
	price, ok := prices.LookupRecord(identity.Provider, identity.Model)
	if !ok {
		return result, dbpkg.Price{}, false
	}
	data, err := json.Marshal(struct {
		Provider string      `json:"provider"`
		Model    string      `json:"model"`
		Price    dbpkg.Price `json:"price"`
	}{Provider: identity.Provider, Model: identity.Model, Price: price})
	if err != nil {
		return result, dbpkg.Price{}, false
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	result.State = "retained"
	result.RecordSHA256 = &hash
	result.Reasons = []string{}
	if price.SourceURL != "" {
		result.SourceURL = statsSummaryString(price.SourceURL)
	}
	if price.RetrievedAt != "" {
		result.RetrievedAt = statsSummaryString(price.RetrievedAt)
	}
	return result, price, true
}

func unavailableStatsSummaryPricingSnapshot(reasons []string) statsSummaryPricingSnapshot {
	return statsSummaryPricingSnapshot{State: statsSummaryUnavailable, Reasons: append([]string{}, reasons...)}
}

func unavailableStatsSummaryAPIEquivalentCostComponents(state string) statsSummaryAPIEquivalentCostComponents {
	return statsSummaryAPIEquivalentCostComponents{
		UncachedInput: unavailableStatsSummaryInteger(state),
		CacheRead:     unavailableStatsSummaryInteger(state),
		CacheWrite:    unavailableStatsSummaryInteger(state),
		Output:        unavailableStatsSummaryInteger(state),
		Reasoning:     unavailableStatsSummaryInteger(state),
	}
}

func aggregateStatsSummaryAPIEquivalentCostComponents(rows []dbpkg.StatsSummaryCacheRow) statsSummaryAPIEquivalentCostComponents {
	var accumulator statsSummaryAPIEquivalentCostComponentsAccumulator
	for _, row := range rows {
		accumulator.add(row)
	}
	return accumulator.measurement(len(rows))
}

type statsSummaryAPIEquivalentCostComponentsAccumulator struct {
	uncachedInput statsSummaryIntegerAccumulator
	cacheRead     statsSummaryIntegerAccumulator
	cacheWrite    statsSummaryIntegerAccumulator
	output        statsSummaryIntegerAccumulator
	reasoning     statsSummaryIntegerAccumulator
}

func (a *statsSummaryAPIEquivalentCostComponentsAccumulator) add(row dbpkg.StatsSummaryCacheRow) {
	a.uncachedInput.add(row.UncachedInput)
	a.cacheRead.add(row.CacheRead)
	a.cacheWrite.add(row.CacheWrite)
	a.output.add(row.Output)
	a.reasoning.add(row.Reasoning)
}

func (a *statsSummaryAPIEquivalentCostComponentsAccumulator) measurement(count int) statsSummaryAPIEquivalentCostComponents {
	return statsSummaryAPIEquivalentCostComponents{
		UncachedInput: a.uncachedInput.measurement(count),
		CacheRead:     a.cacheRead.measurement(count),
		CacheWrite:    a.cacheWrite.measurement(count),
		Output:        a.output.measurement(count),
		Reasoning:     a.reasoning.measurement(count),
	}
}

func statsSummaryAPIEquivalentCostForRow(row dbpkg.StatsSummaryCacheRow, price dbpkg.Price, priceAvailable bool) (*dbpkg.APIEquivalentCostEstimate, []string) {
	if statsSummaryAPIEquivalentContextTokenOverflow(row) {
		return nil, []string{string(dbpkg.APIEquivalentCostContextTokenOverflow)}
	}
	inputs, malformedFields := statsSummaryAPIEquivalentCostInputs(row)
	estimate := dbpkg.EstimateAPIEquivalentCost(inputs, price)
	reasons := make([]string, 0, len(estimate.UnavailableReasons)+len(malformedFields)+1)
	for _, reason := range estimate.UnavailableReasons {
		if statsSummaryAPIEquivalentReasonIsReplacedUnavailable(reason, malformedFields) || (!priceAvailable && statsSummaryAPIEquivalentReasonIsPrice(reason)) {
			continue
		}
		reasons = append(reasons, string(reason))
	}
	reasons = append(reasons, malformedFields...)
	if !priceAvailable {
		reasons = append(reasons, statsSummaryPricingSnapshotUnavailable)
	}
	if len(reasons) > 0 {
		return nil, reasons
	}
	if estimate.Cost == nil || estimate.MicroUSD == nil || estimate.RateSelection == nil {
		return nil, []string{string(dbpkg.APIEquivalentCostNonfinite)}
	}
	return &estimate, nil
}

func statsSummaryAPIEquivalentCostInputs(row dbpkg.StatsSummaryCacheRow) (dbpkg.APIEquivalentCostInputs, []string) {
	uncachedInput, uncachedMalformed := statsSummaryAPIEquivalentTokenInput(row.UncachedInput, "uncached_input")
	output, outputMalformed := statsSummaryAPIEquivalentTokenInput(row.Output, "output")
	reasoning, reasoningMalformed := statsSummaryAPIEquivalentTokenInput(row.Reasoning, "reasoning")
	cacheRead, cacheReadMalformed := statsSummaryAPIEquivalentTokenInput(row.CacheRead, "cache_read")
	cacheWrite, cacheWriteMalformed := statsSummaryAPIEquivalentTokenInput(row.CacheWrite, "cache_write")
	malformed := []string{}
	for _, field := range []string{uncachedMalformed, outputMalformed, reasoningMalformed, cacheReadMalformed, cacheWriteMalformed} {
		if field != "" {
			malformed = append(malformed, field)
		}
	}
	return dbpkg.APIEquivalentCostInputs{
		UncachedInput: uncachedInput,
		Output:        output,
		Reasoning:     reasoning,
		CacheRead:     cacheRead,
		CacheWrite:    cacheWrite,
	}, malformed
}

func statsSummaryAPIEquivalentTokenInput(measurement dbpkg.IntegerMeasurement, name string) (*float64, string) {
	if measurement.State == dbpkg.MeasurementMalformed {
		return nil, name + statsSummaryTokenMalformedSuffix
	}
	if measurement.State != dbpkg.MeasurementMeasured || measurement.Value == nil {
		return nil, ""
	}
	if *measurement.Value >= 1<<53 {
		return nil, name + "_invalid"
	}
	value := float64(*measurement.Value)
	return &value, ""
}

func statsSummaryAPIEquivalentContextTokenOverflow(row dbpkg.StatsSummaryCacheRow) bool {
	values := []dbpkg.IntegerMeasurement{row.UncachedInput, row.CacheRead, row.CacheWrite}
	total := int64(0)
	for _, measurement := range values {
		if measurement.State != dbpkg.MeasurementMeasured || measurement.Value == nil || *measurement.Value < 0 {
			return false
		}
		if *measurement.Value > math.MaxInt64-total {
			return true
		}
		total += *measurement.Value
	}
	return false
}

func statsSummaryAPIEquivalentReasonIsReplacedUnavailable(reason dbpkg.APIEquivalentCostUnavailableReason, replacementFields []string) bool {
	if !strings.HasSuffix(string(reason), "_unavailable") {
		return false
	}
	field := strings.TrimSuffix(string(reason), "_unavailable")
	for _, replacement := range replacementFields {
		if replacement == field+statsSummaryTokenMalformedSuffix || replacement == field+"_invalid" {
			return true
		}
	}
	return false
}

func statsSummaryAPIEquivalentReasonIsPrice(reason dbpkg.APIEquivalentCostUnavailableReason) bool {
	switch reason {
	case dbpkg.APIEquivalentCostInputPriceUnavailable, dbpkg.APIEquivalentCostOutputPriceUnavailable,
		dbpkg.APIEquivalentCostCacheReadPriceUnavailable, dbpkg.APIEquivalentCostCacheWritePriceUnavailable,
		dbpkg.APIEquivalentCostInputPriceInvalid, dbpkg.APIEquivalentCostOutputPriceInvalid,
		dbpkg.APIEquivalentCostCacheReadPriceInvalid, dbpkg.APIEquivalentCostCacheWritePriceInvalid,
		dbpkg.APIEquivalentCostReasoningPriceInvalid:
		return true
	default:
		return false
	}
}

type statsSummaryAPIEquivalentRateAccumulator struct {
	selection  dbpkg.APIEquivalentCostRateSelection
	components statsSummaryAPIEquivalentCostComponentsAccumulator
	microUSD   big.Int
	count      int
}

func (a *statsSummaryAPIEquivalentRateAccumulator) add(row dbpkg.StatsSummaryCacheRow, microUSD *big.Int) {
	a.components.add(row)
	a.microUSD.Add(&a.microUSD, microUSD)
	a.count++
}

func statsSummaryAPIEquivalentRateSelectionKey(selection *dbpkg.APIEquivalentCostRateSelection) string {
	if selection == nil {
		return ""
	}
	contextTierSize := ""
	if selection.ContextTierSize != nil {
		contextTierSize = strconv.FormatInt(*selection.ContextTierSize, 10)
	}
	optionalRate := func(value *float64) string {
		if value == nil {
			return ""
		}
		return strconv.FormatFloat(*value, 'g', -1, 64)
	}
	return strings.Join([]string{
		selection.Basis, contextTierSize,
		strconv.FormatFloat(selection.InputUSDPerMillion, 'g', -1, 64),
		strconv.FormatFloat(selection.OutputUSDPerMillion, 'g', -1, 64),
		optionalRate(selection.CacheReadUSDPerMillion), optionalRate(selection.CacheWriteUSDPerMillion),
		strconv.FormatFloat(selection.ReasoningUSDPerMillion, 'g', -1, 64), selection.ReasoningRatePolicy,
	}, "\x00")
}

func statsSummaryAPIEquivalentRateSelections(accumulators map[string]*statsSummaryAPIEquivalentRateAccumulator) []statsSummaryAPIEquivalentRateSelection {
	keys := make([]string, 0, len(accumulators))
	for key := range accumulators {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]statsSummaryAPIEquivalentRateSelection, 0, len(keys))
	for _, key := range keys {
		accumulator := accumulators[key]
		selection := accumulator.selection
		result = append(result, statsSummaryAPIEquivalentRateSelection{
			Basis:                   selection.Basis,
			ContextTierSize:         selection.ContextTierSize,
			InputUSDPerMillion:      selection.InputUSDPerMillion,
			OutputUSDPerMillion:     selection.OutputUSDPerMillion,
			CacheReadUSDPerMillion:  selection.CacheReadUSDPerMillion,
			CacheWriteUSDPerMillion: selection.CacheWriteUSDPerMillion,
			ReasoningUSDPerMillion:  selection.ReasoningUSDPerMillion,
			ReasoningRatePolicy:     selection.ReasoningRatePolicy,
			ContributingModelSteps:  accumulator.count,
			Components:              accumulator.components.measurement(accumulator.count),
			MicroUSD:                accumulator.microUSD.String(),
		})
	}
	return result
}

func orderStatsSummaryAPIEquivalentCostReasons(reasons []string) []string {
	structural := []string{
		statsSummaryNoContributingModelSteps,
		statsSummaryUnsupportedRuntimeOrVersion,
		statsSummaryRuntimeIdentityMissing,
		statsSummaryRuntimeIdentityMixed,
		statsSummaryPricingSnapshotUnavailable,
	}
	ordered := []string{}
	for _, expected := range structural {
		if containsStatsSummaryString(reasons, expected) {
			ordered = append(ordered, expected)
		}
	}
	typed := make([]dbpkg.APIEquivalentCostUnavailableReason, 0, len(reasons))
	for _, reason := range reasons {
		typed = append(typed, dbpkg.APIEquivalentCostUnavailableReason(reason))
	}
	for _, reason := range dbpkg.OrderAPIEquivalentCostUnavailableReasons(typed) {
		value := string(reason)
		if !containsStatsSummaryString(ordered, value) {
			ordered = append(ordered, value)
		}
	}
	remaining := []string{}
	for _, reason := range reasons {
		if reason != "" && !containsStatsSummaryString(ordered, reason) && !containsStatsSummaryString(remaining, reason) {
			remaining = append(remaining, reason)
		}
	}
	sort.Strings(remaining)
	return append(ordered, remaining...)
}

func containsStatsSummaryString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func statsSummaryString(value string) *string {
	return &value
}

// partitionStatsSummaryCacheIdentity separates model steps by their exact
// runtime-emitted provider/model pair. It never substitutes configured identity
// for source evidence.
func partitionStatsSummaryCacheIdentity(rows []dbpkg.StatsSummaryCacheRow) statsSummaryCacheIdentityPartition {
	byIdentity := make(map[statsSummaryRuntimeIdentity]int)
	partition := statsSummaryCacheIdentityPartition{}
	for _, row := range rows {
		identity, ok := statsSummaryCacheRowIdentity(row)
		if !ok {
			partition.MissingModelSteps++
			continue
		}
		index, exists := byIdentity[identity]
		if !exists {
			index = len(partition.Groups)
			byIdentity[identity] = index
			partition.Groups = append(partition.Groups, statsSummaryCacheIdentityGroup{Identity: identity})
		}
		partition.Groups[index].Rows = append(partition.Groups[index].Rows, row)
	}
	sort.Slice(partition.Groups, func(i, j int) bool {
		if partition.Groups[i].Identity.Provider != partition.Groups[j].Identity.Provider {
			return partition.Groups[i].Identity.Provider < partition.Groups[j].Identity.Provider
		}
		return partition.Groups[i].Identity.Model < partition.Groups[j].Identity.Model
	})
	return partition
}

// aggregateIdentity returns the identity that can authorize one aggregate
// metric. Multiple complete source identities remain available as separate
// groups, but cannot authorize a pooled metric.
func (partition statsSummaryCacheIdentityPartition) aggregateIdentity() (statsSummaryRuntimeIdentity, string) {
	if partition.MissingModelSteps > 0 {
		return statsSummaryRuntimeIdentity{}, statsSummaryRuntimeIdentityMissing
	}
	switch len(partition.Groups) {
	case 0:
		return statsSummaryRuntimeIdentity{}, statsSummaryNoContributingModelSteps
	case 1:
		return partition.Groups[0].Identity, ""
	default:
		return statsSummaryRuntimeIdentity{}, statsSummaryRuntimeIdentityMixed
	}
}

func statsSummaryCacheRowIdentity(row dbpkg.StatsSummaryCacheRow) (statsSummaryRuntimeIdentity, bool) {
	if row.Provider == nil || row.Model == nil || strings.TrimSpace(*row.Provider) == "" || strings.TrimSpace(*row.Model) == "" {
		return statsSummaryRuntimeIdentity{}, false
	}
	return statsSummaryRuntimeIdentity{Provider: *row.Provider, Model: *row.Model}, true
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

func (a *statsSummaryCostAccumulator) add(measurement dbpkg.CostMeasurement) {
	switch measurement.State {
	case dbpkg.MeasurementMeasured:
		if measurement.Value == nil || math.IsNaN(*measurement.Value) || math.IsInf(*measurement.Value, 0) || *measurement.Value < 0 {
			a.malformed = true
			return
		}
		value := new(big.Rat).SetFloat64(*measurement.Value)
		if value == nil {
			a.malformed = true
			return
		}
		a.value.Add(&a.value, value)
	case dbpkg.MeasurementMalformed:
		a.malformed = true
	default:
		a.missing = true
	}
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
	fmt.Fprintln(tw, "WINDOW_FROM\tWINDOW_TO\tCALLS\tCOMPRESSED\tSUCCESSFUL\tERROR\tUNKNOWN\tKNOWN\tERROR_PCT_KNOWN\tOUTCOME_COVERAGE_PCT\tRAW_TOK\tDELIVERED_TOK\tRAW_TPS\tDELIVERED_TPS\tREDUCTION_PCT\tPROVIDER_PROMPT_PREFIX_CACHE_EVIDENCE\tREQUESTED_WINDOW_COVERAGE\tREQUESTED_WINDOW_COVERAGE_REASON\tREQUESTED_WINDOW_COVERAGE_SCOPE\tFIRST_CONTRIBUTING_CAPTURE\tLAST_CONTRIBUTING_CAPTURE\tOBSERVED_SPAN_SECONDS\tCONTRIBUTING_KTX_SESSIONS\tCONTRIBUTING_MODEL_STEPS\tPROMPT_PREFIX_UNCACHED_INPUT\tPROMPT_PREFIX_CACHE_READ\tPROMPT_PREFIX_CACHE_WRITE\tPROMPT_PREFIX_LOGICAL_INPUT\tPROMPT_PREFIX_CACHED_INPUT_PCT\tPROMPT_CACHE_TOKEN_HIT_RATE_PCT\tPROMPT_CACHE_TOKEN_HIT_RATE_STATE\tPROMPT_CACHE_TOKEN_HIT_RATE_REASON\tPROMPT_CACHE_TOKEN_HIT_RATE_KIND\tPROMPT_CACHE_TOKEN_HIT_RATE_SOURCE\tPROMPT_CACHE_TOKEN_HIT_RATE_RUNTIME\tPROMPT_CACHE_TOKEN_HIT_RATE_RUNTIME_VERSION\tPROMPT_CACHE_TOKEN_HIT_RATE_PROVIDER\tPROMPT_CACHE_TOKEN_HIT_RATE_MODEL\tPROMPT_CACHE_TOKEN_HIT_RATE_SCOPE\tPROMPT_CACHE_TOKEN_HIT_UNCACHED_INPUT\tPROMPT_CACHE_TOKEN_HIT_CACHE_READ\tPROMPT_CACHE_TOKEN_HIT_CACHE_WRITE\tPROMPT_CACHE_TOKEN_HIT_LOGICAL_INPUT\tRUNTIME_REPORTED_COST_USD\tRUNTIME_REPORTED_COST_STATE\tRUNTIME_REPORTED_COST_REASONS\tRUNTIME_REPORTED_COST_SOURCE\tAPI_EQUIVALENT_ESTIMATED_COST_USD\tAPI_EQUIVALENT_ESTIMATED_COST_MICRO_USD\tAPI_EQUIVALENT_ESTIMATED_COST_STATE\tAPI_EQUIVALENT_ESTIMATED_COST_REASONS\tAPI_EQUIVALENT_ESTIMATED_COST_PROVIDER\tAPI_EQUIVALENT_ESTIMATED_COST_MODEL\tAPI_EQUIVALENT_ESTIMATED_COST_SOURCE\tAPI_EQUIVALENT_ESTIMATED_COST_PRICING_STATE\tAPI_EQUIVALENT_ESTIMATED_COST_PRICING_RECORD_SHA256\tAPI_EQUIVALENT_ESTIMATED_COST_PRICING_SOURCE_URL\tAPI_EQUIVALENT_ESTIMATED_COST_PRICING_RETRIEVED_AT")
	rate := summary.PromptCacheTokenHitRate
	reported := summary.RuntimeReportedCost
	estimated := summary.APIEquivalentEstimatedAPICost
	fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%d\t%d\t%.4f\t%.4f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s",
		summary.Window.From, summary.Window.To,
		summary.Calls.TotalWithOutput, summary.Calls.WithCompressedArtifact,
		summary.Outcomes.SuccessfulCalls, summary.Outcomes.ErrorCalls, summary.Outcomes.UnknownCalls, summary.Outcomes.KnownCalls,
		formatStatsPercent(summary.Outcomes.ErrorRatePctOfKnown), formatStatsPercent(summary.Outcomes.OutcomeCoveragePct),
		summary.Tokens.RawEstimated, summary.Tokens.DeliveredEstimated,
		summary.Tokens.RawEstimatedPerSecond, summary.Tokens.DeliveredEstimatedPerSecond,
		formatStatsReduction(summary.Tokens.ReductionPercent),
		summary.CacheReuse.EvidenceScope,
		summary.CacheReuse.RequestedWindowCoverage.State,
		summary.CacheReuse.RequestedWindowCoverage.Reason,
		summary.CacheReuse.RequestedWindowCoverage.Scope,
		formatStatsSummaryOptionalString(summary.CacheReuse.FirstContributingCapture),
		formatStatsSummaryOptionalString(summary.CacheReuse.LastContributingCapture),
		formatStatsObservedSpan(summary.CacheReuse.ObservedSpanSeconds),
		summary.CacheReuse.ContributingSessions,
		summary.CacheReuse.ContributingModelSteps,
		formatStatsSummaryInteger(summary.CacheReuse.UncachedInput),
		formatStatsSummaryInteger(summary.CacheReuse.CacheRead),
		formatStatsSummaryInteger(summary.CacheReuse.CacheWrite),
		formatStatsSummaryInteger(summary.CacheReuse.LogicalInput),
		formatStatsSummarySharePercent(summary.CacheReuse.CachedInputShare))
	fmt.Fprintf(tw, "\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		formatStatsSummaryPromptCacheTokenHitRatePercent(rate),
		rate.State,
		formatStatsSummaryOptionalString(rate.Reason),
		rate.Kind,
		rate.Source,
		rate.Runtime,
		rate.RuntimeVersion,
		formatStatsSummaryOptionalString(rate.Provider),
		formatStatsSummaryOptionalString(rate.Model),
		rate.TemporalScope.Scope,
		formatStatsSummaryInteger(rate.RawBuckets.UncachedInput),
		formatStatsSummaryInteger(rate.RawBuckets.CacheRead),
		formatStatsSummaryInteger(rate.RawBuckets.CacheWrite),
		formatStatsSummaryInteger(rate.LogicalInput),
		formatStatsSummaryUSD(reported.Value),
		reported.State,
		formatStatsSummaryReasons(reported.Reasons),
		reported.Source,
		formatStatsSummaryUSD(estimated.Value),
		formatStatsSummaryOptionalString(estimated.MicroUSD),
		estimated.State,
		formatStatsSummaryReasons(estimated.Reasons),
		formatStatsSummaryOptionalString(estimated.Provider),
		formatStatsSummaryOptionalString(estimated.Model),
		estimated.Source,
		estimated.Pricing.State,
		formatStatsSummaryOptionalString(estimated.Pricing.RecordSHA256),
		formatStatsSummaryOptionalString(estimated.Pricing.SourceURL),
		formatStatsSummaryOptionalString(estimated.Pricing.RetrievedAt))
	tw.Flush() //nolint:errcheck // best-effort formatting; errors are not actionable
}

func printStatsBreakdownText(w io.Writer, breakdown statsBreakdown) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WINDOW_FROM\tWINDOW_TO\tCALLS\tCOMPRESSED\tSUCCESSFUL\tERROR\tUNKNOWN\tKNOWN\tERROR_PCT_KNOWN\tOUTCOME_COVERAGE_PCT\tRAW_TOK\tDELIVERED_TOK\tRAW_TPS\tDELIVERED_TPS\tREDUCTION_PCT")
	fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%d\t%d\t%.4f\t%.4f\t%s\n",
		breakdown.Window.From, breakdown.Window.To,
		breakdown.Calls.TotalWithOutput, breakdown.Calls.WithCompressedArtifact,
		breakdown.Outcomes.SuccessfulCalls, breakdown.Outcomes.ErrorCalls, breakdown.Outcomes.UnknownCalls, breakdown.Outcomes.KnownCalls,
		formatStatsPercent(breakdown.Outcomes.ErrorRatePctOfKnown), formatStatsPercent(breakdown.Outcomes.OutcomeCoveragePct),
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

func formatStatsSummaryPromptCacheTokenHitRatePercent(rate statsSummaryPromptCacheTokenHitRate) string {
	if rate.State == statsSummaryMeasured && rate.Value != nil {
		return strconv.FormatFloat(*rate.Value*100, 'f', 2, 64)
	}
	return rate.State
}

func formatStatsSummaryUSD(value *float64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}

func formatStatsSummaryReasons(reasons []string) string {
	if len(reasons) == 0 {
		return "-"
	}
	return strings.Join(reasons, ",")
}

func formatStatsSummaryOptionalString(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}

func formatStatsObservedSpan(seconds *float64) string {
	if seconds == nil {
		return "-"
	}
	return strconv.FormatFloat(*seconds, 'f', -1, 64)
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
	fmt.Fprintln(w, "The fixed-shape response contains window, call, aggregate outcome, token, provider")
	fmt.Fprintln(w, "prompt/prefix-cache token, runtime-reported-cost, and API-equivalent estimated-cost views.")
	fmt.Fprintln(w, "Outcomes use the same call population: is_error=false is success, true is error,")
	fmt.Fprintln(w, "and missing/invalid is unknown; exit_code is diagnostic only. Known is success+error;")
	fmt.Fprintln(w, "total is known+unknown. Error rate is error/known; coverage is known/total.")
	fmt.Fprintln(w, "Empty percentages are null/-; all-unknown is null/- error and 0 coverage.")
	fmt.Fprintln(w, "Use 'ktx stats breakdown' for diagnostic delivery groups.")
	fmt.Fprintln(w, "Accepted OpenCode 1.18.5 model steps use their captured_at timestamp in the")
	fmt.Fprintln(w, "same interval. The report includes first/last contributing capture, observed")
	fmt.Fprintln(w, "span, session count, and model-step count. Requested-window coverage is")
	fmt.Fprintln(w, "unavailable (collector_lifecycle_evidence_unavailable); the evidence scope is")
	fmt.Fprintln(w, "the observed_subset of provider prompt/prefix-cache tokens, not filesystem,")
	fmt.Fprintln(w, "memory, database, HTTP, or other I/O caching. Cache token totals require every")
	fmt.Fprintln(w, "contributing value to be measured. PROMPT_CACHE_TOKEN_HIT_RATE_PCT is the")
	fmt.Fprintln(w, "token-weighted cache_read / (uncached_input + cache_read + cache_write) rate")
	fmt.Fprintln(w, "from runtime-emitted model-step telemetry. It is available only for one")
	fmt.Fprintln(w, "runtime-emitted provider/model population. Missing or mixed identity, missing")
	fmt.Fprintln(w, "or malformed buckets, zero logical input, and unsupported runtime/version stay")
	fmt.Fprintln(w, "unavailable with a machine-readable reason. A measured zero stays measured.")
	fmt.Fprintln(w, "It is not an I/O-cache rate, provider request hit rate, or evidence of provider")
	fmt.Fprintln(w, "TTL, eviction, eligibility, isolation, or prefix composition. The cached-input")
	fmt.Fprintln(w, "share remains a separate token share, not a provider hit rate.")
	fmt.Fprintln(w, "RUNTIME_REPORTED_COST_USD is the sum of runtime-emitted cost fields. It keeps")
	fmt.Fprintln(w, "a literal zero measured. Identity, token, pricing, and runtime-accounting")
	fmt.Fprintln(w, "failures never replace or populate it. Its billing semantics remain unavailable.")
	fmt.Fprintln(w, "API_EQUIVALENT_ESTIMATED_COST_USD is a separate price-derived USD estimate, never")
	fmt.Fprintln(w, "a reported-cost repair. It requires one runtime-emitted provider/model identity")
	fmt.Fprintln(w, "and complete uncached-input, cache-read, cache-write, output, and reasoning")
	fmt.Fprintln(w, "tokens for every OpenCode 1.18.5 model step. It reads only the retained local")
	fmt.Fprintln(w, "price record; it does not fetch prices or backfill identity from configuration.")
	fmt.Fprintln(w, "Each estimate rounds per model step to micro-USD before exact aggregation. The")
	fmt.Fprintln(w, "JSON view includes formula, components, identity groups, selected rates, and")
	fmt.Fprintln(w, "pricing-record provenance. Mixed identities retain independent group views but")
	fmt.Fprintln(w, "leave the aggregate estimate unavailable with ordered reasons.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --window DURATION  positive Go duration (default 30m)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  schema_version 7 JSON object")
	fmt.Fprintln(w, "  text            same current fields in one row; no separate schema version")
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
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --window DURATION  positive Go duration (default 30m)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  schema_version 2 JSON object")
	fmt.Fprintln(w, "  text            same current headline fields plus groups; no separate schema version")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
}
