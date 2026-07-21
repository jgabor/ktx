// json-compact stage: remove insignificant whitespace from strict JSON.
package stages

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// JSONCompact compacts one complete JSON value. When the input is not one
// value, it accepts newline-delimited JSON only if every line is a complete
// JSON value. A final newline is preserved; blank NDJSON records are rejected.
// Invalid input is returned unchanged.
func JSONCompact(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return input, nil
	}
	if !utf8.Valid(input) {
		return input, nil
	}

	var out bytes.Buffer
	if json.Compact(&out, input) == nil {
		return out.Bytes(), nil
	}

	lines := bytes.Split(input, []byte{'\n'})
	trailingNewline := len(lines) > 1 && len(lines[len(lines)-1]) == 0
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return input, nil
	}

	out.Reset()
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		if json.Compact(&out, line) != nil {
			return input, nil
		}
	}
	if trailingNewline {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
