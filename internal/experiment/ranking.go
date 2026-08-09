package experiment

import (
	"fmt"
	"math/big"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type PairResult struct {
	CandidatePipeline string         `json:"candidate_pipeline"`
	ExperimentID      string         `json:"experiment_id"`
	ExperimentDir     string         `json:"experiment_dir"`
	ExitCode          int            `json:"exit_code"`
	LogPath           string         `json:"log_path"`
	StderrTail        string         `json:"stderr_tail"`
	Summary           map[string]any `json:"summary"`
}

type RankingOptions struct {
	TaskFile         string
	Model            string
	Variant          string
	Rounds           int
	BaselinePipeline string
	Candidates       []string
}

func sideMetrics(summary map[string]any, side string) map[string]any {
	result := map[string]any{
		"valid_count": 0, "evaluated_count": 0, "mean_tokens_in": nil,
		"mean_cost": nil, "mean_fact_score": nil, "full_pass_rate": nil,
		"expected_cost_per_correct": nil, "mean_tool_calls": nil,
	}
	if summary == nil {
		return result
	}
	pipelines, _ := summary["pipelines"].(map[string]any)
	details, _ := pipelines[side].(map[string]any)
	if details == nil {
		return result
	}
	result["valid_count"] = details["valid_count"]
	result["evaluated_count"] = details["evaluated_count"]
	result["mean_tokens_in"] = summaryValue(details["tokens_in"])
	result["mean_cost"] = summaryValue(details["cost"])
	result["mean_fact_score"] = summaryValue(details["mean_fact_score"])
	result["full_pass_rate"] = summaryValue(details["full_pass_rate"])
	result["expected_cost_per_correct"] = summaryValue(details["expected_cost_per_correct"])
	result["mean_tool_calls"] = summaryValue(details["tool_call_count"])
	if schemaVersion, _ := summary["schema_version"].(int); schemaVersion >= 3 {
		result["sample_classification"] = details["sample_classification"]
		result["mean_estimated_api_cost"] = summaryValue(details["estimated_api_cost"])
		result["mean_cache_read"] = summaryValue(details["cache_read"])
		result["mean_cache_write"] = summaryValue(details["cache_write"])
	}
	return result
}

func pairedMetric(summary map[string]any, field string) *float64 {
	paired, _ := summary["paired_baseline_minus_candidate"].(map[string]any)
	metrics, _ := paired["metrics"].(map[string]any)
	metric, _ := metrics[field].(map[string]any)
	return summaryValue(metric["mean_paired_delta"])
}

func BuildRanking(options RankingOptions, pairs []PairResult, now time.Time) map[string]any {
	rows := []map[string]any{}
	hasPipelineAcceptance := false
	var baseline map[string]any
	for _, pair := range pairs {
		if pair.Summary != nil {
			baseline = sideMetrics(pair.Summary, Baseline)
			break
		}
	}
	if baseline != nil {
		row := map[string]any{
			"pipeline": options.BaselinePipeline, "side": Baseline, "experiment_id": nil,
			"tool_call_inflation_vs_baseline": 1.0, "tokens_saved_vs_baseline": 0.0, "cost_saved_vs_baseline": 0.0,
		}
		copyMap(row, baseline)
		if _, ok := baseline["sample_classification"]; ok {
			row["paired_mean_tokens_saved_vs_baseline"] = 0.0
			row["paired_mean_cache_read_saved_vs_baseline"] = 0.0
			row["paired_mean_cache_write_saved_vs_baseline"] = 0.0
			row["paired_mean_estimated_api_cost_saved_vs_baseline"] = 0.0
			if len(pairs) > 0 {
				paired, _ := pairs[0].Summary["paired_baseline_minus_candidate"].(map[string]any)
				row["paired_sample_classification"] = paired["sample_classification"]
			}
		}
		rows = append(rows, row)
	}
	for _, pair := range pairs {
		candidate := sideMetrics(pair.Summary, Candidate)
		row := map[string]any{
			"pipeline": pair.CandidatePipeline, "side": Candidate, "experiment_id": pair.ExperimentID, "exit_code": pair.ExitCode,
			"tool_call_inflation_vs_baseline": nil, "tokens_saved_vs_baseline": nil, "cost_saved_vs_baseline": nil, "pair_tool_call_inflation": nil,
		}
		copyMap(row, candidate)
		if baseline != nil {
			baseTools, candidateTools := summaryValue(baseline["mean_tool_calls"]), summaryValue(candidate["mean_tool_calls"])
			if baseTools != nil && *baseTools != 0 && candidateTools != nil {
				row["tool_call_inflation_vs_baseline"] = *candidateTools / *baseTools
			}
			baseTokens, candidateTokens := summaryValue(baseline["mean_tokens_in"]), summaryValue(candidate["mean_tokens_in"])
			if baseTokens != nil && candidateTokens != nil {
				row["tokens_saved_vs_baseline"] = *baseTokens - *candidateTokens
			}
			baseCost, candidateCost := summaryValue(baseline["mean_cost"]), summaryValue(candidate["mean_cost"])
			if baseCost != nil && candidateCost != nil {
				row["cost_saved_vs_baseline"] = *baseCost - *candidateCost
			}
		}
		if pair.Summary != nil {
			row["pair_tool_call_inflation"] = pair.Summary["tool_call_inflation"]
			if schemaVersion, _ := pair.Summary["schema_version"].(int); schemaVersion >= 3 {
				row["paired_mean_tokens_saved_vs_baseline"] = pairedMetric(pair.Summary, "tokens_in")
				row["paired_mean_cache_read_saved_vs_baseline"] = pairedMetric(pair.Summary, "cache_read")
				row["paired_mean_cache_write_saved_vs_baseline"] = pairedMetric(pair.Summary, "cache_write")
				row["paired_mean_estimated_api_cost_saved_vs_baseline"] = pairedMetric(pair.Summary, "estimated_api_cost")
				paired, _ := pair.Summary["paired_baseline_minus_candidate"].(map[string]any)
				row["paired_sample_classification"] = paired["sample_classification"]
			}
			if schemaVersion, _ := pair.Summary["schema_version"].(int); schemaVersion >= 5 {
				row["prompt_cache_token_hit_rate_comparison"] = promptCacheTokenHitRateRankingComparison(pair.Summary)
				if acceptance := anyMap(pair.Summary["pipeline_acceptance"]); len(acceptance) > 0 {
					hasPipelineAcceptance = true
					accepted := acceptance["accepted"] == true
					row["pipeline_acceptance"] = acceptance
					row["accepted"] = accepted
					row["recommendation_eligible"] = accepted
					row["crown_eligible"] = accepted
				}
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		lp, rp := summaryValue(left["full_pass_rate"]), summaryValue(right["full_pass_rate"])
		lclass, rclass := passClass(lp), passClass(rp)
		if lclass != rclass {
			return lclass < rclass
		}
		for _, field := range []string{"expected_cost_per_correct", "mean_cost"} {
			lv, rv := summaryValue(left[field]), summaryValue(right[field])
			if compareNullable(lv, rv) != 0 {
				return compareNullable(lv, rv) < 0
			}
		}
		return fmt.Sprint(left["pipeline"]) < fmt.Sprint(right["pipeline"])
	})
	for index := range rows {
		rows[index]["rank"] = index + 1
	}
	pairRefs := make([]any, 0, len(pairs))
	for _, pair := range pairs {
		pairRefs = append(pairRefs, map[string]any{"candidate_pipeline": pair.CandidatePipeline, "experiment_id": pair.ExperimentID, "exit_code": pair.ExitCode})
	}
	result := map[string]any{
		"schema_version": 1, "created_at": now.UTC().Truncate(time.Second).Format(time.RFC3339),
		"task_file": absolutePath(options.TaskFile), "model": options.Model, "rounds": options.Rounds,
		"baseline_pipeline": options.BaselinePipeline, "candidate_pipelines": options.Candidates,
		"ranking": rows, "pairs": pairRefs,
	}
	if options.Variant != "" {
		result["variant"] = options.Variant
	}
	if hasPipelineAcceptance {
		result["recommendation"] = pipelineAcceptanceRecommendation(rows)
	}
	return result
}

func pipelineAcceptanceRecommendation(rows []map[string]any) map[string]any {
	type eligibleCandidate struct {
		pipeline string
		cost     *big.Int
	}
	eligible := []eligibleCandidate{}
	excluded := []any{}
	for _, row := range rows {
		if row["side"] != Candidate {
			continue
		}
		acceptance := anyMap(row["pipeline_acceptance"])
		if len(acceptance) == 0 {
			continue
		}
		pipeline := fmt.Sprint(row["pipeline"])
		if acceptance["accepted"] != true {
			excluded = append(excluded, map[string]any{"pipeline": pipeline, "reasons": acceptance["reasons"]})
			continue
		}
		candidate := anyMap(acceptance[Candidate])
		perCorrect := anyMap(candidate["estimated_cost_per_correct"])
		microUSD, ok := perCorrect["micro_usd"].(string)
		cost := new(big.Int)
		if _, parsed := cost.SetString(microUSD, 10); !ok || !parsed {
			excluded = append(excluded, map[string]any{"pipeline": pipeline, "reasons": []string{"accepted_cost_projection_unavailable"}})
			continue
		}
		eligible = append(eligible, eligibleCandidate{pipeline: pipeline, cost: cost})
	}
	sort.Slice(eligible, func(i, j int) bool {
		if compared := eligible[i].cost.Cmp(eligible[j].cost); compared != 0 {
			return compared < 0
		}
		return eligible[i].pipeline < eligible[j].pipeline
	})
	sort.Slice(excluded, func(i, j int) bool {
		return fmt.Sprint(anyMap(excluded[i])["pipeline"]) < fmt.Sprint(anyMap(excluded[j])["pipeline"])
	})
	var selected any
	if len(eligible) > 0 {
		selected = eligible[0].pipeline
	}
	return map[string]any{
		"basis":                    "accepted rounded API-equivalent estimated cost per correct",
		"recommended_pipeline":     selected,
		"crowned_pipeline":         selected,
		"eligible_candidate_count": len(eligible),
		"excluded_candidates":      excluded,
	}
}

func promptCacheTokenHitRateRankingComparison(summary map[string]any) map[string]any {
	comparison := anyMap(summary["prompt_cache_token_hit_rate_comparison"])
	if len(comparison) == 0 {
		return nil
	}
	return map[string]any{
		"complete_pair_count": comparison["complete_pair_count"], "baseline_mean": comparison["baseline_mean"],
		"candidate_mean": comparison["candidate_mean"], "percentage_point_delta": comparison["percentage_point_delta"],
		"interval": comparison["interval"], "quality": comparison["quality"], "blocking_reasons": comparison["blocking_reasons"],
		"statement_authorization": summary["prompt_cache_token_hit_rate_statement_authorization"],
	}
}

func copyMap(destination, source map[string]any) {
	for key, value := range source {
		destination[key] = value
	}
}

func absolutePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}

func passClass(value *float64) int {
	if value == nil {
		return 2
	}
	if *value == 1 {
		return 0
	}
	return 1
}

func compareNullable(left, right *float64) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	if *left < *right {
		return -1
	}
	if *left > *right {
		return 1
	}
	return 0
}

func RenderRanking(ranking map[string]any) string {
	lines := []string{
		fmt.Sprintf("task=%s model=%v rounds=%v", filepath.Base(fmt.Sprint(ranking["task_file"])), ranking["model"], ranking["rounds"]),
		"",
		fmt.Sprintf("%4s  %-24s  %6s  %6s  %10s  %10s  %10s  %6s  %6s", "rank", "pipeline", "pass", "fact", "tokens", "cost", "$/correct", "tools", "infl"),
		fmt.Sprintf("%4s  %-24s  %6s  %6s  %10s  %10s  %10s  %6s  %6s", "----", "--------", "----", "----", "------", "----", "---------", "-----", "----"),
	}
	rows, _ := ranking["ranking"].([]map[string]any)
	if rows == nil {
		if generic, ok := ranking["ranking"].([]any); ok {
			for _, item := range generic {
				if row, ok := item.(map[string]any); ok {
					rows = append(rows, row)
				}
			}
		}
	}
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("%4v  %-24v  %6s  %6s  %10s  %10s  %10s  %6s  %6s",
			row["rank"], row["pipeline"], formatSignificant(row["full_pass_rate"], 3), formatSignificant(row["mean_fact_score"], 3),
			formatSignificant(row["mean_tokens_in"], 6), formatSignificant(row["mean_cost"], 4),
			formatSignificant(row["expected_cost_per_correct"], 4), formatSignificant(row["mean_tool_calls"], 3),
			formatSignificant(row["tool_call_inflation_vs_baseline"], 3)))
	}
	if recommendation := anyMap(ranking["recommendation"]); len(recommendation) > 0 {
		lines = append(lines, fmt.Sprintf("recommendation pipeline=%v crowned_pipeline=%v eligible_candidates=%v", recommendation["recommended_pipeline"], recommendation["crowned_pipeline"], recommendation["eligible_candidate_count"]))
		for _, raw := range recommendation["excluded_candidates"].([]any) {
			excluded := anyMap(raw)
			lines = append(lines, fmt.Sprintf("pipeline_acceptance pipeline=%v accepted=false reasons=%s", excluded["pipeline"], compactJSON(excluded["reasons"])))
		}
	}
	for _, row := range rows {
		if _, ok := row["sample_classification"]; !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("paired pipeline=%v sample=%v paired_sample=%v tokens=%s cache_read=%s cache_write=%s estimated_cost=%s",
			row["pipeline"], row["sample_classification"], row["paired_sample_classification"],
			formatSignificant(row["paired_mean_tokens_saved_vs_baseline"], 6),
			formatSignificant(row["paired_mean_cache_read_saved_vs_baseline"], 6),
			formatSignificant(row["paired_mean_cache_write_saved_vs_baseline"], 6),
			formatSignificant(row["paired_mean_estimated_api_cost_saved_vs_baseline"], 6)))
	}
	for _, row := range rows {
		comparison := anyMap(row["prompt_cache_token_hit_rate_comparison"])
		authorization := anyMap(comparison["statement_authorization"])
		for _, outcomeName := range []string{"higher", "lower"} {
			outcome := anyMap(anyMap(authorization["outcomes"])[outcomeName])
			if outcome["authorized"] == true {
				lines = append(lines, fmt.Sprintf("prompt_cache_token_hit_rate pipeline=%v outcome=%s statement=%s", row["pipeline"], outcomeName, outcome["statement"]))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func formatSignificant(value any, digits int) string {
	number := summaryValue(value)
	if number == nil {
		return "-"
	}
	return fmt.Sprintf("%.*g", digits, *number)
}
