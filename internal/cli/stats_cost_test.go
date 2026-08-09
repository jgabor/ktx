package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/cache/reuse"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

const statsCostAllRates = `{
  "openai": {
    "gpt-5": {
      "input": 2,
      "output": 4,
      "cache_read": 0.5,
      "cache_write": 3,
      "reasoning": 6,
      "source_url": "https://models.dev/api.json",
      "retrieved_at": "2026-08-09T12:00:00Z"
    }
  }
}`

func TestStatsSummaryCostViewsKeepReportedAndEstimatedEvidenceSeparate(t *testing.T) {
	provider, model := "openai", "gpt-5"
	rows := []dbpkg.StatsSummaryCacheRow{
		statsCostRow("one", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), &provider, &model,
			statsCostMeasurement(0), statsCostToken(100), statsCostToken(20), statsCostToken(3), statsCostToken(10), statsCostToken(2)),
	}
	prices := statsCostPrices(t, statsCostAllRates)

	reported := aggregateStatsSummaryRuntimeReportedCost(rows)
	estimated := aggregateStatsSummaryAPIEquivalentEstimatedAPICost(rows, 0, prices, nil)
	if reported.State != statsSummaryMeasured || reported.Value == nil || *reported.Value != 0 || reported.Source != statsSummaryRuntimeReportedCostSource || reported.BillingSemantics != statsSummaryUnavailable || len(reported.Reasons) != 0 {
		t.Fatalf("reported cost = %#v, want measured literal zero", reported)
	}
	if estimated.State != statsSummaryMeasured || estimated.Value == nil || math.Abs(*estimated.Value-0.000309) > 1e-15 || estimated.MicroUSD == nil || *estimated.MicroUSD != "309" {
		t.Fatalf("estimated cost = %#v, want measured 309 micro-USD", estimated)
	}
	if estimated.Provider == nil || *estimated.Provider != provider || estimated.Model == nil || *estimated.Model != model || estimated.Source != statsSummaryAPIEquivalentCostSource || estimated.Formula != statsSummaryAPIEquivalentCostFormula || estimated.Aggregation != statsSummaryAPIEquivalentCostAggregation {
		t.Errorf("estimated identity and formula = %#v", estimated)
	}
	if estimated.Pricing.State != "retained" || estimated.Pricing.Provider == nil || *estimated.Pricing.Provider != provider || estimated.Pricing.Model == nil || *estimated.Pricing.Model != model || estimated.Pricing.Path == nil || estimated.Pricing.RecordSHA256 == nil || *estimated.Pricing.RecordSHA256 != "f812b2301791fa15f25cb4b4b573fc9bee00cf02e7cbe4665d3942a3dba94cd0" || estimated.Pricing.SourceURL == nil || *estimated.Pricing.SourceURL != "https://models.dev/api.json" || estimated.Pricing.RetrievedAt == nil || *estimated.Pricing.RetrievedAt != "2026-08-09T12:00:00Z" {
		t.Errorf("pricing provenance = %#v", estimated.Pricing)
	}
	for name, measurement := range map[string]statsSummaryIntegerMeasurement{
		"uncached_input": estimated.Components.UncachedInput,
		"cache_read":     estimated.Components.CacheRead,
		"cache_write":    estimated.Components.CacheWrite,
		"output":         estimated.Components.Output,
		"reasoning":      estimated.Components.Reasoning,
	} {
		if measurement.State != statsSummaryMeasured || measurement.Value == nil {
			t.Errorf("%s component = %#v", name, measurement)
		}
	}
	if len(estimated.RateSelections) != 1 || estimated.RateSelections[0].ContributingModelSteps != 1 || estimated.RateSelections[0].MicroUSD != "309" || estimated.RateSelections[0].ReasoningRatePolicy != "selected_reasoning_rate" {
		t.Errorf("rate selections = %#v", estimated.RateSelections)
	}
	if len(estimated.IdentityGroups) != 1 || estimated.IdentityGroups[0].State != statsSummaryMeasured || estimated.IdentityGroups[0].MicroUSD == nil || *estimated.IdentityGroups[0].MicroUSD != "309" {
		t.Errorf("identity groups = %#v", estimated.IdentityGroups)
	}

	data, err := json.Marshal(estimated)
	if err != nil {
		t.Fatal(err)
	}
	var rendered map[string]any
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatal(err)
	}
	groups := rendered["identity_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("rendered identity groups = %v, want one", groups)
	}
	assertStatsJSONKeys(t, groups[0].(map[string]any),
		"state", "value", "micro_usd", "unit", "formula", "source", "aggregation", "components", "pricing", "rate_selections", "temporal_scope", "reasons", "provider", "model")
}

func TestStatsSummaryCostViewsEstimateSurvivesReportedCostUnavailable(t *testing.T) {
	provider, model := "openai", "gpt-5"
	rows := []dbpkg.StatsSummaryCacheRow{
		statsCostRow("one", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), &provider, &model,
			dbpkg.CostMeasurement{State: dbpkg.MeasurementUnknown}, statsCostToken(1), statsCostToken(1), statsCostToken(0), statsCostToken(0), statsCostToken(0)),
	}
	estimated := aggregateStatsSummaryAPIEquivalentEstimatedAPICost(rows, 0, statsCostPrices(t, statsCostAllRates), nil)
	reported := aggregateStatsSummaryRuntimeReportedCost(rows)
	if reported.State != statsSummaryMissing || reported.Value != nil || !reflect.DeepEqual(reported.Reasons, []string{statsSummaryRuntimeReportedCostMissing}) {
		t.Fatalf("reported cost = %#v, want missing", reported)
	}
	if estimated.State != statsSummaryMeasured || estimated.Value == nil || estimated.MicroUSD == nil || *estimated.MicroUSD != "6" {
		t.Fatalf("estimated cost = %#v, want measured", estimated)
	}
}

func TestStatsSummaryCostViewsEstimateFailuresKeepReportedCost(t *testing.T) {
	provider, model := "openai", "gpt-5"
	base := func() dbpkg.StatsSummaryCacheRow {
		return statsCostRow("one", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), &provider, &model,
			statsCostMeasurement(0.25), statsCostToken(10), statsCostToken(2), statsCostToken(1), statsCostToken(1), statsCostToken(0))
	}
	missingCacheRate := strings.Replace(statsCostAllRates, `"cache_read": 0.5,`, "", 1)
	for _, test := range []struct {
		name        string
		rows        func() []dbpkg.StatsSummaryCacheRow
		prices      string
		unsupported int
		wantReason  string
	}{
		{
			name: "missing runtime identity",
			rows: func() []dbpkg.StatsSummaryCacheRow {
				row := base()
				row.Provider, row.Model = nil, nil
				return []dbpkg.StatsSummaryCacheRow{row}
			},
			prices: statsCostAllRates, wantReason: statsSummaryRuntimeIdentityMissing,
		},
		{
			name: "unknown provider model pricing",
			rows: func() []dbpkg.StatsSummaryCacheRow {
				unknownProvider, unknownModel := "unknown", "model"
				row := base()
				row.Provider, row.Model = &unknownProvider, &unknownModel
				return []dbpkg.StatsSummaryCacheRow{row}
			},
			prices: statsCostAllRates, wantReason: statsSummaryPricingSnapshotUnavailable,
		},
		{
			name: "missing cache rate",
			rows: func() []dbpkg.StatsSummaryCacheRow {
				return []dbpkg.StatsSummaryCacheRow{base()}
			},
			prices: missingCacheRate, wantReason: string(dbpkg.APIEquivalentCostCacheReadPriceUnavailable),
		},
		{
			name: "missing required token",
			rows: func() []dbpkg.StatsSummaryCacheRow {
				row := base()
				row.Output = dbpkg.IntegerMeasurement{State: dbpkg.MeasurementUnknown}
				return []dbpkg.StatsSummaryCacheRow{row}
			},
			prices: statsCostAllRates, wantReason: string(dbpkg.APIEquivalentCostOutputUnavailable),
		},
		{
			name: "malformed required token",
			rows: func() []dbpkg.StatsSummaryCacheRow {
				row := base()
				row.Output = dbpkg.IntegerMeasurement{State: dbpkg.MeasurementMalformed}
				return []dbpkg.StatsSummaryCacheRow{row}
			},
			prices: statsCostAllRates, wantReason: "output_malformed",
		},
		{
			name: "negative required token",
			rows: func() []dbpkg.StatsSummaryCacheRow {
				row := base()
				row.Output = statsCostToken(-1)
				return []dbpkg.StatsSummaryCacheRow{row}
			},
			prices: statsCostAllRates, wantReason: string(dbpkg.APIEquivalentCostOutputInvalid),
		},
		{
			name: "context token overflow",
			rows: func() []dbpkg.StatsSummaryCacheRow {
				row := base()
				row.UncachedInput, row.CacheRead = statsCostToken(int64(1<<63-1)), statsCostToken(1)
				return []dbpkg.StatsSummaryCacheRow{row}
			},
			prices: statsCostAllRates, wantReason: string(dbpkg.APIEquivalentCostContextTokenOverflow),
		},
		{
			name: "unsupported runtime",
			rows: func() []dbpkg.StatsSummaryCacheRow {
				row := base()
				row.Runtime, row.RuntimeVersion = "other", "0"
				return []dbpkg.StatsSummaryCacheRow{row}
			},
			prices: statsCostAllRates, unsupported: 1, wantReason: statsSummaryUnsupportedRuntimeOrVersion,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := test.rows()
			reported := aggregateStatsSummaryRuntimeReportedCost(rows)
			if reported.State != statsSummaryMeasured || reported.Value == nil || *reported.Value != 0.25 {
				t.Fatalf("reported cost = %#v, want measured 0.25", reported)
			}
			supported := rows
			if test.unsupported > 0 {
				supported = nil
			}
			estimated := aggregateStatsSummaryAPIEquivalentEstimatedAPICost(supported, test.unsupported, statsCostPrices(t, test.prices), nil)
			if estimated.State != statsSummaryUnavailable || estimated.Value != nil || !containsStatsSummaryString(estimated.Reasons, test.wantReason) {
				t.Fatalf("estimated cost = %#v, want unavailable reason %q", estimated, test.wantReason)
			}
		})
	}
}

func TestStatsSummaryCostViewsKeepMixedIdentitiesInSeparateEstimateGroups(t *testing.T) {
	providerA, modelA := "anthropic", "claude"
	providerB, modelB := "openai", "gpt-5"
	rows := []dbpkg.StatsSummaryCacheRow{
		statsCostRow("b", time.Date(2026, 8, 9, 12, 1, 0, 0, time.UTC), &providerB, &modelB,
			statsCostMeasurement(0.1), statsCostToken(10), statsCostToken(1), statsCostToken(0), statsCostToken(0), statsCostToken(0)),
		statsCostRow("a", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), &providerA, &modelA,
			statsCostMeasurement(0.2), statsCostToken(10), statsCostToken(1), statsCostToken(0), statsCostToken(0), statsCostToken(0)),
	}
	prices := statsCostPrices(t, `{
  "anthropic": {"claude": {"input": 1, "output": 1, "cache_read": 0, "cache_write": 0}},
  "openai": {"gpt-5": {"input": 2, "output": 2, "cache_read": 0, "cache_write": 0}}
}`)
	estimated := aggregateStatsSummaryAPIEquivalentEstimatedAPICost(rows, 0, prices, nil)
	if estimated.State != statsSummaryUnavailable || !reflect.DeepEqual(estimated.Reasons, []string{statsSummaryRuntimeIdentityMixed}) || estimated.Value != nil {
		t.Fatalf("mixed estimate = %#v", estimated)
	}
	if len(estimated.IdentityGroups) != 2 || estimated.IdentityGroups[0].Provider != providerA || estimated.IdentityGroups[0].Model != modelA || estimated.IdentityGroups[1].Provider != providerB || estimated.IdentityGroups[1].Model != modelB {
		t.Fatalf("identity groups = %#v", estimated.IdentityGroups)
	}
	for _, group := range estimated.IdentityGroups {
		if group.State != statsSummaryMeasured || group.Value == nil || group.MicroUSD == nil || group.Pricing.State != "retained" {
			t.Errorf("group = %#v, want an independent measured estimate", group)
		}
	}
	reported := aggregateStatsSummaryRuntimeReportedCost(rows)
	if reported.State != statsSummaryMeasured || reported.Value == nil || math.Abs(*reported.Value-0.3) > 1e-15 {
		t.Errorf("reported cost = %#v, want independent measured total", reported)
	}
}

func TestStatsSummaryCostViewsReasonAndProvenanceOrderIsDeterministic(t *testing.T) {
	provider, model := "openai", "gpt-5"
	first := statsCostRow("first", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), &provider, &model,
		statsCostMeasurement(0), statsCostToken(1), dbpkg.IntegerMeasurement{State: dbpkg.MeasurementUnknown}, statsCostToken(0), statsCostToken(1), statsCostToken(0))
	second := statsCostRow("second", time.Date(2026, 8, 9, 12, 1, 0, 0, time.UTC), &provider, &model,
		statsCostMeasurement(0), statsCostToken(1), statsCostToken(1), statsCostToken(0), statsCostToken(1), statsCostToken(0))
	prices := statsCostPrices(t, strings.Replace(statsCostAllRates, `"cache_read": 0.5,`, "", 1))
	forward := aggregateStatsSummaryAPIEquivalentEstimatedAPICost([]dbpkg.StatsSummaryCacheRow{first, second}, 0, prices, nil)
	reverse := aggregateStatsSummaryAPIEquivalentEstimatedAPICost([]dbpkg.StatsSummaryCacheRow{second, first}, 0, prices, nil)
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("estimate differs by source order:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
	wantReasons := []string{string(dbpkg.APIEquivalentCostOutputUnavailable), string(dbpkg.APIEquivalentCostCacheReadPriceUnavailable)}
	if !reflect.DeepEqual(forward.Reasons, wantReasons) {
		t.Errorf("ordered reasons = %v, want %v", forward.Reasons, wantReasons)
	}
}

func TestStatsSummaryCostViewsPreserveRetainedSchemaSixEvidence(t *testing.T) {
	root := repositoryRoot(t)
	for relative, want := range map[string]string{
		"tools/bench/evidence/skillsbench-oauth-campaign-archive.json":     "b817943fd2449a607d373205306458403e627a0970946274115f369a89eacb1e",
		"tools/bench/evidence/skillsbench-oauth-campaign-artifacts.sha256": "55b196c49d7d640233db43879c7396093718708a62fb2f7452435aefdf022ec3",
	} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s hash = %s, want %s", relative, got, want)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "tools", "bench", "evidence", "skillsbench-oauth-campaign-archive.json"))
	if err != nil {
		t.Fatal(err)
	}
	var archive struct {
		Campaigns []struct {
			SchemaVersion int `json:"schema_version"`
			Freeze        struct {
				ValidationLimitations []string `json:"validation_limitations"`
			} `json:"freeze"`
		} `json:"campaigns"`
	}
	if err := json.Unmarshal(data, &archive); err != nil {
		t.Fatal(err)
	}
	schemaSix := 0
	for _, campaign := range archive.Campaigns {
		if campaign.SchemaVersion != 6 {
			continue
		}
		schemaSix++
		if !containsStatsSummaryString(campaign.Freeze.ValidationLimitations, "Current schema 7 validation rejects this schema 6 campaign; the historical validator result is not promoted to current evidence.") {
			t.Errorf("schema-6 limitations = %v", campaign.Freeze.ValidationLimitations)
		}
	}
	if schemaSix != 2 {
		t.Errorf("schema-6 campaign count = %d, want 2", schemaSix)
	}
}

func statsCostPrices(t *testing.T, contents string) *dbpkg.Prices {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	prices := dbpkg.NewPrices(path)
	if err := prices.Load(); err != nil {
		t.Fatal(err)
	}
	return prices
}

func statsCostRow(sessionID string, capturedAt time.Time, provider, model *string, cost dbpkg.CostMeasurement, input, output, reasoning, read, write dbpkg.IntegerMeasurement) dbpkg.StatsSummaryCacheRow {
	return dbpkg.StatsSummaryCacheRow{
		SessionID: sessionID, CapturedAt: capturedAt,
		Runtime: reuse.RequiredRuntime, RuntimeVersion: reuse.RequiredRuntimeVersion,
		Provider: provider, Model: model, Cost: cost,
		UncachedInput: input, Output: output, Reasoning: reasoning, CacheRead: read, CacheWrite: write,
	}
}

func statsCostToken(value int64) dbpkg.IntegerMeasurement {
	return dbpkg.IntegerMeasurement{State: dbpkg.MeasurementMeasured, Value: &value}
}

func statsCostMeasurement(value float64) dbpkg.CostMeasurement {
	return dbpkg.CostMeasurement{State: dbpkg.MeasurementMeasured, Value: &value}
}
