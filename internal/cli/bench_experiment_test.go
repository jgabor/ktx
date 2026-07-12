package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/config"
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

// TestBenchAgentPreflightResolvesStagesWithRouteRules covers the bench
// harness preflight: the config path check, Load, and PipelineDefault
// comparison (the same calls runBenchAgent makes) still resolve stages
// correctly when ktx.toml also contains [[route]] blocks.
func TestBenchAgentPreflightResolvesStagesWithRouteRules(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "ktx", "ktx.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`[pipeline.default]
stages = ["strip-ansi", "head:2"]

[pipeline.logs]
stages = ["head:50"]

[[route]]
tool = "bash"
command_prefix = "git"
exit_status = "success"
profile = "logs"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	// Preflight path: config.ConfigPath exists, config.Load succeeds,
	// cfg.PipelineDefault matches expected stages.
	if _, exists := config.ConfigPath(); !exists {
		t.Fatal("config.ConfigPath() not found despite ktx.toml with route rules")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() failed with route rules: %v", err)
	}
	expected := []string{"strip-ansi", "head:2"}
	if !slices.Equal(cfg.PipelineDefault, expected) {
		actual, _ := json.Marshal(cfg.PipelineDefault)
		t.Fatalf("PipelineDefault mismatch: expected=%s actual=%s", `["strip-ansi","head:2"]`, string(actual))
	}
	if len(cfg.Routes) != 1 {
		t.Errorf("Routes = %v, want 1 rule (route rules must not break stage resolution)", cfg.Routes)
	}
}
