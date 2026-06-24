// strip-ansi stage: removes ANSI escape sequences so colored terminal
// transcripts become plain text (M4 plan Task 4 / D14).
//
// The stage handles the CSI (Control Sequence Introducer) form:
// `ESC [ ... letter`, where `...` is a sequence of parameter bytes
// (digits 0x30–0x3F) and intermediate bytes (0x20–0x2F). The final
// byte (0x40–0x7E) terminates the sequence. CSI covers the common
// cases: color (SGR `… m`), cursor movement, erase, scroll, etc.
//
// It also handles the two-byte OSC (Operating System Command) form
// `ESC ] ... BEL` and the `ESC ] ... ESC \` form that some terminals
// emit for title/icon sequences. OSC is included because real
// `ls --color` transcripts and prompt-rendering shells emit OSC
// sequences that would otherwise leave stray bytes in the plain text.
//
// Other ESC sequences (single ESC + one byte, like ESC = or ESC >)
// are stripped as a pair — these are the keyboard-mode sequences
// some shells emit; stripping them is the conservative choice (they
// carry no information that survived the transcript's plain-text
// rendering).
//
// Empty input returns empty output (the runner's empty-output guard
// does not fire because input WAS empty — the invariant's edge case
// preserves empty output). Non-empty input with no ESC bytes is
// returned byte-identical.
package stages

// StripANSI is the parameter-free Stage implementation for the
// strip-ansi built-in. It removes ANSI escape sequences from input
// and returns the plain-text bytes. A nil error means the transform
// completed; this stage never errors (a malformed ESC sequence at
// end-of-input is dropped conservatively — the trailing partial ESC
// is removed rather than preserved, matching the "conservative
// failure" spirit even though the stage itself does not error).
func StripANSI(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}

	out := make([]byte, 0, len(input))
	i := 0
	const (
		esc = 0x1b // ESC
		csi = '['  // Control Sequence Introducer second byte
		osc = ']'  // Operating System Command second byte
		bel = 0x07 // BEL — OSC terminator (xterm form)
		bsl = '\\' // backslash — ESC \ terminator (some terminals)
	)
	for i < len(input) {
		c := input[i]
		if c != esc {
			out = append(out, c)
			i++
			continue
		}
		// ESC at the very end of input: drop it (conservative —
		// a partial escape at EOF carries no information).
		if i+1 >= len(input) {
			break
		}
		next := input[i+1]
		switch next {
		case csi:
			// CSI: ESC [ <params> <intermediates> <final>.
			// Skip the ESC and [, then advance past the
			// parameter (0x30–0x3F) and intermediate (0x20–0x2F)
			// bytes until a final byte (0x40–0x7E).
			j := i + 2
			for j < len(input) {
				b := input[j]
				if b >= 0x40 && b <= 0x7E {
					j++ // include the final byte
					break
				}
				// parameter or intermediate byte
				j++
			}
			i = j
		case osc:
			// OSC: ESC ] <data> (BEL | ESC \).
			// Scan to the next BEL or ESC \.
			j := i + 2
			for j < len(input) {
				if input[j] == bel {
					j++ // include the BEL
					break
				}
				if input[j] == esc && j+1 < len(input) && input[j+1] == bsl {
					j += 2 // include ESC \
					break
				}
				j++
			}
			i = j
		default:
			// Two-byte ESC sequence (ESC X): drop both bytes.
			// This covers ESC =, ESC >, ESC D, ESC M, ESC 7,
			// ESC 8, etc. — the common keyboard-mode and
			// terminal-state sequences a transcript might
			// include.
			i += 2
		}
	}
	return out, nil
}
