package stages

import (
	"bytes"
	"testing"

	"github.com/jgabor/ktx/internal/pipeline"
)

func TestJSONCompact(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "object and array",
			input: " { \"items\" : [ 1, true, null ] } ",
			want:  `{"items":[1,true,null]}`,
		},
		{
			name:  "duplicate members preserve order",
			input: "{ \"item\" : 1, \"item\" : 2 }",
			want:  `{"item":1,"item":2}`,
		},
		{
			name:  "numeric lexemes preserve spelling",
			input: "[ -0, 1.2300, 1e+09, 0.0, 1E-9 ]",
			want:  `[-0,1.2300,1e+09,0.0,1E-9]`,
		},
		{
			name:  "escaped whitespace remains escaped",
			input: "{ \"text\" : \"\\t \\n \\r \\u0020\" }",
			want:  `{"text":"\t \n \r \u0020"}`,
		},
		{
			name:  "UTF-8 content",
			input: "{ \"greeting\" : \"héllö 👋\" }",
			want:  `{"greeting":"héllö 👋"}`,
		},
		{
			name:  "pretty JSON",
			input: "{\n\t\"outer\": {\n\t\t\"inner\": [\n\t\t\t\"one\",\n\t\t\t\"two\"\n\t\t]\n\t}\n}\n",
			want:  `{"outer":{"inner":["one","two"]}}`,
		},
		{
			name:  "compact no-op",
			input: `{"already":"compact"}`,
			want:  `{"already":"compact"}`,
		},
		{
			name:  "NDJSON with trailing newline",
			input: "{ \"first\" : 1 }\n[ true, false ]\n",
			want:  "{\"first\":1}\n[true,false]\n",
		},
		{
			name:  "single JSON final newline is surrounding whitespace",
			input: "{ \"one\" : 1 }\n",
			want:  `{"one":1}`,
		},
		{
			name:  "single JSON permits trailing JSON whitespace",
			input: " { \"one\" : 1 }\n\n",
			want:  `{"one":1}`,
		},
		{
			name:  "malformed JSON passes through",
			input: `{"broken": }`,
			want:  `{"broken": }`,
		},
		{
			name:  "trailing text passes through",
			input: `{"one":1} trailing`,
			want:  `{"one":1} trailing`,
		},
		{
			name:  "blank NDJSON record is ambiguous",
			input: "{\"one\":1}\n\n{\"two\":2}",
			want:  "{\"one\":1}\n\n{\"two\":2}",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "non-empty whitespace cannot become empty",
			input: " \t\r\n",
			want:  " \t\r\n",
		},
		{
			name:  "invalid UTF-8 passes through",
			input: string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}),
			want:  string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(tt.input)
			got, err := JSONCompact(input)
			if err != nil {
				t.Fatalf("JSONCompact(%q): %v", tt.input, err)
			}
			if !bytes.Equal(got, []byte(tt.want)) {
				t.Errorf("JSONCompact(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if len(input) > 0 && len(got) == 0 {
				t.Error("JSONCompact returned empty output for non-empty input")
			}
		})
	}
}

func TestJSONCompactPipelineGate(t *testing.T) {
	reg := pipeline.NewRegistry()
	RegisterBuiltins(reg)

	for _, tt := range []struct {
		name        string
		input       string
		want        string
		transformed bool
	}{
		{
			name:        "compact JSON is rejected by anti-worsening gate",
			input:       `{"one":1}`,
			want:        `{"one":1}`,
			transformed: false,
		},
		{
			name:        "malformed input remains byte-identical",
			input:       `{"one":}`,
			want:        `{"one":}`,
			transformed: false,
		},
		{
			name:        "spaced JSON survives the gate",
			input:       `{ "one" : 1 }`,
			want:        `{"one":1}`,
			transformed: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := (&pipeline.Runner{Registry: reg, Pipeline: []string{"json-compact"}}).Run([]byte(tt.input))
			if !bytes.Equal(res.Out, []byte(tt.want)) {
				t.Errorf("Runner output = %q, want %q", res.Out, tt.want)
			}
			if tt.transformed && len(res.Ran) != 1 {
				t.Errorf("Ran = %v, want json-compact", res.Ran)
			}
			if !tt.transformed && len(res.Ran) != 0 {
				t.Errorf("Ran = %v, want anti-worsening passthrough", res.Ran)
			}
		})
	}
}

func BenchmarkJSONCompact(b *testing.B) {
	input := []byte("{\n  \"results\" : [\n    { \"id\" : 1, \"value\" : \"first item\" },\n    { \"id\" : 2, \"value\" : \"second item\" }\n  ],\n  \"meta\" : { \"total\" : 2 }\n}\n")
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := JSONCompact(input); err != nil {
			b.Fatal(err)
		}
	}
}
