package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jgabor/ktx/internal/telemetry"
)

const (
	RuntimeEventSource                   = "event_hook"
	RuntimeOpen                          = "open"
	RuntimeComplete                      = "complete"
	RuntimeIncomplete                    = "incomplete"
	RuntimeContradictory                 = "contradictory"
	ModelStepAccepted                    = "accepted"
	ModelStepExactDuplicate              = "exact_duplicate"
	ModelStepConflictingDuplicate        = "conflicting_duplicate"
	maxRuntimeEventOrdinal        uint64 = 1<<53 - 1
)

var (
	ErrRuntimeEventConflict   = errors.New("db: conflicting runtime event replay")
	ErrRuntimeIntervalInvalid = errors.New("db: invalid runtime event interval")
)

type RuntimeEventInterval struct {
	ID                  string
	SessionID           string
	AttemptID           string
	Runtime             string
	RuntimeVersion      string
	Source              string
	ContractVersion     string
	OpenedAt            string
	ClosedAt            *string
	CloseState          string
	FirstOrdinal        *uint64
	LastOrdinal         *uint64
	EventCount          uint64
	CollectorEpochCount uint64
	ContradictionCount  uint64
	FailureReason       *string
	Events              []RuntimeEvent
}

type RuntimeEvent struct {
	IntervalID              string
	Ordinal                 uint64
	EpochID                 string
	EpochOrdinal            uint64
	Kind                    string
	EventType               string
	SourceSessionID         *string
	MessageID               *string
	PartID                  *string
	CapturedAt              string
	ModelStepClassification *string
}

type RuntimeEventInput struct {
	IntervalID      string
	SessionID       string
	EpochID         string
	EpochOrdinal    uint64
	AfterOrdinal    *uint64
	Kind            string
	EventType       string
	SourceSessionID *string
	MessageID       *string
	PartID          *string
	CapturedAt      string
	Telemetry       *telemetry.Observation
}

// CreateBenchmarkEventInterval creates the only session and event interval in
// a fresh per-attempt database. It does not create a process identity file.
func (d *DB) CreateBenchmarkEventInterval(attemptID, runtime, runtimeVersion, contractVersion, openedAt string) (*RuntimeEventInterval, error) {
	for name, value := range map[string]string{"attempt_id": attemptID, "runtime": runtime, "runtime_version": runtimeVersion, "contract_version": contractVersion, "opened_at": openedAt} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%w: %s is blank", ErrRuntimeIntervalInvalid, name)
		}
	}
	sessionID, err := NewID()
	if err != nil {
		return nil, err
	}
	intervalID, err := NewID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := writeContext()
	defer cancel()
	err = withWriteTx(ctx, d.db, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: database already contains %d sessions", ErrRuntimeIntervalInvalid, count)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, agent, started_at, last_seen_at, tool_call_count) VALUES (?, 'opencode', ?, ?, 0)`, sessionID, openedAt, openedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO runtime_event_intervals
			(id, session_id, attempt_id, runtime, runtime_version, source, contract_version, opened_at, close_state)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'open')`, intervalID, sessionID, attemptID, runtime, runtimeVersion, RuntimeEventSource, contractVersion, openedAt)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("db: create benchmark event interval: %w", err)
	}
	return &RuntimeEventInterval{ID: intervalID, SessionID: sessionID, AttemptID: attemptID, Runtime: runtime, RuntimeVersion: runtimeVersion, Source: RuntimeEventSource, ContractVersion: contractVersion, OpenedAt: openedAt, CloseState: RuntimeOpen, Events: []RuntimeEvent{}}, nil
}

// PersistRuntimeEvent allocates the next interval-global ordinal and retains
// the normalized event. Model telemetry, when present, is inserted in the same
// transaction. Replaying an identical epoch tuple is idempotent.
func (d *DB) PersistRuntimeEvent(input RuntimeEventInput) (RuntimeEvent, bool, error) {
	if err := validateRuntimeEventInput(input); err != nil {
		return RuntimeEvent{}, false, err
	}
	ctx, cancel := writeContext()
	defer cancel()
	var event RuntimeEvent
	inserted := false
	err := withWriteTx(ctx, d.db, func(tx *sql.Tx) error {
		var owner, state string
		if err := tx.QueryRowContext(ctx, `SELECT session_id, close_state FROM runtime_event_intervals WHERE id = ?`, input.IntervalID).Scan(&owner, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: interval not found", ErrRuntimeIntervalInvalid)
			}
			return err
		}
		if owner != input.SessionID || state != RuntimeOpen {
			return fmt.Errorf("%w: interval owner/state mismatch", ErrRuntimeIntervalInvalid)
		}
		var existing RuntimeEvent
		err := scanRuntimeEvent(tx.QueryRowContext(ctx, `SELECT interval_id, ordinal, epoch_id, epoch_ordinal, kind, event_type, source_session_id, message_id, part_id, captured_at, model_step_classification FROM runtime_events WHERE interval_id = ? AND epoch_id = ? AND epoch_ordinal = ?`, input.IntervalID, input.EpochID, input.EpochOrdinal), &existing)
		if err == nil {
			if !runtimeEventMatches(existing, input) {
				return ErrRuntimeEventConflict
			}
			if input.AfterOrdinal != nil && (*input.AfterOrdinal == maxRuntimeEventOrdinal || existing.Ordinal != *input.AfterOrdinal+1) {
				return fmt.Errorf("%w: stale global ordinal", ErrRuntimeIntervalInvalid)
			}
			if input.Telemetry != nil {
				observation := *input.Telemetry
				observation.Ordinal = existing.Ordinal
				matching, err := sessionTelemetryMatchesTx(ctx, tx, &observation, false)
				if err != nil {
					return err
				}
				conflicting := existing.ModelStepClassification != nil && *existing.ModelStepClassification == ModelStepConflictingDuplicate
				if matching == conflicting {
					return ErrRuntimeEventConflict
				}
			}
			event = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count uint64
		if err := tx.QueryRowContext(ctx, `SELECT event_count FROM runtime_event_intervals WHERE id = ?`, input.IntervalID).Scan(&count); err != nil {
			return err
		}
		if count >= maxRuntimeEventOrdinal {
			return fmt.Errorf("%w: global ordinal overflow", ErrRuntimeIntervalInvalid)
		}
		if input.AfterOrdinal != nil && *input.AfterOrdinal != count {
			return fmt.Errorf("%w: stale global ordinal", ErrRuntimeIntervalInvalid)
		}
		next := count + 1
		event = RuntimeEvent{IntervalID: input.IntervalID, Ordinal: next, EpochID: input.EpochID, EpochOrdinal: input.EpochOrdinal, Kind: input.Kind, EventType: input.EventType, SourceSessionID: input.SourceSessionID, MessageID: input.MessageID, PartID: input.PartID, CapturedAt: input.CapturedAt}
		contradictionDelta := 0
		if input.Telemetry != nil {
			observation := *input.Telemetry
			observation.Ordinal = next
			telemetryInserted, err := insertSessionTelemetryTx(ctx, tx, input.SessionID, input.CapturedAt, &observation)
			if err != nil {
				return err
			}
			classification := ModelStepAccepted
			if !telemetryInserted {
				matching, err := sessionTelemetryMatchesTx(ctx, tx, &observation, false)
				if err != nil {
					return err
				}
				classification = ModelStepExactDuplicate
				if !matching {
					classification = ModelStepConflictingDuplicate
					contradictionDelta = 1
				}
			}
			event.ModelStepClassification = &classification
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_events (interval_id, ordinal, epoch_id, epoch_ordinal, kind, event_type, source_session_id, message_id, part_id, captured_at, model_step_classification) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.IntervalID, event.Ordinal, event.EpochID, event.EpochOrdinal, event.Kind, event.EventType, event.SourceSessionID, event.MessageID, event.PartID, event.CapturedAt, event.ModelStepClassification); err != nil {
			return err
		}
		epochDelta := 0
		if input.Kind == "collector_open" {
			epochDelta = 1
		}
		result, err := tx.ExecContext(ctx, `UPDATE runtime_event_intervals SET first_ordinal = COALESCE(first_ordinal, 1), last_ordinal = ?, event_count = ?, collector_epoch_count = collector_epoch_count + ?, contradiction_count = contradiction_count + ? WHERE id = ? AND event_count = ?`, next, next, epochDelta, contradictionDelta, input.IntervalID, count)
		if err != nil {
			return err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return fmt.Errorf("%w: global ordinal allocation conflict", ErrRuntimeIntervalInvalid)
		}
		inserted = true
		return nil
	})
	if err != nil {
		return RuntimeEvent{}, false, fmt.Errorf("db: persist runtime event: %w", err)
	}
	return event, inserted, nil
}

func validateRuntimeEventInput(input RuntimeEventInput) error {
	for name, value := range map[string]string{"interval_id": input.IntervalID, "session_id": input.SessionID, "epoch_id": input.EpochID, "kind": input.Kind, "event_type": input.EventType, "captured_at": input.CapturedAt} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s is blank", ErrRuntimeIntervalInvalid, name)
		}
	}
	validKind := map[string]bool{"collector_open": true, "model_step": true, "retry": true, "compaction": true, "interruption": true, "other": true}
	if !validKind[input.Kind] || (input.Kind == "collector_open") != (input.EpochOrdinal == 0) {
		return fmt.Errorf("%w: kind/epoch ordinal mismatch", ErrRuntimeIntervalInvalid)
	}
	if input.AfterOrdinal != nil && *input.AfterOrdinal > maxRuntimeEventOrdinal {
		return fmt.Errorf("%w: global ordinal is not a safe JSON integer", ErrRuntimeIntervalInvalid)
	}
	if input.Kind == "model_step" {
		if input.SourceSessionID == nil || input.MessageID == nil || input.PartID == nil || strings.TrimSpace(*input.SourceSessionID) == "" || strings.TrimSpace(*input.MessageID) == "" || strings.TrimSpace(*input.PartID) == "" || input.Telemetry == nil {
			return fmt.Errorf("%w: model step identity/telemetry missing", ErrRuntimeIntervalInvalid)
		}
		if err := telemetry.ValidateObservation(input.Telemetry); err != nil {
			return err
		}
	}
	if input.Telemetry != nil && input.Kind != "model_step" {
		return fmt.Errorf("%w: telemetry belongs only to model steps", ErrRuntimeIntervalInvalid)
	}
	return nil
}

func runtimeEventMatches(event RuntimeEvent, input RuntimeEventInput) bool {
	return event.IntervalID == input.IntervalID && event.EpochID == input.EpochID && event.EpochOrdinal == input.EpochOrdinal && event.Kind == input.Kind && event.EventType == input.EventType && equalStringPointer(event.SourceSessionID, input.SourceSessionID) && equalStringPointer(event.MessageID, input.MessageID) && equalStringPointer(event.PartID, input.PartID)
}

func equalStringPointer(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (d *DB) SealRuntimeEventInterval(id, sessionID, closedAt, state, reason string) error {
	if state != RuntimeComplete && state != RuntimeIncomplete && state != RuntimeContradictory {
		return fmt.Errorf("%w: invalid close state", ErrRuntimeIntervalInvalid)
	}
	ctx, cancel := writeContext()
	defer cancel()
	return withWriteTx(ctx, d.db, func(tx *sql.Tx) error {
		interval, err := loadRuntimeEventInterval(ctx, tx, id)
		if err != nil {
			return err
		}
		if interval.SessionID != sessionID || interval.CloseState != RuntimeOpen {
			return fmt.Errorf("%w: interval owner/state mismatch", ErrRuntimeIntervalInvalid)
		}
		validationErr := validateLoadedRuntimeInterval(interval)
		if state == RuntimeComplete && validationErr != nil {
			return validationErr
		}
		if state == RuntimeContradictory && interval.ContradictionCount == 0 {
			return fmt.Errorf("%w: interval has no retained contradiction", ErrRuntimeIntervalInvalid)
		}
		var failure any
		if reason != "" {
			failure = reason
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runtime_event_intervals SET closed_at = ?, close_state = ?, failure_reason = ? WHERE id = ?`, closedAt, state, failure, id); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE sessions SET ended_at = ?, last_seen_at = ? WHERE id = ? AND ended_at IS NULL`, closedAt, closedAt, sessionID)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return fmt.Errorf("%w: owner close failed", ErrRuntimeIntervalInvalid)
		}
		return nil
	})
}

func (d *DB) LoadRuntimeEventInterval(id string) (*RuntimeEventInterval, error) {
	interval, err := loadRuntimeEventInterval(context.Background(), d.db, id)
	if err != nil {
		return nil, fmt.Errorf("db: load runtime event interval: %w", err)
	}
	return interval, nil
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadRuntimeEventInterval(ctx context.Context, q rowQueryer, id string) (*RuntimeEventInterval, error) {
	interval := &RuntimeEventInterval{}
	var closed, failure sql.NullString
	var first, last sql.NullInt64
	var eventCount, epochCount, contradictionCount int64
	err := q.QueryRowContext(ctx, `SELECT id, session_id, attempt_id, runtime, runtime_version, source, contract_version, opened_at, closed_at, close_state, first_ordinal, last_ordinal, event_count, collector_epoch_count, contradiction_count, failure_reason FROM runtime_event_intervals WHERE id = ?`, id).Scan(&interval.ID, &interval.SessionID, &interval.AttemptID, &interval.Runtime, &interval.RuntimeVersion, &interval.Source, &interval.ContractVersion, &interval.OpenedAt, &closed, &interval.CloseState, &first, &last, &eventCount, &epochCount, &contradictionCount, &failure)
	if err != nil {
		return nil, err
	}
	if closed.Valid {
		interval.ClosedAt = &closed.String
	}
	if failure.Valid {
		interval.FailureReason = &failure.String
	}
	if first.Valid {
		value := uint64(first.Int64)
		interval.FirstOrdinal = &value
	}
	if last.Valid {
		value := uint64(last.Int64)
		interval.LastOrdinal = &value
	}
	interval.EventCount, interval.CollectorEpochCount, interval.ContradictionCount = uint64(eventCount), uint64(epochCount), uint64(contradictionCount)
	rows, err := q.QueryContext(ctx, `SELECT interval_id, ordinal, epoch_id, epoch_ordinal, kind, event_type, source_session_id, message_id, part_id, captured_at, model_step_classification FROM runtime_events WHERE interval_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var event RuntimeEvent
		if err := scanRuntimeEvent(rows, &event); err != nil {
			return nil, err
		}
		interval.Events = append(interval.Events, event)
	}
	return interval, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanRuntimeEvent(row scanner, event *RuntimeEvent) error {
	var ordinal, epochOrdinal int64
	var source, message, part, classification sql.NullString
	if err := row.Scan(&event.IntervalID, &ordinal, &event.EpochID, &epochOrdinal, &event.Kind, &event.EventType, &source, &message, &part, &event.CapturedAt, &classification); err != nil {
		return err
	}
	event.Ordinal, event.EpochOrdinal = uint64(ordinal), uint64(epochOrdinal)
	if source.Valid {
		event.SourceSessionID = &source.String
	}
	if message.Valid {
		event.MessageID = &message.String
	}
	if part.Valid {
		event.PartID = &part.String
	}
	if classification.Valid {
		event.ModelStepClassification = &classification.String
	}
	return nil
}

func ValidateRuntimeEventInterval(interval *RuntimeEventInterval) error {
	return validateLoadedRuntimeInterval(interval)
}

func validateLoadedRuntimeInterval(interval *RuntimeEventInterval) error {
	if interval == nil || interval.EventCount != uint64(len(interval.Events)) || interval.CollectorEpochCount == 0 || interval.EventCount == 0 {
		return fmt.Errorf("%w: missing events or collector epoch", ErrRuntimeIntervalInvalid)
	}
	if interval.ContradictionCount != 0 {
		return fmt.Errorf("%w: retained contradictory model-step callback", ErrRuntimeIntervalInvalid)
	}
	epochNext := map[string]uint64{}
	activeEpoch := ""
	for index, event := range interval.Events {
		if event.Ordinal != uint64(index+1) {
			return fmt.Errorf("%w: global ordinal gap", ErrRuntimeIntervalInvalid)
		}
		next, seen := epochNext[event.EpochID]
		if !seen {
			next = 0
		}
		if event.EpochOrdinal != next {
			return fmt.Errorf("%w: epoch ordinal gap", ErrRuntimeIntervalInvalid)
		}
		if event.Kind == "collector_open" {
			activeEpoch = event.EpochID
		} else if event.EpochID != activeEpoch {
			return fmt.Errorf("%w: collector epochs overlap", ErrRuntimeIntervalInvalid)
		}
		if event.Kind == "model_step" {
			if event.ModelStepClassification == nil || (*event.ModelStepClassification != ModelStepAccepted && *event.ModelStepClassification != ModelStepExactDuplicate) {
				return fmt.Errorf("%w: invalid model-step classification", ErrRuntimeIntervalInvalid)
			}
		} else if event.ModelStepClassification != nil {
			return fmt.Errorf("%w: non-model event has model-step classification", ErrRuntimeIntervalInvalid)
		}
		epochNext[event.EpochID] = next + 1
	}
	if uint64(len(epochNext)) != interval.CollectorEpochCount {
		return fmt.Errorf("%w: collector epoch count mismatch", ErrRuntimeIntervalInvalid)
	}
	return nil
}
