package experiment

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/db"
)

func pipelineAcceptanceManifest() Manifest {
	manifest := promptCacheTokenHitRateManifest()
	manifest.ExperimentID = "pipeline-acceptance"
	manifest.Pricing = &PricingSnapshotMetadata{
		Path: "pricing.json", SHA256: "pricing-sha", SourceURL: "https://models.dev/api.json",
		Provider: "openai", Model: "test",
	}
	return manifest
}

func pipelineAcceptanceAttempts(t *testing.T, baseline, candidate []int) []Attempt {
	t.Helper()
	if len(baseline) != 5 || len(candidate) != 5 {
		t.Fatal("acceptance fixtures require five complete pairs")
	}
	attempts := comparisonPairs(5, 20, 30)
	for index := range attempts {
		attempts[index].ExperimentID = "pipeline-acceptance"
		costTokens := baseline[index/2]
		if attempts[index].Side == Candidate {
			costTokens = candidate[index/2]
		}
		attempts[index].TokensIn = floatPointer(float64(costTokens))
		attempts[index].TokensOut = floatPointer(0)
		attempts[index].Reasoning = floatPointer(0)
		attempts[index].CacheRead = floatPointer(0)
		attempts[index].CacheWrite = floatPointer(0)
		attempts[index].Cost = floatPointer(0)
	}
	zero := 0.0
	return estimateAttemptsWithDetails(attempts, &pricingSnapshot{
		Provider: "openai", Model: "test",
		Price: db.Price{Input: 1, Output: 0, CacheRead: &zero, CacheWrite: &zero, InputSet: true, OutputSet: true, SourceURL: "https://models.dev/api.json"},
	})
}

func pipelineAcceptanceSummary(t *testing.T, manifest Manifest, attempts []Attempt) map[string]any {
	t.Helper()
	summary := BuildSummary(manifest, attempts)
	if summary["schema_version"] != 5 {
		t.Fatalf("schema_version = %#v, want 5", summary["schema_version"])
	}
	return summary
}

func pipelineAcceptanceGuard(t *testing.T, manifest Manifest, attempts []Attempt) map[string]any {
	t.Helper()
	guard := anyMap(pipelineAcceptanceSummary(t, manifest, attempts)["pipeline_acceptance"])
	if len(guard) == 0 {
		t.Fatal("pipeline acceptance is missing")
	}
	return guard
}

func guardReasons(guard map[string]any) []string {
	reasons, _ := strictStringSlice(guard["reasons"])
	return reasons
}

func TestPipelineAcceptanceAcceptsStrictRoundedEstimatedCostImprovement(t *testing.T) {
	manifest := pipelineAcceptanceManifest()
	summary := pipelineAcceptanceSummary(t, manifest, pipelineAcceptanceAttempts(t, []int{20, 20, 20, 20, 20}, []int{10, 10, 10, 10, 10}))
	guard := anyMap(summary["pipeline_acceptance"])
	if guard["accepted"] != true || summary["accepted"] != true || len(guardReasons(guard)) != 0 {
		t.Fatalf("accepted guard = %#v", guard)
	}
	baseline := anyMap(guard[Baseline])
	candidate := anyMap(guard[Candidate])
	if baseline["correct_count"] != 5 || candidate["correct_count"] != 5 ||
		anyMap(baseline["estimated_total"])["micro_usd"] != "100" || anyMap(candidate["estimated_total"])["micro_usd"] != "50" ||
		anyMap(baseline["estimated_cost_per_correct"])["micro_usd"] != "20" || anyMap(candidate["estimated_cost_per_correct"])["micro_usd"] != "10" {
		t.Fatalf("cost-per-correct evidence = %#v", guard)
	}
	rounding := anyMap(guard["rounding"])
	if rounding["scale"] != db.APIEquivalentCostRoundingDecimals || rounding["mode"] != db.APIEquivalentCostRoundingMode {
		t.Fatalf("rounding = %#v", rounding)
	}
	pricing := anyMap(guard["pricing_provenance"])
	if pricing["state"] != "retained" || pricing["provider"] != "openai" || pricing["model"] != "test" || anyMap(pricing["snapshot"])["sha256"] != "pricing-sha" {
		t.Fatalf("pricing provenance = %#v", pricing)
	}
	if !reflect.DeepEqual(guard["quality"], map[string]any{
		"basis": "same_complete_measured_warm_pairs", "preserved": true, "fact_score_not_lower": true, "full_pass_rate_not_lower": true,
		"baseline_fact_score": 1.0, "candidate_fact_score": 1.0, "baseline_full_pass_rate": 1.0, "candidate_full_pass_rate": 1.0,
	}) {
		t.Fatalf("quality = %#v", guard["quality"])
	}

	for _, side := range []string{Baseline, Candidate} {
		cost := anyMap(anyMap(summary["pipelines"])[side])
		values := cost["cost"].(map[string]any)["values"].([]float64)
		for _, value := range values {
			if value != 0 {
				t.Fatalf("reported cost changed for %s: %#v", side, values)
			}
		}
	}
}

func TestPipelineAcceptanceFailsClosedWithOrderedReasons(t *testing.T) {
	base := []int{20, 20, 20, 20, 20}
	tests := []struct {
		name     string
		mutate   func(*Manifest, *[]Attempt)
		contains []string
	}{
		{
			name:     "raw tie",
			contains: []string{"estimated_cost_per_correct_raw_tie"},
		},
		{
			name: "rounded tie",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				*attempts = pipelineAcceptanceAttempts(t, base, []int{19, 20, 20, 20, 20})
			},
			contains: []string{"estimated_cost_per_correct_rounded_tie"},
		},
		{
			name: "missing cost",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				(*attempts)[1].TokensOut = nil
				*attempts = estimateAttemptsWithDetails(*attempts, &pricingSnapshot{Provider: "openai", Model: "test", Price: db.Price{Input: 1, Output: 0, InputSet: true, OutputSet: true}})
			},
			contains: []string{"estimated_cost_unavailable"},
		},
		{
			name: "nonfinite cost",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				(*attempts)[1].EstimatedAPICost = floatPointer(math.Inf(1))
			},
			contains: []string{"estimated_cost_nonfinite"},
		},
		{
			name: "zero correct answers",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				for index := range *attempts {
					(*attempts)[index].FullPass = false
				}
			},
			contains: []string{"zero_correct_answers"},
		},
		{
			name: "incomplete pair",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				*attempts = (*attempts)[:len(*attempts)-1]
			},
			contains: []string{"incomplete_pairs"},
		},
		{
			name: "identity mismatch",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				provider := "anthropic"
				(*attempts)[1].ModelSteps[0].Identity.Provider = &provider
			},
			contains: []string{"incomplete_pairs", "identity_unstable"},
		},
		{
			name: "protocol mismatch",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				(*attempts)[1].CacheControl.ExecutionOrder = (*attempts)[0].CacheControl.ExecutionOrder
			},
			contains: []string{"incomplete_pairs", "protocol_noncompliant"},
		},
		{
			name: "worse fact score",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				for index := range *attempts {
					if (*attempts)[index].Side == Candidate {
						(*attempts)[index].Score.Score = .5
					}
				}
			},
			contains: []string{"fact_score_regressed"},
		},
		{
			name: "worse full pass rate",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				(*attempts)[1].FullPass = false
			},
			contains: []string{"full_pass_rate_regressed"},
		},
		{
			name: "higher cost",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				*attempts = pipelineAcceptanceAttempts(t, base, []int{30, 30, 30, 30, 30})
			},
			contains: []string{"estimated_cost_per_correct_not_lower"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := pipelineAcceptanceManifest()
			attempts := pipelineAcceptanceAttempts(t, base, base)
			if test.mutate != nil {
				test.mutate(&manifest, &attempts)
			}
			summary := pipelineAcceptanceSummary(t, manifest, attempts)
			guard := anyMap(summary["pipeline_acceptance"])
			if guard["accepted"] != false {
				t.Fatalf("guard accepted rejected evidence: %#v", guard)
			}
			if _, err := MarshalJSON(summary); err != nil {
				t.Fatalf("guard emitted a non-finite summary: %v", err)
			}
			reasons := guardReasons(guard)
			for _, expected := range test.contains {
				if !containsString(reasons, expected) {
					t.Fatalf("reasons %v do not contain %q", reasons, expected)
				}
			}
			if !reflect.DeepEqual(reasons, orderPipelineAcceptanceReasons(reasons)) {
				t.Fatalf("reasons are not ordered: %v", reasons)
			}
		})
	}
}

func TestPipelineAcceptancePreservesReportedZeroAndExplainsPricingUnavailability(t *testing.T) {
	manifest := pipelineAcceptanceManifest()
	attempts := pipelineAcceptanceAttempts(t, []int{20, 20, 20, 20, 20}, []int{10, 10, 10, 10, 10})
	for index := range attempts {
		if attempts[index].Cost == nil || *attempts[index].Cost != 0 {
			t.Fatalf("reported zero was not retained before guard: %#v", attempts[index].Cost)
		}
	}
	manifest.Pricing = nil
	attempts = estimateAttemptsWithDetails(attempts, nil)
	guard := pipelineAcceptanceGuard(t, manifest, attempts)
	if guard["accepted"] != false || !containsString(guardReasons(guard), "estimated_cost_unavailable") || !containsString(guardReasons(guard), "pricing_provenance_unavailable") {
		t.Fatalf("unavailable guard = %#v", guard)
	}
	for _, side := range Sides {
		availability := anyMap(anyMap(guard[side])["estimated_total"])
		if availability["state"] != "unavailable" || !containsString(stringSlice(availability["reasons"]), "pricing_snapshot_unavailable") {
			t.Fatalf("%s availability = %#v", side, availability)
		}
	}
}

func TestPipelineAcceptanceRankingRetainsRejectedCandidateWithoutRecommendation(t *testing.T) {
	manifest := pipelineAcceptanceManifest()
	accepted := pipelineAcceptanceSummary(t, manifest, pipelineAcceptanceAttempts(t, []int{20, 20, 20, 20, 20}, []int{10, 10, 10, 10, 10}))
	rejected := pipelineAcceptanceSummary(t, manifest, pipelineAcceptanceAttempts(t, []int{20, 20, 20, 20, 20}, []int{20, 20, 20, 20, 20}))
	ranking := BuildRanking(RankingOptions{TaskFile: "tools/bench/tasks/crossfile-nav-v1.toml", Model: "openai/test", Rounds: 5, BaselinePipeline: "plain", Candidates: []string{"tie", "winner"}}, []PairResult{
		{CandidatePipeline: "tie", ExperimentID: "tie", ExitCode: 0, Summary: rejected},
		{CandidatePipeline: "winner", ExperimentID: "winner", ExitCode: 0, Summary: accepted},
	}, time.Unix(0, 0))
	var rejectedRow map[string]any
	for _, row := range ranking["ranking"].([]map[string]any) {
		if row["pipeline"] == "tie" {
			rejectedRow = row
		}
	}
	if rejectedRow == nil || rejectedRow["accepted"] != false || rejectedRow["recommendation_eligible"] != false || rejectedRow["crown_eligible"] != false {
		t.Fatalf("rejected ranking row = %#v", rejectedRow)
	}
	recommendation := anyMap(ranking["recommendation"])
	if recommendation["recommended_pipeline"] != "winner" || recommendation["crowned_pipeline"] != "winner" || recommendation["eligible_candidate_count"] != 1 {
		t.Fatalf("recommendation = %#v", recommendation)
	}
	if text := RenderRanking(ranking); !strings.Contains(text, "pipeline_acceptance pipeline=tie accepted=false") || !strings.Contains(text, "recommendation pipeline=winner crowned_pipeline=winner") {
		t.Fatalf("ranking text = %s", text)
	}
	onlyRejected := BuildRanking(RankingOptions{TaskFile: "tools/bench/tasks/crossfile-nav-v1.toml", Model: "openai/test", Rounds: 5, BaselinePipeline: "plain", Candidates: []string{"tie"}}, []PairResult{
		{CandidatePipeline: "tie", ExperimentID: "tie", ExitCode: 0, Summary: rejected},
	}, time.Unix(0, 0))
	noRecommendation := anyMap(onlyRejected["recommendation"])
	if noRecommendation["recommended_pipeline"] != nil || noRecommendation["crowned_pipeline"] != nil || noRecommendation["eligible_candidate_count"] != 0 {
		t.Fatalf("rejected-only recommendation = %#v", noRecommendation)
	}
}
