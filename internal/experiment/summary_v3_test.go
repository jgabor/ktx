package experiment

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/db"
)

func TestQuantileLinearInterpolation(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		q      float64
		want   *float64
	}{
		{name: "empty", q: 0.5, want: nil},
		{name: "singleton", values: []float64{7}, q: 0.25, want: floatPointer(7)},
		{name: "odd median", values: []float64{1, 3, 9}, q: 0.5, want: floatPointer(3)},
		{name: "even median", values: []float64{1, 3, 5, 9}, q: 0.5, want: floatPointer(4)},
		{name: "interpolated quartile", values: []float64{0, 10, 20, 30}, q: 0.25, want: floatPointer(7.5)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := quantile(test.values, test.q)
			if test.want == nil {
				if got != nil {
					t.Fatalf("quantile = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *test.want {
				t.Fatalf("quantile = %v, want %v", got, *test.want)
			}
		})
	}
}

func TestPopulationStandardDeviation(t *testing.T) {
	if got := populationStandardDeviation(nil); got != nil {
		t.Fatalf("empty standard deviation = %v, want nil", *got)
	}
	if got := populationStandardDeviation([]float64{4}); got == nil || *got != 0 {
		t.Fatalf("singleton standard deviation = %v, want 0", got)
	}
	got := populationStandardDeviation([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	if got == nil || *got != 2 {
		t.Fatalf("population standard deviation = %v, want 2", got)
	}
}

func TestDescriptiveDistributionEmptyAndSingleton(t *testing.T) {
	empty := descriptiveDistribution(nil)
	if empty["count"] != 0 || empty["mean"] != nil || empty["population_standard_deviation"] != nil {
		t.Fatalf("empty distribution = %#v", empty)
	}
	singleton := descriptiveDistribution([]metricObservation{{AttemptID: "only", Cycle: 1, Value: 9}})
	for _, field := range []string{"min", "p25", "median", "mean", "p75", "max"} {
		if singleton[field] != float64(9) {
			t.Fatalf("singleton %s = %v, want 9", field, singleton[field])
		}
	}
	if singleton["population_standard_deviation"] != float64(0) {
		t.Fatalf("singleton standard deviation = %v, want 0", singleton["population_standard_deviation"])
	}
}

func TestDescriptiveDistributionTukeyOutliers(t *testing.T) {
	observations := []metricObservation{
		{AttemptID: "luna-1", Cycle: 1, Value: 3},
		{AttemptID: "luna-2", Cycle: 2, Value: 3},
		{AttemptID: "luna-3", Cycle: 3, Value: 3},
		{AttemptID: "luna-4", Cycle: 4, Value: 52},
	}
	dist := descriptiveDistribution(observations)
	if dist["count"] != 4 || dist["mean"] != 15.25 {
		t.Fatalf("distribution count/mean = %#v", dist)
	}
	detection := dist["outlier_detection"].(map[string]any)
	if detection["status"] != "complete" {
		t.Fatalf("outlier status = %v", detection["status"])
	}
	points := detection["points"].([]any)
	if len(points) != 1 {
		t.Fatalf("outlier points = %#v, want one", points)
	}
	point := points[0].(map[string]any)
	if point["attempt_id"] != "luna-4" || point["cycle"] != 4 || point["value"] != float64(52) {
		t.Fatalf("outlier point = %#v", point)
	}

	insufficient := descriptiveDistribution(observations[:3])["outlier_detection"].(map[string]any)
	if insufficient["status"] != "insufficient_sample" || len(insufficient["points"].([]any)) != 0 {
		t.Fatalf("small-sample outliers = %#v", insufficient)
	}
}

func v3Manifest() Manifest {
	manifest := cacheFixtureManifest()
	manifest.ExperimentID = "exp-v3"
	manifest.Model = "openai/gpt-5.6-luna"
	manifest.TargetValidPerPipeline = 4
	manifest.Pricing = &PricingSnapshotMetadata{Path: "pricing.json", SHA256: "pricing-sha", SourceURL: "https://models.dev/api.json", Provider: "openai", Model: "gpt-5.6-luna"}
	return manifest
}

func evaluatedAttempt(id, side string, cycle int, tokensIn, cacheRead, cacheWrite, tokensOut, reasoning, cost, estimated, tools, score float64) Attempt {
	return Attempt{
		AttemptID: id, Side: side, Cycle: cycle, Status: "completed", SessionID: stringPointer("session-" + id),
		Metrics: Metrics{
			StreamComplete: true, Score: Score{Score: score, FullPass: score == 1, AnswerMatchesExpected: score == 1},
			ToolCallCount: int(tools), TokensIn: floatPointer(tokensIn), CacheRead: floatPointer(cacheRead),
			CacheWrite: floatPointer(cacheWrite), TokensOut: floatPointer(tokensOut), Reasoning: floatPointer(reasoning),
			Cost: floatPointer(cost), EstimatedAPICost: floatPointer(estimated),
		},
	}
}

func v3Attempts() []Attempt {
	// Deliberately reversed by cycle and side to prove pairing never depends on slice order.
	return []Attempt{
		evaluatedAttempt("candidate-2", Candidate, 2, 70, 20, 2, 8, 3, .7, .07, 3, .8),
		evaluatedAttempt("baseline-1", Baseline, 1, 100, 30, 3, 10, 4, 1, .1, 3, 1),
		evaluatedAttempt("candidate-1", Candidate, 1, 60, 10, 1, 7, 2, .6, .06, 3, 1),
		evaluatedAttempt("baseline-2", Baseline, 2, 110, 40, 4, 11, 5, 1.1, .11, 3, 1),
		evaluatedAttempt("candidate-4", Candidate, 4, 90, 40, 4, 10, 5, .9, .09, 52, 1),
		evaluatedAttempt("baseline-4", Baseline, 4, 130, 60, 6, 13, 7, 1.3, .13, 3, 1),
		evaluatedAttempt("baseline-3", Baseline, 3, 120, 50, 5, 12, 6, 1.2, .12, 3, 1),
		evaluatedAttempt("candidate-3", Candidate, 3, 80, 30, 3, 9, 4, .8, .08, 3, 1),
		evaluatedAttempt("baseline-5", Baseline, 5, 140, 70, 7, 14, 8, 1.4, .14, 3, 1),
		evaluatedAttempt("candidate-6", Candidate, 6, 95, 45, 5, 11, 6, .95, .095, 3, 1),
	}
}

func TestSummaryV3PairedCyclesOutliersAndClassifications(t *testing.T) {
	summary := BuildSummary(v3Manifest(), v3Attempts())
	if summary["schema_version"] != 3 {
		t.Fatalf("schema_version = %v", summary["schema_version"])
	}
	pipelines := summary["pipelines"].(map[string]any)
	baseline := pipelines[Baseline].(map[string]any)
	candidate := pipelines[Candidate].(map[string]any)
	if baseline["sample_classification"] != "descriptive" || candidate["sample_classification"] != "descriptive" {
		t.Fatalf("sample classifications = %v / %v", baseline["sample_classification"], candidate["sample_classification"])
	}
	tools := candidate["tool_call_count"].(map[string]any)
	if tools["mean"] != 12.8 { // The 52-call run remains in the aggregate.
		t.Fatalf("candidate tool-call mean = %v, want 12.8", tools["mean"])
	}
	points := tools["outlier_detection"].(map[string]any)["points"].([]any)
	if len(points) != 1 || points[0].(map[string]any)["attempt_id"] != "candidate-4" {
		t.Fatalf("tool-call outliers = %#v", points)
	}
	for _, raw := range summary["exclusions"].([]any) {
		if raw.(map[string]any)["attempt_id"] == "candidate-4" {
			t.Fatalf("outlier was excluded: %#v", raw)
		}
	}

	paired := summary["paired_baseline_minus_candidate"].(map[string]any)
	if paired["paired_count"] != 4 || paired["sample_classification"] != "exploratory" {
		t.Fatalf("paired metadata = %#v", paired)
	}
	cycles := paired["cycles"].([]any)
	if len(cycles) != 4 || cycles[0].(map[string]any)["cycle"] != 1 || cycles[3].(map[string]any)["cycle"] != 4 {
		t.Fatalf("paired cycles = %#v", cycles)
	}
	tokenStats := paired["metrics"].(map[string]any)["tokens_in"].(map[string]any)
	if tokenStats["mean_paired_delta"] != float64(40) || tokenStats["median_paired_delta"] != float64(40) {
		t.Fatalf("paired token deltas = %#v", tokenStats)
	}
	unpaired := paired["unpaired_attempts"].([]any)
	if len(unpaired) != 2 || unpaired[0].(map[string]any)["attempt_id"] != "baseline-5" || unpaired[1].(map[string]any)["attempt_id"] != "candidate-6" {
		t.Fatalf("unpaired attempts = %#v", unpaired)
	}
}

func TestSummaryV3GoldenAndLegacyBytes(t *testing.T) {
	actual, err := MarshalJSON(BuildSummary(v3Manifest(), v3Attempts()))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(filepath.Join("testdata", "summary_v3.golden"), actual, 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		assertGolden(t, "summary_v3.golden", actual)
	}

	legacyV1, err := MarshalJSON(BuildSummary(fixtureManifest(), fixtureAttempts()))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "summary.golden.json", legacyV1)
	legacyV2, err := MarshalJSON(BuildSummary(cacheFixtureManifest(), cacheFixtureAttempts()))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "summary_cache.golden", legacyV2)
}

func TestRankingSurfacesV3PairedEvidenceOnly(t *testing.T) {
	options := RankingOptions{TaskFile: "/repo/task.toml", Model: "openai/gpt-5.6-luna", Rounds: 5, BaselinePipeline: "passthrough", Candidates: []string{"candidate"}}
	pair := PairResult{CandidatePipeline: "candidate", ExperimentID: "exp-v3", Summary: BuildSummary(v3Manifest(), v3Attempts())}
	ranking := BuildRanking(options, []PairResult{pair}, time.Unix(0, 0))
	rows := ranking["ranking"].([]map[string]any)
	for _, row := range rows {
		if row["sample_classification"] != "descriptive" {
			t.Fatalf("v3 row sample classification = %#v", row)
		}
	}
	var candidate map[string]any
	for _, row := range rows {
		if row["side"] == Candidate {
			candidate = row
		}
	}
	pairedTokens := summaryValue(candidate["paired_mean_tokens_saved_vs_baseline"])
	if pairedTokens == nil || *pairedTokens != 40 || candidate["paired_sample_classification"] != "exploratory" {
		t.Fatalf("v3 ranking paired evidence = %#v", candidate)
	}
	pairedCost := summaryValue(candidate["paired_mean_estimated_api_cost_saved_vs_baseline"])
	if pairedCost == nil || math.Abs(*pairedCost-.04) > 1e-12 {
		t.Fatalf("paired estimated cost = %#v", candidate)
	}
	if text := RenderRanking(ranking); !strings.Contains(text, "paired pipeline=candidate") || !strings.Contains(text, "estimated_cost=0.04") {
		t.Fatalf("v3 ranking text does not surface paired evidence:\n%s", text)
	}
	if text := RenderSummary(pair.Summary); !strings.Contains(text, "outlier candidate.tool_call_count attempt=candidate-4 cycle=4 value=52") {
		t.Fatalf("v3 summary text does not surface outlier:\n%s", text)
	}

	legacy := BuildRanking(options, []PairResult{{CandidatePipeline: "candidate", Summary: BuildSummary(fixtureManifest(), fixtureAttempts())}}, time.Unix(0, 0))
	for _, row := range legacy["ranking"].([]map[string]any) {
		if _, ok := row["sample_classification"]; ok {
			t.Fatalf("legacy ranking gained v3 field: %#v", row)
		}
	}
}

func TestSummaryV3ValidationRegeneration(t *testing.T) {
	dir := t.TempDir()
	rate := .5
	pricing, err := writePricingSnapshot(dir, "openai", "gpt-5.6-luna", db.Price{
		Input: 1, Output: 2, CacheRead: &rate, CacheWrite: &rate, InputSet: true, OutputSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := v3Manifest()
	manifest.Pricing = pricing
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range v3Attempts() {
		if err := AppendAttempt(dir, attempt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}
	report, err := ValidateExperiment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report["valid"] != true || report["summary_byte_match"] != true {
		t.Fatalf("validation report = %#v", report)
	}
}

func TestPricingSnapshotReasoningRoundTrip(t *testing.T) {
	dir := t.TempDir()
	reasoning, tierReasoning, zero := 3.0, 7.0, 0.0
	metadata, err := writePricingSnapshot(dir, "openai", "gpt", db.Price{
		Input: 2, Output: 10, Reasoning: &reasoning, CacheRead: &zero, CacheWrite: &zero, InputSet: true, OutputSet: true,
		Tiers: []db.PriceTier{{Tier: db.PriceTierSelector{Type: "context", Size: 272000}, Input: 4, Output: 20, Reasoning: &tierReasoning, CacheRead: &zero, CacheWrite: &zero, InputSet: true, OutputSet: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loadPricingSnapshot(dir, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Price.Reasoning == nil || *snapshot.Price.Reasoning != 3 || snapshot.Price.Tiers[0].Reasoning == nil || *snapshot.Price.Tiers[0].Reasoning != 7 || snapshot.Price.CacheRead == nil || *snapshot.Price.CacheRead != 0 {
		t.Fatalf("snapshot price = %#v", snapshot.Price)
	}
}

func TestPricingSnapshotWithoutEstimateDoesNotEnableV3(t *testing.T) {
	manifest := cacheFixtureManifest()
	manifest.Pricing = &PricingSnapshotMetadata{Path: "pricing.json", SHA256: "sha", SourceURL: db.ModelsDevURL, Provider: "openai", Model: "gpt"}
	summary := BuildSummary(manifest, cacheFixtureAttempts())
	if summary["schema_version"] != 2 {
		t.Fatalf("schema_version = %v, want 2 without estimated observations", summary["schema_version"])
	}
	baseline := summary["pipelines"].(map[string]any)[Baseline].(map[string]any)
	if _, ok := baseline["estimated_api_cost"]; ok {
		t.Fatalf("unexpected estimated surface: %#v", baseline)
	}
	if _, ok := summary["estimated_api_cost_basis"]; ok {
		t.Fatalf("unexpected estimated basis: %#v", summary)
	}

	manifest.Methodology.Version = "v5-cache-aware"
	if got := BuildSummary(manifest, cacheFixtureAttempts())["schema_version"]; got != 3 {
		t.Fatalf("v5 schema_version = %v, want 3", got)
	}
}
