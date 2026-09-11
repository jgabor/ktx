package experiment

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func directShareManifest() Manifest {
	manifest := cacheAwareManifest()
	manifest.Model = "openai/test"
	manifest.Identities = map[string]string{"opencode": "1.18.5"}
	manifest.CacheControl = &CacheControlManifest{
		Mode: "observed", Source: "unavailable", Model: "openai/test",
		Provider: "openai", Variant: "high", RuntimeVersion: "1.18.5",
	}
	return manifest
}

func directShareAttempt(id string, steps ...SourceEvent) Attempt {
	return Attempt{
		AttemptID: id, Side: Baseline, Role: MeasuredWarm,
		Artifacts: map[string]string{"stream": "artifacts/" + id + "/stream.jsonl"},
		CacheControl: &CacheControlMetadata{
			Mode: "observed", Source: "unavailable", Phase: MeasuredWarm,
			Model: "openai/test", Provider: "openai", Variant: "high", RuntimeVersion: "1.18.5",
		},
		Metrics: Metrics{ModelSteps: steps},
	}
}

func directStep(ordinal uint64, uncached, read, write IntegerMeasurement) SourceEvent {
	return SourceEvent{
		Ordinal: ordinal, Kind: ModelStep, UncachedInput: uncached, CacheRead: read, CacheWrite: write,
		Output: UnknownInteger(), Reasoning: UnknownInteger(), Cost: CostMeasurement{State: MeasurementUnknown},
		Identity: EventIdentity{Provider: stringPointer("openai"), Model: stringPointer("test")},
	}
}

func directAttempts(summary map[string]any) []any {
	return summary["attempts"].([]any)
}

func TestDirectCachedInputShareUsesRetainedStepSums(t *testing.T) {
	attempt := directShareAttempt(
		"measured",
		directStep(1, MeasuredInteger(10), MeasuredInteger(5), MeasuredInteger(2)),
		directStep(2, MeasuredInteger(20), MeasuredInteger(15), MeasuredInteger(3)),
	)
	// Attempt projections are not the authority for this derivation.
	attempt.TokensIn, attempt.CacheRead, attempt.CacheWrite = floatPointer(999), floatPointer(999), floatPointer(999)
	summary := buildDirectCachedInputShare(directShareManifest(), []Attempt{attempt})
	view := directAttempts(summary)[0].(map[string]any)
	if view["attempt_id"] != attempt.AttemptID || view["side"] != attempt.Side || view["phase"] != attempt.Role || view["cycle"] != attempt.Cycle {
		t.Fatalf("attempt coordinates = %#v", view)
	}
	for field, want := range map[string]int64{"uncached_input": 30, "cache_read": 20, "cache_write": 5, "logical_input": 55} {
		measurement := view[field].(map[string]any)
		value, ok := measurement["value"].(*big.Int)
		if measurement["state"] != "measured" || !ok || value.Cmp(big.NewInt(want)) != 0 {
			t.Errorf("%s = %#v, want measured %d", field, measurement, want)
		}
	}
	share := view["share"].(map[string]any)
	if share["state"] != "measured" || math.Abs(share["value"].(float64)-20.0/55.0) > 1e-15 {
		t.Fatalf("share = %#v", share)
	}
	if view["formula"] != "cache_read / logical_input" || view["unit"] != "token_fraction" {
		t.Fatalf("formula/unit = %v / %v", view["formula"], view["unit"])
	}
	source := view["source"].(map[string]any)
	if source["artifact"] != "artifacts/measured/stream.jsonl" || source["accepted_model_steps"] != 2 {
		t.Fatalf("source = %#v", source)
	}
	identity := view["identity_provenance"].(map[string]any)
	if identity["configured_identity_backfills_source_events"] != false {
		t.Fatalf("identity provenance = %#v", identity)
	}
	runtime := identity["runtime"].(map[string]any)
	if runtime["required_runtime_version"] != "1.18.5" {
		t.Fatalf("runtime provenance = %#v", runtime)
	}
	if _, misleading := runtime["required_contract_version"]; misleading {
		t.Fatalf("runtime provenance retained misleading contract-version field: %#v", runtime)
	}
	events := identity["source_events"].([]any)
	if len(events) != 2 || events[0].(map[string]any)["provider"] != "openai" {
		t.Fatalf("source identities = %#v", events)
	}
}

func TestDirectCachedInputSharePreservesZeroAndNotApplicable(t *testing.T) {
	positive := directShareAttempt("zero-share", directStep(1, MeasuredInteger(9), MeasuredInteger(0), MeasuredInteger(1)))
	zero := directShareAttempt("zero-input", directStep(1, MeasuredInteger(0), MeasuredInteger(0), MeasuredInteger(0)))
	views := directAttempts(buildDirectCachedInputShare(directShareManifest(), []Attempt{positive, zero}))
	share := views[0].(map[string]any)["share"].(map[string]any)
	if share["state"] != "measured" || share["value"] != float64(0) {
		t.Fatalf("measured zero share = %#v", share)
	}
	share = views[1].(map[string]any)["share"].(map[string]any)
	if share["state"] != "not_applicable" || share["value"] != nil || share["reason"] != "logical_input_not_positive" {
		t.Fatalf("zero logical input share = %#v", share)
	}
}

func TestDirectCachedInputShareKeepsLogicalInputBeyondInt64Exact(t *testing.T) {
	const steps = 342
	const largestExactFloatInteger = int64(1<<53 - 1)
	events := make([]SourceEvent, steps)
	for i := range events {
		events[i] = directStep(
			uint64(i+1),
			MeasuredInteger(largestExactFloatInteger),
			MeasuredInteger(largestExactFloatInteger),
			MeasuredInteger(largestExactFloatInteger),
		)
	}
	summary := buildDirectCachedInputShare(directShareManifest(), []Attempt{directShareAttempt("beyond-int64", events...)})
	view := directAttempts(summary)[0].(map[string]any)
	expectedBucket := new(big.Int).Mul(big.NewInt(steps), big.NewInt(largestExactFloatInteger))
	expectedLogical := new(big.Int).Mul(big.NewInt(3), expectedBucket)
	if expectedLogical.IsInt64() {
		t.Fatalf("test logical input unexpectedly fits int64: %s", expectedLogical)
	}
	for _, field := range []string{"uncached_input", "cache_read", "cache_write"} {
		measurement := view[field].(map[string]any)
		value, ok := measurement["value"].(*big.Int)
		if measurement["state"] != "measured" || !ok || value.Cmp(expectedBucket) != 0 {
			t.Fatalf("%s = %#v, want measured %s", field, measurement, expectedBucket)
		}
	}
	logical := view["logical_input"].(map[string]any)
	logicalValue, ok := logical["value"].(*big.Int)
	if logical["state"] != "measured" || !ok || logicalValue.Cmp(expectedLogical) != 0 {
		t.Fatalf("logical input = %#v, want measured %s", logical, expectedLogical)
	}
	share := view["share"].(map[string]any)
	if share["state"] != "measured" || share["ratio"] != "1/3" || math.Abs(share["value"].(float64)-1.0/3.0) > 1e-15 {
		t.Fatalf("share = %#v, want measured 1/3", share)
	}

	encoded, err := MarshalJSON(summary)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var serialized map[string]any
	if err := decoder.Decode(&serialized); err != nil {
		t.Fatal(err)
	}
	rows := serialized["attempts"].([]any)
	got := rows[0].(map[string]any)["logical_input"].(map[string]any)["value"].(json.Number)
	if got.String() != expectedLogical.String() {
		t.Fatalf("serialized logical input = %s, want %s", got, expectedLogical)
	}
}

func TestDirectCachedInputShareReportsExactMissingAndMalformedGaps(t *testing.T) {
	attempt := directShareAttempt(
		"gaps",
		directStep(1, MeasuredInteger(10), MeasuredInteger(2), MeasuredInteger(0)),
		directStep(2, UnknownInteger(), MalformedInteger(), MeasuredInteger(0)),
	)
	view := directAttempts(buildDirectCachedInputShare(directShareManifest(), []Attempt{attempt}))[0].(map[string]any)
	share := view["share"].(map[string]any)
	want := []any{
		map[string]any{"field": "uncached_input", "source_event_ordinal": uint64(2), "state": "missing"},
		map[string]any{"field": "cache_read", "source_event_ordinal": uint64(2), "state": "malformed"},
	}
	if share["state"] != "malformed" || share["value"] != nil || !reflect.DeepEqual(share["evidence_gaps"], want) {
		t.Fatalf("share gaps = %#v, want %#v", share, want)
	}
	if got := view["cache_write"].(map[string]any); got["state"] != "measured" || got["value"].(*big.Int).Sign() != 0 {
		t.Fatalf("measured write zero = %#v", got)
	}
}

func TestDirectCachedInputShareIsVersionGatedAndRendered(t *testing.T) {
	manifest := directShareManifest()
	attempt := directShareAttempt("rendered", directStep(1, MeasuredInteger(10), MeasuredInteger(5), MeasuredInteger(0)))
	summary := BuildSummary(manifest, []Attempt{attempt})
	text := RenderSummary(summary)
	for _, fragment := range []string{
		"prompt_cache_token_hit_rate attempt=rendered", "cache_read=5", "logical_input=15",
		"formula=cache_read / (uncached_input + cache_read + cache_write)", "kind=token_weighted", "source=artifacts/rendered/stream.jsonl",
		"runtime=opencode/1.18.5", "configured_identity=openai/test variant=high", "source_event_identities=",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("rendered summary missing %q:\n%s", fragment, text)
		}
	}

	manifest.CacheControl.RuntimeVersion = "1.18.4"
	manifest.Identities["opencode"] = "1.18.4"
	unsupported := BuildSummary(manifest, []Attempt{attempt})
	if _, ok := unsupported[promptCacheTokenHitRateField]; ok {
		t.Fatalf("unsupported runtime gained direct view: %#v", unsupported)
	}
}

func TestDirectCachedInputShareRegeneratesAndValidates(t *testing.T) {
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
	summaryPath := filepath.Join(dir, "summary.json")
	before, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RegenerateSummary(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("summary regeneration changed canonical bytes")
	}
	report, err := ValidateExperiment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report["valid"] != true || report["summary_byte_match"] != true {
		t.Fatalf("validation report = %#v", report)
	}
	direct := report[promptCacheTokenHitRateField].(map[string]any)
	if len(direct["attempts"].([]any)) != 6 {
		t.Fatalf("validated direct attempts = %#v", direct)
	}
	comparison := report[promptCacheTokenHitRateComparisonField].(map[string]any)
	if comparison["purpose"] != "controlled_descriptive_non_ranking" {
		t.Fatalf("validated comparison = %#v", comparison)
	}
	authorization := report[promptCacheTokenHitRateStatementAuthorizationField].(map[string]any)
	outcomes := authorization["outcomes"].(map[string]any)
	if authorization["contract_version"] != promptCacheTokenHitRateContractVersion || outcomes["direct_available"].(map[string]any)["authorized"] != true {
		t.Fatalf("validated statement authorization = %#v", authorization)
	}
}
