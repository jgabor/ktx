// Tests for `ktx init opencode` (internal/cli/init_opencode.go).
//
// Each test puts a fake opencode on a PATH of its own, points HOME and the
// XDG folders at temporary folders and works in a temporary project folder.
// The fake is this test binary run as TestFakeOpencodeProcess: it records
// every call and edits the config files the way `opencode plugin` 1.19 does,
// so no test reaches the real OpenCode configuration or the npm registry.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/opencodeplugin"
	"github.com/jgabor/ktx/internal/opencodeplugin/fakeopencode"
	"github.com/jgabor/ktx/internal/version"
)

const fakeOpencodeDirEnv = "KTX_TEST_FAKE_OPENCODE_DIR"

// TestFakeOpencodeProcess is the opencode executable the tests put on PATH.
// It only runs as that helper process.
func TestFakeOpencodeProcess(t *testing.T) {
	dir := os.Getenv(fakeOpencodeDirEnv)
	if dir == "" {
		t.Skip("runs only as the fake opencode")
	}
	args := os.Args
	for index, arg := range args {
		if arg == "--" {
			args = args[index+1:]
			break
		}
	}
	calls, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(calls, strings.Join(args, " "))
		calls.Close()
	}
	if _, err := os.Stat(filepath.Join(dir, "fail")); err == nil {
		fmt.Fprintln(os.Stderr, "network unreachable")
		os.Exit(1)
	}
	global, err := opencodeplugin.GlobalDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	_, duplicate := os.Stat(filepath.Join(dir, "duplicate"))
	fake := fakeopencode.Plugin{Global: global, Project: filepath.Join(cwd, ".opencode"), Duplicate: duplicate == nil}
	if err := fake.Run(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

type fakeOpencode struct {
	dir, bin, global, project string
}

// setupFakeOpencode puts a fake opencode, when withOpencode is set, and a ktx
// on a PATH of the test's own, gives the test temporary HOME and XDG folders,
// moves into a temporary project and sets the ktx version.
func setupFakeOpencode(t *testing.T, withOpencode bool, ktxVersion string) fakeOpencode {
	t.Helper()
	root := t.TempDir()
	fake := fakeOpencode{dir: filepath.Join(root, "fake"), bin: filepath.Join(root, "bin"), project: filepath.Join(root, "project")}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		path := filepath.Join(root, strings.ToLower(key))
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	fake.global = filepath.Join(root, "xdg_config_home", "opencode")
	for _, dir := range []string{fake.dir, fake.bin, fake.project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if withOpencode {
		// The race detector waits a second before exit unless told not to.
		script := fmt.Sprintf("#!/bin/sh\nGORACE=atexit_sleep_ms=0 exec %s -test.run='^TestFakeOpencodeProcess$' -- \"$@\"\n", shellQuote(os.Args[0]))
		if err := os.WriteFile(filepath.Join(fake.bin, "opencode"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fake.bin, "ktx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeOpencodeDirEnv, fake.dir)
	t.Setenv("PATH", fake.bin)
	t.Chdir(fake.project)
	previous := version.Version
	version.Version = ktxVersion
	t.Cleanup(func() { version.Version = previous })
	return fake
}

func (f fakeOpencode) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	if os.IsNotExist(err) {
		return []string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (f fakeOpencode) set(t *testing.T, name string) {
	t.Helper()
	writeOpencodeFile(t, filepath.Join(f.dir, name), "")
}

func (f fakeOpencode) projectConfig() string { return filepath.Join(f.project, ".opencode") }

func writeOpencodeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readOpencodeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runOpencodeInit(t *testing.T, format string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runInitWith(append([]string{"opencode"}, args...), Options{Format: format}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func decodeOpencodeReport(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	return report
}

// ktxSpecs lists the ktx entries of the plugin arrays of files, in order.
func ktxSpecs(t *testing.T, files ...string) []string {
	t.Helper()
	specs := []string{}
	for _, file := range files {
		_, array, err := opencodeplugin.ReadPluginArray(file)
		if err != nil {
			t.Fatal(err)
		}
		if array == nil {
			continue
		}
		for _, element := range array.Elements {
			if opencodeplugin.IsKtxSpec(element.Spec, filepath.Dir(file), "") {
				specs = append(specs, element.Spec)
			}
		}
	}
	return specs
}

// generatedOpencodePlugin is a plugin file in the shape earlier ktx versions
// generated.
const generatedOpencodePlugin = "const KtxPlugin = async () => ({\n  \"tool.execute.after\": async () => { if (process.env.KTX_SESSION_ID) return },\n})\nexport default KtxPlugin\n"

func TestInitOpencodeInstallsThePinnedPackageGlobally(t *testing.T) {
	fake := setupFakeOpencode(t, true, "v0.2.0")
	code, stdout, stderr := runOpencodeInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	if want := []string{"plugin -g @jgabor/ktx@0.2.0"}; !reflect.DeepEqual(fake.calls(t), want) {
		t.Fatalf("calls = %q, want %q", fake.calls(t), want)
	}
	for _, file := range []string{"opencode.jsonc", "tui.json"} {
		if got := ktxSpecs(t, filepath.Join(fake.global, file)); !reflect.DeepEqual(got, []string{"@jgabor/ktx@0.2.0"}) {
			t.Errorf("%s ktx entries = %q", file, got)
		}
	}
	if _, err := os.Stat(fake.projectConfig()); !os.IsNotExist(err) {
		t.Fatalf("a global install touched the project: %v", err)
	}
	report := decodeOpencodeReport(t, stdout)
	if report["spec"] != "@jgabor/ktx@0.2.0" || report["pinned"] != true || report["scope"] != "global" || report["changed"] != true ||
		report["ktx_on_path"] != filepath.Join(fake.bin, "ktx") || report["runtime_activation"] != "not_verified" {
		t.Fatalf("report = %#v", report)
	}
}

func TestInitOpencodeReplacesAnOlderPinAndKeepsOtherEntries(t *testing.T) {
	fake := setupFakeOpencode(t, true, "v0.2.0")
	server := filepath.Join(fake.global, "opencode.json")
	writeOpencodeFile(t, server, "{\n  // mine\n  \"plugin\": [\n    \"other-plugin\",\n    \"@jgabor/ktx@0.1.0\",\n    [\"tuple-plugin\", {\"a\": 1}],\n  ],\n}\n")
	writeOpencodeFile(t, filepath.Join(fake.global, "tui.json"), `{"plugin": ["@jgabor/ktx@0.1.0", "other-tui"]}`)
	code, stdout, stderr := runOpencodeInit(t, FormatText)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	if want := "{\n  // mine\n  \"plugin\": [\n    \"other-plugin\",\n    [\"tuple-plugin\", {\"a\": 1}], \"@jgabor/ktx@0.2.0\",\n  ],\n}\n"; readOpencodeFile(t, server) != want {
		t.Fatalf("server config = %q, want %q", readOpencodeFile(t, server), want)
	}
	if got := readOpencodeFile(t, filepath.Join(fake.global, "tui.json")); got != `{"plugin": ["other-tui", "@jgabor/ktx@0.2.0"]}` {
		t.Fatalf("TUI config = %q", got)
	}
	for _, want := range []string{"ktx OpenCode plugin installed: @jgabor/ktx@0.2.0 (global, " + fake.global + ")", "Note: restart OpenCode"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
}

func TestInitOpencodeProjectInstallsInTheProject(t *testing.T) {
	fake := setupFakeOpencode(t, true, "v0.2.0")
	code, stdout, stderr := runOpencodeInit(t, FormatJSON, "--project")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	if want := []string{"plugin @jgabor/ktx@0.2.0"}; !reflect.DeepEqual(fake.calls(t), want) {
		t.Fatalf("calls = %q, want %q", fake.calls(t), want)
	}
	if got := ktxSpecs(t, filepath.Join(fake.projectConfig(), "opencode.json"), filepath.Join(fake.projectConfig(), "tui.json")); len(got) != 2 {
		t.Fatalf("project ktx entries = %q", got)
	}
	if _, err := os.Stat(fake.global); !os.IsNotExist(err) {
		t.Fatalf("a project install touched the global config: %v", err)
	}
	if report := decodeOpencodeReport(t, stdout); report["scope"] != "project" || report["config_dir"] != fake.projectConfig() {
		t.Fatalf("report = %#v", report)
	}
}

func TestInitOpencodeRerunChangesNothing(t *testing.T) {
	fake := setupFakeOpencode(t, true, "v0.2.0")
	if code, _, stderr := runOpencodeInit(t, FormatJSON); code != ExitOK {
		t.Fatalf("first exit = %d, stderr=%q", code, stderr)
	}
	before := readOpencodeFile(t, filepath.Join(fake.global, "opencode.jsonc")) + readOpencodeFile(t, filepath.Join(fake.global, "tui.json"))
	code, stdout, stderr := runOpencodeInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("second exit = %d, stderr=%q", code, stderr)
	}
	if len(fake.calls(t)) != 1 {
		t.Fatalf("calls = %q, want only the first install", fake.calls(t))
	}
	if after := readOpencodeFile(t, filepath.Join(fake.global, "opencode.jsonc")) + readOpencodeFile(t, filepath.Join(fake.global, "tui.json")); after != before {
		t.Fatalf("re-run changed the config: %q", after)
	}
	if report := decodeOpencodeReport(t, stdout); report["already_installed"] != true || report["changed"] != false {
		t.Fatalf("report = %#v", report)
	}
}

func TestInitOpencodeFailsOnADuplicateEntry(t *testing.T) {
	fake := setupFakeOpencode(t, true, "v0.2.0")
	fake.set(t, "duplicate")
	writeOpencodeFile(t, filepath.Join(fake.global, "opencode.json"), `{"plugin": ["other"]}`)
	writeOpencodeFile(t, filepath.Join(fake.global, "tui.json"), `{"plugin": ["other"]}`)
	code, stdout, stderr := runOpencodeInit(t, FormatJSON)
	if code != ExitInternal || !strings.Contains(stderr, "2 ktx server entries and 2 ktx TUI entries") || stdout != "" {
		t.Fatalf("exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestInitOpencodeFailureKeepsTheConfigAndTheGeneratedPlugin(t *testing.T) {
	fake := setupFakeOpencode(t, true, "v0.2.0")
	fake.set(t, "fail")
	legacy := filepath.Join(fake.global, "plugins", "ktx.ts")
	writeOpencodeFile(t, legacy, generatedOpencodePlugin)
	server := filepath.Join(fake.global, "opencode.json")
	const pinned = `{"plugin": ["other", "@jgabor/ktx@0.1.0"]}`
	writeOpencodeFile(t, server, pinned)
	code, stdout, stderr := runOpencodeInit(t, FormatText)
	if code != ExitInternal || stdout != "" {
		t.Fatalf("exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, want := range []string{"network unreachable", "every OpenCode config file is back as it was"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q: %s", want, stderr)
		}
	}
	if strings.Contains(stderr, "Recovery:") {
		t.Errorf("a restored config still asks for recovery: %s", stderr)
	}
	if got := readOpencodeFile(t, server); got != pinned {
		t.Fatalf("server config = %q, want the earlier pin kept", got)
	}
	if readOpencodeFile(t, legacy) != generatedOpencodePlugin {
		t.Fatal("the generated plugin was removed although the install failed")
	}
}

func TestInitOpencodeWithoutAReleaseNumber(t *testing.T) {
	t.Run("installs the unpinned package and says so", func(t *testing.T) {
		fake := setupFakeOpencode(t, true, "dev")
		code, stdout, stderr := runOpencodeInit(t, FormatText)
		if code != ExitOK {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		if want := []string{"plugin -g @jgabor/ktx"}; !reflect.DeepEqual(fake.calls(t), want) {
			t.Fatalf("calls = %q, want %q", fake.calls(t), want)
		}
		if !strings.Contains(stdout, "note: this ktx (dev) has no release number, so the unpinned @jgabor/ktx is installed") {
			t.Fatalf("output = %s", stdout)
		}
	})
	t.Run("keeps the checkout folder mage install set", func(t *testing.T) {
		fake := setupFakeOpencode(t, true, "v0.0.0-20261022101500-0123456789ab")
		checkout := filepath.Join(t.TempDir(), "ktx", "plugins", "opencode")
		writeOpencodeFile(t, filepath.Join(checkout, "package.json"), `{"name":"@jgabor/ktx"}`)
		writeOpencodeFile(t, filepath.Join(fake.global, "opencode.jsonc"), `{"plugin": ["`+checkout+`"]}`)
		writeOpencodeFile(t, filepath.Join(fake.global, "tui.json"), `{"plugin": ["`+checkout+`"]}`)
		code, stdout, stderr := runOpencodeInit(t, FormatJSON)
		if code != ExitOK || len(fake.calls(t)) != 0 {
			t.Fatalf("exit = %d, calls = %q, stderr=%q", code, fake.calls(t), stderr)
		}
		report := decodeOpencodeReport(t, stdout)
		if report["spec"] != checkout || report["pinned"] != false || report["already_installed"] != true || !strings.Contains(fmt.Sprint(report["note"]), "existing entry "+checkout+" is kept") {
			t.Fatalf("report = %#v", report)
		}
	})
	t.Run("replaces a release pin with the unpinned package", func(t *testing.T) {
		fake := setupFakeOpencode(t, true, "dev")
		writeOpencodeFile(t, filepath.Join(fake.global, "opencode.json"), `{"plugin": ["other", "@jgabor/ktx@0.1.0"]}`)
		writeOpencodeFile(t, filepath.Join(fake.global, "tui.json"), `{"plugin": ["@jgabor/ktx@0.1.0"]}`)
		code, stdout, stderr := runOpencodeInit(t, FormatJSON)
		if code != ExitOK {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		if want := []string{"plugin -g @jgabor/ktx"}; !reflect.DeepEqual(fake.calls(t), want) {
			t.Fatalf("calls = %q, want %q: a build without a release number kept a release pin", fake.calls(t), want)
		}
		if got := ktxSpecs(t, filepath.Join(fake.global, "opencode.json"), filepath.Join(fake.global, "tui.json")); !reflect.DeepEqual(got, []string{"@jgabor/ktx", "@jgabor/ktx"}) {
			t.Fatalf("ktx entries = %q", got)
		}
		report := decodeOpencodeReport(t, stdout)
		if report["spec"] != "@jgabor/ktx" || report["changed"] != true || !strings.Contains(fmt.Sprint(report["note"]), "unpinned @jgabor/ktx is installed in place of @jgabor/ktx@0.1.0;") {
			t.Fatalf("report = %#v", report)
		}
	})
	t.Run("a release replaces the checkout folder", func(t *testing.T) {
		fake := setupFakeOpencode(t, true, "v0.2.0")
		checkout := filepath.Join(t.TempDir(), "ktx", "plugins", "opencode")
		writeOpencodeFile(t, filepath.Join(checkout, "package.json"), `{"name":"@jgabor/ktx"}`)
		writeOpencodeFile(t, filepath.Join(fake.global, "opencode.jsonc"), `{"plugin": ["`+checkout+`"]}`)
		writeOpencodeFile(t, filepath.Join(fake.global, "tui.json"), `{"plugin": ["`+checkout+`"]}`)
		if code, _, stderr := runOpencodeInit(t, FormatJSON); code != ExitOK {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		if got := ktxSpecs(t, filepath.Join(fake.global, "opencode.jsonc"), filepath.Join(fake.global, "tui.json")); !reflect.DeepEqual(got, []string{"@jgabor/ktx@0.2.0", "@jgabor/ktx@0.2.0"}) {
			t.Fatalf("ktx entries = %q", got)
		}
	})
}

func TestInitOpencodeWithoutOpencodePrintsTheCommands(t *testing.T) {
	fake := setupFakeOpencode(t, false, "v0.2.0")
	server := filepath.Join(fake.global, "opencode.json")
	writeOpencodeFile(t, server, `{"plugin": ["@jgabor/ktx@0.1.0"]}`)
	legacy := filepath.Join(fake.projectConfig(), "plugins", "ktx.ts")
	writeOpencodeFile(t, legacy, generatedOpencodePlugin)
	code, stdout, stderr := runOpencodeInit(t, FormatText)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	for _, want := range []string{"opencode not found on PATH; nothing changed", "  opencode plugin -g -f @jgabor/ktx@0.2.0\n", "  rm " + legacy + "\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	if readOpencodeFile(t, server) != `{"plugin": ["@jgabor/ktx@0.1.0"]}` || readOpencodeFile(t, legacy) != generatedOpencodePlugin {
		t.Fatal("install without opencode changed something")
	}

	code, stdout, _ = runOpencodeInit(t, FormatJSON, "--project")
	report := decodeOpencodeReport(t, stdout)
	steps, _ := report["next_steps"].([]any)
	if code != ExitOK || report["installed"] != false || len(steps) != 2 || steps[0] != "cd "+fake.project+" && opencode plugin -f @jgabor/ktx@0.2.0" {
		t.Fatalf("project report = %#v", report)
	}
}

func TestInitOpencodeWithoutOpencodeWhenAlreadyInstalled(t *testing.T) {
	fake := setupFakeOpencode(t, false, "v0.2.0")
	for _, name := range []string{"opencode.json", "tui.json"} {
		writeOpencodeFile(t, filepath.Join(fake.global, name), `{"plugin": ["@jgabor/ktx@0.2.0"]}`)
	}
	code, stdout, stderr := runOpencodeInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "ktx OpenCode plugin already installed: @jgabor/ktx@0.2.0") || strings.Contains(stdout, "not found on PATH") {
		t.Fatalf("exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
	}

	// A generated plugin is left for the user to remove, since install
	// without opencode changes nothing.
	legacy := filepath.Join(fake.global, "plugins", "ktx.ts")
	writeOpencodeFile(t, legacy, generatedOpencodePlugin)
	code, stdout, _ = runOpencodeInit(t, FormatText)
	for _, want := range []string{"opencode not found on PATH; nothing changed. ktx OpenCode plugin already installed: @jgabor/ktx@0.2.0", "  rm " + legacy + "\n"} {
		if code != ExitOK || !strings.Contains(stdout, want) {
			t.Errorf("exit = %d, output lacks %q:\n%s", code, want, stdout)
		}
	}
	if strings.Contains(stdout, "opencode plugin") {
		t.Errorf("an installed scope was told to run opencode plugin:\n%s", stdout)
	}
	code, stdout, _ = runOpencodeInit(t, FormatJSON)
	report := decodeOpencodeReport(t, stdout)
	if code != ExitOK || report["installed"] != true || report["already_installed"] != true || !reflect.DeepEqual(report["next_steps"], []any{"rm " + legacy}) {
		t.Fatalf("report = %#v", report)
	}
	if readOpencodeFile(t, legacy) != generatedOpencodePlugin {
		t.Fatal("install without opencode removed the generated plugin")
	}
}

func TestInitOpencodeWithoutOpencodeNamesEntriesForceKeeps(t *testing.T) {
	fake := setupFakeOpencode(t, false, "v0.2.0")
	checkout := filepath.Join(t.TempDir(), "ktx", "plugins", "opencode")
	writeOpencodeFile(t, filepath.Join(checkout, "package.json"), `{"name":"@jgabor/ktx"}`)
	server := filepath.Join(fake.global, "opencode.json")
	writeOpencodeFile(t, server, `{"plugin": ["`+checkout+`"]}`)
	code, stdout, _ := runOpencodeInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "first remove these ktx entries, which `opencode plugin -f` does not replace:\n    "+checkout+" in "+server+"\n") {
		t.Fatalf("exit = %d, output:\n%s", code, stdout)
	}

	// An older pin alone is replaced by -f, so nothing is listed.
	writeOpencodeFile(t, server, `{"plugin": ["@jgabor/ktx@0.1.0"]}`)
	code, stdout, _ = runOpencodeInit(t, FormatJSON)
	if report := decodeOpencodeReport(t, stdout); code != ExitOK || !reflect.DeepEqual(report["remove_entries_first"], []any{}) {
		t.Fatalf("report = %#v", report)
	}
}

func TestInitOpencodeRerunReplacesASecondEntry(t *testing.T) {
	fake := setupFakeOpencode(t, true, "v0.2.0")
	for _, dir := range []string{fake.projectConfig(), fake.project} {
		writeOpencodeFile(t, filepath.Join(dir, "opencode.json"), `{"plugin": ["@jgabor/ktx@0.2.0"]}`)
	}
	writeOpencodeFile(t, filepath.Join(fake.projectConfig(), "tui.json"), `{"plugin": ["@jgabor/ktx@0.2.0"]}`)
	code, stdout, stderr := runOpencodeInit(t, FormatJSON, "--project")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	if report := decodeOpencodeReport(t, stdout); report["already_installed"] == true || report["changed"] != true {
		t.Fatalf("a scope loading ktx twice counted as installed: %#v", report)
	}
	if want := []string{"plugin @jgabor/ktx@0.2.0"}; !reflect.DeepEqual(fake.calls(t), want) {
		t.Fatalf("calls = %q, want %q", fake.calls(t), want)
	}
	files := []string{filepath.Join(fake.projectConfig(), "opencode.json"), filepath.Join(fake.project, "opencode.json")}
	if got := ktxSpecs(t, files...); !reflect.DeepEqual(got, []string{"@jgabor/ktx@0.2.0"}) {
		t.Fatalf("project server entries = %q, want one", got)
	}
}

func TestInitOpencodeMalformedConfigStopsBeforeChanges(t *testing.T) {
	for _, withOpencode := range []bool{true, false} {
		t.Run(fmt.Sprintf("opencode on PATH: %t", withOpencode), func(t *testing.T) {
			fake := setupFakeOpencode(t, withOpencode, "v0.2.0")
			legacy := filepath.Join(fake.global, "plugins", "ktx.ts")
			writeOpencodeFile(t, legacy, generatedOpencodePlugin)
			writeOpencodeFile(t, filepath.Join(fake.projectConfig(), "tui.json"), `{"plugin": [`)
			for _, args := range [][]string{nil, {"--show"}, {"--uninstall"}} {
				code, _, stderr := runOpencodeInit(t, FormatJSON, args...)
				if code != ExitInternal || !strings.Contains(stderr, "invalid JSONC") {
					t.Fatalf("%q exit = %d, stderr=%q", args, code, stderr)
				}
			}
			if len(fake.calls(t)) != 0 || readOpencodeFile(t, legacy) != generatedOpencodePlugin {
				t.Fatalf("a malformed config did not stop the command: calls %q", fake.calls(t))
			}
		})
	}
}

func TestInitOpencodeRemovesGeneratedPluginsOnly(t *testing.T) {
	fake := setupFakeOpencode(t, true, "v0.2.0")
	globalLegacy := filepath.Join(fake.global, "plugins", "ktx.ts")
	projectLegacy := filepath.Join(fake.projectConfig(), "plugins", "ktx.ts")
	writeOpencodeFile(t, globalLegacy, generatedOpencodePlugin)
	writeOpencodeFile(t, projectLegacy, generatedOpencodePlugin)
	code, stdout, stderr := runOpencodeInit(t, FormatText)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr)
	}
	for _, path := range []string{globalLegacy, projectLegacy} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("generated plugin %s remains: %v", path, err)
		}
		if !strings.Contains(stdout, "removed the plugin an earlier ktx generated at "+path) {
			t.Errorf("output does not report %s:\n%s", path, stdout)
		}
	}

	// A ktx.ts ktx did not generate stays.
	const users = "export default async () => ({})\n"
	writeOpencodeFile(t, globalLegacy, users)
	code, stdout, _ = runOpencodeInit(t, FormatText)
	if code != ExitOK || readOpencodeFile(t, globalLegacy) != users || strings.Contains(stdout, "removed the plugin") {
		t.Fatalf("someone else's ktx.ts was touched: exit %d, output %s", code, stdout)
	}
}

func TestInitOpencodeShow(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		fake := setupFakeOpencode(t, false, "v0.2.0")
		writeOpencodeFile(t, filepath.Join(fake.global, "opencode.jsonc"), `{"plugin": ["other", "@jgabor/ktx@0.2.0"]}`)
		writeOpencodeFile(t, filepath.Join(fake.global, "tui.json"), `{"plugin": ["@jgabor/ktx@0.2.0"]}`)
		code, stdout, stderr := runOpencodeInit(t, FormatText, "--show")
		if code != ExitOK {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		for _, want := range []string{
			"global (" + fake.global + "): @jgabor/ktx@0.2.0 in " + filepath.Join(fake.global, "opencode.jsonc"),
			"project (" + fake.projectConfig() + "): not installed",
			"expected for this ktx: @jgabor/ktx@0.2.0",
			"ktx on PATH: " + filepath.Join(fake.bin, "ktx"),
			"this ktx: v0.2.0",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("output lacks %q:\n%s", want, stdout)
			}
		}
		if strings.Contains(stdout, "warning") {
			t.Fatalf("matching entries warned:\n%s", stdout)
		}
	})
	t.Run("an older pin is reported", func(t *testing.T) {
		fake := setupFakeOpencode(t, false, "v0.2.0")
		writeOpencodeFile(t, filepath.Join(fake.projectConfig(), "opencode.json"), `{"plugin": ["@jgabor/ktx@0.1.0"]}`)
		writeOpencodeFile(t, filepath.Join(fake.projectConfig(), "tui.json"), `{"plugin": ["@jgabor/ktx@0.1.0"]}`)
		code, stdout, _ := runOpencodeInit(t, FormatJSON, "--show", "--project")
		report := decodeOpencodeReport(t, stdout)
		scopes, _ := report["scopes"].([]any)
		if code != ExitOK || report["matches"] != false || !reflect.DeepEqual(report["mismatched_scopes"], []any{"project"}) || len(scopes) != 1 {
			t.Fatalf("report = %#v", report)
		}
	})
	t.Run("not installed exits 2", func(t *testing.T) {
		fake := setupFakeOpencode(t, false, "v0.2.0")
		writeOpencodeFile(t, filepath.Join(fake.global, "opencode.json"), `{"plugin": ["other"]}`)
		legacy := filepath.Join(fake.global, "plugins", "ktx.ts")
		writeOpencodeFile(t, legacy, generatedOpencodePlugin)
		code, stdout, stderr := runOpencodeInit(t, FormatJSON, "--show")
		if code != ExitNotFound || stdout != "" || !strings.Contains(stderr, "not installed") || !strings.Contains(stderr, "earlier ktx generated plugin is still at "+legacy) {
			t.Fatalf("exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

func TestInitOpencodeUninstall(t *testing.T) {
	t.Run("removes only ktx's entries", func(t *testing.T) {
		fake := setupFakeOpencode(t, false, "v0.2.0")
		server := filepath.Join(fake.global, "opencode.jsonc")
		writeOpencodeFile(t, server, "{\n  \"plugin\": [\n    \"other\", // keep\n    \"@jgabor/ktx@0.2.0\",\n  ],\n}\n")
		writeOpencodeFile(t, filepath.Join(fake.global, "tui.json"), `{"plugin": ["@jgabor/ktx@0.2.0"]}`)
		projectServer := filepath.Join(fake.project, "opencode.json")
		writeOpencodeFile(t, projectServer, `{"plugin": ["@jgabor/ktx", "other"]}`)
		legacy := filepath.Join(fake.projectConfig(), "plugins", "ktx.ts")
		writeOpencodeFile(t, legacy, generatedOpencodePlugin)
		code, stdout, stderr := runOpencodeInit(t, FormatJSON, "--uninstall")
		if code != ExitOK {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		if got := readOpencodeFile(t, server); got != "{\n  \"plugin\": [\n    \"other\", // keep\n  ],\n}\n" {
			t.Fatalf("server config = %q", got)
		}
		if got := readOpencodeFile(t, filepath.Join(fake.global, "tui.json")); got != `{"plugin": []}` {
			t.Fatalf("TUI config = %q", got)
		}
		if got := readOpencodeFile(t, projectServer); got != `{"plugin": ["other"]}` {
			t.Fatalf("project config = %q", got)
		}
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("generated plugin remains: %v", err)
		}
		report := decodeOpencodeReport(t, stdout)
		if entries, _ := report["removed_entries"].([]any); len(entries) != 3 || !reflect.DeepEqual(report["removed_generated_plugins"], []any{legacy}) {
			t.Fatalf("report = %#v", report)
		}
	})
	t.Run("--project leaves the global config", func(t *testing.T) {
		fake := setupFakeOpencode(t, false, "v0.2.0")
		writeOpencodeFile(t, filepath.Join(fake.global, "opencode.json"), `{"plugin": ["@jgabor/ktx@0.2.0"]}`)
		writeOpencodeFile(t, filepath.Join(fake.projectConfig(), "opencode.json"), `{"plugin": ["@jgabor/ktx@0.2.0"]}`)
		code, stdout, stderr := runOpencodeInit(t, FormatText, "--uninstall", "--project")
		if code != ExitOK || !strings.Contains(stdout, "removed @jgabor/ktx@0.2.0 from "+filepath.Join(fake.projectConfig(), "opencode.json")) {
			t.Fatalf("exit = %d, stdout=%q stderr=%q", code, stdout, stderr)
		}
		if got := readOpencodeFile(t, filepath.Join(fake.global, "opencode.json")); got != `{"plugin": ["@jgabor/ktx@0.2.0"]}` {
			t.Fatalf("global config = %q", got)
		}
	})
	t.Run("nothing installed exits 2", func(t *testing.T) {
		fake := setupFakeOpencode(t, false, "v0.2.0")
		users := filepath.Join(fake.global, "plugins", "ktx.ts")
		writeOpencodeFile(t, users, "export default async () => ({})\n")
		writeOpencodeFile(t, filepath.Join(fake.global, "opencode.json"), `{"plugin": ["other"]}`)
		code, _, stderr := runOpencodeInit(t, FormatJSON, "--uninstall")
		if code != ExitNotFound || !strings.Contains(stderr, "not installed") {
			t.Fatalf("exit = %d, stderr=%q", code, stderr)
		}
		if readOpencodeFile(t, users) != "export default async () => ({})\n" || readOpencodeFile(t, filepath.Join(fake.global, "opencode.json")) != `{"plugin": ["other"]}` {
			t.Fatal("uninstall touched files that are not ktx's")
		}
	})
}
