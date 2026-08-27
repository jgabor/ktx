package experiment

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"

	"github.com/jgabor/ktx/internal/identity"
)

type callAuthority struct {
	State      string
	Reason     any
	CallCount  any
	ErrorCount any
	Unknown    any
	Duplicates any
	Calls      map[string]bool
}

type callAccounting struct {
	Stream      callAuthority
	Persistence callAuthority
}

func unavailableCallAuthority(reason string) callAuthority {
	return callAuthority{State: "unavailable", Reason: reason, CallCount: nil, ErrorCount: nil, Duplicates: nil}
}

func streamCallAuthority(path string) callAuthority {
	file, err := os.Open(path)
	if err != nil {
		return unavailableCallAuthority("stream_authority_unavailable")
	}
	defer file.Close()
	calls := map[string]bool{}
	errors, duplicates := 0, 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Part      struct {
				CallID string `json:"callID"`
				State  struct {
					Status string `json:"status"`
				} `json:"state"`
			} `json:"part"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "tool_use" {
			continue
		}
		if event.SessionID == "" || event.Part.CallID == "" {
			return unavailableCallAuthority("stream_call_identity_unavailable")
		}
		id, err := identity.Derive("opencode", event.SessionID, event.Part.CallID)
		if err != nil {
			return unavailableCallAuthority("stream_call_identity_unavailable")
		}
		key := event.SessionID + "\x00" + id
		if _, exists := calls[key]; exists {
			duplicates++
			continue
		}
		calls[key] = event.Part.State.Status == "error"
		if calls[key] {
			errors++
		}
	}
	if scanner.Err() != nil {
		return unavailableCallAuthority("stream_authority_unavailable")
	}
	return callAuthority{State: "available", Reason: nil, CallCount: len(calls), ErrorCount: errors, Duplicates: duplicates, Calls: calls}
}

func persistedCallAuthority(database *sql.DB, sessionID string) callAuthority {
	rows, err := database.Query(`SELECT id,is_error FROM tool_calls WHERE session_id=?`, sessionID)
	if err != nil {
		return unavailableCallAuthority("persistence_authority_unavailable")
	}
	defer rows.Close()
	calls := map[string]bool{}
	errors, unknown := 0, 0
	for rows.Next() {
		var id string
		var isError sql.NullBool
		if rows.Scan(&id, &isError) != nil || id == "" {
			return unavailableCallAuthority("persistence_authority_conflict")
		}
		key := sessionID + "\x00" + id
		if _, exists := calls[key]; exists {
			return unavailableCallAuthority("persistence_authority_conflict")
		}
		calls[key] = isError.Valid && isError.Bool
		if !isError.Valid {
			unknown++
		} else if isError.Bool {
			errors++
		}
	}
	if rows.Err() != nil {
		return unavailableCallAuthority("persistence_authority_unavailable")
	}
	return callAuthority{State: "available", Reason: nil, CallCount: len(calls), ErrorCount: errors, Unknown: unknown, Duplicates: 0, Calls: calls}
}

func callAccountingSummary(attempts []Attempt) map[string]any {
	streamCount, streamErrors, streamDuplicates := 0, 0, 0
	persistedCount, persistedErrors, persistedUnknown := 0, 0, 0
	missingRows, unpairedRows := 0, 0
	streamState, persistenceState := "available", "available"
	var streamReason, persistenceReason any
	if len(attempts) == 0 {
		streamState, streamReason = "unavailable", "stream_authority_unavailable"
		persistenceState, persistenceReason = "unavailable", "persistence_authority_unavailable_or_conflicting"
	}
	for _, attempt := range attempts {
		if attempt.calls == nil || attempt.calls.Stream.State != "available" {
			streamState = "unavailable"
			streamReason = mergeAuthorityReason(streamReason, authorityReason(attempt.calls, true), "stream_authority_unavailable")
		} else {
			streamCount += attempt.calls.Stream.CallCount.(int)
			streamErrors += attempt.calls.Stream.ErrorCount.(int)
			streamDuplicates += attempt.calls.Stream.Duplicates.(int)
		}
		if attempt.calls == nil || attempt.calls.Persistence.State != "available" {
			persistenceState = "unavailable"
			persistenceReason = mergeAuthorityReason(persistenceReason, authorityReason(attempt.calls, false), "persistence_authority_unavailable_or_conflicting")
		} else {
			persistedCount += attempt.calls.Persistence.CallCount.(int)
			persistedErrors += attempt.calls.Persistence.ErrorCount.(int)
			persistedUnknown += attempt.calls.Persistence.Unknown.(int)
		}
		if attempt.calls == nil || attempt.calls.Stream.State != "available" || attempt.calls.Persistence.State != "available" {
			continue
		}
		for key := range attempt.calls.Stream.Calls {
			if _, ok := attempt.calls.Persistence.Calls[key]; !ok {
				missingRows++
			}
		}
		for key := range attempt.calls.Persistence.Calls {
			if _, ok := attempt.calls.Stream.Calls[key]; !ok {
				unpairedRows++
			}
		}
	}
	stream := map[string]any{"state": streamState, "reason": streamReason, "call_count": nil, "error_call_count": nil, "duplicate_event_count": nil}
	persistence := map[string]any{"state": persistenceState, "reason": persistenceReason, "call_count": nil, "error_call_count": nil, "unknown_error_state_count": nil}
	coverage := map[string]any{"state": "unavailable", "value": nil, "reason": "authority_unavailable"}
	discrepancies := map[string]any{"state": "unavailable", "reason": "authority_unavailable", "counts": nil}
	if streamState == "available" {
		stream["call_count"], stream["error_call_count"], stream["duplicate_event_count"] = streamCount, streamErrors, streamDuplicates
	}
	if persistenceState == "available" {
		persistence["call_count"], persistence["error_call_count"], persistence["unknown_error_state_count"] = persistedCount, persistedErrors, persistedUnknown
	}
	if streamState == "available" && persistenceState == "available" {
		var value any
		if streamCount > 0 {
			value = float64(streamCount-missingRows) / float64(streamCount)
		}
		coverage = map[string]any{"state": "available", "value": value, "reason": nil}
		discrepancies = map[string]any{"state": "available", "reason": nil, "counts": map[string]any{"stream_call_without_persisted_row": missingRows, "persisted_row_without_stream_call": unpairedRows}}
	}
	return map[string]any{"stream": stream, "persistence": persistence, "persistence_coverage": coverage, "discrepancies": discrepancies}
}

func authorityReason(calls *callAccounting, stream bool) any {
	if calls == nil {
		return nil
	}
	if stream {
		return calls.Stream.Reason
	}
	return calls.Persistence.Reason
}

func mergeAuthorityReason(current, next any, mixed string) any {
	if next == nil {
		next = mixed
	}
	if current == nil || current == next {
		return next
	}
	return mixed
}
