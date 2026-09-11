package experiment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeV5ValidationFixture(t *testing.T, mutate func(*Manifest, *[]Attempt), withPricing bool) string {
	t.Helper()
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&manifest, &attempts)
	}
	if !withPricing {
		manifest.Pricing = nil
	}
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	writeAttemptRecords(t, dir, attempts...)
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
	manifest.CacheControl.Mode, manifest.CacheControl.Source = "isolated", "provider_adapter"
	manifest.CacheControl.NamespaceSHA256 = map[string]string{Baseline: baselineHash, Candidate: candidateHash}
	for i := range *attempts {
		metadata := (*attempts)[i].CacheControl
		metadata.Mode = "isolated"
		metadata.Source = "provider_adapter"
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
	t.Run("isolated with prior pinned version", func(t *testing.T) {
		assertV5Validation(t, writeV5ValidationFixture(t, func(manifest *Manifest, attempts *[]Attempt) {
			isolateV5Fixture(manifest, attempts, strings.Repeat("a", 64), strings.Repeat("b", 64))
			manifest.CacheControl.RuntimeVersion = "1.17.15"
			manifest.Identities["opencode"] = "1.17.15"
			for i := range *attempts {
				(*attempts)[i].CacheControl.RuntimeVersion = "1.17.15"
			}
		}, true), false)
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
				if a.Side != Candidate || a.Role != Warmup {
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
		if _, err := ValidateExperiment(dir); err == nil || !strings.Contains(err.Error(), "unsupported summary schema_version 2") {
			t.Fatalf("validation error = %v", err)
		}
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
				if (*attempts)[i].Side == Baseline && (*attempts)[i].Role == MeasuredWarm {
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
		report := assertV5Validation(t, writeV5ValidationFixture(t, nil, false), false)
		if !containsString(report["methodology_reasons"].([]string), promptCacheTokenHitRateMethodologyVersion+" pricing snapshot is required") {
			t.Fatalf("missing pricing reason: %v", report)
		}
	})
}
