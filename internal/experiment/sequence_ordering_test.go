package experiment

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"

	"github.com/jgabor/ktx/internal/cache/reuse/sequence"
)

func manyPairAttempt() Attempt {
	const steps = 256
	attempt := directShareAttempt("many-pairs")
	for i := 0; i < steps; i++ {
		attempt.ModelSteps = append(attempt.ModelSteps, directStep(uint64(i+1), MeasuredInteger(2000), MeasuredInteger(1024), MeasuredInteger(0)))
	}
	attempt.sequence = coveredAttemptSequence(steps)
	for ordinal := uint64(1); ordinal <= steps*10+10; ordinal++ {
		attempt.sequence.Events = append(attempt.sequence.Events, sequence.SequenceFact{
			Ordinal: ordinal, SessionID: "session-a",
			Provenance: sequence.SequenceProvenance{Runtime: "opencode", RuntimeVersion: "1.18.5", Source: "event_hook", StartOrdinal: ordinal, EndOrdinal: ordinal},
		})
	}
	rand.New(rand.NewSource(73)).Shuffle(len(attempt.sequence.Events), func(i, j int) {
		attempt.sequence.Events[i], attempt.sequence.Events[j] = attempt.sequence.Events[j], attempt.sequence.Events[i]
	})
	return attempt
}

func TestManyPairSequenceResultBytes(t *testing.T) {
	attempt := manyPairAttempt()
	before, err := json.Marshal(attempt.sequence)
	if err != nil {
		t.Fatal(err)
	}
	row, eligible, _ := secondaryEstimateAttempt(attempt)
	if eligible != 255 || len(attemptTemporalConfounds(attempt)) != 0 {
		t.Fatalf("eligible=%d confounds=%v", eligible, attemptTemporalConfounds(attempt))
	}
	want, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(want)); digest != "e00b2fa98b2f11902c68dcf275829cc49fa4c5f7b5c83c61605eadcc23560363" {
		t.Fatalf("secondary result bytes changed: %s", digest)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			got, count, _ := secondaryEstimateAttempt(attempt)
			data, marshalErr := json.Marshal(got)
			if marshalErr != nil || string(data) != string(want) || count != eligible || len(attemptTemporalConfounds(attempt)) != 0 {
				t.Error("concurrent assessment changed result")
			}
		})
	}
	workers.Wait()
	after, err := json.Marshal(attempt.sequence)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("assessment mutated retained sequence")
	}
}

func BenchmarkManyPairSequenceCallers(b *testing.B) {
	attempt := manyPairAttempt()
	for _, caller := range []string{"secondary", "temporal"} {
		b.Run(fmt.Sprintf("%s/256_steps_2826_events", caller), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if caller == "secondary" {
					_, eligible, _ := secondaryEstimateAttempt(attempt)
					if eligible != 255 {
						b.Fatal(eligible)
					}
				} else if got := attemptTemporalConfounds(attempt); len(got) != 0 {
					b.Fatal(got)
				}
			}
		})
	}
}
