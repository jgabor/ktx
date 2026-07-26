package experiment

import dbpkg "github.com/jgabor/ktx/internal/db"

type deliveryEvidence struct {
	DatabasePath   string
	OriginalBytes  int64
	DeliveredBytes int64
	Sources        []any
}

func newDeliveryEvidence(databasePath string, rows []dbpkg.DeliveryObservation) *deliveryEvidence {
	evidence := &deliveryEvidence{DatabasePath: databasePath, Sources: []any{}}
	for _, row := range rows {
		source := map[string]any{"source_row_id": row.SourceID, "original_bytes": row.OriginalBytes, "delivered_bytes": row.DeliveredBytes, "compressed_artifact_id": nil}
		if row.CompressedID != nil {
			source["compressed_artifact_id"] = *row.CompressedID
		}
		evidence.OriginalBytes += row.OriginalBytes
		evidence.DeliveredBytes += row.DeliveredBytes
		evidence.Sources = append(evidence.Sources, source)
	}
	return evidence
}

func buildCompressionAttribution(manifest Manifest, attempts []Attempt) map[string]any {
	logicalInputFormula := "uncached_input + cache_read"
	if supportsDirectCachedInputShare(manifest) {
		logicalInputFormula = "uncached_input + cache_read + cache_write"
	}
	classes := map[string]any{
		"tool_delivery": map[string]any{
			"authority":       "first retained tool-call source row and first retained compressed artifact",
			"comparison_unit": "UTF-8 bytes per attempt",
			"observations": attributionObservations(attempts, func(attempt Attempt) (any, any) {
				if attempt.delivery == nil {
					return unavailableValue(), nil
				}
				return map[string]any{"state": "measured", "original_bytes": attempt.delivery.OriginalBytes, "delivered_bytes": attempt.delivery.DeliveredBytes}, map[string]any{"database": attempt.delivery.DatabasePath, "rows": attempt.delivery.Sources}
			}),
		},
		"runtime_usage": map[string]any{
			"authority":       "retained raw runtime step-finish events",
			"comparison_unit": "runtime-emitted tokens per attempt",
			"observations": attributionObservations(attempts, func(attempt Attempt) (any, any) {
				return map[string]any{
					"uncached_input": attributedMeasurement(attempt.TokensIn, attempt.ModelSteps, "uncached_input", func(event SourceEvent) MeasurementState { return event.UncachedInput.State }),
					"cache_read":     attributedMeasurement(attempt.CacheRead, attempt.ModelSteps, "cache_read", func(event SourceEvent) MeasurementState { return event.CacheRead.State }),
					"cache_write":    attributedMeasurement(attempt.CacheWrite, attempt.ModelSteps, "cache_write", func(event SourceEvent) MeasurementState { return event.CacheWrite.State }),
				}, map[string]any{"stream": attempt.Artifacts["stream"], "step_finish_events": attempt.UsageEvidence.StepFinishEvents}
			}),
		},
		"quality": map[string]any{
			"authority":       "retained runtime output scored against the manifest task",
			"comparison_unit": "task score per attempt",
			"observations": attributionObservations(attempts, func(attempt Attempt) (any, any) {
				if !attempt.StreamComplete {
					return unavailableValue(), nil
				}
				return map[string]any{"state": "measured", "score": attempt.Score.Score, "full_pass": attempt.FullPass}, map[string]any{"stream": attempt.Artifacts["stream"], "task": "manifest.json#task"}
			}),
		},
		"runtime_reported_cost": map[string]any{
			"authority":         "retained raw runtime step-finish events",
			"comparison_unit":   "runtime-reported USD per attempt",
			"billing_semantics": "unavailable",
			"observations": attributionObservations(attempts, func(attempt Attempt) (any, any) {
				return attributedMeasurement(attempt.Cost, attempt.ModelSteps, "cost", func(event SourceEvent) MeasurementState { return event.Cost.State }), map[string]any{"stream": attempt.Artifacts["stream"], "cost_events": attempt.UsageEvidence.CostEvents}
			}),
		},
		"estimated_cost": map[string]any{
			"authority":       "retained runtime usage and retained pricing snapshot",
			"comparison_unit": "price-derived USD per attempt",
			"observations": attributionObservations(attempts, func(attempt Attempt) (any, any) {
				if attempt.EstimatedAPICost != nil {
					return attributedNumber(attempt.EstimatedAPICost), map[string]any{"stream": attempt.Artifacts["stream"], "pricing_snapshot": pricingPath(manifest)}
				}
				return evidenceGapValue(modelStepEvidenceGaps(attempt.ModelSteps)), map[string]any{"stream": attempt.Artifacts["stream"], "pricing_snapshot": pricingPath(manifest)}
			}),
		},
	}
	return map[string]any{
		"relationship":     "parallel_non_additive",
		"evidence_classes": classes,
		"derived_views": map[string]any{
			"logical_input":             map[string]any{"state": "derived", "formula": logicalInputFormula, "comparison_unit": "runtime-emitted tokens per attempt"},
			"estimated_cost_break_even": map[string]any{"state": "derived", "formula": "setup estimated cost plus paired measured turns", "comparison_unit": "cumulative price-derived USD by measured turn"},
		},
		"non_additivity": map[string]any{"reason": "evidence classes overlap; no cross-field total is defined"},
		"causal_share":   map[string]any{"state": "unavailable", "reason": "correlated delivery and runtime movement does not identify causal shares or provider-internal behavior"},
		"unsupported_observations": map[string]any{
			"provider_cache_hits": "unavailable", "provider_cache_misses": "unavailable", "provider_cache_fills": "unavailable",
			"provider_request_count": "unavailable", "provider_request_boundaries": "unavailable", "cache_isolation": "unavailable",
			"cache_eviction": "unavailable", "provider_prefix_composition": "unavailable", "runtime_cost_billing_semantics": "unavailable",
		},
	}
}

func attributionObservations(attempts []Attempt, value func(Attempt) (any, any)) []any {
	result := make([]any, 0, len(attempts))
	for _, attempt := range attempts {
		observation, provenance := value(attempt)
		result = append(result, map[string]any{"attempt_id": attempt.AttemptID, "side": attempt.Side, "phase": attempt.Role, "value": observation, "provenance": provenance})
	}
	return result
}

func attributedNumber(value *float64) map[string]any {
	if value == nil {
		return unavailableValue()
	}
	return map[string]any{"state": "measured", "value": *value}
}

func attributedMeasurement(value *float64, events []SourceEvent, field string, state func(SourceEvent) MeasurementState) map[string]any {
	if value != nil {
		return attributedNumber(value)
	}
	return evidenceGapValue(measurementEvidenceGaps(events, field, state))
}

func modelStepEvidenceGaps(events []SourceEvent) []any {
	fields := []struct {
		name  string
		state func(SourceEvent) MeasurementState
	}{
		{"uncached_input", func(event SourceEvent) MeasurementState { return event.UncachedInput.State }},
		{"output", func(event SourceEvent) MeasurementState { return event.Output.State }},
		{"cache_read", func(event SourceEvent) MeasurementState { return event.CacheRead.State }},
		{"cache_write", func(event SourceEvent) MeasurementState { return event.CacheWrite.State }},
		{"reasoning", func(event SourceEvent) MeasurementState { return event.Reasoning.State }},
		{"cost", func(event SourceEvent) MeasurementState { return event.Cost.State }},
	}
	gaps := []any{}
	for _, field := range fields {
		gaps = append(gaps, measurementEvidenceGaps(events, field.name, field.state)...)
	}
	return gaps
}

func measurementEvidenceGaps(events []SourceEvent, field string, state func(SourceEvent) MeasurementState) []any {
	gaps := []any{}
	for _, event := range events {
		classification := state(event)
		if classification == MeasurementMeasured {
			continue
		}
		label := "missing"
		if classification == MeasurementMalformed {
			label = "malformed"
		}
		gaps = append(gaps, map[string]any{"field": field, "source_event_ordinal": event.Ordinal, "state": label})
	}
	return gaps
}

func evidenceGapValue(gaps []any) map[string]any {
	if len(gaps) == 0 {
		return unavailableValue()
	}
	state := "missing"
	for _, raw := range gaps {
		if raw.(map[string]any)["state"] == "malformed" {
			state = "malformed"
			break
		}
	}
	return map[string]any{"state": state, "value": nil, "evidence_gaps": gaps}
}

func unavailableValue() map[string]any { return map[string]any{"state": "unavailable", "value": nil} }

func pricingPath(manifest Manifest) any {
	if manifest.Pricing == nil {
		return nil
	}
	return manifest.Pricing.Path
}
