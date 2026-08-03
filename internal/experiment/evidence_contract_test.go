package experiment

import (
	"math"
	"testing"
)

func TestSourceEventContract(t *testing.T) {
	zero := int64(0)
	one := int64(1)
	two := uint64(2)
	requestID := "req-1"
	mode := "isolated"
	eligibility := "eligible"
	namespaceHash := "hash"
	retryAttempt := 1
	events := []SourceEvent{
		{
			Ordinal:       1,
			Kind:          ModelStep,
			UncachedInput: MeasuredInteger(zero),
			Output:        MeasuredInteger(zero),
			CacheRead:     UnknownInteger(),
			CacheWrite:    MalformedInteger(),
			Reasoning:     MeasuredInteger(one),
			Cost:          MeasuredCost(0),
		},
		{
			Ordinal:       2,
			Kind:          NativeRequest,
			UncachedInput: MeasuredInteger(one),
			Output:        MeasuredInteger(one),
			CacheRead:     MeasuredInteger(zero),
			CacheWrite:    MeasuredInteger(zero),
			Reasoning:     MeasuredInteger(zero),
			Cost:          MeasuredCost(0.01),
			Identity:      EventIdentity{ProviderRequestID: &requestID},
			Control:       EventControl{Mode: &mode, NamespaceHash: &namespaceHash, Eligibility: &eligibility},
			Retry: RetryMetadata{
				Attempt:                    &retryAttempt,
				PreviousSourceEventOrdinal: &oneAsUint64,
				ProviderRequestID:          &requestID,
			},
		},
	}

	if err := ValidateSourceEvents(events); err != nil {
		t.Fatalf("ValidateSourceEvents() error = %v", err)
	}
	if got := NativeRequestCount(events); got != 1 {
		t.Errorf("NativeRequestCount() = %d, want 1; model steps are not native requests", got)
	}
	if events[0].UncachedInput.State != MeasurementMeasured || events[0].UncachedInput.Value == nil || *events[0].UncachedInput.Value != 0 {
		t.Errorf("zero measurement = %#v, want measured zero", events[0].UncachedInput)
	}
	if events[0].CacheRead.State != MeasurementUnknown || events[0].CacheRead.Value != nil {
		t.Errorf("unknown measurement = %#v, want unknown with no value", events[0].CacheRead)
	}
	if events[0].CacheWrite.State != MeasurementMalformed || events[0].CacheWrite.Value != nil {
		t.Errorf("malformed measurement = %#v, want malformed with no value", events[0].CacheWrite)
	}

	events[1].Identity = EventIdentity{}
	if err := ValidateSourceEvents(events); err == nil {
		t.Fatal("native request without provider identity or mapping was accepted")
	}
	if got := NativeRequestCount(events); got != 0 {
		t.Errorf("NativeRequestCount() = %d, want 0 for an unproven native-request label", got)
	}
	events[1].Identity = EventIdentity{ProviderRequestID: &requestID}
	mapping := EvidenceReference{Citation: "https://example.test/mapping"}
	events[1].Identity = EventIdentity{OneToOneMapping: &mapping}
	if err := ValidateSourceEvents(events); err != nil {
		t.Fatalf("native request with a cited one-to-one mapping rejected: %v", err)
	}
	events[1].Identity = EventIdentity{ProviderRequestID: &requestID}
	events[1].Retry.PreviousSourceEventOrdinal = &two
	if err := ValidateSourceEvents(events); err == nil {
		t.Fatal("retry pointing at its own event was accepted")
	}
	events[1].Retry.PreviousSourceEventOrdinal = &oneAsUint64
	blank := " "
	events[1].Control.Mode = &blank
	if err := ValidateSourceEvents(events); err == nil {
		t.Fatal("blank control metadata was accepted")
	}
	events[1].Control.Mode = &mode
	events[0].Cost = MeasuredCost(math.Inf(1))
	if err := ValidateSourceEvents(events); err == nil {
		t.Fatal("non-finite cost was accepted")
	}
}

var oneAsUint64 uint64 = 1
