package db

import (
	"database/sql"
	"errors"
	"time"
)

const (
	RecoveryDeliveryAuthority = "opencode_adapter"
	RecoveryAttemptAuthority  = "opencode_hook"
)

type RecoveryCueDelivery struct {
	CollectionID, SourceID, ArtifactID, SessionID, RuntimeSessionID, RuntimeCallID, DeliveredAt, Authority string
}

type RecoveryAttempt struct {
	AttemptSourceID, CollectionID, CueSourceID, AttemptedAt, ResultState, Authority string
	ByteExact                                                                       *bool
}

func (d *DB) AcknowledgeRecoveryCue(o RecoveryCueDelivery) error {
	if !IDPattern.MatchString(o.CollectionID) || !IDPattern.MatchString(o.SourceID) || !IDPattern.MatchString(o.ArtifactID) || !IDPattern.MatchString(o.SessionID) || o.RuntimeSessionID == "" || o.RuntimeCallID == "" || o.Authority != RecoveryDeliveryAuthority {
		return ErrContinuityInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, o.DeliveredAt); err != nil {
		return ErrContinuityInvalid
	}
	ctx, cancel := writeContext()
	defer cancel()
	return d.withWriteTx(ctx, func(tx *sql.Tx) error {
		var sessionID, runtime, producer string
		if err := tx.QueryRow(`SELECT session_id,COALESCE(runtime,''),COALESCE(producer,'') FROM tool_calls WHERE id=?`, o.SourceID).Scan(&sessionID, &runtime, &producer); err != nil {
			return ErrContinuityInvalid
		}
		if sessionID != o.SessionID || runtime != "opencode" || producer != string(ProducerHook) {
			return ErrContinuityInvalid
		}
		var firstArtifact string
		if err := tx.QueryRow(`SELECT id FROM compressed WHERE source_id=? ORDER BY timestamp,rowid LIMIT 1`, o.SourceID).Scan(&firstArtifact); err != nil || firstArtifact != o.ArtifactID {
			return ErrContinuityInvalid
		}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM collection_intervals WHERE id=? AND supported_ingresses='["run"]'`, o.CollectionID).Scan(&n); err != nil || n != 1 {
			return ErrContinuityInvalid
		}
		res, err := tx.Exec(`INSERT INTO recovery_cue_deliveries (collection_id,source_id,artifact_id,session_id,runtime_session_id,runtime_call_id,delivered_at,authority) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(collection_id,source_id) DO NOTHING`, o.CollectionID, o.SourceID, o.ArtifactID, o.SessionID, o.RuntimeSessionID, o.RuntimeCallID, o.DeliveredAt, o.Authority)
		if err != nil {
			return err
		}
		inserted, _ := res.RowsAffected()
		if inserted == 1 {
			return nil
		}
		var artifact, session, runtimeSession, runtimeCall, authority string
		if err := tx.QueryRow(`SELECT artifact_id,session_id,runtime_session_id,runtime_call_id,authority FROM recovery_cue_deliveries WHERE collection_id=? AND source_id=?`, o.CollectionID, o.SourceID).Scan(&artifact, &session, &runtimeSession, &runtimeCall, &authority); err != nil {
			return err
		}
		if artifact != o.ArtifactID || session != o.SessionID || runtimeSession != o.RuntimeSessionID || runtimeCall != o.RuntimeCallID || authority != o.Authority {
			return ErrContinuityInvalid
		}
		return nil
	})
}

func (d *DB) RetainRecoveryAttempt(o RecoveryAttempt) error {
	if !IDPattern.MatchString(o.AttemptSourceID) || !IDPattern.MatchString(o.CollectionID) || !IDPattern.MatchString(o.CueSourceID) || o.Authority != RecoveryAttemptAuthority || (o.ResultState != "succeeded" && o.ResultState != "failed") || (o.ResultState == "succeeded") != (o.ByteExact != nil) {
		return ErrContinuityInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, o.AttemptedAt); err != nil {
		return ErrContinuityInvalid
	}
	ctx, cancel := writeContext()
	defer cancel()
	return d.withWriteTx(ctx, func(tx *sql.Tx) error {
		var producer, runtime, session string
		if err := tx.QueryRow(`SELECT COALESCE(producer,''),COALESCE(runtime,''),session_id FROM tool_calls WHERE id=?`, o.AttemptSourceID).Scan(&producer, &runtime, &session); err != nil || producer != string(ProducerHook) || runtime != "opencode" {
			return ErrContinuityInvalid
		}
		var ownerSession string
		if err := tx.QueryRow(`SELECT session_id FROM recovery_cue_deliveries WHERE collection_id=? AND source_id=?`, o.CollectionID, o.CueSourceID).Scan(&ownerSession); err != nil || ownerSession != session {
			return ErrContinuityInvalid
		}
		res, err := tx.Exec(`INSERT INTO recovery_attempts (attempt_source_id,collection_id,cue_source_id,attempted_at,result_state,byte_exact,authority) VALUES (?,?,?,?,?,?,?) ON CONFLICT(attempt_source_id) DO NOTHING`, o.AttemptSourceID, o.CollectionID, o.CueSourceID, o.AttemptedAt, o.ResultState, boolSQL(o.ByteExact), o.Authority)
		if err != nil {
			return err
		}
		inserted, _ := res.RowsAffected()
		if inserted == 1 {
			return nil
		}
		var collection, cue, state, authority string
		var exact sql.NullBool
		if err := tx.QueryRow(`SELECT collection_id,cue_source_id,result_state,byte_exact,authority FROM recovery_attempts WHERE attempt_source_id=?`, o.AttemptSourceID).Scan(&collection, &cue, &state, &exact, &authority); err != nil {
			return err
		}
		if collection != o.CollectionID || cue != o.CueSourceID || state != o.ResultState || authority != o.Authority || exact.Valid != (o.ByteExact != nil) || (exact.Valid && exact.Bool != *o.ByteExact) {
			return ErrContinuityInvalid
		}
		return nil
	})
}

func boolSQL(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

func (d *DB) RecoveryCue(collectionID, sourceID string) (RecoveryCueDelivery, error) {
	var o RecoveryCueDelivery
	err := d.db.QueryRow(`SELECT collection_id,source_id,artifact_id,session_id,runtime_session_id,runtime_call_id,delivered_at,authority FROM recovery_cue_deliveries WHERE collection_id=? AND source_id=?`, collectionID, sourceID).Scan(&o.CollectionID, &o.SourceID, &o.ArtifactID, &o.SessionID, &o.RuntimeSessionID, &o.RuntimeCallID, &o.DeliveredAt, &o.Authority)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}
