package experiment

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/telemetry"
)

func buildValidSchema3Experiment(t *testing.T) string {
	t.Helper()
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
	write("README.md", "fixture\n")
	methodology := write("tools/bench/methodology-v5-cache-aware.yaml", "methodology_version: v5-cache-aware\n")
	task := write("tools/bench/tasks/task.toml", "id = \"task\"\nprompt = \"prompt\"\nminimum_tool_calls = 1\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
	baseline := write("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
	candidate := write("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = []\n")
	auth := write("auth.json", "{}\n")
	fake := &fakeExecutor{sessions: map[string]string{}, opencodeIdentity: benchcontract.RuntimeVersion, fullStepUsage: true}
	runner := &Runner{Executor: fake, Now: func() time.Time { return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC) }, PID: 77, ResolvePrice: testPriceResolver}
	directory, _, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "openai/test", TaskFile: task, TargetValid: 1, MaxAttempts: 4, WarmupSet: true, CacheControl: "observed",
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: "schema3-audit", DataDir: filepath.Join(root, "data"),
		Image: "image", AuthPath: auth, RepoRoot: root, MethodologyPath: methodology, BaselineConfig: baseline, CandidateConfig: candidate,
		KTXBin: "ktx", DockerBin: "docker",
	})
	if err != nil || code != 0 {
		t.Fatalf("fixture code=%d err=%v", code, err)
	}
	return directory
}

func TestSchema3RequiresExactWrapperEventModelStepIdentityOrder(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, "open")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.PersistRuntimeEvent(db.RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch", Kind: "collector_open", EventType: "collector.open", CapturedAt: "one"}); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	cost := float64(0)
	identities := []modelStepIdentity{{SessionID: "session-a", MessageID: "message-a", PartID: "part-a"}, {SessionID: "session-b", MessageID: "message-b", PartID: "part-b"}}
	for index, identity := range identities {
		observation := &telemetry.Observation{Key: telemetry.Key{SessionID: identity.SessionID, MessageID: identity.MessageID, PartID: identity.PartID}, Ordinal: uint64(index + 1), Reason: "stop", Runtime: benchcontract.Runtime, RuntimeVersion: benchcontract.RuntimeVersion, Source: telemetry.SourceEventHook, Cost: telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: &cost}, Tokens: telemetry.Tokens{Input: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, Output: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, Reasoning: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheRead: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheWrite: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}}}
		if _, _, err := database.PersistRuntimeEvent(db.RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch", EpochOrdinal: uint64(index + 1), Kind: "model_step", EventType: "message.part.updated", SourceSessionID: &identity.SessionID, MessageID: &identity.MessageID, PartID: &identity.PartID, CapturedAt: "event", Telemetry: observation}); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.SealRuntimeEventInterval(interval.ID, interval.SessionID, "closed", db.RuntimeComplete, ""); err != nil {
		t.Fatal(err)
	}
	attempt := Attempt{SchemaVersion: 3, AttemptID: "attempt", SessionID: &interval.SessionID, RuntimeEventIntervalID: &interval.ID}
	tests := []struct {
		name       string
		identities []modelStepIdentity
		steps      int
		wantReason string
	}{
		{name: "exact", identities: identities, steps: 2},
		{name: "missing", identities: []modelStepIdentity{{}, identities[1]}, steps: 2, wantReason: "schema 3 wrapper model-step identity is missing or partial"},
		{name: "partial", identities: []modelStepIdentity{{SessionID: "session-a", MessageID: "message-a"}, identities[1]}, steps: 2, wantReason: "schema 3 wrapper model-step identity is missing or partial"},
		{name: "reordered", identities: []modelStepIdentity{identities[1], identities[0]}, steps: 2, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "duplicate", identities: []modelStepIdentity{identities[0], identities[0]}, steps: 2, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "mismatch", identities: []modelStepIdentity{{SessionID: "foreign", MessageID: "message-a", PartID: "part-a"}, identities[1]}, steps: 2, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "message-mismatch", identities: []modelStepIdentity{{SessionID: "session-a", MessageID: "foreign-message", PartID: "part-a"}, identities[1]}, steps: 2, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "part-mismatch", identities: []modelStepIdentity{{SessionID: "session-a", MessageID: "message-a", PartID: "foreign-part"}, identities[1]}, steps: 2, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "count", identities: identities[:1], steps: 1, wantReason: "schema 3 wrapper and event-hook model-step counts do not match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := Metrics{ModelSteps: make([]SourceEvent, test.steps), wrapperModelIdentities: test.identities}
			sequence, reason := materializeEventHookSequence(database.DB(), attempt, metrics)
			if reason != test.wantReason {
				t.Fatalf("reason=%q want=%q sequence=%#v", reason, test.wantReason, sequence)
			}
			if test.wantReason == "" && (sequence == nil || len(sequence.Coverage) != 6) {
				t.Fatalf("valid sequence=%#v", sequence)
			}
		})
	}
}

func TestSchema3IncompleteIntervalsCannotRegenerateIntoValidEvidence(t *testing.T) {
	directory := buildValidSchema3Experiment(t)
	attempts, err := LoadAttempts(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		path := filepath.Join(directory, filepath.FromSlash(attempt.Artifacts["database"]))
		database, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.DB().Exec(`UPDATE runtime_event_intervals SET close_state = 'incomplete', failure_reason = 'audit mutation'`); err != nil {
			t.Fatal(err)
		}
		database.Close()
	}
	if _, err := RegenerateSummary(directory); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	report, err := ValidateExperiment(directory)
	if err != nil {
		t.Fatal(err)
	}
	if valid, _ := report["valid"].(bool); valid {
		t.Fatalf("validation unexpectedly passed: %#v", report)
	}
	checks := report["attempt_checks"].([]map[string]any)
	for _, check := range checks {
		reasons := check["reasons"].([]string)
		if !containsString(reasons, "schema 3 runtime event interval is not complete") {
			t.Fatalf("attempt reasons=%v", reasons)
		}
	}
	if target, _ := report["target_met"].(bool); target {
		t.Fatal("incomplete schema 3 intervals satisfied the completed experiment target")
	}
	methodologyReasons := report["methodology_reasons"].([]string)
	if len(methodologyReasons) != len(attempts) {
		t.Fatalf("methodology reasons=%v", methodologyReasons)
	}
}
