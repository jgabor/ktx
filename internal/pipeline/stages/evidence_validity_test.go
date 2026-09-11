package stages

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/pipeline"
)

func TestUnwrapResultOutcomeValidity(t *testing.T) {
	for _, flag := range []string{"false", "true", "null", `"false"`, "0"} {
		t.Run(flag, func(t *testing.T) {
			input := []byte(fmt.Sprintf(`{"content":[{"type":"text","text":"42"}],"structuredContent":{"result":"42"},"isError":%s,"_meta":{"fastmcp":{"wrap_result":true}}}`, flag))
			want := input
			if flag == "false" {
				want = []byte("42")
			}
			out, err := UnwrapResult(input)
			if err != nil || !bytes.Equal(out, want) {
				t.Errorf("stage = %q, %v; want %q", out, err, want)
			}
			reg := pipeline.NewRegistry()
			RegisterBuiltins(reg)
			var warnings bytes.Buffer
			runner := pipeline.Runner{Registry: reg, Warn: &warnings, Pipeline: []string{"unwrap-result"}}
			result := runner.Run(input)
			if !bytes.Equal(result.Out, want) || warnings.Len() != 0 {
				t.Errorf("pipeline = %q, warnings=%q; want %q", result.Out, warnings.String(), want)
			}
			if (len(result.Ran) != 0) != (flag == "false") {
				t.Errorf("retained stages = %v for flag %s", result.Ran, flag)
			}
		})
	}
}

func TestKeepEndsExtremeCounts(t *testing.T) {
	maxInt := strconv.Itoa(int(^uint(0) >> 1))
	input := []byte(strings.Repeat("a sufficiently long line\n", 5))
	for _, arg := range []string{"1:1", maxInt + ":1", "1:" + maxInt, maxInt + ":" + maxInt, "0:1", "1:-1", maxInt + "0:1"} {
		t.Run(arg, func(t *testing.T) {
			invalid := arg == "0:1" || arg == "1:-1" || arg == maxInt+"0:1"
			stage, err := keepEndsBuilder(arg)
			if (err != nil) != invalid {
				t.Fatalf("builder error = %v; invalid=%t", err, invalid)
			}
			want := input
			if arg == "1:1" {
				want = []byte("a sufficiently long line\n[ktx: 3 lines omitted]\na sufficiently long line\n")
			}
			reg := pipeline.NewRegistry()
			RegisterBuiltins(reg)
			var warnings bytes.Buffer
			runner := pipeline.Runner{Registry: reg, Warn: &warnings, Pipeline: []string{"keep-ends:" + arg}}
			result := runner.Run(input)
			if !bytes.Equal(result.Out, want) || strings.Contains(warnings.String(), "panicked") || (warnings.Len() != 0) != invalid {
				t.Errorf("pipeline = %q, warnings=%q; want %q", result.Out, warnings.String(), want)
			}
			if !invalid {
				// Direct invocation also proves safety without runner recovery.
				out, err := stage.Apply(input)
				if err != nil || !bytes.Equal(out, want) {
					t.Errorf("stage = %q, %v; want %q", out, err, want)
				}
			}
		})
	}
}
