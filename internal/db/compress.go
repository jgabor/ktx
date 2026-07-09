// Persistence methods for the `compressed` reversibility-layer table.
//
// The compressed table holds compressed artifacts SEPARATE from
// tool_calls (bench cost-aggregates tool_calls; injecting zero-cost
// non-evidence rows would corrupt the cost-summed evidence set). Each
// row references its source via source_id, which REFERENCES
// tool_calls(id); with foreign_keys=1 (enforced by Open's DSN), a
// compressed row cannot point at a non-existent tool_call.
//
// Three methods form the persistence contract:
//
//   - InsertCompressed  — writes one row, mapping a missing source_id
//     to ErrNotFound (the FK rejection surfaces as ErrNotFound so the
//     CLI layer maps it to exit 2, matching the not-found surface
//     used by `ktx read` / `ktx get`).
//   - GetCompressed     — fetches a row by compressed id; ErrNotFound
//     on miss (exit 2 surface).
//   - GetSourceOutput   — the reversal primitive `ktx decompress`
//     invokes: one indexed SELECT against tool_calls.output, returns
//     the source's original output bytes verbatim. ErrNotFound on miss.
//
// Compressed.content_hash is recomputed from the compressed OUTPUT
// (not the source output) per the integrity rule — the caller passes
// the already-hashed value via db.ContentHash(compressedOutput). This
// keeps the hash faithful to what was stored, so a downstream
// integrity check (ktx doctor / future verification) compares the
// hash of the persisted output against the persisted hash.
//
// Compressed.pipeline is a JSON array of stage names that ran
// ("a record of what stages ran"). It is passed as a marshalled
// string by the caller; the runner records the names of stages that
// successfully ran, and the caller passes that JSON through verbatim.
// Storing it as TEXT with the JSON shape keeps it inspectable without
// a registry lookup (a profile-hash would require readers to resolve
// the hash against a registry ktx does not yet have).
package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// Compressed is one row of the `compressed` table (the reversibility
// layer).
//
// JSON tags follow the column names so the CLI layer can marshal
// directly without an intermediate struct (the row IS the JSON shape
// the inspector sees). omitempty is intentionally NOT applied: an
// empty Output is a meaningful signal (a source row with empty output
// compressed to an empty string), and the pipeline/timestamp fields
// are contractual (the runner always records what ran; the
// compressor always records when). Matches the ToolCall / Session
// convention used by ktx read / ktx get.
//
// SourceID references tool_calls.id; InsertCompressed enforces the FK
// (foreign_keys=1). Output is the compressed bytes as a string (the
// tool_calls.output column is TEXT; the pipeline operates on []byte
// but is persisted as text, mirroring how tool_calls.output is
// stored). ContentHash is the SHA-256 hex digest of Output (NOT of
// the source output — recomputed from the artifact that was stored).
// Pipeline is a JSON array of stage names that ran, e.g.
// `["strip-ansi","head:5"]` or `[]`. Timestamp is the compression
// time (RFC3339Nano string, supplied by the caller).
type Compressed struct {
	ID          string `json:"id"`
	SourceID    string `json:"source_id"`
	Output      string `json:"output"`
	ContentHash string `json:"content_hash"`
	Pipeline    string `json:"pipeline"`
	Timestamp   string `json:"timestamp"`
}

// ErrSourceNotFound is a typed sentinel returned by InsertCompressed
// when the SQLite FK rejects a row referencing a non-existent
// tool_calls.id. The modernc.org/sqlite FK error is a generic
// sqlite.Error without a stable sentinel, so InsertCompressed maps
// the FK rejection to this typed sentinel. The CLI layer maps it to
// exit 2 (CodeNotFound) via the existing toKtxError path — it wraps
// ErrNotFound so errors.Is(err, ErrNotFound) holds for callers that
// treat "any not-found" uniformly while errors.Is(err,
// ErrSourceNotFound) distinguishes the compressed-source miss from
// a compressed-id miss.
var ErrSourceNotFound = fmt.Errorf("db: compressed source_id not found: %w", ErrNotFound)

// InsertCompressed persists one `compressed` row.
// The transaction follows the same BEGIN/Commit + defer Rollback
// idempotency pattern as PersistToolCall and InsertBenchRun. The
// INSERT writes all 6 columns; the caller is responsible for
// generating id via NewID, computing ContentHash via
// ContentHash(compressedOutput) (the integrity rule, recomputed from
// the compressed output), and marshalling Pipeline as a JSON array.
//
// A source_id that does not exist in tool_calls is rejected by the
// FK constraint and surfaced as ErrSourceNotFound (which wraps
// ErrNotFound so the CLI maps it to exit 2). The FK rejection is a
// generic sqlite error without a stable sentinel; we detect it by
// checking whether the source row exists BEFORE the insert (one
// indexed SELECT against tool_calls.id) so the error class is
// deterministic regardless of driver error-string drift.
func (d *DB) InsertCompressed(c *Compressed) error {
	if c == nil {
		return errors.New("db: insert compressed: nil compressed")
	}
	if c.ID == "" {
		return errors.New("db: insert compressed: id is empty")
	}
	if c.SourceID == "" {
		return errors.New("db: insert compressed: source_id is empty")
	}

	// Pre-check the source exists so the not-found case maps to a
	// typed error rather than a driver-specific FK error string.
	// The FK still enforces correctness at the SQL level (defense in
	// depth: a race between the check and the insert is
	// impossible because both run on the same connection's
	// transaction), but the typed error keeps the CLI's exit-code
	// mapping stable across SQLite driver versions.
	var one int
	err := d.db.QueryRow(`SELECT 1 FROM tool_calls WHERE id = ? LIMIT 1`, c.SourceID).Scan(&one)
	if err == sql.ErrNoRows {
		return fmt.Errorf("db: insert compressed %q: %w", c.ID, ErrSourceNotFound)
	}
	if err != nil {
		return fmt.Errorf("db: insert compressed %q: check source %q: %w", c.ID, c.SourceID, err)
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("db: begin compressed tx: %w", err)
	}
	defer tx.Rollback() // Safe, low-overhead no-op after tx.Commit()

	_, err = tx.Exec(
		`INSERT INTO compressed (id, source_id, output, content_hash, pipeline, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.SourceID, c.Output, c.ContentHash, c.Pipeline, c.Timestamp,
	)
	if err != nil {
		return fmt.Errorf("db: insert compressed %q: %w", c.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit compressed %q: %w", c.ID, err)
	}
	return nil
}

// GetCompressed fetches a `compressed` row by id.
// Returns ErrNotFound when no row matches so the CLI layer maps a
// missing compressed id to exit 2 (the `ktx decompress` not-found
// path). The returned struct mirrors the persisted row's JSON shape.
func (d *DB) GetCompressed(id string) (*Compressed, error) {
	var c Compressed
	err := d.db.QueryRow(
		`SELECT id, source_id, output, content_hash, pipeline, timestamp
		 FROM compressed WHERE id = ?`,
		id,
	).Scan(
		&c.ID, &c.SourceID, &c.Output, &c.ContentHash, &c.Pipeline, &c.Timestamp,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get compressed %q: %w", id, err)
	}
	return &c, nil
}

// GetSourceOutput is the reversal primitive `ktx decompress` invokes.
// Given a source tool_call id (the
// compressed.source_id), it returns the source's original output
// bytes verbatim — the public-surface reversal: "a compressed
// output can be retrieved as the original bytes via the public surface."
//
// The reversal is OUT-OF-BAND by id: there is no per-stage
// inverse, no in-band hash marker, no CCR hash store. The
// tool_calls table holds append-only originals (INSERT-only,
// content_hash for integrity, nanoid addressability); the
// compressed row records what compressed it (source_id + pipeline
// JSON), and decompression is a single indexed SELECT against
// tool_calls.output.
//
// Returns ErrNotFound when the source row does not exist (exit 2
// surface). A source id is always a tool_call id — there is no
// session-source compressed artifact; the reversal primitive
// is scoped to tool_calls only.
func (d *DB) GetSourceOutput(sourceID string) (string, error) {
	var output string
	err := d.db.QueryRow(
		`SELECT output FROM tool_calls WHERE id = ?`,
		sourceID,
	).Scan(&output)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("db: get source output %q: %w", sourceID, err)
	}
	return output, nil
}
