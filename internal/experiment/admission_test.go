package experiment

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type admissionExecutor struct {
	base            *fakeExecutor
	wrongPairCycles bool
	cancel          context.CancelFunc
	runFailure      bool
}

func (f *admissionExecutor) Run(ctx context.Context, command Command) (CommandResult, error) {
	paid := len(command.Args) > 1 && command.Args[1] == "run" && !contains(command.Args, "--capabilities")
	if paid && f.cancel != nil {
		f.cancel()
		return CommandResult{ExitCode: 130}, ctx.Err()
	}
	if paid && f.runFailure {
		return CommandResult{ExitCode: 127}, errors.New("synthetic launch failure")
	}
	if paid && f.wrongPairCycles {
		var stream bytes.Buffer
		out := command.Stdout
		command.Stdout = &stream
		result, err := f.base.Run(ctx, command)
		data := stream.String()
		if f.base.attemptRuns == 5 || f.base.attemptRuns == 7 {
			data = strings.ReplaceAll(data, `"text":"done"`, `"text":"wrong"`)
		}
		if _, writeErr := io.WriteString(out, data); writeErr != nil {
			return result, writeErr
		}
		return result, err
	}
	return f.base.Run(ctx, command)
}

func admissionFixture(t *testing.T) (*Runner, *admissionExecutor, CompareOptions) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	t.Setenv("KTX_DB", "")
	write := func(name, data string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	method := write("method.yaml", "methodology_version: v7-recovery-aware-comparison\n")
	baseline := write("baseline.toml", "[pipeline.default]\nstages=[]\n")
	candidate := write("candidate.toml", "[pipeline.default]\nstages=['head:1']\n")
	auth := write("auth.json", "{}")
	fake := &admissionExecutor{base: &fakeExecutor{sessions: map[string]string{}, fullStepUsage: true}}
	runner := &Runner{Executor: fake, Now: time.Now, PID: 123, ResolvePrice: testPriceResolver}
	expected := "done"
	return runner, fake, CompareOptions{
		Model: "openai/test", Task: Task{ID: "controlled", Prompt: "controlled task", ExpectedOutput: &expected, MinimumToolCalls: 1, WorkspaceMode: "repo"},
		TargetValid: 5, MaxAttempts: 24, Warmup: 1, WarmupSet: true, CacheControl: "observed", ExperimentID: "admission", DataDir: filepath.Join(root, "data"),
		Image: "image", AuthPath: auth, RepoRoot: root, MethodologyPath: method, BaselineConfig: baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	}
}

func TestRunnerOptionalCompletePairScheduling(t *testing.T) {
	for _, paired := range []bool{false, true} {
		t.Run(map[bool]string{false: "public-default", true: "complete-pairs"}[paired], func(t *testing.T) {
			runner, fake, options := admissionFixture(t)
			fake.wrongPairCycles = true
			if paired {
				options.MinimumCompletePairs = 5
			}
			dir, summary, code, err := runner.Compare(context.Background(), options)
			if err != nil || code != 0 {
				t.Fatalf("compare: %d %v", code, err)
			}
			comparison := summary["recovery_aware_comparison"].(map[string]any)
			pairs := 0
			for _, row := range comparison["paired_cycles"].([]any) {
				pair := row.(map[string]any)
				if pair[Baseline].(map[string]any)["correct"] == true && pair[Candidate].(map[string]any)["correct"] == true {
					pairs++
				}
			}
			if paired && pairs < 5 {
				t.Fatalf("five per arm substituted for paired cycles: %d", pairs)
			}
			if !paired && pairs >= 5 {
				t.Fatalf("public scheduling changed: %d", pairs)
			}
			if _, err := RequireValidExperiment(dir); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunnerAdmissionAndFailedLaunchRetention(t *testing.T) {
	for _, kind := range []string{"admission", "cancel", "launch-error", "collector-error"} {
		t.Run(kind, func(t *testing.T) {
			runner, fake, options := admissionFixture(t)
			options.RetainFailedAttempts = true
			ctx := context.Background()
			admissions := 0
			options.AdmitAttempt = func(context.Context, AttemptAdmission) error {
				admissions++
				if kind == "admission" {
					return errors.New("admission denied")
				}
				return nil
			}
			if kind == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				fake.cancel = cancel
			}
			if kind == "launch-error" {
				fake.runFailure = true
			}
			if kind == "collector-error" {
				fake.base.incompleteFirst = true
			}
			dir, _, _, err := runner.Compare(ctx, options)
			if err == nil || admissions != 1 {
				t.Fatalf("did not stop: %v, admissions %d", err, admissions)
			}
			if kind == "admission" {
				if _, err := os.Stat(filepath.Join(dir, "attempts.jsonl")); !os.IsNotExist(err) {
					t.Fatal("unlaunched attempt charged")
				}
				return
			}
			attempts, err := LoadAttempts(dir)
			if err != nil || len(attempts) != 1 || attempts[0].Status != "invalid" {
				t.Fatalf("failed launch lost: %v %+v", err, attempts)
			}
			if _, err := os.Stat(filepath.Join(dir, attempts[0].Artifacts["database"])); err != nil {
				t.Fatal("owned evidence discarded")
			}
		})
	}
}
