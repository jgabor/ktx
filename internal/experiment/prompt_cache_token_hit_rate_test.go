package experiment

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func promptCacheTokenHitRateManifest() Manifest {
	manifest := comparisonManifest()
	manifest.Methodology.Version = promptCacheHitRateMethodologyV6
	manifest.Methodology.CacheMetricContract = promptCacheMetricContractV2
	return manifest
}

func promptCacheTokenHitRateSummary(t *testing.T, attempts []Attempt) map[string]any {
	t.Helper()
	summary := BuildSummary(promptCacheTokenHitRateManifest(), attempts)
	if summary["schema_version"] != 5 {
		t.Fatalf("schema_version = %#v, want 5", summary["schema_version"])
	}
	return summary
}

func promptCacheTokenHitRateOutcomeForSummary(t *testing.T, summary map[string]any, name string) map[string]any {
	t.Helper()
	authorization := anyMap(summary["prompt_cache_token_hit_rate_statement_authorization"])
	outcome := anyMap(anyMap(authorization["outcomes"])[name])
	if len(outcome) == 0 {
		t.Fatalf("%s outcome missing: %#v", name, authorization)
	}
	return outcome
}

func TestPromptCacheTokenHitRateActivationAndPairedEvidence(t *testing.T) {
	legacy := BuildSummary(comparisonManifest(), comparisonPairs(5, 20, 30))
	if legacy["schema_version"] != 4 {
		t.Fatalf("legacy schema_version = %#v, want 4", legacy["schema_version"])
	}
	for _, field := range []string{"prompt_cache_token_hit_rate", "prompt_cache_token_hit_rate_comparison", "prompt_cache_token_hit_rate_statement_authorization"} {
		if _, exists := legacy[field]; exists {
			t.Fatalf("legacy summary unexpectedly has %q", field)
		}
	}

	summary := promptCacheTokenHitRateSummary(t, comparisonPairs(5, 20, 30))
	for _, field := range []string{"direct_cached_input_share", "estimated_consecutive_step_reuse", "cached_input_share_comparison", "statement_authorization"} {
		if _, exists := summary[field]; exists {
			t.Fatalf("successor summary retained legacy v1 field %q", field)
		}
	}
	metric := anyMap(summary["prompt_cache_token_hit_rate"])
	if metric["contract_version"] != promptCacheMetricContractV2 || metric["kind"] != "token_weighted" ||
		metric["formula"] != "cache_read / (uncached_input + cache_read + cache_write)" || metric["source"] != promptCacheTokenHitRateSource {
		t.Fatalf("metric contract = %#v", metric)
	}
	if metric["attempt_count"] != 10 || len(metric["attempts"].([]any)) != 10 {
		t.Fatalf("metric attempt evidence = %#v", metric)
	}
	first := metric["attempts"].([]any)[0].(map[string]any)
	if first["token_hit_rate"] == nil || first["raw_buckets"] == nil || first["logical_input"] == nil || first["identity_provenance"] == nil {
		t.Fatalf("metric row is not auditable: %#v", first)
	}

	comparison := anyMap(summary["prompt_cache_token_hit_rate_comparison"])
	if comparison["complete_pair_count"] != 5 || len(comparison["pairs"].([]any)) != 5 {
		t.Fatalf("paired evidence = %#v", comparison)
	}
	for field, want := range map[string]float64{"baseline_mean": .2, "candidate_mean": .3, "percentage_point_delta": 10} {
		view := anyMap(comparison[field])
		if value, ok := finiteNumber(view["value"]); !ok || math.Abs(value-want) > 1e-12 {
			t.Errorf("%s = %#v, want %v", field, view, want)
		}
	}
	interval := anyMap(comparison["interval"])
	if interval["unit"] != "percentage_points" || interval["coverage"] != .95 || interval["lower"] != 10.0 || interval["upper"] != 10.0 {
		t.Errorf("interval = %#v", interval)
	}
	if quality := anyMap(comparison["quality"]); quality["preserved"] != true {
		t.Errorf("quality = %#v", quality)
	}
	if blockers := comparison["blocking_reasons"].([]string); len(blockers) != 0 || !reflect.DeepEqual(blockers, comparison["unavailable_reasons"]) {
		t.Errorf("blockers = %#v", comparison)
	}

	higher := promptCacheTokenHitRateOutcomeForSummary(t, summary, "higher")
	lower := promptCacheTokenHitRateOutcomeForSummary(t, summary, "lower")
	if higher["authorized"] != true || lower["authorized"] != false || lower["statement"] != nil {
		t.Fatalf("directional authorization = higher=%#v lower=%#v", higher, lower)
	}
	statement, _ := higher["statement"].(string)
	if !strings.Contains(statement, "higher token-weighted prompt-cache hit rate") {
		t.Fatalf("statement = %q", statement)
	}
	for _, forbidden := range []string{"provider", "request", "ttl", "eviction", "eligibility", "isolation", "prefix"} {
		if strings.Contains(strings.ToLower(statement), forbidden) {
			t.Errorf("statement has forbidden %q: %q", forbidden, statement)
		}
	}
	text := RenderSummary(summary)
	if !strings.Contains(text, statement) {
		t.Errorf("rendered summary omits authorized statement:\n%s", text)
	}
	if !strings.Contains(text, "derived logical_input=uncached_input+cache_read+cache_write") || strings.Contains(text, "derived logical_input=uncached_input+cache_read (runtime") {
		t.Errorf("rendered summary has an unpinned logical-input formula:\n%s", text)
	}
}

func TestPromptCacheTokenHitRateAuthorizationFailsClosed(t *testing.T) {
	tests := []struct {
		name        string
		attempts    func() []Attempt
		wantBlocker string
	}{
		{name: "tie", attempts: func() []Attempt { return comparisonPairs(5, 20, 20) }, wantBlocker: "two_sided_95_percent_interval_contains_zero"},
		{name: "incomplete pair", attempts: func() []Attempt {
			attempts := comparisonPairs(5, 20, 30)
			return attempts[:len(attempts)-1]
		}, wantBlocker: "cycle_pair_incomplete"},
		{name: "missing runtime identity", attempts: func() []Attempt {
			attempts := comparisonPairs(5, 20, 30)
			attempts[1].ModelSteps[0].Identity.Runtime = nil
			return attempts
		}, wantBlocker: "runtime_emitted_identity_missing"},
		{name: "protocol mismatch", attempts: func() []Attempt {
			attempts := comparisonPairs(5, 20, 30)
			attempts[0].CacheControl.ExecutionOrder = 0
			return attempts
		}, wantBlocker: "protocol_noncompliant"},
		{name: "quality mismatch", attempts: func() []Attempt {
			attempts := comparisonPairs(5, 30, 20)
			for index := 1; index < len(attempts); index += 2 {
				attempts[index].Score.Score = .5
				attempts[index].FullPass = false
			}
			return attempts
		}, wantBlocker: "quality_not_preserved"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := promptCacheTokenHitRateSummary(t, test.attempts())
			comparison := anyMap(summary["prompt_cache_token_hit_rate_comparison"])
			if !containsString(comparison["blocking_reasons"].([]string), test.wantBlocker) {
				t.Fatalf("comparison blocker %q missing: %#v", test.wantBlocker, comparison)
			}
			for _, outcomeName := range []string{"higher", "lower"} {
				outcome := promptCacheTokenHitRateOutcomeForSummary(t, summary, outcomeName)
				if outcome["authorized"] != false || outcome["statement"] != nil {
					t.Fatalf("%s directional claim was not blocked: %#v", outcomeName, outcome)
				}
			}
		})
	}
}

func TestPromptCacheTokenHitRateAuthorizationRejectsProjectionDrift(t *testing.T) {
	manifest := promptCacheTokenHitRateManifest()
	attempts := comparisonPairs(5, 20, 30)
	direct := buildDirectCachedInputShare(manifest, attempts)
	secondary := buildSecondaryCacheEstimate(attempts)
	rawComparison := buildCachedInputShareComparison(manifest, attempts)
	for _, mutate := range []func(map[string]any){
		func(comparison map[string]any) { comparison["percentage_point_delta"] = nil },
		func(comparison map[string]any) { anyMap(comparison["interval"])["lower"] = math.NaN() },
	} {
		metric := promptCacheTokenHitRateMetricFromDirect(direct)
		comparison := promptCacheTokenHitRateComparisonFromRaw(rawComparison)
		mutate(comparison)
		authorization := buildPromptCacheTokenHitRateAuthorization(direct, secondary, rawComparison, metric, comparison)
		for _, outcomeName := range []string{"higher", "lower"} {
			outcome := anyMap(anyMap(authorization["outcomes"])[outcomeName])
			if outcome["authorized"] != false || outcome["statement"] != nil || !containsString(outcome["reasons"].([]string), "evidence_projection_inconsistent") {
				t.Fatalf("%s accepted a drifted projection: %#v", outcomeName, outcome)
			}
		}
	}
}

func TestPromptCacheTokenHitRateRankingRendersOnlyAuthorizedDirection(t *testing.T) {
	options := RankingOptions{TaskFile: "tools/bench/tasks/crossfile-nav-v1.toml", Model: "openai/test", Rounds: 5, BaselinePipeline: "plain", Candidates: []string{"compact"}}
	now := time.Unix(0, 0).UTC()
	validSummary := promptCacheTokenHitRateSummary(t, comparisonPairs(5, 20, 30))
	ranking := BuildRanking(options, []PairResult{{CandidatePipeline: "compact", ExperimentID: "prompt-cache", Summary: validSummary}}, now)
	rows := ranking["ranking"].([]map[string]any)
	var candidate map[string]any
	for _, row := range rows {
		if row["side"] == Candidate {
			candidate = row
			break
		}
	}
	if candidate == nil || candidate["prompt_cache_token_hit_rate_comparison"] == nil {
		t.Fatalf("ranking omits successor comparison: %#v", ranking)
	}
	text := RenderRanking(ranking)
	if !strings.Contains(text, "outcome=higher") || strings.Contains(text, "outcome=lower") {
		t.Fatalf("ranking text has an unauthorized direction:\n%s", text)
	}

	tieSummary := promptCacheTokenHitRateSummary(t, comparisonPairs(5, 20, 20))
	tieRanking := BuildRanking(options, []PairResult{{CandidatePipeline: "compact", ExperimentID: "prompt-cache-tie", Summary: tieSummary}}, now)
	if text := RenderRanking(tieRanking); strings.Contains(text, "prompt_cache_token_hit_rate pipeline=") {
		t.Fatalf("ranking rendered a blocked statement:\n%s", text)
	}
}

func TestPromptCacheTokenHitRateActivationValidationAndRegeneration(t *testing.T) {
	metadata, err := methodologyMetadata(filepath.Join("..", "..", "tools", "bench", "methodology-v6-prompt-cache-hit-rate.yaml"))
	if err != nil || metadata.Version != promptCacheHitRateMethodologyV6 || metadata.CacheMetricContract != promptCacheMetricContractV2 {
		t.Fatalf("successor methodology metadata = %#v err=%v", metadata, err)
	}
	for _, methodology := range []string{
		"methodology_version: v6-prompt-cache-hit-rate\n",
		"methodology_version: v5-cache-aware\ncache_metric_contract: cache-reuse-v2\n",
		"methodology_version: v6-prompt-cache-hit-rate\ncache_metric_contract: \ncache_metric_contract: cache-reuse-v2\n",
	} {
		path := filepath.Join(t.TempDir(), "methodology.yaml")
		if err := os.WriteFile(path, []byte(methodology), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := methodologyMetadata(path); err == nil {
			t.Fatalf("invalid activation methodology was accepted: %q", methodology)
		}
	}
	manifest := promptCacheTokenHitRateManifest()
	if err := validateCacheMetricContractActivation(manifest); err != nil {
		t.Fatal(err)
	}
	missing := manifest
	missing.Methodology.CacheMetricContract = ""
	if err := validateCacheMetricContractActivation(missing); err == nil {
		t.Fatal("missing activation marker was accepted")
	}
	mismatch := manifest
	mismatch.Methodology.Version = cacheAwareMethodologyV5
	if err := validateCacheMetricContractActivation(mismatch); err == nil {
		t.Fatal("mismatched activation marker was accepted")
	}

	dir := writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
		manifest.Methodology.Version = promptCacheHitRateMethodologyV6
		manifest.Methodology.CacheMetricContract = promptCacheMetricContractV2
		manifest.CacheControl.RuntimeVersion = directCacheRuntimeVersion
		manifest.Identities[directCacheRuntime] = directCacheRuntimeVersion
		for index := range *attempts {
			(*attempts)[index].CacheControl.RuntimeVersion = directCacheRuntimeVersion
		}
	}, true)
	summaryPath := filepath.Join(dir, "summary.json")
	before, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)
	if _, err := RegenerateSummary(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if afterHash := sha256.Sum256(after); beforeHash != afterHash || !reflect.DeepEqual(before, after) {
		t.Fatalf("successor summary bytes or hash changed: before=%x after=%x", beforeHash, afterHash)
	}
	var summary map[string]any
	if err := json.Unmarshal(after, &summary); err != nil {
		t.Fatal(err)
	}
	if summary["schema_version"] != float64(5) || summary["prompt_cache_token_hit_rate"] == nil {
		t.Fatalf("successor summary = %#v", summary)
	}
	report, err := ValidateExperiment(dir)
	if err != nil || report["valid"] != true || report["summary_byte_match"] != true || report["prompt_cache_token_hit_rate_comparison"] == nil {
		t.Fatalf("successor validation = report=%#v err=%v", report, err)
	}
}

func TestPromptCacheTokenHitRateContractDocument(t *testing.T) {
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, "tools", "bench", "cache-reuse-contract-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract map[string]any
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	activation := anyMap(contract["activation"])
	metric := anyMap(contract["metric"])
	comparison := anyMap(contract["comparison"])
	if contract["contract_version"] != promptCacheMetricContractV2 ||
		activation["manifest_methodology_version"] != promptCacheHitRateMethodologyV6 ||
		activation["manifest_methodology_cache_metric_contract"] != promptCacheMetricContractV2 ||
		metric["formula"] != "cache_read / (uncached_input + cache_read + cache_write)" ||
		comparison["identity_pooling"] != "forbidden" {
		t.Fatalf("contract = %#v", contract)
	}
	documentation, err := os.ReadFile(filepath.Join(root, "docs", "cache-reuse-metric-contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"cache-reuse-v2", "v6-prompt-cache-hit-rate", "prompt_cache_token_hit_rate_comparison", "percentage_point_delta"} {
		if !strings.Contains(string(documentation), fragment) {
			t.Errorf("documentation omits %q", fragment)
		}
	}
}
