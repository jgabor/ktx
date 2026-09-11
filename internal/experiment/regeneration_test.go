package experiment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRegenerationUsesRetainedInputsAfterSourceDisappears(t *testing.T) {
	dir, manifest, attempt := writeAuthorityFixture(t)
	source := filepath.Join(t.TempDir(), "crossfile-nav.toml")
	content := []byte("id = \"crossfile-nav\"\n")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(dir, "inputs", "task", "crossfile-nav.toml")
	if err := os.MkdirAll(filepath.Dir(retained), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retained, content, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Inputs {
		if manifest.Inputs[i].Kind == "task" {
			manifest.Inputs[i] = InputMetadata{ID: "crossfile-nav", Kind: "task", Format: "toml", Path: "inputs/task/crossfile-nav.toml", SHA256: sha256Bytes(content)}
		}
	}
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	writeAttemptRecords(t, dir, attempt)
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := RegenerateSummary(dir); err != nil {
		t.Fatal(err)
	}
	report, err := ValidateExperiment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report["valid"] != false || report["target_met"] != false || report["summary_byte_match"] != true || report["attempt_checks"].([]map[string]any)[0]["valid"] != true {
		t.Fatalf("validation report = %#v", report)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifestBytes, []byte(source)) {
		t.Fatal("manifest retained a host source path")
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
	if _, err := RegenerateSummary(dir); err != nil {
		t.Fatal(err)
	}
	if report, err := ValidateExperiment(dir); err != nil || report["valid"] != false || report["target_met"] != false {
		t.Fatalf("malformed authority acquired credit: %v %v", report, err)
	}
}

func TestAuthoritativeRegenerationMaterializesUnlinkedNonContributingAttempt(t *testing.T) {
	dir, manifest, attempt := writeAuthorityFixture(t)
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	attempt.Status = "timeout"
	attempt.SessionID = nil
	attempt.SessionLink = SessionLink{State: "unlinked"}
	attempt.Artifacts["database"] = "attempts/baseline-001/unavailable.db"
	attempt.ToolCallCount = 99
	attempt.TokensIn = float64Pointer(99)
	writeAttemptRecords(t, dir, attempt)
	regeneration, err := RegenerateSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := regeneration.Summary["pipelines"].(map[string]any)[Baseline].(map[string]any)
	if pipeline["evaluated_count"] != 0 || pipeline["attempt_count"] != 1 {
		t.Fatalf("pipeline = %#v, want one non-contributing attempt", pipeline)
	}
	if len(regeneration.ProjectionDivergences) == 0 {
		t.Fatal("copied timeout measurements were not reported as divergence")
	}
}

func TestAuthoritativeRegenerationRejectsUnavailableContributingAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string, *Attempt)
		want   string
	}{
		{name: "missing session identity", mutate: func(_ string, attempt *Attempt) { attempt.SessionID = nil }, want: "requires a session identity"},
		{name: "unavailable database", mutate: func(_ string, attempt *Attempt) { attempt.Artifacts["database"] = "missing.db" }, want: "open database authority"},
		{name: "malformed database", mutate: func(dir string, attempt *Attempt) {
			path := filepath.Join(dir, "malformed.db")
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			attempt.Artifacts["database"] = "malformed.db"
		}, want: "open database authority"},
		{name: "unknown session", mutate: func(_ string, attempt *Attempt) {
			missing := "missing-session"
			attempt.SessionID = &missing
		}, want: "session authority is unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, manifest, attempt := writeAuthorityFixture(t)
			if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
				t.Fatal(err)
			}
			test.mutate(dir, &attempt)
			writeAttemptRecords(t, dir, attempt)
			if _, err := RegenerateSummary(dir); err != nil {
				t.Fatal(err)
			}
			if report, err := ValidateExperiment(dir); err != nil || report["valid"] != false || report["target_met"] != false {
				t.Fatalf("unavailable authority acquired credit: %v %v", report, err)
			}
		})
	}
}

func TestLegacyRegenerationRejected(t *testing.T) {
	dir := t.TempDir()
	manifest := Manifest{SchemaVersion: 1, ExperimentID: "legacy", TargetValidPerPipeline: 1, Methodology: MethodologyMetadata{Version: "v4"}, Task: Task{MinimumToolCalls: 1, ExpectedOutput: stringPointer("done")}}
	retainFixtureConfigs(t, dir, &manifest)
	session := "legacy-session"
	attempt := Attempt{SchemaVersion: 1, ExperimentID: "legacy", AttemptID: "baseline-001", Side: Baseline, Status: "completed", SessionID: &session, Metrics: Metrics{StreamComplete: true, ToolCallCount: 1}}
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	writeAttemptRecords(t, dir, attempt)
	if _, err := WriteSummary(dir); err == nil || !strings.Contains(err.Error(), "unsupported methodology version") {
		t.Fatalf("legacy regeneration error = %v", err)
	}
}

func writeAuthorityFixture(t *testing.T) (string, Manifest, Attempt) {
	t.Helper()
	dir := produceEntryArchive(t, "exact-match", true, true)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	attempts, err := LoadAttempts(dir)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%v err=%v", attempts, err)
	}
	return dir, manifest, attempts[0]
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
