package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/config"
)

func TestBenchScreenFrozenMatrix(t *testing.T) {
	root := filepath.Join("..", "..")
	pipeline := func(name string) string { return filepath.Join(root, "tools", "bench", "screen-configs", name) }
	args := []string{
		"--population", filepath.Join(root, "tools", "bench", "evidence", "configuration-screen-population-v1.json"),
		"--config", filepath.Join(root, "tools", "bench", "pipelines", "passthrough.toml"),
		"--config", pipeline("keep-ends-50-10.toml"),
		"--config", pipeline("keep-ends-73-1.toml"),
		"--config", pipeline("keep-ends-100-10.toml"),
		"--anchor-config", pipeline("keep-ends-100-50.toml"),
		"--format", "json",
	}
	var stdout, stderr bytes.Buffer
	if code := runBenchScreen(args, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("runBenchScreen code=%d stderr=%s", code, stderr.String())
	}
	for _, forbidden := range []string{"HEAD_REQUIRED", "TAIL_REQUIRED", "ERROR_REQUIRED", "cat opaque-source", "/opaque/source", "configuration-screen-population-v1.json", "screen-keep-ends"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("output exposed %q", forbidden)
		}
	}
	var got struct {
		Calls          int            `json:"calls"`
		Configurations []screenResult `json:"configurations"`
		Advanced       []string       `json:"advanced_config_ids"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []screenResult{
		{ConfigID: "c2c96472876aee9f", Calls: 4, RawBytes: 28672, DeliveredBytes: 28672, NetBytesSaved: 0, RawEstimatedTokens: 7168, DeliveredEstimatedTokens: 7168, Dispositions: map[string]int{"anti_worsening_rejected": 0, "intentional_passthrough": 4, "transformed": 0}, ControlledFactsPass: true},
		{ConfigID: "b578fe9165e1b564", Calls: 4, RawBytes: 28672, DeliveredBytes: 16003, NetBytesSaved: 12669, RawEstimatedTokens: 7168, DeliveredEstimatedTokens: 4001, Dispositions: map[string]int{"anti_worsening_rejected": 0, "intentional_passthrough": 1, "transformed": 3}, RecoveryCueCalls: 3, RecoveryFooterBytes: 316, ControlledFactsPass: false},
		{ConfigID: "8bf31632404adcd2", Calls: 4, RawBytes: 28672, DeliveredBytes: 17155, NetBytesSaved: 11517, RawEstimatedTokens: 7168, DeliveredEstimatedTokens: 4289, Dispositions: map[string]int{"anti_worsening_rejected": 0, "intentional_passthrough": 3, "transformed": 1}, RecoveryCueCalls: 1, RecoveryFooterBytes: 106, ControlledFactsPass: true},
		{ConfigID: "2ac5f3fafdfcf732", Calls: 4, RawBytes: 28672, DeliveredBytes: 19459, NetBytesSaved: 9213, RawEstimatedTokens: 7168, DeliveredEstimatedTokens: 4865, Dispositions: map[string]int{"anti_worsening_rejected": 0, "intentional_passthrough": 3, "transformed": 1}, RecoveryCueCalls: 1, RecoveryFooterBytes: 106, ControlledFactsPass: true},
		{ConfigID: "bbf0c6277e3360df", Calls: 4, RawBytes: 28672, DeliveredBytes: 22019, NetBytesSaved: 6653, RawEstimatedTokens: 7168, DeliveredEstimatedTokens: 5505, Dispositions: map[string]int{"anti_worsening_rejected": 0, "intentional_passthrough": 3, "transformed": 1}, RecoveryCueCalls: 1, RecoveryFooterBytes: 106, ControlledFactsPass: true},
	}
	if got.Calls != 4 || !reflect.DeepEqual(got.Configurations, want) {
		t.Fatalf("screen result mismatch: %#v", got)
	}
	if wantAdvanced := []string{"bbf0c6277e3360df", "8bf31632404adcd2", "2ac5f3fafdfcf732"}; !reflect.DeepEqual(got.Advanced, wantAdvanced) {
		t.Fatalf("advanced=%v want=%v", got.Advanced, wantAdvanced)
	}
}

func TestScreenRoutesWithCallInput(t *testing.T) {
	success := false
	output := bytes.Repeat([]byte("line\n"), 200)
	for _, tt := range []struct {
		name  string
		input string
		route config.RouteRule
	}{
		{name: "command prefix", input: `{"command":"cat file"}`, route: config.RouteRule{CommandPrefix: "cat ", ExitStatus: "success", Profile: "matched"}},
		{name: "subcommand", input: `{"command":"git --no-pager status"}`, route: config.RouteRule{Subcommand: "status", ExitStatus: "success", Profile: "matched"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var c screenCase
			if err := json.Unmarshal([]byte(`{"tool":"bash","input":`+tt.input+`}`), &c); err != nil {
				t.Fatal(err)
			}
			c.Expected.Errors.IsError = &success
			cfg := &config.Config{
				Profiles: map[string]config.Profile{
					"default": {},
					"matched": {Stages: []string{"keep-ends:1:1"}},
				},
				Routes: []config.RouteRule{tt.route},
			}
			got := evaluateScreenConfiguration("opaque", []screenCase{c}, [][]byte{output}, cfg)
			if got.Dispositions["transformed"] != 1 || got.NetBytesSaved <= 0 {
				t.Fatalf("route did not match production input: %#v", got)
			}
		})
	}
}
