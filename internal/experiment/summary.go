package experiment

import (
	"encoding/json"
	"fmt"
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
	return attempt.Status == "completed" && attempt.SessionID != nil && attempt.StreamComplete && attempt.ToolCallCount >= minimumToolCalls
}

func AttemptIsFullPass(attempt Attempt, minimumToolCalls int) bool {
	return AttemptIsEvaluated(attempt, minimumToolCalls) && attempt.FullPass
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
	pipelines := map[string]any{}
	exclusions := []any{}
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
			if AttemptIsEvaluated(attempt, minimum) {
				evaluated = append(evaluated, attempt)
			}
			if AttemptIsFullPass(attempt, minimum) {
				counted = append(counted, attempt)
			} else {
				exclusions = append(exclusions, map[string]any{"attempt_id": attempt.AttemptID, "side": side, "reasons": exclusionReasons(attempt, minimum)})
			}
		}
		contributing := make([]any, 0, len(counted))
		for _, attempt := range counted {
			contributing = append(contributing, map[string]any{"attempt_id": attempt.AttemptID, "session_id": *attempt.SessionID})
		}
		evaluatedRows := make([]any, 0, len(evaluated))
		var scores, tokensIn, tokensOut, costs, toolCalls []float64
		for _, attempt := range evaluated {
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
		pipelines[side] = map[string]any{
			"attempt_count": len(records), "status_counts": statusCounts,
			"evaluated_count": len(evaluated), "valid_count": len(counted),
			"contributing_attempts": contributing, "evaluated_attempts": evaluatedRows,
			"tokens_in": distribution(tokensIn), "tokens_out": distribution(tokensOut),
			"cost": costDistribution, "tool_call_count": distribution(toolCalls),
			"mean_fact_score": factScore, "full_pass_rate": passRate, "pass_rate": passRate,
			"expected_cost_per_correct": expectedCost,
		}
	}

	deltas := map[string]any{}
	for _, field := range []string{"tokens_in", "tokens_out", "cost", "tool_call_count", "pass_rate", "mean_fact_score", "expected_cost_per_correct"} {
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
	return map[string]any{
		"schema_version": 1, "methodology_version": methodologyVersion,
		"experiment_id": manifest.ExperimentID, "target_valid_per_pipeline": manifest.TargetValidPerPipeline,
		"minimum_tool_calls": minimum, "pipelines": pipelines,
		"baseline_minus_candidate": deltas, "tool_call_inflation": inflation, "exclusions": exclusions,
	}
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

func WriteSummary(experimentDir string) (map[string]any, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(experimentDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	attempts, err := LoadAttempts(experimentDir)
	if err != nil {
		return nil, err
	}
	summary := BuildSummary(manifest, attempts)
	if err := WriteJSON(filepath.Join(experimentDir, "summary.json"), summary); err != nil {
		return nil, err
	}
	return summary, nil
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
	}
	if inflation := summaryValue(summary["tool_call_inflation"]); inflation != nil {
		lines = append(lines, fmt.Sprintf("tool_call_inflation=%v", *inflation))
	}
	return strings.Join(lines, "\n")
}

func formatValue(value *float64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%v", *value)
}
