// Tests for `ktx init claude-code` (internal/cli/init_claude_code.go).
//
// Each test points HOME and XDG_DATA_HOME at per-test temporary folders and
// replaces the executable resolver, so no test reads the real data folder or
// depends on where the test binary lives.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/cli/claudecode"
)

// setupClaudeCode isolates HOME and XDG_DATA_HOME, pins an executable file at
// exePath (created with mode 0755 when it does not exist), and returns the
// marketplace root.
func setupClaudeCode(t *testing.T, dataHome, exePath string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", dataHome)
	if err := os.MkdirAll(filepath.Dir(exePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exePath); os.IsNotExist(err) {
		if err := os.WriteFile(exePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	previous := claudeCodeExecutable
	claudeCodeExecutable = func() (string, error) { return exePath, nil }
	t.Cleanup(func() { claudeCodeExecutable = previous })
	return filepath.Join(dataHome, "ktx", "claude-code", "marketplace")
}

func runClaudeCodeInit(t *testing.T, format string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runInitWith(append([]string{"claude-code"}, args...), Options{Format: format}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func decodeJSONObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	return value
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return decodeJSONObject(t, string(data))
}

func TestInitClaudeCodeInstallGeneratesMarketplace(t *testing.T) {
	base := t.TempDir()
	exe := filepath.Join(base, "bin", "ktx")
	root := setupClaudeCode(t, filepath.Join(base, "data"), exe)

	code, stdout, stderr := runClaudeCodeInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	report := decodeJSONObject(t, stdout)
	if report["installed"] != true || report["path"] != root || report["ktx_executable"] != exe {
		t.Fatalf("report = %v", report)
	}
	if report["runtime_activation"] != "not_verified" || report["marketplace"] != "ktx-local" {
		t.Fatalf("report = %v", report)
	}
	if _, ok := report["already_installed"]; ok {
		t.Fatalf("first install reported already_installed: %v", report)
	}
	revision, _ := report["generated_revision"].(string)
	if len(revision) != 64 {
		t.Fatalf("generated_revision = %q", revision)
	}
	wantSteps := []any{"claude plugin marketplace add " + root}
	for _, name := range claudeCodePluginNames() {
		wantSteps = append(wantSteps, "claude plugin install "+name+"@ktx-local")
	}
	if !reflect.DeepEqual(report["next_steps"], wantSteps) {
		t.Fatalf("next_steps = %v, want %v", report["next_steps"], wantSteps)
	}

	hooks := readJSONFile(t, filepath.Join(root, "plugins", "ktx", "hooks", "hooks.json"))
	wantHooks := map[string]any{
		"PostToolUse": []any{map[string]any{
			"matcher": "Bash",
			"hooks": []any{map[string]any{
				"type":          "command",
				"command":       exe,
				"args":          []any{"hook", "--runtime", "claude-code"},
				"statusMessage": "ktx: compressing tool output",
			}},
		}},
	}
	if !reflect.DeepEqual(hooks["hooks"], wantHooks) {
		t.Fatalf("hooks = %#v, want %#v", hooks["hooks"], wantHooks)
	}

	marketplace := readJSONFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"))
	if marketplace["name"] != "ktx-local" {
		t.Fatalf("marketplace name = %v", marketplace["name"])
	}
	owner, _ := marketplace["owner"].(map[string]any)
	if owner["name"] == "" || owner["name"] == nil {
		t.Fatalf("marketplace owner = %v", marketplace["owner"])
	}
	entries, _ := marketplace["plugins"].([]any)
	if len(entries) != len(claudeCodePlugins) {
		t.Fatalf("plugins = %v", entries)
	}
	for index, plugin := range claudeCodePlugins {
		entry, _ := entries[index].(map[string]any)
		if entry["name"] != plugin.name || entry["source"] != "./plugins/"+plugin.name {
			t.Fatalf("entry %d = %v", index, entry)
		}
		manifest := readJSONFile(t, filepath.Join(root, "plugins", plugin.name, ".claude-plugin", "plugin.json"))
		if manifest["name"] != plugin.name {
			t.Fatalf("%s manifest name = %v", plugin.name, manifest["name"])
		}
	}

	marker := readJSONFile(t, filepath.Join(root, ".ktx-generated.json"))
	if marker["ktx_executable"] != exe || marker["revision"] != revision || marker["schema_version"] != float64(1) {
		t.Fatalf("marker = %v", marker)
	}
	listed := []string{}
	for _, file := range marker["files"].([]any) {
		listed = append(listed, file.(string))
	}
	onDisk := []string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() != ".ktx-generated.json" {
			relative, _ := filepath.Rel(root, path)
			onDisk = append(onDisk, filepath.ToSlash(relative))
		}
		return nil
	})
	sort.Strings(onDisk)
	if !reflect.DeepEqual(listed, onDisk) {
		t.Fatalf("marker files = %v, on disk = %v", listed, onDisk)
	}
	for _, name := range listed {
		data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if bytes.Contains(data, []byte("__KTX_EXECUTABLE__")) {
			t.Fatalf("%s still holds the executable placeholder", name)
		}
	}
}

func TestInitClaudeCodeInstallIsIdempotent(t *testing.T) {
	base := t.TempDir()
	root := setupClaudeCode(t, filepath.Join(base, "data"), filepath.Join(base, "bin", "ktx"))
	if code, _, stderr := runClaudeCodeInit(t, FormatJSON); code != ExitOK {
		t.Fatalf("first install exit = %d: %s", code, stderr)
	}
	old := time.Now().Add(-time.Hour)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			_ = os.Chtimes(path, old, old)
		}
		return nil
	})
	code, stdout, stderr := runClaudeCodeInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("second install exit = %d: %s", code, stderr)
	}
	if report := decodeJSONObject(t, stdout); report["already_installed"] != true {
		t.Fatalf("second install report = %v", report)
	}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			info, _ := os.Stat(path)
			if !info.ModTime().Equal(old) {
				t.Errorf("%s was rewritten", path)
			}
		}
		return nil
	})

	code, stdout, _ = runClaudeCodeInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "already up to date") || !strings.Contains(stdout, "claude plugin install ktx@ktx-local") {
		t.Fatalf("text install = %d %q", code, stdout)
	}
}

func TestInitClaudeCodeUnusualPaths(t *testing.T) {
	base := t.TempDir()
	exe := filepath.Join(base, `bin with space 'single' "double" back\slash $HOME`, "ktx")
	dataHome := filepath.Join(base, "data home's dir")
	root := setupClaudeCode(t, dataHome, exe)

	code, stdout, stderr := runClaudeCodeInit(t, FormatText)
	if code != ExitOK {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	quotedRoot := `'` + strings.ReplaceAll(root, `'`, `'"'"'`) + `'`
	if !strings.Contains(stdout, "claude plugin marketplace add "+quotedRoot+"\n") {
		t.Fatalf("text report lacks quoted root %s:\n%s", quotedRoot, stdout)
	}
	hooks := readJSONFile(t, filepath.Join(root, "plugins", "ktx", "hooks", "hooks.json"))
	inner := hooks["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if inner["command"] != exe {
		t.Fatalf("command = %q, want %q", inner["command"], exe)
	}
	if got := pinnedStatsExecutable(t, root); got != exe {
		t.Fatalf("ktx-stats pins %q, want %q", got, exe)
	}
	if code, stdout, _ := runClaudeCodeInit(t, FormatJSON, "--show"); code != ExitOK || decodeJSONObject(t, stdout)["stale"] != false {
		t.Fatalf("show = %d %s", code, stdout)
	}
}

// pinnedStatsExecutable reads back the executable literal generated into the
// ktx-stats hooks module.
func pinnedStatsExecutable(t *testing.T, root string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(root, "plugins", "ktx-stats", "hooks", "register.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "const KTX_EXECUTABLE: string = "
	matches := 0
	var literal string
	for _, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(line, prefix) {
			matches++
			literal = strings.TrimSuffix(strings.TrimPrefix(line, prefix), ";")
		}
	}
	if matches != 1 {
		t.Fatalf("register.tsx has %d executable lines", matches)
	}
	var path string
	if err := json.Unmarshal([]byte(literal), &path); err != nil {
		t.Fatalf("executable literal %s: %v", literal, err)
	}
	return path
}

func TestInitClaudeCodeGeneratesStatsMod(t *testing.T) {
	base := t.TempDir()
	exe := filepath.Join(base, "bin", "ktx")
	root := setupClaudeCode(t, filepath.Join(base, "data"), exe)
	code, stdout, stderr := runClaudeCodeInit(t, FormatText)
	if code != ExitOK {
		t.Fatalf("install: %s", stderr)
	}
	if !strings.Contains(stdout, "claude plugin install ktx-stats@ktx-local") {
		t.Fatalf("install output lacks the ktx-stats step:\n%s", stdout)
	}
	if got := pinnedStatsExecutable(t, root); got != exe {
		t.Fatalf("ktx-stats pins %q, want %q", got, exe)
	}
	mod := filepath.Join(root, "plugins", "ktx-stats")
	if hooks := readJSONFile(t, filepath.Join(mod, "hooks", "hooks.json")); !reflect.DeepEqual(hooks, map[string]any{"modules": []any{"./register.tsx"}}) {
		t.Fatalf("ktx-stats hooks.json = %v", hooks)
	}
	if manifest := readJSONFile(t, filepath.Join(mod, ".claude-plugin", "plugin.json")); manifest["types"] != "./types/index.d.ts" {
		t.Fatalf("ktx-stats manifest = %v", manifest)
	}
	for _, name := range []string{"hooks/stats.ts", "types/index.d.ts"} {
		want, err := claudecode.Files.ReadFile("plugins/ktx-stats/" + name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(mod, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s differs from its source: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(mod, "tests")); !os.IsNotExist(err) {
		t.Fatalf("test files were generated: %v", err)
	}
}

func TestInitClaudeCodeShow(t *testing.T) {
	t.Run("not generated exits 2", func(t *testing.T) {
		base := t.TempDir()
		setupClaudeCode(t, filepath.Join(base, "data"), filepath.Join(base, "bin", "ktx"))
		code, stdout, stderr := runClaudeCodeInit(t, FormatJSON, "--show")
		if code != ExitNotFound || stdout != "" || !strings.Contains(stderr, "not generated") {
			t.Fatalf("show = %d %q %q", code, stdout, stderr)
		}
	})

	t.Run("fresh, then stale for each reason", func(t *testing.T) {
		base := t.TempDir()
		exe := filepath.Join(base, "bin", "ktx")
		root := setupClaudeCode(t, filepath.Join(base, "data"), exe)
		if code, _, stderr := runClaudeCodeInit(t, FormatJSON); code != ExitOK {
			t.Fatalf("install: %s", stderr)
		}
		code, stdout, _ := runClaudeCodeInit(t, FormatJSON, "--show")
		report := decodeJSONObject(t, stdout)
		if code != ExitOK || report["installed"] != true || report["stale"] != false || report["runtime_activation"] != "not_verified" {
			t.Fatalf("show = %d %v", code, report)
		}
		if report["generated_revision"] != report["current_revision"] || report["ktx_executable"] != exe {
			t.Fatalf("show = %v", report)
		}

		// Another binary running --show does not make the generation stale.
		claudeCodeExecutable = func() (string, error) { return filepath.Join(base, "elsewhere", "ktx"), nil }
		if _, stdout, _ := runClaudeCodeInit(t, FormatJSON, "--show"); decodeJSONObject(t, stdout)["stale"] != false {
			t.Fatalf("show from another binary = %s", stdout)
		}

		hooksPath := filepath.Join(root, "plugins", "ktx", "hooks", "hooks.json")
		if err := os.WriteFile(hooksPath, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(exe); err != nil {
			t.Fatal(err)
		}
		code, stdout, _ = runClaudeCodeInit(t, FormatJSON, "--show")
		report = decodeJSONObject(t, stdout)
		reasons := strings.Join(toStrings(report["stale_reasons"]), "\n")
		if code != ExitOK || report["stale"] != true ||
			!strings.Contains(reasons, "changed: plugins/ktx/hooks/hooks.json") ||
			!strings.Contains(reasons, "missing or not executable") {
			t.Fatalf("stale show = %d %v", code, report)
		}

		if err := os.Remove(hooksPath); err != nil {
			t.Fatal(err)
		}
		code, stdout, _ = runClaudeCodeInit(t, FormatText, "--show")
		if code != ExitOK || !strings.Contains(stdout, "stale: yes") || !strings.Contains(stdout, "missing: plugins/ktx/hooks/hooks.json") ||
			!strings.Contains(stdout, "Claude Code registration: not verified") {
			t.Fatalf("text show = %d %q", code, stdout)
		}
	})

	t.Run("a revision from another version is stale", func(t *testing.T) {
		base := t.TempDir()
		root := setupClaudeCode(t, filepath.Join(base, "data"), filepath.Join(base, "bin", "ktx"))
		if code, _, stderr := runClaudeCodeInit(t, FormatJSON); code != ExitOK {
			t.Fatalf("install: %s", stderr)
		}
		markerPath := filepath.Join(root, ".ktx-generated.json")
		marker := readJSONFile(t, markerPath)
		marker["revision"] = strings.Repeat("0", 64)
		data, _ := json.Marshal(marker)
		if err := os.WriteFile(markerPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		_, stdout, _ := runClaudeCodeInit(t, FormatJSON, "--show")
		report := decodeJSONObject(t, stdout)
		if report["stale"] != true || !strings.Contains(strings.Join(toStrings(report["stale_reasons"]), "\n"), "different ktx version") {
			t.Fatalf("show = %v", report)
		}
	})

	t.Run("a malformed marker is an internal error", func(t *testing.T) {
		base := t.TempDir()
		root := setupClaudeCode(t, filepath.Join(base, "data"), filepath.Join(base, "bin", "ktx"))
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, content := range []string{"{", `{"schema_version":1,"files":["../escape"]}`, `{"schema_version":9,"files":[]}`} {
			if err := os.WriteFile(filepath.Join(root, ".ktx-generated.json"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"--show"}, {"--uninstall"}, {}} {
				if code, _, stderr := runClaudeCodeInit(t, FormatJSON, args...); code != ExitInternal {
					t.Fatalf("%v with marker %q: exit %d %s", args, content, code, stderr)
				}
			}
		}
	})
}

func toStrings(value any) []string {
	out := []string{}
	items, _ := value.([]any)
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestInitClaudeCodeUninstall(t *testing.T) {
	t.Run("not generated exits 2", func(t *testing.T) {
		base := t.TempDir()
		setupClaudeCode(t, filepath.Join(base, "data"), filepath.Join(base, "bin", "ktx"))
		if code, _, stderr := runClaudeCodeInit(t, FormatJSON, "--uninstall"); code != ExitNotFound {
			t.Fatalf("uninstall = %d %s", code, stderr)
		}
	})

	t.Run("removes generated files and type folders, keeps others", func(t *testing.T) {
		base := t.TempDir()
		dataHome := filepath.Join(base, "data")
		root := setupClaudeCode(t, dataHome, filepath.Join(base, "bin", "ktx"))
		if code, _, stderr := runClaudeCodeInit(t, FormatJSON); code != ExitOK {
			t.Fatalf("install: %s", stderr)
		}
		types := filepath.Join(root, "plugins", "ktx", ".claude-plugin", "types", "claude-code")
		if err := os.MkdirAll(types, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(types, "index.d.ts"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		notes := filepath.Join(root, "plugins", "ktx", "notes.txt")
		if err := os.WriteFile(notes, []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}

		code, stdout, stderr := runClaudeCodeInit(t, FormatText, "--uninstall")
		if code != ExitOK {
			t.Fatalf("uninstall = %d %s", code, stderr)
		}
		if !strings.Contains(stdout, "claude plugin marketplace remove ktx-local") {
			t.Fatalf("stdout lacks remove command: %q", stdout)
		}
		if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, notes) {
			t.Fatalf("stderr lacks warning for %s: %q", notes, stderr)
		}
		if data, err := os.ReadFile(notes); err != nil || string(data) != "mine" {
			t.Fatalf("unknown file was not kept: %v", err)
		}
		remaining := []string{}
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil {
				relative, _ := filepath.Rel(root, path)
				remaining = append(remaining, filepath.ToSlash(relative))
			}
			return nil
		})
		want := []string{".", "plugins", "plugins/ktx", "plugins/ktx/notes.txt"}
		if !reflect.DeepEqual(remaining, want) {
			t.Fatalf("remaining = %v, want %v", remaining, want)
		}

		if err := os.Remove(notes); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := runClaudeCodeInit(t, FormatJSON, "--uninstall"); code != ExitNotFound {
			t.Fatalf("second uninstall = %d", code)
		}
	})

	t.Run("a clean uninstall removes the folder", func(t *testing.T) {
		base := t.TempDir()
		dataHome := filepath.Join(base, "data")
		root := setupClaudeCode(t, dataHome, filepath.Join(base, "bin", "ktx"))
		if code, _, stderr := runClaudeCodeInit(t, FormatJSON); code != ExitOK {
			t.Fatalf("install: %s", stderr)
		}
		code, stdout, _ := runClaudeCodeInit(t, FormatJSON, "--uninstall")
		report := decodeJSONObject(t, stdout)
		if code != ExitOK || report["uninstalled"] != true || report["next_command"] != "claude plugin marketplace remove ktx-local" ||
			len(toStrings(report["left_files"])) != 0 {
			t.Fatalf("uninstall = %d %v", code, report)
		}
		if _, err := os.Stat(filepath.Dir(root)); !os.IsNotExist(err) {
			t.Fatalf("claude-code folder still exists: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataHome, "ktx")); err != nil {
			t.Fatalf("ktx data folder removed: %v", err)
		}
	})
}

func TestInitClaudeCodeRemovesOutdatedFiles(t *testing.T) {
	base := t.TempDir()
	root := setupClaudeCode(t, filepath.Join(base, "data"), filepath.Join(base, "bin", "ktx"))
	if code, _, stderr := runClaudeCodeInit(t, FormatJSON); code != ExitOK {
		t.Fatalf("install: %s", stderr)
	}
	outdated := filepath.Join(root, "plugins", "retired", "hooks", "old.json")
	if err := os.MkdirAll(filepath.Dir(outdated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outdated, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(root, ".ktx-generated.json")
	marker := readJSONFile(t, markerPath)
	marker["files"] = append(marker["files"].([]any), "plugins/retired/hooks/old.json")
	data, _ := json.Marshal(marker)
	if err := os.WriteFile(markerPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runClaudeCodeInit(t, FormatJSON)
	if code != ExitOK || decodeJSONObject(t, stdout)["already_installed"] != nil {
		t.Fatalf("install = %d %s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "retired")); !os.IsNotExist(err) {
		t.Fatalf("outdated plugin folder still exists: %v", err)
	}
}

func TestInitClaudeCodeRejectsProject(t *testing.T) {
	base := t.TempDir()
	root := setupClaudeCode(t, filepath.Join(base, "data"), filepath.Join(base, "bin", "ktx"))
	for _, args := range [][]string{{"--project"}, {"--project", "--show"}, {"--project", "--uninstall"}} {
		code, stdout, stderr := runClaudeCodeInit(t, FormatJSON, args...)
		if code != ExitUsage || stdout != "" || !strings.Contains(stderr, "--project") {
			t.Fatalf("%v = %d %q %q", args, code, stdout, stderr)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("rejected --project still wrote %s", root)
	}
}

func TestInitHelpListsClaudeCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runInitWith([]string{"--help"}, Options{Format: FormatText}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("help exit = %d", code)
	}
	for _, want := range []string{"claude-code", "hook --runtime claude-code", "/reload-plugins", "ktx init claude-code --show"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help lacks %q", want)
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := runInitWith([]string{"bogus"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitUsage || !strings.Contains(stderr.String(), "claude-code") {
		t.Fatalf("unknown runtime = %d %q", code, stderr.String())
	}
}
