// json-compact stage: remove insignificant whitespace from strict JSON.
package stages

import "github.com/jgabor/ktx/internal/pipeline"

// JSONCompact compacts one complete JSON value. When the input is not one
// value, it accepts newline-delimited JSON only if every line is a complete
// JSON value. A final newline is preserved; blank NDJSON records are rejected.
// Invalid input is returned unchanged.
func JSONCompact(input []byte) ([]byte, error) {
	if out, _, ok := pipeline.CompactJSON(input); ok {
		return out, nil
	}
	return input, nil
}
