package experiment

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/db"
)

func preservationEvent(t *testing.T, kind, tool, call, input, output string) string {
	t.Helper()
	part := map[string]any{}
	switch kind {
	case "tool_use":
		var args any
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			t.Fatal(err)
		}
		part = map[string]any{"tool": tool, "callID": call, "state": map[string]any{"status": "completed", "input": args, "output": output}}
	case "text":
		part["text"] = output
	case "step_finish":
		part["reason"] = "stop"
	}
	b, err := json.Marshal(map[string]any{"type": kind, "sessionID": "ses_test", "part": part})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func TestReadPreservationSixContractsAndBrokenControls(t *testing.T) {
	for id, phases := range readCases {
		t.Run(id, func(t *testing.T) {
			task, err := LoadTask(filepath.Join("../../tools/bench/tasks", id+".toml"))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range task.CorpusFiles {
				workspace := t.TempDir()
				if _, err := prepareWorkspace("../..", workspace, "corpus", []string{name}); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(workspace, "tools/bench/corpus", name))
				if err != nil || sha256Bytes(data) != readFixtureHashes[name] {
					t.Fatalf("fixture drift %s: %v", name, err)
				}
			}
			makeScore := func(mutator func(i int, p readPhase, lines *[]string, input *string, tool *string, answer *string, output *string)) Score {
				t.Helper()
				database, err := db.Open(filepath.Join(t.TempDir(), "owned.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer database.Close()
				if _, err := database.DB().Exec(`INSERT INTO sessions(id) VALUES ('ses_test')`); err != nil {
					t.Fatal(err)
				}
				turns := make([]string, len(phases))
				for i, p := range phases {
					input := fmt.Sprintf(`{"filePath":%q}`, "/work/tools/bench/corpus/"+p.File)
					if p.Offset != 0 {
						input = fmt.Sprintf(`{"filePath":%q,"offset":4,"limit":2}`, "/work/tools/bench/corpus/"+p.File)
					}
					tool, answer, output := "read", p.Answer, "1: "+p.Answer
					lines := []string{}
					if mutator != nil {
						mutator(i, p, &lines, &input, &tool, &answer, &output)
					}
					if _, err := database.DB().Exec(`INSERT INTO tool_calls(id,session_id,tool,input,output,is_error,timestamp_unix_nano) VALUES (?,?,?,?,?,0,?)`, fmt.Sprintf("call-%d", i), "ses_test", tool, input, output, i+1); err != nil {
						t.Fatal(err)
					}
					lines = append(lines, preservationEvent(t, "tool_use", tool, fmt.Sprintf("call-%d", i), input, output), preservationEvent(t, "text", "", "", "", answer), preservationEvent(t, "step_finish", "", "", "", ""))
					turns[i] = strings.Join(lines, "")
				}
				return scoreReadPreservation(task, "ses_test", database.DB(), func(turn int) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader(turns[turn-1])), nil
				})
			}
			if score := makeScore(nil); !score.FullPass || score.Score != 1 || len(score.Phases) != len(phases) {
				t.Fatalf("pass score: %+v", score)
			}
			for name, mutate := range map[string]func(int, readPhase, *[]string, *string, *string, *string, *string){
				"wrong-fact": func(_ int, _ readPhase, _ *[]string, _ *string, _ *string, answer, _ *string) { *answer = "wrong" },
				"missing-read": func(_ int, _ readPhase, lines *[]string, _ *string, _ *string, _ *string, _ *string) {
					*lines = append(*lines, preservationEvent(t, "tool_use", "bash", "forbidden", `{}`, ""))
				},
				"wrong-target": func(_ int, _ readPhase, _ *[]string, input *string, _ *string, _ *string, _ *string) {
					*input = `{"filePath":"/work/tools/bench/corpus/other.txt"}`
				},
				"forbidden-tool": func(_ int, _ readPhase, _ *[]string, _ *string, tool *string, _ *string, _ *string) { *tool = "bash" },
				"edit-attempt": func(_ int, _ readPhase, lines *[]string, _ *string, _ *string, _ *string, _ *string) {
					*lines = append(*lines, preservationEvent(t, "tool_use", "apply_patch", "edit-call", `{}`, "edited"))
				},
				"no-tool-output": func(_ int, _ readPhase, _ *[]string, _ *string, _ *string, _ *string, output *string) {
					*output = "missing"
				},
			} {
				t.Run(name, func(t *testing.T) {
					if score := makeScore(mutate); score.FullPass {
						t.Fatalf("broken control passed: %+v", score)
					}
				})
			}
			if phases[0].Offset != 0 {
				if score := makeScore(func(_ int, _ readPhase, _ *[]string, input *string, _ *string, _ *string, _ *string) {
					*input = strings.Replace(*input, `"offset":4`, `"offset":3`, 1)
				}); score.FullPass {
					t.Fatal("mis-offset read passed")
				}
			} else if strings.HasPrefix(phases[0].File, "window-") {
				if score := makeScore(func(_ int, _ readPhase, _ *[]string, input *string, _ *string, _ *string, _ *string) {
					*input = strings.Replace(*input, `}`, `,"limit":2001}`, 1)
				}); score.FullPass {
					t.Fatal("explicit default or extra page passed")
				}
			}
		})
	}
}

func TestReadPreservationConfigExplicitArguments(t *testing.T) {
	phase := readCases["read-config-first"][0]
	for _, tc := range []struct {
		name, fields string
		pass         bool
	}{
		{"omitted", "", true},
		{"one-line", `,"offset":1,"limit":1`, true},
		{"offset-only", `,"offset":1`, true},
		{"limit-only", `,"limit":2000`, true},
		{"runtime-zero-offset", `,"offset":0,"limit":2`, true},
		{"string-offset", `,"offset":"1"`, false},
		{"string-limit", `,"limit":"1"`, false},
		{"null-offset", `,"offset":null`, false},
		{"null-limit", `,"limit":null`, false},
		{"negative-offset", `,"offset":-1`, false},
		{"negative-limit", `,"limit":-1`, false},
		{"fractional-limit", `,"limit":1.5`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"filePath":"/work/tools/bench/corpus/owner.txt"` + tc.fields + `}`
			stream := preservationEvent(t, "tool_use", "read", "call", input, "1: "+phase.Answer) + preservationEvent(t, "text", "", "", "", phase.Answer) + preservationEvent(t, "step_finish", "", "", "", "")
			session := ""
			if score := scoreReadTurn(strings.NewReader(stream), &session, phase, map[string]bool{}); score.FullPass != tc.pass {
				t.Fatalf("pass=%v score=%+v", tc.pass, score)
			}
		})
	}
}

func TestReadPreservationReadCountsAndAgreement(t *testing.T) {
	for id, phases := range readCases {
		t.Run(id, func(t *testing.T) {
			task, err := LoadTask(filepath.Join("../../tools/bench/tasks", id+".toml"))
			if err != nil {
				t.Fatal(err)
			}
			window := strings.HasPrefix(phases[0].File, "window-")
			for _, tc := range []struct {
				name  string
				reads int
				pass  bool
			}{
				{"single", 1, true},
				{"same-file-reread", 2, !window},
				{"same-file-three-reads", 3, !window},
				{"wrong-file-reread", 2, false},
				{"wrong-arguments-reread", 2, false},
				{"failed-reread", 2, false},
				{"unfinished-reread", 2, false},
				{"conflicting-reread-callback", 2, false},
				{"missing-read", 0, false},
				{"missing-database-reread", 2, false},
				{"extra-database-reread", 1, false},
				{"reversed-database-order", 2, false},
				{"reversed-stream-order", 2, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					store, err := db.Open(filepath.Join(t.TempDir(), "owned.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer store.Close()
					if _, err := store.DB().Exec(`INSERT INTO sessions(id) VALUES ('owner')`); err != nil {
						t.Fatal(err)
					}
					turns := make([]string, len(phases))
					for turn, p := range phases {
						var events []string
						for n := 0; n < tc.reads; n++ {
							input := fmt.Sprintf(`{"filePath":%q}`, "/work/tools/bench/corpus/"+p.File)
							if p.Offset != 0 {
								input = strings.TrimSuffix(input, "}") + `,"offset":4,"limit":2}`
							} else if !window {
								input = strings.TrimSuffix(input, "}") + `,"offset":1,"limit":1}`
							}
							if n == 1 && tc.name == "wrong-file-reread" {
								input = strings.Replace(input, p.File, "other.txt", 1)
							}
							if n == 1 && tc.name == "wrong-arguments-reread" {
								if p.Offset != 0 {
									input = strings.Replace(input, `"offset":4`, `"offset":3`, 1)
								} else if window {
									input = strings.TrimSuffix(input, "}") + `,"limit":2000}`
								} else {
									input = strings.Replace(input, `"offset":1`, `"offset":"1"`, 1)
								}
							}
							call := fmt.Sprintf("call-%d-%d", turn, n)
							// Distinguishable outputs make a swapped stream/row fail
							// even when all calls have the same valid arguments/fact.
							output := fmt.Sprintf("observation %d\n1: %s", n, p.Answer)
							event := preservationEvent(t, "tool_use", "read", call, input, output)
							if n == 1 && tc.name == "failed-reread" {
								event = strings.Replace(event, `"completed"`, `"error"`, 1)
							}
							if n == 1 && tc.name == "unfinished-reread" {
								event = strings.Replace(event, `"completed"`, `"running"`, 1)
							}
							events = append(events, event)
							if n == 1 && tc.name == "conflicting-reread-callback" {
								events = append(events, preservationEvent(t, "tool_use", "read", call, input, output+" extra"))
							}
							if n == 1 && tc.name == "missing-database-reread" {
								continue
							}
							order := turn*10 + n + 1
							if tc.name == "reversed-database-order" {
								order = turn*10 + tc.reads - n
							}
							if _, err := store.DB().Exec(`INSERT INTO tool_calls(id,session_id,tool,input,output,timestamp_unix_nano) VALUES (?,'owner','read',?,?,?)`, call, input, output, order); err != nil {
								t.Fatal(err)
							}
						}
						if tc.name == "reversed-stream-order" {
							events[0], events[1] = events[1], events[0]
						}
						turns[turn] = strings.Join(events, "") + preservationEvent(t, "text", "", "", "", p.Answer) + preservationEvent(t, "step_finish", "", "", "", "")
					}
					if tc.name == "extra-database-reread" {
						if _, err := store.DB().Exec(`INSERT INTO tool_calls(id,session_id,tool,input,output,timestamp_unix_nano) SELECT 'extra',session_id,tool,input,output,100 FROM tool_calls LIMIT 1`); err != nil {
							t.Fatal(err)
						}
					}
					score := scoreReadPreservation(task, "owner", store.DB(), func(turn int) (io.ReadCloser, error) {
						return io.NopCloser(strings.NewReader(turns[turn-1])), nil
					})
					if score.FullPass != tc.pass {
						t.Fatalf("pass=%v score=%+v", tc.pass, score)
					}
				})
			}
		})
	}
}

func TestReadPreservationAnswerOnlyCannotSubstituteForRead(t *testing.T) {
	phase := readCases["read-config-first"][0]
	stream := preservationEvent(t, "text", "", "", "", phase.Answer) + preservationEvent(t, "step_finish", "", "", "", "")
	session := ""
	result := scoreReadTurn(strings.NewReader(stream), &session, phase, map[string]bool{})
	if !result.AnswerMatchesExpected || result.FullPass {
		t.Fatalf("answer without a typed read passed: %+v", result)
	}
}

func TestReadPreservationMissingSecondPhaseAndRelativeSpelling(t *testing.T) {
	task, err := LoadTask("../../tools/bench/tasks/read-config-repeated.toml")
	if err != nil {
		t.Fatal(err)
	}
	if readArgs([]byte(`{"filePath":"tools/bench/corpus/owner.txt"}`), readCases[task.ID][0]) != true {
		t.Fatal("benign relative spelling rejected")
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "owned.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	phase := readCases[task.ID][0]
	first := preservationEvent(t, "tool_use", "read", "first", `{"filePath":"/work/tools/bench/corpus/owner.txt"}`, "1: "+phase.Answer) + preservationEvent(t, "text", "", "", "", phase.Answer) + preservationEvent(t, "step_finish", "", "", "", "")
	score := scoreReadPreservation(task, "ses_test", database.DB(), func(turn int) (io.ReadCloser, error) {
		if turn == 1 {
			return io.NopCloser(strings.NewReader(first)), nil
		}
		return nil, os.ErrNotExist
	})
	if score.FullPass || len(score.Phases) != 2 || !score.Phases[0].FullPass || score.Phases[1].FullPass {
		t.Fatalf("missing follow-up passed: %+v", score)
	}
}

func TestReadPreservationTaskDrift(t *testing.T) {
	for id := range readCases {
		task, err := LoadTask(filepath.Join("../../tools/bench/tasks", id+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		changed := task
		changed.Prompt += " Also guess."
		if validateReadPreservationTask(changed) == nil {
			t.Fatalf("accepted changed instructions in %s", id)
		}
		changed = task
		changed.CorpusFiles = []string{"wrong.txt"}
		if validateReadPreservationTask(changed) == nil {
			t.Fatalf("accepted changed corpus in %s", id)
		}
	}
}

func TestReadPreservationTerminalAuthority(t *testing.T) {
	task, err := LoadTask("../../tools/bench/tasks/read-config-first.toml")
	if err != nil {
		t.Fatal(err)
	}
	p := readCases[task.ID][0]
	input, output := `{"filePath":"/work/tools/bench/corpus/owner.txt"}`, "1: owner=iris"
	tool := preservationEvent(t, "tool_use", "read", "call", input, output)
	answer := preservationEvent(t, "text", "", "", "", p.Answer) + preservationEvent(t, "step_finish", "", "", "", "")
	for _, tc := range []struct {
		name, stream, input, output string
		outcome                     any
		pass                        bool
	}{
		{"completed with unknown pre-delivery outcome", tool + answer, input, output, nil, true},
		{"completed with successful database outcome", tool + answer, input, output, false, true},
		{"identical completed callback", tool + tool + answer, input, output, nil, true},
		{"explicit database failure", tool + answer, input, output, true, false},
		{"malformed database outcome", tool + answer, input, output, "invalid", false},
		{"unknown alone cannot succeed", answer, input, output, nil, false},
		{"database success alone cannot succeed", answer, input, output, false, false},
		{"running is not terminal success", strings.Replace(tool, `"completed"`, `"running"`, 1) + answer, input, output, nil, false},
		{"terminal failure", strings.Replace(tool, `"completed"`, `"error"`, 1) + answer, input, output, nil, false},
		{"retained output mismatch despite correct fact", tool + answer, input, output + "\nextra", nil, false},
		{"retained argument mismatch despite same target", tool + answer, `{"filePath":"tools/bench/corpus/owner.txt"}`, output, nil, false},
		{"conflicting completed callback", tool + strings.Replace(tool, output, output+" extra", 1) + answer, input, output, nil, false},
		{"no final stop", tool + preservationEvent(t, "text", "", "", "", p.Answer), input, output, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := db.Open(filepath.Join(t.TempDir(), "owned.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err := store.DB().Exec(`INSERT INTO sessions(id) VALUES ('owner')`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`INSERT INTO tool_calls(id,session_id,tool,input,output,is_error) VALUES ('call','owner','read',?,?,?)`, tc.input, tc.output, tc.outcome); err != nil {
				t.Fatal(err)
			}
			score := scoreReadPreservation(task, "owner", store.DB(), func(int) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(tc.stream)), nil
			})
			if score.FullPass != tc.pass {
				t.Fatalf("pass=%v score=%+v", tc.pass, score)
			}
		})
	}
}
