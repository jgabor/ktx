package experiment

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCallAccountingKeepsAuthoritiesSeparate(t *testing.T) {
	key1, key2 := "one", "two"
	tests := []struct {
		name              string
		calls             *callAccounting
		streamState       string
		persistenceState  string
		coverage          any
		missing, unpaired int
	}{
		{
			name: "full match",
			calls: &callAccounting{
				Stream:      availableCalls(map[string]bool{key1: false}, 0),
				Persistence: availableCalls(map[string]bool{key1: false}, 0),
			},
			streamState: "available", persistenceState: "available", coverage: 1.0,
		},
		{
			name: "failed stream call missing hook row",
			calls: &callAccounting{
				Stream:      availableCalls(map[string]bool{key1: false, key2: true}, 0),
				Persistence: availableCalls(map[string]bool{key1: false}, 0),
			},
			streamState: "available", persistenceState: "available", coverage: 0.5, missing: 1,
		},
		{
			name: "exact duplicate stream event",
			calls: &callAccounting{
				Stream:      availableCalls(map[string]bool{key1: false}, 1),
				Persistence: availableCalls(map[string]bool{key1: false}, 0),
			},
			streamState: "available", persistenceState: "available", coverage: 1.0,
		},
		{
			name: "stream unavailable",
			calls: &callAccounting{
				Stream: unavailableCallAuthority("stream_authority_unavailable"), Persistence: availableCalls(map[string]bool{key1: false}, 0),
			},
			streamState: "unavailable", persistenceState: "available", coverage: nil,
		},
		{
			name: "database unavailable",
			calls: &callAccounting{
				Stream: availableCalls(map[string]bool{key1: false}, 0), Persistence: unavailableCallAuthority("persistence_authority_unavailable"),
			},
			streamState: "available", persistenceState: "unavailable", coverage: nil,
		},
		{
			name: "database conflict",
			calls: &callAccounting{
				Stream: availableCalls(map[string]bool{key1: false}, 0), Persistence: unavailableCallAuthority("persistence_authority_conflict"),
			},
			streamState: "available", persistenceState: "unavailable", coverage: nil,
		},
		{
			name: "unpaired persisted row",
			calls: &callAccounting{
				Stream: availableCalls(map[string]bool{key1: false}, 0), Persistence: availableCalls(map[string]bool{key1: false, key2: false}, 0),
			},
			streamState: "available", persistenceState: "available", coverage: 1.0, unpaired: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempt := Attempt{calls: test.calls}
			got := callAccountingSummary([]Attempt{attempt})
			stream := got["stream"].(map[string]any)
			persistence := got["persistence"].(map[string]any)
			coverage := got["persistence_coverage"].(map[string]any)
			if stream["state"] != test.streamState || persistence["state"] != test.persistenceState || coverage["value"] != test.coverage {
				t.Fatalf("accounting = %#v", got)
			}
			if test.streamState == "available" && stream["error_call_count"] != countErrors(test.calls.Stream.Calls) {
				t.Fatalf("stream errors = %v", stream["error_call_count"])
			}
			if test.streamState == "available" && stream["duplicate_event_count"] != test.calls.Stream.Duplicates {
				t.Fatalf("stream duplicates = %v", stream["duplicate_event_count"])
			}
			if test.persistenceState == "unavailable" && persistence["reason"] != test.calls.Persistence.Reason {
				t.Fatalf("persistence reason = %v", persistence["reason"])
			}
			if test.streamState == "available" && test.persistenceState == "available" {
				counts := got["discrepancies"].(map[string]any)["counts"].(map[string]any)
				if counts["stream_call_without_persisted_row"] != test.missing || counts["persisted_row_without_stream_call"] != test.unpaired {
					t.Fatalf("discrepancies = %#v", counts)
				}
			}
		})
	}
}

func TestCallAccountingRetainedMidfileShape(t *testing.T) {
	stream, persisted := map[string]bool{}, map[string]bool{}
	for index := 0; index < 19; index++ {
		key := fmt.Sprintf("source-%02d", index)
		stream[key] = index >= 15
		if index < 15 {
			persisted[key] = false
		}
	}
	got := callAccountingSummary([]Attempt{{calls: &callAccounting{Stream: availableCalls(stream, 0), Persistence: availableCalls(persisted, 0)}}})
	streamSummary := got["stream"].(map[string]any)
	persistedSummary := got["persistence"].(map[string]any)
	discrepancies := got["discrepancies"].(map[string]any)["counts"].(map[string]any)
	if streamSummary["call_count"] != 19 || streamSummary["error_call_count"] != 4 || persistedSummary["call_count"] != 15 || discrepancies["stream_call_without_persisted_row"] != 4 || discrepancies["persisted_row_without_stream_call"] != 0 || got["persistence_coverage"].(map[string]any)["value"] != 15.0/19.0 {
		t.Fatalf("accounting = %#v", got)
	}
}

func TestStreamCallAuthoritySeparatesDuplicateAndRetryEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	failed := `{"type":"tool_use","sessionID":"native","part":{"callID":"call","state":{"status":"error"}}}`
	succeeded := `{"type":"tool_use","sessionID":"native","part":{"callID":"call","state":{"status":"completed"}}}`
	if err := os.WriteFile(path, []byte(failed+"\n"+failed+"\n"+succeeded+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := streamCallAuthority(path)
	if got.State != "available" || got.CallCount != 1 || got.ErrorCount != 1 || got.Duplicates != 1 || got.Retries != 1 {
		t.Fatalf("stream authority = %#v", got)
	}
}

func availableCalls(calls map[string]bool, duplicates int) callAuthority {
	return callAuthority{State: "available", Reason: nil, CallCount: len(calls), ErrorCount: countErrors(calls), Unknown: 0, Duplicates: duplicates, Retries: 0, Calls: calls}
}

func countErrors(calls map[string]bool) int {
	count := 0
	for _, failed := range calls {
		if failed {
			count++
		}
	}
	return count
}
