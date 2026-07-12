// replace stage: regex substitution.
//
// The stage compiles a regular expression and applies ReplaceAll
// to the input, substituting every match with the replacement
// string. This is the one missing primitive that prevents filter
// packs from handling common noise patterns (shortening repeated
// path prefixes, stripping timestamp prefixes from log lines,
// redacting secrets).
//
// Token form: `replace:pattern::replacement`. The Registry splits
// at the first colon to give the Builder the arg; the Builder
// splits the arg at the first `::` to separate pattern from
// replacement. The separator is `::` (double colon) so single
// colons in regex patterns survive (e.g.
// `replace:http\://.*::URL`). A pattern containing a literal `::`
// is not representable in this syntax — escape it as `[\x3a\x3a]`
// or use a character class.
//
// An empty pattern or missing `::` separator returns an error
// (the runner treats it as invariant (2): input passes through
// unchanged). An invalid regex returns an error with the same
// invariant. Empty input returns nil (the empty-output guard
// edge case preserves empty output for empty input).
//
// The anti-worsening gate (invariant (4)) fires when the
// replacement does not shrink the output: `replace:DEBUG::INFO`
// (same length) or a pattern that matches nothing (output equals
// input). A pipeline that does not reduce the agent's context cost
// is treated as passthrough.
package stages

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jgabor/ktx/internal/pipeline"
)

// replaceBuilder is the parameterised Builder for the replace
// stage. The arg is split at the first `::` into pattern and
// replacement. An empty pattern, a missing separator, or an
// unparseable regex returns an error which the runner treats as
// a stage-execution-error (invariant (2)).
func replaceBuilder(arg string) (pipeline.Stage, error) {
	parts := strings.SplitN(arg, "::", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("replace: missing '::' separator in %q; expected 'pattern::replacement'", arg)
	}
	pattern := parts[0]
	replacement := parts[1]
	if pattern == "" {
		return nil, fmt.Errorf("replace: empty pattern")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("replace: invalid regex %q: %w", pattern, err)
	}
	rep := []byte(replacement)
	return pipeline.StageFunc(func(input []byte) ([]byte, error) {
		if len(input) == 0 {
			return nil, nil
		}
		return re.ReplaceAll(input, rep), nil
	}), nil
}
