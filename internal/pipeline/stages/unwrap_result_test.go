package stages

import (
	"bytes"
	"testing"

	"github.com/jgabor/ktx/internal/pipeline"
)

func TestUnwrapResult_RecognizedEnvelopes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "Kagi result with matching structured content",
			input: `{"content":[{"type":"text","text":"first\nsecond"}],"structuredContent":{"result":"first\nsecond"},"isError":false,"_meta":{"fastmcp":{"wrap_result":true}}}`,
			want:  "first\nsecond",
		},
		{
			name:  "Context7 resolve-library-id result",
			input: `{"content":[{"type":"text","text":"/vercel/next.js"}]}`,
			want:  "/vercel/next.js",
		},
		{
			name:  "Context7 query-docs result",
			input: `{"content":[{"type":"text","text":"# Router\n\nUse café ☕ and 日本語."}]}`,
			want:  "# Router\n\nUse café ☕ and 日本語.",
		},
		{
			name:  "webrun input text result",
			input: `[{"type":"input_text","text":"alpha\nbeta \u263a"}]`,
			want:  "alpha\nbeta ☺",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := UnwrapResult([]byte(tc.input))
			if err != nil {
				t.Fatalf("UnwrapResult() error = %v", err)
			}
			if string(out) != tc.want {
				t.Errorf("UnwrapResult() = %q, want %q", out, tc.want)
			}
		})
	}
}

func TestUnwrapResult_UnsafeOrUnknownInputIsIdentity(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "plain text", input: "ordinary command output\n"},
		{name: "malformed JSON", input: `{"content":[`},
		{name: "trailing non-whitespace", input: `{"content":[{"type":"text","text":"ok"}]} trailing`},
		{name: "second JSON value", input: `{"content":[{"type":"text","text":"ok"}]} {}`},
		{name: "duplicate top-level key", input: `{"content":[{"type":"text","text":"one"}],"content":[{"type":"text","text":"two"}]}`},
		{name: "duplicate content key", input: `{"content":[{"type":"text","text":"one","text":"two"}]}`},
		{name: "unrelated JSON object", input: `{"result":"ok"}`},
		{name: "unrelated JSON array", input: `[{"name":"result","value":"ok"}]`},
		{name: "unknown top-level field", input: `{"content":[{"type":"text","text":"ok"}],"unexpected":true}`},
		{name: "unknown content field", input: `{"content":[{"type":"text","text":"ok","unexpected":true}]}`},
		{name: "unknown structured content field", input: `{"content":[{"type":"text","text":"ok"}],"structuredContent":{"result":"ok","unexpected":true}}`},
		{name: "error result", input: `{"content":[{"type":"text","text":"failure"}],"isError":true}`},
		{name: "incomplete successful result", input: `{"content":[{"type":"text","text":"ok"}],"isError":false}`},
		{name: "empty MCP text", input: `{"content":[{"type":"text","text":""}]}`},
		{name: "empty webrun text", input: `[{"type":"input_text","text":""}]`},
		{name: "multiple MCP items", input: `{"content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}`},
		{name: "multiple webrun items", input: `[{"type":"input_text","text":"one"},{"type":"input_text","text":"two"}]`},
		{name: "non-text MCP item", input: `{"content":[{"type":"image","data":"abc"}]}`},
		{name: "non-input-text webrun item", input: `[{"type":"output_text","text":"ok"}]`},
		{name: "missing MCP text", input: `{"content":[{"type":"text"}]}`},
		{name: "missing webrun text", input: `[{"type":"input_text"}]`},
		{name: "mismatched Kagi duplicate", input: `{"content":[{"type":"text","text":"one"}],"structuredContent":{"result":"two"},"isError":false}`},
		{name: "Kagi wrapper disabled", input: `{"content":[{"type":"text","text":"one"}],"structuredContent":{"result":"one"},"isError":false,"_meta":{"fastmcp":{"wrap_result":false}}}`},
		{name: "non-string Kagi duplicate", input: `{"content":[{"type":"text","text":"one"}],"structuredContent":{"result":1},"isError":false}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.input)
			out, err := UnwrapResult(input)
			if err != nil {
				t.Fatalf("UnwrapResult() error = %v", err)
			}
			if !bytes.Equal(out, input) {
				t.Errorf("UnwrapResult() = %q, want byte-identical input %q", out, input)
			}
		})
	}
}

func TestUnwrapResult_PipelineCannotDiscardJSONStructure(t *testing.T) {
	reg := pipeline.NewRegistry()
	RegisterBuiltins(reg)

	input := []byte(`{"content":[{"type":"text","text":"first\nsecond\nthird\nfourth\nfifth"}]}`)
	runner := &pipeline.Runner{
		Registry: reg,
		Pipeline: []string{"unwrap-result", "keep-ends:1:1"},
	}
	result := runner.Run(input)

	if !bytes.Equal(result.Out, input) {
		t.Errorf("pipeline output = %q, want original JSON %q", result.Out, input)
	}
	if len(result.Ran) != 0 {
		t.Errorf("pipeline stages = %v, want none retained", result.Ran)
	}
	if !result.AntiWorseningRejected {
		t.Error("AntiWorseningRejected = false, want true")
	}
}
