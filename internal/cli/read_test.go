// Table-driven tests for `ktx read` (internal/cli/read.go).
//
// `ktx read` is the I/O-primitive counterpart to `ktx write`: raw JSON
// output, no --format flag, and byte-faithful round-trip from the persisted
// row.
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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

type persistThenFailWriter struct {
	database *dbpkg.DB
	record   *dbpkg.ToolCall
	writes   int
}

func (writer *persistThenFailWriter) Write(_ []byte) (int, error) {
	writer.writes++
	if err := writer.database.PersistToolCall(writer.record); err != nil {
		return 0, fmt.Errorf("persist while read output is being written: %w", err)
	}
	return 0, errors.New("injected read output failure")
}

func TestRunReadWithReleasesDatabaseBeforeOutputFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ktx.db")
	t.Setenv("KTX_DB", path)
	database := readTestDB(t)
	seedToolCall(t, database, "outputreadsrcaaa", "outputfailsessaa")

	writerDatabase, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writerDatabase.Close()
	failing := &persistThenFailWriter{
		database: writerDatabase,
		record: &dbpkg.ToolCall{
			ID: "outputfailsrcaaa", SessionID: "outputfailsessaa", Tool: "bash",
			Input: "true", Output: "accepted", ContentHash: dbpkg.ContentHash("accepted"),
			Timestamp: "2026-08-11T00:00:00Z", EventType: "tool_call",
		},
	}
	var stderr bytes.Buffer
	if code := runReadWith([]string{"outputreadsrcaaa"}, Options{Format: FormatJSON}, failing, &stderr); code != ExitInternal {
		t.Fatalf("exit = %d, want %d; stderr=%q", code, ExitInternal, stderr.String())
	}
	if failing.writes != 1 {
		t.Fatalf("output writes = %d, want 1", failing.writes)
	}
	stored, err := writerDatabase.GetToolCall("outputfailsrcaaa")
	if err != nil {
		t.Fatalf("writer did not proceed during failed output: %v", err)
	}
	if stored.Output != "accepted" {
		t.Fatalf("stored output = %q, want accepted", stored.Output)
	}
}

func TestRunWithReadPreservesOlderAndNewerSchemaFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		version     int
		failureCode ErrorCode
	}{
		{name: "older schema requires migration", version: dbpkg.MaxSupportedSchema() - 1, failureCode: CodeSchemaMigration},
		{name: "newer schema requires a newer binary", version: dbpkg.MaxSupportedSchema() + 1, failureCode: CodeSchemaIncompatible},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "ktx.db")
			seedSchemaVersion(t, path, test.version)
			t.Setenv("KTX_DB", path)
			before := directorySnapshot(t, directory)

			var stdout, stderr bytes.Buffer
			if code := runWith([]string{"read", "abcdefghijklmnop"}, &stdout, &stderr); code != ExitSchemaIncompatible {
				t.Fatalf("exit = %d, want %d; stdout=%q stderr=%q", code, ExitSchemaIncompatible, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			var envelope struct {
				Error struct {
					Code ErrorCode `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error: %v\nstderr=%s", err, stderr.String())
			}
			if envelope.Error.Code != test.failureCode {
				t.Fatalf("error code = %q, want %q", envelope.Error.Code, test.failureCode)
			}
			if after := directorySnapshot(t, directory); !reflect.DeepEqual(after, before) {
				t.Fatalf("read changed schema storage:\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}
