package experiment

import (
	"strings"
	"testing"
	"time"
)

func TestRecoveryAwareComparisonPrimaryAndDiagnostics(t *testing.T) {
	manifest := recoveryManifest(1)
	baseline := recoveryAttempt("base", Baseline, 1, true, number(100), number(20), number(5), 2, 1, 6, 2)
	candidate := recoveryAttempt("candidate", Candidate, 1, true, number(70), number(20), number(5), 1, 0, 5, 1)
	summary := BuildSummary(manifest, []Attempt{baseline, candidate})
	if summary.SchemaVersion() != 6 {
		t.Fatalf("schema = %d, want 6", summary.SchemaVersion())
	}
	comparison := summary["recovery_aware_comparison"].(map[string]any)
	arms := comparison["arms"].(map[string]any)
	primary := arms[Candidate].(map[string]any)["runtime_total_tokens_per_correct_task"].(map[string]any)
	if primary["state"] != "available" || primary["value"] != 95.0 {
		t.Fatalf("primary = %#v", primary)
	}
	recovery := arms[Baseline].(map[string]any)["recovery"].(map[string]any)
	calls := arms[Baseline].(map[string]any)["tool_calls"].(map[string]any)
	if recovery["attempt_count"] != 2 || recovery["repeated_attempt_count"] != 1 || calls["total_call_count"] != 6 || calls["exact_repeated_call_count"] != 2 {
		t.Fatalf("diagnostics = %#v %#v", recovery, calls)
	}
	if !strings.Contains(RenderSummary(summary), "runtime_total_tokens_per_correct_task=95") {
		t.Fatal("text omitted primary outcome")
	}
	encoded, err := MarshalJSON(summary)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSummary(encoded)
	if err != nil {
		t.Fatal(err)
	}
	regenerated, err := MarshalJSON(decoded)
	if err != nil || string(regenerated) != string(encoded) {
		t.Fatalf("schema 6 did not regenerate byte-exact: %v", err)
	}
}

func TestRecoveryAwareComparisonUnavailableAndRankingExclusion(t *testing.T) {
	manifest := recoveryManifest(1)
	complete := BuildSummary(manifest, []Attempt{
		recoveryAttempt("base", Baseline, 1, true, number(100), number(10), number(5), 0, 0, 1, 0),
		recoveryAttempt("candidate", Candidate, 1, true, number(50), number(10), number(5), 0, 0, 1, 0),
	})
	incomplete := BuildSummary(manifest, []Attempt{
		recoveryAttempt("base2", Baseline, 1, true, number(100), number(10), number(5), 0, 0, 1, 0),
		recoveryAttempt("candidate2", Candidate, 1, true, nil, number(10), number(5), 0, 0, 1, 0),
	})
	failedQuality := BuildSummary(manifest, []Attempt{
		recoveryAttempt("base3", Baseline, 1, true, number(100), number(10), number(5), 0, 0, 1, 0),
		recoveryAttempt("candidate3", Candidate, 1, false, number(1), number(1), number(1), 0, 0, 1, 0),
	})
	ranking := BuildRanking(RankingOptions{BaselinePipeline: "base", Candidates: []string{"complete", "incomplete", "wrong"}}, []PairResult{
		{CandidatePipeline: "complete", Summary: complete}, {CandidatePipeline: "incomplete", Summary: incomplete}, {CandidatePipeline: "wrong", Summary: failedQuality},
	}, time.Unix(0, 0))
	rows := ranking["ranking"].([]map[string]any)
	if rows[0]["pipeline"] != "complete" {
		t.Fatalf("first row = %#v", rows[0])
	}
	for _, row := range rows {
		if row["pipeline"] == "incomplete" {
			primary := row["runtime_total_tokens_per_correct_task"].(map[string]any)
			if primary["state"] != "unavailable" || primary["reason"] != "incomplete_runtime_token_totals" {
				t.Fatalf("incomplete primary = %#v", primary)
			}
			if row["recommendation_eligible"] != false {
				t.Fatalf("incomplete row remained eligible: %#v", row)
			}
		}
		if row["pipeline"] == "wrong" {
			primary := row["runtime_total_tokens_per_correct_task"].(map[string]any)
			if primary["reason"] != "quality_gate_failed" {
				t.Fatalf("quality primary = %#v", primary)
			}
		}
	}
}

func recoveryManifest(target int) Manifest {
	return Manifest{ExperimentID: "test", TargetValidPerPipeline: target, Task: Task{MinimumToolCalls: 1}, Methodology: MethodologyMetadata{Version: recoveryAwareMethodologyVersion}}
}

func recoveryAttempt(id, side string, cycle int, pass bool, in, out, reasoning *float64, recoveries, repeatedRecovery, calls, repeats int) Attempt {
	session := "session-" + id
	return Attempt{
		SchemaVersion: 3, AttemptID: id, Side: side, Role: MeasuredWarm, Cycle: cycle, Status: "completed", SessionID: &session,
		Metrics:  Metrics{StreamComplete: true, Score: Score{Score: map[bool]float64{true: 1}[pass], FullPass: pass}, ToolCallCount: calls, TokensIn: in, TokensOut: out, Reasoning: reasoning},
		outcomes: &attemptOutcomes{Recovery: map[string]any{"cue_count": 1, "followed_cue_count": map[bool]int{true: 1}[recoveries > 0], "attempt_count": recoveries, "repeated_attempt_count": repeatedRecovery}, Repeats: map[string]any{"call_count": calls, "repeated_call_count": repeats}},
	}
}

func number(value float64) *float64 { return &value }
