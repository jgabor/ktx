// keep-ends stage: keeps the first N lines plus the last M lines of
// input, inserting a [... (X lines omitted) ...] marker between them.
//
// Head-and-tail retention avoids losing the bottom of a
// build log — exactly where errors and stack traces live. keep-ends
// preserves them. Configurable via the `keep-ends:N:M` token form
// (e.g. `keep-ends:50:20`). Input with N+M or fewer lines is returned
// unchanged (no truncation needed). The marker line names the count of
// omitted lines so the agent can see how much was elided.
//
// The arg parser splits the arg at its single colon into two ints via
// strconv.Atoi. A malformed arg (e.g. `keep-ends:abc` or
// `keep-ends:5`) returns a non-nil error which the runner treats as a
// stage-execution-error (invariant (2)): the input passes through
// unchanged. A bare-name token (`keep-ends` with no colon) uses the
// documented defaults N=50, M=20 (the canonical real-world pipeline
// value). Non-positive N or M is a configuration error.
//
// Empty input returns empty output (the runner's empty-output guard
// edge case preserves empty output for empty input).
package stages

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/jgabor/ktx/internal/pipeline"
)

// keepEndsDefaultHead is the default N when the bare-name form
// `keep-ends` (no colon, no arg) is used. Matches the canonical
// real-world pipeline value (keep-ends:50:20).
const keepEndsDefaultHead = 50

// keepEndsDefaultTail is the default M when the bare-name form is used.
const keepEndsDefaultTail = 20

// keepEndsBuilder is the parameterised Builder for the keep-ends stage.
// It parses arg "N:M" into two ints; "" uses the documented defaults;
// a malformed or non-positive arg returns an error which the runner
// treats as a stage-execution-error (invariant (2)).
func keepEndsBuilder(arg string) (pipeline.Stage, error) {
	n, m := keepEndsDefaultHead, keepEndsDefaultTail
	if arg != "" {
		parts := strings.SplitN(arg, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("keep-ends: expected N:M, got %q", arg)
		}
		head, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("keep-ends: invalid head count %q: %w", parts[0], err)
		}
		tail, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("keep-ends: invalid tail count %q: %w", parts[1], err)
		}
		if head <= 0 || tail <= 0 {
			return nil, fmt.Errorf("keep-ends: counts must be positive, got head=%d tail=%d", head, tail)
		}
		n, m = head, tail
	}
	return pipeline.StageFunc(func(input []byte) ([]byte, error) {
		return keepEndsApply(input, n, m), nil
	}), nil
}

// keepEndsApply returns the first n lines plus the last m lines with a
// [... (X lines omitted) ...] marker between. Input with n+m or fewer
// lines is returned byte-identical (a fresh slice). The trailing-
// newline convention is preserved (matching head's edge handling).
func keepEndsApply(input []byte, n, m int) []byte {
	if len(input) == 0 {
		return nil
	}

	hadTrailingNewline := input[len(input)-1] == '\n'
	lines := bytes.Split(input, []byte("\n"))
	if hadTrailingNewline && len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}

	// No truncation needed: input has n+m or fewer lines.
	if len(lines) <= n+m {
		out := make([]byte, len(input))
		copy(out, input)
		return out
	}

	head := lines[:n]
	tail := lines[len(lines)-m:]
	omitted := len(lines) - n - m
	marker := fmt.Sprintf("[... (%d lines omitted) ...]", omitted)

	// Join head + marker + tail with newlines so the marker sits on
	// its own line. n head + 1 marker + m tail = n+1+m lines, matching
	// the plan's "total 71 lines (50+1+20)" contract.
	parts := make([][]byte, 0, len(head)+1+len(tail))
	parts = append(parts, head...)
	parts = append(parts, []byte(marker))
	parts = append(parts, tail...)
	joined := bytes.Join(parts, []byte("\n"))
	if hadTrailingNewline {
		joined = append(joined, '\n')
	}
	return joined
}
