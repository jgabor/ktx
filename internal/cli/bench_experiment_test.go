package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/config"
)

func TestBenchComparePreflightIsDBFreeAndKeepsMachineOutputSeparated(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	docker := filepath.Join(bin, "docker")
	capabilities := `{"ktx_version":"test","opencode_version":"` + benchcontract.RuntimeVersion + `","generated_plugin_sha256":"` + strings.Repeat("a", 64) + `","generated_plugin_matches_binary":true,"event_collector_contract":"` + benchcontract.ContractVersion + `","explicit_session_ownership":true,"serialized_callbacks":true,"complete_interval_lifecycle":true,"epoch_tracking":true,"strict_acknowledgements":true,"exact_model_step_identity":true,"retained_duplicate_callbacks":true,"accepted_model_step_projection":true,"callback_queue_continuation":true,"fail_closed_interval_validation":true,"preflight_before_auth":true,"normalized_kinds":["collector_open","model_step","retry","compaction","interruption","other"]}`
	script := "#!/bin/sh\nset -eu\ncase \"$1\" in\n" +
		"build) printf '%s\\n' '#0 building image'; printf '%s\\n' '#21 DONE 0.0s' >&2 ;;\n" +
		"image) if [ \"${KTX_TEST_IMAGE_INSPECT_FAIL:-}\" = 1 ]; then printf '%s\\n' 'image unavailable' >&2; exit 1; fi; printf '%s\\n' 'sha256:" + strings.Repeat("a", 64) + "' ;;\n" +
		"run) printf '%s\\n' '" + capabilities + "' ;;\n" +
		"rm) exit 0 ;;\n" +
		"*) exit 64 ;;\nesac\n"
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	assertDatabaseUntouched := func(t *testing.T, path string) {
		t.Helper()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preflight touched database path: %v", err)
		}
	}

	t.Run("eligible build accepts a registered task", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "ktx.db")
		t.Setenv("KTX_DB", databasePath)
		var stdout, stderr bytes.Buffer
		code := runBenchCompare([]string{"--preflight", "--build", "--repo-root", root, "--spec", "exact-match", "--task", "crossfile-nav", "--baseline", "passthrough", "--candidate", "conservative-a", "--format", "json"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("code = %d, stderr = %s", code, stderr.String())
		}
		var report map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("stdout is not clean JSON: %v\n%s", err, stdout.String())
		}
		if report["eligible"] != true {
			t.Fatalf("report = %#v", report)
		}
		if report["model_attempts"] != float64(0) || report["artifact_creation"] != false {
			t.Fatalf("preflight performed work: %#v", report)
		}
		if !strings.Contains(stderr.String(), "#0 building image") || !strings.Contains(stderr.String(), "#21 DONE 0.0s") {
			t.Fatalf("build diagnostics = %q", stderr.String())
		}
		assertDatabaseUntouched(t, databasePath)
	})

	t.Run("ineligible image accepts a registered task", func(t *testing.T) {
		t.Setenv("KTX_TEST_IMAGE_INSPECT_FAIL", "1")
		databasePath := filepath.Join(t.TempDir(), "ktx.db")
		t.Setenv("KTX_DB", databasePath)
		var stdout, stderr bytes.Buffer
		code := runBenchCompare([]string{"--preflight", "--repo-root", root, "--spec", "exact-match", "--task", "crossfile-nav", "--format", "json"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr = %s", code, stderr.String())
		}
		var report map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("stdout is not clean JSON: %v\n%s", err, stdout.String())
		}
		if report["eligible"] != false || report["model_attempts"] != float64(0) || report["artifact_creation"] != false {
			t.Fatalf("report = %#v", report)
		}
		reasons, ok := report["reasons"].([]any)
		if !ok || len(reasons) != 1 || reasons[0] != "image_capability_unavailable" {
			t.Fatalf("reasons = %#v", report["reasons"])
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
		assertDatabaseUntouched(t, databasePath)
	})
}

func TestBenchComparePreflightRejectsUnregisteredSelectorsBeforeEffects(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing spec", []string{"--task", "crossfile-nav"}},
		{"versioned spec", []string{"--spec", "exact-match-v1", "--task", "crossfile-nav"}},
		{"path task", []string{"--spec", "exact-match", "--task", "tools/bench/tasks/crossfile-nav.toml"}},
		{"unknown baseline", []string{"--spec", "exact-match", "--task", "crossfile-nav", "--baseline", "missing"}},
		{"path candidate", []string{"--spec", "exact-match", "--task", "crossfile-nav", "--candidate", "tools/bench/pipelines/conservative-a.toml"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			databasePath := filepath.Join(root, "ktx.db")
			t.Setenv("KTX_DB", databasePath)
			var stdout, stderr bytes.Buffer
			args := append([]string{"--preflight", "--repo-root", root}, test.args...)
			code := runBenchCompare(args, Options{Format: FormatJSON}, &stdout, &stderr)
			if code != ExitUsage {
				t.Fatalf("code = %d, stderr = %s", code, stderr.String())
			}
			if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
				t.Fatalf("database touched: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".ktx", "experiments")); err == nil {
				t.Fatal("experiment root created")
			}
		})
	}
}

func TestBenchRankRequiresSpecBeforeCreatingCampaign(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := runBenchRank([]string{"--model", "provider/model", "--task", "crossfile-nav"}, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), "--spec") {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".ktx", "experiments")); !os.IsNotExist(err) {
		t.Fatalf("experiment root touched: %v", err)
	}
}

func TestBenchCommandsRejectRemovedSelectorFlags(t *testing.T) {
	tests := []struct {
		name string
		run  func([]string, Options, io.Writer, io.Writer) int
		flag string
	}{
		{"run methodology", runBenchRun, "--methodology"},
		{"report methodology", runBenchReport, "--methodology"},
		{"compare task file", runBenchCompare, "--task-file"},
		{"compare baseline config", runBenchCompare, "--baseline-config"},
		{"compare candidate config", runBenchCompare, "--candidate-config"},
		{"compare data dir", runBenchCompare, "--data-dir"},
		{"rank task file", runBenchRank, "--task-file"},
		{"rank data dir", runBenchRank, "--data-dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := tt.run([]string{tt.flag, "legacy"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitUsage {
				t.Fatalf("code = %d, stderr = %s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "flag provided but not defined") {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

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

// TestBenchAgentAcceptsVariantFlag verifies the --variant flag is parsed
// without error. The test goes through the preflight stage-mismatch path
// (same as TestBenchAgentRejectsStageMismatchBeforeAgentRun) so no real
// opencode invocation is needed.
func TestBenchAgentAcceptsVariantFlag(t *testing.T) {
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
	code := runBenchAgent([]string{"-m", "openai/gpt-5.6-luna", "--variant", "high", "do work"}, Options{Format: FormatJSON}, &stdout, &stderr)
	// Should reach the stage-mismatch error, not a flag-parse error
	if code != ExitUsage {
		t.Fatalf("code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "configured stages mismatch") {
		t.Fatalf("stderr should show stage mismatch (not flag error): %s", stderr.String())
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

// TestBenchAgentRejectsCopilotModels verifies that the github-copilot/*
// prohibition is enforced on the directly-callable `ktx bench agent`
// path, not only on the compare/rank paths that go through runner.Compare.
func TestBenchAgentRejectsCopilotModels(t *testing.T) {
	for _, model := range []string{"github-copilot/claude-sonnet-4.5", "github-copilot/gpt-4"} {
		var stdout, stderr bytes.Buffer
		code := runBenchAgent([]string{"-m", model, "do work"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("model %q: code = %d, want %d (stderr=%s)", model, code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "github-copilot") || !strings.Contains(stderr.String(), "--model openai/gpt-5.6-luna --variant high") {
			t.Fatalf("model %q: stderr should identify the prohibition and correction, got: %s", model, stderr.String())
		}
	}
}
