package pipeline

import (
	"bytes"
	"strings"
	"testing"
)

// TestExternalStage_CatIsIdentity verifies that /bin/cat configured
// as an external stage returns its stdin verbatim (exit 0, stdout =
// stdin). This is the core M5 acceptance: a user plugin named via
// [[plugin]] runs as a pipeline stage.
func TestExternalStage_CatIsIdentity(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterExternalBuilder("mycat", "/bin/cat")

	r := &Runner{Registry: reg, Pipeline: []string{"mycat"}}
	input := []byte("hello world\n")
	res := r.Run(input)

	if string(res.Out) != string(input) {
		t.Errorf("output = %q, want %q (cat is identity)", res.Out, input)
	}
	if len(res.Ran) != 1 || res.Ran[0] != "mycat" {
		t.Errorf("Ran = %v, want [mycat]", res.Ran)
	}
}

// TestExternalStage_EchoWithArg verifies that a name:arg token passes
// the arg as a CLI argument to the external command. `myecho:hello`
// with command /bin/echo runs `echo hello`, producing `hello\n`.
func TestExternalStage_EchoWithArg(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterExternalBuilder("myecho", "/bin/echo")

	r := &Runner{Registry: reg, Pipeline: []string{"myecho:hello"}}
	input := []byte("ignored by echo")
	res := r.Run(input)

	if string(res.Out) != "hello\n" {
		t.Errorf("output = %q, want %q", res.Out, "hello\\n")
	}
	if len(res.Ran) != 1 || res.Ran[0] != "myecho:hello" {
		t.Errorf("Ran = %v, want [myecho:hello]", res.Ran)
	}
}

// TestExternalStage_EmptyOutputGuard verifies D14 invariant 1: /bin/true
// exits 0 but produces empty stdout. The runner's empty-output guard
// discards the empty output and passes the input through unchanged.
func TestExternalStage_EmptyOutputGuard(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterExternalBuilder("empty", "/bin/true")

	r := &Runner{Registry: reg, Pipeline: []string{"empty"}}
	input := []byte("non-empty source")
	res := r.Run(input)

	if string(res.Out) != string(input) {
		t.Errorf("output = %q, want %q (empty-output guard pass-through)", res.Out, input)
	}
	// The stage DID run (exit 0, nil error) — it's recorded in Ran.
	if len(res.Ran) != 1 || res.Ran[0] != "empty" {
		t.Errorf("Ran = %v, want [empty] (stage ran, output discarded)", res.Ran)
	}
}

// TestExternalStage_NonZeroExitPassthrough verifies D14 invariant 2:
// /bin/false exits non-zero. The runner treats this as a
// stage-execution-error and passes the input through unchanged. A
// warning is written to the Warn writer.
func TestExternalStage_NonZeroExitPassthrough(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterExternalBuilder("fail", "/bin/false")

	var warn bytes.Buffer
	r := &Runner{
		Registry: reg,
		Pipeline: []string{"fail"},
		Warn:     &warn,
	}
	input := []byte("pre-failure input")
	res := r.Run(input)

	if string(res.Out) != string(input) {
		t.Errorf("output = %q, want %q (error passthrough)", res.Out, input)
	}
	// The stage did NOT successfully run — absent from Ran.
	if len(res.Ran) != 0 {
		t.Errorf("Ran = %v, want empty (errored stage absent)", res.Ran)
	}
	// A warning was emitted.
	warnStr := warn.String()
	if !strings.Contains(warnStr, "fail") {
		t.Errorf("warn output %q missing the stage name 'fail'", warnStr)
	}
	if !strings.Contains(warnStr, "warning") {
		t.Errorf("warn output %q missing 'warning' keyword", warnStr)
	}
}

// TestExternalStage_LastWriterWins verifies that a RegisterExternalBuilder
// for a name already registered via Register overwrites the built-in.
// A user plugin named "cat" shadows a prior Register("cat", ...) so the
// external subprocess runs instead of the Go stage.
func TestExternalStage_LastWriterWins(t *testing.T) {
	reg := NewRegistry()
	// Register a Go stage first (simulates RegisterBuiltins).
	reg.Register("cat", StageFunc(func(in []byte) ([]byte, error) {
		return []byte("BUILTIN OUTPUT"), nil
	}))
	// Register an external builder with the same name — overwrites.
	reg.RegisterExternalBuilder("cat", "/bin/echo")

	r := &Runner{Registry: reg, Pipeline: []string{"cat:overridden"}}
	res := r.Run([]byte("input"))

	// The external /bin/echo ran, not the Go stage.
	if string(res.Out) != "overridden\n" {
		t.Errorf("output = %q, want %q (external overrode builtin)", res.Out, "overridden\\n")
	}
}

// TestExternalStage_BareNameNoArg verifies that a bare-name token (no
// colon) calls the external command with no CLI args. /bin/cat with
// no args reads stdin and writes stdout (identity).
func TestExternalStage_BareNameNoArg(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterExternalBuilder("mycat", "/bin/cat")

	r := &Runner{Registry: reg, Pipeline: []string{"mycat"}}
	input := []byte("passthrough\n")
	res := r.Run(input)

	if string(res.Out) != string(input) {
		t.Errorf("output = %q, want %q", res.Out, input)
	}
}
