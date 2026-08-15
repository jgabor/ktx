//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestMain(m *testing.M) {
	if os.Getenv("KTX_TEST_CLI_PROCESS") == "1" {
		os.Exit(Run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestOpencodeGeneratedAdapterLogsCoordinatorTimeoutBeforeGenericFallback(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is required to execute the generated OpenCode adapter")
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	dataHome := filepath.Join(root, "data")
	configHome := filepath.Join(root, "config")
	cacheHome := filepath.Join(root, "cache")
	stateHome := filepath.Join(root, "state")
	tmpDir := filepath.Join(root, "tmp")
	project := filepath.Join(root, "project")
	binDir := filepath.Join(root, "bin")
	for _, path := range []string{home, dataHome, configHome, cacheHome, stateHome, tmpDir, project, binDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"HOME": home, "XDG_DATA_HOME": dataHome, "XDG_CONFIG_HOME": configHome,
		"XDG_CACHE_HOME": cacheHome, "XDG_STATE_HOME": stateHome, "TMPDIR": tmpDir,
		"KTX_VERIFY_ROOT": root,
	} {
		t.Setenv(key, value)
	}
	previousDatabase, hadDatabase := os.LookupEnv("KTX_DB")
	if err := os.Unsetenv("KTX_DB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadDatabase {
			_ = os.Setenv("KTX_DB", previousDatabase)
		} else {
			_ = os.Unsetenv("KTX_DB")
		}
	})

	eventPath := filepath.Join(stateHome, "events.jsonl")
	hookWriteUserConfig(t, "[logs]\nlevel = \"error\"\npath = "+strconv.Quote(eventPath)+"\n")
	var initStdout, initStderr bytes.Buffer
	if code := runInitWith([]string{"opencode"}, Options{Format: FormatJSON}, &initStdout, &initStderr); code != ExitOK {
		t.Fatalf("generate OpenCode adapter: exit=%d stderr=%s", code, initStderr.String())
	}
	pluginPath := filepath.Join(configHome, "opencode", "plugins", ktxPluginFileName)

	databasePath := filepath.Join(dataHome, "ktx", "ktx.db")
	database, err := dbpkg.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(databasePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(binDir, "ktx")
	executable, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidatePath, executable, 0o700); err != nil {
		t.Fatal(err)
	}

	driverPath := filepath.Join(project, "driver.ts")
	driver := `import plugin from ` + strconv.Quote(pluginPath) + `
const originalSpawn = Bun.spawn
let eventObservedBeforeParser = false
;(Bun as any).spawn = ((command: string[], options: any) => {
  const proc: any = originalSpawn(command, options)
  if (!Array.isArray(command) || command[1] !== "hook") return proc
  const stderr = new ReadableStream({
    async start(controller) {
      const diagnostic = await new Response(proc.stderr).text()
      const retained = await Bun.file(process.env.KTX_TEST_EVENT_LOG!).text()
      eventObservedBeforeParser = retained.trim().length > 0
      if (!eventObservedBeforeParser) throw new Error("event missing before adapter parsing")
      controller.enqueue(new TextEncoder().encode(diagnostic.trimEnd() + "\nadapter-only parser rejection\n"))
      controller.close()
    },
  })
  return { stdout: proc.stdout, stderr, exited: proc.exited, kill: () => proc.kill() }
}) as any
const hooks: any = await plugin({ $: () => ({ quiet: async () => {} }) } as any)
const input = {tool:"bash",sessionID:"ses_1",callID:"call_1",args:{}}
const output: any = {title:"tool",output:"original tool output",metadata:{kept:true}}
await hooks["tool.execute.after"](input, output)
console.log(JSON.stringify({output: output.output, event_observed_before_parser: eventObservedBeforeParser}))
`
	if err := os.WriteFile(driverPath, []byte(driver), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bun, driverPath)
	cmd.Dir = project
	cmd.Env = disposableAdapterEnvironment(root, binDir, eventPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generated adapter: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var result struct {
		Output                    string `json:"output"`
		EventObservedBeforeParser bool   `json:"event_observed_before_parser"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil {
		t.Fatalf("decode driver result: %v: %q", err, stdout.String())
	}
	if result.Output != "original tool output" || !result.EventObservedBeforeParser {
		t.Fatalf("driver result = %+v", result)
	}
	const fallback = "[ktx] hook failed (exit 4):"
	if strings.Count(stderr.String(), fallback) != 1 || !strings.Contains(stderr.String(), "adapter-only parser rejection") {
		t.Fatalf("generic adapter fallback = %q", stderr.String())
	}

	retained, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(retained), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("event records = %d, want 1", len(lines))
	}
	var event map[string]any
	if err := json.Unmarshal(lines[0], &event); err != nil {
		t.Fatal(err)
	}
	if len(event) != 11 || event["runtime_surface"] != "opencode-hook" ||
		event["failure_class"] != dbpkg.UnavailableClassCoordinatorTimeout || event["operation_phase"] != dbpkg.OperationOpen {
		t.Fatalf("database availability event = %#v", event)
	}
}

func disposableAdapterEnvironment(root, binDir, eventPath string) []string {
	overridden := map[string]bool{
		"HOME": true, "XDG_DATA_HOME": true, "XDG_CONFIG_HOME": true,
		"XDG_CACHE_HOME": true, "XDG_STATE_HOME": true, "TMPDIR": true,
		"KTX_DB": true, "KTX_VERIFY_ROOT": true, "PATH": true,
		"KTX_SESSION_ID": true, "KTX_BENCH_COLLECTOR_MODE": true,
	}
	environment := make([]string, 0, len(os.Environ())+10)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !overridden[key] {
			environment = append(environment, entry)
		}
	}
	return append(environment,
		"HOME="+filepath.Join(root, "home"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"TMPDIR="+filepath.Join(root, "tmp"),
		"KTX_VERIFY_ROOT="+root,
		"KTX_TEST_CLI_PROCESS=1",
		"KTX_TEST_EVENT_LOG="+eventPath,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
}
