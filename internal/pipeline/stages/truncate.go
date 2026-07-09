// truncate stage: keeps the first N bytes of input and appends a
// marker line when truncation occurred.
//
// Default N is 4096 bytes. Configurable via the `truncate:N` token
// form (e.g. `truncate:8192`). Input shorter than N bytes is returned
// unchanged. When truncation occurs, the stage appends a newline (if
// the truncated input's last byte was not already a newline) and the
// marker line `[ktx: truncated N of M bytes]`, where N is the number
// of bytes dropped and M is the original total length.
//
// The arg parser is strconv.Atoi. A malformed arg returns an error
// (the runner treats it as a stage-execution-error: invariant (2)
// pass-through). A non-positive N returns an error (a 0-byte or
// negative truncation limit would silently empty the output, which
// the empty-output guard would then discard — better to error at
// configuration time).
//
// Empty input returns empty output (the runner's empty-output guard
// edge case preserves empty output).
package stages

import (
	"fmt"
	"strconv"

	"github.com/jgabor/ktx/internal/pipeline"
)

// truncateDefaultBytes is the default N when the bare-name form
// `truncate` (no colon, no arg) is used. 4096 is a common log-line
// chunk size and matches the headroom-style "show me enough to know
// what happened" convention without overwhelming model context.
const truncateDefaultBytes = 4096

// truncateBuilder is the parameterised Builder for the truncate
// stage. Parses arg via strconv.Atoi; "" uses the default; a
// malformed or non-positive arg returns an error which the runner
// treats as a stage-execution-error (invariant (2)).
func truncateBuilder(arg string) (pipeline.Stage, error) {
	n := truncateDefaultBytes
	if arg != "" {
		parsed, err := strconv.Atoi(arg)
		if err != nil {
			return nil, fmt.Errorf("truncate: invalid byte limit %q: %w", arg, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("truncate: byte limit must be positive, got %d", parsed)
		}
		n = parsed
	}
	return pipeline.StageFunc(func(input []byte) ([]byte, error) {
		return truncateApply(input, n), nil
	}), nil
}

// truncateApply returns the first n bytes of input plus a marker
// line when truncation occurred. Input with length ≤ n is returned
// byte-identical (a fresh slice; the caller may mutate freely).
func truncateApply(input []byte, n int) []byte {
	if len(input) == 0 {
		// Empty input → empty output (the runner's empty-output
		// guard edge case preserves empty output for empty
		// input). Return nil so bytes.Equal(out, nil) holds.
		return nil
	}
	if len(input) <= n {
		// Short enough — return unchanged. The runner's
		// empty-output guard would only fire if we returned
		// empty from non-empty input; we never do that here.
		out := make([]byte, len(input))
		copy(out, input)
		return out
	}

	// Truncation occurred: keep the first n bytes, then append a
	// newline (if the last kept byte was not already a newline)
	// and the marker line `[ktx: truncated %d of %d bytes]`.
	kept := make([]byte, n)
	copy(kept, input[:n])

	// Build the marker. `dropped` is how many bytes were cut;
	// `total` is the original length. Format matches the plan's
	// spec exactly: `[ktx: truncated N of M bytes]`.
	dropped := len(input) - n
	total := len(input)
	marker := []byte(fmt.Sprintf("[ktx: truncated %d of %d bytes]\n", dropped, total))

	out := make([]byte, 0, len(kept)+2+len(marker))
	out = append(out, kept...)
	// Ensure the kept portion ends in a newline before the marker
	// so the marker starts on its own line.
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, marker...)
	return out
}
