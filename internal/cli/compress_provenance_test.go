package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCompressControlCharacterProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, token, decoded, output string
	}{
		{"ordinary", `replace:long.*::x`, "replace:long.*::x", "x\n"},
		{"tab", `replace:long.*::\t`, "replace:long.*::\t", "\t\n"},
		{"controls", `replace:long.*::\u0000\b\t\n\f\r\u001f`, "replace:long.*::\x00\b\t\n\f\r\x1f", "\x00\b\t\n\f\r\x1f\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			d := compressTestDB(t)
			writeUserConfig(t, `["`+tc.token+`"]`)
			const source = "longlonglonglonglonglonglonglong\n"
			seedSource(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", source)
			var stdout, stderr bytes.Buffer
			if code := runCompressWith([]string{"abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK {
				t.Fatalf("compress exit=%d stderr=%q", code, stderr.String())
			}
			artifact, err := d.GetCompressed(decodeID(t, &stdout))
			if err != nil {
				t.Fatal(err)
			}
			if artifact.Output != tc.output {
				t.Errorf("compressed output = %q, want %q", artifact.Output, tc.output)
			}
			var record struct {
				Profile            string   `json:"profile"`
				Route              string   `json:"route"`
				Stages             []string `json:"stages"`
				StructuredFallback bool     `json:"structured_fallback"`
			}
			if err := json.Unmarshal([]byte(artifact.Pipeline), &record); err != nil {
				t.Fatalf("unreadable persisted provenance %q: %v", artifact.Pipeline, err)
			}
			if record.Profile != "default" || record.Route != "default_fallback" || len(record.Stages) != 1 || record.Stages[0] != tc.decoded || record.StructuredFallback {
				t.Errorf("persisted provenance = %+v", record)
			}
			stdout.Reset()
			if code := runDecompressWith([]string{artifact.ID}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK || stdout.String() != source {
				t.Fatalf("recovery exit=%d output=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}
