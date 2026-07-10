// Persistence methods for the `recorded_streams` table (the recording
// contract).
//
// The recorded_streams table holds the verbatim JSONL stream that
// `ktx bench run` captured from the agent's stdout — the authoritative
// cost evidence. OpenCode 1.17.8 emits tokens/cost/model on
// step_finish events (step-aggregate), not on per-tool tool_use
// events; the wrapper persists only tool_use rows as tool_call rows,
// so those rows are NULL-by-design for tokens/cost/model. The captured
// stream is the single source of truth for the bench_run's cost
// evidence, and the recording contract settles that it lives in the
// database (not a sibling repo file, not an in-memory buffer discarded
// on function return), following the artifact-to-table precedent.
//
// Each row references its session via session_id (REFERENCES
// sessions(id), NOT NULL) and its bench_run via bench_run_id (REFERENCES
// bench_runs(id), NULLABLE — NULL when the stream was captured via
// `ktx run --record-stream` with no bench_runs parent); with
// foreign_keys=1 (enforced by Open's DSN), a recorded_streams row
// cannot point at a non-existent session. The table is the first
// persistent session-to-bench_run linkage (bench_runs has no
// session_id column).
//
// Three methods form the persistence contract:
//
//   - InsertRecordedStream       — writes one row, mapping missing
//     session_id or a non-empty-but-unknown bench_run_id to
//     ErrNotFound (the FK rejection surfaces as ErrNotFound so the CLI
//     layer maps it to exit 2). An empty BenchRunID inserts SQL NULL
//     (the ktx-run --record-stream path) and skips the bench_run
//     pre-check.
//   - GetRecordedStreamBySession — fetches a row by session_id (the
//     public --replay arg); ErrNotFound on miss (exit 2
//     surface).
//   - GetRecordedStreamByBenchRun — fetches a row by bench_run_id
//     (internal FK lookup); ErrNotFound on miss.
//
// RecordedStream.content_hash is recomputed from stream_text per the
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

// RecordedStream is one row of the `recorded_streams` table (the
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
// SessionID references sessions.id (NOT NULL FK); BenchRunID references
// bench_runs.id and is NULLABLE — empty string in Go corresponds to SQL
// NULL. A NULL BenchRunID marks a stream captured via `ktx run
// --record-stream` (no bench_runs parent); a non-empty BenchRunID marks
// a stream captured via `ktx bench run` (1:1 with a bench_runs row).
// InsertRecordedStream enforces the session_id FK (foreign_keys=1) and
// only pre-checks the bench_run when BenchRunID is non-empty.
// StreamText is the verbatim JSONL stream exactly as the agent emitted
// it (the wrapper pipes every line to stdout via teeAndParse, and both
// runBenchRun and executeRun-with---record-stream capture that stdout
// into a bytes.Buffer). ContentHash is the SHA-256 hex digest of
// StreamText (recomputed per the integrity rule). StreamFormat is
// metadata recording what was captured (e.g. 'opencode-jsonl-1') so readers
// can choose the correct parser. CapturedAt is RFC3339Nano, supplied by the
// caller.
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

// InsertRecordedStream persists one `recorded_streams` row (the
// recording contract). The transaction follows the same BEGIN/Commit +
// defer Rollback idempotency pattern as InsertCompressed and
// InsertBenchRun. The INSERT writes all 7 columns; the caller is
// responsible for generating id via NewID, computing ContentHash
// via ContentHash(streamText) (the integrity rule, recomputed from the
// stream text), and supplying stream_format and captured_at.
//
// A session_id that does not exist is rejected by the FK constraint and
// surfaced as ErrRecordedStreamSourceNotFound (which wraps ErrNotFound
// so the CLI maps it to exit 2). The FK rejection is a generic sqlite
// error without a stable sentinel; we detect it by pre-checking the
// session row exists BEFORE the insert (an indexed SELECT) so the error
// class is deterministic regardless of driver error-string drift.
//
// BenchRunID is NULLABLE: an empty string inserts SQL NULL (the
// `ktx run --record-stream` path, where no bench_runs row exists). A
// non-empty BenchRunID is pre-checked against bench_runs the same way
// session_id is, so a reference to a non-existent bench_run surfaces as
// ErrRecordedStreamSourceNotFound. The NULL path skips the bench_run
// pre-check entirely — there is nothing to validate.
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

	// Pre-check the bench_run exists ONLY when BenchRunID is non-empty.
	// An empty BenchRunID inserts SQL NULL (the ktx-run --record-stream
	// path); there is no bench_run to validate.
	if rs.BenchRunID != "" {
		err = d.db.QueryRow(`SELECT 1 FROM bench_runs WHERE id = ? LIMIT 1`, rs.BenchRunID).Scan(&one)
		if err == sql.ErrNoRows {
			return fmt.Errorf("db: insert recorded_stream %q: %w", rs.ID, ErrRecordedStreamSourceNotFound)
		}
		if err != nil {
			return fmt.Errorf("db: insert recorded_stream %q: check bench_run %q: %w", rs.ID, rs.BenchRunID, err)
		}
	}

	// Map an empty BenchRunID to SQL NULL via sql.NullString so the
	// nullable bench_run_id column receives NULL rather than the empty
	// string (keeps the FK contract honest: NULL is "no bench parent",
	// '' would reference nothing without being NULL).
	benchRunID := sql.NullString{String: rs.BenchRunID}
	if rs.BenchRunID != "" {
		benchRunID.Valid = true
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("db: begin recorded_stream tx: %w", err)
	}
	defer tx.Rollback() // Safe, low-overhead no-op after tx.Commit()

	_, err = tx.Exec(
		`INSERT INTO recorded_streams (id, session_id, bench_run_id, stream_text, content_hash, stream_format, captured_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rs.ID, rs.SessionID, benchRunID, rs.StreamText, rs.ContentHash, rs.StreamFormat, rs.CapturedAt,
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
// session_id (the public --replay arg). Returns ErrNotFound
// when no row matches so the CLI layer maps a missing session_id to
// exit 2 (the `ktx bench run --replay` not-found path). Uses the
// recorded_streams_session_id_idx index for O(log n) lookup.
//
// bench_run_id is scanned via sql.NullString so a NULL column (a
// ktx-run --record-stream capture with no bench parent) round-trips
// to the empty string in Go rather than panicking on a NULL-to-string
// scan error.
func (d *DB) GetRecordedStreamBySession(sessionID string) (*RecordedStream, error) {
	var (
		rs         RecordedStream
		benchRunID sql.NullString
	)
	err := d.db.QueryRow(
		`SELECT id, session_id, bench_run_id, stream_text, content_hash, stream_format, captured_at
		 FROM recorded_streams WHERE session_id = ?`,
		sessionID,
	).Scan(
		&rs.ID, &rs.SessionID, &benchRunID, &rs.StreamText, &rs.ContentHash, &rs.StreamFormat, &rs.CapturedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get recorded_stream by session %q: %w", sessionID, err)
	}
	rs.BenchRunID = benchRunID.String
	return &rs, nil
}

// GetRecordedStreamByBenchRun fetches a `recorded_streams` row by
// bench_run_id (internal FK lookup; bench_run_id is NOT a public
// --replay arg). Returns ErrNotFound when no row matches.
// Uses the recorded_streams_bench_run_id_idx index for O(log n) lookup.
//
// bench_run_id is scanned via sql.NullString for consistency with
// GetRecordedStreamBySession; a NULL bench_run_id would not match this
// lookup (IS NULL is the only predicate that matches NULL), so the
// NullString is always Valid on the hit path, but the scan shape stays
// uniform across both getters.
func (d *DB) GetRecordedStreamByBenchRun(benchRunID string) (*RecordedStream, error) {
	var (
		rs   RecordedStream
		brID sql.NullString
	)
	err := d.db.QueryRow(
		`SELECT id, session_id, bench_run_id, stream_text, content_hash, stream_format, captured_at
		 FROM recorded_streams WHERE bench_run_id = ?`,
		benchRunID,
	).Scan(
		&rs.ID, &rs.SessionID, &brID, &rs.StreamText, &rs.ContentHash, &rs.StreamFormat, &rs.CapturedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get recorded_stream by bench_run %q: %w", benchRunID, err)
	}
	rs.BenchRunID = brID.String
	return &rs, nil
}
