package sequence

import (
	"math/rand"
	"reflect"
	"sort"
	"sync"
	"testing"
)

// fullScanBoundaries retains the original copy/sort/filter traversal as an
// independent oracle for interval selection and order-sensitive boundary rules.
func fullScanBoundaries(result *PairCoverage, start, end uint64, includeStart bool, sessionID string, events []SequenceFact) {
	sorted := append([]SequenceFact(nil), events...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Ordinal != sorted[j].Ordinal {
			return sorted[i].Ordinal < sorted[j].Ordinal
		}
		if sorted[i].Boundary != sorted[j].Boundary {
			return sorted[i].Boundary < sorted[j].Boundary
		}
		return sorted[i].SessionID < sorted[j].SessionID
	})
	for _, event := range sorted {
		if event.Ordinal >= end || event.Ordinal < start || (!includeStart && event.Ordinal == start) {
			continue
		}
		concurrent := event.SessionID != "" && event.SessionID != sessionID
		if !usableEventProvenance(event) {
			switch event.Boundary {
			case BoundaryRetry:
				result.RetryCoverage = "unavailable"
			case BoundaryCompaction:
				result.ResetCoverage = "unavailable"
			case BoundaryInterruption, BoundaryRestart:
				result.SegmentCoverage = CoverageContradictory
			}
			if concurrent {
				result.ConcurrentCoverage = CoverageContradictory
			}
			continue
		}
		switch event.Boundary {
		case BoundaryRetry:
			if !matchesProvenance(event.Provenance, result.Provenance["retry"]) {
				result.RetryCoverage = "unavailable"
			} else if result.RetryCoverage == "covered_no_boundary" {
				result.RetryCoverage = "boundary_observed"
			}
		case BoundaryCompaction:
			if !matchesProvenance(event.Provenance, result.Provenance["reset"]) {
				result.ResetCoverage = "unavailable"
			} else if result.ResetCoverage == "covered_no_boundary" {
				result.ResetCoverage = "boundary_observed"
			}
		case BoundaryInterruption, BoundaryRestart:
			if !matchesProvenance(event.Provenance, result.Provenance["segment"]) {
				result.SegmentCoverage = CoverageContradictory
			} else if result.SegmentCoverage == CoverageCovered && result.SegmentBoundary == "none" {
				result.SegmentBoundary = string(event.Boundary)
			}
		}
		if concurrent && result.ConcurrentCoverage == CoverageCovered {
			if matchesProvenance(event.Provenance, result.Provenance["concurrency"]) {
				result.Concurrent = true
			} else {
				result.ConcurrentCoverage = CoverageContradictory
			}
		}
	}
}

func TestEventIndexMatchesFullScan(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	var events []SequenceFact
	for ordinal := uint64(0); ordinal < 32; ordinal++ {
		for _, boundary := range []BoundaryKind{"", "unknown", BoundaryRetry, BoundaryCompaction, BoundaryInterruption, BoundaryRestart} {
			for _, session := range []string{"", "session-a", "session-b"} {
				for variant := range 3 {
					fact := boundaryFact(ordinal, session, boundary)
					switch variant {
					case 1:
						fact.Provenance = SequenceProvenance{}
					case 2:
						fact.Provenance.Source = "wrapper_stream"
					}
					events = append(events, fact)
				}
			}
		}
	}
	rng.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
	before := append([]SequenceFact(nil), events...)
	index := NewEventIndex(events)
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for start := uint64(1 + worker); start < 24; start += 8 {
				for end := start + 1; end <= 33; end++ {
					prior, current := modelFact(start, 1, "session-a"), modelFact(end, 2, "session-a")
					for _, coverage := range [][]CoverageFact{coveredFacts(1, 33), nil, withoutFact(coveredFacts(1, 33), "segment")} {
						want, err := AssessPair(prior, current, nil, coverage)
						if err != nil {
							t.Error(err)
							return
						}
						fullScanBoundaries(&want, start, end, false, prior.SessionID, events)
						got, err := index.AssessPair(prior, current, coverage)
						if err != nil || !reflect.DeepEqual(got, want) {
							t.Errorf("pair (%d,%d): got %#v want %#v error %v", start, end, got, want, err)
						}
					}
					coverage := coveredFacts(1, 33)
					want, err := AssessAttemptBoundaries(prior, current, nil, coverage)
					if err != nil {
						t.Error(err)
						return
					}
					fullScanBoundaries(&want, 1, end, true, prior.SessionID, events)
					got, err := index.AssessAttemptBoundaries(prior, current, coverage)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Errorf("attempt [%d,%d): got %#v want %#v error %v", 1, end, got, want, err)
					}
				}
			}
		})
	}
	workers.Wait()
	if !reflect.DeepEqual(events, before) {
		t.Fatal("index mutated input")
	}
	// The index owns its snapshot even if the caller later reuses its slice.
	for i := range events {
		events[i] = SequenceFact{}
	}
	if !reflect.DeepEqual(index, NewEventIndex(before)) {
		t.Fatal("index retained caller-owned storage")
	}
}
