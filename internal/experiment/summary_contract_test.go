package experiment

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeSummaryRejectsMalformedAndConflictingCore(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "malformed", data: `{`, want: "decode summary"},
		{name: "trailing", data: `{} {}`, want: "trailing JSON value"},
		{name: "pipelines type", data: `{"schema_version":8,"pipelines":[]}`, want: "invalid summary pipelines"},
		{name: "conflicting pass rates", data: `{"schema_version":8,"pipelines":{"baseline":{"attempt_count":1,"evaluated_count":1,"valid_count":1,"pass_rate":1,"full_pass_rate":0},"candidate":{"attempt_count":1,"evaluated_count":1,"valid_count":1}}}`, want: "conflicting summary baseline pass rates"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeSummary([]byte(test.data)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			var summary Summary
			if err := json.Unmarshal([]byte(test.data), &summary); err == nil {
				t.Fatal("json.Unmarshal accepted invalid summary")
			}
		})
	}
}

func TestRecoveryAwareUnavailableTargetCompletion(t *testing.T) {
	tests := []struct {
		name                string
		baseline, candidate int
		comparison          string
		valid               bool
	}{
		{"complete target met", 5, 5, ``, true},
		{"incomplete outcomes with implicit target met", 5, 5, `"state":"unavailable","reason":"incomplete_attempt_outcomes"`, true},
		{"incomplete outcomes with explicit target met", 5, 5, `"state":"unavailable","reason":"incomplete_attempt_outcomes","target_completion":{"met":true}`, true},
		{"target unmet complete outcomes", 4, 5, `"state":"unavailable","reason":"target_valid_per_pipeline_unmet","target_completion":{"met":false,"reason":"target_valid_per_pipeline_unmet"}`, true},
		{"target unmet incomplete outcomes", 4, 5, `"state":"unavailable","reason":"incomplete_attempt_outcomes","target_completion":{"met":false,"reason":"target_valid_per_pipeline_unmet"}`, true},
		{"target unmet marked met", 4, 5, `"state":"unavailable","reason":"incomplete_attempt_outcomes","target_completion":{"met":true}`, false},
		{"target met marked unmet", 5, 5, `"state":"unavailable","reason":"incomplete_attempt_outcomes","target_completion":{"met":false,"reason":"target_valid_per_pipeline_unmet"}`, false},
		{"unknown unavailable reason", 5, 5, `"state":"unavailable","reason":"unknown"`, false},
		{"complete state while target unmet", 4, 5, ``, false},
		{"explicit available state", 5, 5, `"state":"available"`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			comparison := test.comparison
			if comparison != "" {
				comparison = "," + comparison
			}
			data := fmt.Sprintf(`{"schema_version":7,"target_valid_per_pipeline":5,"pipelines":{"baseline":{"attempt_count":5,"evaluated_count":5,"valid_count":%d},"candidate":{"attempt_count":5,"evaluated_count":5,"valid_count":%d}},"recovery_aware_comparison":{"arms":{"baseline":{"runtime_total_tokens_per_correct_task":{"state":"available"}},"candidate":{"runtime_total_tokens_per_correct_task":{"state":"available"}}}%s}}`, test.baseline, test.candidate, comparison)
			_, err := DecodeSummary([]byte(data))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t error=%v", test.valid, err)
			}
		})
	}
}
