package experiment

import (
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cache/reuse/sequence"
)

func comparisonManifest() Manifest {
	manifest := directShareManifest()
	variant := "high"
	manifest.Variant = &variant
	manifest.PipelineExecutionPolicy = map[string]any{"kind": "alternating_by_cycle", "first_cycle": []string{Baseline, Candidate}, "second_cycle": []string{Candidate, Baseline}}
	manifest.Pipelines = map[string]PipelineMetadata{
		Baseline:  {PipelineID: "plain", SHA256: "plain-sha", Stages: []string{}},
		Candidate: {PipelineID: "compact", SHA256: "compact-sha", Stages: []string{"head:10"}},
	}
	return manifest
}

func comparisonAttempt(id, side string, cycle, order, read, logical int, score float64, fullPass bool) Attempt {
	manifest := comparisonManifest()
	provider, model, runtime := "openai", "test", "opencode"
	uncached := logical - read
	attempt := directShareAttempt(id, directStep(1, MeasuredInteger(int64(uncached)), MeasuredInteger(int64(read)), MeasuredInteger(0)))
	attempt.Side, attempt.Cycle = side, cycle
	attempt.ExperimentID = manifest.ExperimentID
	attempt.Status, attempt.StreamComplete = "completed", true
	attempt.SessionID = stringPointer("session-" + id)
	attempt.ToolCallCount = manifest.Task.MinimumToolCalls
	attempt.Score = Score{Score: score, FullPass: fullPass, AnswerMatchesExpected: fullPass}
	attempt.PipelineID = manifest.Pipelines[side].PipelineID
	attempt.ConfigSHA256 = manifest.Pipelines[side].SHA256
	attempt.CacheControl.ExecutionOrder = order
	attempt.ModelSteps[0].Identity.Provider = &provider
	attempt.ModelSteps[0].Identity.Model = &model
	attempt.ModelSteps[0].Identity.Runtime = &runtime
	attempt.sequence = coveredAttemptSequence(1)
	return attempt
}

func comparisonPairs(count int, baselineRead, candidateRead int) []Attempt {
	attempts := []Attempt{}
	order := 1
	for cycle := 1; cycle <= count; cycle++ {
		base := comparisonAttempt("b", Baseline, cycle, order, baselineRead, 100, 1, true)
		candidate := comparisonAttempt("c", Candidate, cycle, order+1, candidateRead, 100, 1, true)
		base.AttemptID, candidate.AttemptID = "b-"+string(rune('0'+cycle)), "c-"+string(rune('0'+cycle))
		if cycle%2 == 0 {
			base.CacheControl.ExecutionOrder, candidate.CacheControl.ExecutionOrder = order+1, order
		}
		attempts = append(attempts, base, candidate)
		order += 2
	}
	return attempts
}

func cacheComparison(t *testing.T, attempts []Attempt) map[string]any {
	t.Helper()
	return cacheComparisonWithManifest(t, comparisonManifest(), attempts)
}

func cacheComparisonWithManifest(t *testing.T, manifest Manifest, attempts []Attempt) map[string]any {
	t.Helper()
	return BuildSummary(manifest, attempts)["cached_input_share_comparison"].(map[string]any)
}

func TestCachedInputShareComparisonExcludesSetupAndPairsOnlyUniqueMeasuredCycles(t *testing.T) {
	attempts := comparisonPairs(2, 20, 30)
	setup := comparisonAttempt("setup", Baseline, 1, 99, 99, 100, 1, true)
	setup.Role, setup.CacheControl.Phase = ColdObservation, ColdObservation
	missing := comparisonAttempt("b-3", Baseline, 3, 5, 20, 100, 1, true)
	duplicate := comparisonAttempt("b-2-duplicate", Baseline, 2, 6, 20, 100, 1, true)
	attempts = append(attempts, setup, missing, duplicate)
	comparison := cacheComparison(t, attempts)
	if comparison["complete_pair_count"] != 1 {
		t.Fatalf("complete pairs = %#v", comparison)
	}
	setupView := comparison["setup"].(map[string]any)
	if setupView["excluded_from_comparison"] != true || len(setupView["observations"].([]any)) != 1 {
		t.Fatalf("setup = %#v", setupView)
	}
	observations := comparison["observations"].([]any)
	if len(observations) != 2 {
		t.Fatalf("observations = %#v", observations)
	}
	if !reflect.DeepEqual(observations[0].(map[string]any)["reasons"], []string{"duplicate_cycle_side"}) ||
		!reflect.DeepEqual(observations[1].(map[string]any)["reasons"], []string{"cycle_pair_incomplete"}) {
		t.Fatalf("observation reasons = %#v", observations)
	}
}

func TestCachedInputShareComparisonRetainsExactPairsAndSamePairQuality(t *testing.T) {
	attempts := comparisonPairs(1, 1, 2)
	attempts[0].Score.Score, attempts[0].FullPass = .75, false
	attempts[1].Score.Score, attempts[1].FullPass = .8, true
	// This unpaired high score must not influence comparison quality.
	unpaired := comparisonAttempt("unpaired", Baseline, 2, 3, 1, 3, 1, true)
	comparison := cacheComparison(t, append(attempts, unpaired))
	pair := comparison["pairs"].([]any)[0].(map[string]any)
	if got := pair["candidate_minus_baseline"].(map[string]any)["ratio"]; got != "1/100" {
		t.Fatalf("exact difference = %v", got)
	}
	base := pair["baseline"].(map[string]any)
	if pair["cycle"] != 1 || base["cycle"] != 1 || pair["candidate"].(map[string]any)["cycle"] != 1 ||
		base["direct_share"].(map[string]any)["ratio"] != "1/100" || base["configuration_sha256"] != "plain-sha" {
		t.Fatalf("inspectable pair = %#v", pair)
	}
	quality := comparison["quality"].(map[string]any)
	if quality["preserved"] != true || quality[Baseline].(map[string]any)["count"] != 1 {
		t.Fatalf("same-pair quality = %#v", quality)
	}
}

func TestPairedDifferenceStatisticsStudentTThresholdAndDirections(t *testing.T) {
	t.Run("four remains descriptive", func(t *testing.T) {
		stats := cacheComparison(t, comparisonPairs(4, 20, 30))["direct"].(map[string]any)["paired_difference"].(map[string]any)
		if stats["descriptive_only"] != true || stats["interval"] != nil || stats["direction"] != "unavailable" {
			t.Fatalf("n=4 stats = %#v", stats)
		}
	})
	t.Run("zero variance higher", func(t *testing.T) {
		stats := cacheComparison(t, comparisonPairs(5, 20, 30))["direct"].(map[string]any)["paired_difference"].(map[string]any)
		interval := stats["interval"].(map[string]any)
		if stats["direction"] != "higher" || interval["lower"] != .1 || interval["upper"] != .1 || stats["sample_standard_deviation"] != float64(0) {
			t.Fatalf("zero variance stats = %#v", stats)
		}
	})
	t.Run("lower", func(t *testing.T) {
		stats := cacheComparison(t, comparisonPairs(5, 30, 20))["direct"].(map[string]any)["paired_difference"].(map[string]any)
		if stats["direction"] != "lower" {
			t.Fatalf("lower stats = %#v", stats)
		}
	})
	t.Run("contains zero", func(t *testing.T) {
		attempts := comparisonPairs(5, 50, 50)
		reads := []int{90, 10, 90, 10, 50}
		for i := range reads {
			attempts[i*2+1].ModelSteps[0] = comparisonAttempt("x", Candidate, i+1, 1, reads[i], 100, 1, true).ModelSteps[0]
		}
		stats := cacheComparison(t, attempts)["direct"].(map[string]any)["paired_difference"].(map[string]any)
		interval := stats["interval"].(map[string]any)
		if stats["direction"] != "inconclusive" || interval["lower"].(float64) > 0 || interval["upper"].(float64) < 0 {
			t.Fatalf("inconclusive stats = %#v", stats)
		}
	})
	if got := studentTCritical95(4); math.Abs(got-2.7764451051977987) > 1e-10 {
		t.Fatalf("t critical df=4 = %.15f", got)
	}
}

func TestCachedInputShareComparisonBlocksIdentityProviderProtocolAndTemporalConfounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]Attempt)
		reason string
	}{
		{name: "provider mismatch", reason: "runtime_emitted_identity_conflict", mutate: func(a []Attempt) { other := "anthropic"; a[1].ModelSteps[0].Identity.Provider = &other }},
		{name: "configured provenance mismatch", reason: "identity_provenance_unstable", mutate: func(a []Attempt) { a[1].CacheControl.Variant = "low" }},
		{name: "unbalanced order", reason: "protocol_noncompliant", mutate: func(a []Attempt) { a[2].CacheControl.ExecutionOrder, a[3].CacheControl.ExecutionOrder = 3, 4 }},
		{name: "temporal coverage", reason: "temporal_coverage_unavailable", mutate: func(a []Attempt) {
			a[1].ModelSteps = append(a[1].ModelSteps, a[1].ModelSteps[0])
			a[1].ModelSteps[1].Ordinal = 2
		}},
		{name: "direct coverage", reason: "direct_evidence_incomplete", mutate: func(a []Attempt) { a[1].ModelSteps[0].CacheRead = UnknownInteger() }},
		{name: "retry", reason: "retry_boundary_observed", mutate: func(a []Attempt) { retry := 1; a[1].ModelSteps[0].Retry.Attempt = &retry }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := comparisonPairs(2, 20, 30)
			test.mutate(attempts)
			comparison := cacheComparison(t, attempts)
			found := false
			for _, raw := range comparison["blocking_reasons"].([]string) {
				found = found || raw == test.reason
			}
			if !found {
				t.Fatalf("blocking reasons = %#v, want %s", comparison["blocking_reasons"], test.reason)
			}
		})
	}
}

func TestCachedInputShareComparisonReportsResetBoundary(t *testing.T) {
	attempts := comparisonPairs(1, 20, 30)
	attempt := &attempts[1]
	attempt.ModelSteps = append(attempt.ModelSteps, attempt.ModelSteps[0])
	attempt.ModelSteps[1].Ordinal = 2
	attempt.sequence = coveredAttemptSequence(2)
	attempt.sequence.Events = append(attempt.sequence.Events, sequence.SequenceFact{
		Ordinal: 15, Boundary: sequence.BoundaryCompaction,
		Provenance: sequence.SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: 15, EndOrdinal: 15},
	})
	comparison := cacheComparison(t, attempts)
	found := false
	for _, reason := range comparison["blocking_reasons"].([]string) {
		found = found || reason == "reset_or_compaction_boundary_observed"
	}
	if !found {
		t.Fatalf("reset boundary reasons = %#v", comparison["blocking_reasons"])
	}
}

func TestCachedInputShareComparisonRejectsBoundariesBeforeFirstModelStep(t *testing.T) {
	tests := []struct {
		name     string
		boundary sequence.BoundaryKind
		session  string
		want     string
	}{
		{name: "retry", boundary: sequence.BoundaryRetry, session: "session-a", want: "retry_boundary_observed"},
		{name: "compaction", boundary: sequence.BoundaryCompaction, session: "session-a", want: "reset_or_compaction_boundary_observed"},
		{name: "interruption", boundary: sequence.BoundaryInterruption, session: "session-a", want: "temporal_boundary_observed"},
		{name: "restart", boundary: sequence.BoundaryRestart, want: "temporal_boundary_observed"},
		{name: "concurrent interleaving", session: "other-session", want: "temporal_boundary_observed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := comparisonPairs(2, 20, 30)
			attempt := &attempts[1]
			for i := range attempt.sequence.Coverage {
				attempt.sequence.Coverage[i].Provenance.StartOrdinal = 1
			}
			attempt.sequence.Events = append(attempt.sequence.Events, sequence.SequenceFact{
				Ordinal: 5, SessionID: test.session, Boundary: test.boundary,
				Provenance: sequence.SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: 5, EndOrdinal: 5},
			})
			comparison := cacheComparison(t, attempts)
			if comparison["complete_pair_count"] != 1 {
				t.Fatalf("boundary pair count = %#v", comparison)
			}
			observation := comparison["observations"].([]any)[0].(map[string]any)
			if !containsString(observation["reasons"].([]string), test.want) {
				t.Fatalf("boundary reasons = %#v, want %s", observation, test.want)
			}
		})
	}
}

func TestCachedInputShareComparisonRejectsBoundaryBeforeFirstOfMultipleSteps(t *testing.T) {
	attempts := comparisonPairs(2, 20, 30)
	attempt := &attempts[1]
	attempt.ModelSteps = append(attempt.ModelSteps, directStep(2, MeasuredInteger(60), MeasuredInteger(40), MeasuredInteger(0)))
	attempt.ModelSteps[1].Identity = attempt.ModelSteps[0].Identity
	attempt.sequence = coveredAttemptSequence(2)
	for i := range attempt.sequence.Coverage {
		attempt.sequence.Coverage[i].Provenance.StartOrdinal = 1
	}
	attempt.sequence.Events = append(attempt.sequence.Events, sequence.SequenceFact{
		Ordinal: 5, SessionID: "session-a", Boundary: sequence.BoundaryRetry,
		Provenance: sequence.SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: 5, EndOrdinal: 5},
	})
	comparison := cacheComparison(t, attempts)
	if comparison["complete_pair_count"] != 1 {
		t.Fatalf("before-first multi-step pair count = %#v", comparison)
	}
	observation := comparison["observations"].([]any)[0].(map[string]any)
	if !containsString(observation["reasons"].([]string), "retry_boundary_observed") {
		t.Fatalf("before-first multi-step reasons = %#v", observation)
	}
}

func TestCachedInputShareComparisonKeepsCleanCoveredAttemptPrefixes(t *testing.T) {
	attempts := comparisonPairs(2, 20, 30)
	for i := range attempts[0].sequence.Coverage {
		attempts[0].sequence.Coverage[i].Provenance.StartOrdinal = 1
	}
	attempts[1].ModelSteps = append(attempts[1].ModelSteps, directStep(2, MeasuredInteger(60), MeasuredInteger(40), MeasuredInteger(0)))
	attempts[1].ModelSteps[1].Identity = attempts[1].ModelSteps[0].Identity
	attempts[1].sequence = coveredAttemptSequence(2)
	for i := range attempts[1].sequence.Coverage {
		attempts[1].sequence.Coverage[i].Provenance.StartOrdinal = 1
	}
	comparison := cacheComparison(t, attempts)
	if comparison["complete_pair_count"] != 2 {
		t.Fatalf("clean covered prefixes rejected: %#v", comparison)
	}
}

func TestCachedInputShareComparisonRejectsContradictoryPrefixEventProvenance(t *testing.T) {
	attempts := comparisonPairs(2, 20, 30)
	attempt := &attempts[1]
	for i := range attempt.sequence.Coverage {
		attempt.sequence.Coverage[i].Provenance.StartOrdinal = 1
	}
	attempt.sequence.Events = append(attempt.sequence.Events, sequence.SequenceFact{
		Ordinal: 5, SessionID: "session-a", Boundary: sequence.BoundaryRetry,
		Provenance: sequence.SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "wrapper_stream", StartOrdinal: 5, EndOrdinal: 5},
	})
	comparison := cacheComparison(t, attempts)
	observation := comparison["observations"].([]any)[0].(map[string]any)
	if !containsString(observation["reasons"].([]string), "temporal_coverage_unavailable") {
		t.Fatalf("contradictory event provenance = %#v", observation)
	}
}

func TestCachedInputShareComparisonRejectsConflictingPrefixCoverageProvenance(t *testing.T) {
	attempts := comparisonPairs(2, 20, 30)
	attempt := &attempts[1]
	for i := range attempt.sequence.Coverage {
		attempt.sequence.Coverage[i].Provenance.StartOrdinal = 1
		if attempt.sequence.Coverage[i].Class == "segment" {
			attempt.sequence.Coverage[i].Provenance.Source = "wrapper_stream"
		}
	}
	comparison := cacheComparison(t, attempts)
	if comparison["complete_pair_count"] != 1 {
		t.Fatalf("conflicting prefix coverage pair count = %#v", comparison)
	}
	observation := comparison["observations"].([]any)[0].(map[string]any)
	if !containsString(observation["reasons"].([]string), "temporal_coverage_unavailable") {
		t.Fatalf("conflicting coverage provenance = %#v", observation)
	}
}

func TestCachedInputShareComparisonKeepsConfiguredIdentityAsContextWithoutBackfill(t *testing.T) {
	attempts := comparisonPairs(1, 20, 30)
	attempts[1].ModelSteps[0].Identity.Provider, attempts[1].ModelSteps[0].Identity.Model = nil, nil
	attempts[1].ModelSteps[0].Identity.Runtime = nil
	comparison := cacheComparison(t, attempts)
	if comparison["complete_pair_count"] != 1 {
		t.Fatalf("configured-only source identity blocked comparison: %#v", comparison)
	}
	candidate := comparison["pairs"].([]any)[0].(map[string]any)["candidate"].(map[string]any)
	identity := candidate["identity_provenance"].(map[string]any)
	if identity["configured_identity_backfills_source_events"] != false {
		t.Fatalf("configured identity backfill marker = %#v", identity)
	}
	event := identity["source_events"].([]any)[0].(map[string]any)
	if event["provider"] != nil || event["model"] != nil || event["runtime"] != nil {
		t.Fatalf("configured identity backfilled source event: %#v", event)
	}
}

func TestCachedInputShareComparisonKeepsSecondaryEstimatedSeparateAndDeterministic(t *testing.T) {
	attempts := comparisonPairs(1, 20, 30)
	for i := range attempts {
		attempts[i].ModelSteps = append(attempts[i].ModelSteps,
			directStep(2, MeasuredInteger(60), MeasuredInteger(40), MeasuredInteger(0)))
		attempts[i].ModelSteps[1].Identity = attempts[i].ModelSteps[0].Identity
		attempts[i].sequence = coveredAttemptSequence(2)
	}
	first := cacheComparison(t, attempts)
	second := cacheComparison(t, append([]Attempt(nil), attempts...))
	if !reflect.DeepEqual(first, second) {
		t.Fatal("comparison output is not deterministic")
	}
	secondary := first["secondary_estimated"].(map[string]any)
	if secondary["label"] != "estimated" || secondary["method"] != "pi_consecutive_step_reusable_basis" || secondary["ranking_effect"] != "none" {
		t.Fatalf("secondary = %#v", secondary)
	}
	if first["complete_pair_count"] != 1 {
		t.Fatalf("secondary changed direct pair eligibility: %#v", first)
	}
	if _, ok := first["success_authorized"]; ok {
		t.Fatalf("comparison implemented claim authorization: %#v", first)
	}
}

func TestPairedDifferenceMeanRetainsExactRational(t *testing.T) {
	stats := pairedDifferenceStatistics([]*big.Rat{big.NewRat(1, 3), big.NewRat(1, 6)})
	if stats["mean"].(map[string]any)["ratio"] != "1/4" {
		t.Fatalf("exact mean = %#v", stats)
	}
}

func TestCachedInputShareComparisonBalancesOnlyFinalEligibleSubset(t *testing.T) {
	attempts := comparisonPairs(4, 20, 30)
	// Raw execution order is balanced. Removing the candidate-first cycles for
	// independent direct-evidence reasons leaves a one-sided provisional subset.
	attempts[3].ModelSteps[0].CacheRead = UnknownInteger()
	attempts[7].ModelSteps[0].CacheRead = UnknownInteger()
	comparison := cacheComparison(t, attempts)
	if comparison["complete_pair_count"] != 0 || len(comparison["pairs"].([]any)) != 0 {
		t.Fatalf("unbalanced eligible subset entered paired statistics: %#v", comparison)
	}
	protocol := comparison["protocol"].(map[string]any)
	counts := protocol["observed_order_counts"].(map[string]int)
	if counts["baseline_first"] != 2 || counts["candidate_first"] != 0 || protocol["compliant"] != false {
		t.Fatalf("eligible-subset order assessment = %#v", protocol)
	}
	found := false
	for _, raw := range comparison["observations"].([]any) {
		observation := raw.(map[string]any)
		if observation["cycle"] == 1 && containsString(observation["reasons"].([]string), "execution_order_unbalanced") {
			found = true
		}
	}
	if !found {
		t.Fatalf("provisional pairs were not reclassified: %#v", comparison["observations"])
	}
}

func TestCachedInputShareComparisonUsesCompleteProtocolEligibility(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]Attempt)
	}{
		{name: "wrong experiment", mutate: func(a []Attempt) { a[1].ExperimentID = "other" }},
		{name: "below minimum tool calls", mutate: func(a []Attempt) { a[1].ToolCallCount = 0 }},
		{name: "cache mode", mutate: func(a []Attempt) { a[1].CacheControl.Mode = "isolated" }},
		{name: "cache source", mutate: func(a []Attempt) { a[1].CacheControl.Source = "runtime_adapter" }},
		{name: "idle gap", mutate: func(a []Attempt) { a[1].CacheControl.IdleGapMS++ }},
		{name: "observed namespace", mutate: func(a []Attempt) { a[1].CacheControl.NamespaceSHA256 = strings.Repeat("a", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := comparisonPairs(2, 20, 30)
			test.mutate(attempts)
			comparison := cacheComparison(t, attempts)
			if comparison["complete_pair_count"] != 1 {
				t.Fatalf("invalid attempt entered comparison: %#v", comparison)
			}
			observation := comparison["observations"].([]any)[0].(map[string]any)
			if !containsString(observation["reasons"].([]string), "protocol_noncompliant") {
				t.Fatalf("protocol reasons = %#v", observation)
			}
			candidate := observation["candidate"].(map[string]any)
			provenance := candidate["protocol_provenance"].(map[string]any)
			if provenance["cache_control"] == nil || provenance["expected_cache_control"] == nil || provenance["minimum_tool_calls"] == nil || provenance["temporal_coverage"] == nil {
				t.Fatalf("protocol provenance is incomplete: %#v", provenance)
			}
		})
	}
}

func TestCachedInputShareComparisonChecksIsolatedSideNamespace(t *testing.T) {
	manifest := comparisonManifest()
	manifest.CacheControl.Mode = "isolated"
	manifest.CacheControl.Source = "provider_adapter"
	manifest.CacheControl.NamespaceSHA256 = map[string]string{Baseline: strings.Repeat("a", 64), Candidate: strings.Repeat("b", 64)}
	attempts := comparisonPairs(2, 20, 30)
	for i := range attempts {
		attempts[i].CacheControl.Mode = "isolated"
		attempts[i].CacheControl.Source = "provider_adapter"
		attempts[i].CacheControl.NamespaceSHA256 = manifest.CacheControl.NamespaceSHA256[attempts[i].Side]
	}
	attempts[1].CacheControl.NamespaceSHA256 = manifest.CacheControl.NamespaceSHA256[Baseline]
	comparison := cacheComparisonWithManifest(t, manifest, attempts)
	if comparison["complete_pair_count"] != 1 {
		t.Fatalf("wrong side namespace entered comparison: %#v", comparison)
	}
}

func TestCachedInputShareComparisonRequiresPositiveSingleStepTemporalCoverage(t *testing.T) {
	attempts := comparisonPairs(2, 20, 30)
	attempts[1].sequence = nil
	comparison := cacheComparison(t, attempts)
	if comparison["complete_pair_count"] != 1 {
		t.Fatalf("single-step silence entered comparison: %#v", comparison)
	}
	observation := comparison["observations"].([]any)[0].(map[string]any)
	if !containsString(observation["reasons"].([]string), "temporal_coverage_unavailable") {
		t.Fatalf("single-step temporal reasons = %#v", observation)
	}
}

func TestCachedInputShareComparisonChecksRuntimeBeforeConfiguredOnlyIdentity(t *testing.T) {
	attempts := comparisonPairs(2, 20, 30)
	wrong := "other-runtime"
	attempts[1].ModelSteps[0].Identity.Provider = nil
	attempts[1].ModelSteps[0].Identity.Model = nil
	attempts[1].ModelSteps[0].Identity.Runtime = &wrong
	comparison := cacheComparison(t, attempts)
	observation := comparison["observations"].([]any)[0].(map[string]any)
	if !containsString(observation["reasons"].([]string), "runtime_emitted_identity_conflict") {
		t.Fatalf("runtime-only conflict was accepted: %#v", observation)
	}
}

func TestCachedInputShareComparisonEmitsIntervalAndQualityBlockersInPrecedence(t *testing.T) {
	attempts := comparisonPairs(5, 50, 50)
	reads := []int{90, 10, 90, 10, 50}
	for i := range reads {
		attempts[i*2+1].ModelSteps[0] = comparisonAttempt("x", Candidate, i+1, 1, reads[i], 100, .5, false).ModelSteps[0]
		attempts[i*2+1].Score.Score = .5
		attempts[i*2+1].FullPass = false
	}
	comparison := cacheComparison(t, attempts)
	want := []string{"two_sided_95_percent_interval_contains_zero", "quality_not_preserved"}
	got := comparison["blocking_reasons"].([]string)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blocking precedence = %v, want %v", got, want)
	}
}

func TestCachedInputShareComparisonAcceptsRunnerAlternationWithPositiveCoverage(t *testing.T) {
	comparison := cacheComparison(t, comparisonPairs(5, 20, 30))
	if comparison["complete_pair_count"] != 5 || comparison["protocol"].(map[string]any)["compliant"] != true {
		t.Fatalf("runner alternating protocol rejected: %#v", comparison)
	}
}

func TestCachedInputShareComparisonAndValidationAgreeOnIsolationAuthority(t *testing.T) {
	buildEvidence := func(source string) (Manifest, []Attempt) {
		manifest := comparisonManifest()
		manifest.TargetValidPerPipeline = 1
		manifest.CacheControl.Mode = "isolated"
		manifest.CacheControl.Source = source
		manifest.CacheControl.WarmupCount = 0
		manifest.CacheControl.NamespaceSHA256 = map[string]string{Baseline: strings.Repeat("a", 64), Candidate: strings.Repeat("b", 64)}
		manifest.Pricing = &PricingSnapshotMetadata{Provider: "openai", Model: "test"}
		attempts := comparisonPairs(2, 20, 30)
		setup := []Attempt{
			comparisonAttempt("b-cold", Baseline, 0, 5, 20, 100, 1, true),
			comparisonAttempt("c-cold", Candidate, 0, 6, 30, 100, 1, true),
		}
		setup[0].Role, setup[0].CacheControl.Phase = ColdObservation, ColdObservation
		setup[1].Role, setup[1].CacheControl.Phase = ColdObservation, ColdObservation
		attempts = append(attempts, setup...)
		for i := range attempts {
			attempts[i].CacheControl.Mode = "isolated"
			attempts[i].CacheControl.Source = source
			attempts[i].CacheControl.NamespaceSHA256 = manifest.CacheControl.NamespaceSHA256[attempts[i].Side]
		}
		return manifest, attempts
	}
	assess := func(t *testing.T, source string) (map[string]any, []string) {
		t.Helper()
		manifest, attempts := buildEvidence(source)
		summary := BuildSummary(manifest, attempts)
		summaryData, err := MarshalJSON(summary)
		if err != nil {
			t.Fatal(err)
		}
		counts := CountValidForMethodology(attempts, manifest.Task.MinimumToolCalls, manifest.Methodology.Version)
		reasons := validateCacheAwareEvidence(manifest, attempts, &pricingSnapshot{Provider: "openai", Model: "test"}, summaryData, counts)
		return summary["cached_input_share_comparison"].(map[string]any), reasons
	}

	t.Run("runtime adapter rejected by both", func(t *testing.T) {
		comparison, reasons := assess(t, "runtime_adapter")
		if comparison["complete_pair_count"] != 0 ||
			!containsString(comparison["blocking_reasons"].([]string), "protocol_noncompliant") ||
			!containsString(reasons, "retained runtime-adapter isolation metadata requires OpenCode 1.18.0") {
			t.Fatalf("comparison/validation diverged: comparison=%#v validation=%v", comparison, reasons)
		}
	})
	t.Run("provider adapter accepted by both", func(t *testing.T) {
		comparison, reasons := assess(t, "provider_adapter")
		if comparison["complete_pair_count"] != 2 || len(reasons) != 0 {
			t.Fatalf("comparison/validation diverged: comparison=%#v validation=%v", comparison, reasons)
		}
	})
}
