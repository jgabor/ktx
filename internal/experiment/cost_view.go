package experiment

import (
	"fmt"
	"strings"
)

// CurrentCostView is an in-memory reporting view, never a serialized summary
// authority or an acceptance result.
type CurrentCostView struct {
	CostSelection CostSelection       `json:"cost_selection"`
	SelectedCost  map[string]any      `json:"selected_cost"`
	SourceCosts   []SourceCost        `json:"source_costs"`
	Phases        map[string]CostView `json:"phases,omitempty"`
	Acceptance    map[string]any      `json:"current_acceptance,omitempty"`
}

type SourceCost struct {
	ID               string   `json:"id"`
	ExperimentID     string   `json:"experiment_id,omitempty"`
	ReportedCost     *float64 `json:"cost"`
	EstimatedAPICost *float64 `json:"estimated_api_cost"`
}

type CostView struct {
	CostSelection CostSelection  `json:"cost_selection"`
	SelectedCost  map[string]any `json:"selected_cost"`
	SourceCosts   []SourceCost   `json:"source_costs"`
}

// LoadCurrentCostView loads retained field authorities without writing files or
// consulting current prices. It uses the same interpretation boundary as
// regeneration; discovery alone remains available for historical archives.
func LoadCurrentCostView(dir string) (CurrentCostView, error) {
	a, err := openArchive(dir)
	if err != nil {
		return CurrentCostView{}, err
	}
	defer a.Close()
	m, attempts, _, err := loadExperimentEvidence(a)
	if err != nil {
		return CurrentCostView{}, err
	}
	observations, err := AttemptCostObservations(dir, m, attempts)
	if err != nil {
		return CurrentCostView{}, err
	}
	headline, facts := []Attempt{}, []CostObservation{}
	for i, attempt := range attempts {
		if AttemptIsEvaluated(attempt, m.Task.MinimumToolCalls) && (!cacheAwareMethodology(m.Methodology.Version) || attempt.Role == MeasuredWarm) {
			headline, facts = append(headline, attempt), append(facts, observations[i])
		}
	}
	view := costView(m, headline, facts)
	result := CurrentCostView{CostSelection: view.CostSelection, SelectedCost: view.SelectedCost, SourceCosts: view.SourceCosts}
	if cacheAwareMethodology(m.Methodology.Version) {
		result.Phases = map[string]CostView{}
		for _, phase := range []string{ColdObservation, Warmup, MeasuredWarm} {
			rows, sources := []Attempt{}, []CostObservation{}
			for i, attempt := range attempts {
				if attempt.Role == phase && AttemptIsEvaluated(attempt, m.Task.MinimumToolCalls) {
					rows, sources = append(rows, attempt), append(sources, observations[i])
				}
			}
			phaseView := costView(m, rows, sources)
			for _, raw := range phaseView.SelectedCost["pipelines"].(map[string]any) {
				details := raw.(map[string]any)
				delete(details, "cost_per_correct")
				delete(details, "full_pass_count")
			}
			delete(phaseView.SelectedCost, "paired_baseline_minus_candidate")
			result.Phases[phase] = phaseView
		}
	}
	if supportsPromptCacheTokenHitRate(m) {
		result.Acceptance = currentAcceptance(m, attempts, observations, nil)
	}
	return result, nil
}

func costView(m Manifest, attempts []Attempt, facts []CostObservation) CostView {
	selection := SelectCostCohort(facts)
	return selectedCostView(m, attempts, facts, selection)
}

func selectedCostView(m Manifest, attempts []Attempt, facts []CostObservation, selection CostSelection) CostView {
	view := CostView{CostSelection: selection, SourceCosts: []SourceCost{}}
	selected := append([]Attempt(nil), attempts...)
	for i, fact := range facts {
		view.SourceCosts = append(view.SourceCosts, SourceCost{ID: fact.id, ExperimentID: fact.experimentID, ReportedCost: copyCost(fact.reported), EstimatedAPICost: copyCost(fact.estimated)})
		selected[i].Cost = selection.Observations[i].Value
	}
	sides := map[string]any{}
	for _, side := range Sides {
		values := []float64{}
		evaluated, correct := 0, 0
		for _, attempt := range selected {
			if attempt.Side != side {
				continue
			}
			evaluated++
			if AttemptIsTargetValid(attempt, m.Task.MinimumToolCalls, m.Methodology.Version) {
				correct++
			}
			if attempt.Cost != nil {
				values = append(values, *attempt.Cost)
			}
		}
		var perCorrect *float64
		if average := mean(values); average != nil && evaluated > 0 {
			denominator := float64(correct) / float64(evaluated)
			if denominator < scoreEpsilon {
				denominator = scoreEpsilon
			}
			value := *average / denominator
			perCorrect = &value
		}
		sides[side] = map[string]any{"distribution": distribution(values), "evaluated_count": evaluated, "full_pass_count": correct, "cost_per_correct": perCorrect}
	}
	paired := pairedSummary(selected, false)
	for _, raw := range paired["cycles"].([]any) {
		row := raw.(map[string]any)
		deltas := row["deltas"].(map[string]any)
		row["deltas"] = map[string]any{"cost": deltas["cost"]}
	}
	metrics := paired["metrics"].(map[string]any)
	paired["metrics"] = map[string]any{"cost": metrics["cost"]}
	view.SelectedCost = map[string]any{"pipelines": sides, "paired_baseline_minus_candidate": paired}
	return view
}

// WithCurrentCost returns a shallow copy so callers cannot add current-policy
// fields to the summary used for archive serialization or byte validation.
func WithCurrentCost(summary Summary, view CurrentCostView) Summary {
	result := Summary{}
	for key, value := range summary {
		result[key] = value
	}
	result["cost_selection"], result["selected_cost"], result["source_costs"] = view.CostSelection, view.SelectedCost, view.SourceCosts
	if view.Phases != nil {
		result["phase_cost_views"] = view.Phases
	}
	if view.Acceptance != nil {
		result["current_acceptance"] = view.Acceptance
	}
	return result
}

func RenderCostSelection(selection CostSelection) string {
	lines := []string{fmt.Sprintf("cost_selection policy_version=%d basis=%s reason=%s unit=%s required=%d reported_eligible=%d estimated_eligible=%d estimate_reason=%s", selection.PolicyVersion, selection.Basis, selection.Reason, selection.Unit, selection.RequiredCount, selection.ReportedEligible, selection.EstimatedEligible, selection.EstimateReason)}
	for _, observation := range selection.Observations {
		pricing := "-"
		if observation.Pricing != nil {
			pricing = observation.Pricing.SHA256
		}
		lines = append(lines, fmt.Sprintf("selected_cost id=%s experiment=%s value=%s source=%s pricing_sha256=%s reported_reasons=%s estimated_reasons=%s", observation.ID, observation.ExperimentID, formatValue(observation.Value), observation.Source, pricing, strings.Join(observation.ReportedReasons, ","), strings.Join(observation.EstimatedReasons, ",")))
	}
	return strings.Join(lines, "\n")
}

func RenderCurrentCostView(view CurrentCostView) string {
	lines := []string{RenderCostSelection(view.CostSelection), "selected_cost: " + compactJSON(view.SelectedCost), "source_costs: " + compactJSON(view.SourceCosts)}
	if view.Acceptance != nil {
		lines = append(lines, "current_acceptance: "+compactJSON(view.Acceptance))
	}
	for _, phase := range []string{ColdObservation, Warmup, MeasuredWarm} {
		if phaseView, ok := view.Phases[phase]; ok {
			lines = append(lines, "phase="+phase+" "+RenderCostSelection(phaseView.CostSelection), "phase_selected_cost: "+compactJSON(phaseView.SelectedCost), "phase_source_costs: "+compactJSON(phaseView.SourceCosts))
		}
	}
	return strings.Join(lines, "\n")
}
