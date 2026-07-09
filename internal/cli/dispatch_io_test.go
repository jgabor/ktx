// End-to-end dispatch tests for the I/O verbs (ktx read, ktx write).
//
// These verbs register via dispatch.Register
// (read.go:init, write.go:init) instead of being entries in the legacy
// commands slice. The regression net for that move is this file: every
// test here invokes the binary through runWith (the production entry
// point), which routes through parseGlobalFlags → dispatchRegistry →
// runRegisteredVerb → readCommandFunc / writeCommandFunc.
//
// stdin injection uses os.Pipe + os.Stdin swap because the dispatch
// module's runRegisteredVerb reads from os.Stdin when NeedsStdin=true.
// Tests in this package run serially (no t.Parallel) so the
// process-global swap is safe; t.Cleanup restores os.Stdin when each
// subtest returns.
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// injectStdin replaces os.Stdin with a pipe pre-loaded with payload.
// The payload is written and the write-end closed BEFORE the caller
// invokes runWith; the kernel pipe buffer holds the data so the
// verb's io.ReadAll returns the payload followed by EOF.
//
// t.Cleanup restores os.Stdin and closes the read end of the pipe.
// Tests are serial within this package, so a process-global swap of
// os.Stdin cannot leak across tests.
func injectStdin(t *testing.T, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := io.WriteString(w, payload); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
}

// TestRunWith_ReadDispatched covers: a
// persisted record fetched via `ktx read <id>` (dispatch path) emits
// the raw JSON record on stdout and exits 0; a missing record exits 2
// with a not-found envelope; --help exits 0 and renders help.
func TestRunWith_ReadDispatched(t *testing.T) {
	t.Run("ktx read routes through dispatch registry and emits raw JSON", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		d := readTestDB(t)
		seedToolCall(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk")

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"read", "abcdefghijklmnop"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on success, got %q", stderr.String())
		}

		// The verb writes raw JSON; the decoder must see the same
		// tool_call shape that was persisted.
		var tc dbpkg.ToolCall
		if err := json.Unmarshal(stdout.Bytes(), &tc); err != nil {
			t.Fatalf("stdout is not valid JSON: %v (raw=%q)", err, stdout.String())
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
	})

	t.Run("ktx read not-found exits 2 via dispatch registry", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		readTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"read", "abcdefghijklmnop"}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout should be empty on not-found, got %q", stdout.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})

	t.Run("ktx read --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"read", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on --help, got %q", stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx read") {
			t.Errorf("stdout missing ktx read header: %q", stdout.String())
		}
	})

	t.Run("ktx read missing id exits 1 via dispatch registry", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		readTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"read"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"usage"`) {
			t.Errorf("stderr missing usage envelope: %q", stderr.String())
		}
		if !strings.Contains(stderr.String(), "missing required argument") {
			t.Errorf("stderr missing missing-argument message: %q", stderr.String())
		}
	})
}

// TestRunWith_WriteDispatched covers
// `ktx write`: a tool_call envelope on stdin persists the record and
// prints the record ID as raw JSON; a session_close envelope updates
// the session's ended_at and prints the session_id as raw JSON.
//
// stdin is injected via os.Pipe because the dispatch module's
// runRegisteredVerb reads from os.Stdin when NeedsStdin=true.
func TestRunWith_WriteDispatched(t *testing.T) {
	t.Run("ktx write tool_call envelope persists and prints record id", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		d, _ := writeTestDB(t)
		preSeedSession(t, d, "zyxwvutsrqponmlk")

		injectStdin(t, validToolCallPayload(t))

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"write"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		got := strings.TrimSpace(stdout.String())
		if got != `"abcdefghijklmnop"` {
			t.Errorf("stdout = %q, want %q", got, `"abcdefghijklmnop"`)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on success, got %q", stderr.String())
		}

		// Verify the record is queryable via db.GetRecord — proves
		// the dispatch path took the same PersistToolCall route as
		// the legacy runWriteWith seam.
		rec, err := d.GetRecord("abcdefghijklmnop")
		if err != nil {
			t.Fatalf("GetRecord: %v", err)
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

	t.Run("ktx write session_close envelope updates ended_at", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		d, _ := writeTestDB(t)
		preSeedSession(t, d, "zyxwvutsrqponmlk")

		payload := `{"event_type":"session_close","session_id":"zyxwvutsrqponmlk","timestamp":"2026-06-18T13:00:00Z"}`
		injectStdin(t, payload)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"write"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		got := strings.TrimSpace(stdout.String())
		if got != `"zyxwvutsrqponmlk"` {
			t.Errorf("stdout = %q, want %q (session id as JSON string)", got, `"zyxwvutsrqponmlk"`)
		}

		// Confirm the dispatch path took the same CloseSession route.
		s, err := d.GetSession("zyxwvutsrqponmlk")
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if s.EndedAt != "2026-06-18T13:00:00Z" {
			t.Errorf("EndedAt = %q, want 2026-06-18T13:00:00Z", s.EndedAt)
		}
	})

	t.Run("ktx write missing required field exits 1 via dispatch registry", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		writeTestDB(t)

		// Missing the "id" field — the most-strict required field
		// (it is the persisted primary key).
		injectStdin(t, `{"tool":"bash","timestamp":"2026-06-18T12:00:00Z","input":"x","output":"y"}`)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"write"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"usage"`) {
			t.Errorf("stderr missing usage envelope: %q", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout should be empty on validation failure, got %q", stdout.String())
		}
	})

	t.Run("ktx write session_close not-found exits 2 via dispatch registry", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		writeTestDB(t)

		injectStdin(t, `{"event_type":"session_close","session_id":"aaaaaaaaaaaaaaaa","timestamp":"2026-06-18T13:00:00Z"}`)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"write"}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})

	t.Run("ktx write --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"write", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on --help, got %q", stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx write") {
			t.Errorf("stdout missing ktx write header: %q", stdout.String())
		}
	})
}