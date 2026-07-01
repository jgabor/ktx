package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func hookTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	path := os.Getenv("KTX_DB")
	if path == "" {
		path = t.TempDir() + "/ktx.db"
		t.Setenv("KTX_DB", path)
	}
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func hookChdirTemp(t *testing.T) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func hookWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestHook_Identity(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"hooktest01","tool":"bash","input":"echo hello","output":"hello\nline 2\nline 3\nline 4\nline 5\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.Output != "hello\nline 2\nline 3\nline 4\nline 5\n" {
		t.Errorf("output = %q, want original (identity)", env.Output)
	}
	if env.CompressedID != nil {
		t.Errorf("compressed_id = %v, want null (no pipeline)", env.CompressedID)
	}
	if env.SourceID != "hooktest01" {
		t.Errorf("source_id = %q, want hooktest01", env.SourceID)
	}

	source, err := d.GetToolCall("hooktest01")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if source.Output != "hello\nline 2\nline 3\nline 4\nline 5\n" {
		t.Errorf("persisted output = %q, want original", source.Output)
	}
}

func TestHook_WithPipeline(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	hookWriteFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:3"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"hookpipe01","tool":"bash","input":"echo hello","output":"line 1\nline 2\nline 3\nline 4\nline 5\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if !strings.HasPrefix(env.Output, "line 1\nline 2\nline 3") {
		t.Errorf("output = %q, want first 3 lines", env.Output)
	}
	if strings.Contains(env.Output, "line 4") {
		t.Errorf("output should NOT contain line 4: %q", env.Output)
	}
	if env.CompressedID == nil {
		t.Fatal("compressed_id is null; want a compressed row id")
	}

	source, _ := d.GetToolCall("hookpipe01")
	if source.Output != "line 1\nline 2\nline 3\nline 4\nline 5\n" {
		t.Errorf("persisted original = %q, want full 5 lines", source.Output)
	}

}

func TestHook_D15Gate(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	hookWriteFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:3"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"hookerr001","tool":"bash","input":"false","output":"Error: failed","content_hash":"","timestamp":"2026-07-01T12:00:00Z","is_error":true}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	if !strings.Contains(stderr.String(), "failure") {
		t.Errorf("stderr should contain failure warning: %q", stderr.String())
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.Output != "Error: failed" {
		t.Errorf("output = %q, want original (D15 gate skips compression)", env.Output)
	}
	if env.CompressedID != nil {
		t.Errorf("compressed_id = %v, want null (D15 gate, no compressed row)", env.CompressedID)
	}
}

func TestHook_MalformedJSON(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader("not json"), d, &stdout, &stderr)
	if err == nil {
		t.Fatal("hookLogic succeeded; want usage error")
	}
	ktxErr, ok := err.(*Error)
	if !ok || ktxErr.Code != CodeUsage {
		t.Fatalf("error = %v, want usage error", err)
	}
}

func TestHook_MissingFields(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{"missing id", `{"tool":"bash","output":"hi","timestamp":"2026-07-01T12:00:00Z"}`, "id"},
		{"missing tool", `{"id":"abc","output":"hi","timestamp":"2026-07-01T12:00:00Z"}`, "tool"},
		{"missing timestamp", `{"id":"abc","tool":"bash","output":"hi"}`, "timestamp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			_, err := hookLogic(strings.NewReader(tc.payload), d, &stdout, &stderr)
			if err == nil {
				t.Fatal("hookLogic succeeded; want usage error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHook_Reversibility(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	hookWriteFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:2"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"hookrev001","tool":"bash","input":"echo","output":"line 1\nline 2\nline 3\nline 4\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	json.Unmarshal(stdout.Bytes(), &env)
	if env.CompressedID == nil {
		t.Fatal("compressed_id is null")
	}

	source, _ := d.GetToolCall("hookrev001")
	if source.Output != "line 1\nline 2\nline 3\nline 4\n" {
		t.Errorf("original = %q, want full 4 lines", source.Output)
	}
}

func TestHook_NoInput(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(""), d, &stdout, &stderr)
	if err == nil {
		t.Fatal("hookLogic succeeded; want usage error for empty stdin")
	}
}
