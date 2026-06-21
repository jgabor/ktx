// Table-driven tests for `ktx init` (internal/cli/init.go).
//
// Coverage:
//   - install to global scope (--home-relative) writes the plugin to
//     $HOME/.config/opencode/plugins/ktx.ts.
//   - install to project scope (--project) writes the plugin to
//     <cwd>/.opencode/plugins/ktx.ts.
//   - --show reports the installed location.
//   - --uninstall removes the plugin file.
//   - unknown runtime exits 1.
//   - missing runtime name exits 1.
//   - --help exits 0.
//
// The global install path is HOME-relative: the test sets HOME to a
// per-test temp dir so we never touch the user's real ~/.config.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunInitWith_OpencodeInstall covers the install/show/uninstall
// flow for the OpenCode adapter. The global install uses
// $HOME/.config/opencode/plugins/ktx.ts; the project install uses
// <cwd>/.opencode/plugins/ktx.ts. Both are exercised below.
func TestRunInitWith_OpencodeInstall(t *testing.T) {
	t.Run("install to global scope writes plugin to $HOME/.config/...", func(t *testing.T) {
		// HOME points at a per-test temp dir so the install
		// never touches the user's real config.
		home := t.TempDir()
		t.Setenv("HOME", home)
		// XDG_CONFIG_HOME is intentionally not set; the
		// opencodeGlobalPluginPath helper uses
		// os.UserHomeDir so XDG_CONFIG_HOME is irrelevant
		// (the comment in init.go documents this).
		t.Setenv("XDG_CONFIG_HOME", "")

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"opencode"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// JSON envelope: {"installed": true, "path": "..."}
		var env map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env["installed"] != true {
			t.Errorf("installed = %v, want true", env["installed"])
		}
		wantPath := filepath.Join(home, ".config", "opencode", "plugins", ktxPluginFileName)
		if env["path"] != wantPath {
			t.Errorf("path = %v, want %v", env["path"], wantPath)
		}

		// The plugin file must exist on disk and contain the
		// canonical template (ktxOpencodePluginTemplate).
		data, err := os.ReadFile(wantPath)
		if err != nil {
			t.Fatalf("read %q: %v", wantPath, err)
		}
		if !strings.Contains(string(data), "KtxPlugin") {
			t.Errorf("plugin file missing KtxPlugin: %q", string(data))
		}
	})

	t.Run("install to project scope writes plugin to <cwd>/.opencode/...", func(t *testing.T) {
		// HOME is a per-test temp dir (we don't care about
		// it for project scope), but t.Chdir into a temp
		// dir so the project path resolves there.
		home := t.TempDir()
		t.Setenv("HOME", home)
		projectRoot := t.TempDir()
		restoreCwd := chdir(t, projectRoot)
		defer restoreCwd()

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"opencode", "--project"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		wantPath := filepath.Join(projectRoot, ".opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(wantPath); err != nil {
			t.Errorf("plugin file %q not present: %v", wantPath, err)
		}
	})

	t.Run("--show reports the global install location", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")

		// Install first.
		runInitWith([]string{"opencode"}, Options{Format: FormatJSON}, &bytes.Buffer{}, &bytes.Buffer{})

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"opencode", "--show"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		var env map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v", err)
		}
		if env["installed"] != true {
			t.Errorf("installed = %v, want true", env["installed"])
		}
	})

	t.Run("--show with no install exits 2 (not-found)", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"opencode", "--show"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
	})

	t.Run("--uninstall removes the plugin file", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")

		// Install first.
		runInitWith([]string{"opencode"}, Options{Format: FormatJSON}, &bytes.Buffer{}, &bytes.Buffer{})

		wantPath := filepath.Join(home, ".config", "opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(wantPath); err != nil {
			t.Fatalf("pre-state: plugin file not present: %v", err)
		}

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"opencode", "--uninstall"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
			t.Errorf("plugin file %q still present after uninstall: %v", wantPath, err)
		}
	})

	t.Run("unknown runtime exits 1 (usage)", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"unknown-runtime"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "unknown-runtime") {
			t.Errorf("stderr missing runtime name: %q", stderr.String())
		}
	})

	t.Run("missing runtime name exits 1 (usage)", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"--show"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "missing runtime name") {
			t.Errorf("stderr missing missing-runtime message: %q", stderr.String())
		}
	})

	t.Run("--help exits 0 with help on stdout", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		var stdout, stderr bytes.Buffer
		code := runInitWith([]string{"--help"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx init") {
			t.Errorf("stdout missing ktx init header: %q", stdout.String())
		}
	})
}

// chdir changes the working directory for the duration of a test
// and returns a function that restores the original cwd. The test
// framework's t.Chdir (Go 1.24+) is not used here because the
// project's go.mod is on an older Go toolchain in CI.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %q: %v", dir, err)
	}
	return func() {
		_ = os.Chdir(cwd)
	}
}
