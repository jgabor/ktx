package reuse

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type cacheReuseContract struct {
	Version string `json:"contract_version"`
	Status  string `json:"status"`
	Scope   struct {
		Runtime        string `json:"runtime"`
		RuntimeVersion string `json:"runtime_version"`
		RuntimeCommit  string `json:"runtime_commit"`
		EvidenceKind   string `json:"evidence_kind"`
		ModelContext   struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Variant  string `json:"variant"`
		} `json:"model_context"`
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
			Classification         string `json:"classification"`
			RuntimeVersion         string `json:"runtime_version"`
			MeasuredModelSteps     int    `json:"measured_model_steps"`
			PositiveCacheWriteStep int    `json:"positive_cache_write_steps"`
			Limitation             string `json:"limitation"`
		} `json:"local_retained_evidence"`
		OtherRuntime string `json:"other_runtime_or_version"`
	} `json:"accounting_evidence"`
	SourceAuthorities map[string]struct {
		Path    string `json:"path"`
		GitBlob string `json:"git_blob"`
	} `json:"source_authorities"`
	Sequence struct {
		ProvenanceRequired []string          `json:"provenance_required"`
		CoverageRule       string            `json:"coverage_rule"`
		EventHook          map[string]string `json:"event_hook"`
		WrapperStream      map[string]string `json:"wrapper_stream"`
		CollectorFacts     string            `json:"collector_boundary_facts"`
		Silence            string            `json:"silence"`
	} `json:"sequence_evidence"`
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
		Output            struct {
			SummarySchemaVersion int    `json:"summary_schema_version"`
			Path                 string `json:"path"`
		} `json:"output"`
	} `json:"direct"`
	Secondary struct {
		Method                   string   `json:"method"`
		Unit                     string   `json:"unit"`
		PriorCacheActivity       string   `json:"prior_cache_activity"`
		PriorLogicalInput        string   `json:"prior_logical_input"`
		CurrentLogicalInput      string   `json:"current_logical_input"`
		EstimatedReusableTokens  string   `json:"estimated_reusable_tokens"`
		Shortfall                string   `json:"shortfall"`
		ReuseShare               string   `json:"reuse_share"`
		Consistency              string   `json:"consistency"`
		SmallValueTreatment      string   `json:"small_value_treatment"`
		RankingEffect            string   `json:"ranking_effect"`
		TTLCause                 string   `json:"ttl_cause"`
		MissedCost               string   `json:"missed_cost"`
		NoiseFloorTokens         int      `json:"noise_floor_tokens"`
		PositiveCoverageRequired []string `json:"positive_coverage_required"`
		Output                   struct {
			SummarySchemaVersion  int      `json:"summary_schema_version"`
			Path                  string   `json:"path"`
			Label                 string   `json:"label"`
			PerPairValues         []string `json:"per_pair_values"`
			Counts                string   `json:"counts"`
			ValidationReport      string   `json:"validation_report"`
			IntegerRepresentation string   `json:"integer_representation"`
			ShareRepresentation   string   `json:"share_representation"`
			RankingEffect         string   `json:"ranking_effect"`
			TTLCause              string   `json:"ttl_cause"`
			ExpiryCause           string   `json:"expiry_cause"`
			LegacySchemas         []int    `json:"legacy_schema_versions_unchanged"`
		} `json:"output"`
		IneligibilitySelection string            `json:"ineligibility_selection"`
		PredicateInputs        map[string]string `json:"predicate_inputs"`
		IneligibilityRules     []struct {
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
		Output    struct {
			SummarySchemaVersion int `json:"summary_schema_version"`
		} `json:"output"`
	} `json:"comparison"`
	StatementAuthorization struct {
		SummarySchemaVersion   int      `json:"summary_schema_version"`
		Path                   string   `json:"path"`
		InputAuthority         []string `json:"input_authority"`
		Outcomes               []string `json:"outcomes"`
		OutcomeShape           []string `json:"outcome_shape"`
		ReasonPrecedence       []string `json:"reason_precedence"`
		ProjectionVerification string   `json:"projection_verification"`
		ProjectionReason       string   `json:"projection_inconsistency_reason"`
		ReasonNormalization    string   `json:"reason_normalization"`
		NullWhenUnauthorized   bool     `json:"statement_null_when_not_authorized"`
		WordingScope           string   `json:"wording_scope"`
		IdentityClasses        []string `json:"identity_classifications"`
		ConfiguredBackfill     bool     `json:"configured_identity_backfills_source_events"`
		AbsentPermitted        bool     `json:"absent_runtime_emitted_identity_permitted"`
		PresentMustAgree       bool     `json:"present_runtime_emitted_identity_must_agree"`
		SecondaryCannotAffect  bool     `json:"secondary_cannot_influence_direct_authorization"`
		ValidationReport       string   `json:"validation_report"`
		LegacySchemas          []int    `json:"legacy_schema_versions_unchanged"`
	} `json:"statement_authorization"`
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

type benchmarkMethodology struct {
	MethodologyVersion string `yaml:"methodology_version"`
	Date               string `yaml:"date"`
	Scope              string `yaml:"scope"`
	Inputs             struct {
		Agent struct {
			Binary  string `yaml:"binary"`
			Version string `yaml:"version"`
		} `yaml:"agent"`
		Model struct {
			ID       string `yaml:"id"`
			Provider string `yaml:"provider"`
			Variant  string `yaml:"variant"`
		} `yaml:"model"`
		Task struct {
			Identifier     string `yaml:"identifier"`
			Prompt         string `yaml:"prompt"`
			ExpectedOutput string `yaml:"expected_output"`
		} `yaml:"task"`
		Scoring struct {
			Function string `yaml:"function"`
		} `yaml:"scoring"`
	} `yaml:"inputs"`
	Trials struct {
		Phases struct {
			ColdObservation     int `yaml:"cold_observation"`
			WarmupDefault       int `yaml:"warmup_default"`
			MeasuredWarmDefault int `yaml:"measured_warm_default"`
		} `yaml:"phases"`
		Accounting string `yaml:"accounting"`
		Order      string `yaml:"order"`
	} `yaml:"trials"`
	CacheControl struct {
		Modes            []string `yaml:"modes"`
		Contract         string   `yaml:"contract"`
		RetainedMetadata string   `yaml:"retained_metadata"`
		OpenCodeRuntime  string   `yaml:"opencode_runtime"`
	} `yaml:"cache_control"`
	Computation string `yaml:"computation"`
	ClaimScope  struct {
		EvidenceKind           string `yaml:"evidence_kind"`
		DirectMetric           string `yaml:"direct_metric"`
		SecondaryMethod        string `yaml:"secondary_method"`
		ComparisonUnit         string `yaml:"comparison_unit"`
		StatementAuthorization string `yaml:"statement_authorization"`
		ProviderNativeClaims   string `yaml:"provider_native_claims"`
		SignificanceClaims     string `yaml:"significance_claims"`
		CausalClaims           string `yaml:"causal_claims"`
	} `yaml:"claim_scope"`
	Output struct {
		SummarySchema int    `yaml:"summary_schema"`
		HeadlinePhase string `yaml:"headline_phase"`
		Validation    string `yaml:"validation"`
	} `yaml:"output"`
	OutOfScope []string `yaml:"out_of_scope"`
}

func TestCacheReuseMetricContract(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	path := filepath.Join(root, "tools", "bench", "cache-reuse-contract-v1.json")
	var contract cacheReuseContract
	readJSON(t, path, &contract)

	if contract.Version != "cache-reuse-v1" || contract.Status != "direct_secondary_comparison_and_statement_authorization_implemented" || contract.Scope.Runtime != "opencode" || contract.Scope.RuntimeVersion != "1.18.5" || contract.Scope.RuntimeCommit != "e5cc278dec9294a627a7b05f47ce6a564408c1a2" {
		t.Fatalf("unexpected contract identity: %#v", contract.Scope)
	}
	if contract.Scope.ModelContext.Provider != "openai" || contract.Scope.ModelContext.Model != "gpt-5.6-luna" || contract.Scope.ModelContext.Variant != "high" {
		t.Fatalf("unexpected contract model context: %#v", contract.Scope.ModelContext)
	}
	if contract.Scope.RankingEffect != "none" || contract.Scope.HistoricalOutputMutation {
		t.Fatalf("contract changes ranking or historical output: %#v", contract.Scope)
	}

	validateAccountingContract(t, root, contract)
	validateSequenceContract(t, contract)
	validateDirectAndSecondaryContract(t, contract)
	validateClaimContract(t, contract)
	validateStatementAuthorizationContract(t, contract)
}

func TestCacheAwareBenchmarkVehicleMatchesMetricContract(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var contract cacheReuseContract
	readJSON(t, filepath.Join(root, "tools", "bench", "cache-reuse-contract-v1.json"), &contract)

	schema := contract.Direct.Output.SummarySchemaVersion
	if schema == 0 || contract.Secondary.Output.SummarySchemaVersion != schema || contract.Comparison.Output.SummarySchemaVersion != schema || contract.StatementAuthorization.SummarySchemaVersion != schema {
		t.Fatalf("cache-reuse contract summary schemas disagree: direct=%d secondary=%d comparison=%d authorization=%d", schema, contract.Secondary.Output.SummarySchemaVersion, contract.Comparison.Output.SummarySchemaVersion, contract.StatementAuthorization.SummarySchemaVersion)
	}

	dockerfile := readText(t, filepath.Join(root, "Dockerfile.benchmark"))
	versionMatch := regexp.MustCompile(`(?m)^ARG\s+OPENCODE_VERSION\s*=\s*([^\s#]+)`).FindStringSubmatch(dockerfile)
	if len(versionMatch) != 2 || versionMatch[1] != contract.Scope.RuntimeVersion {
		t.Fatalf("Dockerfile benchmark OpenCode version = %q, want contract version %q", matchedValue(versionMatch), contract.Scope.RuntimeVersion)
	}
	for name, pattern := range map[string]string{
		"pinned OpenCode install": `(?m)^RUN\s+npm install -g opencode-ai@\$\{OPENCODE_VERSION\}\s*$`,
		"local source copy":       `(?m)^COPY\s+\.\s+\.\s*$`,
		"local ktx build":         `(?m)^RUN\s+go build\s+[^\n]*-o\s+/out/ktx\s+\./cmd/ktx\s*$`,
		"local binary copy":       `(?m)^COPY\s+--from=ktx-build\s+/out/ktx\s+/usr/local/bin/ktx\s*$`,
		"embedded plugin install": `(?m)^RUN\s+ktx init opencode\s*$`,
	} {
		if !regexp.MustCompile(pattern).MatchString(dockerfile) {
			t.Errorf("Dockerfile benchmark omits %s", name)
		}
	}

	methodologyText := readText(t, filepath.Join(root, "tools", "bench", "methodology-v5-cache-aware.yaml"))
	methodology, err := decodeBenchmarkMethodology(strings.NewReader(methodologyText))
	if err != nil {
		t.Fatal(err)
	}
	if methodology.MethodologyVersion != "v5-cache-aware" || methodology.Inputs.Agent.Binary != contract.Scope.Runtime || methodology.Inputs.Agent.Version != contract.Scope.RuntimeVersion ||
		methodology.Inputs.Model.Provider != contract.Scope.ModelContext.Provider || methodology.Inputs.Model.ID != contract.Scope.ModelContext.Model || methodology.Inputs.Model.Variant != contract.Scope.ModelContext.Variant || methodology.Output.SummarySchema != schema {
		t.Fatalf("methodology runtime/model/schema does not match machine contract: %#v", methodology)
	}
	if methodology.ClaimScope.EvidenceKind != contract.Scope.EvidenceKind || methodology.ClaimScope.DirectMetric != strings.Split(contract.Direct.Output.Path, ".")[0] ||
		methodology.ClaimScope.SecondaryMethod != contract.Secondary.Method || methodology.ClaimScope.ComparisonUnit != contract.Comparison.Unit ||
		methodology.ClaimScope.StatementAuthorization != contract.StatementAuthorization.Path || methodology.ClaimScope.ProviderNativeClaims != "unsupported" ||
		methodology.ClaimScope.SignificanceClaims != "unsupported" || methodology.ClaimScope.CausalClaims != "unsupported" {
		t.Fatalf("methodology claim scope does not match machine contract: %#v", methodology.ClaimScope)
	}
}

func TestBenchmarkMethodologyRejectsDuplicateKeysAndExtraDocuments(t *testing.T) {
	valid := readText(t, filepath.Join("..", "..", "..", "tools", "bench", "methodology-v5-cache-aware.yaml"))
	duplicate := strings.Replace(valid, `    version: "1.18.5"`, "    version: \"1.18.5\"\n    version: \"1.18.5\"", 1)
	if _, err := decodeBenchmarkMethodology(strings.NewReader(duplicate)); err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("duplicate-key decode error = %v", err)
	}
	if _, err := decodeBenchmarkMethodology(strings.NewReader(valid + "\n---\nmethodology_version: second\n")); err == nil || !strings.Contains(err.Error(), "extra YAML document") {
		t.Fatalf("extra-document decode error = %v", err)
	}
}

func decodeBenchmarkMethodology(reader io.Reader) (benchmarkMethodology, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var methodology benchmarkMethodology
	if err := decoder.Decode(&methodology); err != nil {
		return methodology, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return methodology, err
		}
		return methodology, errors.New("extra YAML document")
	}
	return methodology, nil
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func matchedValue(match []string) string {
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func validateStatementAuthorizationContract(t *testing.T, contract cacheReuseContract) {
	t.Helper()
	authorization := contract.StatementAuthorization
	wantInputs := []string{"direct_cached_input_share", "estimated_consecutive_step_reuse", "cached_input_share_comparison"}
	wantOutcomes := []string{"direct_available", "comparative_descriptive", "candidate_higher_success", "candidate_lower", "inconclusive", "secondary_available"}
	wantShape := []string{"authorized", "reasons", "gates", "evidence", "scope", "statement"}
	wantReasons := []string{"unsupported_runtime_accounting", "direct_evidence_incomplete", "evidence_projection_inconsistent", "cycle_pair_incomplete", "protocol_noncompliant", "identity_provenance_unstable", "runtime_emitted_identity_conflict", "fewer_than_five_complete_pairs", "two_sided_95_percent_interval_unavailable", "two_sided_95_percent_interval_contains_zero", "candidate_not_higher", "candidate_not_lower", "two_sided_95_percent_interval_excludes_zero", "quality_not_preserved", "no_eligible_consecutive_step_pairs"}
	wantIdentity := []string{"configured_only", "absent", "partial", "present"}
	if authorization.SummarySchemaVersion != 4 || authorization.Path != "statement_authorization" ||
		!reflect.DeepEqual(authorization.InputAuthority, wantInputs) || !reflect.DeepEqual(authorization.Outcomes, wantOutcomes) ||
		!reflect.DeepEqual(authorization.OutcomeShape, wantShape) || !reflect.DeepEqual(authorization.ReasonPrecedence, wantReasons) || !authorization.NullWhenUnauthorized ||
		authorization.ProjectionVerification == "" || authorization.ProjectionReason != "evidence_projection_inconsistent" || authorization.ReasonNormalization == "" ||
		authorization.WordingScope != "OpenCode-reported cached-input share under the retained protocol" ||
		!reflect.DeepEqual(authorization.IdentityClasses, wantIdentity) || authorization.ConfiguredBackfill ||
		!authorization.AbsentPermitted || !authorization.PresentMustAgree || !authorization.SecondaryCannotAffect ||
		authorization.ValidationReport != "same statement authorization" || !reflect.DeepEqual(authorization.LegacySchemas, []int{1, 2, 3}) {
		t.Fatalf("statement authorization contract is incomplete: %#v", authorization)
	}
	for _, required := range []string{"unique positive cycles", "globally unreused attempt IDs", "authoritative direct attempt rows", "matching side", "measured-warm phase", "cycle", "exact measured share", "identity"} {
		if !strings.Contains(authorization.ProjectionVerification, required) {
			t.Errorf("projection verification omits %q: %q", required, authorization.ProjectionVerification)
		}
	}
}

func validateSequenceContract(t *testing.T, contract cacheReuseContract) {
	t.Helper()
	want := []string{"runtime", "runtime_version", "source", "start_ordinal", "end_ordinal"}
	if !reflect.DeepEqual(contract.Sequence.ProvenanceRequired, want) || contract.Sequence.CoverageRule == "" || contract.Sequence.CollectorFacts == "" || contract.Sequence.Silence != "never_coverage" {
		t.Fatalf("sequence provenance contract = %#v", contract.Sequence)
	}
	for _, class := range []string{"order", "retry", "reset", "identity", "interruption", "restart", "concurrency"} {
		if contract.Sequence.EventHook[class] == "" || contract.Sequence.WrapperStream[class] == "" {
			t.Errorf("sequence class %q is not explicit on both collection paths", class)
		}
	}
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
	if accounting.Local.Classification != "historical_corroboration" || accounting.Local.RuntimeVersion != "1.18.4" || accounting.Local.MeasuredModelSteps != 3 || accounting.Local.PositiveCacheWriteStep != 0 || accounting.Local.Limitation != "agrees_with_the_invariant_but_does_not_demonstrate_positive_cache_write" {
		t.Errorf("local evidence limitation changed: %#v", accounting.Local)
	}
	checkHash(t, filepath.Join(root, filepath.FromSlash(accounting.Local.Path)), accounting.Local.SHA256)
	if accounting.OtherRuntime != "ineligible_without_pinned_equivalent_disjoint_invariant" {
		t.Errorf("other runtime fallback = %q", accounting.OtherRuntime)
	}
	wantAuthorities := map[string]struct{ path, blob string }{
		"usage_contract":              {"packages/llm/src/schema/events.ts", "98fcc9a24d417e70589e529fa1a1dd21ef35769c"},
		"event_mapping":               {"packages/core/src/session/runner/publish-llm-event.ts", "33652a618c93ebc6edd1a54410983d361e8f5e48"},
		"event_union":                 {"packages/sdk/js/src/gen/types.gen.ts", "5e4fd8906155b9c88d29cda1c44adafcb09b4aaf"},
		"plugin_event_hook":           {"packages/plugin/src/index.ts", "edfa0139dfcaf0e877ab906fabe8e0527afc3915"},
		"run_json_formatter":          {"packages/opencode/src/cli/cmd/run.ts", "3927f615a08053a0c631e8baaa1cbd0337a37127"},
		"openai_cached_token_mapping": {"packages/llm/src/protocols/openai-responses.ts", "4936d31c921b229d3e56a43e0c831d5bdd1d1790"},
		"openai_codex_adapter":        {"packages/opencode/src/plugin/openai/codex.ts", "d16b7495654c8da0bc1522d9e96118162ff9489f"},
	}
	if len(contract.SourceAuthorities) != len(wantAuthorities) {
		t.Fatalf("source authorities = %#v", contract.SourceAuthorities)
	}
	for name, want := range wantAuthorities {
		got := contract.SourceAuthorities[name]
		if got.Path != want.path || got.GitBlob != want.blob {
			t.Errorf("source authority %s = %#v, want %s at %s", name, got, want.path, want.blob)
		}
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
	wantPairValues := []string{"prior_logical_input", "current_logical_input", "current_cache_read", "estimated_reusable_tokens", "shortfall", "reuse_share", "interpretation"}
	if secondary.Output.SummarySchemaVersion != 4 || secondary.Output.Path != "estimated_consecutive_step_reuse" || secondary.Output.Label != "estimated" || !reflect.DeepEqual(secondary.Output.PerPairValues, wantPairValues) || secondary.Output.Counts == "" || secondary.Output.ValidationReport != "same secondary view" || secondary.Output.IntegerRepresentation == "" || secondary.Output.ShareRepresentation == "" || secondary.Output.RankingEffect != "none" || secondary.Output.TTLCause != "unsupported" || secondary.Output.ExpiryCause != "unsupported" || !reflect.DeepEqual(secondary.Output.LegacySchemas, []int{1, 2, 3}) {
		t.Errorf("secondary output contract is incomplete: %#v", secondary.Output)
	}
	if secondary.IneligibilitySelection != "evaluate ineligibility_rules in array order and retain the first rule whose predicate is true; no matching rule means eligible" || len(secondary.PredicateInputs) != 23 {
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
	if !reflect.DeepEqual(IneligibilityReasons, wantNames) {
		t.Fatalf("implemented ineligibility reasons = %v, contract = %v", IneligibilityReasons, wantNames)
	}
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
	if rules[18].Reason != "prior_cache_activity_absent" || rules[18].Predicate != wantActivity {
		t.Errorf("prior activity predicate = %#v", rules[18])
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
	concurrentCoverage      string
	segmentCoverage         string
	segmentBoundary         string
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
		{"segment unavailable precedes concurrency", func(in *secondaryPairInput) {
			in.segmentCoverage = "unavailable"
			in.concurrentCoverage = "unavailable"
		}, "segment_coverage_unavailable", 2},
		{"segment contradiction precedes interruption", func(in *secondaryPairInput) {
			in.segmentCoverage = "contradictory"
			in.segmentBoundary = "interruption"
		}, "segment_coverage_contradictory", 1},
		{"interruption precedes concurrent coverage", func(in *secondaryPairInput) {
			in.segmentBoundary = "interruption"
			in.concurrentCoverage = "unavailable"
		}, "interruption_boundary_observed", 2},
		{"restart boundary", func(in *secondaryPairInput) { in.segmentBoundary = "restart" }, "restart_boundary_observed", 1},
		{"concurrent coverage unavailable", func(in *secondaryPairInput) { in.concurrentCoverage = "unavailable" }, "concurrent_coverage_unavailable", 1},
		{"concurrent coverage contradictory", func(in *secondaryPairInput) { in.concurrentCoverage = "contradictory" }, "concurrent_coverage_contradictory", 1},
		{"concurrent interleaving", func(in *secondaryPairInput) { in.concurrentInterleaving = true }, "concurrent_interleaving", 1},
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
		priorExists: true, orderCoverage: "covered", adjacent: true, concurrentCoverage: "covered", segmentCoverage: "covered", segmentBoundary: "none",
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
		"segment_coverage_unavailable", "segment_coverage_contradictory", "interruption_boundary_observed", "restart_boundary_observed",
		"concurrent_coverage_unavailable", "concurrent_coverage_contradictory", "concurrent_interleaving", "runtime_session_identity_unavailable", "runtime_session_identity_changed", "runtime_contract_mismatch",
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
	add("segment_coverage_unavailable", in.priorExists && in.segmentCoverage == "unavailable")
	add("segment_coverage_contradictory", in.priorExists && in.segmentCoverage == "contradictory")
	add("interruption_boundary_observed", in.priorExists && in.segmentCoverage == "covered" && in.segmentBoundary == "interruption")
	add("restart_boundary_observed", in.priorExists && in.segmentCoverage == "covered" && in.segmentBoundary == "restart")
	add("concurrent_coverage_unavailable", in.priorExists && in.concurrentCoverage == "unavailable")
	add("concurrent_coverage_contradictory", in.priorExists && in.concurrentCoverage == "contradictory")
	add("concurrent_interleaving", in.priorExists && in.concurrentCoverage == "covered" && in.concurrentInterleaving)
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

	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "cache-reuse-metric-contract.md"))
	if err != nil || len(doc) == 0 {
		t.Fatalf("contract documentation unavailable: %v", err)
	}
	for _, rule := range contract.Secondary.IneligibilityRules {
		if !strings.Contains(string(doc), "`"+rule.Reason+"`") {
			t.Errorf("contract documentation omits ineligibility reason %q", rule.Reason)
		}
	}
}
