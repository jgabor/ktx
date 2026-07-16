package usage

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// intEquals compares two *int64 for nil-equivalence and value equality.
func intEquals(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func intPtr(v int64) *int64 { return &v }

// floatEquals compares two *float64 for nil-equivalence and value
// equality.
func floatEquals(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func floatPtr(v float64) *float64 { return &v }

// TestParse_SumAggregation locks the per-field SUM across step_finish
// events. Cache counts are per-request deltas — SUM matches the
// input/output/cost aggregation; MAX would give a smaller, wrong
// number when the first event is non-zero.
func TestParse_SumAggregation(t *testing.T) {
	stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":500}},"cost":0.01}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"reasoning":3,"cache":{"read":15232,"write":1500}},"cost":0.02}}`
	ev := Parse(strings.NewReader(stream))
	if !ev.HasData {
		t.Fatal("HasData=false, want true (step_finish events present)")
	}
	if ev.Steps != 2 {
		t.Fatalf("Steps=%d, want 2", ev.Steps)
	}
	if !intEquals(ev.TokensIn, intPtr(51)) {
		t.Errorf("TokensIn=%v, want 51 (10+41)", ev.TokensIn)
	}
	if !intEquals(ev.TokensOut, intPtr(4)) {
		t.Errorf("TokensOut=%v, want 4 (2+2)", ev.TokensOut)
	}
	if !intEquals(ev.Reasoning, intPtr(4)) {
		t.Errorf("Reasoning=%v, want 4 (1+3)", ev.Reasoning)
	}
	if !intEquals(ev.CacheRead, intPtr(15332)) {
		t.Errorf("CacheRead=%v, want 15332 (100+15232, SUM not MAX)", ev.CacheRead)
	}
	if !intEquals(ev.CacheWrite, intPtr(2000)) {
		t.Errorf("CacheWrite=%v, want 2000 (500+1500)", ev.CacheWrite)
	}
	if !floatEquals(ev.Cost, floatPtr(0.03)) {
		t.Errorf("Cost=%v, want 0.03 (0.01+0.02)", ev.Cost)
	}
	if ev.TokensInEvents != 2 || ev.TokensOutEvents != 2 || ev.ReasoningEvents != 2 ||
		ev.CacheReadEvents != 2 || ev.CacheWriteEvents != 2 || ev.CostEvents != 2 {
		t.Errorf("event counters = %d/%d/%d/%d/%d/%d, want 2/2/2/2/2/2",
			ev.TokensInEvents, ev.TokensOutEvents, ev.ReasoningEvents,
			ev.CacheReadEvents, ev.CacheWriteEvents, ev.CostEvents)
	}
}

// TestParse_EmittedZeroPreserved locks the absent-versus-zero
// distinction. An explicit zero on every step_finish is a valid
// measurement: the pointer is non-nil and points to zero. An absent
// or null field stays nil.
func TestParse_EmittedZeroPreserved(t *testing.T) {
	stream := `{"type":"step_finish","part":{"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}`
	ev := Parse(strings.NewReader(stream))
	if ev.TokensIn == nil || *ev.TokensIn != 0 {
		t.Errorf("TokensIn=%v, want non-nil pointer to 0", ev.TokensIn)
	}
	if ev.TokensOut == nil || *ev.TokensOut != 0 {
		t.Errorf("TokensOut=%v, want non-nil pointer to 0", ev.TokensOut)
	}
	if ev.Reasoning == nil || *ev.Reasoning != 0 {
		t.Errorf("Reasoning=%v, want non-nil pointer to 0", ev.Reasoning)
	}
	if ev.CacheRead == nil || *ev.CacheRead != 0 {
		t.Errorf("CacheRead=%v, want non-nil pointer to 0", ev.CacheRead)
	}
	if ev.CacheWrite == nil || *ev.CacheWrite != 0 {
		t.Errorf("CacheWrite=%v, want non-nil pointer to 0", ev.CacheWrite)
	}
	if ev.Cost == nil || *ev.Cost != 0 {
		t.Errorf("Cost=%v, want non-nil pointer to 0", ev.Cost)
	}
}

// TestParse_NullIsAbsentNotZero locks that an explicit JSON null is
// treated as absent (nil), not as an emitted zero.
func TestParse_NullIsAbsentNotZero(t *testing.T) {
	stream := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":null,"write":null}},"cost":null}}`
	ev := Parse(strings.NewReader(stream))
	if ev.CacheRead != nil {
		t.Errorf("CacheRead=%v, want nil (JSON null is absent, not zero)", *ev.CacheRead)
	}
	if ev.CacheWrite != nil {
		t.Errorf("CacheWrite=%v, want nil (JSON null is absent, not zero)", *ev.CacheWrite)
	}
	if ev.Cost != nil {
		t.Errorf("Cost=%v, want nil (JSON null is absent, not zero)", *ev.Cost)
	}
	if ev.CacheReadEvents != 0 || ev.CacheWriteEvents != 0 || ev.CostEvents != 0 {
		t.Errorf("null-field event counters = %d/%d/%d, want 0/0/0",
			ev.CacheReadEvents, ev.CacheWriteEvents, ev.CostEvents)
	}
}

// TestParse_CompletenessGate locks that a field present on only some
// step_finish events drops the aggregate to nil rather than reporting
// a partial sum. Each per-field counter must equal Steps.
func TestParse_CompletenessGate(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		second string
		field  string
	}{
		{
			name:   "partial input",
			first:  `"input":10,"output":1,"reasoning":0,"cache":{"read":0,"write":0}`,
			second: `"output":2,"reasoning":0,"cache":{"read":0,"write":0}`,
			field:  "TokensIn",
		},
		{
			name:   "partial output",
			first:  `"input":1,"output":0,"reasoning":0,"cache":{"read":0,"write":0}`,
			second: `"input":2,"reasoning":0,"cache":{"read":0,"write":0}`,
			field:  "TokensOut",
		},
		{
			name:   "partial reasoning",
			first:  `"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}`,
			second: `"input":2,"output":2,"cache":{"read":0,"write":0}`,
			field:  "Reasoning",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stream := `{"type":"step_finish","part":{"tokens":{` + tc.first + `},"cost":0}}` + "\n" +
				`{"type":"step_finish","part":{"tokens":{` + tc.second + `},"cost":0}}`
			ev := Parse(strings.NewReader(stream))
			var got *int64
			switch tc.field {
			case "TokensIn":
				got = ev.TokensIn
			case "TokensOut":
				got = ev.TokensOut
			case "Reasoning":
				got = ev.Reasoning
			}
			if got != nil {
				t.Errorf("%s=%v, want nil (completeness gate: field absent on one event)", tc.field, *got)
			}
		})
	}

	t.Run("partial cache read", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":1,"cache":{"read":100,"write":50}}}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2}}}`
		ev := Parse(strings.NewReader(stream))
		if ev.CacheRead != nil {
			t.Errorf("CacheRead=%v, want nil (cache.read absent on second event)", *ev.CacheRead)
		}
		if ev.CacheWrite != nil {
			t.Errorf("CacheWrite=%v, want nil (cache.write absent on second event)", *ev.CacheWrite)
		}
		if ev.CacheReadEvents != 1 || ev.CacheWriteEvents != 1 {
			t.Errorf("cache event counters = %d/%d, want 1/1", ev.CacheReadEvents, ev.CacheWriteEvents)
		}
	})

	t.Run("partial cost", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}
{"type":"step_finish","part":{"tokens":{"input":2,"output":2,"reasoning":0,"cache":{"read":0,"write":0}}}}`
		ev := Parse(strings.NewReader(stream))
		if ev.Cost != nil {
			t.Errorf("Cost=%v, want nil (cost absent on second event)", *ev.Cost)
		}
		if ev.TokensIn == nil || ev.TokensOut == nil || ev.Reasoning == nil {
			t.Errorf("complete fields dropped: TokensIn=%v TokensOut=%v Reasoning=%v", ev.TokensIn, ev.TokensOut, ev.Reasoning)
		}
	})

	t.Run("null on one event fails gate", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":1,"cache":{"read":100,"write":50}}}}
{"type":"step_finish","part":{"tokens":{"input":41,"output":2,"cache":{"read":null,"write":null}}}}`
		ev := Parse(strings.NewReader(stream))
		if ev.CacheRead != nil {
			t.Errorf("CacheRead=%v, want nil (null on second event fails gate)", *ev.CacheRead)
		}
		if ev.CacheWrite != nil {
			t.Errorf("CacheWrite=%v, want nil (null on second event fails gate)", *ev.CacheWrite)
		}
	})
}

// TestParse_MalformedAndUnrelatedLinesIgnored locks that non-step_finish
// events, malformed JSON, and empty lines never affect the aggregate.
// A step_finish following unrelated noise still aggregates normally.
func TestParse_MalformedAndUnrelatedLinesIgnored(t *testing.T) {
	stream := `` + "\n" +
		`not json at all` + "\n" +
		`{"type":"step_start","part":{}}` + "\n" +
		`{"type":"tool_use","part":{}}` + "\n" +
		`{"type":"text","part":{"text":"answer"}}` + "\n" +
		`{this is malformed}` + "\n" +
		`{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":50}},"cost":0.01}}` + "\n"
	ev := Parse(strings.NewReader(stream))
	if !ev.HasData {
		t.Fatal("HasData=false, want true (one valid step_finish present)")
	}
	if ev.Steps != 1 {
		t.Fatalf("Steps=%d, want 1 (unrelated lines ignored)", ev.Steps)
	}
	if !intEquals(ev.TokensIn, intPtr(10)) {
		t.Errorf("TokensIn=%v, want 10", ev.TokensIn)
	}
	if !intEquals(ev.CacheRead, intPtr(100)) {
		t.Errorf("CacheRead=%v, want 100", ev.CacheRead)
	}
}

// TestParse_NoStepFinish locks the empty-evidence path: a stream with
// no step_finish events yields HasData=false and nil pointers.
func TestParse_NoStepFinish(t *testing.T) {
	stream := `{"type":"tool_use","part":{}}
{"type":"text","part":{"text":"done"}}`
	ev := Parse(strings.NewReader(stream))
	if ev.HasData {
		t.Error("HasData=true, want false (no step_finish events)")
	}
	if ev.Steps != 0 {
		t.Errorf("Steps=%d, want 0", ev.Steps)
	}
	if ev.TokensIn != nil || ev.Cost != nil || ev.CacheRead != nil {
		t.Errorf("expected nil pointers, got TokensIn=%v Cost=%v CacheRead=%v", ev.TokensIn, ev.Cost, ev.CacheRead)
	}
}

// TestParse_IntegerRejection locks that integer fields (tokens, cache,
// reasoning) reject values that cannot be represented exactly as both
// int64 and float64. Fractional values, values above 2^53, and values
// outside int64 range are rejected from completeness for that event.
// Cost (float64) accepts any number including fractional.
func TestParse_IntegerRejection(t *testing.T) {
	t.Run("fractional token value rejected from completeness", func(t *testing.T) {
		// tokens.input=1.5: fractional, not an integer — rejected.
		// The step still counts; tokensIn is absent for this
		// event → completeness gate drops the aggregate to nil.
		stream := `{"type":"step_finish","part":{"tokens":{"input":1.5,"output":2},"cost":0.01}}`
		ev := Parse(strings.NewReader(stream))
		if ev.TokensIn != nil {
			t.Errorf("TokensIn=%v, want nil (fractional value rejected)", *ev.TokensIn)
		}
		if ev.TokensInEvents != 0 {
			t.Errorf("TokensInEvents=%d, want 0 (fractional not counted)", ev.TokensInEvents)
		}
		// Non-fractional fields still aggregate.
		if !intEquals(ev.TokensOut, intPtr(2)) {
			t.Errorf("TokensOut=%v, want 2", ev.TokensOut)
		}
		if !floatEquals(ev.Cost, floatPtr(0.01)) {
			t.Errorf("Cost=%v, want 0.01", ev.Cost)
		}
	})

	t.Run("fractional on one event fails completeness gate", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2},"cost":0.01}}
{"type":"step_finish","part":{"tokens":{"input":1.5,"output":3},"cost":0.02}}`
		ev := Parse(strings.NewReader(stream))
		if ev.TokensIn != nil {
			t.Errorf("TokensIn=%v, want nil (fractional on second event fails gate)", *ev.TokensIn)
		}
		if ev.TokensInEvents != 1 {
			t.Errorf("TokensInEvents=%d, want 1 (only first event had valid integer)", ev.TokensInEvents)
		}
		// TokensOut was integer on both → still complete.
		if !intEquals(ev.TokensOut, intPtr(5)) {
			t.Errorf("TokensOut=%v, want 5 (2+3)", ev.TokensOut)
		}
	})

	t.Run("max exact int (2^53) accepted", func(t *testing.T) {
		// 2^53 = 9007199254740992, the largest integer exactly
		// representable as float64. Both paths accept it.
		stream := `{"type":"step_finish","part":{"tokens":{"input":9007199254740992,"output":0},"cost":0}}`
		ev := Parse(strings.NewReader(stream))
		if !intEquals(ev.TokensIn, intPtr(9007199254740992)) {
			t.Errorf("TokensIn=%v, want 9007199254740992 (2^53, max exact)", ev.TokensIn)
		}
	})

	t.Run("above 2^53 rejected (precision-losing)", func(t *testing.T) {
		// 2^53+1 = 9007199254740993: valid int64 but loses
		// precision as float64 (rounds to 2^53). Rejected from
		// completeness so both paths stay equivalent.
		stream := `{"type":"step_finish","part":{"tokens":{"input":9007199254740993,"output":0},"cost":0}}`
		ev := Parse(strings.NewReader(stream))
		if ev.TokensIn != nil {
			t.Errorf("TokensIn=%v, want nil (2^53+1 loses precision as float64)", *ev.TokensIn)
		}
		if ev.TokensInEvents != 0 {
			t.Errorf("TokensInEvents=%d, want 0 (precision-losing value rejected)", ev.TokensInEvents)
		}
	})

	t.Run("negative max exact int accepted", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":-9007199254740992,"output":0},"cost":0}}`
		ev := Parse(strings.NewReader(stream))
		if !intEquals(ev.TokensIn, intPtr(-9007199254740992)) {
			t.Errorf("TokensIn=%v, want -9007199254740992", ev.TokensIn)
		}
	})

	t.Run("large ordinary integer accepted", func(t *testing.T) {
		// A realistic large token count within the exact range.
		stream := `{"type":"step_finish","part":{"tokens":{"input":8000000,"output":100000,"reasoning":50000,"cache":{"read":7000000,"write":200000}},"cost":0.05}}`
		ev := Parse(strings.NewReader(stream))
		if !intEquals(ev.TokensIn, intPtr(8000000)) {
			t.Errorf("TokensIn=%v, want 8000000", ev.TokensIn)
		}
		if !intEquals(ev.CacheRead, intPtr(7000000)) {
			t.Errorf("CacheRead=%v, want 7000000", ev.CacheRead)
		}
	})

	t.Run("non-numeric token value skips structurally invalid step", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"tokens":{"input":"oops","output":2},"cost":0.01}}`
		ev := Parse(strings.NewReader(stream))
		if ev.HasData || ev.Steps != 0 {
			t.Errorf("HasData=%v Steps=%d, want false/0 (wrong typed usage skips event)", ev.HasData, ev.Steps)
		}
		if ev.TokensIn != nil || ev.TokensOut != nil {
			t.Errorf("TokensIn=%v TokensOut=%v, want nil/nil", ev.TokensIn, ev.TokensOut)
		}
	})
}

func TestParse_IntegerAggregateRange(t *testing.T) {
	stream := func(values ...int64) string {
		var b strings.Builder
		for i, value := range values {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, `{"type":"step_finish","part":{"tokens":{"input":%d}}}`, value)
		}
		return b.String()
	}
	t.Run("2^53 boundary accepted", func(t *testing.T) {
		ev := Parse(strings.NewReader(stream(1<<53-1, 1)))
		if !intEquals(ev.TokensIn, intPtr(1<<53)) {
			t.Errorf("TokensIn=%v, want 2^53", ev.TokensIn)
		}
	})
	t.Run("2^53 crossing omitted", func(t *testing.T) {
		ev := Parse(strings.NewReader(stream(1<<53, 1)))
		if ev.TokensIn != nil {
			t.Errorf("TokensIn=%v, want nil", ev.TokensIn)
		}
	})
	t.Run("positive and negative int64 overflow omitted", func(t *testing.T) {
		for name, tc := range map[string]struct {
			value int64
			count int
		}{
			"positive": {value: 1 << 53, count: 1024},
			"negative": {value: -(1 << 53), count: 1025},
		} {
			t.Run(name, func(t *testing.T) {
				values := make([]int64, tc.count)
				for i := range values {
					values[i] = tc.value
				}
				ev := Parse(strings.NewReader(stream(values...)))
				if ev.TokensIn != nil {
					t.Errorf("TokensIn=%v, want nil", ev.TokensIn)
				}
			})
		}
	})
}

// TestParse_CostAcceptsFractional locks that cost remains a float64
// field: fractional and integer values are both accepted. Cost is not
// subject to the integer precision gate.
func TestParse_CostAcceptsFractional(t *testing.T) {
	stream := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2},"cost":0.005}}
{"type":"step_finish","part":{"tokens":{"input":5,"output":1},"cost":0.015}}`
	ev := Parse(strings.NewReader(stream))
	if !floatEquals(ev.Cost, floatPtr(0.02)) {
		t.Errorf("Cost=%v, want 0.02 (0.005+0.015)", ev.Cost)
	}
	if ev.CostEvents != 2 {
		t.Errorf("CostEvents=%d, want 2", ev.CostEvents)
	}
}

// TestParse_AbsentCacheBlock locks that a step_finish whose part has
// no tokens.cache block leaves cache pointers nil and counters at
// zero, while the present token fields still aggregate.
func TestParse_AbsentCacheBlock(t *testing.T) {
	stream := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2},"cost":0.01}}`
	ev := Parse(strings.NewReader(stream))
	if !ev.HasData {
		t.Fatal("HasData=false, want true")
	}
	if ev.CacheRead != nil || ev.CacheWrite != nil {
		t.Errorf("cache pointers = %v/%v, want nil/nil (no cache block)", ev.CacheRead, ev.CacheWrite)
	}
	if ev.CacheReadEvents != 0 || ev.CacheWriteEvents != 0 {
		t.Errorf("cache counters = %d/%d, want 0/0", ev.CacheReadEvents, ev.CacheWriteEvents)
	}
	if !intEquals(ev.TokensIn, intPtr(41)) {
		t.Errorf("TokensIn=%v, want 41", ev.TokensIn)
	}
	if !floatEquals(ev.Cost, floatPtr(0.01)) {
		t.Errorf("Cost=%v, want 0.01", ev.Cost)
	}
}

// TestParse_ExactMaxRecordSize locks that a JSONL record whose content
// is exactly MaxRecordSize bytes (followed by a newline) is accepted
// at the exact boundary. The reader accommodates the newline delimiter
// separately so the full content is read.
func TestParse_ExactMaxRecordSize(t *testing.T) {
	// Build a step_finish line whose content is exactly MaxRecordSize
	// bytes. The usage evidence is tiny and well-formed; a padding
	// field fills the rest.
	header := `{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":50}},"cost":0.01,"_pad":"`
	footer := `"}}`
	padLen := MaxRecordSize - len(header) - len(footer)
	if padLen < 0 {
		t.Fatalf("MaxRecordSize=%d too small for test header/footer", MaxRecordSize)
	}
	line := header + strings.Repeat("a", padLen) + footer
	if len(line) != MaxRecordSize {
		t.Fatalf("line length %d != MaxRecordSize %d", len(line), MaxRecordSize)
	}
	// Content + newline: the reader must handle the delimiter overhead.
	stream := line + "\n"
	ev := Parse(strings.NewReader(stream))
	if !ev.HasData {
		t.Fatal("HasData=false, want true (exactly-MaxRecordSize record should be accepted)")
	}
	if !intEquals(ev.TokensIn, intPtr(10)) {
		t.Errorf("TokensIn=%v, want 10", ev.TokensIn)
	}
	if !intEquals(ev.CacheRead, intPtr(100)) {
		t.Errorf("CacheRead=%v, want 100", ev.CacheRead)
	}
}

// TestParse_OversizedRecordRejected locks that a record larger than
// MaxRecordSize is skipped without error, and subsequent valid
// records are still aggregated.
func TestParse_OversizedRecordRejected(t *testing.T) {
	header := `{"type":"step_finish","part":{"tokens":{"input":0},"_pad":"`
	footer := `"}}`
	padLen := MaxRecordSize + 1 - len(header) - len(footer)
	stream := header + strings.Repeat("a", padLen) + footer + "\n" +
		`{"type":"step_finish","part":{"tokens":{"input":42,"output":1},"cost":0.01}}`
	ev := Parse(strings.NewReader(stream))
	if !ev.HasData {
		t.Fatal("HasData=false, want true (valid record after oversized one)")
	}
	if ev.Steps != 1 {
		t.Errorf("Steps=%d, want 1 (oversized record skipped, valid one accepted)", ev.Steps)
	}
	if !intEquals(ev.TokensIn, intPtr(42)) {
		t.Errorf("TokensIn=%v, want 42", ev.TokensIn)
	}
}

// TestAggregator_InlineMatchesParse mirrors how callers drive an
// Aggregator within their own scan loop. The result must equal Parse
// over the same bytes.
func TestAggregator_InlineMatchesParse(t *testing.T) {
	stream := []byte(`{"type":"tool_use","part":{}}
{"type":"step_finish","part":{"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":50}},"cost":0.01}}
{"type":"text","part":{"text":"done"}}
{"type":"step_finish","part":{"reason":"stop","tokens":{"input":5,"output":1,"reasoning":2,"cache":{"read":0,"write":0}},"cost":0.02}}`)
	inline := &Aggregator{}
	for _, line := range bytes.Split(stream, []byte("\n")) {
		inline.AddLine(line)
	}
	got := inline.Result()
	want := Parse(bytes.NewReader(stream))
	if !evidenceEqual(got, want) {
		t.Fatalf("inline Aggregator result %+v != Parse result %+v", got, want)
	}
}

// evidenceEqual reports whether two Evidence values agree on every
// pointer (nil-equivalence) and pointed-to value, plus the scalar
// fields. Pointer identity is not required.
func evidenceEqual(a, b Evidence) bool {
	if a.HasData != b.HasData || a.Steps != b.Steps ||
		a.TokensInEvents != b.TokensInEvents || a.TokensOutEvents != b.TokensOutEvents ||
		a.CostEvents != b.CostEvents || a.CacheReadEvents != b.CacheReadEvents ||
		a.CacheWriteEvents != b.CacheWriteEvents || a.ReasoningEvents != b.ReasoningEvents {
		return false
	}
	return intEquals(a.TokensIn, b.TokensIn) &&
		intEquals(a.TokensOut, b.TokensOut) &&
		floatEquals(a.Cost, b.Cost) &&
		intEquals(a.CacheRead, b.CacheRead) &&
		intEquals(a.CacheWrite, b.CacheWrite) &&
		intEquals(a.Reasoning, b.Reasoning)
}
