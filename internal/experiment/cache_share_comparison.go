package experiment

import (
	"math"
	"math/big"
	"sort"
	"time"

	cacheevidence "github.com/jgabor/ktx/internal/cacheevidence/sequence"
)

const pairedCacheComparisonMethod = "student_t_interval_on_paired_differences"

type comparedCacheAttempt struct {
	attempt Attempt
	row     map[string]any
	share   *big.Rat
	reasons []string
}

type comparedCachePair struct {
	cycle               int
	baseline, candidate comparedCacheAttempt
}

func buildCachedInputShareComparison(manifest Manifest, attempts []Attempt) map[string]any {
	setup := []any{}
	measured := map[int]map[string][]Attempt{}
	cycles := []int{}
	executionOrderCounts := map[int]int{}
	for _, attempt := range attempts {
		executionOrderCounts[executionOrder(attempt)]++
		if attempt.Role != MeasuredWarm {
			setup = append(setup, map[string]any{"attempt_id": attempt.AttemptID, "side": attempt.Side, "phase": attempt.Role, "cycle": attempt.Cycle, "execution_order": executionOrder(attempt)})
			continue
		}
		if _, ok := measured[attempt.Cycle]; !ok {
			measured[attempt.Cycle] = map[string][]Attempt{}
			cycles = append(cycles, attempt.Cycle)
		}
		measured[attempt.Cycle][attempt.Side] = append(measured[attempt.Cycle][attempt.Side], attempt)
	}
	sort.Ints(cycles)

	pairs, observations := []any{}, []any{}
	comparisonReasons := []string{}
	provisional := []comparedCachePair{}
	for _, cycle := range cycles {
		baseRecords, candidateRecords := measured[cycle][Baseline], measured[cycle][Candidate]
		cycleReasons := []string{}
		if len(baseRecords) == 0 || len(candidateRecords) == 0 {
			cycleReasons = append(cycleReasons, "cycle_pair_incomplete")
		}
		if len(baseRecords) > 1 || len(candidateRecords) > 1 {
			cycleReasons = append(cycleReasons, "duplicate_cycle_side")
		}
		observation := map[string]any{"cycle": cycle, "baseline_attempt_ids": attemptIDs(baseRecords), "candidate_attempt_ids": attemptIDs(candidateRecords), "reasons": cycleReasons}
		if len(cycleReasons) > 0 {
			comparisonReasons = appendUnique(comparisonReasons, cycleReasons...)
			observations = append(observations, observation)
			continue
		}

		base := compareCacheAttempt(manifest, baseRecords[0])
		candidate := compareCacheAttempt(manifest, candidateRecords[0])
		cycleReasons = appendUnique(cycleReasons, base.reasons...)
		cycleReasons = appendUnique(cycleReasons, candidate.reasons...)
		baseOrder, candidateOrder := executionOrder(base.attempt), executionOrder(candidate.attempt)
		if cycle <= 0 || baseOrder <= 0 || candidateOrder <= 0 || baseOrder == candidateOrder || executionOrderCounts[baseOrder] != 1 || executionOrderCounts[candidateOrder] != 1 {
			cycleReasons = appendUnique(cycleReasons, "protocol_noncompliant")
		} else {
			expected, available := expectedCycleOrder(manifest.PipelineExecutionPolicy, cycle)
			actual := []string{Candidate, Baseline}
			if baseOrder < candidateOrder {
				actual = []string{Baseline, Candidate}
			}
			if !available || !equalStrings(actual, expected) || absInt(baseOrder-candidateOrder) != 1 {
				cycleReasons = appendUnique(cycleReasons, "protocol_noncompliant")
			}
			ordered := []Attempt{base.attempt, candidate.attempt}
			if baseOrder > candidateOrder {
				ordered[0], ordered[1] = ordered[1], ordered[0]
			}
			if !temporalOrderIsPlausible(ordered[0], ordered[1]) {
				cycleReasons = appendUnique(cycleReasons, "temporal_overlap")
			}
		}
		if !sameConfiguredRuntimeContract(base.attempt, candidate.attempt) {
			cycleReasons = appendUnique(cycleReasons, "identity_provenance_unstable")
		}
		if len(cycleReasons) > 0 {
			comparisonReasons = appendUnique(comparisonReasons, cycleReasons...)
			observation["reasons"] = cycleReasons
			observation["baseline"] = base.row
			observation["candidate"] = candidate.row
			observations = append(observations, observation)
			continue
		}
		provisional = append(provisional, comparedCachePair{cycle: cycle, baseline: base, candidate: candidate})
	}

	orderCounts := map[string]int{"baseline_first": 0, "candidate_first": 0}
	for _, pair := range provisional {
		if executionOrder(pair.baseline.attempt) < executionOrder(pair.candidate.attempt) {
			orderCounts["baseline_first"]++
		} else {
			orderCounts["candidate_first"]++
		}
	}
	balancedProtocol := absInt(orderCounts["baseline_first"]-orderCounts["candidate_first"]) <= 1
	protocolReasons := []string{}
	if !balancedProtocol {
		protocolReasons = append(protocolReasons, "protocol_noncompliant", "execution_order_unbalanced")
		for _, pair := range provisional {
			reasons := []string{"protocol_noncompliant", "execution_order_unbalanced"}
			observations = append(observations, map[string]any{
				"cycle": pair.cycle, "baseline_attempt_ids": []any{pair.baseline.attempt.AttemptID}, "candidate_attempt_ids": []any{pair.candidate.attempt.AttemptID},
				"reasons": reasons, "baseline": pair.baseline.row, "candidate": pair.candidate.row,
			})
		}
		comparisonReasons = appendUnique(comparisonReasons, protocolReasons...)
		provisional = nil
	}
	if containsString(comparisonReasons, "protocol_noncompliant") {
		protocolReasons = appendUnique(protocolReasons, "protocol_noncompliant")
	}
	baselineShares, candidateShares, differences := []*big.Rat{}, []*big.Rat{}, []*big.Rat{}
	pairedAttempts := []Attempt{}
	for _, pair := range provisional {
		difference := new(big.Rat).Sub(pair.candidate.share, pair.baseline.share)
		pairs = append(pairs, map[string]any{
			"cycle": pair.cycle, "execution_order": []any{orderedSide(pair.baseline.attempt, pair.candidate.attempt, 0), orderedSide(pair.baseline.attempt, pair.candidate.attempt, 1)},
			"baseline": pair.baseline.row, "candidate": pair.candidate.row, "candidate_minus_baseline": rationalView(difference),
		})
		baselineShares = append(baselineShares, pair.baseline.share)
		candidateShares = append(candidateShares, pair.candidate.share)
		differences = append(differences, difference)
		pairedAttempts = append(pairedAttempts, pair.baseline.attempt, pair.candidate.attempt)
	}
	statistics := pairedDifferenceStatistics(differences)
	quality := pairedQuality(pairedAttempts)
	blocking := comparisonBlockingReasons(manifest, len(pairs), statistics, quality)
	blocking = appendUnique(blocking, comparisonReasons...)
	blocking = appendUnique(blocking, protocolReasons...)
	blocking = orderComparisonReasons(blocking)
	return map[string]any{
		"contract_version": directCacheContractVersion, "purpose": "controlled_descriptive_non_ranking",
		"setup": map[string]any{"excluded_from_comparison": true, "observations": setup},
		"protocol": map[string]any{
			"expected_phase": MeasuredWarm, "pairing_unit": "cycle", "execution_policy": manifest.PipelineExecutionPolicy,
			"observed_order_counts": orderCounts, "compliant": len(protocolReasons) == 0, "reasons": protocolReasons,
		},
		"complete_pair_count": len(pairs), "pairs": pairs, "observations": observations,
		"direct": map[string]any{
			"baseline": exactRationalDistribution(baselineShares), "candidate": exactRationalDistribution(candidateShares),
			"paired_difference": statistics,
		},
		"quality":             quality,
		"secondary_estimated": comparisonSecondaryEstimate(attempts),
		"blocking_reasons":    blocking,
	}
}

func compareCacheAttempt(manifest Manifest, attempt Attempt) comparedCacheAttempt {
	direct := directCachedInputShareAttempt(manifest, attempt)
	reasons := comparisonAttemptProtocolReasons(manifest, attempt)
	shareView := direct["share"].(map[string]any)
	var share *big.Rat
	if shareView["state"] != "measured" {
		reasons = appendUnique(reasons, "direct_evidence_incomplete")
	} else {
		share = new(big.Rat)
		if _, ok := share.SetString(shareView["ratio"].(string)); !ok {
			reasons = appendUnique(reasons, "direct_evidence_incomplete")
		}
	}
	identityReason := runtimeIdentityReason(manifest, attempt)
	if identityReason != "" {
		reasons = appendUnique(reasons, identityReason)
	}
	reasons = appendUnique(reasons, attemptTemporalConfounds(attempt)...)
	return comparedCacheAttempt{attempt: attempt, row: comparisonAttemptView(manifest, attempt, direct), share: share, reasons: reasons}
}

func comparisonAttemptProtocolReasons(manifest Manifest, attempt Attempt) []string {
	reasons := []string{}
	if attempt.ExperimentID != manifest.ExperimentID || !AttemptIsEvaluated(attempt, manifest.Task.MinimumToolCalls) || attempt.Role != MeasuredWarm {
		reasons = appendUnique(reasons, "protocol_noncompliant")
	}
	control := manifest.CacheControl
	metadata := attempt.CacheControl
	if control == nil || metadata == nil || metadata.Phase != attempt.Role {
		return appendUnique(reasons, "protocol_noncompliant")
	}
	if len(retainedCacheControlIssues(control)) > 0 || control.IdleGapMS < 0 {
		reasons = appendUnique(reasons, "protocol_noncompliant")
	}
	if metadata.Mode != control.Mode || metadata.Source != control.Source || metadata.IdleGapMS != control.IdleGapMS {
		reasons = appendUnique(reasons, "protocol_noncompliant")
	}
	provider, _, modelErr := splitModel(manifest.Model)
	variant := ""
	if manifest.Variant != nil {
		variant = *manifest.Variant
	}
	if modelErr != nil || control.Model != manifest.Model || control.Provider != provider || control.Variant != variant ||
		control.RuntimeVersion != manifest.Identities[directCacheRuntime] || metadata.Model != control.Model || metadata.Provider != control.Provider ||
		metadata.Variant != control.Variant || metadata.RuntimeVersion != control.RuntimeVersion {
		reasons = appendUnique(reasons, "identity_provenance_unstable")
	}
	if control.Mode == "isolated" {
		expected, ok := control.NamespaceSHA256[attempt.Side]
		if !ok || len(control.NamespaceSHA256) != len(Sides) || !isLowerSHA256(expected) || metadata.NamespaceSHA256 != expected ||
			control.NamespaceSHA256[Baseline] == control.NamespaceSHA256[Candidate] {
			reasons = appendUnique(reasons, "protocol_noncompliant")
		}
	} else if len(control.NamespaceSHA256) != 0 || metadata.NamespaceSHA256 != "" {
		reasons = appendUnique(reasons, "protocol_noncompliant")
	}
	pipeline, ok := manifest.Pipelines[attempt.Side]
	if !ok || attempt.PipelineID != pipeline.PipelineID || attempt.ConfigSHA256 != pipeline.SHA256 {
		reasons = appendUnique(reasons, "protocol_noncompliant")
	}
	return reasons
}

func comparisonAttemptView(manifest Manifest, attempt Attempt, direct map[string]any) map[string]any {
	pipeline := manifest.Pipelines[attempt.Side]
	delivery := any(map[string]any{"state": "unavailable", "database": attempt.Artifacts["database"], "rows": []any{}})
	if attempt.delivery != nil {
		delivery = map[string]any{"state": "measured", "original_bytes": attempt.delivery.OriginalBytes, "delivered_bytes": attempt.delivery.DeliveredBytes, "database": attempt.delivery.DatabasePath, "rows": attempt.delivery.Sources}
	}
	return map[string]any{
		"attempt_id": attempt.AttemptID, "experiment_id": attempt.ExperimentID, "side": attempt.Side, "phase": attempt.Role,
		"execution_order": executionOrder(attempt), "direct_share": direct["share"],
		"protocol_provenance": map[string]any{
			"expected_experiment_id": manifest.ExperimentID, "status": attempt.Status, "session_id": attempt.SessionID,
			"stream_complete": attempt.StreamComplete, "tool_call_count": attempt.ToolCallCount, "minimum_tool_calls": manifest.Task.MinimumToolCalls,
			"cache_control": attempt.CacheControl, "expected_cache_control": manifest.CacheControl, "temporal_coverage": comparisonTemporalCoverageView(attempt),
		},
		"raw_usage": map[string]any{"uncached_input": direct["uncached_input"], "cache_read": direct["cache_read"], "cache_write": direct["cache_write"], "logical_input": direct["logical_input"]},
		"quality":   map[string]any{"fact_score": attempt.Score.Score, "full_pass": attempt.FullPass},
		"cost":      map[string]any{"runtime_reported": attributedNumber(attempt.Cost), "estimated_api": attributedNumber(attempt.EstimatedAPICost), "stream": attempt.Artifacts["stream"], "pricing_snapshot": pricingPath(manifest)},
		"delivery":  delivery,
		"artifacts": attempt.Artifacts, "content_hashes": attempt.ContentHashes,
		"configuration_sha256": attempt.ConfigSHA256,
		"pipeline":             map[string]any{"id": attempt.PipelineID, "manifest_id": pipeline.PipelineID, "sha256": pipeline.SHA256, "stages": pipeline.Stages},
		"identity_provenance":  direct["identity_provenance"],
	}
}

func comparisonTemporalCoverageView(attempt Attempt) map[string]any {
	view := map[string]any{"state": "unavailable", "facts": []any{}, "coverage": []any{}}
	if attempt.sequence == nil {
		return view
	}
	facts := make([]any, 0, len(attempt.sequence.Facts))
	for _, fact := range attempt.sequence.Facts {
		facts = append(facts, map[string]any{
			"event_ordinal": fact.Ordinal, "model_step_ordinal": fact.ModelStepOrdinal, "session_id": fact.SessionID, "provenance": fact.Provenance,
		})
	}
	coverage := make([]any, 0, len(attempt.sequence.Coverage))
	for _, fact := range attempt.sequence.Coverage {
		coverage = append(coverage, map[string]any{"class": fact.Class, "state": fact.State, "provenance": fact.Provenance})
	}
	view["facts"], view["coverage"] = facts, coverage
	if len(attemptTemporalConfounds(attempt)) == 0 {
		view["state"] = "covered"
	}
	return view
}

func runtimeIdentityReason(manifest Manifest, attempt Attempt) string {
	providers, models, runtimes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, event := range attempt.ModelSteps {
		if event.Kind != ModelStep {
			continue
		}
		if event.Identity.Provider != nil {
			providers[*event.Identity.Provider] = true
		}
		if event.Identity.Model != nil {
			models[*event.Identity.Model] = true
		}
		if event.Identity.Runtime != nil {
			runtimes[*event.Identity.Runtime] = true
		}
	}
	if len(runtimes) > 1 || (len(runtimes) == 1 && !runtimes[directCacheRuntime]) {
		return "runtime_emitted_identity_conflict"
	}
	if len(providers) > 1 || len(models) > 1 || len(runtimes) > 1 || (len(providers) == 0) != (len(models) == 0) {
		return "runtime_emitted_identity_conflict"
	}
	if len(providers) == 0 {
		return ""
	}
	configuredProvider, configuredModel, err := splitModel(manifest.Model)
	if err != nil || !providers[configuredProvider] || !models[configuredModel] {
		return "runtime_emitted_identity_conflict"
	}
	return ""
}

func sameConfiguredRuntimeContract(a, b Attempt) bool {
	if a.CacheControl == nil || b.CacheControl == nil {
		return false
	}
	return a.CacheControl.Provider == b.CacheControl.Provider && a.CacheControl.Model == b.CacheControl.Model &&
		a.CacheControl.Variant == b.CacheControl.Variant && a.CacheControl.RuntimeVersion == b.CacheControl.RuntimeVersion
}

func attemptTemporalConfounds(attempt Attempt) []string {
	steps := []SourceEvent{}
	for _, event := range attempt.ModelSteps {
		if event.Kind == ModelStep {
			steps = append(steps, event)
			if event.Retry.Attempt != nil || event.Retry.PreviousSourceEventOrdinal != nil || event.Retry.ProviderRequestID != nil {
				return []string{"retry_boundary_observed"}
			}
			if event.Retry.Availability != nil && *event.Retry.Availability != "covered_no_boundary" {
				return []string{"temporal_coverage_unavailable"}
			}
		}
	}
	if len(steps) == 0 || attempt.sequence == nil {
		return []string{"temporal_coverage_unavailable"}
	}
	first, last := sequenceFact(attempt.sequence, 1), sequenceFact(attempt.sequence, uint64(len(steps)))
	boundaries, err := cacheevidence.AssessAttemptBoundaries(first, last, attempt.sequence.Events, attempt.sequence.Coverage)
	if err != nil || boundaries.RetryCoverage == "unavailable" || boundaries.ResetCoverage == "unavailable" ||
		boundaries.SegmentCoverage != cacheevidence.CoverageCovered || boundaries.ConcurrentCoverage != cacheevidence.CoverageCovered {
		return []string{"temporal_coverage_unavailable"}
	}
	if boundaries.RetryCoverage == "boundary_observed" {
		return []string{"retry_boundary_observed"}
	}
	if boundaries.ResetCoverage == "boundary_observed" {
		return []string{"reset_or_compaction_boundary_observed"}
	}
	if boundaries.SegmentBoundary != "none" || boundaries.Concurrent {
		return []string{"temporal_boundary_observed"}
	}
	for i := 1; i < len(steps); i++ {
		prior, current := sequenceFact(attempt.sequence, uint64(i)), sequenceFact(attempt.sequence, uint64(i+1))
		coverage, err := cacheevidence.AssessPair(prior, current, attempt.sequence.Events, attempt.sequence.Coverage)
		if err != nil || coverage.OrderCoverage != cacheevidence.CoverageCovered || !coverage.Adjacent {
			return []string{"temporal_coverage_unavailable"}
		}
	}
	return nil
}

func exactRationalDistribution(values []*big.Rat) map[string]any {
	rows := make([]any, len(values))
	numeric := make([]float64, len(values))
	for i, value := range values {
		rows[i] = rationalView(value)
		numeric[i], _ = value.Float64()
	}
	return map[string]any{"count": len(values), "values": rows, "numeric": distribution(numeric)}
}

func pairedDifferenceStatistics(values []*big.Rat) map[string]any {
	result := map[string]any{
		"count": len(values), "definition": "candidate_share - baseline_share", "method": pairedCacheComparisonMethod,
		"coverage": .95, "sides": 2, "mean": nil, "sample_standard_deviation": nil, "interval": nil,
		"direction": "unavailable", "descriptive_only": len(values) < 5,
	}
	if len(values) == 0 {
		return result
	}
	sum := new(big.Rat)
	for _, value := range values {
		sum.Add(sum, value)
	}
	meanRat := new(big.Rat).Quo(sum, big.NewRat(int64(len(values)), 1))
	result["mean"] = rationalView(meanRat)
	if len(values) == 1 {
		return result
	}
	varianceRat := new(big.Rat)
	zeroVariance := true
	for _, value := range values {
		difference := new(big.Rat).Sub(value, meanRat)
		if difference.Sign() != 0 {
			zeroVariance = false
		}
		varianceRat.Add(varianceRat, new(big.Rat).Mul(difference, difference))
	}
	varianceRat.Quo(varianceRat, big.NewRat(int64(len(values)-1), 1))
	standardDeviationBig := new(big.Float).SetPrec(256).SetRat(varianceRat)
	standardDeviationBig.Sqrt(standardDeviationBig)
	standardDeviation, _ := standardDeviationBig.Float64()
	result["sample_standard_deviation"] = standardDeviation
	if len(values) < 5 {
		return result
	}
	meanBig := new(big.Float).SetPrec(256).SetRat(meanRat)
	lowerBig := new(big.Float).SetPrec(256).Set(meanBig)
	upperBig := new(big.Float).SetPrec(256).Set(meanBig)
	if !zeroVariance {
		margin := new(big.Float).SetPrec(256).Set(standardDeviationBig)
		margin.Mul(margin, new(big.Float).SetPrec(256).SetFloat64(studentTCritical95(len(values)-1)))
		margin.Quo(margin, new(big.Float).SetPrec(256).Sqrt(new(big.Float).SetPrec(256).SetInt64(int64(len(values)))))
		lowerBig.Sub(lowerBig, margin)
		upperBig.Add(upperBig, margin)
	}
	lower, _ := lowerBig.Float64()
	upper, _ := upperBig.Float64()
	direction := "inconclusive"
	if lowerBig.Sign() > 0 {
		direction = "higher"
	} else if upperBig.Sign() < 0 {
		direction = "lower"
	}
	result["interval"] = map[string]any{"lower": lower, "upper": upper}
	result["direction"] = direction
	return result
}

func rationalView(value *big.Rat) map[string]any {
	numeric, _ := value.Float64()
	return map[string]any{"value": numeric, "ratio": value.RatString()}
}

func pairedQuality(attempts []Attempt) map[string]any {
	scores := map[string][]float64{Baseline: {}, Candidate: {}}
	passes := map[string]int{Baseline: 0, Candidate: 0}
	for _, attempt := range attempts {
		scores[attempt.Side] = append(scores[attempt.Side], attempt.Score.Score)
		if attempt.FullPass {
			passes[attempt.Side]++
		}
	}
	view := map[string]any{}
	for _, side := range Sides {
		var rate any
		if len(scores[side]) > 0 {
			rate = float64(passes[side]) / float64(len(scores[side]))
		}
		view[side] = map[string]any{"count": len(scores[side]), "mean_fact_score": mean(scores[side]), "full_pass_rate": rate}
	}
	preserved := false
	if len(scores[Baseline]) > 0 && len(scores[Baseline]) == len(scores[Candidate]) {
		preserved = *mean(scores[Candidate]) >= *mean(scores[Baseline]) && passes[Candidate] >= passes[Baseline]
	}
	view["preserved"] = preserved
	view["basis"] = "same_complete_direct_pairs"
	return view
}

func comparisonSecondaryEstimate(attempts []Attempt) map[string]any {
	values := map[string][]*big.Rat{Baseline: {}, Candidate: {}}
	eligible := map[string]int{Baseline: 0, Candidate: 0}
	ineligible := map[string]int{Baseline: 0, Candidate: 0}
	for _, attempt := range attempts {
		if attempt.Role != MeasuredWarm || (attempt.Side != Baseline && attempt.Side != Candidate) {
			continue
		}
		row, okCount, _ := secondaryEstimateAttempt(attempt)
		eligible[attempt.Side] += okCount
		ineligible[attempt.Side] += row["ineligible_count"].(int)
		for _, raw := range row["pairs"].([]any) {
			pair := raw.(map[string]any)
			if pair["eligible"] != true {
				continue
			}
			ratio := new(big.Rat)
			ratio.SetString(pair["reuse_share"].(map[string]any)["ratio"].(string))
			values[attempt.Side] = append(values[attempt.Side], ratio)
		}
	}
	return map[string]any{
		"label": "estimated", "method": "pi_consecutive_step_reusable_basis", "ranking_effect": "none",
		Baseline:  map[string]any{"eligible_count": eligible[Baseline], "ineligible_count": ineligible[Baseline], "distribution": exactRationalDistribution(values[Baseline])},
		Candidate: map[string]any{"eligible_count": eligible[Candidate], "ineligible_count": ineligible[Candidate], "distribution": exactRationalDistribution(values[Candidate])},
	}
}

func comparisonBlockingReasons(manifest Manifest, pairs int, stats, quality map[string]any) []string {
	reasons := []string{}
	if !supportsDirectCachedInputShare(manifest) {
		return []string{"unsupported_runtime_accounting"}
	}
	if pairs < 5 {
		reasons = append(reasons, "fewer_than_five_complete_pairs")
	}
	if pairs >= 5 && stats["interval"] == nil {
		reasons = append(reasons, "two_sided_95_percent_interval_unavailable")
	}
	if pairs >= 5 && stats["direction"] == "inconclusive" {
		reasons = append(reasons, "two_sided_95_percent_interval_contains_zero")
	}
	if pairs > 0 && quality["preserved"] != true {
		reasons = append(reasons, "quality_not_preserved")
	}
	return reasons
}

var comparisonReasonPrecedence = []string{
	"unsupported_runtime_accounting", "direct_evidence_incomplete", "cycle_pair_incomplete", "protocol_noncompliant",
	"identity_provenance_unstable", "runtime_emitted_identity_conflict", "fewer_than_five_complete_pairs",
	"two_sided_95_percent_interval_unavailable", "two_sided_95_percent_interval_contains_zero", "quality_not_preserved",
}

func orderComparisonReasons(reasons []string) []string {
	ordered := []string{}
	for _, expected := range comparisonReasonPrecedence {
		for _, reason := range reasons {
			if reason == expected {
				ordered = appendUnique(ordered, reason)
			}
		}
	}
	for _, reason := range reasons {
		ordered = appendUnique(ordered, reason)
	}
	return ordered
}

func executionOrder(attempt Attempt) int {
	if attempt.CacheControl == nil {
		return 0
	}
	return attempt.CacheControl.ExecutionOrder
}

func orderedSide(a, b Attempt, index int) string {
	order := []Attempt{a, b}
	if executionOrder(order[0]) > executionOrder(order[1]) {
		order[0], order[1] = order[1], order[0]
	}
	return order[index].Side
}

func attemptIDs(attempts []Attempt) []any {
	ids := make([]any, len(attempts))
	for i := range attempts {
		ids[i] = attempts[i].AttemptID
	}
	return ids
}

func appendUnique(values []string, additions ...string) []string {
	for _, addition := range additions {
		seen := false
		for _, value := range values {
			if value == addition {
				seen = true
				break
			}
		}
		if !seen {
			values = append(values, addition)
		}
	}
	return values
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func alternatingPolicy(policy map[string]any) bool {
	return policy != nil && policy["kind"] == "alternating_by_cycle"
}

func expectedCycleOrder(policy map[string]any, cycle int) ([]string, bool) {
	if !alternatingPolicy(policy) || cycle <= 0 {
		return nil, false
	}
	key := "first_cycle"
	if cycle%2 == 0 {
		key = "second_cycle"
	}
	switch values := policy[key].(type) {
	case []string:
		if len(values) == 2 {
			return append([]string(nil), values...), true
		}
	case []any:
		if len(values) == 2 {
			first, firstOK := values[0].(string)
			second, secondOK := values[1].(string)
			if firstOK && secondOK {
				return []string{first, second}, true
			}
		}
	}
	return nil, false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// studentTCritical95 returns the two-sided 95% critical value by inverting the
// Student-t CDF with deterministic bisection. The CDF uses the regularized
// incomplete beta representation and only standard-library arithmetic.
func studentTCritical95(df int) float64 {
	low, high := 0.0, 16.0
	for i := 0; i < 100; i++ {
		mid := (low + high) / 2
		if studentTCDF(mid, float64(df)) < .975 {
			low = mid
		} else {
			high = mid
		}
	}
	return (low + high) / 2
}

func studentTCDF(t, df float64) float64 {
	if t == 0 {
		return .5
	}
	x := df / (df + t*t)
	tail := .5 * regularizedBeta(x, df/2, .5)
	if t > 0 {
		return 1 - tail
	}
	return tail
}

func regularizedBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	lga, _ := math.Lgamma(a)
	lgb, _ := math.Lgamma(b)
	lgab, _ := math.Lgamma(a + b)
	front := math.Exp(lgab - lga - lgb + a*math.Log(x) + b*math.Log1p(-x))
	if x < (a+1)/(a+b+2) {
		return front * betaContinuedFraction(x, a, b) / a
	}
	return 1 - front*betaContinuedFraction(1-x, b, a)/b
}

func betaContinuedFraction(x, a, b float64) float64 {
	const tiny = 1e-300
	c, d := 1.0, 1-(a+b)*x/(a+1)
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= 200; m++ {
		mf := float64(m)
		aa := mf * (b - mf) * x / ((a + 2*mf - 1) * (a + 2*mf))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c
		aa = -(a + mf) * (a + b + mf) * x / ((a + 2*mf) * (a + 2*mf + 1))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1) < 3e-14 {
			break
		}
	}
	return h
}

func temporalOrderIsPlausible(first, second Attempt) bool {
	if first.EndedAt == "" || second.StartedAt == "" {
		return true
	}
	end, endErr := time.Parse(time.RFC3339Nano, first.EndedAt)
	start, startErr := time.Parse(time.RFC3339Nano, second.StartedAt)
	return endErr != nil || startErr != nil || !end.After(start)
}
