//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestDatabaseLoggingLockProcessHelper(t *testing.T) {
	if os.Getenv("KTX_DATABASE_LOGGING_LOCK_HELPER") != "1" {
		return
	}
	file, err := os.OpenFile(os.Getenv("KTX_DB"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if err := os.WriteFile(os.Getenv("KTX_DATABASE_LOGGING_READY"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForHookStartupFile(t, os.Getenv("KTX_DATABASE_LOGGING_RELEASE"), 15*time.Second)
}

func TestGetDatabaseUnavailableLoggingEndToEnd(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "ktx.db")
	database, err := dbpkg.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	databaseBefore, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	ready, release := databasePath+".ready", databasePath+".release"
	holder := exec.Command(os.Args[0], "-test.run=^TestDatabaseLoggingLockProcessHelper$")
	holder.Env = append(os.Environ(),
		"KTX_DATABASE_LOGGING_LOCK_HELPER=1",
		"KTX_DATABASE_LOGGING_READY="+ready,
		"KTX_DATABASE_LOGGING_RELEASE="+release,
		"KTX_DB="+databasePath,
	)
	var holderOutput bytes.Buffer
	holder.Stdout, holder.Stderr = &holderOutput, &holderOutput
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = os.WriteFile(release, nil, 0o600)
			_ = holder.Wait()
		}
	})
	waitForHookStartupFile(t, ready, 5*time.Second)

	t.Setenv("KTX_DB", databasePath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	run := func() (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"get", "aaaaaaaaaaaaaaaa"}, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	baselineCode, baselineStdout, baselineStderr := run()
	if baselineCode != ExitInternal || baselineStdout != "" || baselineStderr == "" {
		t.Fatalf("baseline exit=%d stdout=%q stderr=%q", baselineCode, baselineStdout, baselineStderr)
	}
	defaultLogPath := filepath.Join(root, "state", "ktx", "ktx.log")
	if _, err := os.Stat(defaultLogPath); !os.IsNotExist(err) {
		t.Fatalf("no-config log exists: %v", err)
	}
	elapsedField := regexp.MustCompile(`"elapsed_wait_nanoseconds":[0-9]+`)
	stableStderr := func(stderr string) string {
		return elapsedField.ReplaceAllString(stderr, `"elapsed_wait_nanoseconds":<elapsed>`)
	}

	logPath := filepath.Join(root, "events.jsonl")
	hookWriteUserConfig(t, "[logs]\nlevel = \"error\"\npath = \""+logPath+"\"\n")
	code, stdout, stderr := run()
	if code != baselineCode || stdout != baselineStdout || stableStderr(stderr) != stableStderr(baselineStderr) {
		t.Fatalf("enabled behavior changed: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	record := readDatabaseUnavailableLog(t, logPath)
	if record.Command != "get" || record.FailureClass != dbpkg.UnavailableClassCoordinatorTimeout || record.OperationPhase != dbpkg.OperationOpen {
		t.Fatalf("record = %#v", record)
	}
	if record.CoordinatorRequestedMode != "shared" || record.CoordinatorSnapshotAvailable == nil {
		t.Fatalf("coordinator attribution = %#v", record)
	}
	if runtime.GOOS == "linux" {
		if !*record.CoordinatorSnapshotAvailable || len(record.CoordinatorBlockers) != 1 ||
			record.CoordinatorBlockers[0].PID != holder.Process.Pid || record.CoordinatorBlockers[0].Mode != "exclusive" ||
			record.CoordinatorBlockers[0].Command != "unknown" {
			t.Fatalf("linux blockers = %#v, holder pid %d", record, holder.Process.Pid)
		}
	} else if *record.CoordinatorSnapshotAvailable || len(record.CoordinatorBlockers) != 0 {
		t.Fatalf("non-linux snapshot = %#v", record)
	}

	for name, configBody := range map[string]string{
		"off":     "[logs]\nlevel = \"off\"\n",
		"invalid": "[logs]\nunknown = true\n",
		"failed":  "[logs]\nlevel = \"error\"\npath = \"" + filepath.Join(root, "blocked", "events.jsonl") + "\"\n",
		"unsafe":  "[logs]\nlevel = \"error\"\npath = \"" + databasePath + "\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if name == "failed" {
				if err := os.WriteFile(filepath.Join(root, "blocked"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			hookWriteUserConfig(t, configBody)
			if code, stdout, stderr := run(); code != baselineCode || stdout != baselineStdout || stableStderr(stderr) != stableStderr(baselineStderr) {
				t.Fatalf("behavior changed: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if name == "off" || name == "invalid" {
				if _, err := os.Stat(defaultLogPath); !os.IsNotExist(err) {
					t.Fatalf("default log exists: %v", err)
				}
			}
			if name == "failed" {
				if info, err := os.Stat(filepath.Join(root, "blocked")); err != nil || info.Size() != 0 {
					t.Fatalf("failed sink changed: info=%v err=%v", info, err)
				}
			}
		})
	}
	databaseAfter, err := os.ReadFile(databasePath)
	if err != nil || !bytes.Equal(databaseAfter, databaseBefore) {
		t.Fatalf("unsafe sink changed database: err=%v", err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("lock holder: %v\n%s", err, holderOutput.String())
	}
	released = true
}
