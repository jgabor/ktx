package experiment

import "sort"

func buildRecoveryAwareComparison(manifest Manifest, attempts []Attempt) map[string]any {
	completeOutcomes := true
	for _, attempt := range attempts {
		if attempt.outcomes == nil {
			completeOutcomes = false
		}
	}
	arms := map[string]any{}
	for _, side := range Sides {
		var selected, evaluated, correct []Attempt
		for _, attempt := range attempts {
			if attempt.Side != side || (cacheAwareMethodology(manifest.Methodology.Version) && attempt.Role != MeasuredWarm) {
				continue
			}
			selected = append(selected, attempt)
			if AttemptIsEvaluated(attempt, manifest.Task.MinimumToolCalls) {
				evaluated = append(evaluated, attempt)
			}
			if AttemptIsTargetValid(attempt, manifest.Task.MinimumToolCalls, manifest.Methodology.Version) {
				correct = append(correct, attempt)
			}
		}
		arm := recoveryAwareArm(manifest.TargetValidPerPipeline, evaluated, correct, manifest.legacyRecoverySemantics)
		if manifest.historicalSummarySchema != 6 {
			arm["call_accounting"] = callAccountingSummary(selected)
		}
		arms[side] = arm
	}
	result := map[string]any{
		"primary_outcome": "runtime_total_tokens_per_correct_task",
		"field_authority": map[string]any{
			"quality": "retained_runtime_stream_answer_scoring", "runtime_tokens": "retained_runtime_stream_model_steps",
			"tool_delivery": "retained_attempt_database_compression_attribution", "cost": "retained_runtime_stream_cost",
			"recovery": "retained_attempt_database_cue_and_read_linkage", "tool_calls_and_exact_repeats": "retained_attempt_database_tool_calls",
		},
		"arms": arms, "paired_cycles": recoveryAwareCycles(attempts, manifest),
	}
	counts := CountValidForMethodology(attempts, manifest.Task.MinimumToolCalls, manifest.Methodology.Version)
	targetMet := counts[Baseline] >= manifest.TargetValidPerPipeline && counts[Candidate] >= manifest.TargetValidPerPipeline
	if !completeOutcomes {
		result["state"] = "unavailable"
		result["reason"] = "incomplete_attempt_outcomes"
	} else if !targetMet {
		result["state"] = "unavailable"
		result["reason"] = "target_valid_per_pipeline_unmet"
	}
	if !targetMet {
		result["target_completion"] = map[string]any{"met": false, "reason": "target_valid_per_pipeline_unmet"}
	}
	return result
}

func recoveryAwareArm(target int, evaluated, correct []Attempt, legacy bool) map[string]any {
	qualityQualified := len(correct) >= target && target > 0
	primary := map[string]any{"state": "unavailable", "value": nil, "reason": "quality_gate_failed"}
	if qualityQualified {
		total := 0.0
		complete := true
		for _, attempt := range correct {
			if attempt.TokensIn == nil || attempt.TokensOut == nil || attempt.Reasoning == nil {
				complete = false
				break
			}
			total += *attempt.TokensIn + *attempt.TokensOut + *attempt.Reasoning
		}
		if complete && len(correct) > 0 {
			primary = map[string]any{"state": "available", "value": total / float64(len(correct)), "reason": nil}
		} else {
			primary["reason"] = "incomplete_runtime_token_totals"
		}
	}
	recoveryAttempts, repeatedRecovery, cues, followed, totalCalls, repeatedCalls, attemptsWithRecovery := 0, 0, 0, 0, 0, 0, 0
	recoveryAvailable := len(evaluated) > 0
	for _, attempt := range evaluated {
		if attempt.outcomes == nil {
			recoveryAvailable = false
			continue
		}
		r := attempt.outcomes.Recovery
		x := attempt.outcomes.Repeats
		if r["state"] != "measured" {
			recoveryAvailable = false
		}
		ra := intValue(r["attempt_count"])
		recoveryAttempts += ra
		if ra > 0 {
			attemptsWithRecovery++
		}
		repeatedRecovery += intValue(r["repeated_attempt_count"])
		cues += intValue(r["cue_count"])
		followed += intValue(r["followed_cue_count"])
		totalCalls += intValue(x["call_count"])
		repeatedCalls += intValue(x["repeated_call_count"])
	}
	var followRate any
	if recoveryAvailable && cues > 0 {
		followRate = float64(followed) / float64(cues)
	}
	recovery := map[string]any{"state": "measured", "reason": nil, "attempts_with_recovery": attemptsWithRecovery, "cue_count": cues, "followed_cue_count": followed, "cue_follow_rate": followRate, "attempt_count": recoveryAttempts, "repeated_attempt_count": repeatedRecovery}
	if !recoveryAvailable {
		recovery = map[string]any{"state": "unavailable", "reason": "cue_delivery_authority_unavailable", "attempts_with_recovery": nil, "cue_count": nil, "followed_cue_count": nil, "cue_follow_rate": nil, "attempt_count": nil, "repeated_attempt_count": nil}
	} else if legacy {
		delete(recovery, "state")
		delete(recovery, "reason")
	}
	return map[string]any{
		"quality_gate":                          map[string]any{"qualified": qualityQualified, "required_correct_tasks": target, "correct_task_count": len(correct), "evaluated_task_count": len(evaluated)},
		"runtime_total_tokens_per_correct_task": primary,
		"recovery":                              recovery,
		"tool_calls":                            map[string]any{"total_call_count": totalCalls, "exact_repeated_call_count": repeatedCalls},
	}
}

func recoveryAwareCycles(attempts []Attempt, manifest Manifest) []any {
	byCycle := map[int]map[string]Attempt{}
	for _, attempt := range attempts {
		if attempt.Role != MeasuredWarm || attempt.outcomes == nil || !AttemptIsEvaluated(attempt, manifest.Task.MinimumToolCalls) {
			continue
		}
		if byCycle[attempt.Cycle] == nil {
			byCycle[attempt.Cycle] = map[string]Attempt{}
		}
		byCycle[attempt.Cycle][attempt.Side] = attempt
	}
	cycles := make([]int, 0, len(byCycle))
	for cycle, pair := range byCycle {
		if len(pair) == 2 {
			cycles = append(cycles, cycle)
		}
	}
	sort.Ints(cycles)
	rows := make([]any, 0, len(cycles))
	for _, cycle := range cycles {
		pair := byCycle[cycle]
		rows = append(rows, map[string]any{"cycle": cycle, Baseline: recoveryAwareAttempt(pair[Baseline]), Candidate: recoveryAwareAttempt(pair[Candidate])})
	}
	return rows
}

func recoveryAwareAttempt(attempt Attempt) map[string]any {
	var total any
	if attempt.TokensIn != nil && attempt.TokensOut != nil && attempt.Reasoning != nil {
		total = *attempt.TokensIn + *attempt.TokensOut + *attempt.Reasoning
	}
	var recoveryAttempts any
	if attempt.outcomes.Recovery["state"] == "measured" {
		recoveryAttempts = intValue(attempt.outcomes.Recovery["attempt_count"])
	}
	return map[string]any{
		"attempt_id": attempt.AttemptID, "correct": attempt.FullPass, "runtime_total_tokens": total,
		"recovery_attempt_count": recoveryAttempts, "total_tool_calls": intValue(attempt.outcomes.Repeats["call_count"]), "exact_repeated_tool_calls": intValue(attempt.outcomes.Repeats["repeated_call_count"]),
	}
}

func intValue(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case float64:
		return int(value)
	}
	return 0
}
