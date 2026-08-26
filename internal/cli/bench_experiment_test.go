package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/experiment"
)

func TestBenchSummaryConsumersRejectConflictWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	manifest := experiment.Manifest{ExperimentID: "conflict", Methodology: experiment.MethodologyMetadata{Version: "v4"}, Task: experiment.Task{MinimumToolCalls: 1}}
	if err := experiment.WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []experiment.Attempt{
		{SchemaVersion: 1, ExperimentID: "conflict", AttemptID: "baseline-001", Side: experiment.Baseline, Status: "timeout"},
		{SchemaVersion: 1, ExperimentID: "conflict", AttemptID: "candidate-001", Side: experiment.Candidate, Status: "timeout"},
	} {
		if err := experiment.AppendAttempt(dir, attempt); err != nil {
			t.Fatal(err)
		}
	}
	summary := experiment.BuildSummary(manifest, nil)
	baseline, _ := summary.Pipeline(experiment.Baseline)
	baseline["pass_rate"] = 1.0
	baseline["full_pass_rate"] = 0.0
	summaryPath := filepath.Join(dir, "summary.json")
	if err := experiment.WriteJSON(summaryPath, summary); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{name: "validate", run: func(stdout, stderr *bytes.Buffer) int {
			return runBenchValidate([]string{"--experiment", dir, "--format", "json"}, Options{Format: FormatJSON}, stdout, stderr)
		}},
		{name: "compare summarize", run: func(stdout, stderr *bytes.Buffer) int {
			return runBenchCompare([]string{"--summarize", dir, "--format", "json"}, Options{Format: FormatJSON}, stdout, stderr)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := test.run(&stdout, &stderr); code != ExitInternal {
				t.Fatalf("exit code = %d, want %d; stdout=%s stderr=%s", code, ExitInternal, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "conflicting summary baseline pass rates") {
				t.Fatalf("stderr = %q", stderr.String())
			}
			after, err := os.ReadFile(summaryPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("rejected summary was overwritten")
			}
		})
	}
}

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
		"image) if [ \"${KTX_TEST_IMAGE_INSPECT_FAIL:-}\" = 1 ]; then printf '%s\\n' 'image unavailable' >&2; exit 1; fi; printf '%s\\n' 'sha256:image' ;;\n" +
		"run) printf '%s\\n' '" + capabilities + "' ;;\n" +
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

	t.Run("eligible build ignores an unavailable task file", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "ktx.db")
		t.Setenv("KTX_DB", databasePath)
		missingTask := filepath.Join(t.TempDir(), "missing-task.toml")
		var stdout, stderr bytes.Buffer
		code := runBenchCompare([]string{"--preflight", "--build", "--repo-root", root, "--task-file", missingTask, "--format", "json"}, Options{Format: FormatJSON}, &stdout, &stderr)
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

	t.Run("ineligible image ignores direct task input", func(t *testing.T) {
		t.Setenv("KTX_TEST_IMAGE_INSPECT_FAIL", "1")
		databasePath := filepath.Join(t.TempDir(), "ktx.db")
		t.Setenv("KTX_DB", databasePath)
		var stdout, stderr bytes.Buffer
		code := runBenchCompare([]string{"--preflight", "--task-id", "ignored", "--prompt", "ignored", "--expected-facts", "not-json", "--format", "json"}, Options{Format: FormatJSON}, &stdout, &stderr)
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
