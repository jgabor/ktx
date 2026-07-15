package experiment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func floatPointer(value float64) *float64 { return &value }
func stringPointer(value string) *string  { return &value }

func fixtureManifest() Manifest {
	return Manifest{
		SchemaVersion: 1, ExperimentID: "exp-1", TargetValidPerPipeline: 1,
		Task: Task{MinimumToolCalls: 1}, Methodology: MethodologyMetadata{Version: "v4-eval-matrix"},
	}
}

func fixtureAttempts() []Attempt {
	return []Attempt{
		{AttemptID: "baseline-001", Side: Baseline, Status: "completed", SessionID: stringPointer("s1"), Metrics: Metrics{StreamComplete: true, Score: Score{Score: 1, FullPass: true, AnswerMatchesExpected: true}, ToolCallCount: 2, TokensIn: floatPointer(100), TokensOut: floatPointer(0), Cost: floatPointer(0)}},
		{AttemptID: "baseline-002", Side: Baseline, Status: "completed", SessionID: stringPointer("s2"), Metrics: Metrics{StreamComplete: true, Score: Score{Score: .5}, ToolCallCount: 1}},
		{AttemptID: "candidate-001", Side: Candidate, Status: "timeout", Metrics: Metrics{Score: Score{}, ToolCallCount: 0}},
		{AttemptID: "candidate-002", Side: Candidate, Status: "completed", SessionID: stringPointer("s3"), Metrics: Metrics{StreamComplete: true, Score: Score{Score: 1, FullPass: true, AnswerMatchesExpected: true}, ToolCallCount: 2, TokensIn: floatPointer(40), TokensOut: floatPointer(0), Cost: floatPointer(.04)}},
	}
}

// cacheFixtureManifest ports a known pipeline identity onto the manifest
// so the v2 summary's per-side pipeline_id / stage_labels / pipeline_hash
// assertions run against stable values. The Pipelines map keys are the
// side identifiers the runner already uses.
func cacheFixtureManifest() Manifest {
	return Manifest{
		SchemaVersion: 1, ExperimentID: "exp-cache", TargetValidPerPipeline: 1,
		Task: Task{MinimumToolCalls: 1}, Methodology: MethodologyMetadata{Version: "v4-eval-matrix"},
		Pipelines: map[string]PipelineMetadata{
			Baseline:  {PipelineID: "passthrough", SHA256: "sha-baseline", Stages: []string{}},
			Candidate: {PipelineID: "keep-ends-mid", SHA256: "sha-candidate", Stages: []string{"strip-ansi", "dedup-blank", "normalize", "keep-ends:100:50"}},
		},
	}
}

// cacheFixtureAttempts mirrors fixtureAttempts but adds cache telemetry
// (per-request cache_read / cache_write from OpenCode step_finish events
// under SUM aggregation). baseline keeps a large cache_read second pass;
// candidate uses fewer cache-read tokens. The first baseline attempt
// emits explicit zeros to assert the absent-vs-emitted-zero preservation.
func cacheFixtureAttempts() []Attempt {
	return []Attempt{
		{AttemptID: "baseline-001", Side: Baseline, Status: "completed", SessionID: stringPointer("s1"), Metrics: Metrics{StreamComplete: true, Score: Score{Score: 1, FullPass: true, AnswerMatchesExpected: true}, ToolCallCount: 2, TokensIn: floatPointer(100), TokensOut: floatPointer(0), Cost: floatPointer(0), CacheRead: floatPointer(0), CacheWrite: floatPointer(0)}},
		{AttemptID: "baseline-002", Side: Baseline, Status: "completed", SessionID: stringPointer("s2"), Metrics: Metrics{StreamComplete: true, Score: Score{Score: .5}, ToolCallCount: 1, CacheRead: floatPointer(15000), CacheWrite: floatPointer(2000)}},
		{AttemptID: "candidate-001", Side: Candidate, Status: "timeout", Metrics: Metrics{Score: Score{}, ToolCallCount: 0}},
		{AttemptID: "candidate-002", Side: Candidate, Status: "completed", SessionID: stringPointer("s3"), Metrics: Metrics{StreamComplete: true, Score: Score{Score: 1, FullPass: true, AnswerMatchesExpected: true}, ToolCallCount: 2, TokensIn: floatPointer(40), TokensOut: floatPointer(0), Cost: floatPointer(.04), CacheRead: floatPointer(3000), CacheWrite: floatPointer(1200)}},
	}
}

func TestSummaryGolden(t *testing.T) {
	actual, err := MarshalJSON(BuildSummary(fixtureManifest(), fixtureAttempts()))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "summary.golden.json", actual)
}

// TestSummaryCacheGolden locks the schema-version-2 byte-match contract
// when any attempt under either side carries cache evidence. The golden
// carries per-side pipeline_id / stage_labels / pipeline_hash from the
// manifest, cache_read / cache_write distributions, and the
// baseline_minus_candidate cache deltas — the additive surface the
// v1 summary omits. Locking the byte shape catches regressions in the
// cache aggregation path the same way the fixed-shape golden locks
// the v1 path.
func TestSummaryCacheGolden(t *testing.T) {
	actual, err := MarshalJSON(BuildSummary(cacheFixtureManifest(), cacheFixtureAttempts()))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "summary_cache.golden.json", actual)
}

func TestRankingGolden(t *testing.T) {
	summary := BuildSummary(fixtureManifest(), fixtureAttempts())
	ranking := BuildRanking(RankingOptions{
		TaskFile: "/repo/tools/bench/tasks/example.toml", Model: "neuralwatt/example", Rounds: 3,
		BaselinePipeline: "passthrough", Candidates: []string{"candidate"},
	}, []PairResult{{CandidatePipeline: "candidate", ExperimentID: "pair-1", ExitCode: 0, Summary: summary}}, time.Date(2026, 7, 10, 12, 34, 56, 987, time.UTC))
	actual, err := MarshalJSON(ranking)
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "ranking.golden.json", actual)
}

func TestRankingVariantPropagation(t *testing.T) {
	summary := BuildSummary(fixtureManifest(), fixtureAttempts())
	now := time.Date(2026, 7, 10, 12, 34, 56, 987, time.UTC)

	// Variant present → ranking output includes it
	rankingWith := BuildRanking(RankingOptions{
		TaskFile: "/repo/tools/bench/tasks/example.toml", Model: "openai/gpt-5.6-luna", Variant: "high",
		Rounds: 3, BaselinePipeline: "passthrough", Candidates: []string{"candidate"},
	}, []PairResult{{CandidatePipeline: "candidate", ExperimentID: "pair-1", ExitCode: 0, Summary: summary}}, now)
	if rankingWith["variant"] != "high" {
		t.Fatalf("ranking variant = %v, want \"high\"", rankingWith["variant"])
	}

	// Variant absent → ranking output omits the key entirely (legacy shape)
	rankingWithout := BuildRanking(RankingOptions{
		TaskFile: "/repo/tools/bench/tasks/example.toml", Model: "openai/gpt-5.6-luna", Rounds: 3,
		BaselinePipeline: "passthrough", Candidates: []string{"candidate"},
	}, []PairResult{{CandidatePipeline: "candidate", ExperimentID: "pair-1", ExitCode: 0, Summary: summary}}, now)
	if _, ok := rankingWithout["variant"]; ok {
		t.Fatal("ranking should not contain variant key when unset")
	}
}

func TestManifestVariantOmission(t *testing.T) {
	// When Variant is nil, the JSON omits the field entirely
	manifest := fixtureManifest()
	data, err := MarshalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["variant"]; ok {
		t.Fatalf("manifest should not contain variant key when nil: %s", data)
	}

	// When Variant is set, the JSON includes it
	v := "high"
	manifest.Variant = &v
	data, err = MarshalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Manifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Variant == nil || *decoded.Variant != "high" {
		t.Fatalf("manifest variant = %v, want \"high\"", decoded.Variant)
	}
}

func TestGenerateCorpusMatchesCommittedFixtures(t *testing.T) {
	directory := t.TempDir()
	if _, err := GenerateCorpus(directory); err != nil {
		t.Fatal(err)
	}
	repositoryCorpus := filepath.Join("..", "..", "tools", "bench", "corpus")
	for _, name := range append(append([]string{}, corpusOrder...), "MANIFEST.json") {
		actual, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(filepath.Join(repositoryCorpus, name))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Errorf("generated %s differs from committed fixture", name)
		}
	}
}

func TestScoreAndMetricsPreserveNullAndZero(t *testing.T) {
	expected := "done"
	if score := ScoreAnswer(" done\n", &expected, nil); !score.FullPass || score.Score != 1 {
		t.Fatalf("exact score = %#v", score)
	}
	stream := filepath.Join(t.TempDir(), "stream.jsonl")
	data := []byte("{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\",\"tokens\":{\"input\":0,\"output\":0},\"cost\":0}}\n{\"type\":\"text\",\"part\":{\"text\":\"done\"}}\n")
	if err := os.WriteFile(stream, data, 0o644); err != nil {
		t.Fatal(err)
	}
	metrics := ParseStream(stream, &expected, nil)
	if metrics.TokensIn == nil || *metrics.TokensIn != 0 || metrics.Cost == nil || *metrics.Cost != 0 {
		t.Fatalf("zero evidence was not preserved: %#v", metrics)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	metrics = ParseStream(missing, &expected, nil)
	if metrics.TokensIn != nil || metrics.Cost != nil {
		t.Fatalf("missing evidence was not null: %#v", metrics)
	}
}

// TestParseStreamCacheTelemetry locks the per-request SUM aggregation
// of cache_read / cache_write, the absent-vs-emitted-zero distinction,
// and the completeness-counter gate (a single missing cache field on
// one step_finish leaves the per-attempt cache pointer nil exactly the
// way the existing TokensIn path behaves when a step_finish omits
// tokens.input).
func TestParseStreamCacheTelemetry(t *testing.T) {
	t.Run("per-request SUM + emitted zero preserved", func(t *testing.T) {
		expected := "done"
		stream := filepath.Join(t.TempDir(), "stream.jsonl")
		// Two step_finish events with per-request cache counts.
		// SUM = 100+15232 = 15332, 500+1500 = 2000. An explicit
		// zero is preserved (must round-trip as a non-nil pointer
		// to 0 when every step_finish carries the field).
		data := []byte("{\"type\":\"step_finish\",\"part\":{\"reason\":\"tool-calls\",\"tokens\":{\"input\":100,\"output\":10,\"cache\":{\"write\":500,\"read\":100}}}}\n" +
			"{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\",\"tokens\":{\"input\":50,\"output\":5,\"cache\":{\"write\":1500,\"read\":15232}}}}\n" +
			"{\"type\":\"text\",\"part\":{\"text\":\"done\"}}\n")
		if err := os.WriteFile(stream, data, 0o644); err != nil {
			t.Fatal(err)
		}
		metrics := ParseStream(stream, &expected, nil)
		if metrics.CacheRead == nil {
			t.Fatalf("CacheRead = nil; want pointer to 15332 (SUM of per-request counts)")
		}
		if *metrics.CacheRead != 15332 {
			t.Errorf("CacheRead = %v, want 15332 (SUM of per-request counts)", *metrics.CacheRead)
		}
		if metrics.CacheWrite == nil {
			t.Fatalf("CacheWrite = nil; want pointer to 2000")
		}
		if *metrics.CacheWrite != 2000 {
			t.Errorf("CacheWrite = %v, want 2000 (SUM of per-request counts)", *metrics.CacheWrite)
		}
		if metrics.UsageEvidence.CacheReadEvents != 2 || metrics.UsageEvidence.CacheWriteEvents != 2 {
			t.Errorf("usage evidence counts = (%d, %d), want (2, 2)",
				metrics.UsageEvidence.CacheReadEvents, metrics.UsageEvidence.CacheWriteEvents)
		}
	})

	// Multi-step SUM proof: if MAX were used instead, cache_read
	// would be 200 (the larger value) not 300 (the sum), and
	// cache_write would be 100 not 150. This subtest locks SUM.
	t.Run("MAX would give wrong answer for non-zero first event", func(t *testing.T) {
		expected := "done"
		stream := filepath.Join(t.TempDir(), "stream.jsonl")
		// cache.read=100 on first event, 200 on second.
		// SUM=300, MAX=200 (wrong).
		data := []byte("{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\",\"tokens\":{\"input\":10,\"output\":1,\"cache\":{\"read\":100,\"write\":50}}}}\n" +
			"{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\",\"tokens\":{\"input\":20,\"output\":2,\"cache\":{\"read\":200,\"write\":100}}}}\n" +
			"{\"type\":\"text\",\"part\":{\"text\":\"done\"}}\n")
		if err := os.WriteFile(stream, data, 0o644); err != nil {
			t.Fatal(err)
		}
		metrics := ParseStream(stream, &expected, nil)
		if metrics.CacheRead == nil {
			t.Fatal("CacheRead = nil; want pointer to 300")
		}
		if *metrics.CacheRead != 300 {
			t.Errorf("CacheRead = %v, want 300 (SUM: 100+200), not 200 (MAX)", *metrics.CacheRead)
		}
		if *metrics.CacheWrite != 150 {
			t.Errorf("CacheWrite = %v, want 150 (SUM: 50+100), not 100 (MAX)", *metrics.CacheWrite)
		}
	})

	t.Run("cache absent leaves pointers nil", func(t *testing.T) {
		expected := "done"
		stream := filepath.Join(t.TempDir(), "stream.jsonl")
		// OpenCode-shape step_finish without a cache block — the
		// runtime declined to report cache. Pointers stay nil;
		// the events counter stays at zero.
		data := []byte("{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\",\"tokens\":{\"input\":50,\"output\":5}}}\n" +
			"{\"type\":\"text\",\"part\":{\"text\":\"done\"}}\n")
		if err := os.WriteFile(stream, data, 0o644); err != nil {
			t.Fatal(err)
		}
		metrics := ParseStream(stream, &expected, nil)
		if metrics.CacheRead != nil {
			t.Errorf("CacheRead = %v, want nil (no cache field on step_finish)", *metrics.CacheRead)
		}
		if metrics.CacheWrite != nil {
			t.Errorf("CacheWrite = %v, want nil (no cache field on step_finish)", *metrics.CacheWrite)
		}
		if metrics.UsageEvidence.CacheReadEvents != 0 || metrics.UsageEvidence.CacheWriteEvents != 0 {
			t.Errorf("cache evidence counters = (%d, %d), want (0, 0)",
				metrics.UsageEvidence.CacheReadEvents, metrics.UsageEvidence.CacheWriteEvents)
		}
	})

	t.Run("partial cache evidence leaves pointer nil (completeness gate)", func(t *testing.T) {
		expected := "done"
		stream := filepath.Join(t.TempDir(), "stream.jsonl")
		// The first step_finish carries cache.read; the second does
		// not. The completeness gate (event count must equal step
		// count) mirrors the existing TokensIn behaviour — a single
		// missing field drops the per-attempt cache pointer to nil
		// so we never publish a misleading SUM from a partial
		// stream.
		data := []byte("{\"type\":\"step_finish\",\"part\":{\"reason\":\"tool-calls\",\"tokens\":{\"input\":100,\"output\":10,\"cache\":{\"write\":0,\"read\":100}}}}\n" +
			"{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\",\"tokens\":{\"input\":50,\"output\":5}}}\n" +
			"{\"type\":\"text\",\"part\":{\"text\":\"done\"}}\n")
		if err := os.WriteFile(stream, data, 0o644); err != nil {
			t.Fatal(err)
		}
		metrics := ParseStream(stream, &expected, nil)
		if metrics.CacheRead != nil {
			t.Errorf("CacheRead = %v, want nil (incomplete evidence — one step_finish lacks cache.read)", *metrics.CacheRead)
		}
		if metrics.UsageEvidence.CacheReadEvents != 1 {
			t.Errorf("CacheReadEvents = %d, want 1 (one step_finish carried cache.read)", metrics.UsageEvidence.CacheReadEvents)
		}
	})
}

func TestLoadTaskDefaultsMinimumToolCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.toml")
	data := []byte("id = \"task\"\nprompt = \"work\"\n[scoring]\nkind = \"exact_match\"\nexpected_output = \"done\"\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := LoadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	if task.MinimumToolCalls != 1 || task.WorkspaceMode != "repo" {
		t.Fatalf("task defaults = %#v", task)
	}
}

func assertGolden(t *testing.T, name string, actual []byte) {
	t.Helper()
	expected, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatalf("%s mismatch\n--- actual ---\n%s", name, actual)
	}
}

// TestReconcileCacheFromStream proves that summary regeneration and
// validation derive cache_read/cache_write SUM from the authoritative
// raw stream artifact, not from potentially stale attempts.jsonl fields
// — but ONLY for attempts that already carry cache evidence in
// attempts.jsonl. Legacy cacheless attempts are left untouched even
// if their retained raw stream happens to contain cache fields.
func TestReconcileCacheFromStream(t *testing.T) {
	writeExperiment := func(t *testing.T, manifest Manifest, attempt Attempt, streamData []byte) string {
		t.Helper()
		dir := t.TempDir()
		streamRel := attempt.Artifacts["stream"]
		if streamRel != "" {
			streamPath := filepath.Join(dir, filepath.FromSlash(streamRel))
			if err := os.MkdirAll(filepath.Dir(streamPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(streamPath, streamData, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		attemptJSON, err := json.Marshal(attempt)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "attempts.jsonl"), append(attemptJSON, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		manifestBytes, err := MarshalJSON(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	baseManifest := func() Manifest {
		m := fixtureManifest()
		m.Pipelines = map[string]PipelineMetadata{
			Baseline:  {PipelineID: "passthrough", Stages: []string{}, SHA256: "abc"},
			Candidate: {PipelineID: "candidate", Stages: []string{"head:1"}, SHA256: "def"},
		}
		return m
	}

	cacheStream := []byte(
		`{"type":"tool_use","part":{}}` + "\n" +
			`{"type":"step_finish","part":{"reason":"tool-calls","tokens":{"input":10,"output":1,"cache":{"read":100,"write":50}}}}` + "\n" +
			`{"type":"step_finish","part":{"reason":"stop","tokens":{"input":20,"output":2,"cache":{"read":200,"write":100}}}}` + "\n" +
			`{"type":"text","part":{"text":"done"}}` + "\n",
	)

	t.Run("stale cache-bearing 200→300", func(t *testing.T) {
		manifest := baseManifest()
		manifest.ExperimentID = "exp-stale"
		staleCacheRead := 200.0
		staleCacheWrite := 100.0
		attempt := Attempt{
			SchemaVersion: 1, ExperimentID: "exp-stale", AttemptID: "baseline-1",
			Side: Baseline, PipelineID: "passthrough", Cycle: 1, Ordinal: 1,
			Status: "completed", ProcessExitCode: 0,
			SessionLink: SessionLink{State: "linked", NewSessionCount: 1, NewSessionIDs: []string{"s1"}},
			SessionID:   stringPointer("s1"),
			Metrics: Metrics{
				StreamComplete: true, Score: Score{Score: 1, FullPass: true, AnswerMatchesExpected: true},
				ToolCallCount: 1, TokensIn: floatPointer(30), TokensOut: floatPointer(3),
				Cost: floatPointer(0.02), CacheRead: &staleCacheRead, CacheWrite: &staleCacheWrite,
			},
			ConfigSHA256:  "abc",
			Artifacts:     map[string]string{"stream": "attempts/baseline-1/stream.jsonl"},
			ContentHashes: map[string]string{"stream": "unused"},
		}
		dir := writeExperiment(t, manifest, attempt, cacheStream)

		summary, err := WriteSummary(dir)
		if err != nil {
			t.Fatalf("WriteSummary: %v", err)
		}
		pipelines, _ := summary["pipelines"].(map[string]any)
		baseline, _ := pipelines[Baseline].(map[string]any)
		cacheReadDist, _ := baseline["cache_read"].(map[string]any)
		cacheReadMean, _ := cacheReadDist["mean"].(float64)
		if cacheReadMean != 300 {
			t.Fatalf("cache_read mean = %v, want 300 (stream SUM: 100+200), not 200 (stale MAX from attempts.jsonl)", cacheReadMean)
		}

		report, err := ValidateExperiment(dir)
		if err != nil {
			t.Fatalf("ValidateExperiment: %v", err)
		}
		if byteMatch, _ := report["summary_byte_match"].(bool); !byteMatch {
			t.Fatal("summary_byte_match = false, want true (validator must agree with stream-derived summary)")
		}
	})

	t.Run("legacy cacheless with cache-bearing stream stays v1", func(t *testing.T) {
		manifest := baseManifest()
		manifest.ExperimentID = "exp-cacheless"
		attempt := Attempt{
			SchemaVersion: 1, ExperimentID: "exp-cacheless", AttemptID: "baseline-1",
			Side: Baseline, PipelineID: "passthrough", Cycle: 1, Ordinal: 1,
			Status: "completed", ProcessExitCode: 0,
			SessionLink: SessionLink{State: "linked", NewSessionCount: 1, NewSessionIDs: []string{"s1"}},
			SessionID:   stringPointer("s1"),
			Metrics: Metrics{
				StreamComplete: true, Score: Score{Score: 1, FullPass: true, AnswerMatchesExpected: true},
				ToolCallCount: 1, TokensIn: floatPointer(30), TokensOut: floatPointer(3),
				Cost: floatPointer(0.02),
				// CacheRead/CacheWrite intentionally nil — pre-cache era.
			},
			ConfigSHA256:  "abc",
			Artifacts:     map[string]string{"stream": "attempts/baseline-1/stream.jsonl"},
			ContentHashes: map[string]string{"stream": "unused"},
		}
		dir := writeExperiment(t, manifest, attempt, cacheStream)

		// Generate the "original" summary by writing it once.
		originalSummary, err := WriteSummary(dir)
		if err != nil {
			t.Fatalf("WriteSummary (original): %v", err)
		}
		if schemaVersion, _ := originalSummary["schema_version"].(int); schemaVersion != 1 {
			t.Fatalf("schema_version = %d, want 1 (cacheless attempt must stay v1 even with cache-bearing stream)", schemaVersion)
		}
		pipelines, _ := originalSummary["pipelines"].(map[string]any)
		baseline, _ := pipelines[Baseline].(map[string]any)
		if _, hasCache := baseline["cache_read"]; hasCache {
			t.Fatal("cache_read should be absent from summary for cacheless attempt")
		}

		// Regenerate — must be byte-identical (reconciliation skipped).
		regeneratedSummary, err := WriteSummary(dir)
		if err != nil {
			t.Fatalf("WriteSummary (regenerated): %v", err)
		}
		regeneratedBytes, err := MarshalJSON(regeneratedSummary)
		if err != nil {
			t.Fatal(err)
		}
		originalBytes, err := MarshalJSON(originalSummary)
		if err != nil {
			t.Fatal(err)
		}
		if string(regeneratedBytes) != string(originalBytes) {
			t.Fatalf("regenerated summary differs from original (should be byte-identical):\noriginal: %s\nregenerated: %s", originalBytes, regeneratedBytes)
		}

		// Validator must agree.
		report, err := ValidateExperiment(dir)
		if err != nil {
			t.Fatalf("ValidateExperiment: %v", err)
		}
		if byteMatch, _ := report["summary_byte_match"].(bool); !byteMatch {
			t.Fatal("summary_byte_match = false, want true (cacheless attempt must validate against its original summary)")
		}
	})
}
