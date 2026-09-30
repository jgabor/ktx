package experiment

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/db"
)

const selectionTokens = `"tokens":{"input":100,"output":10,"reasoning":5,"cache":{"read":0,"write":0}}`

func selectionStep(fields string) string {
	return `{"type":"step_finish","part":{"reason":"stop",` + fields + `}}` + "\n"
}

func selectionAttempt(t *testing.T, id, stream string) Attempt {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(stream), 0o600); err != nil {
		t.Fatal(err)
	}
	return Attempt{AttemptID: id, Metrics: ParseStream(path, nil, nil), Artifacts: map[string]string{"stream": path}}
}

func selectionPricing(t *testing.T, price db.Price) (string, Manifest) {
	t.Helper()
	dir := t.TempDir()
	metadata, err := writePricingSnapshot(dir, "openai", "gpt", price)
	if err != nil {
		t.Fatal(err)
	}
	return dir, Manifest{Model: "openai/gpt", Pricing: metadata}
}

func selectAttempts(t *testing.T, dir string, m Manifest, attempts ...Attempt) CostSelection {
	t.Helper()
	facts, err := AttemptCostObservations(dir, m, attempts)
	if err != nil {
		t.Fatal(err)
	}
	return SelectCostCohort(facts)
}

func TestCostSelectionMatrix(t *testing.T) {
	dir, m := selectionPricing(t, db.Price{Input: 1, Output: 2})
	complete := func(cost string) string { return selectionStep(selectionTokens + `,"cost":` + cost) }
	tests := []struct {
		name, baseline, candidate, basis, reason string
	}{
		{"positive reports", complete("1"), complete("2"), "reported", "reported_complete_positive"},
		{"reports without usage", selectionStep(`"cost":1`), selectionStep(`"cost":2`), "reported", "reported_complete_positive"},
		{"mixed zero positive", complete("0"), complete("2"), "reported", "reported_complete_positive"},
		{"uniform zero unknown access", complete("0"), complete("0"), "estimated_api", "reported_uniform_zero"},
		{"missing reports", selectionStep(selectionTokens), selectionStep(selectionTokens), "estimated_api", "reported_incomplete"},
		{"null reports", complete("null"), complete("null"), "estimated_api", "reported_incomplete"},
		{"partial positive reports", complete("9"), complete("null"), "estimated_api", "reported_incomplete"},
		{"partial steps positive subtotal", complete("9") + selectionStep(selectionTokens), complete("1"), "estimated_api", "reported_incomplete"},
		{"malformed report", complete(`"bad"`), complete("1"), "estimated_api", "reported_incomplete"},
		{"nonfinite report", complete("1e309"), complete("1"), "estimated_api", "reported_incomplete"},
		{"negative report", complete("-1"), complete("1"), "estimated_api", "reported_invalid"},
		{"negative report cancellation", complete("-1") + complete("2"), complete("1"), "estimated_api", "reported_invalid"},
		{"uniform zero absent usage", selectionStep(`"cost":0`), selectionStep(`"cost":0`), "unavailable", "reported_uniform_zero"},
		{"missing both", selectionStep(`"cost":null`), selectionStep(`"cost":null`), "unavailable", "reported_incomplete"},
		{"partial reports incomplete estimates", complete("9"), selectionStep(`"cost":null`), "unavailable", "reported_incomplete"},
		{"invalid usage positive report", selectionStep(strings.Replace(selectionTokens, `"input":100`, `"input":-1`, 1) + `,"cost":1`), complete("2"), "reported", "reported_complete_positive"},
		{"invalid usage no report", selectionStep(strings.Replace(selectionTokens, `"input":100`, `"input":-1`, 1)), selectionStep(selectionTokens), "unavailable", "reported_incomplete"},
		{"malformed usage no report", selectionStep(strings.Replace(selectionTokens, `"input":100`, `"input":"bad"`, 1)), selectionStep(selectionTokens), "unavailable", "reported_incomplete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := []Attempt{selectionAttempt(t, "baseline", test.baseline), selectionAttempt(t, "candidate", test.candidate)}
			before, err := MarshalJSON(attempts)
			if err != nil {
				t.Fatal(err)
			}
			got := selectAttempts(t, dir, m, attempts...)
			if got.Basis != test.basis || got.Reason != test.reason || got.RequiredCount != 2 || got.Unit != "USD" || got.PolicyVersion != 1 {
				t.Fatalf("selection = %+v", got)
			}
			for i, row := range got.Observations {
				if row.ID != attempts[i].AttemptID || row.Source != attempts[i].Artifacts["stream"] || row.Pricing.SHA256 != m.Pricing.SHA256 {
					t.Fatalf("lost provenance: %+v", row)
				}
				if got.Basis == "unavailable" {
					if row.Value != nil || got.EstimateReason == "" {
						t.Fatalf("unavailable acquired numeric cost: %+v", got)
					}
				} else if row.Value == nil {
					t.Fatalf("missing selected value: %+v", row)
				} else if got.Basis == "reported" && *row.Value != *attempts[i].Cost {
					t.Fatalf("reported value changed: %+v", row)
				} else if got.Basis == "estimated_api" && *row.Value != *estimateAttemptAPICost(attempts[i], mustSelectionPricing(t, dir, m)).Value {
					t.Fatalf("estimate or rounding changed: %+v", row)
				}
			}
			after, _ := MarshalJSON(attempts)
			if string(before) != string(after) {
				t.Fatal("selector mutated input evidence")
			}
			if _, err := json.Marshal(got); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func mustSelectionPricing(t *testing.T, dir string, m Manifest) *pricingSnapshot {
	t.Helper()
	p, err := preparePricingEvidence(dir, m)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCostSelectionStepUsageCoverage(t *testing.T) {
	dir, m := selectionPricing(t, db.Price{Input: 1, Output: 2})
	for _, field := range []string{`"input":100`, `"output":10`, `"reasoning":5`, `"read":0`, `"write":0`} {
		for _, replacement := range []string{"-1", "null", `"bad"`, "0.5"} {
			t.Run(field+replacement, func(t *testing.T) {
				name := strings.Split(field, ":")[0] + ":"
				first := strings.Replace(selectionTokens, field, name+replacement, 1)
				// A positive second step can hide a signed first step in the
				// historical aggregate parser; selected estimates must reject it.
				second := strings.Replace(selectionTokens, field, name+"200", 1)
				a := selectionAttempt(t, "a", selectionStep(first)+selectionStep(second))
				got := selectAttempts(t, dir, m, a)
				if got.Basis != "unavailable" || got.EstimatedEligible != 0 || !containsString(got.Observations[0].EstimatedReasons, "estimated_step_usage_invalid") {
					t.Fatalf("invalid step accepted: %+v", got)
				}
			})
		}
	}
	good := selectionAttempt(t, "good", selectionStep(selectionTokens)+selectionStep(selectionTokens))
	if got := selectAttempts(t, dir, m, good); got.Basis != "estimated_api" || got.EstimatedEligible != 1 {
		t.Fatalf("complete steps rejected: %+v", got)
	}
}

func TestCostSelectionPricingAuthority(t *testing.T) {
	a := selectionAttempt(t, "a", selectionStep(selectionTokens))
	a.EstimatedAPICost = floatPointer(999) // Never authority.
	for _, test := range []struct {
		name      string
		edit      func(string, *Manifest)
		wantError bool
	}{
		{"valid", func(string, *Manifest) {}, false},
		{"unclaimed pricing", func(_ string, m *Manifest) { m.Pricing = nil }, false},
		{"missing hash", func(_ string, m *Manifest) { m.Pricing.SHA256 = "" }, true},
		{"wrong hash", func(_ string, m *Manifest) { m.Pricing.SHA256 = strings.Repeat("0", 64) }, true},
		{"missing snapshot", func(dir string, _ *Manifest) {
			if err := os.Remove(filepath.Join(dir, "pricing.json")); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"metadata model mismatch", func(_ string, m *Manifest) { m.Pricing.Model = "wrong" }, true},
		{"embedded model mismatch", func(dir string, m *Manifest) {
			p, err := writePricingSnapshot(dir, "openai", "wrong", db.Price{Input: 1, Output: 2})
			if err != nil {
				t.Fatal(err)
			}
			p.Model = "gpt"
			m.Pricing = p
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, m := selectionPricing(t, db.Price{Input: 1, Output: 2})
			test.edit(dir, &m)
			facts, err := AttemptCostObservations(dir, m, []Attempt{a})
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
			if err != nil {
				return
			}
			got := SelectCostCohort(facts)
			if test.name == "unclaimed pricing" {
				if got.Basis != "unavailable" {
					t.Fatalf("copied estimate accepted: %+v", got)
				}
			} else if got.Basis != "estimated_api" || *got.Observations[0].Value != .00013 {
				t.Fatalf("source estimate = %+v", got)
			}
		})
	}
	// Claimed pricing corruption is not downgraded by usable reported cost.
	dir, m := selectionPricing(t, db.Price{Input: 1, Output: 2})
	m.Pricing.SHA256 = "wrong"
	a = selectionAttempt(t, "a", selectionStep(selectionTokens+`,"cost":1`))
	if _, err := AttemptCostObservations(dir, m, []Attempt{a}); err == nil {
		t.Fatal("corruption downgraded to cost availability")
	}
}

func TestCostSelectionRatesAndComparability(t *testing.T) {
	a := selectionAttempt(t, "a", selectionStep(selectionTokens))
	for _, test := range []struct {
		name  string
		price db.Price
		basis string
	}{
		{"priced", db.Price{Input: 1, Output: 2}, "estimated_api"},
		{"missing rates", db.Price{}, "unavailable"},
		{"zero rates", db.Price{InputSet: true, OutputSet: true}, "estimated_api"},
		{"negative rate", db.Price{Input: -1, Output: 2}, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, m := selectionPricing(t, test.price)
			got := selectAttempts(t, dir, m, a)
			if got.Basis != test.basis {
				t.Fatalf("selection = %+v", got)
			}
			if test.name == "zero rates" && (got.Observations[0].Value == nil || *got.Observations[0].Value != 0) {
				t.Fatal("zero estimate treated as missing")
			}
		})
	}
	dir, m := selectionPricing(t, db.Price{Input: 1, Output: 2})
	first, err := AttemptCostObservations(dir, m, []Attempt{a})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		price db.Price
		basis string
	}{
		{"same rates new provenance", db.Price{Input: 1, Output: 2, SourceURL: "https://example.invalid", RetrievedAt: "later"}, "estimated_api"},
		{"different rates", db.Price{Input: 2, Output: 2}, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, m := selectionPricing(t, test.price)
			second, err := AttemptCostObservations(dir, m, []Attempt{a})
			if err != nil {
				t.Fatal(err)
			}
			got := SelectCostCohort(append(first, second...))
			if got.Basis != test.basis {
				t.Fatalf("selection = %+v", got)
			}
			if test.basis == "unavailable" && got.EstimateReason != "estimated_incomparable" {
				t.Fatalf("reason = %+v", got)
			}
		})
	}
}

func TestCostSelectionPersistedAndEmpty(t *testing.T) {
	for _, value := range []*float64{nil, floatPointer(0), floatPointer(-1), floatPointer(math.NaN()), floatPointer(math.Inf(1)), floatPointer(math.Inf(-1)), floatPointer(2)} {
		got := SelectCostCohort([]CostObservation{ReportedCostObservation("row", value, "bench_runs.cost")})
		want := "unavailable"
		if value != nil && *value == 2 {
			want = "reported"
		}
		if got.Basis != want {
			t.Fatalf("value %v: %+v", value, got)
		}
		if want == "unavailable" && got.Observations[0].Value != nil {
			t.Fatal("unavailable acquired numeric value")
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatal(err)
		}
	}
	if got := SelectCostCohort(nil); got.Basis != "unavailable" || got.Reason != "empty_cohort" || len(got.Observations) != 0 {
		t.Fatalf("empty: %+v", got)
	}
	if got := SelectCostCohort([]CostObservation{{}}); got.Basis != "unavailable" {
		t.Fatalf("zero fact: %+v", got)
	}
}

func TestCostSelectionWholeRankingCohortAndEvidencePreservation(t *testing.T) {
	dir, m := selectionPricing(t, db.Price{Input: 1, Output: 2})
	attempts := []Attempt{
		selectionAttempt(t, "baseline", selectionStep(selectionTokens+`,"cost":1`)),
		selectionAttempt(t, "candidate-a", selectionStep(selectionTokens+`,"cost":0`)),
		selectionAttempt(t, "candidate-b", selectionStep(selectionTokens)),
	}
	attempts[1].Status, attempts[1].Role, attempts[1].FullPass = "failed", Warmup, false
	before := append([]Attempt(nil), attempts...)
	got := selectAttempts(t, dir, m, attempts...)
	if got.Basis != "estimated_api" || got.RequiredCount != 3 || got.ReportedEligible != 2 || got.EstimatedEligible != 3 {
		t.Fatalf("cherry-picked cohort: %+v", got)
	}
	for _, row := range got.Observations {
		if row.Value == nil || *row.Value != .00013 {
			t.Fatalf("mixed basis: %+v", got)
		}
	}
	if !reflect.DeepEqual(before, attempts) {
		t.Fatal("observations changed")
	}
	// One unpriced candidate must prevent every cost conclusion, not disappear.
	m.Pricing = nil
	got = selectAttempts(t, dir, m, attempts...)
	if got.Basis != "unavailable" || len(got.Observations) != 3 || !reflect.DeepEqual(before, attempts) {
		t.Fatalf("unavailable discarded evidence: %+v", got)
	}
}

func TestCostSelectionMaterializedArchive(t *testing.T) {
	dir := produceEntryArchive(t, "exact-match", true, true)
	a, err := openArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m, attempts, _, err := loadExperimentEvidence(a)
	if err != nil {
		t.Fatal(err)
	}
	before := BuildSummary(m, attempts)
	got := selectAttempts(t, dir, m, attempts...)
	if got.Basis != "estimated_api" || got.Reason != "reported_uniform_zero" || got.RequiredCount != len(attempts) {
		t.Fatalf("materialized selection: %+v", got)
	}
	if !reflect.DeepEqual(before, BuildSummary(m, attempts)) {
		t.Fatal("historical summary changed")
	}
}

func TestCostSelectionStepIdentity(t *testing.T) {
	dir, m := selectionPricing(t, db.Price{Input: 1, Output: 2})
	matching := `,"provider":"openai","model":"gpt"`
	for _, test := range []struct {
		name, identity string
		laterStep      bool
		reasons        []string
	}{
		{name: "absent"},
		{name: "matching", identity: matching},
		{name: "provider-only-matching", identity: `,"provider":"openai"`},
		{name: "model-only-matching", identity: `,"model":"gpt"`},
		{name: "provider-conflict-only", identity: `,"provider":"different-provider"`, reasons: []string{"estimated_step_provider_conflict"}},
		{name: "model-conflict-only", identity: `,"provider":"openai","model":"different-model"`, reasons: []string{"estimated_step_model_conflict"}},
		{name: "model-conflict-no-provider", identity: `,"model":"different-model"`, reasons: []string{"estimated_step_model_conflict"}},
		{name: "both-conflicts", identity: `,"provider":"different-provider","model":"different-model"`, reasons: []string{"estimated_step_provider_conflict", "estimated_step_model_conflict"}},
		{name: "later-step-conflict", identity: `,"model":"different-model"`, laterStep: true, reasons: []string{"estimated_step_model_conflict"}},
	} {
		for _, reported := range []string{"null", "0", "2"} {
			t.Run(test.name+"/"+reported, func(t *testing.T) {
				fields := selectionTokens + `,"cost":` + reported
				stream := selectionStep(fields + test.identity)
				if test.laterStep {
					stream = selectionStep(fields+matching) + stream
				}
				attempts := []Attempt{
					selectionAttempt(t, "baseline", selectionStep(fields+matching)),
					selectionAttempt(t, "candidate", stream),
				}
				before, err := MarshalJSON(attempts)
				if err != nil {
					t.Fatal(err)
				}
				got := selectAttempts(t, dir, m, attempts...)
				if got.Observations[1].Value != nil {
					t.Logf("candidate selected cost = %.8f", *got.Observations[1].Value)
				}
				wantBasis, wantEligible := "estimated_api", 2
				if len(test.reasons) > 0 {
					wantBasis, wantEligible = "unavailable", 1
				}
				if reported == "2" {
					wantBasis = "reported"
				}
				if got.Basis != wantBasis || got.EstimatedEligible != wantEligible || got.RequiredCount != 2 {
					t.Fatalf("selection = %+v, want basis %s, estimated eligible %d", got, wantBasis, wantEligible)
				}
				if reported != "null" && got.ReportedEligible != 2 {
					t.Fatalf("identity conflict changed reported eligibility: %+v", got)
				}
				for i, row := range got.Observations {
					if i == 1 && !reflect.DeepEqual(row.EstimatedReasons, append([]string{}, test.reasons...)) {
						t.Fatalf("conflict reasons = %v, want %v", row.EstimatedReasons, test.reasons)
					}
					if i == 0 && len(row.EstimatedReasons) != 0 {
						t.Fatalf("unconflicted cohort member became ineligible: %+v", row)
					}
					if got.Basis == "unavailable" && (row.Value != nil || got.EstimateReason != "estimated_incomplete") {
						t.Fatalf("mixed partial cohort cost: %+v", got)
					}
					if got.Basis == "reported" && (row.Value == nil || *row.Value != *attempts[i].Cost) {
						t.Fatalf("reported value changed: %+v", row)
					}
					if got.Basis == "estimated_api" {
						want := .00013 * float64(len(attempts[i].ModelSteps))
						if row.Value == nil || *row.Value != want {
							t.Fatalf("estimate = %+v, want %v", row, want)
						}
					}
				}
				after, err := MarshalJSON(attempts)
				if err != nil || string(before) != string(after) {
					t.Fatalf("source identity or unrelated metrics changed: %v", err)
				}
			})
		}
	}
}
