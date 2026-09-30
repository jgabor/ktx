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

	"github.com/jgabor/ktx/internal/benchcontract"
)

func TestControlledPairNegativeUsageCancellation(t *testing.T) {
	for _, side := range []string{Baseline, Candidate} {
		t.Run(side, func(t *testing.T) {
			runner, o, fake := readDescriptionTestRunner(t)
			fake.extraModelStep = true
			o.EstimatedCostLimit = floatPointer(1)
			executor := runner.Executor
			runner.Executor = responseExecutor(func(ctx context.Context, cmd Command) (CommandResult, error) {
				if !contains(cmd.Args, "run") || contains(cmd.Args, "--capabilities") || dockerEnvironmentValue(cmd.Args, benchcontract.ReadDescriptionEnv) != side {
					return executor.Run(ctx, cmd)
				}
				var output bytes.Buffer
				forward := cmd.Stdout
				cmd.Stdout = &output
				result, err := executor.Run(ctx, cmd)
				if err != nil {
					return result, err
				}
				var stream bytes.Buffer
				decoder := json.NewDecoder(&output)
				step := 0
				for {
					var event map[string]any
					if err := decoder.Decode(&event); err == io.EOF {
						break
					} else if err != nil {
						t.Fatal(err)
					}
					if event["type"] == "step_finish" {
						value := 100
						if step == 1 {
							value = -100
						}
						event["part"].(map[string]any)["tokens"].(map[string]any)["input"] = value
						step++
					}
					if err := json.NewEncoder(&stream).Encode(event); err != nil {
						t.Fatal(err)
					}
				}
				if step != 2 {
					t.Fatalf("fixture has %d steps, want 2", step)
				}
				mount := mountSource(cmd.Args, benchcontract.ReadDescriptionDir)
				if err := os.WriteFile(filepath.Join(mount, "turn-1.jsonl"), stream.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(forward, &stream)
				return result, err
			})
			dir, _, code, err := runner.Compare(context.Background(), o)
			wantRuns := 1
			if side == Candidate {
				wantRuns = 2
			}
			attempts, loadErr := LoadAttempts(dir)
			if loadErr != nil || len(attempts) != fake.attemptRuns {
				t.Fatalf("retained attempts=%d runs=%d err=%v", len(attempts), fake.attemptRuns, loadErr)
			}
			bad := attempts[wantRuns-1]
			if bad.TokensIn == nil || *bad.TokensIn != 0 || bad.EstimatedAPICost == nil || *bad.EstimatedAPICost != 0 || len(bad.ModelSteps) != 2 || bad.ModelSteps[1].UncachedInput.Value == nil || *bad.ModelSteps[1].UncachedInput.Value != -100 {
				t.Fatal("fixture did not retain negative per-step usage cancelling to a valid zero aggregate")
			}
			t.Logf("side=%s retained input steps=100,-100 aggregate=%g estimate=%g runs=%d code=%d", side, *bad.TokensIn, *bad.EstimatedAPICost, fake.attemptRuns, code)
			if err != nil || code != 1 || fake.attemptRuns != wantRuns || len(attempts) != wantRuns {
				t.Fatalf("negative usage did not stop execution: runs=%d want=%d code=%d err=%v", fake.attemptRuns, wantRuns, code, err)
			}
			var stop ExecutionStop
			data, err := os.ReadFile(filepath.Join(dir, "execution-stop.json"))
			if err != nil || json.Unmarshal(data, &stop) != nil || stop.Reason != "estimated_cost_coverage_unknown" || stop.AfterAttemptID != bad.AttemptID {
				t.Fatalf("missing cost-coverage stop: %s %v", data, err)
			}
			if _, err := WriteSummary(dir); err != nil {
				t.Fatal(err)
			}
			if report, err := ValidateExperiment(dir); err != nil || report["valid"] != false || report["target_met"] != false || report["summary_byte_match"] != true {
				t.Fatalf("negative usage eligible as a complete pair: %v %v", report, err)
			}
			invokeReadDescriptionCLI(t, o.RepoRoot, true, "bench", "show", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, false, "bench", "validate", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, true, "bench", "summarize", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, false, "bench", "validate", o.ExperimentID)
		})
	}
}

func TestCompleteEstimatedCostPerStepFields(t *testing.T) {
	fields := []struct {
		name        string
		selectField func(*SourceEvent) *IntegerMeasurement
	}{
		{"uncached_input", func(s *SourceEvent) *IntegerMeasurement { return &s.UncachedInput }},
		{"output", func(s *SourceEvent) *IntegerMeasurement { return &s.Output }},
		{"reasoning", func(s *SourceEvent) *IntegerMeasurement { return &s.Reasoning }},
		{"cache_read", func(s *SourceEvent) *IntegerMeasurement { return &s.CacheRead }},
		{"cache_write", func(s *SourceEvent) *IntegerMeasurement { return &s.CacheWrite }},
	}
	for _, field := range fields {
		for _, tc := range []struct {
			name  string
			value IntegerMeasurement
			pass  bool
		}{
			{"measured-zero", MeasuredInteger(0), true},
			{"negative-cancellation", MeasuredInteger(-100), false},
			{"unknown", UnknownInteger(), false},
			{"malformed", MalformedInteger(), false},
			{"measured-null", IntegerMeasurement{State: MeasurementMeasured}, false},
		} {
			t.Run(field.name+"/"+tc.name, func(t *testing.T) {
				step := SourceEvent{Kind: ModelStep, UncachedInput: MeasuredInteger(0), Output: MeasuredInteger(0), Reasoning: MeasuredInteger(0), CacheRead: MeasuredInteger(0), CacheWrite: MeasuredInteger(0)}
				a := Attempt{
					AttemptID: "per-step-usage", Status: "completed",
					Metrics: Metrics{
						StreamComplete: true, EstimatedAPICost: floatPointer(0),
						TokensIn: floatPointer(0), TokensOut: floatPointer(0), Reasoning: floatPointer(0), CacheRead: floatPointer(0), CacheWrite: floatPointer(0),
						UsageEvidence: UsageEvidence{StepFinishEvents: 2, TokensInEvents: 2, TokensOutEvents: 2, ReasoningEvents: 2, CacheReadEvents: 2, CacheWriteEvents: 2},
						ModelSteps:    []SourceEvent{step, step},
					},
				}
				*field.selectField(&a.ModelSteps[1]) = tc.value
				if tc.name == "negative-cancellation" {
					*field.selectField(&a.ModelSteps[0]) = MeasuredInteger(100)
				}
				cost, reason := completeEstimatedCost([]Attempt{a})
				if cost != 0 || (reason == "") != tc.pass || (!tc.pass && !strings.Contains(reason, "model step 2 has invalid "+field.name)) {
					t.Fatalf("cost=%v reason=%q pass=%t", cost, reason, tc.pass)
				}
			})
		}
	}
}
