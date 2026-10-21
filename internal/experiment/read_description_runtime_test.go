package experiment

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/db"
)

//go:embed testdata/read-runtime-probe.mjs
var readDescriptionRuntimeProbe []byte

type readRuntimeProbeResult struct {
	PluginSHA256     string           `json:"pluginSHA256"`
	Tools            []map[string]any `json:"tools"`
	Session          string           `json:"session"`
	Turns            []string         `json:"turns"`
	Requests         int              `json:"requests"`
	DeliveredOutputs []string         `json:"deliveredOutputs"`
	GuardError       string           `json:"guardError"`
	GuardDiagnostic  string           `json:"guardDiagnostic"`
	HookOrder        []struct {
		Stage, Tool, Before, After, ParametersBefore, ParametersAfter string
		KTXOrdinal                                                    *int
	} `json:"hookOrder"`
	Messages []struct {
		Info struct {
			SessionID, Role string
			Error           json.RawMessage
		}
		Parts []struct {
			Type, Tool, Text, SessionID string
			State                       struct {
				Status, Output string
				Input          json.RawMessage
			}
		}
	} `json:"messages"`
}

// Opt-in native Docker gate. Only public dependency setup has network access.
// A loopback synthetic model drives the real read tool; no live model semantics
// or provider-final schema claim follows from these deterministic observations.
func TestReadDescriptionRuntimeBoundary(t *testing.T) {
	image := os.Getenv("KTX_READ_DESCRIPTION_PROBE_IMAGE")
	if image == "" {
		t.Skip("set KTX_READ_DESCRIPTION_PROBE_IMAGE to the pinned benchmark image")
	}
	binary, err := filepath.Abs("../../build/ktx")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"original", "candidate"} {
		data, err := os.ReadFile("../../docs/reports/normalization/normalization-read-" + name + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		write(name+".txt", data)
	}
	write("probe.mjs", readDescriptionRuntimeProbe)
	write("passthrough.toml", []byte("[pipeline.default]\nstages = []\n"))
	workspace := filepath.Join(root, "workspace")
	if _, err := prepareWorkspace("../..", workspace, "corpus", []string{"window-a.txt", "window-b.txt"}); err != nil {
		t.Fatal(err)
	}
	// prepareWorkspace uses private owner modes; the disposable container's node
	// user needs traversal/read access to this public, read-only corpus only.
	for _, path := range []string{workspace, filepath.Join(workspace, "tools"), filepath.Join(workspace, "tools/bench"), filepath.Join(workspace, "tools/bench/corpus")} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"window-a.txt", "window-b.txt"} {
		if err := os.Chmod(filepath.Join(workspace, "tools/bench/corpus", file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	name := fmt.Sprintf("ktx-read-probe-%d", os.Getpid())
	docker := func(t *testing.T) func(...string) []byte {
		return func(args ...string) []byte {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("docker %v: %v\n%s", args, err, out)
			}
			return out
		}
	}
	run := docker(t)
	run("run", "--rm", "-d", "--name", name, "--entrypoint", "sleep",
		"-v", binary+":/usr/local/bin/ktx:ro", "-v", root+":"+benchcontract.ReadDescriptionDir+":rw",
		"-v", workspace+":/work:ro", "-v", filepath.Join(root, "passthrough.toml")+":/home/node/.config/ktx/ktx.toml:ro",
		"-e", "HOME=/home/node", "-e", "XDG_DATA_HOME=/home/node/.local/share", "-e", "XDG_CONFIG_HOME=/home/node/.config",
		"-e", "XDG_CACHE_HOME=/home/node/.cache", "-e", "XDG_STATE_HOME=/home/node/.local/state",
		"-e", "OPENCODE_DISABLE_MODELS_FETCH=true", "-e", "OPENCODE_DISABLE_AUTOUPDATE=true", image, "900")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Errorf("remove owned probe container: %v: %s", err, out)
		}
	})
	run("exec", "-w", "/home/node/.config/opencode", name, "npm", "install", "--ignore-scripts", "--no-audit", "--no-fund", "@opencode-ai/plugin@1.18.5")
	run("network", "disconnect", "bridge", name)
	if networks := strings.TrimSpace(string(run("inspect", "--format", "{{json .NetworkSettings.Networks}}", name))); networks != "{}" {
		t.Fatalf("container still has external networks: %s", networks)
	}
	run("exec", name, "ktx", "init", "opencode")
	pluginHash := strings.Fields(string(run("exec", name, "sha256sum", "/home/node/.config/opencode/plugins/ktx.ts")))[0]
	version := strings.TrimSpace(string(run("exec", name, "opencode", "--version")))
	if version != "1.18.5" {
		t.Fatalf("runtime=%q", version)
	}
	t.Logf("image=%s binary=%s plugin=%s runtime=%s network=disconnected credentials=none", strings.TrimSpace(string(run("inspect", "--format", "{{.Image}}", name))), strings.Fields(string(run("exec", name, "sha256sum", "/usr/local/bin/ktx")))[0], pluginHash, version)
	var baseline []map[string]any
	var baselineOutputs []string
	for i, tc := range []struct{ side, mode string }{
		{Baseline, "normal"},
		{Candidate, "normal"},
		{Candidate, "normal"},
		{Candidate, "description"},
		{Candidate, "schema"},
		{Candidate, "wrong-limit"},
		{Candidate, "fixture"},
	} {
		t.Run(fmt.Sprintf("%d-%s-%s", i, tc.side, tc.mode), func(t *testing.T) {
			run := docker(t)
			for _, file := range []string{"hooks.jsonl", "order.jsonl"} {
				if err := os.Remove(filepath.Join(root, file)); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			if tc.mode == "fixture" {
				data, err := os.ReadFile(filepath.Join(root, "candidate.txt"))
				if err != nil {
					t.Fatal(err)
				}
				write("candidate.txt", append(data, []byte("changed removal set fixture\n")...))
				got := strings.Fields(string(run("exec", name, "sha256sum", benchcontract.ReadDescriptionDir+"/candidate.txt")))[0]
				if got == benchcontract.ReadDescriptionCandidateSHA256 {
					t.Fatal("changed fixture not mounted")
				}
			}
			databaseName := fmt.Sprintf("probe-%d.db", i)
			database, err := db.Open(filepath.Join(root, databaseName))
			if err != nil {
				t.Fatal(err)
			}
			interval, err := database.CreateBenchmarkEventInterval(fmt.Sprintf("synthetic-probe-%d", i), benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, time.Now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.EnsureCollectionInterval(interval.ID, []string{"run"}, interval.OpenedAt); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(root, databaseName), 0o666); err != nil {
				t.Fatal(err)
			}
			out := run("exec", "-e", "KTX_PROBE_DIRECTORY="+benchcontract.ReadDescriptionDir, "-e", "KTX_PROBE_MODE="+tc.mode,
				"-e", benchcontract.ReadDescriptionEnv+"="+tc.side, "-e", benchcontract.SessionEnv+"="+interval.SessionID,
				"-e", benchcontract.IntervalEnv+"="+interval.ID, "-e", benchcontract.ContractEnv+"="+benchcontract.ContractVersion,
				"-e", benchcontract.CollectorModeEnv+"=required", "-e", "KTX_DB="+benchcontract.ReadDescriptionDir+"/"+databaseName,
				name, "node", benchcontract.ReadDescriptionDir+"/probe.mjs")
			var result readRuntimeProbeResult
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if result.PluginSHA256 != pluginHash {
				t.Fatal("generated collector changed")
			}
			hooks, err := os.ReadFile(filepath.Join(root, "hooks.jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			readCount := 0
			for _, line := range strings.Split(strings.TrimSpace(string(hooks)), "\n") {
				if line == "" {
					continue
				}
				var r readDefinitionObservation
				if err := json.Unmarshal([]byte(line), &r); err != nil {
					t.Fatal(err)
				}
				if r.Owner != interval.SessionID || r.Interval != interval.ID || r.Side != tc.side || !r.ParametersUnchanged || !r.JSONSchemaUnchanged {
					t.Fatal("hook provenance mismatch")
				}
				if r.Tool == "read" {
					readCount++
					if r.BeforeSHA256 != benchcontract.ReadDescriptionOriginalSHA256 || r.ParametersSHA256 != benchcontract.ReadParametersASTSHA256 {
						t.Fatal("hook ingress drift")
					}
				}
			}
			order, _ := json.Marshal(result.HookOrder)
			t.Logf("mode=%s arm=%s read_applications=%d requests=%d hook_order=%s", tc.mode, tc.side, readCount, result.Requests, order)
			if tc.mode == "description" || tc.mode == "schema" {
				if readCount != 0 || result.Requests != 0 || !strings.Contains(result.GuardError, "500") || result.GuardDiagnostic != "read-description source or parameter schema changed" || len(result.HookOrder) != 1 {
					t.Fatalf("changed ingress not rejected: %s", out)
				}
				o := result.HookOrder[0]
				if o.Stage != "before-ktx" || o.Before != benchcontract.ReadDescriptionOriginalSHA256 || o.ParametersBefore != benchcontract.ReadParametersASTSHA256 || (tc.mode == "description" && (o.After == o.Before || o.ParametersAfter != o.ParametersBefore)) || (tc.mode == "schema" && (o.ParametersAfter == o.ParametersBefore || o.After != o.Before)) {
					t.Fatal("test hook did not change the declared read ingress")
				}
				t.Logf("actual KTX guard rejected ingress: %s; %s", result.GuardError, result.GuardDiagnostic)
				return
			}
			if tc.mode == "fixture" {
				if len(hooks) != 0 || result.Requests != 0 || result.GuardError != "read-description fixture identity mismatch" {
					t.Fatalf("stale fixture not rejected: %s", out)
				}
				t.Log("mounted transform fixture identity rejected; no valid treatment evidence (not an upstream reload test)")
				return
			}
			expected := benchcontract.ReadDescriptionOriginalSHA256
			if tc.side == Candidate {
				expected = benchcontract.ReadDescriptionCandidateSHA256
			}
			readFound := false
			for _, tool := range result.Tools {
				if tool["id"] == "read" {
					readFound = true
					if sha256Bytes([]byte(tool["description"].(string))) != expected {
						t.Fatal("runtime returned wrong description")
					}
					tool["description"] = "compared separately"
				}
			}
			if !readFound || readCount == 0 || len(result.HookOrder) != 2*readCount {
				t.Fatal("read application or hook order missing")
			}
			for j := 0; j < len(result.HookOrder); j += 2 {
				before, after := result.HookOrder[j], result.HookOrder[j+1]
				if before.Stage != "before-ktx" || before.After != benchcontract.ReadDescriptionOriginalSHA256 || after.Stage != "after-ktx" || after.After != expected || after.KTXOrdinal == nil {
					t.Fatal("KTX did not run between the test observers")
				}
			}
			if baseline == nil {
				baseline = result.Tools
			} else if !reflect.DeepEqual(baseline, result.Tools) {
				t.Fatal("runtime changed other descriptions or parameters across arms/restart")
			}
			oracleErr := checkReadRuntimeWindows(result)
			store, err := db.OpenQuery(filepath.Join(root, databaseName))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			task, err := LoadTask("../../tools/bench/tasks/read-window-repeated.toml")
			if err != nil || len(result.Turns) != 2 {
				t.Fatalf("real turn evidence unavailable: %v", err)
			}
			score := scoreReadPreservation(task, interval.SessionID, store.DB(), func(turn int) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(result.Turns[turn-1])), nil
			})
			var unknownOutcomes int
			if err := store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE is_error IS NULL`).Scan(&unknownOutcomes); err != nil {
				t.Fatal(err)
			}
			t.Logf("real CLI/collector scorer=%+v pre-delivery-unknown-outcomes=%d", score, unknownOutcomes)
			if tc.mode == "wrong-limit" {
				if oracleErr == nil || score.FullPass {
					t.Fatal("real-tool wrong-limit control passed the default-window oracle")
				}
				t.Logf("real-tool negative control detected: %v", oracleErr)
				return
			}
			if oracleErr != nil {
				t.Fatalf("real read oracle: %v", oracleErr)
			}
			if !score.FullPass {
				t.Fatal("real CLI/collector preservation score failed")
			}
			if baselineOutputs == nil {
				baselineOutputs = result.DeliveredOutputs
			} else if !reflect.DeepEqual(baselineOutputs, result.DeliveredOutputs) {
				t.Fatal("real read delivery changed across arms/restart")
			}
			t.Logf("real read: same_session=%s completed=2 last=a-2000,b-2000 more=2001 cap=nonbinding restart=%t synthetic_model=true", result.Session, i == 2)
		})
	}
}

func checkReadRuntimeWindows(r readRuntimeProbeResult) error {
	if r.Session == "" || r.Requests != 4 || len(r.DeliveredOutputs) != 2 {
		return fmt.Errorf("missing real runtime results")
	}
	phase, reads, answers := -1, 0, 0
	for _, message := range r.Messages {
		if message.Info.SessionID != r.Session || len(message.Info.Error) != 0 {
			return fmt.Errorf("runtime session changed or failed")
		}
		if message.Info.Role == "user" {
			phase++
			if reads != phase || answers != phase {
				return fmt.Errorf("follow-up preceded completed read/answer")
			}
			continue
		}
		if phase < 0 || phase > 1 {
			return fmt.Errorf("unexpected runtime turn")
		}
		p := readCases["read-window-repeated"][phase]
		for _, part := range message.Parts {
			if part.SessionID != r.Session {
				return fmt.Errorf("tool/text part changed session")
			}
			if part.Type == "text" {
				if strings.TrimSpace(part.Text) != p.Answer {
					return fmt.Errorf("wrong completion for %s", p.File)
				}
				answers++
			}
			if part.Type != "tool" {
				continue
			}
			if reads != phase || part.Tool != "read" || part.State.Status != "completed" || !readArgs(part.State.Input, p) {
				return fmt.Errorf("read arguments, status or order mismatch for %s: %s", p.File, part.State.Input)
			}
			output := part.State.Output
			lines := strings.Split(output, "\n")
			dataLines := 0
			for _, line := range lines {
				var n int
				var value string
				if _, err := fmt.Sscanf(line, "%d: %s", &n, &value); err != nil {
					continue
				}
				dataLines++
				if n != dataLines || value != fmt.Sprintf("%c-%04d", p.File[7], n) {
					return fmt.Errorf("wrong numbered data in %s", p.File)
				}
			}
			if dataLines != 2000 || !strings.Contains(output, "(Showing lines 1-2000 of 2001. Use offset=2001 to continue.)") || strings.Contains(output, "Output capped") || !strings.Contains(r.DeliveredOutputs[phase], output) {
				return fmt.Errorf("default-window output or delivery mismatch for %s", p.File)
			}
			reads++
		}
	}
	if phase != 1 || reads != 2 || answers != 2 {
		return fmt.Errorf("incomplete same-session read sequence")
	}
	return nil
}
