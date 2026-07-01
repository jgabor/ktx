// External subprocess-backed pipeline stages (M5 / D21).
//
// A user-defined [[plugin]] block in ktx.toml maps to an external
// Stage: the runner spawns the configured command, feeds the stage's
// input on stdin, and captures stdout as the stage's output. The
// token's arg portion (the `:arg` suffix) is passed as a single CLI
// argument so a plugin like `myecho:hello` runs `command hello` with
// stdin=pipeline-input.
//
// D14 conservative-on-failure invariants are enforced by the Runner,
// NOT by the external stage itself:
//
//  1. Empty-output guard: exit 0 + empty stdout from non-empty stdin
//     → the runner discards the empty output and passes input through.
//  2. Stage-error passthrough: non-zero exit → the runner passes input
//     through unchanged; the external stage just returns the error.
//  3. Unknown-stage skip: handled by Registry.Lookup before the
//     external Stage is constructed.
//
// No timeout is enforced (deferred per M5 scope). Scripts run to
// natural completion. The registry is rebuilt per-invocation, so
// removing a [[plugin]] block from ktx.toml and re-running restores
// the built-in stage (no persistent state between runs).

package pipeline

import (
	"bytes"
	"fmt"
	"os/exec"
)

// RegisterExternalBuilder adds a subprocess-backed stage under name
// using the given command. When the runner resolves a token whose bare
// name matches, the Builder creates an externalStage that spawns
// command with the token's arg portion (if any) as a CLI argument.
// Returns the Registry for chaining. Same last-writer-wins semantics
// as Register / RegisterBuilder — calling RegisterExternalBuilder for
// a name already registered by RegisterBuiltins overwrites the
// built-in, so a user plugin named "head" shadows the built-in head.
func (r *Registry) RegisterExternalBuilder(name, command string) *Registry {
	if r == nil {
		return r
	}
	r.builders[name] = func(arg string) (Stage, error) {
		return &externalStage{command: command, arg: arg}, nil
	}
	return r
}

// externalStage spawns a subprocess to transform pipeline bytes. The
// command receives the stage's input on stdin and its stdout becomes
// the stage's output. A non-zero exit returns an error so the runner
// applies D14 invariant 2 (stage-error passthrough).
type externalStage struct {
	command string
	arg     string
}

// Apply spawns the external command, feeds input on stdin, and
// returns stdout. A non-zero exit code returns an error (including
// the stderr output for diagnostics) so the runner's
// stage-error-passthrough invariant fires. An exit 0 with empty
// stdout returns (empty, nil) so the runner's empty-output guard
// fires — the external stage does NOT replicate the guard logic
// (the runner owns all three D14 invariants).
func (s *externalStage) Apply(input []byte) ([]byte, error) {
	var args []string
	if s.arg != "" {
		args = []string{s.arg}
	}
	cmd := exec.Command(s.command, args...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrText := bytes.TrimSpace(stderr.Bytes())
		if len(stderrText) > 0 {
			return nil, fmt.Errorf("external stage %q: %v: %s", s.command, err, stderrText)
		}
		return nil, fmt.Errorf("external stage %q: %v", s.command, err)
	}
	return stdout.Bytes(), nil
}
