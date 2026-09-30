package experiment

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

type rankingEvidence struct {
	manifest      Manifest
	attempts      []Attempt
	facts         []CostObservation
	headline      []Attempt
	headlineFacts []CostObservation
}

func loadRankingEvidence(dir string) (rankingEvidence, error) {
	a, err := openArchive(dir)
	if err != nil {
		return rankingEvidence{}, err
	}
	defer a.Close()
	m, attempts, _, err := loadExperimentEvidence(a)
	if err != nil {
		return rankingEvidence{}, err
	}
	facts, err := AttemptCostObservations(dir, m, attempts)
	if err != nil {
		return rankingEvidence{}, err
	}
	e := rankingEvidence{manifest: m, attempts: attempts, facts: facts}
	for i, attempt := range attempts {
		if AttemptIsEvaluated(attempt, m.Task.MinimumToolCalls) && (!cacheAwareMethodology(m.Methodology.Version) || attempt.Role == MeasuredWarm) {
			e.headline, e.headlineFacts = append(e.headline, attempt), append(e.headlineFacts, facts[i])
		}
	}
	return e, nil
}

// BuildCurrentRanking loads source evidence for every declared experiment. The
// historical ranking builder and its serialization remain unchanged.
func BuildCurrentRanking(options RankingOptions, pairs []PairResult) (map[string]any, error) {
	evidence := make([]rankingEvidence, len(pairs))
	cohort, guarded := []CostObservation{}, []CostObservation{}
	seen := map[string]bool{}
	for i, pair := range pairs {
		if seen[pair.ExperimentID] {
			return nil, fmt.Errorf("duplicate ranking experiment %q", pair.ExperimentID)
		}
		seen[pair.ExperimentID] = true
		if pair.ExperimentDir == "" {
			return nil, fmt.Errorf("current ranking requires retained evidence for %q", pair.CandidatePipeline)
		}
		e, err := loadRankingEvidence(pair.ExperimentDir)
		if err != nil {
			return nil, err
		}
		if e.manifest.ExperimentID != pair.ExperimentID {
			return nil, fmt.Errorf("ranking experiment identity mismatch for %q", pair.ExperimentID)
		}
		if options.BaselinePipeline != e.manifest.Pipelines[Baseline].PipelineID || pair.CandidatePipeline != e.manifest.Pipelines[Candidate].PipelineID {
			return nil, fmt.Errorf("ranking pipeline labels do not match retained manifest for %q", pair.ExperimentID)
		}
		evidence[i] = e
		cohort = append(cohort, e.headlineFacts...)
		if supportsPromptCacheTokenHitRate(e.manifest) {
			_, _, facts := guardedCosts(e.manifest, e.attempts, e.facts)
			guarded = append(guarded, facts...)
		}
	}
	selection, guardSelection := SelectCostCohort(cohort), SelectCostCohort(guarded)
	currentPairs := append([]PairResult(nil), pairs...)
	acceptances := map[string]any{}
	selectedPairs := map[string]any{}
	sourceCosts := []SourceCost{}
	for i, e := range evidence {
		summary := BuildSummary(e.manifest, e.attempts)
		view := selectedCostView(e.manifest, e.headline, e.headlineFacts, subsetCostSelection(selection, e.headlineFacts))
		sourceCosts = append(sourceCosts, view.SourceCosts...)
		selectedPairs[pairs[i].ExperimentID] = view.SelectedCost["paired_baseline_minus_candidate"]
		for _, side := range Sides {
			details, _ := summary.Pipeline(side)
			selected := anyMap(anyMap(view.SelectedCost["pipelines"])[side])
			details["cost"] = selected["distribution"]
			details["expected_cost_per_correct"] = selected["cost_per_correct"]
		}
		if supportsPromptCacheTokenHitRate(e.manifest) {
			acceptance := currentAcceptance(e.manifest, e.attempts, e.facts, &guardSelection)
			acceptances[pairs[i].CandidatePipeline] = acceptance
			summary[pipelineCostGuardField] = acceptance
		}
		currentPairs[i].Summary = summary
	}
	shadow := BuildRanking(options, currentPairs, time.Time{})
	rows := shadow["ranking"].([]map[string]any)
	primary := "selected_cost_per_correct"
	costOrderAvailable := selection.Basis != "unavailable"
	var winner any
	for _, row := range rows {
		row["selected_cost_per_correct"] = row["expected_cost_per_correct"]
		row["mean_selected_cost"] = row["mean_cost"]
		delete(row, "expected_cost_per_correct")
		delete(row, "mean_cost")
		delete(row, "cost_saved_vs_baseline")
		if id, ok := row["experiment_id"].(string); ok {
			row["selected_paired_baseline_minus_candidate"] = selectedPairs[id]
		}
		if _, tokenPrimary := row["runtime_total_tokens_per_correct_task"]; tokenPrimary {
			primary = "runtime_total_tokens_per_correct_task"
		}
		metric := summaryValue(row["selected_cost_per_correct"])
		comparable := metric != nil && !math.IsNaN(*metric) && !math.IsInf(*metric, 0)
		if !comparable {
			costOrderAvailable = false
		}
		if _, tokenPrimary := row["runtime_total_tokens_per_correct_task"]; tokenPrimary {
			continue
		} else if selection.Basis == "unavailable" || !comparable {
			row["rank"] = nil
			row["recommendation_eligible"] = false
		} else if _, guarded := row["accepted"]; !guarded {
			row["recommendation_eligible"] = passClass(summaryValue(row["full_pass_rate"])) == 0
		}
	}
	if primary == "selected_cost_per_correct" && !costOrderAvailable {
		for _, row := range rows {
			if _, guarded := row["accepted"]; !guarded {
				row["recommendation_eligible"] = false
			}
		}
	}
	if primary == "selected_cost_per_correct" && costOrderAvailable && len(rows) > 0 && !rejectedRankingRow(rows[0]) && passClass(summaryValue(rows[0]["full_pass_rate"])) == 0 {
		best := summaryValue(rows[0]["selected_cost_per_correct"])
		unique := best != nil
		for _, row := range rows[1:] {
			if !rejectedRankingRow(row) && passClass(summaryValue(row["full_pass_rate"])) == 0 && compareNullable(best, summaryValue(row["selected_cost_per_correct"])) == 0 {
				unique = false
			}
		}
		if unique {
			winner = rows[0]["pipeline"]
		}
	}
	result := map[string]any{"policy_version": 1, "primary_metric": primary, "cost_selection": selection, "source_costs": sourceCosts, "ranking": rows, "cost_winner": winner, "cost_order_available": costOrderAvailable, "current_acceptance": acceptances}
	if len(guarded) > 0 {
		result["acceptance_cost_selection"] = guardSelection
	}
	return result, nil
}

// LoadCurrentRanking returns an explicit envelope, preserving raw historical
// fields. References resolve only as validated IDs under the campaign's parent.
func LoadCurrentRanking(dir string) (map[string]any, error) {
	a, err := openArchive(dir)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	data, err := a.readFile("ranking.json")
	if err != nil {
		return nil, err
	}
	var historical struct {
		BaselinePipeline string `json:"baseline_pipeline"`
		Pairs            []struct {
			CandidatePipeline string `json:"candidate_pipeline"`
			ExperimentID      string `json:"experiment_id"`
			ExitCode          *int   `json:"exit_code"`
		} `json:"pairs"`
	}
	if err := json.Unmarshal(data, &historical); err != nil {
		return nil, err
	}
	options := RankingOptions{BaselinePipeline: historical.BaselinePipeline}
	refs := historical.Pairs
	if len(refs) == 0 {
		return nil, fmt.Errorf("current ranking requires retained pair references")
	}
	pairs := []PairResult{}
	exitCodes := map[string]*int{}
	for _, ref := range refs {
		id := ref.ExperimentID
		if err := ValidateExperimentID(id); err != nil {
			return nil, err
		}
		pair := PairResult{CandidatePipeline: ref.CandidatePipeline, ExperimentID: id, ExperimentDir: filepath.Join(filepath.Dir(dir), id)}
		if ref.ExitCode != nil {
			pair.ExitCode = *ref.ExitCode
		}
		exitCodes[id] = ref.ExitCode
		pairs = append(pairs, pair)
	}
	view, err := BuildCurrentRanking(options, pairs)
	if err != nil {
		return nil, err
	}
	for _, row := range view["ranking"].([]map[string]any) {
		if id, ok := row["experiment_id"].(string); ok && exitCodes[id] == nil {
			row["exit_code"] = nil
		}
	}
	return map[string]any{"historical_ranking": json.RawMessage(data), "current_ranking": view}, nil
}

func RenderCurrentRanking(value map[string]any) string {
	current := value
	if view, ok := value["current_ranking"].(map[string]any); ok {
		current = view
	}
	var selection CostSelection
	data, _ := json.Marshal(current["cost_selection"])
	_ = json.Unmarshal(data, &selection)
	var out strings.Builder
	fmt.Fprintln(&out, "Current ranking: current cost policy, retained usage and prices (not today's prices).")
	fmt.Fprintf(&out, "basis=%s reason=%s coverage: reported=%d/%d estimated=%d/%d %s\n", selection.Basis, selection.Reason, selection.ReportedEligible, selection.RequiredCount, selection.EstimatedEligible, selection.RequiredCount, selection.EstimateReason)
	sources := map[string]any{}
	for _, observation := range selection.Observations {
		provenance := observation.Source
		if observation.Pricing != nil {
			provenance += " pricing_sha256=" + observation.Pricing.SHA256
		}
		sources[provenance] = true
	}
	provenance := sortedTextKeys(sources)
	fmt.Fprintf(&out, "Provenance: %d retained source artifacts; showing up to 3 (full provenance in --format json).\n", len(provenance))
	if len(provenance) > 3 {
		provenance = provenance[:3]
	}
	for _, source := range provenance {
		fmt.Fprintln(&out, "source="+source)
	}
	fmt.Fprintf(&out, "primary=%v cost_order_available=%v cost_winner=%s\n", current["primary_metric"], current["cost_order_available"], currentTextValue(current["cost_winner"]))
	if current["primary_metric"] != "selected_cost_per_correct" {
		fmt.Fprintln(&out, "Token-primary order is independent of cost; selected costs do not replace it.")
	}
	fmt.Fprintln(&out, "No cost winner means unavailable, tied, or ineligible; it does not select a default winner.")
	tokenLines := []string{}
	table := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "rank\tpipeline\tpass\tfact\tevaluated\tcorrect\tmean USD\tUSD/correct\teligible\trun exit")
	for _, row := range currentRankingRows(current["ranking"]) {
		fmt.Fprintf(table, "%s\t%v\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", currentTextValue(row["rank"]), row["pipeline"], currentTextNumber(row["full_pass_rate"]), currentTextNumber(row["mean_fact_score"]), currentTextValue(row["evaluated_count"]), currentTextValue(row["valid_count"]), currentTextNumber(row["mean_selected_cost"]), currentTextNumber(row["selected_cost_per_correct"]), currentTextValue(row["recommendation_eligible"]), currentTextValue(row["exit_code"]))
		if primary, ok := row["runtime_total_tokens_per_correct_task"].(map[string]any); ok {
			tokenLines = append(tokenLines, fmt.Sprintf("%v token primary: %s (%s; reason=%s)", row["pipeline"], currentTextNumber(primary["value"]), currentTextValue(primary["state"]), currentTextValue(primary["reason"])))
		}
	}
	_ = table.Flush()
	for _, line := range tokenLines {
		fmt.Fprintln(&out, line)
	}
	if guarded, ok := current["acceptance_cost_selection"].(CostSelection); ok {
		fmt.Fprintf(&out, "Acceptance cost cohort: basis=%s reason=%s reported=%d/%d estimated=%d/%d\n", guarded.Basis, guarded.Reason, guarded.ReportedEligible, guarded.RequiredCount, guarded.EstimatedEligible, guarded.RequiredCount)
	}
	for _, line := range renderCurrentAcceptances(anyMap(current["current_acceptance"])) {
		fmt.Fprintln(&out, line)
	}
	if historical, ok := value["historical_ranking"].(json.RawMessage); ok {
		var ranking map[string]any
		if json.Unmarshal(historical, &ranking) == nil {
			fmt.Fprintln(&out, "\n"+RenderRanking(ranking))
		}
	}
	return strings.TrimSpace(out.String())
}

func currentRankingRows(value any) []map[string]any {
	if rows, ok := value.([]map[string]any); ok {
		return rows
	}
	rows := []map[string]any{}
	if generic, ok := value.([]any); ok {
		for _, item := range generic {
			if row, ok := item.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func currentTextValue(value any) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprint(value)
}

func currentTextNumber(value any) string {
	if number := summaryValue(value); number != nil {
		return fmt.Sprintf("%.6g", *number)
	}
	return "unavailable"
}

func sortedTextKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func renderCurrentAcceptances(values map[string]any) []string {
	lines := []string{}
	for _, pipeline := range sortedTextKeys(values) {
		acceptance := anyMap(values[pipeline])
		lines = append(lines, fmt.Sprintf("current acceptance pipeline=%s accepted=%s reasons=%s", pipeline, currentTextValue(acceptance["accepted"]), strings.Join(stringSlice(acceptance["reasons"]), ",")))
	}
	return lines
}

func renderSelectedCostTable(selected map[string]any) string {
	var out strings.Builder
	fmt.Fprintln(&out, "Current selected costs (USD):")
	table := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "side\tevaluated\tcorrect\tmean USD\tUSD/correct")
	for _, side := range Sides {
		row := anyMap(anyMap(selected["pipelines"])[side])
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", side, currentTextValue(row["evaluated_count"]), currentTextValue(row["full_pass_count"]), currentTextNumber(row["distribution"]), currentTextNumber(row["cost_per_correct"]))
	}
	_ = table.Flush()
	return strings.TrimSpace(out.String())
}
