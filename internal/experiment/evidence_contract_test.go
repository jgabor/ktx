package experiment

import "testing"

func TestSourceEventContract(t *testing.T) {
	zero := int64(0)
	one := int64(1)
	requestID := "req-1"
	events := []SourceEvent{
		{
			Ordinal:       1,
			Kind:          ModelStep,
			UncachedInput: MeasuredInteger(zero),
			CacheRead:     UnknownInteger(),
			CacheWrite:    MalformedInteger(),
			Reasoning:     MeasuredInteger(one),
			Cost:          MeasuredCost(0),
		},
		{
			Ordinal:       2,
			Kind:          NativeRequest,
			UncachedInput: MeasuredInteger(one),
			CacheRead:     MeasuredInteger(zero),
			CacheWrite:    MeasuredInteger(zero),
			Reasoning:     MeasuredInteger(zero),
			Cost:          MeasuredCost(0.01),
			Identity:      EventIdentity{ProviderRequestID: &requestID},
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
	empty := ""
	events[1].Identity = EventIdentity{ProviderRequestID: &empty}
	if err := ValidateSourceEvents(events); err == nil {
		t.Fatal("native request with an empty provider identity was accepted")
	}
	ordered := append([]SourceEvent(nil), events...)
	ordered[1].Identity = EventIdentity{ProviderRequestID: &requestID}
	ordered[1].Ordinal = 3
	if err := ValidateSourceEvents(ordered); err == nil {
		t.Fatal("out-of-order source events were accepted")
	}
	malformed := events[0]
	malformed.CacheWrite.Value = &zero
	if err := ValidateSourceEvents([]SourceEvent{malformed}); err == nil {
		t.Fatal("malformed telemetry with a measurement was accepted")
	}
}

func TestSourceEventContractRejectsInvalidClaimsAndCapabilities(t *testing.T) {
	if err := (CacheClaimEvidence{Claim: ProviderInternalMiss}).Validate(); err == nil {
		t.Fatal("provider-internal miss without a controlled eligible lookup was accepted")
	}
	if err := (CacheClaimEvidence{Claim: ProviderInternalMiss, ControlledEligibleLookup: true}).Validate(); err != nil {
		t.Fatalf("provider-internal miss with a controlled eligible lookup rejected: %v", err)
	}

	valid := CapabilityEntry{
		Source:                    "opencode",
		Version:                   "1.18.0",
		AuthoritativeSource:       CapabilityEvidence{State: CapabilitySupported, References: []EvidenceReference{{Citation: "https://example.test/source"}}},
		RuntimeNormalization:      CapabilityEvidence{State: CapabilityUnknown, References: []EvidenceReference{{Artifact: "fixtures/runtime.json"}}},
		LiveObservation:           CapabilityEvidence{State: CapabilityUnknown, References: []EvidenceReference{{Artifact: "fixtures/native.json"}}},
		ControlValidatedIsolation: CapabilityEvidence{State: CapabilityUnsupported, References: []EvidenceReference{{Citation: "https://example.test/control"}}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid capability entry rejected: %v", err)
	}
	valid.LiveObservation.References = nil
	if err := valid.Validate(); err == nil {
		t.Fatal("capability entry without a live-observation reference was accepted")
	}
}
