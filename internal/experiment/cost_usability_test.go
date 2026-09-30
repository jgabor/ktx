package experiment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentRankingReadableSourceTable(t *testing.T) {
	for _, tc := range []struct {
		name, baseline, candidate, basis string
		priced                           bool
	}{
		{"reported", "2", "1", "reported", true},
		{"reported zero", "2", "0", "reported", true},
		{"fallback", "missing", "missing", "estimated_api", true},
		{"uniform zero fallback", "0", "0", "estimated_api", true},
		{"unavailable", "missing", "missing", "unavailable", false},
		{"tie", "1", "1", "reported", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := currentDecisionArchive(t, "readable", "exact-match", tc.baseline, tc.candidate, tc.priced, true)
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
			view := anyMap(envelope["current_ranking"])
			text := RenderCurrentRanking(envelope)
			for _, want := range []string{"Current ranking:", "basis=" + tc.basis, "coverage:", "source=", "pipeline", "evaluated", "correct", "mean USD", "USD/correct", "Historical ranking:"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q: %s", want, text)
				}
			}
			if strings.Contains(text, `"ranking":`) || strings.Contains(text, "<nil>") {
				t.Fatalf("raw JSON or nil: %s", text)
			}
			if (tc.name == "tie" || tc.basis == "unavailable") && !strings.Contains(text, "cost_winner=unavailable") {
				t.Fatal(text)
			}
			if tc.basis == "unavailable" && !strings.Contains(text, "cost_order_available=false") {
				t.Fatal(text)
			}
			historical["current_ranking"] = view
			if RenderRanking(historical) != text {
				t.Fatalf("live/offline text differs:\n%s\n%s", RenderRanking(historical), text)
			}
			data, _ := MarshalJSON(view)
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if RenderCurrentRanking(decoded) != RenderCurrentRanking(view) {
				t.Fatal("typed and decoded current views differ")
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				out, err := entryCLI(t, campaign, "rank", "--current-cost", "--format", "text")
				if err != nil || !bytes.Equal(out, []byte(text+"\n")) {
					t.Fatalf("built text: %v\n%s", err, out)
				}
				t.Logf("source-generated live ranking renderer and offline binary text match:\n%s", out)
			}
			after, _, err := directorySHA256(filepath.Dir(dir))
			if err != nil || before != after {
				t.Fatalf("inspection wrote archive: %v", err)
			}
		})
	}
}

func TestCurrentRankingTextNullZeroTokenPrimary(t *testing.T) {
	zero := 0.0
	view := map[string]any{
		"primary_metric": "runtime_total_tokens_per_correct_task", "cost_selection": CostSelection{Basis: "estimated_api"}, "cost_order_available": false, "cost_winner": nil,
		"ranking":            []map[string]any{{"pipeline": "zero", "rank": 1, "mean_selected_cost": &zero, "selected_cost_per_correct": &zero, "exit_code": nil, "runtime_total_tokens_per_correct_task": map[string]any{"value": &zero, "state": "measured", "reason": "fixture"}}, {"pipeline": "unknown", "rank": nil}},
		"current_acceptance": map[string]any{"zero": map[string]any{"accepted": false, "reasons": []string{"selected_cost_unavailable"}}},
	}
	text := RenderCurrentRanking(view)
	for _, want := range []string{"Token-primary order is independent of cost", "cost_order_available=false cost_winner=unavailable", "token primary: 0", "current acceptance pipeline=zero accepted=false reasons=selected_cost_unavailable", "unavailable"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
	if currentTextNumber(&zero) != "0" || currentTextNumber(nil) != "unavailable" || currentTextValue(nil) != "unavailable" {
		t.Fatal("NULL and measured zero conflated")
	}
}

func TestCurrentRankingReadableGuardAndTokenPrimary(t *testing.T) {
	for _, spec := range []string{"prompt-cache-token-hit-rate", "recovery-aware-comparison"} {
		t.Run(spec, func(t *testing.T) {
			dir := currentDecisionArchive(t, "readable-guard", spec, "1", "2", true, true)
			campaign, historical := currentRankingCampaign(t, dir, 0)
			if err := WriteJSON(filepath.Join(campaign, "ranking.json"), historical); err != nil {
				t.Fatal(err)
			}
			envelope, err := LoadCurrentRanking(campaign)
			if err != nil {
				t.Fatal(err)
			}
			text := RenderCurrentRanking(envelope)
			if strings.Contains(text, "<nil>") {
				t.Fatalf("unknown token-primary fields must be unavailable: %s", text)
			}
			wants := []string{"Token-primary order is independent of cost", "cost_winner=unavailable", "token primary:"}
			if spec == "prompt-cache-token-hit-rate" {
				wants = []string{"Acceptance cost cohort: basis=reported", "current acceptance pipeline=conservative-a accepted=false", "historical acceptance pipeline=conservative-a accepted=true"}
			}
			for _, want := range wants {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q: %s", want, text)
				}
			}
			historical["current_ranking"] = envelope["current_ranking"]
			if RenderRanking(historical) != text {
				t.Fatal("live/offline text differs")
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				out, err := entryCLI(t, campaign, "rank", "--current-cost", "--format", "text")
				if err != nil || !bytes.Equal(out, []byte(text+"\n")) {
					t.Fatalf("built guard/token text: %v\n%s", err, out)
				}
				t.Logf("source-generated live ranking renderer and offline binary text match:\n%s", out)
			}
		})
	}
}

func TestSummaryHistoricalCostLabels(t *testing.T) {
	dir := currentDecisionArchive(t, "acceptance-text", "prompt-cache-token-hit-rate", "1", "2", true, true)
	regen, err := RegenerateSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	view, err := LoadCurrentCostView(dir)
	if err != nil {
		t.Fatal(err)
	}
	text := RenderSummary(WithCurrentCost(regen.Summary, view))
	for _, want := range []string{"Current selected costs (USD):", "Historical summary:", "historical pipeline_cost_guard accepted=", "current acceptance pipeline=candidate accepted=false"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
	if strings.Index(text, "Current selected costs") > strings.Index(text, "Historical summary") {
		t.Fatal("historical costs precede current costs")
	}
	if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
		before, _, err := directorySHA256(dir)
		if err != nil {
			t.Fatal(err)
		}
		out, err := entryCLI(t, dir, "summarize", "--format", "text")
		if err != nil || !bytes.Equal(out, []byte(text+"\n")) {
			t.Fatalf("built summary text: %v\n%s", err, out)
		}
		after, _, err := directorySHA256(dir)
		if err != nil || before != after {
			t.Fatalf("regeneration changed retained bytes: %v", err)
		}
		t.Logf("source-generated compare summary renderer and binary summarize text match:\n%s", out)
	}
}
