package cachemetric

import "math/big"

const (
	PromptCacheTokenHitRateKind    = "token_weighted"
	PromptCacheTokenHitRateUnit    = "token_fraction"
	PromptCacheTokenHitRateFormula = "cache_read / (uncached_input + cache_read + cache_write)"
)

// PromptCacheTokenHitRate calculates the rate from the three disjoint input
// buckets. A nil bucket or a non-positive denominator makes the rate unavailable.
func PromptCacheTokenHitRate(uncachedInput, cacheRead, cacheWrite *big.Int) (logicalInput *big.Int, rate *big.Rat, measured bool) {
	if uncachedInput == nil || cacheRead == nil || cacheWrite == nil {
		return nil, nil, false
	}
	logicalInput = new(big.Int).Add(uncachedInput, cacheRead)
	logicalInput.Add(logicalInput, cacheWrite)
	if logicalInput.Sign() <= 0 {
		return logicalInput, nil, false
	}
	return logicalInput, new(big.Rat).SetFrac(cacheRead, logicalInput), true
}
