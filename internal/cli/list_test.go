// Table-driven tests for `ktx list` (internal/cli/list.go).
//
// Acceptance coverage:
//
//   - AC 4 (Task 3): --format text in either flag position triggers
//     text rendering; absent --format falls back to JSON default.
//
// Companion cases: empty database (no sessions), invalid --format
// (exit 1), unexpected positional arg (exit 1). Each subtest uses
// bytes.Buffer capture so the renderer path is exercised without
// touching os.Stdout.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// listTestDB opens a per-test KTX_DB database.
func listTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	path := os.Getenv("KTX_DB")
	if path == "" {
		t.Fatal("listTestDB requires KTX_DB to be set")
	}
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedListSession inserts a single session row with a non-empty
// agent and tool_call_count so the renderer has data to surface.
func seedListSession(t *testing.T, d *dbpkg.DB) {
	t.Helper()
	if err := d.InsertSession(&dbpkg.Session{
		ID:            "zyxwvutsrqponmlk",
		Agent:         "test-agent",
		StartedAt:     "2026-06-18T12:00:00Z",
		LastSeenAt:    "2026-06-18T12:00:00Z",
		ToolCallCount: 3,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
}

// TestRunListWith_FormatAcceptance covers AC 4 (list version): the
// list subcommand accepts --format in either position. The JSON
// default emits a `[]sessionListRow` array (Decision 4 default; M2
// rollup on the m2 field when at least one tool_call has M2 data).
// The text default emits a tabwriter-aligned table.
func TestRunListWith_FormatAcceptance(t *testing.T) {
	t.Run("absent --format emits JSON array of sessionListRow", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		seedListSession(t, listTestDB(t))

		var stdout, stderr bytes.Buffer
		code := runListWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Decode as a generic list of maps to assert the
		// array shape (the row type is local to the cli
		// package and embeds *dbpkg.Session, so JSON-decoding
		// into []map[string]any confirms the structure).
		var rows []map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
			t.Fatalf("stdout is not a JSON array: %v\nstdout=%q", err, stdout.String())
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		row := rows[0]
		if row["id"] != "zyxwvutsrqponmlk" {
			t.Errorf("row.id = %v, want zyxwvutsrqponmlk", row["id"])
		}
		if row["agent"] != "test-agent" {
			t.Errorf("row.agent = %v, want test-agent", row["agent"])
		}
		// No tool_call has M2 data → the m2 field is
		// omitted (omitempty on the wrapper).
		if _, ok := row["m2"]; ok {
			t.Errorf("row.m2 = %v, want absent (no M2 data)", row["m2"])
		}
	})

	t.Run("--format text before no-op emits text renderer", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		seedListSession(t, listTestDB(t))

		var stdout, stderr bytes.Buffer
		code := runListWith([]string{"--format", "text"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		out := stdout.String()
		// The text renderer emits a tabwriter-aligned
		// table. We assert the header columns and the
		// session ID are present.
		if !strings.Contains(out, "ID") || !strings.Contains(out, "AGENT") {
			t.Errorf("text output missing column headers: %q", out)
		}
		if !strings.Contains(out, "zyxwvutsrqponmlk") || !strings.Contains(out, "test-agent") {
			t.Errorf("text output missing session data: %q", out)
		}
		// No M2 data → the M2 columns render "-" (AC6).
		if !strings.Contains(out, " - ") {
			t.Errorf("text output missing '-' for no-M2 columns: %q", out)
		}
	})

	t.Run("--format text after subcommand verb emits text renderer", func(t *testing.T) {
		// extractFormat strips --format text from the args
		// regardless of position; the subcommand sees only
		// the value via opts.Format.
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		seedListSession(t, listTestDB(t))

		var stdout, stderr bytes.Buffer
		// empty prefix args; the global --format comes via
		// opts.
		code := runListWith(nil, Options{Format: FormatText}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "zyxwvutsrqponmlk") {
			t.Errorf("text output missing session ID: %q", stdout.String())
		}
	})

	t.Run("invalid --format exits 1 (usage)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		listTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runListWith([]string{"--format", "yaml"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "yaml") {
			t.Errorf("stderr missing bad-value echo: %q", stderr.String())
		}
	})

	t.Run("empty database emits JSON empty array, exit 0", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		listTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runListWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "[]" {
			t.Errorf("stdout = %q, want []", got)
		}
	})

	t.Run("unexpected positional arg exits 1 (usage)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		listTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runListWith([]string{"unexpected-arg"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "unexpected argument") {
			t.Errorf("stderr missing unexpected-argument message: %q", stderr.String())
		}
	})
}
