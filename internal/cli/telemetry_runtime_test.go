package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

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
	database, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, "open")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.AttemptSessionEnv, interval.SessionID)
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

func TestBenchAgentRejectsPartialCollectorEnvironmentBeforeAuth(t *testing.T) {
	t.Setenv("KTX_BENCH_COLLECTOR_MODE", "required")
	t.Setenv("OPENCODE_AUTH_PATH", filepath.Join(t.TempDir(), "missing-auth"))
	var stdout, stderr bytes.Buffer
	code := runBenchAgent([]string{"-m", "openai/test", "prompt"}, Options{Format: FormatText}, &stdout, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), "benchmark event collection requires") || strings.Contains(stderr.String(), "auth missing") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
