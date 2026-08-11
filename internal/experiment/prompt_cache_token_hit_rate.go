package experiment

import (
	"fmt"
	"math/big"
)

const (
	promptCacheTokenHitRateField                       = "prompt_cache_token_hit_rate"
	promptCacheTokenHitRateComparisonField             = "prompt_cache_token_hit_rate_comparison"
	promptCacheTokenHitRateStatementAuthorizationField = "prompt_cache_token_hit_rate_statement_authorization"
	promptCacheTokenHitRateStatementScope              = "token-weighted prompt-cache hit rate under the pinned contract (cache-reuse-v2)"
)

func buildPromptCacheTokenHitRate(direct map[string]any) map[string]any {
	directRows, _ := sliceValue(direct["attempts"])
	rows := make([]any, 0, len(directRows))
	for _, row := range directRows {
		rows = append(rows, promptCacheTokenHitRateAttempt(row))
	}
	return map[string]any{
		"contract_version": promptCacheTokenHitRateContractVersion,
		"description":      "token-weighted prompt-cache token hit rate",
		"kind":             "token_weighted",
		"formula":          "sum(cache_read) / sum(uncached_input + cache_read + cache_write)",
		"source":           "retained raw runtime model-step events",
		"purpose":          "descriptive_non_ranking",
		"attempts":         rows,
	}
}

func promptCacheTokenHitRateAttempt(value any) map[string]any {
	row := copyPromptCacheMap(anyMap(value))
	row[promptCacheTokenHitRateField] = row["share"]
	delete(row, "share")
	row["formula"] = "cache_read / (uncached_input + cache_read + cache_write)"
	row["kind"] = "token_weighted"
	return row
}

func buildPromptCacheTokenHitRateComparison(legacy map[string]any) map[string]any {
	direct := anyMap(legacy["direct"])
	quality := copyPromptCacheMap(anyMap(legacy["quality"]))
	if len(quality) > 0 {
		quality["basis"] = "same_complete_prompt_cache_token_hit_rate_pairs"
	}
	return map[string]any{
		"contract_version":    promptCacheTokenHitRateContractVersion,
		"metric":              promptCacheTokenHitRateField,
		"comparison_unit":     "measured_cycle_pair",
		"purpose":             legacy["purpose"],
		"setup":               legacy["setup"],
		"protocol":            legacy["protocol"],
		"complete_pair_count": legacy["complete_pair_count"],
		"pairs":               promptCacheTokenHitRatePairs(legacy["pairs"]),
		"observations":        promptCacheTokenHitRateObservations(legacy["observations"]),
		promptCacheTokenHitRateField: map[string]any{
			"baseline":                 direct["baseline"],
			"candidate":                direct["candidate"],
			"means":                    promptCacheTokenHitRateMeans(direct),
			"candidate_minus_baseline": promptCacheTokenHitRateDifference(anyMap(direct["paired_difference"])),
		},
		"quality":                  quality,
		"secondary_estimated":      legacy["secondary_estimated"],
		"blocking_reasons":         legacy["blocking_reasons"],
		"provider_internal_claims": "forbidden",
	}
}

func promptCacheTokenHitRateMeans(direct map[string]any) map[string]any {
	mean := func(side string) any {
		return anyMap(anyMap(direct[side])["numeric"])["mean"]
	}
	return map[string]any{"baseline": mean(Baseline), "candidate": mean(Candidate)}
}

func promptCacheTokenHitRateDifference(legacy map[string]any) map[string]any {
	result := copyPromptCacheMap(legacy)
	result["definition"] = "candidate_prompt_cache_token_hit_rate - baseline_prompt_cache_token_hit_rate"
	result["percentage_point_delta"] = promptCacheTokenHitRatePercentagePoints(legacy["mean"])
	result["percentage_point_interval"] = promptCacheTokenHitRatePercentagePointInterval(legacy["interval"])
	return result
}

func promptCacheTokenHitRatePercentagePoints(value any) any {
	view := anyMap(value)
	ratioText, ok := view["ratio"].(string)
	if !ok || ratioText == "" {
		return nil
	}
	ratio := new(big.Rat)
	if _, ok := ratio.SetString(ratioText); !ok {
		return nil
	}
	return rationalView(ratio.Mul(ratio, big.NewRat(100, 1)))
}

func promptCacheTokenHitRatePercentagePointInterval(value any) any {
	interval := anyMap(value)
	lower, lowerOK := finiteNumber(interval["lower"])
	upper, upperOK := finiteNumber(interval["upper"])
	if !lowerOK || !upperOK {
		return nil
	}
	return map[string]any{"lower": lower * 100, "upper": upper * 100}
}

func promptCacheTokenHitRatePairs(value any) []any {
	rows, _ := sliceValue(value)
	result := make([]any, 0, len(rows))
	for _, row := range rows {
		pair := copyPromptCacheMap(anyMap(row))
		pair[Baseline] = promptCacheTokenHitRateAttemptView(pair[Baseline])
		pair[Candidate] = promptCacheTokenHitRateAttemptView(pair[Candidate])
		result = append(result, pair)
	}
	return result
}

func promptCacheTokenHitRateObservations(value any) []any {
	rows, _ := sliceValue(value)
	result := make([]any, 0, len(rows))
	for _, row := range rows {
		observation := copyPromptCacheMap(anyMap(row))
		for _, side := range Sides {
			if _, ok := observation[side]; ok {
				observation[side] = promptCacheTokenHitRateAttemptView(observation[side])
			}
		}
		result = append(result, observation)
	}
	return result
}

func promptCacheTokenHitRateAttemptView(value any) any {
	view, ok := mapValue(value)
	if !ok {
		return value
	}
	result := copyPromptCacheMap(view)
	if rate, ok := result["direct_share"]; ok {
		delete(result, "direct_share")
		result[promptCacheTokenHitRateField] = rate
	}
	return result
}

func buildPromptCacheTokenHitRateStatementAuthorization(direct, secondary, comparison, promptComparison map[string]any) map[string]any {
	authorization := buildCacheStatementAuthorization(direct, secondary, comparison)
	authorization["contract_version"] = promptCacheTokenHitRateContractVersion
	authorization["metric"] = promptCacheTokenHitRateField
	authorization["scope"] = promptCacheTokenHitRateStatementScope
	authorization["provider_internal_claims"] = "forbidden"
	identity := anyMap(authorization["identity_provenance"])
	identity["basis"] = "configured context and prompt_cache_token_hit_rate identity_provenance source_events"

	outcomes := anyMap(authorization["outcomes"])
	for name, raw := range outcomes {
		outcome := anyMap(raw)
		promptCacheTokenHitRateAuthorizationOutcome(name, outcome, promptComparison)
	}
	return authorization
}

func promptCacheTokenHitRateAuthorizationOutcome(name string, outcome, comparison map[string]any) {
	evidence := anyMap(outcome["evidence"])
	gates := anyMap(outcome["gates"])
	if name == "direct_available" {
		evidence["reference"] = promptCacheTokenHitRateField + ".attempts"
		if rates, ok := evidence["attempt_shares"]; ok {
			delete(evidence, "attempt_shares")
			evidence["attempt_rates"] = rates
		}
		if available, ok := gates["at_least_one_available_direct_share"]; ok {
			delete(gates, "at_least_one_available_direct_share")
			gates["at_least_one_available_prompt_cache_token_hit_rate"] = available
		}
		outcome["scope"] = "attempt-level descriptive, non-ranking; provider-request and provider-internal claims are not authorized"
		if outcome["authorized"] == true {
			outcome["statement"] = fmt.Sprintf("%s was available for %v of %v retained attempts: %s.", promptCacheTokenHitRateStatementScope, evidence["available_count"], evidence["attempt_count"], joinMachineValues(stringSlice(evidence["attempt_rates"])))
		}
		return
	}

	if name == "secondary_available" {
		outcome["scope"] = "secondary estimated descriptive, non-ranking; no effect on prompt-cache token hit-rate authorization"
		return
	}
	evidence["reference"] = promptCacheTokenHitRateComparisonField + ".pairs"
	delta, interval := promptCacheTokenHitRateComparisonDelta(comparison)
	evidence["percentage_point_delta"] = delta
	evidence["percentage_point_interval"] = interval
	if available, ok := gates["complete_pairs_use_available_direct_shares"]; ok {
		delete(gates, "complete_pairs_use_available_direct_shares")
		gates["complete_pairs_use_available_prompt_cache_token_hit_rates"] = available
	}
	outcome["scope"] = "paired descriptive, non-ranking; provider-request and provider-internal claims are not authorized"
	if name == "candidate_higher_success" {
		outcome["scope"] = "paired higher-rate authorization with same-pair quality preservation; provider-request and provider-internal claims are not authorized"
	}
	if outcome["authorized"] != true {
		return
	}

	count := evidence["complete_pair_count"]
	mean := formatRational(evidence["mean"])
	intervalText := formatInterval(evidence["two_sided_95_percent_interval"])
	points := promptCacheTokenHitRatePercentagePointText(delta)
	switch name {
	case "comparative_descriptive":
		outcome["statement"] = fmt.Sprintf("Across %v complete pairs, candidate-minus-baseline %s values had mean %s (%s; two-sided 95%% interval %s).", count, promptCacheTokenHitRateStatementScope, mean, points, intervalText)
	case "candidate_higher_success":
		outcome["statement"] = fmt.Sprintf("Across %v complete pairs, the candidate had higher %s (candidate-minus-baseline mean %s; %s; two-sided 95%% interval %s).", count, promptCacheTokenHitRateStatementScope, mean, points, intervalText)
	case "candidate_lower":
		outcome["statement"] = fmt.Sprintf("Across %v complete pairs, the candidate had lower %s (candidate-minus-baseline mean %s; %s; two-sided 95%% interval %s).", count, promptCacheTokenHitRateStatementScope, mean, points, intervalText)
	case "inconclusive":
		outcome["statement"] = fmt.Sprintf("Across %v complete pairs, the two-sided 95%% interval for candidate-minus-baseline %s contained zero, so no higher or lower direction was authorized (mean %s; %s; interval %s).", count, promptCacheTokenHitRateStatementScope, mean, points, intervalText)
	}
}

func promptCacheTokenHitRateComparisonDelta(comparison map[string]any) (any, any) {
	rate := anyMap(comparison[promptCacheTokenHitRateField])
	difference := anyMap(rate["candidate_minus_baseline"])
	return difference["percentage_point_delta"], difference["percentage_point_interval"]
}

func promptCacheTokenHitRatePercentagePointText(value any) string {
	view := anyMap(value)
	if number, ok := view["value"]; ok {
		return fmt.Sprintf("%v percentage points", number)
	}
	return "percentage-point delta unavailable"
}

func copyPromptCacheMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
