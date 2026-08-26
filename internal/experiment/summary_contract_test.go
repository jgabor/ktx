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
