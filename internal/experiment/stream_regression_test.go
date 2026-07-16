package experiment

import (
	"os"
	"path/filepath"
	"testing"
)

// writeStream writes a stream to a temp file and returns its path.
func writeStream(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	return path
}

// TestParseStream_MalformedTypedFieldsPreservesScoring locks the
// caller-specific behavior that a wrong-type cost or tokens field on
// a text or step_finish event causes the entire line to be skipped by
// the strict event struct (preserving answer text extraction, step
// counts, stream-complete detection, and thus scoring and completion
// status), while a fractional numeric value on a step_finish is still
// counted as a step (the completeness gate then drops the affected
// aggregate to nil). This distinguishes "wrong JSON type" (line
// skipped) from "fractional value" (aggregator rejects from
// completeness but the step is still counted).
func TestParseStream_MalformedTypedFieldsPreservesScoring(t *testing.T) {
	t.Run("malformed cost on text event does not corrupt answer", func(t *testing.T) {
		// Two text events: the malformed one is skipped, the valid
		// one provides the answer. The malformed line's text content
		// is NOT used as the answer.
		expected := "correct"
		stream := `{"type":"text","part":{"text":"wrong","cost":"oops"}}
{"type":"text","part":{"text":"correct"}}
{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":2},"cost":0.01}}`
		path := writeStream(t, stream)
		m := ParseStream(path, &expected, nil)
		if !m.FullPass {
			t.Fatalf("FullPass=false, want true (answer should be 'correct' from valid text event)")
		}
		if m.AnswerExcerpt != "correct" {
			t.Errorf("AnswerExcerpt=%q, want 'correct' (malformed text line skipped)", m.AnswerExcerpt)
		}
		if !m.StreamComplete {
			t.Error("StreamComplete=false, want true (step_finish reason=stop on valid line)")
		}
		if m.TokensIn == nil || *m.TokensIn != 10 {
			t.Errorf("TokensIn=%v, want 10 (step_finish with valid fields still aggregates)", m.TokensIn)
		}
	})

	t.Run("malformed tokens on step_finish skips step (not counted)", func(t *testing.T) {
		// A step_finish with a string-typed tokens.input is skipped
		// entirely (the strict struct fails to unmarshal): the step
		// is NOT counted, StreamComplete is NOT set, and usage
		// evidence is nil. A subsequent valid step_finish provides
		// complete evidence.
		expected := "done"
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":"oops","output":2},"cost":0.01}}
{"type":"text","part":{"text":"done"}}
{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":2},"cost":0.01}}`
		path := writeStream(t, stream)
		m := ParseStream(path, &expected, nil)
		// Only 1 valid step_finish (the first was skipped).
		if m.UsageEvidence.StepFinishEvents != 1 {
			t.Errorf("StepFinishEvents=%d, want 1 (malformed step_finish skipped)", m.UsageEvidence.StepFinishEvents)
		}
		if !m.StreamComplete {
			t.Error("StreamComplete=false, want true (valid step_finish with reason=stop)")
		}
		if m.TokensIn == nil || *m.TokensIn != 10 {
			t.Errorf("TokensIn=%v, want 10 (only valid step_finish counted)", m.TokensIn)
		}
		if !m.FullPass {
			t.Fatal("FullPass=false, want true (answer 'done' from valid text event)")
		}
	})

	t.Run("malformed cost on all step_finish events yields nil evidence", func(t *testing.T) {
		// Both step_finish events have wrong-type cost. Both are
		// skipped entirely → no step_finish counted, no tokens,
		// StreamComplete not set. Text event provides the answer.
		expected := "answer"
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":2},"cost":"bad"}}
{"type":"step_finish","part":{"reason":"stop","tokens":{"input":5,"output":1},"cost":"bad"}}
{"type":"text","part":{"text":"answer"}}`
		path := writeStream(t, stream)
		m := ParseStream(path, &expected, nil)
		if m.UsageEvidence.StepFinishEvents != 0 {
			t.Errorf("StepFinishEvents=%d, want 0 (both step_finish lines skipped)", m.UsageEvidence.StepFinishEvents)
		}
		if m.StreamComplete {
			t.Error("StreamComplete=true, want false (no valid step_finish processed)")
		}
		if m.TokensIn != nil {
			t.Errorf("TokensIn=%v, want nil (no valid step_finish)", m.TokensIn)
		}
		if !m.FullPass {
			t.Fatal("FullPass=false, want true (answer from valid text event)")
		}
	})

	t.Run("malformed step_finish with reason=stop does not set StreamComplete", func(t *testing.T) {
		// A step_finish with reason=stop but malformed tokens.input
		// (string type) is skipped → StreamComplete NOT set, even
		// though reason=stop appears in the JSON.
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":"bad","output":2},"cost":0.01}}`
		path := writeStream(t, stream)
		m := ParseStream(path, nil, nil)
		if m.StreamComplete {
			t.Error("StreamComplete=true, want false (malformed step_finish with reason=stop is skipped)")
		}
		if m.UsageEvidence.StepFinishEvents != 0 {
			t.Errorf("StepFinishEvents=%d, want 0 (malformed step_finish skipped)", m.UsageEvidence.StepFinishEvents)
		}
	})

	t.Run("fractional token: step counted, StreamComplete set, aggregate nil", func(t *testing.T) {
		// Fractional tokens.input (1.5) is a valid JSON number
		// accepted by the strict *float64 struct → the line is NOT
		// skipped. The step IS counted; StreamComplete IS set
		// (reason=stop). But the aggregator rejects 1.5 from
		// completeness (fractional) → TokensIn is nil.
		expected := "done"
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":1.5,"output":2},"cost":0.01}}
{"type":"text","part":{"text":"done"}}`
		path := writeStream(t, stream)
		m := ParseStream(path, &expected, nil)
		if m.UsageEvidence.StepFinishEvents != 1 {
			t.Errorf("StepFinishEvents=%d, want 1 (fractional is valid JSON, step counted)", m.UsageEvidence.StepFinishEvents)
		}
		if !m.StreamComplete {
			t.Error("StreamComplete=false, want true (reason=stop on valid-JSON step_finish)")
		}
		if m.TokensIn != nil {
			t.Errorf("TokensIn=%v, want nil (fractional rejected from completeness)", m.TokensIn)
		}
		// TokensOut is still valid (integer 2 on the only event).
		if m.TokensOut == nil || *m.TokensOut != 2 {
			t.Errorf("TokensOut=%v, want 2 (integer value, not fractional)", m.TokensOut)
		}
		// Cost is fractional but float64 — accepted.
		if m.Cost == nil || *m.Cost != 0.01 {
			t.Errorf("Cost=%v, want 0.01 (cost accepts fractional)", m.Cost)
		}
		if !m.FullPass {
			t.Fatal("FullPass=false, want true (answer 'done')")
		}
	})
}

// TestParseStream_IntegerRejectionViaExperiment locks that the
// experiment path rejects the same integer values as the shared
// aggregator: fractional and above 2^53. The experiment
// converts *int64 to *float64 at its boundary; the conversion is
// exact for all accepted values.
func TestParseStream_IntegerRejectionViaExperiment(t *testing.T) {
	t.Run("above 2^53 rejected", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":9007199254740993,"output":0},"cost":0}}`
		path := writeStream(t, stream)
		m := ParseStream(path, nil, nil)
		if m.TokensIn != nil {
			t.Errorf("TokensIn=%v, want nil (2^53+1 loses precision as float64)", m.TokensIn)
		}
	})

	t.Run("max exact int (2^53) accepted", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":9007199254740992,"output":0},"cost":0}}`
		path := writeStream(t, stream)
		m := ParseStream(path, nil, nil)
		if m.TokensIn == nil || *m.TokensIn != 9007199254740992 {
			t.Errorf("TokensIn=%v, want 9007199254740992 (2^53, max exact)", m.TokensIn)
		}
	})

	t.Run("fractional cost accepted (cost is float64)", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":2},"cost":0.005}}`
		path := writeStream(t, stream)
		m := ParseStream(path, nil, nil)
		if m.Cost == nil || *m.Cost != 0.005 {
			t.Errorf("Cost=%v, want 0.005 (fractional cost accepted)", m.Cost)
		}
	})
}
