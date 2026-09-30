package experiment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func currentDecisionArchive(t *testing.T, id, spec, baseline, candidate string, priced, usage bool) string {
	t.Helper()
	e := &costViewExecutor{fakeExecutor: fakeExecutor{sessions: map[string]string{}, fullStepUsage: usage}, costs: map[string]string{Baseline: baseline, Candidate: candidate}}
	return produceEntryArchiveID(t, id, spec, priced, usage, e)
}

func TestCurrentRankingUnionAndHistory(t *testing.T) {
	for _, tc := range []struct {
		name, secondReport, basis, winner string
		priced                            bool
	}{
		{"reported opposite estimate", "2", "reported", "passthrough", true},
		{"partial uses whole estimate", "missing", "estimated_api", "", true},
		{"partial unavailable whole cohort", "missing", "unavailable", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirs := []string{currentDecisionArchive(t, "first", "exact-match", "1", "3", tc.priced, true), currentDecisionArchive(t, "second", "exact-match", "1", tc.secondReport, tc.priced, true)}
			pairs := []PairResult{}
			before := []string{}
			for i, dir := range dirs {
				regen, err := RegenerateSummary(dir)
				if err != nil {
					t.Fatal(err)
				}
				pairs = append(pairs, PairResult{CandidatePipeline: "conservative-a", ExperimentID: []string{"first", "second"}[i], ExperimentDir: dir, Summary: regen.Summary})
				hash, _, err := directorySHA256(dir)
				if err != nil {
					t.Fatal(err)
				}
				before = append(before, hash)
			}
			options := RankingOptions{BaselinePipeline: "passthrough"}
			old := BuildRanking(options, pairs, time.Unix(0, 0))
			oldBytes, _ := MarshalJSON(old)
			view, err := BuildCurrentRanking(options, pairs)
			if err != nil {
				t.Fatal(err)
			}
			selection := view["cost_selection"].(CostSelection)
			if selection.Basis != tc.basis || selection.RequiredCount != 4 {
				t.Fatalf("selection=%+v", selection)
			}
			if tc.winner != "" && view["cost_winner"] != tc.winner {
				t.Fatalf("winner=%v view=%v", view["cost_winner"], view)
			}
			if tc.winner == "" && view["cost_winner"] != nil {
				t.Fatalf("tie/unavailable crowned winner: %v", view)
			}
			for _, row := range view["ranking"].([]map[string]any) {
				if tc.basis == "unavailable" && (summaryValue(row["selected_cost_per_correct"]) != nil || summaryValue(row["mean_selected_cost"]) != nil || row["rank"] != nil) {
					t.Fatalf("unavailable numeric ranking: %v", row)
				}
				if summaryValue(row["full_pass_rate"]) == nil || *summaryValue(row["full_pass_rate"]) != 1 {
					t.Fatalf("quality lost: %v", row)
				}
			}
			again, _ := MarshalJSON(BuildRanking(options, pairs, time.Unix(0, 0)))
			if !bytes.Equal(oldBytes, again) {
				t.Fatal("historical ranking changed")
			}
			for i, dir := range dirs {
				hash, _, err := directorySHA256(dir)
				if err != nil || hash != before[i] {
					t.Fatalf("archive changed: %v", err)
				}
			}
			if _, err := BuildCurrentRanking(options, []PairResult{{CandidatePipeline: "summary-only", Summary: pairs[0].Summary}}); err == nil {
				t.Fatal("accepted summary-only authority")
			}
		})
	}
}

func TestCurrentAcceptanceRetainedReportedAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, baseline, candidate, basis, comparison string
		priced, accepted                             bool
	}{
		{"reported rejects cheaper estimate", "1", "2", "reported", "higher", true, false},
		{"reported accepts", "2", "1", "reported", "lower", true, true},
		{"zero falls back", "0", "0", "estimated_api", "lower", true, true},
		{"missing falls back", "missing", "missing", "estimated_api", "lower", true, true},
		{"unavailable retains quality", "missing", "missing", "unavailable", "unavailable", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := currentDecisionArchive(t, "entry-control", "prompt-cache-token-hit-rate", tc.baseline, tc.candidate, tc.priced, true)
			raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			view, err := LoadCurrentCostView(dir)
			if err != nil {
				t.Fatal(err)
			}
			guard := view.Acceptance
			selection := guard["cost_selection"].(CostSelection)
			if selection.Basis != tc.basis || guard["accepted"] != tc.accepted || guard["comparison"] != tc.comparison {
				t.Fatalf("guard=%v", guard)
			}
			if anyMap(guard["quality"])["preserved"] != true {
				t.Fatalf("quality discarded: %v", guard)
			}
			validation, err := ValidateExperiment(dir)
			if err != nil || validation["valid"] != true {
				t.Fatalf("valid evidence rejected: %v %v", validation, err)
			}
			regen, err := RegenerateSummary(dir)
			if err != nil {
				t.Fatal(err)
			}
			regenerated, _ := MarshalJSON(regen.Summary)
			if !bytes.Equal(raw, regenerated) {
				t.Fatal("historical bytes changed")
			}
			rank, err := BuildCurrentRanking(RankingOptions{BaselinePipeline: "passthrough"}, []PairResult{{CandidatePipeline: "conservative-a", ExperimentID: "entry-control", ExperimentDir: dir}})
			if err != nil {
				t.Fatal(err)
			}
			rankGuard := anyMap(rank["current_acceptance"])["conservative-a"].(map[string]any)
			if !reflect.DeepEqual(rankGuard, guard) {
				t.Fatalf("cross-consumer guards differ:\n%v\n%v", rankGuard, guard)
			}
			if binary := os.Getenv("KTX_ENTRY_CONTRACT_BINARY"); binary != "" {
				out, err := entryCLI(t, dir, "show", "--current-cost")
				if err != nil {
					t.Fatalf("CLI: %v %s", err, out)
				}
				var envelope struct {
					View CurrentCostView `json:"current_cost_view"`
				}
				if err := json.Unmarshal(out, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.View.Acceptance["accepted"] != tc.accepted {
					t.Fatalf("CLI guard mismatch: %s", out)
				}
			}
		})
	}
}

func TestCurrentAcceptanceQualityAndExcludedPopulation(t *testing.T) {
	m, rows := pipelineCostGuardFixture()
	facts := []CostObservation{}
	for _, row := range rows {
		facts = append(facts, ReportedCostObservation(row.AttemptID, floatPointer(1), "stream"))
	}
	// These direct-evidence pairs remain the cost population even if incorrect.
	rows[1].FullPass = false
	rows[1].Score.Score = 0
	guard := currentAcceptance(m, rows, facts, nil)
	if guard["accepted"] != false || !strings.Contains(compactJSON(guard["reasons"]), "quality_not_preserved") {
		t.Fatalf("quality gate weakened: %v", guard)
	}
	before := guard["cost_selection"].(CostSelection).RequiredCount
	excluded := rows[0]
	excluded.AttemptID = "excluded"
	excluded.Role = ColdObservation
	excluded.Cycle = 100
	control := *excluded.CacheControl
	excluded.CacheControl = &control
	excluded.CacheControl.ExecutionOrder = 1000
	excluded.Cost = nil
	rows = append(rows, excluded)
	facts = append(facts, ReportedCostObservation("excluded", nil, "stream"))
	guard = currentAcceptance(m, rows, facts, nil)
	if guard["cost_selection"].(CostSelection).RequiredCount != before {
		t.Fatalf("setup injected into guarded cost: %v", guard)
	}
}

func TestCurrentRankingCLIEnvelope(t *testing.T) {
	if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") == "" {
		t.Skip("local binary check runs after mage build")
	}
	for _, reports := range [][2]string{{"2", "1"}, {"1", "2"}, {"missing", "missing"}} {
		dir := currentDecisionArchive(t, "entry-control", "prompt-cache-token-hit-rate", reports[0], reports[1], reports[0] != "missing", true)
		regen, err := RegenerateSummary(dir)
		if err != nil {
			t.Fatal(err)
		}
		pairs := []PairResult{{CandidatePipeline: "conservative-a", ExperimentID: "entry-control", ExperimentDir: dir, Summary: regen.Summary}}
		campaign := filepath.Join(filepath.Dir(dir), "offline-rank")
		if err := os.Mkdir(campaign, 0o700); err != nil {
			t.Fatal(err)
		}
		historical := BuildRanking(RankingOptions{BaselinePipeline: "passthrough"}, pairs, time.Unix(0, 0))
		historical["unknown_retained_field"] = "preserved"
		if err := WriteJSON(filepath.Join(campaign, "ranking.json"), historical); err != nil {
			t.Fatal(err)
		}
		before, _, err := directorySHA256(filepath.Dir(dir))
		if err != nil {
			t.Fatal(err)
		}
		out, err := entryCLI(t, campaign, "rank", "--current-cost")
		if err != nil {
			t.Fatalf("CLI: %v %s", err, out)
		}
		var envelope map[string]any
		if err := json.Unmarshal(out, &envelope); err != nil {
			t.Fatal(err)
		}
		if anyMap(envelope["historical_ranking"])["unknown_retained_field"] != "preserved" {
			t.Fatalf("raw history lost: %s", out)
		}
		view := anyMap(envelope["current_ranking"])
		guard := anyMap(anyMap(view["current_acceptance"])["conservative-a"])
		if guard["accepted"] != (reports[0] == "2") {
			t.Fatalf("CLI acceptance: %s", out)
		}
		after, _, err := directorySHA256(filepath.Dir(dir))
		if err != nil || before != after {
			t.Fatalf("rank inspection wrote evidence: %v", err)
		}
		if out, err := entryCLI(t, campaign, "rank", "--current-cost", "--model", "openai/test"); err == nil {
			t.Fatalf("inspection accepted execution flag: %s", out)
		}
		if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
			t.Fatal(err)
		}
		if out, err := entryCLI(t, campaign, "rank", "--current-cost"); err == nil {
			t.Fatalf("missing evidence accepted: %s", out)
		}
	}
}

func TestCurrentAcceptanceZeroEstimateAndUnion(t *testing.T) {
	m, attempts := pipelineCostGuardFixture()
	facts := []CostObservation{}
	for _, attempt := range attempts {
		fact := ReportedCostObservation(attempt.AttemptID, floatPointer(0), "stream")
		fact.estimated, fact.estimatedReasons, fact.estimateBasis = floatPointer(0), []string{}, "same-pricing"
		fact.pricing = m.Pricing
		facts = append(facts, fact)
	}
	guard := currentAcceptance(m, attempts, facts, nil)
	if guard["accepted"] != false || guard["comparison"] != "tie" || guard["cost_selection"].(CostSelection).Basis != "estimated_api" {
		t.Fatalf("zero invented advantage: %v", guard)
	}
	// One unavailable member in another declared guarded cohort disables cost
	// for every candidate without discarding direct evidence or quality.
	union := append(append([]CostObservation{}, facts...), ReportedCostObservation("other", nil, "other-stream"))
	common := SelectCostCohort(union)
	guard = currentAcceptance(m, attempts, facts, &common)
	if guard["accepted"] != false || guard["comparison"] != "unavailable" || anyMap(guard["quality"])["preserved"] != true {
		t.Fatalf("union allowed per-candidate fallback: %v", guard)
	}
}

func TestCurrentRankingRecoveryPrimaryUnchanged(t *testing.T) {
	dir := currentDecisionArchive(t, "entry-control", "recovery-aware-comparison", "1", "9", true, true)
	regen, err := RegenerateSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	pairs := []PairResult{{CandidatePipeline: "conservative-a", ExperimentID: "entry-control", ExperimentDir: dir, Summary: regen.Summary}}
	options := RankingOptions{BaselinePipeline: "passthrough"}
	historical := BuildRanking(options, pairs, time.Unix(0, 0))
	view, err := BuildCurrentRanking(options, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if view["primary_metric"] != "runtime_total_tokens_per_correct_task" || view["cost_winner"] != nil {
		t.Fatalf("cost replaced token primary: %v", view)
	}
	oldRows, newRows := historical["ranking"].([]map[string]any), view["ranking"].([]map[string]any)
	for i, row := range newRows {
		for _, key := range []string{"pipeline", "rank", "runtime_total_tokens_per_correct_task", "quality_gate", "recommendation_eligible"} {
			if !reflect.DeepEqual(row[key], oldRows[i][key]) {
				t.Fatalf("token primary %s changed: old=%v new=%v", key, oldRows[i], row)
			}
		}
	}
}
