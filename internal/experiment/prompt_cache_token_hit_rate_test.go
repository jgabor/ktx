package experiment

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func promptCacheTokenHitRateManifest() Manifest {
	manifest := comparisonManifest()
	manifest.Methodology.Version = promptCacheTokenHitRateMethodologyVersion
	return manifest
}

func TestPromptCacheTokenHitRateUsesExplicitFutureSchema(t *testing.T) {
	attempts := comparisonPairs(5, 20, 30)
	legacy := BuildSummary(comparisonManifest(), attempts)
	if legacy["schema_version"] != 4 {
		t.Fatalf("legacy schema_version = %v, want 4", legacy["schema_version"])
	}
	if _, ok := legacy[promptCacheTokenHitRateField]; ok {
		t.Fatalf("legacy summary gained prompt-cache token hit rate: %#v", legacy)
	}

	summary := BuildSummary(promptCacheTokenHitRateManifest(), attempts)
	if summary["schema_version"] != 5 {
		t.Fatalf("schema_version = %v, want 5", summary["schema_version"])
	}
	if _, ok := summary["direct_cached_input_share"]; ok {
		t.Fatalf("future summary retained v1 metric name: %#v", summary)
	}
	if _, ok := summary["cached_input_share_comparison"]; ok {
		t.Fatalf("future summary retained v1 comparison name: %#v", summary)
	}
	encoded, err := MarshalJSON(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacyField := range [][]byte{[]byte(`"direct_cached_input_share"`), []byte(`"cached_input_share_comparison"`), []byte(`"direct_share"`)} {
		if bytes.Contains(encoded, legacyField) {
			t.Fatalf("future summary retained v1 field %s", legacyField)
		}
	}

	rate := summary[promptCacheTokenHitRateField].(map[string]any)
	if rate["contract_version"] != promptCacheTokenHitRateContractVersion || rate["kind"] != "token_weighted" || rate["formula"] != "sum(cache_read) / sum(uncached_input + cache_read + cache_write)" {
		t.Fatalf("rate contract = %#v", rate)
	}
	row := rate["attempts"].([]any)[0].(map[string]any)
	if _, exists := row["share"]; exists {
		t.Fatalf("future attempt retained share field: %#v", row)
	}
	value := row[promptCacheTokenHitRateField].(map[string]any)
	if value["state"] != "measured" || value["ratio"] != "1/5" || row["kind"] != "token_weighted" {
		t.Fatalf("attempt rate = %#v", row)
	}

	comparison := summary[promptCacheTokenHitRateComparisonField].(map[string]any)
	if comparison["contract_version"] != promptCacheTokenHitRateContractVersion || comparison["metric"] != promptCacheTokenHitRateField || comparison["comparison_unit"] != "measured_cycle_pair" || comparison["complete_pair_count"] != 5 {
		t.Fatalf("comparison identity = %#v", comparison)
	}
	comparedRate := comparison[promptCacheTokenHitRateField].(map[string]any)
	means := comparedRate["means"].(map[string]any)
	if means[Baseline] != .2 || means[Candidate] != .3 {
		t.Fatalf("rate means = %#v", means)
	}
	difference := comparedRate["candidate_minus_baseline"].(map[string]any)
	points := difference["percentage_point_delta"].(map[string]any)
	if points["ratio"] != "10" || difference["interval"] == nil || difference["percentage_point_interval"] == nil {
		t.Fatalf("rate difference = %#v", difference)
	}
	if comparison["quality"].(map[string]any)["preserved"] != true || len(comparison["blocking_reasons"].([]string)) != 0 {
		t.Fatalf("comparison gates = %#v", comparison)
	}

	authorization := summary[promptCacheTokenHitRateStatementAuthorizationField].(map[string]any)
	if authorization["contract_version"] != promptCacheTokenHitRateContractVersion || authorization["provider_internal_claims"] != "forbidden" {
		t.Fatalf("authorization = %#v", authorization)
	}
	outcome := authorization["outcomes"].(map[string]any)["candidate_higher_success"].(map[string]any)
	statement, statementOK := outcome["statement"].(string)
	if outcome["authorized"] != true || !statementOK || !strings.Contains(statement, "higher token-weighted prompt-cache hit rate under the pinned contract (cache-reuse-v2)") || strings.Contains(statement, "provider") {
		t.Fatalf("higher statement = %#v", outcome)
	}
	unsupported := authorization["permanently_unsupported_claims"].([]string)
	if !containsString(unsupported, "provider_cache_hit") || !containsString(unsupported, "provider_cache_miss") {
		t.Fatalf("provider claim boundary = %#v", unsupported)
	}
	if rendered := RenderSummary(summary); !strings.Contains(rendered, statement) {
		t.Fatalf("rendered summary omitted authorized prompt-cache statement: %s", rendered)
	}

	lowerSummary := BuildSummary(promptCacheTokenHitRateManifest(), comparisonPairs(5, 30, 20))
	lowerOutcome := lowerSummary[promptCacheTokenHitRateStatementAuthorizationField].(map[string]any)["outcomes"].(map[string]any)["candidate_lower"].(map[string]any)
	lowerStatement, lowerStatementOK := lowerOutcome["statement"].(string)
	if lowerOutcome["authorized"] != true || !lowerStatementOK || !strings.Contains(lowerStatement, "lower token-weighted prompt-cache hit rate under the pinned contract (cache-reuse-v2)") || strings.Contains(lowerStatement, "provider") {
		t.Fatalf("lower statement = %#v", lowerOutcome)
	}
	if rendered := RenderSummary(lowerSummary); !strings.Contains(rendered, lowerStatement) {
		t.Fatalf("rendered summary omitted lower prompt-cache statement: %s", rendered)
	}
}

func TestPromptCacheTokenHitRateKeepsSchemaFourBoundary(t *testing.T) {
	manifest := comparisonManifest()
	attempts := comparisonPairs(5, 20, 30)
	before, err := MarshalJSON(BuildSummary(manifest, attempts))
	if err != nil {
		t.Fatal(err)
	}
	preserveHistoricalSummarySchema(&manifest, before)
	after, err := MarshalJSON(BuildSummary(manifest, attempts))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("schema 4 summary bytes changed")
	}
	beforeHash, afterHash := sha256.Sum256(before), sha256.Sum256(after)
	if beforeHash != afterHash {
		t.Fatal("schema 4 summary hash changed")
	}
	t.Logf("schema 4 summary bytes=%d sha256=%x", len(after), afterHash)

	future := promptCacheTokenHitRateManifest()
	preserveHistoricalSummarySchema(&future, before)
	if future.historicalSummarySchema != 4 || supportsPromptCacheTokenHitRate(future) {
		t.Fatalf("schema 4 was eligible for schema 5 rewrite: %#v", future)
	}
}

func TestPromptCacheTokenHitRateContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "tools", "bench", "contracts", "cache-reuse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract map[string]any
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if contract["contract_version"] != promptCacheTokenHitRateContractVersion {
		t.Fatalf("contract version = %#v", contract)
	}
	activation := contract["activation"].(map[string]any)
	if activation["methodology_version"] != promptCacheTokenHitRateMethodologyVersion || activation["summary_schema_version"] != float64(5) {
		t.Fatalf("contract activation = %#v", activation)
	}
	metric := contract[promptCacheTokenHitRateField].(map[string]any)
	if metric["kind"] != "token_weighted" || metric["formula"] != "sum(cache_read) / sum(uncached_input + cache_read + cache_write)" {
		t.Fatalf("contract metric = %#v", metric)
	}
	guard := contract[pipelineCostGuardField].(map[string]any)
	if guard["path"] != pipelineCostGuardField || guard["accepted_path"] != "accepted" || guard["rounding_decimals"] != float64(6) {
		t.Fatalf("contract guard = %#v", guard)
	}
	reasons := guard["reason_precedence"].([]any)
	if len(reasons) != len(pipelineCostGuardReasonPrecedence) {
		t.Fatalf("contract guard precedence = %#v", reasons)
	}
	for index, want := range pipelineCostGuardReasonPrecedence {
		if reasons[index] != want {
			t.Fatalf("contract guard precedence = %#v, want %v", reasons, pipelineCostGuardReasonPrecedence)
		}
	}
}

func TestPromptCacheTokenHitRatePreservesV5ValidationWording(t *testing.T) {
	manifest := comparisonManifest()
	manifest.TargetValidPerPipeline = 0
	reasons := validateCacheAwareEvidence(manifest, nil, nil, []byte(`{"schema_version":0}`), map[string]int{Baseline: 0, Candidate: 0})
	for _, expected := range []string{
		"v5 summary schema_version must be 4",
		"v5 pricing snapshot is required",
		"v5 target valid per pipeline must be positive",
	} {
		if !containsString(reasons, expected) {
			t.Fatalf("validation reasons %v do not contain historical wording %q", reasons, expected)
		}
	}
	for _, reason := range reasons {
		if strings.Contains(reason, "v5-cache-aware") {
			t.Fatalf("historical validation wording changed: %q", reason)
		}
	}
}
