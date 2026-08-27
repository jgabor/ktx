package experiment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

// Summary is the serialized experiment-summary contract. Lower-level retained
// artifacts remain authoritative; this value is only their projection.
type Summary map[string]any

func DecodeSummary(data []byte) (Summary, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode summary: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("decode summary: trailing JSON value")
	}
	summary := Summary(value)
	if summary == nil {
		return nil, fmt.Errorf("decode summary: expected JSON object")
	}
	if err := summary.Validate(); err != nil {
		return nil, err
	}
	return summary, nil
}

func (summary *Summary) UnmarshalJSON(data []byte) error {
	decoded, err := DecodeSummary(data)
	if err != nil {
		return err
	}
	*summary = decoded
	return nil
}

func (summary Summary) Validate() error {
	if raw, exists := summary["schema_version"]; exists {
		if _, ok := numberAsInt(raw); !ok {
			return fmt.Errorf("invalid summary schema_version")
		}
	}
	rawPipelines, exists := summary["pipelines"]
	if !exists {
		return nil
	}
	pipelines, ok := rawPipelines.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid summary pipelines")
	}
	for _, side := range Sides {
		pipeline, ok := pipelines[side].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid summary pipeline %q", side)
		}
		for _, field := range []string{"attempt_count", "evaluated_count", "valid_count"} {
			if _, ok := numberAsInt(pipeline[field]); !ok {
				return fmt.Errorf("invalid summary %s.%s", side, field)
			}
		}
		if pass, exists := pipeline["pass_rate"]; exists && !reflect.DeepEqual(pass, pipeline["full_pass_rate"]) {
			return fmt.Errorf("conflicting summary %s pass rates", side)
		}
	}
	if summary.SchemaVersion() == 6 || summary.SchemaVersion() == 7 {
		comparison, ok := summary["recovery_aware_comparison"].(map[string]any)
		if !ok {
			return fmt.Errorf("summary schema %d is missing recovery_aware_comparison", summary.SchemaVersion())
		}
		arms, ok := comparison["arms"].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid recovery-aware arms")
		}
		for _, side := range Sides {
			arm, ok := arms[side].(map[string]any)
			if !ok {
				return fmt.Errorf("invalid recovery-aware arm %q", side)
			}
			primary, ok := arm["runtime_total_tokens_per_correct_task"].(map[string]any)
			if !ok || (primary["state"] != "available" && primary["state"] != "unavailable") {
				return fmt.Errorf("invalid recovery-aware primary outcome %q", side)
			}
		}
		if comparison["state"] == "unavailable" {
			if reason, ok := comparison["reason"].(string); !ok || reason == "" {
				return fmt.Errorf("invalid recovery-aware unavailable reason")
			}
			completion, ok := comparison["target_completion"].(map[string]any)
			if !ok || completion["met"] != false {
				return fmt.Errorf("invalid recovery-aware target completion")
			}
			if reason, ok := completion["reason"].(string); !ok || reason == "" {
				return fmt.Errorf("invalid recovery-aware target completion reason")
			}
		}
	}
	return nil
}

func (summary Summary) Pipeline(side string) (map[string]any, bool) {
	pipelines, ok := summary["pipelines"].(map[string]any)
	if !ok {
		return nil, false
	}
	pipeline, ok := pipelines[side].(map[string]any)
	return pipeline, ok
}

func (summary Summary) SchemaVersion() int {
	version, _ := numberAsInt(summary["schema_version"])
	return version
}

func numberAsInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		integer := int(number)
		return integer, number == float64(integer)
	default:
		return 0, false
	}
}
