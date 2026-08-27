package experiment

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/db"
)

func TestRecoveryAwareComparisonPrimaryAndDiagnostics(t *testing.T) {
	manifest := recoveryManifest(1)
	baseline := recoveryAttempt("base", Baseline, 1, true, number(100), number(20), number(5), 2, 1, 6, 2)
	candidate := recoveryAttempt("candidate", Candidate, 1, true, number(70), number(20), number(5), 1, 0, 5, 1)
	summary := BuildSummary(manifest, []Attempt{baseline, candidate})
	if summary.SchemaVersion() != 6 {
		t.Fatalf("schema = %d, want 6", summary.SchemaVersion())
	}
	comparison := summary["recovery_aware_comparison"].(map[string]any)
	arms := comparison["arms"].(map[string]any)
	primary := arms[Candidate].(map[string]any)["runtime_total_tokens_per_correct_task"].(map[string]any)
	if primary["state"] != "available" || primary["value"] != 95.0 {
		t.Fatalf("primary = %#v", primary)
	}
	recovery := arms[Baseline].(map[string]any)["recovery"].(map[string]any)
	calls := arms[Baseline].(map[string]any)["tool_calls"].(map[string]any)
	if recovery["attempt_count"] != 2 || recovery["repeated_attempt_count"] != 1 || calls["total_call_count"] != 6 || calls["exact_repeated_call_count"] != 2 {
		t.Fatalf("diagnostics = %#v %#v", recovery, calls)
	}
	if !strings.Contains(RenderSummary(summary), "runtime_total_tokens_per_correct_task=95") {
		t.Fatal("text omitted primary outcome")
	}
	encoded, err := MarshalJSON(summary)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSummary(encoded)
	if err != nil {
		t.Fatal(err)
	}
	regenerated, err := MarshalJSON(decoded)
	if err != nil || string(regenerated) != string(encoded) {
		t.Fatalf("schema 6 did not regenerate byte-exact: %v", err)
	}
}

func TestRecoveryAwareComparisonUnavailableAndRankingExclusion(t *testing.T) {
	manifest := recoveryManifest(1)
	complete := BuildSummary(manifest, []Attempt{
		recoveryAttempt("base", Baseline, 1, true, number(100), number(10), number(5), 0, 0, 1, 0),
		recoveryAttempt("candidate", Candidate, 1, true, number(50), number(10), number(5), 0, 0, 1, 0),
	})
	incomplete := BuildSummary(manifest, []Attempt{
		recoveryAttempt("base2", Baseline, 1, true, number(100), number(10), number(5), 0, 0, 1, 0),
		recoveryAttempt("candidate2", Candidate, 1, true, nil, number(10), number(5), 0, 0, 1, 0),
	})
	failedQuality := BuildSummary(manifest, []Attempt{
		recoveryAttempt("base3", Baseline, 1, true, number(100), number(10), number(5), 0, 0, 1, 0),
		recoveryAttempt("candidate3", Candidate, 1, false, number(1), number(1), number(1), 0, 0, 1, 0),
	})
	ranking := BuildRanking(RankingOptions{BaselinePipeline: "base", Candidates: []string{"complete", "incomplete", "wrong"}}, []PairResult{
		{CandidatePipeline: "complete", Summary: complete}, {CandidatePipeline: "incomplete", Summary: incomplete}, {CandidatePipeline: "wrong", Summary: failedQuality},
	}, time.Unix(0, 0))
	rows := ranking["ranking"].([]map[string]any)
	if rows[0]["pipeline"] != "complete" {
		t.Fatalf("first row = %#v", rows[0])
	}
	for _, row := range rows {
		if row["pipeline"] == "incomplete" {
			primary := row["runtime_total_tokens_per_correct_task"].(map[string]any)
			if primary["state"] != "unavailable" || primary["reason"] != "incomplete_runtime_token_totals" {
				t.Fatalf("incomplete primary = %#v", primary)
			}
			if row["recommendation_eligible"] != false {
				t.Fatalf("incomplete row remained eligible: %#v", row)
			}
			if row["rank"] != nil {
				t.Fatalf("incomplete row remained ranked: %#v", row)
			}
		}
		if row["pipeline"] == "wrong" {
			primary := row["runtime_total_tokens_per_correct_task"].(map[string]any)
			if primary["reason"] != "quality_gate_failed" {
				t.Fatalf("quality primary = %#v", primary)
			}
			if row["rank"] != nil {
				t.Fatalf("quality-failed row remained ranked: %#v", row)
			}
		}
	}
	text := RenderRanking(ranking)
	for _, want := range []string{"primary pipeline=incomplete rank=-", "state=unavailable", "reason=incomplete_runtime_token_totals"} {
		if !strings.Contains(text, want) {
			t.Fatalf("ranking text omitted %q:\n%s", want, text)
		}
	}
}

func TestRecoveryAwareArchiveValidatesAndRegeneratesByteExact(t *testing.T) {
	dir := writeRecoveryArchive(t)
	report, err := ValidateExperiment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report["valid"] != true || report["summary_byte_match"] != true {
		t.Fatalf("validation report = %#v", report)
	}
	before, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RegenerateSummary(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("schema 6 summary did not regenerate byte-exact")
	}
}

func TestIncompleteRecoveryAwareArchiveIsStructuralButTargetUnmet(t *testing.T) {
	dir := writeRecoveryArchive(t)
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.TargetValidPerPipeline = 2
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}

	summaryData, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := DecodeSummary(summaryData)
	if err != nil {
		t.Fatalf("structural validation failed: %v", err)
	}
	comparison := summary["recovery_aware_comparison"].(map[string]any)
	if comparison["state"] != "unavailable" || comparison["reason"] != "target_valid_per_pipeline_unmet" {
		t.Fatalf("comparison = %#v", comparison)
	}
	if _, exists := summary["rank"]; exists {
		t.Fatal("incomplete summary contains rank")
	}
	if _, exists := summary["recommendation"]; exists {
		t.Fatal("incomplete summary contains recommendation")
	}
	report, err := ValidateExperiment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report["summary_byte_match"] != true || report["target_met"] != false || report["target_reason"] != "target_valid_per_pipeline_unmet" || report["valid"] != false {
		t.Fatalf("validation report = %#v", report)
	}
}

func TestIncompleteHistoricalRecoverySummaryIsNotRewritten(t *testing.T) {
	dir := writeRecoveryArchive(t)
	path := filepath.Join(dir, "summary.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	delete(summary, "recovery_aware_comparison")
	before, err := MarshalJSON(summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := ValidateExperiment(dir)
	if err != nil {
		t.Fatal(err)
	}
	projection, ok := report["derived_projection"].(map[string]any)
	if !ok || projection["kind"] != "recovery_aware_comparison" || projection["source"] != "retained_authoritative_artifacts" || projection["experiment_id"] != "recovery-archive" {
		t.Fatalf("derived projection = %#v", report["derived_projection"])
	}
	if _, err := RegenerateSummary(dir); err == nil || !strings.Contains(err.Error(), "missing recovery_aware_comparison") {
		t.Fatalf("regeneration error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("historical incomplete summary was rewritten")
	}
}

func writeRecoveryArchive(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	manifest := recoveryManifest(1)
	manifest.ExperimentID = "recovery-archive"
	manifest.Model = "openai/test"
	manifest.Task.ExpectedFacts = []string{"FACT=ok"}
	manifest.Pipelines = map[string]PipelineMetadata{Baseline: {PipelineID: "baseline", SHA256: "b"}, Candidate: {PipelineID: "candidate", SHA256: "c"}}
	manifest.CacheControl = &CacheControlManifest{Mode: "observed", Source: "unavailable", WarmupCount: 1, Model: manifest.Model, Provider: "openai", RuntimeVersion: "1.17.20"}
	manifest.Identities = map[string]string{"opencode": "1.17.20"}
	rate := .5
	pricing, err := writePricingSnapshot(dir, "openai", "test", db.Price{Input: 1, Output: 2, CacheRead: &rate, CacheWrite: &rate, InputSet: true, OutputSet: true})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Pricing = pricing
	attempts := []Attempt{
		phaseAttempt("b-cold", Baseline, ColdObservation, 0, 1), phaseAttempt("c-cold", Candidate, ColdObservation, 0, 1),
		phaseAttempt("b-warm", Baseline, Warmup, 0, 1), phaseAttempt("c-warm", Candidate, Warmup, 0, 1),
		phaseAttempt("b-measured", Baseline, MeasuredWarm, 1, 1), phaseAttempt("c-measured", Candidate, MeasuredWarm, 1, 1),
	}
	for index := range attempts {
		prepareRecoveryArchiveAttempt(t, dir, manifest, &attempts[index], index+1)
	}
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if err := AppendAttempt(dir, attempt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteSummary(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func prepareRecoveryArchiveAttempt(t *testing.T, root string, manifest Manifest, attempt *Attempt, order int) {
	t.Helper()
	attempt.SchemaVersion = 2
	attempt.ExperimentID = manifest.ExperimentID
	attempt.PipelineID = manifest.Pipelines[attempt.Side].PipelineID
	attempt.ConfigSHA256 = manifest.Pipelines[attempt.Side].SHA256
	attempt.CacheControl.ExecutionOrder = order
	attempt.CacheControl.Model = manifest.CacheControl.Model
	attempt.CacheControl.Provider = manifest.CacheControl.Provider
	attempt.CacheControl.RuntimeVersion = manifest.CacheControl.RuntimeVersion
	session := "session-owner-" + attempt.AttemptID
	attempt.SessionID = &session
	attemptDir := filepath.Join(root, "attempts", attempt.AttemptID)
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(attemptDir, "ktx.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InsertSession(&db.Session{ID: session, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	call := &db.ToolCall{ID: "tool-call-" + attempt.AttemptID, SessionID: session, Tool: "read", Input: `{}`, Output: "ok", ContentHash: db.ContentHash("ok")}
	if _, err := database.PersistToolCallOnce(call); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	stream := "{\"type\":\"tool_use\"}\n" +
		"{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\",\"tokens\":{\"input\":10,\"output\":2,\"reasoning\":1,\"cache\":{\"read\":0,\"write\":0}},\"cost\":0}}\n" +
		"{\"type\":\"text\",\"part\":{\"text\":\"FACT=ok\"}}\n"
	if err := os.WriteFile(filepath.Join(attemptDir, "stream.jsonl"), []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt.Artifacts = map[string]string{
		"database": filepath.ToSlash(filepath.Join("attempts", attempt.AttemptID, "ktx.db")),
		"stream":   filepath.ToSlash(filepath.Join("attempts", attempt.AttemptID, "stream.jsonl")),
	}
}

func recoveryManifest(target int) Manifest {
	return Manifest{ExperimentID: "test", TargetValidPerPipeline: target, Task: Task{MinimumToolCalls: 1}, Methodology: MethodologyMetadata{Version: recoveryAwareMethodologyVersion}}
}

func recoveryAttempt(id, side string, cycle int, pass bool, in, out, reasoning *float64, recoveries, repeatedRecovery, calls, repeats int) Attempt {
	session := "session-" + id
	return Attempt{
		SchemaVersion: 3, AttemptID: id, Side: side, Role: MeasuredWarm, Cycle: cycle, Status: "completed", SessionID: &session,
		Metrics:  Metrics{StreamComplete: true, Score: Score{Score: map[bool]float64{true: 1}[pass], FullPass: pass}, ToolCallCount: calls, TokensIn: in, TokensOut: out, Reasoning: reasoning},
		outcomes: &attemptOutcomes{Recovery: map[string]any{"state": "measured", "cue_count": 1, "followed_cue_count": map[bool]int{true: 1}[recoveries > 0], "attempt_count": recoveries, "repeated_attempt_count": repeatedRecovery}, Repeats: map[string]any{"call_count": calls, "repeated_call_count": repeats}},
	}
}

func number(value float64) *float64 { return &value }
