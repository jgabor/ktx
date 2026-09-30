package experiment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One source-generated prompt-cache pair is both the headline and guarded
// population. Multi-experiment unions and excluded phases have separate tests.
func TestCurrentCostSharedPopulationConformance(t *testing.T) {
	for _, tc := range []struct {
		name, baseline, candidate, basis, reason string
		priced, conflict, wrong, accepted        bool
		candidateInput                           int
	}{
		{"missing reported", "missing", "missing", "estimated_api", "reported_incomplete", true, false, false, true, 0},
		{"uniform zero", "0", "0", "estimated_api", "reported_uniform_zero", true, false, false, true, 0},
		{"mixed zero positive", "0", "2", "reported", "reported_complete_positive", true, false, false, false, 0},
		{"reported overrides opposite estimate", "2", "1", "reported", "reported_complete_positive", true, false, false, true, 200},
		{"price rates unavailable", "missing", "missing", "unavailable", "reported_incomplete", false, false, false, false, 0},
		{"source identity conflict", "missing", "missing", "unavailable", "reported_incomplete", true, true, false, false, 0},
		{"quality failure", "2", "1", "reported", "reported_complete_positive", true, false, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &costViewExecutor{
				fakeExecutor: fakeExecutor{sessions: map[string]string{}, fullStepUsage: true, candidateInput: tc.candidateInput},
				costs:        map[string]string{Baseline: tc.baseline, Candidate: tc.candidate}, identityConflict: tc.conflict, wrongAnswer: tc.wrong,
			}
			expectedExit := 0
			if tc.wrong {
				expectedExit = 1
			}
			dir := produceEntryArchiveIDExit(t, "entry-control", "prompt-cache-token-hit-rate", tc.priced, true, expectedExit, executor)
			validation, err := ValidateExperiment(dir)
			if err != nil || validation["summary_structural_valid"] != true || validation["summary_byte_match"] != true || validation["valid"] != !tc.wrong || validation["target_met"] != !tc.wrong {
				t.Fatalf("artifact validity is not cost or quality eligibility: %v %v", validation, err)
			}
			// The validator's top-level valid also requires target completion.
			// Its integrity checks remain valid when the quality target fails.
			for _, check := range validation["attempt_checks"].([]map[string]any) {
				if check["valid"] != true {
					t.Fatalf("quality failure corrupted attempt evidence: %v", check)
				}
			}
			view, err := LoadCurrentCostView(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := view.CostSelection
			if want.Basis != tc.basis || want.Reason != tc.reason || want.RequiredCount != 2 {
				t.Fatalf("headline population: %+v", want)
			}
			assertSelection := func(raw any) {
				t.Helper()
				data, err := json.Marshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				var got CostSelection
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				if got.Basis != want.Basis || got.Reason != want.Reason || got.RequiredCount != want.RequiredCount || got.ReportedEligible != want.ReportedEligible || got.EstimatedEligible != want.EstimatedEligible {
					t.Fatalf("same population selected differently: got=%+v want=%+v", got, want)
				}
				if got.Basis == "unavailable" {
					for _, observation := range got.Observations {
						if observation.Value != nil {
							t.Fatalf("unknown cost became numeric: %+v", observation)
						}
					}
				}
			}
			assertGuard := func(raw any) {
				t.Helper()
				guard := anyMap(raw)
				if tc.conflict {
					// Identity conflict excludes the direct-evidence pair, not the
					// evaluated headline. This is intentionally a different cohort.
					data, _ := json.Marshal(guard["cost_selection"])
					var selection CostSelection
					if err := json.Unmarshal(data, &selection); err != nil || selection.RequiredCount != 0 || selection.Basis != "unavailable" || selection.Reason != "empty_cohort" || guard["accepted"] != false {
						t.Fatalf("conflicting identity entered guarded population: %v %v", guard, err)
					}
					return
				}
				assertSelection(guard["cost_selection"])
				if guard["accepted"] != tc.accepted || anyMap(guard["quality"])["preserved"] != !tc.wrong {
					t.Fatalf("independent acceptance/quality: %v", guard)
				}
			}
			assertGuard(view.Acceptance)
			campaign, historical := currentRankingCampaign(t, dir, 0)
			if err := WriteJSON(filepath.Join(campaign, "ranking.json"), historical); err != nil {
				t.Fatal(err)
			}
			before, _, err := directorySHA256(filepath.Dir(dir))
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := LoadCurrentRanking(campaign)
			if err != nil {
				t.Fatal(err)
			}
			ranking := anyMap(envelope["current_ranking"])
			assertSelection(ranking["cost_selection"])
			if !tc.conflict {
				assertSelection(ranking["acceptance_cost_selection"])
			}
			assertGuard(anyMap(ranking["current_acceptance"])["conservative-a"])
			if tc.basis == "unavailable" {
				if ranking["cost_winner"] != nil || ranking["cost_order_available"] != false {
					t.Fatalf("unknown cost acquired a winner: %v", ranking)
				}
				for _, row := range ranking["ranking"].([]map[string]any) {
					if summaryValue(row["mean_selected_cost"]) != nil || summaryValue(row["selected_cost_per_correct"]) != nil || row["rank"] != nil || summaryValue(row["full_pass_rate"]) == nil {
						t.Fatalf("cost unavailable must retain quality and NULL scores: %v", row)
					}
				}
			}
			if tc.candidateInput != 0 && (view.SourceCosts[0].EstimatedAPICost == nil || view.SourceCosts[1].EstimatedAPICost == nil || *view.SourceCosts[0].EstimatedAPICost >= *view.SourceCosts[1].EstimatedAPICost) {
				t.Fatal("fixture did not produce the opposite estimated comparison")
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				for _, verb := range []string{"show", "summarize", "rank"} {
					for _, format := range []string{"json", "text"} {
						target, args := dir, []string{"--current-cost", "--format", format}
						switch verb {
						case "rank":
							target = campaign
						case "summarize":
							args = []string{"--format", format}
						}
						out, err := entryCLI(t, target, verb, args...)
						if err != nil {
							t.Fatalf("%s %s: %v %s", verb, format, err, out)
						}
						if format == "text" {
							basis, reason := "basis="+tc.basis, "reason="+tc.reason
							if verb == "rank" {
								if !strings.Contains(string(out), "Current ranking:") || strings.Contains(string(out), `"ranking":`) {
									t.Fatalf("rank text must be a readable current table: %s", out)
								}
							}
							if !strings.Contains(string(out), basis) || !strings.Contains(string(out), reason) {
								t.Fatalf("%s text selection: %s", verb, out)
							}
							continue
						}
						var got map[string]any
						if err := json.Unmarshal(out, &got); err != nil {
							t.Fatal(err)
						}
						switch verb {
						case "show":
							got = anyMap(got["current_cost_view"])
						case "rank":
							got = anyMap(got["current_ranking"])
						}
						assertSelection(got["cost_selection"])
						guard := got["current_acceptance"]
						if verb == "rank" {
							guard = anyMap(guard)["conservative-a"]
						}
						assertGuard(guard)
					}
				}
			}
			after, _, err := directorySHA256(filepath.Dir(dir))
			if err != nil || before != after {
				t.Fatalf("current inspection or historical regeneration changed retained bytes: %v", err)
			}
		})
	}
}
