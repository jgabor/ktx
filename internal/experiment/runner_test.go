package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/db"
)

func testPriceResolver(_, _ string) (db.Price, error) {
	zero := 0.0
	return db.Price{Input: 1, Output: 1, CacheRead: &zero, CacheWrite: &zero, InputSet: true, OutputSet: true, SourceURL: db.ModelsDevURL, RetrievedAt: "2026-07-10T12:00:00Z"}, nil
}

func TestOSExecutorTerminatesProcessGroup(t *testing.T) {
	started := time.Now()
	result, err := (OSExecutor{}).Run(context.Background(), Command{
		Args:   []string{"sh", "-c", "trap '' TERM; sleep 30 & wait"},
		Stdout: io.Discard, Stderr: io.Discard, Timeout: 50 * time.Millisecond, Grace: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.ExitCode != 124 {
		t.Fatalf("result = %#v", result)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("process group cancellation exceeded deadline")
	}
}

type fakeExecutor struct {
	mu                   sync.Mutex
	sessions             map[string]string
	sides                []string
	cleanup              []string
	timeoutFirst         bool
	attemptRuns          int
	runArgs              [][]string
	phases               []string
	opencodeIdentity     string
	omitOpenCodeIdentity bool
}

func (fake *fakeExecutor) Run(_ context.Context, command Command) (CommandResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	write := func(writer io.Writer, value string) {
		if writer != nil {
			_, _ = io.WriteString(writer, value)
		}
	}
	if command.Args[0] == "git" {
		if command.Args[len(command.Args)-2] == "--short" {
			write(command.Stdout, "abc123\n")
		} else if command.Args[len(command.Args)-1] == "HEAD" {
			write(command.Stdout, strings.Repeat("a", 40)+"\n")
		}
		return CommandResult{}, nil
	}
	if command.Args[0] == "docker" {
		if len(command.Args) > 2 && command.Args[1] == "image" {
			write(command.Stdout, "sha256:image\n")
			return CommandResult{}, nil
		}
		if len(command.Args) > 1 && command.Args[1] == "rm" {
			fake.cleanup = append(fake.cleanup, command.Args[len(command.Args)-1])
			return CommandResult{}, nil
		}
		if contains(command.Args, "--identity") {
			identity := fake.opencodeIdentity
			if identity == "" {
				identity = "test"
			}
			write(command.Stdout, "ktx\ttest\n")
			if !fake.omitOpenCodeIdentity {
				write(command.Stdout, "opencode\t"+identity+"\n")
			}
			return CommandResult{}, nil
		}
		if len(command.Args) > 1 && command.Args[1] == "run" {
			fake.attemptRuns++
			fake.runArgs = append(fake.runArgs, command.Args)
			fake.phases = append(fake.phases, dockerEnvironmentValue(command.Args, "KTX_BENCH_CACHE_PHASE"))
			if fake.timeoutFirst && fake.attemptRuns == 1 {
				return CommandResult{ExitCode: 124, TimedOut: true}, nil
			}
			database, config := mountSource(command.Args, "/home/node/.local/share/ktx/ktx.db"), mountSource(command.Args, "/home/node/.config/ktx/ktx.toml")
			side := strings.TrimPrefix(filepath.Ext(config), ".")
			fake.sessions[database] = side
			fake.sides = append(fake.sides, side)
			write(command.Stdout, `{"type":"tool_use","part":{}}`+"\n")
			write(command.Stdout, fmt.Sprintf(`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":%d,"output":0},"cost":0}}`+"\n", map[string]int{Baseline: 100, Candidate: 40}[side]))
			write(command.Stdout, `{"type":"text","part":{"text":"done"}}`+"\n")
			return CommandResult{}, nil
		}
	}
	if command.Args[0] == "ktx" {
		database := environmentValue(command.Env, "KTX_DB")
		if session := fake.sessions[database]; session != "" {
			write(command.Stdout, fmt.Sprintf(`[{"id":%q}]`+"\n", session))
		} else {
			write(command.Stdout, "[]\n")
		}
		return CommandResult{}, nil
	}
	return CommandResult{ExitCode: 2}, nil
}

func TestRunnerCacheAwarePhasesIsolationAndFallback(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(relative, value string) string {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	writeFixture("README.md", "fixture\n")
	writeFixture("Dockerfile.benchmark", "FROM scratch\n")
	methodology := writeFixture("tools/bench/methodology-v5-cache-aware.yaml", "methodology_version: v5-cache-aware\n")
	baseline := writeFixture("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
	candidate := writeFixture("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = [\"head:1\"]\n")
	task := writeFixture("tools/bench/tasks/task.toml", "id = \"task\"\nprompt = \"do work\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
	auth := writeFixture("auth.json", "{}\n")

	fake := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "opencode v1.18.0"}
	runner := &Runner{Executor: fake, Now: func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) }, PID: 29, ResolvePrice: testPriceResolver}
	dir, summary, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "openai/test", Variant: "high", TaskFile: task, TargetValid: 2, MaxAttempts: 8, Warmup: 1, CacheControl: "auto",
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "cache-aware",
		DataDir: filepath.Join(root, "data"), Image: "image", AuthPath: auth, RepoRoot: root, MethodologyPath: methodology,
		BaselineConfig: baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("Compare code=%d err=%v", code, err)
	}
	if got := strings.Join(fake.phases, ","); got != "cold_observation,cold_observation,warmup,warmup,measured_warm,measured_warm,measured_warm,measured_warm" {
		t.Fatalf("phases = %s", got)
	}
	if fake.sides[0] == fake.sides[1] || fake.sides[2] != fake.sides[1] || fake.sides[3] != fake.sides[0] {
		t.Fatalf("setup order was not counterbalanced: %v", fake.sides[:4])
	}
	if got := strings.Join(fake.sides[4:], ","); got != "baseline,candidate,candidate,baseline" {
		t.Fatalf("measured order = %s", got)
	}
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if counts := CountValid(attempts, 1); counts[Baseline] != 2 || counts[Candidate] != 2 {
		t.Fatalf("measured counts = %#v", counts)
	}
	seenHashes := map[string]string{}
	for _, attempt := range attempts {
		if attempt.CacheControl == nil || attempt.CacheControl.Mode != "isolated" || attempt.CacheControl.Source != "runtime_adapter" {
			t.Fatalf("cache metadata = %#v", attempt.CacheControl)
		}
		if attempt.CacheControl.Phase != attempt.Role || attempt.CacheControl.ExecutionOrder == 0 || attempt.CacheControl.Model != "openai/test" || attempt.CacheControl.Provider != "openai" || attempt.CacheControl.Variant != "high" || attempt.CacheControl.RuntimeVersion != "1.18.0" || attempt.CacheControl.IdleGapMS != 0 {
			t.Fatalf("incomplete cache metadata = %#v", attempt.CacheControl)
		}
		if prior := seenHashes[attempt.Side]; prior != "" && prior != attempt.CacheControl.NamespaceSHA256 {
			t.Fatalf("namespace changed for %s", attempt.Side)
		}
		seenHashes[attempt.Side] = attempt.CacheControl.NamespaceSHA256
	}
	if seenHashes[Baseline] == "" || seenHashes[Baseline] == seenHashes[Candidate] {
		t.Fatalf("namespace hashes = %#v", seenHashes)
	}
	retained, _ := os.ReadFile(filepath.Join(dir, "attempts.jsonl"))
	manifestBytes, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	rawBySide := map[string]string{}
	for i, args := range fake.runArgs {
		content := dockerEnvironmentValue(args, "OPENCODE_CONFIG_CONTENT")
		var config struct {
			Provider map[string]struct {
				Models map[string]struct {
					Options map[string]string `json:"options"`
				} `json:"models"`
			} `json:"provider"`
		}
		if content != "" && json.Unmarshal([]byte(content), &config) == nil {
			for _, provider := range config.Provider {
				for _, model := range provider.Models {
					raw := model.Options["promptCacheKey"]
					if prior := rawBySide[fake.sides[i]]; prior != "" && prior != raw {
						t.Fatalf("raw namespace changed for %s", fake.sides[i])
					}
					rawBySide[fake.sides[i]] = raw
					if raw != "" && (strings.Contains(string(retained), raw) || strings.Contains(string(manifestBytes), raw)) {
						t.Fatal("raw cache namespace retained")
					}
				}
			}
		}
	}
	if rawBySide[Baseline] == "" || rawBySide[Baseline] == rawBySide[Candidate] {
		t.Fatalf("raw namespaces = %#v", rawBySide)
	}
	if summary["headline_phase"] != MeasuredWarm {
		t.Fatalf("headline phase = %v", summary["headline_phase"])
	}
	summaryCache := summary["cache_control"].(*CacheControlManifest)
	if summaryCache.Model != "openai/test" || summaryCache.Provider != "openai" || summaryCache.Variant != "high" || summaryCache.RuntimeVersion != "1.18.0" {
		t.Fatalf("summary cache metadata = %#v", summaryCache)
	}
	if summary["pipelines"].(map[string]any)[Baseline].(map[string]any)["sample_classification"] != "exploratory" {
		t.Fatal("smoke sample was not classified exploratory")
	}
	report, err := ValidateExperiment(dir)
	if err != nil || report["valid"] != true || report["summary_byte_match"] != true {
		t.Fatalf("validation report=%#v err=%v", report, err)
	}

	observedFake := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "1.17.15"}
	observedRunner := &Runner{Executor: observedFake, Now: runner.Now, PID: 30, ResolvePrice: testPriceResolver}
	observedDir, _, code, err := observedRunner.Compare(context.Background(), CompareOptions{
		Model: "openai/test", TaskFile: task, TargetValid: 1, MaxAttempts: 6, Warmup: 1, CacheControl: "auto",
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "observed",
		DataDir: filepath.Join(root, "observed-data"), Image: "image", AuthPath: auth, RepoRoot: root, MethodologyPath: methodology,
		BaselineConfig: baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("observed Compare code=%d err=%v", code, err)
	}
	observed, _ := LoadAttempts(observedDir)
	if observed[0].CacheControl == nil || observed[0].CacheControl.Mode != "observed" || observed[0].CacheControl.Source != "unavailable" || observed[0].CacheControl.NamespaceSHA256 != "" {
		t.Fatalf("observed metadata = %#v", observed[0].CacheControl)
	}

	unsupported := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "custom-build"}
	unsupportedRunner := &Runner{Executor: unsupported, Now: runner.Now, PID: 31, ResolvePrice: testPriceResolver}
	_, _, code, err = unsupportedRunner.Compare(context.Background(), CompareOptions{Model: "openai/test", TaskFile: task, CacheControl: "isolated", RepoRoot: root, MethodologyPath: methodology, AuthPath: auth, BaselineConfig: baseline, CandidateConfig: candidate, DockerBin: "docker", KTXBin: "ktx"})
	if err == nil || !strings.Contains(err.Error(), "unsupported") || code != 2 || unsupported.attemptRuns != 0 {
		t.Fatalf("unsupported isolation code=%d err=%v paid=%d", code, err, unsupported.attemptRuns)
	}
	for _, test := range []struct {
		name, identity string
		omit           bool
	}{
		{name: "unsupported_version", identity: "1.17.15"},
		{name: "unsupported_newer", identity: "1.17.20"},
		{name: "malformed_version", identity: "version banana"},
		{name: "unknown_version", omit: true},
	} {
		t.Run("explicit isolated "+test.name, func(t *testing.T) {
			fake := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: test.identity, omitOpenCodeIdentity: test.omit}
			r := &Runner{Executor: fake, Now: runner.Now, PID: 40, ResolvePrice: testPriceResolver}
			_, _, code, err := r.Compare(context.Background(), CompareOptions{Model: "openai/test", TaskFile: task, CacheControl: "isolated", RepoRoot: root, MethodologyPath: methodology, AuthPath: auth, BaselineConfig: baseline, CandidateConfig: candidate, DockerBin: "docker", KTXBin: "ktx"})
			if err == nil || code != 2 || fake.attemptRuns != 0 {
				t.Fatalf("code=%d err=%v paid=%d", code, err, fake.attemptRuns)
			}
		})
	}
	defaults := CompareOptions{Model: "openai/test", TaskFile: task, RepoRoot: root, MethodologyPath: methodology, AuthPath: auth, BaselineConfig: baseline, CandidateConfig: candidate, DockerBin: "docker", KTXBin: "ktx"}
	if err := runner.prepareOptions(&defaults); err != nil {
		t.Fatal(err)
	}
	if defaults.TargetValid != 5 || defaults.Warmup != 1 || defaults.MaxAttempts < 14 {
		t.Fatalf("v5 defaults = target %d warmup %d max %d", defaults.TargetValid, defaults.Warmup, defaults.MaxAttempts)
	}
}

func TestRunnerAlternatesAndCleansContainers(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(relative, value string) string {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	writeFixture("README.md", "fixture\n")
	writeFixture("Dockerfile.benchmark", "FROM scratch\n")
	writeFixture("tools/bench/methodology-v4-eval-matrix.yaml", "methodology_version: v4-eval-matrix\n")
	baseline := writeFixture("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
	candidate := writeFixture("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = [\"head:1\"]\n")
	task := writeFixture("tools/bench/tasks/task.toml", "id = \"task\"\nprompt = \"do work\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
	auth := writeFixture("auth.json", "{}\n")
	fake := &fakeExecutor{sessions: map[string]string{}}
	runner := &Runner{Executor: fake, Now: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }, PID: 7, ResolvePrice: testPriceResolver}
	directory, summary, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "neuralwatt/test", TaskFile: task, TargetValid: 2, MaxAttempts: 4,
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "experiment",
		DataDir: filepath.Join(root, "data"), Image: "image", AuthPath: auth, RepoRoot: root,
		MethodologyPath: filepath.Join(root, "tools/bench/methodology-v4-eval-matrix.yaml"),
		BaselineConfig:  baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("Compare code=%d err=%v", code, err)
	}
	if got := strings.Join(fake.sides, ","); got != "baseline,candidate,candidate,baseline" {
		t.Fatalf("side order = %s", got)
	}
	if len(fake.cleanup) != 4 {
		t.Fatalf("cleanup calls = %d", len(fake.cleanup))
	}
	if summary["tool_call_inflation"] == nil {
		t.Fatal("summary missing tool call inflation")
	}
	legacyAttempts, err := LoadAttempts(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range legacyAttempts {
		if attempt.Role != "" || attempt.CacheControl != nil {
			t.Fatalf("legacy attempt gained cache-aware metadata: %#v", attempt)
		}
	}
	manifestData, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Pricing == nil || manifest.Pricing.SourceURL != db.ModelsDevURL || manifest.Pricing.RetrievedAt == nil {
		t.Fatalf("pricing metadata = %#v", manifest.Pricing)
	}
	pricingHash, err := fileSHA256(filepath.Join(directory, manifest.Pricing.Path))
	if err != nil || pricingHash != manifest.Pricing.SHA256 {
		t.Fatalf("pricing snapshot hash = %q, err=%v, metadata=%#v", pricingHash, err, manifest.Pricing)
	}
	before, err := os.ReadFile(filepath.Join(directory, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(directory); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(directory, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("summary regeneration changed bytes")
	}

	timeoutFake := &fakeExecutor{sessions: map[string]string{}, timeoutFirst: true}
	timeoutRunner := &Runner{Executor: timeoutFake, Now: runner.Now, PID: 8, ResolvePrice: testPriceResolver}
	_, timeoutSummary, code, err := timeoutRunner.Compare(context.Background(), CompareOptions{
		Model: "neuralwatt/test", TaskFile: task, TargetValid: 1, MaxAttempts: 3,
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "timeout-retry",
		DataDir: filepath.Join(root, "timeout-data"), Image: "image", AuthPath: auth, RepoRoot: root,
		MethodologyPath: filepath.Join(root, "tools/bench/methodology-v4-eval-matrix.yaml"),
		BaselineConfig:  baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("timeout retry code=%d err=%v", code, err)
	}
	if len(timeoutFake.cleanup) != 3 {
		t.Fatalf("timeout cleanup calls = %d", len(timeoutFake.cleanup))
	}
	pipelines := timeoutSummary["pipelines"].(map[string]any)
	baselineSummary := pipelines[Baseline].(map[string]any)
	statusCounts := baselineSummary["status_counts"].(map[string]int)
	if statusCounts["timeout"] != 1 || baselineSummary["valid_count"] != 1 {
		t.Fatalf("timeout summary = %#v", baselineSummary)
	}
}

func TestRunnerRejectsCopilotModels(t *testing.T) {
	root := t.TempDir()
	taskPath := filepath.Join(root, "task.toml")
	if err := os.WriteFile(taskPath, []byte("id = \"task\"\nprompt = \"do work\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"github-copilot/claude-sonnet-4.5", "github-copilot/gpt-4"} {
		runner := &Runner{Executor: &fakeExecutor{sessions: map[string]string{}}, Now: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }, PID: 9, ResolvePrice: testPriceResolver}
		_, _, _, err := runner.Compare(context.Background(), CompareOptions{
			Model: model, TaskFile: taskPath, TargetValid: 1, MaxAttempts: 2,
			AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "copilot-reject",
			DataDir: filepath.Join(root, "data"), Image: "image", RepoRoot: root,
			KTXBin: "ktx", DockerBin: "docker",
		})
		if err == nil {
			t.Fatalf("expected error for model %q, got nil", model)
		}
		if !strings.Contains(err.Error(), "github-copilot") {
			t.Fatalf("error for model %q should mention github-copilot, got: %v", model, err)
		}
	}
}

func TestRunnerAcceptsNonNeuralwattProviders(t *testing.T) {
	root := t.TempDir()
	writeFixture := func(relative, value string) string {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	writeFixture("README.md", "fixture\n")
	writeFixture("Dockerfile.benchmark", "FROM scratch\n")
	writeFixture("tools/bench/methodology-v4-eval-matrix.yaml", "methodology_version: v4-eval-matrix\n")
	baseline := writeFixture("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
	candidate := writeFixture("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = [\"head:1\"]\n")
	task := writeFixture("tools/bench/tasks/task.toml", "id = \"task\"\nprompt = \"do work\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
	auth := writeFixture("auth.json", "{}\n")
	fake := &fakeExecutor{sessions: map[string]string{}}
	runner := &Runner{Executor: fake, Now: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }, PID: 10, ResolvePrice: testPriceResolver}
	_, _, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "openai/gpt-5.6-luna", TaskFile: task, TargetValid: 2, MaxAttempts: 4,
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "openai-accept",
		DataDir: filepath.Join(root, "data"), Image: "image", AuthPath: auth, RepoRoot: root,
		MethodologyPath: filepath.Join(root, "tools/bench/methodology-v4-eval-matrix.yaml"),
		BaselineConfig:  baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("openai model should be accepted, got code=%d err=%v", code, err)
	}
}

func TestRunnerVariantPropagation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		variant    string
		wantFlag   bool
		wantManifi bool
	}{
		{"variant_set", "high", true, true},
		{"variant_unset", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture := func(relative, value string) string {
				path := filepath.Join(root, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			}
			writeFixture("README.md", "fixture\n")
			writeFixture("Dockerfile.benchmark", "FROM scratch\n")
			writeFixture("tools/bench/methodology-v4-eval-matrix.yaml", "methodology_version: v4-eval-matrix\n")
			baseline := writeFixture("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
			candidate := writeFixture("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = [\"head:1\"]\n")
			task := writeFixture("tools/bench/tasks/task.toml", "id = \"task\"\nprompt = \"do work\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
			auth := writeFixture("auth.json", "{}\n")
			fake := &fakeExecutor{sessions: map[string]string{}}
			runner := &Runner{Executor: fake, Now: func() time.Time { return time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC) }, PID: 11, ResolvePrice: testPriceResolver}
			directory, _, code, err := runner.Compare(context.Background(), CompareOptions{
				Model: "openai/gpt-5.6-luna", Variant: tc.variant, TaskFile: task, TargetValid: 2, MaxAttempts: 4,
				AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "variant-" + tc.name,
				DataDir: filepath.Join(root, "data"), Image: "image", AuthPath: auth, RepoRoot: root,
				MethodologyPath: filepath.Join(root, "tools/bench/methodology-v4-eval-matrix.yaml"),
				BaselineConfig:  baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
			})
			if err != nil || code != 0 {
				t.Fatalf("Compare code=%d err=%v", code, err)
			}
			// Verify Docker command args
			if len(fake.runArgs) == 0 {
				t.Fatal("no docker run commands captured")
			}
			for i, args := range fake.runArgs {
				hasVariant := contains(args, "--variant")
				if hasVariant != tc.wantFlag {
					t.Fatalf("docker run #%d: --variant present=%v, want %v (args=%v)", i, hasVariant, tc.wantFlag, args)
				}
				if hasVariant {
					// Verify the variant value follows the flag
					idx := -1
					for j, a := range args {
						if a == "--variant" {
							idx = j
							break
						}
					}
					if idx < 0 || idx+1 >= len(args) || args[idx+1] != tc.variant {
						t.Fatalf("docker run #%d: --variant value mismatch (args=%v)", i, args)
					}
				}
			}
			// Verify manifest
			manifestData, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest Manifest
			if err := json.Unmarshal(manifestData, &manifest); err != nil {
				t.Fatal(err)
			}
			if tc.wantManifi {
				if manifest.Variant == nil || *manifest.Variant != tc.variant {
					t.Fatalf("manifest variant = %v, want %q", manifest.Variant, tc.variant)
				}
			} else {
				if manifest.Variant != nil {
					t.Fatalf("manifest variant should be nil when unset, got %v", *manifest.Variant)
				}
				// Verify the JSON key is omitted (omitempty) by re-decoding into a map
				var raw map[string]any
				if err := json.Unmarshal(manifestData, &raw); err != nil {
					t.Fatal(err)
				}
				if _, ok := raw["variant"]; ok {
					t.Fatalf("manifest JSON should not contain variant key when unset: %s", manifestData)
				}
			}
		})
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mountSource(args []string, destination string) string {
	for index, value := range args {
		if value == "-v" && index+1 < len(args) {
			parts := strings.Split(args[index+1], ":")
			if len(parts) > 1 && parts[1] == destination {
				return parts[0]
			}
		}
	}
	return ""
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}

func dockerEnvironmentValue(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-e" && strings.HasPrefix(args[i+1], key+"=") {
			return strings.TrimPrefix(args[i+1], key+"=")
		}
	}
	return ""
}
