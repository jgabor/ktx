package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

const hookDispatchHelperEnv = "KTX_HOOK_DISPATCH_HELPER"

const (
	hookDispatchStageEnv     = "KTX_HOOK_DISPATCH_STAGE"
	hookDispatchStageReady   = "KTX_HOOK_DISPATCH_STAGE_READY"
	hookDispatchStageRelease = "KTX_HOOK_DISPATCH_STAGE_RELEASE"
)

func TestHookDispatchProcessHelper(t *testing.T) {
	if os.Getenv(hookDispatchHelperEnv) != "1" {
		return
	}
	var stdout, stderr bytes.Buffer
	code := runWith([]string{"hook"}, &stdout, &stderr)
	want, err := strconv.Atoi(os.Getenv("KTX_HOOK_DISPATCH_EXIT"))
	if err != nil {
		t.Fatal(err)
	}
	if code != want {
		t.Fatalf("hook exit = %d, want %d; stdout=%q stderr=%q", code, want, stdout.String(), stderr.String())
	}
	if want == ExitSchemaIncompatible {
		var envelope struct {
			Error struct {
				Code          string `json:"code"`
				ActualSchema  int    `json:"actual_schema"`
				MaximumSchema int    `json:"maximum_schema"`
			} `json:"error"`
		}
		if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
			t.Fatalf("decode schema-incompatible result: %v; stderr=%q", err, stderr.String())
		}
		if stdout.Len() != 0 || envelope.Error.Code != string(CodeSchemaIncompatible) ||
			envelope.Error.ActualSchema != dbpkg.MaxSupportedSchema()+1 || envelope.Error.MaximumSchema != dbpkg.MaxSupportedSchema() {
			t.Fatalf("schema-incompatible result stdout=%q error=%+v", stdout.String(), envelope.Error)
		}
	}
}

func TestHookDispatchStageHelper(t *testing.T) {
	if os.Getenv(hookDispatchStageEnv) != "1" {
		return
	}
	ready := os.Getenv(hookDispatchStageReady)
	release := os.Getenv(hookDispatchStageRelease)
	if ready == "" || release == "" {
		t.Fatal("dispatch stage paths are required")
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForHookDispatchFile(t, release, 5*time.Second)
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(os.Stdout, "compressed\n"); err != nil {
		t.Fatal(err)
	}
}

func TestHookDispatchCoordinationWindows(t *testing.T) {
	t.Run("current schema changing hook", func(t *testing.T) {
		root := newHookDispatchRoot(t)
		initializeHookDispatchDatabase(t, root.database)
		writeHookDispatchConfig(t, root, "[pipeline.default]\nstages = [\"head:1\"]\n")
		original := strings.Repeat("first\nsecond\n", 20)
		payload := openCodeHookPayload(t, "dispatch-changing-session", "dispatch-changing-call", "bash", `{"command":"true"}`, original)
		audit := root.audit("changing")
		runHookDispatchProcess(t, root, audit, payload, ExitOK)
		assertExclusiveCoordinationCount(t, audit, 1)

		database, err := dbpkg.Open(root.database)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		assertHookEvidenceCounts(t, database, mustOpenCodeID(t, "dispatch-changing-session", "dispatch-changing-call"), 1, 1, 1)
	})

	t.Run("pre-source rejection", func(t *testing.T) {
		root := newHookDispatchRoot(t)
		initializeHookDispatchDatabase(t, root.database)
		audit := root.audit("rejected")
		runHookDispatchProcess(t, root, audit, `{"hook_event_name":"Unsupported"}`, ExitUsage)
		assertExclusiveCoordinationCount(t, audit, 0)
	})

	t.Run("complete retry", func(t *testing.T) {
		root := newHookDispatchRoot(t)
		initializeHookDispatchDatabase(t, root.database)
		payload := openCodeHookPayload(t, "dispatch-retry-session", "dispatch-retry-call", "bash", `{"command":"true"}`, "original")
		firstAudit := root.audit("retry-first")
		runHookDispatchProcess(t, root, firstAudit, payload, ExitOK)
		assertExclusiveCoordinationCount(t, firstAudit, 1)

		retryAudit := root.audit("retry-complete")
		runHookDispatchProcess(t, root, retryAudit, payload, ExitOK)
		assertExclusiveCoordinationCount(t, retryAudit, 0)
	})

	t.Run("newer schema fails closed", func(t *testing.T) {
		root := newHookDispatchRoot(t)
		seedSchemaVersion(t, root.database, dbpkg.MaxSupportedSchema()+1)
		audit := root.audit("newer")
		payload := openCodeHookPayload(t, "dispatch-newer-session", "dispatch-newer-call", "bash", `{"command":"true"}`, "original")
		runHookDispatchProcess(t, root, audit, payload, ExitSchemaIncompatible)
		assertExclusiveCoordinationCount(t, audit, 0)
	})
}

func TestHookDispatchFallbackRaces(t *testing.T) {
	for _, test := range []struct {
		name string
		seed func(*testing.T, string)
	}{
		{name: "absent"},
		{name: "stale", seed: func(t *testing.T, path string) {
			seedSchemaVersion(t, path, dbpkg.MaxSupportedSchema()-1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newHookDispatchRoot(t)
			if test.seed != nil {
				test.seed(t, root.database)
			}
			const processes = 4
			audit := root.audit(test.name)
			var wait sync.WaitGroup
			errorsCh := make(chan error, processes)
			for i := 0; i < processes; i++ {
				wait.Add(1)
				go func(i int) {
					defer wait.Done()
					payload := openCodeHookPayload(t, fmt.Sprintf("dispatch-%s-session-%d", test.name, i), fmt.Sprintf("dispatch-%s-call-%d", test.name, i), "bash", `{"command":"true"}`, "original")
					if err := runHookDispatchCommand(root, audit, payload, ExitOK); err != nil {
						errorsCh <- err
					}
				}(i)
			}
			wait.Wait()
			close(errorsCh)
			for err := range errorsCh {
				t.Error(err)
			}
			if t.Failed() {
				return
			}
			windows := exclusiveCoordinationCount(t, audit)
			if windows < processes+1 || windows > processes*2 {
				t.Fatalf("exclusive coordination windows = %d, want %d..%d", windows, processes+1, processes*2)
			}

			database, err := dbpkg.Open(root.database)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			version, err := database.SchemaVersion()
			if err != nil || version != dbpkg.MaxSupportedSchema() {
				t.Fatalf("schema version = %d, %v", version, err)
			}
			var calls int
			if err := database.DB().QueryRow(`SELECT count(*) FROM tool_calls`).Scan(&calls); err != nil {
				t.Fatal(err)
			}
			if calls != processes {
				t.Fatalf("tool calls = %d, want %d", calls, processes)
			}
		})
	}
}

func TestHookDispatchRejectsSchemaUpgradeBeforeWriterOwnership(t *testing.T) {
	root := newHookDispatchRoot(t)
	initializeHookDispatchDatabase(t, root.database)
	writeHookDispatchConfig(t, root, fmt.Sprintf(`[pipeline.default]
stages = ["dispatch-schema-race:-test.run=^TestHookDispatchStageHelper$"]

[[plugin]]
name = "dispatch-schema-race"
command = %q
`, os.Args[0]))
	ready := filepath.Join(root.root, "stage-ready")
	release := filepath.Join(root.root, "stage-release")
	t.Setenv(hookDispatchStageEnv, "1")
	t.Setenv(hookDispatchStageReady, ready)
	t.Setenv(hookDispatchStageRelease, release)

	payload := openCodeHookPayload(t, "dispatch-upgrade-session", "dispatch-upgrade-call", "bash", `{"command":"true"}`, strings.Repeat("first\nsecond\n", 20))
	sourceID := mustOpenCodeID(t, "dispatch-upgrade-session", "dispatch-upgrade-call")
	audit := root.audit("concurrent-upgrade")
	command, output := newHookDispatchCommand(root, audit, payload, ExitSchemaIncompatible)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	waitForHookDispatchFile(t, ready, 5*time.Second)

	upgrader, err := dbpkg.OpenHook(root.database)
	if err != nil {
		t.Fatal(err)
	}
	err = upgrader.WithHookWriter(func(*dbpkg.HookWriter) error {
		_, err := upgrader.DB().Exec(`PRAGMA user_version = ` + strconv.Itoa(dbpkg.MaxSupportedSchema()+1))
		return err
	})
	if closeErr := upgrader.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("coordinated schema upgrade: %v", err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("hook dispatch helper: %v: %s", err, output.String())
	}
	waited = true
	assertExclusiveCoordinationCount(t, audit, 1)

	raw, err := sql.Open("sqlite", "file:"+root.database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for table, query := range map[string]string{
		"tool_calls":               `SELECT count(*) FROM tool_calls WHERE id = ?`,
		"compressed":               `SELECT count(*) FROM compressed WHERE source_id = ?`,
		"compression_dispositions": `SELECT count(*) FROM compression_dispositions WHERE source_id = ?`,
	} {
		var count int
		if err := raw.QueryRow(query, sourceID).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s rows = %d, %v; want 0", table, count, err)
		}
	}
	var sessions, rollup int
	if err := raw.QueryRow(`SELECT count(*), COALESCE(sum(tool_call_count), 0) FROM sessions`).Scan(&sessions, &rollup); err != nil || sessions != 0 || rollup != 0 {
		t.Errorf("session evidence count=%d rollup=%d err=%v; want zero", sessions, rollup, err)
	}
}

type hookDispatchRoot struct {
	root, home, data, config, cache, state, runtime, temporary, database string
}

func newHookDispatchRoot(t *testing.T) hookDispatchRoot {
	t.Helper()
	root := t.TempDir()
	result := hookDispatchRoot{
		root: root, home: filepath.Join(root, "home"), data: filepath.Join(root, "data"),
		config: filepath.Join(root, "config"), cache: filepath.Join(root, "cache"), state: filepath.Join(root, "state"),
		runtime: filepath.Join(root, "runtime"), temporary: filepath.Join(root, "tmp"),
	}
	result.database = filepath.Join(result.data, "ktx", "ktx.db")
	for _, directory := range []string{result.home, result.data, result.config, result.cache, result.state, result.runtime, result.temporary, filepath.Dir(result.database)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func (r hookDispatchRoot) audit(name string) string {
	return filepath.Join(r.root, "audit-"+name+".jsonl")
}

func initializeHookDispatchDatabase(t *testing.T, path string) {
	t.Helper()
	database, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeHookDispatchConfig(t *testing.T, root hookDispatchRoot, content string) {
	t.Helper()
	directory := filepath.Join(root.config, "ktx")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "ktx.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runHookDispatchProcess(t *testing.T, root hookDispatchRoot, audit, payload string, wantExit int) {
	t.Helper()
	if err := runHookDispatchCommand(root, audit, payload, wantExit); err != nil {
		t.Fatal(err)
	}
}

func runHookDispatchCommand(root hookDispatchRoot, audit, payload string, wantExit int) error {
	command, output := newHookDispatchCommand(root, audit, payload, wantExit)
	if err := command.Run(); err != nil {
		return fmt.Errorf("hook dispatch helper: %w: %s", err, output.String())
	}
	return nil
}

func newHookDispatchCommand(root hookDispatchRoot, audit, payload string, wantExit int) (*exec.Cmd, *bytes.Buffer) {
	command := exec.Command(os.Args[0], "-test.run=^TestHookDispatchProcessHelper$")
	command.Env = append(hookDispatchBaseEnvironment(),
		hookDispatchHelperEnv+"=1",
		"KTX_HOOK_DISPATCH_EXIT="+strconv.Itoa(wantExit),
		"HOME="+root.home,
		"XDG_DATA_HOME="+root.data,
		"XDG_CONFIG_HOME="+root.config,
		"XDG_CACHE_HOME="+root.cache,
		"XDG_STATE_HOME="+root.state,
		"XDG_RUNTIME_DIR="+root.runtime,
		"TMPDIR="+root.temporary,
		"KTX_VERIFY_ROOT="+root.root,
		"KTX_VERIFY_DB_AUDIT="+audit,
		"KTX_VERIFY_FORBIDDEN_DB_PATHS=[]",
	)
	command.Stdin = strings.NewReader(payload)
	output := new(bytes.Buffer)
	command.Stdout = output
	command.Stderr = output
	return command, output
}

func hookDispatchBaseEnvironment() []string {
	blocked := []string{"KTX_DB=", "KTX_SESSION_ID=", hookDispatchHelperEnv + "=", "KTX_HOOK_DISPATCH_EXIT="}
	result := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		keep := true
		for _, prefix := range blocked {
			if strings.HasPrefix(entry, prefix) {
				keep = false
				break
			}
		}
		if keep {
			result = append(result, entry)
		}
	}
	return result
}

func assertExclusiveCoordinationCount(t *testing.T, path string, want int) {
	t.Helper()
	if got := exclusiveCoordinationCount(t, path); got != want {
		t.Fatalf("exclusive coordination windows = %d, want %d", got, want)
	}
}

func exclusiveCoordinationCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	count := 0
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var record struct {
			Operation string `json:"operation"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode database audit: %v: %q", err, line)
		}
		if record.Operation == "exclusive_coordination" {
			count++
		}
	}
	return count
}

func waitForHookDispatchFile(t *testing.T, path string, timeout time.Duration) {
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
