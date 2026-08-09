// Cost derivation centralizes price lookup, soft-failure behavior, null
// preservation, and rounding for every persisted record type.
package db

import (
	"math"
	"math/big"
)

// costRoundDecimals is the precision for the cost derivation. The
// methodology-v1 headline number is six decimals
// (`tools/bench/methodology-v1.yaml`'s `cost_per_correct = cost /
// max(score, 1e-9)` plus the reproducibility `six decimal places` rule); the
// persisted cost uses the same precision as the benchmark headline.
//
// Both write-time callers use the same persisted precision.
const (
	// APIEquivalentCostRoundingDecimals is the persisted precision for one
	// API-equivalent estimate in USD.
	APIEquivalentCostRoundingDecimals = 6
	// APIEquivalentCostRoundingMode describes math.Round for the non-negative
	// cost values accepted by the estimator.
	APIEquivalentCostRoundingMode = "half_away_from_zero"

	costRoundDecimals = APIEquivalentCostRoundingDecimals
)

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

// APIEquivalentCostInputs preserves the completeness and numeric state of all
// five priced token buckets. It intentionally has no reported-cost field.
type APIEquivalentCostInputs struct {
	UncachedInput *float64
	Output        *float64
	Reasoning     *float64
	CacheRead     *float64
	CacheWrite    *float64
}

// APIEquivalentCostUnavailableReason identifies why an API-equivalent
// estimate is unavailable. The result keeps these reasons in a stable order.
type APIEquivalentCostUnavailableReason string

const (
	APIEquivalentCostUncachedInputUnavailable   APIEquivalentCostUnavailableReason = "uncached_input_unavailable"
	APIEquivalentCostOutputUnavailable          APIEquivalentCostUnavailableReason = "output_unavailable"
	APIEquivalentCostReasoningUnavailable       APIEquivalentCostUnavailableReason = "reasoning_unavailable"
	APIEquivalentCostCacheReadUnavailable       APIEquivalentCostUnavailableReason = "cache_read_unavailable"
	APIEquivalentCostCacheWriteUnavailable      APIEquivalentCostUnavailableReason = "cache_write_unavailable"
	APIEquivalentCostUncachedInputNonfinite     APIEquivalentCostUnavailableReason = "uncached_input_nonfinite"
	APIEquivalentCostOutputNonfinite            APIEquivalentCostUnavailableReason = "output_nonfinite"
	APIEquivalentCostReasoningNonfinite         APIEquivalentCostUnavailableReason = "reasoning_nonfinite"
	APIEquivalentCostCacheReadNonfinite         APIEquivalentCostUnavailableReason = "cache_read_nonfinite"
	APIEquivalentCostCacheWriteNonfinite        APIEquivalentCostUnavailableReason = "cache_write_nonfinite"
	APIEquivalentCostUncachedInputInvalid       APIEquivalentCostUnavailableReason = "uncached_input_invalid"
	APIEquivalentCostOutputInvalid              APIEquivalentCostUnavailableReason = "output_invalid"
	APIEquivalentCostReasoningInvalid           APIEquivalentCostUnavailableReason = "reasoning_invalid"
	APIEquivalentCostCacheReadInvalid           APIEquivalentCostUnavailableReason = "cache_read_invalid"
	APIEquivalentCostCacheWriteInvalid          APIEquivalentCostUnavailableReason = "cache_write_invalid"
	APIEquivalentCostContextTokenOverflow       APIEquivalentCostUnavailableReason = "context_token_count_overflow"
	APIEquivalentCostInputPriceUnavailable      APIEquivalentCostUnavailableReason = "input_price_unavailable"
	APIEquivalentCostOutputPriceUnavailable     APIEquivalentCostUnavailableReason = "output_price_unavailable"
	APIEquivalentCostCacheReadPriceUnavailable  APIEquivalentCostUnavailableReason = "cache_read_price_unavailable"
	APIEquivalentCostCacheWritePriceUnavailable APIEquivalentCostUnavailableReason = "cache_write_price_unavailable"
	APIEquivalentCostInputPriceInvalid          APIEquivalentCostUnavailableReason = "input_price_invalid"
	APIEquivalentCostOutputPriceInvalid         APIEquivalentCostUnavailableReason = "output_price_invalid"
	APIEquivalentCostCacheReadPriceInvalid      APIEquivalentCostUnavailableReason = "cache_read_price_invalid"
	APIEquivalentCostCacheWritePriceInvalid     APIEquivalentCostUnavailableReason = "cache_write_price_invalid"
	APIEquivalentCostReasoningPriceInvalid      APIEquivalentCostUnavailableReason = "reasoning_price_invalid"
	APIEquivalentCostNonfinite                  APIEquivalentCostUnavailableReason = "estimated_cost_nonfinite"
)

var apiEquivalentCostReasonPrecedence = []APIEquivalentCostUnavailableReason{
	APIEquivalentCostUncachedInputUnavailable, APIEquivalentCostOutputUnavailable,
	APIEquivalentCostReasoningUnavailable, APIEquivalentCostCacheReadUnavailable, APIEquivalentCostCacheWriteUnavailable,
	APIEquivalentCostUncachedInputNonfinite, APIEquivalentCostOutputNonfinite,
	APIEquivalentCostReasoningNonfinite, APIEquivalentCostCacheReadNonfinite, APIEquivalentCostCacheWriteNonfinite,
	APIEquivalentCostUncachedInputInvalid, APIEquivalentCostOutputInvalid,
	APIEquivalentCostReasoningInvalid, APIEquivalentCostCacheReadInvalid, APIEquivalentCostCacheWriteInvalid,
	APIEquivalentCostContextTokenOverflow,
	APIEquivalentCostInputPriceUnavailable, APIEquivalentCostOutputPriceUnavailable,
	APIEquivalentCostCacheReadPriceUnavailable, APIEquivalentCostCacheWritePriceUnavailable,
	APIEquivalentCostInputPriceInvalid, APIEquivalentCostOutputPriceInvalid,
	APIEquivalentCostCacheReadPriceInvalid, APIEquivalentCostCacheWritePriceInvalid,
	APIEquivalentCostReasoningPriceInvalid, APIEquivalentCostNonfinite,
}

// APIEquivalentCostRateSelection identifies the exact rates used for an
// estimate. ReasoningRatePolicy is explicit when reasoning uses output pricing.
type APIEquivalentCostRateSelection struct {
	Basis                   string
	ContextTokenCount       int64
	ContextTierSize         *int64
	InputUSDPerMillion      float64
	OutputUSDPerMillion     float64
	CacheReadUSDPerMillion  *float64
	CacheWriteUSDPerMillion *float64
	ReasoningUSDPerMillion  float64
	ReasoningRatePolicy     string
}

// APIEquivalentCostEstimate is independent of provider-reported cost. Cost and
// MicroUSD describe the same rounded result. A nil Cost always has at least one
// ordered unavailable reason.
type APIEquivalentCostEstimate struct {
	Cost               *float64
	MicroUSD           *big.Int
	UnavailableReasons []APIEquivalentCostUnavailableReason
	RateSelection      *APIEquivalentCostRateSelection
}

// EstimateAPIEquivalentCost derives a price-based estimate independently of
// provider-reported cost. It requires complete non-negative integer evidence
// for uncached input, output, reasoning, cache read, and cache write.
func EstimateAPIEquivalentCost(in APIEquivalentCostInputs, price Price) APIEquivalentCostEstimate {
	uncachedInput, reasons := apiEquivalentToken(in.UncachedInput, APIEquivalentCostUncachedInputUnavailable, APIEquivalentCostUncachedInputNonfinite, APIEquivalentCostUncachedInputInvalid)
	output, outputReasons := apiEquivalentToken(in.Output, APIEquivalentCostOutputUnavailable, APIEquivalentCostOutputNonfinite, APIEquivalentCostOutputInvalid)
	reasoning, reasoningReasons := apiEquivalentToken(in.Reasoning, APIEquivalentCostReasoningUnavailable, APIEquivalentCostReasoningNonfinite, APIEquivalentCostReasoningInvalid)
	cacheRead, cacheReadReasons := apiEquivalentToken(in.CacheRead, APIEquivalentCostCacheReadUnavailable, APIEquivalentCostCacheReadNonfinite, APIEquivalentCostCacheReadInvalid)
	cacheWrite, cacheWriteReasons := apiEquivalentToken(in.CacheWrite, APIEquivalentCostCacheWriteUnavailable, APIEquivalentCostCacheWriteNonfinite, APIEquivalentCostCacheWriteInvalid)
	reasons = append(reasons, outputReasons...)
	reasons = append(reasons, reasoningReasons...)
	reasons = append(reasons, cacheReadReasons...)
	reasons = append(reasons, cacheWriteReasons...)
	if len(reasons) > 0 {
		return APIEquivalentCostEstimate{UnavailableReasons: orderAPIEquivalentCostReasons(reasons)}
	}
	if uncachedInput > math.MaxInt64-cacheRead || uncachedInput+cacheRead > math.MaxInt64-cacheWrite {
		return APIEquivalentCostEstimate{UnavailableReasons: []APIEquivalentCostUnavailableReason{APIEquivalentCostContextTokenOverflow}}
	}
	selection := price.SelectRatesWithProvenance(uncachedInput + cacheRead + cacheWrite)
	rates := selection.Rates
	reasons = apiEquivalentRateReasons(rates, cacheRead, cacheWrite)
	if len(reasons) > 0 {
		return APIEquivalentCostEstimate{UnavailableReasons: orderAPIEquivalentCostReasons(reasons)}
	}
	cacheReadPrice, cacheWritePrice := 0.0, 0.0
	if rates.CacheRead != nil {
		cacheReadPrice = *rates.CacheRead
	}
	if rates.CacheWrite != nil {
		cacheWritePrice = *rates.CacheWrite
	}
	reasoningPrice, reasoningPolicy := rates.Output, "output_rate_fallback"
	if rates.Reasoning != nil {
		reasoningPrice, reasoningPolicy = *rates.Reasoning, "selected_reasoning_rate"
	}
	if !validAPIEquivalentRate(reasoningPrice) {
		return APIEquivalentCostEstimate{UnavailableReasons: []APIEquivalentCostUnavailableReason{APIEquivalentCostReasoningPriceInvalid}}
	}
	raw := float64(uncachedInput)*rates.Input + float64(cacheRead)*cacheReadPrice +
		float64(cacheWrite)*cacheWritePrice + float64(output)*rates.Output + float64(reasoning)*reasoningPrice
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw < 0 {
		return APIEquivalentCostEstimate{UnavailableReasons: []APIEquivalentCostUnavailableReason{APIEquivalentCostNonfinite}}
	}
	cost := roundCost(raw / 1_000_000)
	if cost == nil || math.IsNaN(*cost) || math.IsInf(*cost, 0) || *cost < 0 {
		return APIEquivalentCostEstimate{UnavailableReasons: []APIEquivalentCostUnavailableReason{APIEquivalentCostNonfinite}}
	}
	microUSD, ok := APIEquivalentCostMicroUSD(*cost)
	if !ok {
		return APIEquivalentCostEstimate{UnavailableReasons: []APIEquivalentCostUnavailableReason{APIEquivalentCostNonfinite}}
	}
	return APIEquivalentCostEstimate{Cost: cost, RateSelection: &APIEquivalentCostRateSelection{
		Basis: selection.Basis, ContextTokenCount: selection.ContextTokenCount, ContextTierSize: selection.ContextTierSize,
		InputUSDPerMillion: rates.Input, OutputUSDPerMillion: rates.Output,
		CacheReadUSDPerMillion: rates.CacheRead, CacheWriteUSDPerMillion: rates.CacheWrite,
		ReasoningUSDPerMillion: reasoningPrice, ReasoningRatePolicy: reasoningPolicy,
	}, MicroUSD: microUSD}
}

func apiEquivalentToken(value *float64, unavailable, nonfinite, invalid APIEquivalentCostUnavailableReason) (int64, []APIEquivalentCostUnavailableReason) {
	if value == nil {
		return 0, []APIEquivalentCostUnavailableReason{unavailable}
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) {
		return 0, []APIEquivalentCostUnavailableReason{nonfinite}
	}
	if *value < 0 || math.Trunc(*value) != *value || *value >= math.Exp2(63) {
		return 0, []APIEquivalentCostUnavailableReason{invalid}
	}
	return int64(*value), nil
}

func apiEquivalentRateReasons(rates PriceRates, cacheRead, cacheWrite int64) []APIEquivalentCostUnavailableReason {
	reasons := []APIEquivalentCostUnavailableReason{}
	if !(rates.InputSet || rates.Input != 0) {
		reasons = append(reasons, APIEquivalentCostInputPriceUnavailable)
	} else if !validAPIEquivalentRate(rates.Input) {
		reasons = append(reasons, APIEquivalentCostInputPriceInvalid)
	}
	if !(rates.OutputSet || rates.Output != 0) {
		reasons = append(reasons, APIEquivalentCostOutputPriceUnavailable)
	} else if !validAPIEquivalentRate(rates.Output) {
		reasons = append(reasons, APIEquivalentCostOutputPriceInvalid)
	}
	if cacheRead != 0 {
		if rates.CacheRead == nil {
			reasons = append(reasons, APIEquivalentCostCacheReadPriceUnavailable)
		} else if !validAPIEquivalentRate(*rates.CacheRead) {
			reasons = append(reasons, APIEquivalentCostCacheReadPriceInvalid)
		}
	}
	if cacheWrite != 0 {
		if rates.CacheWrite == nil {
			reasons = append(reasons, APIEquivalentCostCacheWritePriceUnavailable)
		} else if !validAPIEquivalentRate(*rates.CacheWrite) {
			reasons = append(reasons, APIEquivalentCostCacheWritePriceInvalid)
		}
	}
	return reasons
}

func validAPIEquivalentRate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func orderAPIEquivalentCostReasons(reasons []APIEquivalentCostUnavailableReason) []APIEquivalentCostUnavailableReason {
	ordered := []APIEquivalentCostUnavailableReason{}
	for _, expected := range apiEquivalentCostReasonPrecedence {
		for _, reason := range reasons {
			if reason == expected {
				seen := false
				for _, existing := range ordered {
					if existing == reason {
						seen = true
					}
				}
				if !seen {
					ordered = append(ordered, reason)
				}
			}
		}
	}
	return ordered
}

// OrderAPIEquivalentCostUnavailableReasons returns the stable reason order used
// by EstimateAPIEquivalentCost. Callers that combine step-level estimates use
// it before rendering an aggregate unavailable state.
func OrderAPIEquivalentCostUnavailableReasons(reasons []APIEquivalentCostUnavailableReason) []APIEquivalentCostUnavailableReason {
	return orderAPIEquivalentCostReasons(reasons)
}

// EstimateAPICost derives an API-equivalent estimate independently of reported
// cost. Complete reasoning and cache counts are required; rates are selected
// from supported context tiers using all prompt-side tokens.
func EstimateAPICost(in EstimatedCostInputs, price Price) *float64 {
	if in.Reasoning == nil || in.CacheRead == nil || in.CacheWrite == nil {
		return nil
	}
	contextTokens := in.UncachedInput + *in.CacheRead + *in.CacheWrite
	rates := price.SelectRates(contextTokens)
	if !rates.hasOrdinaryPrices() {
		return nil
	}
	if (*in.CacheRead != 0 && rates.CacheRead == nil) || (*in.CacheWrite != 0 && rates.CacheWrite == nil) {
		return nil
	}
	cacheReadPrice, cacheWritePrice := 0.0, 0.0
	if rates.CacheRead != nil {
		cacheReadPrice = *rates.CacheRead
	}
	if rates.CacheWrite != nil {
		cacheWritePrice = *rates.CacheWrite
	}
	reasoningPrice := rates.Output
	if rates.Reasoning != nil {
		reasoningPrice = *rates.Reasoning
	}
	raw := float64(in.UncachedInput)*rates.Input + float64(*in.CacheRead)*cacheReadPrice +
		float64(*in.CacheWrite)*cacheWritePrice + float64(in.Output)*rates.Output + float64(*in.Reasoning)*reasoningPrice
	return roundCost(raw / 1_000_000)
}

func roundCost(raw float64) *float64 {
	rounded := math.Round(raw*math.Pow(10, costRoundDecimals)) / math.Pow(10, costRoundDecimals)
	return &rounded
}

// APIEquivalentCostMicroUSD converts one rounded API-equivalent USD estimate
// to the integer micro-USD unit used for exact aggregate sums.
func APIEquivalentCostMicroUSD(value float64) (*big.Int, bool) {
	scaled := math.Round(value * math.Pow10(APIEquivalentCostRoundingDecimals))
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled < 0 || scaled >= math.Exp2(63) {
		return nil, false
	}
	return big.NewInt(int64(scaled)), true
}

// APIEquivalentCostUSD converts an exact non-negative micro-USD total to the
// public USD value. It reports false when the value cannot be represented as a
// finite float64.
func APIEquivalentCostUSD(microUSD *big.Int) (*float64, bool) {
	if microUSD == nil || microUSD.Sign() < 0 {
		return nil, false
	}
	value, _ := new(big.Rat).SetFrac(new(big.Int).Set(microUSD), big.NewInt(int64(math.Pow10(APIEquivalentCostRoundingDecimals)))).Float64()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, false
	}
	return &value, true
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
//     rounded to costRoundDecimals (6) decimal places. in_price and
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
