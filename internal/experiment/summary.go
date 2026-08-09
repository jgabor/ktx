package experiment

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func LoadAttempts(experimentDir string) ([]Attempt, error) {
	return loadJSONLines[Attempt](filepath.Join(experimentDir, "attempts.jsonl"))
}

func AppendAttempt(experimentDir string, attempt Attempt) error {
	return appendJSONLine(filepath.Join(experimentDir, "attempts.jsonl"), attempt)
}

func AttemptIsEvaluated(attempt Attempt, minimumToolCalls int) bool {
	return len(attempt.validationReasons) == 0 && attempt.Status == "completed" && attempt.SessionID != nil && attempt.StreamComplete && attempt.ToolCallCount >= minimumToolCalls
}

func AttemptIsFullPass(attempt Attempt, minimumToolCalls int) bool {
	return AttemptIsEvaluated(attempt, minimumToolCalls) && attempt.FullPass && (attempt.Role == "" || attempt.Role == MeasuredWarm)
}

func CountValid(attempts []Attempt, minimumToolCalls int) map[string]int {
	counts := map[string]int{Baseline: 0, Candidate: 0}
	for _, attempt := range attempts {
		if AttemptIsFullPass(attempt, minimumToolCalls) {
			counts[attempt.Side]++
		}
	}
	return counts
}

func CountValidForMethodology(attempts []Attempt, minimumToolCalls int, methodologyVersion string) map[string]int {
	if !isCacheAwareMethodology(methodologyVersion) {
		return CountValid(attempts, minimumToolCalls)
	}
	counts := map[string]int{Baseline: 0, Candidate: 0}
	for _, attempt := range attempts {
		if AttemptIsTargetValid(attempt, minimumToolCalls, methodologyVersion) {
			counts[attempt.Side]++
		}
	}
	return counts
}

func AttemptIsTargetValid(attempt Attempt, minimumToolCalls int, methodologyVersion string) bool {
	if isCacheAwareMethodology(methodologyVersion) && attempt.Role != MeasuredWarm {
		return false
	}
	if !isCacheAwareMethodology(methodologyVersion) {
		return AttemptIsFullPass(attempt, minimumToolCalls)
	}
	return AttemptIsEvaluated(attempt, minimumToolCalls) && attempt.FullPass
}

func mean(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	value := total / float64(len(values))
	return &value
}

func distribution(values []float64) map[string]any {
	if values == nil {
		values = []float64{}
	}
	sort.Float64s(values)
	result := map[string]any{"values": values, "min": nil, "max": nil, "mean": nil}
	if len(values) == 0 {
		return result
	}
	result["min"] = values[0]
	result["max"] = values[len(values)-1]
	result["mean"] = *mean(values)
	return result
}

type metricObservation struct {
	AttemptID string
	Cycle     int
	Value     float64
}

func quantile(values []float64, q float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	position := q * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	value := sorted[lower]
	if upper != lower {
		value += (sorted[upper] - sorted[lower]) * (position - float64(lower))
	}
	return &value
}

func populationStandardDeviation(values []float64) *float64 {
	average := mean(values)
	if average == nil {
		return nil
	}
	squaredDifferences := 0.0
	for _, value := range values {
		difference := value - *average
		squaredDifferences += difference * difference
	}
	value := math.Sqrt(squaredDifferences / float64(len(values)))
	return &value
}

func descriptiveDistribution(observations []metricObservation) map[string]any {
	sorted := append([]metricObservation(nil), observations...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Value != sorted[j].Value {
			return sorted[i].Value < sorted[j].Value
		}
		if sorted[i].Cycle != sorted[j].Cycle {
			return sorted[i].Cycle < sorted[j].Cycle
		}
		return sorted[i].AttemptID < sorted[j].AttemptID
	})
	values := make([]float64, len(sorted))
	for i, observation := range sorted {
		values[i] = observation.Value
	}
	result := map[string]any{
		"count": len(values), "values": values, "min": nil, "p25": nil,
		"median": nil, "mean": nil, "p75": nil, "max": nil,
		"population_standard_deviation": nil,
	}
	detection := map[string]any{"status": "insufficient_sample", "lower_fence": nil, "upper_fence": nil, "points": []any{}}
	result["outlier_detection"] = detection
	if len(values) == 0 {
		return result
	}
	p25, median, p75 := quantile(values, .25), quantile(values, .5), quantile(values, .75)
	result["min"], result["p25"], result["median"] = values[0], *p25, *median
	result["mean"], result["p75"], result["max"] = *mean(values), *p75, values[len(values)-1]
	result["population_standard_deviation"] = *populationStandardDeviation(values)
	if len(values) < 4 {
		return result
	}
	iqr := *p75 - *p25
	lowerFence, upperFence := *p25-1.5*iqr, *p75+1.5*iqr
	detection["status"], detection["lower_fence"], detection["upper_fence"] = "complete", lowerFence, upperFence
	points := []any{}
	for _, observation := range sorted {
		if observation.Value < lowerFence || observation.Value > upperFence {
			points = append(points, map[string]any{"attempt_id": observation.AttemptID, "cycle": observation.Cycle, "value": observation.Value})
		}
	}
	detection["points"] = points
	return result
}

func sampleClassification(count int) string {
	if count < 5 {
		return "exploratory"
	}
	return "descriptive"
}

func attemptMetric(attempt Attempt, field string) *float64 {
	switch field {
	case "tokens_in":
		return attempt.TokensIn
	case "cache_read":
		return attempt.CacheRead
	case "cache_write":
		return attempt.CacheWrite
	case "total_logical_input":
		if attempt.TokensIn == nil || attempt.CacheRead == nil || attempt.CacheWrite == nil {
			return nil
		}
		value := *attempt.TokensIn + *attempt.CacheRead + *attempt.CacheWrite
		return &value
	case "tokens_out":
		return attempt.TokensOut
	case "reasoning":
		return attempt.Reasoning
	case "cost":
		return attempt.Cost
	case "estimated_api_cost":
		return attempt.EstimatedAPICost
	case "tool_call_count":
		value := float64(attempt.ToolCallCount)
		return &value
	case "quality_score":
		value := attempt.Score.Score
		return &value
	default:
		return nil
	}
}

var descriptiveMetricFields = []string{
	"tokens_in", "cache_read", "cache_write", "total_logical_input", "tokens_out",
	"reasoning", "cost", "estimated_api_cost", "tool_call_count", "quality_score",
}

func summaryMetricFields(includeEstimated bool) []string {
	if includeEstimated {
		return descriptiveMetricFields
	}
	fields := make([]string, 0, len(descriptiveMetricFields)-1)
	for _, field := range descriptiveMetricFields {
		if field != "estimated_api_cost" {
			fields = append(fields, field)
		}
	}
	return fields
}

func observationsFor(attempts []Attempt, field string) []metricObservation {
	observations := []metricObservation{}
	for _, attempt := range attempts {
		if value := attemptMetric(attempt, field); value != nil {
			observations = append(observations, metricObservation{AttemptID: attempt.AttemptID, Cycle: attempt.Cycle, Value: *value})
		}
	}
	return observations
}

func pairedSummary(evaluated []Attempt, includeEstimated bool) map[string]any {
	sorted := append([]Attempt(nil), evaluated...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Cycle != sorted[j].Cycle {
			return sorted[i].Cycle < sorted[j].Cycle
		}
		if sorted[i].Side != sorted[j].Side {
			return sorted[i].Side == Baseline
		}
		return sorted[i].AttemptID < sorted[j].AttemptID
	})
	byCycle := map[int]map[string]Attempt{}
	cycleOrder := []int{}
	for _, attempt := range sorted {
		if _, ok := byCycle[attempt.Cycle]; !ok {
			byCycle[attempt.Cycle] = map[string]Attempt{}
			cycleOrder = append(cycleOrder, attempt.Cycle)
		}
		if _, exists := byCycle[attempt.Cycle][attempt.Side]; !exists {
			byCycle[attempt.Cycle][attempt.Side] = attempt
		}
	}
	cycles, unpaired := []any{}, []any{}
	metricDeltas := map[string][]float64{}
	for _, cycle := range cycleOrder {
		baseline, hasBaseline := byCycle[cycle][Baseline]
		candidate, hasCandidate := byCycle[cycle][Candidate]
		if !hasBaseline || !hasCandidate {
			for _, side := range Sides {
				if attempt, ok := byCycle[cycle][side]; ok {
					unpaired = append(unpaired, map[string]any{"attempt_id": attempt.AttemptID, "side": side, "cycle": cycle})
				}
			}
			continue
		}
		deltas := map[string]any{}
		for _, field := range summaryMetricFields(includeEstimated) {
			baselineValue, candidateValue := attemptMetric(baseline, field), attemptMetric(candidate, field)
			if baselineValue == nil || candidateValue == nil {
				deltas[field] = nil
				continue
			}
			delta := *baselineValue - *candidateValue
			deltas[field] = delta
			metricDeltas[field] = append(metricDeltas[field], delta)
		}
		cycles = append(cycles, map[string]any{
			"cycle": cycle, "baseline_attempt_id": baseline.AttemptID,
			"candidate_attempt_id": candidate.AttemptID, "deltas": deltas,
		})
	}
	metrics := map[string]any{}
	for _, field := range summaryMetricFields(includeEstimated) {
		values := metricDeltas[field]
		stats := map[string]any{"count": len(values), "mean_paired_delta": nil, "median_paired_delta": nil}
		if len(values) > 0 {
			stats["mean_paired_delta"] = *mean(values)
			stats["median_paired_delta"] = *quantile(values, .5)
		}
		metrics[field] = stats
	}
	return map[string]any{
		"paired_count": len(cycles), "sample_classification": sampleClassification(len(cycles)),
		"cycles": cycles, "unpaired_attempts": unpaired, "metrics": metrics,
	}
}

func exclusionReasons(attempt Attempt, minimumToolCalls int) []string {
	var reasons []string
	if attempt.Status != "completed" {
		reasons = append(reasons, "status="+attempt.Status)
	}
	if attempt.SessionID == nil {
		reasons = append(reasons, "missing session_id")
	}
	if !attempt.StreamComplete {
		reasons = append(reasons, "incomplete stream")
	}
	if AttemptIsEvaluated(attempt, minimumToolCalls) && !attempt.FullPass {
		reasons = append(reasons, "partial_or_failed_facts")
	} else if !attempt.AnswerMatchesExpected && !attempt.FullPass {
		reasons = append(reasons, "answer mismatch")
	}
	if attempt.ToolCallCount < minimumToolCalls {
		reasons = append(reasons, "too few tool calls")
	}
	return reasons
}

func BuildSummary(manifest Manifest, attempts []Attempt) map[string]any {
	minimum := manifest.Task.MinimumToolCalls
	methodologyVersion := manifest.Methodology.Version
	if methodologyVersion == "" {
		methodologyVersion = "v4-eval-matrix"
	}
	cacheAware := isCacheAwareMethodology(methodologyVersion)
	// Cache telemetry is optional evidence: a runtime that emits
	// part.tokens.cache.{read,write} on step_finish produces cache
	// counts on the attempts; a runtime that does not emits neither
	// and the summary stays on its legacy v1 shape. The v2 schema
	// bumps ONLY when at least one attempt under either side carries
	// cache evidence, so cache-less experiments regenerate their v1
	// summary byte-for-byte identically (the byte-match contract
	// ValidateExperiment relies on).
	hasCacheEvidence := false
	hasEstimatedCost := false
	for _, attempt := range attempts {
		if attempt.CacheRead != nil || attempt.CacheWrite != nil {
			hasCacheEvidence = true
		}
		if attempt.EstimatedAPICost != nil {
			hasEstimatedCost = true
		}
	}
	v3Required := hasEstimatedCost || cacheAware
	schemaVersion := 1
	if hasCacheEvidence {
		schemaVersion = 2
	}
	if v3Required {
		schemaVersion = 3
	}
	directCacheShare := supportsDirectCachedInputShare(manifest)
	promptCacheTokenHitRate := promptCacheMetricContractActivated(manifest)
	if directCacheShare {
		schemaVersion = 4
	}
	if promptCacheTokenHitRate {
		schemaVersion = 5
	}
	pipelines := map[string]any{}
	exclusions := []any{}
	allEvaluated := []Attempt{}
	for _, side := range Sides {
		var records, evaluated, counted []Attempt
		statusCounts := map[string]int{}
		for _, status := range AttemptStatuses {
			statusCounts[status] = 0
		}
		for _, attempt := range attempts {
			if attempt.Side != side {
				continue
			}
			records = append(records, attempt)
			if _, ok := statusCounts[attempt.Status]; ok {
				statusCounts[attempt.Status]++
			}
			headlineAttempt := !cacheAware || attempt.Role == MeasuredWarm
			if headlineAttempt && AttemptIsEvaluated(attempt, minimum) {
				evaluated = append(evaluated, attempt)
			}
			if headlineAttempt && AttemptIsTargetValid(attempt, minimum, methodologyVersion) {
				counted = append(counted, attempt)
			} else if headlineAttempt {
				exclusions = append(exclusions, map[string]any{"attempt_id": attempt.AttemptID, "side": side, "reasons": exclusionReasons(attempt, minimum)})
			}
		}
		contributing := make([]any, 0, len(counted))
		for _, attempt := range counted {
			contributing = append(contributing, map[string]any{"attempt_id": attempt.AttemptID, "session_id": *attempt.SessionID})
		}
		evaluatedRows := make([]any, 0, len(evaluated))
		var scores, tokensIn, tokensOut, costs, toolCalls []float64
		var cacheReads, cacheWrites, estimatedCosts []float64
		for _, attempt := range evaluated {
			allEvaluated = append(allEvaluated, attempt)
			evaluatedRows = append(evaluatedRows, map[string]any{"attempt_id": attempt.AttemptID, "session_id": *attempt.SessionID, "score": attempt.Score.Score})
			scores = append(scores, attempt.Score.Score)
			toolCalls = append(toolCalls, float64(attempt.ToolCallCount))
			if attempt.TokensIn != nil {
				tokensIn = append(tokensIn, *attempt.TokensIn)
			}
			if attempt.TokensOut != nil {
				tokensOut = append(tokensOut, *attempt.TokensOut)
			}
			if attempt.Cost != nil {
				costs = append(costs, *attempt.Cost)
			}
			if attempt.CacheRead != nil {
				cacheReads = append(cacheReads, *attempt.CacheRead)
			}
			if attempt.CacheWrite != nil {
				cacheWrites = append(cacheWrites, *attempt.CacheWrite)
			}
			if attempt.EstimatedAPICost != nil {
				estimatedCosts = append(estimatedCosts, *attempt.EstimatedAPICost)
			}
		}
		factScore := mean(scores)
		var passRate *float64
		if len(evaluated) > 0 {
			value := float64(len(counted)) / float64(len(evaluated))
			passRate = &value
		}
		costDistribution := distribution(costs)
		var expectedCost *float64
		if costMean, ok := costDistribution["mean"].(float64); ok && passRate != nil {
			denominator := *passRate
			if denominator < scoreEpsilon {
				denominator = scoreEpsilon
			}
			value := costMean / denominator
			expectedCost = &value
		}
		sideSummary := map[string]any{
			"attempt_count": len(records), "status_counts": statusCounts,
			"evaluated_count": len(evaluated), "valid_count": len(counted),
			"contributing_attempts": contributing, "evaluated_attempts": evaluatedRows,
			"tokens_in": distribution(tokensIn), "tokens_out": distribution(tokensOut),
			"cost": costDistribution, "tool_call_count": distribution(toolCalls),
			"mean_fact_score": factScore, "full_pass_rate": passRate, "pass_rate": passRate,
			"expected_cost_per_correct": expectedCost,
		}
		// Self-contained pipeline identity on the cache-bearing v2
		// summary: pipeline_id, stage_labels, and pipeline_hash are
		// surfaced per side so a reader of summary.json alone can
		// attribute cache/token/cost effects to the named strategy
		// without consulting the manifest. Cache distributions
		// join the existing token/cost distributions.
		if hasCacheEvidence || v3Required {
			pipeline := manifest.Pipelines[side]
			sideSummary["pipeline_id"] = pipeline.PipelineID
			sideSummary["stage_labels"] = pipeline.Stages
			sideSummary["pipeline_hash"] = pipeline.SHA256
		}
		if hasCacheEvidence {
			sideSummary["cache_read"] = distribution(cacheReads)
			sideSummary["cache_write"] = distribution(cacheWrites)
		}
		if hasEstimatedCost {
			estimatedDistribution := distribution(estimatedCosts)
			sideSummary["estimated_api_cost"] = estimatedDistribution
			var perCorrect *float64
			if estimatedMean, ok := estimatedDistribution["mean"].(float64); ok && passRate != nil {
				denominator := *passRate
				if denominator < scoreEpsilon {
					denominator = scoreEpsilon
				}
				value := estimatedMean / denominator
				perCorrect = &value
			}
			sideSummary["estimated_api_cost_per_correct"] = perCorrect
		}
		if v3Required {
			sideSummary["sample_classification"] = sampleClassification(len(evaluated))
			for _, field := range summaryMetricFields(hasEstimatedCost) {
				sideSummary[field] = descriptiveDistribution(observationsFor(evaluated, field))
			}
		}
		pipelines[side] = sideSummary
	}

	deltaFields := []string{"tokens_in", "tokens_out", "cost", "tool_call_count", "pass_rate", "mean_fact_score", "expected_cost_per_correct"}
	if hasCacheEvidence {
		deltaFields = append(deltaFields, "cache_read", "cache_write")
	}
	if hasEstimatedCost {
		deltaFields = append(deltaFields, "estimated_api_cost", "estimated_api_cost_per_correct")
	}
	deltas := map[string]any{}
	for _, field := range deltaFields {
		baseline := summaryValue(pipelines[Baseline].(map[string]any)[field])
		candidate := summaryValue(pipelines[Candidate].(map[string]any)[field])
		if baseline == nil || candidate == nil {
			deltas[field] = nil
		} else {
			deltas[field] = *baseline - *candidate
		}
	}
	baseTools := summaryValue(pipelines[Baseline].(map[string]any)["tool_call_count"])
	candidateTools := summaryValue(pipelines[Candidate].(map[string]any)["tool_call_count"])
	var inflation any
	if baseTools != nil && *baseTools != 0 && candidateTools != nil {
		inflation = *candidateTools / *baseTools
	}
	result := map[string]any{
		"schema_version": schemaVersion, "methodology_version": methodologyVersion,
		"experiment_id": manifest.ExperimentID, "target_valid_per_pipeline": manifest.TargetValidPerPipeline,
		"minimum_tool_calls": minimum, "pipelines": pipelines,
		"baseline_minus_candidate": deltas, "tool_call_inflation": inflation, "exclusions": exclusions,
	}
	if v3Required {
		result["paired_baseline_minus_candidate"] = pairedSummary(allEvaluated, hasEstimatedCost)
	}
	if hasEstimatedCost {
		result["estimated_api_cost_basis"] = map[string]any{
			"formula":                "(uncached_input*input_price + cache_read*cache_read_price + cache_write*cache_write_price + output*output_price + reasoning*reasoning_price)/1000000",
			"rounding_decimals":      6,
			"pricing_snapshot":       manifest.Pricing,
			"reasoning_price_policy": "use the selected base or context-tier reasoning price when present; otherwise use that selected record's output price",
			"null_policy":            "ordinary input/output prices and complete reasoning/cache usage are required; nonzero cache usage requires its corresponding cache price",
			"unavailable_guidance":   "run `ktx prices update` when the warm cache lacks required model or cache pricing",
		}
	}
	if cacheAware {
		result["headline_phase"] = MeasuredWarm
		result["cache_control"] = manifest.CacheControl
		result["phase_distributions"] = phaseDistributions(attempts, minimum)
		result["cold_to_warm_rebuild_penalty"] = rebuildPenalty(attempts, minimum)
		result["estimated_api_cost_break_even"] = estimatedCostBreakEven(attempts, minimum, manifest.CacheControl)
		result["compression_attribution"] = buildCompressionAttribution(manifest, attempts)
		result["ttft_ms"] = nil
	}
	if directCacheShare {
		direct := buildDirectCachedInputShare(manifest, attempts)
		secondary := buildSecondaryCacheEstimate(attempts)
		comparison := buildCachedInputShareComparison(manifest, attempts)
		result["direct_cached_input_share"] = direct
		result["estimated_consecutive_step_reuse"] = secondary
		result["cached_input_share_comparison"] = comparison
		result["statement_authorization"] = buildCacheStatementAuthorization(direct, secondary, comparison)
	}
	if promptCacheTokenHitRate {
		metric, comparison, authorization := buildPromptCacheTokenHitRateContract(manifest, attempts)
		result["prompt_cache_token_hit_rate"] = metric
		result["prompt_cache_token_hit_rate_comparison"] = comparison
		result["prompt_cache_token_hit_rate_statement_authorization"] = authorization
	}
	return result
}

func phaseDistributions(attempts []Attempt, minimum int) map[string]any {
	result := map[string]any{}
	for _, phase := range []string{ColdObservation, Warmup, MeasuredWarm} {
		sides := map[string]any{}
		for _, side := range Sides {
			selected := []Attempt{}
			for _, attempt := range attempts {
				if attempt.Side == side && attempt.Role == phase && AttemptIsEvaluated(attempt, minimum) {
					selected = append(selected, attempt)
				}
			}
			metrics := map[string]any{"attempt_count": len(selected), "sample_classification": sampleClassification(len(selected))}
			for _, field := range descriptiveMetricFields {
				metrics[field] = descriptiveDistribution(observationsFor(selected, field))
			}
			sides[side] = metrics
		}
		result[phase] = sides
	}
	return result
}

func rebuildPenalty(attempts []Attempt, minimum int) map[string]any {
	result := map[string]any{}
	for _, side := range Sides {
		var cold []float64
		var measured []float64
		for _, attempt := range attempts {
			if attempt.Side != side || !AttemptIsEvaluated(attempt, minimum) || attempt.EstimatedAPICost == nil {
				continue
			}
			switch attempt.Role {
			case ColdObservation:
				cold = append(cold, *attempt.EstimatedAPICost)
			case MeasuredWarm:
				measured = append(measured, *attempt.EstimatedAPICost)
			}
		}
		entry := map[string]any{"estimated_api_cost": nil, "reason": nil}
		switch {
		case len(cold) != 1:
			entry["reason"] = "cold_observation_estimated_api_cost_unavailable"
		case len(measured) == 0:
			entry["reason"] = "measured_warm_estimated_api_cost_unavailable"
		default:
			entry["estimated_api_cost"] = cold[0] - *mean(measured)
		}
		result[side] = entry
	}
	return result
}

func estimatedCostBreakEven(attempts []Attempt, minimum int, metadata *CacheControlManifest) map[string]any {
	unavailable := func(reason string) map[string]any {
		return map[string]any{"measured_turn": nil, "cycle": nil, "baseline_cumulative_estimated_api_cost": nil, "candidate_cumulative_estimated_api_cost": nil, "reason": reason}
	}
	warmups := 1
	if metadata != nil {
		warmups = metadata.WarmupCount
	}
	setup := map[string]float64{Baseline: 0, Candidate: 0}
	setupCount := map[string]int{Baseline: 0, Candidate: 0}
	byCycle := map[int]map[string]Attempt{}
	cycles := []int{}
	for _, attempt := range attempts {
		if !AttemptIsEvaluated(attempt, minimum) {
			continue
		}
		if attempt.Role == ColdObservation || attempt.Role == Warmup {
			if attempt.EstimatedAPICost == nil {
				return unavailable("estimated_api_cost_unavailable")
			}
			setup[attempt.Side] += *attempt.EstimatedAPICost
			setupCount[attempt.Side]++
		}
		if attempt.Role == MeasuredWarm {
			if _, ok := byCycle[attempt.Cycle]; !ok {
				byCycle[attempt.Cycle] = map[string]Attempt{}
				cycles = append(cycles, attempt.Cycle)
			}
			if _, exists := byCycle[attempt.Cycle][attempt.Side]; !exists {
				byCycle[attempt.Cycle][attempt.Side] = attempt
			}
		}
	}
	for _, side := range Sides {
		if setupCount[side] != 1+warmups {
			return unavailable("setup_estimated_api_cost_unavailable")
		}
	}
	sort.Ints(cycles)
	baseTotal, candidateTotal := setup[Baseline], setup[Candidate]
	turn := 0
	for _, cycle := range cycles {
		base, baseOK := byCycle[cycle][Baseline]
		candidate, candidateOK := byCycle[cycle][Candidate]
		if !baseOK || !candidateOK {
			continue
		}
		if base.EstimatedAPICost == nil || candidate.EstimatedAPICost == nil {
			return unavailable("estimated_api_cost_unavailable")
		}
		turn++
		baseTotal += *base.EstimatedAPICost
		candidateTotal += *candidate.EstimatedAPICost
		if candidateTotal < baseTotal {
			return map[string]any{"measured_turn": turn, "cycle": cycle, "baseline_cumulative_estimated_api_cost": baseTotal, "candidate_cumulative_estimated_api_cost": candidateTotal, "reason": nil}
		}
	}
	if turn == 0 {
		return unavailable("paired_measured_estimated_api_cost_unavailable")
	}
	return map[string]any{"measured_turn": nil, "cycle": nil, "baseline_cumulative_estimated_api_cost": baseTotal, "candidate_cumulative_estimated_api_cost": candidateTotal, "reason": "candidate_not_lower_within_measured_sample"}
}

func summaryValue(value any) *float64 {
	if object, ok := value.(map[string]any); ok {
		value = object["mean"]
	}
	switch number := value.(type) {
	case float64:
		return &number
	case *float64:
		return number
	default:
		return nil
	}
}

type SummaryRegeneration struct {
	Summary               map[string]any
	ProjectionDivergences []ProjectionDivergence
}

func RegenerateSummary(experimentDir string) (SummaryRegeneration, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(experimentDir, "manifest.json"))
	if err != nil {
		return SummaryRegeneration{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return SummaryRegeneration{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := validateCacheMetricContractActivation(manifest); err != nil {
		return SummaryRegeneration{}, err
	}
	attempts, err := LoadAttempts(experimentDir)
	if err != nil {
		return SummaryRegeneration{}, err
	}
	attempts, divergences, err := materializeAttempts(experimentDir, manifest, attempts)
	if err != nil {
		return SummaryRegeneration{}, err
	}
	existingSummary, readSummaryErr := os.ReadFile(filepath.Join(experimentDir, "summary.json"))
	if readSummaryErr != nil && !os.IsNotExist(readSummaryErr) {
		return SummaryRegeneration{}, readSummaryErr
	}
	preserveHistoricalSummarySchema(&manifest, existingSummary)
	pricing, err := preparePricingEvidence(experimentDir, manifest, attempts, existingSummary)
	if err != nil {
		return SummaryRegeneration{}, err
	}
	attempts = estimateAttempts(attempts, pricing)
	summary := BuildSummary(manifest, attempts)
	if err := WriteJSON(filepath.Join(experimentDir, "summary.json"), summary); err != nil {
		return SummaryRegeneration{}, err
	}
	return SummaryRegeneration{Summary: summary, ProjectionDivergences: divergences}, nil
}

func preserveHistoricalSummarySchema(manifest *Manifest, summary []byte) {
	var existing struct {
		SchemaVersion int `json:"schema_version"`
	}
	if json.Unmarshal(summary, &existing) == nil && existing.SchemaVersion > 0 && existing.SchemaVersion <= 4 {
		manifest.historicalSummarySchema = existing.SchemaVersion
	}
}

func WriteSummary(experimentDir string) (map[string]any, error) {
	regeneration, err := RegenerateSummary(experimentDir)
	return regeneration.Summary, err
}

func RenderSummary(summary map[string]any) string {
	lines := []string{"side       full  eval  attempts  avg tokens in  avg cost    fact score  $/correct", "---------  ----  ----  --------  -------------  ----------  ----------  ---------"}
	pipelines, _ := summary["pipelines"].(map[string]any)
	for _, side := range Sides {
		details, _ := pipelines[side].(map[string]any)
		lines = append(lines, fmt.Sprintf("%-9s  %4v  %4v  %8v  %13s  %10s  %10s  %9s",
			side, details["valid_count"], details["evaluated_count"], details["attempt_count"],
			formatValue(summaryValue(details["tokens_in"])), formatValue(summaryValue(details["cost"])),
			formatValue(summaryValue(details["mean_fact_score"])), formatValue(summaryValue(details["expected_cost_per_correct"]))))
		if classification, ok := details["sample_classification"].(string); ok {
			lines = append(lines, fmt.Sprintf("sample %s=%s (n=%v)", side, classification, details["evaluated_count"]))
			for _, field := range descriptiveMetricFields {
				distribution, _ := details[field].(map[string]any)
				detection, _ := distribution["outlier_detection"].(map[string]any)
				points, _ := detection["points"].([]any)
				for _, rawPoint := range points {
					point, _ := rawPoint.(map[string]any)
					lines = append(lines, fmt.Sprintf("outlier %s.%s attempt=%v cycle=%v value=%v", side, field, point["attempt_id"], point["cycle"], point["value"]))
				}
			}
		}
	}
	if paired, ok := summary["paired_baseline_minus_candidate"].(map[string]any); ok {
		lines = append(lines, fmt.Sprintf("paired n=%v sample=%v", paired["paired_count"], paired["sample_classification"]))
	}
	if inflation := summaryValue(summary["tool_call_inflation"]); inflation != nil {
		lines = append(lines, fmt.Sprintf("tool_call_inflation=%v", *inflation))
	}
	if attribution, ok := summary["compression_attribution"].(map[string]any); ok {
		lines = append(lines, "attribution: evidence classes are parallel and non-additive; no cross-field total")
		classes, _ := attribution["evidence_classes"].(map[string]any)
		for _, name := range []string{"tool_delivery", "runtime_usage", "quality", "runtime_reported_cost", "estimated_cost"} {
			class, _ := classes[name].(map[string]any)
			observations, _ := class["observations"].([]any)
			for _, raw := range observations {
				observation, _ := raw.(map[string]any)
				lines = append(lines, fmt.Sprintf("evidence %s attempt=%v side=%v phase=%v unit=%v value=%s authority=%v provenance=%s",
					name, observation["attempt_id"], observation["side"], observation["phase"], class["comparison_unit"], compactJSON(observation["value"]), class["authority"], compactJSON(observation["provenance"])))
			}
		}
		logicalInputFormula := "uncached_input+cache_read"
		if _, ok := summary["direct_cached_input_share"]; ok {
			logicalInputFormula = "uncached_input+cache_read+cache_write"
		} else if _, ok := summary["prompt_cache_token_hit_rate"]; ok {
			logicalInputFormula = "uncached_input+cache_read+cache_write"
		}
		lines = append(lines,
			"derived logical_input="+logicalInputFormula+" (runtime-emitted tokens per attempt)",
			"derived estimated_cost_break_even=setup estimated cost plus paired measured turns (cumulative price-derived USD by measured turn)",
			"unavailable provider cache hits/misses/fills, request count/boundaries, isolation, eviction, prefix composition, runtime-cost billing semantics",
			"interpretation: correlated delivery and runtime movement does not identify causal shares or provider-internal behavior")
	}
	if direct, ok := summary["direct_cached_input_share"].(map[string]any); ok {
		for _, raw := range direct["attempts"].([]any) {
			attempt := raw.(map[string]any)
			identity := attempt["identity_provenance"].(map[string]any)
			runtime := identity["runtime"].(map[string]any)
			configured := identity["configured_attempt"].(map[string]any)
			source := attempt["source"].(map[string]any)
			lines = append(lines, fmt.Sprintf("direct_cached_input_share attempt=%v state=%v share=%v cache_read=%v logical_input=%v uncached_input=%v cache_write=%v formula=%v unit=%v source=%v runtime=%v/%v configured_identity=%v variant=%v provider=%v source_event_identities=%s configured_identity_backfills_source_events=%v purpose=descriptive_non_ranking",
				attempt["attempt_id"], attempt["share"].(map[string]any)["state"], attempt["share"].(map[string]any)["value"],
				attempt["cache_read"].(map[string]any)["value"], attempt["logical_input"].(map[string]any)["value"],
				attempt["uncached_input"].(map[string]any)["value"], attempt["cache_write"].(map[string]any)["value"],
				attempt["formula"], attempt["unit"], source["artifact"], runtime["name"], runtime["version"],
				configured["model"], configured["variant"], configured["provider"], compactJSON(identity["source_events"]),
				identity["configured_identity_backfills_source_events"]))
		}
	}
	if estimate, ok := summary["estimated_consecutive_step_reuse"].(map[string]any); ok {
		lines = append(lines, fmt.Sprintf("estimated_consecutive_step_reuse label=%v method=%v eligible=%v ineligible=%v ineligible_counts=%s noise_floor_tokens=%v ranking_effect=%v ttl_cause=%v expiry_cause=%v",
			estimate["label"], estimate["method"], estimate["eligible_count"], estimate["ineligible_count"], compactJSON(estimate["ineligible_counts"]),
			estimate["noise_floor_tokens"], estimate["ranking_effect"], estimate["ttl_cause"], estimate["expiry_cause"]))
		for _, rawAttempt := range estimate["attempts"].([]any) {
			attempt := rawAttempt.(map[string]any)
			for _, rawPair := range attempt["pairs"].([]any) {
				pair := rawPair.(map[string]any)
				lines = append(lines, fmt.Sprintf("estimated_reuse_pair attempt=%v prior=%v current=%v eligible=%v reason=%v reusable_tokens=%v shortfall=%v reuse_share=%s interpretation=%v source=%v",
					attempt["attempt_id"], pair["prior_source_event_ordinal"], pair["current_source_event_ordinal"], pair["eligible"], pair["reason"],
					pair["estimated_reusable_tokens"], pair["shortfall"], compactJSON(pair["reuse_share"]), pair["interpretation"], attempt["source"].(map[string]any)["artifact"]))
			}
		}
	}
	if comparison, ok := summary["cached_input_share_comparison"].(map[string]any); ok {
		direct := comparison["direct"].(map[string]any)
		paired := direct["paired_difference"].(map[string]any)
		lines = append(lines, fmt.Sprintf("cached_input_share_comparison complete_pairs=%v mean_difference=%s interval=%s direction=%v descriptive_only=%v quality_preserved=%v blocking_reasons=%s",
			comparison["complete_pair_count"], compactJSON(paired["mean"]), compactJSON(paired["interval"]), paired["direction"], paired["descriptive_only"],
			comparison["quality"].(map[string]any)["preserved"], compactJSON(comparison["blocking_reasons"])))
	}
	if authorization, ok := summary["statement_authorization"].(map[string]any); ok {
		identity := anyMap(authorization["identity_provenance"])
		lines = append(lines, fmt.Sprintf("statement_identity configured_context_classification=%v source_identity_classification=%v stable_nonconflicting=%v conflicts=%s configured_identity_backfills_source_events=%v",
			identity["configured_context_classification"], identity["source_identity_classification"], identity["stable_nonconflicting"], compactJSON(identity["source_identity_conflicts"]), identity["configured_identity_backfills_source_events"]))
		outcomes := anyMap(authorization["outcomes"])
		for _, name := range []string{"direct_available", "comparative_descriptive", "candidate_higher_success", "candidate_lower", "inconclusive", "secondary_available"} {
			outcome := anyMap(outcomes[name])
			lines = append(lines, fmt.Sprintf("statement_authorization outcome=%s authorized=%v reasons=%s gates=%s evidence=%s scope=%v statement=%s",
				name, outcome["authorized"], compactJSON(outcome["reasons"]), compactJSON(outcome["gates"]), compactJSON(outcome["evidence"]), outcome["scope"], compactJSON(outcome["statement"])))
		}
	}
	if comparison, ok := summary["prompt_cache_token_hit_rate_comparison"].(map[string]any); ok {
		quality := anyMap(comparison["quality"])
		lines = append(lines, fmt.Sprintf("prompt_cache_token_hit_rate_comparison complete_pairs=%v baseline_mean=%s candidate_mean=%s percentage_point_delta=%s interval=%s quality_preserved=%v blocking_reasons=%s",
			comparison["complete_pair_count"], compactJSON(comparison["baseline_mean"]), compactJSON(comparison["candidate_mean"]),
			compactJSON(comparison["percentage_point_delta"]), compactJSON(comparison["interval"]), quality["preserved"], compactJSON(comparison["blocking_reasons"])))
		if authorization, ok := summary["prompt_cache_token_hit_rate_statement_authorization"].(map[string]any); ok {
			for _, outcomeName := range []string{"higher", "lower"} {
				outcome := anyMap(anyMap(authorization["outcomes"])[outcomeName])
				if outcome["authorized"] == true {
					lines = append(lines, fmt.Sprintf("prompt_cache_token_hit_rate_statement outcome=%s statement=%s", outcomeName, outcome["statement"]))
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

func compactJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func formatValue(value *float64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%v", *value)
}
