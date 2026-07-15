package experiment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/db"
)

func writeV5ValidationFixture(t *testing.T, mutate func(*Manifest, *[]Attempt), withPricing bool) string {
	t.Helper()
	dir := t.TempDir()
	manifest := cacheAwareManifest()
	manifest.ExperimentID = "v5-validation"
	manifest.TargetValidPerPipeline = 1
	manifest.Model = "openai/test"
	manifest.Pipelines = map[string]PipelineMetadata{Baseline: {PipelineID: "baseline", SHA256: "b"}, Candidate: {PipelineID: "candidate", SHA256: "c"}}
	manifest.CacheControl = &CacheControlManifest{Mode: "observed", Source: "unavailable", WarmupCount: 1, Model: "openai/test", Provider: "openai", RuntimeVersion: "1.17.20"}
	manifest.Identities = map[string]string{"opencode": "1.17.20"}
	attempts := []Attempt{
		phaseAttempt("b-cold", Baseline, ColdObservation, 0, 1), phaseAttempt("c-cold", Candidate, ColdObservation, 0, 1),
		phaseAttempt("b-warm", Baseline, Warmup, 0, 1), phaseAttempt("c-warm", Candidate, Warmup, 0, 1),
		phaseAttempt("b-measured", Baseline, MeasuredWarm, 1, 1), phaseAttempt("c-measured", Candidate, MeasuredWarm, 1, 1),
	}
	for i := range attempts {
		attempts[i].ExperimentID = manifest.ExperimentID
		attempts[i].PipelineID = manifest.Pipelines[attempts[i].Side].PipelineID
		attempts[i].ConfigSHA256 = manifest.Pipelines[attempts[i].Side].SHA256
		attempts[i].CacheControl.ExecutionOrder = i + 1
		attempts[i].CacheControl.Model = manifest.CacheControl.Model
		attempts[i].CacheControl.Provider = manifest.CacheControl.Provider
		attempts[i].CacheControl.RuntimeVersion = manifest.CacheControl.RuntimeVersion
	}
	if mutate != nil {
		mutate(&manifest, &attempts)
	}
	if withPricing {
		rate := .5
		pricing, err := writePricingSnapshot(dir, "openai", "test", db.Price{Input: 1, Output: 2, CacheRead: &rate, CacheWrite: &rate, InputSet: true, OutputSet: true})
		if err != nil {
			t.Fatal(err)
		}
		manifest.Pricing = pricing
	} else {
		manifest.Pricing = nil
	}
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if err := AppendAttempt(dir, attempt); err != nil {
			t.Fatal(err)
		}
	}
	if !withPricing {
		if err := WriteJSON(filepath.Join(dir, "summary.json"), BuildSummary(manifest, attempts)); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertV5Validation(t *testing.T, dir string, want bool) map[string]any {
	t.Helper()
	report, err := ValidateExperiment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report["valid"] != want {
		t.Fatalf("valid = %v, want %v; report=%#v", report["valid"], want, report)
	}
	return report
}

func isolateV5Fixture(manifest *Manifest, attempts *[]Attempt, baselineHash, candidateHash string) {
	manifest.CacheControl.Mode, manifest.CacheControl.Source = "isolated", "runtime_adapter"
	manifest.CacheControl.RuntimeVersion = "1.17.15"
	manifest.Identities["opencode"] = "1.17.15"
	manifest.CacheControl.NamespaceSHA256 = map[string]string{Baseline: baselineHash, Candidate: candidateHash}
	for i := range *attempts {
		metadata := (*attempts)[i].CacheControl
		metadata.Mode = "isolated"
		metadata.Source = "runtime_adapter"
		metadata.RuntimeVersion = "1.17.15"
		metadata.NamespaceSHA256 = manifest.CacheControl.NamespaceSHA256[(*attempts)[i].Side]
	}
}

func TestValidateV5ObservedAndMalformedEvidence(t *testing.T) {
	t.Run("valid observed", func(t *testing.T) { assertV5Validation(t, writeV5ValidationFixture(t, nil, true), true) })
	t.Run("valid isolated", func(t *testing.T) {
		assertV5Validation(t, writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
			isolateV5Fixture(manifest, attempts, strings.Repeat("a", 64), strings.Repeat("b", 64))
		}, true), true)
	})
	t.Run("missing roles cannot satisfy target", func(t *testing.T) {
		dir := writeV5ValidationFixture(t, func(_ *Manifest, attempts *[]Attempt) {
			for i := range *attempts {
				if (*attempts)[i].Role == MeasuredWarm {
					(*attempts)[i].Role = ""
					(*attempts)[i].CacheControl = nil
				}
			}
		}, true)
		report := assertV5Validation(t, dir, false)
		counts := report["valid_counts"].(map[string]int)
		if counts[Baseline] != 0 || counts[Candidate] != 0 {
			t.Fatalf("strict counts = %#v", counts)
		}
	})
	t.Run("missing setup", func(t *testing.T) {
		assertV5Validation(t, writeV5ValidationFixture(t, func(_ *Manifest, attempts *[]Attempt) {
			filtered := (*attempts)[:0]
			for _, a := range *attempts {
				if a.AttemptID != "c-warm" {
					filtered = append(filtered, a)
				}
			}
			*attempts = filtered
		}, true), false)
	})
	t.Run("wrong schema", func(t *testing.T) {
		dir := writeV5ValidationFixture(t, nil, true)
		path := filepath.Join(dir, "summary.json")
		data, _ := os.ReadFile(path)
		var summary map[string]any
		if err := json.Unmarshal(data, &summary); err != nil {
			t.Fatal(err)
		}
		summary["schema_version"] = 2
		if err := WriteJSON(path, summary); err != nil {
			t.Fatal(err)
		}
		assertV5Validation(t, dir, false)
	})
	t.Run("namespace reuse", func(t *testing.T) {
		assertV5Validation(t, writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
			same := strings.Repeat("c", 64)
			isolateV5Fixture(manifest, attempts, same, same)
		}, true), false)
	})
	t.Run("namespace drift", func(t *testing.T) {
		assertV5Validation(t, writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
			isolateV5Fixture(manifest, attempts, strings.Repeat("d", 64), strings.Repeat("e", 64))
			for i := range *attempts {
				if (*attempts)[i].AttemptID == "b-measured" {
					(*attempts)[i].CacheControl.NamespaceSHA256 = strings.Repeat("f", 64)
				}
			}
		}, true), false)
	})
	for _, test := range []struct{ name, hash string }{
		{name: "short namespace hash", hash: "abc123"},
		{name: "uppercase namespace hash", hash: strings.Repeat("A", 64)},
		{name: "nonhex namespace hash", hash: strings.Repeat("g", 64)},
		{name: "raw namespace value", hash: "ktx-secret-namespace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertV5Validation(t, writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
				isolateV5Fixture(manifest, attempts, test.hash, strings.Repeat("b", 64))
			}, true), false)
		})
	}
	t.Run("observed with retained hash", func(t *testing.T) {
		assertV5Validation(t, writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
			manifest.CacheControl.NamespaceSHA256 = map[string]string{Baseline: strings.Repeat("a", 64)}
			(*attempts)[0].CacheControl.NamespaceSHA256 = strings.Repeat("a", 64)
		}, true), false)
	})
	t.Run("manifest attempt namespace mismatch", func(t *testing.T) {
		assertV5Validation(t, writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
			isolateV5Fixture(manifest, attempts, strings.Repeat("a", 64), strings.Repeat("b", 64))
			(*attempts)[0].CacheControl.NamespaceSHA256 = strings.Repeat("c", 64)
		}, true), false)
	})
	t.Run("missing pricing rejects derived evidence", func(t *testing.T) {
		if _, err := ValidateExperiment(writeV5ValidationFixture(t, nil, false)); err == nil {
			t.Fatal("ValidateExperiment returned nil error without pricing snapshot")
		}
	})
}
