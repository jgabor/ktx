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
	dir := t.TempDir()
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if err := AppendAttempt(dir, attempt); err != nil {
			t.Fatal(err)
		}
	}
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
		return Attempt{AttemptID: id, ExperimentID: "pricing-validation", Side: side, Status: "completed", SessionID: stringPointer("session-" + id), Metrics: Metrics{
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
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateExperiment(dir); err == nil || !strings.Contains(err.Error(), "embedded provider/model") {
		t.Fatalf("ValidateExperiment error = %v, want embedded identity mismatch", err)
	}
}

func TestV4EstimatedEvidenceWithoutSnapshotRejected(t *testing.T) {
	manifest := pricingValidationManifest()
	attempts := pricingValidationAttempts()
	attempts[0].EstimatedAPICost = floatPointer(.01)
	dir := writePricingValidationFixture(t, manifest, attempts)
	if _, err := WriteSummary(dir); err == nil || !strings.Contains(err.Error(), "estimated_api_cost") {
		t.Fatalf("WriteSummary error = %v, want missing snapshot rejection", err)
	}

	// Summary-only derived evidence is also untrusted.
	attempts[0].EstimatedAPICost = nil
	if err := os.WriteFile(filepath.Join(dir, "attempts.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if err := AppendAttempt(dir, attempt); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"estimated_api_cost":0.01}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(dir); err == nil || !strings.Contains(err.Error(), "estimated_api_cost") {
		t.Fatalf("WriteSummary summary-only error = %v, want missing snapshot rejection", err)
	}
	if _, err := ValidateExperiment(dir); err == nil || !strings.Contains(err.Error(), "estimated_api_cost") {
		t.Fatalf("ValidateExperiment error = %v, want summary evidence rejection", err)
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
