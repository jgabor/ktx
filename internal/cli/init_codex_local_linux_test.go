package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCodexSharedLocalCLI runs a locally built binary against a fake codex
// and a project hooks file that earlier ktx installations, under other paths,
// merged their hooks into.
func TestCodexSharedLocalCLI(t *testing.T) {
	built := os.Getenv("KTX_CODEX_CLI")
	if built == "" {
		t.Skip("set KTX_CODEX_CLI to the locally built binary")
	}
	root := t.TempDir()
	fake := fakeCodex{dir: filepath.Join(root, "fake"), bin: filepath.Join(root, "bin"), project: filepath.Join(root, "project")}
	for _, dir := range []string{fake.dir, fake.bin, fake.project} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fake.bin, "codex"), []byte(fakeCodexScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fake.write(t, "marketplaces.json", codexNoMarketplaces)
	fake.write(t, "plugins.json", codexNoPlugins)
	target := fake.projectHooks()
	run := func(want int, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, built, append([]string{"init", "codex", "--format", "json"}, args...)...)
		cmd.Dir = fake.project
		cmd.Env = []string{"PATH=" + fake.bin, "FAKE_CODEX_DIR=" + fake.dir}
		isolated := t.TempDir()
		for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR", "TMP", "TEMP"} {
			path := filepath.Join(isolated, key)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			cmd.Env = append(cmd.Env, key+"="+path)
		}
		out, err := cmd.Output()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("%v: exit=%d want=%d: %s", args, code, want, out)
		}
		if code == ExitOK && !json.Valid(bytes.TrimSpace(out)) {
			t.Fatalf("invalid JSON report: %s", out)
		}
		return out
	}

	old := ktxCodexPostToolUseEntry(quoteCodexHookPath(filepath.Join(root, "old 'quoted' $literal; path", "ktx")) + " hook")
	old["future"] = "outer-kept"
	old["hooks"] = append(old["hooks"].([]any), map[string]any{"type": "command", "command": "'/other/program' hook", "future": json.Number("9007199254740993")})
	doc := map[string]any{
		"hooks": map[string]any{
			"PostToolUse":  []any{old, ktxCodexPostToolUseEntry(quoteCodexHookPath(filepath.Join(root, "new path", "renamed-binary")) + " hook"), ktxCodexPostToolUseEntry(ktxHookCommand)},
			"SessionStart": []any{map[string]any{"future": true}},
		},
		"unknown": json.Number("9007199254740993"),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeHooksFile(t, target, string(raw))

	out := run(ExitOK)
	var report struct {
		Removed []codexLegacyHooks `json:"removed_legacy_hooks"`
	}
	if err := json.Unmarshal(out, &report); err != nil || len(report.Removed) != 1 || report.Removed[0].Path != target || report.Removed[0].Entries != 3 {
		t.Fatalf("report = %s %v", out, err)
	}
	if got := fake.calls(t); strings.Join(got, "\n") != "plugin marketplace add jgabor/ktx\nplugin add ktx@ktx" {
		t.Fatalf("calls = %v", got)
	}
	doc, _, err = loadCodexHooks(target)
	if err != nil {
		t.Fatal(err)
	}
	entries := codexPostToolUseArray(codexHooksObject(doc))
	if len(entries) != 1 {
		t.Fatalf("PostToolUse = %v", entries)
	}
	entry := entries[0].(map[string]any)
	if doc["unknown"] != json.Number("9007199254740993") || entry["future"] != "outer-kept" || len(entry["hooks"].([]any)) != 1 || codexHooksObject(doc)["SessionStart"] == nil {
		t.Fatalf("merged siblings lost: %v", doc)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions changed: %v %v", info, err)
	}

	// A re-run changes neither Codex nor the hooks file.
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	run(ExitOK)
	if got := fake.calls(t); len(got) != 2 {
		t.Fatalf("re-run changed Codex: %v", got)
	}
	if after, err := os.ReadFile(target); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("re-run changed the hooks file: %s %v", after, err)
	}

	writeHooksFile(t, target, `{"hooks":false}`)
	run(ExitInternal)
	if after, err := os.ReadFile(target); err != nil || string(after) != `{"hooks":false}` {
		t.Fatalf("malformed document changed: %s %v", after, err)
	}
}
