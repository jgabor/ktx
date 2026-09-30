package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cost is wrapper-stream authority. The offline executor keeps real owned
// databases and matching collector identities while varying that source field.
type costViewExecutor struct {
	fakeExecutor
	costs            map[string]string
	identityConflict bool
	negativeUsage    bool
	wrongAnswer      bool
}

func (e *costViewExecutor) Run(ctx context.Context, command Command) (CommandResult, error) {
	writer := command.Stdout
	var stream bytes.Buffer
	command.Stdout = &stream
	result, err := e.fakeExecutor.Run(ctx, command)
	text := stream.String()
	if command.Args[0] == "docker" && contains(command.Args, "run") && !contains(command.Args, "--capabilities") {
		side := Baseline
		if strings.HasSuffix(mountSource(command.Args, "/home/node/.config/ktx/ktx.toml"), ".candidate") {
			side = Candidate
		}
		if cost, ok := e.costs[side]; ok {
			replacement := ""
			if cost != "missing" {
				replacement = `,"cost":` + cost
			}
			text = strings.ReplaceAll(text, `,"cost":0`, replacement)
		}
		if side == Candidate && e.identityConflict {
			text = strings.ReplaceAll(text, `"reason":"stop"`, `"reason":"stop","model":"conflicting-model"`)
		}
		if side == Candidate && e.negativeUsage {
			text = strings.ReplaceAll(text, `"input":40`, `"input":-1`)
		}
		if side == Candidate && e.wrongAnswer {
			text = strings.ReplaceAll(text, `"text":"done"`, `"text":"wrong"`)
		}
	}
	if writer != nil {
		_, writeErr := io.WriteString(writer, text)
		if err == nil {
			err = writeErr
		}
	}
	return result, err
}

func TestCurrentCostViewRetainedSourcesAndCLI(t *testing.T) {
	for _, tc := range []struct {
		name, baseline, candidate, basis, reason string
		priced, usage                            bool
	}{
		{"missing reports priced usage", "missing", "missing", "estimated_api", "reported_incomplete", true, true},
		{"uniform zero", "0", "0", "estimated_api", "reported_uniform_zero", true, true},
		{"mixed reports", "0", "2", "reported", "reported_complete_positive", true, true},
		{"reports without usage", "1", "2", "reported", "reported_complete_positive", false, false},
		{"missing unclaimed snapshot", "missing", "missing", "unavailable", "reported_incomplete", false, true},
		{"no usage no report", "missing", "missing", "unavailable", "reported_incomplete", true, false},
		{"partial report", "9", "missing", "estimated_api", "reported_incomplete", true, true},
		{"identity conflict estimate", "missing", "missing", "unavailable", "reported_incomplete", true, true},
		{"identity conflict positive report", "0", "2", "reported", "reported_complete_positive", true, true},
		{"negative usage estimate", "missing", "missing", "unavailable", "reported_incomplete", true, true},
		{"negative usage positive report", "1", "2", "reported", "reported_complete_positive", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &costViewExecutor{fakeExecutor: fakeExecutor{sessions: map[string]string{}, fullStepUsage: tc.usage}, costs: map[string]string{Baseline: tc.baseline, Candidate: tc.candidate}}
			e.identityConflict = strings.Contains(tc.name, "identity conflict")
			e.negativeUsage = strings.Contains(tc.name, "negative usage")
			dir := produceEntryArchive(t, "exact-match", tc.priced, tc.usage, e)
			before, count, err := directorySHA256(dir)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(filepath.Join(dir, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			view, err := LoadCurrentCostView(dir)
			if err != nil {
				t.Fatal(err)
			}
			s := view.CostSelection
			if s.Basis != tc.basis || s.Reason != tc.reason || s.RequiredCount != 2 || s.PolicyVersion != 1 || s.Unit != "USD" {
				t.Fatalf("selection=%+v", s)
			}
			for i, row := range s.Observations {
				if row.Source == "" || row.ID != view.SourceCosts[i].ID {
					t.Fatalf("lost provenance: %+v", view)
				}
				if tc.basis == "unavailable" && row.Value != nil {
					t.Fatalf("partial selected cost: %+v", s)
				}
				if tc.basis == "estimated_api" && (row.Value == nil || *row.Value <= 0 || row.Pricing == nil) {
					t.Fatalf("estimate=%+v", row)
				}
			}
			paired := view.SelectedCost["paired_baseline_minus_candidate"].(map[string]any)
			cycles := paired["cycles"].([]any)
			if len(cycles) != 1 {
				t.Fatalf("pairs=%+v", paired)
			}
			delta := cycles[0].(map[string]any)["deltas"].(map[string]any)["cost"]
			if tc.basis == "unavailable" {
				if delta != nil {
					t.Fatalf("unavailable acquired delta: %v", delta)
				}
			} else if delta != *s.Observations[0].Value-*s.Observations[1].Value {
				t.Fatalf("delta mixed sources: %v", delta)
			}
			var raw Summary
			if err := json.Unmarshal(original, &raw); err != nil {
				t.Fatal(err)
			}
			current := WithCurrentCost(raw, view)
			if _, ok := raw["cost_selection"]; ok {
				t.Fatal("view mutated original summary")
			}
			if current["pipelines"] == nil || !strings.Contains(RenderSummary(current), "basis="+tc.basis) {
				t.Fatal("lost historical fields or text selection")
			}
			// Copied cost, usage and score cannot supply the current view.
			attempts, err := LoadAttempts(dir)
			if err != nil {
				t.Fatal(err)
			}
			for i := range attempts {
				attempts[i].Cost, attempts[i].EstimatedAPICost, attempts[i].TokensIn = floatPointer(123), floatPointer(123), floatPointer(123)
				attempts[i].Score.Score = 0
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				for _, format := range []string{"json", "text"} {
					out, err := entryCLI(t, dir, "show", "--current-cost", "--format", format)
					if err != nil {
						t.Fatalf("show %s: %v %s", format, err, out)
					}
					if format == "json" {
						var envelope struct {
							Summary Summary         `json:"summary"`
							View    CurrentCostView `json:"current_cost_view"`
						}
						if err := json.Unmarshal(out, &envelope); err != nil || envelope.View.CostSelection.Basis != tc.basis {
							t.Fatalf("CLI view: %v %s", err, out)
						}
						rawBytes, _ := MarshalJSON(raw)
						gotBytes, _ := MarshalJSON(envelope.Summary)
						if !bytes.Equal(rawBytes, gotBytes) {
							t.Fatal("show changed historical summary fields")
						}
					} else if !strings.Contains(string(out), "basis="+tc.basis) || !strings.Contains(string(out), "required=2") || !strings.Contains(string(out), "source=") {
						t.Fatalf("text: %s", out)
					}
				}
			}
			after, afterCount, err := directorySHA256(dir)
			if err != nil || before != after || count != afterCount {
				t.Fatalf("inspection changed archive: %v", err)
			}
			writeAttemptRecords(t, dir, attempts...)
			driftView, err := LoadCurrentCostView(dir)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := MarshalJSON(view)
			got, _ := MarshalJSON(driftView)
			if !bytes.Equal(want, got) {
				t.Fatal("current view trusted copied metrics")
			}
			if _, err := RegenerateSummary(dir); err != nil {
				t.Fatal(err)
			}
			regenerated, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
			if !bytes.Equal(original, regenerated) {
				t.Fatal("historical regeneration bytes changed")
			}
		})
	}
}

func TestCurrentCostViewClaimedPricingCorruption(t *testing.T) {
	for _, kind := range []string{"missing", "hash", "identity"} {
		t.Run(kind, func(t *testing.T) {
			dir := produceEntryArchive(t, "exact-match", true, true)
			var m Manifest
			data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				if err := os.Remove(filepath.Join(dir, m.Pricing.Path)); err != nil {
					t.Fatal(err)
				}
			case "hash":
				m.Pricing.SHA256 = strings.Repeat("0", 64)
			case "identity":
				m.Pricing.Model = "conflict"
			}
			if err := WriteJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCurrentCostView(dir); err == nil {
				t.Fatal("claimed corruption became unavailable cost")
			}
			if _, err := ValidateExperiment(dir); err == nil {
				t.Fatal("legacy validator no longer rejects claimed corruption")
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				for _, format := range []string{"json", "text"} {
					out, err := entryCLI(t, dir, "show", "--current-cost", "--format", format)
					if err == nil || !strings.Contains(string(out), "pricing") {
						t.Fatalf("corrupt CLI: %v %s", err, out)
					}
				}
			}
		})
	}
}

func TestCurrentCostViewPhaseCoverageAndRejectedRecommendation(t *testing.T) {
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true, &fakeExecutor{sessions: map[string]string{}, fullStepUsage: true, candidateInput: 100})
	view, err := LoadCurrentCostView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if view.CostSelection.RequiredCount != 2 || view.CostSelection.Basis != "estimated_api" {
		t.Fatalf("headline=%+v", view.CostSelection)
	}
	for _, phase := range []string{ColdObservation, Warmup, MeasuredWarm} {
		if view.Phases[phase].CostSelection.RequiredCount != 2 {
			t.Fatalf("phase %s=%+v", phase, view.Phases[phase])
		}
	}
	before, _, err := directorySHA256(dir)
	if err != nil {
		t.Fatal(err)
	}
	var summary Summary
	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	current := WithCurrentCost(summary, view)
	if current["accepted"] != false {
		t.Fatalf("changed acceptance: %+v", current)
	}
	validation, err := ValidateExperiment(dir)
	if err != nil || validation["valid"] != true || validation["accepted"] != false {
		t.Fatalf("valid rejected artifact: %v %+v", err, validation)
	}
	if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
		out, err := entryCLI(t, dir, "validate")
		if err != nil {
			t.Fatalf("valid rejected CLI contract: %v %s", err, out)
		}
		out, err = entryCLI(t, dir, "summarize")
		if err != nil {
			t.Fatalf("summarize: %v %s", err, out)
		}
		var got Summary
		if err := json.Unmarshal(out, &got); err != nil || got["accepted"] != false || got["cost_selection"] == nil {
			t.Fatalf("summarize output: %v %s", err, out)
		}
	}
	after, _, err := directorySHA256(dir)
	if err != nil || before != after {
		t.Fatalf("view/regeneration changed archive: %v", err)
	}
}
