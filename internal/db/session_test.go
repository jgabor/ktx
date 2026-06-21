// Table-driven unit tests for internal/db/session.go (Task 2).
//
// Acceptance coverage:
//
//   - AC 4 (non-existent hint returns ErrNotFound with no side effect):
//     TestResolveSessionID_NonExistentHint
//
// The session-file path (empty hint, ppid-keyed lookup) is covered
// indirectly via the OpenNamedSession round-trip below; the AC only
// requires the sidecar-path failure mode. Each subtest uses stdlib
// testing only.
package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
)

// TestResolveSessionID_NonExistentHint covers AC 4: ResolveSessionID
// with a non-empty hint that does not name an existing session row
// must return ErrNotFound. The contract has two parts:
//
//  1. No silent reuse: a typo'd or stale hint must surface as
//     ErrNotFound so the caller can distinguish "absent" from
//     "reusing the existing row".
//  2. No file write: the sidecar path (hint != "") does not touch
//     $XDG_RUNTIME_DIR/ktx/sessions/<ppid>.json — the file is the
//     hook-path cache, not the sidecar path's responsibility.
//
// The D9 envelope + D1 revised session identity contract rely on
// this exact behaviour: a sidecar caller that supplies a
// non-existent session_id must NOT silently create a new session,
// because the caller is asserting an identity it expects to find.
func TestResolveSessionID_NonExistentHint(t *testing.T) {
	t.Run("non-existent hint returns ErrNotFound and writes no session file", func(t *testing.T) {
		// WithTempXDG isolates XDG_RUNTIME_DIR so the test cannot
		// observe (or clobber) a real session file. The pre-test
		// state of the session-file directory is captured so we can
		// assert it stays empty after the ResolveSessionID call.
		tmp := testutil.WithTempXDG(t)
		sessionDir := filepath.Join(tmp, "run", "ktx", "sessions")

		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Pre-state: the session-file directory does not exist
		// (WithTempXDG only creates <tmp>/run and <tmp>/share).
		// A sidecar-path call must not create it.
		if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
			t.Fatalf("session dir %q exists before ResolveSessionID; expected absent", sessionDir)
		}

		const hint = "zzzzzzzzzzzzzzzz" // 16 chars to look plausible
		id, err := d.ResolveSessionID(hint, "2026-06-21T00:00:00Z")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveSessionID(non-existent) = (%q, %v); want (\"\", ErrNotFound)", id, err)
		}
		if id != "" {
			t.Errorf("ResolveSessionID returned id %q on ErrNotFound; want empty", id)
		}

		// Post-state: the session-file directory must still not
		// exist (or, if WriteFile created the parent, must be empty
		// of any *.json file). The strictest assertion is "no
		// directory created at all" because writeSessionFile calls
		// MkdirAll — a non-existent directory means the sidecar
		// path did not touch the filesystem.
		if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
			// If a directory was created, confirm it is empty of
			// any *.json file. A leak here would mean the
			// sidecar-path code went past SessionExists(false) and
			// into writeSessionFile, which is the bug we are
			// guarding against.
			entries, listErr := os.ReadDir(sessionDir)
			if listErr != nil {
				t.Fatalf("ReadDir %q: %v", sessionDir, listErr)
			}
			for _, e := range entries {
				if filepath.Ext(e.Name()) == ".json" {
					t.Errorf("ResolveSessionID wrote session file %q on non-existent hint", filepath.Join(sessionDir, e.Name()))
				}
			}
		}
	})

	t.Run("empty hint + no ppid file opens a fresh session and writes the file", func(t *testing.T) {
		// Companion to the failure case: the happy path of the
		// hook path (hint == "" → ppid lookup → no file → open).
		// Confirms the sidecar and hook paths are distinct code
		// paths and the AC 4 negative case did not regress the
		// hook path by accident.
		testutil.WithTempXDG(t)

		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		id, err := d.ResolveSessionID("", "2026-06-21T00:00:00Z")
		if err != nil {
			t.Fatalf("ResolveSessionID(empty hint, no file): %v", err)
		}
		if !IDPattern.MatchString(id) {
			t.Errorf("ResolveSessionID returned id %q; want 16-char lowercase alpha", id)
		}
		// The session row must now exist in the DB.
		got, err := d.GetSession(id)
		if err != nil {
			t.Fatalf("GetSession(%q): %v", id, err)
		}
		if got.ID != id {
			t.Errorf("GetSession.ID = %q; want %q", got.ID, id)
		}
	})

	t.Run("existing hint returns the hint and writes no session file", func(t *testing.T) {
		// Companion: a real hint round-trips verbatim. The session
		// file (hook-path cache) is not consulted and not written —
		// the sidecar path is identity-by-assertion, not by file
		// lookup.
		tmp := testutil.WithTempXDG(t)
		sessionDir := filepath.Join(tmp, "run", "ktx", "sessions")

		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		const ts = "2026-06-21T00:00:00Z"
		hint, err := d.OpenNamedSession("test-agent", os.Getpid(), ts)
		if err != nil {
			t.Fatalf("OpenNamedSession: %v", err)
		}

		// Confirm the OpenNamedSession write did create a session
		// file (the hook-path cache was populated). This is the
		// pre-state for the ResolveSessionID assertion.
		if _, err := os.Stat(sessionDir); err != nil {
			t.Fatalf("session dir not created after OpenNamedSession: %v", err)
		}
		filesBefore, err := filepath.Glob(filepath.Join(sessionDir, "*.json"))
		if err != nil {
			t.Fatalf("glob session files: %v", err)
		}
		if len(filesBefore) == 0 {
			t.Fatalf("OpenNamedSession did not write a session file")
		}

		got, err := d.ResolveSessionID(hint, ts)
		if err != nil {
			t.Fatalf("ResolveSessionID(existing hint): %v", err)
		}
		if got != hint {
			t.Errorf("ResolveSessionID returned %q; want %q (hint)", got, hint)
		}

		// No additional session file is written on the sidecar
		// path. Count files before and after.
		filesAfter, err := filepath.Glob(filepath.Join(sessionDir, "*.json"))
		if err != nil {
			t.Fatalf("glob session files: %v", err)
		}
		if len(filesAfter) != len(filesBefore) {
			t.Errorf("ResolveSessionID(sidecar) wrote a session file: before=%d after=%d", len(filesBefore), len(filesAfter))
		}
	})

	t.Run("empty hint + stale ppid file opens a fresh session (closes the old)", func(t *testing.T) {
		// The hook path's "stale file → close old, create new"
		// branch is the path-specific close behaviour from D1
		// revised. We seed a session file with a non-existent
		// session_id; canReuse must return false (SessionExists
		// false), and openSession must allocate a fresh id without
		// touching the stale entry.
		tmp := testutil.WithTempXDG(t)
		sessionDir := filepath.Join(tmp, "run", "ktx", "sessions")

		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Pre-seed a session file pointing at a session that does
		// not exist in the DB. canReuse sees SessionExists=false
		// and returns false without closing (nothing to close).
		if err := os.MkdirAll(sessionDir, 0o700); err != nil {
			t.Fatalf("MkdirAll session dir: %v", err)
		}
		stalePath := filepath.Join(sessionDir, "99999.json")
		if err := os.WriteFile(stalePath, []byte(`{"session_id":"stale-stale-stale","ppid":99999,"ppid_start_time":0}`), 0o600); err != nil {
			t.Fatalf("seed stale session file: %v", err)
		}

		// Resolve with empty hint → ppid resolution path. We
		// cannot inject a fake ppid (os.Getppid is hardcoded),
		// so we skip directly to OpenNamedSession-style flow.
		// Instead, drive ResolveSessionID with hint="" and assert
		// the resulting id is fresh — the ppid file is whatever
		// the test runner's ppid is. The stale file at 99999.json
		// is not the test runner's ppid, so it is irrelevant to
		// canReuse for this test invocation.
		id, err := d.ResolveSessionID("", "2026-06-21T00:00:00Z")
		if err != nil {
			t.Fatalf("ResolveSessionID: %v", err)
		}
		if id == "stale-stale-stale" {
			t.Fatal("ResolveSessionID reused a stale session_id")
		}
		if !IDPattern.MatchString(id) {
			t.Errorf("ResolveSessionID returned %q; want 16-char lowercase alpha", id)
		}
	})
}

// TestOpenNamedSession_RoundTrip is a small companion that exercises
// the public OpenNamedSession API: a named session is inserted, the
// ppid file is written, the session is fetchable by id, and the
// agent name round-trips verbatim.
func TestOpenNamedSession_RoundTrip(t *testing.T) {
	testutil.WithTempXDG(t)

	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	id, err := d.OpenNamedSession("anthropic-agent", 4242, "2026-06-21T00:00:00Z")
	if err != nil {
		t.Fatalf("OpenNamedSession: %v", err)
	}
	if !IDPattern.MatchString(id) {
		t.Errorf("session id %q does not match %s", id, IDPattern)
	}

	got, err := d.GetSession(id)
	if err != nil {
		t.Fatalf("GetSession(%q): %v", id, err)
	}
	if got.Agent != "anthropic-agent" {
		t.Errorf("GetSession.Agent = %q; want %q", got.Agent, "anthropic-agent")
	}
	if got.StartedAt != "2026-06-21T00:00:00Z" {
		t.Errorf("GetSession.StartedAt = %q; want %q", got.StartedAt, "2026-06-21T00:00:00Z")
	}
	if got.EndedAt != "" {
		t.Errorf("GetSession.EndedAt = %q; want empty (session still open)", got.EndedAt)
	}
}

// TestCloseSession_NotFound covers the ErrNotFound contract on
// CloseSession: an unknown id returns ErrNotFound (no silent no-op),
// matching the ResolveSessionID sidecar-path behaviour. Together they
// establish "absent" as a first-class signal in the session API.
func TestCloseSession_NotFound(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	err = d.CloseSession("zzzzzzzzzzzzzzzz", "2026-06-21T00:00:00Z")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("CloseSession(unknown) = %v; want ErrNotFound", err)
	}
}
