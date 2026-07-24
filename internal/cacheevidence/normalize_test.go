package cacheevidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/experiment"
)

func TestBuildParityEvidencePreservesIndependentSources(t *testing.T) {
	native := fixture(t, "openai-responses-cache-sanitized.json")
	normalized := fixture(t, "opencode-cache-step-sanitized.jsonl")
	nativeBefore, normalizedBefore := string(native), string(normalized)

	evidence, err := BuildParityEvidence(native, normalized, "2026-07-24 documentation snapshot", "1.18.4")
	if err != nil {
		t.Fatalf("BuildParityEvidence() error = %v", err)
	}
	if evidence.Native.Raw != nativeBefore || evidence.Normalized.Raw != normalizedBefore || string(native) != nativeBefore || string(normalized) != normalizedBefore {
		t.Fatal("normalization rewrote a source record")
	}
	if evidence.Native.Source == evidence.Normalized.Source || len(evidence.Native.Observations) != 1 || len(evidence.Normalized.Observations) != 1 {
		t.Fatalf("evidence sources = %#v", evidence)
	}
	n := evidence.Native.Observations[0]
	o := evidence.Normalized.Observations[0]
	if n.Kind != experiment.NativeRequest || o.Kind != experiment.ModelStep {
		t.Fatalf("kinds = %q/%q", n.Kind, o.Kind)
	}
	assertMeasured(t, "native input", n.UncachedInput, 1100)
	assertMeasured(t, "native cache read", n.CacheRead, 1024)
	assertMeasured(t, "native cache write", n.CacheWrite, 0)
	assertMeasured(t, "native output", n.Output, 12)
	assertMeasured(t, "normalized input", o.UncachedInput, 1100)
	assertMeasured(t, "normalized cache read", o.CacheRead, 1024)
	assertMeasured(t, "normalized cache write", o.CacheWrite, 0)
	assertMeasured(t, "normalized output", o.Output, 12)
	if n.Identity.RequestID == nil || n.Identity.ResponseID == nil || n.Control.NamespaceHash == nil || n.Retry.MaxAttempts == nil {
		t.Errorf("native identity/control/retry fields missing: %#v", n)
	}
	if o.Identity.Provider == nil || o.Identity.Model == nil || o.Identity.SessionID == nil || o.Identity.MessageID == nil || o.Identity.PartID == nil || o.Control.Availability == nil || o.Retry.Availability == nil || *o.Retry.Availability != "unavailable_in_step_finish" {
		t.Errorf("normalized identity/retry fields missing: %#v", o)
	}
}

func TestNormalizationRejectsInvalidOrUnavailableCases(t *testing.T) {
	native := fixture(t, "openai-responses-cache-sanitized.json")
	native = []byte(strings.ReplaceAll(string(native), `"x_request_id": "req_synthetic_1"`, `"x_request_id": ""`))
	if _, err := NormalizeOpenAI(native, "snapshot"); err == nil || !strings.Contains(err.Error(), "lacks provider request identity") {
		t.Fatalf("NormalizeOpenAI() error = %v", err)
	}
	if _, err := NormalizeOpenCode([]byte(`{"type":"message.updated"}`), "1.18.4"); err == nil || !strings.Contains(err.Error(), "no step-finish") {
		t.Fatalf("NormalizeOpenCode() error = %v", err)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "tools", "bench", "fixtures", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertMeasured(t *testing.T, name string, got experiment.IntegerMeasurement, want int64) {
	t.Helper()
	if got.State != experiment.MeasurementMeasured || got.Value == nil || *got.Value != want {
		t.Errorf("%s = %#v, want measured %d", name, got, want)
	}
}
