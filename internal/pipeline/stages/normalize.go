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

	out := make([]byte, 0, len(input))
	start := 0
	for i := 0; i < len(input); i++ {
		c := input[i]
		if c != '\r' && c != '\n' {
			continue
		}
		out = append(out, bytes.TrimRight(input[start:i], " \t")...)
		out = append(out, '\n')
		if c == '\r' && i+1 < len(input) && input[i+1] == '\n' {
			i++
		}
		start = i + 1
	}
	out = append(out, bytes.TrimRight(input[start:], " \t")...)
	return out, nil
}
