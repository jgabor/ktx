package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestContractFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/contract_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string   `json:"name"`
		Decoder  string   `json:"decoder"`
		Accepted bool     `json:"accepted"`
		Rules    []string `json:"rules"`
		Payload  string   `json:"payload"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	type coverage struct{ accepted, rejected bool }
	covered := make(map[string]coverage)
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			if len(test.Rules) == 0 {
				t.Fatal("fixture does not identify a contract rule")
			}
			var err error
			switch test.Decoder {
			case "step":
				_, err = DecodeModelStep([]byte(test.Payload))
			case "message":
				_, _, err = DecodeMessageIdentity([]byte(test.Payload))
			default:
				t.Fatalf("unknown decoder %q", test.Decoder)
			}
			if test.Accepted && err != nil {
				t.Fatalf("accepted fixture rejected: %v", err)
			}
			if !test.Accepted && err == nil {
				t.Fatal("rejected fixture accepted")
			}
			for _, rule := range test.Rules {
				state := covered[rule]
				state.accepted = state.accepted || test.Accepted
				state.rejected = state.rejected || !test.Accepted
				covered[rule] = state
			}
		})
	}
	for rule, state := range covered {
		if !state.accepted || !state.rejected {
			t.Errorf("rule %q coverage = accepted:%t rejected:%t, want both", rule, state.accepted, state.rejected)
		}
	}
}

func TestCollectorCorrelatesDeduplicatesAndOrders(t *testing.T) {
	collector := NewCollector()
	identity := []byte(`{"type":"message.updated","properties":{"info":{"id":"msg_1","sessionID":"ses_1","role":"assistant","providerID":"openai","modelID":"gpt-5"}}}`)
	if observation, err := collector.Add(identity); err != nil || observation != nil {
		t.Fatalf("identity result = %#v, %v", observation, err)
	}
	first := modelStep("prt_1", "msg_1", 0, 0)
	observation, err := collector.Add(first)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Ordinal != 1 || observation.Provider == nil || *observation.Provider != "openai" || observation.Model == nil || *observation.Model != "gpt-5" {
		t.Fatalf("first observation = %#v", observation)
	}
	if _, err := collector.Add(first); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate error = %v, want ErrDuplicate", err)
	}
	second, err := collector.Add(modelStep("prt_2", "msg_2", 12, 3))
	if err != nil {
		t.Fatal(err)
	}
	if second.Ordinal != 2 || second.Provider != nil || second.Model != nil {
		t.Fatalf("second observation = %#v", second)
	}
	if _, err := collector.Add([]byte(`{"type":"session.idle","properties":{"sessionID":"ses_1"}}`)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported error = %v, want ErrUnsupported", err)
	}
	third, err := collector.Add(modelStep("prt_3", "msg_3", 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if third.Ordinal != 3 {
		t.Fatalf("third ordinal = %d, want 3", third.Ordinal)
	}
}

func TestCollectorKeepsFirstObservationAuthoritative(t *testing.T) {
	collector := NewCollector()
	first, err := collector.Add(modelStep("prt_1", "msg_1", 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if first.Provider != nil || first.Model != nil {
		t.Fatalf("uncorrelated identity was synthesized: %#v", first)
	}
	identity := []byte(`{"type":"message.updated","properties":{"info":{"id":"msg_1","sessionID":"ses_1","role":"assistant","providerID":"openai","modelID":"gpt-5"}}}`)
	if _, err := collector.Add(identity); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Add(modelStep("prt_1", "msg_1", 9, 9)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("changed duplicate error = %v, want ErrDuplicate", err)
	}
	if first.Provider != nil || first.Model != nil || first.Tokens.CacheRead != 1 || first.Tokens.CacheWrite != 2 {
		t.Fatalf("first observation changed: %#v", first)
	}
}

func modelStep(partID, messageID string, read, write int64) []byte {
	payload := map[string]any{
		"type": "message.part.updated",
		"properties": map[string]any{"part": map[string]any{
			"id": partID, "sessionID": "ses_1", "messageID": messageID,
			"type": "step-finish", "reason": "stop", "cost": 0,
			"tokens": map[string]any{"input": 1, "output": 1, "reasoning": 0, "cache": map[string]any{"read": read, "write": write}},
		}},
	}
	data, _ := json.Marshal(payload)
	return data
}
