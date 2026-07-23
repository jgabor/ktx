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

// TestParseStream_MalformedTypedFieldsPreservesScoring keeps malformed text
// events out of answer extraction while treating every structurally
// identifiable step_finish as a model step. A malformed leaf makes only that
// field unavailable; valid siblings remain available and completeness prevents
// their partial totals from becoming attempt totals.
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

	t.Run("malformed tokens on step_finish count the event but not a partial total", func(t *testing.T) {
		expected := "done"
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":"oops","output":2},"cost":0.01}}
{"type":"text","part":{"text":"done"}}
{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":2},"cost":0.01}}`
		path := writeStream(t, stream)
		m := ParseStream(path, &expected, nil)
		if m.UsageEvidence.StepFinishEvents != 2 {
			t.Errorf("StepFinishEvents=%d, want 2 (both step_finish events counted)", m.UsageEvidence.StepFinishEvents)
		}
		if !m.StreamComplete {
			t.Error("StreamComplete=false, want true (valid step_finish with reason=stop)")
		}
		if m.TokensIn != nil {
			t.Errorf("TokensIn=%v, want nil (malformed first event makes the total partial)", m.TokensIn)
		}
		if m.TokensOut == nil || *m.TokensOut != 4 {
			t.Errorf("TokensOut=%v, want 4 (valid sibling fields are retained)", m.TokensOut)
		}
		if !m.FullPass {
			t.Fatal("FullPass=false, want true (answer 'done' from valid text event)")
		}
	})

	t.Run("malformed cost on all step_finish events leaves only cost unavailable", func(t *testing.T) {
		expected := "answer"
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":10,"output":2},"cost":"bad"}}
{"type":"step_finish","part":{"reason":"stop","tokens":{"input":5,"output":1},"cost":"bad"}}
{"type":"text","part":{"text":"answer"}}`
		path := writeStream(t, stream)
		m := ParseStream(path, &expected, nil)
		if m.UsageEvidence.StepFinishEvents != 2 {
			t.Errorf("StepFinishEvents=%d, want 2", m.UsageEvidence.StepFinishEvents)
		}
		if !m.StreamComplete {
			t.Error("StreamComplete=false, want true (both identifiable steps stopped)")
		}
		if m.TokensIn == nil || *m.TokensIn != 15 {
			t.Errorf("TokensIn=%v, want 15 (valid token siblings retained)", m.TokensIn)
		}
		if m.Cost != nil {
			t.Errorf("Cost=%v, want nil (malformed cost is unavailable)", m.Cost)
		}
		if !m.FullPass {
			t.Fatal("FullPass=false, want true (answer from valid text event)")
		}
	})

	t.Run("malformed step_finish with reason=stop remains complete", func(t *testing.T) {
		stream := `{"type":"step_finish","part":{"reason":"stop","tokens":{"input":"bad","output":2},"cost":0.01}}`
		path := writeStream(t, stream)
		m := ParseStream(path, nil, nil)
		if !m.StreamComplete {
			t.Error("StreamComplete=false, want true (identifiable step_finish retains reason)")
		}
		if m.UsageEvidence.StepFinishEvents != 1 {
			t.Errorf("StepFinishEvents=%d, want 1", m.UsageEvidence.StepFinishEvents)
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
