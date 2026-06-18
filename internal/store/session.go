// Session identity resolution for the adapter/hook path (Decision 1 revised).
//
// When a tool_call arrives without a session_id, ktx resolves the session by
// ppid file lookup. The ktx subprocess is a direct child of the agent process,
// so os.Getppid() is the agent's PID — stable for the agent's lifetime. The
// session file at $XDG_RUNTIME_DIR/ktx/sessions/<ppid>.json caches the
// session_id, ppid, and ppid_start_time so subsequent invocations reuse the
// same session without caller-side coordination.
//
// Staleness contract (D1 revised): a session file is stale when the ppid is
// dead OR the recorded ppid_start_time no longer matches (PID reuse by a
// different process). A stale file triggers close-old + create-new on the
// next call. On non-Linux (no /proc), the liveness/start-time check is
// skipped and a file pointing at a live DB session is reused (conservative).
//
// The session file is a cache of identity; the database is the source of
// truth. If the file and database ever disagree, the database wins — a file
// pointing at a non-existent session is treated as stale.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SessionFile is the ppid-keyed session identity cache (Decision 1 revised).
type SessionFile struct {
	SessionID     string `json:"session_id"`
	PPID          int    `json:"ppid"`
	PPIDStartTime int64  `json:"ppid_start_time"`
}

// sessionDir resolves the session-file directory (Decision 1 revised):
// $XDG_RUNTIME_DIR/ktx/sessions, falling back to os.TempDir()/ktx/sessions
// when XDG_RUNTIME_DIR is unset (some CI shells and non-launchd macOS setups
// do not set it). The fallback is adequate for PoC; the spec mandates
// XDG_RUNTIME_DIR but does not require ktx to fail when it is absent.
func sessionDir() string {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return filepath.Join(base, "ktx", "sessions")
	}
	return filepath.Join(os.TempDir(), "ktx", "sessions")
}

func sessionFilePath(ppid int) string {
	return filepath.Join(sessionDir(), fmt.Sprintf("%d.json", ppid))
}

// readSessionFile reads the ppid-keyed session file. It returns (nil, nil)
// when the file does not exist — the common "first call" case — so callers
// can distinguish "no file" from "read error" without checking os.IsNotExist.
func readSessionFile(ppid int) (*SessionFile, error) {
	data, err := os.ReadFile(sessionFilePath(ppid))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: read session file for ppid %d: %w", ppid, err)
	}
	var sf SessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("store: parse session file for ppid %d: %w", ppid, err)
	}
	return &sf, nil
}

// writeSessionFile writes the ppid-keyed session file, creating the directory
// with private permissions (0o700) if missing. The file is 0o600 because it
// is private session identity keyed to this user's process tree.
//
// Concurrent ktx invocations (parallel tool calls) may race on file
// creation: both see no file, both open sessions, last writer wins. The
// orphaned session is reconciled by a future `ktx db prune` (D1 revised).
// File locking would be clever, not boring — out of scope for PoC.
func writeSessionFile(ppid int, sf *SessionFile) error {
	dir := sessionDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("store: create session dir %q: %w", dir, err)
	}
	data, err := json.Marshal(sf)
	if err != nil {
		return fmt.Errorf("store: encode session file: %w", err)
	}
	path := sessionFilePath(ppid)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("store: write session file %q: %w", path, err)
	}
	return nil
}

// procAvailable reports whether the /proc filesystem is mounted. Used to
// decide between the precise liveness/start-time check (Linux) and the
// conservative "file exists + session in DB = reuse" fallback (macOS and
// other non-Linux). The task contract accepts this fallback for the PoC.
func procAvailable() bool {
	info, err := os.Stat("/proc")
	return err == nil && info.IsDir()
}

// ppidStartTime reads /proc/<pid>/stat field 22 (starttime in clock ticks
// since boot) and returns (starttime, true) on success. It returns (0, false)
// if the process is dead, /proc is unavailable, or the stat file is
// unreadable — all of which the caller treats as "cannot verify liveness".
//
// The stat format is: `pid (comm) state ppid ... starttime ...` where comm
// may contain spaces and parentheses. We find the last ')' and split the
// remainder; starttime is field 22, which is the 20th field after ')' (state
// is field 3 = index 0 after ')').
func ppidStartTime(pid int) (int64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		return 0, false
	}
	rest := strings.Fields(s[idx+1:])
	if len(rest) <= 19 {
		return 0, false
	}
	n, err := strconv.ParseInt(rest[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// ResolveSessionID resolves the session id for a tool_call (Decision 1
// revised). When hint is non-empty, the caller provided a session_id (sidecar
// path, D9); ktx trusts it and verifies the session exists, returning
// ErrNotFound if not. When hint is empty, ktx resolves by ppid file lookup
// (adapter/hook path, D1 revised):
//
//  1. Read $XDG_RUNTIME_DIR/ktx/sessions/<ppid>.json.
//  2. File exists + ppid alive + start-time matches + session in DB → reuse.
//  3. File exists + ppid dead OR start-time mismatch OR session gone from DB
//     → close old session (set ended_at), create new, write new file.
//  4. File absent → create new session, write file.
//
// timestamp is used as started_at and last_seen_at for newly created sessions
// and as ended_at for sessions closed by the staleness path.
func (d *DB) ResolveSessionID(hint, timestamp string) (string, error) {
	// Caller-provided session_id (sidecar path, D9): trust and verify.
	if hint != "" {
		exists, err := d.SessionExists(hint)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", ErrNotFound
		}
		return hint, nil
	}

	// ppid-based resolution (adapter/hook path, D1 revised).
	ppid := os.Getppid()
	sf, err := readSessionFile(ppid)
	if err != nil {
		return "", err
	}

	if sf != nil && d.canReuse(ppid, sf, timestamp) {
		return sf.SessionID, nil
	}

	return d.openSession(ppid, timestamp)
}

// canReuse reports whether the session file's session can be reused. As a
// side effect, it closes the old session (sets ended_at) when it returns
// false and the old session still exists in the database — so the caller
// only has to openSession() on a false result.
func (d *DB) canReuse(ppid int, sf *SessionFile, timestamp string) bool {
	exists, err := d.SessionExists(sf.SessionID)
	if err != nil || !exists {
		// File points at a session that's gone from the DB. Nothing to
		// close; just open a new one.
		return false
	}

	if !procAvailable() {
		// Non-Linux: conservative fallback (file exists + session in DB).
		return true
	}

	startTime, alive := ppidStartTime(ppid)
	if !alive || startTime != sf.PPIDStartTime {
		// Parent died or PID was reused. Close the old session so it is not
		// left open forever; a new one opens on the next line.
		_ = d.CloseSession(sf.SessionID, timestamp)
		return false
	}
	return true
}

// openSession creates a new session row and writes the ppid session file. The
// agent is set to "unknown" (Decision 1: the adapter/wrapper overrides this
// in Tasks 5/6). The ppid_start_time is captured from /proc when available so
// the next invocation can detect PID reuse; it is 0 on non-Linux, where the
// conservative fallback never compares it.
func (d *DB) openSession(ppid int, timestamp string) (string, error) {
	return d.openNamedSession("unknown", ppid, timestamp)
}

// OpenNamedSession creates a new session with the supplied agent name and
// writes a ppid-keyed session file (Decision 1 revised). It is the public
// counterpart to the unexported openSession used by ResolveSessionID, exposed
// for the wrapper path (ktx run) where the agent binary name is known at
// session-open time (Tasks 5/6). The hook path defaults agent to "unknown"
// because the agent name is not known when ktx write runs.
//
// ppid is the session-file key. For the wrapper, pass os.Getpid() — the
// wrapper's own PID, since the child sees the wrapper as its parent. For the
// hook path, pass os.Getppid() — the agent's PID. timestamp is used as both
// started_at and last_seen_at.
func (d *DB) OpenNamedSession(agent string, ppid int, timestamp string) (string, error) {
	if agent == "" {
		return "", errors.New("store: open named session: agent is empty")
	}
	return d.openNamedSession(agent, ppid, timestamp)
}

// openNamedSession is the shared implementation for the hook path (default
// "unknown" agent) and the wrapper path (caller-supplied agent). Splitting
// it from the public surface keeps the unknown-vs-named choice mechanical.
func (d *DB) openNamedSession(agent string, ppid int, timestamp string) (string, error) {
	id, err := NewID()
	if err != nil {
		return "", fmt.Errorf("store: generate session id: %w", err)
	}

	if err := d.InsertSession(&Session{
		ID:         id,
		Agent:      agent,
		StartedAt:  timestamp,
		LastSeenAt: timestamp,
	}); err != nil {
		return "", err
	}

	var startTime int64
	if procAvailable() {
		startTime, _ = ppidStartTime(ppid)
	}
	if err := writeSessionFile(ppid, &SessionFile{
		SessionID:     id,
		PPID:          ppid,
		PPIDStartTime: startTime,
	}); err != nil {
		return "", err
	}
	return id, nil
}
