package experiment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

type responseExecutor func(context.Context, Command) (CommandResult, error)

func (run responseExecutor) Run(ctx context.Context, command Command) (CommandResult, error) {
	return run(ctx, command)
}

func TestInspectionResponseBoundary(t *testing.T) {
	for _, probe := range []string{"image", "run"} {
		for _, size := range []int{len(testImageID) + 1, 65535, 65536, 65537} {
			t.Run(fmt.Sprintf("%s/%d", probe, size), func(t *testing.T) {
				fake := &fakeExecutor{}
				runner := &Runner{Executor: responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
					if command.Args[1] != probe {
						return fake.Run(ctx, command)
					}
					value := testImageID + "\n"
					if probe == "run" {
						value = `{}`
					}
					// Padding counts before trimming or decoding; a valid prefix must not pass.
					_, _ = io.WriteString(command.Stdout, value+strings.Repeat(" ", size-len(value)))
					return CommandResult{}, nil // Even an executor ignoring Write errors must fail closed.
				})}
				got, _, err := runner.inspectBenchmarkRuntime(context.Background(), CompareOptions{DockerBin: "docker", Image: "fake"})
				if size > 65536 {
					if err == nil || !strings.Contains(err.Error(), "response exceeds 65536-byte maximum") {
						t.Fatalf("oversize response error=%v", err)
					}
				} else if err != nil || got.ImageID != testImageID {
					t.Fatalf("within-limit runtime=%+v err=%v", got, err)
				}
				wantCleanup := 1
				if probe == "image" && size > 65536 {
					wantCleanup = 0
				}
				if len(fake.cleanup) != wantCleanup {
					t.Fatalf("cleanup calls=%d, want %d", len(fake.cleanup), wantCleanup)
				}
			})
		}
	}
}

func TestInspectionResponseHugeAllocation(t *testing.T) {
	for _, probe := range []string{"image", "run"} {
		t.Run(probe, func(t *testing.T) {
			payload := bytes.Repeat([]byte("x"), 32<<20)
			fake := &fakeExecutor{}
			var retained, consumed int
			runner := &Runner{Executor: responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
				if command.Args[1] != probe {
					return fake.Run(ctx, command)
				}
				n, err := command.Stdout.Write(payload)
				consumed = n
				retained = command.Stdout.(interface{ Len() int }).Len()
				return CommandResult{}, err
			})}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, _, err := runner.inspectBenchmarkRuntime(context.Background(), CompareOptions{DockerBin: "docker", Image: "fake"})
			runtime.ReadMemStats(&after)
			if err == nil || !strings.Contains(err.Error(), "response exceeds 65536-byte maximum") {
				t.Errorf("oversize response error=%v", err)
			}
			if retained != 65537 || consumed != 65537 {
				t.Errorf("retained=%d consumed=%d; want one-byte sentinel only", retained, consumed)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
				t.Errorf("32 MiB response allocated %d bytes", allocated)
			}
		})
	}
}

func TestInspectionResponseErrors(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			fake := &fakeExecutor{}
			cleanupFailure := errors.New("cleanup transport failure")
			runner := &Runner{Executor: responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
				switch command.Args[1] {
				case "run":
					payload := "not JSON"
					if oversized {
						payload += strings.Repeat(" ", 65537)
					}
					_, _ = io.WriteString(command.Stdout, payload)
					return CommandResult{}, nil
				case "rm":
					if ctx.Err() != nil {
						t.Errorf("cleanup inherited cancellation: %v", ctx.Err())
					}
					if oversized {
						return CommandResult{}, cleanupFailure
					}
				}
				return fake.Run(ctx, command)
			})}
			_, _, err := runner.inspectBenchmarkRuntime(context.Background(), CompareOptions{DockerBin: "docker", Image: "fake"})
			if !oversized {
				if err == nil || !strings.Contains(err.Error(), "decode benchmark capabilities") {
					t.Fatalf("malformed response error=%v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "response exceeds 65536-byte maximum") || !errors.Is(err, errContainerCleanup) || !errors.Is(err, cleanupFailure) || strings.Contains(err.Error(), "decode benchmark capabilities") {
				t.Fatalf("size and cleanup errors must survive before decode: %v", err)
			}
		})
	}
}

func TestGenericOutputRemainsUncapped(t *testing.T) {
	want := strings.Repeat("session-id\n", 10000)
	runner := &Runner{Executor: responseExecutor(func(_ context.Context, command Command) (CommandResult, error) {
		_, err := io.WriteString(command.Stdout, want)
		return CommandResult{}, err
	})}
	got, err := runner.output(context.Background(), Command{Args: []string{"git", "status"}, Timeout: time.Second})
	if err != nil || got != want {
		t.Fatalf("generic output length=%d want=%d err=%v", len(got), len(want), err)
	}
}

func TestPreflightResponseSizeError(t *testing.T) {
	for _, probe := range []string{"image", "run"} {
		t.Run(probe, func(t *testing.T) {
			fake := &fakeExecutor{}
			runner := &Runner{Executor: responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
				if command.Args[1] == probe {
					_, _ = io.WriteString(command.Stdout, strings.Repeat(" ", 65537))
					return CommandResult{}, nil
				}
				return fake.Run(ctx, command)
			})}
			report, _, err := runner.preflightPrepared(context.Background(), CompareOptions{DockerBin: "docker", Image: "fake"})
			if report.Eligible || err == nil || !strings.Contains(err.Error(), "response exceeds 65536-byte maximum") {
				t.Fatalf("preflight hid response-size failure: report=%+v err=%v", report, err)
			}
		})
	}
}
