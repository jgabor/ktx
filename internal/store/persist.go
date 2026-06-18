// Persistence methods for the ktx database (Decision 7).
//
// Two-table model:
//   - tool_calls: append-only. INSERT only (PersistToolCall).
//   - sessions: aggregate. INSERT on open (InsertSession), UPDATE on
//     roll-up and close (PersistToolCall's tx, CloseSession).
//
// The ToolCall struct carries the full D9 flat JSON shape, including the
// optional M2 fields. The M1 schema has no columns for the optional fields,
// so they are accepted in the payload but not stored until M2 adds columns.
package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a referenced session or record does not exist
// in the database. The cli layer maps it to a not-found exit code (D4).
var ErrNotFound = errors.New("store: not found")

// ToolCall is the flat JSON tool_call payload (Decision 9).
//
// Required fields are persisted to the tool_calls table. The optional M2
// fields (TokensIn, TokensOut, Cost, LatencyMs, Model, Provider) are
// accepted in the payload so callers can include them without erroring, but
// the M1 schema has no columns for them — they are dropped on persist until
// M2 adds the columns.
//
// EventType defaults to "tool_call" when empty; "session_close" selects the
// close payload shape (just SessionID + Timestamp).
type ToolCall struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	Tool        string `json:"tool"`
	Input       string `json:"input"`
	Output      string `json:"output"`
	ContentHash string `json:"content_hash"`
	Timestamp   string `json:"timestamp"`
	EventType   string `json:"event_type"`

	// Optional M2 fields — accepted but not persisted in M1.
	TokensIn  int64   `json:"tokens_in,omitempty"`
	TokensOut int64   `json:"tokens_out,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
	LatencyMs int64   `json:"latency_ms,omitempty"`
	Model     string  `json:"model,omitempty"`
	Provider  string  `json:"provider,omitempty"`
}

// Session is a row in the sessions table (Decision 7).
type Session struct {
	ID            string
	Agent         string
	StartedAt     string
	EndedAt       string
	LastSeenAt    string
	ToolCallCount int64
}

// SessionExists reports whether a session with the given id exists. An empty
// id is treated as "does not exist" so the caller-provided hint path can
// distinguish "absent" from "present" without a separate guard.
func (d *DB) SessionExists(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var one int
	err := d.db.QueryRow(`SELECT 1 FROM sessions WHERE id = ? LIMIT 1`, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: check session %q: %w", id, err)
	}
	return true, nil
}

// InsertSession creates a new session row (Decision 7: INSERT on open). The
// caller is responsible for generating the id via NewID and for writing the
// ppid session file when this is part of the adapter/hook open path.
func (d *DB) InsertSession(s *Session) error {
	_, err := d.db.Exec(
		`INSERT INTO sessions (id, agent, started_at, ended_at, last_seen_at, tool_call_count) VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.Agent, s.StartedAt, s.EndedAt, s.LastSeenAt, s.ToolCallCount,
	)
	if err != nil {
		return fmt.Errorf("store: insert session %q: %w", s.ID, err)
	}
	return nil
}

// CloseSession sets ended_at on the session row (Decision 7: UPDATE on
// close). It returns ErrNotFound when no session matches id, so the close
// path surfaces a clear not-found rather than a silent no-op.
func (d *DB) CloseSession(id, endedAt string) error {
	res, err := d.db.Exec(`UPDATE sessions SET ended_at = ? WHERE id = ?`, endedAt, id)
	if err != nil {
		return fmt.Errorf("store: close session %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PersistToolCall inserts the tool_call row and updates the session roll-up
// (tool_call_count + 1, last_seen_at = tc.Timestamp) in one transaction
// (Decision 7: tool_calls INSERT only, sessions UPDATE roll-up). The
// transaction keeps the aggregate consistent with the evidence — a
// tool_call never exists without its session counters reflecting it.
//
// EventType defaults to "tool_call" when empty.
func (d *DB) PersistToolCall(tc *ToolCall) error {
	eventType := tc.EventType
	if eventType == "" {
		eventType = "tool_call"
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}

	_, err = tx.Exec(
		`INSERT INTO tool_calls (id, session_id, tool, input, output, content_hash, timestamp, event_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tc.ID, tc.SessionID, tc.Tool, tc.Input, tc.Output, tc.ContentHash, tc.Timestamp, eventType,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: insert tool_call %q: %w", tc.ID, err)
	}

	_, err = tx.Exec(
		`UPDATE sessions SET tool_call_count = tool_call_count + 1, last_seen_at = ? WHERE id = ?`,
		tc.Timestamp, tc.SessionID,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: update session roll-up %q: %w", tc.SessionID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tool_call %q: %w", tc.ID, err)
	}
	return nil
}
