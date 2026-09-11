package stages

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/pipeline"
)

func TestJSONBlock(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"mixed blocks", "Notice\n{\n  \"x\": [ 1, 2 ]\n}\nBetween\n[ true, null ]\nDone\n", "Notice\n{\"x\":[1,2]}\nBetween\n[true,null]\nDone\n"},
		{"surrounding bytes", "Before\r\n \t{\r\n \"x\" : 1\r\n} \t\r\n\r\nAfter", "Before\r\n \t{\"x\":1} \t\r\n\r\nAfter"},
		{"lexical fidelity", `{ "x": -0, "x": 1.2300, "n": 1e+09, "big": 9007199254740993, "s": "\u0020\t héllö" }`, `{"x":-0,"x":1.2300,"n":1e+09,"big":9007199254740993,"s":"\u0020\t héllö"}`},
		{"string delimiters", `{ "s": "} [ \" \\", "a": [ { "b": 1 } ] }`, `{"s":"} [ \" \\","a":[{"b":1}]}`},
		{"fenced block", "```json\n{ \"x\": 1 }\n```\n", "```json\n{\"x\":1}\n```\n"},
		{"invalid then valid", "{\"broken\": }\n{ \"ok\": 1 }\n", "{\"broken\": }\n{\"ok\":1}\n"},
		{"mismatched then valid", "{\n]\n{ \"ok\": 1 }\n", "{\n]\n{\"ok\":1}\n"},
		{"nested invalid", "{\nBAD\n{ \"ok\": 1 }\n}\n", "{\nBAD\n{ \"ok\": 1 }\n}\n"},
		{"unclosed remainder", "{\n{ \"ok\": 1 }\n", "{\n{ \"ok\": 1 }\n"},
		{"compacted before unclosed", "{ \"ok\": 1 }\n[\n[ 2 ]\n", "{\"ok\":1}\n[\n[ 2 ]\n"},
		{"inline prefix", "Result: { \"x\": 1 }\n", "Result: { \"x\": 1 }\n"},
		{"inline suffix", "{\n \"x\": 1\n} trailing\n", "{\n \"x\": 1\n} trailing\n"},
		{"adjacent values", "{ \"x\": 1 } [ 2 ]\n", "{ \"x\": 1 } [ 2 ]\n"},
		{"scalar lines", " 123 \n true \n \"text\" \n", " 123 \n true \n \"text\" \n"},
		{"compact", "{\"x\":1}\n", "{\"x\":1}\n"},
		{"whitespace", " \t\r\n \t", " \t\r\n \t"},
		{"empty", "", ""},
		{"invalid UTF-8", "{ \"x\": 1 }\n\xff", "{ \"x\": 1 }\n\xff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(tt.input)
			got, err := JSONBlock(input)
			if err != nil || string(got) != tt.want {
				t.Fatalf("JSONBlock() = %q, %v; want %q", got, err, tt.want)
			}
			if string(input) != tt.input {
				t.Fatal("mutated input")
			}
			again, err := JSONBlock(got)
			if err != nil || !bytes.Equal(again, got) {
				t.Fatalf("not idempotent: %q, %v", again, err)
			}
		})
	}
}

func TestJSONBlockRunner(t *testing.T) {
	reg := pipeline.NewRegistry()
	RegisterBuiltins(reg)
	for _, input := range []string{
		"Notice\n{\n  \"x\": [ 1, 2 ]\n}\nDone\n",
		"{\n  \"x\": [ 1, 2 ]\n}\nDone\n",
		"{\n  \"x\": [ 1, 2 ]\n}\n",
	} {
		want, _ := JSONBlock([]byte(input))
		res := (&pipeline.Runner{Registry: reg, Pipeline: []string{"json-block"}}).Run([]byte(input))
		if !bytes.Equal(res.Out, want) || len(res.Ran) != 1 || res.Ran[0] != "json-block" {
			t.Fatalf("Runner output = %q, stages = %v; want %q via json-block", res.Out, res.Ran, want)
		}
	}
}

func TestJSONBlockExcessiveDepth(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("[\n", 20000),
		strings.Repeat("[\n", 20000) + "0" + strings.Repeat("]\n", 20000),
	} {
		got, err := JSONBlock([]byte(input))
		if err != nil || string(got) != input {
			t.Fatal("excessively deep candidate must pass through unchanged")
		}
	}
}

func BenchmarkJSONBlockUnclosed(b *testing.B) {
	input := []byte(strings.Repeat("[\n", 100000))
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := JSONBlock(input); err != nil {
			b.Fatal(err)
		}
	}
}
