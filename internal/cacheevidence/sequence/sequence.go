package sequence

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type CoverageState string

const (
	CoverageCovered       CoverageState = "covered"
	CoverageUnavailable   CoverageState = "unavailable"
	CoverageContradictory CoverageState = "contradictory"
)

type BoundaryKind string

const (
	BoundaryRetry        BoundaryKind = "retry"
	BoundaryCompaction   BoundaryKind = "compaction"
	BoundaryInterruption BoundaryKind = "interruption"
	BoundaryRestart      BoundaryKind = "restart"
)

type SequenceProvenance struct {
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`
	Source         string `json:"source"`
	StartOrdinal   uint64 `json:"start_ordinal"`
	EndOrdinal     uint64 `json:"end_ordinal"`
}

type SequenceFact struct {
	Ordinal          uint64
	ModelStepOrdinal uint64
	SessionID        string
	Boundary         BoundaryKind
	Provenance       SequenceProvenance
}

type CoverageFact struct {
	Class      string
	State      CoverageState
	Provenance SequenceProvenance
}

type PairCoverage struct {
	OrderCoverage      CoverageState
	Adjacent           bool
	RetryCoverage      string
	ResetCoverage      string
	SegmentCoverage    CoverageState
	SegmentBoundary    string
	ConcurrentCoverage CoverageState
	Concurrent         bool
	IdentityCoverage   CoverageState
	SameSession        bool
	Provenance         map[string]SequenceProvenance
}

// AssessPair derives only sequence eligibility inputs. A clean interval is
// available only when the collector explicitly declares complete coverage for
// that boundary class over the exact pair window.
func AssessPair(prior, current SequenceFact, events []SequenceFact, coverage []CoverageFact) (PairCoverage, error) {
	if prior.Ordinal == 0 || current.Ordinal <= prior.Ordinal || prior.ModelStepOrdinal == 0 || current.ModelStepOrdinal == 0 {
		return PairCoverage{}, fmt.Errorf("invalid model-step pair")
	}
	result := PairCoverage{
		OrderCoverage: CoverageUnavailable, RetryCoverage: "unavailable", ResetCoverage: "unavailable",
		SegmentCoverage: CoverageUnavailable, SegmentBoundary: "none", ConcurrentCoverage: CoverageUnavailable,
		IdentityCoverage: CoverageUnavailable, Provenance: map[string]SequenceProvenance{},
	}
	for _, class := range []string{"order", "retry", "reset", "segment", "concurrency", "identity"} {
		state, provenance := coverageFor(class, prior.Ordinal, current.Ordinal, coverage)
		if provenance != nil {
			result.Provenance[class] = *provenance
		}
		if state == CoverageCovered && (provenance == nil || !matchesProvenance(prior.Provenance, *provenance) || !matchesProvenance(current.Provenance, *provenance)) {
			state = CoverageContradictory
		}
		switch class {
		case "order":
			result.OrderCoverage = state
		case "retry":
			switch state {
			case CoverageCovered:
				result.RetryCoverage = "covered_no_boundary"
			case CoverageContradictory:
				result.RetryCoverage = "unavailable"
			}
		case "reset":
			switch state {
			case CoverageCovered:
				result.ResetCoverage = "covered_no_boundary"
			case CoverageContradictory:
				result.ResetCoverage = "unavailable"
			}
		case "segment":
			result.SegmentCoverage = state
		case "concurrency":
			result.ConcurrentCoverage = state
		case "identity":
			result.IdentityCoverage = state
		}
	}
	result.Adjacent = result.OrderCoverage == CoverageCovered && current.ModelStepOrdinal == prior.ModelStepOrdinal+1
	result.SameSession = result.IdentityCoverage == CoverageCovered && prior.SessionID != "" && prior.SessionID == current.SessionID

	assessBoundaryEvents(&result, prior.Ordinal, current.Ordinal, false, prior.SessionID, events)
	return result, nil
}

// AssessAttemptBoundaries assesses retry, reset, segment, and concurrency
// events from the start of one positively covered attempt interval through its
// last accepted model step. All four coverage declarations must identify the
// same retained interval and collection authority.
func AssessAttemptBoundaries(first, last SequenceFact, events []SequenceFact, coverage []CoverageFact) (PairCoverage, error) {
	if first.Ordinal == 0 || last.Ordinal < first.Ordinal || first.ModelStepOrdinal == 0 || last.ModelStepOrdinal == 0 {
		return PairCoverage{}, fmt.Errorf("invalid model-step interval")
	}
	for _, fact := range []SequenceFact{first, last} {
		if fact.Provenance.Runtime != RequiredRuntime || fact.Provenance.RuntimeVersion != RequiredRuntimeVersion ||
			(fact.Provenance.Source != "event_hook" && fact.Provenance.Source != "wrapper_stream") || !usableEventProvenance(fact) {
			return PairCoverage{}, fmt.Errorf("invalid model-step provenance")
		}
	}
	result := PairCoverage{
		RetryCoverage: "unavailable", ResetCoverage: "unavailable", SegmentCoverage: CoverageUnavailable,
		SegmentBoundary: "none", ConcurrentCoverage: CoverageUnavailable, Provenance: map[string]SequenceProvenance{},
	}
	var interval *SequenceProvenance
	for _, class := range []string{"retry", "reset", "segment", "concurrency"} {
		state, provenance := coverageFor(class, first.Ordinal, last.Ordinal, coverage)
		if provenance != nil {
			result.Provenance[class] = *provenance
		}
		if state == CoverageCovered && (provenance == nil || !matchesProvenance(first.Provenance, *provenance) || !matchesProvenance(last.Provenance, *provenance)) {
			state = CoverageContradictory
		}
		switch class {
		case "retry":
			if state == CoverageCovered {
				result.RetryCoverage = "covered_no_boundary"
			}
		case "reset":
			if state == CoverageCovered {
				result.ResetCoverage = "covered_no_boundary"
			}
		case "segment":
			result.SegmentCoverage = state
		case "concurrency":
			result.ConcurrentCoverage = state
		}
		if state != CoverageCovered || provenance == nil {
			continue
		}
		if interval == nil {
			retained := *provenance
			interval = &retained
		} else if *interval != *provenance {
			result.RetryCoverage = "unavailable"
			result.ResetCoverage = "unavailable"
			result.SegmentCoverage = CoverageContradictory
			result.ConcurrentCoverage = CoverageContradictory
			return result, nil
		}
	}
	if interval != nil && result.RetryCoverage == "covered_no_boundary" && result.ResetCoverage == "covered_no_boundary" &&
		result.SegmentCoverage == CoverageCovered && result.ConcurrentCoverage == CoverageCovered {
		assessBoundaryEvents(&result, interval.StartOrdinal, last.Ordinal, true, first.SessionID, events)
	}
	return result, nil
}

func assessBoundaryEvents(result *PairCoverage, start, end uint64, includeStart bool, sessionID string, events []SequenceFact) {
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

func usableEventProvenance(event SequenceFact) bool {
	return event.Provenance.Runtime != "" && event.Provenance.RuntimeVersion != "" && event.Provenance.Source != "" &&
		event.Provenance.StartOrdinal == event.Ordinal && event.Provenance.EndOrdinal == event.Ordinal
}

// DecodeOpenCodeSequenceFact recognizes only sequence-relevant events emitted
// by the pinned OpenCode contract. The wrapper stream exposes model steps and
// errors but filters retry and compaction events, so callers cannot declare
// wrapper coverage for those classes from silence.
func DecodeOpenCodeSequenceFact(data []byte, source string, ordinal, modelStepOrdinal uint64) (SequenceFact, bool) {
	if ordinal == 0 || (source != "event_hook" && source != "wrapper_stream") {
		return SequenceFact{}, false
	}
	var envelope struct {
		Type       string                     `json:"type"`
		SessionID  string                     `json:"sessionID"`
		Part       map[string]json.RawMessage `json:"part"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return SequenceFact{}, false
	}
	fact := SequenceFact{Ordinal: ordinal, Provenance: SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.4", Source: source, StartOrdinal: ordinal, EndOrdinal: ordinal}}
	part := envelope.Part
	if source == "event_hook" {
		if envelope.Type == "server.instance.disposed" {
			fact.Boundary = BoundaryInterruption
			return fact, true
		}
		if envelope.Type == "session.error" {
			fact.SessionID = jsonString(envelope.Properties["sessionID"])
			fact.Boundary = BoundaryInterruption
			return fact, true
		}
		if envelope.Type == "session.compacted" {
			fact.SessionID = jsonString(envelope.Properties["sessionID"])
			fact.Boundary = BoundaryCompaction
			return fact, strings.TrimSpace(fact.SessionID) != ""
		}
		if envelope.Type == "session.status" {
			var status struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(envelope.Properties["status"], &status) == nil && status.Type == "retry" {
				fact.SessionID = jsonString(envelope.Properties["sessionID"])
				fact.Boundary = BoundaryRetry
				return fact, strings.TrimSpace(fact.SessionID) != ""
			}
			return SequenceFact{}, false
		}
		if envelope.Type != "message.part.updated" {
			return SequenceFact{}, false
		}
		if json.Unmarshal(envelope.Properties["part"], &part) != nil {
			return SequenceFact{}, false
		}
	} else if envelope.Type == "error" {
		fact.SessionID = envelope.SessionID
		fact.Boundary = BoundaryInterruption
		return fact, true
	} else if envelope.Type != "step_finish" {
		return SequenceFact{}, false
	}
	fact.SessionID = jsonString(part["sessionID"])
	switch jsonString(part["type"]) {
	case "step-finish":
		fact.ModelStepOrdinal = modelStepOrdinal
		return fact, fact.ModelStepOrdinal > 0 && strings.TrimSpace(fact.SessionID) != ""
	case "retry":
		if source == "event_hook" {
			fact.Boundary = BoundaryRetry
			return fact, strings.TrimSpace(fact.SessionID) != ""
		}
	case "compaction":
		if source == "event_hook" {
			fact.Boundary = BoundaryCompaction
			return fact, strings.TrimSpace(fact.SessionID) != ""
		}
	}
	return SequenceFact{}, false
}

func jsonString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func coverageFor(class string, start, end uint64, facts []CoverageFact) (CoverageState, *SequenceProvenance) {
	state := CoverageUnavailable
	var provenance *SequenceProvenance
	for i := range facts {
		fact := &facts[i]
		if fact.Class != class || fact.Provenance.Runtime == "" || fact.Provenance.RuntimeVersion == "" || fact.Provenance.Source == "" || fact.Provenance.StartOrdinal == 0 || fact.Provenance.StartOrdinal > start || fact.Provenance.EndOrdinal < end {
			continue
		}
		if fact.State != CoverageCovered && fact.State != CoverageUnavailable && fact.State != CoverageContradictory {
			continue
		}
		if provenance != nil && (state != fact.State || *provenance != fact.Provenance) {
			return CoverageContradictory, nil
		}
		state, provenance = fact.State, &fact.Provenance
	}
	return state, provenance
}

func matchesProvenance(event, coverage SequenceProvenance) bool {
	return event.Runtime == coverage.Runtime && event.RuntimeVersion == coverage.RuntimeVersion && event.Source == coverage.Source &&
		event.StartOrdinal == event.EndOrdinal && event.StartOrdinal >= coverage.StartOrdinal && event.EndOrdinal <= coverage.EndOrdinal
}
