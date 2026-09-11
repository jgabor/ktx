package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opt in with an explicitly locally built binary; never discover an installed CLI.
func TestBenchLocalCLISignals(t *testing.T) {
	binary := os.Getenv("KTX_LIFECYCLE_CLI")
	if binary == "" {
		t.Skip("set KTX_LIFECYCLE_CLI to the mage-built binary")
	}
	for _, verb := range []string{"compare", "rank"} {
		for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(fmt.Sprintf("%s/%s", verb, signal), func(t *testing.T) {
				root := t.TempDir()
				tools, err := filepath.Abs(filepath.Join("..", "..", "tools"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(tools, filepath.Join(root, "tools")); err != nil {
					t.Fatal(err)
				}
				pidFile := filepath.Join(root, "docker.pid")
				docker := filepath.Join(root, "docker")
				if err := os.WriteFile(docker, []byte(`#!/bin/sh
case "$1" in
image) echo sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; exit 0 ;;
rm) printf '%s\n' "$3" > "$BENCH_SIGNAL_PID.cleaned"; exit 0 ;;
run) printf '%s\n' "$3" > "$BENCH_SIGNAL_PID.owner" ;;
*) exit 91 ;;
esac
echo $$ > "$BENCH_SIGNAL_PID"
trap '' TERM
exec sleep 30
`), 0o700); err != nil {
					t.Fatal(err)
				}
				command := exec.Command(binary, "bench", verb, "--spec", "prompt-cache-token-hit-rate", "--task", "crossfile-nav", "--model", "openai/test", "--candidate", "conservative-a", "--termination-grace", "50ms")
				command.Dir = root
				for _, entry := range os.Environ() {
					key, _, _ := strings.Cut(entry, "=")
					if key != "KTX_DB" && key != "HOME" && !strings.HasPrefix(key, "XDG_") && key != "DOCKER_BIN" && key != "KTX_BIN" {
						command.Env = append(command.Env, entry)
					}
				}
				for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
					command.Env = append(command.Env, key+"="+filepath.Join(root, key))
				}
				command.Env = append(command.Env, "DOCKER_BIN="+docker, "KTX_BIN="+binary, "BENCH_SIGNAL_PID="+pidFile)
				var output bytes.Buffer
				command.Stdout, command.Stderr = &output, &output
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				wait := make(chan error, 1)
				go func() { wait <- command.Wait() }()
				t.Cleanup(func() { _ = command.Process.Kill() })
				deadline := time.Now().Add(10 * time.Second)
				var pid int
				for pid == 0 {
					data, _ := os.ReadFile(pidFile)
					pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
					select {
					case err := <-wait:
						t.Fatalf("CLI exited before fake Docker: %v: %s", err, &output)
					default:
					}
					if time.Now().After(deadline) {
						t.Fatal("fake Docker not started")
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
				if err := command.Process.Signal(signal); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-wait:
					if err == nil {
						t.Error("interrupted CLI returned success")
					}
				case <-time.After(8 * time.Second):
					t.Fatal("CLI cancellation exceeded bound")
				}
				stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if err == nil && !strings.Contains(string(stat), ") Z ") {
					t.Fatalf("owned Docker client %d survived parent %s", pid, signal)
				}
				owner, ownerErr := os.ReadFile(pidFile + ".owner")
				cleaned, cleanupErr := os.ReadFile(pidFile + ".cleaned")
				if ownerErr != nil || cleanupErr != nil || !bytes.Equal(owner, cleaned) || !strings.HasPrefix(string(owner), "ktx-capabilities-") {
					t.Fatalf("container cleanup ownership: owner=%q cleaned=%q errors=%v/%v", owner, cleaned, ownerErr, cleanupErr)
				}
			})
		}
	}
}
