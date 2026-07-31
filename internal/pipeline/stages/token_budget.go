// token-budget stage: byte-based truncation to an approximate token budget.
//
// The stage estimates tokens from byte length with a four-bytes-per-token
// heuristic and truncates to a target byte count. It snaps back to the last
// newline when one exists before the limit, but can cut mid-line otherwise.
//
// Configurable via the `token-budget:N` token form (e.g.
// `token-budget:2000`). Input under budget is returned unchanged (a
// fresh slice). When over budget, the stage keeps the first ~N*4
// bytes (snapped back to the last newline before that count when present) and
// appends a marker `[ktx: truncated ~N tokens from M]` where M is
// the estimated original token count.
//
// The arg parser is strconv.Atoi. A malformed or non-positive arg
// returns an error (the runner treats it as a stage-execution-error:
// invariant (2) pass-through). A bare-name token (`token-budget`)
// uses the documented default N=2000 (the canonical real-world
// pipeline value).
//
// Empty input returns empty output (the empty-output guard edge case
// preserves empty output for empty input). Under-budget input returns
// a byte-identical copy; the anti-worsening gate then fires on the
// equal-length result and returns the input unchanged with no
// compressed row (no help → no row).
package stages

import (
	"bytes"
	"fmt"
	"strconv"

	"github.com/jgabor/ktx/internal/pipeline"
)

// tokenBudgetDefault is the default N when the bare-name form
// `token-budget` (no colon, no arg) is used. Matches the canonical
// real-world pipeline value (token-budget:2000).
const tokenBudgetDefault = 2000

// tokenBudgetBuilder is the parameterised Builder for the token-budget
// stage. Parses arg via strconv.Atoi; "" uses the default; a malformed
// or non-positive arg returns an error which the runner treats as a
// stage-execution-error (invariant (2)).
func tokenBudgetBuilder(arg string) (pipeline.Stage, error) {
	n := tokenBudgetDefault
	if arg != "" {
		parsed, err := strconv.Atoi(arg)
		if err != nil {
			return nil, fmt.Errorf("token-budget: invalid budget %q: %w", arg, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("token-budget: budget must be positive, got %d", parsed)
		}
		n = parsed
	}
	return pipeline.StageFunc(func(input []byte) ([]byte, error) {
		return tokenBudgetApply(input, n), nil
	}), nil
}

// tokenBudgetApply truncates input to roughly n*4 bytes, preferring the last
// newline before that limit, when the estimated token count exceeds n. It
// appends a marker. Input at or under budget is returned byte-identical.
func tokenBudgetApply(input []byte, n int) []byte {
	if len(input) == 0 {
		return nil
	}

	totalTokens := (len(input) + 3) / 4
	if totalTokens <= n {
		// Under budget — return unchanged.
		out := make([]byte, len(input))
		copy(out, input)
		return out
	}

	// Over budget: truncate to ~n*4 bytes, preferring the last newline at or
	// before that count. Input without an earlier newline is cut mid-line.
	targetChars := n * 4
	if targetChars > len(input) {
		targetChars = len(input)
	}
	cut := targetChars
	if idx := bytes.LastIndex(input[:cut], []byte("\n")); idx >= 0 {
		cut = idx + 1 // include the newline (keep the line whole)
	}
	// If cut is 0 (the first newline is past targetChars, or no
	// newline at all before cut), keep targetChars as-is: a mid-line
	// cut is unavoidable when there are no newlines.

	kept := input[:cut]
	marker := fmt.Sprintf("[ktx: truncated ~%d tokens from %d]", n, totalTokens)

	out := make([]byte, 0, len(kept)+1+len(marker)+1)
	out = append(out, kept...)
	// Ensure the kept portion ends in a newline before the marker so
	// the marker starts on its own line.
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, marker...)
	// Preserve the trailing-newline convention.
	if input[len(input)-1] == '\n' {
		out = append(out, '\n')
	}
	return out
}
