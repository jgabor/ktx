package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/binarycap"
	"github.com/jgabor/ktx/internal/config"
)

func TestBenchAgentPreservesAmbientRuntimeProofErrorsBeforeCollectorOpen(t *testing.T) {
	original := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{ExecutablePath: "/tmp/verified-ktx", ExecutableSHA256: testBinaryDigest, BuildVersion: "test", MaxDatabaseSchema: 8}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = original })

	tests := []struct {
		name     string
		proof    string
		class    string
		expected map[string]any
	}{
		{name: "missing", class: "runtime-proof-required"},
		{name: "mismatch", proof: otherRuntimeDigest, class: "runtime-proof-mismatch", expected: map[string]any{
			"expected_sha256": otherRuntimeDigest,
			"actual_sha256":   testBinaryDigest,
			"executable_path": "/tmp/verified-ktx",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dataHome := filepath.Join(root, "data")
			auditPath := filepath.Join(root, "audit.jsonl")
			modelMarker := filepath.Join(root, "model-called")
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			fake := "#!/bin/sh\ntouch \"$KTX_TEST_MODEL_MARKER\"\nexit 99\n"
			if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir)
			t.Setenv("KTX_TEST_MODEL_MARKER", modelMarker)
			t.Setenv("KTX_DB", "")
			t.Setenv("XDG_DATA_HOME", dataHome)
			t.Setenv("KTX_VERIFY_DB_AUDIT", auditPath)
			t.Setenv("KTX_VERIFY_FORBIDDEN_DB_PATHS", "[]")
			t.Setenv(benchcontract.SessionEnv, "aaaaaaaaaaaaaaaa")
			t.Setenv(benchcontract.IntervalEnv, "bbbbbbbbbbbbbbbb")
			t.Setenv(benchcontract.ContractEnv, benchcontract.ContractVersion)
			t.Setenv(benchcontract.CollectorModeEnv, benchcontract.CollectorRequired)

			var stdout, stderr bytes.Buffer
			code := runBenchAgent([]string{"-m", "openai/test", "prompt"}, Options{Format: FormatJSON, ExpectedRuntimeSHA256: test.proof}, &stdout, &stderr)
			if code != ExitConflict {
				t.Fatalf("exit = %d, want %d; stderr=%s", code, ExitConflict, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			var envelope struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error["code"] != string(CodeConflict) || envelope.Error["failure_class"] != test.class || envelope.Error["correction"] != runtimeProofCorrection {
				t.Fatalf("error = %#v", envelope.Error)
			}
			for key, want := range test.expected {
				if envelope.Error[key] != want {
					t.Errorf("error[%q] = %#v, want %#v", key, envelope.Error[key], want)
				}
			}

			file, err := os.Open(auditPath)
			if err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(file)
			var operations []string
			for scanner.Scan() {
				var event struct {
					Operation string `json:"operation"`
				}
				if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
					t.Fatal(err)
				}
				operations = append(operations, event.Operation)
			}
			if err := errors.Join(scanner.Err(), file.Close()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(operations, []string{"resolve"}) {
				t.Fatalf("audit operations = %v, want [resolve]", operations)
			}
			if _, err := os.Stat(filepath.Join(dataHome, "ktx")); !os.IsNotExist(err) {
				t.Fatalf("database directory changed: %v", err)
			}
			if _, err := os.Stat(modelMarker); !os.IsNotExist(err) {
				t.Fatalf("model command was invoked: %v", err)
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
