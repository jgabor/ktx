// Table-driven tests for the cli dispatcher (internal/cli/cli.go).
//
// The dispatcher (Run / runWith) owns three responsibilities:
//  1. Global flag parsing (--format, --quiet, --help, --version).
//  2. Subcommand lookup against the commandIndex table.
//  3. Error envelope rendering and exit-code mapping for dispatcher-level
//     failures (unknown subcommand, bad --format, no subcommand).
//
// Subcommand-specific behavior lives in each subcommand's own runXWith;
// dispatcher tests exercise the dispatch surface only. Companion tests
// for unknown subcommand + invalid format + version + help keep the
// dispatcher regression net honest without duplicating subcommand tests.
package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunWith_Dispatch covers the dispatcher's pass/fail surface:
//   - no args          → help on stdout, exit 0 (the docker/kubectl convention)
//   - --help           → help on stdout, exit 0
//   - --version        → version on stdout, exit 0
//   - unknown subcommand → usage error envelope on stderr, exit 1
//   - invalid --format → usage error envelope on stderr, exit 1
//
// Each subtest is a t.Run block with a small table of name/args/expected
// fields so the surface is enumerable without a giant switch.
func TestRunWith_Dispatch(t *testing.T) {
	t.Run("no args prints help and exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith(nil, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx") {
			t.Errorf("stdout missing 'ktx' header: %q", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Commands:") {
			t.Errorf("stdout missing 'Commands:' section: %q", stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty, got %q", stderr.String())
		}
	})

	t.Run("--help prints help and exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d", code, ExitOK)
		}
		if !strings.Contains(stdout.String(), "Usage: ktx") {
			t.Errorf("stdout missing usage line: %q", stdout.String())
		}
	})

	t.Run("--version prints version and exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"--version"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d", code, ExitOK)
		}
		if got := strings.TrimSpace(stdout.String()); got == "" {
			t.Errorf("stdout empty; expected version string")
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty, got %q", stderr.String())
		}
	})

	t.Run("unknown subcommand emits usage error and exits 1", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"nonexistent-subcommand"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout should be empty on error, got %q", stdout.String())
		}
		// JSON envelope shape: {"error":{"code":"usage","message":"..."}}
		if !strings.Contains(stderr.String(), `"code":"usage"`) {
			t.Errorf("stderr missing usage error code: %q", stderr.String())
		}
		if !strings.Contains(stderr.String(), "nonexistent-subcommand") {
			t.Errorf("stderr missing subcommand name: %q", stderr.String())
		}
	})

	t.Run("invalid --format emits usage error and exits 1", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"--format", "yaml", "list"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"usage"`) {
			t.Errorf("stderr missing usage error code: %q", stderr.String())
		}
		if !strings.Contains(stderr.String(), "yaml") {
			t.Errorf("stderr missing bad-value echo: %q", stderr.String())
		}
	})

	t.Run("global --format propagates to opts and survives dispatch", func(t *testing.T) {
		// parseGlobalFlags is the dispatcher's only injection point
		// for the global --format; subcommands read it via
		// opts.Format when no subcommand-local --format is set.
		// Verify the parser stores the value and returns the
		// remaining subcommand args so dispatch can find the verb.
		p, rest, err := parseGlobalFlags([]string{"--format", "text", "list"})
		if err != nil {
			t.Fatalf("parseGlobalFlags: %v", err)
		}
		if p.Format != FormatText {
			t.Errorf("opts.Format = %q, want %q", p.Format, FormatText)
		}
		if len(rest) != 1 || rest[0] != "list" {
			t.Errorf("rest = %v, want [list]", rest)
		}
	})
}
