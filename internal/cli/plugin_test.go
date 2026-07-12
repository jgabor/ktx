package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// userConfigPath returns the user-level config path
// ($XDG_CONFIG_HOME/ktx/ktx.toml). Requires XDG_CONFIG_HOME to be
// set (pluginTestEnv sets it).
func userConfigPath(t *testing.T) string {
	t.Helper()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		t.Fatal("XDG_CONFIG_HOME not set")
	}
	return filepath.Join(xdg, "ktx", "ktx.toml")
}

// pluginTestEnv isolates cwd and XDG_CONFIG_HOME for plugin tests.
// Returns a cleanup function that restores the original cwd.
func pluginTestEnv(t *testing.T) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})
}

// runPluginDispatch invokes the plugin verb through the dispatch
// registry (the real CLI path) so tests exercise the same routing
// as `ktx plugin ...`.
func runPluginDispatch(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runDispatch("plugin", args, false, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// decodePluginEnvelope decodes a JSON envelope from stdout into a
// generic map for assertion.
func decodePluginEnvelope(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("decode JSON: %v\nstdout was: %s", err, stdout)
	}
	return m
}

func TestPluginAdd_CreatesNewKtxToml(t *testing.T) {
	pluginTestEnv(t)

	code, stdout, stderr := runPluginDispatch(t, []string{"add", "mycat", "/bin/cat", "--description", "passthrough"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	// Verify JSON envelope.
	env := decodePluginEnvelope(t, stdout)
	p, ok := env["plugin"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'plugin' key: %v", env)
	}
	if p["name"] != "mycat" {
		t.Errorf("name = %v, want mycat", p["name"])
	}
	if p["command"] != "/bin/cat" {
		t.Errorf("command = %v, want /bin/cat", p["command"])
	}
	if p["description"] != "passthrough" {
		t.Errorf("description = %v, want passthrough", p["description"])
	}

	// Verify ktx.toml was created.
	data, err := os.ReadFile(userConfigPath(t))
	if err != nil {
		t.Fatalf("read ktx.toml: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[[plugin]]") {
		t.Errorf("ktx.toml missing [[plugin]] block:\n%s", content)
	}
	if !strings.Contains(content, `name = "mycat"`) {
		t.Errorf("ktx.toml missing name field:\n%s", content)
	}
	if !strings.Contains(content, `command = "/bin/cat"`) {
		t.Errorf("ktx.toml missing command field:\n%s", content)
	}
	if !strings.Contains(content, `description = "passthrough"`) {
		t.Errorf("ktx.toml missing description field:\n%s", content)
	}
}

func TestPluginAdd_AppendsToExistingKtxToml(t *testing.T) {
	pluginTestEnv(t)

	// Create an existing ktx.toml with a pipeline.
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "head:100"]
`)

	code, stdout, stderr := runPluginDispatch(t, []string{"add", "myecho", "/bin/echo"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	// Verify JSON envelope has no description (omitted → empty).
	env := decodePluginEnvelope(t, stdout)
	p, _ := env["plugin"].(map[string]any)
	if p["description"] != "" {
		t.Errorf("description = %v, want empty", p["description"])
	}

	// Verify ktx.toml preserves existing content AND has new plugin.
	data, _ := os.ReadFile(userConfigPath(t))
	content := string(data)
	if !strings.Contains(content, "[pipeline.default]") {
		t.Errorf("existing [pipeline.default] lost:\n%s", content)
	}
	if !strings.Contains(content, "strip-ansi") {
		t.Errorf("existing stages lost:\n%s", content)
	}
	if !strings.Contains(content, "[[plugin]]") {
		t.Errorf("new [[plugin]] block missing:\n%s", content)
	}
	if !strings.Contains(content, `name = "myecho"`) {
		t.Errorf("new plugin name missing:\n%s", content)
	}
}

func TestPluginAdd_DuplicateNameRejected(t *testing.T) {
	pluginTestEnv(t)

	hookWriteUserConfig(t, "[[plugin]]\nname = \"mycat\"\ncommand = \"/bin/cat\"\n")

	code, _, stderr := runPluginDispatch(t, []string{"add", "mycat", "/bin/echo"})

	if code != ExitConflict {
		t.Fatalf("exit=%d, want %d (conflict); stderr=%q", code, ExitConflict, stderr)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr missing 'already exists': %q", stderr)
	}
}

func TestPluginList_NoUserPlugins(t *testing.T) {
	pluginTestEnv(t)
	// No ktx.toml.

	code, stdout, stderr := runPluginDispatch(t, []string{"list"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	env := decodePluginEnvelope(t, stdout)
	plugins, ok := env["plugins"].([]any)
	if !ok {
		t.Fatalf("envelope missing 'plugins' array: %v", env)
	}
	if len(plugins) != 10 {
		t.Fatalf("plugins count = %d, want 10 builtins", len(plugins))
	}
	// Verify first entry is a builtin.
	first := plugins[0].(map[string]any)
	if first["builtin"] != true {
		t.Errorf("first plugin builtin = %v, want true", first["builtin"])
	}
	if first["removable"] != false {
		t.Errorf("first plugin removable = %v, want false", first["removable"])
	}
	// Verify built-in names are present.
	names := make([]string, 0, len(plugins))
	for _, p := range plugins {
		names = append(names, p.(map[string]any)["name"].(string))
	}
	for _, want := range []string{"strip-ansi", "dedup-blank", "normalize", "head", "truncate", "keep-ends", "grep", "grep-v", "token-budget", "replace"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("builtin %q not found in plugin list: %v", want, names)
		}
	}
	// All built-ins must report builtin=true, removable=false.
	for _, p := range plugins {
		entry := p.(map[string]any)
		if entry["builtin"] != true {
			t.Errorf("builtin stage %q has builtin=%v, want true", entry["name"], entry["builtin"])
		}
		if entry["removable"] != false {
			t.Errorf("builtin stage %q has removable=%v, want false", entry["name"], entry["removable"])
		}
	}
}

func TestPluginList_WithUserPlugins(t *testing.T) {
	pluginTestEnv(t)

	hookWriteUserConfig(t, "[[plugin]]\nname = \"mycat\"\ncommand = \"/bin/cat\"\ndescription = \"test\"\n")

	code, stdout, stderr := runPluginDispatch(t, []string{"list"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	env := decodePluginEnvelope(t, stdout)
	plugins := env["plugins"].([]any)
	if len(plugins) != 11 {
		t.Fatalf("plugins count = %d, want 11 (10 builtins + 1 user)", len(plugins))
	}

	// User plugin should be the last entry.
	last := plugins[10].(map[string]any)
	if last["name"] != "mycat" {
		t.Errorf("user plugin name = %v, want mycat", last["name"])
	}
	if last["builtin"] != false {
		t.Errorf("user plugin builtin = %v, want false", last["builtin"])
	}
	if last["removable"] != true {
		t.Errorf("user plugin removable = %v, want true", last["removable"])
	}
	if last["command"] != "/bin/cat" {
		t.Errorf("user plugin command = %v, want /bin/cat", last["command"])
	}
}

func TestPluginRemove_UserPlugin(t *testing.T) {
	pluginTestEnv(t)

	hookWriteUserConfig(t, `[pipeline.default]
stages = ["mycat"]

[[plugin]]
name = "mycat"
command = "/bin/cat"
description = "test"
`)

	code, stdout, stderr := runPluginDispatch(t, []string{"remove", "mycat"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	// Verify JSON envelope.
	env := decodePluginEnvelope(t, stdout)
	removed, ok := env["removed"].(map[string]any)
	if !ok {
		t.Fatalf("envelope missing 'removed' key: %v", env)
	}
	if removed["name"] != "mycat" {
		t.Errorf("removed.name = %v, want mycat", removed["name"])
	}

	// Verify ktx.toml no longer has the [[plugin]] block.
	data, _ := os.ReadFile(userConfigPath(t))
	content := string(data)
	if strings.Contains(content, "[[plugin]]") {
		t.Errorf("ktx.toml still has [[plugin]] block:\n%s", content)
	}
	// The [pipeline.default].stages array may still reference the
	// removed plugin name — that's expected (remove only deletes the
	// [[plugin]] block, not its references in pipeline configs).
	// Verify pipeline header is preserved.
	if !strings.Contains(content, "[pipeline.default]") {
		t.Errorf("ktx.toml lost [pipeline.default] section:\n%s", content)
	}
}

func TestPluginRemove_BuiltinWithoutOverride(t *testing.T) {
	pluginTestEnv(t)

	// No ktx.toml — 'head' is a builtin without override.
	code, _, stderr := runPluginDispatch(t, []string{"remove", "head"})

	if code != ExitUsage {
		t.Fatalf("exit=%d, want %d (usage)", code, ExitUsage)
	}

	// Verify self-correcting JSON error.
	var errEnv map[string]any
	if err := json.Unmarshal([]byte(stderr), &errEnv); err != nil {
		t.Fatalf("decode stderr JSON: %v\nstderr was: %q", err, stderr)
	}
	errObj, ok := errEnv["error"].(map[string]any)
	if !ok {
		t.Fatalf("stderr missing 'error' object: %v", errEnv)
	}
	if errObj["code"] != "usage" {
		t.Errorf("error code = %v, want usage", errObj["code"])
	}
	msg, ok := errObj["message"].(string)
	if !ok || !strings.Contains(msg, "head") {
		t.Errorf("error message = %v, want it to contain 'head'", errObj["message"])
	}
	if _, ok := errObj["valid_examples"]; !ok {
		t.Errorf("error envelope missing 'valid_examples' field")
	}
}

func TestPluginRemove_BuiltinWithOverride(t *testing.T) {
	pluginTestEnv(t)

	// User has a [[plugin]] named "head" (overrides builtin).
	hookWriteUserConfig(t, "[[plugin]]\nname = \"head\"\ncommand = \"/bin/cat\"\n")

	code, stdout, stderr := runPluginDispatch(t, []string{"remove", "head"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	// Verify removed envelope.
	env := decodePluginEnvelope(t, stdout)
	removed, _ := env["removed"].(map[string]any)
	if removed["name"] != "head" {
		t.Errorf("removed.name = %v, want head", removed["name"])
	}

	// Verify ktx.toml no longer has the override.
	data, _ := os.ReadFile(userConfigPath(t))
	content := string(data)
	if strings.Contains(content, "[[plugin]]") {
		t.Errorf("ktx.toml still has [[plugin]] block:\n%s", content)
	}
}

func TestPluginAdd_MissingArguments(t *testing.T) {
	pluginTestEnv(t)

	code, _, stderr := runPluginDispatch(t, []string{"add", "mycat"})
	if code != ExitUsage {
		t.Fatalf("exit=%d, want %d (usage)", code, ExitUsage)
	}
	if !strings.Contains(stderr, "missing required arguments") {
		t.Errorf("stderr missing usage message: %q", stderr)
	}
}

func TestPluginHelp(t *testing.T) {
	var stdout bytes.Buffer
	code := renderHelp(printPluginHelp, &stdout)
	if code != ExitOK {
		t.Fatalf("renderHelp exit=%d", code)
	}
	help := stdout.String()
	for _, want := range []string{"add", "list", "remove", "ktx init"} {
		if !strings.Contains(help, want) {
			t.Errorf("help text missing %q:\n%s", want, help)
		}
	}
}
