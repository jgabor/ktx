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
	methodology := write("tools/bench/specs/cache-aware.yaml", "methodology_version: v5-cache-aware\n")
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
	identities := []modelStepIdentity{{SessionID: "session-a", MessageID: "message-a", PartID: "part-a"}, {SessionID: "session-b", MessageID: "message-b", PartID: "part-b"}, {SessionID: "session-c", MessageID: "message-c", PartID: "part-c"}}
	callbackIdentities := []modelStepIdentity{identities[0], identities[1], identities[1], identities[2]}
	for index, identity := range callbackIdentities {
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
		{name: "exact duplicate occurrence is not a second measurement", identities: identities, steps: 3},
		{name: "missing", identities: []modelStepIdentity{{}, identities[1], identities[2]}, steps: 3, wantReason: "schema 3 wrapper model-step identity is missing or partial"},
		{name: "partial", identities: []modelStepIdentity{{SessionID: "session-a", MessageID: "message-a"}, identities[1], identities[2]}, steps: 3, wantReason: "schema 3 wrapper model-step identity is missing or partial"},
		{name: "reordered", identities: []modelStepIdentity{identities[1], identities[0], identities[2]}, steps: 3, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "duplicate", identities: []modelStepIdentity{identities[0], identities[0], identities[2]}, steps: 3, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "mismatch", identities: []modelStepIdentity{{SessionID: "foreign", MessageID: "message-a", PartID: "part-a"}, identities[1], identities[2]}, steps: 3, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "message-mismatch", identities: []modelStepIdentity{{SessionID: "session-a", MessageID: "foreign-message", PartID: "part-a"}, identities[1], identities[2]}, steps: 3, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "part-mismatch", identities: []modelStepIdentity{{SessionID: "session-a", MessageID: "message-a", PartID: "foreign-part"}, identities[1], identities[2]}, steps: 3, wantReason: "schema 3 wrapper and event-hook model-step identity order does not match"},
		{name: "count", identities: identities[:2], steps: 2, wantReason: "schema 3 wrapper and event-hook model-step counts do not match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := Metrics{ModelSteps: make([]SourceEvent, test.steps), wrapperModelIdentities: test.identities}
			sequence, reason := materializeEventHookSequence(database.DB(), attempt, metrics)
			if reason != test.wantReason {
				t.Fatalf("reason=%q want=%q sequence=%#v", reason, test.wantReason, sequence)
			}
			if test.wantReason == "" && (sequence == nil || len(sequence.Coverage) != 6 || len(sequence.Facts) != 3 || len(sequence.Events) != 5) {
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

func TestRecoveryComparisonAllowsOnlyAuxiliaryCountDivergence(t *testing.T) {
	manifest := Manifest{Methodology: MethodologyMetadata{Version: "v7-recovery-aware-comparison"}}
	countMismatch := "schema 3 wrapper and event-hook model-step counts do not match"
	if !auxiliaryOutcomeIncomplete(manifest, Attempt{Role: ColdObservation, validationReasons: []string{countMismatch}}) {
		t.Fatal("auxiliary count divergence was not recognized")
	}
	for _, attempt := range []Attempt{
		{Role: MeasuredWarm, validationReasons: []string{countMismatch}},
		{Role: ColdObservation, validationReasons: []string{"schema 3 runtime event interval is not complete"}},
		{Role: ColdObservation, validationReasons: []string{countMismatch, "other"}},
	} {
		if auxiliaryOutcomeIncomplete(manifest, attempt) {
			t.Fatalf("contradictory attempt accepted: %#v", attempt)
		}
	}
}

func TestSchema3RetainedContractVersionControlsLedgerInterpretation(t *testing.T) {
	tests := []struct {
		name       string
		contract   string
		classified bool
		wantReason string
	}{
		{name: "unmigrated schema 7 v1", contract: benchcontract.LegacyContractVersion},
		{name: "schema 8 migrated v1", contract: benchcontract.LegacyContractVersion, classified: true},
		{name: "unknown contract", contract: "opencode-event-v4", classified: true, wantReason: "schema 3 runtime event interval provenance does not match the required contract"},
		{name: "v2 without classifications", contract: benchcontract.PriorContractVersion, wantReason: "schema 3 v2 runtime events are missing duplicate classifications"},
		{name: "v3 without classifications", contract: benchcontract.ContractVersion, wantReason: "schema 3 v2 runtime events are missing duplicate classifications"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, "open")
			if err != nil {
				t.Fatal(err)
			}
			installRetainedSchema3Ledger(t, database, interval, test.contract, test.classified)
			attempt := Attempt{SchemaVersion: 3, AttemptID: "attempt", SessionID: &interval.SessionID, RuntimeEventIntervalID: &interval.ID}
			metrics := Metrics{ModelSteps: make([]SourceEvent, 2), wrapperModelIdentities: []modelStepIdentity{
				{SessionID: "runtime", MessageID: "message-w1", PartID: "w1"},
				{SessionID: "runtime", MessageID: "message-w2", PartID: "w2"},
			}}
			sequence, reason := materializeEventHookSequence(database.DB(), attempt, metrics)
			if reason != test.wantReason {
				t.Fatalf("reason=%q want=%q sequence=%#v", reason, test.wantReason, sequence)
			}
			if test.wantReason == "" && (sequence == nil || len(sequence.Facts) != 2 || len(sequence.Events) != 3 || len(sequence.Coverage) != 6) {
				t.Fatalf("valid retained v1 sequence=%#v", sequence)
			}
		})
	}
}

func installRetainedSchema3Ledger(t *testing.T, database *db.DB, interval *db.RuntimeEventInterval, contract string, classified bool) {
	t.Helper()
	intervalColumns := "collector_epoch_count INTEGER, failure_reason TEXT"
	eventColumns := "captured_at TEXT"
	if classified {
		intervalColumns = "collector_epoch_count INTEGER, contradiction_count INTEGER DEFAULT 0, failure_reason TEXT"
		eventColumns = "captured_at TEXT, model_step_classification TEXT"
	}
	if _, err := database.DB().Exec(`DROP TABLE runtime_events; DROP TABLE runtime_event_intervals;
		CREATE TABLE runtime_event_intervals (
			id TEXT, session_id TEXT, attempt_id TEXT, runtime TEXT, runtime_version TEXT, source TEXT,
			contract_version TEXT, opened_at TEXT, closed_at TEXT, close_state TEXT,
			first_ordinal INTEGER, last_ordinal INTEGER, event_count INTEGER, ` + intervalColumns + `);
		CREATE TABLE runtime_events (
			interval_id TEXT, ordinal INTEGER, epoch_id TEXT, epoch_ordinal INTEGER, kind TEXT, event_type TEXT,
			source_session_id TEXT, message_id TEXT, part_id TEXT, ` + eventColumns + `)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`UPDATE sessions SET ended_at = 'closed' WHERE id = ?`, interval.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`INSERT INTO runtime_event_intervals
		(id,session_id,attempt_id,runtime,runtime_version,source,contract_version,opened_at,closed_at,close_state,first_ordinal,last_ordinal,event_count,collector_epoch_count,failure_reason)
		VALUES (?,?,'attempt',?,?,'event_hook',?,'open','closed','complete',1,3,3,1,NULL)`, interval.ID, interval.SessionID, benchcontract.Runtime, benchcontract.RuntimeVersion, contract); err != nil {
		t.Fatal(err)
	}
	if classified {
		_, err := database.DB().Exec(`INSERT INTO runtime_events VALUES
			(?,1,'epoch',0,'collector_open','collector.open',NULL,NULL,NULL,'open',NULL),
			(?,2,'epoch',1,'model_step','message.part.updated','runtime','message-w1','w1','event','accepted'),
			(?,3,'epoch',2,'model_step','message.part.updated','runtime','message-w2','w2','event','accepted')`, interval.ID, interval.ID, interval.ID)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		_, err := database.DB().Exec(`INSERT INTO runtime_events VALUES
			(?,1,'epoch',0,'collector_open','collector.open',NULL,NULL,NULL,'open'),
			(?,2,'epoch',1,'model_step','message.part.updated','runtime','message-w1','w1','event'),
			(?,3,'epoch',2,'model_step','message.part.updated','runtime','message-w2','w2','event')`, interval.ID, interval.ID, interval.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchema3LegacyLedgerWithMissingDuplicateOccurrenceRemainsInvalid(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, "open")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`DROP TABLE runtime_events; DROP TABLE runtime_event_intervals;
		CREATE TABLE runtime_event_intervals (
			id TEXT, session_id TEXT, attempt_id TEXT, runtime TEXT, runtime_version TEXT, source TEXT,
			contract_version TEXT, opened_at TEXT, closed_at TEXT, close_state TEXT,
			first_ordinal INTEGER, last_ordinal INTEGER, event_count INTEGER, collector_epoch_count INTEGER, failure_reason TEXT);
		CREATE TABLE runtime_events (
			interval_id TEXT, ordinal INTEGER, epoch_id TEXT, epoch_ordinal INTEGER, kind TEXT, event_type TEXT,
			source_session_id TEXT, message_id TEXT, part_id TEXT, captured_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`UPDATE sessions SET ended_at = 'closed' WHERE id = ?`, interval.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`INSERT INTO runtime_event_intervals VALUES (?,?,'attempt',?,?,'event_hook',?,'open','closed','complete',1,3,3,1,NULL)`, interval.ID, interval.SessionID, benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.LegacyContractVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`INSERT INTO runtime_events VALUES
			(?,1,'epoch',0,'collector_open','collector.open',NULL,NULL,NULL,'open'),
			(?,2,'epoch',1,'model_step','message.part.updated','runtime','message-w1','w1','event'),
			(?,3,'epoch',2,'model_step','message.part.updated','runtime','message-w2','w2','event')`, interval.ID, interval.ID, interval.ID); err != nil {
		t.Fatal(err)
	}
	owner, intervalID := interval.SessionID, interval.ID
	attempt := Attempt{SchemaVersion: 3, AttemptID: "attempt", SessionID: &owner, RuntimeEventIntervalID: &intervalID}
	metrics := Metrics{ModelSteps: make([]SourceEvent, 3), wrapperModelIdentities: []modelStepIdentity{
		{SessionID: "runtime", MessageID: "message-w1", PartID: "w1"},
		{SessionID: "runtime", MessageID: "message-w2", PartID: "w2"},
		{SessionID: "runtime", MessageID: "message-w3", PartID: "w3"},
	}}
	_, reason := materializeEventHookSequence(database.DB(), attempt, metrics)
	if reason != "schema 3 wrapper and event-hook model-step counts do not match" {
		t.Fatalf("legacy failed archive reason=%q", reason)
	}
}
