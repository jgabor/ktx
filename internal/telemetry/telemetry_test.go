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
		Tokens:         Tokens{},
		Runtime:        "opencode",
		RuntimeVersion: "1.18.4",
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
