// Table-driven tests for `ktx get` (internal/cli/get.go).
//
// Coverage:
//
//   - --format text in either flag position triggers
//     text rendering; absent --format falls back to JSON default. The
//     "either position" surface is the main testable contract: the
//     global --format (parsed by the dispatcher) and the subcommand
//     --format (extracted by extractFormat from inside the args) both
//     reach the renderer.
//
// Companion cases: not-found (exit 2), missing id (exit 1), and
// invalid --format (exit 1). Each subtest uses bytes.Buffer capture
// so the renderer path is exercised without touching os.Stdout.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// seedToolCallForGet is a thin wrapper that inserts a session and
// tool_call row with a cost-publication pointer set, so the text
// renderer exercises the cost-section path.
func seedToolCallForGet(t *testing.T, d *dbpkg.DB) {
	t.Helper()
	if err := d.InsertSession(&dbpkg.Session{
		ID:         "zyxwvutsrqponmlk",
		Agent:      "test-agent",
		StartedAt:  "2026-06-18T12:00:00Z",
		LastSeenAt: "2026-06-18T12:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	cost := 0.0123
	if err := d.PersistToolCall(&dbpkg.ToolCall{
		ID:          "abcdefghijklmnop",
		SessionID:   "zyxwvutsrqponmlk",
		Tool:        "bash",
		Input:       `{"command":"echo hi"}`,
		Output:      "hi\n",
		ContentHash: dbpkg.ContentHash("hi\n"),
		Timestamp:   "2026-06-18T12:00:00Z",
		EventType:   "tool_call",
		Cost:        &cost,
	}); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}
}

// getTestDB opens a per-test KTX_DB database.
func getTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	path := os.Getenv("KTX_DB")
	if path == "" {
		t.Fatal("getTestDB requires KTX_DB to be set")
	}
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestRunGetWith_FormatAcceptance covers --format text triggering
// the text renderer in either flag position. The subcommand-local
// --format takes precedence over the global opts.Format; absent
// --format falls back to JSON default.
//
// The three "either position" permutations under test:
//
//   - {<id>}                                  (no --format, JSON default)
//   - {"--format", "text", <id>}              (subcommand position before)
//   - {<id>, "--format", "text"}              (subcommand position after)
func TestRunGetWith_FormatAcceptance(t *testing.T) {
	t.Run("absent --format emits JSON default", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		seedToolCallForGet(t, getTestDB(t))

		var stdout, stderr bytes.Buffer
		code := runGetWith([]string{"abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// JSON default: the output must round-trip as a
		// *dbpkg.ToolCall. We decode to confirm the shape (not
		// just byte equality with the persisted row).
		var tc dbpkg.ToolCall
		if err := json.Unmarshal(stdout.Bytes(), &tc); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if tc.ID != "abcdefghijklmnop" {
			t.Errorf("ID = %q, want abcdefghijklmnop", tc.ID)
		}
	})

	t.Run("--format text before id emits text renderer", func(t *testing.T) {
		// subcommand-local --format BEFORE the positional id.
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		seedToolCallForGet(t, getTestDB(t))

		var stdout, stderr bytes.Buffer
		code := runGetWith([]string{"--format", "text", "abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Text renderer emits a key-value line per field plus
		// the cost section. The exact format is documented in
		// printGetText; we assert the presence of the fields
		// and the cost marker.
		out := stdout.String()
		if !strings.Contains(out, "id:") || !strings.Contains(out, "abcdefghijklmnop") {
			t.Errorf("text output missing id field: %q", out)
		}
		if !strings.Contains(out, "tool:") || !strings.Contains(out, "bash") {
			t.Errorf("text output missing tool field: %q", out)
		}
		if !strings.Contains(out, "Costs:") {
			t.Errorf("text output missing Costs section (cost was non-nil): %q", out)
		}
		if !strings.Contains(out, "cost:") {
			t.Errorf("text output missing Costs cost: %q", out)
		}
	})

	t.Run("--format text after id emits text renderer", func(t *testing.T) {
		// subcommand-local --format AFTER the positional id.
		// extractFormat pulls it out of the args regardless of
		// position; this is the "either flag position"
		// contract.
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		seedToolCallForGet(t, getTestDB(t))

		var stdout, stderr bytes.Buffer
		code := runGetWith([]string{"abcdefghijklmnop", "--format", "text"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Costs:") {
			t.Errorf("text output missing Costs section: %q", stdout.String())
		}
	})

	t.Run("global --format text propagates to renderer", func(t *testing.T) {
		// The global --format from the dispatcher (opts.Format)
		// is used as the default when no subcommand --format is
		// present. This is the dispatcher→subcommand propagation
		// contract.
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		seedToolCallForGet(t, getTestDB(t))

		var stdout, stderr bytes.Buffer
		code := runGetWith([]string{"abcdefghijklmnop"}, Options{Format: FormatText}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Costs:") {
			t.Errorf("text output missing Costs section: %q", stdout.String())
		}
	})

	t.Run("invalid --format exits 1 (usage)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		seedToolCallForGet(t, getTestDB(t))

		var stdout, stderr bytes.Buffer
		code := runGetWith([]string{"--format", "yaml", "abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "yaml") {
			t.Errorf("stderr missing bad-value echo: %q", stderr.String())
		}
	})

	t.Run("get missing id exits 2 (not-found)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		getTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runGetWith([]string{"abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})
}
