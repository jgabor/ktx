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

func TestCacheClaimEvidenceBoundaries(t *testing.T) {
	noReuse := &EvidenceReference{Artifact: "fixtures/no-reuse.json"}
	for _, evidence := range []CacheClaimEvidence{
		{Claim: CacheReuse},
		{Claim: CacheFill},
		{Claim: CacheIneligible},
		{Claim: CacheNoReportedReuse, EligibleLookup: true},
		{Claim: CacheUnknown},
		{Claim: ProviderInternalMiss, EligibleLookup: true, ControlledLookup: true, NoReuseEvidence: noReuse},
	} {
		if err := evidence.Validate(); err != nil {
			t.Errorf("valid %q claim rejected: %v", evidence.Claim, err)
		}
	}

	for _, evidence := range []CacheClaimEvidence{
		{Claim: CacheNoReportedReuse},
		{Claim: ProviderInternalMiss, EligibleLookup: true, ControlledLookup: true},
		{Claim: ProviderInternalMiss, EligibleLookup: true, NoReuseEvidence: noReuse},
		{Claim: CacheIneligible, EligibleLookup: true},
		{Claim: CacheReuse, NoReuseEvidence: noReuse},
		{Claim: CacheUnknown, NoReuseEvidence: noReuse},
		{Claim: CacheFill, ControlledLookup: true},
		{Claim: CacheNoReportedReuse, EligibleLookup: true, NoReuseEvidence: &EvidenceReference{Citation: " "}},
	} {
		if err := evidence.Validate(); err == nil {
			t.Errorf("invalid %q claim was accepted: %#v", evidence.Claim, evidence)
		}
	}
}

func TestCapabilityEntryNormalizesAndRequiresReferences(t *testing.T) {
	valid := CapabilityEntry{
		Source:                    " opencode ",
		Version:                   " 1.18.0 ",
		AuthoritativeSource:       CapabilityEvidence{State: CapabilitySupported, References: []EvidenceReference{{Citation: " https://example.test/source "}}},
		RuntimeNormalization:      CapabilityEvidence{State: CapabilityUnknown, References: []EvidenceReference{{Artifact: " fixtures/runtime.json "}}},
		LiveObservation:           CapabilityEvidence{State: CapabilityUnknown, References: []EvidenceReference{{Artifact: " fixtures/native.json "}}},
		ControlValidatedIsolation: CapabilityEvidence{State: CapabilityUnsupported, References: []EvidenceReference{{Citation: " https://example.test/control "}}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid capability entry rejected: %v", err)
	}
	if valid.Source != "opencode" || valid.Version != "1.18.0" || valid.AuthoritativeSource.References[0].Citation != "https://example.test/source" || valid.RuntimeNormalization.References[0].Artifact != "fixtures/runtime.json" {
		t.Errorf("Validate() did not normalize whitespace: %#v", valid)
	}

	for _, mutate := range []func(*CapabilityEntry){
		func(e *CapabilityEntry) { e.Source = " " },
		func(e *CapabilityEntry) { e.Version = " " },
		func(e *CapabilityEntry) { e.LiveObservation.References[0].Artifact = " " },
		func(e *CapabilityEntry) { e.AuthoritativeSource.References[0].Citation = " " },
	} {
		entry := valid
		entry.AuthoritativeSource.References = append([]EvidenceReference(nil), valid.AuthoritativeSource.References...)
		entry.RuntimeNormalization.References = append([]EvidenceReference(nil), valid.RuntimeNormalization.References...)
		entry.LiveObservation.References = append([]EvidenceReference(nil), valid.LiveObservation.References...)
		entry.ControlValidatedIsolation.References = append([]EvidenceReference(nil), valid.ControlValidatedIsolation.References...)
		mutate(&entry)
		if err := entry.Validate(); err == nil {
			t.Fatal("capability entry with blank-after-trim identity or reference was accepted")
		}
	}
}
