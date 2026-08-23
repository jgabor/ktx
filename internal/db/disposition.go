package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	CompressionDispositionArtifactPersisted      = "artifact_persisted"
	CompressionDispositionIntentionalPassthrough = "intentional_passthrough"
	CompressionDispositionAntiWorseningRejected  = "anti_worsening_rejected"
	CompressionDispositionUnobserved             = "unobserved"
)

// CompressionDisposition is the converged hook-processing outcome for one
// source call. Empty Profile and Route values represent unavailable selection
// metadata and are stored as NULL. ArtifactID is present only after promotion
// to the first authoritative artifact.
type CompressionDisposition struct {
	SourceID    string
	Profile     string
	Route       string
	Disposition string
	ArtifactID  string
	ObservedAt  string
}

// RecordCompressionDisposition retains the first non-artifact observation,
// permits one promotion to an authoritative artifact, and never downgrades or
// replaces artifact authority. The upsert is atomic under concurrent retries.
func (d *DB) RecordCompressionDisposition(disposition *CompressionDisposition) error {
	return d.recordCompressionDisposition(disposition, d.withWriteTx)
}

func (d *DB) recordCompressionDisposition(disposition *CompressionDisposition, write func(context.Context, func(*sql.Tx) error) error) error {
	if disposition == nil {
		return errors.New("db: record compression disposition: nil disposition")
	}
	if err := validateCompressionDisposition(disposition); err != nil {
		return err
	}

	ctx, cancel := writeContext()
	defer cancel()
	if err := write(ctx, func(tx *sql.Tx) error {
		var retained string
		err := tx.QueryRowContext(ctx, `SELECT disposition FROM compression_dispositions WHERE source_id = ?`, disposition.SourceID).Scan(&retained)
		if err == nil && retained == CompressionDispositionArtifactPersisted {
			return nil
		}
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("inspect retained disposition: %w", err)
		}
		if err := requireCompressionDispositionReferences(ctx, tx, disposition); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO compression_dispositions
				(source_id, profile, route, disposition, artifact_id, observed_at)
			VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?)
			ON CONFLICT(source_id) DO UPDATE SET
				profile = excluded.profile,
				route = excluded.route,
				disposition = excluded.disposition,
				artifact_id = excluded.artifact_id,
				observed_at = excluded.observed_at
			WHERE compression_dispositions.disposition <> 'artifact_persisted'
			  AND excluded.disposition = 'artifact_persisted'`,
			disposition.SourceID, disposition.Profile, disposition.Route,
			disposition.Disposition, disposition.ArtifactID, disposition.ObservedAt,
		)
		return err
	}); err != nil {
		return fmt.Errorf("db: record compression disposition for %q: %w", disposition.SourceID, err)
	}
	return nil
}

func (w *HookWriter) RecordCompressionDisposition(disposition *CompressionDisposition) error {
	return w.db.recordCompressionDisposition(disposition, w.write)
}

func (w *HookWriter) GetCompressed(id string) (*Compressed, error) {
	return w.db.GetCompressed(id)
}

func validateCompressionDisposition(disposition *CompressionDisposition) error {
	if disposition.SourceID == "" || disposition.ObservedAt == "" {
		return errors.New("db: record compression disposition: source_id and observed_at are required")
	}
	if disposition.Route != "" && disposition.Route != "matched" && disposition.Route != "default_fallback" {
		return fmt.Errorf("db: record compression disposition: invalid route %q", disposition.Route)
	}
	switch disposition.Disposition {
	case CompressionDispositionArtifactPersisted:
		if disposition.ArtifactID == "" {
			return errors.New("db: record compression disposition: artifact_persisted requires artifact_id")
		}
	case CompressionDispositionIntentionalPassthrough, CompressionDispositionAntiWorseningRejected:
		if disposition.ArtifactID != "" {
			return fmt.Errorf("db: record compression disposition: %s cannot carry artifact_id", disposition.Disposition)
		}
	default:
		return fmt.Errorf("db: record compression disposition: invalid disposition %q", disposition.Disposition)
	}
	return nil
}

func requireCompressionDispositionReferences(ctx context.Context, tx *sql.Tx, disposition *CompressionDisposition) error {
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM tool_calls WHERE id = ?`, disposition.SourceID).Scan(&one); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return fmt.Errorf("check source: %w", err)
	}
	if disposition.ArtifactID == "" {
		return nil
	}
	var authoritativeArtifactID string
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM compressed WHERE source_id = ?
		ORDER BY COALESCE(timestamp_unix_nano, ktx_unix_nano(timestamp)) ASC, id ASC LIMIT 1`, disposition.SourceID).Scan(&authoritativeArtifactID); err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return fmt.Errorf("check artifact: %w", err)
	}
	if authoritativeArtifactID != disposition.ArtifactID {
		return fmt.Errorf("artifact %q is not authoritative; first artifact is %q", disposition.ArtifactID, authoritativeArtifactID)
	}
	return nil
}

// GetCompressionDisposition returns the retained projection for sourceID.
func (d *DB) GetCompressionDisposition(sourceID string) (CompressionDisposition, error) {
	var disposition CompressionDisposition
	var profile, route, artifactID sql.NullString
	err := d.db.QueryRow(`
		SELECT source_id, profile, route, disposition, artifact_id, observed_at
		FROM compression_dispositions WHERE source_id = ?`, sourceID).Scan(
		&disposition.SourceID, &profile, &route, &disposition.Disposition, &artifactID, &disposition.ObservedAt,
	)
	if err == sql.ErrNoRows {
		return CompressionDisposition{}, ErrNotFound
	}
	if err != nil {
		return CompressionDisposition{}, fmt.Errorf("db: get compression disposition for %q: %w", sourceID, err)
	}
	disposition.Profile = profile.String
	disposition.Route = route.String
	disposition.ArtifactID = artifactID.String
	return disposition, nil
}
