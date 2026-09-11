package pipeline

import (
	"bytes"
	"testing"
)

func FuzzJSONRecognitionOracle(f *testing.F) {
	for _, seed := range []string{"", "\xff", "null", "{", "{}\n[]\n", "{}\n\n[]", "{ \"n\": 1 }\ntrailer", "{} {}", `"\ud800"`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		// Bound test work only; product acceptance is unchanged.
		if len(input) > 4096 {
			t.Skip()
		}
		original := bytes.Clone(input)
		got, ok := recognizeJSON(input)
		want, wantOK := recognitionOracle(input)
		if ok != wantOK || (ok && (got.shape != want.shape || !bytes.Equal(got.faithful, want.faithful))) {
			t.Fatal("recognition differs from whole-input oracle")
		}
		if !bytes.Equal(input, original) {
			t.Fatal("recognition mutated input")
		}
	})
}
