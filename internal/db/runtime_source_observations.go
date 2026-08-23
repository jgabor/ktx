package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type RuntimeSourceObservation struct {
	CollectionID, SessionID, SourceID, LatencyState, LatencyUnit, LatencyAuthority, ObservedAt string
	LatencyMs                                                                                  *int64
	DeliveredBytes                                                                             *int64
	RecoveryCueObserved                                                                        bool
}

// RetainRuntimeSourceObservation links wrapper-observed runtime timing only to
// source evidence already owned by the OpenCode hook in the same session.
func RecoveryFooter(sourceID string, originalBytes, compressedBytes int) string {
	reduction := 0
	if originalBytes > 0 && compressedBytes < originalBytes {
		reduction = (originalBytes - compressedBytes) * 100 / originalBytes
	}
	return fmt.Sprintf("\n[ktx] output compressed: %d bytes (%d%% reduction) • get full output with `ktx read %s`\n", compressedBytes, reduction, sourceID)
}

func (d *DB) RetainRuntimeSourceObservation(o RuntimeSourceObservation, deliveredOutput string) error {
	if !IDPattern.MatchString(o.CollectionID) || !IDPattern.MatchString(o.SessionID) || !IDPattern.MatchString(o.SourceID) {
		return ErrContinuityInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, o.ObservedAt); err != nil {
		return ErrContinuityInvalid
	}
	probe := &ToolCall{LatencyMs: o.LatencyMs, LatencyState: o.LatencyState, LatencyUnit: o.LatencyUnit, LatencyAuthority: o.LatencyAuthority}
	if err := normalizeLatencyEvidence(probe); err != nil {
		return ErrContinuityInvalid
	}
	ctx, cancel := writeContext()
	defer cancel()
	return d.withWriteTx(ctx, func(tx *sql.Tx) error {
		var sessionID, runtime, original string
		var producer sql.NullString
		if err := tx.QueryRow(`SELECT session_id,COALESCE(runtime,''),producer,output FROM tool_calls WHERE id=?`, o.SourceID).Scan(&sessionID, &runtime, &producer, &original); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrContinuityInvalid
			}
			return err
		}
		if sessionID != o.SessionID || runtime != "opencode" || !producer.Valid || producer.String != string(ProducerHook) {
			return ErrContinuityInvalid
		}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM collection_intervals WHERE id=? AND supported_ingresses='["run"]'`, o.CollectionID).Scan(&n); err != nil || n != 1 {
			return ErrContinuityInvalid
		}
		if err := tx.QueryRow(`SELECT count(*) FROM ingress_continuity WHERE interval_id=? AND ingress='run' AND instance_id=? AND state='started'`, o.CollectionID, o.SessionID).Scan(&n); err != nil || n != 1 {
			return ErrContinuityInvalid
		}
		var disposition string
		var dispositionArtifact sql.NullString
		dispositionErr := tx.QueryRow(`SELECT disposition,artifact_id FROM compression_dispositions WHERE source_id=?`, o.SourceID).Scan(&disposition, &dispositionArtifact)
		var firstArtifactID, artifactOutput string
		artifactErr := tx.QueryRow(`SELECT id,output FROM compressed WHERE source_id=? ORDER BY timestamp_unix_nano,id LIMIT 1`, o.SourceID).Scan(&firstArtifactID, &artifactOutput)
		var cueArtifact string
		cueErr := tx.QueryRow(`SELECT artifact_id FROM recovery_cue_deliveries WHERE collection_id=? AND source_id=?`, o.CollectionID, o.SourceID).Scan(&cueArtifact)
		expected := original
		cueObserved := false
		switch {
		case dispositionErr == nil && (disposition == "intentional_passthrough" || disposition == "anti_worsening_rejected") && !dispositionArtifact.Valid && errors.Is(artifactErr, sql.ErrNoRows) && errors.Is(cueErr, sql.ErrNoRows):
		case dispositionErr == nil && disposition == "artifact_persisted" && dispositionArtifact.Valid && artifactErr == nil && dispositionArtifact.String == firstArtifactID:
			expected = artifactOutput
			if cueErr == nil && cueArtifact == firstArtifactID {
				expected += RecoveryFooter(o.SourceID, len(original), len(artifactOutput))
				cueObserved = true
			} else if !errors.Is(cueErr, sql.ErrNoRows) {
				return ErrContinuityInvalid
			}
		default:
			return ErrContinuityInvalid
		}
		if deliveredOutput != expected {
			return ErrContinuityInvalid
		}
		deliveredBytes := int64(len(deliveredOutput))
		o.DeliveredBytes = &deliveredBytes
		o.RecoveryCueObserved = cueObserved
		res, err := tx.Exec(`INSERT INTO runtime_source_observations (collection_id,session_id,source_id,latency_ms,latency_state,latency_unit,latency_authority,delivered_bytes,recovery_cue_observed,observed_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(collection_id,source_id) DO NOTHING`, o.CollectionID, o.SessionID, o.SourceID, o.LatencyMs, o.LatencyState, o.LatencyUnit, o.LatencyAuthority, o.DeliveredBytes, o.RecoveryCueObserved, o.ObservedAt)
		if err != nil {
			return err
		}
		inserted, err := res.RowsAffected()
		if err != nil || inserted != 1 {
			return ErrContinuityInvalid
		}
		return nil
	})
}
