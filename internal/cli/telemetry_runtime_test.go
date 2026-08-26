package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestNormalizeRuntimeEventKindsAndIdentityPrecedence(t *testing.T) {
	tests := []struct {
		name, raw, kind, session string
		model                    bool
	}{
		{"model", `{"type":"message.part.updated","properties":{"sessionID":"property","info":{"sessionID":"info"},"part":{"type":"step-finish","sessionID":"part","messageID":"message","id":"part-id","reason":"stop","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}}}`, "model_step", "part", true},
		{"retry-part", `{"type":"message.part.updated","properties":{"part":{"type":"retry","sessionID":"part"}}}`, "retry", "part", false},
		{"retry-status", `{"type":"session.status","properties":{"sessionID":"property","status":{"type":"retry"}}}`, "retry", "property", false},
		{"compaction-part", `{"type":"message.part.updated","properties":{"part":{"type":"compaction","sessionID":"part"}}}`, "compaction", "part", false},
		{"compacted", `{"type":"session.compacted","properties":{"sessionID":"property"}}`, "compaction", "property", false},
		{"error", `{"type":"session.error","properties":{"sessionID":"property","error":"secret body"}}`, "interruption", "property", false},
		{"disposed", `{"type":"server.instance.disposed","properties":{}}`, "interruption", "", false},
		{"other-info", `{"type":"message.updated","properties":{"info":{"sessionID":"info","content":"prompt"}}}`, "other", "info", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, observation, err := normalizeRuntimeEvent(json.RawMessage(test.raw))
			if err != nil || input.Kind != test.kind {
				t.Fatalf("input=%#v observation=%#v err=%v", input, observation, err)
			}
			if test.session == "" && input.SourceSessionID != nil || test.session != "" && (input.SourceSessionID == nil || *input.SourceSessionID != test.session) {
				t.Fatalf("source session=%v", input.SourceSessionID)
			}
			if test.model != (observation != nil) {
				t.Fatalf("model observation=%#v", observation)
			}
		})
	}
}

func TestRequiredCollectorCommandsUseExactPrecreatedOwner(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	openedAt := time.Now().UTC().Format(time.RFC3339Nano)
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, openedAt)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.SessionEnv, interval.SessionID)
	t.Setenv(benchcontract.IntervalEnv, interval.ID)
	t.Setenv(benchcontract.ContractEnv, benchcontract.ContractVersion)
	t.Setenv(benchcontract.CollectorModeEnv, benchcontract.CollectorRequired)
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	var stdout, stderr bytes.Buffer
	if _, err := telemetryLogic([]string{"collector-open"}, FormatJSON, strings.NewReader(`{"epoch_id":"epoch"}`), database, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var openAck runtimeEventAck
	if err := json.Unmarshal(stdout.Bytes(), &openAck); err != nil || !openAck.OK || openAck.SessionID != interval.SessionID || openAck.Ordinal != 1 {
		t.Fatalf("ack=%#v err=%v", openAck, err)
	}
	stdout.Reset()
	payload := `{"epoch_id":"epoch","epoch_ordinal":1,"after_ordinal":1,"event":{"type":"message.part.updated","properties":{"part":{"type":"step-finish","sessionID":"runtime","messageID":"message","id":"part","reason":"stop","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}}}}`
	if _, err := telemetryLogic([]string{"event-record"}, FormatJSON, strings.NewReader(payload), database, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.LoadRuntimeEventInterval(interval.ID)
	if err != nil || loaded.EventCount != 2 || len(loaded.Events) != 2 || loaded.Events[1].Kind != "model_step" {
		t.Fatalf("interval=%#v err=%v", loaded, err)
	}
	stdout.Reset()
	stale := `{"epoch_id":"epoch","epoch_ordinal":2,"after_ordinal":1,"event":{"type":"session.status","properties":{"status":{"type":"retry"}}}}`
	if _, err := telemetryLogic([]string{"event-record"}, FormatJSON, strings.NewReader(stale), database, &stdout, &stderr); err == nil {
		t.Fatal("event-record accepted a stale global ordinal")
	}
	loaded, err = database.LoadRuntimeEventInterval(interval.ID)
	if err != nil || loaded.EventCount != 2 {
		t.Fatalf("stale event changed interval=%#v err=%v", loaded, err)
	}
	entries, err := filepath.Glob(filepath.Join(runtimeDir, "ktx", "sessions", "*.json"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("parent session files=%v err=%v", entries, err)
	}
}

func TestRequiredCueDeliveryRejectsIntervalOwnershipMismatch(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	openedAt := time.Now().UTC().Format(time.RFC3339Nano)
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, openedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureCollectionInterval(interval.ID, []string{"run"}, openedAt); err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.SessionEnv, interval.SessionID)
	t.Setenv(benchcontract.IntervalEnv, interval.ID)
	t.Setenv(benchcontract.ContractEnv, benchcontract.ContractVersion)
	t.Setenv(benchcontract.CollectorModeEnv, benchcontract.CollectorRequired)
	if _, err := telemetryLogic([]string{"collector-open"}, FormatJSON, strings.NewReader(`{"epoch_id":"epoch"}`), database, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.SessionEnv, "wrongwrongwrongw")
	var stdout bytes.Buffer
	err = acknowledgeRecoveryCue(nil, strings.NewReader(`{}`), database, &stdout)
	if err == nil || !strings.Contains(err.Error(), "benchmark interval owner is unavailable") || stdout.Len() != 0 {
		t.Fatalf("err=%v stdout=%q", err, stdout.String())
	}
}

func TestRequiredRecoveryOwnerUsesIntervalAcrossMountedDatabasePath(t *testing.T) {
	database, err := dbpkg.Open(filepath.Join(t.TempDir(), "attempt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	openedAt := time.Now().UTC().Format(time.RFC3339Nano)
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, openedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureCollectionInterval(interval.ID, []string{"run"}, openedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`UPDATE collection_intervals SET database_target='/host/owner/attempt.db' WHERE id=?`, interval.ID); err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.SessionEnv, interval.SessionID)
	t.Setenv(benchcontract.IntervalEnv, interval.ID)
	t.Setenv(benchcontract.ContractEnv, benchcontract.ContractVersion)
	t.Setenv(benchcontract.CollectorModeEnv, benchcontract.CollectorRequired)
	if _, err := telemetryLogic([]string{"collector-open"}, FormatJSON, strings.NewReader(`{"epoch_id":"epoch"}`), database, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	collection, session, present, err := recoveryEvidenceOwner(database)
	if err != nil || !present || collection != interval.ID || session != interval.SessionID {
		t.Fatalf("collection=%q session=%q present=%v err=%v", collection, session, present, err)
	}
}

func TestBenchAgentRejectsPartialCollectorEnvironmentBeforeAuth(t *testing.T) {
	t.Setenv("KTX_BENCH_COLLECTOR_MODE", "required")
	t.Setenv("OPENCODE_AUTH_PATH", filepath.Join(t.TempDir(), "missing-auth"))
	var stdout, stderr bytes.Buffer
	code := runBenchAgent([]string{"-m", "openai/test", "prompt"}, Options{Format: FormatText}, &stdout, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), "benchmark event collection requires") || strings.Contains(stderr.String(), "auth missing") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRequiredCollectorAcknowledgesExactDuplicateAndContinues(t *testing.T) {
	database, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, "open")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.SessionEnv, interval.SessionID)
	t.Setenv(benchcontract.IntervalEnv, interval.ID)
	t.Setenv(benchcontract.ContractEnv, benchcontract.ContractVersion)
	t.Setenv(benchcontract.CollectorModeEnv, benchcontract.CollectorRequired)
	var stdout, stderr bytes.Buffer
	if _, err := telemetryLogic([]string{"collector-open"}, FormatJSON, strings.NewReader(`{"epoch_id":"epoch"}`), database, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	step := func(epoch, after int, part string, input int) runtimeEventAck {
		t.Helper()
		stdout.Reset()
		payload := fmt.Sprintf(`{"epoch_id":"epoch","epoch_ordinal":%d,"after_ordinal":%d,"event":{"type":"message.part.updated","properties":{"part":{"type":"step-finish","sessionID":"runtime","messageID":"message-%s","id":"%s","reason":"stop","cost":0,"tokens":{"input":%d,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}}}}`, epoch, after, part, part, input)
		if _, err := telemetryLogic([]string{"event-record"}, FormatJSON, strings.NewReader(payload), database, &stdout, &stderr); err != nil {
			t.Fatalf("event %d: %v", epoch, err)
		}
		var ack runtimeEventAck
		if err := json.Unmarshal(stdout.Bytes(), &ack); err != nil {
			t.Fatal(err)
		}
		return ack
	}
	for index, input := range []struct {
		part  string
		value int
		class string
	}{{"w1", 10, dbpkg.ModelStepAccepted}, {"w2", 20, dbpkg.ModelStepAccepted}, {"w2", 20, dbpkg.ModelStepExactDuplicate}, {"w3", 30, dbpkg.ModelStepAccepted}} {
		ack := step(index+1, index+1, input.part, input.value)
		if ack.Ordinal != uint64(index+2) || ack.ModelStepClassification == nil || *ack.ModelStepClassification != input.class {
			t.Fatalf("ack %d=%#v", index, ack)
		}
	}
	loaded, err := database.LoadRuntimeEventInterval(interval.ID)
	if err != nil || loaded.EventCount != 5 || loaded.ContradictionCount != 0 {
		t.Fatalf("interval=%#v err=%v", loaded, err)
	}
	observations, err := database.ListSessionTelemetry(interval.SessionID, 10)
	if err != nil || len(observations) != 3 || observations[0].PartID != "w1" || observations[1].PartID != "w2" || observations[2].PartID != "w3" {
		t.Fatalf("accepted observations=%#v err=%v", observations, err)
	}
}

func TestRequiredCollectorConflictingDuplicateFailsAfterCommit(t *testing.T) {
	database, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, "open")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.SessionEnv, interval.SessionID)
	t.Setenv(benchcontract.IntervalEnv, interval.ID)
	t.Setenv(benchcontract.ContractEnv, benchcontract.ContractVersion)
	t.Setenv(benchcontract.CollectorModeEnv, benchcontract.CollectorRequired)
	var stdout, stderr bytes.Buffer
	if _, err := telemetryLogic([]string{"collector-open"}, FormatJSON, strings.NewReader(`{"epoch_id":"epoch"}`), database, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	record := func(epoch, after, input int) error {
		stdout.Reset()
		payload := fmt.Sprintf(`{"epoch_id":"epoch","epoch_ordinal":%d,"after_ordinal":%d,"event":{"type":"message.part.updated","properties":{"part":{"type":"step-finish","sessionID":"runtime","messageID":"message","id":"part","reason":"stop","cost":0,"tokens":{"input":%d,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}}}}`, epoch, after, input)
		_, err := telemetryLogic([]string{"event-record"}, FormatJSON, strings.NewReader(payload), database, &stdout, &stderr)
		return err
	}
	if err := record(1, 1, 10); err != nil {
		t.Fatal(err)
	}
	if err := record(2, 2, 11); err == nil || !strings.Contains(err.Error(), "conflicting duplicate model step retained") {
		t.Fatalf("conflicting callback err=%v", err)
	}
	loaded, err := database.LoadRuntimeEventInterval(interval.ID)
	if err != nil || loaded.EventCount != 3 || loaded.ContradictionCount != 1 || loaded.Events[2].ModelStepClassification == nil || *loaded.Events[2].ModelStepClassification != dbpkg.ModelStepConflictingDuplicate {
		t.Fatalf("retained conflict=%#v err=%v", loaded, err)
	}
	observations, err := database.ListSessionTelemetry(interval.SessionID, 10)
	if err != nil || len(observations) != 1 || observations[0].UncachedInput.Value == nil || *observations[0].UncachedInput.Value != 10 {
		t.Fatalf("first telemetry=%#v err=%v", observations, err)
	}
}
