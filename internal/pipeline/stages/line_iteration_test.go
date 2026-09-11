package stages

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"
)

// splitLineOracle retains the previous whole-input algorithm as a byte oracle.
// In particular, a final lone CR in normalize still produces a final LF.
func splitLineOracle(input []byte, operation string, n, m int) []byte {
	if len(input) == 0 {
		return nil
	}
	trailing := input[len(input)-1] == '\n'
	if operation == "normalize" {
		input = bytes.ReplaceAll(input, []byte("\r\n"), []byte("\n"))
		input = bytes.ReplaceAll(input, []byte("\r"), []byte("\n"))
	}
	lines := bytes.Split(input, []byte("\n"))
	if trailing {
		lines = lines[:len(lines)-1]
	}
	switch operation {
	case "head":
		if n < len(lines) {
			lines = lines[:n]
		}
	case "keep":
		if n < len(lines) && m < len(lines)-n {
			out := append([][]byte{}, lines[:n]...)
			out = append(out, fmt.Appendf(nil, "[ktx: %d lines omitted]", len(lines)-n-m))
			lines = append(out, lines[len(lines)-m:]...)
		}
	case "dedup":
		var out [][]byte
		prev := false
		for _, line := range lines {
			blank := len(bytes.Trim(line, " \t")) == 0
			if !blank || !prev {
				out = append(out, line)
			}
			prev = blank
		}
		lines = out
	case "normalize":
		for i := range lines {
			lines[i] = bytes.TrimRight(lines[i], " \t")
		}
	}
	out := bytes.Join(lines, []byte("\n"))
	if trailing {
		out = append(out, '\n')
	}
	return out
}

func TestIncrementalLinesMatchSplitOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(41, 73))
	cases := [][]byte{nil, {}, []byte("\r"), []byte("\n\n"), []byte("a\n\n"), []byte("\r\n\r \t"), bytes.Repeat([]byte("\n"), 1<<16), bytes.Repeat([]byte("x"), 1<<20)}
	alphabet := []byte("ab \t\r\n\x00\xffé\u2003")
	for range 3000 {
		input := make([]byte, rng.IntN(512))
		for i := range input {
			input[i] = alphabet[rng.IntN(len(alphabet))]
		}
		cases = append(cases, input)
	}
	for i, input := range cases {
		original := bytes.Clone(input)
		n, m := 1+rng.IntN(12), 1+rng.IntN(12)
		for _, operation := range []string{"head", "keep", "dedup", "normalize"} {
			var got []byte
			switch operation {
			case "head":
				got = headApply(input, n)
			case "keep":
				got = keepEndsApply(input, n, m)
			case "dedup":
				got, _ = DedupBlank(input)
			case "normalize":
				got, _ = Normalize(input)
			}
			want := splitLineOracle(input, operation, n, m)
			if !bytes.Equal(got, want) {
				t.Fatalf("case %d %s (%d,%d): got %q want %q", i, operation, n, m, got, want)
			}
			if !bytes.Equal(input, original) {
				t.Fatalf("%s mutated input", operation)
			}
			if len(got) > 0 {
				got[0] ^= 1
			}
			if !bytes.Equal(input, original) {
				t.Fatalf("%s output aliases input", operation)
			}
		}
	}
}
