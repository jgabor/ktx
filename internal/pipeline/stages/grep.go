// grep and grep-v stages: keep (grep) or reject (grep-v) lines
// matching a regular expression (D22 Phase 2 / D14).
//
// Content-aware filtering lets a pipeline strip debug noise
// (grep-v:^debug:) or keep only errors (grep:(?i)error|fail|panic).
// Both are pure stdlib (regexp); invalid regexes compile-fail in the
// Builder, which the runner treats as a stage-execution-error
// (invariant (2)): the input passes through unchanged (lowfat's
// proc_grep passthrough behavior, pipeline.rs:327-336). A warning is
// emitted so the user sees the bad pattern — this is D14-consistent
// (invariant 2 warns on stage errors) rather than lowfat's silent
// passthrough, but the observable output (input unchanged) matches.
//
// The pattern is the full token arg after the first colon:
// `grep:(?i)error|fail|panic` → name="grep", pattern="(?i)error|fail|panic".
// The Registry splits the token at the FIRST colon only, so a pattern
// may itself contain colons. Matching is per-line (each line is a
// separate Match), so `^` and `$` anchors bind to line boundaries
// without the multiline flag.
//
// A grep that matches NO lines produces empty output from non-empty
// input → the runner's empty-output guard (invariant (1)) passes the
// input through unchanged. So `grep:never-matches` is a no-op (the
// pipeline does not help), and the anti-worsening gate then also fires
// on the passthrough result. This is the intended conservative behavior:
// a filter that removes everything is treated as "did not help."
//
// Empty input returns empty output (the empty-output guard edge case
// preserves empty output for empty input).
package stages

import (
	"bytes"
	"fmt"
	"regexp"

	"github.com/jgabor/ktx/internal/pipeline"
)

// grepBuilder is the shared parameterised Builder for grep and grep-v.
// keep=true keeps matching lines (grep); keep=false rejects them
// (grep-v). The arg is the regex pattern; an empty or unparseable
// pattern returns an error which the runner treats as a
// stage-execution-error (invariant (2)).
func grepBuilder(arg string, keep bool) (pipeline.Stage, error) {
	if arg == "" {
		return nil, fmt.Errorf("grep: empty pattern")
	}
	re, err := regexp.Compile(arg)
	if err != nil {
		name := "grep"
		if !keep {
			name = "grep-v"
		}
		return nil, fmt.Errorf("%s: invalid regex %q: %w", name, arg, err)
	}
	return pipeline.StageFunc(func(input []byte) ([]byte, error) {
		return grepApply(input, re, keep), nil
	}), nil
}

// grepApply filters lines: lines whose Match == keep are retained.
// The trailing-newline convention is preserved (matching head/keep-ends).
// An empty result (all lines filtered) returns nil so the runner's
// empty-output guard fires on non-empty input.
func grepApply(input []byte, re *regexp.Regexp, keep bool) []byte {
	if len(input) == 0 {
		return nil
	}

	hadTrailingNewline := input[len(input)-1] == '\n'
	lines := bytes.Split(input, []byte("\n"))
	if hadTrailingNewline && len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}

	var kept [][]byte
	for _, line := range lines {
		if re.Match(line) == keep {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		// All lines filtered out → empty output. The runner's
		// empty-output guard (invariant (1)) passes the input
		// through when input was non-empty.
		return nil
	}
	joined := bytes.Join(kept, []byte("\n"))
	if hadTrailingNewline {
		joined = append(joined, '\n')
	}
	return joined
}
