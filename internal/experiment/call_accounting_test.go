package experiment

import "testing"

func TestCallAccountingKeepsAuthoritiesSeparate(t *testing.T) {
	key1, key2 := "session\x00one", "session\x00two"
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
			if test.streamState == "available" && test.persistenceState == "available" {
				counts := got["discrepancies"].(map[string]any)["counts"].(map[string]any)
				if counts["stream_call_without_persisted_row"] != test.missing || counts["persisted_row_without_stream_call"] != test.unpaired {
					t.Fatalf("discrepancies = %#v", counts)
				}
			}
		})
	}
}

func availableCalls(calls map[string]bool, duplicates int) callAuthority {
	return callAuthority{State: "available", Reason: nil, CallCount: len(calls), ErrorCount: countErrors(calls), Unknown: 0, Duplicates: duplicates, Calls: calls}
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
