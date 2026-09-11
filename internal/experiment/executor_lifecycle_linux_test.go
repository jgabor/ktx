package experiment

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOSExecutorReapsLeaderFirstGroup(t *testing.T) {
	for _, mode := range []string{"leader-exit", "leader-exit-pipe", "leader-term-exit", "leader-term-exit-pipe"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child")
			var output io.Writer
			if strings.HasSuffix(mode, "-pipe") {
				output = io.Discard
			}
			started := time.Now()
			finish := "exit 0"
			if strings.Contains(mode, "term") {
				finish = "trap 'exit 0' TERM; wait"
			}
			_, err := (OSExecutor{}).Run(context.Background(), Command{
				Args:   []string{"sh", "-c", "sh -c 'trap \"\" TERM; echo $$ > \"$1\"; exec sleep 30' sh \"$1\" & while [ ! -s \"$1\" ]; do sleep 0.01; done; " + finish, "sh", pidFile},
				Stdout: output, Stderr: output, Timeout: 200 * time.Millisecond, Grace: 50 * time.Millisecond,
			})
			data, readErr := os.ReadFile(pidFile)
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			if time.Since(started) > 2*time.Second {
				t.Fatalf("execution exceeded bound: %v", err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if os.IsNotExist(statErr) || strings.Contains(string(stat), ") Z ") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("descendant %d remains alive after leader exit (err=%v)", pid, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
