package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/telemetry"
	telemetryopencode "github.com/jgabor/ktx/internal/telemetry/opencode"
)

func TestOpencodeInstallIsIdempotentAndReportsGeneratedContract(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	restoreCwd := chdir(t, project)
	defer restoreCwd()

	globalPath := filepath.Join(home, ".config", "opencode", "plugins", ktxPluginFileName)
	projectPath := filepath.Join(project, ".opencode", "plugins", ktxPluginFileName)

	for _, test := range []struct {
		name string
		args []string
		path string
	}{
		{name: "global", args: []string{"opencode"}, path: globalPath},
		{name: "project", args: []string{"opencode", "--project"}, path: projectPath},
	} {
		t.Run(test.name, func(t *testing.T) {
			var firstReport, secondReport map[string]any
			var firstBytes []byte
			for attempt := 0; attempt < 2; attempt++ {
				var stdout, stderr bytes.Buffer
				if code := runInitWith(test.args, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitOK {
					t.Fatalf("attempt %d exit code = %d, stderr=%q", attempt+1, code, stderr.String())
				}
				var report map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
					t.Fatalf("attempt %d report: %v", attempt+1, err)
				}
				installed, err := os.ReadFile(test.path)
				if err != nil {
					t.Fatalf("attempt %d read installed adapter: %v", attempt+1, err)
				}
				if attempt == 0 {
					firstReport, firstBytes = report, installed
				} else {
					secondReport = report
					if !bytes.Equal(installed, firstBytes) {
						t.Fatal("adapter bytes changed on repeated installation")
					}
				}
			}
			if !reflect.DeepEqual(firstReport, secondReport) {
				t.Fatalf("installation report changed on repeat:\nfirst:  %#v\nsecond: %#v", firstReport, secondReport)
			}
			if firstReport["path"] != test.path || firstReport["runtime_activation"] != "not_verified" {
				t.Fatalf("report path/activation = %#v", firstReport)
			}
			if revision, ok := firstReport["generated_revision"].(string); !ok || len(revision) != 64 {
				t.Fatalf("generated_revision = %#v, want 64-character SHA-256", firstReport["generated_revision"])
			}
			locations, ok := firstReport["candidate_locations"].(map[string]any)
			if !ok || locations["global"] != globalPath || locations["project"] != projectPath {
				t.Fatalf("candidate_locations = %#v", firstReport["candidate_locations"])
			}
			if hooks := firstReport["declared_hooks"]; !reflect.DeepEqual(hooks, []any{"event", "tool.execute.after"}) {
				t.Fatalf("declared_hooks = %#v", hooks)
			}
			hookRoles, ok := firstReport["hook_roles"].(map[string]any)
			if !ok || hookRoles["event"] != opencodeTelemetryEvidence || hookRoles["tool.execute.after"] != "tool-result compression" {
				t.Fatalf("hook_roles = %#v", firstReport["hook_roles"])
			}
			if firstReport["telemetry_claim_boundary"] != opencodeTelemetryClaimBoundary ||
				firstReport["refresh_command"] != opencodeRefreshCommand ||
				firstReport["troubleshooting"] != opencodeTroubleshooting {
				t.Fatalf("integration guidance = %#v", firstReport)
			}
			for _, forbidden := range []string{"active", "activated", "loaded", "running"} {
				if _, exists := firstReport[forbidden]; exists {
					t.Errorf("report makes unsupported runtime claim %q", forbidden)
				}
			}
		})
	}
}

func TestInitAndTelemetryHelpDocumentOpenCodeIntegration(t *testing.T) {
	var initHelp bytes.Buffer
	printInitHelp(&initHelp)
	for _, want := range []string{
		"$XDG_CONFIG_HOME/opencode/plugins/ktx.ts",
		"./.opencode/plugins/ktx.ts",
		"event for optional model-step telemetry",
		"tool.execute.after for tool-result compression",
		"KTX_SESSION_ID",
		"preventing duplicate evidence",
		opencodeTelemetryEvidence,
		opencodeTelemetryClaimBoundary,
		opencodeRefreshCommand,
		opencodeTroubleshooting,
	} {
		if !strings.Contains(initHelp.String(), want) {
			t.Errorf("init help missing %q", want)
		}
	}

	var telemetryHelp bytes.Buffer
	printTelemetryHelp(&telemetryHelp)
	for _, want := range []string{opencodeTelemetryEvidence, opencodeTelemetryClaimBoundary} {
		if !strings.Contains(telemetryHelp.String(), want) {
			t.Errorf("telemetry help missing %q", want)
		}
	}
}

func TestOpencodeGeneratedAdapterEventAndToolIntegration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses a POSIX shell executable")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is required to execute the generated OpenCode adapter")
	}

	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "ktx.ts")
	if err := os.WriteFile(pluginPath, []byte(ktxOpencodePluginTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeKtx := `#!/bin/sh
payload=$(cat)
printf '%s\t%s\n' "$*" "$payload" >> "$KTX_TEST_LOG"
if [ "$1" = "telemetry" ] && [ "$KTX_TEST_TELEMETRY_FAIL" = "1" ]; then
  printf 'telemetry unavailable\n' >&2
  exit 8
fi
if [ "$1" = "hook" ]; then
  if [ "$KTX_TEST_HOOK_FAIL" = "1" ]; then
    printf 'hook unavailable\n' >&2
    exit 7
  fi
  printf '{"output":"compressed tool output","compressed_id":"compressedid","source_id":"sourceid"}\n'
fi
`
	if err := os.WriteFile(filepath.Join(binDir, "ktx"), []byte(fakeKtx), 0o755); err != nil {
		t.Fatal(err)
	}

	driverPath := filepath.Join(dir, "driver.ts")
	driver := `import plugin from ` + strconv.Quote(pluginPath) + `
const hooks: any = await plugin({ $: () => ({ quiet: async () => {} }) } as any)
const identity = {type:"message.updated",properties:{info:{id:"msg_1",sessionID:"ses_1",role:"assistant",providerID:"openai",modelID:"gpt-5"}}}
const first = {type:"message.part.updated",properties:{part:{id:"prt_1",sessionID:"ses_1",messageID:"msg_1",type:"step-finish",reason:"stop",cost:0,tokens:{input:116,output:3,reasoning:2,cache:{read:15232,write:0}}}}}
const second = {type:"message.part.updated",properties:{part:{id:"prt_2",sessionID:"ses_1",messageID:"msg_2",type:"step-finish",reason:"stop",cost:1.25,tokens:{input:8,output:5,reasoning:0,cache:{read:0,write:4}}}}}
const incomplete = {type:"message.part.updated",properties:{part:{id:"prt_3",sessionID:"ses_1",messageID:"msg_3",type:"step-finish",reason:"stop",cost:null,tokens:{input:0,output:"bad",cache:{read:null,write:4}}}}}
const malformedTokens = {type:"message.part.updated",properties:{part:{id:"prt_4",sessionID:"ses_1",messageID:"msg_4",type:"step-finish",reason:"stop",cost:0,tokens:[]}}}
const malformedCache = {type:"message.part.updated",properties:{part:{id:"prt_5",sessionID:"ses_1",messageID:"msg_5",type:"step-finish",reason:"stop",cost:0,tokens:{input:1,output:2,reasoning:3,cache:[]}}}}
const malformedLeaves = {type:"message.part.updated",properties:{part:{id:"prt_6",sessionID:"ses_1",messageID:"msg_6",type:"step-finish",reason:"stop",cost:Number.POSITIVE_INFINITY,tokens:{input:-1,output:1.5,reasoning:0,cache:{read:"bad",write:0}}}}}
const rejected = {type:"message.part.updated",properties:{part:{type:"step-finish"}}}
const originalEvents = JSON.stringify([identity, first, second, incomplete, malformedTokens, malformedCache, malformedLeaves, rejected])
await hooks.event({event: identity})
await Promise.all([hooks.event({event: first}), hooks.event({event: first}), hooks.event({event: second}), hooks.event({event: incomplete}), hooks.event({event: malformedTokens}), hooks.event({event: malformedCache}), hooks.event({event: malformedLeaves}), hooks.event({event: rejected})])
if (JSON.stringify([identity, first, second, incomplete, malformedTokens, malformedCache, malformedLeaves, rejected]) !== originalEvents) throw new Error("event hook mutated model events")
const output: any = {title:"tool",output:"original tool output",metadata:{kept:true}}
await hooks["tool.execute.after"]({tool:"bash",sessionID:"ses_1",callID:"call_1",args:{}}, output)
console.log(JSON.stringify(output))
`
	if err := os.WriteFile(driverPath, []byte(driver), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, extraEnv ...string) (map[string]any, []string, string) {
		t.Helper()
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cmd := exec.Command(bun, driverPath)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "KTX_TEST_LOG="+logPath)
		cmd.Env = append(cmd.Env, extraEnv...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("bun adapter driver: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		}
		var output map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &output); err != nil {
			t.Fatalf("driver output: %v: %q", err, stdout.String())
		}
		data, err := os.ReadFile(logPath)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		var calls []string
		if len(data) > 0 {
			calls = strings.Split(strings.TrimSpace(string(data)), "\n")
		}
		return output, calls, stderr.String()
	}

	t.Run("unwrapped session records accepted events and preserves hook behavior", func(t *testing.T) {
		output, calls, _ := run(t)
		if output["output"] != "compressed tool output" {
			t.Fatalf("tool output = %#v", output)
		}
		if len(calls) != 7 {
			t.Fatalf("calls = %q, want six telemetry records and one tool hook", calls)
		}
		var observations []map[string]any
		for ordinal, call := range calls[:6] {
			fields := strings.SplitN(call, "\t", 2)
			if len(fields) != 2 || fields[0] != "telemetry record" {
				t.Fatalf("telemetry call = %q", call)
			}
			var observation map[string]any
			if err := json.Unmarshal([]byte(fields[1]), &observation); err != nil {
				t.Fatalf("telemetry payload: %v", err)
			}
			if observation["ordinal"] != float64(ordinal+1) {
				t.Fatalf("ordinal = %#v, want %d", observation["ordinal"], ordinal+1)
			}
			observations = append(observations, observation)
		}
		assertGeneratedMeasurementStates(t, observations[2], map[string]string{
			"cost": "unknown", "input": "measured", "output": "malformed", "reasoning": "unknown", "cache_read": "unknown", "cache_write": "measured",
		})
		assertGeneratedMeasurementStates(t, observations[3], map[string]string{
			"cost": "measured", "input": "malformed", "output": "malformed", "reasoning": "malformed", "cache_read": "malformed", "cache_write": "malformed",
		})
		assertGeneratedMeasurementStates(t, observations[4], map[string]string{
			"cost": "measured", "input": "measured", "output": "measured", "reasoning": "measured", "cache_read": "malformed", "cache_write": "malformed",
		})
		assertGeneratedMeasurementStates(t, observations[5], map[string]string{
			"cost": "malformed", "input": "malformed", "output": "malformed", "reasoning": "measured", "cache_read": "malformed", "cache_write": "measured",
		})
		wrapperCases := []string{
			`{"type":"step_finish","part":{"id":"prt_3","sessionID":"ses_1","messageID":"msg_3","type":"step-finish","reason":"stop","cost":null,"tokens":{"input":0,"output":"bad","cache":{"read":null,"write":4}}}}`,
			`{"type":"step_finish","part":{"id":"prt_4","sessionID":"ses_1","messageID":"msg_4","type":"step-finish","reason":"stop","cost":0,"tokens":[]}}`,
			`{"type":"step_finish","part":{"id":"prt_5","sessionID":"ses_1","messageID":"msg_5","type":"step-finish","reason":"stop","cost":0,"tokens":{"input":1,"output":2,"reasoning":3,"cache":[]}}}`,
			`{"type":"step_finish","part":{"id":"prt_6","sessionID":"ses_1","messageID":"msg_6","type":"step-finish","reason":"stop","cost":"bad","tokens":{"input":-1,"output":1.5,"reasoning":0,"cache":{"read":"bad","write":0}}}}`,
		}
		for i, payload := range wrapperCases {
			canonical, err := telemetryopencode.DecodeStreamModelStep([]byte(payload))
			if err != nil {
				t.Fatalf("canonical wrapper case %d: %v", i, err)
			}
			if got, want := generatedMeasurementStates(observations[i+2]), canonicalMeasurementStates(canonical); !reflect.DeepEqual(got, want) {
				t.Errorf("generated/wrapper states differ for case %d: got %v want %v", i, got, want)
			}
		}
		if !strings.HasPrefix(calls[6], "hook\t") {
			t.Fatalf("tool call = %q", calls[6])
		}
	})

	t.Run("wrapper ownership disables both evidence paths", func(t *testing.T) {
		output, calls, _ := run(t, "KTX_SESSION_ID=wrapper-session")
		if output["output"] != "original tool output" || len(calls) != 0 {
			t.Fatalf("output=%#v calls=%q", output, calls)
		}
	})

	t.Run("telemetry and tool failures remain fail open", func(t *testing.T) {
		output, calls, stderr := run(t, "KTX_TEST_TELEMETRY_FAIL=1", "KTX_TEST_HOOK_FAIL=1")
		if output["output"] != "original tool output" {
			t.Fatalf("tool output changed on failure: %#v", output)
		}
		if len(calls) != 7 || !strings.Contains(stderr, "telemetry unavailable") || !strings.Contains(stderr, "hook failed") {
			t.Fatalf("calls=%q stderr=%q", calls, stderr)
		}
	})
}

func assertGeneratedMeasurementStates(t *testing.T, observation map[string]any, want map[string]string) {
	t.Helper()
	measurements := generatedMeasurementStates(observation)
	for field, state := range want {
		if measurements[field] != state {
			t.Errorf("%s state = %q, want %q", field, measurements[field], state)
		}
	}
}

func generatedMeasurementStates(observation map[string]any) map[string]string {
	tokens := observation["tokens"].(map[string]any)
	measurements := map[string]any{"cost": observation["cost"]}
	for _, field := range []string{"input", "output", "reasoning", "cache_read", "cache_write"} {
		measurements[field] = tokens[field]
	}
	states := make(map[string]string, len(measurements))
	for field, value := range measurements {
		measurement, ok := value.(map[string]any)
		if !ok {
			states[field] = "invalid"
			continue
		}
		states[field], _ = measurement["state"].(string)
	}
	return states
}

func canonicalMeasurementStates(observation telemetry.Observation) map[string]string {
	return map[string]string{
		"cost": string(observation.Cost.State), "input": string(observation.Tokens.Input.State),
		"output": string(observation.Tokens.Output.State), "reasoning": string(observation.Tokens.Reasoning.State),
		"cache_read": string(observation.Tokens.CacheRead.State), "cache_write": string(observation.Tokens.CacheWrite.State),
	}
}
