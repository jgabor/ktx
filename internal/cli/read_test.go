// Table-driven tests for `ktx read` (internal/cli/read.go).
//
// `ktx read` is the I/O-primitive counterpart to `ktx write` (Decision
// 2/8): raw JSON output, no --format flag, byte-faithful round-trip
// from the persisted row. The acceptance contract is exercised by the
// companion to write_test.go's AC 1: a persisted record fetched by
// `ktx read` returns the same JSON shape that `ktx write` accepts on
// stdin.
//
// Coverage:
//   - tool_call readback (raw JSON, exit 0)
//   - session readback (raw JSON, exit 0)
//   - not-found path (exit 2)
//   - missing id argument (exit 1)
//   - --format rejected (read is I/O; no --format at subcommand level)
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// seedToolCall inserts a session and a tool_call row so the test can
// read them back. IDs use the stored 16-character lowercase format.
func seedToolCall(t *testing.T, d *dbpkg.DB, toolCallID, sessionID string) {
	t.Helper()
	if err := d.InsertSession(&dbpkg.Session{
		ID:         sessionID,
		Agent:      "test-agent",
		StartedAt:  "2026-06-18T12:00:00Z",
		LastSeenAt: "2026-06-18T12:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if err := d.PersistToolCall(&dbpkg.ToolCall{
		ID:          toolCallID,
		SessionID:   sessionID,
		Tool:        "read",
		Input:       "README.md",
		Output:      "ktx\n",
		ContentHash: dbpkg.ContentHash("ktx\n"),
		Timestamp:   "2026-06-18T12:00:00Z",
		EventType:   "tool_call",
	}); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}
}

// readTestDB opens a per-test KTX_DB database. Mirrors writeTestDB.
func readTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	path := os.Getenv("KTX_DB")
	if path == "" {
		t.Fatal("readTestDB requires KTX_DB to be set")
	}
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestRunReadWith_RoundTrip covers the I/O-primitive contract: a
// persisted record fetched by `ktx read <id>` is printed as raw JSON
// (the same shape `ktx write` accepts on stdin). Companion cases
// cover not-found, missing id, and --format rejection.
func TestRunReadWith_RoundTrip(t *testing.T) {
	t.Run("read tool_call by id emits raw JSON, exit 0", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		d := readTestDB(t)
		seedToolCall(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk")

		var stdout, stderr bytes.Buffer
		code := runReadWith([]string{"abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// The output must be valid JSON for a *dbpkg.ToolCall.
		// We do a round-trip decode to confirm the shape survives
		// the read path (not just byte equality with the
		// originally persisted values).
		var tc dbpkg.ToolCall
		if err := json.Unmarshal(stdout.Bytes(), &tc); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if tc.ID != "abcdefghijklmnop" {
			t.Errorf("ID = %q, want abcdefghijklmnop", tc.ID)
		}
		if tc.Tool != "read" {
			t.Errorf("Tool = %q, want read", tc.Tool)
		}
		if tc.SessionID != "zyxwvutsrqponmlk" {
			t.Errorf("SessionID = %q, want zyxwvutsrqponmlk", tc.SessionID)
		}
		if tc.Input != "README.md" {
			t.Errorf("Input = %q, want README.md", tc.Input)
		}
	})

	t.Run("read session by id emits raw JSON, exit 0", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		d := readTestDB(t)
		if err := d.InsertSession(&dbpkg.Session{
			ID:         "zyxwvutsrqponmlk",
			Agent:      "test-agent",
			StartedAt:  "2026-06-18T12:00:00Z",
			LastSeenAt: "2026-06-18T12:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		var stdout, stderr bytes.Buffer
		code := runReadWith([]string{"zyxwvutsrqponmlk"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		var s dbpkg.Session
		if err := json.Unmarshal(stdout.Bytes(), &s); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if s.ID != "zyxwvutsrqponmlk" {
			t.Errorf("ID = %q, want zyxwvutsrqponmlk", s.ID)
		}
		if s.Agent != "test-agent" {
			t.Errorf("Agent = %q, want test-agent", s.Agent)
		}
	})

	t.Run("read missing id exits 2 (not-found)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		readTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runReadWith([]string{"abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})

	t.Run("read with no id argument exits 1 (usage)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		readTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runReadWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "missing required argument") {
			t.Errorf("stderr missing missing-argument message: %q", stderr.String())
		}
	})

	t.Run("read with --format flag exits 1 (I/O primitive)", func(t *testing.T) {
		// ktx read is a raw I/O primitive: it has no
		// --format flag at the subcommand level. The global --format
		// from the dispatcher is parsed but the subcommand does not
		// accept it; passing it as a subcommand arg is a usage
		// error. (The dispatcher's --format still routes correctly
		// for query commands; the test of that contract lives in
		// the read --format-text call path which is rejected.)
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		readTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runReadWith([]string{"--format", "text", "abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
	})
}
