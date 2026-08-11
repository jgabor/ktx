package experiment

import (
	"math"

	"github.com/jgabor/ktx/internal/db"
)

const pipelineCostGuardField = "pipeline_cost_guard"

var pipelineCostGuardReasonPrecedence = []string{
	"incomplete_pairs",
	"identity_provenance_unstable",
	"protocol_noncompliant",
	"estimated_api_cost_unavailable",
	"estimated_api_cost_nonfinite",
	"zero_correct_answers",
	"quality_not_preserved",
	"candidate_estimated_api_cost_per_correct_not_lower",
}

type pipelineCostEstimate struct {
	value   *float64
	reasons []string
	view    map[string]any
}

type pipelineCostPair struct {
	cycle               int
	baseline, candidate Attempt
	baselineEstimate    pipelineCostEstimate
	candidateEstimate   pipelineCostEstimate
}

type pipelineCostSide struct {
	view       map[string]any
	reasons    []string
	perCorrect *float64
}

func buildPipelineCostGuard(manifest Manifest, attempts []Attempt, comparison map[string]any) map[string]any {
	byID := make(map[string]Attempt, len(attempts))
	for _, attempt := range attempts {
		byID[attempt.AttemptID] = attempt
	}

	pairs := []pipelineCostPair{}
	pairRows, _ := sliceValue(comparison["pairs"])
	for _, raw := range pairRows {
		pair := anyMap(raw)
		baseline := anyMap(pair[Baseline])
		candidate := anyMap(pair[Candidate])
		baseAttempt, baseOK := byID[stringValue(baseline["attempt_id"])]
		candidateAttempt, candidateOK := byID[stringValue(candidate["attempt_id"])]
		if !baseOK || !candidateOK {
			continue
		}
		cycle, _ := pair["cycle"].(int)
		pairs = append(pairs, pipelineCostPair{
			cycle: cycle, baseline: baseAttempt, candidate: candidateAttempt,
			baselineEstimate:  pipelineCostEstimateFor(manifest, baseAttempt),
			candidateEstimate: pipelineCostEstimateFor(manifest, candidateAttempt),
		})
	}

	baseline := pipelineCostGuardSide(pairs, Baseline)
	candidate := pipelineCostGuardSide(pairs, Candidate)
	qualityAttempts := make([]Attempt, 0, len(pairs)*2)
	pairViews := make([]any, 0, len(pairs))
	for _, pair := range pairs {
		qualityAttempts = append(qualityAttempts, pair.baseline, pair.candidate)
		pairViews = append(pairViews, map[string]any{
			"cycle":     pair.cycle,
			"baseline":  pipelineCostGuardAttemptView(pair.baseline, pair.baselineEstimate),
			"candidate": pipelineCostGuardAttemptView(pair.candidate, pair.candidateEstimate),
		})
	}
	quality := pairedQuality(qualityAttempts)
	quality["basis"] = "same_complete_estimated_api_cost_pairs"

	reasons := pipelineCostGuardComparisonReasons(comparison)
	requiredPairs := manifest.TargetValidPerPipeline
	if requiredPairs < 1 {
		requiredPairs = 1
	}
	if len(pairs) < requiredPairs {
		reasons = appendUnique(reasons, "incomplete_pairs")
	}
	for _, side := range []pipelineCostSide{baseline, candidate} {
		for _, reason := range side.reasons {
			if reason == "estimated_api_cost_nonfinite" {
				reasons = appendUnique(reasons, "estimated_api_cost_nonfinite")
			} else {
				reasons = appendUnique(reasons, "estimated_api_cost_unavailable")
			}
		}
	}
	if len(pairs) > 0 {
		if baseline.view["correct_answers"] == 0 || candidate.view["correct_answers"] == 0 {
			reasons = appendUnique(reasons, "zero_correct_answers")
		}
		if quality["preserved"] != true {
			reasons = appendUnique(reasons, "quality_not_preserved")
		}
	}

	comparisonState := "unavailable"
	if baseline.perCorrect != nil && candidate.perCorrect != nil {
		switch {
		case *candidate.perCorrect < *baseline.perCorrect:
			comparisonState = "lower"
		case *candidate.perCorrect > *baseline.perCorrect:
			comparisonState = "higher"
		default:
			comparisonState = "tie"
		}
		if comparisonState != "lower" {
			reasons = appendUnique(reasons, "candidate_estimated_api_cost_per_correct_not_lower")
		}
	}
	reasons = orderPipelineCostGuardReasons(reasons)
	return map[string]any{
		"contract_version":               promptCacheTokenHitRateContractVersion,
		"purpose":                        "portable_quality_adjusted_api_equivalent_estimated_cost_gate",
		"comparison_unit":                "measured_cycle_pair",
		"rounding_decimals":              db.CostRoundDecimals,
		"rounding":                       "round each API-equivalent estimate, side total, and cost per correct to six decimal places before comparison",
		"pricing_provenance":             manifest.Pricing,
		"required_complete_pair_count":   requiredPairs,
		"complete_pair_count":            len(pairs),
		"pairs":                          pairViews,
		"baseline":                       baseline.view,
		"candidate":                      candidate.view,
		"quality":                        quality,
		"estimated_api_cost_per_correct": map[string]any{Baseline: nullablePipelineCost(baseline.perCorrect), Candidate: nullablePipelineCost(candidate.perCorrect), "comparison": comparisonState},
		"comparison_evidence_reasons":    stringSlice(comparison["blocking_reasons"]),
		"reason_precedence":              append([]string(nil), pipelineCostGuardReasonPrecedence...),
		"reasons":                        reasons,
		"accepted":                       len(reasons) == 0,
	}
}

func nullablePipelineCost(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func pipelineCostGuardComparisonReasons(comparison map[string]any) []string {
	reasons := []string{}
	for _, reason := range stringSlice(comparison["blocking_reasons"]) {
		switch reason {
		case "cycle_pair_incomplete", "duplicate_cycle_side", "direct_evidence_incomplete":
			reasons = appendUnique(reasons, "incomplete_pairs")
		case "identity_provenance_unstable", "runtime_emitted_identity_conflict":
			reasons = appendUnique(reasons, "identity_provenance_unstable")
		case "protocol_noncompliant", "execution_order_unbalanced", "temporal_overlap", "temporal_coverage_unavailable", "retry_boundary_observed", "reset_or_compaction_boundary_observed", "temporal_boundary_observed":
			reasons = appendUnique(reasons, "protocol_noncompliant")
		case "quality_not_preserved":
			reasons = appendUnique(reasons, "quality_not_preserved")
		}
	}
	protocol := anyMap(comparison["protocol"])
	if compliant, ok := protocol["compliant"].(bool); ok && !compliant {
		reasons = appendUnique(reasons, "protocol_noncompliant")
	}
	return reasons
}

func pipelineCostEstimateFor(manifest Manifest, attempt Attempt) pipelineCostEstimate {
	reasons := append([]string(nil), attempt.estimatedAPICostReasons...)
	if manifest.Pricing == nil {
		reasons = appendUnique(reasons, "pricing_snapshot_unavailable")
	}
	if attempt.EstimatedAPICost == nil {
		if len(reasons) == 0 {
			reasons = append(reasons, "estimated_api_cost_unavailable")
		}
	} else if math.IsNaN(*attempt.EstimatedAPICost) || math.IsInf(*attempt.EstimatedAPICost, 0) {
		reasons = appendUnique(reasons, "estimated_api_cost_nonfinite")
	}
	view := map[string]any{
		"state": "unavailable", "value": nil,
		"availability_reasons": reasons,
		"pricing_provenance":   manifest.Pricing,
	}
	if len(reasons) == 0 {
		view["state"] = "estimated"
		view["value"] = *attempt.EstimatedAPICost
	}
	return pipelineCostEstimate{value: attempt.EstimatedAPICost, reasons: reasons, view: view}
}

func normalizePipelineCostEstimates(attempts []Attempt) []Attempt {
	result := append([]Attempt(nil), attempts...)
	for i := range result {
		if result[i].EstimatedAPICost == nil || (!math.IsNaN(*result[i].EstimatedAPICost) && !math.IsInf(*result[i].EstimatedAPICost, 0)) {
			continue
		}
		result[i].EstimatedAPICost = nil
		result[i].estimatedAPICostReasons = appendUnique(result[i].estimatedAPICostReasons, "estimated_api_cost_nonfinite")
	}
	return result
}

func pipelineCostGuardAttemptView(attempt Attempt, estimate pipelineCostEstimate) map[string]any {
	reported := map[string]any{"state": "unavailable", "value": nil}
	if attempt.Cost != nil && !math.IsNaN(*attempt.Cost) && !math.IsInf(*attempt.Cost, 0) {
		reported["state"] = "measured"
		reported["value"] = *attempt.Cost
	} else if attempt.Cost != nil {
		reported["reason"] = "reported_cost_nonfinite"
	}
	return map[string]any{
		"attempt_id":         attempt.AttemptID,
		"side":               attempt.Side,
		"reported_cost":      reported,
		"estimated_api_cost": estimate.view,
		"fact_score":         attempt.Score.Score,
		"full_pass":          attempt.FullPass,
	}
}

func pipelineCostGuardSide(pairs []pipelineCostPair, side string) pipelineCostSide {
	reasons := []string{}
	total := 0.0
	correct := 0
	for _, pair := range pairs {
		attempt, estimate := pair.baseline, pair.baselineEstimate
		if side == Candidate {
			attempt, estimate = pair.candidate, pair.candidateEstimate
		}
		if attempt.FullPass {
			correct++
		}
		if len(estimate.reasons) > 0 || estimate.value == nil {
			for _, reason := range estimate.reasons {
				reasons = appendUnique(reasons, reason)
			}
			continue
		}
		rounded := db.RoundCost(*estimate.value)
		if rounded == nil {
			reasons = appendUnique(reasons, "estimated_api_cost_nonfinite")
			continue
		}
		total += *rounded
	}
	view := map[string]any{
		"complete_pair_count":            len(pairs),
		"correct_answers":                correct,
		"estimated_api_cost_total":       nil,
		"estimated_api_cost_per_correct": nil,
		"availability_reasons":           reasons,
	}
	if len(pairs) == 0 || len(reasons) > 0 {
		return pipelineCostSide{view: view, reasons: reasons}
	}
	roundedTotal := db.RoundCost(total)
	if roundedTotal == nil {
		reasons = append(reasons, "estimated_api_cost_nonfinite")
		view["availability_reasons"] = reasons
		return pipelineCostSide{view: view, reasons: reasons}
	}
	view["estimated_api_cost_total"] = *roundedTotal
	if correct == 0 {
		return pipelineCostSide{view: view, reasons: reasons}
	}
	perCorrect := db.RoundCost(*roundedTotal / float64(correct))
	if perCorrect == nil {
		reasons = append(reasons, "estimated_api_cost_nonfinite")
		view["availability_reasons"] = reasons
		return pipelineCostSide{view: view, reasons: reasons}
	}
	view["estimated_api_cost_per_correct"] = *perCorrect
	return pipelineCostSide{view: view, reasons: reasons, perCorrect: perCorrect}
}

func orderPipelineCostGuardReasons(reasons []string) []string {
	ordered := []string{}
	for _, expected := range pipelineCostGuardReasonPrecedence {
		if containsString(reasons, expected) {
			ordered = append(ordered, expected)
		}
	}
	for _, reason := range reasons {
		ordered = appendUnique(ordered, reason)
	}
	return ordered
}
