package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
)

func TestControlledPairOrder(t *testing.T) {
	for _, first := range []string{"", Baseline, Candidate} {
		t.Run("first-"+first, func(t *testing.T) {
			runner, o, fake := readDescriptionTestRunner(t)
			o.FirstArm = first
			if first != "" {
				o.EstimatedCostLimit = floatPointer(1)
			}
			dir, _, code, err := runner.Compare(context.Background(), o)
			if err != nil || code != 0 || fake.attemptRuns != 2 {
				t.Fatalf("runs=%d code=%d err=%v", fake.attemptRuns, code, err)
			}
			want, _ := pairOrder(first)
			if !reflect.DeepEqual(fake.sides, want) {
				t.Fatalf("actual launch order=%v want=%v", fake.sides, want)
			}
			attempts, err := LoadAttempts(dir)
			if err != nil {
				t.Fatal(err)
			}
			for i, a := range attempts {
				if a.Side != want[i] || a.Role != "" || a.Cycle != 1 || a.Ordinal != 1 || a.ReadDescriptionTreatment != benchcontract.ReadDescriptionTreatment || a.RuntimeConfigSHA256 != attempts[0].RuntimeConfigSHA256 || a.ConfigSHA256 != attempts[0].ConfigSHA256 {
					t.Fatalf("arm semantics changed: %+v", a)
				}
				if a.FirstArm != first || (first != "" && a.ExecutionOrder != i+1) || (first == "" && a.ExecutionOrder != 0) {
					t.Fatal("requested/executed order provenance lost")
				}
				if dockerEnvironmentValue(fake.runArgs[i], benchcontract.ReadDescriptionEnv) != want[i] {
					t.Fatal("treatment label swapped")
				}
				hooks, err := os.ReadFile(filepath.Join(dir, a.Artifacts["read_description"]))
				if err != nil {
					t.Fatal(err)
				}
				readSeen := false
				for _, line := range strings.Split(strings.TrimSpace(string(hooks)), "\n") {
					var hook readDefinitionObservation
					if err := json.Unmarshal([]byte(line), &hook); err != nil {
						t.Fatal(err)
					}
					if hook.Tool == "read" {
						readSeen = true
						wantHash := benchcontract.ReadDescriptionOriginalSHA256
						if a.Side == Candidate {
							wantHash = benchcontract.ReadDescriptionCandidateSHA256
						}
						if hook.Side != a.Side || hook.BeforeSHA256 != benchcontract.ReadDescriptionOriginalSHA256 || hook.AfterSHA256 != wantHash {
							t.Fatal("hook identities changed with launch order")
						}
					}
				}
				if !readSeen {
					t.Fatal("missing actual hook provenance")
				}
			}
			if _, err := RequireValidExperiment(dir); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
			if _, err := WriteSummary(dir); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
			if string(before) != string(after) {
				t.Fatal("regeneration changed summary bytes")
			}
			invokeReadDescriptionCLI(t, o.RepoRoot, true, "bench", "show", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, true, "bench", "validate", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, true, "bench", "summarize", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, true, "bench", "validate", o.ExperimentID)
			if first == Candidate {
				var m Manifest
				data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
				if err := json.Unmarshal(data, &m); err != nil {
					t.Fatal(err)
				}
				m.PipelineExecutionPolicy["first_cycle"] = []string{Baseline, Candidate}
				if err := WriteJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
					t.Fatal(err)
				}
				if _, err := WriteSummary(dir); err == nil || !strings.Contains(err.Error(), "requested first arm") {
					t.Fatalf("source configuration order tamper accepted: %v", err)
				}
				if _, err := ValidateExperiment(dir); err == nil {
					t.Fatal("validation accepted policy tamper")
				}
				invokeReadDescriptionCLI(t, o.RepoRoot, false, "bench", "validate", o.ExperimentID)
				invokeReadDescriptionCLI(t, o.RepoRoot, false, "bench", "summarize", o.ExperimentID)
			}
		})
	}
}

func TestControlledPairCostCheckpoint(t *testing.T) {
	for _, name := range []string{"above-limit", "at-limit", "unknown-usage", "known-failed-subtotal", "under-limit", "final-overshoot", "wrong-answer", "no-option-unknown"} {
		t.Run(name, func(t *testing.T) {
			runner, o, fake := readDescriptionTestRunner(t)
			o.EstimatedCostLimit = floatPointer(1)
			wantRuns, wantCode, wantReason := 1, 1, "estimated_cost_limit_reached"
			switch name {
			case "above-limit":
				o.EstimatedCostLimit = floatPointer(0.00009)
			case "at-limit":
				o.EstimatedCostLimit = floatPointer(0.0001)
			case "unknown-usage":
				fake.fullStepUsage = false
				wantReason = "estimated_cost_coverage_unknown"
			case "known-failed-subtotal":
				wantReason = "estimated_cost_coverage_unknown"
				executor := runner.Executor
				runner.Executor = responseExecutor(func(ctx context.Context, cmd Command) (CommandResult, error) {
					r, err := executor.Run(ctx, cmd)
					if cmd.Args[0] == "docker" && cmd.Args[1] == "run" && !contains(cmd.Args, "--capabilities") {
						r.ExitCode = 1
					}
					return r, err
				})
			case "under-limit":
				wantRuns, wantCode, wantReason = 2, 0, ""
			case "final-overshoot":
				o.EstimatedCostLimit = floatPointer(0.00012)
				wantRuns, wantCode = 2, 0
			case "wrong-answer":
				o.Task.ExpectedOutput = stringPointer("wrong")
				wantRuns, wantCode, wantReason = 2, 1, ""
			case "no-option-unknown":
				o.EstimatedCostLimit = nil
				fake.fullStepUsage = false
				wantRuns, wantCode, wantReason = 2, 0, ""
			}
			dir, _, code, err := runner.Compare(context.Background(), o)
			if err != nil || code != wantCode || fake.attemptRuns != wantRuns {
				t.Fatalf("runs=%d code=%d err=%v", fake.attemptRuns, code, err)
			}
			attempts, err := LoadAttempts(dir)
			if err != nil || len(attempts) != wantRuns {
				t.Fatalf("retained attempts=%d err=%v", len(attempts), err)
			}
			if attempts[0].Cost == nil || *attempts[0].Cost != 0 {
				t.Fatal("fixture must report zero USD independently of the estimate")
			}
			if name == "known-failed-subtotal" && (attempts[0].EstimatedAPICost == nil || *attempts[0].EstimatedAPICost <= 0) {
				t.Fatal("known partial cost discarded")
			}
			if wantReason == "" {
				if _, err := os.Stat(filepath.Join(dir, "execution-stop.json")); !os.IsNotExist(err) {
					t.Fatal("unexpected stop receipt")
				}
				return
			}
			var receipt ExecutionStop
			data, err := os.ReadFile(filepath.Join(dir, "execution-stop.json"))
			if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.Reason != wantReason || receipt.AfterAttemptID != attempts[len(attempts)-1].AttemptID {
				t.Fatalf("stop receipt=%s err=%v", data, err)
			}
			report, err := ValidateExperiment(dir)
			completePair := name == "final-overshoot"
			if err != nil || report["valid"] != completePair || report["target_met"] != completePair || report["summary_byte_match"] != true {
				t.Fatalf("stopped counterpart fabricated: %v %v", report, err)
			}
			invokeReadDescriptionCLI(t, o.RepoRoot, true, "bench", "show", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, completePair, "bench", "validate", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, true, "bench", "summarize", o.ExperimentID)
			invokeReadDescriptionCLI(t, o.RepoRoot, completePair, "bench", "validate", o.ExperimentID)
		})
	}
}

func TestExecutionControlValidation(t *testing.T) {
	for _, value := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		o := readDescriptionTestOptions(t)
		o.EstimatedCostLimit = &value
		if err := validateExecutionControlOptions(o); err == nil || !strings.Contains(err.Error(), "finite and positive") {
			t.Fatalf("accepted limit %v: %v", value, err)
		}
	}
	for _, name := range []string{"unsupported-first", "without-treatment", "missing-pin", "missing-runtime"} {
		t.Run(name, func(t *testing.T) {
			runner, o, fake := readDescriptionTestRunner(t)
			o.FirstArm, o.EstimatedCostLimit = Candidate, floatPointer(1)
			switch name {
			case "unsupported-first":
				o.FirstArm = "random"
			case "without-treatment":
				o.ReadDescriptionTreatment = ""
			case "missing-pin":
				o.PricingSHA256 = ""
			case "missing-runtime":
				o.RuntimeConfig = ""
			}
			if _, _, _, err := runner.Compare(context.Background(), o); err == nil || fake.attemptRuns != 0 || fake.capabilityRuns != 0 {
				t.Fatalf("invalid controls reached executor: %v", err)
			}
		})
	}
}

func TestControlledPairExecutedOrderTamper(t *testing.T) {
	runner, o, _ := readDescriptionTestRunner(t)
	o.FirstArm = Candidate
	dir, _, _, err := runner.Compare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A conflicting duplicate cannot replace the first orchestration envelope.
	attempts[0].ExecutionOrder = 2
	if err := AppendAttempt(dir, attempts[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(dir); err == nil || !strings.Contains(err.Error(), "conflicting authoritative duplicate") {
		t.Fatalf("executed order tamper accepted: %v", err)
	}
}

func TestControlledPairRetainedStopBoundary(t *testing.T) {
	for _, name := range []string{"budget-prefix", "requested-order", "executed-order"} {
		t.Run(name, func(t *testing.T) {
			runner, o, _ := readDescriptionTestRunner(t)
			o.FirstArm, o.EstimatedCostLimit = Baseline, floatPointer(1)
			dir, _, _, err := runner.Compare(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if name == "executed-order" {
				attempts, err := LoadAttempts(dir)
				if err != nil {
					t.Fatal(err)
				}
				attempts[0].ExecutionOrder = 2
				var data bytes.Buffer
				for _, a := range attempts {
					if err := json.NewEncoder(&data).Encode(a); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(dir, "attempts.jsonl"), data.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				var m Manifest
				data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
				if err := json.Unmarshal(data, &m); err != nil {
					t.Fatal(err)
				}
				if name == "budget-prefix" {
					m.EstimatedCostLimit = floatPointer(0.0001)
				} else {
					m.FirstArm = Candidate
					first, second := pairOrder(Candidate)
					m.PipelineExecutionPolicy["first_cycle"], m.PipelineExecutionPolicy["second_cycle"] = first, second
				}
				if err := WriteJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := WriteSummary(dir); err == nil {
				t.Fatal("regeneration accepted a violated launch boundary")
			}
			if _, err := ValidateExperiment(dir); err == nil {
				t.Fatal("validation accepted a violated launch boundary")
			}
		})
	}
}

func TestControlledPairCheckpointRequiresAllStartedWork(t *testing.T) {
	runner, o, _ := readDescriptionTestRunner(t)
	o.EstimatedCostLimit = floatPointer(1)
	dir, _, _, err := runner.Compare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "attempts.jsonl"), filepath.Join(dir, "test-retained-attempts.jsonl")); err != nil {
		t.Fatal(err)
	}
	stop, err := runner.costCheckpoint(dir, m, 2)
	if err == nil || stop == nil || stop.Reason != "evidence_invalid" || !strings.Contains(err.Error(), "does not cover 2 started attempts") {
		t.Fatalf("missing ledger treated as zero work: stop=%+v err=%v", stop, err)
	}
}

func TestControlledPairFinalProjectionRemainsInvalid(t *testing.T) {
	runner, o, _ := readDescriptionTestRunner(t)
	o.EstimatedCostLimit = floatPointer(1)
	dir, _, _, err := runner.Compare(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The final arm has a copied subtotal that differs from its retained stream.
	attempts[1].TokensIn = floatPointer(999)
	var ledger bytes.Buffer
	for _, a := range attempts {
		if err := json.NewEncoder(&ledger).Encode(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "attempts.jsonl"), ledger.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}
	report, err := ValidateExperiment(dir)
	if err != nil || report["valid"] != false || report["target_met"] != false || report["summary_byte_match"] != true {
		t.Fatalf("final projection mismatch repaired into a valid pair: report=%v err=%v", report, err)
	}
}

func TestControlledPairEvidenceFailureRetainsStop(t *testing.T) {
	runner, o, fake := readDescriptionTestRunner(t)
	o.EstimatedCostLimit = floatPointer(1)
	fake.incompleteFirst = true
	dir, _, code, err := runner.Compare(context.Background(), o)
	if err == nil || code != 2 || fake.attemptRuns != 1 {
		t.Fatalf("incomplete evidence did not stop: code=%d runs=%d err=%v", code, fake.attemptRuns, err)
	}
	attempts, err := LoadAttempts(dir)
	if err != nil || len(attempts) != 1 || attempts[0].EstimatedAPICost == nil || *attempts[0].EstimatedAPICost <= 0 {
		t.Fatalf("incurred estimate lost: attempts=%v err=%v", attempts, err)
	}
	var receipt ExecutionStop
	data, err := os.ReadFile(filepath.Join(dir, "execution-stop.json"))
	if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.Reason != "evidence_invalid" || receipt.AfterAttemptID != attempts[0].AttemptID || receipt.Detail == "" {
		t.Fatalf("evidence stop receipt=%s err=%v", data, err)
	}
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}
	if report, err := ValidateExperiment(dir); err != nil || report["valid"] != false || report["target_met"] != false {
		t.Fatalf("incomplete evidence repaired: %v %v", report, err)
	}
}
