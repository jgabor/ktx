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

func TestCodexSharedLocalCLI(t *testing.T) {
	built := os.Getenv("KTX_CODEX_CLI")
	if built == "" {
		t.Skip("set KTX_CODEX_CLI to the locally built binary")
	}
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var binaries []string
	for _, name := range []string{"old 'quoted' $literal; path/ktx", "new path/renamed-binary"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
		binaries = append(binaries, path)
	}
	link := filepath.Join(root, "binary-link")
	if err := os.Symlink(binaries[1], link); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(project, ".codex", "hooks.json")
	run := func(binary string, want int, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"init", "codex", "--format", "json"}, args...)...)
		cmd.Dir = project
		cmd.Env = os.Environ()
		for i := len(cmd.Env) - 1; i >= 0; i-- {
			if strings.HasPrefix(cmd.Env[i], "KTX_DB=") {
				cmd.Env = append(cmd.Env[:i], cmd.Env[i+1:]...)
			}
		}
		isolated := t.TempDir()
		for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR", "TMP", "TEMP"} {
			path := filepath.Join(isolated, key)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			cmd.Env = append(cmd.Env, key+"="+path)
		}
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("%s %v: exit=%d want=%d: %s", binary, args, code, want, out)
		}
		if !json.Valid(bytes.TrimSpace(out)) {
			t.Fatalf("invalid JSON report: %s", out)
		}
		return out
	}
	run(binaries[0], ExitOK, "--project")
	original, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	run(binaries[1], ExitOK, "--show")
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("show changed document: %s %v", after, err)
	}
	shown, err := os.Stat(target)
	if err != nil || !shown.ModTime().Equal(info.ModTime()) || !os.SameFile(info, shown) {
		t.Fatalf("show replaced document: %v", err)
	}
	// Uninstall from the new binary without first reinstalling it.
	run(binaries[1], ExitOK, "--uninstall", "--project")
	run(binaries[1], ExitNotFound, "--show")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("owned-only document remains: %v", err)
	}
	run(binaries[0], ExitOK, "--project")
	doc, _, err := loadCodexHooks(target)
	if err != nil {
		t.Fatal(err)
	}
	entry := codexPostToolUseArray(codexHooksObject(doc))[0].(map[string]any)
	entry["future"] = "outer-kept"
	entry["hooks"] = append(entry["hooks"].([]any), map[string]any{"type": "command", "command": "'/other/program' hook", "future": json.Number("9007199254740993")})
	// Include a duplicate of the currently generated command as well as the old
	// one: an idempotency shortcut must not leave duplicate owned entries.
	newEntry := ktxCodexPostToolUseEntry(quoteCodexHookPath(binaries[1]))
	hooks := codexHooksObject(doc)
	hooks["PostToolUse"] = append(codexPostToolUseArray(hooks), newEntry)
	hooks["SessionStart"] = []any{map[string]any{"future": true}}
	doc["unknown"] = json.Number("9007199254740993")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	run(link, ExitOK, "--project")
	doc, _, err = loadCodexHooks(target)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, value := range codexPostToolUseArray(codexHooksObject(doc)) {
		entry := value.(map[string]any)
		for _, value := range entry["hooks"].([]any) {
			hook := value.(map[string]any)
			if isKtxCodexHook(hook) {
				count++
				if hook["command"] != quoteCodexHookPath(binaries[1]) {
					t.Errorf("canonical pin changed: %v", hook)
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("owned entries=%d want=1", count)
	}
	run(binaries[1], ExitOK, "--uninstall", "--project")
	run(binaries[1], ExitNotFound, "--show")
	doc, _, err = loadCodexHooks(target)
	if err != nil {
		t.Fatal(err)
	}
	entry = codexPostToolUseArray(codexHooksObject(doc))[0].(map[string]any)
	if doc["unknown"] != json.Number("9007199254740993") || entry["future"] != "outer-kept" || len(entry["hooks"].([]any)) != 1 || codexHooksObject(doc)["SessionStart"] == nil {
		t.Fatalf("merged siblings lost: %v", doc)
	}
	info, err = os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions changed: %v %v", info, err)
	}
	if err := os.WriteFile(target, []byte(`{"hooks":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run(binaries[1], ExitInternal, "--project")
	after, err = os.ReadFile(target)
	if err != nil || string(after) != `{"hooks":false}` {
		t.Fatalf("malformed document changed: %s %v", after, err)
	}
}
