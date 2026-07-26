package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jgabor/ktx/internal/telemetry"
)

const (
	MeasurementMeasured  = "measured"
	MeasurementUnknown   = "unknown"
	MeasurementMalformed = "malformed"
	maxTelemetryList     = 1000
)

var (
	ErrSessionTelemetrySourceNotFound = fmt.Errorf("db: session telemetry source not found: %w", ErrNotFound)
	ErrSessionTelemetryConflict       = errors.New("db: conflicting session telemetry evidence")
)

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
// available. Numeric fields retain measured, unknown, and malformed states.
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
	Source          string             `json:"source"`
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
			provider, model, runtime, runtime_version, source, captured_at,
			cost_state, tokens_input_state, tokens_output_state, tokens_reasoning_state,
			cache_read_state, cache_write_state
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(runtime, source_session_id, message_id, part_id) DO NOTHING`,
			sessionID, observation.SessionID, observation.MessageID, observation.PartID,
			observation.Ordinal, observation.Reason, observation.Cost.Value,
			observation.Tokens.Input.Value, observation.Tokens.Output.Value, observation.Tokens.Reasoning.Value,
			observation.Tokens.CacheRead.Value, observation.Tokens.CacheWrite.Value,
			observation.Provider, observation.Model, observation.Runtime, observation.RuntimeVersion, observation.Source, capturedAt,
			observation.Cost.State, observation.Tokens.Input.State, observation.Tokens.Output.State,
			observation.Tokens.Reasoning.State, observation.Tokens.CacheRead.State, observation.Tokens.CacheWrite.State,
		)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		inserted = rows == 1
		if err != nil || inserted {
			return err
		}
		var matching int
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM session_telemetry WHERE
			runtime = ? AND source_session_id = ? AND message_id = ? AND part_id = ? AND
			ordinal IS ? AND reason IS ? AND cost IS ? AND tokens_input IS ? AND tokens_output IS ? AND
			tokens_reasoning IS ? AND cache_read IS ? AND cache_write IS ? AND provider IS ? AND model IS ? AND
			runtime_version IS ? AND cost_state IS ? AND tokens_input_state IS ? AND tokens_output_state IS ? AND
			tokens_reasoning_state IS ? AND cache_read_state IS ? AND cache_write_state IS ?)`,
			observation.Runtime, observation.SessionID, observation.MessageID, observation.PartID,
			observation.Ordinal, observation.Reason, observation.Cost.Value,
			observation.Tokens.Input.Value, observation.Tokens.Output.Value, observation.Tokens.Reasoning.Value,
			observation.Tokens.CacheRead.Value, observation.Tokens.CacheWrite.Value,
			observation.Provider, observation.Model, observation.RuntimeVersion, observation.Cost.State,
			observation.Tokens.Input.State, observation.Tokens.Output.State, observation.Tokens.Reasoning.State,
			observation.Tokens.CacheRead.State, observation.Tokens.CacheWrite.State,
		).Scan(&matching)
		if err != nil {
			return err
		}
		if matching == 0 {
			return ErrSessionTelemetryConflict
		}
		return nil
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
		provider, model, runtime, runtime_version, source, captured_at,
		cost_state, tokens_input_state, tokens_output_state, tokens_reasoning_state,
		cache_read_state, cache_write_state
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
		var input, output, reasoning, cacheRead, cacheWrite sql.NullInt64
		var cost sql.NullFloat64
		var costState, inputState, outputState, reasoningState, cacheReadState, cacheWriteState string
		var provider, model sql.NullString
		if err := rows.Scan(
			&row.SessionID, &row.SourceSessionID, &row.MessageID, &row.PartID, &ordinal,
			&row.Reason, &cost, &input, &output, &reasoning, &cacheRead, &cacheWrite,
			&provider, &model, &row.Runtime, &row.RuntimeVersion, &row.Source, &row.CapturedAt,
			&costState, &inputState, &outputState, &reasoningState, &cacheReadState, &cacheWriteState,
		); err != nil {
			return nil, fmt.Errorf("db: scan session telemetry: %w", err)
		}
		row.Ordinal = uint64(ordinal)
		row.UncachedInput = databaseIntegerMeasurement(inputState, input)
		row.Output = databaseIntegerMeasurement(outputState, output)
		row.Reasoning = databaseIntegerMeasurement(reasoningState, reasoning)
		row.CacheRead = databaseIntegerMeasurement(cacheReadState, cacheRead)
		row.CacheWrite = databaseIntegerMeasurement(cacheWriteState, cacheWrite)
		row.Cost = databaseCostMeasurement(costState, cost)
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

func databaseIntegerMeasurement(state string, value sql.NullInt64) IntegerMeasurement {
	measurement := IntegerMeasurement{State: state}
	if value.Valid {
		measurement.Value = &value.Int64
	}
	return measurement
}

func databaseCostMeasurement(state string, value sql.NullFloat64) CostMeasurement {
	measurement := CostMeasurement{State: state}
	if value.Valid {
		measurement.Value = &value.Float64
	}
	return measurement
}
