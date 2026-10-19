package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/experiment"
	"github.com/jgabor/ktx/internal/telemetry"
)

type optimizeFakeExecutor struct {
	t                     *testing.T
	launches              int
	capabilities          int
	wrongRuntime          bool
	wrongAnswer           bool
	missingReasoning      bool
	missingCost           bool
	reportedCost          float64
	noImprovement         bool
	tamper                string
	cancel                context.CancelFunc
	seenConfigs           map[string][]byte
	beforeLaunch          func()
	pendingDatabaseTamper string
}

func optimizeMount(args []string, target string) string {
	for i, arg := range args {
		if arg == "-v" && i+1 < len(args) {
			parts := strings.Split(args[i+1], ":")
			if len(parts) > 1 && parts[1] == target {
				return parts[0]
			}
		}
	}
	return ""
}

func optimizeEnv(args []string, key string) string {
	for i := len(args) - 1; i >= 0; i-- {
		arg := args[i]
		if strings.HasPrefix(arg, key+"=") {
			return strings.TrimPrefix(arg, key+"=")
		}
	}
	return ""
}

func (f *optimizeFakeExecutor) Run(ctx context.Context, command experiment.Command) (experiment.CommandResult, error) {
	write := func(value string) {
		f.t.Helper()
		if command.Stdout != nil {
			if _, err := io.WriteString(command.Stdout, value); err != nil {
				f.t.Fatal(err)
			}
		}
	}
	args := command.Args
	if args[0] == "git" {
		if args[len(args)-1] == "HEAD" {
			write(strings.Repeat("a", 40) + "\n")
		}
		return experiment.CommandResult{}, nil
	}
	if len(args) > 1 && args[1] == "image" {
		write("sha256:" + strings.Repeat("a", 64) + "\n")
		return experiment.CommandResult{}, nil
	}
	if len(args) > 1 && args[1] == "rm" {
		return experiment.CommandResult{}, nil
	}
	if len(args) > 1 && args[len(args)-1] == "--capabilities" {
		f.capabilities++
		identity := benchcontract.RuntimeVersion
		if f.wrongRuntime {
			identity = "unsupported"
		}
		write(fmt.Sprintf(`{"ktx_version":"test","opencode_version":%q,"generated_plugin_sha256":"%s","generated_plugin_matches_binary":true,"event_collector_contract":%q,"explicit_session_ownership":true,"serialized_callbacks":true,"complete_interval_lifecycle":true,"epoch_tracking":true,"strict_acknowledgements":true,"exact_model_step_identity":true,"retained_duplicate_callbacks":true,"accepted_model_step_projection":true,"callback_queue_continuation":true,"fail_closed_interval_validation":true,"preflight_before_auth":true,"normalized_kinds":["collector_open","model_step","retry","compaction","interruption","other"]}`+"\n", identity, strings.Repeat("a", 64), benchcontract.ContractVersion))
		return experiment.CommandResult{}, nil
	}
	if len(args) > 1 && args[1] == "run" {
		if f.beforeLaunch != nil {
			f.beforeLaunch()
		}
		f.launches++
		cfgPath := optimizeMount(args, "/home/node/.config/ktx/ktx.toml")
		side := strings.TrimPrefix(filepath.Ext(cfgPath), ".")
		cfgBytes, err := os.ReadFile(cfgPath)
		if err != nil {
			return experiment.CommandResult{}, err
		}
		if previous, exists := f.seenConfigs[side]; exists && !bytes.Equal(previous, cfgBytes) {
			f.t.Fatal("full config changed between launches")
		}
		f.seenConfigs[side] = cfgBytes
		if !strings.Contains(strings.Join(args, " "), "sha256:"+strings.Repeat("a", 64)) {
			f.t.Fatal("mutable image launched")
		}
		if f.cancel != nil {
			write(`{"type":"tool_use","part":{}}` + "\n")
			f.cancel()
			return experiment.CommandResult{ExitCode: 130}, ctx.Err()
		}
		database := optimizeMount(args, "/home/node/.local/share/ktx/ktx.db")
		owner, interval := optimizeEnv(args, benchcontract.SessionEnv), optimizeEnv(args, benchcontract.IntervalEnv)
		store, err := dbpkg.Open(database)
		if err != nil {
			return experiment.CommandResult{}, err
		}
		defer store.Close()
		captured := "2026-10-03T12:00:00Z"
		_, _, err = store.PersistRuntimeEvent(dbpkg.RuntimeEventInput{IntervalID: interval, SessionID: owner, EpochID: "epoch", Kind: "collector_open", EventType: "collector.open", CapturedAt: captured})
		if err != nil {
			return experiment.CommandResult{}, err
		}
		runtimeSession, message, part := "runtime-"+side, "message", "part"
		input := int64(100)
		if side == experiment.Candidate && !f.noImprovement {
			input = 40
		}
		zero := int64(0)
		cost := f.reportedCost
		measured := func(value *int64) telemetry.IntegerMeasurement {
			if value == nil {
				return telemetry.IntegerMeasurement{State: telemetry.MeasurementUnknown}
			}
			return telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: value}
		}
		reasoning := &zero
		if f.missingReasoning {
			reasoning = nil
		}
		costMeasurement := telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: &cost}
		if f.missingCost {
			costMeasurement = telemetry.CostMeasurement{State: telemetry.MeasurementUnknown}
		}
		obs := &telemetry.Observation{
			Key: telemetry.Key{SessionID: runtimeSession, MessageID: message, PartID: part}, Ordinal: 1, Reason: "stop", Runtime: benchcontract.Runtime, RuntimeVersion: benchcontract.RuntimeVersion, Source: telemetry.SourceEventHook, Cost: costMeasurement,
			Tokens: telemetry.Tokens{Input: measured(&input), Output: measured(&zero), Reasoning: measured(reasoning), CacheRead: measured(&zero), CacheWrite: measured(&zero)},
		}
		_, _, err = store.PersistRuntimeEvent(dbpkg.RuntimeEventInput{IntervalID: interval, SessionID: owner, EpochID: "epoch", EpochOrdinal: 1, Kind: "model_step", EventType: "message.part.updated", SourceSessionID: &runtimeSession, MessageID: &message, PartID: &part, CapturedAt: captured, Telemetry: obs})
		if err != nil {
			return experiment.CommandResult{}, err
		}
		toolID, err := dbpkg.NewID()
		if err != nil {
			return experiment.CommandResult{}, err
		}
		success := false
		_, err = store.PersistToolCallOnce(&dbpkg.ToolCall{ID: toolID, SessionID: owner, Tool: "bash", Input: `{"command":"cat tools/bench/corpus/qualification.txt"}`, Output: "controlled fixture", IsError: &success, ContentHash: dbpkg.ContentHash("controlled fixture")})
		if err != nil {
			return experiment.CommandResult{}, err
		}
		if err := store.SealRuntimeEventInterval(interval, owner, captured, dbpkg.RuntimeComplete, ""); err != nil {
			return experiment.CommandResult{}, err
		}
		write(`{"type":"tool_use","part":{}}` + "\n")
		tokens := map[string]any{"input": input, "output": 0, "cache": map[string]int{"read": 0, "write": 0}}
		if reasoning != nil {
			tokens["reasoning"] = 0
		}
		step := map[string]any{"sessionID": runtimeSession, "messageID": message, "id": part, "type": "step-finish", "reason": "stop", "tokens": tokens}
		if !f.missingCost {
			step["cost"] = cost
		}
		write(string(mustJSON(map[string]any{"type": "step_finish", "part": step})) + "\n")
		answer := "custodian=cedar-otter retry-policy=bounded-three release-key=violet-73 escalation=harbor-queue"
		if f.wrongAnswer && side == experiment.Candidate {
			answer = "wrong answer"
		}
		write(string(mustJSON(map[string]any{"type": "text", "part": map[string]any{"text": answer}})) + "\n")
		if f.tamper != "" && f.launches == 1 {
			target := cfgPath
			if f.tamper == "pricing" {
				target = filepath.Join(filepath.Dir(filepath.Dir(cfgPath)), "pricing.json")
			}
			if f.tamper == "manifest" {
				target = filepath.Join(filepath.Dir(filepath.Dir(cfgPath)), "manifest.json")
			}
			if f.tamper == "scoring" {
				target = filepath.Join(filepath.Dir(filepath.Dir(cfgPath)), "repository/tools/bench/specs/recovery-aware-comparison.yaml")
			}
			if f.tamper == "corpus" {
				target = filepath.Join(filepath.Dir(filepath.Dir(cfgPath)), "repository/tools/bench/corpus/qualification.txt")
			}
			if f.tamper == "database" {
				f.pendingDatabaseTamper = database
				return experiment.CommandResult{}, nil
			}
			file, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return experiment.CommandResult{}, err
			}
			_, writeErr := io.WriteString(file, "\nchanged\n")
			closeErr := file.Close()
			if writeErr != nil {
				return experiment.CommandResult{}, writeErr
			}
			if closeErr != nil {
				return experiment.CommandResult{}, closeErr
			}
		}
		return experiment.CommandResult{}, nil
	}
	// The producer's list operation is bound to its newly owned database only.
	if len(args) > 1 && args[1] == "list" {
		database := optimizeEnv(command.Env, "KTX_DB")
		if f.pendingDatabaseTamper != "" && database != f.pendingDatabaseTamper {
			file, err := os.OpenFile(f.pendingDatabaseTamper, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return experiment.CommandResult{}, err
			}
			_, writeErr := io.WriteString(file, "changed after collection")
			closeErr := file.Close()
			if writeErr != nil {
				return experiment.CommandResult{}, writeErr
			}
			if closeErr != nil {
				return experiment.CommandResult{}, closeErr
			}
			f.pendingDatabaseTamper = ""
		}
		if database == "" {
			f.t.Fatal("ambient database list attempted")
		}
		store, err := dbpkg.OpenReadOnly(database)
		if err != nil {
			return experiment.CommandResult{}, err
		}
		defer store.Close()
		var owner string
		if err := store.DB().QueryRow("SELECT id FROM sessions").Scan(&owner); err != nil {
			return experiment.CommandResult{}, err
		}
		write(string(mustJSON([]map[string]string{{"id": owner}})) + "\n")
		return experiment.CommandResult{}, nil
	}
	return experiment.CommandResult{}, fmt.Errorf("unexpected fake command: %v", args)
}

func optimizeLiveFixture(t *testing.T) (optimizeResult, optimizeArgs, *experiment.Runner, *optimizeFakeExecutor) {
	t.Helper()
	root := optimizeTestRoot(t)
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bin, "docker")
	optimizeWrite(t, path, "#!/bin/sh\nexit 99\n")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	auth := filepath.Join(os.Getenv("HOME"), ".local/share/opencode")
	if err := os.MkdirAll(auth, 0o700); err != nil {
		t.Fatal(err)
	}
	optimizeWrite(t, filepath.Join(auth, "auth.json"), "{}")
	development := optimizeRun(t)
	fake := &optimizeFakeExecutor{t: t, seenConfigs: map[string][]byte{}}
	runner := experiment.NewRunner()
	runner.Executor = fake
	runner.Now = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	return development, optimizeArgs{live: true, model: "openai/test", maxAttempts: 24}, runner, fake
}

func TestOptimizeLiveReadyAndUnavailableCost(t *testing.T) {
	development, args, runner, fake := optimizeLiveFixture(t)
	fake.missingCost = true
	var diagnostics bytes.Buffer
	fake.beforeLaunch = func() {
		if !strings.Contains(diagnostics.String(), "24 container/agent launches") || !strings.Contains(diagnostics.String(), "frozen before first model launch") {
			t.Fatal("launch before displayed bound and freeze")
		}
	}
	r, err := qualifyOptimize(context.Background(), development, args, runner, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != "ready" || !r.ApplyEligible || !r.Live.ValidationPassed || r.Live.CompletePairs != 5 || fake.launches != 14 || r.Live.AttemptsAdmitted != 14 {
		t.Fatalf("qualification: %+v / %+v launches=%d", r, r.Live, fake.launches)
	}
	if r.Scope.WorkloadEvidence || !strings.Contains(r.Live.Scope, "not representative") || r.Cost.State != "unavailable" || r.RuntimeTokens.State != "available" || *r.RuntimeTokens.Candidate != 40 || *r.RuntimeTokens.Baseline != 100 {
		t.Fatalf("claims: %+v", r)
	}
	if r.ID == development.ID {
		t.Fatal("development identity overwritten")
	}
	shown, err := loadOptimizeResult(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(shown)) != string(mustJSON(r)) {
		t.Fatal("show changed qualification")
	}
	if _, err := loadOptimizeResult(development.ID); err != nil {
		t.Fatal("development invalidated")
	}
	if bytes.Contains(fake.seenConfigs[experiment.Candidate], []byte("custodian")) {
		t.Fatal("fixture mixed into config")
	}
	archive := filepath.Join(".ktx/optimizations", r.ID)
	optimizeWrite(t, filepath.Join(archive, "experiments/qualification/attempts/baseline-001/stream.jsonl"), "modified")
	if _, err := loadOptimizeResult(r.ID); err == nil {
		t.Fatal("altered ready evidence accepted")
	}
}

func TestOptimizeLiveFailureCancellationAndTamper(t *testing.T) {
	for _, kind := range []string{"quality", "tokens", "no-improvement", "cancel", "config", "pricing", "manifest", "scoring", "corpus", "database", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			development, args, runner, fake := optimizeLiveFixture(t)
			ctx := context.Background()
			switch kind {
			case "quality":
				fake.wrongAnswer = true
			case "tokens":
				fake.missingReasoning = true
			case "no-improvement":
				fake.noImprovement = true
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				fake.cancel = cancel
			case "runtime":
				fake.wrongRuntime = true
			default:
				fake.tamper = kind
			}
			var diagnostics bytes.Buffer
			r, err := qualifyOptimize(ctx, development, args, runner, &diagnostics)
			if err != nil {
				t.Fatal(err)
			}
			if r.Outcome == "ready" || r.ApplyEligible || fake.launches > args.maxAttempts {
				t.Fatalf("failure eligible: %+v", r)
			}
			if kind == "runtime" && fake.launches != 0 {
				t.Fatal("unsupported runtime launched models")
			}
			if kind == "config" || kind == "pricing" || kind == "manifest" || kind == "scoring" || kind == "corpus" || kind == "database" || kind == "cancel" {
				if fake.launches != 1 || r.Live.AttemptsAdmitted != 1 {
					t.Fatalf("launch after stop: %d %+v", fake.launches, r.Live)
				}
			}
			if kind == "quality" && fake.launches != args.maxAttempts {
				t.Fatal("exhaustion did not consume finite bound")
			}
			if kind == "cancel" {
				attempts, err := experiment.LoadAttempts(filepath.Join(".ktx/optimizations", r.ID, r.Live.Experiment))
				if err != nil || len(attempts) != 1 || attempts[0].Status != "invalid" {
					t.Fatalf("cancelled launch not retained: %v %+v", err, attempts)
				}
				if !strings.Contains(strings.Join(r.Reasons, ","), "cancelled") {
					t.Fatal(r.Reasons)
				}
			}
			if kind == "no-improvement" && r.Outcome != "no_change" {
				t.Fatalf("token result: %+v", r)
			}
			if kind != "config" && kind != "pricing" && kind != "manifest" && kind != "scoring" && kind != "corpus" && kind != "database" {
				if _, err := loadOptimizeResult(r.ID); err != nil {
					t.Fatalf("failed result inspection: %v", err)
				}
			}
		})
	}
}

func TestOptimizeLiveReportedCostAndBadPricing(t *testing.T) {
	for _, badPricing := range []bool{false, true} {
		t.Run(fmt.Sprint(badPricing), func(t *testing.T) {
			development, args, runner, fake := optimizeLiveFixture(t)
			fake.reportedCost = 0.25
			if badPricing {
				optimizeWrite(t, "pricing.json", `{"provider":"wrong","model":"test","price":{"input":1,"output":1}}`)
				args.pricing = "pricing.json"
			}
			r, err := qualifyOptimize(context.Background(), development, args, runner, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if badPricing {
				if fake.launches != 0 || r.Outcome != "blocked" || r.ApplyEligible {
					t.Fatal("invalid price identity treated as mere missing cost")
				}
			} else if r.Outcome != "ready" || r.Live.CostSelection.Basis != "reported" {
				t.Fatalf("reported-first authority: %+v", r.Live)
			}
		})
	}
}

func TestOptimizeLiveConsentAndRoutingBlock(t *testing.T) {
	for _, argv := range [][]string{{"--live"}, {"--model", "openai/test"}, {"--live", "--model", "github-copilot/test"}, {"--live", "--model", "openai/test", "--max-attempts", "13"}} {
		optimizeTestRoot(t)
		code, _, _ := optimizeInvoke(t, argv...)
		if code != ExitUsage {
			t.Fatalf("consent %v: %d", argv, code)
		}
	}
	_, args, runner, fake := optimizeLiveFixture(t)
	optimizeWrite(t, "target.toml", "[pipeline.default]\nstages=[]\n[pipeline.selected]\nstages=['normalize']\n[[route]]\ntool='read'\nexit_status='success'\nprofile='selected'\n")
	development := optimizeRun(t, "--config", "target.toml")
	r, err := qualifyOptimize(context.Background(), development, args, runner, io.Discard)
	if err != nil || fake.launches != 0 || fake.capabilities != 0 || r.Outcome != "blocked" {
		t.Fatalf("success-only read claimed routable: %+v %v", r, err)
	}
}

func TestOptimizeLivePairGate(t *testing.T) {
	makeSummary := func(pairCount int, qualified bool, candidateValue any) experiment.Summary {
		pairs := []any{}
		for i := 0; i < pairCount; i++ {
			pairs = append(pairs, map[string]any{"baseline": map[string]any{"correct": true, "runtime_total_tokens": 100}, "candidate": map[string]any{"correct": true, "runtime_total_tokens": candidateValue}})
		}
		arms := map[string]any{}
		for side, value := range map[string]any{"baseline": 100, "candidate": candidateValue} {
			arms[side] = map[string]any{"quality_gate": map[string]any{"qualified": qualified, "correct_task_count": 5, "evaluated_task_count": 5}, "runtime_total_tokens_per_correct_task": map[string]any{"state": "available", "value": value}}
		}
		return experiment.Summary{"recovery_aware_comparison": map[string]any{"arms": arms, "paired_cycles": pairs}}
	}
	for _, tt := range []struct {
		pairs     int
		qualified bool
		value     any
		ready     bool
	}{{5, true, 40, true}, {4, true, 40, false}, {5, false, 40, false}, {5, true, nil, false}, {5, true, 100, false}} {
		r := optimizeResult{Outcome: "incomplete", Live: &optimizeQualification{MinimumCompletePairs: 5, ValidationPassed: true}}
		assessOptimizeQualification(&r, makeSummary(tt.pairs, tt.qualified, tt.value))
		if (r.Outcome == "ready") != tt.ready || r.ApplyEligible != tt.ready {
			t.Fatalf("pair gate %+v: %+v", tt, r)
		}
	}
	r := optimizeResult{Outcome: "incomplete", Live: &optimizeQualification{MinimumCompletePairs: 5, ValidationPassed: true}}
	summary := makeSummary(5, true, 40)
	comparison := summary["recovery_aware_comparison"].(map[string]any)
	comparison["arms"].(map[string]any)["candidate"].(map[string]any)["quality_gate"].(map[string]any)["evaluated_task_count"] = 6
	assessOptimizeQualification(&r, summary)
	if r.ApplyEligible || r.Outcome == "ready" || r.Reasons[0] != "behavioral_quality_not_preserved" {
		t.Fatal("retries hid a candidate quality regression")
	}
}
