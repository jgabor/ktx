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
	ProviderRequestID *string            `json:"provider_request_id,omitempty"`
	OneToOneMapping   *EvidenceReference `json:"one_to_one_mapping,omitempty"`
}

// EventControl is applicable cache-control metadata reported for one source
// event. It deliberately retains only descriptive control metadata, not raw
// cache namespaces or credentials.
type EventControl struct {
	Mode          *string `json:"mode,omitempty"`
	NamespaceHash *string `json:"namespace_hash,omitempty"`
	Breakpoint    *string `json:"breakpoint,omitempty"`
	Eligibility   *string `json:"eligibility,omitempty"`
}

// RetryMetadata is retry data when the source reports it. Its absence does
// not establish that an event was not retried.
type RetryMetadata struct {
	Attempt                    *int    `json:"attempt,omitempty"`
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
		"provider": i.Provider, "runtime": i.Runtime, "model": i.Model, "provider request ID": i.ProviderRequestID,
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
		"control mode": c.Mode, "namespace hash": c.NamespaceHash, "breakpoint": c.Breakpoint, "eligibility": c.Eligibility,
	} {
		if err := validateOptionalText(name, value); err != nil {
			return err
		}
	}
	return nil
}

func (r RetryMetadata) validate(ordinal uint64) error {
	if r.Attempt != nil && *r.Attempt < 0 {
		return fmt.Errorf("retry attempt must not be negative")
	}
	if r.PreviousSourceEventOrdinal != nil && (*r.PreviousSourceEventOrdinal == 0 || *r.PreviousSourceEventOrdinal >= ordinal) {
		return fmt.Errorf("previous source event ordinal must precede the event")
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

// CacheClaim is the claim that evidence can support. Cache reuse and a cache
// fill are observations, not provider-internal hit or miss conclusions.
type CacheClaim string

const (
	CacheReuse           CacheClaim = "cache_reuse"
	CacheFill            CacheClaim = "cache_fill"
	CacheIneligible      CacheClaim = "ineligible"
	CacheNoReportedReuse CacheClaim = "no_reported_reuse"
	CacheUnknown         CacheClaim = "unknown"
	ProviderInternalMiss CacheClaim = "provider_internal_miss"
)

// CacheClaimEvidence describes the evidence boundary for a cache claim.
type CacheClaimEvidence struct {
	Claim            CacheClaim         `json:"claim"`
	EligibleLookup   bool               `json:"eligible_lookup"`
	ControlledLookup bool               `json:"controlled_lookup"`
	NoReuseEvidence  *EvidenceReference `json:"no_reuse_evidence,omitempty"`
}

// Validate enforces the boundary between reported reuse and an internal miss.
func (e CacheClaimEvidence) Validate() error {
	if e.ControlledLookup && !e.EligibleLookup {
		return fmt.Errorf("controlled lookup requires an eligible lookup")
	}
	if e.NoReuseEvidence != nil && !e.NoReuseEvidence.valid() {
		return fmt.Errorf("no-reuse evidence requires a citation or retained artifact")
	}
	switch e.Claim {
	case CacheReuse, CacheFill:
		if e.NoReuseEvidence != nil {
			return fmt.Errorf("%s contradicts no-reuse evidence", e.Claim)
		}
		return nil
	case CacheIneligible:
		if e.EligibleLookup || e.ControlledLookup || e.NoReuseEvidence != nil {
			return fmt.Errorf("ineligible claim contradicts lookup evidence")
		}
		return nil
	case CacheNoReportedReuse:
		if !e.EligibleLookup {
			return fmt.Errorf("no reported reuse requires an eligible lookup")
		}
		return nil
	case CacheUnknown:
		if e.NoReuseEvidence != nil {
			return fmt.Errorf("unknown claim contradicts no-reuse evidence")
		}
		return nil
	case ProviderInternalMiss:
		if e.EligibleLookup && e.ControlledLookup && e.NoReuseEvidence != nil {
			return nil
		}
		return fmt.Errorf("provider-internal miss requires controlled eligible lookup and no-reuse evidence")
	default:
		return fmt.Errorf("invalid cache claim %q", e.Claim)
	}
}

// CapabilityState reports what an evidence level establishes.
type CapabilityState string

const (
	CapabilitySupported   CapabilityState = "supported"
	CapabilityUnsupported CapabilityState = "unsupported"
	CapabilityUnknown     CapabilityState = "unknown"
)

// EvidenceReference points to either a source citation or a retained artifact.
type EvidenceReference struct {
	Citation string `json:"citation,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

// CapabilityEvidence is one independently reviewed evidence level.
type CapabilityEvidence struct {
	State      CapabilityState     `json:"state"`
	References []EvidenceReference `json:"references"`
}

// CapabilityEntry keeps source documentation, runtime normalization, live
// observation, and isolation controls separate rather than letting one level
// imply another.
type CapabilityEntry struct {
	Source                    string             `json:"source"`
	Version                   string             `json:"version"`
	AuthoritativeSource       CapabilityEvidence `json:"authoritative_source"`
	RuntimeNormalization      CapabilityEvidence `json:"runtime_normalization"`
	LiveObservation           CapabilityEvidence `json:"live_observation"`
	ControlValidatedIsolation CapabilityEvidence `json:"control_validated_isolation"`
}

// Validate requires a source/version identity and a citation or artifact for
// every independently stated evidence level.
func (e *CapabilityEntry) Validate() error {
	e.Source = strings.TrimSpace(e.Source)
	e.Version = strings.TrimSpace(e.Version)
	if e.Source == "" || e.Version == "" {
		return fmt.Errorf("capability entry requires source and version")
	}
	for _, level := range []*CapabilityEvidence{&e.AuthoritativeSource, &e.RuntimeNormalization, &e.LiveObservation, &e.ControlValidatedIsolation} {
		if err := level.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (e *CapabilityEvidence) validate() error {
	if e.State != CapabilitySupported && e.State != CapabilityUnsupported && e.State != CapabilityUnknown {
		return fmt.Errorf("invalid capability state %q", e.State)
	}
	if len(e.References) == 0 {
		return fmt.Errorf("capability evidence requires a citation or retained artifact")
	}
	for i := range e.References {
		if !e.References[i].normalizeAndValidate() {
			return fmt.Errorf("capability evidence requires a citation or retained artifact")
		}
	}
	return nil
}

func (r *EvidenceReference) normalizeAndValidate() bool {
	r.Citation = strings.TrimSpace(r.Citation)
	r.Artifact = strings.TrimSpace(r.Artifact)
	return r.Citation != "" || r.Artifact != ""
}

func (r EvidenceReference) valid() bool {
	return strings.TrimSpace(r.Citation) != "" || strings.TrimSpace(r.Artifact) != ""
}
