package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/telemetry"
	"github.com/jgabor/ktx/internal/telemetry/opencode"
	"github.com/jgabor/ktx/internal/testjs"
)

type durableCase struct {
	Name       string                     `json:"name"`
	Required   bool                       `json:"required"`
	State      telemetry.MeasurementState `json:"state"`
	Event      json.RawMessage            `json:"event"`
	JavaScript string
}

// adapterSpawnTap wraps the generated adapter's real ktx processes. A tapped
// process forwards stdin through the harness and may rewrite stdout and the exit
// code after the real process closes. Every write still reaches the local CLI.
const adapterSpawnTap = `
import childProcess from "node:child_process"
import {EventEmitter} from "node:events"
import {PassThrough} from "node:stream"
const originalSpawn=childProcess.spawn
const tapProcess=(proc:any,onChunk:(chunk:Buffer)=>void,respond?:(stdout:string,code:number|null)=>Promise<[string,number|null]>)=>{
  const child:any=new EventEmitter()
  const input=new PassThrough()
  proc.stdin.on("error",()=>{})
  input.on("data",onChunk)
  input.pipe(proc.stdin)
  child.stdin=input; child.stderr=proc.stderr; child.stdout=new PassThrough()
  const stdout:Buffer[]=[]
  proc.stdout.on("data",(chunk:Buffer)=>stdout.push(chunk))
  proc.on("error",(error:Error)=>child.emit("error",error))
  proc.on("close",async(code:number|null,signal:string|null)=>{
    const body=Buffer.concat(stdout).toString()
    const [rewritten,exitCode]=respond ? await respond(body,code) : [body,code]
    child.stdout.on("end",()=>child.emit("close",exitCode,signal))
    child.stdout.end(rewritten)
  })
  return child
}
const tapPayload=(proc:any,payloads:Promise<string>[],respond?:(stdout:string,code:number|null)=>Promise<[string,number|null]>)=>{
  const chunks:Buffer[]=[]
  const child=tapProcess(proc,chunk=>chunks.push(chunk),respond)
  payloads.push(new Promise(resolve=>child.stdin.on("end",()=>resolve(Buffer.concat(chunks).toString()))))
  return child
}
`

type telemetryDiagnosticWriter func([]byte) (int, error)

func (w telemetryDiagnosticWriter) Write(p []byte) (int, error) { return w(p) }

func TestWrapperTelemetryLocalDurableRedelivery(t *testing.T) {
	root, binary, _, _, path := localTelemetryAdapter(t, false)
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB().Exec(`CREATE TRIGGER fail_telemetry BEFORE INSERT ON session_telemetry BEGIN SELECT RAISE(FAIL, 'controlled persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(root, "continue")
	runtimePath := filepath.Join(root, "opencode")
	conflict := strings.Replace(durableStep, `"cost":0.25`, `"cost":9`, 1)
	script := "#!/bin/sh\nprintf '%s\\n' '" + durableStep + "'\nwhile [ ! -e " + strconv.Quote(release) + " ]; do sleep 0.01; done\nprintf '%s\\n' '" + conflict + "' '" + durableStep + "' '" + durableStep + "'\n"
	if err := os.WriteFile(runtimePath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "--quiet", runtimePath)
	cmd.Dir = root
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	var recoveredAt time.Time
	cmd.Stdout = &stdout
	cmd.Stderr = telemetryDiagnosticWriter(func(p []byte) (int, error) {
		_, _ = stderr.Write(p)
		if recoveredAt.IsZero() && strings.Contains(stderr.String(), "controlled persistence failure") {
			recoveredAt = time.Now().UTC()
			if _, err := d.DB().Exec(`DROP TRIGGER fail_telemetry`); err != nil {
				return 0, err
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	})
	if err := cmd.Run(); err != nil {
		t.Fatalf("local wrapper: %v stdout=%s stderr=%s", err, &stdout, &stderr)
	}
	var count int
	var cost float64
	var capturedAt string
	if err := d.DB().QueryRow(`SELECT count(*),sum(cost),min(captured_at) FROM session_telemetry`).Scan(&count, &cost, &capturedAt); err != nil {
		t.Fatal(err)
	}
	when, err := time.Parse(time.RFC3339Nano, capturedAt)
	if err != nil || count != 1 || cost != 0.25 || !when.Before(recoveredAt) {
		t.Fatalf("count=%d cost=%f captured=%s recovered=%s err=%v", count, cost, capturedAt, recoveredAt, err)
	}
	if stdout.String() != durableStep+"\n"+conflict+"\n"+durableStep+"\n"+durableStep+"\n" {
		t.Fatalf("wrapper forwarding changed: %s", &stdout)
	}
}

func durableCases(t *testing.T) []durableCase {
	t.Helper()
	data, err := os.ReadFile("../telemetry/opencode/testdata/durable_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []durableCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return append(cases, durableNumberCases(t, cases[4].Event)...)
}

// The same cases feed Go and the real generated adapter. NaN has no JSON
// number representation, so Go receives the existing malformed string form.
func durableNumberCases(t *testing.T, base json.RawMessage) []durableCase {
	t.Helper()
	data, err := os.ReadFile("../telemetry/opencode/testdata/durable_numbers.json")
	if err != nil {
		t.Fatal(err)
	}
	var numbers []struct {
		Name       string
		JSON       json.RawMessage
		JavaScript string
		State      telemetry.MeasurementState
		CostState  telemetry.MeasurementState `json:"cost_state"`
	}
	if err := json.Unmarshal(data, &numbers); err != nil {
		t.Fatal(err)
	}
	var cases []durableCase
	for _, number := range numbers {
		for _, field := range []string{"cost", "tokens", "tokens.input", "tokens.output", "tokens.reasoning", "tokens.cache", "tokens.cache.read", "tokens.cache.write"} {
			var event map[string]any
			if err := json.Unmarshal(base, &event); err != nil {
				t.Fatal(err)
			}
			part := event["properties"].(map[string]any)["part"].(map[string]any)
			target := part
			path := strings.Split(field, ".")
			for _, key := range path[:len(path)-1] {
				target = target[key].(map[string]any)
			}
			target[path[len(path)-1]] = "$measurement"
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			raw := strings.Replace(string(encoded), `"$measurement"`, string(number.JSON), 1)
			js := number.JavaScript
			if js == "" {
				js = string(number.JSON)
			}
			js = strings.Replace(string(encoded), `"$measurement"`, js, 1)
			state := number.State
			if field == "cost" {
				state = number.CostState
			} else if (field == "tokens" || field == "tokens.cache") && state == telemetry.MeasurementMeasured {
				state = telemetry.MeasurementMalformed // Numbers are not measurement containers.
			}
			want, err := opencode.DecodeModelStep([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			for key, got := range map[string]telemetry.MeasurementState{
				"cost": want.Cost.State, "tokens.input": want.Tokens.Input.State,
				"tokens.output": want.Tokens.Output.State, "tokens.reasoning": want.Tokens.Reasoning.State,
				"tokens.cache.read": want.Tokens.CacheRead.State, "tokens.cache.write": want.Tokens.CacheWrite.State,
			} {
				if (key == field || strings.HasPrefix(key, field+".")) && got != state {
					t.Fatalf("%s/%s: %s state=%s want=%s", number.Name, field, key, got, state)
				}
			}
			cases = append(cases, durableCase{
				Name:  "number/" + number.Name + "/" + field,
				Event: json.RawMessage(raw), JavaScript: js, State: want.Cost.State, Required: state == telemetry.MeasurementMeasured,
			})
		}
	}
	return cases
}

func telemetryOwner(t *testing.T, path string, required bool) (*dbpkg.DB, string) {
	t.Helper()
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	t.Setenv(benchcontract.CollectorModeEnv, "")
	if !required {
		if err := d.InsertSession(&dbpkg.Session{ID: "owner", Agent: "opencode"}); err != nil {
			t.Fatal(err)
		}
		return d, "owner"
	}
	i, err := d.CreateBenchmarkEventInterval("attempt", benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(benchcontract.CollectorModeEnv, benchcontract.CollectorRequired)
	t.Setenv(benchcontract.SessionEnv, i.SessionID)
	t.Setenv(benchcontract.IntervalEnv, i.ID)
	t.Setenv(benchcontract.ContractEnv, benchcontract.ContractVersion)
	return d, i.SessionID
}

func assertDurableObservation(t *testing.T, row dbpkg.SessionTelemetry, want telemetry.Observation) {
	t.Helper()
	// Round-trip through the common JSON measurement representation, rather than
	// comparing the distinct database and normalizer Go types.
	data, err := json.Marshal(map[string]any{
		"session_id": row.SourceSessionID, "message_id": row.MessageID, "part_id": row.PartID,
		"ordinal": row.Ordinal, "reason": row.Reason, "cost": row.Cost,
		"tokens":   map[string]any{"input": row.UncachedInput, "output": row.Output, "reasoning": row.Reasoning, "cache_read": row.CacheRead, "cache_write": row.CacheWrite},
		"provider": row.Provider, "model": row.Model, "runtime": row.Runtime, "runtime_version": row.RuntimeVersion, "source": row.Source,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got telemetry.Observation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want.Ordinal = row.Ordinal
	want.Source = telemetry.SourceEventHook
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("durable observation differs:\n got %s\nwant %+v", data, want)
	}
}

func TestTelemetryDurableSharedConformance(t *testing.T) {
	for _, test := range durableCases(t) {
		for _, required := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/required=%t", test.Name, required), func(t *testing.T) {
				d, owner := telemetryOwner(t, filepath.Join(t.TempDir(), "fixture.db"), required)
				var stdout, stderr bytes.Buffer
				c := opencode.NewCollector()
				err := c.Collect(test.Event, func(o *telemetry.Observation, at string) error {
					if o.Cost.State != test.State {
						t.Fatalf("cost state = %s", o.Cost.State)
					}
					data, _ := json.Marshal(o)
					var input map[string]any
					if err := json.Unmarshal(data, &input); err != nil {
						return err
					}
					input["ktx_session_id"], input["captured_at"] = owner, at
					data, _ = json.Marshal(input)
					_, err := telemetryLogic([]string{"record", "--ack"}, FormatJSON, bytes.NewReader(data), d, &stdout, &stderr)
					return err
				})
				if (err == nil) != (!required || test.Required) {
					t.Fatalf("required=%t err=%v ack=%s", required, err, &stdout)
				}
				rows, err := d.ListSessionTelemetry(owner, 100)
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows=%+v err=%v", rows, err)
				}
				want, err := opencode.DecodeModelStep(test.Event)
				if err != nil {
					t.Fatal(err)
				}
				assertDurableObservation(t, rows[0], want)
			})
		}
	}
}

func localTelemetryAdapter(t *testing.T, required bool) (root, binary, node, plugin, path string) {
	t.Helper()
	binary = os.Getenv("KTX_ADAPTER_CLI")
	if binary == "" {
		t.Skip("set KTX_ADAPTER_CLI to the locally built binary")
	}
	node = testjs.Runtime(t, "node", true)
	root = t.TempDir()
	testjs.WriteModulePackage(t, root)
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "TMPDIR"} {
		p := filepath.Join(root, key)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, p)
	}
	if _, present := os.LookupEnv("KTX_DB"); present {
		t.Fatal("unset KTX_DB for local verification")
	}
	t.Setenv("KTX_SESSION_ID", "")
	t.Setenv("KTX_COLLECTION_ID", "")
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	path = filepath.Join(os.Getenv("XDG_DATA_HOME"), "ktx", "ktx.db")
	d, _ := telemetryOwner(t, path, required)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "init", "opencode")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("local install: %v: %s", err, out)
	}
	plugin = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "plugins", "ktx.ts")
	return
}

func runTelemetryDriver(t *testing.T, root, node, driver string) {
	t.Helper()
	path := filepath.Join(root, "driver.ts")
	if err := os.WriteFile(path, []byte(driver), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, testjs.DisableExperimentalWarning, path)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real adapter/CLI: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}

func TestTelemetryAdapterLocalConformance(t *testing.T) {
	for _, test := range durableCases(t) {
		for _, required := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/required=%t", test.Name, required), func(t *testing.T) {
				root, _, node, plugin, path := localTelemetryAdapter(t, required)
				event := test.JavaScript
				if event == "" {
					event = string(test.Event)
				}
				driver := `import plugin from ` + strconv.Quote(plugin) + `
const hooks:any = await plugin({} as any)
let rejected=false
try { await hooks.event({event:` + event + `}) } catch { rejected=true }
if(rejected !== ` + strconv.FormatBool(required && !test.Required) + `) throw new Error("incorrect required measurement policy: "+rejected)
console.log("measurement policy verified")
`
				runTelemetryDriver(t, root, node, driver)
				d, err := dbpkg.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				var owner string
				if err := d.DB().QueryRow(`SELECT session_id FROM session_telemetry`).Scan(&owner); err != nil {
					t.Fatal(err)
				}
				rows, err := d.ListSessionTelemetry(owner, 100)
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows=%+v err=%v", rows, err)
				}
				want, err := opencode.DecodeModelStep(test.Event)
				if err != nil {
					t.Fatal(err)
				}
				assertDurableObservation(t, rows[0], want)
			})
		}
	}
}

func TestTelemetryAdapterLocalNonfiniteSnapshot(t *testing.T) {
	root, _, node, plugin, path := localTelemetryAdapter(t, true)
	driver := `import plugin from ` + strconv.Quote(plugin) + `
import {DatabaseSync as Database} from "node:sqlite"
` + adapterSpawnTap + `
const payloads:Promise<string>[]=[]
childProcess.spawn=((command:any,args:any,options:any)=>{
  const proc=originalSpawn(command,args,options)
  if(args[0]!=="telemetry" || args[1]!=="event-record") return proc
  return tapPayload(proc,payloads)
}) as typeof childProcess.spawn
const hooks:any=await plugin({} as any)
const tokens={input:NaN,output:2,reasoning:0,cache:{read:0,write:0},extra:Infinity}
const event={type:"message.part.updated",cost:Infinity,properties:{metadata:tokens,part:{
  type:"step-finish",sessionID:"runtime",messageID:"message",id:"part",reason:"stop",
  cost:Infinity,tokens,extra:-Infinity,
}}}
const retry=structuredClone(event)
const expected=JSON.parse(JSON.stringify(event))
expected.properties.part.cost="Infinity"
expected.properties.part.tokens.input="NaN"
const started=Date.now()
const first=hooks.event({event})
if(event.properties.part.cost!==Infinity || !Number.isNaN(tokens.input)) throw new Error("mutated runtime event")
// The callback must already own its snapshot, even before its queued write runs.
event.properties.part.cost=1; tokens.input=1
let rejected=false
try { await first } catch { rejected=true }
if(!rejected) throw new Error("required malformed evidence accepted")
await new Promise(resolve=>setTimeout(resolve,10))
const retryAt=Date.now()
const conflict=structuredClone(retry); conflict.properties.part.cost=null as any
try { await hooks.event({event:conflict}) } catch {}
if(payloads.length!==1) throw new Error("null retry replaced malformed evidence")
rejected=false
try { await hooks.event({event:retry}) } catch { rejected=true }
if(!rejected) throw new Error("required retry failed open")
const sent=await Promise.all(payloads)
if(sent.length!==2 || sent[0]!==sent[1]) throw new Error("retry changed first payload/time")
const firstPayload=JSON.parse(sent[0])
if(JSON.stringify(firstPayload.event)!==JSON.stringify(expected)) throw new Error("snapshot changed unrelated metadata")
const captured=Date.parse(firstPayload.captured_at)
if(captured<started || captured>=retryAt) throw new Error("lost first capture time")
const db=new Database(` + strconv.Quote(path) + `)
const rows:any[]=db.prepare("SELECT cost_state,tokens_input_state,captured_at FROM session_telemetry").all()
if(rows.length!==1 || rows[0].cost_state!=="malformed" || rows[0].tokens_input_state!=="malformed" || rows[0].captured_at!==firstPayload.captured_at) throw new Error("wrong persisted evidence: "+JSON.stringify(rows))
db.close()
console.log("nonfinite snapshot, metadata, and retry verified")
`
	runTelemetryDriver(t, root, node, driver)
}

func TestTelemetryAdapterLocalDurableRedelivery(t *testing.T) {
	for _, mode := range []string{"before-commit", "overlapping", "ack-loss", "malformed-ack", "wrong-schema", "wrong-runtime", "wrong-version", "wrong-source", "wrong-key", "wrong-measurement", "not-durable", "nonzero", "required-before-commit", "required-ack-loss"} {
		t.Run(mode, func(t *testing.T) {
			required := strings.HasPrefix(mode, "required-")
			root, _, node, plugin, path := localTelemetryAdapter(t, required)
			inspectTelemetryCollector(t, plugin, false)
			var event json.RawMessage
			for _, c := range durableCases(t) {
				if c.Name == "nonzero" {
					event = c.Event
				}
			}
			driver := `import plugin from ` + strconv.Quote(plugin) + `
import {DatabaseSync as Database} from "node:sqlite"
const db=new Database(` + strconv.Quote(path) + `)
const mode=` + strconv.Quote(mode) + `
const required=` + strconv.FormatBool(required) + `
const overlapping=mode==="overlapping"
const beforeCommit=mode.endsWith("before-commit") || overlapping
if(beforeCommit) db.exec("CREATE TRIGGER fail_telemetry BEFORE INSERT ON session_telemetry BEGIN SELECT RAISE(FAIL,'controlled persistence failure'); END")
` + adapterSpawnTap + `
const payloads:Promise<string>[]=[]
let deliveries=0
// Fault only the acknowledgement transport. Every write uses the actual local CLI.
childProcess.spawn=((command:any,args:any,options:any)=>{
  const proc=originalSpawn(command,args,options)
  const selected=args[0]==="telemetry" && (args[1]==="record" || args[1]==="event-record")
  if(!selected) return proc
  const number=++deliveries
  return tapPayload(proc,payloads,async(body,code)=>{
    if(number===1 && !beforeCommit){
      if(mode.endsWith("ack-loss")) body=""
      else if(mode==="malformed-ack") body="not JSON"
      else {
        const ack=JSON.parse(body)
        if(mode==="wrong-schema") ack.schema="ktx.telemetry-ack.v999"
        if(mode==="wrong-runtime") ack.observation.runtime="forged"
        if(mode==="wrong-version") ack.observation.runtime_version="1.18.50"
        if(mode==="wrong-source") ack.observation.source="wrapper_stream"
        if(mode==="wrong-key") ack.observation.part_id="other"
        if(mode==="wrong-measurement") ack.observation.tokens.input.value=999
        if(mode==="not-durable") ack.durable=false
        body=JSON.stringify(ack)
      }
    }
    if(number===1 && overlapping) db.exec("DROP TRIGGER fail_telemetry")
    return [body,number===1 && mode==="nonzero" ? 8 : code]
  })
}) as typeof childProcess.spawn
const hooks:any=await plugin({} as any)
const state=hooks.__collectorState()
const event=` + string(event) + `
let rejected=false
const initial=hooks.event({event})
const overlap=overlapping ? hooks.event({event}) : null
try { await initial } catch { rejected=true }
if(overlap) await overlap
if(rejected!==required) throw new Error("initial failure policy "+rejected)
if(required){
  if(state.observed.size!==0 || state.pendingRequired.size!==1) throw new Error("required pending retention changed")
} else {
  const delivery=state.observed.values().next().value
  if(!delivery || delivery.accepted!==overlapping || Object.hasOwn(delivery,"payload")===overlapping) throw new Error("incorrect failed/accepted payload retention")
}
const count=()=> (db.prepare("SELECT count(*) AS n FROM session_telemetry").get() as any).n
if(count()!==((beforeCommit && !overlapping)?0:1)) throw new Error("wrong first persistence state")
if(beforeCommit && !overlapping) db.exec("DROP TRIGGER fail_telemetry")
if(!required) await hooks.event({event:{type:"message.updated",properties:{info:{id:"message",sessionID:"runtime",role:"assistant",providerID:"late",modelID:"late"}}}})
const conflict=structuredClone(event); conflict.properties.part.cost=9
try { await hooks.event({event:conflict}) } catch { if(!required) throw new Error("optional conflict escaped") }
if(deliveries!==(overlapping?2:1)) throw new Error("conflicting retry replaced pending evidence")
await Promise.all([hooks.event({event}),hooks.event({event})])
if(deliveries!==(required?3:2)) throw new Error("retry/duplicate count "+deliveries)
const sent=await Promise.all(payloads)
if(sent[0]!==sent[1]) throw new Error("retry changed first bytes or time")
if(count()!==1) throw new Error("duplicate telemetry rows")
if(required){
  if(state.pendingRequired.size!==0 || state.observed.size!==0) throw new Error("required ACK cleanup changed")
} else {
  const delivery=state.observed.values().next().value
  if(!delivery?.accepted || Object.hasOwn(delivery,"payload")) throw new Error("valid ACK retained payload")
}
const row:any=db.prepare("SELECT captured_at,cost,provider,model FROM session_telemetry").get()
const first=JSON.parse(sent[0])
if(row.captured_at!==first.captured_at || row.cost!==0.25 || row.provider!==null || row.model!==null) throw new Error("first evidence enriched/replaced: "+JSON.stringify(row))
const rollup:any=db.prepare("SELECT sum(tool_call_count) AS n FROM sessions").get()
if(rollup.n!==0) throw new Error("telemetry retry changed tool rollup")
if(required){
  const interval:any=db.prepare("SELECT event_count,contradiction_count FROM runtime_event_intervals").get()
  if(interval.event_count!==3 || interval.contradiction_count!==0) throw new Error("required callback convergence "+JSON.stringify(interval))
}
db.close()
console.log("durable retry verified: "+mode)
`
			runTelemetryDriver(t, root, node, driver)
		})
	}
}
