package experiment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/db"
)

func writePricingValidationFixture(t *testing.T, manifest Manifest, attempts []Attempt) string {
	t.Helper()
	dir := produceEntryArchive(t, "exact-match", false, true)
	produced, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range produced {
		produced[i].EstimatedAPICost = attempts[i].EstimatedAPICost
	}
	retainFixtureConfigs(t, dir, &manifest)
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	writeAttemptRecords(t, dir, produced...)
	return dir
}

func pricingValidationManifest() Manifest {
	manifest := cacheFixtureManifest()
	manifest.ExperimentID = "pricing-validation"
	manifest.Model = "openai/gpt"
	manifest.TargetValidPerPipeline = 1
	return manifest
}

func pricingValidationAttempts() []Attempt {
	makeAttempt := func(id, side string) Attempt {
		return Attempt{SchemaVersion: 1, AttemptID: id, ExperimentID: "pricing-validation", Side: side, Status: "completed", SessionID: stringPointer("session-" + id), Metrics: Metrics{
			StreamComplete: true, Score: Score{Score: 1, FullPass: true, AnswerMatchesExpected: true}, ToolCallCount: 1,
			TokensIn: floatPointer(100), TokensOut: floatPointer(10), Reasoning: floatPointer(5), CacheRead: floatPointer(0), CacheWrite: floatPointer(0),
		}}
	}
	return []Attempt{makeAttempt("baseline", Baseline), makeAttempt("candidate", Candidate)}
}

func TestV4PricingValidationWrongEmbeddedModel(t *testing.T) {
	manifest := pricingValidationManifest()
	dir := writePricingValidationFixture(t, manifest, pricingValidationAttempts())
	metadata, err := writePricingSnapshot(dir, "openai", "wrong", db.Price{Input: 1, Output: 2, InputSet: true, OutputSet: true})
	if err != nil {
		t.Fatal(err)
	}
	metadata.Model = "gpt" // Metadata matches; embedded hash-bound identity does not.
	manifest.Pricing = metadata
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(dir); err == nil || !strings.Contains(err.Error(), "embedded provider/model") {
		t.Fatalf("WriteSummary error = %v, want embedded identity mismatch", err)
	}
	if _, err := ValidateExperiment(dir); err == nil || !strings.Contains(err.Error(), "embedded provider/model") {
		t.Fatalf("ValidateExperiment error = %v, want embedded identity mismatch", err)
	}
}

func TestCopiedEstimatedEvidenceWithoutSnapshotIsNotAuthority(t *testing.T) {
	manifest := pricingValidationManifest()
	attempts := pricingValidationAttempts()
	attempts[0].EstimatedAPICost = floatPointer(.01)
	dir := writePricingValidationFixture(t, manifest, attempts)
	summary, err := WriteSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := summary["estimated_api_cost_basis"]; exists {
		t.Fatal("copied attempt estimate acquired pricing authority")
	}

	// Summary-only derived evidence is also untrusted.
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"estimated_api_cost":0.01}`), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err = WriteSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := summary["estimated_api_cost_basis"]; exists {
		t.Fatal("copied summary estimate acquired pricing authority")
	}
	if report, err := ValidateExperiment(dir); err != nil || report["valid"] != true {
		t.Fatalf("regenerated retained evidence = %v %v", report, err)
	}
}

func TestV4ValidPricingSnapshotRegeneratesAndValidates(t *testing.T) {
	manifest := pricingValidationManifest()
	dir := writePricingValidationFixture(t, manifest, pricingValidationAttempts())
	metadata, err := writePricingSnapshot(dir, "openai", "gpt", db.Price{Input: 1, Output: 2, InputSet: true, OutputSet: true})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Pricing = metadata
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}
	report, err := ValidateExperiment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if valid, _ := report["valid"].(bool); !valid {
		t.Fatalf("validation report = %#v", report)
	}
}
