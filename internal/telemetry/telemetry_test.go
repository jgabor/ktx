package telemetry

import (
	"errors"
	"testing"
)

func TestValidateObservationRequiresRuntimeIdentity(t *testing.T) {
	valid := Observation{
		Key:            Key{SessionID: "ses_1", MessageID: "msg_1", PartID: "prt_1"},
		Ordinal:        1,
		Reason:         "stop",
		Cost:           measuredCost(0),
		Tokens:         measuredTokens(0, 0, 0, 0, 0),
		Runtime:        "opencode",
		RuntimeVersion: "1.18.4",
		Source:         SourceEventHook,
	}
	if err := ValidateObservation(&valid); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	for _, field := range []struct {
		name  string
		clear func(*Observation)
	}{
		{name: "runtime", clear: func(observation *Observation) { observation.Runtime = "" }},
		{name: "runtime_version", clear: func(observation *Observation) { observation.RuntimeVersion = "" }},
	} {
		t.Run(field.name, func(t *testing.T) {
			observation := valid
			field.clear(&observation)
			if err := ValidateObservation(&observation); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateObservation() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidateObservationPreservesMeasurementStates(t *testing.T) {
	zero := int64(0)
	observation := Observation{
		Key:     Key{SessionID: "ses_1", MessageID: "msg_1", PartID: "prt_1"},
		Ordinal: 1,
		Reason:  "stop",
		Cost:    CostMeasurement{State: MeasurementUnknown},
		Tokens: Tokens{
			Input:      IntegerMeasurement{State: MeasurementMeasured, Value: &zero},
			Output:     IntegerMeasurement{State: MeasurementUnknown},
			Reasoning:  IntegerMeasurement{State: MeasurementMalformed},
			CacheRead:  IntegerMeasurement{State: MeasurementMeasured, Value: &zero},
			CacheWrite: IntegerMeasurement{State: MeasurementUnknown},
		},
		Runtime: "opencode", RuntimeVersion: "1.18.4", Source: SourceWrapperStream,
	}
	if err := ValidateObservation(&observation); err != nil {
		t.Fatalf("valid states rejected: %v", err)
	}
	bad := observation
	bad.Tokens.Output = IntegerMeasurement{State: MeasurementUnknown, Value: &zero}
	if err := ValidateObservation(&bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("state/value mismatch error = %v, want ErrInvalid", err)
	}
}

func measuredTokens(input, output, reasoning, cacheRead, cacheWrite int64) Tokens {
	return Tokens{
		Input: measuredInteger(input), Output: measuredInteger(output), Reasoning: measuredInteger(reasoning),
		CacheRead: measuredInteger(cacheRead), CacheWrite: measuredInteger(cacheWrite),
	}
}

func measuredInteger(value int64) IntegerMeasurement {
	return IntegerMeasurement{State: MeasurementMeasured, Value: &value}
}

func measuredCost(value float64) CostMeasurement {
	return CostMeasurement{State: MeasurementMeasured, Value: &value}
}
