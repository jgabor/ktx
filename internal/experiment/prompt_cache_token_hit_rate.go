package experiment

import (
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"reflect"
	"sort"

	"github.com/jgabor/ktx/internal/cachemetric"
)

const promptCacheTokenHitRateSource = "retained_runtime_model_step_telemetry"

const (
	promptCacheTokenHitRateBootstrapMethod     = "paired_cluster_bootstrap_percentile"
	promptCacheTokenHitRateBootstrapReplicates = 10000
	promptCacheTokenHitRateBootstrapSeed       = int64(1)
)

type promptCacheTokenBuckets struct {
	uncachedInput *big.Int
	cacheRead     *big.Int
	cacheWrite    *big.Int
}

type promptCacheTokenPairBuckets struct {
	cycle               int
	baseline, candidate promptCacheTokenBuckets
}

var promptCacheTokenHitRateReasonPrecedence = []string{
	"unsupported_runtime_accounting", "direct_evidence_incomplete", "evidence_projection_inconsistent",
	"cycle_pair_incomplete", "protocol_noncompliant", "identity_provenance_unstable",
	"runtime_emitted_identity_missing", "runtime_emitted_identity_conflict", "fewer_than_five_complete_pairs",
	"two_sided_95_percent_interval_unavailable", "two_sided_95_percent_interval_contains_zero",
	"aggregate_rate_tie", "aggregate_direction_interval_mismatch",
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
	comparison := promptCacheTokenHitRateComparisonFromRaw(direct, rawComparison)
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

func promptCacheTokenHitRateComparisonFromRaw(direct, raw map[string]any) map[string]any {
	baseline, candidate, aggregateBuckets, pairedDifference, aggregateValid := promptCacheTokenHitRateAggregateComparison(direct, raw)
	blocking := promptCacheTokenHitRateBlockingReasons(raw, pairedDifference, aggregateValid)
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
		"blocking_reasons": blocking, "unavailable_reasons": append([]string{}, blocking...),
		"aggregate_buckets": aggregateBuckets, "raw_pair_difference": pairedDifference,
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
		"aggregate_buckets": nil, "raw_pair_difference": promptCacheTokenHitRatePairedStatistics(nil),
	}
}

func promptCacheTokenHitRateAggregateComparison(direct, raw map[string]any) (any, any, any, map[string]any, bool) {
	emptyStatistics := promptCacheTokenHitRatePairedStatistics(nil)
	pairRows, pairsOK := sliceValue(raw["pairs"])
	pairCount, pairCountOK := exactNonnegativeInt(raw["complete_pair_count"])
	directRows, directOK := promptCacheTokenDirectAttemptsByID(direct)
	if !pairsOK || !pairCountOK || pairCount != len(pairRows) || !directOK {
		return nil, nil, nil, emptyStatistics, false
	}
	pairs := make([]promptCacheTokenPairBuckets, 0, len(pairRows))
	seenCycles := map[int]bool{}
	for _, value := range pairRows {
		pair, pairOK := mapValue(value)
		cycle, cycleOK := exactPositiveInt(pair["cycle"])
		if !pairOK || !cycleOK || seenCycles[cycle] {
			return nil, nil, nil, emptyStatistics, false
		}
		seenCycles[cycle] = true
		baseline, baselineOK := promptCacheTokenBucketsForPairedAttempt(pair[Baseline], directRows, Baseline, cycle)
		candidate, candidateOK := promptCacheTokenBucketsForPairedAttempt(pair[Candidate], directRows, Candidate, cycle)
		if !baselineOK || !candidateOK {
			return nil, nil, nil, emptyStatistics, false
		}
		pairs = append(pairs, promptCacheTokenPairBuckets{cycle: cycle, baseline: baseline, candidate: candidate})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].cycle < pairs[j].cycle })
	if len(pairs) == 0 {
		return nil, nil, nil, emptyStatistics, true
	}

	baselineTotals, candidateTotals := newPromptCacheTokenBuckets(), newPromptCacheTokenBuckets()
	for _, pair := range pairs {
		baselineTotals.add(pair.baseline)
		candidateTotals.add(pair.candidate)
	}
	_, baselineRate, baselineOK := promptCacheTokenBucketRate(baselineTotals)
	_, candidateRate, candidateOK := promptCacheTokenBucketRate(candidateTotals)
	if !baselineOK || !candidateOK {
		return nil, nil, nil, emptyStatistics, false
	}
	return rationalView(baselineRate), rationalView(candidateRate), map[string]any{
		"basis":  "sum_disjoint_token_buckets_over_complete_pairs",
		Baseline: promptCacheTokenBucketView(baselineTotals), Candidate: promptCacheTokenBucketView(candidateTotals),
	}, promptCacheTokenHitRatePairedStatistics(pairs), true
}

func promptCacheTokenDirectAttemptsByID(direct map[string]any) (map[string]map[string]any, bool) {
	values, ok := sliceValue(direct["attempts"])
	if !ok {
		return nil, false
	}
	rows := make(map[string]map[string]any, len(values))
	for _, value := range values {
		row, rowOK := mapValue(value)
		attemptID, idOK := nonemptyString(row["attempt_id"])
		if !rowOK || !idOK || rows[attemptID] != nil {
			return nil, false
		}
		rows[attemptID] = row
	}
	return rows, true
}

func promptCacheTokenBucketsForPairedAttempt(value any, directRows map[string]map[string]any, side string, cycle int) (promptCacheTokenBuckets, bool) {
	pairRow, pairOK := mapValue(value)
	attemptID, idOK := nonemptyString(pairRow["attempt_id"])
	pairCycle, pairCycleOK := exactPositiveInt(pairRow["cycle"])
	directRow, directOK := directRows[attemptID]
	directCycle, directCycleOK := exactPositiveInt(directRow["cycle"])
	if !pairOK || !idOK || !pairCycleOK || pairCycle != cycle || stringValue(pairRow["side"]) != side || stringValue(pairRow["phase"]) != MeasuredWarm ||
		!directOK || !directCycleOK || directCycle != cycle || stringValue(directRow["side"]) != side || stringValue(directRow["phase"]) != MeasuredWarm {
		return promptCacheTokenBuckets{}, false
	}
	pairBuckets, pairBucketsOK := promptCacheTokenBucketsFromPairAttempt(pairRow)
	directBuckets, directBucketsOK := promptCacheTokenBucketsFromDirectAttempt(directRow)
	if !pairBucketsOK || !directBucketsOK || !promptCacheTokenBucketsEqual(pairBuckets, directBuckets) {
		return promptCacheTokenBuckets{}, false
	}
	return directBuckets, true
}

func promptCacheTokenBucketsFromPairAttempt(value any) (promptCacheTokenBuckets, bool) {
	row, rowOK := mapValue(value)
	rawUsage, usageOK := mapValue(row["raw_usage"])
	if !rowOK || !usageOK {
		return promptCacheTokenBuckets{}, false
	}
	return promptCacheTokenBucketsFromMeasurements(rawUsage, row["direct_share"])
}

func promptCacheTokenBucketsFromDirectAttempt(row map[string]any) (promptCacheTokenBuckets, bool) {
	return promptCacheTokenBucketsFromMeasurements(row, row["share"])
}

func promptCacheTokenBucketsFromMeasurements(measurements map[string]any, declaredShare any) (promptCacheTokenBuckets, bool) {
	uncached, uncachedOK := promptCacheTokenMeasuredInteger(measurements["uncached_input"])
	read, readOK := promptCacheTokenMeasuredInteger(measurements["cache_read"])
	write, writeOK := promptCacheTokenMeasuredInteger(measurements["cache_write"])
	logical, rate, measured := cachemetric.PromptCacheTokenHitRate(uncached, read, write)
	declaredLogical, logicalOK := promptCacheTokenMeasuredInteger(measurements["logical_input"])
	declaredRate, rateOK := parseMeasuredShare(declaredShare)
	if !uncachedOK || !readOK || !writeOK || !measured || !logicalOK || logical.Cmp(declaredLogical) != 0 || !rateOK || rate.Cmp(declaredRate) != 0 {
		return promptCacheTokenBuckets{}, false
	}
	return promptCacheTokenBuckets{uncachedInput: uncached, cacheRead: read, cacheWrite: write}, true
}

func promptCacheTokenBucketsEqual(left, right promptCacheTokenBuckets) bool {
	return left.uncachedInput.Cmp(right.uncachedInput) == 0 && left.cacheRead.Cmp(right.cacheRead) == 0 && left.cacheWrite.Cmp(right.cacheWrite) == 0
}

func promptCacheTokenMeasuredInteger(value any) (*big.Int, bool) {
	measurement, measurementOK := mapValue(value)
	if !measurementOK || stringValue(measurement["state"]) != "measured" {
		return nil, false
	}
	var result *big.Int
	switch number := measurement["value"].(type) {
	case *big.Int:
		if number != nil {
			result = new(big.Int).Set(number)
		}
	case big.Int:
		result = new(big.Int).Set(&number)
	case int:
		result = big.NewInt(int64(number))
	case int64:
		result = big.NewInt(number)
	case uint:
		result = new(big.Int).SetUint64(uint64(number))
	case uint64:
		result = new(big.Int).SetUint64(number)
	}
	return result, result != nil && result.Sign() >= 0
}

func newPromptCacheTokenBuckets() promptCacheTokenBuckets {
	return promptCacheTokenBuckets{uncachedInput: new(big.Int), cacheRead: new(big.Int), cacheWrite: new(big.Int)}
}

func (buckets *promptCacheTokenBuckets) add(other promptCacheTokenBuckets) {
	buckets.uncachedInput.Add(buckets.uncachedInput, other.uncachedInput)
	buckets.cacheRead.Add(buckets.cacheRead, other.cacheRead)
	buckets.cacheWrite.Add(buckets.cacheWrite, other.cacheWrite)
}

func promptCacheTokenBucketRate(buckets promptCacheTokenBuckets) (*big.Int, *big.Rat, bool) {
	return cachemetric.PromptCacheTokenHitRate(buckets.uncachedInput, buckets.cacheRead, buckets.cacheWrite)
}

func promptCacheTokenBucketView(buckets promptCacheTokenBuckets) map[string]any {
	logical, _, _ := promptCacheTokenBucketRate(buckets)
	return map[string]any{
		"uncached_input": new(big.Int).Set(buckets.uncachedInput), "cache_read": new(big.Int).Set(buckets.cacheRead),
		"cache_write": new(big.Int).Set(buckets.cacheWrite), "logical_input": logical,
	}
}

func promptCacheTokenHitRatePairedStatistics(pairs []promptCacheTokenPairBuckets) map[string]any {
	result := map[string]any{
		"count": len(pairs), "definition": "candidate_aggregate_token_hit_rate - baseline_aggregate_token_hit_rate",
		"method": promptCacheTokenHitRateBootstrapMethod, "coverage": .95, "sides": 2,
		"mean": nil, "sample_standard_deviation": nil, "interval": nil, "direction": "unavailable",
		"descriptive_only": len(pairs) < 5,
		"aggregation":      "sum(cache_read) / sum(uncached_input + cache_read + cache_write) by side over complete pairs",
		"resampling": map[string]any{
			"unit": "complete_pair_disjoint_token_buckets", "replicates": promptCacheTokenHitRateBootstrapReplicates,
			"seed": promptCacheTokenHitRateBootstrapSeed,
		},
	}
	if len(pairs) == 0 {
		return result
	}
	baseline, candidate := newPromptCacheTokenBuckets(), newPromptCacheTokenBuckets()
	for _, pair := range pairs {
		baseline.add(pair.baseline)
		candidate.add(pair.candidate)
	}
	_, baselineRate, baselineOK := promptCacheTokenBucketRate(baseline)
	_, candidateRate, candidateOK := promptCacheTokenBucketRate(candidate)
	if !baselineOK || !candidateOK {
		return result
	}
	difference := new(big.Rat).Sub(candidateRate, baselineRate)
	result["mean"] = rationalView(difference)
	if len(pairs) < 5 {
		return result
	}

	distribution := make([]float64, 0, promptCacheTokenHitRateBootstrapReplicates)
	random := rand.New(rand.NewSource(promptCacheTokenHitRateBootstrapSeed))
	for replicate := 0; replicate < promptCacheTokenHitRateBootstrapReplicates; replicate++ {
		baseline, candidate := newPromptCacheTokenBuckets(), newPromptCacheTokenBuckets()
		for draw := 0; draw < len(pairs); draw++ {
			pair := pairs[random.Intn(len(pairs))]
			baseline.add(pair.baseline)
			candidate.add(pair.candidate)
		}
		_, baselineRate, baselineOK := promptCacheTokenBucketRate(baseline)
		_, candidateRate, candidateOK := promptCacheTokenBucketRate(candidate)
		if !baselineOK || !candidateOK {
			return result
		}
		value, _ := new(big.Rat).Sub(candidateRate, baselineRate).Float64()
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return result
		}
		distribution = append(distribution, value)
	}
	lower, upper := quantile(distribution, .025), quantile(distribution, .975)
	if lower == nil || upper == nil || math.IsNaN(*lower) || math.IsInf(*lower, 0) || math.IsNaN(*upper) || math.IsInf(*upper, 0) {
		return result
	}
	result["interval"] = map[string]any{"lower": *lower, "upper": *upper}
	if difference.Sign() > 0 && *lower > 0 {
		result["direction"] = "higher"
	} else if difference.Sign() < 0 && *upper < 0 {
		result["direction"] = "lower"
	} else {
		result["direction"] = "inconclusive"
	}
	return result
}

func promptCacheTokenHitRateBlockingReasons(raw, statistics map[string]any, aggregateValid bool) []string {
	blocking, blockingOK := strictStringSlice(raw["blocking_reasons"])
	result := []string{}
	if !blockingOK {
		result = append(result, "evidence_projection_inconsistent")
	}
	for _, reason := range blocking {
		switch reason {
		case "fewer_than_five_complete_pairs", "two_sided_95_percent_interval_unavailable", "two_sided_95_percent_interval_contains_zero", "quality_not_preserved":
			continue
		default:
			result = appendUnique(result, reason)
		}
	}
	pairCount, pairCountOK := exactNonnegativeInt(raw["complete_pair_count"])
	statisticsCount, statisticsCountOK := exactNonnegativeInt(statistics["count"])
	if !aggregateValid || !pairCountOK || !statisticsCountOK || pairCount != statisticsCount {
		result = appendUnique(result, "evidence_projection_inconsistent")
	}
	if pairCountOK && pairCount < 5 {
		result = appendUnique(result, "fewer_than_five_complete_pairs")
	}
	if pairCountOK && pairCount >= 5 {
		if _, intervalOK := mapValue(statistics["interval"]); !intervalOK {
			result = appendUnique(result, "two_sided_95_percent_interval_unavailable")
		} else {
			switch stringValue(statistics["direction"]) {
			case "higher", "lower":
			case "inconclusive":
				result = appendUnique(result, promptCacheTokenHitRateInconclusiveReason(statistics))
			default:
				result = appendUnique(result, "two_sided_95_percent_interval_unavailable")
			}
		}
	}
	quality, qualityOK := mapValue(raw["quality"])
	if !qualityOK {
		result = appendUnique(result, "evidence_projection_inconsistent")
	} else if pairCountOK && pairCount > 0 && quality["preserved"] != true {
		result = appendUnique(result, "quality_not_preserved")
	}
	return orderPromptCacheTokenHitRateReasons(result)
}

func promptCacheTokenHitRateInconclusiveReason(statistics map[string]any) string {
	mean, meanOK := parseRationalView(anyMap(statistics["mean"]))
	interval, intervalOK := mapValue(statistics["interval"])
	lower, lowerOK := finiteNumber(interval["lower"])
	upper, upperOK := finiteNumber(interval["upper"])
	if !meanOK || !intervalOK || !lowerOK || !upperOK || lower > upper {
		return "two_sided_95_percent_interval_unavailable"
	}
	if lower <= 0 && upper >= 0 {
		return "two_sided_95_percent_interval_contains_zero"
	}
	if mean.Sign() == 0 {
		return "aggregate_rate_tie"
	}
	return "aggregate_direction_interval_mismatch"
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
	result := map[string]any{
		"method": statistics["method"], "coverage": statistics["coverage"], "sides": statistics["sides"],
		"unit": "percentage_points", "lower": lower * 100, "upper": upper * 100,
	}
	if resampling, ok := mapValue(statistics["resampling"]); ok {
		result["resampling"] = copyPromptCacheMap(resampling)
	}
	return result
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
	comparisonProjectionMatches := reflect.DeepEqual(comparison, promptCacheTokenHitRateComparisonFromRaw(direct, rawComparison))
	rawEvidenceAuthorization := buildCacheStatementAuthorization(direct, secondary, rawComparison)
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
			"higher": promptCacheTokenHitRateDirectionalOutcome("higher", rawEvidenceAuthorization, blockers, blockersValid, pairCount, pairCountValid, quality, difference, baseGates),
			"lower":  promptCacheTokenHitRateDirectionalOutcome("lower", rawEvidenceAuthorization, blockers, blockersValid, pairCount, pairCountValid, quality, difference, baseGates),
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

func promptCacheTokenHitRateDirectionalOutcome(direction string, rawEvidenceAuthorization map[string]any, blockers []string, blockersValid bool, pairCount int, pairCountValid bool, quality, difference, baseGates map[string]any) map[string]any {
	pairedEvidence := anyMap(anyMap(rawEvidenceAuthorization["outcomes"])["comparative_descriptive"])
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
	if pairedEvidence["authorized"] != true {
		reasons = appendUnique(reasons, stringSlice(pairedEvidence["reasons"])...)
	}
	if stringValue(difference["direction"]) != direction {
		if stringValue(difference["direction"]) == "inconclusive" {
			reasons = appendUnique(reasons, promptCacheTokenHitRateInconclusiveReason(difference))
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
	gates["paired_evidence_authorization"] = pairedEvidence["authorized"] == true
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
