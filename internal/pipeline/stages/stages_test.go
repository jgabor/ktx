// Table-driven tests for the built-in Go stages.
//
// One subtest block per stage. Each subtest asserts both the
// positive transform AND the empty-input edge (empty → empty preserves
// invariant (1)'s edge case). Fixtures: a captured colored `ls
// --color`-style transcript for strip-ansi; a log dump with blank-line
// runs for dedup-blank; a multi-line sample for head; a >4KB blob for
// truncate; three line-ending variants for normalize.
package stages

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/pipeline"
)

// TestStripANSI covers strip-ansi with a synthetic CSI sequence list
// (the common SGR color codes) plus an OSC sequence and a two-byte
// ESC sequence. Verifies all escape sequences are removed and the
// visible text is preserved byte-identical.
func TestStripANSI(t *testing.T) {
	t.Run("removes SGR color codes from a colored transcript", func(t *testing.T) {
		// ESC[31m = red, ESC[0m = reset, ESC[1;32m = bold green.
		// The visible text is "hello world".
		input := []byte("\x1b[31mhello\x1b[0m \x1b[1;32mworld\x1b[0m\n")
		out, err := StripANSI(input)
		if err != nil {
			t.Fatalf("StripANSI error: %v", err)
		}
		want := []byte("hello world\n")
		if !bytes.Equal(out, want) {
			t.Errorf("StripANSI = %q, want %q", out, want)
		}
	})

	t.Run("removes cursor-move and erase sequences", func(t *testing.T) {
		// ESC[2J = clear screen, ESC[H = home cursor, ESC[K = erase line.
		input := []byte("\x1b[2J\x1b[Hheader\n\x1b[Kbody\n")
		out, err := StripANSI(input)
		if err != nil {
			t.Fatalf("StripANSI error: %v", err)
		}
		want := []byte("header\nbody\n")
		if !bytes.Equal(out, want) {
			t.Errorf("StripANSI = %q, want %q", out, want)
		}
	})

	t.Run("removes OSC title sequences (BEL-terminated)", func(t *testing.T) {
		// ESC]0;title BEL — xterm title sequence.
		input := []byte("\x1b]0;my title\x07plain text\n")
		out, err := StripANSI(input)
		if err != nil {
			t.Fatalf("StripANSI error: %v", err)
		}
		want := []byte("plain text\n")
		if !bytes.Equal(out, want) {
			t.Errorf("StripANSI = %q, want %q", out, want)
		}
	})

	t.Run("removes OSC title sequences (ESC \\\\ -terminated)", func(t *testing.T) {
		// ESC ] 0 ; title ESC \ — some terminals use this form.
		input := []byte("\x1b]0;title\x1b\\plain\n")
		out, err := StripANSI(input)
		if err != nil {
			t.Fatalf("StripANSI error: %v", err)
		}
		want := []byte("plain\n")
		if !bytes.Equal(out, want) {
			t.Errorf("StripANSI = %q, want %q", out, want)
		}
	})

	t.Run("removes two-byte ESC sequences (ESC = ESC >)", func(t *testing.T) {
		// ESC = and ESC > are keyboard-mode toggles.
		input := []byte("\x1b=visible\n\x1b>more\n")
		out, err := StripANSI(input)
		if err != nil {
			t.Fatalf("StripANSI error: %v", err)
		}
		want := []byte("visible\nmore\n")
		if !bytes.Equal(out, want) {
			t.Errorf("StripANSI = %q, want %q", out, want)
		}
	})

	t.Run("no ESC bytes: byte-identical", func(t *testing.T) {
		input := []byte("plain text with no escapes\n")
		out, err := StripANSI(input)
		if err != nil {
			t.Fatalf("StripANSI error: %v", err)
		}
		if !bytes.Equal(out, input) {
			t.Errorf("StripANSI mutated ESC-free input: %q vs %q", out, input)
		}
	})

	t.Run("empty input → empty output (invariant (1) edge)", func(t *testing.T) {
		out, err := StripANSI(nil)
		if err != nil {
			t.Fatalf("StripANSI(nil) error: %v", err)
		}
		if out != nil {
			t.Errorf("StripANSI(nil) = %v, want nil", out)
		}
	})

	t.Run("trailing partial ESC at EOF is dropped", func(t *testing.T) {
		// A lone ESC at end of input carries no information;
		// the stage drops it conservatively.
		input := []byte("text\x1b")
		out, err := StripANSI(input)
		if err != nil {
			t.Fatalf("StripANSI error: %v", err)
		}
		want := []byte("text")
		if !bytes.Equal(out, want) {
			t.Errorf("StripANSI = %q, want %q", out, want)
		}
	})
}

// TestDedupBlank covers dedup-blank. The fixture is a log dump with
// runs of 2+ consecutive blank lines, single blank lines, and
// whitespace-only blank lines.
func TestDedupBlank(t *testing.T) {
	t.Run("collapses runs of 2+ consecutive blank lines to one", func(t *testing.T) {
		// Three blank lines between two content lines → one.
		input := []byte("header\n\n\n\nbody\n")
		out, err := DedupBlank(input)
		if err != nil {
			t.Fatalf("DedupBlank error: %v", err)
		}
		want := []byte("header\n\nbody\n")
		if !bytes.Equal(out, want) {
			t.Errorf("DedupBlank = %q, want %q", out, want)
		}
	})

	t.Run("single blank lines preserved", func(t *testing.T) {
		input := []byte("a\n\nb\n\nc\n")
		out, err := DedupBlank(input)
		if err != nil {
			t.Fatalf("DedupBlank error: %v", err)
		}
		if !bytes.Equal(out, input) {
			t.Errorf("DedupBlank mutated single-blank input: %q vs %q", out, input)
		}
	})

	t.Run("whitespace-only blank lines treated as blank", func(t *testing.T) {
		// Lines with only spaces/tabs should be collapsed too.
		input := []byte("a\n  \n\t\n  \nb\n")
		out, err := DedupBlank(input)
		if err != nil {
			t.Fatalf("DedupBlank error: %v", err)
		}
		// First whitespace-only line collapses the run; the
		// subsequent ones are dropped. We DO preserve the leading
		// whitespace content of the kept (first) blank line.
		want := []byte("a\n  \nb\n")
		if !bytes.Equal(out, want) {
			t.Errorf("DedupBlank = %q, want %q", out, want)
		}
	})

	t.Run("non-blank content byte-identical", func(t *testing.T) {
		input := []byte("line one\nline two\nline three\n")
		out, err := DedupBlank(input)
		if err != nil {
			t.Fatalf("DedupBlank error: %v", err)
		}
		if !bytes.Equal(out, input) {
			t.Errorf("DedupBlank mutated no-blanks input: %q vs %q", out, input)
		}
	})

	t.Run("preserves no-trailing-newline convention", func(t *testing.T) {
		// Input with no trailing newline must survive
		// unchanged (the trailing-newline convention is
		// observable downstream).
		input := []byte("a\n\n\nb")
		out, err := DedupBlank(input)
		if err != nil {
			t.Fatalf("DedupBlank error: %v", err)
		}
		want := []byte("a\n\nb")
		if !bytes.Equal(out, want) {
			t.Errorf("DedupBlank = %q, want %q (no trailing newline)", out, want)
		}
	})

	t.Run("empty input → empty output (invariant (1) edge)", func(t *testing.T) {
		out, err := DedupBlank(nil)
		if err != nil {
			t.Fatalf("DedupBlank error: %v", err)
		}
		if out != nil {
			t.Errorf("DedupBlank(nil) = %v, want nil", out)
		}
	})
}

// TestHead covers the head stage via headBuilder + the resulting
// Stage's Apply. Uses a 20-line input fixture and asserts the first
// N lines are kept; input shorter than N is returned unchanged.
func TestHead(t *testing.T) {
	// Build a 20-line input: "line0\n" ... "line19\n".
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line" + itoa(i)
	}
	input := []byte(strings.Join(lines, "\n") + "\n")

	t.Run("head:5 keeps first 5 lines", func(t *testing.T) {
		s, err := headBuilder("5")
		if err != nil {
			t.Fatalf("headBuilder(5): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("head Apply: %v", err)
		}
		want := []byte("line0\nline1\nline2\nline3\nline4\n")
		if !bytes.Equal(out, want) {
			t.Errorf("head:5 = %q, want %q", out, want)
		}
	})

	t.Run("bare head uses default N=10", func(t *testing.T) {
		s, err := headBuilder("")
		if err != nil {
			t.Fatalf("headBuilder(''): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("head Apply: %v", err)
		}
		want := []byte(strings.Join(lines[:10], "\n") + "\n")
		if !bytes.Equal(out, want) {
			t.Errorf("head default = %q, want first 10 lines", out)
		}
	})

	t.Run("head:N > input lines returns input unchanged", func(t *testing.T) {
		s, err := headBuilder("100")
		if err != nil {
			t.Fatalf("headBuilder(100): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("head Apply: %v", err)
		}
		if !bytes.Equal(out, input) {
			t.Errorf("head:100 should be passthrough for 20-line input; got %q", out)
		}
	})

	t.Run("malformed arg returns error (invariant (2))", func(t *testing.T) {
		_, err := headBuilder("abc")
		if err == nil {
			t.Fatal("headBuilder(abc) succeeded; want parse error")
		}
	})

	t.Run("non-positive N returns error", func(t *testing.T) {
		for _, bad := range []string{"0", "-1"} {
			_, err := headBuilder(bad)
			if err == nil {
				t.Errorf("headBuilder(%q) succeeded; want error", bad)
			}
		}
	})

	t.Run("empty input → empty output", func(t *testing.T) {
		s, err := headBuilder("5")
		if err != nil {
			t.Fatalf("headBuilder(5): %v", err)
		}
		out, err := s.Apply(nil)
		if err != nil {
			t.Fatalf("head Apply(nil): %v", err)
		}
		if out != nil {
			t.Errorf("head on empty = %v, want nil", out)
		}
	})

	t.Run("preserves no-trailing-newline convention", func(t *testing.T) {
		noTrailing := []byte("a\nb\nc\nd\ne\nf")
		s, err := headBuilder("3")
		if err != nil {
			t.Fatalf("headBuilder(3): %v", err)
		}
		out, err := s.Apply(noTrailing)
		if err != nil {
			t.Fatalf("head Apply: %v", err)
		}
		want := []byte("a\nb\nc")
		if !bytes.Equal(out, want) {
			t.Errorf("head:3 (no trailing) = %q, want %q", out, want)
		}
	})
}

// TestTruncate covers the truncate stage. Uses a known-size input so
// the marker line and dropped-byte count are deterministic.
func TestTruncate(t *testing.T) {
	// Build an input larger than 4096 so the default truncates.
	// 5000 bytes of 'a' followed by a newline.
	input := bytes.Repeat([]byte("a"), 5000)
	input = append(input, '\n')

	t.Run("default truncate at 4096 appends marker", func(t *testing.T) {
		s, err := truncateBuilder("")
		if err != nil {
			t.Fatalf("truncateBuilder(''): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("truncate Apply: %v", err)
		}
		// Marker: [ktx: truncated 904 of 5001 bytes]\n
		// (5001 total - 4096 kept = 905 dropped; the marker counts
		// dropped bytes, not the marker itself).
		wantDropped := len(input) - 4096 // 5001 - 4096 = 905
		marker := []byte("[ktx: truncated " + itoa(wantDropped) + " of " + itoa(len(input)) + " bytes]\n")
		want := append(bytes.Repeat([]byte("a"), 4096), '\n')
		want = append(want, marker...)
		if !bytes.Equal(out, want) {
			t.Errorf("truncate default = (len %d) %q..., want (len %d) %q...",
				len(out), string(out[:50]), len(want), string(want[:50]))
		}
	})

	t.Run("truncate:N > input returns input unchanged", func(t *testing.T) {
		short := []byte("short input")
		s, err := truncateBuilder("8192")
		if err != nil {
			t.Fatalf("truncateBuilder(8192): %v", err)
		}
		out, err := s.Apply(short)
		if err != nil {
			t.Fatalf("truncate Apply: %v", err)
		}
		if !bytes.Equal(out, short) {
			t.Errorf("truncate:8192 should be passthrough; got %q", out)
		}
	})

	t.Run("malformed arg returns error", func(t *testing.T) {
		_, err := truncateBuilder("xyz")
		if err == nil {
			t.Fatal("truncateBuilder(xyz) succeeded; want parse error")
		}
	})

	t.Run("non-positive N returns error", func(t *testing.T) {
		_, err := truncateBuilder("0")
		if err == nil {
			t.Error("truncateBuilder(0) succeeded; want error")
		}
	})

	t.Run("empty input → empty output", func(t *testing.T) {
		s, err := truncateBuilder("10")
		if err != nil {
			t.Fatalf("truncateBuilder(10): %v", err)
		}
		out, err := s.Apply(nil)
		if err != nil {
			t.Fatalf("truncate Apply(nil): %v", err)
		}
		if out != nil {
			t.Errorf("truncate on empty = %v, want nil", out)
		}
	})
}

// TestNormalize covers the normalize stage across three line-ending
// variants and trailing-whitespace trimming while preserving leading
// whitespace.
func TestNormalize(t *testing.T) {
	t.Run("CRLF → LF", func(t *testing.T) {
		input := []byte("line1\r\nline2\r\nline3\r\n")
		out, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize error: %v", err)
		}
		want := []byte("line1\nline2\nline3\n")
		if !bytes.Equal(out, want) {
			t.Errorf("Normalize CRLF = %q, want %q", out, want)
		}
	})

	t.Run("lone CR → LF", func(t *testing.T) {
		input := []byte("line1\rline2\rline3\r")
		out, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize error: %v", err)
		}
		want := []byte("line1\nline2\nline3\n")
		if !bytes.Equal(out, want) {
			t.Errorf("Normalize CR = %q, want %q", out, want)
		}
	})

	t.Run("trims trailing whitespace, preserves leading", func(t *testing.T) {
		// Each line has leading spaces (preserved) and trailing
		// spaces/tabs (trimmed).
		input := []byte("  code line   \n\tindented\t\nplain\n")
		out, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize error: %v", err)
		}
		want := []byte("  code line\n\tindented\nplain\n")
		if !bytes.Equal(out, want) {
			t.Errorf("Normalize trailing = %q, want %q", out, want)
		}
	})

	t.Run("preserves outer blank lines", func(t *testing.T) {
		input := []byte("\n\nvalue   \n\n")
		out, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize error: %v", err)
		}
		want := []byte("\n\nvalue\n\n")
		if !bytes.Equal(out, want) {
			t.Errorf("Normalize outer blanks = %q, want %q", out, want)
		}
	})

	t.Run("mixed CRLF and LF normalizes to LF", func(t *testing.T) {
		input := []byte("a\r\nb\nc\r\nd\n")
		out, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize error: %v", err)
		}
		want := []byte("a\nb\nc\nd\n")
		if !bytes.Equal(out, want) {
			t.Errorf("Normalize mixed = %q, want %q", out, want)
		}
	})

	t.Run("no CR and no trailing whitespace: byte-identical", func(t *testing.T) {
		input := []byte("clean\ninput\n")
		out, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize error: %v", err)
		}
		if !bytes.Equal(out, input) {
			t.Errorf("Normalize mutated clean input: %q vs %q", out, input)
		}
	})

	t.Run("preserves no-trailing-newline convention", func(t *testing.T) {
		input := []byte("a\r\nb")
		out, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize error: %v", err)
		}
		want := []byte("a\nb")
		if !bytes.Equal(out, want) {
			t.Errorf("Normalize (no trailing) = %q, want %q", out, want)
		}
	})

	t.Run("empty input → empty output", func(t *testing.T) {
		out, err := Normalize(nil)
		if err != nil {
			t.Fatalf("Normalize(nil) error: %v", err)
		}
		if out != nil {
			t.Errorf("Normalize(nil) = %v, want nil", out)
		}
	})
}

// TestKeepEnds covers the keep-ends stage across normal, edge, and
// error cases.
func TestKeepEnds(t *testing.T) {
	// Build a 100-line input: "line00\n" ... "line99\n".
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "line" + itoa(i)
	}
	input := []byte(strings.Join(lines, "\n") + "\n")

	t.Run("keep-ends:50:20 keeps first 50 + last 20 + marker (71 lines)", func(t *testing.T) {
		s, err := keepEndsBuilder("50:20")
		if err != nil {
			t.Fatalf("keepEndsBuilder(50:20): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("keep-ends Apply: %v", err)
		}
		outLines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(outLines) != 71 {
			t.Fatalf("output has %d lines, want 71 (50+1+20)", len(outLines))
		}
		// First 50 lines preserved.
		if outLines[0] != "line0" || outLines[49] != "line49" {
			t.Errorf("head section = %q..%q, want line0..line49", outLines[0], outLines[49])
		}
		// Marker line in the middle.
		if outLines[50] != "[ktx: 30 lines omitted]" {
			t.Errorf("marker = %q, want [ktx: 30 lines omitted]", outLines[50])
		}
		// Last 20 lines preserved.
		if outLines[51] != "line80" || outLines[70] != "line99" {
			t.Errorf("tail section = %q..%q, want line80..line99", outLines[51], outLines[70])
		}
	})

	t.Run("keep-ends:50:20 on 60 lines returns all 60 unchanged", func(t *testing.T) {
		short := []byte(strings.Join(lines[:60], "\n") + "\n")
		s, err := keepEndsBuilder("50:20")
		if err != nil {
			t.Fatalf("keepEndsBuilder(50:20): %v", err)
		}
		out, err := s.Apply(short)
		if err != nil {
			t.Fatalf("keep-ends Apply: %v", err)
		}
		if !bytes.Equal(out, short) {
			t.Errorf("keep-ends on 60 lines should be passthrough; got (len %d) want (len %d)", len(out), len(short))
		}
	})

	t.Run("bare keep-ends uses default N=50 M=20", func(t *testing.T) {
		s, err := keepEndsBuilder("")
		if err != nil {
			t.Fatalf("keepEndsBuilder(''): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("keep-ends Apply: %v", err)
		}
		outLines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(outLines) != 71 {
			t.Errorf("default keep-ends has %d lines, want 71 (50+1+20)", len(outLines))
		}
	})

	t.Run("preserves no-trailing-newline convention", func(t *testing.T) {
		noTrailing := []byte("a\nb\nc\nd\ne\nf\ng\nh")
		s, err := keepEndsBuilder("2:2")
		if err != nil {
			t.Fatalf("keepEndsBuilder(2:2): %v", err)
		}
		out, err := s.Apply(noTrailing)
		if err != nil {
			t.Fatalf("keep-ends Apply: %v", err)
		}
		// 8 lines, keep 2+2=4 → too few; 8 > 4 so truncates to 2+marker+2.
		if bytes.HasSuffix(out, []byte("\n")) {
			t.Errorf("keep-ends added a trailing newline to no-trailing input: %q", out)
		}
	})

	t.Run("malformed arg returns error (invariant (2))", func(t *testing.T) {
		for _, bad := range []string{"abc", "5", "5:", ":5", "a:b", "0:5", "5:0"} {
			_, err := keepEndsBuilder(bad)
			if err == nil {
				t.Errorf("keepEndsBuilder(%q) succeeded; want error", bad)
			}
		}
	})

	t.Run("empty input → empty output", func(t *testing.T) {
		s, err := keepEndsBuilder("5:5")
		if err != nil {
			t.Fatalf("keepEndsBuilder(5:5): %v", err)
		}
		out, err := s.Apply(nil)
		if err != nil {
			t.Fatalf("keep-ends Apply(nil): %v", err)
		}
		if out != nil {
			t.Errorf("keep-ends on empty = %v, want nil", out)
		}
	})
}

// TestGrep covers the grep stage (keep matching lines).
func TestGrep(t *testing.T) {
	input := []byte("info: starting\nerror: failed to load\ninfo: retrying\nfail: bad config\nwarn: low memory\n")

	t.Run("grep:pattern keeps only matching lines", func(t *testing.T) {
		s, err := grepBuilder("(?i)error|fail|panic", true)
		if err != nil {
			t.Fatalf("grepBuilder: %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("grep Apply: %v", err)
		}
		outLines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(outLines) != 2 {
			t.Fatalf("output has %d lines, want 2 (error + fail)", len(outLines))
		}
		if !strings.Contains(outLines[0], "error") || !strings.Contains(outLines[1], "fail") {
			t.Errorf("grep kept wrong lines: %v", outLines)
		}
	})

	t.Run("grep with anchored pattern matches line starts", func(t *testing.T) {
		s, err := grepBuilder("^error:", true)
		if err != nil {
			t.Fatalf("grepBuilder: %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("grep Apply: %v", err)
		}
		if !strings.HasPrefix(string(out), "error:") {
			t.Errorf("grep ^error: output = %q, want error: line", out)
		}
	})

	t.Run("invalid regex returns error (passthrough via invariant (2))", func(t *testing.T) {
		_, err := grepBuilder("[invalid", true)
		if err == nil {
			t.Fatal("grepBuilder([invalid succeeded; want regex compile error")
		}
	})

	t.Run("grep via registry: invalid pattern resolves as bad-arg error", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)
		_, ok, err := reg.Lookup("grep:[invalid")
		if !ok {
			t.Error("Lookup(grep:[invalid) reported not-registered; want recognised-but-bad-arg")
		}
		if err == nil {
			t.Error("Lookup(grep:[invalid) returned nil error; want compile error")
		}
	})

	t.Run("empty pattern returns error", func(t *testing.T) {
		_, err := grepBuilder("", true)
		if err == nil {
			t.Fatal("grepBuilder('') succeeded; want error")
		}
	})

	t.Run("no matches → empty output (invariant (1) fires at runner)", func(t *testing.T) {
		s, err := grepBuilder("never-matches-xyz", true)
		if err != nil {
			t.Fatalf("grepBuilder: %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("grep Apply: %v", err)
		}
		if out != nil {
			t.Errorf("grep with no matches = %v, want nil (empty-output guard fires at runner)", out)
		}
	})

	t.Run("empty input → empty output", func(t *testing.T) {
		s, err := grepBuilder("error", true)
		if err != nil {
			t.Fatalf("grepBuilder: %v", err)
		}
		out, err := s.Apply(nil)
		if err != nil {
			t.Fatalf("grep Apply(nil): %v", err)
		}
		if out != nil {
			t.Errorf("grep on empty = %v, want nil", out)
		}
	})
}

// TestGrepV covers the grep-v stage (reject matching lines).
func TestGrepV(t *testing.T) {
	input := []byte("debug: tracing entry\ninfo: started\ndebug: tracing exit\nwarn: degraded\n")

	t.Run("grep-v:pattern rejects matching lines", func(t *testing.T) {
		s, err := grepBuilder("^debug:", false)
		if err != nil {
			t.Fatalf("grepBuilder(grep-v): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("grep-v Apply: %v", err)
		}
		outLines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(outLines) != 2 {
			t.Fatalf("output has %d lines, want 2 (info + warn)", len(outLines))
		}
		for _, l := range outLines {
			if strings.HasPrefix(l, "debug:") {
				t.Errorf("grep-v kept a debug line: %q", l)
			}
		}
	})

	t.Run("grep-v via registry resolves", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)
		s, ok, err := reg.Lookup("grep-v:^debug:")
		if err != nil || !ok || s == nil {
			t.Fatalf("Lookup(grep-v:^debug:) = (%v, %v, err=%v), want resolved", s, ok, err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("grep-v Apply: %v", err)
		}
		if strings.Contains(string(out), "debug:") {
			t.Errorf("grep-v output still contains debug lines: %q", out)
		}
	})

	t.Run("invalid regex returns error", func(t *testing.T) {
		_, err := grepBuilder("(unterminated", false)
		if err == nil {
			t.Fatal("grepBuilder(grep-v, invalid) succeeded; want error")
		}
	})

	t.Run("grep-v that rejects all → empty output", func(t *testing.T) {
		// Every line starts with something; reject all via a pattern
		// matching any line start.
		s, err := grepBuilder("^", false)
		if err != nil {
			t.Fatalf("grepBuilder: %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("grep-v Apply: %v", err)
		}
		if out != nil {
			t.Errorf("grep-v rejecting all = %v, want nil", out)
		}
	})
}

// TestTokenBudget covers the token-budget stage.
func TestTokenBudget(t *testing.T) {
	t.Run("token-budget:100 truncates ~500-token input to ~100 tokens + marker", func(t *testing.T) {
		// 2000 chars of 'a' on lines of 40 chars each (~500 tokens).
		var sb strings.Builder
		for i := 0; i < 50; i++ {
			sb.WriteString(strings.Repeat("a", 39))
			sb.WriteByte('\n')
		}
		input := []byte(sb.String()) // 2000 chars
		s, err := tokenBudgetBuilder("100")
		if err != nil {
			t.Fatalf("tokenBudgetBuilder(100): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("token-budget Apply: %v", err)
		}
		// Marker present and names the original token estimate (500).
		marker := "[ktx: truncated ~100 tokens from 500]"
		if !bytes.Contains(out, []byte(marker)) {
			t.Errorf("output missing marker %q; got (len %d) %q", marker, len(out), string(out[:min(80, len(out))]))
		}
		// Kept content (before marker) snapped back to a newline and
		// is at most ~400 chars (100 tokens * 4).
		kept := out
		if idx := bytes.Index(out, []byte(marker)); idx >= 0 {
			kept = out[:idx]
		}
		if len(kept) > 400 {
			t.Errorf("kept content = %d chars, want <= 400 (~100 tokens)", len(kept))
		}
		// No mid-line cut: kept ends in a newline (snapped back).
		if len(kept) > 0 && kept[len(kept)-1] != '\n' {
			t.Errorf("kept content not snapped to a newline boundary: %q", kept[len(kept)-1:])
		}
		// Output shrunk (gate will let it through).
		if len(out) >= len(input) {
			t.Errorf("output (%d) not smaller than input (%d)", len(out), len(input))
		}
	})

	t.Run("token-budget:2000 on ~125-token input returns unchanged", func(t *testing.T) {
		input := bytes.Repeat([]byte("a"), 500) // ~125 tokens
		s, err := tokenBudgetBuilder("2000")
		if err != nil {
			t.Fatalf("tokenBudgetBuilder(2000): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("token-budget Apply: %v", err)
		}
		if !bytes.Equal(out, input) {
			t.Errorf("under-budget input should be unchanged; got (len %d) want (len %d)", len(out), len(input))
		}
	})

	t.Run("bare token-budget uses default 2000", func(t *testing.T) {
		s, err := tokenBudgetBuilder("")
		if err != nil {
			t.Fatalf("tokenBudgetBuilder(''): %v", err)
		}
		// Under default budget → unchanged.
		short := []byte("tiny")
		out, err := s.Apply(short)
		if err != nil {
			t.Fatalf("token-budget Apply: %v", err)
		}
		if !bytes.Equal(out, short) {
			t.Errorf("bare token-budget on tiny = %q, want %q", out, short)
		}
	})

	t.Run("malformed arg returns error", func(t *testing.T) {
		for _, bad := range []string{"abc", "-1", "0"} {
			_, err := tokenBudgetBuilder(bad)
			if err == nil {
				t.Errorf("tokenBudgetBuilder(%q) succeeded; want error", bad)
			}
		}
	})

	t.Run("empty input → empty output", func(t *testing.T) {
		s, err := tokenBudgetBuilder("100")
		if err != nil {
			t.Fatalf("tokenBudgetBuilder(100): %v", err)
		}
		out, err := s.Apply(nil)
		if err != nil {
			t.Fatalf("token-budget Apply(nil): %v", err)
		}
		if out != nil {
			t.Errorf("token-budget on empty = %v, want nil", out)
		}
	})

	t.Run("input with no newlines still truncates + marks", func(t *testing.T) {
		// A single long line (no newline): snap finds no newline, so
		// a mid-line cut is unavoidable. The marker still appends.
		input := bytes.Repeat([]byte("x"), 2000)
		s, err := tokenBudgetBuilder("100")
		if err != nil {
			t.Fatalf("tokenBudgetBuilder(100): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("token-budget Apply: %v", err)
		}
		if !bytes.Contains(out, []byte("from 500")) {
			t.Errorf("no-newline input missing 'from 500' marker: %q", string(out[:min(60, len(out))]))
		}
		if !bytes.HasPrefix(out, bytes.Repeat([]byte("x"), 400)) {
			t.Errorf("no-newline input did not retain the first 400 bytes")
		}
	})

	t.Run("estimates tokens from byte length", func(t *testing.T) {
		input := bytes.Repeat([]byte("界"), 200) // 600 bytes, 200 runes
		s, err := tokenBudgetBuilder("100")
		if err != nil {
			t.Fatalf("tokenBudgetBuilder(100): %v", err)
		}
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("token-budget Apply: %v", err)
		}
		if !bytes.Contains(out, []byte("from 150")) {
			t.Errorf("multibyte input missing byte-based token estimate: %q", string(out[len(out)-min(80, len(out)):]))
		}
	})
}

// TestPipeline_EndToEnd wires the real-world default pipeline
// (strip-ansi + grep-v + keep-ends + token-budget) through a fresh
// registry and asserts it meaningfully shrinks a verbose build-log-
// style input. This is the deterministic compression the pipeline
// promises: zero ML, pure stdlib, the anti-worsening gate lets the
// result through because the net output is smaller than the input.
func TestPipeline_EndToEnd(t *testing.T) {
	reg := pipeline.NewRegistry()
	RegisterBuiltins(reg)

	// A verbose build-log-style input: ANSI color codes, debug noise,
	// 120 lines, well over the token budget.
	var sb strings.Builder
	sb.WriteString("\x1b[31merror\x1b[0m: build failed\n")
	for i := 0; i < 50; i++ {
		sb.WriteString("debug: tracing step ")
		sb.WriteString(itoa(i))
		sb.WriteByte('\n')
	}
	for i := 0; i < 60; i++ {
		sb.WriteString("compile: unit ")
		sb.WriteString(itoa(i))
		sb.WriteString(" ok\n")
	}
	sb.WriteString("\x1b[31mfatal\x1b[0m: linker error at end\n")
	input := []byte(sb.String())

	r := &pipeline.Runner{
		Registry: reg,
		Pipeline: []string{"strip-ansi", "grep-v:^debug:", "keep-ends:50:20", "token-budget:2000"},
	}
	res := r.Run(input)

	if len(res.Out) == 0 {
		t.Fatal("output empty; want a non-empty compressed result")
	}
	if len(res.Out) >= len(input) {
		t.Errorf("output (%d bytes) not smaller than input (%d); the pipeline should shrink a verbose build log",
			len(res.Out), len(input))
	}
	// The anti-worsening gate did NOT fire (result smaller than input),
	// so Ran records the stages that ran.
	if len(res.Ran) == 0 {
		t.Errorf("Ran empty; want the stages recorded (gate should not fire on a shrink)")
	}
	// debug lines must be gone (grep-v).
	if bytes.Contains(res.Out, []byte("debug:")) {
		t.Errorf("output still contains debug: lines (grep-v:^debug: should have stripped them)")
	}
	// ANSI codes must be gone (strip-ansi).
	if bytes.Contains(res.Out, []byte("\x1b[")) {
		t.Errorf("output still contains ANSI escape codes (strip-ansi should have removed them)")
	}
	// The fatal error at the end is preserved (keep-ends keeps the tail).
	if !bytes.Contains(res.Out, []byte("linker error")) {
		t.Errorf("output lost the bottom-of-log error (keep-ends should preserve the tail)")
	}
}

// TestRegisterBuiltins covers the registry-wiring contract: after
// RegisterBuiltins runs on a fresh registry, all built-in stage names
// resolve. Simple stages resolve bare-name; parameterised stages
// resolve bare-name (default) and `name:arg` (configured). Unknown
// names do not resolve (so the runner's invariant (3) skips them).
func TestRegisterBuiltins(t *testing.T) {
	t.Run("all built-ins resolve under their bare names", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)

		// grep and grep-v REQUIRE a pattern; the bare-name form is a
		// recognised-but-bad-arg (ok=true, err!=nil, s=nil) which the
		// runner treats as a stage-execution-error passthrough. Every
		// other built-in resolves to a usable Stage on the bare form.
		requireArg := map[string]bool{"grep": true, "grep-v": true, "replace": true}
		for _, name := range BuiltinStageNames() {
			s, ok, err := reg.Lookup(name)
			if !ok {
				t.Errorf("Lookup(%q) not registered", name)
				continue
			}
			if requireArg[name] {
				if err == nil {
					t.Errorf("Lookup(%q) bare form returned nil error; want empty-pattern error", name)
				}
				if s != nil {
					t.Errorf("Lookup(%q) bare form returned a Stage; want nil (arg required)", name)
				}
			} else {
				if err != nil {
					t.Errorf("Lookup(%q) err = %v", name, err)
				}
				if s == nil {
					t.Errorf("Lookup(%q) returned nil Stage", name)
				}
			}
		}
	})

	t.Run("BuiltinStageNames includes the four deterministic stages", func(t *testing.T) {
		want := map[string]bool{
			"keep-ends":    true,
			"grep":         true,
			"grep-v":       true,
			"token-budget": true,
			"replace":      true,
		}
		for _, name := range BuiltinStageNames() {
			delete(want, name)
		}
		if len(want) > 0 {
			t.Errorf("BuiltinStageNames missing: %v", want)
		}
	})

	t.Run("head:100 resolves to a configured head stage", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)

		s, ok, err := reg.Lookup("head:100")
		if err != nil {
			t.Fatalf("Lookup(head:100): %v", err)
		}
		if !ok {
			t.Fatal("Lookup(head:100) not registered")
		}
		// Apply on a 200-line input should keep only the first 100.
		lines := make([]string, 200)
		for i := range lines {
			lines[i] = "l" + itoa(i)
		}
		input := []byte(strings.Join(lines, "\n") + "\n")
		out, err := s.Apply(input)
		if err != nil {
			t.Fatalf("head:100 Apply: %v", err)
		}
		wantFirst100 := strings.Join(lines[:100], "\n") + "\n"
		if string(out) != wantFirst100 {
			t.Errorf("head:100 kept wrong number of lines (len %d)", len(out))
		}
	})

	t.Run("truncate:8192 resolves to a configured truncate stage", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)

		s, ok, err := reg.Lookup("truncate:8192")
		if err != nil {
			t.Fatalf("Lookup(truncate:8192): %v", err)
		}
		if !ok {
			t.Fatal("Lookup(truncate:8192) not registered")
		}
		// A short input (well under 8192) should be passthrough.
		short := []byte("small")
		out, err := s.Apply(short)
		if err != nil {
			t.Fatalf("truncate:8192 Apply: %v", err)
		}
		if !bytes.Equal(out, short) {
			t.Errorf("truncate:8192 on short = %q, want %q", out, short)
		}
	})

	t.Run("unknown name does not resolve", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)
		_, ok, err := reg.Lookup("frobnicate")
		if ok {
			t.Error("Lookup(frobnicate) succeeded; want not-registered")
		}
		if err != nil {
			t.Errorf("Lookup(frobnicate) err = %v, want nil (unknown is not an error)", err)
		}
	})

	t.Run("malformed parameterised arg returns error", func(t *testing.T) {
		reg := pipeline.NewRegistry()
		RegisterBuiltins(reg)
		_, ok, err := reg.Lookup("head:abc")
		if !ok {
			t.Error("Lookup(head:abc) reported not-registered; want recognised-but-bad-arg")
		}
		if err == nil {
			t.Error("Lookup(head:abc) returned nil error; want parse error")
		}
	})

	t.Run("nil registry is a no-op (defensive)", func(t *testing.T) {
		// Should not panic.
		RegisterBuiltins(nil)
	})
}

// TestParseNameArg covers the token splitter: bare name → (name, "");
// `name:arg` → (name, arg); first-colon-only split.
func TestParseNameArg(t *testing.T) {
	for _, tc := range []struct {
		token    string
		wantName string
		wantArg  string
	}{
		{token: "strip-ansi", wantName: "strip-ansi", wantArg: ""},
		{token: "head:100", wantName: "head", wantArg: "100"},
		{token: "head", wantName: "head", wantArg: ""},
		{token: ":arg-only", wantName: "", wantArg: "arg-only"},
		{token: "name:arg:with:colons", wantName: "name", wantArg: "arg:with:colons"},
		{token: "", wantName: "", wantArg: ""},
	} {
		t.Run("token="+tc.token, func(t *testing.T) {
			gotName, gotArg := ParseNameArg(tc.token)
			if gotName != tc.wantName || gotArg != tc.wantArg {
				t.Errorf("ParseNameArg(%q) = (%q, %q), want (%q, %q)",
					tc.token, gotName, gotArg, tc.wantName, tc.wantArg)
			}
		})
	}
}

// itoa is a thin strconv.Itoa wrapper so the test file imports leanly.
func itoa(n int) string { return strconv.Itoa(n) }
