// Table-driven tests for `ktx init` (internal/cli/init.go): the usage
// errors and help shared by every runtime. Each runtime's install, show and
// uninstall flow has its own test file.
package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestRunInitWith_Usage covers the runtime-independent paths of `ktx init`.
func TestRunInitWith_Usage(t *testing.T) {
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
