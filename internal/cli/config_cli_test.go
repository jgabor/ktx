package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func configTestEnv(t *testing.T) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func runConfigDispatch(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runDispatch("config", args, false, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestConfigShow_JSON_NoKtxToml(t *testing.T) {
	configTestEnv(t)

	code, stdout, stderr := runConfigDispatch(t, []string{"show"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode JSON: %v\nstdout=%s", err, stdout)
	}
	profiles, ok := env["profiles"].(map[string]any)
	if !ok {
		t.Fatalf("missing 'profiles' map: %v", env)
	}
	def, ok := profiles["default"].(map[string]any)
	if !ok {
		t.Fatalf("missing 'default' profile: %v", profiles)
	}
	stages, ok := def["stages"].([]any)
	if !ok {
		t.Fatalf("default.stages not an array: %v", def)
	}
	if len(stages) != 0 {
		t.Errorf("default stages = %v, want empty", stages)
	}
}

func TestConfigShow_JSON_WithStages(t *testing.T) {
	configTestEnv(t)
	os.WriteFile("ktx.toml", []byte(`[pipeline.default]
stages = ["strip-ansi", "head:5"]
`), 0o644)

	code, stdout, stderr := runConfigDispatch(t, []string{"show"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var env map[string]any
	json.Unmarshal([]byte(stdout), &env)
	profiles := env["profiles"].(map[string]any)
	def := profiles["default"].(map[string]any)
	stages := def["stages"].([]any)
	if len(stages) != 2 {
		t.Fatalf("stages = %v, want 2", stages)
	}
	if stages[0] != "strip-ansi" {
		t.Errorf("stages[0] = %v, want strip-ansi", stages[0])
	}
	if stages[1] != "head:5" {
		t.Errorf("stages[1] = %v, want head:5", stages[1])
	}
}

func TestConfigShow_JSON_MultipleProfiles(t *testing.T) {
	configTestEnv(t)
	os.WriteFile("ktx.toml", []byte(`[pipeline.default]
stages = ["strip-ansi"]

[pipeline.json]
stages = ["normalize"]

[pipeline.logs]
stages = ["dedup-blank", "truncate:2048"]
`), 0o644)

	code, stdout, stderr := runConfigDispatch(t, []string{"show"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var env map[string]any
	json.Unmarshal([]byte(stdout), &env)
	profiles := env["profiles"].(map[string]any)
	if len(profiles) < 3 {
		t.Fatalf("profiles count = %d, want >= 3", len(profiles))
	}
	for _, name := range []string{"default", "json", "logs"} {
		if _, ok := profiles[name]; !ok {
			t.Errorf("profile %q missing from output", name)
		}
	}
}

func TestConfigShow_Text(t *testing.T) {
	configTestEnv(t)
	os.WriteFile("ktx.toml", []byte(`[pipeline.default]
stages = ["strip-ansi", "head:5"]
`), 0o644)

	code, stdout, stderr := runConfigDispatch(t, []string{"show", "--format", "text"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "default") {
		t.Errorf("text output missing 'default':\n%s", stdout)
	}
	if !strings.Contains(stdout, "strip-ansi") {
		t.Errorf("text output missing 'strip-ansi':\n%s", stdout)
	}
}

func TestConfigHelp(t *testing.T) {
	var stdout bytes.Buffer
	code := renderHelp(printConfigHelp, &stdout)
	if code != ExitOK {
		t.Fatalf("renderHelp exit=%d", code)
	}
	help := stdout.String()
	for _, want := range []string{"show", "ktx plugin list"} {
		if !strings.Contains(help, want) {
			t.Errorf("help text missing %q:\n%s", want, help)
		}
	}
}
