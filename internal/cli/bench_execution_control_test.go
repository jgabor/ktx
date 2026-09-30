package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
)

func TestBenchExecutionControlCLI(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	pricing := []byte(`{"provider":"openai","model":"gpt-6-luna","price":{"input":1,"output":1,"cache_read":0,"cache_write":0}}`)
	pricePath := filepath.Join(tmp, "pricing.json")
	if err := os.WriteFile(pricePath, pricing, 0o600); err != nil {
		t.Fatal(err)
	}
	// This executable can only attest a fake image. Any non-preflight run fails.
	docker := filepath.Join(tmp, "docker")
	capabilities := `{"ktx_version":"test","opencode_version":"` + benchcontract.RuntimeVersion + `","generated_plugin_sha256":"` + strings.Repeat("a", 64) + `","generated_plugin_matches_binary":true,"event_collector_contract":"` + benchcontract.ContractVersion + `","explicit_session_ownership":true,"serialized_callbacks":true,"complete_interval_lifecycle":true,"epoch_tracking":true,"strict_acknowledgements":true,"exact_model_step_identity":true,"retained_duplicate_callbacks":true,"accepted_model_step_projection":true,"callback_queue_continuation":true,"fail_closed_interval_validation":true,"preflight_before_auth":true,"normalized_kinds":["collector_open","model_step","retry","compaction","interruption","other"],"read_description_treatment":"` + benchcontract.ReadDescriptionTreatment + `","ktx_binary_sha256":"` + strings.Repeat("c", 64) + `"}`
	script := "#!/bin/sh\nset -eu\ncase \"$1\" in\nimage) printf '%s\\n' 'sha256:" + strings.Repeat("a", 64) + "';;\nrm) exit 0;;\nrun) case \" $* \" in *' --capabilities '*) printf '%s\\n' '" + capabilities + "';; *) exit 99;; esac;;\n*) exit 99;;\nesac\n"
	if err := os.WriteFile(docker, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_BIN", docker)
	base := []string{"compare", "--preflight", "--repo-root", root, "--spec", "exact-match", "--task", "read-config-first", "--model", "openai/gpt-6-luna", "--variant", "high", "--baseline", "passthrough", "--candidate", "passthrough", "--read-description-treatment", benchcontract.ReadDescriptionTreatment, "--runtime-config", filepath.Join(root, "docs/normalization-model-registration.json"), "--pricing-snapshot", pricePath, "--pricing-sha256", fmt.Sprintf("%x", sha256.Sum256(pricing)), "--cache-control", "observed", "--target-valid", "1", "--max-attempts", "2"}
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"candidate-first", []string{"--first-arm", "candidate", "--estimated-cost-limit", "0.5"}, ExitOK},
		{"baseline-first", []string{"--first-arm", "baseline", "--estimated-cost-limit", "0.5"}, ExitOK},
		{"limit-only", []string{"--estimated-cost-limit", "0.5"}, ExitOK},
		{"invalid-first", []string{"--first-arm", "random"}, ExitUsage},
		{"empty-first", []string{"--first-arm="}, ExitUsage},
		{"zero", []string{"--estimated-cost-limit", "0"}, ExitUsage},
		{"negative", []string{"--estimated-cost-limit", "-1"}, ExitUsage},
		{"nan", []string{"--estimated-cost-limit", "NaN"}, ExitUsage},
		{"infinite", []string{"--estimated-cost-limit", "+Inf"}, ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append(append([]string(nil), base...), tc.args...)
			if code := runBenchWith(args, Options{Format: FormatJSON}, &stdout, &stderr); code != tc.want {
				t.Fatalf("code=%d want=%d stdout=%s stderr=%s", code, tc.want, stdout.String(), stderr.String())
			}
		})
	}
	for _, flag := range []string{"--first-arm=candidate", "--estimated-cost-limit=0.5"} {
		var stdout, stderr bytes.Buffer
		if code := runBenchWith([]string{"rank", flag}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitUsage || !strings.Contains(stderr.String(), "flag provided but not defined") {
			t.Fatalf("rank accepted compare-only control: code=%d stderr=%s", code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runBenchWith([]string{"compare", "--help"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK || !strings.Contains(stdout.String(), "--first-arm") || !strings.Contains(stdout.String(), "--estimated-cost-limit") {
		t.Fatal("focused help omits execution controls")
	}
}
