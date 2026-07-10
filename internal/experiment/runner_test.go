package experiment

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOSExecutorTerminatesProcessGroup(t *testing.T) {
	started := time.Now()
	result, err := (OSExecutor{}).Run(context.Background(), Command{
		Args:   []string{"sh", "-c", "trap '' TERM; sleep 30 & wait"},
		Stdout: io.Discard, Stderr: io.Discard, Timeout: 50 * time.Millisecond, Grace: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.ExitCode != 124 {
		t.Fatalf("result = %#v", result)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("process group cancellation exceeded deadline")
	}
}

type fakeExecutor struct {
	mu           sync.Mutex
	sessions     map[string]string
	sides        []string
	cleanup      []string
	timeoutFirst bool
	attemptRuns  int
}

func (fake *fakeExecutor) Run(_ context.Context, command Command) (CommandResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	write := func(writer io.Writer, value string) {
		if writer != nil {
			_, _ = io.WriteString(writer, value)
		}
	}
	if command.Args[0] == "git" {
		if command.Args[len(command.Args)-2] == "--short" {
			write(command.Stdout, "abc123\n")
		} else if command.Args[len(command.Args)-1] == "HEAD" {
			write(command.Stdout, strings.Repeat("a", 40)+"\n")
		}
		return CommandResult{}, nil
	}
	if command.Args[0] == "docker" {
		if len(command.Args) > 2 && command.Args[1] == "image" {
			write(command.Stdout, "sha256:image\n")
			return CommandResult{}, nil
		}
		if len(command.Args) > 1 && command.Args[1] == "rm" {
			fake.cleanup = append(fake.cleanup, command.Args[len(command.Args)-1])
			return CommandResult{}, nil
		}
		if contains(command.Args, "--identity") {
			write(command.Stdout, "ktx\ttest\nopencode\ttest\n")
			return CommandResult{}, nil
		}
		if len(command.Args) > 1 && command.Args[1] == "run" {
			fake.attemptRuns++
			if fake.timeoutFirst && fake.attemptRuns == 1 {
				return CommandResult{ExitCode: 124, TimedOut: true}, nil
			}
			database, config := mountSource(command.Args, "/home/node/.local/share/ktx/ktx.db"), mountSource(command.Args, "/home/node/.config/ktx/ktx.toml")
			side := strings.TrimPrefix(filepath.Ext(config), ".")
			fake.sessions[database] = side
			fake.sides = append(fake.sides, side)
			write(command.Stdout, `{"type":"tool_use","part":{}}`+"\n")
			write(command.Stdout, fmt.Sprintf(`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":%d,"output":0},"cost":0}}`+"\n", map[string]int{Baseline: 100, Candidate: 40}[side]))
			write(command.Stdout, `{"type":"text","part":{"text":"done"}}`+"\n")
			return CommandResult{}, nil
		}
	}
	if command.Args[0] == "ktx" {
		database := environmentValue(command.Env, "KTX_DB")
		if session := fake.sessions[database]; session != "" {
			write(command.Stdout, fmt.Sprintf(`[{"id":%q}]`+"\n", session))
		} else {
			write(command.Stdout, "[]\n")
		}
		return CommandResult{}, nil
	}
	return CommandResult{ExitCode: 2}, nil
}

func TestRunnerAlternatesAndCleansContainers(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(relative, value string) string {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	writeFixture("README.md", "fixture\n")
	writeFixture("Dockerfile.benchmark", "FROM scratch\n")
	writeFixture("tools/bench/methodology-v4-eval-matrix.yaml", "methodology_version: v4-eval-matrix\n")
	baseline := writeFixture("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
	candidate := writeFixture("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = [\"head:1\"]\n")
	task := writeFixture("tools/bench/tasks/task.toml", "id = \"task\"\nprompt = \"do work\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
	auth := writeFixture("auth.json", "{}\n")
	fake := &fakeExecutor{sessions: map[string]string{}}
	runner := &Runner{Executor: fake, Now: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }, PID: 7}
	directory, summary, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "neuralwatt/test", TaskFile: task, TargetValid: 2, MaxAttempts: 4,
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "experiment",
		DataDir: filepath.Join(root, "data"), Image: "image", AuthPath: auth, RepoRoot: root,
		MethodologyPath: filepath.Join(root, "tools/bench/methodology-v4-eval-matrix.yaml"),
		BaselineConfig:  baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("Compare code=%d err=%v", code, err)
	}
	if got := strings.Join(fake.sides, ","); got != "baseline,candidate,candidate,baseline" {
		t.Fatalf("side order = %s", got)
	}
	if len(fake.cleanup) != 4 {
		t.Fatalf("cleanup calls = %d", len(fake.cleanup))
	}
	if summary["tool_call_inflation"] == nil {
		t.Fatal("summary missing tool call inflation")
	}
	before, err := os.ReadFile(filepath.Join(directory, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(directory); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(directory, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("summary regeneration changed bytes")
	}

	timeoutFake := &fakeExecutor{sessions: map[string]string{}, timeoutFirst: true}
	timeoutRunner := &Runner{Executor: timeoutFake, Now: runner.Now, PID: 8}
	_, timeoutSummary, code, err := timeoutRunner.Compare(context.Background(), CompareOptions{
		Model: "neuralwatt/test", TaskFile: task, TargetValid: 1, MaxAttempts: 3,
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "timeout-retry",
		DataDir: filepath.Join(root, "timeout-data"), Image: "image", AuthPath: auth, RepoRoot: root,
		MethodologyPath: filepath.Join(root, "tools/bench/methodology-v4-eval-matrix.yaml"),
		BaselineConfig:  baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("timeout retry code=%d err=%v", code, err)
	}
	if len(timeoutFake.cleanup) != 3 {
		t.Fatalf("timeout cleanup calls = %d", len(timeoutFake.cleanup))
	}
	pipelines := timeoutSummary["pipelines"].(map[string]any)
	baselineSummary := pipelines[Baseline].(map[string]any)
	statusCounts := baselineSummary["status_counts"].(map[string]int)
	if statusCounts["timeout"] != 1 || baselineSummary["valid_count"] != 1 {
		t.Fatalf("timeout summary = %#v", baselineSummary)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mountSource(args []string, destination string) string {
	for index, value := range args {
		if value == "-v" && index+1 < len(args) {
			parts := strings.Split(args[index+1], ":")
			if len(parts) > 1 && parts[1] == destination {
				return parts[0]
			}
		}
	}
	return ""
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}
