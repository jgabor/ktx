package experiment

import "github.com/jgabor/ktx/internal/db"

// guardedCosts fixes pair membership before inspecting cost availability.
func guardedCosts(m Manifest, attempts []Attempt, facts []CostObservation) (map[string]any, []Attempt, []CostObservation) {
	comparison := buildCachedInputShareComparison(m, attempts)
	byID := map[string]int{}
	for i, attempt := range attempts {
		byID[attempt.AttemptID] = i
	}
	rows, _ := sliceValue(comparison["pairs"])
	selected, sources := []Attempt{}, []CostObservation{}
	for _, raw := range rows {
		pair := anyMap(raw)
		for _, side := range Sides {
			id := stringValue(anyMap(pair[side])["attempt_id"])
			if i, ok := byID[id]; ok {
				selected, sources = append(selected, attempts[i]), append(sources, facts[i])
			}
		}
	}
	return comparison, selected, sources
}

// currentAcceptance is separate from the frozen estimated-cost protocol result.
// A ranking caller supplies values selected over the union of guarded cohorts.
func currentAcceptance(m Manifest, attempts []Attempt, facts []CostObservation, common *CostSelection) map[string]any {
	comparison, rows, sources := guardedCosts(m, attempts, facts)
	selection := SelectCostCohort(sources)
	selected := selection.Observations
	if common != nil {
		selection = *common
		selected = subsetCostSelection(*common, sources).Observations
	}
	reasons := pipelineCostGuardComparisonReasons(comparison)
	required := m.TargetValidPerPipeline
	if required < 1 {
		required = 1
	}
	if len(rows)/2 < required {
		reasons = appendUnique(reasons, "incomplete_pairs")
	}
	quality := pairedQuality(rows)
	quality["basis"] = "same_complete_direct_evidence_pairs"
	if len(rows) > 0 && quality["preserved"] != true {
		reasons = appendUnique(reasons, "quality_not_preserved")
	}
	if selection.Basis == "unavailable" {
		reasons = appendUnique(reasons, "selected_cost_unavailable")
	}
	arms := map[string]any{}
	values := map[string]*float64{}
	for _, side := range Sides {
		total, correct := 0.0, 0
		for i, attempt := range rows {
			if attempt.Side != side {
				continue
			}
			if attempt.FullPass {
				correct++
			}
			if value := selected[i].Value; value != nil {
				if rounded := db.RoundCost(*value); rounded != nil {
					total += *rounded
				} else {
					reasons = appendUnique(reasons, "selected_cost_nonfinite")
				}
			}
		}
		var sum, perCorrect *float64
		if selection.Basis != "unavailable" && len(rows) > 0 {
			sum = db.RoundCost(total)
			if sum != nil && correct > 0 {
				perCorrect = db.RoundCost(*sum / float64(correct))
			}
			if sum == nil || (correct > 0 && perCorrect == nil) {
				reasons = appendUnique(reasons, "selected_cost_nonfinite")
			}
		}
		if len(rows) > 0 && correct == 0 {
			reasons = appendUnique(reasons, "zero_correct_answers")
		}
		values[side] = perCorrect
		arms[side] = map[string]any{"correct_answers": correct, "selected_cost_total": sum, "selected_cost_per_correct": perCorrect}
	}
	state := "unavailable"
	if values[Baseline] != nil && values[Candidate] != nil {
		state = "tie"
		if *values[Candidate] < *values[Baseline] {
			state = "lower"
		} else if *values[Candidate] > *values[Baseline] {
			state = "higher"
		}
		if state != "lower" {
			reasons = appendUnique(reasons, "candidate_selected_cost_per_correct_not_lower")
		}
	}
	return map[string]any{"policy_version": 1, "cost_selection": selection, "comparison_unit": "measured_cycle_pair", "required_complete_pair_count": required, "complete_pair_count": len(rows) / 2, "quality": quality, "arms": arms, "comparison": state, "reasons": reasons, "accepted": len(reasons) == 0, "recommendation_eligible": len(reasons) == 0}
}

func subsetCostSelection(common CostSelection, facts []CostObservation) CostSelection {
	result := common
	result.Observations = []SelectedCostObservation{}
	for _, fact := range facts {
		for _, observation := range common.Observations {
			if observation.ID == fact.id && observation.ExperimentID == fact.experimentID {
				result.Observations = append(result.Observations, observation)
				break
			}
		}
	}
	return result
}
