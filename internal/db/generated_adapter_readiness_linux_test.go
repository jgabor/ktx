//go:build linux

package db

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/identity"
)

const generatedAdapterCompressedRows = compressedTimestampMigrationBatch + 1

type capturedProcess struct {
	command *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	done    chan error
}

type generatedAdapterFixture struct {
	root        string
	database    string
	binary      string
	bun         string
	driver      string
	ready       string
	start       string
	callbacks   string
	digest      string
	observation string
	env         []string
}

func TestGeneratedOpenCodeAdapterReadinessOverlap(t *testing.T) {
	binary := builtKTXBinary(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("KTX_TEST_REQUIRE_GENERATED_ADAPTER") == "1" {
			t.Fatal("bun is required to execute the generated OpenCode adapter")
		}
		t.Skip("bun is required to execute the generated OpenCode adapter")
	}

	t.Run("query owner hands readiness to generated hook and telemetry", func(t *testing.T) {
		fixture := newGeneratedAdapterFixture(t, binary, bun)
		adapter, query := runGeneratedAdapterOverlap(t, fixture, true)
		if query.command.ProcessState.ExitCode() != 0 || adapter.command.ProcessState.ExitCode() != 0 {
			t.Fatalf("success exits: query=%d adapter=%d", query.command.ProcessState.ExitCode(), adapter.command.ProcessState.ExitCode())
		}
		assertNoReadinessLeak(t, query.stderr.String()+adapter.stderr.String())
		assertAdapterOutput(t, adapter.stdout.Bytes())
		assertStableQueryOutput(t, fixture, query.stdout.Bytes())
		assertGeneratedAdapterEvidence(t, fixture.database)
	})

	t.Run("coordination deadline is attributed and retry recovers", func(t *testing.T) {
		fixture := newGeneratedAdapterFixture(t, binary, bun)
		adapter, query := runGeneratedAdapterOverlap(t, fixture, false)
		if query.command.ProcessState.ExitCode() != 4 || adapter.command.ProcessState.ExitCode() != 0 {
			t.Fatalf("failure exits: query=%d adapter=%d", query.command.ProcessState.ExitCode(), adapter.command.ProcessState.ExitCode())
		}
		if len(bytes.TrimSpace(query.stdout.Bytes())) != 0 {
			t.Fatalf("failed overlap query wrote stdout: %s", query.stdout.Bytes())
		}
		assertDatabaseUnavailableEnvelope(t, query.stderr.String(), fixture, "")
		assertDatabaseUnavailableEnvelope(t, adapter.stderr.String(), fixture, "opencode-hook", "opencode-telemetry")
		assertNoReadinessLeak(t, query.stderr.String()+adapter.stderr.String())
		assertAdapterOutput(t, adapter.stdout.Bytes())
		assertGeneratedAdapterFailureLeftNoEvidence(t, fixture.database)

		adapter, query = runGeneratedAdapterOverlap(t, fixture, true)
		if query.command.ProcessState.ExitCode() != 0 || adapter.command.ProcessState.ExitCode() != 0 {
			t.Fatalf("recovery exits: query=%d adapter=%d\nquery stderr=%s\nadapter stderr=%s", query.command.ProcessState.ExitCode(), adapter.command.ProcessState.ExitCode(), query.stderr.String(), adapter.stderr.String())
		}
		assertNoReadinessLeak(t, query.stderr.String()+adapter.stderr.String())
		assertAdapterOutput(t, adapter.stdout.Bytes())
		assertStableQueryOutput(t, fixture, query.stdout.Bytes())
		assertGeneratedAdapterEvidence(t, fixture.database)
	})
}

func TestGeneratedOpenCodeAdapterProductionReadiness(t *testing.T) {
	database := os.Getenv("KTX_TEST_SCHEMA15_DATABASE")
	if database == "" {
		t.Skip("KTX_TEST_SCHEMA15_DATABASE is required for the production-scale release rehearsal")
	}
	expectedRows, err := strconv.ParseInt(os.Getenv("KTX_TEST_SCHEMA15_EXPECTED_ROWS"), 10, 64)
	if err != nil || expectedRows <= 0 {
		t.Fatal("KTX_TEST_SCHEMA15_EXPECTED_ROWS must be a positive integer")
	}
	binary := builtKTXBinary(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal("bun is required to execute the generated OpenCode adapter")
	}

	fixture := newGeneratedAdapterFixture(t, binary, bun)
	fixture.database = database
	fixture.observation = os.Getenv("KTX_TEST_COMBINED_OBSERVATION")
	fixture.env = replaceEnvironment(fixture.env, "KTX_DB", database)
	fixture.env = replaceEnvironment(fixture.env, "KTX_VERIFY_ROOT", filepath.Dir(database))
	raw, err := sql.Open(sqliteDriver, "file:"+database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO sessions (id,agent,started_at,last_seen_at,tool_call_count,agent_attribution_authority)
		VALUES ('adapterownersess','opencode','2026-08-24T00:00:00Z','2026-08-24T00:00:00Z',0,'identified')`); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	adapter, query := runGeneratedAdapterOverlap(t, fixture, true)
	if query.command.ProcessState.ExitCode() != 0 || adapter.command.ProcessState.ExitCode() != 0 {
		t.Fatalf("production exits: query=%d adapter=%d\nquery stderr=%s\nadapter stderr=%s", query.command.ProcessState.ExitCode(), adapter.command.ProcessState.ExitCode(), query.stderr.String(), adapter.stderr.String())
	}
	assertNoReadinessLeak(t, query.stderr.String()+adapter.stderr.String())
	assertAdapterOutput(t, adapter.stdout.Bytes())
	counts := querySessionCountsAny(t, query.stdout.Bytes())
	if counts["ssssssssssssssss"] != expectedRows || (counts["adapterownersess"] != 0 && counts["adapterownersess"] != 1) {
		t.Fatalf("production overlap query counts = %v", counts)
	}
}

func builtKTXBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("KTX_TEST_BUILT_BINARY")
	if path == "" {
		path = filepath.Join("..", "..", "build", "ktx")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absolute); errors.Is(err, os.ErrNotExist) {
		if os.Getenv("KTX_TEST_REQUIRE_GENERATED_ADAPTER") == "1" {
			t.Fatalf("built KTX binary is required at %s", absolute)
		}
		t.Skip("build/ktx is required; run mage build before this test")
	} else if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func newGeneratedAdapterFixture(t *testing.T, binary, bun string) generatedAdapterFixture {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"home", "data", "config", "cache", "state", "runtime", "project"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	database := filepath.Join(root, "data", "readiness.db")
	raw := openHistoricalSchema(t, database, 15)
	seedGeneratedAdapterSchema15(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	env := withoutEnvironment(os.Environ(), "KTX_DB", "HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "PATH")
	env = append(env,
		"KTX_DB="+database,
		"HOME="+filepath.Join(root, "home"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"),
		"KTX_VERIFY_ROOT="+root,
		"KTX_VERIFY_FORBIDDEN_DB_PATHS=[]",
		"PATH="+filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	project := filepath.Join(root, "project")
	initCommand := exec.Command(binary, "init", "opencode", "--project")
	initCommand.Dir = project
	initCommand.Env = env
	if output, err := initCommand.CombinedOutput(); err != nil {
		t.Fatalf("generate OpenCode adapter: %v\n%s", err, output)
	}
	plugin := filepath.Join(project, ".opencode", "plugins", "ktx.ts")
	if _, err := os.Stat(plugin); err != nil {
		t.Fatalf("generated adapter: %v", err)
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(binaryBytes))

	ready := filepath.Join(root, "adapter-ready")
	start := filepath.Join(root, "adapter-start")
	callbacks := filepath.Join(root, "callbacks-started")
	driver := filepath.Join(root, "driver.ts")
	source := `import plugin from ` + strconv.Quote(plugin) + `
const telemetryHooks: any = await plugin({ $: () => ({ quiet: async () => {} }) } as any)
const toolHooks: any = await plugin({ $: () => ({ quiet: async () => {} }) } as any)
if (!telemetryHooks.event || !toolHooks["tool.execute.after"]) throw new Error("generated hooks unavailable")
await Bun.write(process.env.KTX_TEST_ADAPTER_READY!, "ready")
while (!(await Bun.file(process.env.KTX_TEST_ADAPTER_START!).exists())) await Bun.sleep(5)
const identity = {type:"message.updated",properties:{info:{id:"message-overlap",sessionID:"session-overlap",role:"assistant",providerID:"openai",modelID:"gpt-5"}}}
const step = {type:"message.part.updated",properties:{part:{id:"part-overlap",sessionID:"session-overlap",messageID:"message-overlap",type:"step-finish",reason:"stop",cost:1.25,tokens:{input:11,output:7,reasoning:3,cache:{read:5,write:2}}}}}
await telemetryHooks.event({event:identity})
const output: any = {title:"tool",output:"adapter overlap output",metadata:{kept:true}}
await Bun.write(process.env.KTX_TEST_CALLBACKS_STARTED!, "started")
await Promise.all([
  telemetryHooks.event({event:step}),
  toolHooks["tool.execute.after"]({tool:"bash",sessionID:"session-overlap",callID:"call-overlap",args:{command:"true"}}, output),
])
console.log(JSON.stringify(output))
`
	if err := os.WriteFile(driver, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	env = append(env, "KTX_TEST_ADAPTER_READY="+ready, "KTX_TEST_ADAPTER_START="+start, "KTX_TEST_CALLBACKS_STARTED="+callbacks)
	return generatedAdapterFixture{root: root, database: database, binary: binary, bun: bun, driver: driver, ready: ready, start: start, callbacks: callbacks, digest: digest, env: env}
}

func seedGeneratedAdapterSchema15(t *testing.T, raw *sql.DB) {
	t.Helper()
	if _, err := raw.Exec(`INSERT INTO sessions (id,agent,started_at,last_seen_at,tool_call_count,agent_attribution_authority)
		VALUES ('historicalsessxx','fixture','2026-08-24T00:00:00Z','2026-08-24T00:00:01Z',1,'unknown'),
		       ('adapterownersess','opencode','2026-08-24T00:00:00Z','2026-08-24T00:00:00Z',0,'identified');
		INSERT INTO tool_calls (id,session_id,tool,input,output,content_hash,timestamp,event_type,runtime,producer,timestamp_unix_nano)
		VALUES ('historicalsource','historicalsessxx','bash','{}','historical output','historical-hash','2026-08-24T00:00:01Z','tool_call','opencode','hook',1787529601000000000)`); err != nil {
		t.Fatal(err)
	}
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < generatedAdapterCompressedRows; i++ {
		if _, err := tx.Exec(`INSERT INTO compressed (id,source_id,output,content_hash,pipeline,timestamp,producer) VALUES (?,?,?,?,?,?,?)`,
			fmt.Sprintf("h%015d", i), "historicalsource", fmt.Sprintf("historical-%d", i), fmt.Sprintf("hash-%d", i), `{}`, fmt.Sprintf("2026-08-24T00:00:01.%09dZ", i+1), "hook"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func runGeneratedAdapterOverlap(t *testing.T, fixture generatedAdapterFixture, release bool) (*capturedProcess, *capturedProcess) {
	t.Helper()
	_ = os.Remove(fixture.ready)
	_ = os.Remove(fixture.start)
	_ = os.Remove(fixture.callbacks)

	lockFile, err := os.Open(fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		lockFile.Close()
		t.Fatal(err)
	}
	locked := true
	t.Cleanup(func() {
		if locked {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		}
		_ = lockFile.Close()
	})

	adapter := startCapturedProcess(t, fixture.bun, []string{fixture.driver}, fixture.root, fixture.env, nil)
	waitForPath(t, fixture.ready, 5*time.Second)
	seedAdapterSessionOwner(t, fixture, adapter.command.Process.Pid)
	query := startCapturedProcess(t, fixture.binary, []string{"list", "--format", "json"}, fixture.root, fixture.env, nil)
	queryStarted := time.Now().UnixNano()
	waitForGateOwner(t, lockFile, query.command.Process.Pid, 5*time.Second)
	if err := os.WriteFile(fixture.start, []byte("start"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForPath(t, fixture.callbacks, 5*time.Second)
	waitForAdapterCommands(t, adapter.command.Process.Pid, fixture.binary, 5*time.Second)

	if release {
		releasedAt := time.Now().UnixNano()
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
			t.Fatal(err)
		}
		locked = false
		if fixture.observation != "" {
			observation := struct {
				QueryStartedAtUnixNano int64    `json:"query_started_at_unix_nano"`
				ReleasedAtUnixNano     int64    `json:"released_at_unix_nano"`
				Roles                  []string `json:"roles"`
			}{queryStarted, releasedAt, []string{"query", "hook", "telemetry"}}
			data, err := json.Marshal(observation)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.observation, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	waitCapturedProcess(t, query, 10*time.Second)
	waitCapturedProcess(t, adapter, 10*time.Second)
	if !release {
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
			t.Fatal(err)
		}
		locked = false
	}
	return adapter, query
}

func seedAdapterSessionOwner(t *testing.T, fixture generatedAdapterFixture, pid int) {
	t.Helper()
	startTime, ok := adapterProcessStartTime(pid)
	if !ok {
		t.Fatalf("read adapter process %d start time", pid)
	}
	directory := filepath.Join(fixture.root, "runtime", "ktx", "sessions")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(SessionFile{SessionID: "adapterownersess", PPID: pid, PPIDStartTime: startTime})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, strconv.Itoa(pid)+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func startCapturedProcess(t *testing.T, executable string, args []string, directory string, env []string, stdin []byte) *capturedProcess {
	t.Helper()
	process := &capturedProcess{command: exec.Command(executable, args...), done: make(chan error, 1)}
	process.command.Dir = directory
	process.command.Env = env
	process.command.Stdout = &process.stdout
	process.command.Stderr = &process.stderr
	if stdin != nil {
		process.command.Stdin = bytes.NewReader(stdin)
	}
	if err := process.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { process.done <- process.command.Wait() }()
	return process
}

func waitCapturedProcess(t *testing.T, process *capturedProcess, timeout time.Duration) {
	t.Helper()
	select {
	case <-process.done:
		return
	case <-time.After(timeout):
		_ = process.command.Process.Kill()
		<-process.done
		t.Fatalf("outer process timeout fired for %s", strings.Join(process.command.Args, " "))
	}
}

func waitForGateOwner(t *testing.T, file *os.File, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		probe := syscall.Flock_t{Type: syscall.F_RDLCK, Whence: 0, Start: 1, Len: 1}
		if err := syscall.FcntlFlock(file.Fd(), syscall.F_GETLK, &probe); err != nil {
			t.Fatal(err)
		}
		if probe.Type == syscall.F_WRLCK && int(probe.Pid) == pid {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("query process %d did not become the sole migration gate owner", pid)
}

func waitForAdapterCommands(t *testing.T, parent int, binary string, timeout time.Duration) {
	t.Helper()
	binaryInfo, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		seen := map[string]bool{}
		entries, err := os.ReadDir("/proc")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || processParent(pid) != parent {
				continue
			}
			info, err := os.Stat(filepath.Join("/proc", entry.Name(), "exe"))
			if err != nil || !os.SameFile(info, binaryInfo) {
				continue
			}
			argv, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
			if err != nil {
				continue
			}
			args := strings.Split(strings.TrimRight(string(argv), "\x00"), "\x00")
			for i, arg := range args {
				if arg == "hook" {
					seen["hook"] = true
				}
				if arg == "telemetry" && i+1 < len(args) && args[i+1] == "record" {
					seen["telemetry"] = true
				}
			}
		}
		if seen["hook"] && seen["telemetry"] {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("generated adapter hook and telemetry commands did not overlap readiness")
}

func processParent(pid int) int {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return -1
	}
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return -1
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) < 2 {
		return -1
	}
	parent, _ := strconv.Atoi(fields[1])
	return parent
}

func adapterProcessStartTime(pid int) (int64, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) <= 19 {
		return 0, false
	}
	value, err := strconv.ParseInt(fields[19], 10, 64)
	return value, err == nil
}

func waitForPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func assertAdapterOutput(t *testing.T, output []byte) {
	t.Helper()
	var value struct {
		Output   string         `json:"output"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &value); err != nil {
		t.Fatalf("adapter output: %v\n%s", err, output)
	}
	if value.Output != "adapter overlap output" || value.Metadata["kept"] != true {
		t.Fatalf("adapter output = %#v", value)
	}
}

func assertStableQueryOutput(t *testing.T, fixture generatedAdapterFixture, overlap []byte) {
	t.Helper()
	overlappedCounts := querySessionCounts(t, overlap)
	if overlappedCounts["historicalsessxx"] != 1 || (overlappedCounts["adapterownersess"] != 0 && overlappedCounts["adapterownersess"] != 1) {
		t.Fatalf("overlapped query counts = %v, want historical=1 and adapter=0 or 1", overlappedCounts)
	}
	first := runKTXQuery(t, fixture)
	second := runKTXQuery(t, fixture)
	if !bytes.Equal(first, second) {
		t.Fatalf("query output changed between current-schema reads:\nfirst=%s\nsecond=%s", first, second)
	}
	assertQuerySessionCounts(t, first, map[string]int64{"historicalsessxx": 1, "adapterownersess": 1})
}

func assertQuerySessionCounts(t *testing.T, output []byte, want map[string]int64) {
	t.Helper()
	got := querySessionCounts(t, output)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("query session counts = %v, want %v", got, want)
	}
}

func querySessionCounts(t *testing.T, output []byte) map[string]int64 {
	t.Helper()
	var sessions []struct {
		ID            string `json:"id"`
		Agent         string `json:"agent"`
		ToolCallCount int64  `json:"tool_call_count"`
	}
	if err := json.Unmarshal(output, &sessions); err != nil {
		t.Fatalf("query output: %v\n%s", err, output)
	}
	if len(sessions) != 2 {
		t.Fatalf("query sessions = %d, want 2: %s", len(sessions), output)
	}
	counts := make(map[string]int64, len(sessions))
	agents := map[string]string{"historicalsessxx": "fixture", "adapterownersess": "opencode"}
	for _, session := range sessions {
		agent, ok := agents[session.ID]
		if !ok || session.Agent != agent {
			t.Fatalf("query session = %+v, want agents %v", session, agents)
		}
		counts[session.ID] = session.ToolCallCount
	}
	if len(counts) != len(agents) {
		t.Fatalf("query session identities = %v, want %v", counts, agents)
	}
	return counts
}

func querySessionCountsAny(t *testing.T, output []byte) map[string]int64 {
	t.Helper()
	var sessions []struct {
		ID            string `json:"id"`
		ToolCallCount int64  `json:"tool_call_count"`
	}
	if err := json.Unmarshal(output, &sessions); err != nil {
		t.Fatalf("query output: %v\n%s", err, output)
	}
	counts := make(map[string]int64, len(sessions))
	for _, session := range sessions {
		counts[session.ID] = session.ToolCallCount
	}
	return counts
}

func runKTXQuery(t *testing.T, fixture generatedAdapterFixture) []byte {
	t.Helper()
	command := exec.Command(fixture.binary, "list", "--format", "json")
	command.Dir = fixture.root
	command.Env = fixture.env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("current-schema query: %v\n%s", err, output)
	}
	return output
}

func assertDatabaseUnavailableEnvelope(t *testing.T, stderr string, fixture generatedAdapterFixture, surfaces ...string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) != len(surfaces) {
		t.Fatalf("database-unavailable lines = %d, want %d\n%s", len(lines), len(surfaces), stderr)
	}
	wantSurfaces := append([]string(nil), surfaces...)
	sort.Strings(wantSurfaces)
	var gotSurfaces []string
	canonical, err := CanonicalPath(fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		var envelope struct {
			Error struct {
				Code                   string `json:"code"`
				DatabasePath           string `json:"database_path"`
				ExecutablePath         string `json:"executable_path"`
				ExecutableSHA256       string `json:"executable_sha256"`
				FailureClass           string `json:"failure_class"`
				OperationPhase         string `json:"operation_phase"`
				ElapsedWaitNanoseconds int64  `json:"elapsed_wait_nanoseconds"`
				Correction             string `json:"correction"`
				RuntimeSurface         string `json:"runtime_surface"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("database-unavailable envelope: %v\n%s", err, line)
		}
		errorValue := envelope.Error
		if errorValue.Code != "database-unavailable" || errorValue.DatabasePath != canonical ||
			errorValue.ExecutablePath != fixture.binary || errorValue.ExecutableSHA256 != fixture.digest ||
			errorValue.FailureClass != UnavailableClassCoordinatorTimeout || errorValue.OperationPhase != OperationOpen ||
			errorValue.ElapsedWaitNanoseconds <= 0 || errorValue.Correction == "" {
			t.Errorf("database-unavailable envelope = %+v", errorValue)
		}
		gotSurfaces = append(gotSurfaces, errorValue.RuntimeSurface)
	}
	sort.Strings(gotSurfaces)
	if !reflect.DeepEqual(gotSurfaces, wantSurfaces) {
		t.Fatalf("runtime surfaces = %v, want %v", gotSurfaces, wantSurfaces)
	}
}

func assertNoReadinessLeak(t *testing.T, output string) {
	t.Helper()
	lower := strings.ToLower(output)
	for _, forbidden := range []string{"database is locked", "sqlite_busy", "sqlite_locked", "migration-required", "ktx db migrate", "outer process timeout"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("readiness output exposes %q: %s", forbidden, output)
		}
	}
}

func generatedAdapterSourceID(t *testing.T) string {
	t.Helper()
	id, err := identity.Derive("opencode", "session-overlap", "call-overlap")
	if err != nil {
		t.Fatalf("derive generated source: %v", err)
	}
	return id
}

func assertGeneratedAdapterFailureLeftNoEvidence(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open(sqliteDriver, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, sources, telemetryRows int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM tool_calls WHERE id=?`, generatedAdapterSourceID(t)).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM session_telemetry`).Scan(&telemetryRows); err != nil {
		t.Fatal(err)
	}
	if version != 15 || sources != 0 || telemetryRows != 0 {
		t.Fatalf("failed overlap changed evidence: version=%d sources=%d telemetry=%d", version, sources, telemetryRows)
	}
}

func assertGeneratedAdapterEvidence(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	sourceID := generatedAdapterSourceID(t)
	var version, sessions, sources, compressedRows, dispositions, telemetryRows, cursor int
	var integrity, sourceSession, sourceOutput, telemetrySession string
	checks := []struct {
		query string
		args  []any
		dest  []any
	}{
		{`PRAGMA user_version`, nil, []any{&version}},
		{`SELECT count(*) FROM sessions`, nil, []any{&sessions}},
		{`SELECT count(*),session_id,output FROM tool_calls WHERE id=?`, []any{sourceID}, []any{&sources, &sourceSession, &sourceOutput}},
		{`SELECT count(*) FROM compressed`, nil, []any{&compressedRows}},
		{`SELECT count(*) FROM compression_dispositions WHERE source_id=?`, []any{sourceID}, []any{&dispositions}},
		{`SELECT count(*),session_id FROM session_telemetry WHERE source_session_id='session-overlap' AND message_id='message-overlap' AND part_id='part-overlap' AND ordinal=1 AND cost=1.25 AND tokens_input=11 AND tokens_output=7 AND tokens_reasoning=3 AND cache_read=5 AND cache_write=2`, nil, []any{&telemetryRows, &telemetrySession}},
		{`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='compressed_timestamp_migration'`, nil, []any{&cursor}},
		{`PRAGMA integrity_check`, nil, []any{&integrity}},
	}
	for _, check := range checks {
		if err := raw.QueryRow(check.query, check.args...).Scan(check.dest...); err != nil {
			t.Fatalf("verify generated adapter evidence (%s): %v", check.query, err)
		}
	}
	if version != schemaVersion || sessions != 2 || sources != 1 || sourceOutput != "adapter overlap output" ||
		compressedRows != generatedAdapterCompressedRows || dispositions != 1 || telemetryRows != 1 ||
		sourceSession == "" || sourceSession != telemetrySession || cursor != 0 || integrity != "ok" {
		t.Fatalf("generated evidence: version=%d sessions=%d sources=%d source_session=%q output=%q compressed=%d dispositions=%d telemetry=%d telemetry_session=%q cursor=%d integrity=%q",
			version, sessions, sources, sourceSession, sourceOutput, compressedRows, dispositions, telemetryRows, telemetrySession, cursor, integrity)
	}
}

func withoutEnvironment(environment []string, names ...string) []string {
	blocked := make(map[string]struct{}, len(names))
	for _, name := range names {
		blocked[name] = struct{}{}
	}
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		name, _, _ := strings.Cut(value, "=")
		if _, excluded := blocked[name]; !excluded {
			result = append(result, value)
		}
	}
	return result
}

func replaceEnvironment(environment []string, name, value string) []string {
	return append(withoutEnvironment(environment, name), name+"="+value)
}
