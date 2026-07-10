package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchAgentRejectsStageMismatchBeforeAgentRun(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "ktx", "ktx.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[pipeline.default]\nstages = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(root, "auth.json")
	if err := os.WriteFile(auth, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("OPENCODE_AUTH_PATH", auth)
	t.Setenv("KTX_EXPECTED_STAGES_JSON", `["head:2"]`)
	var stdout, stderr bytes.Buffer
	code := runBenchAgent([]string{"-m", "neuralwatt/test", "do work"}, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "configured stages mismatch") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}
