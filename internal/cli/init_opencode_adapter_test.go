package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
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
if [ "$1" = "binary-capabilities" ]; then
  path=$(readlink -f "$0")
  digest=$(sha256sum "$path" | cut -d' ' -f1)
  case "$KTX_TEST_BAD_RESPONSE" in
    identity-malformed) printf '[]\n'; exit 0 ;;
    identity-path) path=/wrong/ktx ;;
    identity-digest) digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
    identity-schema) schema=7 ;;
  esac
  : "${schema:=8}"
  printf '{"schema":"ktx.binary-capabilities/v1","executable_path":"%s","executable_sha256":"%s","build_version":"fixture","max_database_schema":%s}\n' "$path" "$digest" "$schema"
  exit 0
fi
if [ "$1 $2" = "db path" ]; then
  [ -z "$KTX_TEST_PATH_LOG" ] || printf 'db-path\n' >> "$KTX_TEST_PATH_LOG"
  printf '%s\n' "${KTX_TEST_DB_PATH:-$PWD/ktx.db}"
  exit 0
fi
payload=$(cat)
printf '%s\t%s\n' "$*" "$payload" >> "$KTX_TEST_LOG"
if [ "$1" = "hook" ] || [ "$1 $2" = "telemetry record" ]; then
  case "$KTX_TEST_SCHEMA_MODE" in
    valid)
      path=$(readlink -f "$0"); digest=$(sha256sum "$path" | cut -d' ' -f1)
      schema_actual=${KTX_TEST_SCHEMA_ACTUAL:-9}
      case "$KTX_TEST_FINGERPRINT_CASE" in
        observed-race)
          marker="$KTX_TEST_LOG.observed-race"
          if [ ! -f "$marker" ]; then
            schema_actual=9
            printf 'changed after schema 9 observation' > "$KTX_TEST_DB_PATH"
            : > "$marker"
          else
            schema_actual=10
          fi ;;
        probe-pre-failure)
          [ -e "$KTX_TEST_DB_PATH" ] || printf 'created during invocation' > "$KTX_TEST_DB_PATH" ;;
        probe-post-failure)
          rm -f "$KTX_TEST_DB_PATH" "$KTX_TEST_DB_PATH-wal" "$KTX_TEST_DB_PATH-shm" ;;
        probe-mismatch)
          printf x >> "$KTX_TEST_DB_PATH" ;;
      esac
      schema_max=${KTX_TEST_SCHEMA_MAX:-8}
      printf '{"error":{"code":"schema-incompatible","message":"schema too new","actual_schema":%s,"maximum_schema":%s,"database_path":"%s","executable_path":"%s","executable_sha256":"%s","build_version":"fixture","correction":"install a newer binary"}}\n' "$schema_actual" "$schema_max" "$KTX_TEST_DB_PATH" "$path" "$digest" >&2
      exit 5 ;;
    malformed) printf '{"error":{"code":"schema-incompatible"}}\n' >&2; exit 5 ;;
    other) printf 'ordinary failure\n' >&2; exit 7 ;;
  esac
fi
if [ "$1" = "telemetry" ] && [ "$KTX_TEST_TELEMETRY_FAIL" = "1" ]; then
  printf 'telemetry unavailable\n' >&2
  exit 8
fi
if [ "$1 $2" = "telemetry collector-open" ]; then
  epoch=$(printf '%s' "$payload" | sed -n 's/.*"epoch_id":"\([^"]*\)".*/\1/p')
  state="$KTX_TEST_LOG.ordinal"
  current=0; [ ! -f "$state" ] || read current < "$state"
  allocated=$((current + 1)); printf '%s\n' "$allocated" > "$state"
  case "$KTX_TEST_BAD_RESPONSE" in
    collector-session) session=wrongwrongwrongwr ;;
    *) session=aaaaaaaaaaaaaaaa ;;
  esac
  interval=bbbbbbbbbbbbbbbb; [ "$KTX_TEST_BAD_RESPONSE" = collector-interval ] && interval=wrongwrongwrongwr
  [ "$KTX_TEST_BAD_RESPONSE" = collector-epoch ] && epoch=wrong-epoch
  kind=collector_open; [ "$KTX_TEST_BAD_RESPONSE" = collector-kind ] && kind=other
  ordinal=$allocated; [ "$KTX_TEST_BAD_RESPONSE" = collector-ordinal ] && ordinal=0
	[ "$KTX_TEST_BAD_RESPONSE" = collector-type ] && ordinal='"1"'
	contract=opencode-event-v2; [ "$KTX_TEST_BAD_RESPONSE" = collector-contract ] && contract=wrong-contract
  if [ "$KTX_TEST_BAD_RESPONSE" = collector-malformed ]; then printf '[]\n'
  elif [ "$KTX_TEST_BAD_RESPONSE" = collector-missing ]; then printf '{"ok":true}\n'
	else printf '{"ok":true,"session_id":"%s","interval_id":"%s","epoch_id":"%s","epoch_ordinal":0,"ordinal":%s,"kind":"%s","contract":"%s"}\n' "$session" "$interval" "$epoch" "$ordinal" "$kind" "$contract"
  fi
fi
if [ "$1 $2" = "telemetry event-record" ]; then
  epoch=$(printf '%s' "$payload" | sed -n 's/.*"epoch_id":"\([^"]*\)".*/\1/p')
  epoch_ordinal=$(printf '%s' "$payload" | sed -n 's/.*"epoch_ordinal":\([0-9]*\).*/\1/p')
  after_ordinal=$(printf '%s' "$payload" | sed -n 's/.*"after_ordinal":\([0-9]*\).*/\1/p')
  if [ -n "$KTX_TEST_EVENT_FAIL_ORDINAL" ] && [ "$epoch_ordinal" -eq "$KTX_TEST_EVENT_FAIL_ORDINAL" ]; then
    printf 'event callback unavailable\n' >&2
    exit 8
  fi
  state="$KTX_TEST_LOG.ordinal"
  current=0; [ ! -f "$state" ] || read current < "$state"
  if [ -z "$after_ordinal" ] || [ "$after_ordinal" -ne "$current" ]; then
    printf 'stale global ordinal: after=%s current=%s\n' "$after_ordinal" "$current" >&2
    exit 9
  fi
  allocated=$((current + 1)); printf '%s\n' "$allocated" > "$state"
  ordinal=$allocated
  kind=other
  printf '%s' "$payload" | grep -q '"type":"step-finish"' && kind=model_step
  [ "$KTX_TEST_BAD_RESPONSE" = event-kind ] && kind=other
  session=aaaaaaaaaaaaaaaa; [ "$KTX_TEST_BAD_RESPONSE" = event-session ] && session=wrongwrongwrongwr
	interval=bbbbbbbbbbbbbbbb; [ "$KTX_TEST_BAD_RESPONSE" = event-interval ] && interval=wrongwrongwrongwr
	[ "$KTX_TEST_BAD_RESPONSE" = event-epoch ] && epoch=wrong-epoch
	case "$KTX_TEST_BAD_RESPONSE" in
	  event-same) ordinal=1 ;;
	  event-backward) ordinal=0 ;;
	  event-gap-two) ordinal=3 ;;
	  event-gap-99) ordinal=99 ;;
	  event-large) ordinal=9007199254740992 ;;
	  event-fractional) ordinal=2.5 ;;
	  event-string) ordinal='"2"' ;;
	  event-null) ordinal=null ;;
	  event-nonfinite) ordinal=1e999 ;;
	esac
	contract=opencode-event-v2; [ "$KTX_TEST_BAD_RESPONSE" = event-contract ] && contract=wrong-contract
  if [ "$KTX_TEST_BAD_RESPONSE" = event-missing ]; then printf '{"ok":true}\n'
	else printf '{"ok":true,"session_id":"%s","interval_id":"%s","epoch_id":"%s","epoch_ordinal":%s,"ordinal":%s,"kind":"%s","contract":"%s"}\n' "$session" "$interval" "$epoch" "$epoch_ordinal" "$ordinal" "$kind" "$contract"
  fi
fi
if [ "$1" = "hook" ]; then
  if [ "$KTX_TEST_HOOK_FAIL" = "1" ]; then
    printf 'hook unavailable\n' >&2
    exit 7
  fi
  if [ "$KTX_TEST_BAD_RESPONSE" = hook-envelope ]; then printf '{"output":7,"compressed_id":false,"source_id":""}\n'
  else printf '{"output":"compressed tool output","compressed_id":"compressedid","source_id":"sourceid","ktx_session_id":"aaaaaaaaaaaaaaaa","runtime_session_id":"ses_1","call_id":"call_1"}\n'
  fi
fi
`
	if err := os.WriteFile(filepath.Join(binDir, "ktx"), []byte(fakeKtx), 0o755); err != nil {
		t.Fatal(err)
	}

	driverPath := filepath.Join(dir, "driver.ts")
	driver := `import plugin from ` + strconv.Quote(pluginPath) + `
const hooks: any = await plugin({ $: () => ({ quiet: async () => {} }) } as any)
if (process.env.KTX_TEST_PATH_AFTER_LOAD) process.env.PATH = process.env.KTX_TEST_PATH_AFTER_LOAD
if (!hooks.event) {
  console.log(JSON.stringify({title:"tool",output:"original tool output",metadata:{kept:true}}))
  process.exit(0)
}
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

	t.Run("resolved candidate stays pinned after PATH changes", func(t *testing.T) {
		otherBin := t.TempDir()
		if err := os.WriteFile(filepath.Join(otherBin, "ktx"), []byte("#!/bin/sh\nprintf 'PATH drift selected\\n' >&2\nexit 99\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		output, calls, stderr := run(t, "KTX_TEST_PATH_AFTER_LOAD="+otherBin)
		if output["output"] != "compressed tool output" || len(calls) != 7 || strings.Contains(stderr, "PATH drift selected") {
			t.Fatalf("output=%#v calls=%q stderr=%q", output, calls, stderr)
		}
	})

	containmentDriverPath := filepath.Join(dir, "containment-driver.ts")
	containmentDriver := `import plugin from ` + strconv.Quote(pluginPath) + `
const input = {tool:"bash",sessionID:"ses_1",callID:"call_1",args:{}}
const invoke = async (hooks: any) => {
  const output: any = {title:"tool",output:"original tool output",metadata:{kept:true}}
  const before = JSON.stringify(output)
  let rejected = false
  let rejection = ""
  try { await hooks["tool.execute.after"](input, output) } catch (error) { rejected = true; rejection = String(error) }
  if (JSON.stringify(output) !== before) throw new Error("ordinary output changed")
  return {output, rejected, rejection}
}
const load = () => plugin({ $: () => ({ quiet: async () => {} }) } as any)
const scenario = process.env.KTX_TEST_CONTAINMENT
const results: any[] = []
if (scenario === "restart") {
  results.push(await invoke(await load()))
  results.push(await invoke(await load()))
} else {
  const hooks: any = await load()
  if (scenario === "concurrent") {
    results.push(...await Promise.all([invoke(hooks), invoke(hooks)]))
  } else {
    results.push(await invoke(hooks))
    if (scenario === "cross-callback") {
    const event = {type:"message.part.updated",properties:{part:{id:"prt_1",sessionID:"ses_1",messageID:"msg_1",type:"step-finish",reason:"stop",cost:0,tokens:{input:1,output:1,reasoning:0,cache:{read:0,write:0}}}}}
    await hooks.event({event})
    results.push({output:{title:"tool",output:"original tool output",metadata:{kept:true}},rejected:false,rejection:""})
    } else {
      if (scenario === "distinct") {
        process.env.KTX_TEST_DB_PATH = process.env.KTX_TEST_DB_PATH_2
        process.env.KTX_DB = process.env.KTX_TEST_DB_PATH_2
        process.env.KTX_TEST_SCHEMA_ACTUAL = "10"
      } else if (scenario === "same-path-database") {
        await Bun.write(process.env.KTX_TEST_DB_PATH!, "changed database metadata")
        process.env.KTX_TEST_SCHEMA_ACTUAL = "10"
      } else if (scenario === "same-path-symlink") {
        await Bun.write(process.env.KTX_TEST_DB_PATH!, "changed symlink target metadata")
        process.env.KTX_TEST_SCHEMA_ACTUAL = "10"
      } else if (scenario === "same-path-wal") {
        await Bun.write(process.env.KTX_TEST_DB_PATH! + "-wal", "new wal metadata")
        process.env.KTX_TEST_SCHEMA_ACTUAL = "10"
      } else if (scenario?.startsWith("windows-")) {
        const variable = scenario.slice("windows-".length).toUpperCase()
        process.env[variable] = (process.env[variable] || "") + "-changed"
        process.env.KTX_TEST_DB_PATH = process.env.KTX_TEST_DB_PATH_2
        await Bun.write(process.env.KTX_TEST_DB_PATH!, "second database")
      }
      results.push(await invoke(hooks))
      if (scenario === "same-path-database" || scenario === "same-path-symlink" || scenario === "same-path-wal" ||
          scenario === "observed-race" || scenario?.startsWith("probe-")) {
        results.push(await invoke(hooks))
      }
    }
  }
}
console.log(JSON.stringify(results))
`
	if err := os.WriteFile(containmentDriverPath, []byte(containmentDriver), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("schema incompatibility containment", func(t *testing.T) {
		for _, test := range []struct {
			name            string
			scenario        string
			mode            string
			required        bool
			wantCalls       int
			wantPathCalls   int
			wantDiagnostics int
			wantRejected    bool
			wantDeepEqual   bool
		}{
			{name: "identical ordinary callbacks", scenario: "repeat", mode: "valid", wantCalls: 1, wantPathCalls: 1, wantDiagnostics: 1},
			{name: "concurrent ordinary callbacks", scenario: "concurrent", mode: "valid", wantCalls: 1, wantPathCalls: 1, wantDiagnostics: 1},
			{name: "later telemetry callback", scenario: "cross-callback", mode: "valid", wantCalls: 1, wantPathCalls: 1, wantDiagnostics: 1},
			{name: "new plugin instance", scenario: "restart", mode: "valid", wantCalls: 2, wantPathCalls: 2, wantDiagnostics: 2},
			{name: "distinct database and schema", scenario: "distinct", mode: "valid", wantCalls: 2, wantPathCalls: 2, wantDiagnostics: 2},
			{name: "same path database metadata change", scenario: "same-path-database", mode: "valid", wantCalls: 2, wantPathCalls: 1, wantDiagnostics: 2},
			{name: "same path symlink target metadata change", scenario: "same-path-symlink", mode: "valid", wantCalls: 2, wantPathCalls: 1, wantDiagnostics: 2},
			{name: "same path wal metadata change", scenario: "same-path-wal", mode: "valid", wantCalls: 2, wantPathCalls: 1, wantDiagnostics: 2},
			{name: "metadata changes after observed schema", scenario: "observed-race", mode: "valid", wantCalls: 2, wantPathCalls: 1, wantDiagnostics: 2, wantDeepEqual: true},
			{name: "pre invocation fingerprint failure", scenario: "probe-pre-failure", mode: "valid", wantCalls: 2, wantPathCalls: 1, wantDiagnostics: 1},
			{name: "post invocation fingerprint failure", scenario: "probe-post-failure", mode: "valid", wantCalls: 3, wantPathCalls: 1, wantDiagnostics: 1},
			{name: "pre post fingerprint mismatch", scenario: "probe-mismatch", mode: "valid", wantCalls: 3, wantPathCalls: 1, wantDiagnostics: 1},
			{name: "USERPROFILE path change", scenario: "windows-userprofile", mode: "valid", wantCalls: 2, wantPathCalls: 2, wantDiagnostics: 2},
			{name: "HOMEDRIVE path change", scenario: "windows-homedrive", mode: "valid", wantCalls: 2, wantPathCalls: 2, wantDiagnostics: 2},
			{name: "HOMEPATH path change", scenario: "windows-homepath", mode: "valid", wantCalls: 2, wantPathCalls: 2, wantDiagnostics: 2},
			{name: "malformed exit five", scenario: "repeat", mode: "malformed", wantCalls: 2, wantPathCalls: 1, wantDiagnostics: 2},
			{name: "non schema errors", scenario: "repeat", mode: "other", wantCalls: 2, wantPathCalls: 1, wantDiagnostics: 2},
			{name: "required benchmark", scenario: "repeat", mode: "valid", required: true, wantCalls: 3, wantPathCalls: 0, wantDiagnostics: 2, wantRejected: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := t.TempDir()
				logPath := filepath.Join(fixture, "calls.log")
				pathLog := filepath.Join(fixture, "paths.log")
				dbPath := filepath.Join(fixture, "first.db")
				secondDBPath := filepath.Join(fixture, "second.db")
				if test.scenario != "probe-pre-failure" {
					if err := os.WriteFile(dbPath, []byte("first database"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(secondDBPath, []byte("second database"), 0o600); err != nil {
					t.Fatal(err)
				}
				if test.scenario == "same-path-symlink" {
					physical := filepath.Join(fixture, "physical.db")
					if err := os.WriteFile(physical, []byte("physical database"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(dbPath); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(physical, dbPath); err != nil {
						t.Skipf("symlink fixture unavailable: %v", err)
					}
				}
				cmd := exec.Command(bun, containmentDriverPath)
				cmd.Env = append(os.Environ(),
					"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
					"KTX_TEST_LOG="+logPath,
					"KTX_TEST_PATH_LOG="+pathLog,
					"KTX_TEST_DB_PATH="+dbPath,
					"KTX_TEST_DB_PATH_2="+secondDBPath,
					"KTX_DB="+dbPath,
					"USERPROFILE=profile-a",
					"HOMEDRIVE=drive-a",
					"HOMEPATH=path-a",
					"KTX_TEST_SCHEMA_MODE="+test.mode,
					"KTX_TEST_CONTAINMENT="+test.scenario,
					"KTX_TEST_FINGERPRINT_CASE="+test.scenario,
				)
				if test.required {
					cmd.Env = append(cmd.Env,
						"KTX_BENCH_SESSION_ID=aaaaaaaaaaaaaaaa",
						"KTX_BENCH_EVENT_INTERVAL_ID=bbbbbbbbbbbbbbbb",
						"KTX_BENCH_EVENT_CONTRACT=opencode-event-v2",
						"KTX_BENCH_COLLECTOR_MODE=required",
					)
				}
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("containment driver: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
				}
				var results []struct {
					Output    map[string]any `json:"output"`
					Rejected  bool           `json:"rejected"`
					Rejection string         `json:"rejection"`
				}
				if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &results); err != nil {
					t.Fatalf("decode results: %v: %q", err, stdout.String())
				}
				for _, result := range results {
					if result.Output["output"] != "original tool output" || result.Output["title"] != "tool" || result.Rejected != test.wantRejected {
						t.Fatalf("result=%#v, want original output and rejected=%v", result, test.wantRejected)
					}
					if test.required && (!strings.Contains(result.Rejection, "exited 5") || !strings.Contains(result.Rejection, "schema-incompatible")) {
						t.Errorf("required rejection lost exit or fields: %q", result.Rejection)
					}
				}
				if test.wantDeepEqual {
					for index := 1; index < len(results); index++ {
						if !reflect.DeepEqual(results[index], results[0]) {
							t.Errorf("callback %d result changed: first=%#v current=%#v", index+1, results[0], results[index])
						}
					}
				}
				if got := countFixtureLines(t, logPath); got != test.wantCalls {
					t.Errorf("DB-opening calls = %d, want %d", got, test.wantCalls)
				}
				if got := countFixtureLines(t, pathLog); got != test.wantPathCalls {
					t.Errorf("db path calls = %d, want %d", got, test.wantPathCalls)
				}
				diagnostic := "schema-incompatible"
				if test.mode == "other" {
					diagnostic = "ordinary failure"
				}
				if got := strings.Count(stderr.String()+stdout.String(), diagnostic); got != test.wantDiagnostics {
					t.Errorf("diagnostics = %d, want %d; stdout=%q stderr=%q", got, test.wantDiagnostics, stdout.String(), stderr.String())
				}
				if (strings.HasPrefix(test.scenario, "same-path-") || test.scenario == "observed-race") &&
					(strings.Count(stderr.String(), `"actual_schema":9`) != 1 || strings.Count(stderr.String(), `"actual_schema":10`) != 1) {
					t.Errorf("same-path diagnostics did not expose both exact keys: %q", stderr.String())
				}
			})
		}
	})

	t.Run("plugin factories pin independent candidates and containment state", func(t *testing.T) {
		fixture := t.TempDir()
		binB := filepath.Join(fixture, "bin-b")
		if err := os.Mkdir(binB, 0o755); err != nil {
			t.Fatal(err)
		}
		candidateBBytes := []byte(fakeKtx + "\n# candidate B\n")
		candidateB := filepath.Join(binB, "ktx")
		if err := os.WriteFile(candidateB, candidateBBytes, 0o755); err != nil {
			t.Fatal(err)
		}
		factoryDriverPath := filepath.Join(fixture, "factory-driver.ts")
		factoryDriver := `import plugin from ` + strconv.Quote(pluginPath) + `
const input = {tool:"bash",sessionID:"ses_1",callID:"call_1",args:{}}
const invoke = async (hooks: any) => {
  const output: any = {title:"tool",output:"original tool output",metadata:{kept:true}}
  await hooks["tool.execute.after"](input, output)
  if (output.output !== "original tool output") throw new Error("schema failure changed output")
}
process.env.PATH = process.env.KTX_TEST_BIN_A!
const factoryA: any = await plugin({ $: () => ({ quiet: async () => {} }) } as any)
await invoke(factoryA)
process.env.PATH = process.env.KTX_TEST_BIN_B!
const factoryB: any = await plugin({ $: () => ({ quiet: async () => {} }) } as any)
await invoke(factoryB)
await invoke(factoryA)
console.log("ok")
`
		if err := os.WriteFile(factoryDriverPath, []byte(factoryDriver), 0o644); err != nil {
			t.Fatal(err)
		}
		dbPath := filepath.Join(fixture, "factory.db")
		if err := os.WriteFile(dbPath, []byte("factory database"), 0o600); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(fixture, "calls.log")
		pathLog := filepath.Join(fixture, "paths.log")
		cmd := exec.Command(bun, factoryDriverPath)
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"KTX_TEST_BIN_A="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"KTX_TEST_BIN_B="+binB+string(os.PathListSeparator)+os.Getenv("PATH"),
			"KTX_TEST_LOG="+logPath,
			"KTX_TEST_PATH_LOG="+pathLog,
			"KTX_TEST_DB_PATH="+dbPath,
			"KTX_DB="+dbPath,
			"KTX_TEST_SCHEMA_MODE=valid",
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil || strings.TrimSpace(stdout.String()) != "ok" {
			t.Fatalf("factory driver: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		calls := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(calls) != 2 || !strings.HasPrefix(calls[0], "hook\t") || !strings.HasPrefix(calls[1], "hook\t") {
			t.Fatalf("factory calls=%q, want candidate A then B and no third call", calls)
		}
		canonicalA, _ := filepath.Abs(filepath.Join(binDir, "ktx"))
		canonicalB, _ := filepath.Abs(candidateB)
		if got := countFixtureLines(t, pathLog); got != 2 || strings.Count(stderr.String(), "schema-incompatible") != 2 ||
			!strings.Contains(stderr.String(), canonicalA) || !strings.Contains(stderr.String(), canonicalB) {
			t.Fatalf("path calls=%d stderr=%q, want two isolated first failures", got, stderr.String())
		}
	})

	for _, bad := range []string{"identity-malformed", "identity-path", "identity-digest", "identity-schema"} {
		t.Run("ordinary mode disables unverifiable candidate "+bad, func(t *testing.T) {
			output, calls, stderr := run(t, "KTX_TEST_BAD_RESPONSE="+bad)
			if output["output"] != "original tool output" || len(calls) != 0 ||
				!strings.Contains(stderr, "plugin disabled") || !strings.Contains(stderr, "reinstall") {
				t.Fatalf("output=%#v calls=%q stderr=%q", output, calls, stderr)
			}
		})
	}

	t.Run("required benchmark mode retains and serializes every callback", func(t *testing.T) {
		output, calls, _ := run(t,
			"KTX_BENCH_SESSION_ID=aaaaaaaaaaaaaaaa",
			"KTX_BENCH_EVENT_INTERVAL_ID=bbbbbbbbbbbbbbbb",
			"KTX_BENCH_EVENT_CONTRACT=opencode-event-v2",
			"KTX_BENCH_COLLECTOR_MODE=required",
		)
		if output["output"] != "compressed tool output" || len(calls) != 11 {
			t.Fatalf("output=%#v calls=%q", output, calls)
		}
		if !strings.HasPrefix(calls[0], "telemetry collector-open\t") || !strings.HasPrefix(calls[10], "hook\t") {
			t.Fatalf("required call boundaries=%q", calls)
		}
		for index, call := range calls[1:10] {
			if !strings.HasPrefix(call, "telemetry event-record\t") || !strings.Contains(call, fmt.Sprintf(`"epoch_ordinal":%d`, index+1)) {
				t.Fatalf("required call %d=%q", index+1, call)
			}
		}
	})

	queueDriverPath := filepath.Join(dir, "queue-driver.ts")
	queueDriver := `import plugin from ` + strconv.Quote(pluginPath) + `
const hooks: any = await plugin({ $: () => ({ quiet: async () => {} }) } as any)
const step = (part: string, input: number) => ({type:"message.part.updated",properties:{part:{id:part,sessionID:"ses",messageID:"msg-"+part,type:"step-finish",reason:"stop",cost:0,tokens:{input,output:0,reasoning:0,cache:{read:0,write:0}}}}})
await hooks.event({event:step("w1", 10)})
await hooks.event({event:step("w2", 20)})
let rejected = false
try { await hooks.event({event:step("w2", 20)}) } catch { rejected = true }
if (!rejected) throw new Error("injected duplicate callback failure was not returned")
await hooks.event({event:step("w3", 30)})
console.log("ok")
`
	if err := os.WriteFile(queueDriverPath, []byte(queueDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Run("required callback failure does not poison later queue work", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cmd := exec.Command(bun, queueDriverPath)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "KTX_TEST_LOG="+logPath,
			"KTX_BENCH_SESSION_ID=aaaaaaaaaaaaaaaa", "KTX_BENCH_EVENT_INTERVAL_ID=bbbbbbbbbbbbbbbb",
			"KTX_BENCH_EVENT_CONTRACT=opencode-event-v2", "KTX_BENCH_COLLECTOR_MODE=required", "KTX_TEST_EVENT_FAIL_ORDINAL=3")
		output, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "ok" {
			t.Fatalf("queue driver: %v\n%s", err, output)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		calls := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(calls) != 5 || !strings.Contains(calls[3], `"id":"w2"`) || !strings.Contains(calls[4], `"id":"w3"`) || !strings.Contains(calls[4], `"epoch_ordinal":4`) || !strings.Contains(calls[4], `"after_ordinal":3`) {
			t.Fatalf("queue calls=%q", calls)
		}
	})

	epochDriverPath := filepath.Join(dir, "epoch-driver.ts")
	epochDriver := `import plugin from ` + strconv.Quote(pluginPath) + `
const load = () => plugin({ $: () => ({ quiet: async () => {} }) } as any)
const event = {type:"message.updated",properties:{info:{id:"msg",sessionID:"ses",role:"user"}}}
const first: any = await load()
if (process.env.KTX_TEST_EPOCH_SCENARIO === "sequential") {
  await first.event({event})
  const second: any = await load()
  await second.event({event})
} else {
  await load()
  let rejected = false
  try { await first.event({event}) } catch { rejected = true }
  if (!rejected) throw new Error("overlapping epoch callback succeeded")
}
console.log("ok")
`
	if err := os.WriteFile(epochDriverPath, []byte(epochDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	runEpochDriver := func(t *testing.T, scenario string) ([]string, string) {
		t.Helper()
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cmd := exec.Command(bun, epochDriverPath)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "KTX_TEST_LOG="+logPath,
			"KTX_BENCH_SESSION_ID=aaaaaaaaaaaaaaaa", "KTX_BENCH_EVENT_INTERVAL_ID=bbbbbbbbbbbbbbbb",
			"KTX_BENCH_EVENT_CONTRACT=opencode-event-v2", "KTX_BENCH_COLLECTOR_MODE=required", "KTX_TEST_EPOCH_SCENARIO="+scenario)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("epoch driver: %v\n%s", err, output)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n"), logPath
	}

	t.Run("required second sequential epoch is a restart", func(t *testing.T) {
		calls, _ := runEpochDriver(t, "sequential")
		if len(calls) != 4 || !strings.HasPrefix(calls[0], "telemetry collector-open\t") ||
			!strings.Contains(calls[1], `"after_ordinal":1`) ||
			!strings.HasPrefix(calls[2], "telemetry collector-open\t") ||
			!strings.Contains(calls[3], `"after_ordinal":3`) {
			t.Fatalf("sequential epoch calls=%q", calls)
		}
	})

	t.Run("required overlapping old epoch callback fails before allocation", func(t *testing.T) {
		calls, logPath := runEpochDriver(t, "overlap")
		if len(calls) != 3 || !strings.Contains(calls[2], `"after_ordinal":1`) {
			t.Fatalf("overlapping epoch calls=%q", calls)
		}
		state, err := os.ReadFile(logPath + ".ordinal")
		if err != nil || strings.TrimSpace(string(state)) != "2" {
			t.Fatalf("ledger state=%q err=%v, want unchanged ordinal 2", state, err)
		}
	})

	t.Run("required benchmark failure rejects plugin startup", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		cmd := exec.Command(bun, driverPath)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "KTX_TEST_LOG="+logPath,
			"KTX_BENCH_SESSION_ID=aaaaaaaaaaaaaaaa", "KTX_BENCH_EVENT_INTERVAL_ID=bbbbbbbbbbbbbbbb",
			"KTX_BENCH_EVENT_CONTRACT=opencode-event-v2", "KTX_BENCH_COLLECTOR_MODE=required", "KTX_TEST_TELEMETRY_FAIL=1")
		if err := cmd.Run(); err == nil {
			t.Fatal("required collector failure did not reject plugin startup")
		}
	})

	for _, bad := range []string{
		"identity-malformed", "identity-path", "identity-digest", "identity-schema",
		"collector-session", "collector-interval", "collector-epoch", "collector-kind", "collector-ordinal", "collector-type", "collector-contract", "collector-malformed", "collector-missing",
		"event-session", "event-interval", "event-epoch", "event-kind", "event-contract", "event-missing", "hook-envelope",
	} {
		t.Run("required rejects "+bad, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "calls.log")
			cmd := exec.Command(bun, driverPath)
			cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "KTX_TEST_LOG="+logPath,
				"KTX_BENCH_SESSION_ID=aaaaaaaaaaaaaaaa", "KTX_BENCH_EVENT_INTERVAL_ID=bbbbbbbbbbbbbbbb",
				"KTX_BENCH_EVENT_CONTRACT=opencode-event-v2", "KTX_BENCH_COLLECTOR_MODE=required", "KTX_TEST_BAD_RESPONSE="+bad)
			if err := cmd.Run(); err == nil {
				t.Fatalf("required mode accepted %s", bad)
			}
		})
	}

	for _, test := range []struct {
		name, response string
	}{
		{"same ordinal", "event-same"},
		{"backward ordinal", "event-backward"},
		{"gap of two", "event-gap-two"},
		{"audit jump from one to ninety nine", "event-gap-99"},
		{"unsafe large ordinal", "event-large"},
		{"fractional ordinal", "event-fractional"},
		{"string ordinal", "event-string"},
		{"null ordinal", "event-null"},
		{"non finite ordinal", "event-nonfinite"},
	} {
		t.Run("required rejects "+test.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "calls.log")
			cmd := exec.Command(bun, driverPath)
			cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "KTX_TEST_LOG="+logPath,
				"KTX_BENCH_SESSION_ID=aaaaaaaaaaaaaaaa", "KTX_BENCH_EVENT_INTERVAL_ID=bbbbbbbbbbbbbbbb",
				"KTX_BENCH_EVENT_CONTRACT=opencode-event-v2", "KTX_BENCH_COLLECTOR_MODE=required", "KTX_TEST_BAD_RESPONSE="+test.response)
			if err := cmd.Run(); err == nil {
				t.Fatalf("required mode accepted %s", test.name)
			}
		})
	}

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

func countFixtureLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func canonicalMeasurementStates(observation telemetry.Observation) map[string]string {
	return map[string]string{
		"cost": string(observation.Cost.State), "input": string(observation.Tokens.Input.State),
		"output": string(observation.Tokens.Output.State), "reasoning": string(observation.Tokens.Reasoning.State),
		"cache_read": string(observation.Tokens.CacheRead.State), "cache_write": string(observation.Tokens.CacheWrite.State),
	}
}
