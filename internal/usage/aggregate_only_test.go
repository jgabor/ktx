package usage

import (
	"reflect"
	"testing"
)

func TestAggregateOnlyPreservesEvidenceWithoutHistory(t *testing.T) {
	rich := &Aggregator{}
	aggregate := NewAggregateOnly()
	for _, line := range []string{
		`{"type":"step_finish","part":{"tokens":{"input":9007199254740992,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0.1}}`,
		`{"type":"step_finish","part":{"tokens":{"input":-9007199254740992,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"cost":-0.1}}`,
		`{"type":"step_finish","part":{"tokens":{"input":1,"output":null,"reasoning":1.5,"cache":{"read":"bad"}},"cost":0}}`,
		`{"type":"step_finish","part":null}`,
		`{"type":"text","content":"4"}`,
	} {
		if rich.AddLine([]byte(line)) != aggregate.AddLine([]byte(line)) {
			t.Fatal("event acceptance changed")
		}
		want, got := rich.Result(), aggregate.Result()
		want.ModelSteps = nil
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("aggregate mismatch: got %#v want %#v", got, want)
		}
		if len(aggregate.modelSteps) != 0 {
			t.Fatal("aggregate retained model step history")
		}
	}
}
