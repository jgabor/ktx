// Table-driven unit tests for the costDeriver deep module
// (cost.go). The tests cover every branch in the Derive
// contract: caller-supplied cost wins, NULL inputs short-circuit,
// cold-cache miss triggers a refresh, warm-cache miss is silent,
// refresh failure is silent, and zero-tokens is a real value for
// the persist path.
//
// The fakePriceSource below is a function-field struct so each
// test case composes the exact behaviour it needs (a hit, a miss,
// a counter, an error). No httptest, no Prices singleton, no
// on-disk file. The cold/warm distinction is a function field, not
// a side effect of Load — that side effect belongs to the Prices
// type's tests (prices_test.go), not to the deriver's tests.
package db

import (
	"errors"
	"math"
	"testing"
)

// fakePriceSource is a hand-rolled PriceSource for unit tests. The
// three function fields compose the exact behaviour each test case
// needs; the three int counters are the "caller-side observation"
// surface (how many times each method was called). The Refresh
// function may mutate the Lookup function field to simulate the
// source's own cache-write side effect ("the cache write
// is the source's responsibility").
type fakePriceSource struct {
	LookupFn  func(provider, model string) (in, out float64, ok bool)
	ColdFn    func() bool
	RefreshFn func() error

	lookupCalls  int
	coldCalls    int
	refreshCalls int
}

func (f *fakePriceSource) Lookup(provider, model string) (in, out float64, ok bool) {
	f.lookupCalls++
	if f.LookupFn == nil {
		return 0, 0, false
	}
	return f.LookupFn(provider, model)
}

func (f *fakePriceSource) Cold() bool {
	f.coldCalls++
	if f.ColdFn == nil {
		return false
	}
	return f.ColdFn()
}

func (f *fakePriceSource) Refresh() error {
	f.refreshCalls++
	if f.RefreshFn == nil {
		return nil
	}
	return f.RefreshFn()
}

// ptrFloat64Cost returns a pointer to v; a tiny helper so table
// cases can express "value" without a literal-address dance. The
// name mirrors the persist_test.go helper of the same purpose so
// the shape is familiar to the existing test surface.
func ptrFloat64Cost(v float64) *float64 { return &v }

// near reports whether a and b agree within the cost-derivation 6-decimal
// precision (1e-9 is well below the rounding step of 1e-6). The
// helper exists so the test cases can compare derived values
// without re-implementing the tolerance check at every row.
func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }

// TestCostDeriver_Derive covers branches 1 through 7 of the contract.
// Each row is one
// branch of the contract; the table is the regression net for
// the deep module so a future change to Derive has a single place
// to update or extend.
//
// The later branches are covered by the existing persist_test.go and
// bench_test.go tests (TestPersistToolCall_CostDerivation,
// TestSummarizeBenchToolCalls, TestInsertBenchRun_RoundTrip); this
// file stays focused on the pure-function surface of the deriver.
func TestCostDeriver_Derive(t *testing.T) {
	var (
		sonnet    = "claude-sonnet-4-20250514"
		anthropic = "anthropic"
		m         = "m"
		p         = "p"
	)
	oneM := int64(1_000_000)
	zero := int64(0)

	// sonnetLookup is the hit shape: (provider, model) → (3, 15, true).
	// The closure is shared by the rows that need a hit-on-first-call.
	sonnetLookup := func(_, _ string) (float64, float64, bool) { return 3, 15, true }

	// The cold-miss row needs a LookupFn that returns miss on the first
	// call and hit on the second. A plain int counter in the test
	// scope is the cleanest way to model "the source's Refresh
	// mutates the in-memory cache" without entangling the
	// fakePriceSource struct with stateful closures.
	ac4LookupCalls := 0
	ac4LookupFn := func(_, _ string) (float64, float64, bool) {
		ac4LookupCalls++
		if ac4LookupCalls == 1 {
			return 0, 0, false // pre-refresh: miss
		}
		return 3, 15, true // post-refresh: hit (source's "cache write" side effect)
	}

	// errRefreshFailed is a sentinel for the refresh-failure row: any non-nil
	// error makes the point. The value is declared in the test
	// scope (not as a package var) so the test's failure mode is
	// local to this function.
	errRefreshFailed := errors.New("refresh failed in test")

	cases := []struct {
		name string
		in   CostInputs
		src  *fakePriceSource
		// wantNil says the result must be nil (any non-nil result is a
		// failure). When wantNil is false, wantValue is compared
		// against *Derive(...) within the cost-derivation precision.
		wantNil   bool
		wantValue float64
		// wantLookupCalls / wantRefreshCalls constrain the side-effect
		// counters on the fake source. Defaults are 1 lookup / 0
		// refresh for a hit-on-first-call case.
		wantLookupCalls  int
		wantRefreshCalls int
	}{
		{
			name: "1M+1M tokens on claude-sonnet-4 anthropic, hit on first lookup → 18.000000",
			in: CostInputs{
				TokensIn:  &oneM,
				TokensOut: &oneM,
				Model:     &sonnet,
				Provider:  &anthropic,
			},
			src: &fakePriceSource{
				LookupFn: sonnetLookup,
				// Cold() is irrelevant on a hit; the deriver never
				// reaches the cold-fetch branch. Returning false
				// documents that intent — and the test asserts the
				// refresh counter stays at 0.
				ColdFn: func() bool { return false },
			},
			wantValue:       18.0,
			wantLookupCalls: 1,
		},
		{
			name: "caller-supplied Cost=0.005 wins; no lookup, no refresh",
			in: CostInputs{
				TokensIn:  &oneM,
				TokensOut: &oneM,
				Model:     &sonnet,
				Provider:  &anthropic,
				Cost:      ptrFloat64Cost(0.005),
			},
			src: &fakePriceSource{
				LookupFn: func(_, _ string) (float64, float64, bool) {
					// A Lookup call here would mean the deriver
					// overrode the caller-supplied cost. Fail
					// loudly inside the test runner so the
					// regression is reported with a stack, not
					// just a counter diff.
					t.Fatal("Lookup called; caller-supplied cost must short-circuit Derive")
					return 0, 0, false
				},
				ColdFn: func() bool { return false },
				RefreshFn: func() error {
					t.Fatal("Refresh called; caller-supplied cost must short-circuit Derive")
					return nil
				},
			},
			wantValue:       0.005,
			wantLookupCalls: 0,
		},
		{
			name: "nil TokensIn short-circuits → nil (NULL preservation, no lookup)",
			in: CostInputs{
				TokensIn:  nil,
				TokensOut: &oneM,
				Model:     &sonnet,
				Provider:  &anthropic,
			},
			src: &fakePriceSource{
				LookupFn: func(_, _ string) (float64, float64, bool) {
					t.Fatal("Lookup called; NULL input must short-circuit Derive")
					return 0, 0, false
				},
				ColdFn: func() bool { return false },
			},
			wantNil: true,
		},
		{
			name: "nil TokensOut short-circuits → nil (NULL preservation, no lookup)",
			in: CostInputs{
				TokensIn:  &oneM,
				TokensOut: nil,
				Model:     &sonnet,
				Provider:  &anthropic,
			},
			src: &fakePriceSource{
				LookupFn: func(_, _ string) (float64, float64, bool) {
					t.Fatal("Lookup called; NULL input must short-circuit Derive")
					return 0, 0, false
				},
				ColdFn: func() bool { return false },
			},
			wantNil: true,
		},
		{
			name: "nil Model short-circuits → nil (NULL preservation, no lookup)",
			in: CostInputs{
				TokensIn:  &oneM,
				TokensOut: &oneM,
				Model:     nil,
				Provider:  &anthropic,
			},
			src: &fakePriceSource{
				LookupFn: func(_, _ string) (float64, float64, bool) {
					t.Fatal("Lookup called; NULL input must short-circuit Derive")
					return 0, 0, false
				},
				ColdFn: func() bool { return false },
			},
			wantNil: true,
		},
		{
			name: "nil Provider short-circuits → nil (NULL preservation, no lookup)",
			in: CostInputs{
				TokensIn:  &oneM,
				TokensOut: &oneM,
				Model:     &sonnet,
				Provider:  nil,
			},
			src: &fakePriceSource{
				LookupFn: func(_, _ string) (float64, float64, bool) {
					t.Fatal("Lookup called; NULL input must short-circuit Derive")
					return 0, 0, false
				},
				ColdFn: func() bool { return false },
			},
			wantNil: true,
		},
		{
			name: "cold-cache miss → Refresh → post-refresh hit → 18.000000; cache write is source's responsibility",
			in: CostInputs{
				TokensIn:  &oneM,
				TokensOut: &oneM,
				Model:     &sonnet,
				Provider:  &anthropic,
			},
			src: &fakePriceSource{
				LookupFn: ac4LookupFn,
				ColdFn:   func() bool { return true },
				RefreshFn: func() error {
					// The source's Refresh side effect is
					// captured by ac4LookupFn (the local
					// counter; the post-refresh lookup hits).
					// The deriver's job is to call Refresh and
					// re-Lookup; it does not write the cache
					// itself.
					return nil
				},
			},
			wantValue:        18.0,
			wantLookupCalls:  2,
			wantRefreshCalls: 1,
		},
		{
			name: "warm-cache miss → nil, Refresh NOT called",
			in: CostInputs{
				TokensIn:  &oneM,
				TokensOut: &oneM,
				Model:     &sonnet,
				Provider:  &anthropic,
			},
			src: &fakePriceSource{
				LookupFn: func(_, _ string) (float64, float64, bool) { return 0, 0, false },
				ColdFn:   func() bool { return false },
				RefreshFn: func() error {
					t.Fatal("Refresh called; warm-cache miss must not trigger a fetch")
					return nil
				},
			},
			wantNil:          true,
			wantLookupCalls:  1,
			wantRefreshCalls: 0,
		},
		{
			name: "cold-cache miss + Refresh fails → nil (soft-fail)",
			in: CostInputs{
				TokensIn:  &oneM,
				TokensOut: &oneM,
				Model:     &sonnet,
				Provider:  &anthropic,
			},
			src: &fakePriceSource{
				LookupFn: func(_, _ string) (float64, float64, bool) { return 0, 0, false },
				ColdFn:   func() bool { return true },
				RefreshFn: func() error {
					// The specific error is irrelevant to the
					// soft-fail contract. The deriver must drop
					// it and surface nil.
					return errRefreshFailed
				},
			},
			wantNil:          true,
			wantLookupCalls:  1,
			wantRefreshCalls: 1,
		},
		{
			name: "zero tokens (0 in, 0 out) + hit → 0.000000 (zero is a real value)",
			in: CostInputs{
				TokensIn:  &zero,
				TokensOut: &zero,
				Model:     &m,
				Provider:  &p,
			},
			src: &fakePriceSource{
				LookupFn: func(_, _ string) (float64, float64, bool) { return 3, 15, true },
				ColdFn:   func() bool { return false },
			},
			wantValue:       0.0,
			wantLookupCalls: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := (&costDeriver{}).Derive(tc.in, tc.src)
			if tc.wantNil {
				if got != nil {
					t.Errorf("Derive = %v, want nil", *got)
				}
			} else {
				if got == nil {
					t.Fatal("Derive = nil, want non-nil")
				}
				if !near(*got, tc.wantValue) {
					t.Errorf("Derive = %v, want %v (6-decimal precision)", *got, tc.wantValue)
				}
				// 6-decimal precision check: a derived value
				// must equal its 6-decimal-rounded form (the
				// methodology-v1 headline precision). Matches
				// the precision check in TestPersistToolCall_CostDerivation.
				rounded := math.Round(*got*1e6) / 1e6
				if rounded != *got {
					t.Errorf("Derive = %v is not 6-decimal precise (would round to %v)", *got, rounded)
				}
			}
			if tc.src.lookupCalls != tc.wantLookupCalls {
				t.Errorf("src.Lookup called %d times, want %d", tc.src.lookupCalls, tc.wantLookupCalls)
			}
			if tc.src.refreshCalls != tc.wantRefreshCalls {
				t.Errorf("src.Refresh called %d times, want %d", tc.src.refreshCalls, tc.wantRefreshCalls)
			}
		})
	}
}

func TestEstimateAPICostCacheReasoningAndMissingRates(t *testing.T) {
	cacheReadRate, cacheWriteRate := .25, 3.125
	reasoning, cacheRead, cacheWrite := int64(20), int64(100), int64(10)
	price := Price{Input: 2.5, Output: 15, CacheRead: &cacheReadRate, CacheWrite: &cacheWriteRate}
	got := EstimateAPICost(EstimatedCostInputs{UncachedInput: 1000, Output: 50, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite}, price)
	if got == nil || *got != 0.003606 {
		t.Fatalf("estimate = %v, want 0.003606", got)
	}

	price.CacheRead = nil
	if got := EstimateAPICost(EstimatedCostInputs{UncachedInput: 1000, Output: 50, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite}, price); got != nil {
		t.Fatalf("estimate with missing nonzero cache rate = %v, want nil", *got)
	}
	cacheRead = 0
	if got := EstimateAPICost(EstimatedCostInputs{UncachedInput: 1000, Output: 50, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite}, price); got == nil {
		t.Fatal("zero cache reads should not require a cache-read rate")
	}
}

func TestEstimateAPICostReasoningRateAndOutputFallback(t *testing.T) {
	reasoningRate := 3.0
	zeroReasoningRate := 0.0
	reasoning, cacheRead, cacheWrite := int64(100), int64(0), int64(0)
	inputs := EstimatedCostInputs{UncachedInput: 0, Output: 100, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite}
	base := Price{Input: 2, Output: 10, Reasoning: &reasoningRate, InputSet: true, OutputSet: true}
	if got := EstimateAPICost(inputs, base); got == nil || *got != .0013 {
		t.Fatalf("independent reasoning estimate = %v, want .0013", got)
	}
	base.Reasoning = nil
	if got := EstimateAPICost(inputs, base); got == nil || *got != .002 {
		t.Fatalf("output fallback estimate = %v, want .002", got)
	}
	base.Reasoning = &zeroReasoningRate
	if got := EstimateAPICost(inputs, base); got == nil || *got != .001 {
		t.Fatalf("zero reasoning rate estimate = %v, want .001", got)
	}
	base.Tiers = []PriceTier{{Tier: PriceTierSelector{Type: "context", Size: 0}, Input: 4, Output: 20, Reasoning: &reasoningRate, InputSet: true, OutputSet: true}}
	if got := EstimateAPICost(inputs, base); got == nil || *got != .0023 {
		t.Fatalf("tier reasoning estimate = %v, want .0023", got)
	}
}

func TestEstimateAPIEquivalentCostReportsOrderedAvailabilityAndRateProvenance(t *testing.T) {
	input, output, reasoning, cacheRead, cacheWrite := 100.0, 10.0, 3.0, 5.0, 0.0
	cacheRate := 0.25
	price := Price{Input: 2, Output: 4, CacheRead: &cacheRate, InputSet: true, OutputSet: true}
	estimate := EstimateAPIEquivalentCost(APIEquivalentCostInputs{
		UncachedInput: &input, Output: &output, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite,
	}, price)
	if estimate.Cost == nil || *estimate.Cost != 0.000253 || estimate.RateSelection == nil {
		t.Fatalf("estimate = %#v", estimate)
	}
	selection := estimate.RateSelection
	if selection.Basis != "base" || selection.ContextTokenCount != 105 || selection.ReasoningRatePolicy != "output_rate_fallback" || selection.ReasoningUSDPerMillion != 4 {
		t.Fatalf("rate selection = %#v", selection)
	}

	estimate = EstimateAPIEquivalentCost(APIEquivalentCostInputs{UncachedInput: &input, Output: nil, Reasoning: nil, CacheRead: nil, CacheWrite: &cacheWrite}, price)
	want := []APIEquivalentCostUnavailableReason{
		APIEquivalentCostOutputUnavailable, APIEquivalentCostReasoningUnavailable, APIEquivalentCostCacheReadUnavailable,
	}
	if estimate.Cost != nil || len(estimate.UnavailableReasons) != len(want) {
		t.Fatalf("unavailable estimate = %#v", estimate)
	}
	for index, reason := range want {
		if estimate.UnavailableReasons[index] != reason {
			t.Fatalf("reason[%d] = %q, want %q", index, estimate.UnavailableReasons[index], reason)
		}
	}

	cacheRead = 5
	price.CacheRead = nil
	estimate = EstimateAPIEquivalentCost(APIEquivalentCostInputs{
		UncachedInput: &input, Output: &output, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite,
	}, price)
	if estimate.Cost != nil || len(estimate.UnavailableReasons) != 1 || estimate.UnavailableReasons[0] != APIEquivalentCostCacheReadPriceUnavailable {
		t.Fatalf("missing cache rate estimate = %#v", estimate)
	}
}
