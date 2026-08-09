package experiment

import (
	"fmt"
	"math/big"
	"reflect"
	"sort"

	"github.com/jgabor/ktx/internal/cachemetric"
)

const promptCacheTokenHitRateSource = "retained_runtime_model_step_telemetry"

var promptCacheTokenHitRateReasonPrecedence = []string{
	"unsupported_runtime_accounting", "direct_evidence_incomplete", "evidence_projection_inconsistent",
	"cycle_pair_incomplete", "protocol_noncompliant", "identity_provenance_unstable",
	"runtime_emitted_identity_missing", "runtime_emitted_identity_conflict", "fewer_than_five_complete_pairs",
	"two_sided_95_percent_interval_unavailable", "two_sided_95_percent_interval_contains_zero",
	"quality_not_preserved", "candidate_not_higher", "candidate_not_lower",
}

var promptCacheTokenHitRateUnsupportedClaims = []string{
	"provider_internal_cache_state", "request_hit_rate", "ttl", "eviction", "eligibility", "isolation", "prefix", "provider_request_boundaries",
}

func buildPromptCacheTokenHitRateContract(manifest Manifest, attempts []Attempt) (map[string]any, map[string]any, map[string]any) {
	if !supportsPromptCacheTokenHitRate(manifest) {
		metric := unavailablePromptCacheTokenHitRateMetric()
		comparison := unavailablePromptCacheTokenHitRateComparison()
		return metric, comparison, unavailablePromptCacheTokenHitRateAuthorization(comparison)
	}
	direct := buildDirectCachedInputShare(manifest, attempts)
	secondary := buildSecondaryCacheEstimate(attempts)
	rawComparison := buildCachedInputShareComparison(manifest, attempts)
	metric := promptCacheTokenHitRateMetricFromDirect(direct)
	comparison := promptCacheTokenHitRateComparisonFromRaw(rawComparison)
	return metric, comparison, buildPromptCacheTokenHitRateAuthorization(direct, secondary, rawComparison, metric, comparison)
}

func promptCacheTokenHitRateMetricFromDirect(direct map[string]any) map[string]any {
	rows := []map[string]any{}
	for _, raw := range direct["attempts"].([]any) {
		row := raw.(map[string]any)
		rows = append(rows, map[string]any{
			"attempt_id": row["attempt_id"], "side": row["side"], "phase": row["phase"], "cycle": row["cycle"],
			"token_hit_rate": row["share"],
			"raw_buckets": map[string]any{
				"uncached_input": row["uncached_input"], "cache_read": row["cache_read"], "cache_write": row["cache_write"],
			},
			"logical_input": row["logical_input"], "kind": cachemetric.PromptCacheTokenHitRateKind,
			"unit": cachemetric.PromptCacheTokenHitRateUnit, "formula": cachemetric.PromptCacheTokenHitRateFormula,
			"source": row["source"], "identity_provenance": row["identity_provenance"],
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i]["side"] != rows[j]["side"] {
			return rows[i]["side"].(string) < rows[j]["side"].(string)
		}
		if rows[i]["phase"] != rows[j]["phase"] {
			return rows[i]["phase"].(string) < rows[j]["phase"].(string)
		}
		if rows[i]["cycle"] != rows[j]["cycle"] {
			return rows[i]["cycle"].(int) < rows[j]["cycle"].(int)
		}
		return rows[i]["attempt_id"].(string) < rows[j]["attempt_id"].(string)
	})
	attempts := make([]any, len(rows))
	for index := range rows {
		attempts[index] = rows[index]
	}
	return map[string]any{
		"contract_version": promptCacheMetricContractV2, "name": "prompt_cache_token_hit_rate",
		"kind": cachemetric.PromptCacheTokenHitRateKind, "unit": cachemetric.PromptCacheTokenHitRateUnit,
		"formula": cachemetric.PromptCacheTokenHitRateFormula, "source": promptCacheTokenHitRateSource,
		"attempt_count": len(attempts), "attempts": attempts,
	}
}

func unavailablePromptCacheTokenHitRateMetric() map[string]any {
	return map[string]any{
		"contract_version": promptCacheMetricContractV2, "name": "prompt_cache_token_hit_rate",
		"kind": cachemetric.PromptCacheTokenHitRateKind, "unit": cachemetric.PromptCacheTokenHitRateUnit,
		"formula": cachemetric.PromptCacheTokenHitRateFormula, "source": promptCacheTokenHitRateSource,
		"availability":  map[string]any{"state": "unavailable", "reason": "unsupported_runtime_accounting"},
		"attempt_count": 0, "attempts": []any{},
	}
}

func promptCacheTokenHitRateComparisonFromRaw(raw map[string]any) map[string]any {
	direct := anyMap(raw["direct"])
	baseline := exactRationalMean(anyMap(direct[Baseline]))
	candidate := exactRationalMean(anyMap(direct[Candidate]))
	pairedDifference := anyMap(direct["paired_difference"])
	blocking := append([]string(nil), stringSlice(raw["blocking_reasons"])...)
	return map[string]any{
		"contract_version": promptCacheMetricContractV2, "name": "prompt_cache_token_hit_rate_comparison",
		"kind": cachemetric.PromptCacheTokenHitRateKind, "unit": cachemetric.PromptCacheTokenHitRateUnit,
		"formula": cachemetric.PromptCacheTokenHitRateFormula, "source": promptCacheTokenHitRateSource,
		"pairing":             map[string]any{"phase": MeasuredWarm, "unit": "cycle", "identity_pooling": "forbidden"},
		"complete_pair_count": raw["complete_pair_count"], "pairs": promptCacheTokenHitRatePairs(raw["pairs"]),
		"observations": promptCacheTokenHitRateObservations(raw["observations"]), "setup": promptCacheTokenHitRateSetup(raw["setup"]), "protocol": raw["protocol"],
		"baseline_mean": baseline, "candidate_mean": candidate,
		"percentage_point_delta": percentagePointView(pairedDifference["mean"]),
		"interval":               percentagePointInterval(pairedDifference), "quality": raw["quality"],
		"blocking_reasons": blocking, "unavailable_reasons": append([]string(nil), blocking...),
		"raw_pair_difference": pairedDifference,
	}
}

func unavailablePromptCacheTokenHitRateComparison() map[string]any {
	blocking := []string{"unsupported_runtime_accounting"}
	return map[string]any{
		"contract_version": promptCacheMetricContractV2, "name": "prompt_cache_token_hit_rate_comparison",
		"kind": cachemetric.PromptCacheTokenHitRateKind, "unit": cachemetric.PromptCacheTokenHitRateUnit,
		"formula": cachemetric.PromptCacheTokenHitRateFormula, "source": promptCacheTokenHitRateSource,
		"pairing":             map[string]any{"phase": MeasuredWarm, "unit": "cycle", "identity_pooling": "forbidden"},
		"complete_pair_count": 0, "pairs": []any{}, "observations": []any{},
		"setup":         map[string]any{"excluded_from_comparison": true, "observations": []any{}},
		"protocol":      map[string]any{"expected_phase": MeasuredWarm, "compliant": false, "reasons": blocking},
		"baseline_mean": nil, "candidate_mean": nil, "percentage_point_delta": nil, "interval": nil,
		"quality": pairedQualityFromObservations(nil), "blocking_reasons": blocking, "unavailable_reasons": append([]string(nil), blocking...),
		"raw_pair_difference": pairedDifferenceStatistics(nil),
	}
}

func exactRationalMean(distribution map[string]any) any {
	values, ok := sliceValue(distribution["values"])
	if !ok || len(values) == 0 {
		return nil
	}
	total := new(big.Rat)
	for _, raw := range values {
		ratio, ok := parseRationalView(anyMap(raw))
		if !ok {
			return nil
		}
		total.Add(total, ratio)
	}
	return rationalView(new(big.Rat).Quo(total, big.NewRat(int64(len(values)), 1)))
}

func percentagePointView(value any) any {
	ratio, ok := parseRationalView(anyMap(value))
	if !ok {
		return nil
	}
	return rationalView(new(big.Rat).Mul(ratio, big.NewRat(100, 1)))
}

func percentagePointInterval(statistics map[string]any) any {
	interval, ok := mapValue(statistics["interval"])
	if !ok {
		return nil
	}
	lower, lowerOK := finiteNumber(interval["lower"])
	upper, upperOK := finiteNumber(interval["upper"])
	if !lowerOK || !upperOK {
		return nil
	}
	return map[string]any{
		"method": statistics["method"], "coverage": statistics["coverage"], "sides": statistics["sides"],
		"unit": "percentage_points", "lower": lower * 100, "upper": upper * 100,
	}
}

func promptCacheTokenHitRatePairs(value any) []any {
	pairs, _ := sliceValue(value)
	result := make([]any, 0, len(pairs))
	for _, raw := range pairs {
		pair, ok := mapValue(raw)
		if !ok {
			result = append(result, raw)
			continue
		}
		converted := copyPromptCacheMap(pair)
		converted[Baseline] = promptCacheTokenHitRatePairAttempt(anyMap(pair[Baseline]))
		converted[Candidate] = promptCacheTokenHitRatePairAttempt(anyMap(pair[Candidate]))
		converted["candidate_minus_baseline_token_fraction"] = pair["candidate_minus_baseline"]
		converted["candidate_minus_baseline_percentage_points"] = percentagePointView(pair["candidate_minus_baseline"])
		delete(converted, "candidate_minus_baseline")
		result = append(result, converted)
	}
	sortPromptCacheRows(result, "cycle")
	return result
}

func promptCacheTokenHitRateObservations(value any) []any {
	observations, _ := sliceValue(value)
	result := make([]any, 0, len(observations))
	for _, raw := range observations {
		observation, ok := mapValue(raw)
		if !ok {
			result = append(result, raw)
			continue
		}
		converted := copyPromptCacheMap(observation)
		if baseline, exists := observation[Baseline]; exists {
			converted[Baseline] = promptCacheTokenHitRatePairAttempt(anyMap(baseline))
		}
		if candidate, exists := observation[Candidate]; exists {
			converted[Candidate] = promptCacheTokenHitRatePairAttempt(anyMap(candidate))
		}
		result = append(result, converted)
	}
	sortPromptCacheRows(result, "cycle")
	return result
}

func promptCacheTokenHitRateSetup(value any) any {
	setup, ok := mapValue(value)
	if !ok {
		return value
	}
	converted := copyPromptCacheMap(setup)
	observations, _ := sliceValue(setup["observations"])
	ordered := append([]any(nil), observations...)
	sortPromptCacheRows(ordered, "execution_order", "cycle", "attempt_id", "side")
	converted["observations"] = ordered
	return converted
}

func sortPromptCacheRows(rows []any, fields ...string) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := anyMap(rows[i]), anyMap(rows[j])
		for _, field := range fields {
			if reflect.DeepEqual(left[field], right[field]) {
				continue
			}
			if leftNumber, leftOK := exactNonnegativeInt(left[field]); leftOK {
				rightNumber, rightOK := exactNonnegativeInt(right[field])
				if rightOK {
					return leftNumber < rightNumber
				}
			}
			return fmt.Sprint(left[field]) < fmt.Sprint(right[field])
		}
		return false
	})
}

func promptCacheTokenHitRatePairAttempt(row map[string]any) map[string]any {
	converted := copyPromptCacheMap(row)
	if rate, exists := row["direct_share"]; exists {
		converted["prompt_cache_token_hit_rate"] = rate
		delete(converted, "direct_share")
	}
	return converted
}

func copyPromptCacheMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func buildPromptCacheTokenHitRateAuthorization(direct, secondary, rawComparison, metric, comparison map[string]any) map[string]any {
	metricProjectionMatches := reflect.DeepEqual(metric, promptCacheTokenHitRateMetricFromDirect(direct))
	comparisonProjectionMatches := reflect.DeepEqual(comparison, promptCacheTokenHitRateComparisonFromRaw(rawComparison))
	legacy := buildCacheStatementAuthorization(direct, secondary, rawComparison)
	blockers, blockersValid := strictStringSlice(comparison["blocking_reasons"])
	quality := anyMap(comparison["quality"])
	difference := anyMap(comparison["raw_pair_difference"])
	direction := stringValue(difference["direction"])
	pairCount, pairCountValid := exactNonnegativeInt(comparison["complete_pair_count"])
	baseGates := map[string]any{
		"contract_activated":                         promptCacheMetricContractV2 == stringValue(metric["contract_version"]) && promptCacheMetricContractV2 == stringValue(comparison["contract_version"]),
		"metric_projection_matches_raw_evidence":     metricProjectionMatches,
		"comparison_projection_matches_raw_evidence": comparisonProjectionMatches,
		"blocking_reasons_valid":                     blockersValid, "no_blocking_reasons": blockersValid && len(blockers) == 0,
		"at_least_five_complete_pairs": pairCountValid && pairCount >= 5,
		"quality_preserved":            quality["preserved"] == true,
		"interval_direction":           direction,
	}
	return map[string]any{
		"contract_version":               promptCacheMetricContractV2,
		"scope":                          "paired token-weighted prompt-cache hit rate under pinned disjoint token accounting",
		"permanently_unsupported_claims": append([]string(nil), promptCacheTokenHitRateUnsupportedClaims...),
		"outcomes": map[string]any{
			"higher": promptCacheTokenHitRateDirectionalOutcome("higher", legacy, blockers, blockersValid, pairCount, pairCountValid, quality, difference, baseGates),
			"lower":  promptCacheTokenHitRateDirectionalOutcome("lower", legacy, blockers, blockersValid, pairCount, pairCountValid, quality, difference, baseGates),
		},
	}
}

func unavailablePromptCacheTokenHitRateAuthorization(comparison map[string]any) map[string]any {
	blockers := []string{"unsupported_runtime_accounting"}
	gates := map[string]any{
		"contract_activated": true, "metric_projection_matches_raw_evidence": false,
		"comparison_projection_matches_raw_evidence": false, "blocking_reasons_valid": true,
		"no_blocking_reasons": false, "at_least_five_complete_pairs": false,
		"quality_preserved": false, "interval_direction": "unavailable",
	}
	return map[string]any{
		"contract_version":               promptCacheMetricContractV2,
		"scope":                          "paired token-weighted prompt-cache hit rate under pinned disjoint token accounting",
		"permanently_unsupported_claims": append([]string(nil), promptCacheTokenHitRateUnsupportedClaims...),
		"outcomes": map[string]any{
			"higher": promptCacheTokenHitRateOutcome(false, blockers, gates, comparison),
			"lower":  promptCacheTokenHitRateOutcome(false, blockers, gates, comparison),
		},
	}
}

func promptCacheTokenHitRateDirectionalOutcome(direction string, legacy map[string]any, blockers []string, blockersValid bool, pairCount int, pairCountValid bool, quality, difference, baseGates map[string]any) map[string]any {
	legacyName := "candidate_higher_success"
	if direction == "lower" {
		legacyName = "candidate_lower"
	}
	legacyOutcome := anyMap(anyMap(legacy["outcomes"])[legacyName])
	reasons := append([]string(nil), blockers...)
	if !blockersValid || baseGates["metric_projection_matches_raw_evidence"] != true || baseGates["comparison_projection_matches_raw_evidence"] != true || baseGates["contract_activated"] != true {
		reasons = appendUnique(reasons, "evidence_projection_inconsistent")
	}
	if !pairCountValid {
		reasons = appendUnique(reasons, "evidence_projection_inconsistent")
	}
	if quality["preserved"] != true {
		reasons = appendUnique(reasons, "quality_not_preserved")
	}
	if legacyOutcome["authorized"] != true {
		reasons = appendUnique(reasons, stringSlice(legacyOutcome["reasons"])...)
	}
	if stringValue(difference["direction"]) != direction {
		if stringValue(difference["direction"]) == "inconclusive" {
			reasons = appendUnique(reasons, "two_sided_95_percent_interval_contains_zero")
		} else if direction == "higher" {
			reasons = appendUnique(reasons, "candidate_not_higher")
		} else {
			reasons = appendUnique(reasons, "candidate_not_lower")
		}
	}
	reasons = orderPromptCacheTokenHitRateReasons(reasons)
	authorized := len(reasons) == 0
	gates := copyPromptCacheMap(baseGates)
	gates["direction_matches_requested_outcome"] = stringValue(difference["direction"]) == direction
	gates["legacy_evidence_authorization"] = legacyOutcome["authorized"] == true
	evidence := map[string]any{
		"reference": "prompt_cache_token_hit_rate_comparison.pairs", "complete_pair_count": pairCount,
		"percentage_point_delta": percentagePointView(difference["mean"]),
		"interval":               percentagePointInterval(difference), "quality": quality,
		"observed_blocking_reasons": append([]string(nil), blockers...),
	}
	return promptCacheTokenHitRateOutcomeWithStatement(authorized, reasons, gates, evidence, direction)
}

func promptCacheTokenHitRateOutcome(authorized bool, reasons []string, gates, comparison map[string]any) map[string]any {
	return map[string]any{
		"authorized": authorized, "reasons": orderPromptCacheTokenHitRateReasons(reasons), "gates": gates,
		"evidence": map[string]any{"reference": "prompt_cache_token_hit_rate_comparison", "comparison": comparison}, "statement": nil,
	}
}

func promptCacheTokenHitRateOutcomeWithStatement(authorized bool, reasons []string, gates, evidence map[string]any, direction string) map[string]any {
	var statement any
	if authorized {
		statement = fmt.Sprintf("Across %d complete pairs, the candidate had %s token-weighted prompt-cache hit rate under the pinned disjoint token accounting contract (candidate-minus-baseline %s percentage points; two-sided 95%% interval %s).",
			evidence["complete_pair_count"], direction, formatPercentagePointValue(evidence["percentage_point_delta"]), formatPercentagePointInterval(evidence["interval"]))
	}
	return map[string]any{"authorized": authorized, "reasons": reasons, "gates": gates, "evidence": evidence, "statement": statement}
}

func formatPercentagePointValue(value any) string {
	view := anyMap(value)
	if number, ok := finiteNumber(view["value"]); ok {
		return fmt.Sprintf("%v", number)
	}
	return "unavailable"
}

func formatPercentagePointInterval(value any) string {
	interval := anyMap(value)
	lower, lowerOK := finiteNumber(interval["lower"])
	upper, upperOK := finiteNumber(interval["upper"])
	if !lowerOK || !upperOK {
		return "unavailable"
	}
	return fmt.Sprintf("[%v, %v] percentage points", lower, upper)
}

func orderPromptCacheTokenHitRateReasons(reasons []string) []string {
	ordered := []string{}
	for _, expected := range promptCacheTokenHitRateReasonPrecedence {
		if containsString(reasons, expected) {
			ordered = append(ordered, expected)
		}
	}
	remaining := []string{}
	for _, reason := range reasons {
		if !containsString(ordered, reason) {
			remaining = appendUnique(remaining, reason)
		}
	}
	sort.Strings(remaining)
	return append(ordered, remaining...)
}
