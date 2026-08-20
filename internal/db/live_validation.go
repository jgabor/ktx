package db

import (
	"database/sql"
	"fmt"
	"sort"
	"time"
)

type LiveValidationSource struct {
	ID, SessionID, Disposition, ArtifactID, LatencyState, LatencyAuthority string
	RawBytes, DeliveredBytes                                               int
	DeliveryMeasured                                                       bool
	RecoveryCueObserved                                                    bool
	LatencyMs                                                              *int64
}

type LiveValidationRecovery struct {
	CueSourceID, AttemptSourceID, ResultState string
	ByteExact                                 *bool
}

type LiveValidationRunEvent struct{ InstanceID, State, ObservedAt string }

type LiveValidationData struct {
	Sources                []LiveValidationSource
	Recoveries             []LiveValidationRecovery
	RunEvents              []LiveValidationRunEvent
	AuthoritativeToolCalls int
}

// LiveValidationDataFor returns only future explicit-collection evidence for
// the frozen validation protocol. Bounds are half-open and source-authoritative.
func (d *DB) LiveValidationDataFor(collectionID, ingress string, from, to time.Time) (LiveValidationData, error) {
	var out LiveValidationData
	if !IDPattern.MatchString(collectionID) || ingress != "run" || !from.Before(to) {
		return out, ErrContinuityInvalid
	}
	if err := d.ValidateCollectionInterval(collectionID, []string{ingress}); err != nil {
		return out, err
	}
	rows, err := d.db.Query(`SELECT tc.id,tc.session_id,tc.timestamp,LENGTH(CAST(tc.output AS BLOB)),COALESCE(cd.disposition,''),COALESCE(cd.artifact_id,''),rso.delivered_bytes,rso.recovery_cue_observed,rso.latency_ms,rso.latency_state,rso.latency_authority
		FROM runtime_source_observations rso JOIN tool_calls tc ON tc.id=rso.source_id
		LEFT JOIN compression_dispositions cd ON cd.source_id=tc.id LEFT JOIN compressed c ON c.id=cd.artifact_id
		WHERE rso.collection_id=? AND tc.producer='hook' AND tc.runtime='opencode' AND tc.tool='read' AND tc.is_error=0`, collectionID)
	if err != nil {
		return out, fmt.Errorf("db: live validation sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r LiveValidationSource
		var timestamp string
		var latency, delivered sql.NullInt64
		if err := rows.Scan(&r.ID, &r.SessionID, &timestamp, &r.RawBytes, &r.Disposition, &r.ArtifactID, &delivered, &r.RecoveryCueObserved, &latency, &r.LatencyState, &r.LatencyAuthority); err != nil {
			return out, err
		}
		at, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return out, fmt.Errorf("db: live validation source timestamp: %w", err)
		}
		if at.Before(from) || !at.Before(to) {
			continue
		}
		if latency.Valid {
			v := latency.Int64
			r.LatencyMs = &v
		}
		if delivered.Valid {
			r.DeliveredBytes = int(delivered.Int64)
			r.DeliveryMeasured = true
		}
		out.Sources = append(out.Sources, r)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	recoveryRows, err := d.db.Query(`SELECT d.source_id,tc.timestamp,COALESCE(a.attempt_source_id,''),COALESCE(a.result_state,''),a.byte_exact,COALESCE(a.attempted_at,'') FROM recovery_cue_deliveries d JOIN tool_calls tc ON tc.id=d.source_id LEFT JOIN recovery_attempts a ON a.collection_id=d.collection_id AND a.cue_source_id=d.source_id WHERE d.collection_id=? ORDER BY d.source_id,a.attempt_source_id`, collectionID)
	if err != nil {
		return out, fmt.Errorf("db: live validation recovery: %w", err)
	}
	defer recoveryRows.Close()
	type recoveryGroup struct {
		sourceInWindow bool
		attempts       []LiveValidationRecovery
	}
	groups := map[string]*recoveryGroup{}
	var cueOrder []string
	for recoveryRows.Next() {
		var r LiveValidationRecovery
		var exact sql.NullBool
		var sourceTimestamp, attemptedAt string
		if err := recoveryRows.Scan(&r.CueSourceID, &sourceTimestamp, &r.AttemptSourceID, &r.ResultState, &exact, &attemptedAt); err != nil {
			return out, err
		}
		group := groups[r.CueSourceID]
		if group == nil {
			sourceAt, parseErr := time.Parse(time.RFC3339Nano, sourceTimestamp)
			if parseErr != nil {
				return out, fmt.Errorf("db: live validation cue timestamp: %w", parseErr)
			}
			group = &recoveryGroup{sourceInWindow: !sourceAt.Before(from) && sourceAt.Before(to)}
			groups[r.CueSourceID] = group
			cueOrder = append(cueOrder, r.CueSourceID)
		}
		if r.AttemptSourceID == "" {
			continue
		}
		attemptAt, parseErr := time.Parse(time.RFC3339Nano, attemptedAt)
		if parseErr != nil {
			return out, fmt.Errorf("db: live validation attempt timestamp: %w", parseErr)
		}
		if !group.sourceInWindow || attemptAt.Before(from) || !attemptAt.Before(to) {
			continue
		}
		if exact.Valid {
			v := exact.Bool
			r.ByteExact = &v
		}
		group.attempts = append(group.attempts, r)
	}
	if err := recoveryRows.Err(); err != nil {
		return out, err
	}
	for _, cue := range cueOrder {
		g := groups[cue]
		if !g.sourceInWindow {
			continue
		}
		if len(g.attempts) == 0 {
			out.Recoveries = append(out.Recoveries, LiveValidationRecovery{CueSourceID: cue})
		} else {
			out.Recoveries = append(out.Recoveries, g.attempts...)
		}
	}
	runRows, err := d.db.Query(`SELECT instance_id,state,observed_at FROM ingress_continuity WHERE interval_id=? AND ingress=? ORDER BY rowid`, collectionID, ingress)
	if err != nil {
		return out, fmt.Errorf("db: live validation runs: %w", err)
	}
	defer runRows.Close()
	for runRows.Next() {
		var r LiveValidationRunEvent
		if err := runRows.Scan(&r.InstanceID, &r.State, &r.ObservedAt); err != nil {
			return out, err
		}
		at, parseErr := time.Parse(time.RFC3339Nano, r.ObservedAt)
		if parseErr != nil {
			return out, fmt.Errorf("db: live validation run timestamp: %w", parseErr)
		}
		if at.Before(from) || at.After(to) || at.Equal(to) && r.State != "stopped" {
			continue
		}
		out.RunEvents = append(out.RunEvents, r)
	}
	if err := runRows.Err(); err != nil {
		return out, err
	}
	toolRows, err := d.db.Query(`SELECT tc.timestamp FROM runtime_source_observations rso JOIN tool_calls tc ON tc.id=rso.source_id WHERE rso.collection_id=? AND tc.producer='hook' AND tc.runtime='opencode'`, collectionID)
	if err != nil {
		return out, err
	}
	defer toolRows.Close()
	for toolRows.Next() {
		var timestamp string
		if err := toolRows.Scan(&timestamp); err != nil {
			return out, err
		}
		at, parseErr := time.Parse(time.RFC3339Nano, timestamp)
		if parseErr != nil {
			return out, fmt.Errorf("db: live validation tool timestamp: %w", parseErr)
		}
		if !at.Before(from) && at.Before(to) {
			out.AuthoritativeToolCalls++
		}
	}
	if err := toolRows.Err(); err != nil {
		return out, err
	}
	return out, nil
}
