// Package telemetry defines normalized runtime telemetry observations.
package telemetry

import (
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

// Tokens contains normalized model-step token measurements.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// Observation is one normalized model-step observation. Runtime identity is
// assigned by the trusted ingestion boundary, not decoded from user input.
type Observation struct {
	Key
	Ordinal        uint64  `json:"ordinal"`
	Reason         string  `json:"reason"`
	Cost           float64 `json:"cost"`
	Tokens         Tokens  `json:"tokens"`
	Provider       *string `json:"provider"`
	Model          *string `json:"model"`
	Runtime        string  `json:"runtime"`
	RuntimeVersion string  `json:"runtime_version"`
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
	} {
		if strings.TrimSpace(field.value) == "" {
			return invalid(field.name + " must be a non-blank string")
		}
	}
	if observation.Ordinal == 0 || observation.Ordinal > math.MaxInt64 {
		return invalid("ordinal must be a positive signed 64-bit integer")
	}
	if observation.Cost < 0 || math.IsInf(observation.Cost, 0) || math.IsNaN(observation.Cost) {
		return invalid("cost must be a non-negative number")
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"tokens.input", observation.Tokens.Input},
		{"tokens.output", observation.Tokens.Output},
		{"tokens.reasoning", observation.Tokens.Reasoning},
		{"tokens.cache_read", observation.Tokens.CacheRead},
		{"tokens.cache_write", observation.Tokens.CacheWrite},
	} {
		if field.value < 0 || field.value >= 1<<53 {
			return invalid(field.name + " must be a non-negative safe integer")
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

func invalid(detail string) error { return fmt.Errorf("%w: %s", ErrInvalid, detail) }
