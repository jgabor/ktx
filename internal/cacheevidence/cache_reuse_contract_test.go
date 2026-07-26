package cacheevidence

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type cacheReuseContract struct {
	Version string `json:"contract_version"`
	Status  string `json:"status"`
	Scope   struct {
		Runtime                  string `json:"runtime"`
		RuntimeVersion           string `json:"runtime_version"`
		RuntimeCommit            string `json:"runtime_commit"`
		RankingEffect            string `json:"ranking_effect"`
		HistoricalOutputMutation bool   `json:"historical_output_mutation"`
	} `json:"scope"`
	Accounting struct {
		Conclusion string `json:"conclusion"`
		Invariant  string `json:"invariant"`
		Usage      struct {
			Path    string `json:"path"`
			GitBlob string `json:"git_blob"`
		} `json:"usage_contract"`
		Mapping struct {
			Path          string `json:"path"`
			GitBlob       string `json:"git_blob"`
			UncachedInput string `json:"uncached_input"`
			CacheRead     string `json:"cache_read"`
			CacheWrite    string `json:"cache_write"`
		} `json:"event_mapping"`
		Local struct {
			Path                   string `json:"path"`
			SHA256                 string `json:"sha256"`
			MeasuredModelSteps     int    `json:"measured_model_steps"`
			PositiveCacheWriteStep int    `json:"positive_cache_write_steps"`
			Limitation             string `json:"limitation"`
		} `json:"local_retained_evidence"`
		OtherRuntime string `json:"other_runtime_or_version"`
	} `json:"accounting_evidence"`
	Direct struct {
		Unit              string `json:"unit"`
		ContributingSteps string `json:"contributing_steps"`
		UncachedInput     string `json:"uncached_input"`
		CacheRead         string `json:"cache_read"`
		CacheWrite        string `json:"cache_write"`
		LogicalInput      string `json:"logical_input"`
		Share             string `json:"share"`
		Availability      string `json:"availability"`
		MeasuredZero      string `json:"measured_zero"`
		Unknown           string `json:"unknown"`
		Malformed         string `json:"malformed"`
		Purpose           string `json:"purpose"`
	} `json:"direct"`
	Secondary struct {
		Method                   string            `json:"method"`
		Unit                     string            `json:"unit"`
		PriorCacheActivity       string            `json:"prior_cache_activity"`
		PriorLogicalInput        string            `json:"prior_logical_input"`
		CurrentLogicalInput      string            `json:"current_logical_input"`
		EstimatedReusableTokens  string            `json:"estimated_reusable_tokens"`
		Shortfall                string            `json:"shortfall"`
		ReuseShare               string            `json:"reuse_share"`
		Consistency              string            `json:"consistency"`
		SmallValueTreatment      string            `json:"small_value_treatment"`
		RankingEffect            string            `json:"ranking_effect"`
		TTLCause                 string            `json:"ttl_cause"`
		MissedCost               string            `json:"missed_cost"`
		NoiseFloorTokens         int               `json:"noise_floor_tokens"`
		PositiveCoverageRequired []string          `json:"positive_coverage_required"`
		IneligibilitySelection   string            `json:"ineligibility_selection"`
		PredicateInputs          map[string]string `json:"predicate_inputs"`
		IneligibilityRules       []struct {
			Reason    string `json:"reason"`
			Predicate string `json:"predicate"`
		} `json:"ineligibility_rules"`
	} `json:"secondary"`
	Comparison struct {
		Unit                 string `json:"unit"`
		Difference           string `json:"difference"`
		QualityPreserved     string `json:"quality_preserved"`
		IdentityProvenance   string `json:"identity_provenance"`
		CrossProviderPooling string `json:"cross_provider_pooling"`
		SetupExcluded        bool   `json:"setup_excluded"`
		MinimumCompletePairs int    `json:"minimum_complete_pairs"`
		Uncertainty          struct {
			Coverage     float64 `json:"coverage"`
			Sides        int     `json:"sides"`
			Method       string  `json:"method"`
			Center       string  `json:"center"`
			Spread       string  `json:"spread"`
			ZeroVariance string  `json:"zero_variance"`
		} `json:"uncertainty"`
		Direction map[string]string `json:"direction"`
	} `json:"comparison"`
	ClaimMatrix []struct {
		Outcome, Authorizes string
		Requires            []string
	} `json:"claim_matrix"`
	ComparativeBlockingReasonPrecedence []string `json:"comparative_blocking_reason_precedence"`
	PermanentlyUnsupportedClaims        []string `json:"permanently_unsupported_claims"`
	Compatibility                       struct {
		ExistingSummarySchemasUnchanged bool   `json:"existing_summary_schemas_unchanged"`
		HistoricalBytesUnchanged        bool   `json:"historical_bytes_unchanged"`
		CostPerCorrectRankingUnchanged  bool   `json:"cost_per_correct_ranking_unchanged"`
		HistoricalProviderFixtures      string `json:"historical_provider_fixtures"`
	} `json:"compatibility"`
}

func TestCacheReuseMetricContract(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "tools", "bench", "cache-reuse-contract-v1.json")
	var contract cacheReuseContract
	readJSON(t, path, &contract)

	if contract.Version != "cache-reuse-v1" || contract.Status != "contract_only" || contract.Scope.Runtime != "opencode" || contract.Scope.RuntimeVersion != "1.18.4" || contract.Scope.RuntimeCommit != "49c69c5ed3ccf706b61b3febb43c8aaff7f8325e" {
		t.Fatalf("unexpected contract identity: %#v", contract.Scope)
	}
	if contract.Scope.RankingEffect != "none" || contract.Scope.HistoricalOutputMutation {
		t.Fatalf("contract changes ranking or historical output: %#v", contract.Scope)
	}

	validateAccountingContract(t, root, contract)
	validateDirectAndSecondaryContract(t, contract)
	validateClaimContract(t, contract)
}

func validateAccountingContract(t *testing.T, root string, contract cacheReuseContract) {
	t.Helper()
	accounting := contract.Accounting
	if accounting.Conclusion != "cache_write_is_disjoint" || accounting.Invariant != "nonCachedInputTokens + cacheReadInputTokens + cacheWriteInputTokens = inputTokens" {
		t.Fatalf("unexpected accounting conclusion: %#v", accounting)
	}
	if accounting.Usage.Path != "packages/llm/src/schema/events.ts" || accounting.Usage.GitBlob != "98fcc9a24d417e70589e529fa1a1dd21ef35769c" {
		t.Errorf("usage contract is not pinned: %#v", accounting.Usage)
	}
	if accounting.Mapping.Path != "packages/core/src/session/runner/publish-llm-event.ts" || accounting.Mapping.GitBlob != "33652a618c93ebc6edd1a54410983d361e8f5e48" || accounting.Mapping.UncachedInput != "usage.nonCachedInputTokens" || accounting.Mapping.CacheRead != "usage.cacheReadInputTokens" || accounting.Mapping.CacheWrite != "usage.cacheWriteInputTokens" {
		t.Errorf("event mapping is not pinned: %#v", accounting.Mapping)
	}
	if accounting.Local.MeasuredModelSteps != 3 || accounting.Local.PositiveCacheWriteStep != 0 || accounting.Local.Limitation != "agrees_with_the_invariant_but_does_not_demonstrate_positive_cache_write" {
		t.Errorf("local evidence limitation changed: %#v", accounting.Local)
	}
	checkHash(t, filepath.Join(root, filepath.FromSlash(accounting.Local.Path)), accounting.Local.SHA256)
	if accounting.OtherRuntime != "ineligible_without_pinned_equivalent_disjoint_invariant" {
		t.Errorf("other runtime fallback = %q", accounting.OtherRuntime)
	}
}

func validateDirectAndSecondaryContract(t *testing.T, contract cacheReuseContract) {
	t.Helper()
	direct := contract.Direct
	if direct.Unit != "attempt" || direct.ContributingSteps != "all_accepted_model_steps" || direct.UncachedInput != "sum(step.uncached_input)" || direct.CacheRead != "sum(step.cache_read)" || direct.CacheWrite != "sum(step.cache_write)" || direct.LogicalInput != "uncached_input + cache_read + cache_write" || direct.Share != "cache_read / logical_input" {
		t.Fatalf("direct formula changed: %#v", direct)
	}
	if direct.Availability != "every_contributing_bucket_measured_and_logical_input_positive" || direct.MeasuredZero != "retained" || direct.Unknown != "unavailable_not_backfilled" || direct.Malformed != "malformed_not_backfilled" || direct.Purpose != "descriptive_non_ranking" {
		t.Errorf("direct availability changed: %#v", direct)
	}

	secondary := contract.Secondary
	if secondary.Method != "pi_consecutive_step_reusable_basis" || secondary.EstimatedReusableTokens != "min(prior_logical_input, current_logical_input)" || secondary.Shortfall != "estimated_reusable_tokens - current.cache_read" || secondary.ReuseShare != "current.cache_read / estimated_reusable_tokens" || secondary.Consistency != "0 <= current.cache_read <= estimated_reusable_tokens" {
		t.Fatalf("secondary formula changed: %#v", secondary)
	}
	wantCoverage := []string{"adjacent_runtime_order", "no_retry_boundary", "no_reset_or_compaction_boundary", "native_runtime_session_continuity"}
	if !reflect.DeepEqual(secondary.PositiveCoverageRequired, wantCoverage) || secondary.PriorCacheActivity != "prior.cache_read.state == 'measured' && prior.cache_write.state == 'measured' && prior.cache_read.value + prior.cache_write.value > 0" {
		t.Errorf("secondary coverage changed: %#v", secondary)
	}
	if secondary.NoiseFloorTokens != 1024 || secondary.SmallValueTreatment != "retain_exact_value; shortfall <= 1024 is within_noise_floor; shortfall > 1024 is above_noise_floor" || secondary.RankingEffect != "none" || secondary.TTLCause != "unsupported" || secondary.MissedCost != "unsupported" {
		t.Errorf("secondary interpretation changed: %#v", secondary)
	}
	if secondary.IneligibilitySelection != "evaluate ineligibility_rules in array order and retain the first rule whose predicate is true; no matching rule means eligible" || len(secondary.PredicateInputs) != 20 {
		t.Errorf("ineligibility selection contract is incomplete: selection=%q inputs=%v", secondary.IneligibilitySelection, secondary.PredicateInputs)
	}
	validateSecondaryRules(t, secondary.IneligibilityRules)
}

func validateSecondaryRules(t *testing.T, rules []struct {
	Reason    string `json:"reason"`
	Predicate string `json:"predicate"`
},
) {
	t.Helper()
	wantNames := secondaryReasonNames()
	if len(rules) != len(wantNames) {
		t.Fatalf("ineligibility rules=%d, want %d", len(rules), len(wantNames))
	}
	seen := map[string]bool{}
	for i, rule := range rules {
		if rule.Reason != wantNames[i] || rule.Predicate == "" || seen[rule.Reason] {
			t.Errorf("invalid ineligibility rule %d: %#v", i, rule)
		}
		seen[rule.Reason] = true
	}
	wantActivity := "prior.exists && prior.cache_read.state == 'measured' && prior.cache_write.state == 'measured' && prior.cache_read.value + prior.cache_write.value == 0"
	if rules[12].Reason != "prior_cache_activity_absent" || rules[12].Predicate != wantActivity {
		t.Errorf("prior activity predicate = %#v", rules[12])
	}
}

type secondaryValue struct {
	state string
	value float64
}

type secondaryIdentity struct {
	state string
	value string
}

type secondaryPairInput struct {
	priorExists             bool
	orderCoverage           string
	adjacent                bool
	retryCoverage           string
	resetCoverage           string
	concurrentInterleaving  bool
	priorSession            secondaryIdentity
	currentSession          secondaryIdentity
	priorRuntimeContract    string
	currentRuntimeContract  string
	requiredRuntimeContract string
	priorUncached           secondaryValue
	priorRead               secondaryValue
	priorWrite              secondaryValue
	currentUncached         secondaryValue
	currentRead             secondaryValue
	currentWrite            secondaryValue
}

func TestSecondaryIneligibilityPrecedence(t *testing.T) {
	valid := validSecondaryPair()
	cases := []struct {
		name       string
		mutate     func(*secondaryPairInput)
		want       string
		wantRawMin int
	}{
		{"eligible", func(*secondaryPairInput) {}, "", 0},
		{"nonconsecutive precedes retry", func(in *secondaryPairInput) { in.adjacent = false; in.retryCoverage = "boundary_observed" }, "non_consecutive_order", 2},
		{"unavailable order precedes retry", func(in *secondaryPairInput) { in.orderCoverage = "unavailable"; in.retryCoverage = "boundary_observed" }, "order_coverage_unavailable", 2},
		{"retry unavailable precedes reset", func(in *secondaryPairInput) { in.retryCoverage = "unavailable"; in.resetCoverage = "boundary_observed" }, "retry_coverage_unavailable", 2},
		{"identity unavailable precedes runtime mismatch", func(in *secondaryPairInput) { in.priorSession.state = "unknown"; in.currentRuntimeContract = "other" }, "runtime_session_identity_unavailable", 2},
		{"measured absent activity precedes unknown uncached", func(in *secondaryPairInput) { in.priorRead.value = 0; in.priorUncached.state = "unknown" }, "prior_cache_activity_absent", 2},
		{"unknown read cannot mean absent activity", func(in *secondaryPairInput) { in.priorRead.state = "unknown"; in.priorWrite.state = "malformed" }, "prior_cache_read_unknown", 2},
		{"malformed uncached precedes unknown read", func(in *secondaryPairInput) { in.priorUncached.state = "malformed"; in.priorRead.state = "unknown" }, "prior_uncached_input_malformed", 2},
		{"current read unknown precedes malformed write", func(in *secondaryPairInput) { in.currentRead.state = "unknown"; in.currentWrite.state = "malformed" }, "current_cache_read_unknown", 2},
		{"all-zero prior activity precedes logical failures", func(in *secondaryPairInput) {
			in.priorUncached.value = 0
			in.priorRead.value = 0
			in.currentUncached.value = 0
			in.currentRead.value = 0
		}, "prior_cache_activity_absent", 3},
		{"current zero logical input precedes reusable basis", func(in *secondaryPairInput) { in.currentUncached.value = 0; in.currentRead.value = 0 }, "current_logical_input_not_positive", 2},
		{"read exceeds reusable basis", func(in *secondaryPairInput) { in.priorUncached.value = 1; in.currentRead.value = 3 }, "current_cache_read_exceeds_estimated_reusable_tokens", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.mutate(&input)
			matches := secondaryRuleMatches(input)
			selected := selectSecondaryReason(matches)
			if selected != tc.want {
				t.Fatalf("selected %q from %v, want %q", selected, matches, tc.want)
			}
			if len(matches) < tc.wantRawMin {
				t.Fatalf("raw matches %v, want at least %d to exercise overlap", matches, tc.wantRawMin)
			}
			selectedReasons := []string{}
			if selected != "" {
				selectedReasons = append(selectedReasons, selected)
			}
			wantSelected := 1
			if tc.want == "" {
				wantSelected = 0
			}
			if len(selectedReasons) != wantSelected {
				t.Fatalf("selected reasons = %v, want exactly %d", selectedReasons, wantSelected)
			}
		})
	}
}

func validSecondaryPair() secondaryPairInput {
	measured := func(value float64) secondaryValue { return secondaryValue{state: "measured", value: value} }
	return secondaryPairInput{
		priorExists: true, orderCoverage: "covered", adjacent: true,
		retryCoverage: "covered_no_boundary", resetCoverage: "covered_no_boundary",
		priorSession: secondaryIdentity{state: "measured", value: "session"}, currentSession: secondaryIdentity{state: "measured", value: "session"},
		priorRuntimeContract: "required", currentRuntimeContract: "required", requiredRuntimeContract: "required",
		priorUncached: measured(10), priorRead: measured(1), priorWrite: measured(0),
		currentUncached: measured(10), currentRead: measured(1), currentWrite: measured(0),
	}
}

func secondaryReasonNames() []string {
	return []string{
		"first_step", "non_consecutive_order", "order_coverage_unavailable", "order_coverage_contradictory",
		"retry_coverage_unavailable", "retry_boundary_observed", "reset_coverage_unavailable", "reset_or_compaction_boundary_observed",
		"concurrent_interleaving", "runtime_session_identity_unavailable", "runtime_session_identity_changed", "runtime_contract_mismatch",
		"prior_cache_activity_absent", "prior_uncached_input_unknown", "prior_uncached_input_malformed", "prior_cache_read_unknown",
		"prior_cache_read_malformed", "prior_cache_write_unknown", "prior_cache_write_malformed", "current_uncached_input_unknown",
		"current_uncached_input_malformed", "current_cache_read_unknown", "current_cache_read_malformed", "current_cache_write_unknown",
		"current_cache_write_malformed", "prior_logical_input_not_positive", "current_logical_input_not_positive",
		"estimated_reusable_tokens_not_positive", "current_cache_read_exceeds_estimated_reusable_tokens",
	}
}

func secondaryRuleMatches(in secondaryPairInput) []string {
	var matches []string
	add := func(reason string, matchesRule bool) {
		if matchesRule {
			matches = append(matches, reason)
		}
	}
	add("first_step", !in.priorExists)
	add("non_consecutive_order", in.priorExists && in.orderCoverage == "covered" && !in.adjacent)
	add("order_coverage_unavailable", in.priorExists && in.orderCoverage == "unavailable")
	add("order_coverage_contradictory", in.priorExists && in.orderCoverage == "contradictory")
	add("retry_coverage_unavailable", in.priorExists && in.retryCoverage == "unavailable")
	add("retry_boundary_observed", in.priorExists && in.retryCoverage == "boundary_observed")
	add("reset_coverage_unavailable", in.priorExists && in.resetCoverage == "unavailable")
	add("reset_or_compaction_boundary_observed", in.priorExists && in.resetCoverage == "boundary_observed")
	add("concurrent_interleaving", in.priorExists && in.concurrentInterleaving)
	add("runtime_session_identity_unavailable", in.priorExists && (in.priorSession.state != "measured" || in.currentSession.state != "measured"))
	add("runtime_session_identity_changed", in.priorExists && in.priorSession.state == "measured" && in.currentSession.state == "measured" && in.priorSession.value != in.currentSession.value)
	add("runtime_contract_mismatch", in.priorExists && (in.priorRuntimeContract != in.requiredRuntimeContract || in.currentRuntimeContract != in.requiredRuntimeContract))
	add("prior_cache_activity_absent", in.priorExists && measured(in.priorRead, in.priorWrite) && in.priorRead.value+in.priorWrite.value == 0)
	add("prior_uncached_input_unknown", in.priorExists && in.priorUncached.state == "unknown")
	add("prior_uncached_input_malformed", in.priorExists && in.priorUncached.state == "malformed")
	add("prior_cache_read_unknown", in.priorExists && in.priorRead.state == "unknown")
	add("prior_cache_read_malformed", in.priorExists && in.priorRead.state == "malformed")
	add("prior_cache_write_unknown", in.priorExists && in.priorWrite.state == "unknown")
	add("prior_cache_write_malformed", in.priorExists && in.priorWrite.state == "malformed")
	add("current_uncached_input_unknown", in.priorExists && in.currentUncached.state == "unknown")
	add("current_uncached_input_malformed", in.priorExists && in.currentUncached.state == "malformed")
	add("current_cache_read_unknown", in.priorExists && in.currentRead.state == "unknown")
	add("current_cache_read_malformed", in.priorExists && in.currentRead.state == "malformed")
	add("current_cache_write_unknown", in.priorExists && in.currentWrite.state == "unknown")
	add("current_cache_write_malformed", in.priorExists && in.currentWrite.state == "malformed")
	currentLogical := in.currentUncached.value + in.currentRead.value + in.currentWrite.value
	priorLogical := in.priorUncached.value + in.priorRead.value + in.priorWrite.value
	add("prior_logical_input_not_positive", in.priorExists && measured(in.priorUncached, in.priorRead, in.priorWrite) && priorLogical <= 0)
	add("current_logical_input_not_positive", in.priorExists && measured(in.currentUncached, in.currentRead, in.currentWrite) && currentLogical <= 0)
	add("estimated_reusable_tokens_not_positive", in.priorExists && measured(in.priorUncached, in.priorRead, in.priorWrite, in.currentUncached, in.currentRead, in.currentWrite) && min(priorLogical, currentLogical) <= 0)
	add("current_cache_read_exceeds_estimated_reusable_tokens", in.priorExists && measured(in.priorUncached, in.priorRead, in.priorWrite, in.currentUncached, in.currentRead, in.currentWrite) && priorLogical > 0 && currentLogical > 0 && in.currentRead.value > min(priorLogical, currentLogical))
	return matches
}

func selectSecondaryReason(matches []string) string {
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func measured(values ...secondaryValue) bool {
	for _, value := range values {
		if value.state != "measured" {
			return false
		}
	}
	return true
}

func validateClaimContract(t *testing.T, contract cacheReuseContract) {
	t.Helper()
	comparison := contract.Comparison
	if comparison.Unit != "measured_cycle_pair" || !comparison.SetupExcluded || comparison.Difference != "candidate_direct_share - baseline_direct_share" || comparison.MinimumCompletePairs != 5 {
		t.Fatalf("comparison unit changed: %#v", comparison)
	}
	if comparison.Uncertainty.Coverage != 0.95 || comparison.Uncertainty.Sides != 2 || comparison.Uncertainty.Method != "student_t_interval_on_paired_differences" || comparison.Uncertainty.Spread != "sample_standard_deviation_denominator_n_minus_1" || comparison.Uncertainty.ZeroVariance != "interval_equals_mean" {
		t.Errorf("uncertainty contract changed: %#v", comparison.Uncertainty)
	}
	if comparison.Direction["higher"] != "lower_bound > 0" || comparison.Direction["lower"] != "upper_bound < 0" || comparison.Direction["inconclusive"] != "lower_bound <= 0 && upper_bound >= 0" {
		t.Errorf("direction matrix changed: %#v", comparison.Direction)
	}
	if comparison.CrossProviderPooling != "forbidden" || comparison.QualityPreserved == "" || comparison.IdentityProvenance == "" {
		t.Errorf("comparison gates incomplete: %#v", comparison)
	}
	wantOutcomes := map[string]bool{"direct_available": false, "comparative_descriptive": false, "candidate_higher_success": false, "candidate_lower": false, "inconclusive": false, "secondary_available": false}
	for _, row := range contract.ClaimMatrix {
		if _, ok := wantOutcomes[row.Outcome]; !ok || len(row.Requires) == 0 || row.Authorizes == "" {
			t.Errorf("invalid claim row: %#v", row)
			continue
		}
		wantOutcomes[row.Outcome] = true
	}
	for outcome, found := range wantOutcomes {
		if !found {
			t.Errorf("missing claim outcome %q", outcome)
		}
	}
	if len(contract.ComparativeBlockingReasonPrecedence) != 10 || contract.ComparativeBlockingReasonPrecedence[0] != "unsupported_runtime_accounting" || contract.ComparativeBlockingReasonPrecedence[9] != "quality_not_preserved" {
		t.Errorf("comparative blockers changed: %v", contract.ComparativeBlockingReasonPrecedence)
	}
	wantUnsupported := []string{"provider_request", "provider_cache_hit", "provider_cache_miss", "provider_cache_fill", "provider_cache_eviction", "provider_cache_eligibility", "provider_visible_prefix", "provider_cache_isolation", "ttl_cause", "missed_cost", "causal_compression_effect"}
	if !reflect.DeepEqual(contract.PermanentlyUnsupportedClaims, wantUnsupported) {
		t.Errorf("unsupported claims changed: %v", contract.PermanentlyUnsupportedClaims)
	}
	compatibility := contract.Compatibility
	if !compatibility.ExistingSummarySchemasUnchanged || !compatibility.HistoricalBytesUnchanged || !compatibility.CostPerCorrectRankingUnchanged || compatibility.HistoricalProviderFixtures != "compatibility_and_provenance_only" {
		t.Errorf("historical boundary changed: %#v", compatibility)
	}

	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "cache-reuse-metric-contract.md"))
	if err != nil || len(doc) == 0 {
		t.Fatalf("contract documentation unavailable: %v", err)
	}
	for _, rule := range contract.Secondary.IneligibilityRules {
		if !strings.Contains(string(doc), "`"+rule.Reason+"`") {
			t.Errorf("contract documentation omits ineligibility reason %q", rule.Reason)
		}
	}
}
