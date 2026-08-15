package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// TestHookStartupLockProcessHelper supplies separate processes for the lock
// holder and the real hook command. It is invoked only by
// TestHookRetriesStartupLock.
func TestHookStartupLockProcessHelper(t *testing.T) {
	if os.Getenv("KTX_HOOK_STARTUP_HELPER") != "1" {
		return
	}

	switch os.Getenv("KTX_HOOK_STARTUP_ROLE") {
	case "hold-exclusive":
		database, err := sql.Open("sqlite", "file:"+os.Getenv("KTX_DB")+"?_pragma=busy_timeout(1)")
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		database.SetMaxOpenConns(1)
		if _, err := database.Exec(`BEGIN EXCLUSIVE`); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("KTX_HOOK_STARTUP_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForHookStartupFile(t, os.Getenv("KTX_HOOK_STARTUP_RELEASE"), 10*time.Second)
		if _, err := database.Exec(`COMMIT`); err != nil {
			t.Fatal(err)
		}
	case "hook":
		if code := Run([]string{"hook"}); code != ExitOK {
			t.Fatalf("hook exit code = %d, want %d", code, ExitOK)
		}
	default:
		t.Fatalf("unknown helper role %q", os.Getenv("KTX_HOOK_STARTUP_ROLE"))
	}
}

func TestHookRetriesStartupLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hook-startup-lock.db")
	initialized, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}

	ready := path + ".ready"
	release := path + ".release"
	holder, holderOutput := startHookStartupHelper(t, "hold-exclusive", path, ready, release)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		_ = holder.Wait()
	})
	waitForHookStartupFile(t, ready, 5*time.Second)

	hook, hookOutput := startHookStartupHelper(t, "hook", path, "", "")
	hook.Stdin = bytes.NewBufferString(`{"id":"hookstartuplocka","tool":"bash","input":"true","output":"ok","timestamp":"2026-07-21T00:00:00Z"}` + "\n")
	started := time.Now()
	if err := hook.Start(); err != nil {
		t.Fatal(err)
	}
	// Hold longer than the configured driver busy timeout, then release within
	// the two-attempt startup budget.
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := hook.Wait(); err != nil {
		t.Fatalf("ktx hook under startup lock: %v\nhook output: %s\nholder output: %s", err, hookOutput.String(), holderOutput.String())
	}
	// Include one driver busy-wait beyond the retry deadline for process startup
	// and race-instrumentation scheduling overhead. The attempt cap remains the
	// production bound on SQLite waits.
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("hook startup retry exceeded bound: elapsed %s", elapsed)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("exclusive lock holder: %v\n%s", err, holderOutput.String())
	}
}

func TestHookStartupLockFailureRendersDatabaseUnavailable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "hook-startup-lock-failure.db")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	logPath := filepath.Join(root, "hook-errors.jsonl")
	hookWriteUserConfig(t, "[logs]\nlevel = \"error\"\npath = \""+logPath+"\"\n")
	initialized, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}

	ready := path + ".ready"
	release := path + ".release"
	holder, _ := startHookStartupHelper(t, "hold-exclusive", path, ready, release)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		_ = holder.Wait()
	})
	waitForHookStartupFile(t, ready, 5*time.Second)

	t.Setenv("KTX_DB", path)
	var stdout, stderr bytes.Buffer
	if code := runWith([]string{"hook"}, &stdout, &stderr); code != ExitInternal {
		t.Fatalf("hook exit code = %d, want %d; stdout=%q stderr=%q", code, ExitInternal, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("hook stdout = %q, want empty", stdout.String())
	}

	var envelope struct {
		Error struct {
			Code         string `json:"code"`
			DatabasePath string `json:"database_path"`
			Correction   string `json:"correction"`
			Message      string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v\nstderr=%s", err, stderr.String())
	}
	if envelope.Error.Code != string(CodeDatabaseUnavailable) {
		t.Errorf("error code = %q, want %q", envelope.Error.Code, CodeDatabaseUnavailable)
	}
	wantPath, err := dbpkg.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Error.DatabasePath != wantPath {
		t.Errorf("database_path = %q, want %q", envelope.Error.DatabasePath, wantPath)
	}
	if envelope.Error.Correction == "" {
		t.Error("correction is empty")
	}
	for _, forbidden := range []string{"database is locked", "sqlite_busy", "sqlite_locked"} {
		if strings.Contains(strings.ToLower(stderr.String()), forbidden) {
			t.Errorf("stderr exposes driver lock wording %q: %q", forbidden, stderr.String())
		}
	}
	record := readDatabaseUnavailableLog(t, logPath)
	if record.Command != "hook" || record.FailureClass != dbpkg.UnavailableClassSQLiteLock || record.OperationPhase != dbpkg.OperationOpen || record.ElapsedWaitNanoseconds <= 0 {
		t.Fatalf("availability record = %#v", record)
	}
}

func startHookStartupHelper(t *testing.T, role, path, ready, release string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookStartupLockProcessHelper$")
	cmd.Env = append(os.Environ(),
		"KTX_HOOK_STARTUP_HELPER=1",
		"KTX_HOOK_STARTUP_ROLE="+role,
		"KTX_DB="+path,
		"XDG_CONFIG_HOME="+t.TempDir(),
		"KTX_HOOK_STARTUP_READY="+ready,
		"KTX_HOOK_STARTUP_RELEASE="+release,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	return cmd, &output
}

func waitForHookStartupFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
