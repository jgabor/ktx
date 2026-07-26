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

	"github.com/jgabor/ktx/internal/db"
)

func directShareManifest() Manifest {
	manifest := cacheAwareManifest()
	manifest.Model = "openai/test"
	manifest.Identities = map[string]string{"opencode": "1.18.4"}
	manifest.CacheControl = &CacheControlManifest{
		Mode: "observed", Source: "unavailable", Model: "openai/test",
		Provider: "openai", Variant: "high", RuntimeVersion: "1.18.4",
	}
	return manifest
}

func directShareAttempt(id string, steps ...SourceEvent) Attempt {
	return Attempt{
		AttemptID: id, Side: Baseline, Role: MeasuredWarm,
		Artifacts: map[string]string{"stream": "artifacts/" + id + "/stream.jsonl"},
		CacheControl: &CacheControlMetadata{
			Mode: "observed", Source: "unavailable", Phase: MeasuredWarm,
			Model: "openai/test", Provider: "openai", Variant: "high", RuntimeVersion: "1.18.4",
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
	return summary["direct_cached_input_share"].(map[string]any)["attempts"].([]any)
}

func TestDirectCachedInputShareUsesRetainedStepSums(t *testing.T) {
	attempt := directShareAttempt(
		"measured",
		directStep(1, MeasuredInteger(10), MeasuredInteger(5), MeasuredInteger(2)),
		directStep(2, MeasuredInteger(20), MeasuredInteger(15), MeasuredInteger(3)),
	)
	// Attempt projections are not the authority for this derivation.
	attempt.TokensIn, attempt.CacheRead, attempt.CacheWrite = floatPointer(999), floatPointer(999), floatPointer(999)
	summary := BuildSummary(directShareManifest(), []Attempt{attempt})
	if summary["schema_version"] != 4 {
		t.Fatalf("schema_version = %v, want 4", summary["schema_version"])
	}
	view := directAttempts(summary)[0].(map[string]any)
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
	if runtime["required_runtime_version"] != "1.18.4" {
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
	views := directAttempts(BuildSummary(directShareManifest(), []Attempt{positive, zero}))
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
	summary := BuildSummary(directShareManifest(), []Attempt{directShareAttempt("beyond-int64", events...)})
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
	rows := serialized["direct_cached_input_share"].(map[string]any)["attempts"].([]any)
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
	view := directAttempts(BuildSummary(directShareManifest(), []Attempt{attempt}))[0].(map[string]any)
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
		"direct_cached_input_share attempt=rendered", "cache_read=5", "logical_input=15",
		"formula=cache_read / logical_input", "unit=token_fraction", "source=artifacts/rendered/stream.jsonl",
		"runtime=opencode/1.18.4", "configured_identity=openai/test variant=high", "source_event_identities=",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("rendered summary missing %q:\n%s", fragment, text)
		}
	}

	manifest.CacheControl.RuntimeVersion = "1.18.3"
	manifest.Identities["opencode"] = "1.18.3"
	legacy := BuildSummary(manifest, []Attempt{attempt})
	if legacy["schema_version"] != 3 {
		t.Fatalf("unsupported runtime schema = %v, want historical schema 3", legacy["schema_version"])
	}
	if _, ok := legacy["direct_cached_input_share"]; ok {
		t.Fatalf("unsupported runtime gained direct view: %#v", legacy)
	}
}

func TestDirectCachedInputShareRegeneratesAndValidates(t *testing.T) {
	dir := writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
		manifest.CacheControl.RuntimeVersion = "1.18.4"
		manifest.Identities["opencode"] = "1.18.4"
		for i := range *attempts {
			attempt := &(*attempts)[i]
			attempt.CacheControl.RuntimeVersion = "1.18.4"
			attempt.ModelSteps = []SourceEvent{directStep(1, MeasuredInteger(10), MeasuredInteger(5), MeasuredInteger(0))}
		}
	}, true)
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
	direct := report["direct_cached_input_share"].(map[string]any)
	if len(direct["attempts"].([]any)) != 6 {
		t.Fatalf("validated direct attempts = %#v", direct)
	}
	estimate := report["estimated_consecutive_step_reuse"].(map[string]any)
	if estimate["label"] != "estimated" || estimate["ranking_effect"] != "none" {
		t.Fatalf("validated secondary estimate = %#v", estimate)
	}
}

func TestDirectCachedInputSharePreservesHistoricalSchemaThreeBytes(t *testing.T) {
	dir := t.TempDir()
	manifest := directShareManifest()
	manifest.ExperimentID = "historical-schema-three"
	rate := .5
	pricing, err := writePricingSnapshot(dir, "openai", "test", db.Price{Input: 1, Output: 2, CacheRead: &rate, CacheWrite: &rate, InputSet: true, OutputSet: true})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Pricing = pricing
	attempt := directShareAttempt("historical", directStep(1, MeasuredInteger(10), MeasuredInteger(5), MeasuredInteger(0)))
	attempt.ExperimentID = manifest.ExperimentID
	manifest.historicalSummarySchema = 3
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := AppendAttempt(dir, attempt); err != nil {
		t.Fatal(err)
	}
	before, err := MarshalJSON(BuildSummary(manifest, []Attempt{attempt}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), before, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RegenerateSummary(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("historical schema 3 bytes changed")
	}
	var summary map[string]any
	if err := json.Unmarshal(after, &summary); err != nil {
		t.Fatal(err)
	}
	if summary["schema_version"] != float64(3) {
		t.Fatalf("historical schema = %v", summary["schema_version"])
	}
	if _, ok := summary["direct_cached_input_share"]; ok {
		t.Fatalf("historical schema gained direct output: %#v", summary)
	}
}
