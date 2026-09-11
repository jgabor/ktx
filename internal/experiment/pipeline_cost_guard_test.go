package experiment

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func pipelineCostGuardFixture() (Manifest, []Attempt) {
	manifest := promptCacheTokenHitRateManifest()
	manifest.TargetValidPerPipeline = 5
	manifest.Pricing = &PricingSnapshotMetadata{
		Path: "pricing.json", SHA256: "pricing-sha", SourceURL: "https://models.dev/api.json", Provider: "openai", Model: "test",
	}
	attempts := comparisonPairs(5, 20, 30)
	for index := range attempts {
		cost := .01
		if attempts[index].Side == Candidate {
			cost = .005
		}
		attempts[index].EstimatedAPICost = floatPointer(cost)
		attempts[index].Cost = floatPointer(0)
	}
	return manifest, attempts
}

func pipelineCostGuard(t *testing.T, manifest Manifest, attempts []Attempt) map[string]any {
	t.Helper()
	summary := BuildSummary(manifest, attempts)
	if summary["schema_version"] != 5 {
		t.Fatalf("schema_version = %v, want 5", summary["schema_version"])
	}
	guard, _ := summary[pipelineCostGuardField].(map[string]any)
	if guard == nil {
		t.Fatalf("missing %s: %#v", pipelineCostGuardField, summary)
	}
	if summary["accepted"] != guard["accepted"] || !reflect.DeepEqual(summary["acceptance_reasons"], guard["reasons"]) {
		t.Fatalf("top-level acceptance disagrees with guard: %#v", summary)
	}
	return guard
}

func TestPipelineCostGuardAcceptsOnlyLowerQualityPreservingEstimate(t *testing.T) {
	manifest, attempts := pipelineCostGuardFixture()
	guard := pipelineCostGuard(t, manifest, attempts)
	if guard["accepted"] != true || len(guard["reasons"].([]string)) != 0 {
		t.Fatalf("guard = %#v", guard)
	}
	cost := guard["estimated_api_cost_per_correct"].(map[string]any)
	if cost[Baseline] != .01 || cost[Candidate] != .005 || cost["comparison"] != "lower" {
		t.Fatalf("cost per correct = %#v", cost)
	}
	pair := guard["pairs"].([]any)[0].(map[string]any)
	reported := pair[Baseline].(map[string]any)["reported_cost"].(map[string]any)
	if reported["state"] != "measured" || reported["value"] != float64(0) {
		t.Fatalf("reported zero was not preserved: %#v", reported)
	}
	estimated := pair[Baseline].(map[string]any)["estimated_api_cost"].(map[string]any)
	if estimated["state"] != "estimated" || estimated["pricing_provenance"] != manifest.Pricing {
		t.Fatalf("estimated pricing provenance = %#v", estimated)
	}
}

func TestPipelineCostGuardFailsClosedWithOrderedReasons(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest, *[]Attempt)
		want   []string
	}{
		{
			name: "tie after rounding",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				for index := range *attempts {
					if (*attempts)[index].Side == Candidate {
						(*attempts)[index].EstimatedAPICost = floatPointer(.0100004)
					}
				}
			},
			want: []string{"candidate_estimated_api_cost_per_correct_not_lower"},
		},
		{
			name: "missing estimate",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				(*attempts)[1].EstimatedAPICost = nil
			},
			want: []string{"estimated_api_cost_unavailable"},
		},
		{
			name: "missing pricing provenance",
			mutate: func(manifest *Manifest, _ *[]Attempt) {
				manifest.Pricing = nil
			},
			want: []string{"estimated_api_cost_unavailable"},
		},
		{
			name: "nonfinite estimate",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				(*attempts)[1].EstimatedAPICost = floatPointer(math.Inf(1))
			},
			want: []string{"estimated_api_cost_nonfinite"},
		},
		{
			name: "zero correct answers",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				for index := range *attempts {
					(*attempts)[index].FullPass = false
					(*attempts)[index].AnswerMatchesExpected = false
				}
			},
			want: []string{"zero_correct_answers"},
		},
		{
			name: "incomplete pair",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				*attempts = (*attempts)[:len(*attempts)-1]
			},
			want: []string{"incomplete_pairs"},
		},
		{
			name: "unstable identity",
			mutate: func(manifest *Manifest, attempts *[]Attempt) {
				manifest.TargetValidPerPipeline = 4
				(*attempts)[1].CacheControl.Model = "openai/other"
			},
			want: []string{"identity_provenance_unstable"},
		},
		{
			name: "noncompliant protocol",
			mutate: func(manifest *Manifest, attempts *[]Attempt) {
				manifest.TargetValidPerPipeline = 4
				(*attempts)[1].CacheControl.ExecutionOrder = (*attempts)[0].CacheControl.ExecutionOrder
			},
			want: []string{"protocol_noncompliant"},
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
			want: []string{"quality_not_preserved"},
		},
		{
			name: "worse full pass rate",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				(*attempts)[1].FullPass = false
				(*attempts)[1].AnswerMatchesExpected = false
			},
			want: []string{"quality_not_preserved"},
		},
		{
			name: "multiple reasons retain precedence",
			mutate: func(_ *Manifest, attempts *[]Attempt) {
				(*attempts)[1].EstimatedAPICost = floatPointer(math.Inf(1))
				for index := range *attempts {
					(*attempts)[index].FullPass = false
					(*attempts)[index].AnswerMatchesExpected = false
					if (*attempts)[index].Side == Candidate {
						(*attempts)[index].Score.Score = .5
					}
				}
				*attempts = (*attempts)[:len(*attempts)-1]
			},
			want: []string{"incomplete_pairs", "estimated_api_cost_nonfinite", "zero_correct_answers", "quality_not_preserved"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, attempts := pipelineCostGuardFixture()
			test.mutate(&manifest, &attempts)
			guard := pipelineCostGuard(t, manifest, attempts)
			if guard["accepted"] != false {
				t.Fatalf("rejected guard accepted: %#v", guard)
			}
			if got := guard["reasons"].([]string); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("reasons = %v, want %v", got, test.want)
			}
			if test.name == "nonfinite estimate" {
				if _, err := MarshalJSON(BuildSummary(manifest, attempts)); err != nil {
					t.Fatalf("nonfinite estimate was not rendered unavailable: %v", err)
				}
			}
		})
	}
}

func TestPipelineCostGuardRankRetainsRejectedCandidateWithoutCrowningIt(t *testing.T) {
	manifest, attempts := pipelineCostGuardFixture()
	for index := range attempts {
		if attempts[index].Side == Candidate {
			attempts[index].EstimatedAPICost = floatPointer(.01)
		}
	}
	summary := BuildSummary(manifest, attempts)
	ranking := BuildRanking(RankingOptions{
		TaskFile: "tools/bench/tasks/example.toml", Model: "openai/test", Rounds: 5,
		BaselinePipeline: "plain", Candidates: []string{"compact"},
	}, []PairResult{{CandidatePipeline: "compact", ExperimentID: "pair", ExitCode: 0, Summary: summary}}, time.Unix(0, 0))
	rows := ranking["ranking"].([]map[string]any)
	if rows[0]["side"] != Baseline || rows[0]["rank"] != 1 {
		t.Fatalf("rejected candidate took rank one: %#v", rows)
	}
	candidate := rows[1]
	if candidate["pipeline"] != "compact" || candidate["accepted"] != false || candidate["recommendation_eligible"] != false || candidate["rank"] == 1 {
		t.Fatalf("rejected candidate row = %#v", candidate)
	}
	if !strings.Contains(RenderRanking(ranking), "acceptance pipeline=compact accepted=false recommendation_eligible=false") {
		t.Fatalf("ranking text omitted rejected acceptance state: %s", RenderRanking(ranking))
	}
}

func TestPipelineCostGuardIsNotUsedForExactMatch(t *testing.T) {
	tests := []struct {
		name     string
		manifest Manifest
		attempts []Attempt
	}{
		{name: "cacheless", manifest: fixtureManifest(), attempts: fixtureAttempts()},
		{name: "cache", manifest: cacheFixtureManifest(), attempts: cacheFixtureAttempts()},
		{name: "priced", manifest: v3Manifest(), attempts: v3Attempts()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := BuildSummary(test.manifest, test.attempts)
			if _, exists := summary[pipelineCostGuardField]; exists {
				t.Fatalf("historical summary gained guard: %#v", summary)
			}
			if _, exists := summary["accepted"]; exists {
				t.Fatalf("historical summary gained acceptance: %#v", summary)
			}
		})
	}
}
