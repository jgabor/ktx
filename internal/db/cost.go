// Cost derivation centralizes price lookup, soft-failure behavior, null
// preservation, and rounding for every persisted record type.
package db

import "math"

// CostRoundDecimals is the precision for cost derivation. The
// methodology-v1 headline number is six decimals
// (`tools/bench/methodology-v1.yaml`'s `cost_per_correct = cost /
// max(score, 1e-9)` plus the reproducibility `six decimal places` rule); the
// persisted cost uses the same precision as the benchmark headline.
//
// Both write-time callers use the same persisted precision.
const CostRoundDecimals = 6

type EstimatedCostResult struct {
	Value               *float64
	AvailabilityReasons []string
}

// PriceSource abstracts the cache lookup and the cold-fetch for
// the deriver. The *Prices type in prices.go satisfies this
// interface verbatim:
//
//	(p *Prices) Lookup(provider, model string) (in, out float64, ok bool)
//	(p *Prices) Cold() bool
//	(p *Prices) Refresh() error
//
// The source owns persistence; Derive calls Refresh and then retries lookup.
type PriceSource interface {
	Lookup(provider, model string) (in, out float64, ok bool)
	Cold() bool
	Refresh() error
}

// CostInputs is the cross-shape input the two write-time callers
// build before delegating to Derive. The pointer types preserve the
// absent-vs-zero distinction: a nil pointer means "field absent"
// (Derive returns nil), a non-nil pointer to zero means "field set
// to zero" (Derive uses 0 as a real value).
type CostInputs struct {
	TokensIn, TokensOut *int64
	Model, Provider     *string
	Cost                *float64
}

type EstimatedCostInputs struct {
	UncachedInput int64
	Output        int64
	Reasoning     *int64
	CacheRead     *int64
	CacheWrite    *int64
}

// EstimateAPICost derives an API-equivalent estimate independently of reported
// cost. Complete reasoning and cache counts are required; rates are selected
// from supported context tiers using all prompt-side tokens.
func EstimateAPICost(in EstimatedCostInputs, price Price) *float64 {
	return EstimateAPICostDetails(in, price).Value
}

// EstimateAPICostDetails derives an API-equivalent estimate and retains every
// deterministic reason it could not do so. Zero token counts and zero prices
// are valid measured inputs.
func EstimateAPICostDetails(in EstimatedCostInputs, price Price) EstimatedCostResult {
	reasons := []string{}
	if in.UncachedInput < 0 || in.Output < 0 {
		reasons = append(reasons, "token_count_invalid")
	}
	if in.Reasoning == nil {
		reasons = append(reasons, "reasoning_unavailable")
	} else if *in.Reasoning < 0 {
		reasons = append(reasons, "reasoning_invalid")
	}
	if in.CacheRead == nil {
		reasons = append(reasons, "cache_read_unavailable")
	} else if *in.CacheRead < 0 {
		reasons = append(reasons, "cache_read_invalid")
	}
	if in.CacheWrite == nil {
		reasons = append(reasons, "cache_write_unavailable")
	} else if *in.CacheWrite < 0 {
		reasons = append(reasons, "cache_write_invalid")
	}
	if len(reasons) > 0 {
		return EstimatedCostResult{AvailabilityReasons: reasons}
	}
	contextTokens := in.UncachedInput + *in.CacheRead + *in.CacheWrite
	rates := price.SelectRates(contextTokens)
	if !rates.hasOrdinaryPrices() {
		return EstimatedCostResult{AvailabilityReasons: []string{"pricing_input_or_output_unavailable"}}
	}
	if !validEstimatedCostRate(rates.Input) || !validEstimatedCostRate(rates.Output) {
		return EstimatedCostResult{AvailabilityReasons: []string{estimatedCostRateReason("pricing_input_or_output", rates.Input, rates.Output)}}
	}
	if *in.CacheRead != 0 {
		if rates.CacheRead == nil {
			return EstimatedCostResult{AvailabilityReasons: []string{"pricing_cache_read_unavailable"}}
		}
		if !validEstimatedCostRate(*rates.CacheRead) {
			return EstimatedCostResult{AvailabilityReasons: []string{estimatedCostRateReason("pricing_cache_read", *rates.CacheRead)}}
		}
	}
	if *in.CacheWrite != 0 {
		if rates.CacheWrite == nil {
			return EstimatedCostResult{AvailabilityReasons: []string{"pricing_cache_write_unavailable"}}
		}
		if !validEstimatedCostRate(*rates.CacheWrite) {
			return EstimatedCostResult{AvailabilityReasons: []string{estimatedCostRateReason("pricing_cache_write", *rates.CacheWrite)}}
		}
	}
	cacheReadPrice, cacheWritePrice := 0.0, 0.0
	if *in.CacheRead != 0 && rates.CacheRead != nil {
		cacheReadPrice = *rates.CacheRead
	}
	if *in.CacheWrite != 0 && rates.CacheWrite != nil {
		cacheWritePrice = *rates.CacheWrite
	}
	reasoningPrice := rates.Output
	if rates.Reasoning != nil {
		if !validEstimatedCostRate(*rates.Reasoning) {
			return EstimatedCostResult{AvailabilityReasons: []string{estimatedCostRateReason("pricing_reasoning", *rates.Reasoning)}}
		}
		reasoningPrice = *rates.Reasoning
	}
	raw := float64(in.UncachedInput)*rates.Input + float64(*in.CacheRead)*cacheReadPrice +
		float64(*in.CacheWrite)*cacheWritePrice + float64(in.Output)*rates.Output + float64(*in.Reasoning)*reasoningPrice
	value := roundCost(raw / 1_000_000)
	if value == nil {
		return EstimatedCostResult{AvailabilityReasons: []string{"estimated_api_cost_nonfinite"}}
	}
	return EstimatedCostResult{Value: value}
}

func validEstimatedCostRate(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func estimatedCostRateReason(prefix string, values ...float64) string {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return prefix + "_nonfinite"
		}
	}
	return prefix + "_invalid"
}

func roundCost(raw float64) *float64 {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return nil
	}
	rounded := math.Round(raw*math.Pow(10, CostRoundDecimals)) / math.Pow(10, CostRoundDecimals)
	if math.IsNaN(rounded) || math.IsInf(rounded, 0) {
		return nil
	}
	return &rounded
}

// RoundCost applies the shared API-equivalent cost precision.
func RoundCost(raw float64) *float64 {
	return roundCost(raw)
}

// costDeriver is the deep cost module. The instance is stateless
// after construction; the type exists to give Derive a home and
// to make the lookup-and-cold-fetch sequence one symbol. One
// instance per process is enough; the literal `&costDeriver{}` is
// idiomatic. The type is unexported because no caller outside the
// package needs to construct one — they reach the math through
// the wrapper methods on *DB (deriveCost, deriveBenchCost).
type costDeriver struct{}

// Derive returns the cost in USD rounded to 6 decimals, or nil on
// any soft-fail branch. The return value is a *float64 so the
// caller can assign it to the same field on the input struct
// without copying (ToolCall.Cost, BenchRun.Cost).
//
// Contract:
//
//   - Caller-supplied Cost wins: a non-nil pointer (including &0)
//     round-trips verbatim. No lookup, no refresh, no math. The
//     caller owns the cost evidence.
//
//   - Any NULL input (any of TokensIn / TokensOut / Model /
//     Provider nil) → return nil. NULL preservation: a
//     partial set would force the deriver to invent the missing
//     value, which violates the conservative-failure quality
//     attribute.
//
//   - Otherwise: src.Lookup(provider, model) → if ok → formula.
//
//   - Lookup miss + Cold() → src.Refresh() → retry Lookup → formula.
//
//   - Lookup miss + !Cold() → return nil (warm-cache miss is silent;
//     the on-disk cache is the source of truth between fetches).
//
//   - Lookup miss + Cold + Refresh fails → return nil (soft-fail;
//     no error propagates from Derive).
//
//   - Formula:
//
//     cost_USD = (tokens_in * in_price + tokens_out * out_price) / 1_000_000
//
//     rounded to CostRoundDecimals (6) decimal places. in_price and
//     out_price are USD per million tokens, the units models.dev
//     uses for its cost.input and cost.output fields; the division
//     by 1_000_000 converts to "USD per token" multiplied by token
//     count.
func (cd *costDeriver) Derive(in CostInputs, src PriceSource) *float64 {
	// Caller-supplied cost wins. A non-nil pointer (including a
	// pointer to zero) round-trips verbatim — derivation is about
	// filling in what the caller omitted, not overwriting what
	// they supplied.
	if in.Cost != nil {
		return in.Cost
	}
	// NULL preservation: all four derivation inputs must be
	// non-nil. Any nil field short-circuits derivation; the
	// caller persists the row with NULL cost.
	if in.TokensIn == nil || in.TokensOut == nil ||
		in.Model == nil || in.Provider == nil {
		return nil
	}

	inPrice, outPrice, ok := src.Lookup(*in.Provider, *in.Model)
	// Cold-cache lookup miss triggers a fetch.
	// Warm-cache lookup miss is silent: the on-disk
	// cache is the source of truth between fetches; the user
	// must run `ktx prices update` to refresh.
	if !ok && src.Cold() {
		// Refresh is bounded by the source (Prices caps it at
		// 5s); the soft-fail contract drops a non-nil error
		// and leaves cost nil.
		if err := src.Refresh(); err == nil {
			inPrice, outPrice, ok = src.Lookup(*in.Provider, *in.Model)
		}
	}
	if !ok {
		return nil
	}

	raw := float64(*in.TokensIn)*inPrice + float64(*in.TokensOut)*outPrice
	raw = raw / 1_000_000
	return roundCost(raw)
}
