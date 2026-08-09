package cachemetric

import (
	"math/big"
	"testing"
)

func TestPromptCacheTokenHitRate(t *testing.T) {
	uncached, read, write := big.NewInt(20), big.NewInt(30), big.NewInt(0)
	logical, rate, measured := PromptCacheTokenHitRate(uncached, read, write)
	if !measured || logical == nil || logical.String() != "50" || rate == nil || rate.RatString() != "3/5" {
		t.Fatalf("rate = logical=%v rate=%v measured=%v", logical, rate, measured)
	}
	if uncached.String() != "20" || read.String() != "30" || write.String() != "0" {
		t.Fatalf("calculator mutated a source bucket: uncached=%s read=%s write=%s", uncached, read, write)
	}

	logical, rate, measured = PromptCacheTokenHitRate(big.NewInt(0), big.NewInt(0), big.NewInt(0))
	if measured || logical == nil || logical.Sign() != 0 || rate != nil {
		t.Fatalf("zero denominator = logical=%v rate=%v measured=%v", logical, rate, measured)
	}

	logical, rate, measured = PromptCacheTokenHitRate(nil, big.NewInt(1), big.NewInt(0))
	if measured || logical != nil || rate != nil {
		t.Fatalf("missing bucket = logical=%v rate=%v measured=%v", logical, rate, measured)
	}
}
