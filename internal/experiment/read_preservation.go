package experiment

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jgabor/ktx/internal/canon"
)

const readPreservationScorer = "read-preservation-v1"

type readPhase struct {
	File, Answer  string
	Offset, Limit int
}

var readCases = map[string][]readPhase{
	"read-config-first":    {{File: "owner.txt", Answer: "owner=iris"}},
	"read-config-repeated": {{File: "owner.txt", Answer: "owner=iris"}, {File: "deployment.txt", Answer: "deployment=blue"}},
	"read-paged-first":     {{File: "log-a.txt", Answer: "FACT=quartz", Offset: 4, Limit: 2}},
	"read-paged-repeated":  {{File: "log-a.txt", Answer: "FACT=quartz", Offset: 4, Limit: 2}, {File: "log-b.txt", Answer: "FACT=amber", Offset: 4, Limit: 2}},
	"read-window-first":    {{File: "window-a.txt", Answer: "a-2000"}},
	"read-window-repeated": {{File: "window-a.txt", Answer: "a-2000"}, {File: "window-b.txt", Answer: "b-2000"}},
}

var readFixtureHashes = map[string]string{
	"owner.txt":      "90395ac97b879084b4c2ba7ffbbb5cc4b53691d652c1e15a18f79a0c8818e2b9",
	"deployment.txt": "2b0708f97537f19b968930bd21911b8fc6f413c20a0f10d21f3026aeb00075dc",
	"log-a.txt":      "74d4599331766aa9ed8872596b1a6d5c712b9ab979bece7616a88caea9010b4a",
	"log-b.txt":      "52a8a76b33bc75c894d175cbe029a8f6a1d7287b086c660ffcee10d40aa6148a",
	"window-a.txt":   "4e240122c360213a86186a128d840203105d86575d0a080cfc656c7bd25c955c",
	"window-b.txt":   "47992f89310624676e42a5f6489c8c20027e0484f5491367f9163f95e41db022",
}

func readPrompt(p readPhase) string {
	path := "/work/tools/bench/corpus/" + p.File
	if p.Offset != 0 {
		return fmt.Sprintf("Use read on %s with offset=4 and limit=2. Reply with only the FACT line. Do not edit files or use any non-read tool.", path)
	}
	if strings.HasPrefix(p.File, "window-") {
		return fmt.Sprintf("Use a single default read of %s. Omit limit and offset; do not page further. Reply with only the last numbered line returned. Do not edit files or use any non-read tool.", path)
	}
	return fmt.Sprintf("Use read on %s. Reply with only its key=value line. Do not edit files or use any non-read tool.", path)
}

func validateReadPreservationTask(task Task) error {
	phases, ok := readCases[task.ID]
	if !ok || task.ScoringKind != "read_preservation" {
		return errors.New("unsupported read-preservation task")
	}
	files := make([]string, 0, len(phases))
	for _, p := range phases {
		files = append(files, p.File)
	}
	followup := ""
	if len(phases) == 2 {
		followup = "Now use " + strings.TrimPrefix(readPrompt(phases[1]), "Use ")
	}
	if task.WorkspaceMode != "corpus" || task.Skill != "" || task.MinimumToolCalls != len(phases) || !reflect.DeepEqual(task.CorpusFiles, files) || task.Prompt != readPrompt(phases[0]) || task.FollowupPrompt != followup || task.ExpectedOutput == nil || *task.ExpectedOutput != phases[len(phases)-1].Answer || len(task.ExpectedFacts) != 0 {
		return errors.New("read-preservation task differs from its frozen contract")
	}
	return nil
}

// The long windows are generated from the frozen decimal-line rule, never
// inserted into a model prompt or copied from a mutable checkout fixture.
func readWindow(name string) []byte {
	var b strings.Builder
	for n := 1; n <= 2001; n++ {
		fmt.Fprintf(&b, "%c-%04d\n", name[7], n)
	}
	return []byte(b.String())
}

func mountReadFixture(source, destination, name string) error {
	want, controlled := readFixtureHashes[name]
	if !controlled {
		return copyTreeFile(source, destination)
	}
	var data []byte
	var err error
	if name == "window-a.txt" || name == "window-b.txt" {
		data = readWindow(name)
	} else {
		data, err = os.ReadFile(source)
	}
	if err != nil || sha256Bytes(data) != want {
		return fmt.Errorf("read fixture %s missing or changed: %v", name, err)
	}
	return os.WriteFile(destination, data, 0o644)
}

// A phase needs explicit terminal success, not a model assertion of tool use.
// The read hook's pre-delivery database outcome can be unknown; its arguments
// and output must match the successful terminal observation, never stand in for it.
func scoreReadPreservation(task Task, session string, db *sql.DB, openTurn func(int) (io.ReadCloser, error)) Score {
	phases := readCases[task.ID]
	result := Score{FactsMatched: []string{}, FactsMissed: []string{}, Phases: make([]ReadPhaseScore, len(phases))}
	all := true
	answerOK := true
	callIDs := map[string]bool{}
	streamSession := ""
	var completed []readCall
	for i, phase := range phases {
		observed := readTurnScore{ReadPhaseScore: ReadPhaseScore{File: phase.File}}
		r, err := openTurn(i + 1)
		if err == nil {
			observed = scoreReadTurn(r, &streamSession, phase, callIDs)
			_ = r.Close()
		}
		completed = append(completed, observed.Calls...)
		result.Phases[i] = observed.ReadPhaseScore
		all = all && observed.FullPass
		answerOK = answerOK && observed.AnswerMatchesExpected
	}
	// Keep every distinct completed read in order, including same-file rereads.
	// Each must agree with one retained row; callback duplicates are not new calls.
	rows, err := db.Query(`SELECT tool,COALESCE(input,''),COALESCE(output,''),is_error FROM tool_calls WHERE session_id=? ORDER BY COALESCE(timestamp_unix_nano,ktx_unix_nano(timestamp)),id`, session)
	if err != nil {
		all = false
	} else {
		defer rows.Close()
		index := 0
		for rows.Next() {
			var tool, input, output string
			var failed sql.NullBool
			if rows.Scan(&tool, &input, &output, &failed) != nil || index >= len(completed) {
				all = false
				break
			}
			terminal := completed[index]
			if tool != "read" || (failed.Valid && failed.Bool) || !canon.Equal([]byte(input), terminal.Input) || output != terminal.Output {
				all = false
			}
			index++
		}
		all = all && rows.Err() == nil && index == len(completed) && len(callIDs) == len(completed)
	}
	result.AnswerMatchesExpected = answerOK
	result.FullPass = all
	if all {
		result.Score = 1
	}
	return result
}

type readTurnScore struct {
	ReadPhaseScore
	Calls []readCall
}

type readCall struct {
	Input  json.RawMessage
	Output string
}

func scoreReadTurn(r io.Reader, session *string, phase readPhase, callIDs map[string]bool) readTurnScore {
	result := readTurnScore{ReadPhaseScore: ReadPhaseScore{File: phase.File}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(nil, 64<<20)
	var text strings.Builder
	stopped, invalid := false, false
	seen := map[string]int{}
	for scanner.Scan() {
		var e struct {
			Type, SessionID string
			Part            struct {
				Tool, CallID, Text, Reason string
				State                      struct {
					Status string
					Input  json.RawMessage
					Output string
				} `json:"state"`
			} `json:"part"`
		}
		if json.Unmarshal(scanner.Bytes(), &e) != nil || !strings.HasPrefix(e.SessionID, "ses_") || (*session != "" && e.SessionID != *session) {
			invalid = true
			continue
		}
		*session = e.SessionID
		switch e.Type {
		case "tool_use":
			index, seenHere := seen[e.Part.CallID]
			if e.Part.CallID == "" || e.Part.Tool != "read" || !readArgs(e.Part.State.Input, phase) || (callIDs[e.Part.CallID] && !seenHere) {
				invalid = true
			} else {
				if !seenHere {
					seen[e.Part.CallID] = -1
				}
				if e.Part.State.Status == "completed" && strings.Contains(e.Part.State.Output, phase.Answer) {
					if !callIDs[e.Part.CallID] {
						seen[e.Part.CallID] = len(result.Calls)
						result.Calls = append(result.Calls, readCall{Input: e.Part.State.Input, Output: e.Part.State.Output})
					} else if !canon.Equal(result.Calls[index].Input, e.Part.State.Input) || result.Calls[index].Output != e.Part.State.Output {
						invalid = true
					}
					callIDs[e.Part.CallID] = true
				} else if e.Part.State.Status != "pending" && e.Part.State.Status != "running" {
					invalid = true
				}
			}
		case "text":
			text.WriteString(e.Part.Text)
		case "step_finish":
			stopped = e.Part.Reason == "stop"
		case "error":
			invalid = true
		}
	}
	result.AnswerMatchesExpected = strings.TrimSpace(text.String()) == phase.Answer
	reads := len(result.Calls)
	cardinalityOK := reads >= 1 && (!strings.HasPrefix(phase.File, "window-") || reads == 1)
	result.FullPass = result.AnswerMatchesExpected && cardinalityOK && reads == len(seen) && stopped && !invalid && scanner.Err() == nil
	return result
}

func readArgs(raw []byte, phase readPhase) bool {
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return false
	}
	var file string
	if json.Unmarshal(args["filePath"], &file) != nil || file == "" {
		return false
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join("/work", file)
	}
	if filepath.Clean(file) != "/work/tools/bench/corpus/"+phase.File {
		return false
	}
	if strings.HasPrefix(phase.File, "window-") {
		return args["offset"] == nil && args["limit"] == nil
	}
	if phase.Offset == 0 {
		// Config lookup permits optional numeric read arguments. Its output and
		// exact answer, not omitted pagination fields, establish preservation.
		for _, key := range []string{"offset", "limit"} {
			if raw, present := args[key]; present {
				var value *int
				if json.Unmarshal(raw, &value) != nil || value == nil || *value < 0 {
					return false
				}
			}
		}
		return true
	}
	var offset, limit int
	return json.Unmarshal(args["offset"], &offset) == nil && json.Unmarshal(args["limit"], &limit) == nil && offset == phase.Offset && limit == phase.Limit
}

func checkReadWorkspace(a *archive, attempt Attempt, task Task) error {
	for _, name := range task.CorpusFiles {
		f, err := a.open(filepath.ToSlash(filepath.Join(attempt.Artifacts["workspace"], "tools/bench/corpus", name)))
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != readFixtureHashes[name] {
			return fmt.Errorf("read fixture %s differs from frozen input", name)
		}
	}
	return nil
}

func scoreLocalReadAttempt(task Task, session, directory string) (Score, error) {
	d, err := openArchive(directory)
	if err != nil {
		return Score{}, err
	}
	defer d.Close()
	conn, cleanup, err := d.openDatabase("ktx.db")
	if err != nil {
		return Score{}, err
	}
	defer cleanup()
	defer conn.Close()
	return scoreReadPreservation(task, session, conn.DB(), func(turn int) (io.ReadCloser, error) {
		return os.Open(filepath.Join(directory, "read-description", fmt.Sprintf("turn-%d.jsonl", turn)))
	}), nil
}
