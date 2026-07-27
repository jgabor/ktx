package reuse

import (
	"reflect"
	"sync"
	"testing"
)

func TestSequenceBoundaryEligibilityInputs(t *testing.T) {
	prior := modelFact(10, 1, "session-a")
	current := modelFact(20, 2, "session-a")
	covered := coveredFacts(10, 20)

	tests := []struct {
		name     string
		current  SequenceFact
		events   []SequenceFact
		coverage []CoverageFact
		check    func(*testing.T, PairCoverage)
	}{
		{"all classes covered", current, nil, covered, func(t *testing.T, got PairCoverage) {
			if got.OrderCoverage != CoverageCovered || !got.Adjacent || got.RetryCoverage != "covered_no_boundary" || got.ResetCoverage != "covered_no_boundary" || got.SegmentCoverage != CoverageCovered || got.ConcurrentCoverage != CoverageCovered || got.Concurrent || got.IdentityCoverage != CoverageCovered || !got.SameSession || len(got.Provenance) != 6 {
				t.Fatalf("covered pair = %#v", got)
			}
		}},
		{"order unavailable when fact is late", current, nil, replaceFact(covered, "order", CoverageFact{Class: "order", State: CoverageCovered, Provenance: provenance(11, 20)}), func(t *testing.T, got PairCoverage) {
			if got.OrderCoverage != CoverageUnavailable || got.Adjacent {
				t.Fatalf("order = %#v", got)
			}
		}},
		{"order contradictory", current, nil, append(covered, CoverageFact{Class: "order", State: CoverageCovered, Provenance: SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "wrapper_stream", StartOrdinal: 10, EndOrdinal: 20}}), func(t *testing.T, got PairCoverage) {
			if got.OrderCoverage != CoverageContradictory {
				t.Fatalf("order = %#v", got)
			}
		}},
		{"retry boundary", current, []SequenceFact{boundaryFact(15, "session-a", BoundaryRetry)}, covered, func(t *testing.T, got PairCoverage) {
			if got.RetryCoverage != "boundary_observed" {
				t.Fatalf("retry = %#v", got)
			}
		}},
		{"retry unavailable", current, nil, withoutFact(covered, "retry"), func(t *testing.T, got PairCoverage) {
			if got.RetryCoverage != "unavailable" {
				t.Fatalf("retry = %#v", got)
			}
		}},
		{"compaction boundary", current, []SequenceFact{boundaryFact(15, "session-a", BoundaryCompaction)}, covered, func(t *testing.T, got PairCoverage) {
			if got.ResetCoverage != "boundary_observed" {
				t.Fatalf("reset = %#v", got)
			}
		}},
		{"reset unavailable", current, nil, withoutFact(covered, "reset"), func(t *testing.T, got PairCoverage) {
			if got.ResetCoverage != "unavailable" {
				t.Fatalf("reset = %#v", got)
			}
		}},
		{"identity changed", modelFact(20, 2, "session-b"), nil, covered, func(t *testing.T, got PairCoverage) {
			if got.IdentityCoverage != CoverageCovered || got.SameSession {
				t.Fatalf("identity = %#v", got)
			}
		}},
		{"identity unavailable", current, nil, withoutFact(covered, "identity"), func(t *testing.T, got PairCoverage) {
			if got.IdentityCoverage != CoverageUnavailable || got.SameSession {
				t.Fatalf("identity = %#v", got)
			}
		}},
		{"interruption boundary", current, []SequenceFact{boundaryFact(15, "session-a", BoundaryInterruption)}, covered, func(t *testing.T, got PairCoverage) {
			if got.SegmentBoundary != "interruption" {
				t.Fatalf("segment = %#v", got)
			}
		}},
		{"restart boundary", current, []SequenceFact{boundaryFact(15, "", BoundaryRestart)}, covered, func(t *testing.T, got PairCoverage) {
			if got.SegmentBoundary != "restart" {
				t.Fatalf("segment = %#v", got)
			}
		}},
		{"segment unavailable", current, nil, withoutFact(covered, "segment"), func(t *testing.T, got PairCoverage) {
			if got.SegmentCoverage != CoverageUnavailable {
				t.Fatalf("segment = %#v", got)
			}
		}},
		{"concurrent interleaving", current, []SequenceFact{boundaryFact(15, "session-b", "")}, covered, func(t *testing.T, got PairCoverage) {
			if !got.Concurrent {
				t.Fatalf("concurrency = %#v", got)
			}
		}},
		{"concurrency unavailable", current, nil, withoutFact(covered, "concurrency"), func(t *testing.T, got PairCoverage) {
			if got.ConcurrentCoverage != CoverageUnavailable || got.Concurrent {
				t.Fatalf("concurrency = %#v", got)
			}
		}},
		{"boundary provenance contradictory", current, []SequenceFact{{Ordinal: 15, SessionID: "session-a", Boundary: BoundaryInterruption, Provenance: SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "wrapper_stream", StartOrdinal: 15, EndOrdinal: 15}}}, covered, func(t *testing.T, got PairCoverage) {
			if got.SegmentCoverage != CoverageContradictory {
				t.Fatalf("segment = %#v", got)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := AssessPair(prior, test.current, test.events, test.coverage)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, got)
		})
	}
}

func TestSequenceCoverageDeterministicUnderConcurrency(t *testing.T) {
	prior := modelFact(10, 1, "session-a")
	current := modelFact(20, 2, "session-a")
	events := []SequenceFact{boundaryFact(18, "session-b", ""), boundaryFact(12, "session-a", BoundaryRetry)}
	want, err := AssessPair(prior, current, events, coveredFacts(10, 20))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, assessErr := AssessPair(prior, current, []SequenceFact{events[1], events[0]}, coveredFacts(10, 20))
			if assessErr != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("got %#v, %v; want %#v", got, assessErr, want)
			}
		}()
	}
	wg.Wait()
}

func TestAttemptBoundaryAssessmentIncludesCoveredPrefix(t *testing.T) {
	step := modelFact(10, 1, "session-a")
	coverage := coveredFacts(1, 10)
	tests := []struct {
		name   string
		event  SequenceFact
		assert func(*testing.T, PairCoverage)
	}{
		{"clean", SequenceFact{}, func(t *testing.T, got PairCoverage) {
			if got.RetryCoverage != "covered_no_boundary" || got.ResetCoverage != "covered_no_boundary" || got.SegmentCoverage != CoverageCovered || got.ConcurrentCoverage != CoverageCovered || got.Concurrent {
				t.Fatalf("clean prefix = %#v", got)
			}
		}},
		{"retry", boundaryFact(5, "session-a", BoundaryRetry), func(t *testing.T, got PairCoverage) {
			if got.RetryCoverage != "boundary_observed" {
				t.Fatalf("retry prefix = %#v", got)
			}
		}},
		{"compaction", boundaryFact(5, "session-a", BoundaryCompaction), func(t *testing.T, got PairCoverage) {
			if got.ResetCoverage != "boundary_observed" {
				t.Fatalf("compaction prefix = %#v", got)
			}
		}},
		{"interruption", boundaryFact(5, "session-a", BoundaryInterruption), func(t *testing.T, got PairCoverage) {
			if got.SegmentBoundary != "interruption" {
				t.Fatalf("interruption prefix = %#v", got)
			}
		}},
		{"concurrent", boundaryFact(5, "session-b", ""), func(t *testing.T, got PairCoverage) {
			if !got.Concurrent {
				t.Fatalf("concurrent prefix = %#v", got)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []SequenceFact(nil)
			if test.event.Ordinal != 0 {
				events = append(events, test.event)
			}
			got, err := AssessAttemptBoundaries(step, step, events, coverage)
			if err != nil {
				t.Fatal(err)
			}
			test.assert(t, got)
		})
	}
}

func TestAttemptBoundaryAssessmentRejectsConflictingCoverageIntervals(t *testing.T) {
	step := modelFact(10, 1, "session-a")
	coverage := coveredFacts(1, 10)
	for i := range coverage {
		if coverage[i].Class == "segment" {
			coverage[i].Provenance.StartOrdinal = 2
		}
	}
	got, err := AssessAttemptBoundaries(step, step, nil, coverage)
	if err != nil {
		t.Fatal(err)
	}
	if got.RetryCoverage != "unavailable" || got.ResetCoverage != "unavailable" || got.SegmentCoverage != CoverageContradictory || got.ConcurrentCoverage != CoverageContradictory {
		t.Fatalf("conflicting intervals = %#v", got)
	}
}

func TestEveryCoverageClassMustMatchPairProvenance(t *testing.T) {
	prior := modelFact(10, 1, "session-a")
	current := modelFact(20, 2, "session-a")
	for _, mismatch := range []struct {
		name   string
		mutate func(*SequenceProvenance)
	}{
		{"runtime", func(p *SequenceProvenance) { p.Runtime = "other-runtime" }},
		{"runtime version", func(p *SequenceProvenance) { p.RuntimeVersion = "other-version" }},
		{"collection source", func(p *SequenceProvenance) { p.Source = "wrapper_stream" }},
	} {
		for _, class := range []string{"order", "retry", "reset", "segment", "concurrency", "identity"} {
			t.Run(mismatch.name+"/"+class, func(t *testing.T) {
				fact := CoverageFact{Class: class, State: CoverageCovered, Provenance: provenance(10, 20)}
				mismatch.mutate(&fact.Provenance)
				got, err := AssessPair(prior, current, nil, replaceFact(coveredFacts(10, 20), class, fact))
				if err != nil {
					t.Fatal(err)
				}
				switch class {
				case "order":
					if got.OrderCoverage != CoverageContradictory || got.Adjacent {
						t.Fatalf("order = %#v", got)
					}
				case "retry":
					if got.RetryCoverage != "unavailable" {
						t.Fatalf("retry = %#v", got)
					}
				case "reset":
					if got.ResetCoverage != "unavailable" {
						t.Fatalf("reset = %#v", got)
					}
				case "segment":
					if got.SegmentCoverage != CoverageContradictory {
						t.Fatalf("segment = %#v", got)
					}
				case "concurrency":
					if got.ConcurrentCoverage != CoverageContradictory {
						t.Fatalf("concurrency = %#v", got)
					}
				case "identity":
					if got.IdentityCoverage != CoverageContradictory || got.SameSession {
						t.Fatalf("identity = %#v", got)
					}
				}
			})
		}
	}
}

func TestMalformedBoundaryCannotBecomeSilence(t *testing.T) {
	prior := modelFact(10, 1, "session-a")
	current := modelFact(20, 2, "session-a")
	tests := []struct {
		name  string
		event SequenceFact
		check func(*testing.T, PairCoverage)
	}{
		{"retry", SequenceFact{Ordinal: 15, SessionID: "session-a", Boundary: BoundaryRetry}, func(t *testing.T, got PairCoverage) {
			if got.RetryCoverage != "unavailable" {
				t.Fatalf("retry = %#v", got)
			}
		}},
		{"compaction", SequenceFact{Ordinal: 15, SessionID: "session-a", Boundary: BoundaryCompaction, Provenance: SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: 15}}, func(t *testing.T, got PairCoverage) {
			if got.ResetCoverage != "unavailable" {
				t.Fatalf("reset = %#v", got)
			}
		}},
		{"interruption", SequenceFact{Ordinal: 15, SessionID: "session-a", Boundary: BoundaryInterruption, Provenance: SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", StartOrdinal: 15, EndOrdinal: 15}}, func(t *testing.T, got PairCoverage) {
			if got.SegmentCoverage != CoverageContradictory {
				t.Fatalf("segment = %#v", got)
			}
		}},
		{"restart", SequenceFact{Ordinal: 15, Boundary: BoundaryRestart, Provenance: SequenceProvenance{RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: 15, EndOrdinal: 15}}, func(t *testing.T, got PairCoverage) {
			if got.SegmentCoverage != CoverageContradictory {
				t.Fatalf("segment = %#v", got)
			}
		}},
		{"concurrent session", SequenceFact{Ordinal: 15, SessionID: "session-b"}, func(t *testing.T, got PairCoverage) {
			if got.ConcurrentCoverage != CoverageContradictory || got.Concurrent {
				t.Fatalf("concurrency = %#v", got)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := AssessPair(prior, current, []SequenceFact{test.event}, coveredFacts(10, 20))
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, got)
		})
	}
}

func TestCrossSourcePositiveEventsCannotCertifyPair(t *testing.T) {
	prior := modelFact(10, 1, "session-a")
	current := modelFact(20, 2, "session-a")
	foreign := func(boundary BoundaryKind, session string) SequenceFact {
		return SequenceFact{Ordinal: 15, SessionID: session, Boundary: boundary, Provenance: SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "wrapper_stream", StartOrdinal: 15, EndOrdinal: 15}}
	}
	tests := []struct {
		name  string
		event SequenceFact
		check func(*testing.T, PairCoverage)
	}{
		{"retry", foreign(BoundaryRetry, "session-a"), func(t *testing.T, got PairCoverage) {
			if got.RetryCoverage != "unavailable" {
				t.Fatalf("retry = %#v", got)
			}
		}},
		{"compaction", foreign(BoundaryCompaction, "session-a"), func(t *testing.T, got PairCoverage) {
			if got.ResetCoverage != "unavailable" {
				t.Fatalf("reset = %#v", got)
			}
		}},
		{"interruption", foreign(BoundaryInterruption, "session-a"), func(t *testing.T, got PairCoverage) {
			if got.SegmentCoverage != CoverageContradictory || got.SegmentBoundary != "none" {
				t.Fatalf("segment = %#v", got)
			}
		}},
		{"concurrent session", foreign("", "session-b"), func(t *testing.T, got PairCoverage) {
			if got.ConcurrentCoverage != CoverageContradictory || got.Concurrent {
				t.Fatalf("concurrency = %#v", got)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := AssessPair(prior, current, []SequenceFact{test.event}, coveredFacts(10, 20))
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, got)
		})
	}
}

func TestDecodePinnedOpenCodeSequenceEvents(t *testing.T) {
	tests := []struct {
		name, source, payload string
		wantBoundary          BoundaryKind
		wantModel             uint64
		want                  bool
	}{
		{"hook model step", "event_hook", `{"type":"message.part.updated","properties":{"part":{"type":"step-finish","sessionID":"session-a"}}}`, "", 1, true},
		{"hook retry", "event_hook", `{"type":"message.part.updated","properties":{"part":{"type":"retry","sessionID":"session-a"}}}`, BoundaryRetry, 0, true},
		{"hook retry status", "event_hook", `{"type":"session.status","properties":{"sessionID":"session-a","status":{"type":"retry","attempt":1}}}`, BoundaryRetry, 0, true},
		{"hook compaction", "event_hook", `{"type":"message.part.updated","properties":{"part":{"type":"compaction","sessionID":"session-a"}}}`, BoundaryCompaction, 0, true},
		{"hook compacted", "event_hook", `{"type":"session.compacted","properties":{"sessionID":"session-a"}}`, BoundaryCompaction, 0, true},
		{"hook interruption", "event_hook", `{"type":"session.error","properties":{"sessionID":"session-a","error":{"name":"UnknownError"}}}`, BoundaryInterruption, 0, true},
		{"wrapper model step", "wrapper_stream", `{"type":"step_finish","sessionID":"session-a","part":{"type":"step-finish","sessionID":"session-a"}}`, "", 1, true},
		{"wrapper error", "wrapper_stream", `{"type":"error","sessionID":"session-a"}`, BoundaryInterruption, 0, true},
		{"wrapper retry filtered", "wrapper_stream", `{"type":"retry","sessionID":"session-a","part":{"type":"retry","sessionID":"session-a"}}`, "", 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := DecodeOpenCodeSequenceFact([]byte(test.payload), test.source, 7, test.wantModel)
			if ok != test.want || ok && (got.Boundary != test.wantBoundary || got.ModelStepOrdinal != test.wantModel || got.Provenance.Source != test.source || got.Provenance.StartOrdinal != 7) {
				t.Fatalf("fact = %#v, %v", got, ok)
			}
		})
	}
}

func coveredFacts(start, end uint64) []CoverageFact {
	facts := make([]CoverageFact, 0, 6)
	for _, class := range []string{"order", "retry", "reset", "segment", "concurrency", "identity"} {
		facts = append(facts, CoverageFact{Class: class, State: CoverageCovered, Provenance: provenance(start, end)})
	}
	return facts
}

func provenance(start, end uint64) SequenceProvenance {
	return SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: start, EndOrdinal: end}
}

func boundaryFact(ordinal uint64, session string, boundary BoundaryKind) SequenceFact {
	return SequenceFact{Ordinal: ordinal, SessionID: session, Boundary: boundary, Provenance: provenance(ordinal, ordinal)}
}

func modelFact(ordinal, modelOrdinal uint64, session string) SequenceFact {
	return SequenceFact{Ordinal: ordinal, ModelStepOrdinal: modelOrdinal, SessionID: session, Provenance: provenance(ordinal, ordinal)}
}

func withoutFact(facts []CoverageFact, class string) []CoverageFact {
	result := append([]CoverageFact(nil), facts...)
	for i := range result {
		if result[i].Class == class {
			return append(result[:i], result[i+1:]...)
		}
	}
	return result
}

func replaceFact(facts []CoverageFact, class string, replacement CoverageFact) []CoverageFact {
	return append(withoutFact(facts, class), replacement)
}
