package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/experiment"
	"github.com/jgabor/ktx/internal/usage"
)

// usagePair bundles the usage evidence a single recorded stream yields
// from the two consuming paths so an equivalence check can compare
// them field by field.
type usagePair struct {
	cli streamMetrics
	exp experiment.Metrics
}

// runBoth feeds the same recorded stream through the live-benchmark
// path (cli.extractStreamMetrics, bytes in) and the experiment path
// (experiment.ParseStream, file in) and returns both results. The
// stream is written to a temp file so ParseStream's file-based entry
// point reads exactly the bytes extractStreamMetrics saw.
func runBoth(t *testing.T, stream string) usagePair {
	t.Helper()
	return runBothBytes(t, []byte(stream))
}

func runBothBytes(t *testing.T, stream []byte) usagePair {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, stream, 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	return usagePair{
		cli: extractStreamMetrics(stream),
		exp: experiment.ParseStream(path, nil, nil),
	}
}

// assertEquiv compares usage evidence between the two paths: tokens
// input/output, cost, cache read/write, and reasoning. Nil-equivalence
// is required (both nil or both non-nil); values compared as float64
// since the CLI reports *int64 and the experiment reports *float64,
// but the recorded stream emits integer counts (within the shared
// 2^53 acceptance window) so both paths produce equivalent values.
func assertEquiv(t *testing.T, p usagePair) {
	t.Helper()
	assertPtrEquivInt(t, "tokens_in", p.cli.tokensIn, p.exp.TokensIn)
	assertPtrEquivInt(t, "tokens_out", p.cli.tokensOut, p.exp.TokensOut)
	assertPtrEquivInt(t, "cache_read", p.cli.cacheRead, p.exp.CacheRead)
	assertPtrEquivInt(t, "cache_write", p.cli.cacheWrite, p.exp.CacheWrite)
	assertPtrEquivInt(t, "reasoning", p.cli.reasoning, p.exp.Reasoning)
	assertPtrEquivFloat(t, "cost", p.cli.cost, p.exp.Cost)
	if p.cli.hasData != (p.exp.UsageEvidence.StepFinishEvents > 0) {
		t.Errorf("hasData=%v but StepFinishEvents=%d", p.cli.hasData, p.exp.UsageEvidence.StepFinishEvents)
	}
}

func assertPtrEquivInt(t *testing.T, name string, cliVal *int64, expVal *float64) {
	t.Helper()
	if (cliVal == nil) != (expVal == nil) {
		t.Errorf("%s nil mismatch: cli=%v exp=%v", name, cliVal, expVal)
		return
	}
	if cliVal == nil {
		return
	}
	if float64(*cliVal) != *expVal {
		t.Errorf("%s mismatch: cli=%d exp=%v", name, *cliVal, *expVal)
	}
}

func assertPtrEquivFloat(t *testing.T, name string, cliVal, expVal *float64) {
	t.Helper()
	if (cliVal == nil) != (expVal == nil) {
		t.Errorf("%s nil mismatch: cli=%v exp=%v", name, cliVal, expVal)
		return
	}
	if cliVal == nil {
		return
	}
	if *cliVal != *expVal {
		t.Errorf("%s mismatch: cli=%v exp=%v", name, *cliVal, *expVal)
	}
}

// TestUsageEvidence_CrossPathEquivalence feeds representative
// recorded streams through both consuming paths and asserts they
// report equivalent usage evidence. This is the central acceptance:
// the same stream yields the same tokens, cost, cache, and reasoning
// totals and the same nil-versus-zero behaviour regardless of which
// path reads it.
func TestUsageEvidence_CrossPathEquivalence(t *testing.T) {
	t.Run("complete multi-step SUM", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":500}},"cost":0.01}}
{"type":"step_finish","part":{"reason":"stop","tokens":{"input":41,"output":2,"reasoning":3,"cache":{"read":15232,"write":1500}},"cost":0.02}}`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.tokensIn == nil || *p.cli.tokensIn != 51 {
			t.Errorf("tokens_in = %v, want 51", p.cli.tokensIn)
		}
	})

	t.Run("explicit zeros preserved on every path", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.tokensIn == nil || *p.cli.tokensIn != 0 {
			t.Errorf("tokens_in = %v, want non-nil 0", p.cli.tokensIn)
		}
		if p.exp.TokensIn == nil || *p.exp.TokensIn != 0 {
			t.Errorf("experiment tokens_in = %v, want non-nil 0", p.exp.TokensIn)
		}
	})

	t.Run("null treated as absent on both paths", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":null,"write":null}},"cost":null}}`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.cacheRead != nil || p.exp.CacheRead != nil {
			t.Errorf("cache_read: cli=%v exp=%v, want nil/nil (null is absent)", p.cli.cacheRead, p.exp.CacheRead)
		}
		if p.cli.cost != nil || p.exp.Cost != nil {
			t.Errorf("cost: cli=%v exp=%v, want nil/nil (null is absent)", p.cli.cost, p.exp.Cost)
		}
	})

	t.Run("partial evidence omits the aggregate on both paths", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":1,"cache":{"read":100,"write":50}}}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2}}}`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.cacheRead != nil || p.exp.CacheRead != nil {
			t.Errorf("cache_read: cli=%v exp=%v, want nil/nil (incomplete evidence)", p.cli.cacheRead, p.exp.CacheRead)
		}
		// Tokens carried on every event remain complete.
		if p.cli.tokensIn == nil || *p.cli.tokensIn != 51 {
			t.Errorf("tokens_in: cli=%v, want non-nil 51", p.cli.tokensIn)
		}
	})

	t.Run("absent cache block leaves nil on both paths", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2},"cost":0.01}}`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.cacheRead != nil || p.exp.CacheRead != nil {
			t.Errorf("cache_read: cli=%v exp=%v, want nil/nil (no cache block)", p.cli.cacheRead, p.exp.CacheRead)
		}
	})

	t.Run("malformed unrelated lines ignored on both paths", func(t *testing.T) {
		stream := `
not json
{"type":"tool_use","part":{}}
{"type":"text","part":{"text":"answer"}}
{bad}
{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":50}},"cost":0.01}}
`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.tokensIn == nil || *p.cli.tokensIn != 10 {
			t.Errorf("tokens_in = %v, want 10", p.cli.tokensIn)
		}
	})

	t.Run("fractional token value rejected from completeness on both paths", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":1.5,"output":2},"cost":0.01}}`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.tokensIn != nil || p.exp.TokensIn != nil {
			t.Errorf("tokens_in: cli=%v exp=%v, want nil/nil (fractional rejected)", p.cli.tokensIn, p.exp.TokensIn)
		}
		// Non-fractional fields still aggregate.
		if p.cli.tokensOut == nil || *p.cli.tokensOut != 2 {
			t.Errorf("tokens_out: cli=%v, want non-nil 2", p.cli.tokensOut)
		}
	})

	t.Run("above 2^53 rejected on both paths", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":9007199254740993,"output":0},"cost":0}}`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.tokensIn != nil || p.exp.TokensIn != nil {
			t.Errorf("tokens_in: cli=%v exp=%v, want nil/nil (precision-losing rejected)", p.cli.tokensIn, p.exp.TokensIn)
		}
	})

	t.Run("large ordinary integer accepted on both paths", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":8000000,"output":100000,"reasoning":50000,"cache":{"read":7000000,"write":200000}},"cost":0.05}}`
		p := runBoth(t, stream)
		assertEquiv(t, p)
		if p.cli.tokensIn == nil || *p.cli.tokensIn != 8000000 {
			t.Errorf("tokens_in: cli=%v, want 8000000", p.cli.tokensIn)
		}
		if p.exp.TokensIn == nil || *p.exp.TokensIn != 8000000 {
			t.Errorf("experiment tokens_in: %v, want 8000000", p.exp.TokensIn)
		}
	})
}

func TestUsageEvidence_CrossPathMalformedTypedStepRetained(t *testing.T) {
	tests := map[string]string{
		"tokens block": `"tokens":"bad"`,
		"input":        `"tokens":{"input":"bad"}`,
		"output":       `"tokens":{"output":false}`,
		"reasoning":    `"tokens":{"reasoning":{}}`,
		"cache block":  `"tokens":{"cache":"bad"}`,
		"cache read":   `"tokens":{"cache":{"read":[]}}`,
		"cache write":  `"tokens":{"cache":{"write":"bad"}}`,
		"cost":         `"cost":"bad"`,
	}
	for name, fields := range tests {
		t.Run(name, func(t *testing.T) {
			stream := `{"type":"step_finish","part":{"reason":"stop",` + fields + `}}`
			p := runBoth(t, stream)
			assertEquiv(t, p)
			if !p.cli.hasData {
				t.Error("CLI hasData=false, want true for identifiable step_finish")
			}
			if p.exp.UsageEvidence.StepFinishEvents != 1 {
				t.Errorf("experiment StepFinishEvents=%d, want 1", p.exp.UsageEvidence.StepFinishEvents)
			}
			if !p.exp.StreamComplete {
				t.Error("experiment StreamComplete=false, want true for identifiable stop step")
			}
		})
	}
}

func TestUsageEvidence_CrossPathAggregateRange(t *testing.T) {
	step := func(input int64) string {
		return fmt.Sprintf(`{"type":"step_finish","part":{"tokens":{"input":%d},"cost":0}}`, input)
	}
	tests := []struct {
		name   string
		values []int64
		want   *int64
	}{
		{name: "positive 2^53 boundary", values: []int64{1<<53 - 1, 1}, want: int64TestPtr(1 << 53)},
		{name: "negative 2^53 boundary", values: []int64{-(1 << 53) + 1, -1}, want: int64TestPtr(-(1 << 53))},
		{name: "positive 2^53 crossing", values: []int64{1 << 53, 1}},
		{name: "negative 2^53 crossing", values: []int64{-(1 << 53), -1}},
		{name: "positive int64 overflow", values: repeatedInt64(1<<53, 1024)},
		{name: "negative int64 overflow", values: repeatedInt64(-(1 << 53), 1025)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines := make([]string, len(tc.values))
			for i, value := range tc.values {
				lines[i] = step(value)
			}
			p := runBoth(t, strings.Join(lines, "\n"))
			assertEquiv(t, p)
			if tc.want == nil {
				if p.cli.tokensIn != nil || p.exp.TokensIn != nil {
					t.Fatalf("tokens_in: cli=%v exp=%v, want nil/nil", p.cli.tokensIn, p.exp.TokensIn)
				}
				return
			}
			if p.cli.tokensIn == nil || *p.cli.tokensIn != *tc.want {
				t.Fatalf("CLI tokens_in=%v, want %d", p.cli.tokensIn, *tc.want)
			}
		})
	}
}

func int64TestPtr(value int64) *int64 { return &value }

func repeatedInt64(value int64, count int) []int64 {
	values := make([]int64, count)
	for i := range values {
		values[i] = value
	}
	return values
}

// TestUsageEvidence_CrossPathLargeRecord feeds a valid JSONL record
// larger than 4 MiB (the old live-benchmark cap) but no larger than
// 64 MiB through both paths and asserts equivalent evidence. Both
// paths now accept such records consistently.
func TestUsageEvidence_CrossPathLargeRecord(t *testing.T) {
	pad := strings.Repeat("a", 4*1024*1024+256)
	stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":50}},"cost":0.01,"_pad":"` + pad + `"}}`
	if len(stream) <= 4*1024*1024 {
		t.Fatalf("test stream length %d not > 4 MiB", len(stream))
	}
	p := runBoth(t, stream)
	assertEquiv(t, p)
	if p.cli.tokensIn == nil || *p.cli.tokensIn != 10 {
		t.Errorf("tokens_in = %v, want 10 (large record accepted)", p.cli.tokensIn)
	}
	if p.exp.TokensIn == nil || *p.exp.TokensIn != 10 {
		t.Errorf("experiment tokens_in = %v, want 10 (large record accepted)", p.exp.TokensIn)
	}
}

// TestUsageEvidence_CrossPathExactMaxRecord feeds a JSONL record
// whose content is exactly 64 MiB (MaxRecordSize bytes) and which has
// a trailing newline through both paths. Both must accept it at the
// exact boundary.
func TestUsageEvidence_CrossPathExactMaxRecord(t *testing.T) {
	header := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":50}},"cost":0.01,"_pad":"`
	footer := `"}}`
	padLen := usage.MaxRecordSize - len(header) - len(footer)
	if padLen < 0 {
		t.Fatalf("MaxRecordSize too small for test header/footer")
	}
	var stream bytes.Buffer
	stream.Grow(usage.MaxRecordSize + 1)
	stream.WriteString(header)
	padding := strings.Repeat("a", 64*1024)
	for padLen >= len(padding) {
		stream.WriteString(padding)
		padLen -= len(padding)
	}
	stream.WriteString(padding[:padLen])
	stream.WriteString(footer)
	if stream.Len() != usage.MaxRecordSize {
		t.Fatalf("record length %d != MaxRecordSize %d", stream.Len(), usage.MaxRecordSize)
	}
	stream.WriteByte('\n')
	p := runBothBytes(t, stream.Bytes())
	assertEquiv(t, p)
	if p.cli.tokensIn == nil || *p.cli.tokensIn != 10 {
		t.Errorf("tokens_in = %v, want 10 (exactly-64-MiB record accepted)", p.cli.tokensIn)
	}
	if p.exp.TokensIn == nil || *p.exp.TokensIn != 10 {
		t.Errorf("experiment tokens_in = %v, want 10 (exactly-64-MiB record accepted)", p.exp.TokensIn)
	}
}
