// Table-driven tests for the pipeline Runner.
//
// Coverage of the three conservative-on-failure invariants:
//
//  1. Empty-output guard: non-empty input → empty output (len 0)
//     → runner discards empty output, passes input through.
//     Edge: empty input → empty output preserves the empty output.
//  2. Stage-execution-error passthrough: non-nil error OR a
//     panicked Apply → input passes through unchanged.
//  3. Unknown-stage skip: unresolved name → skipped with a stderr
//     warning, pipeline continues, NOT fatal.
//
// Plus: empty pipeline = passthrough (the no-ktx.toml default); Ran
// records only successfully-executed stages.
//
// All subtests use fake StageFunc stages (closures capturing their
// input) per the test approach. No DB, no filesystem.
package pipeline

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// stageRecorder is a test helper that captures the input each Apply
// sees and returns a caller-supplied output. The capture lets a test
// assert pass-through semantics: if invariant (2) fired, the next
// stage must see the PRE-st-error input, not the errored stage's
// output.
type stageRecorder struct {
	out []byte
	err error
	// seen is populated by Apply with the input it received. Tests
	// read this to assert stage chaining.
	seen []byte
}

func (s *stageRecorder) Apply(input []byte) ([]byte, error) {
	s.seen = append([]byte(nil), input...)
	return s.out, s.err
}

// panickyStage is a Stage that always panics with value v. Used to
// exercise the runner's recover path.
type panickyStage struct{ v any }

func (p panickyStage) Apply([]byte) ([]byte, error) { panic(p.v) }

// TestRun_EmptyPipelineIsIdentity covers the no-ktx.toml default:
// an empty Pipeline (zero stages) returns the input unchanged, Ran
// is empty (nothing ran).
func TestRun_EmptyPipelineIsIdentity(t *testing.T) {
	r := &Runner{Registry: NewRegistry(), Pipeline: nil}
	input := []byte("hello world")
	res := r.Run(input)
	if !bytes.Equal(res.Out, input) {
		t.Errorf("empty pipeline output = %q, want %q (passthrough)", res.Out, input)
	}
	if len(res.Ran) != 0 {
		t.Errorf("empty pipeline Ran = %v, want empty", res.Ran)
	}
}

// TestRun_LinearChaining covers the happy path: each stage receives
// the previous stage's output; the final bytes are returned; Ran
// records every stage that ran, in order.
func TestRun_LinearChaining(t *testing.T) {
	reg := NewRegistry()
	a := &stageRecorder{out: []byte("A")}
	b := &stageRecorder{out: []byte("AB")}
	c := &stageRecorder{out: []byte("ABC")}
	reg.Register("a", a)
	reg.Register("b", b)
	reg.Register("c", c)

	r := &Runner{Registry: reg, Pipeline: []string{"a", "b", "c"}}
	res := r.Run([]byte(""))

	// Stage a saw the empty input.
	if string(a.seen) != "" {
		t.Errorf("stage a saw %q, want empty", a.seen)
	}
	// Stage b saw stage a's output "A".
	if string(b.seen) != "A" {
		t.Errorf("stage b saw %q, want A", b.seen)
	}
	// Stage c saw stage b's output "AB".
	if string(c.seen) != "AB" {
		t.Errorf("stage c saw %q, want AB", c.seen)
	}
	// Final output is stage c's output.
	if string(res.Out) != "ABC" {
		t.Errorf("final output = %q, want ABC", res.Out)
	}
	// Ran records every stage in order.
	if len(res.Ran) != 3 || res.Ran[0] != "a" || res.Ran[1] != "b" || res.Ran[2] != "c" {
		t.Errorf("Ran = %v, want [a b c]", res.Ran)
	}
}

// TestRun_Invariant1_EmptyOutputGuard covers invariant (1): a
// stage producing empty output from non-empty input discards the
// empty output and passes the input through. Plus the edge: empty
// input → empty output preserves the empty output.
func TestRun_Invariant1_EmptyOutputGuard(t *testing.T) {
	t.Run("non-empty input → empty output: input passes through", func(t *testing.T) {
		reg := NewRegistry()
		emptier := &stageRecorder{out: []byte("")} // produces empty
		// A downstream stage captures whatever it saw.
		downstream := &stageRecorder{out: []byte("END")}
		reg.Register("emptier", emptier)
		reg.Register("downstream", downstream)

		r := &Runner{Registry: reg, Pipeline: []string{"emptier", "downstream"}}
		input := []byte("non-empty source")
		res := r.Run(input)

		// The emptier stage RAN (it returned nil error), so Ran
		// records it — the guard discards its output but the
		// stage did execute.
		if len(res.Ran) != 2 || res.Ran[0] != "emptier" || res.Ran[1] != "downstream" {
			t.Errorf("Ran = %v, want [emptier downstream]", res.Ran)
		}
		// Downstream saw the INPUT (the guard passed it through),
		// NOT the empty output the emptier produced.
		if string(downstream.seen) != "non-empty source" {
			t.Errorf("downstream saw %q, want the original input passed through",
				downstream.seen)
		}
		// Final output is downstream's output.
		if string(res.Out) != "END" {
			t.Errorf("final output = %q, want END", res.Out)
		}
	})

	t.Run("empty input → empty output: empty output preserved", func(t *testing.T) {
		// The guard only fires when input was non-empty. A stage
		// receiving empty input and returning empty output is a
		// legitimate "stage ran on empty input, produced empty"
		// — the empty output IS passed downstream (not replaced
		// with the input, because the input WAS empty too — the
		// distinction matters for a stage that legitimately
		// empties a non-existent payload).
		reg := NewRegistry()
		emptier := &stageRecorder{out: []byte("")}
		downstream := &stageRecorder{out: []byte("DOWN")}
		reg.Register("emptier", emptier)
		reg.Register("downstream", downstream)

		r := &Runner{Registry: reg, Pipeline: []string{"emptier", "downstream"}}
		res := r.Run([]byte(""))

		if len(res.Ran) != 2 {
			t.Errorf("Ran = %v, want [emptier downstream] (both ran)", res.Ran)
		}
		// Downstream received the empty output — this is the
		// edge case the invariant explicitly carves out.
		if string(downstream.seen) != "" {
			t.Errorf("downstream saw %q, want empty (the empty output was preserved)",
				downstream.seen)
		}
		if string(res.Out) != "DOWN" {
			t.Errorf("final output = %q, want DOWN", res.Out)
		}
	})
}

// TestRun_Invariant2_StageErrorPassthrough covers invariant (2):
// a stage returning a non-nil error passes its input through
// unchanged to the next stage. The error is NOT fatal to the
// pipeline.
func TestRun_Invariant2_StageErrorPassthrough(t *testing.T) {
	t.Run("error returning stage: input passes through, stage absent from Ran", func(t *testing.T) {
		reg := NewRegistry()
		errStage := &stageRecorder{
			out: []byte("SHOULD NEVER BE USED"),
			err: errors.New("simulated stage failure"),
		}
		downstream := &stageRecorder{out: []byte("OUTPUT")}
		reg.Register("err", errStage)
		reg.Register("downstream", downstream)

		r := &Runner{Registry: reg, Pipeline: []string{"err", "downstream"}}
		input := []byte("pre-error input")
		res := r.Run(input)

		// Downstream saw the INPUT (pass-through), not the
		// should-never-be-used output.
		if string(downstream.seen) != "pre-error input" {
			t.Errorf("downstream saw %q, want pre-error input (pass-through)",
				downstream.seen)
		}
		// Ran records ONLY downstream — the errored stage did
		// not successfully run.
		if len(res.Ran) != 1 || res.Ran[0] != "downstream" {
			t.Errorf("Ran = %v, want [downstream] (errored stage absent)", res.Ran)
		}
	})

	t.Run("panicking stage: input passes through, stage absent from Ran", func(t *testing.T) {
		reg := NewRegistry()
		reg.Register("panic", panickyStage{v: "boom"})
		downstream := &stageRecorder{out: []byte("OUTPUT")}
		reg.Register("downstream", downstream)

		r := &Runner{Registry: reg, Pipeline: []string{"panic", "downstream"}}
		input := []byte("pre-panic input")
		res := r.Run(input)

		// Downstream saw the input (the panic was recovered,
		// the input passed through).
		if string(downstream.seen) != "pre-panic input" {
			t.Errorf("downstream saw %q, want pre-panic input (panic recovered, pass-through)",
				downstream.seen)
		}
		// Only downstream ran.
		if len(res.Ran) != 1 || res.Ran[0] != "downstream" {
			t.Errorf("Ran = %v, want [downstream] (panicking stage absent)", res.Ran)
		}
	})

	t.Run("error in middle stage does not abort the pipeline", func(t *testing.T) {
		// A three-stage pipeline where the middle stage errors:
		// first stage ran (saw input, produced "A"), middle stage
		// errored (pass-through of "A"), last stage ran (saw "A",
		// produced "ABC"). The pipeline MUST continue past the
		// erroring stage ("the error is not fatal to the
		// pipeline").
		reg := NewRegistry()
		first := &stageRecorder{out: []byte("A")}
		mid := &stageRecorder{out: []byte("X"), err: errors.New("mid failure")}
		last := &stageRecorder{out: []byte("ABC")}
		reg.Register("first", first)
		reg.Register("mid", mid)
		reg.Register("last", last)

		r := &Runner{Registry: reg, Pipeline: []string{"first", "mid", "last"}}
		res := r.Run([]byte(""))

		// Last stage saw "A" (first's output), not "X" (mid's
		// discarded output).
		if string(last.seen) != "A" {
			t.Errorf("last stage saw %q, want A (mid errored, pass-through)", last.seen)
		}
		// Final output is last's output.
		if string(res.Out) != "ABC" {
			t.Errorf("final output = %q, want ABC", res.Out)
		}
		// Ran: first and last. Mid absent (errored).
		if len(res.Ran) != 2 || res.Ran[0] != "first" || res.Ran[1] != "last" {
			t.Errorf("Ran = %v, want [first last]", res.Ran)
		}
	})
}

// TestRun_Invariant3_UnknownStageSkip covers invariant (3): an
// unresolved stage name is skipped with a stderr warning, the
// pipeline continues, the run is NOT a fatal error.
func TestRun_Invariant3_UnknownStageSkip(t *testing.T) {
	t.Run("unknown name skipped with warning, pipeline continues", func(t *testing.T) {
		reg := NewRegistry()
		known := &stageRecorder{out: []byte("KNOWN")}
		reg.Register("known", known)
		// "unknown" is NOT registered.

		var warn bytes.Buffer
		r := &Runner{
			Registry: reg,
			Pipeline: []string{"unknown", "known"},
			Warn:     &warn,
		}
		// Input is longer than "KNOWN" so the anti-worsening gate
		// does not discard the known stage's output.
		input := []byte("the input value")
		res := r.Run(input)

		// The known stage still ran and saw "the input value" (the
		// unknown stage was skipped, so the pipeline continued with
		// the input unchanged).
		if string(known.seen) != "the input value" {
			t.Errorf("known stage saw %q, want the input value (unknown was skipped)", known.seen)
		}
		// Final output is the known stage's output.
		if string(res.Out) != "KNOWN" {
			t.Errorf("final output = %q, want KNOWN", res.Out)
		}
		// Ran records ONLY "known" — the unknown stage did not
		// run.
		if len(res.Ran) != 1 || res.Ran[0] != "known" {
			t.Errorf("Ran = %v, want [known] (unknown skipped)", res.Ran)
		}
		// The warning names the skipped stage.
		warnStr := warn.String()
		if !strings.Contains(warnStr, "unknown") {
			t.Errorf("warn output %q missing the unknown stage name", warnStr)
		}
		if !strings.Contains(warnStr, "warning") {
			t.Errorf("warn output %q missing 'warning' keyword", warnStr)
		}
	})

	t.Run("warn nil defaults to io.Discard (no panic)", func(t *testing.T) {
		// A caller that does not set Warn must not crash on an
		// unknown stage. The runner defaults to io.Discard.
		reg := NewRegistry()
		r := &Runner{Registry: reg, Pipeline: []string{"unknown"}}
		res := r.Run([]byte("input"))
		if string(res.Out) != "input" {
			t.Errorf("output = %q, want input (passthrough, unknown skipped)", res.Out)
		}
		if len(res.Ran) != 0 {
			t.Errorf("Ran = %v, want empty", res.Ran)
		}
	})

	t.Run("nil Registry: every name unknown, passthrough", func(t *testing.T) {
		// A misconfigured caller with a nil Registry degrades
		// to passthrough (every stage skipped). This is the
		// safe-failure mode — better than a nil-pointer panic.
		r := &Runner{Pipeline: []string{"a", "b", "c"}}
		res := r.Run([]byte("input"))
		if string(res.Out) != "input" {
			t.Errorf("output = %q, want input (nil Registry → passthrough)", res.Out)
		}
		if len(res.Ran) != 0 {
			t.Errorf("Ran = %v, want empty", res.Ran)
		}
	})
}

// TestRun_AntiWorseningGate covers the fourth
// conservative-on-failure invariant: a pipeline whose result is not
// smaller than its input (len(out) >= len(in), input non-empty) has
// its result discarded — the input is returned unchanged and Ran is
// cleared (so the result reads as passthrough: no compressed row).
//
// The gate fires on the pipeline RESULT, not per-stage; the three
// per-stage invariants (1–3) still fire during the loop. Equal-size or larger
// output passes the original through unchanged.
func TestRun_AntiWorseningGate(t *testing.T) {
	t.Run("pipeline grows output → gate discards, input returned", func(t *testing.T) {
		// A "repeater" stage that doubles every byte: output is
		// larger than input, so the gate fires.
		reg := NewRegistry()
		reg.Register("grow", StageFunc(func(in []byte) ([]byte, error) {
			out := make([]byte, 0, len(in)*2)
			for _, b := range in {
				out = append(out, b, b)
			}
			return out, nil
		}))
		r := &Runner{Registry: reg, Pipeline: []string{"grow"}}
		input := []byte("abc")
		res := r.Run(input)

		if string(res.Out) != "abc" {
			t.Errorf("output = %q, want %q (gate discarded grown output)", res.Out, input)
		}
		if len(res.Ran) != 0 {
			t.Errorf("Ran = %v, want empty (gate cleared Ran; result discarded)", res.Ran)
		}
		if !res.AntiWorseningRejected {
			t.Error("AntiWorseningRejected = false, want true")
		}
	})

	t.Run("pipeline shrinks output → gate does NOT fire, compressed returned", func(t *testing.T) {
		reg := NewRegistry()
		reg.Register("shrink", StageFunc(func(in []byte) ([]byte, error) {
			return []byte("X"), nil // 1 byte, smaller than any non-empty input of len>1
		}))
		r := &Runner{Registry: reg, Pipeline: []string{"shrink"}}
		input := []byte("some longer input that shrinks to X")
		res := r.Run(input)

		if string(res.Out) != "X" {
			t.Errorf("output = %q, want X (gate did not fire; pipeline helped)", res.Out)
		}
		if len(res.Ran) != 1 || res.Ran[0] != "shrink" {
			t.Errorf("Ran = %v, want [shrink] (gate preserved Ran)", res.Ran)
		}
		if res.AntiWorseningRejected {
			t.Error("AntiWorseningRejected = true, want false")
		}
	})

	t.Run("equal length output → gate fires (equal is 'not better')", func(t *testing.T) {
		// A stage that rewrites bytes at the same length (e.g. an
		// uppercase stage on already-uppercase input). Equal length
		// is treated as "not better", so the gate fires.
		reg := NewRegistry()
		reg.Register("noop", StageFunc(func(in []byte) ([]byte, error) {
			out := make([]byte, len(in))
			copy(out, in) // same length, same content
			return out, nil
		}))
		r := &Runner{Registry: reg, Pipeline: []string{"noop"}}
		input := []byte("unchanged")
		res := r.Run(input)

		if string(res.Out) != "unchanged" {
			t.Errorf("output = %q, want %q (gate fired on equal length)", res.Out, input)
		}
		if len(res.Ran) != 0 {
			t.Errorf("Ran = %v, want empty (equal length → gate discarded)", res.Ran)
		}
		if !res.AntiWorseningRejected {
			t.Error("AntiWorseningRejected = false, want true")
		}
	})

	t.Run("empty input → gate does NOT fire", func(t *testing.T) {
		// Empty input → empty/larger output is a legitimate result;
		// the gate's len(input) > 0 guard prevents firing.
		reg := NewRegistry()
		reg.Register("grow", StageFunc(func(in []byte) ([]byte, error) {
			return []byte("GROWN"), nil // 5 bytes > 0 (empty input)
		}))
		r := &Runner{Registry: reg, Pipeline: []string{"grow"}}
		res := r.Run([]byte(""))

		if string(res.Out) != "GROWN" {
			t.Errorf("output = %q, want GROWN (gate must not fire on empty input)", res.Out)
		}
		if len(res.Ran) != 1 || res.Ran[0] != "grow" {
			t.Errorf("Ran = %v, want [grow] (empty input → no gate)", res.Ran)
		}
		if res.AntiWorseningRejected {
			t.Error("AntiWorseningRejected = true, want false")
		}
	})

	t.Run("stage failure is conservative passthrough, not size rejection", func(t *testing.T) {
		reg := NewRegistry()
		reg.Register("fail", StageFunc(func(in []byte) ([]byte, error) {
			return nil, errors.New("cannot process")
		}))
		input := []byte("unchanged after failure")
		res := (&Runner{Registry: reg, Pipeline: []string{"fail"}}).Run(input)
		if string(res.Out) != string(input) || len(res.Ran) != 0 {
			t.Fatalf("result = %q ran=%v, want conservative passthrough", res.Out, res.Ran)
		}
		if res.AntiWorseningRejected {
			t.Error("AntiWorseningRejected = true, want false for stage failure")
		}
	})

	t.Run("multi-stage net shrink → gate does NOT fire", func(t *testing.T) {
		// A chain that grows then shrinks: net result smaller than
		// input → gate does not fire. Verifies the gate compares
		// against the ORIGINAL input, not stage intermediates.
		reg := NewRegistry()
		reg.Register("double", StageFunc(func(in []byte) ([]byte, error) {
			out := make([]byte, 0, len(in)*2)
			out = append(out, in...)
			out = append(out, in...)
			return out, nil
		}))
		reg.Register("take1", StageFunc(func(in []byte) ([]byte, error) {
			if len(in) == 0 {
				return nil, nil
			}
			return in[:1], nil
		}))
		r := &Runner{Registry: reg, Pipeline: []string{"double", "take1"}}
		input := []byte("hello") // 5 bytes; double→10; take1→1; net shrink
		res := r.Run(input)

		if string(res.Out) != "h" {
			t.Errorf("output = %q, want h (net shrink; gate did not fire)", res.Out)
		}
		if len(res.Ran) != 2 {
			t.Errorf("Ran = %v, want [double take1] (net shrink preserved)", res.Ran)
		}
	})
}

// TestEncodePipelineJSON covers the stage-list JSON encoder embedded in
// pipeline records. It must produce a valid array for empty, single, multi,
// and escaped-name cases.
func TestEncodePipelineJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{name: "empty", in: nil, want: "[]"},
		{name: "single", in: []string{"strip-ansi"}, want: `["strip-ansi"]`},
		{name: "multi", in: []string{"strip-ansi", "head:100"}, want: `["strip-ansi","head:100"]`},
		{name: "escaped quote", in: []string{`a"b`}, want: `["a\"b"]`},
		{name: "escaped backslash", in: []string{`a\b`}, want: `["a\\b"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EncodePipelineJSON(tc.in)
			if got != tc.want {
				t.Errorf("EncodePipelineJSON(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEncodePipelineRecord covers the pipeline-record encoder which carries
// the selected profile and route provenance alongside the stages that ran.
func TestEncodePipelineRecord(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		route   string
		ran     []string
		want    string
	}{
		{name: "empty profile empty stages", profile: "default", route: "default_fallback", ran: nil, want: `{"profile":"default","route":"default_fallback","stages":[]}`},
		{name: "profile with stages", profile: "git-status", route: "matched", ran: []string{"strip-ansi", "head:5"}, want: `{"profile":"git-status","route":"matched","stages":["strip-ansi","head:5"]}`},
		{name: "profile single stage", profile: "logs", route: "matched", ran: []string{"head:100"}, want: `{"profile":"logs","route":"matched","stages":["head:100"]}`},
		{name: "escaped quote in profile", profile: `a"b`, route: "matched", ran: nil, want: `{"profile":"a\"b","route":"matched","stages":[]}`},
		{name: "escaped backslash in profile", profile: `a\b`, route: "matched", ran: nil, want: `{"profile":"a\\b","route":"matched","stages":[]}`},
		{name: "escaped quote in stage", profile: "default", route: "default_fallback", ran: []string{`a"b`}, want: `{"profile":"default","route":"default_fallback","stages":["a\"b"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := EncodePipelineRecord(tc.profile, tc.route, tc.ran)
			if got != tc.want {
				t.Errorf("EncodePipelineRecord(%q, %q, %v) = %q, want %q", tc.profile, tc.route, tc.ran, got, tc.want)
			}
		})
	}
}
