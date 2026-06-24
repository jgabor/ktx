// dedup-blank stage: collapses runs of 2+ consecutive blank lines to a
// single blank line (M4 plan Task 4 / D14).
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
// The implementation splits the input into lines (preserving the
// trailing-newline convention), walks the slices tracking whether
// the previous emitted line was blank, and emits each non-blank
// line plus the FIRST blank of any run while skipping subsequent
// blanks. The result is re-joined with \n and the trailing newline
// (if any) re-applied so the byte shape matches the input's
// convention.
func DedupBlank(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}

	// Determine whether input ends with a trailing newline so the
	// output preserves the convention (a file ending in "\n" after
	// the last content line vs no trailing newline).
	hadTrailingNewline := len(input) > 0 && input[len(input)-1] == '\n'
	// SplitIntoLines preserves empty segments (so blank lines are
	// visible as "" entries) but drops the final "" that
	// bytes.Split would emit after a trailing newline. We re-add
	// the trailing newline below based on hadTrailingNewline.
	lines := bytes.Split(input, []byte("\n"))
	// If the input ended with a newline, bytes.Split appended a
	// trailing "" — drop it so we do not double-count the trailing
	// newline when re-joining. For input "a\n\n", split yields
	// ["a", "", ""]; the trailing "" is the post-newline artifact.
	if hadTrailingNewline && len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}

	out := make([][]byte, 0, len(lines))
	prevBlank := false
	for _, line := range lines {
		isBlank := isBlankLine(line)
		if isBlank {
			if prevBlank {
				// A run of consecutive blanks: skip this
				// one (we already emitted one).
				continue
			}
			// First blank of a run: emit it.
			out = append(out, line)
			prevBlank = true
			continue
		}
		out = append(out, line)
		prevBlank = false
	}

	joined := bytes.Join(out, []byte("\n"))
	if hadTrailingNewline {
		joined = append(joined, '\n')
	}
	return joined, nil
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
