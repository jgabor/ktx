package experiment

import "testing"

func cacheAwareManifest() Manifest {
	m := v3Manifest()
	m.Methodology.Version = "v5-cache-aware"
	m.TargetValidPerPipeline = 2
	m.CacheControl = &CacheControlManifest{Mode: "observed", Source: "unavailable", WarmupCount: 1, TTFTAvailable: false, TTFTUnavailableReason: "runtime timestamps unavailable"}
	return m
}

func phaseAttempt(id, side, role string, cycle int, estimated float64) Attempt {
	a := evaluatedAttempt(id, side, cycle, 10, 0, 0, 1, 0, estimated, estimated, 1, 1)
	a.Role = role
	a.CacheControl = &CacheControlMetadata{Mode: "observed", Source: "unavailable", Phase: role, ExecutionOrder: cycle}
	return a
}

func TestCacheAwareSummaryPhasesRebuildAndBreakEven(t *testing.T) {
	attempts := []Attempt{
		phaseAttempt("b-cold", Baseline, ColdObservation, 0, 10), phaseAttempt("c-cold", Candidate, ColdObservation, 0, 13),
		phaseAttempt("b-warmup", Baseline, Warmup, 0, 4), phaseAttempt("c-warmup", Candidate, Warmup, 0, 6),
		phaseAttempt("b-1", Baseline, MeasuredWarm, 1, 5), phaseAttempt("c-1", Candidate, MeasuredWarm, 1, 2),
		phaseAttempt("b-2", Baseline, MeasuredWarm, 2, 5), phaseAttempt("c-2", Candidate, MeasuredWarm, 2, 2),
	}
	summary := BuildSummary(cacheAwareManifest(), attempts)
	if summary["headline_phase"] != MeasuredWarm {
		t.Fatalf("headline phase = %v", summary["headline_phase"])
	}
	pipelines := summary["pipelines"].(map[string]any)
	if pipelines[Baseline].(map[string]any)["valid_count"] != 2 {
		t.Fatalf("setup leaked into target: %#v", pipelines[Baseline])
	}
	phases := summary["phase_distributions"].(map[string]any)
	if phases[ColdObservation].(map[string]any)[Baseline].(map[string]any)["estimated_api_cost"].(map[string]any)["count"] != 1 {
		t.Fatalf("cold distributions = %#v", phases)
	}
	rebuild := summary["cold_to_warm_rebuild_penalty"].(map[string]any)
	if rebuild[Baseline].(map[string]any)["estimated_api_cost"] != float64(5) {
		t.Fatalf("rebuild = %#v", rebuild)
	}
	breakEven := summary["estimated_api_cost_break_even"].(map[string]any)
	if breakEven["measured_turn"] != 2 || breakEven["reason"] != nil {
		t.Fatalf("break even = %#v", breakEven)
	}
	if pipelines[Baseline].(map[string]any)["sample_classification"] != "exploratory" {
		t.Fatalf("classification = %#v", pipelines[Baseline])
	}
}

func TestCacheAwareBreakEvenNotFoundAndUnavailable(t *testing.T) {
	m := cacheAwareManifest()
	notFound := []Attempt{
		phaseAttempt("b-cold", Baseline, ColdObservation, 0, 1), phaseAttempt("c-cold", Candidate, ColdObservation, 0, 2),
		phaseAttempt("b-warmup", Baseline, Warmup, 0, 1), phaseAttempt("c-warmup", Candidate, Warmup, 0, 2),
		phaseAttempt("b-1", Baseline, MeasuredWarm, 1, 1), phaseAttempt("c-1", Candidate, MeasuredWarm, 1, 2),
	}
	result := BuildSummary(m, notFound)["estimated_api_cost_break_even"].(map[string]any)
	if result["measured_turn"] != nil || result["reason"] != "candidate_not_lower_within_measured_sample" {
		t.Fatalf("not found = %#v", result)
	}
	notFound[0].EstimatedAPICost = nil
	unavailableSummary := BuildSummary(m, notFound)
	result = unavailableSummary["estimated_api_cost_break_even"].(map[string]any)
	if result["measured_turn"] != nil || result["reason"] != "estimated_api_cost_unavailable" {
		t.Fatalf("unavailable = %#v", result)
	}
	rebuild := unavailableSummary["cold_to_warm_rebuild_penalty"].(map[string]any)[Baseline].(map[string]any)
	if rebuild["estimated_api_cost"] != nil || rebuild["reason"] == nil {
		t.Fatalf("unavailable rebuild = %#v", rebuild)
	}
}
