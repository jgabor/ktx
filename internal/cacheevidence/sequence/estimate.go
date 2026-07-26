package sequence

import "math/big"

const (
	RequiredRuntime         = "opencode"
	RequiredRuntimeVersion  = "1.18.4"
	RequiredContractVersion = "cache-reuse-v1"
)

type FieldState string

const (
	FieldMeasured  FieldState = "measured"
	FieldUnknown   FieldState = "unknown"
	FieldMalformed FieldState = "malformed"
)

type IntegerField struct {
	State FieldState
	Value *big.Int
}

type TextField struct {
	State FieldState
	Value string
}

type RuntimeContract struct {
	Runtime, RuntimeVersion, ContractVersion string
}

type EstimateStep struct {
	SessionIdentity TextField
	RuntimeContract RuntimeContract
	UncachedInput   IntegerField
	CacheRead       IntegerField
	CacheWrite      IntegerField
}

type EstimatePairInput struct {
	PriorExists            bool
	OrderCoverage          CoverageState
	Adjacent               bool
	RetryCoverage          string
	ResetCoverage          string
	SegmentCoverage        CoverageState
	SegmentBoundary        string
	ConcurrentCoverage     CoverageState
	ConcurrentInterleaving bool
	Prior, Current         EstimateStep
}

type EstimatePairResult struct {
	Eligible                bool
	Reason                  string
	EstimatedReusableTokens *big.Int
	Shortfall               *big.Int
	ReuseShare              *big.Rat
	Interpretation          string
}

var requiredRuntimeContract = RuntimeContract{RequiredRuntime, RequiredRuntimeVersion, RequiredContractVersion}

var IneligibilityReasons = []string{
	"first_step", "non_consecutive_order", "order_coverage_unavailable", "order_coverage_contradictory",
	"retry_coverage_unavailable", "retry_boundary_observed", "reset_coverage_unavailable", "reset_or_compaction_boundary_observed",
	"segment_coverage_unavailable", "segment_coverage_contradictory", "interruption_boundary_observed", "restart_boundary_observed",
	"concurrent_coverage_unavailable", "concurrent_coverage_contradictory", "concurrent_interleaving",
	"runtime_session_identity_unavailable", "runtime_session_identity_changed", "runtime_contract_mismatch", "prior_cache_activity_absent",
	"prior_uncached_input_unknown", "prior_uncached_input_malformed", "prior_cache_read_unknown", "prior_cache_read_malformed",
	"prior_cache_write_unknown", "prior_cache_write_malformed", "current_uncached_input_unknown", "current_uncached_input_malformed",
	"current_cache_read_unknown", "current_cache_read_malformed", "current_cache_write_unknown", "current_cache_write_malformed",
	"prior_logical_input_not_positive", "current_logical_input_not_positive", "estimated_reusable_tokens_not_positive",
	"current_cache_read_exceeds_estimated_reusable_tokens",
}

// EstimatePair applies the contract's ineligibility rules in their declared
// order, then computes the estimate with exact integer and rational arithmetic.
func EstimatePair(input EstimatePairInput) EstimatePairResult {
	if reason := estimateIneligibilityReason(input); reason != "" {
		return EstimatePairResult{Reason: reason}
	}
	priorLogical := logicalInput(input.Prior)
	currentLogical := logicalInput(input.Current)
	basis := new(big.Int).Set(priorLogical)
	if currentLogical.Cmp(basis) < 0 {
		basis.Set(currentLogical)
	}
	shortfall := new(big.Int).Sub(new(big.Int).Set(basis), input.Current.CacheRead.Value)
	interpretation := "within_noise_floor"
	if shortfall.Cmp(big.NewInt(1024)) > 0 {
		interpretation = "above_noise_floor"
	}
	return EstimatePairResult{
		Eligible: true, EstimatedReusableTokens: basis, Shortfall: shortfall,
		ReuseShare: new(big.Rat).SetFrac(input.Current.CacheRead.Value, basis), Interpretation: interpretation,
	}
}

func estimateIneligibilityReason(input EstimatePairInput) string {
	switch {
	case !input.PriorExists:
		return "first_step"
	case input.OrderCoverage == CoverageCovered && !input.Adjacent:
		return "non_consecutive_order"
	case input.OrderCoverage == CoverageUnavailable:
		return "order_coverage_unavailable"
	case input.OrderCoverage == CoverageContradictory:
		return "order_coverage_contradictory"
	case input.RetryCoverage == "unavailable":
		return "retry_coverage_unavailable"
	case input.RetryCoverage == "boundary_observed":
		return "retry_boundary_observed"
	case input.ResetCoverage == "unavailable":
		return "reset_coverage_unavailable"
	case input.ResetCoverage == "boundary_observed":
		return "reset_or_compaction_boundary_observed"
	case input.SegmentCoverage == CoverageUnavailable:
		return "segment_coverage_unavailable"
	case input.SegmentCoverage == CoverageContradictory:
		return "segment_coverage_contradictory"
	case input.SegmentCoverage == CoverageCovered && input.SegmentBoundary == "interruption":
		return "interruption_boundary_observed"
	case input.SegmentCoverage == CoverageCovered && input.SegmentBoundary == "restart":
		return "restart_boundary_observed"
	case input.ConcurrentCoverage == CoverageUnavailable:
		return "concurrent_coverage_unavailable"
	case input.ConcurrentCoverage == CoverageContradictory:
		return "concurrent_coverage_contradictory"
	case input.ConcurrentCoverage == CoverageCovered && input.ConcurrentInterleaving:
		return "concurrent_interleaving"
	case normalizedTextState(input.Prior.SessionIdentity) != FieldMeasured || normalizedTextState(input.Current.SessionIdentity) != FieldMeasured:
		return "runtime_session_identity_unavailable"
	case input.Prior.SessionIdentity.Value != input.Current.SessionIdentity.Value:
		return "runtime_session_identity_changed"
	case input.Prior.RuntimeContract != requiredRuntimeContract || input.Current.RuntimeContract != requiredRuntimeContract:
		return "runtime_contract_mismatch"
	case measured(input.Prior.CacheRead) && measured(input.Prior.CacheWrite) && new(big.Int).Add(input.Prior.CacheRead.Value, input.Prior.CacheWrite.Value).Sign() == 0:
		return "prior_cache_activity_absent"
	}
	for _, field := range []struct {
		name  string
		value IntegerField
	}{
		{"prior_uncached_input", input.Prior.UncachedInput},
		{"prior_cache_read", input.Prior.CacheRead},
		{"prior_cache_write", input.Prior.CacheWrite},
		{"current_uncached_input", input.Current.UncachedInput},
		{"current_cache_read", input.Current.CacheRead},
		{"current_cache_write", input.Current.CacheWrite},
	} {
		switch normalizedIntegerState(field.value) {
		case FieldUnknown:
			return field.name + "_unknown"
		case FieldMalformed:
			return field.name + "_malformed"
		}
	}
	priorLogical := logicalInput(input.Prior)
	currentLogical := logicalInput(input.Current)
	switch {
	case priorLogical.Sign() <= 0:
		return "prior_logical_input_not_positive"
	case currentLogical.Sign() <= 0:
		return "current_logical_input_not_positive"
	}
	basis := priorLogical
	if currentLogical.Cmp(basis) < 0 {
		basis = currentLogical
	}
	switch {
	case basis.Sign() <= 0:
		return "estimated_reusable_tokens_not_positive"
	case input.Current.CacheRead.Value.Cmp(basis) > 0:
		return "current_cache_read_exceeds_estimated_reusable_tokens"
	}
	return ""
}

func measured(field IntegerField) bool {
	return normalizedIntegerState(field) == FieldMeasured
}

func normalizedIntegerState(field IntegerField) FieldState {
	if field.State == FieldMeasured && (field.Value == nil || field.Value.Sign() < 0) {
		return FieldMalformed
	}
	return field.State
}

func normalizedTextState(field TextField) FieldState {
	if field.State == FieldMeasured && field.Value == "" {
		return FieldMalformed
	}
	return field.State
}

func logicalInput(step EstimateStep) *big.Int {
	result := new(big.Int).Add(step.UncachedInput.Value, step.CacheRead.Value)
	return result.Add(result, step.CacheWrite.Value)
}
