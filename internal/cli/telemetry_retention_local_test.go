package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Expose only the generated collector's owned state for retention assertions.
// The installed production adapter has no inspection hook.
func inspectTelemetryCollector(t *testing.T, plugin string, retainAcceptedPayload bool) {
	t.Helper()
	data, err := os.ReadFile(plugin)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	const boundary = "  return {\n    \"event\":"
	if strings.Count(source, boundary) != 1 {
		t.Fatal("generated collector inspection boundary changed")
	}
	source = strings.Replace(source, boundary, "  return {\n    __collectorState: () => ({ observed, identities, pendingRequired, ordinal }),\n    \"event\":", 1)
	if retainAcceptedPayload {
		// Paired control: restore only the old post-ACK retention operation.
		const release = "observed.set(key, { evidence: delivery.evidence, accepted: true })"
		if strings.Count(source, release) != 1 {
			t.Fatal("generated collector ACK transition changed")
		}
		source = strings.Replace(source, release, "delivery.accepted = true", 1)
	}
	if err := os.WriteFile(plugin, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTelemetryAdapterLocalPayloadRelease(t *testing.T) {
	root, _, node, plugin, path := localTelemetryAdapter(t, false)
	inspectTelemetryCollector(t, plugin, false)
	driver := `import plugin from ` + strconv.Quote(plugin) + `
import {DatabaseSync as Database} from "node:sqlite"
const db=new Database(` + strconv.Quote(path) + `)
const hooks:any=await plugin({} as any)
const state=hooks.__collectorState()
const event={type:"message.part.updated",properties:{part:{type:"step-finish",sessionID:"runtime",messageID:"message",id:"part",reason:"stop",cost:0.25,tokens:{input:10,output:2,reasoning:0,cache:{read:0,write:0}}}}}
const key=JSON.stringify(["runtime","message","part"])
let started:()=>void, release:()=>void
const starting=new Promise<void>(resolve=>started=resolve)
const gate=new Promise<void>(resolve=>release=resolve)
` + adapterSpawnTap + `
let deliveries=0, spawns=0
childProcess.spawn=((command:any,args:any,options:any)=>{
  spawns++
  const proc=originalSpawn(command,args,options)
  if(args[0]!=="telemetry" || args[1]!=="record") return proc
  const number=++deliveries
  return tapProcess(proc,()=>{},async(body,code)=>{
    if(number===1){ started(); await gate }
    // Commit through the real CLI, but lose the first ACK in transport.
    return [number===1 ? "" : body,code]
  })
}) as typeof childProcess.spawn
const diagnostics:string[]=[]
console.error=(message:any)=>diagnostics.push(String(message))
const first=hooks.event({event})
await starting
const pending=state.observed.get(key)
if(!pending || pending.accepted || typeof pending.payload!=="string") throw new Error("in-flight payload missing")
const evidence=pending.evidence, payload=pending.payload
const observation=JSON.parse(payload)
if(observation.ordinal!==1 || !observation.captured_at) throw new Error("missing first order/time")
release(); await first
if(state.observed.get(key)!==pending || pending.payload!==payload || pending.accepted) throw new Error("uncertain ACK replaced pending state")
if(!diagnostics.some(s=>s.includes("not acknowledged"))) throw new Error("missing uncertain ACK diagnostic")
await hooks.event({event:{type:"message.updated",properties:{info:{id:"message",sessionID:"runtime",role:"assistant",providerID:"late",modelID:"late"}}}})
await Promise.all([hooks.event({event}),hooks.event({event})])
const accepted=state.observed.get(key)
if(!accepted?.accepted || Object.hasOwn(accepted,"payload") || accepted.evidence!==evidence || state.observed.size!==1) throw new Error("valid ACK did not release only payload")
const before=spawns
await hooks.event({event})
if(spawns!==before || deliveries!==2) throw new Error("accepted retry spawned a process")
const conflict=structuredClone(event); conflict.properties.part.cost=9
await hooks.event({event:conflict})
if(!diagnostics.some(s=>s.includes("conflicting model-step redelivery")) || spawns!==before || state.observed.get(key)!==accepted) throw new Error("accepted conflict not preserved")
const rows:any[]=db.prepare("SELECT ordinal,captured_at,cost,provider,model FROM session_telemetry").all()
if(rows.length!==1 || rows[0].ordinal!==observation.ordinal || rows[0].captured_at!==observation.captured_at || rows[0].cost!==0.25 || rows[0].provider!==null || rows[0].model!==null) throw new Error("first durable evidence changed: "+JSON.stringify(rows))
const identity=state.identities.get(JSON.stringify(["runtime","message"]))
if(identity?.provider!=="late" || identity?.model!=="late") throw new Error("late assistant association lost")
const later=structuredClone(event); later.properties.part.id="later"
await hooks.event({event:later})
const row:any=db.prepare("SELECT ordinal,provider,model FROM session_telemetry WHERE part_id='later'").get()
if(row.ordinal!==2 || row.provider!=="late" || row.model!=="late") throw new Error("late identity unavailable to later part")
if(state.pendingRequired.size!==0) throw new Error("optional collection changed required pending state")
db.close()
console.log("payload release, uncertain/in-flight retention, exact retry, accepted conflict, overlap and late identity verified")
`
	runTelemetryDriver(t, root, node, driver)
}

func TestTelemetryAdapterLocalPairedRetention(t *testing.T) {
	if os.Getenv("KTX_ADAPTER_CLI") == "" {
		t.Skip("set KTX_ADAPTER_CLI to the locally built binary")
	}
	type retention struct {
		EvidenceBytes    int `json:"evidenceBytes"`
		PayloadBytes     int `json:"payloadBytes"`
		IdentityBytes    int `json:"identityBytes"`
		KeyBytes         int `json:"keyBytes"`
		TotalStringBytes int `json:"totalStringBytes"`
		SentBytes        int `json:"sentBytes"`
	}
	var before, after retention
	for _, retain := range []bool{true, false} {
		name := "released"
		if retain {
			name = "retained-control"
		}
		t.Run(name, func(t *testing.T) {
			root, _, node, plugin, path := localTelemetryAdapter(t, false)
			inspectTelemetryCollector(t, plugin, retain)
			measurement := filepath.Join(root, "retention.json")
			driver := `import plugin from ` + strconv.Quote(plugin) + `
import {DatabaseSync as Database} from "node:sqlite"
import {writeFile} from "node:fs/promises"
const hooks:any=await plugin({} as any)
const retainedControl=` + strconv.FormatBool(retain) + `
const state=hooks.__collectorState()
let sentBytes=0, deliveries=0
` + adapterSpawnTap + `
childProcess.spawn=((command:any,args:any,options:any)=>{
  const proc=originalSpawn(command,args,options)
  if(args[0]!=="telemetry" || args[1]!=="record") return proc
  // Count and forward chunks, never keep a payload corpus in the harness.
  deliveries++
  return tapProcess(proc,chunk=>{sentBytes+=chunk.byteLength})
}) as typeof childProcess.spawn
for(let i=0;i<256;i++){
  const id="message-"+i
  await hooks.event({event:{type:"message.updated",properties:{info:{sessionID:"runtime",id,role:"assistant",providerID:"provider",modelID:"model"}}}})
  await hooks.event({event:{type:"message.part.updated",properties:{part:{type:"step-finish",sessionID:"runtime",messageID:id,id:"part-"+i,reason:"stop",cost:0.25,tokens:{input:10,output:2,reasoning:0,cache:{read:0,write:0}}}}}})
}
const bytes=(s:string)=>Buffer.byteLength(s)
let evidenceBytes=0, payloadBytes=0, identityBytes=0, keyBytes=0
for(const [key,value] of state.observed){
  if(!value.accepted || Object.hasOwn(value,"payload")!==retainedControl) throw new Error("incorrect acknowledged retention")
  keyBytes+=bytes(key); evidenceBytes+=bytes(value.evidence)
  if(retainedControl) payloadBytes+=bytes(value.payload)
}
for(const [key,value] of state.identities){keyBytes+=bytes(key);identityBytes+=bytes(value.provider)+bytes(value.model)}
if(deliveries!==256 || state.observed.size!==256 || state.identities.size!==256 || state.pendingRequired.size!==0) throw new Error("identity membership changed")
if(payloadBytes!==(retainedControl?sentBytes:0)) throw new Error("retained payload byte accounting differs from actual writes")
const db=new Database(` + strconv.Quote(path) + `)
const row:any=db.prepare("SELECT count(*) AS n,min(ordinal) AS first,max(ordinal) AS last FROM session_telemetry").get()
if(row.n!==256 || row.first!==1 || row.last!==256) throw new Error("paired workload did not persist exactly once")
db.close()
const measurement=JSON.stringify({variant:retainedControl?"retained-control":"released",pairs:256,deliveries,evidenceBytes,payloadBytes,identityBytes,keyBytes,totalStringBytes:evidenceBytes+payloadBytes+identityBytes+keyBytes,sentBytes})
await writeFile(` + strconv.Quote(measurement) + `,measurement)
console.log(measurement)
`
			runTelemetryDriver(t, root, node, driver)
			data, err := os.ReadFile(measurement)
			if err != nil {
				t.Fatal(err)
			}
			result := &after
			if retain {
				result = &before
			}
			if err := json.Unmarshal(data, result); err != nil {
				t.Fatal(err)
			}
		})
	}
	if before.EvidenceBytes != after.EvidenceBytes || before.IdentityBytes != after.IdentityBytes || before.KeyBytes != after.KeyBytes || before.SentBytes != after.SentBytes {
		t.Fatalf("paired workload evidence changed: before=%+v after=%+v", before, after)
	}
	if before.PayloadBytes == 0 || after.PayloadBytes != 0 || before.TotalStringBytes-after.TotalStringBytes != before.PayloadBytes {
		t.Fatalf("retention did not decrease by exactly the acknowledged payload bytes: before=%+v after=%+v", before, after)
	}
}
