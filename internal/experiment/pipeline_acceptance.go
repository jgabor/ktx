package experiment

import (
	"math"
	"math/big"
	"sort"
	"strings"

	"github.com/jgabor/ktx/internal/db"
)

const pipelineAcceptanceMinimumCompletePairs = 5

var pipelineAcceptanceReasonPrecedence = []string{
	"unsupported_runtime_accounting",
	"incomplete_pairs",
	"identity_unstable",
	"protocol_noncompliant",
	"estimated_cost_nonfinite",
	"estimated_cost_unavailable",
	"pricing_provenance_unavailable",
	"zero_correct_answers",
	"quality_evidence_unavailable",
	"fact_score_regressed",
	"full_pass_rate_regressed",
	"estimated_cost_per_correct_raw_tie",
	"estimated_cost_per_correct_rounded_tie",
	"estimated_cost_per_correct_not_lower",
}

type pipelineAcceptancePair struct {
	cycle               int
	baseline, candidate Attempt
}

type pipelineAcceptanceSide struct {
	attempts             []Attempt
	totalMicroUSD        *big.Int
	costAvailable        bool
	costNonfinite        bool
	costReasons          []string
	correctCount         int
	scoreTotal           *big.Rat
	scoreAvailable       bool
	rateSelections       []any
	estimateProvenanceOK bool
}

// buildPipelineAcceptance evaluates only the activated prompt-cache successor
// path. It consumes the same complete measured-warm pairs as the comparison,
// but cost acceptance does not depend on the diagnostic hit-rate direction.
func buildPipelineAcceptance(manifest Manifest, attempts []Attempt) map[string]any {
	comparison := buildCachedInputShareComparison(manifest, attempts)
	pairs, checks := pipelineAcceptancePairs(comparison, attempts)
	reasons := []string{}
	if !supportsPromptCacheTokenHitRate(manifest) {
		reasons = append(reasons, "unsupported_runtime_accounting")
	}
	if !checks.complete {
		reasons = append(reasons, "incomplete_pairs")
	}
	if !checks.identityStable {
		reasons = append(reasons, "identity_unstable")
	}
	if !checks.protocolCompliant {
		reasons = append(reasons, "protocol_noncompliant")
	}

	sides := map[string]*pipelineAcceptanceSide{
		Baseline:  newPipelineAcceptanceSide(),
		Candidate: newPipelineAcceptanceSide(),
	}
	for _, pair := range pairs {
		addPipelineAcceptanceAttempt(sides[Baseline], pair.baseline)
		addPipelineAcceptanceAttempt(sides[Candidate], pair.candidate)
	}
	pricing, pricingOK := pipelineAcceptancePricingProvenance(manifest)
	for _, side := range Sides {
		state := sides[side]
		if !state.costAvailable {
			if state.costNonfinite {
				reasons = append(reasons, "estimated_cost_nonfinite")
			} else {
				reasons = append(reasons, "estimated_cost_unavailable")
			}
		}
		if !state.estimateProvenanceOK {
			reasons = append(reasons, "pricing_provenance_unavailable")
		}
		if checks.completePairCount > 0 && state.correctCount == 0 {
			reasons = append(reasons, "zero_correct_answers")
		}
	}
	if !pricingOK {
		reasons = append(reasons, "pricing_provenance_unavailable")
	}

	quality, qualityReasons := pipelineAcceptanceQuality(sides)
	if checks.completePairCount > 0 {
		reasons = append(reasons, qualityReasons...)
	}
	baselineView, baselineRaw, baselineRounded := pipelineAcceptanceSideView(sides[Baseline])
	candidateView, candidateRaw, candidateRounded := pipelineAcceptanceSideView(sides[Candidate])
	if baselineRaw != nil && candidateRaw != nil && baselineRounded != nil && candidateRounded != nil {
		switch candidateRaw.Cmp(baselineRaw) {
		case 0:
			reasons = append(reasons, "estimated_cost_per_correct_raw_tie")
		default:
			if candidateRounded.Cmp(baselineRounded) == 0 {
				reasons = append(reasons, "estimated_cost_per_correct_rounded_tie")
			} else if candidateRounded.Cmp(baselineRounded) > 0 {
				reasons = append(reasons, "estimated_cost_per_correct_not_lower")
			}
		}
	}
	reasons = orderPipelineAcceptanceReasons(reasons)
	return map[string]any{
		"name":                "pipeline_acceptance",
		"contract_version":    promptCacheMetricContractV2,
		"phase":               MeasuredWarm,
		"comparison_unit":     "api_equivalent_estimated_usd_per_full_pass",
		"pair_checks":         checks.view(),
		"rounding":            pipelineAcceptanceRounding(),
		"pricing_provenance":  pricing,
		Baseline:              baselineView,
		Candidate:             candidateView,
		"quality":             quality,
		"accepted":            len(reasons) == 0,
		"reasons":             reasons,
		"unavailable_reasons": append([]string(nil), reasons...),
	}
}

type pipelineAcceptancePairChecks struct {
	completePairCount int
	complete          bool
	identityStable    bool
	protocolCompliant bool
	sourceReasons     []string
}

func (checks pipelineAcceptancePairChecks) view() map[string]any {
	return map[string]any{
		"phase":                  MeasuredWarm,
		"pairing_unit":           "cycle",
		"minimum_complete_pairs": pipelineAcceptanceMinimumCompletePairs,
		"complete_pair_count":    checks.completePairCount,
		"complete":               checks.complete,
		"identity_stable":        checks.identityStable,
		"protocol_compliant":     checks.protocolCompliant,
		"source_reasons":         append([]string(nil), checks.sourceReasons...),
	}
}

func pipelineAcceptancePairs(comparison map[string]any, attempts []Attempt) ([]pipelineAcceptancePair, pipelineAcceptancePairChecks) {
	checks := pipelineAcceptancePairChecks{identityStable: true, protocolCompliant: true}
	byID := map[string]Attempt{}
	duplicates := map[string]bool{}
	for _, attempt := range attempts {
		if _, exists := byID[attempt.AttemptID]; exists {
			duplicates[attempt.AttemptID] = true
			continue
		}
		byID[attempt.AttemptID] = attempt
	}
	pairRows, pairsOK := sliceValue(comparison["pairs"])
	declaredCount, countOK := exactNonnegativeInt(comparison["complete_pair_count"])
	if !pairsOK || !countOK || declaredCount != len(pairRows) {
		checks.sourceReasons = append(checks.sourceReasons, "pair_projection_incomplete")
	}
	pairs := make([]pipelineAcceptancePair, 0, len(pairRows))
	cycles := map[int]bool{}
	for _, raw := range pairRows {
		row, rowOK := mapValue(raw)
		cycle, cycleOK := exactPositiveInt(row["cycle"])
		baselineID, baselineOK := pipelineAcceptancePairAttemptID(row, Baseline)
		candidateID, candidateOK := pipelineAcceptancePairAttemptID(row, Candidate)
		if !rowOK || !cycleOK || !baselineOK || !candidateOK || cycles[cycle] || duplicates[baselineID] || duplicates[candidateID] || baselineID == candidateID {
			checks.sourceReasons = append(checks.sourceReasons, "pair_projection_incomplete")
			continue
		}
		baseline, baselineFound := byID[baselineID]
		candidate, candidateFound := byID[candidateID]
		if !baselineFound || !candidateFound || baseline.Side != Baseline || candidate.Side != Candidate || baseline.Role != MeasuredWarm || candidate.Role != MeasuredWarm || baseline.Cycle != cycle || candidate.Cycle != cycle {
			checks.sourceReasons = append(checks.sourceReasons, "pair_projection_incomplete")
			continue
		}
		cycles[cycle] = true
		pairs = append(pairs, pipelineAcceptancePair{cycle: cycle, baseline: baseline, candidate: candidate})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].cycle < pairs[j].cycle })
	if observations, observationsOK := sliceValue(comparison["observations"]); !observationsOK {
		checks.sourceReasons = append(checks.sourceReasons, "pair_observations_unavailable")
	} else {
		for _, raw := range observations {
			observation, observationOK := mapValue(raw)
			reasons, reasonsOK := strictStringSlice(observation["reasons"])
			if !observationOK || !reasonsOK {
				checks.sourceReasons = append(checks.sourceReasons, "pair_observations_unavailable")
				continue
			}
			checks.sourceReasons = append(checks.sourceReasons, reasons...)
		}
	}
	protocol, protocolOK := mapValue(comparison["protocol"])
	if !protocolOK || protocol["compliant"] != true {
		checks.protocolCompliant = false
		if protocolOK {
			if reasons, ok := strictStringSlice(protocol["reasons"]); ok {
				checks.sourceReasons = append(checks.sourceReasons, reasons...)
			}
		} else {
			checks.sourceReasons = append(checks.sourceReasons, "protocol_projection_incomplete")
		}
	}
	for _, reason := range checks.sourceReasons {
		switch pipelineAcceptanceReasonClass(reason) {
		case "identity":
			checks.identityStable = false
		case "protocol":
			checks.protocolCompliant = false
		}
	}
	checks.completePairCount = len(pairs)
	checks.complete = len(checks.sourceReasons) == 0 && checks.completePairCount >= pipelineAcceptanceMinimumCompletePairs
	if checks.completePairCount < pipelineAcceptanceMinimumCompletePairs {
		checks.sourceReasons = append(checks.sourceReasons, "fewer_than_five_complete_pairs")
	}
	checks.sourceReasons = sortedUniqueStrings(checks.sourceReasons)
	return pairs, checks
}

func pipelineAcceptancePairAttemptID(pair map[string]any, side string) (string, bool) {
	member, ok := mapValue(pair[side])
	if !ok {
		return "", false
	}
	id, ok := member["attempt_id"].(string)
	return id, ok && id != ""
}

func pipelineAcceptanceReasonClass(reason string) string {
	switch {
	case strings.Contains(reason, "identity"):
		return "identity"
	case strings.Contains(reason, "protocol"), strings.Contains(reason, "temporal"), strings.Contains(reason, "execution_order"),
		strings.Contains(reason, "retry"), strings.Contains(reason, "compaction"), strings.Contains(reason, "interruption"),
		strings.Contains(reason, "restart"), strings.Contains(reason, "concurrent"):
		return "protocol"
	default:
		return "incomplete"
	}
}

func newPipelineAcceptanceSide() *pipelineAcceptanceSide {
	return &pipelineAcceptanceSide{totalMicroUSD: big.NewInt(0), costAvailable: true, scoreTotal: big.NewRat(0, 1), scoreAvailable: true, estimateProvenanceOK: true}
}

func addPipelineAcceptanceAttempt(side *pipelineAcceptanceSide, attempt Attempt) {
	side.attempts = append(side.attempts, attempt)
	if attempt.FullPass {
		side.correctCount++
	}
	if math.IsNaN(attempt.Score.Score) || math.IsInf(attempt.Score.Score, 0) || attempt.Score.Score < 0 || attempt.Score.Score > 1 {
		side.scoreAvailable = false
	} else {
		score := new(big.Rat)
		score.SetFloat64(attempt.Score.Score)
		side.scoreTotal.Add(side.scoreTotal, score)
	}
	if attempt.estimatedAPICostDetail != nil && len(attempt.estimatedAPICostDetail.reasons) > 0 {
		side.costAvailable = false
		side.costReasons = append(side.costReasons, attempt.estimatedAPICostDetail.reasons...)
		for _, reason := range attempt.estimatedAPICostDetail.reasons {
			if strings.Contains(reason, "nonfinite") || strings.Contains(reason, "invalid") {
				side.costNonfinite = true
			}
		}
		return
	}
	if attempt.EstimatedAPICost == nil {
		side.costAvailable = false
		if attempt.estimatedAPICostDetail == nil || len(attempt.estimatedAPICostDetail.reasons) == 0 {
			side.costReasons = append(side.costReasons, "estimated_api_cost_unavailable")
		} else {
			side.costReasons = append(side.costReasons, attempt.estimatedAPICostDetail.reasons...)
		}
		return
	}
	if math.IsNaN(*attempt.EstimatedAPICost) || math.IsInf(*attempt.EstimatedAPICost, 0) || *attempt.EstimatedAPICost < 0 {
		side.costAvailable, side.costNonfinite = false, true
		side.costReasons = append(side.costReasons, "estimated_cost_nonfinite")
		return
	}
	microUSD, ok := estimatedCostMicroUSD(*attempt.EstimatedAPICost)
	if !ok {
		side.costAvailable, side.costNonfinite = false, true
		side.costReasons = append(side.costReasons, "estimated_cost_nonfinite")
		return
	}
	side.totalMicroUSD.Add(side.totalMicroUSD, microUSD)
	if attempt.estimatedAPICostDetail == nil || attempt.estimatedAPICostDetail.rateSelection == nil {
		side.estimateProvenanceOK = false
		return
	}
	side.rateSelections = append(side.rateSelections, map[string]any{
		"attempt_id": attempt.AttemptID,
		"selection":  pipelineAcceptanceRateSelection(attempt.estimatedAPICostDetail.rateSelection),
	})
}

func estimatedCostMicroUSD(value float64) (*big.Int, bool) {
	return db.APIEquivalentCostMicroUSD(value)
}

func pipelineAcceptanceRateSelection(selection *db.APIEquivalentCostRateSelection) map[string]any {
	result := map[string]any{
		"basis":                       selection.Basis,
		"context_token_count":         selection.ContextTokenCount,
		"context_tier_size":           selection.ContextTierSize,
		"input_usd_per_million":       selection.InputUSDPerMillion,
		"output_usd_per_million":      selection.OutputUSDPerMillion,
		"cache_read_usd_per_million":  selection.CacheReadUSDPerMillion,
		"cache_write_usd_per_million": selection.CacheWriteUSDPerMillion,
		"reasoning_usd_per_million":   selection.ReasoningUSDPerMillion,
		"reasoning_rate_policy":       selection.ReasoningRatePolicy,
	}
	return result
}

func pipelineAcceptancePricingProvenance(manifest Manifest) (map[string]any, bool) {
	if manifest.Pricing == nil {
		return map[string]any{"state": "unavailable", "reasons": []string{"pricing_snapshot_unavailable"}}, false
	}
	pricing := manifest.Pricing
	complete := pricing.Path != "" && pricing.SHA256 != "" && pricing.SourceURL != "" && pricing.Provider != "" && pricing.Model != ""
	configuredProvider, configuredModel, modelErr := splitModel(manifest.Model)
	identityMatches := modelErr == nil && pricing.Provider == configuredProvider && pricing.Model == configuredModel
	if !complete || !identityMatches {
		reasons := []string{}
		if !complete {
			reasons = append(reasons, "pricing_snapshot_provenance_incomplete")
		}
		if !identityMatches {
			reasons = append(reasons, "pricing_snapshot_identity_mismatch")
		}
		return map[string]any{
			"state":    "unavailable",
			"reasons":  reasons,
			"snapshot": map[string]any{"path": pricing.Path, "sha256": pricing.SHA256, "source_url": pricing.SourceURL, "retrieved_at": pricing.RetrievedAt},
			"provider": pricing.Provider,
			"model":    pricing.Model,
		}, false
	}
	return map[string]any{
		"state":    "retained",
		"provider": pricing.Provider,
		"model":    pricing.Model,
		"snapshot": map[string]any{"path": pricing.Path, "sha256": pricing.SHA256, "source_url": pricing.SourceURL, "retrieved_at": pricing.RetrievedAt},
		"estimator": map[string]any{
			"formula":             "(uncached_input*input_price + cache_read*cache_read_price + cache_write*cache_write_price + output*output_price + reasoning*reasoning_price)/1000000",
			"reported_cost_input": false,
		},
	}, true
}

func pipelineAcceptanceQuality(sides map[string]*pipelineAcceptanceSide) (map[string]any, []string) {
	result := map[string]any{"basis": "same_complete_measured_warm_pairs", "preserved": false, "fact_score_not_lower": false, "full_pass_rate_not_lower": false}
	means := map[string]*big.Rat{}
	for _, side := range Sides {
		state := sides[side]
		if state.scoreAvailable && len(state.attempts) > 0 {
			means[side] = new(big.Rat).Quo(state.scoreTotal, big.NewRat(int64(len(state.attempts)), 1))
		}
	}
	if means[Baseline] == nil || means[Candidate] == nil || len(sides[Baseline].attempts) == 0 || len(sides[Baseline].attempts) != len(sides[Candidate].attempts) {
		result["baseline_fact_score"] = nil
		result["candidate_fact_score"] = nil
		return result, []string{"quality_evidence_unavailable"}
	}
	baselineScore, baselineOK := rationalFiniteFloat(means[Baseline])
	candidateScore, candidateOK := rationalFiniteFloat(means[Candidate])
	if !baselineOK || !candidateOK {
		result["baseline_fact_score"] = nil
		result["candidate_fact_score"] = nil
		return result, []string{"quality_evidence_unavailable"}
	}
	baseline, candidate := sides[Baseline], sides[Candidate]
	count := len(baseline.attempts)
	baselineRate := float64(baseline.correctCount) / float64(count)
	candidateRate := float64(candidate.correctCount) / float64(count)
	factScoreNotLower := means[Candidate].Cmp(means[Baseline]) >= 0
	fullPassRateNotLower := candidate.correctCount >= baseline.correctCount
	result["baseline_fact_score"] = baselineScore
	result["candidate_fact_score"] = candidateScore
	result["baseline_full_pass_rate"] = baselineRate
	result["candidate_full_pass_rate"] = candidateRate
	result["fact_score_not_lower"] = factScoreNotLower
	result["full_pass_rate_not_lower"] = fullPassRateNotLower
	result["preserved"] = factScoreNotLower && fullPassRateNotLower
	reasons := []string{}
	if !factScoreNotLower {
		reasons = append(reasons, "fact_score_regressed")
	}
	if !fullPassRateNotLower {
		reasons = append(reasons, "full_pass_rate_regressed")
	}
	return result, reasons
}

func pipelineAcceptanceSideView(side *pipelineAcceptanceSide) (map[string]any, *big.Rat, *big.Int) {
	result := map[string]any{
		"correct_count":              side.correctCount,
		"estimated_total":            pipelineAcceptanceUnavailableCostView(side.costReasons),
		"estimated_cost_per_correct": pipelineAcceptanceUnavailableCostPerCorrectView(side.costReasons),
		"rate_selections":            side.rateSelections,
	}
	if !side.costAvailable || side.totalMicroUSD == nil {
		return result, nil, nil
	}
	totalValue, totalOK := microUSDValue(side.totalMicroUSD)
	if !totalOK {
		result["estimated_total"] = pipelineAcceptanceUnavailableCostView([]string{"estimated_cost_nonfinite"})
		result["estimated_cost_per_correct"] = pipelineAcceptanceUnavailableCostPerCorrectView([]string{"estimated_cost_nonfinite"})
		return result, nil, nil
	}
	result["estimated_total"] = map[string]any{"state": "measured", "unit": "usd", "value": totalValue, "micro_usd": side.totalMicroUSD.String()}
	if side.correctCount == 0 {
		result["estimated_cost_per_correct"] = pipelineAcceptanceUnavailableCostPerCorrectView([]string{"zero_correct_answers"})
		return result, nil, nil
	}
	raw := new(big.Rat).SetFrac(new(big.Int).Set(side.totalMicroUSD), big.NewInt(int64(side.correctCount)))
	rounded := roundPositiveRational(raw)
	value, valueOK := microUSDValue(rounded)
	if !valueOK {
		result["estimated_cost_per_correct"] = pipelineAcceptanceUnavailableCostPerCorrectView([]string{"estimated_cost_nonfinite"})
		return result, nil, nil
	}
	result["estimated_cost_per_correct"] = map[string]any{
		"state": "measured", "unit": "usd_per_correct", "value": value,
		"micro_usd": rounded.String(), "raw_micro_usd_ratio": raw.RatString(),
	}
	return result, raw, rounded
}

func pipelineAcceptanceUnavailableCostView(reasons []string) map[string]any {
	return map[string]any{"state": "unavailable", "unit": "usd", "value": nil, "micro_usd": nil, "reasons": orderedUniqueStrings(reasons)}
}

func pipelineAcceptanceUnavailableCostPerCorrectView(reasons []string) map[string]any {
	return map[string]any{"state": "unavailable", "unit": "usd_per_correct", "value": nil, "micro_usd": nil, "reasons": orderedUniqueStrings(reasons)}
}

func microUSDValue(value *big.Int) (float64, bool) {
	result, ok := db.APIEquivalentCostUSD(value)
	if !ok || result == nil {
		return 0, false
	}
	return *result, true
}

func roundPositiveRational(value *big.Rat) *big.Int {
	numerator, denominator := new(big.Int).Set(value.Num()), new(big.Int).Set(value.Denom())
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	remainder.Lsh(remainder, 1)
	if remainder.Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}

func rationalFiniteFloat(value *big.Rat) (float64, bool) {
	result, _ := value.Float64()
	return result, !math.IsNaN(result) && !math.IsInf(result, 0)
}

func pipelineAcceptanceRounding() map[string]any {
	return map[string]any{
		"currency":    "usd",
		"scale":       db.APIEquivalentCostRoundingDecimals,
		"mode":        db.APIEquivalentCostRoundingMode,
		"per_attempt": "round each API-equivalent estimate to micro-USD before aggregation",
		"per_correct": "sum exact micro-USD over complete pairs, divide by full-pass count, then round to micro-USD",
		"comparison":  "candidate rounded micro-USD per correct must be strictly lower than baseline",
	}
}

func orderPipelineAcceptanceReasons(reasons []string) []string {
	ordered := []string{}
	for _, expected := range pipelineAcceptanceReasonPrecedence {
		if containsString(reasons, expected) {
			ordered = append(ordered, expected)
		}
	}
	remaining := []string{}
	for _, reason := range reasons {
		if !containsString(ordered, reason) {
			remaining = append(remaining, reason)
		}
	}
	sort.Strings(remaining)
	return append(ordered, remaining...)
}

func sortedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		if value != "" {
			seen[value] = true
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func orderedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
