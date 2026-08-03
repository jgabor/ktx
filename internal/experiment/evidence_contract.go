package experiment

import (
	"fmt"
	"math"
	"strings"
)

// SourceEventKind identifies what emitted a source event. A model step is not
// a native request unless the source provides a provider request identifier or
// a documented one-to-one mapping.
type SourceEventKind string

const (
	ModelStep     SourceEventKind = "model_step"
	NativeRequest SourceEventKind = "native_request"
)

// MeasurementState preserves the distinction between a reported value,
// unavailable telemetry, and malformed telemetry. Measured zero is a value.
type MeasurementState string

const (
	MeasurementMeasured  MeasurementState = "measured"
	MeasurementUnknown   MeasurementState = "unknown"
	MeasurementMalformed MeasurementState = "malformed"
)

// IntegerMeasurement is one token measurement from one source event.
type IntegerMeasurement struct {
	State MeasurementState `json:"state"`
	Value *int64           `json:"value"`
}

func MeasuredInteger(value int64) IntegerMeasurement {
	return IntegerMeasurement{State: MeasurementMeasured, Value: &value}
}

func UnknownInteger() IntegerMeasurement {
	return IntegerMeasurement{State: MeasurementUnknown}
}

func MalformedInteger() IntegerMeasurement {
	return IntegerMeasurement{State: MeasurementMalformed}
}

// CostMeasurement is one reported cost measurement from one source event.
type CostMeasurement struct {
	State MeasurementState `json:"state"`
	Value *float64         `json:"value"`
}

func MeasuredCost(value float64) CostMeasurement {
	return CostMeasurement{State: MeasurementMeasured, Value: &value}
}

// EventIdentity is identity metadata reported by the source event. Native
// request identity requires either ProviderRequestID or OneToOneMapping.
type EventIdentity struct {
	Provider          *string            `json:"provider,omitempty"`
	Runtime           *string            `json:"runtime,omitempty"`
	Model             *string            `json:"model,omitempty"`
	ResponseID        *string            `json:"response_id,omitempty"`
	RequestID         *string            `json:"request_id,omitempty"`
	SessionID         *string            `json:"session_id,omitempty"`
	MessageID         *string            `json:"message_id,omitempty"`
	PartID            *string            `json:"part_id,omitempty"`
	ProviderRequestID *string            `json:"provider_request_id,omitempty"`
	OneToOneMapping   *EvidenceReference `json:"one_to_one_mapping,omitempty"`
}

// EventControl is applicable cache-control metadata reported for one source
// event. It deliberately retains only descriptive control metadata, not raw
// cache namespaces or credentials.
type EventControl struct {
	Mode            *string `json:"mode,omitempty"`
	NamespaceHash   *string `json:"namespace_hash,omitempty"`
	Breakpoint      *string `json:"breakpoint,omitempty"`
	Eligibility     *string `json:"eligibility,omitempty"`
	Availability    *string `json:"availability,omitempty"`
	MaxOutputTokens *int64  `json:"max_output_tokens,omitempty"`
}

// RetryMetadata is retry data when the source reports it. Its absence does
// not establish that an event was not retried.
type RetryMetadata struct {
	Attempt                    *int    `json:"attempt,omitempty"`
	MaxAttempts                *int    `json:"max_attempts,omitempty"`
	Availability               *string `json:"availability,omitempty"`
	PreviousSourceEventOrdinal *uint64 `json:"previous_source_event_ordinal,omitempty"`
	ProviderRequestID          *string `json:"provider_request_id,omitempty"`
}

// SourceEvent is one ordered observation from a runtime or provider source.
// Every measurement belongs to this event; attempt totals may be reported only
// after the relevant field is measured on every source event.
type SourceEvent struct {
	Ordinal       uint64             `json:"ordinal"`
	Kind          SourceEventKind    `json:"kind"`
	UncachedInput IntegerMeasurement `json:"uncached_input"`
	Output        IntegerMeasurement `json:"output"`
	CacheRead     IntegerMeasurement `json:"cache_read"`
	CacheWrite    IntegerMeasurement `json:"cache_write"`
	Reasoning     IntegerMeasurement `json:"reasoning"`
	Cost          CostMeasurement    `json:"cost"`
	Identity      EventIdentity      `json:"identity"`
	Control       EventControl       `json:"control"`
	Retry         RetryMetadata      `json:"retry"`
}

// ValidateSourceEvents verifies the ordered source-event contract without
// inferring missing telemetry or native request identities.
func ValidateSourceEvents(events []SourceEvent) error {
	for i, event := range events {
		if event.Ordinal != uint64(i+1) {
			return fmt.Errorf("source event %d has ordinal %d, want %d", i, event.Ordinal, i+1)
		}
		if event.Kind != ModelStep && event.Kind != NativeRequest {
			return fmt.Errorf("source event %d has invalid kind %q", event.Ordinal, event.Kind)
		}
		if event.Kind == NativeRequest && !event.Identity.hasNativeRequestIdentity() {
			return fmt.Errorf("native request %d lacks provider request identity or one-to-one mapping", event.Ordinal)
		}
		if err := event.Identity.validate(); err != nil {
			return fmt.Errorf("source event %d: %w", event.Ordinal, err)
		}
		if err := event.Control.validate(); err != nil {
			return fmt.Errorf("source event %d: %w", event.Ordinal, err)
		}
		if err := event.Retry.validate(event.Ordinal); err != nil {
			return fmt.Errorf("source event %d: %w", event.Ordinal, err)
		}
		for _, measurement := range []IntegerMeasurement{event.UncachedInput, event.Output, event.CacheRead, event.CacheWrite, event.Reasoning} {
			if err := measurement.validate(); err != nil {
				return fmt.Errorf("source event %d: %w", event.Ordinal, err)
			}
		}
		if err := event.Cost.validate(); err != nil {
			return fmt.Errorf("source event %d: %w", event.Ordinal, err)
		}
	}
	return nil
}

func (i EventIdentity) hasNativeRequestIdentity() bool {
	return (i.ProviderRequestID != nil && strings.TrimSpace(*i.ProviderRequestID) != "") ||
		(i.OneToOneMapping != nil && strings.TrimSpace(i.OneToOneMapping.Citation) != "")
}

func (i EventIdentity) validate() error {
	for name, value := range map[string]*string{
		"provider": i.Provider, "runtime": i.Runtime, "model": i.Model,
		"response ID": i.ResponseID, "request ID": i.RequestID, "session ID": i.SessionID,
		"message ID": i.MessageID, "part ID": i.PartID, "provider request ID": i.ProviderRequestID,
	} {
		if err := validateOptionalText(name, value); err != nil {
			return err
		}
	}
	if i.OneToOneMapping != nil && strings.TrimSpace(i.OneToOneMapping.Citation) == "" {
		return fmt.Errorf("one-to-one mapping requires a citation")
	}
	return nil
}

func (c EventControl) validate() error {
	for name, value := range map[string]*string{
		"control mode": c.Mode, "namespace hash": c.NamespaceHash, "breakpoint": c.Breakpoint,
		"eligibility": c.Eligibility, "control availability": c.Availability,
	} {
		if err := validateOptionalText(name, value); err != nil {
			return err
		}
	}
	if c.MaxOutputTokens != nil && *c.MaxOutputTokens <= 0 {
		return fmt.Errorf("maximum output tokens must be positive")
	}
	return nil
}

func (r RetryMetadata) validate(ordinal uint64) error {
	if r.Attempt != nil && *r.Attempt < 0 {
		return fmt.Errorf("retry attempt must not be negative")
	}
	if r.MaxAttempts != nil && *r.MaxAttempts <= 0 {
		return fmt.Errorf("retry maximum attempts must be positive")
	}
	if r.Attempt != nil && r.MaxAttempts != nil && *r.Attempt > *r.MaxAttempts {
		return fmt.Errorf("retry attempt exceeds maximum attempts")
	}
	if r.PreviousSourceEventOrdinal != nil && (*r.PreviousSourceEventOrdinal == 0 || *r.PreviousSourceEventOrdinal >= ordinal) {
		return fmt.Errorf("previous source event ordinal must precede the event")
	}
	if err := validateOptionalText("retry availability", r.Availability); err != nil {
		return err
	}
	return validateOptionalText("retry provider request ID", r.ProviderRequestID)
}

func validateOptionalText(name string, value *string) error {
	if value != nil && strings.TrimSpace(*value) == "" {
		return fmt.Errorf("%s must not be blank", name)
	}
	return nil
}

func (m IntegerMeasurement) validate() error {
	if m.State == MeasurementMeasured && m.Value != nil {
		return nil
	}
	if (m.State == MeasurementUnknown || m.State == MeasurementMalformed) && m.Value == nil {
		return nil
	}
	return fmt.Errorf("invalid integer measurement state %q", m.State)
}

func (m CostMeasurement) validate() error {
	if m.State == MeasurementMeasured && m.Value != nil && !math.IsInf(*m.Value, 0) && !math.IsNaN(*m.Value) {
		return nil
	}
	if (m.State == MeasurementUnknown || m.State == MeasurementMalformed) && m.Value == nil {
		return nil
	}
	return fmt.Errorf("invalid cost measurement state %q", m.State)
}

// NativeRequestCount counts only source events proven to be native requests.
func NativeRequestCount(events []SourceEvent) int {
	count := 0
	for _, event := range events {
		if event.Kind == NativeRequest && event.Identity.hasNativeRequestIdentity() {
			count++
		}
	}
	return count
}

// EvidenceReference cites documentation for a one-to-one source mapping.
type EvidenceReference struct {
	Citation string `json:"citation,omitempty"`
}
