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

func TestReadDescriptionStopsOnEvidenceFailure(t *testing.T) {
	for _, mismatch := range []bool{true, false} {
		name := "incorrect-answer-does-not-stop"
		if mismatch {
			name = "projected-model-step-mismatch-stops"
		}
		t.Run(name, func(t *testing.T) {
			runner, options, fake := readDescriptionTestRunner(t)
			if mismatch {
				executor := runner.Executor
				runner.Executor = responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
					if command.Args[0] != "docker" || command.Args[1] != "run" || contains(command.Args, "--capabilities") {
						return executor.Run(ctx, command)
					}
					var stream bytes.Buffer
					forward := command.Stdout
					command.Stdout = &stream
					result, err := executor.Run(ctx, command)
					if err != nil {
						return result, err
					}
					// The wrapper claims another completed step; the collector's
					// original event ledger and counters are deliberately untouched.
					for _, line := range strings.Split(strings.TrimSpace(stream.String()), "\n") {
						var event map[string]any
						if err := json.Unmarshal([]byte(line), &event); err != nil {
							t.Fatal(err)
						}
						if event["type"] == "step_finish" {
							event["part"].(map[string]any)["id"] = "unprojected-step"
							if err := json.NewEncoder(&stream).Encode(event); err != nil {
								t.Fatal(err)
							}
							break
						}
					}
					mount := mountSource(command.Args, benchcontract.ReadDescriptionDir)
					if err := os.WriteFile(filepath.Join(mount, "turn-1.jsonl"), stream.Bytes(), 0o600); err != nil {
						t.Fatal(err)
					}
					_, err = io.Copy(forward, &stream)
					return result, err
				})
			} else {
				wrong := "not the runtime answer"
				options.Task.ExpectedOutput = &wrong
			}
			dir, _, code, err := runner.Compare(context.Background(), options)
			if !mismatch {
				if err != nil || code != 1 || fake.attemptRuns != 2 {
					t.Fatalf("incorrect answer stopped execution: attempts=%d code=%d err=%v", fake.attemptRuns, code, err)
				}
				return
			}
			const reason = "schema 3 wrapper and event-hook model-step counts do not match"
			if err == nil || !strings.Contains(err.Error(), reason) || code != 2 || fake.attemptRuns != 1 {
				t.Fatalf("evidence failure: attempts=%d code=%d err=%v", fake.attemptRuns, code, err)
			}
			attempts, err := LoadAttempts(dir)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("retained attempts=%d err=%v", len(attempts), err)
			}
			if attempts[0].UsageEvidence.StepFinishEvents != 2 {
				t.Fatal("projected model-step mismatch was repaired or discarded")
			}
			assertReadDescriptionInvalidArchive(t, dir, options, reason)
		})
	}
}

func TestReadDescriptionRetainsFailedFollowup(t *testing.T) {
	for _, unknownUsage := range []bool{false, true} {
		name := "failure-after-first-response"
		if unknownUsage {
			name = "followup-usage-unavailable"
		}
		t.Run(name, func(t *testing.T) {
			runner, options, fake := readDescriptionTestRunner(t)
			fake.incompleteFirst = true
			options.Task.FollowupPrompt = "synthetic follow-up"
			executor := runner.Executor
			runner.Executor = responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
				result, err := executor.Run(ctx, command)
				if err != nil || command.Args[0] != "docker" || command.Args[1] != "run" || contains(command.Args, "--capabilities") {
					return result, err
				}
				var second bytes.Buffer
				if unknownUsage {
					second.WriteString(`{"type":"step_finish","sessionID":"ses_runtime-baseline","part":{"sessionID":"ses_runtime-baseline","messageID":"second-message","id":"second-part","reason":"error","tokens":{}}}` + "\n")
				}
				second.WriteString(`{"type":"error","sessionID":"ses_runtime-baseline","error":{"message":"synthetic follow-up failed"}}` + "\n")
				mount := mountSource(command.Args, benchcontract.ReadDescriptionDir)
				if err := os.WriteFile(filepath.Join(mount, "turn-2.jsonl"), second.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(command.Stdout, &second)
				return CommandResult{ExitCode: 4}, err
			})
			dir, _, code, err := runner.Compare(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), "completion mismatch") || code != 2 || fake.attemptRuns != 1 {
				t.Fatalf("failed follow-up: attempts=%d code=%d err=%v", fake.attemptRuns, code, err)
			}
			attempts, err := LoadAttempts(dir)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("partial attempt not retained: count=%d err=%v", len(attempts), err)
			}
			attempt := attempts[0]
			if attempt.Status != "failed" || attempt.ProcessExitCode != 4 || attempt.SessionID == nil || attempt.RuntimeEventIntervalID == nil {
				t.Fatalf("lost failure/owner identity: %+v", attempt)
			}
			if err := validateAttemptDatabase(filepath.Join(dir, attempt.Artifacts["database"]), attempt.AttemptID, *attempt.SessionID, *attempt.RuntimeEventIntervalID); err == nil {
				t.Fatal("incomplete collector was made complete")
			}
			first, err := os.ReadFile(filepath.Join(dir, attempt.Artifacts["turn_1"]))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := RuntimeTurnSession(bytes.NewReader(first)); err != nil {
				t.Fatalf("first completed response lost: %v", err)
			}
			assertPartialReadDescriptionUsage(t, attempt, unknownUsage)
			materialized := assertReadDescriptionInvalidArchive(t, dir, options, "turn")
			assertPartialReadDescriptionUsage(t, materialized, unknownUsage)
		})
	}
}

func assertPartialReadDescriptionUsage(t *testing.T, attempt Attempt, unknown bool) {
	t.Helper()
	if len(attempt.ModelSteps) == 0 || attempt.ModelSteps[0].UncachedInput.Value == nil || *attempt.ModelSteps[0].UncachedInput.Value != 100 || attempt.ModelSteps[0].Cost.Value == nil {
		t.Fatal("first-turn incurred usage/cost evidence lost")
	}
	if unknown {
		if len(attempt.ModelSteps) != 2 || attempt.ModelSteps[1].Cost.State != MeasurementUnknown || attempt.ModelSteps[1].Cost.Value != nil || attempt.Cost != nil || attempt.EstimatedAPICost != nil {
			t.Fatal("unavailable follow-up usage/cost replaced by a known total")
		}
	} else if attempt.EstimatedAPICost == nil || *attempt.EstimatedAPICost <= 0 {
		t.Fatal("observed first-turn estimated cost lost")
	}
}

func assertReadDescriptionInvalidArchive(t *testing.T, dir string, options CompareOptions, reason string) Attempt {
	t.Helper()
	a, err := openArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	manifest, attempts, _, err := loadExperimentEvidence(a)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("cannot enumerate invalid incurred work: attempts=%d err=%v", len(attempts), err)
	}
	if !strings.Contains(strings.Join(attempts[0].validationReasons, "; "), reason) || AttemptIsFullPass(attempts[0], manifest.Task.MinimumToolCalls) {
		t.Fatalf("invalid evidence eligible or reason lost: %v", attempts[0].validationReasons)
	}
	pricing, err := a.preparePricingEvidence(manifest)
	if err != nil {
		t.Fatal(err)
	}
	attempts = estimateAttempts(attempts, pricing)
	retained := map[string][]byte{}
	for _, path := range []string{"attempts.jsonl", attempts[0].Artifacts["stream"], attempts[0].Artifacts["read_description"], attempts[0].Artifacts["turn_1"], attempts[0].Artifacts["turn_2"]} {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		retained[path] = data
	}
	summary, err := WriteSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	baseline, _ := summary.Pipeline(Baseline)
	if baseline["attempt_count"] != 1 || baseline["valid_count"] != 0 || baseline["evaluated_count"] != 0 || summaryValue(baseline["cost"]) != nil {
		t.Fatalf("invalid work counted as valid or free: %v", baseline)
	}
	report, err := ValidateExperiment(dir)
	if err != nil || report["valid"] != false || report["target_met"] != false {
		t.Fatalf("invalid archive passed: %v %v", report, err)
	}
	invokeReadDescriptionCLI(t, options.RepoRoot, true, "bench", "show", options.ExperimentID)
	invokeReadDescriptionCLI(t, options.RepoRoot, true, "bench", "summarize", options.ExperimentID)
	invokeReadDescriptionCLI(t, options.RepoRoot, false, "bench", "validate", options.ExperimentID)
	for path, before := range retained {
		after, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("regeneration changed partial evidence %s: %v", path, err)
		}
	}
	return attempts[0]
}

func TestReadDescriptionCorrectionPreservesUntreatedCompletionGuard(t *testing.T) {
	runner, options, fake := readDescriptionTestRunner(t)
	runner.Executor = fake
	fake.incompleteFirst = true
	options.ReadDescriptionTreatment, options.CacheControl = "", ""
	dir, _, code, err := runner.Compare(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "completion mismatch") || code != 2 || fake.attemptRuns != 1 {
		t.Fatalf("untreated guard changed: attempts=%d code=%d err=%v", fake.attemptRuns, code, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "attempts.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("untreated early-rejection behavior changed: %v", err)
	}
}
