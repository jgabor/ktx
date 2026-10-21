//go:build linux

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// optimizeQualified holds one simulated qualification per target state.
// Qualification dominates these tests' runtime, so each state is qualified
// once per test binary and every caller restores a pristine copy. Results
// record absolute target paths, so copies are always restored to one fixed
// work directory.
var optimizeQualified struct {
	base      string
	templates map[bool]optimizeQualifiedTemplate
}

type optimizeQualifiedTemplate struct {
	snapshot string
	result   []byte
	original []byte
}

func TestMain(m *testing.M) {
	code := m.Run()
	if optimizeQualified.base != "" {
		if err := optimizeRemoveTree(optimizeQualified.base); err != nil {
			fmt.Fprintln(os.Stderr, err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func optimizeApplyFixture(t *testing.T, present bool) (optimizeResult, []byte) {
	t.Helper()
	template, ok := optimizeQualified.templates[present]
	if !ok {
		template = optimizeQualifyTemplate(t, present)
	}
	work := filepath.Join(optimizeQualified.base, "work")
	if err := optimizeRemoveTree(work); err != nil {
		t.Fatal(err)
	}
	if err := optimizeCopyTree(template.snapshot, work); err != nil {
		t.Fatal(err)
	}
	optimizeTestEnvironment(t, work)
	t.Setenv("PATH", filepath.Join(work, "bin"))
	var r optimizeResult
	if err := json.Unmarshal(template.result, &r); err != nil {
		t.Fatal(err)
	}
	if !present {
		// Screening resolves the default target without creating its parent,
		// so preview and apply exercise missing parents.
		if _, err := os.Lstat(filepath.Dir(r.Target.Path)); !os.IsNotExist(err) {
			t.Fatalf("screening created the absent target parent: %v", err)
		}
	}
	return r, bytes.Clone(template.original)
}

func optimizeQualifyTemplate(t *testing.T, present bool) optimizeQualifiedTemplate {
	t.Helper()
	if optimizeQualified.base == "" {
		base, err := os.MkdirTemp("", "ktx-optimize-qualified-")
		if err != nil {
			t.Fatal(err)
		}
		optimizeQualified.base = base
		optimizeQualified.templates = map[bool]optimizeQualifiedTemplate{}
	}
	work := filepath.Join(optimizeQualified.base, "work")
	if err := optimizeRemoveTree(work); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	development, args, runner, _ := optimizeLiveFixtureIn(t, work)
	var original []byte
	if present {
		parent := filepath.Join(work, "owned-config")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(parent, "target.toml")
		original = []byte("# original bytes and private settings stay recoverable\n[pipeline.default]\nstages=[]\ntimeout='500ms'\nrecover_footer=true\n[pipeline.other]\nstages=[]\n[logs]\nlevel='off'\n")
		optimizeWrite(t, target, string(original))
		if err := os.Chmod(target, 0o640); err != nil {
			t.Fatal(err)
		}
		development = optimizeRun(t, "--config", target)
	}
	r, err := qualifyOptimize(context.Background(), development, args, runner, io.Discard)
	if err != nil || r.Outcome != "ready" {
		t.Fatalf("simulated qualification: %v %+v", err, r)
	}
	result, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(optimizeQualified.base, fmt.Sprintf("qualified-%t", present))
	if err := optimizeRemoveTree(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := optimizeCopyTree(work, snapshot); err != nil {
		t.Fatal(err)
	}
	template := optimizeQualifiedTemplate{snapshot: snapshot, result: result, original: original}
	optimizeQualified.templates[present] = template
	return template
}

// optimizeCopyTree copies directories and regular files with their exact
// permission bits. Copies never share inodes with the source.
func optimizeCopyTree(source, destination string) error {
	modes := map[string]fs.FileMode{}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			modes[target] = info.Mode().Perm()
			return os.Mkdir(target, 0o700)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, data, 0o600); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		default:
			return fmt.Errorf("unexpected file type in qualified fixture: %s", path)
		}
	})
	if err != nil {
		return err
	}
	// Restore directory modes after their contents exist.
	for path, mode := range modes {
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
	}
	return nil
}

// optimizeRemoveTree removes root even when a test left a directory without
// write permission.
func optimizeRemoveTree(root string) error {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(root)
}

func optimizeMutationRun(t *testing.T, id, action string, dry bool) optimizeMutationReport {
	t.Helper()
	args := []string{action, id}
	if dry {
		args = append(args, "--dry-run")
	}
	code, out, stderr := optimizeInvoke(t, args...)
	if code != ExitOK {
		t.Fatalf("%s: exit %d: %s", action, code, stderr)
	}
	var report optimizeMutationReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestOptimizeMutationLifecycle(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			r, original := optimizeApplyFixture(t, present)
			dir := filepath.Join(".ktx", "optimizations", r.ID)
			before, err := optimizeTreeHashes(dir, ".")
			if err != nil {
				t.Fatal(err)
			}
			preview := optimizeMutationRun(t, r.ID, "apply", true)
			if preview.Outcome != "preview" || preview.Target != r.Target.Path || preview.Diff.Field != "pipeline.default.stages" || len(preview.Diff.After) == 0 {
				t.Fatalf("preview %+v", preview)
			}
			after, err := optimizeTreeHashes(dir, ".")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("preview wrote archive files")
			}
			if !present {
				if _, err := os.Stat(filepath.Dir(r.Target.Path)); !os.IsNotExist(err) {
					t.Fatal("preview created target parent")
				}
			}
			code, text, _ := optimizeInvoke(t, "apply", r.ID, "--dry-run", "--format", "text")
			if code != 0 || !strings.Contains(text, r.Target.Path) || !strings.Contains(text, "pipeline.default.stages") || strings.Contains(text, "private settings") {
				t.Fatal("unsafe or inconsistent preview text")
			}
			applied := optimizeMutationRun(t, r.ID, "apply", false)
			if applied.Outcome != "applied" {
				t.Fatal(applied)
			}
			candidate, err := os.ReadFile(filepath.Join(dir, r.SelectedCandidate.Artifact))
			if err != nil {
				t.Fatal(err)
			}
			active, err := os.ReadFile(r.Target.Path)
			if err != nil || !bytes.Equal(active, candidate) {
				t.Fatal("not the exact qualified config")
			}
			info, err := os.Stat(r.Target.Path)
			if err != nil {
				t.Fatal(err)
			}
			wantMode := os.FileMode(0o600)
			if present {
				wantMode = 0o640
			}
			if info.Mode().Perm() != wantMode {
				t.Fatal("original mode lost")
			}
			if _, _, err := optimizeStageOnlyDiff(original, candidate); present && err != nil {
				t.Fatal("unrelated settings changed")
			}
			if present {
				backup, err := os.ReadFile(filepath.Join(dir, "application/original.toml"))
				if err != nil || !bytes.Equal(backup, original) {
					t.Fatal("original lost")
				}
				bi, _ := os.Stat(filepath.Join(dir, "application/original.toml"))
				if bi.Mode().Perm() != 0o600 {
					t.Fatal("backup not private")
				}
			}
			stableInfo := info
			if retry := optimizeMutationRun(t, r.ID, "apply", false); retry.Outcome != "noop" {
				t.Fatal("retry was not noop")
			}
			info, _ = os.Stat(r.Target.Path)
			if !os.SameFile(stableInfo, info) {
				t.Fatal("retry republished config")
			}
			shown, err := loadOptimizeResult(r.ID)
			if err != nil || shown.Application == nil || shown.Application.State != "applied" {
				t.Fatalf("show application %v %+v", err, shown.Application)
			}
			if rollback := optimizeMutationRun(t, r.ID, "rollback", false); rollback.Outcome != "rolled_back" {
				t.Fatal(rollback)
			}
			if present {
				restored, err := os.ReadFile(r.Target.Path)
				if err != nil || !bytes.Equal(restored, original) {
					t.Fatal("original bytes not restored")
				}
				info, _ = os.Stat(r.Target.Path)
				if info.Mode().Perm() != 0o640 {
					t.Fatal("original mode not restored")
				}
			} else {
				if _, err := os.Lstat(r.Target.Path); !os.IsNotExist(err) {
					t.Fatal("original absence not restored")
				}
			}
			if retry := optimizeMutationRun(t, r.ID, "rollback", false); retry.Outcome != "noop" {
				t.Fatal("rollback retry not noop")
			}
			code, _, _ = optimizeInvoke(t, "apply", r.ID)
			if code != ExitConflict {
				t.Fatal("rolled back result reused without a new cycle")
			}
		})
	}
}

func TestOptimizeMutationConflictAndEvidenceRefusal(t *testing.T) {
	for _, kind := range []string{"baseline-edit", "appeared", "offline", "selected-edit", "baseline-artifact", "qualification-missing", "stream-edit", "backup-edit", "later-edit", "mode-edit", "relabel"} {
		t.Run(kind, func(t *testing.T) {
			present := kind != "appeared"
			r, _ := optimizeApplyFixture(t, present)
			dir := filepath.Join(".ktx", "optimizations", r.ID)
			action := "apply"
			switch kind {
			case "baseline-edit":
				optimizeWrite(t, r.Target.Path, "[pipeline.default]\nstages=['head:2']\n")
			case "appeared":
				if err := os.MkdirAll(filepath.Dir(r.Target.Path), 0o700); err != nil {
					t.Fatal(err)
				}
				optimizeWrite(t, r.Target.Path, "appeared")
			case "offline":
				r = optimizeRun(t)
			case "selected-edit":
				optimizeWrite(t, filepath.Join(dir, r.SelectedCandidate.Artifact), "changed")
			case "baseline-artifact":
				optimizeWrite(t, filepath.Join(dir, "baseline.toml"), "changed")
			case "qualification-missing":
				if err := os.RemoveAll(filepath.Join(dir, "experiments/qualification")); err != nil {
					t.Fatal(err)
				}
			case "stream-edit":
				optimizeWrite(t, filepath.Join(dir, "experiments/qualification/attempts/baseline-001/stream.jsonl"), "changed")
			case "backup-edit", "later-edit", "mode-edit":
				optimizeMutationRun(t, r.ID, "apply", false)
				action = "rollback"
				if kind == "backup-edit" {
					optimizeWrite(t, filepath.Join(dir, "application/original.toml"), "changed")
				}
				if kind == "later-edit" {
					optimizeWrite(t, r.Target.Path, "later user edits")
				}
				if kind == "mode-edit" {
					if err := os.Chmod(r.Target.Path, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "relabel":
				// Copying flags onto an offline record does not supply any experiment authority.
				r = optimizeRun(t)
				r.Schema = optimizeLiveSchema
				r.Mode = "live"
				r.Outcome = "ready"
				r.ApplyEligible = true
				r.Live = &optimizeQualification{Experiment: "experiments/qualification", CompletePairs: 5, ValidationPassed: true, MinimumCompletePairs: 5}
				v := 40.0
				b := 100.0
				r.RuntimeTokens = optimizeUnavailable{State: "available", Baseline: &b, Candidate: &v}
				dir = filepath.Join(".ktx", "optimizations", r.ID)
				rewriteOptimizeReport(t, dir, r)
			}
			current, _ := os.ReadFile(r.Target.Path)
			code, _, _ := optimizeInvoke(t, action, r.ID)
			if code == ExitOK {
				t.Fatalf("%s did not refuse", kind)
			}
			after, _ := os.ReadFile(r.Target.Path)
			if !bytes.Equal(current, after) {
				t.Fatal("refusal overwrote config")
			}
		})
	}
}

func rewriteOptimizeReport(t *testing.T, dir string, r optimizeResult) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	optimizeWrite(t, filepath.Join(dir, "result.json"), string(data))
	optimizeWrite(t, filepath.Join(dir, "result.sha256"), optimizeHash(data))
}

func TestOptimizeMutationSuppliedUnrelatedSettingsRefuse(t *testing.T) {
	_, args, runner, fake := optimizeLiveFixture(t)
	// Built-in proposals touch default stages only. This supplied, stronger
	// screened proposal changes unrelated logging settings and must not publish.
	optimizeWrite(t, "target.toml", "[pipeline.default]\nstages=[]\n[logs]\nlevel='off'\n")
	optimizeWrite(t, "proposal.toml", "[pipeline.default]\nstages=['json-compact','strip-ansi','dedup-blank','normalize']\n[logs]\nlevel='debug'\n")
	d := optimizeRun(t, "--config", "target.toml", "--candidate", "proposal.toml")
	if d.Outcome != "blocked" || d.SelectedCandidate != nil || d.Candidates[2].Screen != nil {
		t.Fatal("unsupported supplied changes reached execution or selection")
	}
	r, err := qualifyOptimize(context.Background(), d, args, runner, io.Discard)
	if err != nil || r.Outcome != "blocked" || r.ApplyEligible || fake.launches != 0 || fake.capabilities != 0 {
		t.Fatalf("qualification %v %+v", err, r)
	}
	before, _ := os.ReadFile(r.Target.Path)
	code, _, stderr := optimizeInvoke(t, "apply", r.ID, "--dry-run")
	if code != ExitConflict {
		t.Fatalf("scope refusal: %d %s", code, stderr)
	}
	code, _, _ = optimizeInvoke(t, "apply", r.ID)
	if code != ExitConflict {
		t.Fatal("unrelated supplied changes applied")
	}
	after, _ := os.ReadFile(r.Target.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("unrelated config overwritten")
	}
}

func TestOptimizeMutationPathHazards(t *testing.T) {
	for _, kind := range []string{"target-symlink", "parent-symlink", "target-fifo", "hardlink", "archive-symlink", "backup-symlink"} {
		t.Run(kind, func(t *testing.T) {
			r, original := optimizeApplyFixture(t, true)
			dir := filepath.Join(".ktx", "optimizations", r.ID)
			switch kind {
			case "target-symlink":
				other := r.Target.Path + ".other"
				optimizeWrite(t, other, string(original))
				if err := os.Remove(r.Target.Path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, r.Target.Path); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				parent := filepath.Dir(r.Target.Path)
				if err := os.Rename(parent, parent+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+".saved", parent); err != nil {
					t.Fatal(err)
				}
			case "target-fifo":
				if err := os.Remove(r.Target.Path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(r.Target.Path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(r.Target.Path, r.Target.Path+".alias"); err != nil {
					t.Fatal(err)
				}
			case "archive-symlink":
				if err := os.Rename(dir, dir+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(dir)+".saved", dir); err != nil {
					t.Fatal(err)
				}
			case "backup-symlink":
				optimizeMutationRun(t, r.ID, "apply", false)
				backup := filepath.Join(dir, "application/original.toml")
				if err := os.Rename(backup, backup+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("original.toml.saved", backup); err != nil {
					t.Fatal(err)
				}
			}
			action := "apply"
			if kind == "backup-symlink" {
				action = "rollback"
			}
			code, _, _ := optimizeInvoke(t, action, r.ID)
			if code == 0 {
				t.Fatal("unsafe path accepted")
			}
		})
	}
}

func TestOptimizeMutationInterruptedPublication(t *testing.T) {
	for _, action := range []string{"apply", "rollback"} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-after-%t", action, after), func(t *testing.T) {
				r, original := optimizeApplyFixture(t, true)
				if action == "rollback" {
					optimizeMutationRun(t, r.ID, "apply", false)
				}
				failure := func() error { return errors.New("simulated interruption at atomic publication boundary") }
				hooks := optimizeMutationHooks{beforePublish: failure}
				if after {
					hooks = optimizeMutationHooks{afterPublish: failure}
				}
				if _, err := mutateOptimize(r.ID, action, false, hooks); err == nil {
					t.Fatal("injection did not fail")
				}
				active, err := os.ReadFile(r.Target.Path)
				if err != nil {
					t.Fatal(err)
				}
				candidate, err := os.ReadFile(filepath.Join(".ktx", "optimizations", r.ID, r.SelectedCandidate.Artifact))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(active, original) && !bytes.Equal(active, candidate) {
					t.Fatal("partial active config")
				}
				optimizeMutationRun(t, r.ID, action, false)
				if action == "rollback" {
					active, _ = os.ReadFile(r.Target.Path)
					if !bytes.Equal(active, original) {
						t.Fatal("rollback interruption not recoverable")
					}
				}
			})
		}
	}
}

func TestOptimizeMutationWriteFailureAndConcurrentRetry(t *testing.T) {
	r, original := optimizeApplyFixture(t, true)
	if os.Geteuid() != 0 {
		parent := filepath.Dir(r.Target.Path)
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		_, err := mutateOptimize(r.ID, "apply", false, optimizeMutationHooks{})
		if err == nil {
			t.Fatal("unwritable parent accepted")
		}
		active, _ := os.ReadFile(r.Target.Path)
		if !bytes.Equal(active, original) {
			t.Fatal("write failure changed active config")
		}
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errorsFound := make(chan error, 2)
	outcomes := make(chan string, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report, err := mutateOptimize(r.ID, "apply", false, optimizeMutationHooks{})
			errorsFound <- err
			outcomes <- report.Outcome
		}()
	}
	wg.Wait()
	close(errorsFound)
	close(outcomes)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	counts := map[string]int{}
	for outcome := range outcomes {
		counts[outcome]++
	}
	if counts["applied"] != 1 || counts["noop"] != 1 {
		t.Fatal(counts)
	}
	optimizeMutationRun(t, r.ID, "rollback", false)
}

func TestOptimizeMutationObservedEditBeforePublication(t *testing.T) {
	r, _ := optimizeApplyFixture(t, true)
	later := []byte("later editor's complete config")
	hooks := optimizeMutationHooks{beforePublish: func() error { return os.WriteFile(r.Target.Path, later, 0o640) }}
	if _, err := mutateOptimize(r.ID, "apply", false, hooks); err == nil {
		t.Fatal("observed editor change overwritten")
	}
	actual, _ := os.ReadFile(r.Target.Path)
	if !bytes.Equal(actual, later) {
		t.Fatal("later edit lost")
	}
}

func TestOptimizeMutationAbsentRollbackInterruption(t *testing.T) {
	r, _ := optimizeApplyFixture(t, false)
	optimizeMutationRun(t, r.ID, "apply", false)
	_, err := mutateOptimize(r.ID, "rollback", false, optimizeMutationHooks{afterPublish: func() error { return errors.New("interrupted after unlink") }})
	if err == nil {
		t.Fatal("interruption not injected")
	}
	if _, err := os.Lstat(r.Target.Path); !os.IsNotExist(err) {
		t.Fatal("original absence not restored atomically")
	}
	if report := optimizeMutationRun(t, r.ID, "rollback", false); report.Outcome != "noop" {
		t.Fatal(report)
	}
}

func TestOptimizeMutationCrashHelper(t *testing.T) {
	root := os.Getenv("KTX_OPTIMIZE_MUTATION_CRASH_ROOT")
	if root == "" {
		t.Skip("subprocess crash helper")
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	hook := func() error { os.Exit(99); return nil }
	hooks := optimizeMutationHooks{beforePublish: hook}
	if os.Getenv("KTX_OPTIMIZE_MUTATION_CRASH_AFTER") == "true" {
		hooks = optimizeMutationHooks{afterPublish: hook}
	}
	if _, err := mutateOptimize(os.Getenv("KTX_OPTIMIZE_MUTATION_CRASH_ID"), "apply", false, hooks); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash boundary did not execute")
}

func TestOptimizeMutationAbruptProcessExit(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			r, original := optimizeApplyFixture(t, true)
			root, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestOptimizeMutationCrashHelper$")
			cmd.Env = append(optimizeChildEnvironment(t), "KTX_OPTIMIZE_MUTATION_CRASH_ROOT="+root, "KTX_OPTIMIZE_MUTATION_CRASH_ID="+r.ID, "KTX_OPTIMIZE_MUTATION_CRASH_AFTER="+fmt.Sprint(after))
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 99 {
				t.Fatalf("crash process: %v %s", err, output)
			}
			active, err := os.ReadFile(r.Target.Path)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := os.ReadFile(filepath.Join(".ktx", "optimizations", r.ID, r.SelectedCandidate.Artifact))
			if err != nil {
				t.Fatal(err)
			}
			if (!after && !bytes.Equal(active, original)) || (after && !bytes.Equal(active, candidate)) {
				t.Fatal("abrupt exit left partial active config")
			}
			optimizeMutationRun(t, r.ID, "apply", false)
			optimizeMutationRun(t, r.ID, "rollback", false)
		})
	}
}

func optimizeChildEnvironment(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()
	env := []string{}
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch name {
		case "KTX_DB", "HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME":
			continue
		}
		env = append(env, value)
	}
	for _, name := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		env = append(env, name+"="+filepath.Join(root, name))
	}
	return env
}

func TestOptimizeMutationBuiltBinaryLifecycle(t *testing.T) {
	binary := os.Getenv("KTX_OPTIMIZE_TEST_BINARY")
	if binary == "" {
		t.Skip("set KTX_OPTIMIZE_TEST_BINARY to the locally built CLI for entrypoint smoke")
	}
	r, original := optimizeApplyFixture(t, true)
	invoke := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"optimize"}, args...)...)
		cmd.Env = optimizeChildEnvironment(t)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		data, err := cmd.Output()
		if err != nil {
			t.Fatalf("local CLI %v: %v %s", args, err, stderr.String())
		}
		return data
	}
	for _, args := range [][]string{{"apply", "--help"}, {"rollback", "--help"}, {"apply", r.ID, "--dry-run"}, {"apply", r.ID}, {"apply", r.ID}, {"show", r.ID}, {"rollback", r.ID}, {"rollback", r.ID}} {
		data := invoke(args...)
		if args[0] == "show" {
			var shown optimizeResult
			if err := json.Unmarshal(data, &shown); err != nil || shown.Application == nil || shown.Application.State != "applied" {
				t.Fatal("native show did not expose application state")
			}
		}
	}
	active, _ := os.ReadFile(r.Target.Path)
	if !bytes.Equal(active, original) {
		t.Fatal("native lifecycle did not restore exact original")
	}
	for _, args := range [][]string{{"apply", r.ID}, {"apply", r.ID, "--config", r.Target.Path}, {"rollback", r.ID, "--dry-run"}} {
		cmd := exec.Command(binary, append([]string{"optimize"}, args...)...)
		cmd.Env = optimizeChildEnvironment(t)
		output, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("native refusal unexpectedly passed: %v %s", args, output)
		}
		want := ExitUsage
		if len(args) == 2 {
			want = ExitConflict
		}
		if exit.ExitCode() != want {
			t.Fatalf("native refusal %v exit %d, want %d", args, exit.ExitCode(), want)
		}
	}
	t.Log("local built CLI eligible lifecycle used simulated retained qualification, not real model evidence")
}

func TestOptimizeNextCycleUsesAppliedIncumbent(t *testing.T) {
	r, _ := optimizeApplyFixture(t, true)
	optimizeMutationRun(t, r.ID, "apply", false)
	dir := filepath.Join(".ktx", "optimizations", r.ID)
	before, err := optimizeTreeHashes(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(r.Target.Path)
	if err != nil {
		t.Fatal(err)
	}
	next := optimizeRun(t, "--config", r.Target.Path)
	if next.ID == r.ID || next.Target.SHA256 == nil || *next.Target.SHA256 != optimizeHash(active) || next.Baseline.SHA256 != r.SelectedCandidate.SHA256 {
		t.Fatal("next cycle did not evaluate current incumbent")
	}
	after, err := optimizeTreeHashes(dir, ".")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("next cycle rewrote previous evidence")
	}
	optimizeMutationRun(t, r.ID, "rollback", false)
	shown, err := loadOptimizeResult(r.ID)
	if err != nil || shown.ApplyEligible {
		t.Fatal("retired result advertised apply eligibility")
	}
	for _, command := range shown.NextCommands {
		if strings.Contains(command, "optimize apply") {
			t.Fatal("retired result recommends apply")
		}
	}
	code, _, _ := optimizeInvoke(t, "apply", next.ID)
	if code != ExitConflict {
		t.Fatal("offline second-cycle screen became apply-ready")
	}
}
