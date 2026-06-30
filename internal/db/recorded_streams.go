// Persistence methods for the `recorded_streams` table (D20 recording
// contract).
//
// The recorded_streams table holds the verbatim JSONL stream that
// `ktx bench run` captured from the agent's stdout — the authoritative
// cost evidence per D17. OpenCode 1.17.8 emits tokens/cost/model on
// step_finish events (step-aggregate), not on per-tool tool_use
// events; the wrapper persists only tool_use rows as tool_call rows,
// so those rows are NULL-by-design for tokens/cost/model. The captured
// stream is the single source of truth for the bench_run's cost
// evidence, and D20 settles that it lives in the database (not a
// sibling repo file, not an in-memory buffer discarded on function
// return), following D14's artifact-to-table precedent.
//
// Each row references its session via session_id (REFERENCES
// sessions(id)) and its bench_run via bench_run_id (REFERENCES
// bench_runs(id)); with foreign_keys=1 (enforced by Open's DSN), a
// recorded_streams row cannot point at a non-existent session or
// bench_run. The table is the first persistent session-to-bench_run
// linkage (bench_runs has no session_id column).
//
// Three methods form the D20 persistence contract:
//
//   - InsertRecordedStream       — writes one row, mapping missing
//     session_id or bench_run_id to ErrNotFound (the FK rejection
//     surfaces as ErrNotFound so the CLI layer maps it to exit 2).
//   - GetRecordedStreamBySession — fetches a row by session_id (the
//     public --replay arg per D13); ErrNotFound on miss (exit 2
//     surface).
//   - GetRecordedStreamByBenchRun — fetches a row by bench_run_id
//     (internal FK lookup); ErrNotFound on miss.
//
// RecordedStream.content_hash is recomputed from stream_text per D9's
// integrity rule — the caller passes the already-hashed value via
// db.ContentHash(streamText). This keeps the hash faithful to what was
// stored, so a downstream --replay consistency check (stream integrity)
// compares the hash of the persisted stream_text against the persisted
// hash.
package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// RecordedStream is one row of the `recorded_streams` table (D20
// recording contract).
//
// JSON tags follow the column names so the CLI layer can marshal
// directly without an intermediate struct (the row IS the JSON shape
// the --replay consistency envelope sees). omitempty is intentionally
// NOT applied: an empty StreamText is a meaningful signal (a session
// that emitted no events still gets captured), and the stream_format
// and captured_at fields are contractual. Matches the Compressed /
// ToolCall convention used by ktx get / ktx list.
//
// SessionID references sessions.id; BenchRunID references bench_runs.id;
// InsertRecordedStream enforces both FKs (foreign_keys=1). StreamText
// is the verbatim JSONL stream exactly as the agent emitted it (the
// wrapper pipes every line to stdout via teeAndParse, and runBenchRun
// captures that stdout into a bytes.Buffer). ContentHash is the
// SHA-256 hex digest of StreamText (recomputed per D9 integrity rule).
// StreamFormat is metadata recording what was captured (e.g.
// 'opencode-jsonl-1'); no parser dispatch exists today, but the
// column future-proofs against silently mis-parsing a non-OpenCode
// stream. CapturedAt is RFC3339Nano, supplied by the caller.
type RecordedStream struct {
	ID           string `json:"id"`
	SessionID    string `json:"session_id"`
	BenchRunID   string `json:"bench_run_id"`
	StreamText   string `json:"stream_text"`
	ContentHash  string `json:"content_hash"`
	StreamFormat string `json:"stream_format"`
	CapturedAt   string `json:"captured_at"`
}

// ErrRecordedStreamSourceNotFound is a typed sentinel returned by
// InsertRecordedStream when the SQLite FK rejects a row referencing a
// non-existent session_id or bench_run_id. Wraps ErrNotFound so the
// CLI layer maps it to exit 2 (CodeNotFound) via the existing
// toKtxError path, while errors.Is(err, ErrRecordedStreamSourceNotFound)
// distinguishes the recorded-stream-source miss from a recorded-stream-
// id miss.
var ErrRecordedStreamSourceNotFound = fmt.Errorf("db: recorded_stream source not found: %w", ErrNotFound)

// InsertRecordedStream persists one `recorded_streams` row (D20
// recording contract). The transaction follows the same BEGIN/Commit +
// defer Rollback idempotency pattern as InsertCompressed and
// InsertBenchRun. The INSERT writes all 7 columns; the caller is
// responsible for generating id via NewID (D3), computing ContentHash
// via ContentHash(streamText) (D9 integrity rule, recomputed from the
// stream text), and supplying stream_format and captured_at.
//
// A session_id or bench_run_id that does not exist is rejected by the
// FK constraint and surfaced as ErrRecordedStreamSourceNotFound (which
// wraps ErrNotFound so the CLI maps it to exit 2). The FK rejection is
// a generic sqlite error without a stable sentinel; we detect it by
// pre-checking both source rows exist BEFORE the insert (two indexed
// SELECTs) so the error class is deterministic regardless of driver
// error-string drift.
func (d *DB) InsertRecordedStream(rs *RecordedStream) error {
	if rs == nil {
		return errors.New("db: insert recorded_stream: nil recorded_stream")
	}
	if rs.ID == "" {
		return errors.New("db: insert recorded_stream: id is empty")
	}
	if rs.SessionID == "" {
		return errors.New("db: insert recorded_stream: session_id is empty")
	}
	if rs.BenchRunID == "" {
		return errors.New("db: insert recorded_stream: bench_run_id is empty")
	}

	// Pre-check the session exists so the not-found case maps to a
	// typed error rather than a driver-specific FK error string.
	var one int
	err := d.db.QueryRow(`SELECT 1 FROM sessions WHERE id = ? LIMIT 1`, rs.SessionID).Scan(&one)
	if err == sql.ErrNoRows {
		return fmt.Errorf("db: insert recorded_stream %q: %w", rs.ID, ErrRecordedStreamSourceNotFound)
	}
	if err != nil {
		return fmt.Errorf("db: insert recorded_stream %q: check session %q: %w", rs.ID, rs.SessionID, err)
	}

	// Pre-check the bench_run exists.
	err = d.db.QueryRow(`SELECT 1 FROM bench_runs WHERE id = ? LIMIT 1`, rs.BenchRunID).Scan(&one)
	if err == sql.ErrNoRows {
		return fmt.Errorf("db: insert recorded_stream %q: %w", rs.ID, ErrRecordedStreamSourceNotFound)
	}
	if err != nil {
		return fmt.Errorf("db: insert recorded_stream %q: check bench_run %q: %w", rs.ID, rs.BenchRunID, err)
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("db: begin recorded_stream tx: %w", err)
	}
	defer tx.Rollback() // Safe, low-overhead no-op after tx.Commit()

	_, err = tx.Exec(
		`INSERT INTO recorded_streams (id, session_id, bench_run_id, stream_text, content_hash, stream_format, captured_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rs.ID, rs.SessionID, rs.BenchRunID, rs.StreamText, rs.ContentHash, rs.StreamFormat, rs.CapturedAt,
	)
	if err != nil {
		return fmt.Errorf("db: insert recorded_stream %q: %w", rs.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit recorded_stream %q: %w", rs.ID, err)
	}
	return nil
}

// GetRecordedStreamBySession fetches a `recorded_streams` row by
// session_id (D20 / D13's public --replay arg). Returns ErrNotFound
// when no row matches so the CLI layer maps a missing session_id to
// exit 2 (the `ktx bench run --replay` not-found path). Uses the
// recorded_streams_session_id_idx index for O(log n) lookup.
func (d *DB) GetRecordedStreamBySession(sessionID string) (*RecordedStream, error) {
	var rs RecordedStream
	err := d.db.QueryRow(
		`SELECT id, session_id, bench_run_id, stream_text, content_hash, stream_format, captured_at
		 FROM recorded_streams WHERE session_id = ?`,
		sessionID,
	).Scan(
		&rs.ID, &rs.SessionID, &rs.BenchRunID, &rs.StreamText, &rs.ContentHash, &rs.StreamFormat, &rs.CapturedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get recorded_stream by session %q: %w", sessionID, err)
	}
	return &rs, nil
}

// GetRecordedStreamByBenchRun fetches a `recorded_streams` row by
// bench_run_id (internal FK lookup; bench_run_id is NOT a public
// --replay arg per D13/D20). Returns ErrNotFound when no row matches.
// Uses the recorded_streams_bench_run_id_idx index for O(log n) lookup.
func (d *DB) GetRecordedStreamByBenchRun(benchRunID string) (*RecordedStream, error) {
	var rs RecordedStream
	err := d.db.QueryRow(
		`SELECT id, session_id, bench_run_id, stream_text, content_hash, stream_format, captured_at
		 FROM recorded_streams WHERE bench_run_id = ?`,
		benchRunID,
	).Scan(
		&rs.ID, &rs.SessionID, &rs.BenchRunID, &rs.StreamText, &rs.ContentHash, &rs.StreamFormat, &rs.CapturedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get recorded_stream by bench_run %q: %w", benchRunID, err)
	}
	return &rs, nil
}
