package experiment

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseStreamRetainsOrderedModelSteps(t *testing.T) {
	stream := `{"type":"step_finish","part":{"provider":"openai","model":"gpt","tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":100,"write":500}},"cost":0.01}}
{"type":"step_finish","part":{"provider":"openai","model":"gpt","tokens":{"input":41,"output":2,"reasoning":3,"cache":{"read":15232,"write":1500}},"cost":0.02}}`
	metrics := ParseStream(writeStream(t, stream), nil, nil)
	if err := ValidateSourceEvents(metrics.ModelSteps); err != nil {
		t.Fatalf("ValidateSourceEvents() error = %v", err)
	}
	if len(metrics.ModelSteps) != 2 || metrics.ModelSteps[0].Kind != ModelStep || metrics.ModelSteps[1].Ordinal != 2 {
		t.Fatalf("model steps = %#v", metrics.ModelSteps)
	}
	if metrics.ModelSteps[0].Identity.Provider == nil || *metrics.ModelSteps[0].Identity.Provider != "openai" ||
		metrics.ModelSteps[0].Identity.Model == nil || *metrics.ModelSteps[0].Identity.Model != "gpt" {
		t.Fatalf("model-step identity = %#v", metrics.ModelSteps[0].Identity)
	}
	for _, check := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"uncached_input", metrics.TokensIn, 51},
		{"output", metrics.TokensOut, 4},
		{"reasoning", metrics.Reasoning, 4},
		{"cache_read", metrics.CacheRead, 15332},
		{"cache_write", metrics.CacheWrite, 2000},
		{"cost", metrics.Cost, .03},
	} {
		if check.got == nil || *check.got != check.want {
			t.Errorf("%s total = %v, want %v", check.name, check.got, check.want)
		}
	}
}

func TestModelStepMeasurementStatesAndCompleteness(t *testing.T) {
	fields := []struct {
		name, marker string
		total        func(Metrics) *float64
		measurement  func(SourceEvent) IntegerMeasurement
	}{
		{"uncached_input", "INPUT", func(m Metrics) *float64 { return m.TokensIn }, func(e SourceEvent) IntegerMeasurement { return e.UncachedInput }},
		{"output", "OUTPUT", func(m Metrics) *float64 { return m.TokensOut }, func(e SourceEvent) IntegerMeasurement { return e.Output }},
		{"reasoning", "REASONING", func(m Metrics) *float64 { return m.Reasoning }, func(e SourceEvent) IntegerMeasurement { return e.Reasoning }},
		{"cache_read", "READ", func(m Metrics) *float64 { return m.CacheRead }, func(e SourceEvent) IntegerMeasurement { return e.CacheRead }},
		{"cache_write", "WRITE", func(m Metrics) *float64 { return m.CacheWrite }, func(e SourceEvent) IntegerMeasurement { return e.CacheWrite }},
	}
	for _, field := range fields {
		for _, form := range []string{"missing", "null", "malformed"} {
			t.Run(field.name+"/"+form, func(t *testing.T) {
				metrics := ParseStream(writeStream(t, incompleteStream(field.marker, form)), nil, nil)
				if got := field.total(metrics); got != nil {
					t.Fatalf("total = %v, want nil", *got)
				}
				measurement := field.measurement(metrics.ModelSteps[1])
				wantState := MeasurementUnknown
				if form == "malformed" {
					wantState = MeasurementMalformed
				}
				if measurement.State != wantState || measurement.Value != nil {
					t.Fatalf("second measurement = %#v, want %s without value", measurement, wantState)
				}
			})
		}
	}
	for _, form := range []string{"missing", "null", "malformed"} {
		t.Run("cost/"+form, func(t *testing.T) {
			metrics := ParseStream(writeStream(t, incompleteStream("COST", form)), nil, nil)
			if metrics.Cost != nil {
				t.Fatalf("cost = %v, want nil", *metrics.Cost)
			}
			wantState := MeasurementUnknown
			if form == "malformed" {
				wantState = MeasurementMalformed
			}
			if got := metrics.ModelSteps[1].Cost; got.State != wantState || got.Value != nil {
				t.Fatalf("second cost = %#v, want %s without value", got, wantState)
			}
		})
	}
}

func TestModelStepZeroRoundTrip(t *testing.T) {
	metrics := ParseStream(writeStream(t, `{"type":"step_finish","part":{"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}`), nil, nil)
	dir := t.TempDir()
	attempt := Attempt{SchemaVersion: 2, AttemptID: "zero", Metrics: metrics}
	if err := AppendAttempt(dir, attempt); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || !reflect.DeepEqual(loaded[0].ModelSteps, metrics.ModelSteps) {
		t.Fatalf("reloaded model steps = %#v, want %#v", loaded, metrics.ModelSteps)
	}
	for _, value := range []*float64{loaded[0].TokensIn, loaded[0].TokensOut, loaded[0].Reasoning, loaded[0].CacheRead, loaded[0].CacheWrite, loaded[0].Cost} {
		if value == nil || *value != 0 {
			t.Fatalf("reloaded total = %v, want explicit zero", value)
		}
	}
}

func incompleteStream(marker, form string) string {
	first := `{"type":"step_finish","part":{"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}}`
	second := `{"type":"step_finish","part":{"tokens":{"input":INPUT,"output":OUTPUT,"reasoning":REASONING,"cache":{"read":READ,"write":WRITE}},"cost":COST}}`
	values := map[string]string{"INPUT": "10", "OUTPUT": "2", "REASONING": "3", "READ": "4", "WRITE": "5", "COST": "0.1"}
	for placeholder, value := range values {
		second = strings.ReplaceAll(second, placeholder, value)
	}
	keys := map[string]string{"INPUT": "input", "OUTPUT": "output", "REASONING": "reasoning", "READ": "read", "WRITE": "write", "COST": "cost"}
	key, value := keys[marker], values[marker]
	switch form {
	case "null":
		second = strings.Replace(second, `"`+key+`":`+value, `"`+key+`":null`, 1)
	case "malformed":
		second = strings.Replace(second, `"`+key+`":`+value, `"`+key+`":"bad"`, 1)
	case "missing":
		if marker == "COST" {
			second = strings.Replace(second, `,"cost":`+value, "", 1)
		} else {
			second = strings.Replace(second, `"`+key+`":`+value+`,`, "", 1)
			second = strings.Replace(second, `,"`+key+`":`+value, "", 1)
		}
	}
	return fmt.Sprintf("%s\n%s", first, second)
}

func TestModelStepsValidateAgainstRetainedStream(t *testing.T) {
	for _, test := range []struct {
		name   string
		stream string
		mutate func(*Metrics)
		valid  bool
	}{
		{"complete", incompleteStream("INPUT", "complete"), nil, true},
		{"incomplete", incompleteStream("READ", "malformed"), nil, true},
		{"tampered", incompleteStream("READ", "malformed"), func(metrics *Metrics) { metrics.ModelSteps[1].CacheRead = MeasuredInteger(0) }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			streamPath := filepath.Join(dir, "stream.jsonl")
			if err := os.WriteFile(streamPath, []byte(test.stream), 0o644); err != nil {
				t.Fatal(err)
			}
			metrics := ParseStream(streamPath, nil, nil)
			if test.mutate != nil {
				test.mutate(&metrics)
			}
			manifest := fixtureManifest()
			manifest.ExperimentID = "model-steps"
			manifest.TargetValidPerPipeline = 0
			if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
				t.Fatal(err)
			}
			attempt := Attempt{SchemaVersion: 2, ExperimentID: manifest.ExperimentID, AttemptID: test.name, Side: Baseline, Metrics: metrics, Artifacts: map[string]string{"stream": "stream.jsonl"}}
			if err := AppendAttempt(dir, attempt); err != nil {
				t.Fatal(err)
			}
			if err := WriteJSON(filepath.Join(dir, "summary.json"), BuildSummary(manifest, []Attempt{attempt})); err != nil {
				t.Fatal(err)
			}
			report, err := ValidateExperiment(dir)
			if err != nil {
				t.Fatal(err)
			}
			if report["valid"] != test.valid {
				t.Fatalf("valid = %v, want %v; report=%#v", report["valid"], test.valid, report)
			}
		})
	}
}
