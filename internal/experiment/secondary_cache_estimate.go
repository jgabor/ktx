package experiment

import (
	"math/big"

	reusesequence "github.com/jgabor/ktx/internal/cache/reuse/sequence"
)

type attemptSequence struct {
	Facts    []reusesequence.SequenceFact
	Events   []reusesequence.SequenceFact
	Coverage []reusesequence.CoverageFact
}

func wrapperAttemptSequence(facts []reusesequence.SequenceFact, modelStepCount int) *attemptSequence {
	result := &attemptSequence{Events: append([]reusesequence.SequenceFact(nil), facts...)}
	for _, fact := range facts {
		if fact.ModelStepOrdinal > 0 {
			result.Facts = append(result.Facts, fact)
		}
	}
	if len(result.Facts) != modelStepCount || len(result.Facts) == 0 {
		return result
	}
	start := result.Facts[0].Ordinal
	end := result.Facts[len(result.Facts)-1].Ordinal
	provenance := reusesequence.SequenceProvenance{
		Runtime: reusesequence.RequiredRuntime, RuntimeVersion: reusesequence.RequiredRuntimeVersion,
		Source: "wrapper_stream", StartOrdinal: start, EndOrdinal: end,
	}
	for _, class := range []string{"order", "identity"} {
		result.Coverage = append(result.Coverage, reusesequence.CoverageFact{Class: class, State: reusesequence.CoverageCovered, Provenance: provenance})
	}
	return result
}

func buildSecondaryCacheEstimate(attempts []Attempt) map[string]any {
	rows := make([]any, 0, len(attempts))
	counts := make(map[string]int, len(reusesequence.IneligibilityReasons))
	for _, reason := range reusesequence.IneligibilityReasons {
		counts[reason] = 0
	}
	eligible := 0
	for _, attempt := range attempts {
		row, attemptEligible, attemptCounts := secondaryEstimateAttempt(attempt)
		eligible += attemptEligible
		for reason, count := range attemptCounts {
			counts[reason] += count
		}
		rows = append(rows, row)
	}
	ineligible := 0
	for _, count := range counts {
		ineligible += count
	}
	return map[string]any{
		"contract_version": directCacheContractVersion,
		"label":            "estimated", "method": "pi_consecutive_step_reusable_basis",
		"description": "consecutive-step reuse-shortfall estimate", "purpose": "secondary_descriptive_non_ranking",
		"formula": "P=min(prior_logical_input,current_logical_input); shortfall=P-current_cache_read; reuse_share=current_cache_read/P",
		"unit":    "token_fraction", "noise_floor_tokens": 1024,
		"eligible_count": eligible, "ineligible_count": ineligible, "ineligible_counts": counts,
		"ranking_effect": "none", "ttl_cause": "unsupported", "expiry_cause": "unsupported", "attempts": rows,
	}
}

func secondaryEstimateAttempt(attempt Attempt) (map[string]any, int, map[string]int) {
	steps := make([]SourceEvent, 0, len(attempt.ModelSteps))
	for _, step := range attempt.ModelSteps {
		if step.Kind == ModelStep {
			steps = append(steps, step)
		}
	}
	pairs := make([]any, 0, len(steps))
	counts := map[string]int{}
	eligible := 0
	for i, current := range steps {
		input, coverage := estimateInput(attempt.sequence, steps, i)
		result := reusesequence.EstimatePair(input)
		row := map[string]any{
			"current_source_event_ordinal": current.Ordinal, "eligible": result.Eligible,
			"reason": nullableText(result.Reason), "coverage": coverage,
			"estimated_reusable_tokens": nil, "shortfall": nil, "reuse_share": nil, "interpretation": nil,
		}
		if i > 0 {
			row["prior_source_event_ordinal"] = steps[i-1].Ordinal
		} else {
			row["prior_source_event_ordinal"] = nil
		}
		if result.Eligible {
			eligible++
			priorLogical := logicalInputForEstimate(input.Prior)
			currentLogical := logicalInputForEstimate(input.Current)
			share, _ := result.ReuseShare.Float64()
			row["prior_logical_input"] = priorLogical
			row["current_logical_input"] = currentLogical
			row["current_cache_read"] = new(big.Int).Set(input.Current.CacheRead.Value)
			row["estimated_reusable_tokens"] = result.EstimatedReusableTokens
			row["shortfall"] = result.Shortfall
			row["reuse_share"] = map[string]any{"value": share, "ratio": result.ReuseShare.RatString()}
			row["interpretation"] = result.Interpretation
		} else {
			counts[result.Reason]++
		}
		pairs = append(pairs, row)
	}
	return map[string]any{
		"attempt_id": attempt.AttemptID, "side": attempt.Side, "phase": attempt.Role,
		"source":         map[string]any{"authority": "retained model-step fields and positively covered runtime sequence when available", "artifact": attempt.Artifacts["stream"]},
		"eligible_count": eligible, "ineligible_count": len(steps) - eligible, "pairs": pairs,
	}, eligible, counts
}

func estimateInput(sequence *attemptSequence, steps []SourceEvent, index int) (reusesequence.EstimatePairInput, map[string]any) {
	input := reusesequence.EstimatePairInput{PriorExists: index > 0}
	coverageView := map[string]any{}
	if index == 0 {
		input.Current = estimateStep(steps[0], reusesequence.SequenceFact{})
		return input, coverageView
	}
	priorFact, currentFact := sequenceFact(sequence, uint64(index)), sequenceFact(sequence, uint64(index+1))
	input.Prior, input.Current = estimateStep(steps[index-1], priorFact), estimateStep(steps[index], currentFact)
	pairCoverage := reusesequence.PairCoverage{
		OrderCoverage: reusesequence.CoverageUnavailable, RetryCoverage: "unavailable", ResetCoverage: "unavailable",
		SegmentCoverage: reusesequence.CoverageUnavailable, SegmentBoundary: "none", ConcurrentCoverage: reusesequence.CoverageUnavailable,
	}
	if priorFact.Ordinal > 0 && currentFact.Ordinal > priorFact.Ordinal && sequence != nil {
		if assessed, err := reusesequence.AssessPair(priorFact, currentFact, sequence.Events, sequence.Coverage); err == nil {
			pairCoverage = assessed
		}
	}
	input.OrderCoverage, input.Adjacent = pairCoverage.OrderCoverage, pairCoverage.Adjacent
	input.RetryCoverage, input.ResetCoverage = pairCoverage.RetryCoverage, pairCoverage.ResetCoverage
	input.SegmentCoverage, input.SegmentBoundary = pairCoverage.SegmentCoverage, pairCoverage.SegmentBoundary
	input.ConcurrentCoverage, input.ConcurrentInterleaving = pairCoverage.ConcurrentCoverage, pairCoverage.Concurrent
	if pairCoverage.IdentityCoverage != reusesequence.CoverageCovered {
		input.Prior.SessionIdentity = reusesequence.TextField{State: reusesequence.FieldUnknown}
		input.Current.SessionIdentity = reusesequence.TextField{State: reusesequence.FieldUnknown}
	}
	coverageView = map[string]any{
		"order": pairCoverage.OrderCoverage, "adjacent": pairCoverage.Adjacent,
		"retry": pairCoverage.RetryCoverage, "reset": pairCoverage.ResetCoverage,
		"segment": pairCoverage.SegmentCoverage, "segment_boundary": pairCoverage.SegmentBoundary,
		"concurrency": pairCoverage.ConcurrentCoverage, "concurrent_interleaving": pairCoverage.Concurrent,
		"identity": pairCoverage.IdentityCoverage, "provenance": pairCoverage.Provenance,
	}
	return input, coverageView
}

func sequenceFact(sequence *attemptSequence, modelOrdinal uint64) reusesequence.SequenceFact {
	if sequence != nil {
		for _, fact := range sequence.Facts {
			if fact.ModelStepOrdinal == modelOrdinal {
				return fact
			}
		}
	}
	return reusesequence.SequenceFact{}
}

func estimateStep(step SourceEvent, fact reusesequence.SequenceFact) reusesequence.EstimateStep {
	identity := reusesequence.TextField{State: reusesequence.FieldUnknown}
	contract := reusesequence.RuntimeContract{}
	if fact.Ordinal > 0 {
		identity = reusesequence.TextField{State: reusesequence.FieldMeasured, Value: fact.SessionID}
		contract = reusesequence.RuntimeContract{Runtime: fact.Provenance.Runtime, RuntimeVersion: fact.Provenance.RuntimeVersion, ContractVersion: reusesequence.RequiredContractVersion}
	}
	return reusesequence.EstimateStep{
		SessionIdentity: identity, RuntimeContract: contract,
		UncachedInput: estimateInteger(step.UncachedInput), CacheRead: estimateInteger(step.CacheRead), CacheWrite: estimateInteger(step.CacheWrite),
	}
}

func estimateInteger(field IntegerMeasurement) reusesequence.IntegerField {
	result := reusesequence.IntegerField{State: reusesequence.FieldState(field.State)}
	if field.Value != nil {
		result.Value = big.NewInt(*field.Value)
	}
	return result
}

func logicalInputForEstimate(step reusesequence.EstimateStep) *big.Int {
	value := new(big.Int).Add(step.UncachedInput.Value, step.CacheRead.Value)
	return value.Add(value, step.CacheWrite.Value)
}
