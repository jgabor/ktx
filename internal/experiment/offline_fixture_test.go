package experiment

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSemanticIDOfflineExperimentFixture(t *testing.T) {
	work := os.Getenv("KTX_OFFLINE_FIXTURE_ROOT")
	if work == "" {
		work = t.TempDir()
	}
	source := filepath.Join(work, "source")
	write := func(name string, content []byte) string {
		t.Helper()
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("README.md", []byte("fixture\n"))
	write("Dockerfile.benchmark", []byte("FROM scratch\n"))
	auth := write("auth.json", []byte("{}\n"))
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	for _, name := range []string{"tools/bench/specs/exact-match.yaml", "tools/bench/pipelines/passthrough.toml", "tools/bench/pipelines/conservative-a.toml"} {
		content, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		write(name, content)
	}
	expected := "done"
	task := Task{ID: "crossfile-nav", Prompt: "do work", MinimumToolCalls: 1, ExpectedOutput: &expected, WorkspaceMode: "repo"}
	runner := &Runner{Executor: &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "1.18.5"}, Now: func() time.Time { return time.Date(2026, 8, 28, 20, 0, 0, 0, time.UTC) }, PID: 56, ResolvePrice: testPriceResolver}
	dir, _, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "opencode/gpt-5.6-luna", Variant: "high", Task: task, TaskID: "crossfile-nav", TargetValid: 1, MaxAttempts: 2,
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "semantic-cutover-offline",
		DataDir: filepath.Join(work, ".ktx", "experiments"), Image: "image", AuthPath: auth, RepoRoot: source,
		MethodologyPath: "exact-match", BaselineConfig: "passthrough", CandidateConfig: "conservative-a", KTXBin: "ktx", DockerBin: "docker",
		RegisteredInputs: true,
	})
	if err != nil || code != 0 {
		t.Fatalf("Compare code=%d err=%v", code, err)
	}
	original, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		summary, err := WriteSummary(dir)
		if err != nil || summary.SchemaVersion() != 8 {
			t.Fatalf("summary=%v err=%v", summary, err)
		}
		report, err := ValidateExperiment(dir)
		if err != nil || report["valid"] != true || report["target_met"] != true {
			t.Fatalf("report=%v err=%v", report, err)
		}
		regenerated, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
		if !bytes.Equal(original, regenerated) {
			t.Fatal("inline producer/regenerator summary bytes differ")
		}
		if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
			for _, verb := range []string{"list", "show", "summarize", "validate"} {
				if out, err := entryCLI(t, dir, verb); err != nil {
					t.Fatalf("inline %s: %v %s", verb, err, out)
				}
			}
		}
	}
}
