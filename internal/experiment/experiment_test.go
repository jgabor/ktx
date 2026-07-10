package experiment

import (
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

func TestSummaryGolden(t *testing.T) {
	actual, err := MarshalJSON(BuildSummary(fixtureManifest(), fixtureAttempts()))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "summary.golden.json", actual)
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
