// Cost derivation (Decision 11) is a deep module: a small interface
// (PriceSource) hides the cache lookup and the cold-fetch policy; a
// tiny struct (costDeriver) holds the math and the NULL-preservation
// branches; a single shape (CostInputs) is the cross-struct adapter
// the two write-time callers (PersistToolCall and InsertBenchRun) use
// to delegate. The two callers become thin wrappers that build
// CostInputs and call Derive.
//
// Why a deep module here (vs. the previous two inline helpers):
// the two write-time paths shared the same math, the same cache
// policy, the same rounding precision, and the same NULL-preservation
// contract; only the input shape differed (ToolCall vs BenchRun).
// Inlining the helper in each file kept the duplication hidden
// behind identical function bodies. The deep module unifies the
// math and the policy behind a small interface so a future third
// caller (the M4 hook path, the bench-replay rewriter, the audit
// report) imports the same D11 contract with the same rounding
// precision without copying the lookup-then-cold-fetch-then-round
// sequence.
//
// Concurrency: costDeriver is not concurrency-safe by itself. The
// instance is meant to be created per call site (a single
// `&costDeriver{}` literal is enough), and the caller (PersistToolCall,
// InsertBenchRun) holds the only reference to the PriceSource. The
// *Prices implementation of PriceSource is itself concurrency-safe
// (sync.RWMutex inside prices.go); the deriver adds no shared state
// to the lookup path, so the contract is "one goroutine per
// (deriver, source) pair". The two existing write-time callers are
// single-goroutine by construction (the wrapper's teeAndParse loop
// is sequential), so the contract holds in practice.
//
// Soft-fail (D11): Derive never returns an error. Every branch that
// cannot derive returns nil — caller-supplied wins, NULL inputs
// short-circuit, warm-cache miss is silent, cold-cache miss + refresh
// failure is silent. The write path persists the row with whatever
// cost the caller supplied (or NULL) on any failure to derive.
package db

import "math"

// costRoundDecimals is the precision for the D11 derivation. The
// methodology-v1 headline number is six decimals
// (`.agentera/bench/methodology-v1.yaml`'s `cost_per_correct = cost /
// max(score, 1e-9)` plus the D12 `six decimal places` rule); the
// persisted cost uses the same precision so a future reader does
// not have to round before dividing.
//
// Moved from persist.go:419 in plan Task 1 — both write-time
// callers (PersistToolCall, InsertBenchRun) now reach the constant
// through the costDeriver, not directly.
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
// D9 absent-vs-zero distinction: a nil pointer means "field absent"
// (Derive returns nil), a non-nil pointer to zero means "field set
// to zero" (Derive uses 0 as a real value).
type CostInputs struct {
	TokensIn, TokensOut *int64
	Model, Provider     *string
	Cost                *float64
}

// costDeriver is the D11 deep module. The instance is stateless
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
// Contract (D11):
//
//   - Caller-supplied Cost wins: a non-nil pointer (including &0)
//     round-trips verbatim. No lookup, no refresh, no math. The
//     caller owns the cost evidence.
//
//   - Any NULL input (any of TokensIn / TokensOut / Model /
//     Provider nil) → return nil. NULL preservation (D11): a
//     partial set would force the deriver to invent the missing
//     value, which violates "conservative failure" (ARCHITECTURE.md
//     Quality attributes).
//
//   - Otherwise: src.Lookup(provider, model) → if ok → formula.
//
//   - Lookup miss + Cold() → src.Refresh() → retry Lookup → formula.
//
//   - Lookup miss + !Cold() → return nil (warm-cache miss is silent;
//     the on-disk cache is the source of truth between fetches).
//
//   - Lookup miss + Cold + Refresh fails → return nil (D11 soft-fail;
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
	// pointer to zero) round-trips verbatim — D11 is about
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
	// Cold-cache lookup miss triggers a fetch (D11 AC1, AC4).
	// Warm-cache lookup miss is silent (D11 AC2): the on-disk
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
