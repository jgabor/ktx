package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/experiment"
)

func TestBenchReportCurrentCostOwnedRows(t *testing.T) {
	ptr := func(value float64) *float64 { return &value }
	for _, tc := range []struct {
		name, basis, reason string
		costs               []*float64
	}{
		{"mixed zero positive", "reported", "reported_complete_positive", []*float64{ptr(0), ptr(2)}},
		{"positive subtotal missing row", "unavailable", "reported_incomplete", []*float64{ptr(9), nil}},
		{"uniform zero", "unavailable", "reported_uniform_zero", []*float64{ptr(0), ptr(0)}},
		{"missing reports stored estimates", "unavailable", "reported_incomplete", []*float64{nil, nil}},
		{"invalid reports", "unavailable", "reported_invalid", []*float64{ptr(-1), ptr(2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owned.db")
			d, err := dbpkg.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			rows := []dbpkg.BenchRun{}
			for i, id := range []string{"ownedbaseline", "ownedcandidate"} {
				row := dbpkg.BenchRun{ID: id, Agent: "offline", Model: "fixture", Task: "fixture", Score: float64(i), Cost: tc.costs[i], EstimatedAPICost: ptr(.25), MethodologyVersion: "v8-exact-match", Timestamp: int64(i + 1)}
				if err := d.InsertBenchRun(&row); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, row)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			for _, format := range []string{FormatJSON, FormatText} {
				root := t.TempDir()
				for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
					t.Setenv(key, filepath.Join(root, key))
				}
				t.Setenv("KTX_DB", path) // Explicitly seeded disposable owner, never ambient.
				var stdout, stderr bytes.Buffer
				args := []string{"--format", format}
				if code := runBenchReport(args, Options{Format: format}, &stdout, &stderr); code != ExitOK {
					t.Fatalf("code=%d stderr=%s", code, stderr.String())
				}
				if format == FormatJSON {
					var envelope struct {
						Headline  *float64                 `json:"headline"`
						Rows      []dbpkg.BenchRun         `json:"rows"`
						Selection experiment.CostSelection `json:"cost_selection"`
						Selected  *float64                 `json:"selected_cost_per_correct"`
					}
					if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
						t.Fatal(err)
					}
					if envelope.Selection.Basis != tc.basis || envelope.Selection.Reason != tc.reason || envelope.Selection.RequiredCount != 2 || envelope.Selection.EstimatedEligible != 0 {
						t.Fatalf("envelope=%+v", envelope)
					}
					if tc.basis == "unavailable" && envelope.Selected != nil {
						t.Fatal("partial subtotal became selected cost")
					}
					if tc.basis == "reported" && (envelope.Selected == nil || *envelope.Selected != 2) {
						t.Fatalf("selected=%v", envelope.Selected)
					}
					for i, row := range envelope.Rows {
						if row.EstimatedAPICost == nil || *row.EstimatedAPICost != .25 || (row.Cost == nil) != (rows[i].Cost == nil) {
							t.Fatal("stored fields changed")
						}
					}
				} else if !strings.Contains(stdout.String(), "basis="+tc.basis) || !strings.Contains(stdout.String(), "required=2") || !strings.Contains(stdout.String(), "source=bench_runs.cost") || !strings.Contains(stdout.String(), "estimated_api_cost=0.250000") {
					t.Fatalf("text=%s", stdout.String())
				}
				if binary := os.Getenv("KTX_ENTRY_CONTRACT_BINARY"); binary != "" {
					root := t.TempDir()
					for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
						t.Setenv(key, filepath.Join(root, key))
					}
					cmd := exec.Command(binary, append([]string{"bench", "report"}, args...)...)
					out, err := cmd.CombinedOutput()
					if err != nil || !bytes.Equal(out, stdout.Bytes()) {
						t.Fatalf("built report: %v\n%s\nwant %s", err, out, stdout.Bytes())
					}
				}
			}
			var stdout, stderr bytes.Buffer
			if code := runBenchReport([]string{"--current-cost", "ownedcandidate"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK {
				t.Fatalf("single code=%d stderr=%s", code, stderr.String())
			}
			var single map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &single); err != nil || single["row"] == nil || single["cost_selection"] == nil {
				t.Fatalf("single view: %v %s", err, stdout.String())
			}
			stdout.Reset()
			if code := runBenchReport([]string{"ownedcandidate"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK {
				t.Fatal(code)
			}
			if err := json.Unmarshal(stdout.Bytes(), &single); err != nil {
				t.Fatal(err)
			}
			var raw dbpkg.BenchRun
			if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil || raw.ID != "ownedcandidate" {
				t.Fatalf("legacy row changed: %v %s", err, stdout.String())
			}
		})
	}
}
