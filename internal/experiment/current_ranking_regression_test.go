package experiment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func failCurrentRankingCandidate(t *testing.T, dir string) {
	t.Helper()
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attempts.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.Side == Candidate {
			attempt.Status, attempt.ProcessExitCode = "failed", 1
		}
		if err := AppendAttempt(dir, attempt); err != nil {
			t.Fatal(err)
		}
	}
}

func currentRankingCampaign(t *testing.T, dir string, exit int) (string, map[string]any) {
	t.Helper()
	regen, err := RegenerateSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	pairs := []PairResult{{CandidatePipeline: "conservative-a", ExperimentID: filepath.Base(dir), ExperimentDir: dir, ExitCode: exit, Summary: regen.Summary}}
	historical := BuildRanking(RankingOptions{BaselinePipeline: "passthrough", Candidates: []string{"conservative-a"}}, pairs, time.Unix(0, 0))
	campaign := filepath.Join(filepath.Dir(dir), "offline-regression")
	if err := os.Mkdir(campaign, 0o700); err != nil {
		t.Fatal(err)
	}
	return campaign, historical
}

func TestCurrentRankingDeclaredRowComparability(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "evaluated", true: "all candidate work failed"}[failed], func(t *testing.T) {
			dir := currentDecisionArchive(t, "row-coverage", "exact-match", "2", "1", true, true)
			if failed {
				failCurrentRankingCandidate(t, dir)
			}
			campaign, historical := currentRankingCampaign(t, dir, 1)
			if err := WriteJSON(filepath.Join(campaign, "ranking.json"), historical); err != nil {
				t.Fatal(err)
			}
			envelope, err := LoadCurrentRanking(campaign)
			if err != nil {
				t.Fatal(err)
			}
			view := anyMap(envelope["current_ranking"])
			if view["cost_selection"].(CostSelection).Basis != "reported" {
				t.Fatalf("unexpected source selection: %v", view)
			}
			if view["cost_order_available"] != !failed {
				t.Fatalf("cohort comparability: %v", view)
			}
			if failed && view["cost_winner"] != nil {
				t.Fatalf("missing row crowned winner: %v", view)
			}
			if !failed && view["cost_winner"] != "conservative-a" {
				t.Fatalf("comparable winner withheld: %v", view)
			}
			for _, row := range view["ranking"].([]map[string]any) {
				if row["side"] != Candidate {
					continue
				}
				if failed && (row["evaluated_count"] != 0 || summaryValue(row["selected_cost_per_correct"]) != nil || row["rank"] != nil || row["recommendation_eligible"] != false) {
					t.Fatalf("failed row not retained honestly: %v", row)
				}
				if !failed && (summaryValue(row["selected_cost_per_correct"]) == nil || row["rank"] == nil) {
					t.Fatalf("usable row lost cost rank: %v", row)
				}
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				out, err := entryCLI(t, campaign, "rank", "--current-cost")
				if err != nil {
					t.Fatalf("CLI query: %v %s", err, out)
				}
				var got map[string]any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatal(err)
				}
				if anyMap(got["current_ranking"])["cost_order_available"] != !failed {
					t.Fatalf("CLI comparison: %s", out)
				}
				t.Logf("rank query exit=0, failed_candidate=%t, cost_order_available=%t", failed, !failed)
			}
		})
	}
}

func TestCurrentRankingSourcePipelineLabels(t *testing.T) {
	for _, field := range []string{"matching", "candidate", "baseline"} {
		t.Run(field, func(t *testing.T) {
			dir := currentDecisionArchive(t, "source-labels", "exact-match", "2", "1", true, true)
			campaign, historical := currentRankingCampaign(t, dir, 0)
			if field == "candidate" {
				historical["pairs"].([]any)[0].(map[string]any)["candidate_pipeline"] = "never-executed"
			}
			if field == "baseline" {
				historical["baseline_pipeline"] = "never-executed"
			}
			// Lists and historical ranking rows are retained raw, not current identities.
			historical["candidate_pipelines"] = []string{"copied-list-label"}
			for _, row := range historical["ranking"].([]map[string]any) {
				row["pipeline"] = "copied-row-label"
			}
			if err := WriteJSON(filepath.Join(campaign, "ranking.json"), historical); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(campaign, "ranking.json"))
			if err != nil {
				t.Fatal(err)
			}
			before, _, err := directorySHA256(filepath.Dir(dir))
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := LoadCurrentRanking(campaign)
			if field != "matching" {
				if err == nil {
					t.Fatalf("source mismatch accepted: %v", envelope)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(envelope["historical_ranking"].(json.RawMessage), raw) {
					t.Fatal("raw historical envelope changed")
				}
				if anyMap(envelope["current_ranking"])["cost_winner"] != "conservative-a" {
					t.Fatalf("copied alias became current identity: %v", envelope)
				}
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				out, err := entryCLI(t, campaign, "rank", "--current-cost")
				if (err == nil) != (field == "matching") {
					t.Fatalf("CLI mismatch handling: %v %s", err, out)
				}
				t.Logf("CLI matching_source_labels=%t, query_error=%v", field == "matching", err)
			}
			after, _, err := directorySHA256(filepath.Dir(dir))
			if err != nil || before != after {
				t.Fatalf("label inspection changed evidence: %v", err)
			}
		})
	}
	// The shared builder validates every comparison, not just the first baseline.
	dirs := []string{currentDecisionArchive(t, "label-first", "exact-match", "2", "1", true, true), currentDecisionArchive(t, "label-second", "exact-match", "2", "1", true, true)}
	pairs := []PairResult{{CandidatePipeline: "conservative-a", ExperimentID: "label-first", ExperimentDir: dirs[0]}, {CandidatePipeline: "never-executed", ExperimentID: "label-second", ExperimentDir: dirs[1]}}
	if _, err := BuildCurrentRanking(RankingOptions{BaselinePipeline: "passthrough"}, pairs); err == nil {
		t.Fatal("second comparison label mismatch accepted")
	}
	if _, err := BuildCurrentRanking(RankingOptions{BaselinePipeline: "never-executed"}, pairs[:1]); err == nil {
		t.Fatal("baseline label mismatch accepted")
	}
}

func TestCurrentRankingRetainedExitDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name             string
		value            any
		missing, invalid bool
	}{
		{"zero", 0, false, false},
		{"failed run", 1, false, false},
		{"missing", nil, true, false},
		{"null", nil, false, false},
		{"fraction", 1.5, false, true},
		{"string", "1", false, true},
		{"boolean", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := currentDecisionArchive(t, "exit-diagnostics", "exact-match", "2", "1", true, true)
			if tc.name == "failed run" {
				failCurrentRankingCandidate(t, dir)
			}
			campaign, historical := currentRankingCampaign(t, dir, 0)
			ref := historical["pairs"].([]any)[0].(map[string]any)
			if tc.missing {
				delete(ref, "exit_code")
			} else {
				ref["exit_code"] = tc.value
			}
			if err := WriteJSON(filepath.Join(campaign, "ranking.json"), historical); err != nil {
				t.Fatal(err)
			}
			envelope, err := LoadCurrentRanking(campaign)
			if tc.invalid {
				if err == nil {
					t.Fatalf("invalid exit diagnostic accepted: %v", envelope)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "failed run" {
				validation, err := ValidateExperiment(dir)
				if err != nil || validation["valid"] != false || validation["target_met"] != false || validation["summary_structural_valid"] != true || validation["summary_byte_match"] != true {
					t.Fatalf("failed run evidence/target distinction changed: %v %v", validation, err)
				}
				for _, check := range validation["attempt_checks"].([]map[string]any) {
					if check["valid"] != true {
						t.Fatalf("failed target lost valid attempt evidence: %v", validation)
					}
				}
			}
			for _, row := range anyMap(envelope["current_ranking"])["ranking"].([]map[string]any) {
				if row["side"] == Candidate && !reflect.DeepEqual(row["exit_code"], tc.value) {
					t.Fatalf("retained diagnostic changed: %v, want %v", row, tc.value)
				}
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				out, err := entryCLI(t, campaign, "rank", "--current-cost")
				if err != nil {
					t.Fatalf("query conflated with run exit: %v %s", err, out)
				}
				var got map[string]any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatal(err)
				}
				for _, raw := range anyMap(got["current_ranking"])["ranking"].([]any) {
					row := anyMap(raw)
					if row["side"] != Candidate {
						continue
					}
					want := tc.value
					if value, ok := want.(int); ok {
						want = float64(value)
					}
					if !reflect.DeepEqual(row["exit_code"], want) {
						t.Fatalf("CLI retained diagnostic changed: %s", out)
					}
				}
				t.Logf("rank query exit=0, retained exit_code=%v", tc.value)
			}
		})
	}
}
