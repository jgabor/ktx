// head stage: keeps the first N lines of input (M4 plan Task 4 / D14).
//
// Default N is 10 (matching the Unix `head` tool's default). Configurable
// via the `head:N` token form (e.g. `head:100`). Input shorter than N
// lines is returned unchanged; the trailing-newline convention is
// preserved (a 5-line input ending in "\n" returns the same 5 lines
// ending in "\n"; a 5-line input with no trailing newline returns the
// same 5 lines with no trailing newline).
//
// The arg parser is strconv.Atoi; a malformed arg (e.g. `head:abc`)
// returns a non-nil error which the runner treats as a
// stage-execution-error (invariant (2)): the input passes through
// unchanged to the next stage. A bare-name token (`head` with no
// colon) uses the default N=10.
//
// Empty input returns empty output. A negative or zero N is a usage
// error (returns an error; the runner passes input through).
package stages

import (
	"bytes"
	"fmt"
	"strconv"

	"github.com/jgabor/ktx/internal/pipeline"
)

// headDefaultLines is the default N when the bare-name form `head`
// (no colon, no arg) is used. Matches the Unix `head` tool's default.
const headDefaultLines = 10

// headBuilder is the parameterised Builder for the head stage. It
// parses arg via strconv.Atoi; "" uses headDefaultLines; a malformed
// arg returns an error which the runner treats as a
// stage-execution-error (invariant (2)); a non-positive N returns
// an error (a head of 0 or -1 lines is meaningless and would silently
// empty the output, which the empty-output guard would then discard
// — better to error at configuration time so the user sees the bug).
func headBuilder(arg string) (pipeline.Stage, error) {
	n := headDefaultLines
	if arg != "" {
		parsed, err := strconv.Atoi(arg)
		if err != nil {
			return nil, fmt.Errorf("head: invalid line count %q: %w", arg, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("head: line count must be positive, got %d", parsed)
		}
		n = parsed
	}
	return pipeline.StageFunc(func(input []byte) ([]byte, error) {
		return headApply(input, n), nil
	}), nil
}

// headApply returns the first n lines of input, preserving the
// trailing-newline convention. Input with fewer than n lines is
// returned unchanged.
func headApply(input []byte, n int) []byte {
	if len(input) == 0 || n <= 0 {
		// An empty input or non-positive n yields empty output.
		// The runner's empty-output guard handles non-empty-input
		// → empty-output by passing the input through; for an
		// empty input, the empty output is preserved (the guard's
		// edge case).
		return nil
	}

	hadTrailingNewline := input[len(input)-1] == '\n'
	lines := bytes.Split(input, []byte("\n"))
	if hadTrailingNewline && len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}

	if n >= len(lines) {
		// Input has fewer than n lines — return unchanged.
		// We must preserve the trailing-newline convention; the
		// simplest path is to return the input verbatim (it is
		// already in the right shape).
		out := make([]byte, len(input))
		copy(out, input)
		return out
	}

	kept := lines[:n]
	joined := bytes.Join(kept, []byte("\n"))
	if hadTrailingNewline {
		joined = append(joined, '\n')
	}
	return joined
}
