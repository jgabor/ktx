package experiment

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestCompressionAttributionKeepsEvidenceClassesParallel(t *testing.T) {
	price := PricingSnapshotMetadata{Path: "pricing.json"}
	manifest := Manifest{Methodology: MethodologyMetadata{Version: promptCacheTokenHitRateMethodologyVersion}, Pricing: &price}
	baseline := attributionAttempt("baseline-003", Baseline, 21386, 11264, 0, 0, 0.026682)
	baseline.delivery = &deliveryEvidence{DatabasePath: "attempts/baseline-003/ktx.db", OriginalBytes: 50118, DeliveredBytes: 50118, Sources: []any{map[string]any{"source_row_id": "sgveusltqzacpqyj"}}}
	candidate := attributionAttempt("candidate-003", Candidate, 1898, 11264, 0, 0, 0.005316)
	candidate.delivery = &deliveryEvidence{DatabasePath: "attempts/candidate-003/ktx.db", OriginalBytes: 50118, DeliveredBytes: 1819, Sources: []any{map[string]any{"source_row_id": "rqbgfbpeivqecrti", "compressed_artifact_id": "ojldcmcdmwfwmefq"}}}

	summary := BuildSummary(manifest, []Attempt{baseline, candidate})
	attribution := summary["compression_attribution"].(map[string]any)
	if attribution["relationship"] != "parallel_non_additive" || attribution["non_additivity"].(map[string]any)["reason"] == "" {
		t.Fatalf("attribution contract = %#v", attribution)
	}
	if _, exists := attribution["cross_field_total"]; exists {
		t.Fatalf("cross-field total must not be emitted: %#v", attribution)
	}
	classes := attribution["evidence_classes"].(map[string]any)
	delivery := classes["tool_delivery"].(map[string]any)["observations"].([]any)
	value := delivery[1].(map[string]any)["value"].(map[string]any)
	if value["original_bytes"] != int64(50118) || value["delivered_bytes"] != int64(1819) {
		t.Fatalf("candidate delivery = %#v", value)
	}
	runtime := classes["runtime_usage"].(map[string]any)["observations"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if runtime["uncached_input"].(map[string]any)["value"] != float64(21386) || runtime["cache_read"].(map[string]any)["value"] != float64(11264) || runtime["cache_write"].(map[string]any)["value"] != float64(0) {
		t.Fatalf("baseline runtime = %#v", runtime)
	}
	assertAttributionValue(t, classes, "quality", 1, "score", float64(1))
	assertAttributionValue(t, classes, "runtime_reported_cost", 0, "value", float64(0))
	assertAttributionValue(t, classes, "estimated_cost", 0, "value", 0.026682)
	assertAttributionValue(t, classes, "estimated_cost", 1, "value", 0.005316)
	text := RenderSummary(summary)
	for _, want := range []string{"phase=measured_warm", "parallel and non-additive", "derived logical_input", "does not identify causal shares or provider-internal behavior"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
}

func TestCompressionAttributionLabelsUnavailableEvidence(t *testing.T) {
	manifest := Manifest{Methodology: MethodologyMetadata{Version: promptCacheTokenHitRateMethodologyVersion}}
	attempts := []Attempt{
		{AttemptID: "unsupported-cold", Side: Baseline, Role: ColdObservation, Status: "failed", Artifacts: map[string]string{}},
		{AttemptID: "unsupported-warm", Side: Candidate, Role: Warmup, Status: "failed", Artifacts: map[string]string{}},
	}
	attribution := BuildSummary(manifest, attempts)["compression_attribution"].(map[string]any)
	classes := attribution["evidence_classes"].(map[string]any)
	delivery := classes["tool_delivery"].(map[string]any)["observations"].([]any)[0].(map[string]any)
	if delivery["phase"] != ColdObservation || delivery["value"].(map[string]any)["state"] != "unavailable" {
		t.Fatalf("unavailable delivery = %#v", delivery)
	}
	runtime := classes["runtime_usage"].(map[string]any)["observations"].([]any)[0].(map[string]any)["value"].(map[string]any)
	if runtime["uncached_input"].(map[string]any)["state"] != "unavailable" {
		t.Fatalf("unavailable runtime = %#v", runtime)
	}
	warm := classes["tool_delivery"].(map[string]any)["observations"].([]any)[1].(map[string]any)
	if warm["phase"] != Warmup || warm["value"].(map[string]any)["state"] != "unavailable" {
		t.Fatalf("unavailable warmup delivery = %#v", warm)
	}
}

func TestCompressionAttributionReportsExactModelStepGaps(t *testing.T) {
	for _, test := range []struct {
		name, form, state string
	}{
		{name: "missing", form: "missing", state: "missing"},
		{name: "malformed", form: "malformed", state: "malformed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			metrics := ParseStream(writeStream(t, incompleteStream("INPUT", test.form)), nil, nil)
			attempt := Attempt{
				AttemptID: "affected", Side: Baseline, Role: MeasuredWarm,
				Metrics: metrics, Artifacts: map[string]string{"stream": "attempts/affected/stream.jsonl"},
			}
			manifest := Manifest{Methodology: MethodologyMetadata{Version: promptCacheTokenHitRateMethodologyVersion}}
			classes := BuildSummary(manifest, []Attempt{attempt})["compression_attribution"].(map[string]any)["evidence_classes"].(map[string]any)
			observation := classes["runtime_usage"].(map[string]any)["observations"].([]any)[0].(map[string]any)
			value := observation["value"].(map[string]any)["uncached_input"].(map[string]any)
			if value["state"] != test.state || value["value"] != nil {
				t.Fatalf("uncached input = %#v", value)
			}
			wantGap := map[string]any{"field": "uncached_input", "source_event_ordinal": uint64(2), "state": test.state}
			gaps, _ := value["evidence_gaps"].([]any)
			if len(gaps) != 1 || !reflect.DeepEqual(gaps[0], wantGap) {
				t.Fatalf("evidence gaps = %#v, want %#v", gaps, wantGap)
			}
			estimate := classes["estimated_cost"].(map[string]any)["observations"].([]any)[0].(map[string]any)["value"].(map[string]any)
			if estimate["state"] != test.state || !reflect.DeepEqual(estimate["evidence_gaps"], gaps) {
				t.Fatalf("estimated cost gap = %#v", estimate)
			}
			report := RenderSummary(BuildSummary(manifest, []Attempt{attempt}))
			for _, want := range []string{`"state":"` + test.state + `"`, `"field":"uncached_input"`, `"source_event_ordinal":2`} {
				if !strings.Contains(report, want) {
					t.Fatalf("report missing %s:\n%s", want, report)
				}
			}
		})
	}
}

func TestDeliveryAuthorityUsesFirstCompressedArtifact(t *testing.T) {
	dir, manifest, attempt := writeAuthorityFixture(t)
	database, err := dbpkg.Open(filepath.Join(dir, attempt.Artifacts["database"]))
	if err != nil {
		t.Fatal(err)
	}
	var sourceID string
	if err := database.DB().QueryRow("SELECT id FROM tool_calls WHERE session_id = ?", *attempt.SessionID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []dbpkg.Compressed{
		{ID: "aaaaaaaaaaaaaaaa", SourceID: sourceID, Output: "one", ContentHash: dbpkg.ContentHash("one"), Pipeline: "[]", Timestamp: "2026-01-01T00:00:00Z"},
		{ID: "bbbbbbbbbbbbbbbb", SourceID: sourceID, Output: "x", ContentHash: dbpkg.ContentHash("x"), Pipeline: "[]", Timestamp: "2026-01-02T00:00:00Z"},
	} {
		if err := database.InsertCompressed(&artifact); err != nil {
			t.Fatal(err)
		}
	}
	database.Close()
	materialized, err := materializeAttempt(dir, manifest, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.delivery.DeliveredBytes != 3 || materialized.delivery.Sources[0].(map[string]any)["compressed_artifact_id"] != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("delivery authority = %#v", materialized.delivery)
	}
}

func attributionAttempt(id, side string, input, read, write int, cost, estimate float64) Attempt {
	return Attempt{
		AttemptID: id, Side: side, Role: MeasuredWarm, Status: "completed",
		Metrics:   Metrics{StreamComplete: true, Score: Score{Score: 1, FullPass: true}, TokensIn: floatPointer(float64(input)), CacheRead: floatPointer(float64(read)), CacheWrite: floatPointer(float64(write)), Cost: floatPointer(cost), EstimatedAPICost: floatPointer(estimate), UsageEvidence: UsageEvidence{StepFinishEvents: 2, CostEvents: 2}},
		Artifacts: map[string]string{"stream": "attempts/" + id + "/stream.jsonl"},
	}
}

func assertAttributionValue(t *testing.T, classes map[string]any, class string, observation int, field string, want float64) {
	t.Helper()
	rows := classes[class].(map[string]any)["observations"].([]any)
	value := rows[observation].(map[string]any)["value"].(map[string]any)[field]
	if value != want {
		t.Fatalf("%s observation %d %s = %v, want %v", class, observation, field, value, want)
	}
}
