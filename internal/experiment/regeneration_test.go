package experiment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestAuthoritativeRegenerationIgnoresProjectionDriftAndDuplicates(t *testing.T) {
	dir, manifest, attempt := writeAuthorityFixture(t)
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	writeAttemptRecords(t, dir, attempt)
	first, err := RegenerateSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}

	drifted := attempt
	drifted.ToolCallCount = 9
	drifted.TokensIn = float64Pointer(1)
	drifted.CacheRead = float64Pointer(1)
	writeAttemptRecords(t, dir, drifted, drifted)
	second, err := RegenerateSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	regenerated, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, regenerated) {
		t.Fatal("summary bytes changed after copied projection drift")
	}
	if len(second.ProjectionDivergences) == 0 {
		t.Fatal("projection drift was not reported")
	}
	pipeline := second.Summary["pipelines"].(map[string]any)[Baseline].(map[string]any)
	if pipeline["attempt_count"] != 1 {
		t.Fatalf("attempt_count = %v, want one idempotent observation", pipeline["attempt_count"])
	}
	if first.Summary["schema_version"] != second.Summary["schema_version"] {
		t.Fatal("canonical summary schema changed")
	}
}

func TestAuthoritativeRegenerationRejectsConflictingDuplicate(t *testing.T) {
	dir, manifest, attempt := writeAuthorityFixture(t)
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	conflict := attempt
	conflict.Side = Candidate
	writeAttemptRecords(t, dir, attempt, conflict)
	if _, err := RegenerateSummary(dir); err == nil || !strings.Contains(err.Error(), "conflicting authoritative duplicate") {
		t.Fatalf("error = %v, want visible authoritative duplicate conflict", err)
	}
	got, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Side != Baseline {
		t.Fatal("first evidence was replaced")
	}
}

func TestAuthoritativeRegenerationRejectsMalformedAuthorityLink(t *testing.T) {
	dir, manifest, attempt := writeAuthorityFixture(t)
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	attempt.Artifacts["stream"] = "attempts/baseline-001/missing-stream.jsonl"
	writeAttemptRecords(t, dir, attempt)
	if _, err := RegenerateSummary(dir); err == nil || !strings.Contains(err.Error(), "read stream authority") {
		t.Fatalf("error = %v, want malformed authority-link failure", err)
	}
}

func TestLegacyRegenerationPreservesSerializedBytes(t *testing.T) {
	dir := t.TempDir()
	manifest := Manifest{ExperimentID: "legacy", Methodology: MethodologyMetadata{Version: "v4"}, Task: Task{MinimumToolCalls: 1}}
	session := "legacy-session"
	attempt := Attempt{SchemaVersion: 1, ExperimentID: "legacy", AttemptID: "baseline-001", Side: Baseline, Status: "completed", SessionID: &session, Metrics: Metrics{StreamComplete: true, ToolCallCount: 1}}
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	writeAttemptRecords(t, dir, attempt)
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
	if !bytes.Equal(first, second) {
		t.Fatal("legacy summary regeneration was not byte-identical")
	}
}

func writeAuthorityFixture(t *testing.T) (string, Manifest, Attempt) {
	t.Helper()
	dir := t.TempDir()
	attemptDir := filepath.Join(dir, "attempts", "baseline-001")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(attemptDir, "ktx.db")
	database, err := dbpkg.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	session := "abcdefghijklmnop"
	if err := database.InsertSession(&dbpkg.Session{ID: session, Agent: "test"}); err != nil {
		t.Fatal(err)
	}
	tool := &dbpkg.ToolCall{ID: "qrstuvwxyzabcdef", SessionID: session, Tool: "bash", Output: "first", ContentHash: dbpkg.ContentHash("first")}
	if _, err := database.PersistToolCallOnce(tool); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	stream := "" +
		`{"type":"tool_use"}` + "\n" +
		`{"type":"step_finish","part":{"reason":"tool-calls","tokens":{"input":503,"output":10,"reasoning":3,"cache":{"read":5632,"write":0}},"cost":0}}` + "\n" +
		`{"type":"text","part":{"text":"FACT=ok"}}` + "\n" +
		`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":20883,"output":20,"reasoning":4,"cache":{"read":5632,"write":0}},"cost":0}}` + "\n"
	if err := os.WriteFile(filepath.Join(attemptDir, "stream.jsonl"), []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ExperimentID: "authority", Methodology: MethodologyMetadata{Version: "v4"}, Task: Task{ExpectedFacts: []string{"FACT=ok"}, MinimumToolCalls: 1}}
	attempt := Attempt{
		SchemaVersion: 2, ExperimentID: "authority", AttemptID: "baseline-001", Side: Baseline,
		Status: "completed", SessionID: &session, SessionLink: SessionLink{State: "linked", NewSessionCount: 9},
		Metrics:   Metrics{ToolCallCount: 9, TokensIn: float64Pointer(1), CacheRead: float64Pointer(1)},
		Artifacts: map[string]string{"database": "attempts/baseline-001/ktx.db", "stream": "attempts/baseline-001/stream.jsonl"},
	}
	return dir, manifest, attempt
}

func writeAttemptRecords(t *testing.T, dir string, attempts ...Attempt) {
	t.Helper()
	var data []byte
	for _, attempt := range attempts {
		encoded, err := json.Marshal(attempt)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "attempts.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func float64Pointer(value float64) *float64 { return &value }
