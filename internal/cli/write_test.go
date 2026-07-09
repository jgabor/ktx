// Table-driven tests for `ktx write` (internal/cli/write.go).
//
// Coverage:
//
//   - a tool_call JSON envelope piped through runWriteWith produces
//     the persisted record ID as JSON on stdout, and the record is
//     queryable via db.GetRecord.
//   - Companion negative cases: missing required field, empty stdin,
//     session_close with a known session updates ended_at, and the
//     unknown event_type branch.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// validToolCallPayload returns a JSON-encoded tool_call matching the
// canonical shape: a record with id, tool, input, output, timestamp,
// event_type, and an explicit session_id (so the test does not depend
// on the ppid-based session resolution path, which is non-deterministic
// in unit tests).
func validToolCallPayload(t *testing.T) string {
	t.Helper()
	tc := map[string]any{
		"id":           "abcdefghijklmnop",
		"session_id":   "zyxwvutsrqponmlk",
		"tool":         "bash",
		"input":        `{"command":"echo hello"}`,
		"output":       "hello\n",
		"content_hash": "",
		"timestamp":    "2026-06-18T12:00:00Z",
		"event_type":   "tool_call",
	}
	b, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal tool_call: %v", err)
	}
	return string(b)
}

// writeTestDB returns a *dbpkg.DB open against a fresh on-disk
// database in a per-test temp dir, plus the file path (for cleanup
// assertions). The on-disk path is required so the write flow can
// exercise dbpkg.Open's parent-dir-creation + idempotent migration
// contract.
//
// The test must set KTX_DB before calling writeTestDB so the helper
// uses the same path as runWriteWith (which resolves via
// dbpkg.ResolvePath("") → KTX_DB env > default).
func writeTestDB(t *testing.T) (*dbpkg.DB, string) {
	t.Helper()
	path := os.Getenv("KTX_DB")
	if path == "" {
		t.Fatal("writeTestDB requires KTX_DB to be set (runWriteWith resolves via KTX_DB)")
	}
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

// preSeedSession inserts a session row with id and returns nothing;
// called when a test payload carries a session_id that must already
// exist in the database (tool_calls.session_id FK references
// sessions.id).
func preSeedSession(t *testing.T, d *dbpkg.DB, id string) {
	t.Helper()
	if err := d.InsertSession(&dbpkg.Session{
		ID:         id,
		Agent:      "test-agent",
		StartedAt:  "2026-06-18T12:00:00Z",
		LastSeenAt: "2026-06-18T12:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession(%q): %v", id, err)
	}
}

// TestRunWriteWith_PersistsRecord covers: a tool_call JSON envelope on
// stdin → stdout is the persisted record ID as JSON, and the record is
// queryable via db.GetRecord. The companion negative case asserts that
// a missing required field (id) returns a usage error envelope and
// exit 1.
func TestRunWriteWith_PersistsRecord(t *testing.T) {
	t.Run("tool_call envelope persists and is queryable", func(t *testing.T) {
		// Isolate the database to a per-test file so we do not
		// touch the user's real production database.
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")

		d, _ := writeTestDB(t)
		preSeedSession(t, d, "zyxwvutsrqponmlk")

		stdin := bytes.NewBufferString(validToolCallPayload(t))
		var stdout, stderr bytes.Buffer
		code := runWriteWith(nil, Options{Format: FormatJSON}, stdin, &stdout, &stderr)

		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// stdout must be the record ID as a JSON string. The m1
		// verification log shows `"abcdefghijklmnop"` (with
		// surrounding quotes from JSON encoding + trailing
		// newline from json.NewEncoder).
		got := strings.TrimSpace(stdout.String())
		if got != `"abcdefghijklmnop"` {
			t.Errorf("stdout = %q, want %q", got, `"abcdefghijklmnop"`)
		}

		// The record must be queryable via db.GetRecord (the
		// testable surface used by `ktx read`). Re-open the
		// on-disk database to confirm persistence is real
		// (not just an in-memory side effect).
		d.Close()
		d2, err := dbpkg.Open(os.Getenv("KTX_DB"))
		if err != nil {
			t.Fatalf("reopen db: %v", err)
		}
		t.Cleanup(func() { d2.Close() })
		rec, err := d2.GetRecord("abcdefghijklmnop")
		if err != nil {
			t.Fatalf("GetRecord(abcdefghijklmnop): %v", err)
		}
		tc, ok := rec.(*dbpkg.ToolCall)
		if !ok {
			t.Fatalf("GetRecord returned %T, want *dbpkg.ToolCall", rec)
		}
		if tc.Tool != "bash" {
			t.Errorf("Tool = %q, want bash", tc.Tool)
		}
		if tc.SessionID != "zyxwvutsrqponmlk" {
			t.Errorf("SessionID = %q, want zyxwvutsrqponmlk", tc.SessionID)
		}
	})

	t.Run("missing required field id exits 1 with usage error", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		writeTestDB(t)

		// Tool_call payload missing the "id" field. Required
		// fields are id, tool, timestamp; id is
		// the most-strict — it is the persisted primary key.
		payload := `{"tool":"bash","timestamp":"2026-06-18T12:00:00Z","input":"x","output":"y"}`
		stdin := bytes.NewBufferString(payload)
		var stdout, stderr bytes.Buffer
		code := runWriteWith(nil, Options{Format: FormatJSON}, stdin, &stdout, &stderr)

		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"usage"`) {
			t.Errorf("stderr missing usage envelope: %q", stderr.String())
		}
		if !strings.Contains(stderr.String(), "id") {
			t.Errorf("stderr missing missing-field name: %q", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout should be empty on validation failure, got %q", stdout.String())
		}
	})

	t.Run("empty stdin exits 1 with usage error", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		writeTestDB(t)

		stdin := bytes.NewBufferString("")
		var stdout, stderr bytes.Buffer
		code := runWriteWith(nil, Options{Format: FormatJSON}, stdin, &stdout, &stderr)

		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "no input on stdin") {
			t.Errorf("stderr missing empty-stdin message: %q", stderr.String())
		}
	})

	t.Run("malformed JSON exits 1 with usage error", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		writeTestDB(t)

		stdin := bytes.NewBufferString(`{not-json`)
		var stdout, stderr bytes.Buffer
		code := runWriteWith(nil, Options{Format: FormatJSON}, stdin, &stdout, &stderr)

		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "parse JSON") {
			t.Errorf("stderr missing parse-JSON message: %q", stderr.String())
		}
	})
}

// TestRunWriteWith_SessionClose covers the second payload shape:
// a session_close event_type updates ended_at on the session row. The
// companion negative case asserts that a session_close for a
// non-existent session returns ExitNotFound (exit 2, not-found contract).
func TestRunWriteWith_SessionClose(t *testing.T) {
	t.Run("session_close updates ended_at and prints session_id", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")

		d, _ := writeTestDB(t)
		preSeedSession(t, d, "zyxwvutsrqponmlk")

		payload := `{"event_type":"session_close","session_id":"zyxwvutsrqponmlk","timestamp":"2026-06-18T13:00:00Z"}`
		stdin := bytes.NewBufferString(payload)
		var stdout, stderr bytes.Buffer
		code := runWriteWith(nil, Options{Format: FormatJSON}, stdin, &stdout, &stderr)

		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		got := strings.TrimSpace(stdout.String())
		if got != `"zyxwvutsrqponmlk"` {
			t.Errorf("stdout = %q, want %q (session id as JSON string)", got, `"zyxwvutsrqponmlk"`)
		}

		// Confirm ended_at was updated on the row.
		s, err := d.GetSession("zyxwvutsrqponmlk")
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if s.EndedAt != "2026-06-18T13:00:00Z" {
			t.Errorf("EndedAt = %q, want 2026-06-18T13:00:00Z", s.EndedAt)
		}
	})

	t.Run("session_close for unknown session exits 2 (not-found)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		writeTestDB(t)

		payload := `{"event_type":"session_close","session_id":"aaaaaaaaaaaaaaaa","timestamp":"2026-06-18T13:00:00Z"}`
		stdin := bytes.NewBufferString(payload)
		var stdout, stderr bytes.Buffer
		code := runWriteWith(nil, Options{Format: FormatJSON}, stdin, &stdout, &stderr)

		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})
}

// TestRunWriteWith_FailureSignalFields covers: ktx write persists
// exit_code and is_error from the sidecar payload; absent fields round-trip
// as nil.
func TestRunWriteWith_FailureSignalFields(t *testing.T) {
	t.Run("is_error=true exit_code=127 round-trip", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		d, _ := writeTestDB(t)
		preSeedSession(t, d, "zyxwvutsrqponmlk")

		payload := `{"id":"abcdefghijklmnop","session_id":"zyxwvutsrqponmlk","tool":"bash","input":"x","output":"y","content_hash":"` +
			dbpkg.ContentHash("y") + `","timestamp":"2026-06-26T12:00:00Z","event_type":"tool_call","is_error":true,"exit_code":127}`
		stdin := bytes.NewBufferString(payload)
		var stdout, stderr bytes.Buffer
		code := runWriteWith(nil, Options{Format: FormatJSON}, stdin, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit = %d (stderr=%q)", code, stderr.String())
		}

		got, err := d.GetToolCall("abcdefghijklmnop")
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		if got.IsError == nil || *got.IsError != true {
			t.Errorf("IsError = %v, want pointer to true", got.IsError)
		}
		if got.ExitCode == nil || *got.ExitCode != 127 {
			t.Errorf("ExitCode = %v, want pointer to 127", got.ExitCode)
		}
	})

	t.Run("without is_error or exit_code → nil nil", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		d, _ := writeTestDB(t)
		preSeedSession(t, d, "zyxwvutsrqponmlk")

		stdin := bytes.NewBufferString(validToolCallPayload(t))
		var stdout, stderr bytes.Buffer
		code := runWriteWith(nil, Options{Format: FormatJSON}, stdin, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit = %d (stderr=%q)", code, stderr.String())
		}

		got, err := d.GetToolCall("abcdefghijklmnop")
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		if got.IsError != nil {
			t.Errorf("IsError = %v, want nil", got.IsError)
		}
		if got.ExitCode != nil {
			t.Errorf("ExitCode = %v, want nil", got.ExitCode)
		}
	})
}

// TestRunWriteWith_Help covers the I/O-primitive's --help branch:
// the subcommand-local flag set accepts -h/--help and exits 0.
func TestRunWriteWith_Help(t *testing.T) {
	t.Run("--help prints help and exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWriteWith([]string{"--help"}, Options{Format: FormatJSON}, &bytes.Buffer{}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx write") {
			t.Errorf("stdout missing ktx write header: %q", stdout.String())
		}
	})
}
