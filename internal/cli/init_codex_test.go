// Tests for `ktx init codex` (internal/cli/init_codex.go).
//
// Coverage mirrors the OpenCode tests: install to global, install to
// project, --show found, --show not-found (exit 2), --uninstall
// removes the entry, and unknown/missing runtime handling. The
// Codex-specific cases exercise the merge behavior:
//
//   - install into a hooks.json that already has a SessionStart hook
//     preserves that hook (merge, not overwrite)
//   - re-running install is idempotent (file mtime+content unchanged)
//   - uninstall removes only ktx's entry and preserves other hooks
//   - the install output mentions the /hooks trust-review step
//
// The global install path is HOME-relative (~/.codex/hooks.json), so
// each test points HOME at a per-test temp dir via t.Setenv. Project
// installs use t.Chdir-equivalent chdir helper to scope ./.codex/.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunInitWith_CodexInstall covers the install/show/uninstall flow
// for the Codex adapter. The global install uses ~/.codex/hooks.json;
// the project install uses <cwd>/.codex/hooks.json. Both are
// exercised below, along with the merge and idempotency contracts.
func TestRunInitWith_CodexInstall(t *testing.T) {
	t.Run("install to global scope creates hooks.json with PostToolUse ktx entry", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"codex"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		var env map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env["installed"] != true {
			t.Errorf("installed = %v, want true", env["installed"])
		}
		wantPath := filepath.Join(home, ".codex", "hooks.json")
		if env["path"] != wantPath {
			t.Errorf("path = %v, want %v", env["path"], wantPath)
		}

		data, err := os.ReadFile(wantPath)
		if err != nil {
			t.Fatalf("read %q: %v", wantPath, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("hooks.json is not valid JSON: %v", err)
		}
		hooks, ok := doc["hooks"].(map[string]any)
		if !ok {
			t.Fatalf("hooks.json missing hooks object: %s", data)
		}
		arr, ok := hooks["PostToolUse"].([]any)
		if !ok || len(arr) != 1 {
			t.Fatalf("PostToolUse not a 1-element array: %v", hooks["PostToolUse"])
		}
		entry, ok := arr[0].(map[string]any)
		if !ok {
			t.Fatalf("PostToolUse entry not an object: %v", arr[0])
		}
		if entry["matcher"] != ".*" {
			t.Errorf("matcher = %v, want .*", entry["matcher"])
		}
		inner, ok := entry["hooks"].([]any)
		if !ok || len(inner) != 1 {
			t.Fatalf("inner hooks not a 1-element array: %v", entry["hooks"])
		}
		h, ok := inner[0].(map[string]any)
		if !ok {
			t.Fatalf("inner hook[0] not an object: %v", inner[0])
		}
		if h["command"] != "ktx hook" {
			t.Errorf("command = %v, want ktx hook", h["command"])
		}
		if h["type"] != "command" {
			t.Errorf("type = %v, want command", h["type"])
		}
		if h["statusMessage"] != "ktx: recording tool call" {
			t.Errorf("statusMessage = %v, want ktx: recording tool call", h["statusMessage"])
		}
		if h["timeout"] != float64(10) {
			t.Errorf("timeout = %v, want 10", h["timeout"])
		}
	})

	t.Run("install with existing SessionStart hook preserves other hooks", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		hooksPath := filepath.Join(home, ".codex", "hooks.json")
		if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Pre-existing hooks.json with a SessionStart hook from some
		// other source. ktx install must merge, not overwrite.
		existing := []byte(`{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {"type": "command", "command": "echo session started", "timeout": 5}
        ]
      }
    ]
  }
}
`)
		if err := os.WriteFile(hooksPath, existing, 0o644); err != nil {
			t.Fatalf("write pre-existing hooks.json: %v", err)
		}

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"codex"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		data, err := os.ReadFile(hooksPath)
		if err != nil {
			t.Fatalf("read merged hooks.json: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("merged hooks.json not valid JSON: %v\n%s", err, data)
		}
		hooks, ok := doc["hooks"].(map[string]any)
		if !ok {
			t.Fatalf("hooks object lost after install: %s", data)
		}
		// SessionStart must survive.
		ss, ok := hooks["SessionStart"].([]any)
		if !ok || len(ss) != 1 {
			t.Fatalf("SessionStart hook not preserved: %v", hooks["SessionStart"])
		}
		// PostToolUse ktx entry must be added.
		ptu, ok := hooks["PostToolUse"].([]any)
		if !ok || len(ptu) != 1 {
			t.Fatalf("PostToolUse ktx entry not added: %v", hooks["PostToolUse"])
		}
	})

	t.Run("idempotent install: file content unchanged on re-install", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		// First install.
		var stdout1, stderr1 bytes.Buffer
		if code := runInitWith([]string{"codex"}, Options{Format: FormatJSON}, &stdout1, &stderr1); code != ExitOK {
			t.Fatalf("first install: exit code = %d (stderr=%q)", code, stderr1.String())
		}
		hooksPath := filepath.Join(home, ".codex", "hooks.json")
		afterFirst, err := os.ReadFile(hooksPath)
		if err != nil {
			t.Fatalf("read after first install: %v", err)
		}

		// Second install — must be a no-op.
		var stdout2, stderr2 bytes.Buffer
		if code := runInitWith([]string{"codex"}, Options{Format: FormatJSON}, &stdout2, &stderr2); code != ExitOK {
			t.Fatalf("second install: exit code = %d (stderr=%q)", code, stderr2.String())
		}
		afterSecond, err := os.ReadFile(hooksPath)
		if err != nil {
			t.Fatalf("read after second install: %v", err)
		}

		if string(afterFirst) != string(afterSecond) {
			t.Errorf("hooks.json changed on re-install:\nfirst:  %s\nsecond: %s", afterFirst, afterSecond)
		}
		// The second install's envelope should mark already_installed.
		var env map[string]any
		if err := json.Unmarshal(stdout2.Bytes(), &env); err != nil {
			t.Fatalf("second install stdout not JSON: %v", err)
		}
		if env["already_installed"] != true {
			t.Errorf("second install already_installed = %v, want true", env["already_installed"])
		}
	})

	t.Run("uninstall removes ktx entry, preserves other hooks", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		hooksPath := filepath.Join(home, ".codex", "hooks.json")
		if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// File with both a SessionStart hook and ktx's PostToolUse
		// entry. Uninstall must remove only the PostToolUse ktx entry.
		existing := []byte(`{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {"type": "command", "command": "echo session started", "timeout": 5}
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": ".*",
        "hooks": [
          {"type": "command", "command": "ktx hook", "timeout": 10, "statusMessage": "ktx: recording tool call"}
        ]
      }
    ]
  }
}
`)
		if err := os.WriteFile(hooksPath, existing, 0o644); err != nil {
			t.Fatalf("write pre-existing hooks.json: %v", err)
		}

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"codex", "--uninstall"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		data, err := os.ReadFile(hooksPath)
		if err != nil {
			t.Fatalf("read after uninstall: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("hooks.json not valid JSON after uninstall: %v\n%s", err, data)
		}
		hooks, ok := doc["hooks"].(map[string]any)
		if !ok {
			t.Fatalf("hooks object lost after uninstall: %s", data)
		}
		// SessionStart must survive.
		if _, ok := hooks["SessionStart"]; !ok {
			t.Errorf("SessionStart hook lost after uninstall: %s", data)
		}
		// PostToolUse must be gone (it only held ktx's entry).
		if _, ok := hooks["PostToolUse"]; ok {
			t.Errorf("PostToolUse key should be removed, still present: %v", hooks["PostToolUse"])
		}
	})

	t.Run("install to project scope writes to ./.codex/hooks.json", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectRoot := t.TempDir()
		restoreCwd := chdir(t, projectRoot)
		defer restoreCwd()

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"codex", "--project"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		wantPath := filepath.Join(projectRoot, ".codex", "hooks.json")
		if _, err := os.Stat(wantPath); err != nil {
			t.Fatalf("project hooks.json not present: %v", err)
		}
		// Global path must NOT be written by --project install.
		globalPath := filepath.Join(home, ".codex", "hooks.json")
		if _, err := os.Stat(globalPath); !os.IsNotExist(err) {
			t.Errorf("global hooks.json should not exist after --project install: %v", err)
		}
	})

	t.Run("--show reports installed (global + project)", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectRoot := t.TempDir()
		restoreCwd := chdir(t, projectRoot)
		defer restoreCwd()

		// Install to both scopes.
		if code := runInitWith([]string{"codex"}, Options{Format: FormatJSON}, &bytes.Buffer{}, &bytes.Buffer{}); code != ExitOK {
			t.Fatalf("global install failed: code=%d", code)
		}
		if code := runInitWith([]string{"codex", "--project"}, Options{Format: FormatJSON}, &bytes.Buffer{}, &bytes.Buffer{}); code != ExitOK {
			t.Fatalf("project install failed: code=%d", code)
		}

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"codex", "--show"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		var env map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout not JSON: %v", err)
		}
		if env["installed"] != true {
			t.Errorf("installed = %v, want true", env["installed"])
		}
		if env["command"] != "ktx hook" {
			t.Errorf("command = %v, want ktx hook", env["command"])
		}
		scopes, _ := env["scopes"].([]any)
		if len(scopes) != 2 {
			t.Errorf("scopes = %v, want both global and project", scopes)
		}
	})

	t.Run("--show with no install exits 2 (not-found)", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectRoot := t.TempDir()
		restoreCwd := chdir(t, projectRoot)
		defer restoreCwd()

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"codex", "--show"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
	})

	t.Run("install output includes /hooks trust review instruction", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		// JSON output should include a note mentioning /hooks.
		var jstdout, jstderr bytes.Buffer
		if code := runInitWith([]string{"codex"}, Options{Format: FormatJSON}, &jstdout, &jstderr); code != ExitOK {
			t.Fatalf("json install failed: code=%d (stderr=%q)", code, jstderr.String())
		}
		var env map[string]any
		if err := json.Unmarshal(jstdout.Bytes(), &env); err != nil {
			t.Fatalf("json stdout not parseable: %v", err)
		}
		note, _ := env["note"].(string)
		if !strings.Contains(note, "/hooks") {
			t.Errorf("JSON note missing /hooks reference: %q", note)
		}

		// Text output should also include /hooks.
		home2 := t.TempDir()
		t.Setenv("HOME", home2)
		var tstdout, tstderr bytes.Buffer
		if code := runInitWith([]string{"codex"}, Options{Format: FormatText}, &tstdout, &tstderr); code != ExitOK {
			t.Fatalf("text install failed: code=%d (stderr=%q)", code, tstderr.String())
		}
		if !strings.Contains(tstdout.String(), "/hooks") {
			t.Errorf("text stdout missing /hooks reference: %q", tstdout.String())
		}
	})

	t.Run("uninstall with no ktx entry exits 2 (not-found)", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		hooksPath := filepath.Join(home, ".codex", "hooks.json")
		if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// hooks.json exists but has no ktx entry — uninstall must
		// report not-found rather than corrupting the file.
		existing := []byte(`{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "echo hi", "timeout": 5}]}
    ]
  }
}
`)
		if err := os.WriteFile(hooksPath, existing, 0o644); err != nil {
			t.Fatalf("write hooks.json: %v", err)
		}

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"codex", "--uninstall"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		// File must still exist and be untouched.
		after, err := os.ReadFile(hooksPath)
		if err != nil {
			t.Fatalf("hooks.json missing after not-found uninstall: %v", err)
		}
		if !strings.Contains(string(after), "SessionStart") {
			t.Errorf("SessionStart hook lost on not-found uninstall: %s", after)
		}
	})

	t.Run("uninstall when hooks.json does not exist exits 2", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"codex", "--uninstall"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
	})

	t.Run("uninstall removes file when no other hooks remain", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		// Install: creates hooks.json with just the ktx PostToolUse
		// entry. Uninstall should then delete the file entirely
		// since no other hooks remain to preserve.
		var stdout, stderr bytes.Buffer
		if code := runInitWith([]string{"codex"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK {
			t.Fatalf("install failed: code=%d (stderr=%q)", code, stderr.String())
		}
		hooksPath := filepath.Join(home, ".codex", "hooks.json")
		if _, err := os.Stat(hooksPath); err != nil {
			t.Fatalf("pre-state: hooks.json missing: %v", err)
		}

		var ustdout, ustderr bytes.Buffer
		code := runInitWith([]string{"codex", "--uninstall"}, Options{Format: FormatJSON}, &ustdout, &ustderr)
		if code != ExitOK {
			t.Fatalf("uninstall exit = %d, want %d (stderr=%q)", code, ExitOK, ustderr.String())
		}
		if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
			t.Errorf("hooks.json should be removed when empty, still present: %v", err)
		}
	})
}

// TestRunInitWith_CodexRuntimeErrors covers the runtime-validation
// error paths so unknown/missing-runtime surface as usage errors with
// codex listed as a supported option.
func TestRunInitWith_CodexRuntimeErrors(t *testing.T) {
	t.Run("unknown runtime exits 1 with codex in supported list", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"unknown-runtime"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "codex") {
			t.Errorf("supported list missing codex: %q", stderr.String())
		}
	})

	t.Run("missing runtime name exits 1 with codex in usage", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"--show"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "codex") {
			t.Errorf("usage missing codex: %q", stderr.String())
		}
	})

	t.Run("--help mentions codex as a supported runtime", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"--help"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "codex") {
			t.Errorf("help missing codex: %q", stdout.String())
		}
		if !strings.Contains(stdout.String(), "/hooks") {
			t.Errorf("help missing /hooks trust note: %q", stdout.String())
		}
	})
}
