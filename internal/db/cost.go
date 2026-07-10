// Cost derivation centralizes price lookup, soft-failure behavior, null
// preservation, and rounding for every persisted record type.
package db

import "math"

// costRoundDecimals is the precision for the cost derivation. The
// methodology-v1 headline number is six decimals
// (`tools/bench/methodology-v1.yaml`'s `cost_per_correct = cost /
// max(score, 1e-9)` plus the reproducibility `six decimal places` rule); the
// persisted cost uses the same precision so a future reader does
// not have to round before dividing.
//
// Both write-time callers use the same persisted precision.
const costRoundDecimals = 6

// PriceSource abstracts the cache lookup and the cold-fetch for
// the deriver. The *Prices type in prices.go satisfies this
// interface verbatim:
//
//	(p *Prices) Lookup(provider, model string) (in, out float64, ok bool)
//	(p *Prices) Cold() bool
//	(p *Prices) Refresh() error
//
// Tests inject a hand-rolled fakePriceSource (cost_test.go) to
// exercise every branch without httptest, the *Prices singleton, or
// a live network call. The cache write (atomic on-disk file) is
// the source's responsibility — the deriver calls Refresh and lets
// the source decide whether to persist the in-memory result.
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
	rounded := math.Round(raw*math.Pow(10, costRoundDecimals)) / math.Pow(10, costRoundDecimals)
	return &rounded
}
