package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchRankCurrentCostHelp(t *testing.T) {
	for _, args := range [][]string{{"rank", "--help"}, {"rank", "--current-cost", "--help"}, {"rank", "--help", "--current-cost"}, {"rank", "--current-cost", "-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
				t.Setenv(key, filepath.Join(root, key))
			}
			t.Setenv("KTX_DB", "")
			var stdout, stderr bytes.Buffer
			if code := runBenchWith(args, Options{Format: FormatText}, &stdout, &stderr); code != ExitOK {
				t.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
			for _, want := range []string{"--current-cost <campaign-id> --format text", "not an\nexperiment ID", "current cost policy", "not today's prices", "reported cost", "uniformly zero", "whole-cohort API estimate", "common basis", "retained usage", "pricing snapshots", "unavailable (NULL)", "no cost winner", "Historical fields stay unchanged", "no model calls", "no database", "writes no archive", "not just summary.json", "--spec is required for live comparisons only"} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("help missing %q: %s", want, stdout.String())
				}
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("help created files: %v %v", entries, err)
			}
			if binary := os.Getenv("KTX_ENTRY_CONTRACT_BINARY"); binary != "" {
				cmd := exec.Command(binary, append([]string{"bench"}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil || !bytes.Equal(out, stdout.Bytes()) {
					t.Fatalf("built help: %v\n%s", err, out)
				}
			}
		})
	}
}
