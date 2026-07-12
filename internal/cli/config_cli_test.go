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
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "head:5"]
`)

	code, stdout, stderr := runConfigDispatch(t, []string{"show"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
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

// TestConfigShow_JSON_DeterministicStages covers a ktx.toml
// referencing the four deterministic stages surfaces them in the active
// pipeline.profiles.default.stages array via `ktx config show --json`.
func TestConfigShow_JSON_DeterministicStages(t *testing.T) {
	configTestEnv(t)
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "grep-v:^debug:", "keep-ends:50:20", "token-budget:2000"]
`)

	code, stdout, stderr := runConfigDispatch(t, []string{"show"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	profiles := env["profiles"].(map[string]any)
	def := profiles["default"].(map[string]any)
	stages := def["stages"].([]any)
	if len(stages) != 4 {
		t.Fatalf("stages = %v, want 4 (deterministic pipeline)", stages)
	}
	want := []string{"strip-ansi", "grep-v:^debug:", "keep-ends:50:20", "token-budget:2000"}
	for i, w := range want {
		if stages[i] != w {
			t.Errorf("stages[%d] = %v, want %q", i, stages[i], w)
		}
	}
}

func TestConfigShow_JSON_MultipleProfiles(t *testing.T) {
	configTestEnv(t)
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]

[pipeline.json]
stages = ["normalize"]

[pipeline.logs]
stages = ["dedup-blank", "truncate:2048"]
`)

	code, stdout, stderr := runConfigDispatch(t, []string{"show"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
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
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "head:5"]
`)

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

// TestConfigShow_JSON_WithRoutes covers the JSON envelope when route
// rules exist: the "routes" key appears alongside "profiles" without
// changing the shape of existing keys.
func TestConfigShow_JSON_WithRoutes(t *testing.T) {
	configTestEnv(t)
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]

[pipeline.logs]
stages = ["head:50"]

[[route]]
tool = "bash"
command_prefix = "git"
exit_status = "success"
profile = "logs"

[[route]]
tool = "bash"
exit_status = "failure"
profile = "default"
`)

	code, stdout, stderr := runConfigDispatch(t, []string{"show"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout)
	}

	// Existing "profiles" key shape unchanged.
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
	if len(stages) != 1 || stages[0] != "strip-ansi" {
		t.Errorf("default.stages = %v, want [strip-ansi]", stages)
	}

	// "routes" key present with both rules.
	routes, ok := env["routes"].([]any)
	if !ok {
		t.Fatalf("missing 'routes' array: %v", env)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %v, want 2 entries", routes)
	}
	r0, ok := routes[0].(map[string]any)
	if !ok {
		t.Fatalf("routes[0] not an object: %v", routes[0])
	}
	if r0["tool"] != "bash" {
		t.Errorf("routes[0].tool = %v, want bash", r0["tool"])
	}
	if r0["command_prefix"] != "git" {
		t.Errorf("routes[0].command_prefix = %v, want git", r0["command_prefix"])
	}
	if r0["exit_status"] != "success" {
		t.Errorf("routes[0].exit_status = %v, want success", r0["exit_status"])
	}
	if r0["profile"] != "logs" {
		t.Errorf("routes[0].profile = %v, want logs", r0["profile"])
	}

	r1, ok := routes[1].(map[string]any)
	if !ok {
		t.Fatalf("routes[1] not an object: %v", routes[1])
	}
	if r1["exit_status"] != "failure" {
		t.Errorf("routes[1].exit_status = %v, want failure", r1["exit_status"])
	}
	if r1["profile"] != "default" {
		t.Errorf("routes[1].profile = %v, want default", r1["profile"])
	}
}

// TestConfigShow_JSON_NoRoutes covers the no-rules case: routes is an
// empty array ([] not null) and profiles keeps its shape.
func TestConfigShow_JSON_NoRoutes(t *testing.T) {
	configTestEnv(t)
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]
`)

	code, stdout, stderr := runConfigDispatch(t, []string{"show"})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout)
	}
	routes, ok := env["routes"].([]any)
	if !ok {
		t.Fatalf("missing 'routes' array: %v", env)
	}
	if len(routes) != 0 {
		t.Errorf("routes = %v, want empty array", routes)
	}
}
