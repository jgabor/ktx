//go:build linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func optimizeGeneratedLiveArgs(t *testing.T, r optimizeResult) []string {
	t.Helper()
	for _, command := range r.NextCommands {
		if strings.Contains(command, "--live") {
			// This regression exercises the absent-default command with no quoted
			// path operands. Existing/supplied-path preservation has separate tests.
			args := strings.Fields(command)[1:]
			for i, arg := range args {
				if arg == "--model" {
					args[i+1] = "openai/test"
				}
			}
			return args
		}
	}
	t.Fatal("screened result has no live next command")
	return nil
}

func TestOptimizeAbsentDefaultNextCommandExecutes(t *testing.T) {
	optimizeTestRoot(t) // Empty PATH prevents Docker and provider execution.
	r := optimizeRun(t)
	if r.Target.Exists == nil || *r.Target.Exists {
		t.Fatal("fixture default is not absent")
	}
	args := optimizeGeneratedLiveArgs(t, r)
	for _, arg := range args {
		if arg == "--config" {
			t.Fatal("absent default became an explicit missing target")
		}
	}
	var stdout, stderr bytes.Buffer
	code := runWith(args, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("generated command: %d %s", code, stderr.String())
	}
	var follow optimizeResult
	if err := json.Unmarshal(stdout.Bytes(), &follow); err != nil {
		t.Fatal(err)
	}
	if follow.Mode != "live" || follow.Outcome != "blocked" || follow.Live == nil || follow.Live.AttemptsAdmitted != 0 || !strings.Contains(strings.Join(follow.Reasons, ","), "benchmark_preflight_or_evidence_failed") {
		t.Fatalf("did not reach provider-free preflight refusal: %+v", follow)
	}
	if follow.Target.Path != r.Target.Path || follow.Target.Exists == nil || *follow.Target.Exists || strings.Contains(stdout.String(), "explicit_target_missing") {
		t.Fatal("generated command lost effective-default target selection")
	}
	code, _, _ = optimizeInvoke(t, "--live")
	if code != ExitUsage {
		t.Fatal("live command stopped requiring model input")
	}
	if _, err := os.Stat(r.Target.Path); !os.IsNotExist(err) {
		t.Fatal("absent config was created")
	}

	optimizeWrite(t, "existing.toml", "[pipeline.default]\nstages=[]\n")
	existing := optimizeRun(t, "--config", "existing.toml")
	if !strings.Contains(strings.Join(existing.NextCommands, "\n"), "--config '") {
		t.Fatal("existing explicit target was dropped")
	}
}

func TestOptimizeInputReaderRefusesFIFO(t *testing.T) {
	root := optimizeTestRoot(t)
	regular := filepath.Join(root, "regular.toml")
	optimizeWrite(t, regular, "[pipeline.default]\nstages=[]\n")
	data, err := optimizeRead(regular, optimizeConfigLimit)
	if err != nil || len(data) == 0 {
		t.Fatalf("regular input regressed: %v", err)
	}
	if err := os.Symlink(regular, regular+".link"); err != nil {
		t.Fatal(err)
	}
	linked, err := optimizeRead(regular+".link", optimizeConfigLimit)
	if err != nil || !bytes.Equal(linked, data) {
		t.Fatal("ordinary symlink input contract changed")
	}
	fifo := filepath.Join(root, "no-writer.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := optimizeRead(fifo, optimizeConfigLimit); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO was not rejected: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no-writer FIFO blocked the bounded reader")
	}
}

func TestOptimizeCorrectionBuiltBinary(t *testing.T) {
	binary := os.Getenv("KTX_OPTIMIZE_TEST_BINARY")
	if binary == "" {
		t.Skip("set KTX_OPTIMIZE_TEST_BINARY to the locally built CLI for correction smoke")
	}
	root := optimizeTestRoot(t)
	invoke := func(args ...string) (int, []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = append(optimizeChildEnvironment(t), "PATH=")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		data, err := command.Output()
		if ctx.Err() != nil {
			t.Fatalf("local command blocked: %v", args)
		}
		if err == nil {
			return ExitOK, data
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), data
		}
		t.Fatalf("local command: %v %s", err, stderr.String())
		return -1, nil
	}
	code, data := invoke("optimize")
	if code != ExitOK {
		t.Fatal("initial offline command failed")
	}
	var initial optimizeResult
	if err := json.Unmarshal(data, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Outcome != "screened" || initial.Target.Exists == nil || *initial.Target.Exists {
		t.Fatal("native default was not absent/screened")
	}
	args := optimizeGeneratedLiveArgs(t, initial)
	code, data = invoke(args...)
	var follow optimizeResult
	if err := json.Unmarshal(data, &follow); err != nil {
		t.Fatal(err)
	}
	if code != ExitOK || follow.Mode != "live" || follow.Outcome != "blocked" || follow.Live == nil || follow.Live.AttemptsAdmitted != 0 || !strings.Contains(strings.Join(follow.Reasons, ","), "benchmark_preflight_or_evidence_failed") || strings.Contains(string(data), "explicit_target_missing") {
		t.Fatalf("native generated command failed target selection: %+v", follow)
	}
	if code, _ := invoke("optimize", "--live"); code != ExitUsage {
		t.Fatal("native model input requirement changed")
	}
	if _, err := os.Stat(initial.Target.Path); !os.IsNotExist(err) {
		t.Fatal("native next action created initial config")
	}
	t.Log("generated absent-default live command reached missing-Docker preflight with zero model launches")

	fifo := filepath.Join(root, "no-writer.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(root, "baseline.toml")
	const original = "[pipeline.default]\nstages=[]\n"
	optimizeWrite(t, baseline, original)
	for _, tt := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"config", []string{"optimize", "--config", fifo}, "target_unreadable_or_exceeds_bound"},
		{"candidate", []string{"optimize", "--config", baseline, "--candidate", fifo}, "candidate_unreadable_or_exceeds_bound"},
		{"pricing", []string{"optimize", "--live", "--model", "openai/test", "--config", baseline, "--pricing-snapshot", fifo}, "pricing_snapshot_unreadable"},
	} {
		started := time.Now()
		code, data := invoke(tt.args...)
		var report optimizeResult
		if err := json.Unmarshal(data, &report); err != nil {
			t.Fatal(err)
		}
		if code != ExitOK || report.ApplyEligible || !strings.Contains(strings.Join(report.Reasons, ","), tt.reason) {
			t.Fatalf("FIFO %s refusal: %+v", tt.name, report)
		}
		t.Logf("FIFO %s returned %s in %s", tt.name, tt.reason, time.Since(started))
	}
	current, err := os.ReadFile(baseline)
	if err != nil || string(current) != original {
		t.Fatal("FIFO verification changed config")
	}
	info, err := os.Lstat(fifo)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("FIFO was replaced")
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".db") {
			t.Errorf("unexpected database: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOptimizeScreeningRefusesUnapplicableTargets(t *testing.T) {
	const data = "[pipeline.default]\nstages=[]\n"
	for _, kind := range []string{"plain", "absent-default", "target-symlink", "parent-symlink", "hardlink", "absent-under-symlink", "special-bit"} {
		t.Run(kind, func(t *testing.T) {
			root := optimizeTestRoot(t)
			args := []string{"--config", filepath.Join(root, "owned", "target.toml")}
			if err := os.Mkdir(filepath.Join(root, "owned"), 0o700); err != nil {
				t.Fatal(err)
			}
			target := args[1]
			switch kind {
			case "plain":
				optimizeWrite(t, target, data)
			case "absent-default":
				args = nil
			case "target-symlink":
				optimizeWrite(t, filepath.Join(root, "dotfile.toml"), data)
				if err := os.Symlink(filepath.Join(root, "dotfile.toml"), target); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				optimizeWrite(t, target, data)
				if err := os.Symlink(filepath.Join(root, "owned"), filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
				args[1] = filepath.Join(root, "linked", "target.toml")
			case "hardlink":
				optimizeWrite(t, target, data)
				if err := os.Link(target, target+".alias"); err != nil {
					t.Fatal(err)
				}
			case "absent-under-symlink":
				// A dotfile-managed config directory whose target does not exist yet.
				if err := os.Symlink(filepath.Join(root, "owned"), os.Getenv("XDG_CONFIG_HOME")); err != nil {
					t.Fatal(err)
				}
				args = nil
			case "special-bit":
				optimizeWrite(t, target, data)
				if err := os.Chmod(target, 0o600|os.ModeSticky); err != nil {
					t.Fatal(err)
				}
				if info, err := os.Stat(target); err != nil || info.Mode()&os.ModeSticky == 0 {
					t.Skip("filesystem does not retain the sticky bit on regular files")
				}
			}
			r := optimizeRun(t, args...)
			refused := strings.Contains(strings.Join(r.Reasons, ","), "target_not_safely_applicable")
			if kind == "plain" || kind == "absent-default" {
				if refused || r.Outcome != "screened" || r.SelectedCandidate == nil {
					t.Fatalf("safe target refused: %+v", r)
				}
				return
			}
			if !refused || r.Outcome != "blocked" || r.BaselineScreen != nil || len(r.Candidates) != 0 || r.SelectedCandidate != nil {
				t.Fatalf("unapplicable target screened: %+v", r)
			}
			for _, command := range r.NextCommands {
				if strings.Contains(command, "--live") {
					t.Fatal("refused target still offers paid qualification")
				}
			}
			code, text, _ := optimizeInvoke(t, "show", r.ID, "--format", "text")
			if code != ExitOK || !strings.Contains(text, "target_not_safely_applicable") {
				t.Fatal("refusal reason missing from text report")
			}
		})
	}
}
