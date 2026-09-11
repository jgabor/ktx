package experiment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestContainerIdentityKeepsFullInput(t *testing.T) {
	a := sanitizeContainerName("ktx-" + strings.Repeat("a", 127) + "b-baseline-001")
	b := sanitizeContainerName("ktx-" + strings.Repeat("a", 127) + "c-baseline-001")
	if a == b || len(a) > 120 || len(b) > 120 {
		t.Fatalf("container identities collide or exceed limit: %q %q", a, b)
	}
}

type containerExecutor struct {
	mu      sync.Mutex
	names   map[string]bool
	ready   chan struct{}
	started int
}

func (executor *containerExecutor) Run(ctx context.Context, command Command) (CommandResult, error) {
	executor.mu.Lock()
	if command.Args[1] == "rm" {
		defer executor.mu.Unlock()
		name := command.Args[3]
		if !executor.names[name] {
			return CommandResult{ExitCode: 1}, errors.New("cleanup touched another owner")
		}
		delete(executor.names, name)
		return CommandResult{}, nil
	}
	name := command.Args[3]
	collision := executor.names[name]
	executor.names[name] = true
	executor.started++
	if executor.started == 2 {
		close(executor.ready)
	}
	executor.mu.Unlock()
	select {
	case <-executor.ready:
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	}
	if collision {
		return CommandResult{ExitCode: 125}, errors.New("container owner collision")
	}
	return CommandResult{}, nil
}

func TestContainerCleanupOwnership(t *testing.T) {
	for _, sameIdentity := range []bool{false, true} {
		t.Run(fmt.Sprint(sameIdentity), func(t *testing.T) {
			fake := &containerExecutor{names: map[string]bool{}, ready: make(chan struct{})}
			runner := &Runner{Executor: fake}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			errorsOut := make(chan error, 2)
			for _, suffix := range []string{"b", "c"} {
				if sameIdentity {
					suffix = "b"
				}
				identity := "ktx-" + strings.Repeat("a", 127) + suffix + "-baseline-001"
				go func() {
					_, err := runner.runContainer(ctx, identity, Command{Args: []string{"docker", "run", "image"}})
					errorsOut <- err
				}()
			}
			for range 2 {
				if err := <-errorsOut; err != nil {
					t.Error(err)
				}
			}
			if len(fake.names) != 0 {
				t.Fatalf("owned containers remain: %v", fake.names)
			}
		})
	}
}

func TestOSExecutorDoesNotStartCanceledCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	marker := filepath.Join(t.TempDir(), "started")
	_, err := (OSExecutor{}).Run(ctx, Command{Args: []string{"sh", "-c", "touch \"$1\"", "sh", marker}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled execution error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("canceled command started: %v", err)
	}
}

type lifecycleExecutor struct {
	fakeExecutor
	cleanupErr      error
	cleanupCode     int
	cleanupTimedOut bool
	cancel          context.CancelFunc
}

func (executor *lifecycleExecutor) Run(ctx context.Context, command Command) (CommandResult, error) {
	result, err := executor.fakeExecutor.Run(ctx, command)
	if len(command.Args) > 1 && command.Args[0] == "docker" && command.Args[1] == "rm" && !strings.HasPrefix(command.Args[len(command.Args)-1], "ktx-capabilities-") {
		return CommandResult{ExitCode: executor.cleanupCode, TimedOut: executor.cleanupTimedOut}, executor.cleanupErr
	}
	if contains(command.Args, "--name") && !contains(command.Args, "--capabilities") && executor.cancel != nil {
		executor.cancel()
	}
	return result, err
}

func TestRunnerStopsAfterCleanupFailureOrCancellation(t *testing.T) {
	for _, kind := range []string{"cleanup-error", "cleanup-exit", "cleanup-timeout", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, content string) string {
				path := filepath.Join(root, name)
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &lifecycleExecutor{fakeExecutor: fakeExecutor{sessions: map[string]string{}}}
			switch kind {
			case "cleanup-error":
				fake.cleanupErr = errors.New("cleanup unavailable")
			case "cleanup-exit":
				fake.cleanupCode = 17
			case "cleanup-timeout":
				fake.cleanupTimedOut = true
			case "cancellation":
				fake.cancel = cancel
			}
			runner := &Runner{Executor: fake, Now: time.Now, ResolvePrice: testPriceResolver, Diagnostics: io.Discard}
			_, _, _, err := runner.Compare(ctx, CompareOptions{
				Model: "openai/test", TargetValid: 2, MaxAttempts: 4, ExperimentID: "lifecycle",
				RepoRoot: root, DataDir: filepath.Join(root, "data"), DockerBin: "docker", KTXBin: "ktx",
				TaskFile:        write("task.toml", "id = \"task\"\nprompt = \"do work\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n"),
				MethodologyPath: write("methodology.yaml", "methodology_version: v8-exact-match\n"),
				BaselineConfig:  write("baseline.toml", "[pipeline.default]\nstages = []\n"),
				CandidateConfig: write("candidate.toml", "[pipeline.default]\nstages = [\"head:1\"]\n"),
				AuthPath:        write("auth.json", "{}"),
			})
			if err == nil || fake.attemptRuns > 1 {
				t.Fatalf("err=%v attempts=%d; must stop before another attempt", err, fake.attemptRuns)
			}
			if kind != "cancellation" && !strings.Contains(err.Error(), "cleanup") {
				t.Fatalf("missing cleanup diagnostic: %v", err)
			}
			if kind == "cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("missing cancellation: %v", err)
			}
		})
	}
}
