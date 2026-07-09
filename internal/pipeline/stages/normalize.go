// normalize stage: normalizes mixed line endings and trims trailing
// whitespace from each line.
//
// Behavior:
//   - \r\n (Windows CRLF) → \n (Unix LF)
//   - standalone \r (old Mac CR) → \n
//   - trailing whitespace (spaces, tabs) on each line is trimmed;
//     leading whitespace is preserved (so indentation survives —
//     critical for code-formatted content the stage might see).
//   - the trailing-newline convention is preserved (input ending in
//     \n → output ending in \n; input with no trailing newline →
//     output with no trailing newline).
//
// Empty input returns empty output. Input with no CR bytes and no
// trailing-line whitespace is returned byte-identical.
//
// The "leading whitespace preserved" rule is the deliberate
// asymmetry: a code fragment's indentation is load-bearing for its
// meaning, while trailing whitespace is almost always accidental
// (editor noise, terminal paste artifacts). Trimming both would
// corrupt code-formatted tool output.
package stages

import "bytes"

// Normalize is the parameter-free Stage implementation for the
// normalize built-in. It normalizes line endings to \n and trims
// trailing whitespace per line while preserving leading whitespace.
// A nil error means the transform completed; this stage never errors.
func Normalize(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}

	hadTrailingNewline := input[len(input)-1] == '\n'
	// First normalize standalone \r\n → \n and lone \r → \n, so the
	// subsequent per-line splitting sees only \n line endings.
	// bytes.Map would allocate per-byte; a manual scan with a fresh
	// buffer is both faster and easier to reason about.
	normalized := make([]byte, 0, len(input))
	for i := 0; i < len(input); i++ {
		c := input[i]
		if c == '\r' {
			// Either \r\n → \n, or lone \r → \n.
			if i+1 < len(input) && input[i+1] == '\n' {
				normalized = append(normalized, '\n')
				i++ // consume the \n
				continue
			}
			normalized = append(normalized, '\n')
			continue
		}
		normalized = append(normalized, c)
	}

	// Now split on \n, trim trailing whitespace from each line,
	// and rejoin. The trailing-newline convention is re-applied
	// below based on hadTrailingNewline.
	lines := bytes.Split(normalized, []byte("\n"))
	if hadTrailingNewline && len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}

	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		out = append(out, bytes.TrimRight(line, " \t"))
	}

	joined := bytes.Join(out, []byte("\n"))
	if hadTrailingNewline {
		joined = append(joined, '\n')
	}
	return joined, nil
}
