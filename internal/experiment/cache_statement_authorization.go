package experiment

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
)

const cacheStatementScope = "OpenCode-reported cached-input share under the retained protocol"

var permanentlyUnsupportedCacheClaims = []string{
	"provider_request", "provider_cache_hit", "provider_cache_miss", "provider_cache_fill", "provider_cache_eviction",
	"provider_cache_eligibility", "provider_visible_prefix", "provider_cache_isolation", "ttl_cause", "missed_cost", "causal_compression_effect",
}

var statementAuthorizationReasonPrecedence = []string{
	"unsupported_runtime_accounting", "direct_evidence_incomplete", "evidence_projection_inconsistent", "cycle_pair_incomplete", "protocol_noncompliant",
	"identity_provenance_unstable", "runtime_emitted_identity_conflict", "fewer_than_five_complete_pairs",
	"two_sided_95_percent_interval_unavailable", "two_sided_95_percent_interval_contains_zero",
	"candidate_not_higher", "candidate_not_lower", "two_sided_95_percent_interval_excludes_zero",
	"quality_not_preserved", "no_eligible_consecutive_step_pairs",
}

type directAuthorizationEvidence struct {
	contractValid bool
	runtimeValid  bool
	available     []string
	attemptCount  int
	rowsByID      map[string]map[string]any
	duplicateIDs  map[string]bool
	rows          []map[string]any
	reasons       []string
	diagnostics   []string
	identity      map[string]any
}

type comparisonAuthorizationEvidence struct {
	pairCount       int
	differences     []*big.Rat
	statistics      map[string]any
	quality         map[string]any
	foundation      []string
	diagnostics     []string
	observedReasons []string
	identity        map[string]any
}

type secondaryAuthorizationEvidence struct {
	method, label, rankingEffect string
	eligible, ineligible         int
	valid                        bool
	diagnostics                  []string
}

func buildCacheStatementAuthorization(direct, secondary, comparison map[string]any) map[string]any {
	directEvidence := deriveDirectAuthorizationEvidence(direct)
	comparisonEvidence := deriveComparisonAuthorizationEvidence(directEvidence, comparison)
	secondaryEvidence := deriveSecondaryAuthorizationEvidence(secondary)
	return map[string]any{
		"contract_version": directCacheContractVersion,
		"scope":            cacheStatementScope,
		"configured_identity_backfills_source_events": false,
		"identity_provenance":                         comparisonEvidence.identity,
		"permanently_unsupported_claims":              append([]string(nil), permanentlyUnsupportedCacheClaims...),
		"outcomes": map[string]any{
			"direct_available":         authorizeDirectStatement(directEvidence),
			"comparative_descriptive":  authorizeComparativeDescription(comparisonEvidence),
			"candidate_higher_success": authorizeComparisonDirection(comparisonEvidence, "candidate_higher_success"),
			"candidate_lower":          authorizeComparisonDirection(comparisonEvidence, "candidate_lower"),
			"inconclusive":             authorizeComparisonDirection(comparisonEvidence, "inconclusive"),
			"secondary_available":      authorizeSecondaryStatement(secondaryEvidence),
		},
	}
}

func newAuthorizationOutcome(authorized bool, reasons []string, gates, evidence map[string]any, scope, statement string) map[string]any {
	var rendered any
	if authorized {
		rendered = statement
	}
	return map[string]any{"authorized": authorized, "reasons": reasons, "gates": gates, "evidence": evidence, "scope": scope, "statement": rendered}
}

func deriveDirectAuthorizationEvidence(direct map[string]any) directAuthorizationEvidence {
	result := directAuthorizationEvidence{
		contractValid: stringValue(direct["contract_version"]) == directCacheContractVersion,
		runtimeValid:  true,
		rowsByID:      map[string]map[string]any{},
		duplicateIDs:  map[string]bool{},
	}
	rawRows, rowsOK := sliceValue(direct["attempts"])
	if !rowsOK {
		result.diagnostics = append(result.diagnostics, "direct_attempts_missing_or_malformed")
		result.reasons = append(result.reasons, "direct_evidence_incomplete")
	}
	for _, raw := range rawRows {
		row, ok := mapValue(raw)
		if !ok {
			result.diagnostics = append(result.diagnostics, "direct_attempt_row_malformed")
			result.reasons = appendUnique(result.reasons, "direct_evidence_incomplete")
			continue
		}
		attemptID, ok := nonemptyString(row["attempt_id"])
		if !ok {
			result.diagnostics = append(result.diagnostics, "direct_attempt_id_missing_or_malformed")
			result.reasons = appendUnique(result.reasons, "evidence_projection_inconsistent")
			continue
		}
		if prior, duplicate := result.rowsByID[attemptID]; duplicate {
			result.duplicateIDs[attemptID] = true
			diagnostic := "duplicate_direct_attempt_id:"
			if !reflect.DeepEqual(prior, row) {
				diagnostic = "duplicate_direct_attempt_id_conflicts:"
			}
			result.diagnostics = append(result.diagnostics, diagnostic+attemptID)
			result.reasons = appendUnique(result.reasons, "evidence_projection_inconsistent")
			continue
		}
		result.rowsByID[attemptID] = row
		result.rows = append(result.rows, row)
		if !directRowRuntimeScopeValid(row) {
			result.runtimeValid = false
			result.diagnostics = append(result.diagnostics, "direct_runtime_scope_mismatch:"+attemptID)
		}
		share, shareOK := mapValue(row["share"])
		if !shareOK {
			result.reasons = appendUnique(result.reasons, "direct_evidence_incomplete")
			result.diagnostics = append(result.diagnostics, "direct_share_missing_or_malformed:"+attemptID)
			continue
		}
		state, stateOK := nonemptyString(share["state"])
		if !stateOK {
			result.reasons = appendUnique(result.reasons, "direct_evidence_incomplete")
			result.diagnostics = append(result.diagnostics, "direct_share_state_missing_or_malformed:"+attemptID)
			continue
		}
		if state != "measured" {
			continue
		}
		ratio, ratioOK := parseRationalView(share)
		if !ratioOK || ratio.Sign() < 0 || ratio.Cmp(big.NewRat(1, 1)) > 0 {
			result.reasons = appendUnique(result.reasons, "direct_evidence_incomplete")
			result.diagnostics = append(result.diagnostics, "direct_share_projection_invalid:"+attemptID)
			continue
		}
		result.available = append(result.available, fmt.Sprintf("%s=%s", attemptID, ratio.RatString()))
	}
	result.attemptCount = len(result.rows)
	if !result.contractValid || !result.runtimeValid {
		result.reasons = appendUnique(result.reasons, "unsupported_runtime_accounting")
	}
	if len(result.available) == 0 {
		result.reasons = appendUnique(result.reasons, "direct_evidence_incomplete")
	}
	result.reasons = orderAuthorizationReasons(result.reasons)
	identity, _, identityDiagnostics := assessIdentityRows(result.rows)
	result.identity = identity
	result.diagnostics = appendUnique(result.diagnostics, identityDiagnostics...)
	return result
}

func directRowRuntimeScopeValid(row map[string]any) bool {
	identity, ok := mapValue(row["identity_provenance"])
	if !ok || identity["configured_identity_backfills_source_events"] != false {
		return false
	}
	runtime, ok := mapValue(identity["runtime"])
	return ok && stringValue(runtime["name"]) == directCacheRuntime &&
		stringValue(runtime["version"]) == directCacheRuntimeVersion &&
		stringValue(runtime["cache_control_version"]) == directCacheRuntimeVersion &&
		stringValue(runtime["required_runtime_version"]) == directCacheRuntimeVersion
}

func authorizeDirectStatement(evidence directAuthorizationEvidence) map[string]any {
	authorized := len(evidence.reasons) == 0
	statement := fmt.Sprintf("%s was available for %d of %d retained attempts: %s.", cacheStatementScope, len(evidence.available), evidence.attemptCount, joinMachineValues(evidence.available))
	return newAuthorizationOutcome(authorized, evidence.reasons,
		map[string]any{"supported_runtime_accounting": evidence.contractValid && evidence.runtimeValid, "at_least_one_available_direct_share": len(evidence.available) > 0},
		map[string]any{"reference": "direct_cached_input_share.attempts", "available_count": len(evidence.available), "attempt_count": evidence.attemptCount, "attempt_shares": evidence.available, "diagnostics": evidence.diagnostics},
		"attempt-level descriptive, non-ranking", statement)
}

func deriveComparisonAuthorizationEvidence(direct directAuthorizationEvidence, comparison map[string]any) comparisonAuthorizationEvidence {
	result := comparisonAuthorizationEvidence{statistics: pairedDifferenceStatistics(nil), quality: pairedQualityFromObservations(nil), foundation: append([]string(nil), direct.reasons...)}
	pairRows, pairsOK := sliceValue(comparison["pairs"])
	if !pairsOK && len(comparison) > 0 {
		result.foundation = append(result.foundation, "evidence_projection_inconsistent")
		result.diagnostics = append(result.diagnostics, "comparison_pairs_missing_or_malformed")
	}
	qualityObservations := []pairedQualityObservation{}
	pairedIDs := map[string]bool{}
	pairIdentityRows := []map[string]any{}
	seenCycles := map[int]bool{}
	usedAttemptIDs := map[string]bool{}
	projectionFailure := func(diagnostic string) {
		result.foundation = appendUnique(result.foundation, "evidence_projection_inconsistent")
		result.diagnostics = appendUnique(result.diagnostics, diagnostic)
	}
	for index, raw := range pairRows {
		pair, ok := mapValue(raw)
		if !ok {
			projectionFailure(fmt.Sprintf("pair_row_malformed:%d", index))
			continue
		}
		pairValid := true
		cycle, cycleOK := exactPositiveInt(pair["cycle"])
		if !cycleOK {
			projectionFailure(fmt.Sprintf("pair_cycle_missing_or_malformed:%d", index))
			pairValid = false
		} else if seenCycles[cycle] {
			projectionFailure(fmt.Sprintf("pair_cycle_reused:%d", cycle))
			pairValid = false
		} else {
			seenCycles[cycle] = true
		}
		shares := map[string]*big.Rat{}
		qualities := map[string]pairedQualityObservation{}
		directRows := map[string]map[string]any{}
		attemptIDs := map[string]string{}
		for _, side := range []string{Baseline, Candidate} {
			row, rowOK := mapValue(pair[side])
			if !rowOK {
				projectionFailure(fmt.Sprintf("pair_%s_row_missing_or_malformed:%d", side, index))
				pairValid = false
				continue
			}
			pairIdentityRows = append(pairIdentityRows, row)
			if stringValue(row["side"]) != side {
				projectionFailure(fmt.Sprintf("pair_%s_side_mismatch:%d", side, index))
				pairValid = false
			}
			if stringValue(row["phase"]) != MeasuredWarm {
				projectionFailure(fmt.Sprintf("pair_%s_phase_mismatch:%d", side, index))
				pairValid = false
			}
			nestedCycle, nestedCycleOK := exactPositiveInt(row["cycle"])
			if !nestedCycleOK || !cycleOK || nestedCycle != cycle {
				projectionFailure(fmt.Sprintf("pair_%s_cycle_mismatch:%d", side, index))
				pairValid = false
			}
			attemptID, idOK := nonemptyString(row["attempt_id"])
			if !idOK {
				projectionFailure(fmt.Sprintf("pair_%s_attempt_id_missing_or_malformed:%d", side, index))
				pairValid = false
			} else {
				attemptIDs[side] = attemptID
				if usedAttemptIDs[attemptID] {
					projectionFailure("pair_attempt_id_reused:" + attemptID)
					pairValid = false
				}
				usedAttemptIDs[attemptID] = true
				directRow, found := direct.rowsByID[attemptID]
				if !found || direct.duplicateIDs[attemptID] {
					projectionFailure("paired_attempt_does_not_resolve_once_in_direct:" + attemptID)
					pairValid = false
				} else {
					directRows[side] = directRow
					if stringValue(directRow["side"]) != side {
						projectionFailure("paired_direct_side_mismatch:" + attemptID)
						pairValid = false
					}
					if stringValue(directRow["phase"]) != MeasuredWarm {
						projectionFailure("paired_direct_phase_mismatch:" + attemptID)
						pairValid = false
					}
					directCycle, directCycleOK := exactPositiveInt(directRow["cycle"])
					if !directCycleOK || !cycleOK || directCycle != cycle || directCycle != nestedCycle {
						projectionFailure("paired_direct_cycle_mismatch:" + attemptID)
						pairValid = false
					}
					if !reflect.DeepEqual(row["identity_provenance"], directRow["identity_provenance"]) {
						projectionFailure("paired_identity_projection_differs_from_direct:" + attemptID)
						pairValid = false
					}
				}
			}
			share, shareOK := parseMeasuredShare(row["direct_share"])
			if !shareOK {
				projectionFailure(fmt.Sprintf("pair_%s_direct_share_invalid:%d", side, index))
				pairValid = false
			} else {
				shares[side] = share
				if directRow := directRows[side]; directRow != nil {
					authoritativeShare, authoritativeOK := parseMeasuredShare(directRow["share"])
					if !authoritativeOK || share.Cmp(authoritativeShare) != 0 {
						projectionFailure("paired_share_differs_from_direct:" + attemptIDs[side])
						pairValid = false
					}
				}
			}
			quality, qualityOK := mapValue(row["quality"])
			score, scoreOK := finiteNumber(quality["fact_score"])
			fullPass, passOK := quality["full_pass"].(bool)
			if !qualityOK || !scoreOK || !passOK {
				projectionFailure(fmt.Sprintf("pair_%s_quality_invalid:%d", side, index))
				pairValid = false
			} else {
				qualities[side] = pairedQualityObservation{side: side, score: score, fullPass: fullPass}
			}
		}
		if baselineID, baselineOK := attemptIDs[Baseline]; baselineOK {
			if candidateID, candidateOK := attemptIDs[Candidate]; candidateOK && baselineID == candidateID {
				projectionFailure(fmt.Sprintf("pair_attempt_ids_not_distinct:%d", index))
				pairValid = false
			}
		}
		baseline, baselineOK := shares[Baseline]
		candidate, candidateOK := shares[Candidate]
		if baselineOK && candidateOK {
			difference := new(big.Rat).Sub(candidate, baseline)
			declaredDifference, differenceOK := parseRationalView(anyMap(pair["candidate_minus_baseline"]))
			if !differenceOK || declaredDifference.Cmp(difference) != 0 {
				projectionFailure(fmt.Sprintf("pair_difference_projection_mismatch:%d", index))
				pairValid = false
			}
			if pairValid {
				result.pairCount++
				result.differences = append(result.differences, difference)
				qualityObservations = append(qualityObservations, qualities[Baseline], qualities[Candidate])
				for _, side := range []string{Baseline, Candidate} {
					attemptID := attemptIDs[side]
					pairedIDs[attemptID] = true
				}
			}
		}
	}
	declaredCount, countOK := exactNonnegativeInt(comparison["complete_pair_count"])
	if len(comparison) > 0 && (!countOK || declaredCount != result.pairCount) {
		projectionFailure("complete_pair_count_projection_mismatch")
	}

	result.statistics = pairedDifferenceStatistics(result.differences)
	if len(pairRows) > 0 && (len(result.differences) != result.pairCount || !statisticsProjectionMatches(comparison, result.statistics)) {
		projectionFailure("paired_statistics_projection_mismatch")
	}
	result.quality = pairedQualityFromObservations(qualityObservations)
	if len(pairRows) > 0 && (len(qualityObservations) != result.pairCount*2 || !qualityProjectionMatches(comparison["quality"], result.quality)) {
		projectionFailure("paired_quality_projection_mismatch")
	}

	result.observedReasons, result.foundation = normalizeComparisonReasons(comparison, result.foundation)
	if result.pairCount == 0 {
		result.foundation = appendUnique(result.foundation, "cycle_pair_incomplete")
	}
	if !direct.contractValid || !direct.runtimeValid {
		result.foundation = appendUnique(result.foundation, "unsupported_runtime_accounting")
	}
	result.foundation = orderAuthorizationReasons(result.foundation)

	allIdentity := cloneMap(direct.identity)
	selectedDirectRows := make([]map[string]any, 0, len(pairedIDs))
	for _, row := range direct.rows {
		if pairedIDs[stringValue(row["attempt_id"])] {
			selectedDirectRows = append(selectedDirectRows, row)
		}
	}
	comparisonIdentity, directPairConflict, identityDiagnostics := assessIdentityRows(selectedDirectRows)
	_, projectedPairConflict, projectedDiagnostics := assessIdentityRows(pairIdentityRows)
	result.diagnostics = appendUnique(result.diagnostics, identityDiagnostics...)
	result.diagnostics = appendUnique(result.diagnostics, projectedDiagnostics...)
	if directPairConflict || projectedPairConflict {
		result.foundation = orderAuthorizationReasons(appendUnique(result.foundation, "runtime_emitted_identity_conflict"))
		comparisonIdentity["stable_nonconflicting"] = false
		comparisonIdentity["source_identity_conflicts"] = []string{"runtime_emitted_identity_conflict"}
	}
	comparisonIdentity["attempt_ids"] = sortedStringKeys(pairedIDs)
	allIdentity["comparison_scope"] = comparisonIdentity
	result.identity = allIdentity
	return result
}

func statisticsProjectionMatches(comparison, derived map[string]any) bool {
	direct, ok := mapValue(comparison["direct"])
	if !ok {
		return false
	}
	declared, ok := mapValue(direct["paired_difference"])
	if !ok || stringValue(declared["definition"]) != stringValue(derived["definition"]) ||
		stringValue(declared["method"]) != stringValue(derived["method"]) || stringValue(declared["direction"]) != stringValue(derived["direction"]) ||
		declared["descriptive_only"] != derived["descriptive_only"] {
		return false
	}
	declaredCount, countOK := exactNonnegativeInt(declared["count"])
	derivedCount, _ := exactNonnegativeInt(derived["count"])
	coverage, coverageOK := finiteNumber(declared["coverage"])
	sides, sidesOK := exactNonnegativeInt(declared["sides"])
	if !countOK || declaredCount != derivedCount || !coverageOK || coverage != .95 || !sidesOK || sides != 2 ||
		!optionalRationalProjectionMatches(declared["mean"], derived["mean"]) ||
		!optionalFiniteNumberMatches(declared["sample_standard_deviation"], derived["sample_standard_deviation"]) {
		return false
	}
	return intervalProjectionMatches(declared["interval"], derived["interval"])
}

func qualityProjectionMatches(declaredValue any, derived map[string]any) bool {
	declared, ok := mapValue(declaredValue)
	if !ok || declared["preserved"] != derived["preserved"] || stringValue(declared["basis"]) != stringValue(derived["basis"]) {
		return false
	}
	for _, side := range []string{Baseline, Candidate} {
		declaredSide, declaredOK := mapValue(declared[side])
		derivedSide := anyMap(derived[side])
		declaredCount, countOK := exactNonnegativeInt(declaredSide["count"])
		derivedCount, _ := exactNonnegativeInt(derivedSide["count"])
		if !declaredOK || !countOK || declaredCount != derivedCount ||
			!optionalFiniteNumberMatches(declaredSide["mean_fact_score"], derivedSide["mean_fact_score"]) ||
			!optionalFiniteNumberMatches(declaredSide["full_pass_rate"], derivedSide["full_pass_rate"]) {
			return false
		}
	}
	return true
}

func normalizeComparisonReasons(comparison map[string]any, initial []string) ([]string, []string) {
	observed := []string{}
	malformed := false
	addReasons := func(value any) {
		reasons, ok := strictStringSlice(value)
		if !ok {
			malformed = true
			return
		}
		observed = appendUnique(observed, reasons...)
	}
	if value, exists := comparison["blocking_reasons"]; exists {
		addReasons(value)
	}
	if protocol, ok := mapValue(comparison["protocol"]); ok {
		if value, exists := protocol["reasons"]; exists {
			addReasons(value)
		}
	} else if len(comparison) > 0 {
		malformed = true
	}
	if observations, ok := sliceValue(comparison["observations"]); ok {
		for _, raw := range observations {
			observation, observationOK := mapValue(raw)
			if !observationOK {
				malformed = true
				continue
			}
			addReasons(observation["reasons"])
		}
	} else if len(comparison) > 0 {
		malformed = true
	}
	result := append([]string(nil), initial...)
	if malformed {
		result = appendUnique(result, "evidence_projection_inconsistent")
	}
	for _, reason := range observed {
		switch reason {
		case "unsupported_runtime_accounting":
			result = appendUnique(result, "unsupported_runtime_accounting")
		case "direct_evidence_incomplete":
			result = appendUnique(result, "direct_evidence_incomplete")
		case "cycle_pair_incomplete", "duplicate_cycle_side":
			result = appendUnique(result, "cycle_pair_incomplete")
		case "identity_provenance_unstable":
			result = appendUnique(result, "identity_provenance_unstable")
		case "runtime_emitted_identity_conflict":
			result = appendUnique(result, "runtime_emitted_identity_conflict")
		case "fewer_than_five_complete_pairs", "two_sided_95_percent_interval_unavailable", "two_sided_95_percent_interval_contains_zero", "quality_not_preserved":
			// These are rederived for their outcome-specific gates.
		default:
			result = appendUnique(result, "protocol_noncompliant")
		}
	}
	return observed, result
}

func authorizeComparativeDescription(evidence comparisonAuthorizationEvidence) map[string]any {
	authorized := evidence.pairCount > 0 && len(evidence.foundation) == 0
	statementEvidence := comparisonStatementEvidence(evidence)
	statement := fmt.Sprintf("Across %d complete pairs, candidate-minus-baseline %s values were %s, with mean %s and a two-sided 95%% interval %s.",
		evidence.pairCount, cacheStatementScope, joinMachineValues(stringSlice(statementEvidence["candidate_minus_baseline_ratios"])),
		formatRational(statementEvidence["mean"]), formatInterval(statementEvidence["two_sided_95_percent_interval"]))
	controlled := evidence.pairCount > 0 && len(evidence.foundation) == 0
	return newAuthorizationOutcome(authorized, evidence.foundation,
		map[string]any{"controlled_comparative_evidence_valid": controlled, "at_least_one_complete_pair": evidence.pairCount > 0, "complete_pairs_use_available_direct_shares": !containsString(evidence.foundation, "direct_evidence_incomplete"), "complete_pairs_protocol_compliant": !containsString(evidence.foundation, "protocol_noncompliant"), "complete_pairs_have_stable_nonconflicting_identity_provenance": !containsString(evidence.foundation, "identity_provenance_unstable") && !containsString(evidence.foundation, "runtime_emitted_identity_conflict")},
		statementEvidence, "paired descriptive, non-ranking; no directional, significance, or causal interpretation", statement)
}

func authorizeComparisonDirection(evidence comparisonAuthorizationEvidence, outcome string) map[string]any {
	reasons := append([]string(nil), evidence.foundation...)
	direction := stringValue(evidence.statistics["direction"])
	intervalAvailable := evidence.statistics["interval"] != nil
	if evidence.pairCount < 5 {
		reasons = appendUnique(reasons, "fewer_than_five_complete_pairs")
	} else if !intervalAvailable {
		reasons = appendUnique(reasons, "two_sided_95_percent_interval_unavailable")
	} else {
		switch outcome {
		case "candidate_higher_success":
			if direction == "inconclusive" {
				reasons = appendUnique(reasons, "two_sided_95_percent_interval_contains_zero")
			} else if direction != "higher" {
				reasons = appendUnique(reasons, "candidate_not_higher")
			}
		case "candidate_lower":
			if direction == "inconclusive" {
				reasons = appendUnique(reasons, "two_sided_95_percent_interval_contains_zero")
			} else if direction != "lower" {
				reasons = appendUnique(reasons, "candidate_not_lower")
			}
		case "inconclusive":
			if direction != "inconclusive" {
				reasons = appendUnique(reasons, "two_sided_95_percent_interval_excludes_zero")
			}
		}
	}
	if outcome == "candidate_higher_success" && evidence.pairCount > 0 && evidence.quality["preserved"] != true {
		reasons = appendUnique(reasons, "quality_not_preserved")
	}
	reasons = orderAuthorizationReasons(reasons)
	statementEvidence := comparisonStatementEvidence(evidence)
	statementEvidence["quality"] = evidence.quality
	statement := ""
	switch outcome {
	case "candidate_higher_success":
		statement = fmt.Sprintf("Across %d complete pairs, the candidate had higher %s (candidate-minus-baseline mean %s; two-sided 95%% interval %s).", evidence.pairCount, cacheStatementScope, formatRational(statementEvidence["mean"]), formatInterval(statementEvidence["two_sided_95_percent_interval"]))
	case "candidate_lower":
		statement = fmt.Sprintf("Across %d complete pairs, the candidate had lower %s (candidate-minus-baseline mean %s; two-sided 95%% interval %s).", evidence.pairCount, cacheStatementScope, formatRational(statementEvidence["mean"]), formatInterval(statementEvidence["two_sided_95_percent_interval"]))
	case "inconclusive":
		statement = fmt.Sprintf("Across %d complete pairs, the two-sided 95%% interval for candidate-minus-baseline %s contained zero, so no higher or lower direction was authorized (mean %s; interval %s).", evidence.pairCount, cacheStatementScope, formatRational(statementEvidence["mean"]), formatInterval(statementEvidence["two_sided_95_percent_interval"]))
	}
	gates := map[string]any{
		"controlled_comparative_evidence_valid":                  evidence.pairCount > 0 && len(evidence.foundation) == 0,
		"at_least_five_complete_pairs":                           evidence.pairCount >= 5,
		"two_sided_95_percent_interval_available":                intervalAvailable,
		"two_sided_95_percent_interval_lower_bound_above_zero":   direction == "higher",
		"two_sided_95_percent_interval_upper_bound_below_zero":   direction == "lower",
		"two_sided_95_percent_interval_contains_zero":            direction == "inconclusive",
		"stable_nonconflicting_identity_provenance":              !containsString(evidence.foundation, "identity_provenance_unstable") && !containsString(evidence.foundation, "runtime_emitted_identity_conflict"),
		"candidate_mean_fact_score_and_full_pass_rate_not_lower": evidence.quality["preserved"] == true,
		"configured_identity_backfills_source_events":            false,
	}
	scope := "paired descriptive direction only; no success, failure, significance, or causal interpretation"
	if outcome == "candidate_higher_success" {
		scope = "paired higher-share success authorization with same-pair quality preservation; no significance or causal interpretation"
	}
	return newAuthorizationOutcome(len(reasons) == 0, reasons, gates, statementEvidence, scope, statement)
}

func comparisonStatementEvidence(evidence comparisonAuthorizationEvidence) map[string]any {
	ratios := make([]string, len(evidence.differences))
	for index, difference := range evidence.differences {
		ratios[index] = difference.RatString()
	}
	return map[string]any{
		"reference":                       "cached_input_share_comparison.pairs",
		"complete_pair_count":             evidence.pairCount,
		"candidate_minus_baseline_ratios": ratios,
		"mean":                            evidence.statistics["mean"],
		"two_sided_95_percent_interval":   evidence.statistics["interval"],
		"uncertainty_method":              evidence.statistics["method"],
		"observed_blocking_reasons":       evidence.observedReasons,
		"projection_diagnostics":          evidence.diagnostics,
	}
}

func deriveSecondaryAuthorizationEvidence(secondary map[string]any) secondaryAuthorizationEvidence {
	result := secondaryAuthorizationEvidence{method: stringValue(secondary["method"]), label: stringValue(secondary["label"]), rankingEffect: stringValue(secondary["ranking_effect"]), valid: true}
	declaredEligible, eligibleOK := exactNonnegativeInt(secondary["eligible_count"])
	declaredIneligible, ineligibleOK := exactNonnegativeInt(secondary["ineligible_count"])
	if !eligibleOK || !ineligibleOK {
		result.valid = false
		result.diagnostics = append(result.diagnostics, "secondary_top_level_counts_missing_or_malformed")
	}
	attempts, attemptsOK := sliceValue(secondary["attempts"])
	if !attemptsOK {
		result.valid = false
		result.diagnostics = append(result.diagnostics, "secondary_attempts_missing_or_malformed")
	}
	for index, raw := range attempts {
		attempt, ok := mapValue(raw)
		if !ok {
			result.valid = false
			result.diagnostics = append(result.diagnostics, fmt.Sprintf("secondary_attempt_malformed:%d", index))
			continue
		}
		declaredAttemptEligible, attemptEligibleOK := exactNonnegativeInt(attempt["eligible_count"])
		declaredAttemptIneligible, attemptIneligibleOK := exactNonnegativeInt(attempt["ineligible_count"])
		pairs, pairsOK := sliceValue(attempt["pairs"])
		actualEligible, actualIneligible := 0, 0
		if pairsOK {
			for _, rawPair := range pairs {
				pair, pairOK := mapValue(rawPair)
				eligible, boolOK := pair["eligible"].(bool)
				if !pairOK || !boolOK {
					result.valid = false
					continue
				}
				if eligible {
					actualEligible++
				} else {
					actualIneligible++
				}
			}
		} else {
			result.valid = false
		}
		if !attemptEligibleOK || !attemptIneligibleOK || declaredAttemptEligible != actualEligible || declaredAttemptIneligible != actualIneligible {
			result.valid = false
			result.diagnostics = append(result.diagnostics, fmt.Sprintf("secondary_attempt_count_projection_mismatch:%d", index))
		}
		result.eligible += actualEligible
		result.ineligible += actualIneligible
	}
	if declaredEligible != result.eligible || declaredIneligible != result.ineligible {
		result.valid = false
		result.diagnostics = append(result.diagnostics, "secondary_total_count_projection_mismatch")
	}
	if count, ok := sumCountMap(secondary["ineligible_counts"]); !ok || count != result.ineligible {
		result.valid = false
		result.diagnostics = append(result.diagnostics, "secondary_reason_count_projection_mismatch")
	}
	if result.method != "pi_consecutive_step_reusable_basis" || result.label != "estimated" || result.rankingEffect != "none" {
		result.valid = false
		result.diagnostics = append(result.diagnostics, "secondary_contract_projection_mismatch")
	}
	return result
}

func authorizeSecondaryStatement(evidence secondaryAuthorizationEvidence) map[string]any {
	reasons := []string{}
	if !evidence.valid {
		reasons = append(reasons, "evidence_projection_inconsistent")
	}
	if evidence.eligible == 0 && evidence.valid {
		reasons = append(reasons, "no_eligible_consecutive_step_pairs")
	}
	reasons = orderAuthorizationReasons(reasons)
	statement := fmt.Sprintf("An estimated consecutive-step reuse view using %s was available for %d eligible and %d ineligible pairs; it is descriptive and non-ranking.", evidence.method, evidence.eligible, evidence.ineligible)
	return newAuthorizationOutcome(len(reasons) == 0, reasons,
		map[string]any{"method_is_pi_consecutive_step_reusable_basis": evidence.method == "pi_consecutive_step_reusable_basis", "label_is_estimated": evidence.label == "estimated", "at_least_one_eligible_consecutive_step_pair": evidence.eligible > 0, "ranking_effect_is_none": evidence.rankingEffect == "none", "counts_internally_consistent": evidence.valid},
		map[string]any{"reference": "estimated_consecutive_step_reuse", "method": evidence.method, "eligible_count": evidence.eligible, "ineligible_count": evidence.ineligible, "diagnostics": evidence.diagnostics},
		"secondary estimated descriptive, non-ranking; no effect on direct authorization", statement)
}

func assessIdentityRows(rows []map[string]any) (map[string]any, bool, []string) {
	configuredPresent, configuredPartial := 0, 0
	sourceCounts := map[string]int{"absent": 0, "partial": 0, "present": 0}
	conflict := false
	diagnostics := []string{}
	for _, row := range rows {
		attemptID := stringValue(row["attempt_id"])
		identity, identityOK := mapValue(row["identity_provenance"])
		configured, configuredOK := mapValue(identity["configured_attempt"])
		runtime, runtimeOK := mapValue(identity["runtime"])
		configuredFields := 0
		for _, value := range []any{configured["model"], configured["provider"], runtime["name"], runtime["version"]} {
			if _, ok := nonemptyString(value); ok {
				configuredFields++
			}
		}
		if identityOK && configuredOK && runtimeOK && configuredFields == 4 {
			configuredPresent++
		} else if configuredFields > 0 {
			configuredPartial++
		}
		expectedProvider := stringValue(configured["provider"])
		expectedRuntime := stringValue(runtime["name"])
		expectedModel := ""
		if _, model, err := splitModel(stringValue(configured["model"])); err == nil {
			expectedModel = model
		}
		events, eventsOK := sliceValue(identity["source_events"])
		if !eventsOK {
			conflict = true
			diagnostics = append(diagnostics, "source_identity_events_missing_or_malformed:"+attemptID)
			continue
		}
		for _, rawEvent := range events {
			event, eventOK := mapValue(rawEvent)
			if !eventOK {
				conflict = true
				diagnostics = append(diagnostics, "source_identity_event_malformed:"+attemptID)
				continue
			}
			present := 0
			for _, field := range []struct{ name, expected string }{{"runtime", expectedRuntime}, {"provider", expectedProvider}, {"model", expectedModel}} {
				value := event[field.name]
				if value == nil {
					continue
				}
				text, ok := nonemptyString(value)
				if !ok {
					conflict = true
					diagnostics = append(diagnostics, "source_identity_field_malformed:"+attemptID+":"+field.name)
					continue
				}
				present++
				if field.expected == "" || text != field.expected {
					conflict = true
					diagnostics = append(diagnostics, "source_identity_conflict:"+attemptID+":"+field.name)
				}
			}
			switch present {
			case 0:
				sourceCounts["absent"]++
			case 3:
				sourceCounts["present"]++
			default:
				sourceCounts["partial"]++
			}
		}
	}
	configuredClassification := "absent"
	if len(rows) > 0 && configuredPresent == len(rows) {
		configuredClassification = "present"
	} else if configuredPresent > 0 || configuredPartial > 0 {
		configuredClassification = "partial"
	}
	totalSource := sourceCounts["absent"] + sourceCounts["partial"] + sourceCounts["present"]
	sourceClassification := "absent"
	switch {
	case totalSource == 0 && len(rows) > 0 && configuredClassification != "absent":
		sourceClassification = "configured_only"
	case totalSource > 0 && sourceCounts["present"] == totalSource:
		sourceClassification = "present"
	case totalSource > 0 && sourceCounts["absent"] == totalSource && configuredClassification != "absent":
		sourceClassification = "configured_only"
	case totalSource > 0 && sourceCounts["absent"] != totalSource:
		sourceClassification = "partial"
	}
	conflicts := []string{}
	if conflict {
		conflicts = append(conflicts, "runtime_emitted_identity_conflict")
	}
	return map[string]any{
		"attempt_count":                                len(rows),
		"configured_context_classification":            configuredClassification,
		"source_identity_classification":               sourceClassification,
		"source_event_classification_counts":           sourceCounts,
		"source_identity_conflicts":                    conflicts,
		"stable_nonconflicting":                        !conflict,
		"present_runtime_emitted_identity_must_agree":  true,
		"absent_runtime_emitted_identity_is_permitted": true,
		"configured_identity_backfills_source_events":  false,
		"basis": "configured context and direct_cached_input_share identity_provenance source_events",
	}, conflict, diagnostics
}

func orderAuthorizationReasons(reasons []string) []string {
	ordered := []string{}
	for _, expected := range statementAuthorizationReasonPrecedence {
		if containsString(reasons, expected) {
			ordered = append(ordered, expected)
		}
	}
	return ordered
}

func parseRationalView(view map[string]any) (*big.Rat, bool) {
	ratioText, ok := nonemptyString(view["ratio"])
	if !ok {
		return nil, false
	}
	ratio := new(big.Rat)
	if _, ok := ratio.SetString(ratioText); !ok {
		return nil, false
	}
	numeric, ok := finiteNumber(view["value"])
	if !ok {
		return nil, false
	}
	expected, _ := ratio.Float64()
	return ratio, numeric == expected
}

func parseMeasuredShare(value any) (*big.Rat, bool) {
	view, ok := mapValue(value)
	if !ok || stringValue(view["state"]) != "measured" {
		return nil, false
	}
	ratio, ok := parseRationalView(view)
	return ratio, ok && ratio.Sign() >= 0 && ratio.Cmp(big.NewRat(1, 1)) <= 0
}

func optionalRationalProjectionMatches(declared, derived any) bool {
	if declared == nil || derived == nil {
		return declared == nil && derived == nil
	}
	declaredRatio, declaredOK := parseRationalView(anyMap(declared))
	derivedRatio, derivedOK := parseRationalView(anyMap(derived))
	return declaredOK && derivedOK && declaredRatio.Cmp(derivedRatio) == 0
}

func intervalProjectionMatches(declared, derived any) bool {
	if declared == nil || derived == nil {
		return declared == nil && derived == nil
	}
	declaredMap, declaredOK := mapValue(declared)
	derivedMap, derivedOK := mapValue(derived)
	if !declaredOK || !derivedOK {
		return false
	}
	return optionalFiniteNumberMatches(declaredMap["lower"], derivedMap["lower"]) && optionalFiniteNumberMatches(declaredMap["upper"], derivedMap["upper"])
}

func optionalFiniteNumberMatches(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	a, aOK := finiteNumber(left)
	b, bOK := finiteNumber(right)
	return aOK && bOK && a == b
}

func finiteNumber(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case *float64:
		if typed == nil {
			return 0, false
		}
		number = *typed
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func exactNonnegativeInt(value any) (int, bool) {
	number, ok := finiteNumber(value)
	if !ok || number < 0 || math.Trunc(number) != number || number > float64(^uint(0)>>1) {
		return 0, false
	}
	return int(number), true
}

func exactPositiveInt(value any) (int, bool) {
	number, ok := exactNonnegativeInt(value)
	return number, ok && number > 0
}

func sumCountMap(value any) (int, bool) {
	total := 0
	switch counts := value.(type) {
	case map[string]int:
		for _, count := range counts {
			if count < 0 {
				return 0, false
			}
			total += count
		}
	case map[string]any:
		for _, raw := range counts {
			count, ok := exactNonnegativeInt(raw)
			if !ok {
				return 0, false
			}
			total += count
		}
	default:
		return 0, false
	}
	return total, true
}

func mapValue(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}

func sliceValue(value any) ([]any, bool) {
	result, ok := value.([]any)
	return result, ok
}

func strictStringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		result := make([]string, len(values))
		for index, raw := range values {
			text, ok := raw.(string)
			if !ok {
				return nil, false
			}
			result[index] = text
		}
		return result, true
	default:
		return nil, false
	}
}

func nonemptyString(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && text != ""
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func sortedStringKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func anyMap(value any) map[string]any {
	result, _ := mapValue(value)
	return result
}

func stringSlice(value any) []string {
	result, _ := strictStringSlice(value)
	return result
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func formatRational(value any) string {
	view := anyMap(value)
	if ratio := stringValue(view["ratio"]); ratio != "" {
		return ratio
	}
	return "unavailable"
}

func formatInterval(value any) string {
	if value == nil {
		return "unavailable"
	}
	interval := anyMap(value)
	return fmt.Sprintf("[%v, %v]", interval["lower"], interval["upper"])
}

func joinMachineValues(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	result := values[0]
	for _, value := range values[1:] {
		result += ", " + value
	}
	return result
}
