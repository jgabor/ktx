package experiment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInspectionResponseStopsInfiniteWriter(t *testing.T) {
	for _, probe := range []string{"image", "run"} {
		t.Run(probe, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "docker")
			// Both leader and descendant ignore TERM. The producer also ignores
			// PIPE, so merely returning a Write error would not stop the group.
			script := fmt.Sprintf(`#!/bin/sh
root=%q
case "$1" in
rm) printf '%%s' "$3" > "$root/cleanup"; exit 0;;
run) printf '%%s' "$3" > "$root/owner";;
esac
if [ "$1" != %q ]; then printf '%s\n'; exit 0; fi
trap '' TERM PIPE
printf '%%s' "$$" > "$root/leader"
sh -c 'trap "" TERM; printf "%%s" "$$" > "$1/child"; exec sleep 30' sh "$root" &
while [ ! -s "$root/child" ]; do sleep 0.001; done
while :; do printf '%%8192s' x; printf '%%8192s' e >&2; sleep 0.001; done
`, root, probe, testImageID)
			if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := &Runner{Executor: OSExecutor{}}
			start := time.Now()
			_, _, err := runner.inspectBenchmarkRuntime(context.Background(), CompareOptions{DockerBin: path, Image: "fake", AttemptTimeout: 2 * time.Second, TerminationGrace: 20 * time.Millisecond})
			elapsed := time.Since(start)
			for _, file := range []string{"leader", "child"} {
				data, readErr := os.ReadFile(filepath.Join(root, file))
				if readErr != nil {
					t.Fatal(readErr)
				}
				pid, parseErr := strconv.Atoi(string(data))
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
				deadline := time.Now().Add(time.Second)
				for {
					stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
					if os.IsNotExist(statErr) || strings.Contains(string(stat), ") Z ") {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("%s %d remains running after inspection returned: %v", file, pid, statErr)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if err == nil || !strings.Contains(err.Error(), "response exceeds 65536-byte maximum") || len(err.Error()) > 1024 {
				t.Fatalf("unbounded or missing size error: %v", err)
			}
			if elapsed > time.Second {
				t.Fatalf("overflow waited for attempt timeout: %s", elapsed)
			}
			cleanup, cleanupErr := os.ReadFile(filepath.Join(root, "cleanup"))
			if probe == "run" {
				owner, readErr := os.ReadFile(filepath.Join(root, "owner"))
				if readErr != nil || cleanupErr != nil || string(owner) != string(cleanup) || !strings.HasPrefix(string(owner), "ktx-capabilities-") {
					t.Fatalf("cleanup not settled for exact owner: owner=%q cleanup=%q errors=%v/%v", owner, cleanup, readErr, cleanupErr)
				}
			} else if !os.IsNotExist(cleanupErr) {
				t.Fatalf("image overflow unexpectedly started container cleanup: %v", cleanupErr)
			}
		})
	}
}
