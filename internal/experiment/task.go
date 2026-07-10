package experiment

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type taskFile struct {
	ID               string   `toml:"id"`
	Prompt           string   `toml:"prompt"`
	MinimumToolCalls *int     `toml:"minimum_tool_calls"`
	Skill            string   `toml:"skill"`
	TokenBand        []int    `toml:"token_band"`
	CorpusFiles      []string `toml:"corpus_files"`
	WorkspaceMode    string   `toml:"workspace_mode"`
	QualityRisks     []string `toml:"quality_risks"`
	Scoring          struct {
		Kind           string   `toml:"kind"`
		ExpectedOutput *string  `toml:"expected_output"`
		Facts          []string `toml:"facts"`
	} `toml:"scoring"`
}

func LoadTask(path string) (Task, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Task{}, err
	}
	var raw taskFile
	if _, err := toml.DecodeFile(absolute, &raw); err != nil {
		return Task{}, fmt.Errorf("read task %q: %w", absolute, err)
	}
	if raw.ID == "" {
		return Task{}, fmt.Errorf("task file missing id: %s", absolute)
	}
	if strings.TrimSpace(raw.Prompt) == "" {
		return Task{}, fmt.Errorf("task file missing prompt: %s", absolute)
	}
	minimumToolCalls := 1
	if raw.MinimumToolCalls != nil {
		minimumToolCalls = *raw.MinimumToolCalls
	}
	if minimumToolCalls < 0 {
		return Task{}, fmt.Errorf("task file has invalid minimum_tool_calls: %s", absolute)
	}
	kind := raw.Scoring.Kind
	if kind == "" {
		kind = "exact_match"
	}
	task := Task{
		ID: raw.ID, Prompt: strings.TrimSpace(raw.Prompt), MinimumToolCalls: minimumToolCalls,
		Skill: raw.Skill, TokenBand: raw.TokenBand, CorpusFiles: raw.CorpusFiles,
		WorkspaceMode: raw.WorkspaceMode, QualityRisks: raw.QualityRisks, ScoringKind: kind,
		TaskFile: absolute,
	}
	if task.WorkspaceMode == "" {
		if len(task.CorpusFiles) > 0 {
			task.WorkspaceMode = "corpus"
		} else {
			task.WorkspaceMode = "repo"
		}
	}
	if task.WorkspaceMode != "corpus" && task.WorkspaceMode != "repo" {
		return Task{}, fmt.Errorf("task file workspace_mode must be corpus or repo: %s", absolute)
	}
	if task.WorkspaceMode == "corpus" {
		for _, name := range task.CorpusFiles {
			if filepath.Base(name) != name || filepath.Ext(name) != ".txt" {
				return Task{}, fmt.Errorf("corpus workspace only mounts .txt fixtures, not %q: %s", name, absolute)
			}
		}
	}
	switch kind {
	case "exact_match":
		if raw.Scoring.ExpectedOutput == nil {
			return Task{}, fmt.Errorf("exact_match task needs scoring.expected_output: %s", absolute)
		}
		task.ExpectedOutput = raw.Scoring.ExpectedOutput
	case "multi_fact_fractional":
		if len(raw.Scoring.Facts) == 0 {
			return Task{}, fmt.Errorf("task file needs scoring.facts string list: %s", absolute)
		}
		task.ExpectedFacts = raw.Scoring.Facts
	default:
		return Task{}, fmt.Errorf("unsupported scoring.kind %q in %s", kind, absolute)
	}
	return task, nil
}

func ScoreAnswer(answer string, expectedOutput *string, expectedFacts []string) Score {
	text := strings.TrimSpace(answer)
	if expectedFacts != nil {
		matched := make([]string, 0, len(expectedFacts))
		missed := make([]string, 0, len(expectedFacts))
		for _, fact := range expectedFacts {
			if strings.Contains(text, fact) {
				matched = append(matched, fact)
			} else {
				missed = append(missed, fact)
			}
		}
		full := len(expectedFacts) > 0 && len(missed) == 0
		score := 0.0
		if len(expectedFacts) > 0 {
			score = float64(len(matched)) / float64(len(expectedFacts))
		}
		return Score{Score: score, FullPass: full, AnswerMatchesExpected: full, FactsMatched: matched, FactsMissed: missed, FactsTotal: len(expectedFacts)}
	}
	expected := ""
	if expectedOutput != nil {
		expected = strings.TrimSpace(*expectedOutput)
	}
	full := text == expected
	score := 0.0
	if full {
		score = 1
	}
	return Score{Score: score, FullPass: full, AnswerMatchesExpected: full, FactsMatched: []string{}, FactsMissed: []string{}}
}
