// dedup-blank stage: collapses runs of 2+ consecutive blank lines to a
// single blank line.
//
// A "blank line" is a line whose trimmed content is empty — that is,
// the line is either empty (just "\n") or contains only whitespace
// (spaces, tabs). Single blank lines and the leading/trailing blank
// lines are preserved; runs of consecutive blanks collapse to one.
// Non-blank content is byte-identical (other than line-ending
// normalization, which dedup-blank does NOT do — that is the
// normalize stage's job).
//
// Empty input returns empty output (the runner's empty-output guard
// edge case preserves empty output). Input with no blank lines is
// returned byte-identical.
package stages

import (
	"bytes"
)

// DedupBlank is the parameter-free Stage implementation for the
// dedup-blank built-in. It collapses runs of 2+ consecutive blank
// lines to a single blank line. A nil error means the transform
// completed; this stage never errors.
//
// Lines are visited incrementally, emitting the first blank of each run
// and preserving the input's trailing-newline convention.
func DedupBlank(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}

	// Determine whether input ends with a trailing newline so the
	// output preserves the convention (a file ending in "\n" after
	// the last content line vs no trailing newline).
	hadTrailingNewline := len(input) > 0 && input[len(input)-1] == '\n'
	out := make([]byte, 0, len(input))
	prevBlank := false
	emitted := false
	for len(input) > 0 {
		line, rest, _ := bytes.Cut(input, []byte{'\n'})
		input = rest
		isBlank := isBlankLine(line)
		if isBlank && prevBlank {
			continue
		}
		if emitted {
			out = append(out, '\n')
		}
		out = append(out, line...)
		emitted = true
		prevBlank = isBlank
	}
	if hadTrailingNewline {
		out = append(out, '\n')
	}
	return out, nil
}

// isBlankLine reports whether the line bytes are entirely whitespace
// (spaces, tabs) or empty. A blank line is collapsed by dedup-blank;
// a line with any non-whitespace content is preserved.
func isBlankLine(line []byte) bool {
	for _, c := range line {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}
