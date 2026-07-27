package reuse

import (
	"math"
	"math/big"
	"testing"
)

func measuredField(value int64) IntegerField {
	return IntegerField{State: FieldMeasured, Value: big.NewInt(value)}
}

func eligibleEstimateInput() EstimatePairInput {
	contract := RuntimeContract{Runtime: RequiredRuntime, RuntimeVersion: RequiredRuntimeVersion, ContractVersion: RequiredContractVersion}
	step := EstimateStep{
		SessionIdentity: TextField{State: FieldMeasured, Value: "session-a"}, RuntimeContract: contract,
		UncachedInput: measuredField(1000), CacheRead: measuredField(500), CacheWrite: measuredField(0),
	}
	return EstimatePairInput{
		PriorExists: true, OrderCoverage: CoverageCovered, Adjacent: true,
		RetryCoverage: "covered_no_boundary", ResetCoverage: "covered_no_boundary",
		SegmentCoverage: CoverageCovered, SegmentBoundary: "none", ConcurrentCoverage: CoverageCovered,
		Prior: step, Current: step,
	}
}

func TestEstimatePairSelectsFirstReasonInContractOrder(t *testing.T) {
	tests := []struct {
		name, want string
		mutate     func(*EstimatePairInput)
	}{
		{"first step", "first_step", func(v *EstimatePairInput) { v.PriorExists = false; v.OrderCoverage = CoverageUnavailable }},
		{"nonconsecutive before retry", "non_consecutive_order", func(v *EstimatePairInput) { v.Adjacent = false; v.RetryCoverage = "unavailable" }},
		{"order unavailable", "order_coverage_unavailable", func(v *EstimatePairInput) { v.OrderCoverage = CoverageUnavailable }},
		{"order contradictory", "order_coverage_contradictory", func(v *EstimatePairInput) { v.OrderCoverage = CoverageContradictory }},
		{"retry unavailable", "retry_coverage_unavailable", func(v *EstimatePairInput) { v.RetryCoverage = "unavailable" }},
		{"retry boundary", "retry_boundary_observed", func(v *EstimatePairInput) { v.RetryCoverage = "boundary_observed" }},
		{"reset unavailable", "reset_coverage_unavailable", func(v *EstimatePairInput) { v.ResetCoverage = "unavailable" }},
		{"reset boundary", "reset_or_compaction_boundary_observed", func(v *EstimatePairInput) { v.ResetCoverage = "boundary_observed" }},
		{"segment unavailable", "segment_coverage_unavailable", func(v *EstimatePairInput) { v.SegmentCoverage = CoverageUnavailable }},
		{"segment contradictory", "segment_coverage_contradictory", func(v *EstimatePairInput) { v.SegmentCoverage = CoverageContradictory }},
		{"interruption", "interruption_boundary_observed", func(v *EstimatePairInput) { v.SegmentBoundary = "interruption" }},
		{"restart", "restart_boundary_observed", func(v *EstimatePairInput) { v.SegmentBoundary = "restart" }},
		{"concurrency unavailable", "concurrent_coverage_unavailable", func(v *EstimatePairInput) { v.ConcurrentCoverage = CoverageUnavailable }},
		{"concurrency contradictory", "concurrent_coverage_contradictory", func(v *EstimatePairInput) { v.ConcurrentCoverage = CoverageContradictory }},
		{"interleaving", "concurrent_interleaving", func(v *EstimatePairInput) { v.ConcurrentInterleaving = true }},
		{"identity unavailable", "runtime_session_identity_unavailable", func(v *EstimatePairInput) { v.Current.SessionIdentity = TextField{State: FieldUnknown} }},
		{"identity changed", "runtime_session_identity_changed", func(v *EstimatePairInput) { v.Current.SessionIdentity.Value = "session-b" }},
		{"prior runtime contract", "runtime_contract_mismatch", func(v *EstimatePairInput) { v.Prior.RuntimeContract.RuntimeVersion = "1.18.4" }},
		{"current runtime contract", "runtime_contract_mismatch", func(v *EstimatePairInput) { v.Current.RuntimeContract.RuntimeVersion = "1.18.4" }},
		{"prior activity absent before uncached unknown", "prior_cache_activity_absent", func(v *EstimatePairInput) {
			v.Prior.CacheRead = measuredField(0)
			v.Prior.CacheWrite = measuredField(0)
			v.Prior.UncachedInput = IntegerField{State: FieldUnknown}
		}},
		{"prior uncached unknown", "prior_uncached_input_unknown", func(v *EstimatePairInput) { v.Prior.UncachedInput = IntegerField{State: FieldUnknown} }},
		{"prior uncached malformed", "prior_uncached_input_malformed", func(v *EstimatePairInput) { v.Prior.UncachedInput = IntegerField{State: FieldMalformed} }},
		{"prior read unknown", "prior_cache_read_unknown", func(v *EstimatePairInput) { v.Prior.CacheRead = IntegerField{State: FieldUnknown} }},
		{"prior read malformed", "prior_cache_read_malformed", func(v *EstimatePairInput) { v.Prior.CacheRead = IntegerField{State: FieldMalformed} }},
		{"prior write unknown", "prior_cache_write_unknown", func(v *EstimatePairInput) { v.Prior.CacheWrite = IntegerField{State: FieldUnknown} }},
		{"prior write malformed", "prior_cache_write_malformed", func(v *EstimatePairInput) { v.Prior.CacheWrite = IntegerField{State: FieldMalformed} }},
		{"current uncached unknown", "current_uncached_input_unknown", func(v *EstimatePairInput) { v.Current.UncachedInput = IntegerField{State: FieldUnknown} }},
		{"current uncached malformed", "current_uncached_input_malformed", func(v *EstimatePairInput) { v.Current.UncachedInput = IntegerField{State: FieldMalformed} }},
		{"current read unknown", "current_cache_read_unknown", func(v *EstimatePairInput) { v.Current.CacheRead = IntegerField{State: FieldUnknown} }},
		{"current read malformed", "current_cache_read_malformed", func(v *EstimatePairInput) { v.Current.CacheRead = IntegerField{State: FieldMalformed} }},
		{"current write unknown", "current_cache_write_unknown", func(v *EstimatePairInput) { v.Current.CacheWrite = IntegerField{State: FieldUnknown} }},
		{"current write malformed", "current_cache_write_malformed", func(v *EstimatePairInput) { v.Current.CacheWrite = IntegerField{State: FieldMalformed} }},
		{"current logical input nonpositive", "current_logical_input_not_positive", func(v *EstimatePairInput) {
			v.Current.UncachedInput = measuredField(0)
			v.Current.CacheRead = measuredField(0)
			v.Current.CacheWrite = measuredField(0)
		}},
		{"read exceeds basis", "current_cache_read_exceeds_estimated_reusable_tokens", func(v *EstimatePairInput) {
			v.Prior.UncachedInput = measuredField(1)
			v.Prior.CacheRead = measuredField(1)
			v.Current.UncachedInput = measuredField(0)
			v.Current.CacheRead = measuredField(3)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := eligibleEstimateInput()
			test.mutate(&input)
			got := EstimatePair(input)
			if got.Eligible || got.Reason != test.want {
				t.Fatalf("result = %#v, want reason %q", got, test.want)
			}
		})
	}
}

func TestEstimatePairRetainsExactBasisShortfallAndNoiseInterpretation(t *testing.T) {
	for _, test := range []struct {
		name                       string
		basis, read, wantShortfall int64
		wantInterpretation         string
	}{
		{"zero", 2048, 2048, 0, "within_noise_floor"},
		{"noise boundary", 2048, 1024, 1024, "within_noise_floor"},
		{"above noise", 2048, 1023, 1025, "above_noise_floor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := eligibleEstimateInput()
			input.Prior.UncachedInput, input.Prior.CacheRead = measuredField(test.basis-1), measuredField(1)
			input.Current.UncachedInput, input.Current.CacheRead = measuredField(test.basis-test.read), measuredField(test.read)
			result := EstimatePair(input)
			if !result.Eligible || result.EstimatedReusableTokens.Cmp(big.NewInt(test.basis)) != 0 || result.Shortfall.Cmp(big.NewInt(test.wantShortfall)) != 0 || result.Interpretation != test.wantInterpretation {
				t.Fatalf("result = %#v", result)
			}
			want := new(big.Rat).SetFrac(big.NewInt(test.read), big.NewInt(test.basis))
			if result.ReuseShare.Cmp(want) != 0 {
				t.Fatalf("share = %s, want %s", result.ReuseShare, want)
			}
		})
	}
}

func TestEstimatePairLogicalInputBeyondInt64IsExact(t *testing.T) {
	input := eligibleEstimateInput()
	for _, step := range []*EstimateStep{&input.Prior, &input.Current} {
		step.UncachedInput = measuredField(math.MaxInt64)
		step.CacheRead = measuredField(math.MaxInt64)
		step.CacheWrite = measuredField(math.MaxInt64)
	}
	result := EstimatePair(input)
	want := new(big.Int).Mul(big.NewInt(3), big.NewInt(math.MaxInt64))
	if !result.Eligible || result.EstimatedReusableTokens.Cmp(want) != 0 || result.Shortfall.Cmp(new(big.Int).Sub(want, big.NewInt(math.MaxInt64))) != 0 || result.ReuseShare.RatString() != "1/3" {
		t.Fatalf("result = %#v, want basis %s", result, want)
	}
}
