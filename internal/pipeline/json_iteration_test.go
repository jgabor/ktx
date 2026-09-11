package pipeline

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"testing"
	"unicode/utf8"
)

func splitJSONOracle(input []byte) ([]byte, int, bool) {
	if len(input) == 0 || !utf8.Valid(input) {
		return nil, 0, false
	}
	if out, ok := compactJSON(input); ok {
		return out, 1, true
	}
	lines := bytes.Split(input, []byte{'\n'})
	final := len(lines) > 1 && len(lines[len(lines)-1]) == 0
	if final {
		lines = lines[:len(lines)-1]
	}
	var out bytes.Buffer
	for i, line := range lines {
		if len(line) == 0 {
			return nil, 0, false
		}
		compact, ok := compactJSON(line)
		if !ok {
			return nil, 0, false
		}
		if i > 0 {
			out.WriteByte('\n')
		}
		out.Write(compact)
	}
	if final {
		out.WriteByte('\n')
	}
	return out.Bytes(), len(lines), true
}

func recognitionOracle(input []byte) (recognizedJSON, bool) {
	if len(input) == 0 || !utf8.Valid(input) {
		return recognizedJSON{}, false
	}
	if compact, records, ok := splitJSONOracle(input); ok {
		shape := jsonSingle
		if records != 1 {
			shape = jsonLines
		}
		return recognizedJSON{shape: shape, faithful: compact}, true
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	var value json.RawMessage
	if dec.Decode(&value) != nil {
		return recognizedJSON{}, false
	}
	offset := dec.InputOffset()
	trailer := input[offset:]
	if len(trimJSONSpace(trailer)) == 0 {
		return recognizedJSON{}, false
	}
	if json.NewDecoder(bytes.NewReader(trailer)).Decode(&value) == nil {
		return recognizedJSON{}, false
	}
	compact, ok := compactJSON(input[:offset])
	if !ok {
		return recognizedJSON{}, false
	}
	return recognizedJSON{shape: jsonWithTrailer, faithful: append(compact, trailer...)}, true
}

func TestIncrementalJSONRecognitionOracle(t *testing.T) {
	large := append([]byte(`{"value":"`), bytes.Repeat([]byte("x"), 2<<20)...)
	large = append(large, []byte(`"}`)...)
	cases := [][]byte{nil, []byte("\n"), []byte("\r\n"), large, append(bytes.Clone(large), []byte("\n{ \"n\": 2 }\n")...)}
	atoms := []string{"", " ", "\t", "\r", "\n", "null", "{}", "[]", "1", "true", `"é"`, `"\ud800"`, "{ \"a\": 1 }", "text trailer", "\xff"}
	for _, a := range atoms {
		for _, sep := range []string{"", "\n", "\r\n", " \n\n"} {
			for _, z := range atoms {
				cases = append(cases, []byte(a+sep+z))
			}
		}
	}
	rng := rand.New(rand.NewPCG(11, 29))
	for range 1000 {
		var in []byte
		for range rng.IntN(15) {
			in = append(in, atoms[rng.IntN(len(atoms))]...)
		}
		cases = append(cases, in)
	}
	reg := NewRegistry()
	reg.Register("cut", StageFunc(func(in []byte) ([]byte, error) {
		if len(in) > 4 {
			return bytes.Clone(in[:4]), nil
		}
		return in, nil
	}))
	for i, input := range cases {
		original := bytes.Clone(input)
		got, count, ok := CompactJSON(input)
		want, wantCount, wantOK := splitJSONOracle(input)
		if !bytes.Equal(got, want) || count != wantCount || ok != wantOK {
			t.Fatalf("compact case %d", i)
		}
		shape, recognized := recognizeJSON(input)
		oldShape, oldRecognized := recognitionOracle(input)
		if recognized != oldRecognized || !reflect.DeepEqual(shape, oldShape) {
			t.Fatalf("recognize case %d", i)
		}
		// Apply the previous guard and anti-worsening ordering to a truncation.
		out, ran, candidate := applyStageWithWarn(StageFunc(func(in []byte) ([]byte, error) {
			if len(in) > 4 {
				return bytes.Clone(in[:4]), nil
			}
			return in, nil
		}), "cut", input, &Runner{})
		expected := Result{Out: out}
		if ran {
			expected.Ran = []string{"cut"}
		}
		if oldRecognized && !oldShape.accepts(out) {
			expected.Out = oldShape.faithful
			expected.StructuredFallback = true
		}
		if len(expected.Out) >= len(input) {
			expected = Result{Out: input, AntiWorseningRejected: candidate}
		}
		actual := (&Runner{Registry: reg, Pipeline: []string{"cut"}}).Run(input)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("runner case %d", i)
		}
		if empty := (&Runner{}).Run(input); !reflect.DeepEqual(empty, Result{Out: input}) {
			t.Fatalf("empty runner case %d", i)
		}
		if !bytes.Equal(original, input) {
			t.Fatalf("mutated case %d", i)
		}
	}
}
