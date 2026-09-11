// Package telemetry defines normalized runtime telemetry observations.
package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

var ErrInvalid = errors.New("invalid telemetry observation")

// Key identifies one model-step observation in its source runtime.
type Key struct {
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	PartID    string `json:"part_id"`
}

type MeasurementState string

const (
	MeasurementMeasured  MeasurementState = "measured"
	MeasurementUnknown   MeasurementState = "unknown"
	MeasurementMalformed MeasurementState = "malformed"

	SourceEventHook     = "event_hook"
	SourceWrapperStream = "wrapper_stream"
)

type IntegerMeasurement struct {
	State MeasurementState `json:"state"`
	Value *int64           `json:"value"`
}

type CostMeasurement struct {
	State MeasurementState `json:"state"`
	Value *float64         `json:"value"`
}

// Tokens contains normalized model-step token measurements.
type Tokens struct {
	Input      IntegerMeasurement `json:"input"`
	Output     IntegerMeasurement `json:"output"`
	Reasoning  IntegerMeasurement `json:"reasoning"`
	CacheRead  IntegerMeasurement `json:"cache_read"`
	CacheWrite IntegerMeasurement `json:"cache_write"`
}

// Observation is one normalized model-step observation. Runtime identity is
// assigned by the trusted ingestion boundary, not decoded from user input.
type Observation struct {
	Key
	Ordinal        uint64          `json:"ordinal"`
	Reason         string          `json:"reason"`
	Cost           CostMeasurement `json:"cost"`
	Tokens         Tokens          `json:"tokens"`
	Provider       *string         `json:"provider"`
	Model          *string         `json:"model"`
	Runtime        string          `json:"runtime"`
	RuntimeVersion string          `json:"runtime_version"`
	Source         string          `json:"source"`
}

// ValidateObservation checks a normalized observation before persistence.
func ValidateObservation(observation *Observation) error {
	if observation == nil {
		return invalid("missing observation")
	}
	for _, field := range []struct{ name, value string }{
		{"session_id", observation.SessionID},
		{"message_id", observation.MessageID},
		{"part_id", observation.PartID},
		{"reason", observation.Reason},
		{"runtime", observation.Runtime},
		{"runtime_version", observation.RuntimeVersion},
		{"source", observation.Source},
	} {
		if strings.TrimSpace(field.value) == "" {
			return invalid(field.name + " must be a non-blank string")
		}
	}
	if observation.Ordinal == 0 || observation.Ordinal > math.MaxInt64 {
		return invalid("ordinal must be a positive signed 64-bit integer")
	}
	if observation.Source != SourceEventHook && observation.Source != SourceWrapperStream {
		return invalid("source must identify a supported collection path")
	}
	if err := validateCostMeasurement("cost", observation.Cost); err != nil {
		return err
	}
	for _, field := range []struct {
		name        string
		measurement IntegerMeasurement
	}{
		{"tokens.input", observation.Tokens.Input},
		{"tokens.output", observation.Tokens.Output},
		{"tokens.reasoning", observation.Tokens.Reasoning},
		{"tokens.cache_read", observation.Tokens.CacheRead},
		{"tokens.cache_write", observation.Tokens.CacheWrite},
	} {
		if err := validateIntegerMeasurement(field.name, field.measurement); err != nil {
			return err
		}
	}
	if (observation.Provider == nil) != (observation.Model == nil) {
		return invalid("provider and model must both be available or both be absent")
	}
	if observation.Provider != nil && (strings.TrimSpace(*observation.Provider) == "" || strings.TrimSpace(*observation.Model) == "") {
		return invalid("provider and model must be non-blank when available")
	}
	return nil
}

func DecodeIntegerMeasurement(raw json.RawMessage, containerState MeasurementState) IntegerMeasurement {
	if containerState != MeasurementMeasured {
		return IntegerMeasurement{State: containerState}
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return IntegerMeasurement{State: MeasurementUnknown}
	}
	// Match the runtime's JSON number semantics: numeric strings are not
	// measurements, while integral decimal/exponent forms are safe integers.
	var number float64
	if json.Unmarshal(raw, &number) != nil || number < 0 || number >= 1<<53 || math.Trunc(number) != number {
		return IntegerMeasurement{State: MeasurementMalformed}
	}
	value := int64(number)
	return IntegerMeasurement{State: MeasurementMeasured, Value: &value}
}

func DecodeCostMeasurement(raw json.RawMessage, containerState MeasurementState) CostMeasurement {
	if containerState != MeasurementMeasured {
		return CostMeasurement{State: containerState}
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return CostMeasurement{State: MeasurementUnknown}
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil || value < 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return CostMeasurement{State: MeasurementMalformed}
	}
	return CostMeasurement{State: MeasurementMeasured, Value: &value}
}

func validateIntegerMeasurement(name string, measurement IntegerMeasurement) error {
	if measurement.State == MeasurementMeasured {
		if measurement.Value == nil || *measurement.Value < 0 || *measurement.Value >= 1<<53 {
			return invalid(name + " measured value must be a non-negative safe integer")
		}
		return nil
	}
	if (measurement.State != MeasurementUnknown && measurement.State != MeasurementMalformed) || measurement.Value != nil {
		return invalid(name + " has an invalid measurement state/value")
	}
	return nil
}

func validateCostMeasurement(name string, measurement CostMeasurement) error {
	if measurement.State == MeasurementMeasured {
		if measurement.Value == nil || *measurement.Value < 0 || math.IsInf(*measurement.Value, 0) || math.IsNaN(*measurement.Value) {
			return invalid(name + " measured value must be a non-negative number")
		}
		return nil
	}
	if (measurement.State != MeasurementUnknown && measurement.State != MeasurementMalformed) || measurement.Value != nil {
		return invalid(name + " has an invalid measurement state/value")
	}
	return nil
}

func invalid(detail string) error { return fmt.Errorf("%w: %s", ErrInvalid, detail) }
