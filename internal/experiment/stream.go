package experiment

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/jgabor/ktx/internal/cache/reuse/sequence"
	"github.com/jgabor/ktx/internal/usage"
)

func ParseStream(path string, expectedOutput *string, expectedFacts []string) Metrics {
	return parseStream(path, expectedOutput, expectedFacts, false)
}

func parseSchema3Stream(path string, expectedOutput *string, expectedFacts []string) Metrics {
	return parseStream(path, expectedOutput, expectedFacts, true)
}

func parseStream(path string, expectedOutput *string, expectedFacts []string, retainModelIdentity bool) Metrics {
	metrics := Metrics{Score: Score{FactsMatched: []string{}, FactsMissed: []string{}}}
	file, err := os.Open(path)
	if err != nil {
		return metrics
	}
	defer file.Close()
	// Usage-evidence aggregation for step_finish events is shared via
	// the usage package so the live benchmark path and this experiment
	// path apply one interpretation to the same recorded stream. The
	// Aggregator is driven inline so the stream is read exactly once:
	// the same scan also collects tool_use counts, the answer text,
	// and the stream-complete marker, which stay here because they are
	// not usage evidence.
	agg := &usage.Aggregator{}
	lastText := ""
	var streamOrdinal, modelStepOrdinal uint64
	sequenceFacts := []sequence.SequenceFact{}
	wrapperIdentities := []modelStepIdentity{}
	usage.ReadDelimitedLines(file, usage.MaxRecordSize, func(line []byte) bool {
		streamOrdinal++
		var event struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			Part    json.RawMessage `json:"part"`
		}
		if json.Unmarshal(line, &event) != nil {
			return false // skip malformed line (preserves scoring/status)
		}
		switch event.Type {
		case "tool_use":
			metrics.ToolCallCount++
		case "step_finish":
			if !agg.AddLine(line) {
				return false
			}
			modelStepOrdinal++
			if retainModelIdentity {
				var identity struct {
					SessionID string `json:"sessionID"`
					MessageID string `json:"messageID"`
					PartID    string `json:"id"`
				}
				_ = json.Unmarshal(event.Part, &identity)
				wrapperIdentities = append(wrapperIdentities, modelStepIdentity{SessionID: identity.SessionID, MessageID: identity.MessageID, PartID: identity.PartID})
			}
			if fact, ok := sequence.DecodeOpenCodeSequenceFact(line, "wrapper_stream", streamOrdinal, modelStepOrdinal); ok {
				sequenceFacts = append(sequenceFacts, fact)
			}
			var part struct {
				Reason string `json:"reason"`
			}
			if json.Unmarshal(event.Part, &part) == nil && part.Reason == "stop" {
				metrics.StreamComplete = true
			}
		case "text":
			// Text stays strict so malformed text metadata cannot replace a
			// valid answer. Step telemetry follows the independent usage
			// classification above instead.
			var textEvent struct {
				Content json.RawMessage `json:"content"`
				Part    struct {
					Text    string   `json:"text"`
					Content string   `json:"content"`
					Cost    *float64 `json:"cost"`
				} `json:"part"`
			}
			if json.Unmarshal(line, &textEvent) != nil {
				return false
			}
			switch {
			case textEvent.Part.Text != "":
				lastText = textEvent.Part.Text
			case textEvent.Part.Content != "":
				lastText = textEvent.Part.Content
			default:
				var content string
				if json.Unmarshal(textEvent.Content, &content) == nil {
					lastText = content
				}
			}
		}
		if event.Type == "error" {
			if fact, ok := sequence.DecodeOpenCodeSequenceFact(line, "wrapper_stream", streamOrdinal, modelStepOrdinal); ok {
				sequenceFacts = append(sequenceFacts, fact)
			}
		}
		return false
	})
	metrics.Score = ScoreAnswer(lastText, expectedOutput, expectedFacts)
	answer := strings.TrimSpace(lastText)
	if len(answer) > 500 {
		answer = answer[:500]
	}
	metrics.AnswerExcerpt = answer
	ev := agg.Result()
	metrics.ModelSteps = sourceEvents(ev.ModelSteps, wrapperIdentities, retainModelIdentity)
	metrics.wrapperModelIdentities = wrapperIdentities
	metrics.sequence = wrapperAttemptSequence(sequenceFacts, len(metrics.ModelSteps))
	metrics.UsageEvidence.StepFinishEvents = ev.Steps
	metrics.UsageEvidence.TokensInEvents = ev.TokensInEvents
	metrics.UsageEvidence.TokensOutEvents = ev.TokensOutEvents
	metrics.UsageEvidence.CostEvents = ev.CostEvents
	metrics.UsageEvidence.CacheReadEvents = ev.CacheReadEvents
	metrics.UsageEvidence.CacheWriteEvents = ev.CacheWriteEvents
	metrics.UsageEvidence.ReasoningEvents = ev.ReasoningEvents
	// Integer evidence is shared as exact *int64. The experiment's
	// Metrics uses *float64 for JSON serialization; the conversion is
	// exact for every accepted value (the aggregator rejects fractional
	// and precision-losing values above 2^53 from completeness).
	metrics.TokensIn = int64ToFloat64(ev.TokensIn)
	metrics.TokensOut = int64ToFloat64(ev.TokensOut)
	metrics.Cost = ev.Cost
	metrics.CacheRead = int64ToFloat64(ev.CacheRead)
	metrics.CacheWrite = int64ToFloat64(ev.CacheWrite)
	metrics.Reasoning = int64ToFloat64(ev.Reasoning)
	return metrics
}

func sourceEvents(steps []usage.ModelStep, identities []modelStepIdentity, retainIdentity bool) []SourceEvent {
	if len(steps) == 0 {
		return nil
	}
	events := make([]SourceEvent, len(steps))
	for i, step := range steps {
		identity := EventIdentity{Provider: step.Provider, Model: step.Model}
		if retainIdentity && i < len(identities) {
			identity.SessionID = nonblankPointer(identities[i].SessionID)
			identity.MessageID = nonblankPointer(identities[i].MessageID)
			identity.PartID = nonblankPointer(identities[i].PartID)
		}
		events[i] = SourceEvent{
			Ordinal: step.Ordinal, Kind: ModelStep,
			UncachedInput: integerSourceMeasurement(step.UncachedInput),
			Output:        integerSourceMeasurement(step.Output),
			CacheRead:     integerSourceMeasurement(step.CacheRead),
			CacheWrite:    integerSourceMeasurement(step.CacheWrite),
			Reasoning:     integerSourceMeasurement(step.Reasoning),
			Cost:          costSourceMeasurement(step.Cost),
			Identity:      identity,
		}
	}
	return events
}

func nonblankPointer(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func integerSourceMeasurement(measurement usage.IntegerMeasurement) IntegerMeasurement {
	return IntegerMeasurement{State: MeasurementState(measurement.State), Value: measurement.Value}
}

func costSourceMeasurement(measurement usage.CostMeasurement) CostMeasurement {
	return CostMeasurement{State: MeasurementState(measurement.State), Value: measurement.Value}
}

// int64ToFloat64 converts an exact-int64 evidence pointer to the
// *float64 type the experiment's Metrics uses for JSON serialization.
// The conversion is exact for all values the aggregator accepts
// (within [-2^53, 2^53]). nil passes through as nil.
func int64ToFloat64(v *int64) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v)
	return &f
}

func AttemptStatus(exitCode int, newSessionIDs []string, metrics Metrics) string {
	if exitCode == 124 || exitCode == 137 {
		return "timeout"
	}
	if exitCode != 0 {
		return "failed"
	}
	if len(newSessionIDs) != 1 {
		return "unlinked"
	}
	if !metrics.StreamComplete {
		return "invalid"
	}
	return "completed"
}
