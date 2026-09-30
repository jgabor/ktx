package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/db"
)

func TestReadPreservationProducerAndRegeneration(t *testing.T) {
	for _, reads := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d-reads", reads), func(t *testing.T) {
			testReadPreservationProducerAndRegeneration(t, reads)
		})
	}
}

func testReadPreservationProducerAndRegeneration(t *testing.T, reads int) {
	runner, options, _ := readDescriptionTestRunner(t)
	task, err := LoadTask("../../tools/bench/tasks/read-config-first.toml")
	if err != nil {
		t.Fatal(err)
	}
	options.Task = task
	corpus := filepath.Join(options.RepoRoot, "tools/bench/corpus")
	if err := os.MkdirAll(corpus, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corpus, "owner.txt"), []byte("owner=iris\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	underlying := runner.Executor
	const input = `{"filePath":"/work/tools/bench/corpus/owner.txt","offset":1,"limit":1}`
	const toolOutput = "1: owner=iris"
	runner.Executor = responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
		if len(command.Args) < 2 || command.Args[0] != "docker" || command.Args[1] != "run" || contains(command.Args, "--capabilities") {
			return underlying.Run(ctx, command)
		}
		var output bytes.Buffer
		forward := command.Stdout
		command.Stdout = &output
		result, err := underlying.Run(ctx, command)
		if err != nil {
			return result, err
		}
		var changed bytes.Buffer
		for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
			var event map[string]any
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatal(err)
			}
			switch event["type"] {
			case "tool_use":
				for call := 1; call <= reads; call++ {
					event["part"] = map[string]any{"tool": "read", "callID": fmt.Sprintf("call-%d", call), "state": map[string]any{"status": "completed", "input": json.RawMessage(input), "output": toolOutput}}
					if err := json.NewEncoder(&changed).Encode(event); err != nil {
						t.Fatal(err)
					}
				}
				continue
			case "text":
				event["part"].(map[string]any)["text"] = "owner=iris"
			}
			if err := json.NewEncoder(&changed).Encode(event); err != nil {
				t.Fatal(err)
			}
		}
		mount := mountSource(command.Args, benchcontract.ReadDescriptionDir)
		if err := os.WriteFile(filepath.Join(mount, "turn-1.jsonl"), changed.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		owner := dockerEnvironmentValue(command.Args, benchcontract.SessionEnv)
		store, err := db.Open(mountSource(command.Args, "/home/node/.local/share/ktx/ktx.db"))
		if err != nil {
			t.Fatal(err)
		}
		// The real read hook has no pre-delivery outcome authority. Terminal
		// success comes from the retained runtime stream, not an invented flag.
		_, err = store.DB().Exec(`UPDATE tool_calls SET tool='read',input=?,output=?,is_error=NULL WHERE session_id=?`, input, toolOutput, owner)
		if err != nil {
			t.Fatal(err)
		}
		for call := 2; call <= reads; call++ {
			id, err := db.NewID()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PersistToolCall(&db.ToolCall{ID: id, SessionID: owner, Tool: "read", Input: input, Output: toolOutput, ContentHash: db.ContentHash(toolOutput)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = changed.WriteTo(forward)
		return result, err
	})
	dir, _, code, err := runner.Compare(context.Background(), options)
	if err != nil || code != 0 {
		attempts, _ := LoadAttempts(dir)
		t.Fatalf("producer code=%d err=%v attempts=%+v", code, err, attempts)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	attempts, err := LoadAttempts(dir)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%d err=%v", len(attempts), err)
	}
	for _, attempt := range attempts {
		if !attempt.FullPass || len(attempt.Phases) != 1 || !attempt.Phases[0].FullPass || attempt.ToolCallCount != reads {
			t.Fatalf("producer score: %+v", attempt.Score)
		}
	}
	materialized, _, err := materializeAttempts(dir, manifest, attempts)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range materialized {
		if !attempt.FullPass || attempt.ToolCallCount != reads || attempt.calls.Stream.CallCount != reads || attempt.calls.Persistence.CallCount != reads {
			t.Fatalf("regenerated score: %+v", attempt.Score)
		}
	}
	if os.Getenv("KTX_READ_DESCRIPTION_CLI") != "" {
		invokeReadDescriptionCLI(t, options.RepoRoot, true, "bench", "validate", options.ExperimentID)
		invokeReadDescriptionCLI(t, options.RepoRoot, true, "bench", "summarize", options.ExperimentID)
		invokeReadDescriptionCLI(t, options.RepoRoot, true, "bench", "validate", options.ExperimentID)
	}
	archiveRoot := os.Getenv("KTX_READ_PRESERVATION_ARCHIVE_ROOT")
	if archiveRoot != "" && reads != 1 {
		archiveRoot = filepath.Join(archiveRoot, fmt.Sprintf("%d-reads", reads))
	}
	if archiveRoot != "" {
		if err := copyTree(dir, filepath.Join(archiveRoot, "positive", ".ktx", "experiments", options.ExperimentID), nil); err != nil {
			t.Fatal(err)
		}
	}
	// Claiming a pass in the copied ledger cannot replace retained behavior.
	attempts[0].Score = Score{Score: 1, FullPass: true, AnswerMatchesExpected: true}
	stream := filepath.Join(dir, attempts[0].Artifacts["turn_1"])
	data, err := os.ReadFile(stream)
	if err != nil {
		t.Fatal(err)
	}
	broken := bytes.Replace(data, []byte(`"text":"owner=iris"`), []byte(`"text":"wrong"`), 1)
	if bytes.Equal(data, broken) {
		t.Fatal("test failed to alter answer")
	}
	if err := os.WriteFile(stream, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, attempts[0].Artifacts["stream"]), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	attempts[0].ContentHashes["turn_1"] = sha256Bytes(broken)
	attempts[0].ContentHashes["stream"] = sha256Bytes(broken)
	// Even internally consistent retained streams override a claimed ledger pass.
	materialized, _, err = materializeAttempts(dir, manifest, attempts)
	if err != nil {
		t.Fatal(err)
	}
	if materialized[0].FullPass || materialized[0].AnswerMatchesExpected || materialized[0].ToolCallCount != reads || materialized[0].calls.Stream.CallCount != reads || materialized[0].calls.Persistence.CallCount != reads {
		t.Fatal("changed source turn accepted as a claimed pass")
	}
	var lines bytes.Buffer
	for _, attempt := range attempts {
		if err := json.NewEncoder(&lines).Encode(attempt); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "attempts.jsonl"), lines.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KTX_READ_DESCRIPTION_CLI") != "" {
		invokeReadDescriptionCLI(t, options.RepoRoot, false, "bench", "validate", options.ExperimentID)
		// Summarize regenerates a failing score rather than refusing a fully
		// observed wrong answer. All reads and incurred estimates must survive.
		out := invokeReadDescriptionCLI(t, options.RepoRoot, true, "bench", "summarize", options.ExperimentID)
		var summary struct {
			Pipelines map[string]struct {
				FullPassRate *float64 `json:"full_pass_rate"`
				ToolCalls    struct {
					Values []int `json:"values"`
				} `json:"tool_call_count"`
				EstimatedCost struct {
					Values []float64 `json:"values"`
				} `json:"estimated_api_cost"`
			} `json:"pipelines"`
		}
		if err := json.Unmarshal(out, &summary); err != nil {
			t.Fatal(err)
		}
		for i, side := range []string{Baseline, Candidate} {
			p := summary.Pipelines[side]
			if p.FullPassRate == nil || *p.FullPassRate != float64(i) || len(p.ToolCalls.Values) != 1 || p.ToolCalls.Values[0] != reads || len(p.EstimatedCost.Values) != 1 || attempts[i].EstimatedAPICost == nil || p.EstimatedCost.Values[0] != *attempts[i].EstimatedAPICost {
				t.Fatalf("regenerated failure lost score, reads or cost: %s %+v", side, p)
			}
		}
		t.Logf("rebuilt CLI: %d reads retained; positive validate/summarize/validate passed, false-pass validation rejected, negative summary recomputed failure with all reads and estimates", reads)
	}
	if archiveRoot != "" {
		if err := copyTree(dir, filepath.Join(archiveRoot, "negative", ".ktx", "experiments", options.ExperimentID), nil); err != nil {
			t.Fatal(err)
		}
	}
}
