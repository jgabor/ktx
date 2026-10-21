package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	benchdata "github.com/jgabor/ktx/tools/bench"
)

func optimizeTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	optimizeTestEnvironment(t, root)
	return root
}

// optimizeTestEnvironment confines the test to root and makes any provider
// subprocess or network request fail.
func optimizeTestEnvironment(t *testing.T, root string) {
	t.Helper()
	t.Chdir(root)
	t.Setenv("KTX_DB", "")
	for _, name := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, filepath.Join(root, name))
	}
	t.Setenv("PATH", "")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	previous := http.DefaultTransport
	http.DefaultTransport = optimizeNoNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = previous })
}

type optimizeNoNetwork struct{ t *testing.T }

func (n optimizeNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Error("offline optimization attempted an HTTP request")
	return nil, fmt.Errorf("network forbidden in offline test")
}

func optimizeWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func optimizeInvoke(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := runWith(append([]string{"optimize"}, args...), &out, &stderr)
	return code, out.String(), stderr.String()
}

func optimizeRun(t *testing.T, args ...string) optimizeResult {
	t.Helper()
	code, out, stderr := optimizeInvoke(t, args...)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var r optimizeResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.ApplyEligible || r.Outcome == "ready" || r.Scope.WorkloadEvidence || r.Cost.State != "unavailable" || r.RuntimeTokens.State != "unavailable" || r.Bounds.ProviderAttempts != 0 {
		t.Fatalf("offline claim: %+v", r)
	}
	return r
}

func TestOptimizeScreenAndInspection(t *testing.T) {
	root := optimizeTestRoot(t)
	path := filepath.Join(root, "target.toml")
	original := "# keep this byte-identical\n[pipeline.default]\ntimeout = '500ms'\nrecover_footer = true\nstages = []\n[logs]\nlevel = 'off'\n"
	optimizeWrite(t, path, original)
	r := optimizeRun(t, "--config", path)
	if r.Outcome != "screened" || r.SelectedCandidate == nil || len(r.Candidates) != 2 || r.BaselineScreen.Calls != 7 || r.Target.Exists == nil || !*r.Target.Exists || *r.Target.SHA256 != optimizeHash([]byte(original)) {
		t.Fatalf("unexpected result: %+v", r)
	}
	if !r.BaselineScreen.ControlledFactsPass {
		t.Fatal("baseline gates failed")
	}
	for _, c := range r.Candidates {
		if c.Screen == nil || !c.Screen.ControlledFactsPass || c.Screen.Calls != r.Bounds.CasesPerConfig {
			t.Fatalf("candidate: %+v", c)
		}
		data, err := os.ReadFile(filepath.Join(root, ".ktx", "optimizations", r.ID, c.Config.Artifact))
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if _, err := toml.Decode(string(data), &document); err != nil {
			t.Fatal(err)
		}
		if document["logs"].(map[string]any)["level"] != "off" {
			t.Fatal("unrelated logs lost")
		}
		cfg, err := config.LoadFile(filepath.Join(root, ".ktx", "optimizations", r.ID, c.Config.Artifact))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Profiles["default"].Timeout.String() != "500ms" || !config.ResolveRecoverFooter(cfg, "default") {
			t.Fatal("safety settings lost")
		}
	}
	code, showJSON, stderr := optimizeInvoke(t, "show", r.ID)
	if code != 0 {
		t.Fatalf("show: %s", stderr)
	}
	var shown optimizeResult
	if err := json.Unmarshal([]byte(showJSON), &shown); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, shown) {
		t.Fatal("root/show diverged")
	}
	code, text, stderr := optimizeInvoke(t, "show", r.ID, "--format", "text")
	if code != 0 {
		t.Fatalf("text: %s", stderr)
	}
	for _, value := range append(append(append([]string{r.ID, r.Outcome, r.Target.Path, r.Scope.Population, r.Scope.Routing, r.Quality, r.Integrity, r.Cost.Reason, r.RuntimeTokens.Reason, "Apply eligible: false", "3 candidates; 7 cases/config", r.SelectedCandidate.SHA256}, r.Reasons...), r.MissingInputs...), r.NextActions...) {
		if !strings.Contains(text, value) {
			t.Fatalf("text omitted JSON field %q", value)
		}
	}
	var directText bytes.Buffer
	if err := renderOptimizeText(&directText, r); err != nil || directText.String() != text {
		t.Fatal("root/show text diverged")
	}
	for _, secret := range []string{"keep this byte-identical", "recover_footer", "HEAD_FACT", "MIDDLE_FACT", `"command":`, "printf controlled", "controlled.json", "level"} {
		if strings.Contains(showJSON, secret) {
			t.Fatalf("ordinary report exposed %q", secret)
		}
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("target mutated")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "ktx", "ktx.db")); !os.IsNotExist(err) {
		t.Fatal("database touched")
	}
	dir := filepath.Join(root, ".ktx", "optimizations", r.ID)
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("archive not private")
	}
	for _, name := range []string{"result.json", "result.sha256", "baseline.toml", "candidate-1.toml", "population.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private artifact %s", name)
		}
	}
	second := optimizeRun(t, "--config", path)
	if second.ID == r.ID {
		t.Fatal("result identity reused")
	}
	// Current target edits do not rewrite the captured incumbent or inspection.
	optimizeWrite(t, path, "[pipeline.default]\nstages = ['head:1']\n")
	shown, err = loadOptimizeResult(r.ID)
	if err != nil || !reflect.DeepEqual(r, shown) {
		t.Fatal("frozen evidence changed")
	}
}

func TestOptimizeAbsenceAndNoChange(t *testing.T) {
	optimizeTestRoot(t)
	r := optimizeRun(t)
	if r.Target.Exists == nil || *r.Target.Exists || r.Target.SHA256 != nil || r.Baseline == nil || r.Outcome != "screened" {
		t.Fatalf("absence: %+v", r)
	}
	if _, err := os.Stat(r.Target.Path); !os.IsNotExist(err) {
		t.Fatal("absent config created")
	}
	if _, err := os.Lstat(os.Getenv("XDG_CONFIG_HOME")); !os.IsNotExist(err) {
		t.Fatal("offline screening created the config directory")
	}
	optimizeWrite(t, "target.toml", "[pipeline.default]\nstages = ['json-compact', 'strip-ansi', 'dedup-blank', 'normalize']\n")
	r = optimizeRun(t, "--config", "target.toml")
	if r.Outcome != "no_change" || r.SelectedCandidate != nil {
		t.Fatalf("no-change: %+v", r)
	}
}

func TestOptimizeInvalidAndUnsupported(t *testing.T) {
	for _, tt := range []struct{ name, data, outcome, reason string }{
		{"syntax", "[broken", "blocked", "invalid_target_config"},
		{"unknown-key", "[pipeline.default]\nstagess = []\n", "blocked", "invalid_target_config"},
		{"plugin", "[[plugin]]\nname = 'normalize'\ncommand = 'touch SHOULD_NOT_EXIST'\n[pipeline.default]\nstages = ['normalize']\n", "blocked", "external_plugins_unsupported_not_executed"},
		{"stage", "[pipeline.default]\nstages = ['unknown-stage']\n", "blocked", "unsupported_stage_not_executed"},
		{"argument", "[pipeline.default]\nstages = ['head:bad']\n", "blocked", "invalid_or_excessive_stage_arguments_not_executed"},
		{"resource", "[pipeline.default]\nstages = ['token-budget:9223372036854775807']\n", "blocked", "invalid_or_excessive_stage_arguments_not_executed"},
		{"regex", "[pipeline.default]\nstages = ['replace:x::y']\n", "blocked", "unsupported_stage_not_executed"},
		{"outcome-route", "[pipeline.default]\nstages = []\n[[route]]\nexit_status = 'success'\nprofile = 'default'\n", "blocked", "outcome_routing_not_qualified"},
		{"screening-shadow", "[pipeline.default]\nstages = []\n[[route]]\ncommand_prefix = 'printf'\nprofile = 'default'\n", "blocked", "route_shadows_controlled_case"},
		{"qualification-shadow", "[pipeline.default]\nstages = []\n[[route]]\ntool = 'bash'\ncommand_prefix = 'cat'\nprofile = 'default'\n", "blocked", "route_shadows_controlled_case"},
		{"other-tool-shadow", "[pipeline.default]\nstages = []\n[[route]]\ntool = 'read'\nsubcommand = 'x'\nprofile = 'default'\n[[route]]\ncommand_prefix = 'printf controlled'\nprofile = 'default'\n", "blocked", "route_shadows_controlled_case"},
		{"size", strings.Repeat("#", optimizeConfigLimit+1), "incomplete", "target_unreadable_or_exceeds_bound"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			optimizeTestRoot(t)
			optimizeWrite(t, "target.toml", tt.data)
			r := optimizeRun(t, "--config", "target.toml")
			if r.Outcome != tt.outcome || !strings.Contains(strings.Join(r.Reasons, ","), tt.reason) || r.BaselineScreen != nil || len(r.Candidates) != 0 {
				t.Fatalf("not blocked before execution: %+v", r)
			}
			if _, err := os.Stat("SHOULD_NOT_EXIST"); !os.IsNotExist(err) {
				t.Fatal("plugin executed")
			}
			if _, err := loadOptimizeResult(r.ID); err != nil {
				t.Fatalf("blocked show: %v", err)
			}
			code, text, _ := optimizeInvoke(t, "show", r.ID, "--format", "text")
			if code != ExitOK || !strings.Contains(text, tt.reason) || !strings.Contains(text, r.Outcome) || !strings.Contains(text, "Apply eligible: false") {
				t.Fatal("blocked text/JSON disagree")
			}
			data, _ := os.ReadFile("target.toml")
			if string(data) != tt.data {
				t.Fatal("mutated invalid config")
			}
		})
	}
}

func TestOptimizeSuppliedCandidateAndPreservation(t *testing.T) {
	for _, tt := range []struct{ name, data, candidateOutcome string }{
		{"passing", "[pipeline.default]\nstages = ['normalize']\n", "screened"},
		{"fact-loss", "[pipeline.default]\nstages = ['head:1']\n", "rejected"},
		{"invalid", "[broken", "blocked"},
		{"unsupported", "[pipeline.default]\nstages = ['mystery']\n", "blocked"},
		{"no-improvement", "[pipeline.default]\nstages = []\n", "no_change"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			optimizeTestRoot(t)
			optimizeWrite(t, "target.toml", "[pipeline.default]\nstages = []\n")
			optimizeWrite(t, "proposal.toml", tt.data)
			r := optimizeRun(t, "--candidate", "proposal.toml", "--config", "target.toml")
			if len(r.Candidates) != 3 || r.Candidates[2].Outcome != tt.candidateOutcome {
				t.Fatalf("candidate: %+v", r)
			}
			if tt.candidateOutcome == "blocked" && (r.Outcome != "blocked" || r.Candidates[2].Screen != nil || r.SelectedCandidate != nil) {
				t.Fatal("unsupported candidate executed or advanced")
			}
			if tt.candidateOutcome == "rejected" && r.Candidates[2].Screen.ControlledFactsPass {
				t.Fatal("lost facts passed")
			}
			data, _ := os.ReadFile("proposal.toml")
			if string(data) != tt.data {
				t.Fatal("candidate mutated")
			}
			if _, err := loadOptimizeResult(r.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOptimizeRoutingAndPopulationGates(t *testing.T) {
	optimizeTestRoot(t)
	cases, outputs, err := optimizePopulation()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 7 {
		t.Fatal("unbounded population")
	}
	for _, output := range outputs {
		if len(output) > 8192 {
			t.Fatal("oversized generated output")
		}
	}
	optimizeWrite(t, "target.toml", "[pipeline.default]\nstages = []\n[pipeline.clean]\nstages = ['normalize']\n[[route]]\ntool = 'bash'\nprofile = 'clean'\n")
	cfg, err := config.LoadFile("target.toml")
	if err != nil {
		t.Fatal(err)
	}
	if reasons := optimizeSupported(cfg); len(reasons) != 0 {
		t.Fatal(reasons)
	}
	pass := evaluateOptimizeConfiguration("controlled", cases, outputs, cfg)
	if !pass.ControlledFactsPass || pass.Dispositions["transformed"] != 2 {
		t.Fatalf("success+unknown route or failure bypass: %+v", pass)
	}
	// A deliberately wrong fact assertion must fail rather than become success.
	cases[0].Expected.RequiredFacts[0].ExactOccurrences = 2
	fail := evaluateOptimizeConfiguration("controlled", cases, outputs, cfg)
	if fail.ControlledFactsPass {
		t.Fatal("false preservation assertion passed")
	}
}

func TestOptimizeShowRejectsCorruptionAndUsage(t *testing.T) {
	for _, artifact := range []string{"result.json", "baseline.toml", "candidate-1.toml", "population.json"} {
		t.Run(artifact, func(t *testing.T) {
			optimizeTestRoot(t)
			r := optimizeRun(t)
			optimizeWrite(t, filepath.Join(".ktx", "optimizations", r.ID, artifact), "corrupted")
			code, out, _ := optimizeInvoke(t, "show", r.ID)
			if code != ExitInternal || out != "" {
				t.Fatal("corrupt evidence reported as valid")
			}
		})
	}
	optimizeTestRoot(t)
	for _, args := range [][]string{{"show", "../escape"}, {"show"}, {"--config"}, {"--candidate", ""}, {"--format", "xml"}, {"--live"}, {"apply", "id"}, {"show", strings.Repeat("a", 32), "--config", "x"}} {
		code, _, _ := optimizeInvoke(t, args...)
		if code != ExitUsage {
			t.Fatalf("usage %v: %d", args, code)
		}
	}
	code, _, _ := optimizeInvoke(t, "show", strings.Repeat("a", 32))
	if code != ExitNotFound {
		t.Fatal("missing result exit")
	}
	code, help, stderr := optimizeInvoke(t, "--help")
	if code != ExitOK || !strings.Contains(help, "Offline by default") {
		t.Fatalf("help: %d %s", code, stderr)
	}
	r := optimizeRun(t, "--config", "missing.toml")
	if r.Outcome != "blocked" || r.Baseline != nil {
		t.Fatal("missing explicit config silently defaulted")
	}
	r = optimizeRun(t, "--candidate", "missing.toml")
	if r.Outcome != "blocked" {
		t.Fatal("missing candidate silently ignored")
	}
}

func TestOptimizeShowCannotPromoteGeneratedEvidence(t *testing.T) {
	for _, promotion := range []string{"ready", "apply"} {
		t.Run(promotion, func(t *testing.T) {
			optimizeTestRoot(t)
			r := optimizeRun(t)
			if promotion == "ready" {
				r.Outcome = "ready"
			} else {
				r.ApplyEligible = true
			}
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(".ktx", "optimizations", r.ID)
			optimizeWrite(t, filepath.Join(dir, "result.json"), string(data))
			optimizeWrite(t, filepath.Join(dir, "result.sha256"), optimizeHash(data))
			code, out, _ := optimizeInvoke(t, "show", r.ID)
			if code != ExitInternal || out != "" {
				t.Fatal("generated evidence promoted")
			}
		})
	}
}

const optimizeRoutedConfig = "[pipeline.default]\nstages = []\n[pipeline.search]\nstages = ['dedup-blank']\n[pipeline.git-status]\nstages = ['normalize']\n" +
	"[[route]]\ntool = 'bash'\ncommand_prefix = 'rg'\nprofile = 'search'\n[[route]]\ntool = 'grep'\nprofile = 'search'\n" +
	"[[route]]\ntool = 'bash'\ncommand_prefix = 'git'\nsubcommand = 'status'\nprofile = 'git-status'\n[[route]]\ntool = 'todowrite'\nprofile = 'search'\n" +
	"[pipeline.json-block]\nstages = ['json-block']\n[[route]]\ntool = 'bash'\ncommand_prefix = 'npx -y agentera@next prime'\nprofile = 'json-block'\n"

// Unreached command, subcommand, and other-tool routes are admitted, and the
// traffic they select is delivered byte-identically by every candidate.
func TestOptimizeUnreachedRoutesScreenUnchanged(t *testing.T) {
	optimizeTestRoot(t)
	optimizeWrite(t, "target.toml", optimizeRoutedConfig)
	r := optimizeRun(t, "--config", "target.toml")
	if r.Outcome != "screened" || r.SelectedCandidate == nil || len(r.Candidates) != 2 {
		t.Fatalf("unreached routes blocked: %+v", r)
	}
	dir := filepath.Join(".ktx", "optimizations", r.ID)
	baseline, err := config.LoadFile(filepath.Join(dir, "baseline.toml"))
	if err != nil {
		t.Fatal(err)
	}
	output := "match   \n\n\n\nsecond   \n\n\n\nthird\n"
	routed := []dbpkg.ToolCall{
		{Tool: "bash", Input: `{"command":"rg pattern"}`},
		{Tool: "grep", Input: `{"pattern":"x"}`},
		{Tool: "bash", Input: `{"command":"git status --short"}`},
		{Tool: "todowrite", Input: `{}`},
		{Tool: "bash", Input: `{"command":"npx -y agentera@next prime"}`},
	}
	deliver := func(cfg *config.Config, call dbpkg.ToolCall) pipelineExecution {
		t.Helper()
		call.Output = output
		var warnings bytes.Buffer
		execution, err := compressPipelineWithConfig(&call, cfg, nil, &warnings, true)
		if err != nil {
			t.Fatal(err)
		}
		return execution
	}
	for _, c := range r.Candidates {
		candidate, err := config.LoadFile(filepath.Join(dir, c.Config.Artifact))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range routed {
			before, after := deliver(baseline, call), deliver(candidate, call)
			if before.profile == "default" || before.profile != after.profile || before.output != after.output || before.transformed != after.transformed {
				t.Fatalf("%s changed routed %s traffic: %+v -> %+v", c.Name, call.Tool, before, after)
			}
		}
		if *r.SelectedCandidate == c.Config {
			// Control: the same output on default traffic does change, so the
			// routed comparison above can detect a difference.
			call := dbpkg.ToolCall{Tool: "bash", Input: `{"command":"printf controlled"}`}
			if before, after := deliver(baseline, call), deliver(candidate, call); before.output == after.output {
				t.Fatalf("%s did not change default traffic", c.Name)
			}
		}
	}
}

func TestOptimizeQualificationCommandMatchesEmbeddedTask(t *testing.T) {
	assets, err := benchdata.OptimizationAssets()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(assets["qualification.toml"]), "run "+optimizeQualificationCommand+".") {
		t.Fatal("reachability check no longer names the embedded qualification command")
	}
	if strings.Contains(string(assets["qualification.toml"]), "run cat tools/bench/corpus/other.txt") {
		t.Fatal("unexpected qualification command")
	}
}

func TestOptimizeStageAllowlistAppliesToReachableProfiles(t *testing.T) {
	const routed = "[pipeline.default]\nstages = []\n[pipeline.json-block]\nstages = %s\n[[route]]\ntool = %s\ncommand_prefix = %s\nprofile = 'json-block'\n"
	for _, tt := range []struct{ name, stages, tool, prefix, outcome, reason string }{
		{"unreached-builtin", "['json-block', 'keep-ends:2000000:1']", "'bash'", "'npx -y agentera@next prime'", "screened", ""},
		{"unreached-unknown", "['mystery']", "'bash'", "'npx'", "blocked", "invalid_stage_in_unreached_profile"},
		{"exercised-route", "['json-block']", "'read'", "''", "blocked", "unsupported_stage_not_executed"},
		{"default-profile", "[]", "'bash'", "'npx'", "blocked", "unsupported_stage_not_executed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			optimizeTestRoot(t)
			data := fmt.Sprintf(routed, tt.stages, tt.tool, tt.prefix)
			if tt.name == "default-profile" {
				data = strings.Replace(data, "[pipeline.default]\nstages = []", "[pipeline.default]\nstages = ['json-block']", 1)
			}
			optimizeWrite(t, "target.toml", data)
			r := optimizeRun(t, "--config", "target.toml")
			if r.Outcome != tt.outcome || (tt.reason != "" && strings.Join(r.Reasons, ",") != tt.reason) {
				t.Fatalf("reasons %v outcome %s", r.Reasons, r.Outcome)
			}
		})
	}
}
