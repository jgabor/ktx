package reuse

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/experiment"
)

func TestGeminiParityPreservesNativeRuntimeAndFieldLoss(t *testing.T) {
	native := fixture(t, "gemini-v1beta-explicit-cache-sanitized.json")
	normalized := fixture(t, "opencode-gemini-cache-step-sanitized.jsonl")
	nativeBefore, normalizedBefore := string(native), string(normalized)

	evidence, err := BuildGeminiParityEvidence(native, normalized, "v1beta retrieved 2026-07-24", "1.18.4")
	if err != nil {
		t.Fatalf("BuildGeminiParityEvidence() error = %v", err)
	}
	if evidence.Native.Raw != nativeBefore || evidence.Normalized.Raw != normalizedBefore || string(native) != nativeBefore || string(normalized) != normalizedBefore {
		t.Fatal("Gemini normalization rewrote a retained source")
	}
	if evidence.Native.Source == evidence.Normalized.Source || evidence.Native.CacheObject == nil {
		t.Fatalf("independent evidence sources = %#v", evidence)
	}
	assertMeasured(t, "cache object tokens", evidence.Native.CacheObject.TotalTokens, 4096)
	if evidence.Native.CacheObject.Retry.MaxAttempts == nil || *evidence.Native.CacheObject.Retry.MaxAttempts != 1 {
		t.Fatalf("cache-object retry evidence = %#v", evidence.Native.CacheObject.Retry)
	}
	if len(evidence.Native.Observations) != 1 || len(evidence.Normalized.Observations) != 1 {
		t.Fatalf("observation counts = %d/%d", len(evidence.Native.Observations), len(evidence.Normalized.Observations))
	}
	nativeEvent, runtimeEvent := evidence.Native.Observations[0], evidence.Normalized.Observations[0]
	assertMeasured(t, "native uncached input", nativeEvent.UncachedInput, 64)
	assertMeasured(t, "native cache read", nativeEvent.CacheRead, 4096)
	assertMeasured(t, "native output", nativeEvent.Output, 12)
	assertMeasured(t, "native measured-zero reasoning", nativeEvent.Reasoning, 0)
	if nativeEvent.CacheWrite.State != experiment.MeasurementUnknown || nativeEvent.CacheWrite.Value != nil {
		t.Fatalf("native cache write = %#v, want unavailable", nativeEvent.CacheWrite)
	}
	assertMeasured(t, "runtime uncached input", runtimeEvent.UncachedInput, 64)
	assertMeasured(t, "runtime cache read", runtimeEvent.CacheRead, 4096)
	assertMeasured(t, "runtime cache write", runtimeEvent.CacheWrite, 0)
	if nativeEvent.Identity.ResponseID == nil || nativeEvent.Control.NamespaceHash == nil || nativeEvent.Retry.MaxAttempts == nil {
		t.Fatalf("native identity/control/retry = %#v", nativeEvent)
	}
	if runtimeEvent.Identity.SessionID == nil || runtimeEvent.Control.Availability == nil || runtimeEvent.Retry.Availability == nil {
		t.Fatalf("runtime identity/control/retry = %#v", runtimeEvent)
	}
	if len(evidence.FieldAccounting) < 10 {
		t.Fatalf("field accounting has %d entries", len(evidence.FieldAccounting))
	}
	for _, want := range []string{"dropped", "renamed", "unavailable", "split", "weakened"} {
		found := false
		for _, field := range evidence.FieldAccounting {
			found = found || field.Disposition == want
		}
		if !found {
			t.Errorf("field accounting lacks %q disposition", want)
		}
	}
}

func TestGeminiUnavailableFixtureRetainsAbsenceWithoutInventingZero(t *testing.T) {
	raw := fixture(t, "gemini-v1beta-explicit-cache-unavailable-sanitized.json")
	evidence, err := NormalizeGemini(raw, "v1beta retrieved 2026-07-24")
	if err != nil {
		t.Fatalf("NormalizeGemini() error = %v", err)
	}
	if evidence.Raw != string(raw) || evidence.Outcome != "unavailable" || evidence.Unavailable == nil || !strings.Contains(*evidence.Unavailable, "INVALID_ARGUMENT") {
		t.Fatalf("unavailable evidence = %#v", evidence)
	}
	if evidence.CacheObject != nil || len(evidence.Observations) != 0 {
		t.Fatalf("unavailable fixture fabricated observations: %#v", evidence)
	}
}

func TestGeminiNormalizationPreservesMalformedAndAbsentTokenStates(t *testing.T) {
	original := string(fixture(t, "gemini-v1beta-explicit-cache-sanitized.json"))
	for _, tt := range []struct {
		name, old, replacement string
		measurement            func(experiment.SourceEvent) experiment.IntegerMeasurement
		want                   experiment.MeasurementState
	}{
		{"fractional cache read", `"cached_content_token_count": 4096`, `"cached_content_token_count": 4096.5`, func(event experiment.SourceEvent) experiment.IntegerMeasurement { return event.CacheRead }, experiment.MeasurementMalformed},
		{"absent reasoning", `"thoughts_token_count": 0,`, ``, func(event experiment.SourceEvent) experiment.IntegerMeasurement { return event.Reasoning }, experiment.MeasurementUnknown},
		{"uncached underflow", `"prompt_token_count": 4160`, `"prompt_token_count": 4000`, func(event experiment.SourceEvent) experiment.IntegerMeasurement { return event.UncachedInput }, experiment.MeasurementMalformed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(strings.Replace(original, tt.old, tt.replacement, 1))
			retained, err := NormalizeGemini(raw, "snapshot")
			if err != nil {
				t.Fatalf("NormalizeGemini() error = %v", err)
			}
			measurement := tt.measurement(retained.Observations[0])
			if measurement.State != tt.want || measurement.Value != nil {
				t.Fatalf("measurement = %#v, want %s without value", measurement, tt.want)
			}
		})
	}
}

func TestGeminiCapabilityEvidenceIsVersionAndSourceQualified(t *testing.T) {
	raw := fixtureFromEvidence(t, "gemini-opencode-capability.json")
	var capability struct {
		RetrievedAt string `json:"retrieved_at"`
		Gemini      struct {
			APIFamily   string            `json:"api_family"`
			Eligibility string            `json:"eligibility"`
			Retry       string            `json:"retry"`
			References  []string          `json:"references"`
			Explicit    map[string]any    `json:"explicit_cache_objects"`
			Usage       map[string]string `json:"generate_usage"`
		} `json:"gemini"`
		OpenCode struct {
			Version   string `json:"version"`
			Commit    string `json:"commit"`
			Semantics string `json:"semantics"`
		} `json:"opencode"`
		EvidenceState  map[string]string `json:"evidence_state"`
		ArtifactSHA256 map[string]string `json:"artifact_sha256"`
	}
	if err := json.Unmarshal(raw, &capability); err != nil {
		t.Fatal(err)
	}
	if capability.RetrievedAt != "2026-07-24" || !strings.Contains(capability.Gemini.APIFamily, "v1beta") || len(capability.Gemini.References) < 5 {
		t.Fatalf("Gemini source qualification = %#v", capability.Gemini)
	}
	if len(capability.Gemini.Explicit) < 6 || len(capability.Gemini.Usage) != 5 || capability.Gemini.Eligibility == "" || capability.Gemini.Retry == "" {
		t.Fatalf("Gemini capability semantics incomplete: %#v", capability.Gemini)
	}
	if capability.OpenCode.Version != "1.18.4" || len(capability.OpenCode.Commit) != 40 || !strings.Contains(capability.OpenCode.Semantics, "not wired") {
		t.Fatalf("OpenCode source qualification = %#v", capability.OpenCode)
	}
	if !strings.Contains(capability.EvidenceState["live_direct"], "unavailable") || !strings.Contains(capability.EvidenceState["offline_fixtures"], "not live") {
		t.Fatalf("evidence states = %#v", capability.EvidenceState)
	}
	if len(capability.ArtifactSHA256) != 5 {
		t.Fatalf("artifact hashes = %#v", capability.ArtifactSHA256)
	}
	for path, want := range capability.ArtifactSHA256 {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
			t.Errorf("%s sha256 = %s, want %s", path, got, want)
		}
	}
}
