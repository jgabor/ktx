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
// keeps the hash faithful to what was stored and allows integrity checks to
// compare the persisted output with its hash.
//
// Compressed.pipeline is a JSON object with selected profile, route
// provenance, retained stage names, and structured-fallback provenance. It is
// passed as a marshalled string by the caller; the runner records execution and
// the caller adds selection.
// Storing it as TEXT with the JSON shape keeps it inspectable without
// a registry lookup (a profile-hash would require readers to resolve
// the hash against a registry ktx does not yet have).
package db

import (
	"context"
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
// Pipeline is a JSON object such as
// `{"profile":"default","route":"default_fallback","stages":[],"structured_fallback":false}`.
// Timestamp is the compression time (RFC3339Nano string, supplied by the
// caller).
type Compressed struct {
	ID          string    `json:"id"`
	SourceID    string    `json:"source_id"`
	Output      string    `json:"output"`
	ContentHash string    `json:"content_hash"`
	Pipeline    string    `json:"pipeline"`
	Timestamp   string    `json:"timestamp"`
	Producer    *Producer `json:"producer"`
}

// DeliveryObservation identifies the retained rows that determine how many
// bytes a tool call delivered. The first compressed artifact is authoritative;
// without one, the original tool-call output was delivered unchanged.
type DeliveryObservation struct {
	SourceID       string
	OriginalBytes  int64
	CompressedID   *string
	DeliveredBytes int64
}

// QueryDeliveryObservations returns one observation per tool call in stable
// source order. A later recompression cannot replace the first delivered
// artifact.
func (d *DB) QueryDeliveryObservations(sessionID string) ([]DeliveryObservation, error) {
	return QueryDeliveryObservations(d.db, sessionID)
}

type deliveryQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

// QueryDeliveryObservations reads delivery evidence from either a managed DB
// or a read-only SQL snapshot.
func QueryDeliveryObservations(database deliveryQueryer, sessionID string) ([]DeliveryObservation, error) {
	rows, err := database.Query(`
		SELECT t.id,
		       length(CAST(t.output AS BLOB)),
		       c.id,
		       COALESCE(length(CAST(c.output AS BLOB)), length(CAST(t.output AS BLOB)))
		FROM tool_calls t
		LEFT JOIN compressed c ON c.id = (
			SELECT first.id FROM compressed first
			WHERE first.source_id = t.id
			ORDER BY first.timestamp_unix_nano ASC, first.id ASC
			LIMIT 1
		)
		WHERE t.session_id = ?
		ORDER BY t.timestamp ASC, t.id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("db: query delivery observations: %w", err)
	}
	defer rows.Close()
	result := []DeliveryObservation{}
	for rows.Next() {
		var observation DeliveryObservation
		if err := rows.Scan(&observation.SourceID, &observation.OriginalBytes, &observation.CompressedID, &observation.DeliveredBytes); err != nil {
			return nil, fmt.Errorf("db: scan delivery observation: %w", err)
		}
		result = append(result, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: query delivery observations: %w", err)
	}
	return result, nil
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
// The write uses the shared bounded transaction retry path. The INSERT writes
// all columns; the caller is responsible for
// generating id via NewID, computing ContentHash via
// ContentHash(compressedOutput) (the integrity rule, recomputed from
// the compressed output), and marshalling the Pipeline JSON record.
//
// A source_id that does not exist in tool_calls is rejected by the
// FK constraint and surfaced as ErrSourceNotFound (which wraps
// ErrNotFound so the CLI maps it to exit 2). The FK rejection is a
// generic sqlite error without a stable sentinel; we detect it by
// checking whether the source row exists BEFORE the insert (one
// indexed SELECT against tool_calls.id) so the error class is
// deterministic regardless of driver error-string drift.
func (d *DB) InsertCompressed(c *Compressed) error {
	return d.insertCompressed(c, d.withWriteTx)
}

func (d *DB) insertCompressed(c *Compressed, write func(context.Context, func(*sql.Tx) error) error) error {
	if c == nil {
		return errors.New("db: insert compressed: nil compressed")
	}
	if c.ID == "" {
		return errors.New("db: insert compressed: id is empty")
	}
	if c.SourceID == "" {
		return errors.New("db: insert compressed: source_id is empty")
	}

	ctx, cancel := writeContext()
	defer cancel()
	err := write(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM tool_calls WHERE id = ? LIMIT 1`, c.SourceID).Scan(&one)
		if err == sql.ErrNoRows {
			return ErrSourceNotFound
		}
		if err != nil {
			return fmt.Errorf("check source %q: %w", c.SourceID, err)
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO compressed (id, source_id, output, content_hash, pipeline, timestamp, timestamp_unix_nano, producer)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.SourceID, c.Output, c.ContentHash, c.Pipeline, c.Timestamp, sortableTimestamp(c.Timestamp), c.Producer,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: insert compressed %q: %w", c.ID, err)
	}
	return nil
}

// InsertCompressedOnce records at most one compressed artifact for a source.
// Concurrent retries race through one atomic INSERT ... SELECT statement; the
// winner's artifact is returned to every caller and later candidates are not
// persisted.
func (d *DB) InsertCompressedOnce(c *Compressed) (*Compressed, bool, error) {
	return d.insertCompressedOnce(c, d.withWriteTx)
}

func (d *DB) insertCompressedOnce(c *Compressed, write func(context.Context, func(*sql.Tx) error) error) (*Compressed, bool, error) {
	if c == nil {
		return nil, false, errors.New("db: insert compressed once: nil compressed")
	}
	if c.ID == "" || c.SourceID == "" {
		return nil, false, errors.New("db: insert compressed once: id and source_id are required")
	}

	ctx, cancel := writeContext()
	defer cancel()
	var (
		stored   *Compressed
		inserted bool
	)
	err := write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO compressed (id, source_id, output, content_hash, pipeline, timestamp, producer)
			 SELECT ?, ?, ?, ?, ?, ?, ?
		 WHERE EXISTS (SELECT 1 FROM tool_calls WHERE id = ?)
		   AND NOT EXISTS (SELECT 1 FROM compressed WHERE source_id = ?)`,
			c.ID, c.SourceID, c.Output, c.ContentHash, c.Pipeline, c.Timestamp, c.Producer,
			c.SourceID, c.SourceID,
		)
		if err != nil {
			return fmt.Errorf("db: insert compressed once %q: %w", c.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("db: inspect compressed once %q: %w", c.ID, err)
		}
		inserted = n != 0
		if !inserted {
			var existing Compressed
			err := tx.QueryRowContext(ctx,
				`SELECT id, source_id, output, content_hash, pipeline, timestamp, producer
				 FROM compressed WHERE source_id = ?
				 ORDER BY timestamp_unix_nano ASC, id ASC LIMIT 1`, c.SourceID,
			).Scan(&existing.ID, &existing.SourceID, &existing.Output, &existing.ContentHash, &existing.Pipeline, &existing.Timestamp, &existing.Producer)
			if err == sql.ErrNoRows {
				return ErrSourceNotFound
			}
			if err != nil {
				return fmt.Errorf("db: get compressed for source %q: %w", c.SourceID, err)
			}
			stored = &existing
			return nil
		}
		stored = c
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return stored, inserted, nil
}

func (w *HookWriter) InsertCompressed(c *Compressed) error {
	return w.db.insertCompressed(c, w.write)
}

func (w *HookWriter) InsertCompressedOnce(c *Compressed) (*Compressed, bool, error) {
	return w.db.insertCompressedOnce(c, w.write)
}

func (w *HookWriter) GetCompressedBySource(sourceID string) (*Compressed, error) {
	return w.db.GetCompressedBySource(sourceID)
}

// GetCompressed fetches a `compressed` row by id.
// Returns ErrNotFound when no row matches so the CLI layer maps a
// missing compressed id to exit 2 (the `ktx decompress` not-found
// path). The returned struct mirrors the persisted row's JSON shape.
func (d *DB) GetCompressed(id string) (*Compressed, error) {
	c, err := scanCompressed(d.db.QueryRow(
		`SELECT id, source_id, output, content_hash, pipeline, timestamp, producer
		 FROM compressed WHERE id = ?`,
		id,
	))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get compressed %q: %w", id, err)
	}
	return c, nil
}

// GetCompressedBySource fetches the first compressed artifact for a source
// tool_call id. Timestamp and ID ordering matches QueryDeliveryObservations so
// explicit recompression cannot replace delivery authority. Returns ErrNotFound
// when no compressed artifact exists for the source.
func (d *DB) GetCompressedBySource(sourceID string) (*Compressed, error) {
	c, err := scanCompressed(d.db.QueryRow(
		`SELECT id, source_id, output, content_hash, pipeline, timestamp, producer
		 FROM compressed WHERE source_id = ?
		 ORDER BY timestamp_unix_nano ASC, id ASC LIMIT 1`,
		sourceID,
	))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get compressed for source %q: %w", sourceID, err)
	}
	return c, nil
}

func scanCompressed(row rowScanner) (*Compressed, error) {
	var c Compressed
	if err := row.Scan(&c.ID, &c.SourceID, &c.Output, &c.ContentHash, &c.Pipeline, &c.Timestamp, &c.Producer); err != nil {
		return nil, err
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
