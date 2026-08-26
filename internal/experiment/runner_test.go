package experiment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/telemetry"
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
	capabilityRuns       int
	runArgs              [][]string
	phases               []string
	opencodeIdentity     string
	collectorContract    string
	omitOpenCodeIdentity bool
	fullStepUsage        bool
	candidateInput       int
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
		if contains(command.Args, "--capabilities") {
			fake.capabilityRuns++
			identity := fake.opencodeIdentity
			if identity == "" {
				identity = benchcontract.RuntimeVersion
			}
			if fake.omitOpenCodeIdentity {
				identity = ""
			}
			contract := fake.collectorContract
			if contract == "" {
				contract = benchcontract.ContractVersion
			}
			write(command.Stdout, fmt.Sprintf(`{"ktx_version":"test","opencode_version":%q,"generated_plugin_sha256":"%s","generated_plugin_matches_binary":true,"event_collector_contract":"%s","explicit_session_ownership":true,"serialized_callbacks":true,"complete_interval_lifecycle":true,"epoch_tracking":true,"strict_acknowledgements":true,"exact_model_step_identity":true,"retained_duplicate_callbacks":true,"accepted_model_step_projection":true,"callback_queue_continuation":true,"fail_closed_interval_validation":true,"preflight_before_auth":true,"normalized_kinds":["collector_open","model_step","retry","compaction","interruption","other"]}`+"\n", identity, strings.Repeat("a", 64), contract))
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
			owner := dockerEnvironmentValue(command.Args, benchcontract.SessionEnv)
			intervalID := dockerEnvironmentValue(command.Args, benchcontract.IntervalEnv)
			fake.sessions[database] = owner
			store, err := db.Open(database)
			if err != nil {
				return CommandResult{}, err
			}
			captured := "2026-07-15T12:00:00Z"
			_, _, err = store.PersistRuntimeEvent(db.RuntimeEventInput{IntervalID: intervalID, SessionID: owner, EpochID: "epoch", Kind: "collector_open", EventType: "collector.open", CapturedAt: captured})
			if err != nil {
				_ = store.Close()
				return CommandResult{}, err
			}
			runtimeSession := "runtime-" + side
			messageID, partID := "message", "part"
			zero := int64(0)
			cost := float64(0)
			inputTokens := int64(map[string]int{Baseline: 100, Candidate: 40}[side])
			observation := &telemetry.Observation{Key: telemetry.Key{SessionID: runtimeSession, MessageID: messageID, PartID: partID}, Ordinal: 1, Reason: "stop", Runtime: benchcontract.Runtime, RuntimeVersion: benchcontract.RuntimeVersion, Source: telemetry.SourceEventHook, Cost: telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: &cost}, Tokens: telemetry.Tokens{Input: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &inputTokens}, Output: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, Reasoning: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheRead: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheWrite: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}}}
			_, _, err = store.PersistRuntimeEvent(db.RuntimeEventInput{IntervalID: intervalID, SessionID: owner, EpochID: "epoch", EpochOrdinal: 1, Kind: "model_step", EventType: "message.part.updated", SourceSessionID: &runtimeSession, MessageID: &messageID, PartID: &partID, CapturedAt: captured, Telemetry: observation})
			if err != nil {
				_ = store.Close()
				return CommandResult{}, err
			}
			toolID, err := db.NewID()
			if err == nil {
				_, err = store.PersistToolCallOnce(&db.ToolCall{ID: toolID, SessionID: owner, Tool: "bash", Output: "done", ContentHash: db.ContentHash("done")})
			}
			if err == nil {
				err = store.SealRuntimeEventInterval(intervalID, owner, captured, db.RuntimeComplete, "")
			}
			closeErr := store.Close()
			if err != nil {
				return CommandResult{}, err
			}
			if closeErr != nil {
				return CommandResult{}, closeErr
			}
			fake.sides = append(fake.sides, side)
			write(command.Stdout, `{"type":"tool_use","part":{}}`+"\n")
			input := map[string]int{Baseline: 100, Candidate: 40}[side]
			if side == Candidate && fake.candidateInput != 0 {
				input = fake.candidateInput
			}
			if fake.fullStepUsage {
				write(command.Stdout, fmt.Sprintf(`{"type":"step_finish","part":{"sessionID":%q,"messageID":"message","id":"part","type":"step-finish","reason":"stop","tokens":{"input":%d,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}`+"\n", runtimeSession, input))
			} else {
				write(command.Stdout, fmt.Sprintf(`{"type":"step_finish","part":{"sessionID":%q,"messageID":"message","id":"part","type":"step-finish","reason":"stop","tokens":{"input":%d,"output":0},"cost":0}}`+"\n", runtimeSession, input))
			}
			write(command.Stdout, `{"type":"text","part":{"text":"done"}}`+"\n")
			return CommandResult{}, nil
		}
	}
	if command.Args[0] == "ktx" {
		database := environmentValue(command.Env, "KTX_DB")
		if session := fake.sessions[database]; session != "" {
			write(command.Stdout, fmt.Sprintf(`[{"id":%q}]`+"\n", session))
		} else {
			handle, err := db.OpenReadOnly(database)
			if err != nil {
				return CommandResult{}, err
			}
			var session string
			err = handle.DB().QueryRow(`SELECT id FROM sessions`).Scan(&session)
			handle.Close()
			if err != nil {
				return CommandResult{}, err
			}
			write(command.Stdout, fmt.Sprintf(`[{"id":%q}]`+"\n", session))
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

	fake := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "1.18.5", fullStepUsage: true}
	runner := &Runner{Executor: fake, Now: func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) }, PID: 29, ResolvePrice: testPriceResolver, ResolveCacheCapability: func(CacheCapabilityRequest) CacheCapability {
		return CacheCapability{Supported: true, Source: "provider_adapter"}
	}}
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
		if attempt.SchemaVersion != 3 || len(attempt.ModelSteps) != 1 {
			t.Fatalf("generated model-step telemetry = %#v", attempt.ModelSteps)
		}
		step := attempt.ModelSteps[0]
		if step.Identity.SessionID == nil || *step.Identity.SessionID != "runtime-"+attempt.Side || step.Identity.MessageID == nil || *step.Identity.MessageID != "message" || step.Identity.PartID == nil || *step.Identity.PartID != "part" {
			t.Fatalf("schema 3 wrapper identity = %#v", step.Identity)
		}
		for _, measurement := range []IntegerMeasurement{step.UncachedInput, step.Output, step.Reasoning, step.CacheRead, step.CacheWrite} {
			if measurement.State != MeasurementMeasured || measurement.Value == nil {
				t.Fatalf("model-step token state = %#v", step)
			}
		}
		if step.Cost.State != MeasurementMeasured || step.Cost.Value == nil {
			t.Fatalf("model-step cost state = %#v", step)
		}
		if attempt.CacheControl == nil || attempt.CacheControl.Mode != "isolated" || attempt.CacheControl.Source != "provider_adapter" {
			t.Fatalf("cache metadata = %#v", attempt.CacheControl)
		}
		if attempt.CacheControl.Phase != attempt.Role || attempt.CacheControl.ExecutionOrder == 0 || attempt.CacheControl.Model != "openai/test" || attempt.CacheControl.Provider != "openai" || attempt.CacheControl.Variant != "high" || attempt.CacheControl.RuntimeVersion != "1.18.5" || attempt.CacheControl.IdleGapMS != 0 {
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
	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if _, exists := manifest["model_step_evidence_version"]; exists {
		t.Fatalf("optional telemetry gained manifest attestation: %s", manifestBytes)
	}
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
	if summaryCache.Model != "openai/test" || summaryCache.Provider != "openai" || summaryCache.Variant != "high" || summaryCache.RuntimeVersion != "1.18.5" {
		t.Fatalf("summary cache metadata = %#v", summaryCache)
	}
	if summary["pipelines"].(map[string]any)[Baseline].(map[string]any)["sample_classification"] != "exploratory" {
		t.Fatal("smoke sample was not classified exploratory")
	}
	report, err := ValidateExperiment(dir)
	if err != nil || report["valid"] != true || report["summary_byte_match"] != true {
		t.Fatalf("validation report=%#v err=%v", report, err)
	}

	observedFake := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "1.18.5"}
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

	timeoutFake := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "1.18.5", timeoutFirst: true, fullStepUsage: true}
	timeoutRunner := &Runner{Executor: timeoutFake, Now: runner.Now, PID: 32, ResolvePrice: testPriceResolver}
	_, _, code, err = timeoutRunner.Compare(context.Background(), CompareOptions{
		Model: "openai/test", TaskFile: task, TargetValid: 1, MaxAttempts: 6, Warmup: 1, CacheControl: "auto",
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "cache-timeout-retry",
		DataDir: filepath.Join(root, "cache-timeout-data"), Image: "image", AuthPath: auth, RepoRoot: root, MethodologyPath: methodology,
		BaselineConfig: baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err == nil || code != 2 || timeoutFake.attemptRuns != 1 {
		t.Fatalf("unsealed timeout code=%d err=%v attempts=%d", code, err, timeoutFake.attemptRuns)
	}

	unsupported := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "custom-build"}
	unsupportedRunner := &Runner{Executor: unsupported, Now: runner.Now, PID: 31, ResolvePrice: testPriceResolver}
	_, _, code, err = unsupportedRunner.Compare(context.Background(), CompareOptions{Model: "openai/test", TaskFile: task, CacheControl: "isolated", RepoRoot: root, MethodologyPath: methodology, AuthPath: auth, BaselineConfig: baseline, CandidateConfig: candidate, DockerBin: "docker", KTXBin: "ktx"})
	if err == nil || code != 2 || unsupported.attemptRuns != 0 {
		t.Fatalf("unsupported isolation code=%d err=%v paid=%d", code, err, unsupported.attemptRuns)
	}
	for _, test := range []struct {
		name, identity string
		omit           bool
	}{
		{name: "historical_version", identity: "1.18.0"},
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

func TestRunnerCompareKeepsValidRejectedCostGuardAsSuccess(t *testing.T) {
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
	methodology := writeFixture("tools/bench/methodology-v6-prompt-cache-token-hit-rate.yaml", "methodology_version: v6-prompt-cache-token-hit-rate\n")
	baseline := writeFixture("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
	candidate := writeFixture("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = [\"head:1\"]\n")
	task := writeFixture("tools/bench/tasks/task.toml", "id = \"task\"\nprompt = \"do work\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
	auth := writeFixture("auth.json", "{}\n")
	fake := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "1.18.5", fullStepUsage: true, candidateInput: 100}
	runner := &Runner{Executor: fake, Now: func() time.Time { return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC) }, PID: 41, ResolvePrice: testPriceResolver}
	directory, summary, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "openai/test", Variant: "high", TaskFile: task, TargetValid: 1, MaxAttempts: 4, Warmup: 0, WarmupSet: true, CacheControl: "observed",
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "rejected-cost-guard",
		DataDir: filepath.Join(root, "data"), Image: "image", AuthPath: auth, RepoRoot: root, MethodologyPath: methodology,
		BaselineConfig: baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("valid rejected Compare code=%d err=%v", code, err)
	}
	if summary["accepted"] != false || !containsString(summary["acceptance_reasons"].([]string), "candidate_estimated_api_cost_per_correct_not_lower") {
		t.Fatalf("comparison acceptance = %#v", summary)
	}
	report, err := ValidateExperiment(directory)
	if err != nil || report["valid"] != true || report["accepted"] != false {
		t.Fatalf("rejected comparison validation report=%#v err=%v", report, err)
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
	_, _, code, err = timeoutRunner.Compare(context.Background(), CompareOptions{
		Model: "neuralwatt/test", TaskFile: task, TargetValid: 1, MaxAttempts: 3,
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "timeout-retry",
		DataDir: filepath.Join(root, "timeout-data"), Image: "image", AuthPath: auth, RepoRoot: root,
		MethodologyPath: filepath.Join(root, "tools/bench/methodology-v4-eval-matrix.yaml"),
		BaselineConfig:  baseline, CandidateConfig: candidate, KTXBin: "ktx", DockerBin: "docker",
	})
	if err == nil || code != 2 || timeoutFake.attemptRuns != 1 {
		t.Fatalf("unsealed timeout code=%d err=%v attempts=%d", code, err, timeoutFake.attemptRuns)
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

func TestCompareAttestsCapabilitiesBeforeAuthAndArtifacts(t *testing.T) {
	root := t.TempDir()
	write := func(relative, content string) string {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	methodology := write("tools/bench/methodology-v5-cache-aware.yaml", "methodology_version: v5-cache-aware\n")
	task := write("tools/bench/tasks/task.toml", "id = \"task\"\nprompt = \"prompt\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
	baseline := write("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
	candidate := write("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = []\n")
	missingAuth := filepath.Join(root, "missing-auth.json")
	options := CompareOptions{Model: "openai/test", TaskFile: task, RepoRoot: root, MethodologyPath: methodology, BaselineConfig: baseline, CandidateConfig: candidate, AuthPath: missingAuth, DockerBin: "docker", KTXBin: "ktx", DataDir: filepath.Join(root, "artifacts"), ExperimentID: "ordering", CacheControl: "observed"}

	capable := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: benchcontract.RuntimeVersion}
	runner := &Runner{Executor: capable, ResolvePrice: testPriceResolver}
	_, _, code, err := runner.Compare(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "auth file not found") || code != 2 || capable.capabilityRuns != 1 || capable.attemptRuns != 0 {
		t.Fatalf("capable code=%d err=%v capabilities=%d attempts=%d", code, err, capable.capabilityRuns, capable.attemptRuns)
	}
	if _, err := os.Stat(options.DataDir); !os.IsNotExist(err) {
		t.Fatalf("artifact directory created: %v", err)
	}

	mismatch := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: "wrong-version"}
	runner = &Runner{Executor: mismatch, ResolvePrice: testPriceResolver}
	_, _, code, err = runner.Compare(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "benchmark preflight failed") || strings.Contains(err.Error(), "auth") || code != 2 || mismatch.capabilityRuns != 1 || mismatch.attemptRuns != 0 {
		t.Fatalf("mismatch code=%d err=%v capabilities=%d attempts=%d", code, err, mismatch.capabilityRuns, mismatch.attemptRuns)
	}
	if _, err := os.Stat(options.DataDir); !os.IsNotExist(err) {
		t.Fatalf("artifact directory created after mismatch: %v", err)
	}

	stale := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: benchcontract.RuntimeVersion, collectorContract: "opencode-event-v1"}
	runner = &Runner{Executor: stale, ResolvePrice: testPriceResolver}
	_, _, code, err = runner.Compare(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "runtime_or_event_contract_mismatch") || code != 2 || stale.capabilityRuns != 1 || stale.attemptRuns != 0 {
		t.Fatalf("stale collector code=%d err=%v capabilities=%d attempts=%d", code, err, stale.capabilityRuns, stale.attemptRuns)
	}
}

func TestValidateLocalCollectorSourceAcceptsCurrentSource(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLocalCollectorSource(filepath.Join(workingDirectory, "..", "..")); err != nil {
		t.Fatal(err)
	}
}

func TestBenchcontractDeclarationsIgnoreAlignmentAndNeighbors(t *testing.T) {
	source := []byte(`const (
	ContractVersion = "opencode-event-v3"
	LegacyContractVersion     = "opencode-event-v1"
	CollectorRequired	=     "required" // benchmark mode
)
`)
	if !hasStringDeclaration(source, "ContractVersion", benchcontract.ContractVersion) {
		t.Fatal("ContractVersion declaration was not recognized")
	}
	if !hasStringDeclaration(source, "CollectorRequired", benchcontract.CollectorRequired) {
		t.Fatal("CollectorRequired declaration was not recognized")
	}
}

func TestBenchcontractDeclarationsRejectWrongValues(t *testing.T) {
	for _, test := range []struct {
		name       string
		source     string
		identifier string
		value      string
	}{
		{"contract version", `ContractVersion = "opencode-event-v1"`, "ContractVersion", benchcontract.ContractVersion},
		{"collector mode", `CollectorRequired = "optional"`, "CollectorRequired", benchcontract.CollectorRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			if hasStringDeclaration([]byte(test.source), test.identifier, test.value) {
				t.Fatalf("accepted wrong declaration %q", test.source)
			}
		})
	}
}

type forbiddenExecutor struct {
	calls int
}

func (executor *forbiddenExecutor) Run(context.Context, Command) (CommandResult, error) {
	executor.calls++
	return CommandResult{}, errors.New("unexpected command")
}

func TestPreflightLocalSourceMismatchPreventsDockerCommands(t *testing.T) {
	root := t.TempDir()
	write := func(relative, content string) {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile.benchmark", "ARG OPENCODE_VERSION="+benchcontract.RuntimeVersion+"\nRUN ktx init opencode\nENTRYPOINT [\"/usr/local/bin/ktx\", \"bench\", \"agent\"]\n")
	write(filepath.Join("internal", "cli", "init.go"), `["telemetry", "collector-open"] ["telemetry", "event-record"] eventQueue.then next.catch(() => undefined)`)
	write(filepath.Join("internal", "db", "schema.go"), "runtime_event_intervals runtime_events model_step_classification contradiction_count")
	write(filepath.Join("internal", "benchcontract", "contract.go"), `const (
	ContractVersion = "wrong"
	CollectorRequired = "required"
)`)
	executor := &forbiddenExecutor{}
	runner := &Runner{Executor: executor}
	report, _, err := runner.preflightPrepared(context.Background(), CompareOptions{RepoRoot: root, Image: "image", DockerBin: "docker", BuildImage: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(report.Reasons, ","); got != "local_source_contract_mismatch" {
		t.Fatalf("reasons = %q", got)
	}
	if executor.calls != 0 {
		t.Fatalf("executed %d Docker commands after local-source failure", executor.calls)
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
