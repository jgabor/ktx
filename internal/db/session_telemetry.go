package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jgabor/ktx/internal/telemetry"
)

const (
	MeasurementMeasured = "measured"
	maxTelemetryList    = 1000
)

var ErrSessionTelemetrySourceNotFound = fmt.Errorf("db: session telemetry source not found: %w", ErrNotFound)

type IntegerMeasurement struct {
	State string `json:"state"`
	Value *int64 `json:"value"`
}

type CostMeasurement struct {
	State string   `json:"state"`
	Value *float64 `json:"value"`
}

// SessionTelemetry is one accepted model-step observation associated with a
// ktx session. Provider and model remain nil when correlation was not
// available; accepted numeric fields are always measured, including zero.
type SessionTelemetry struct {
	SessionID       string             `json:"session_id"`
	SourceSessionID string             `json:"source_session_id"`
	MessageID       string             `json:"message_id"`
	PartID          string             `json:"part_id"`
	Ordinal         uint64             `json:"ordinal"`
	Reason          string             `json:"reason"`
	UncachedInput   IntegerMeasurement `json:"uncached_input"`
	Output          IntegerMeasurement `json:"output"`
	Reasoning       IntegerMeasurement `json:"reasoning"`
	CacheRead       IntegerMeasurement `json:"cache_read"`
	CacheWrite      IntegerMeasurement `json:"cache_write"`
	Cost            CostMeasurement    `json:"cost"`
	Provider        *string            `json:"provider"`
	Model           *string            `json:"model"`
	Runtime         string             `json:"runtime"`
	RuntimeVersion  string             `json:"runtime_version"`
	CapturedAt      string             `json:"captured_at"`
}

// PersistSessionTelemetry inserts one accepted observation. Runtime plus the
// source tuple is the idempotency key; the first accepted occurrence remains authoritative.
// The session check and insert share one transaction, and the foreign key is
// the final association guard.
func (d *DB) PersistSessionTelemetry(sessionID, capturedAt string, observation *telemetry.Observation) (bool, error) {
	if err := telemetry.ValidateObservation(observation); err != nil {
		return false, err
	}
	if sessionID == "" || capturedAt == "" {
		return false, errors.New("db: session telemetry requires session_id and captured_at")
	}
	ctx, cancel := writeContext()
	defer cancel()
	inserted := false
	err := withWriteTx(ctx, d.db, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSessionTelemetrySourceNotFound
			}
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO session_telemetry (
			session_id, source_session_id, message_id, part_id, ordinal, reason, cost,
			tokens_input, tokens_output, tokens_reasoning, cache_read, cache_write,
			provider, model, runtime, runtime_version, captured_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(runtime, source_session_id, message_id, part_id) DO NOTHING`,
			sessionID, observation.SessionID, observation.MessageID, observation.PartID,
			observation.Ordinal, observation.Reason, observation.Cost,
			observation.Tokens.Input, observation.Tokens.Output, observation.Tokens.Reasoning,
			observation.Tokens.CacheRead, observation.Tokens.CacheWrite,
			observation.Provider, observation.Model, observation.Runtime, observation.RuntimeVersion, capturedAt,
		)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		inserted = rows == 1
		return err
	})
	if err != nil {
		return false, fmt.Errorf("db: persist session telemetry: %w", err)
	}
	return inserted, nil
}

// ListSessionTelemetry returns at most limit observations in local acceptance
// order. Source identity is the deterministic tie-breaker for equal ordinals.
func (d *DB) ListSessionTelemetry(sessionID string, limit int) ([]SessionTelemetry, error) {
	if sessionID == "" {
		return nil, errors.New("db: list session telemetry requires session_id")
	}
	if limit < 1 || limit > maxTelemetryList {
		return nil, fmt.Errorf("db: session telemetry limit must be between 1 and %d", maxTelemetryList)
	}
	rows, err := d.db.QueryContext(context.Background(), `SELECT
		session_id, source_session_id, message_id, part_id, ordinal, reason, cost,
		tokens_input, tokens_output, tokens_reasoning, cache_read, cache_write,
		provider, model, runtime, runtime_version, captured_at
		FROM session_telemetry WHERE session_id = ?
		ORDER BY ordinal, runtime, source_session_id, message_id, part_id LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("db: list session telemetry: %w", err)
	}
	defer rows.Close()
	result := make([]SessionTelemetry, 0)
	for rows.Next() {
		var row SessionTelemetry
		var ordinal int64
		var input, output, reasoning, cacheRead, cacheWrite int64
		var cost float64
		var provider, model sql.NullString
		if err := rows.Scan(
			&row.SessionID, &row.SourceSessionID, &row.MessageID, &row.PartID, &ordinal,
			&row.Reason, &cost, &input, &output, &reasoning, &cacheRead, &cacheWrite,
			&provider, &model, &row.Runtime, &row.RuntimeVersion, &row.CapturedAt,
		); err != nil {
			return nil, fmt.Errorf("db: scan session telemetry: %w", err)
		}
		row.Ordinal = uint64(ordinal)
		row.UncachedInput = measuredInteger(input)
		row.Output = measuredInteger(output)
		row.Reasoning = measuredInteger(reasoning)
		row.CacheRead = measuredInteger(cacheRead)
		row.CacheWrite = measuredInteger(cacheWrite)
		row.Cost = CostMeasurement{State: MeasurementMeasured, Value: &cost}
		if provider.Valid {
			row.Provider = &provider.String
		}
		if model.Valid {
			row.Model = &model.String
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate session telemetry: %w", err)
	}
	return result, nil
}

func measuredInteger(value int64) IntegerMeasurement {
	return IntegerMeasurement{State: MeasurementMeasured, Value: &value}
}
