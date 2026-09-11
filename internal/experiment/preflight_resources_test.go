package experiment

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type resourceProbeExecutor struct {
	t     *testing.T
	calls int
}

type noisyBuildExecutor struct {
	t    *testing.T
	fake fakeExecutor
}

func (e *noisyBuildExecutor) Run(ctx context.Context, command Command) (CommandResult, error) {
	if command.Args[1] == "build" {
		if command.Timeout != time.Second || command.Grace != time.Millisecond {
			e.t.Errorf("build did not inherit resource options: %+v", command)
		}
		_, _ = io.WriteString(command.Stdout, strings.Repeat("o", 128<<10))
		_, _ = io.WriteString(command.Stderr, strings.Repeat("e", 128<<10))
		return CommandResult{}, nil
	}
	return e.fake.Run(ctx, command)
}

func TestPreflightNoisyBuildSuccess(t *testing.T) {
	var diagnostics bytes.Buffer
	runner := &Runner{Executor: &noisyBuildExecutor{t: t}, Diagnostics: &diagnostics}
	report, _, err := runner.preflightPrepared(context.Background(), CompareOptions{DockerBin: "docker", Image: "fake", RepoRoot: filepath.Join("..", ".."), BuildImage: true, AttemptTimeout: time.Second, TerminationGrace: time.Millisecond})
	if err != nil || !report.Eligible {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if diagnostics.Len() > 70<<10 || !strings.Contains(diagnostics.String(), "[diagnostics truncated]") {
		t.Fatalf("build diagnostic length=%d", diagnostics.Len())
	}
}

func TestPreflightHungNoisyCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncase \"$1\" in\nimage) printf image-id;;\nrm) exit 0;;\nrun) while :; do printf 'noisy diagnostics................................................................' >&2; done;;\nesac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Executor: OSExecutor{}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := runner.inspectBenchmarkRuntime(ctx, CompareOptions{DockerBin: path, Image: "fake", AttemptTimeout: 100 * time.Millisecond, TerminationGrace: 10 * time.Millisecond})
	if err == nil || len(err.Error()) > 70<<10 {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("probe took %s; configured timeout was not enforced", elapsed)
	}
}

func TestOutputNoisyFailureAndSuccess(t *testing.T) {
	runner := &Runner{Executor: OSExecutor{}}
	for _, exit := range []string{"0", "7"} {
		output, err := runner.output(context.Background(), Command{Args: []string{"sh", "-c", "printf success; i=0; while [ $i -lt 2000 ]; do printf '%0100d' 0 >&2; i=$((i+1)); done; exit " + exit}, Timeout: time.Second, Grace: 10 * time.Millisecond})
		if exit == "0" {
			if err != nil || output != "success" {
				t.Fatalf("output=%q err=%v", output, err)
			}
		} else if err == nil || len(err.Error()) > 70<<10 || !strings.Contains(err.Error(), "[diagnostics truncated]") {
			t.Fatalf("unexpected failure diagnostic: %v", err)
		}
	}
}

func TestDiagnosticLimitConcurrentDrain(t *testing.T) {
	var out bytes.Buffer
	w := newDiagnosticLimit(&out)
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			defer func() { done <- struct{}{} }()
			p := bytes.Repeat([]byte("x"), 128<<10)
			if n, err := w.Write(p); n != len(p) || err != nil {
				t.Errorf("n=%d err=%v", n, err)
			}
		}()
	}
	<-done
	<-done
	if out.Len() != (64<<10)+len("\n[diagnostics truncated]\n") || strings.Count(out.String(), "[diagnostics truncated]") != 1 {
		t.Fatalf("diagnostics length=%d", out.Len())
	}
}

func (e *resourceProbeExecutor) Run(_ context.Context, command Command) (CommandResult, error) {
	e.calls++
	if command.Timeout <= 0 || command.Grace <= 0 {
		e.t.Errorf("unbounded command: %v timeout=%s grace=%s", command.Args, command.Timeout, command.Grace)
	}
	if e.calls == 1 {
		_, _ = io.WriteString(command.Stdout, testImageID)
		return CommandResult{}, nil
	}
	if command.Args[1] == "rm" {
		return CommandResult{}, nil
	}
	_, _ = io.WriteString(command.Stderr, strings.Repeat("x", 2<<20))
	return CommandResult{ExitCode: 1}, nil
}

func TestPreflightProbeResources(t *testing.T) {
	executor := &resourceProbeExecutor{t: t}
	runner := &Runner{Executor: executor}
	_, _, err := runner.inspectBenchmarkRuntime(context.Background(), CompareOptions{DockerBin: "fake-docker", Image: "fake", AttemptTimeout: time.Second, TerminationGrace: time.Millisecond})
	if err == nil {
		t.Fatal("expected failed capability probe")
	}
	if len(err.Error()) > 70<<10 {
		t.Fatalf("diagnostic retained %d bytes", len(err.Error()))
	}
	if executor.calls != 3 {
		t.Fatalf("calls=%d", executor.calls)
	}
}
