package experiment

import (
	"encoding/json"
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
		{name: "pipelines type", data: `{"schema_version":1,"pipelines":[]}`, want: "invalid summary pipelines"},
		{name: "conflicting pass rates", data: `{"schema_version":1,"pipelines":{"baseline":{"attempt_count":1,"evaluated_count":1,"valid_count":1,"pass_rate":1,"full_pass_rate":0},"candidate":{"attempt_count":1,"evaluated_count":1,"valid_count":1}}}`, want: "conflicting summary baseline pass rates"},
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
	base := `"pipelines":{"baseline":{"attempt_count":5,"evaluated_count":5,"valid_count":5},"candidate":{"attempt_count":5,"evaluated_count":5,"valid_count":5}},"recovery_aware_comparison":{"arms":{"baseline":{"runtime_total_tokens_per_correct_task":{"state":"available"}},"candidate":{"runtime_total_tokens_per_correct_task":{"state":"available"}}},`
	tests := []struct {
		name, suffix string
		valid        bool
	}{
		{"incomplete outcomes with implicit target met", `"state":"unavailable","reason":"incomplete_attempt_outcomes"}`, true},
		{"incomplete outcomes with explicit target met", `"state":"unavailable","reason":"incomplete_attempt_outcomes","target_completion":{"met":true}}`, true},
		{"target unmet", `"state":"unavailable","reason":"target_valid_per_pipeline_unmet","target_completion":{"met":false,"reason":"target_valid_per_pipeline_unmet"}}`, true},
		{"target unmet marked met", `"state":"unavailable","reason":"target_valid_per_pipeline_unmet","target_completion":{"met":true}}`, false},
		{"incomplete outcomes marked unmet", `"state":"unavailable","reason":"incomplete_attempt_outcomes","target_completion":{"met":false,"reason":"target_valid_per_pipeline_unmet"}}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeSummary([]byte(`{"schema_version":7,` + base + test.suffix + `}`))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%t error=%v", test.valid, err)
			}
		})
	}
}
