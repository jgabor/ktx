package experiment

import (
	"database/sql"
	"fmt"
)

// ValidateCollectedAttempts checks retained field authorities without requiring
// the final target count or rewriting a derived summary. It is an admission
// check for callers that must stop on invalid evidence before another launch.
func ValidateCollectedAttempts(directory string) error {
	a, err := openArchive(directory)
	if err != nil {
		return err
	}
	defer a.Close()
	m, attempts, _, err := loadExperimentEvidence(a)
	if err != nil {
		return err
	}
	if _, err := a.preparePricingEvidence(m); err != nil {
		return err
	}
	for _, attempt := range attempts {
		if len(attempt.validationReasons) > 0 {
			return fmt.Errorf("attempt %s has invalid retained evidence: %v", attempt.AttemptID, attempt.validationReasons)
		}
	}
	return nil
}

// RetainedToolCall is the routing input of one persisted attempt tool call.
type RetainedToolCall struct {
	AttemptID string
	Tool      string
	Input     string
	IsError   *bool
}

// RetainedToolCalls reads the persisted tool calls of every retained attempt
// through confined private database copies. An attempt database that cannot
// be read is an error, never an empty call list.
func RetainedToolCalls(directory string) ([]RetainedToolCall, error) {
	a, err := openArchive(directory)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	_, attempts, _, err := loadExperimentEvidence(a)
	if err != nil {
		return nil, err
	}
	calls := []RetainedToolCall{}
	seen := map[string]bool{}
	for _, attempt := range attempts {
		name := attempt.Artifacts["database"]
		if name == "" || seen[attempt.AttemptID] {
			continue
		}
		seen[attempt.AttemptID] = true
		if attempt.SessionID == nil || *attempt.SessionID == "" {
			return nil, fmt.Errorf("attempt %s database has no session owner", attempt.AttemptID)
		}
		attemptCalls, err := a.retainedToolCalls(name, attempt.AttemptID, *attempt.SessionID)
		if err != nil {
			return nil, fmt.Errorf("attempt %s tool calls: %w", attempt.AttemptID, err)
		}
		calls = append(calls, attemptCalls...)
	}
	return calls, nil
}

func (a *archive) retainedToolCalls(name, attemptID, sessionID string) ([]RetainedToolCall, error) {
	database, cleanup, err := a.openDatabase(name)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.DB().Query(`SELECT tool, input, is_error FROM tool_calls WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := []RetainedToolCall{}
	for rows.Next() {
		var tool, input sql.NullString
		var isError sql.NullBool
		if err := rows.Scan(&tool, &input, &isError); err != nil {
			return nil, err
		}
		call := RetainedToolCall{AttemptID: attemptID, Tool: tool.String, Input: input.String}
		if isError.Valid {
			value := isError.Bool
			call.IsError = &value
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

func completeAttemptPairs(attempts []Attempt, options CompareOptions) int {
	cycles := map[int]map[string]bool{}
	for _, attempt := range attempts {
		if attempt.Role != MeasuredWarm || !AttemptIsTargetValid(attempt, options.Task.MinimumToolCalls, options.methodologyVersion) || attempt.TokensIn == nil || attempt.TokensOut == nil || attempt.Reasoning == nil {
			continue
		}
		if cycles[attempt.Cycle] == nil {
			cycles[attempt.Cycle] = map[string]bool{}
		}
		cycles[attempt.Cycle][attempt.Side] = true
	}
	count := 0
	for _, sides := range cycles {
		if sides[Baseline] && sides[Candidate] {
			count++
		}
	}
	return count
}
