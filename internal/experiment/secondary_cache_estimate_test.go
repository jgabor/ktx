package experiment

import (
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/cache/reuse/sequence"
)

func coveredAttemptSequence(stepCount int) *attemptSequence {
	facts := make([]sequence.SequenceFact, stepCount)
	for i := range facts {
		ordinal := uint64((i + 1) * 10)
		facts[i] = sequence.SequenceFact{
			Ordinal: ordinal, ModelStepOrdinal: uint64(i + 1), SessionID: "session-a",
			Provenance: sequence.SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: ordinal, EndOrdinal: ordinal},
		}
	}
	coverage := make([]sequence.CoverageFact, 0, 6)
	provenance := sequence.SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: facts[0].Ordinal, EndOrdinal: facts[len(facts)-1].Ordinal}
	for _, class := range []string{"order", "retry", "reset", "segment", "concurrency", "identity"} {
		coverage = append(coverage, sequence.CoverageFact{Class: class, State: sequence.CoverageCovered, Provenance: provenance})
	}
	return &attemptSequence{Facts: facts, Events: append([]sequence.SequenceFact(nil), facts...), Coverage: coverage}
}

func TestSecondaryEstimateRetainsEligibleRawBasisAndLabels(t *testing.T) {
	attempt := directShareAttempt(
		"eligible",
		directStep(1, MeasuredInteger(1000), MeasuredInteger(1000), MeasuredInteger(0)),
		directStep(2, MeasuredInteger(976), MeasuredInteger(1024), MeasuredInteger(0)),
	)
	attempt.sequence = coveredAttemptSequence(2)
	estimate := buildSecondaryCacheEstimate([]Attempt{attempt})
	if estimate["label"] != "estimated" || estimate["method"] != "pi_consecutive_step_reusable_basis" || estimate["eligible_count"] != 1 || estimate["ineligible_count"] != 1 || estimate["ranking_effect"] != "none" || estimate["ttl_cause"] != "unsupported" || estimate["expiry_cause"] != "unsupported" {
		t.Fatalf("estimate metadata = %#v", estimate)
	}
	rows := estimate["attempts"].([]any)[0].(map[string]any)["pairs"].([]any)
	if rows[0].(map[string]any)["reason"] != "first_step" {
		t.Fatalf("first row = %#v", rows[0])
	}
	pair := rows[1].(map[string]any)
	for field, want := range map[string]int64{"prior_logical_input": 2000, "current_logical_input": 2000, "current_cache_read": 1024, "estimated_reusable_tokens": 2000, "shortfall": 976} {
		value, ok := pair[field].(*big.Int)
		if !ok || value.Cmp(big.NewInt(want)) != 0 {
			t.Errorf("%s = %#v, want %d", field, pair[field], want)
		}
	}
	if pair["interpretation"] != "within_noise_floor" || pair["reuse_share"].(map[string]any)["ratio"] != "64/125" {
		t.Fatalf("eligible pair = %#v", pair)
	}
	counts := estimate["ineligible_counts"].(map[string]int)
	if counts["first_step"] != 1 || len(counts) != len(sequence.IneligibilityReasons) {
		t.Fatalf("ineligible counts = %#v", counts)
	}
}

func TestSecondaryEstimateConsumesRetainedWrapperSequenceWithoutInferringSilence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	stream := strings.Join([]string{
		`{"type":"step_finish","sessionID":"session-a","part":{"type":"step-finish","sessionID":"session-a","reason":"tool-calls","tokens":{"input":1000,"output":1,"reasoning":0,"cache":{"read":100,"write":0}}}}`,
		`{"type":"step_finish","sessionID":"session-a","part":{"type":"step-finish","sessionID":"session-a","reason":"stop","tokens":{"input":1000,"output":1,"reasoning":0,"cache":{"read":100,"write":0}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	metrics := ParseStream(path, nil, nil)
	attempt := directShareAttempt("wrapper")
	attempt.Metrics = metrics
	attempt.Artifacts["stream"] = "stream.jsonl"
	rows := buildSecondaryCacheEstimate([]Attempt{attempt})["attempts"].([]any)[0].(map[string]any)["pairs"].([]any)
	second := rows[1].(map[string]any)
	if second["reason"] != "retry_coverage_unavailable" {
		t.Fatalf("wrapper pair = %#v", second)
	}
	coverage := second["coverage"].(map[string]any)
	if coverage["order"] != sequence.CoverageCovered || coverage["identity"] != sequence.CoverageCovered || coverage["retry"] != "unavailable" {
		t.Fatalf("wrapper coverage = %#v", coverage)
	}
}

func TestSecondaryEstimateTreatsMissingSequenceCoverageAsUnavailable(t *testing.T) {
	attempt := directShareAttempt(
		"unavailable",
		directStep(1, MeasuredInteger(100), MeasuredInteger(10), MeasuredInteger(0)),
		directStep(2, MeasuredInteger(100), MeasuredInteger(10), MeasuredInteger(0)),
	)
	estimate := buildSecondaryCacheEstimate([]Attempt{attempt})
	rows := estimate["attempts"].([]any)[0].(map[string]any)["pairs"].([]any)
	if rows[0].(map[string]any)["reason"] != "first_step" || rows[1].(map[string]any)["reason"] != "order_coverage_unavailable" || estimate["eligible_count"] != 0 {
		t.Fatalf("unavailable estimate = %#v", estimate)
	}
}

func TestSecondaryEstimateUsesSequenceReasonPrecedence(t *testing.T) {
	attempt := directShareAttempt(
		"precedence",
		directStep(1, MeasuredInteger(100), MeasuredInteger(10), MeasuredInteger(0)),
		directStep(2, UnknownInteger(), MeasuredInteger(10), MeasuredInteger(0)),
	)
	attempt.sequence = coveredAttemptSequence(2)
	for i := range attempt.sequence.Coverage {
		if attempt.sequence.Coverage[i].Class == "retry" {
			attempt.sequence.Coverage = append(attempt.sequence.Coverage[:i], attempt.sequence.Coverage[i+1:]...)
			break
		}
	}
	rows := buildSecondaryCacheEstimate([]Attempt{attempt})["attempts"].([]any)[0].(map[string]any)["pairs"].([]any)
	if got := rows[1].(map[string]any)["reason"]; got != "retry_coverage_unavailable" {
		t.Fatalf("reason = %v, want retry coverage before current field", got)
	}
}

func TestSecondaryEstimateCannotAffectRanking(t *testing.T) {
	summary := BuildSummary(directShareManifest(), []Attempt{})
	options := RankingOptions{TaskFile: "task.toml", BaselinePipeline: "baseline", Candidates: []string{"candidate"}}
	pair := PairResult{CandidatePipeline: "candidate", ExperimentID: "exp", Summary: summary}
	before := BuildRanking(options, []PairResult{pair}, time.Unix(0, 0))
	estimate := summary[promptCacheTokenHitRateComparisonField].(map[string]any)["secondary_estimated"].(map[string]any)
	estimate["eligible_count"] = 999
	estimate["ineligible_count"] = 999
	after := BuildRanking(options, []PairResult{pair}, time.Unix(0, 0))
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("secondary estimate changed ranking:\nbefore=%#v\nafter=%#v", before, after)
	}
}
