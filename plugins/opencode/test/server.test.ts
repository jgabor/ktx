// Tests for the server target. A fake ktx on PATH stands in for the binary, so
// no test opens a ktx database or reaches a provider.
import assert from "node:assert/strict"
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { delimiter, join } from "node:path"
import { afterEach, beforeEach, test, type TestContext } from "node:test"
import KtxPlugin from "../server.ts"

// The fake answers `db path`, `hook`, and `telemetry record --ack` the way ktx
// does, and logs each call with its stdin payload.
const fakeKtx = `#!${process.execPath}
const fs = require("node:fs")
const args = process.argv.slice(2)
const payload = args[0] === "db" ? "" : fs.readFileSync(0, "utf8")
fs.appendFileSync(process.env.FAKE_KTX_LOG, JSON.stringify({ args, payload }) + "\\n")
const reply = () => {
  if (args.join(" ") === "db path") return console.log(process.env.FAKE_KTX_DB)
  if (args.join(" ") === "hook") {
    if (process.env.FAKE_KTX_HOOK === "fail") {
      console.error("hook failed")
      return 3
    }
    return console.log(JSON.stringify({ output: "compressed", compressed_id: "abcdefghabcdefgh", source_id: "sourceabcdefghij" }))
  }
  if (args.join(" ") === "telemetry record --ack") {
    const observation = JSON.parse(payload)
    observation.provider ??= null
    observation.model ??= null
    observation.runtime = "opencode"
    observation.runtime_version = process.env.FAKE_KTX_RUNTIME_VERSION || "1.18.5"
    observation.source = "event_hook"
    return console.log(JSON.stringify({ schema: "ktx.telemetry-ack.v1", ok: true, durable: true, observation }))
  }
  return 9
}
process.exitCode = reply() ?? 0
`

const ownedVariables = [
  "PATH", "KTX_DB", "KTX_SESSION_ID", "KTX_COLLECTION_ID", "KTX_BENCH_COLLECTOR_MODE",
  "KTX_BENCH_READ_DESCRIPTION", "FAKE_KTX_LOG", "FAKE_KTX_DB", "FAKE_KTX_HOOK", "FAKE_KTX_RUNTIME_VERSION",
]
let saved: Record<string, string | undefined> = {}
let directory = ""
let log = ""

beforeEach(() => {
  saved = Object.fromEntries(ownedVariables.map((name) => [name, process.env[name]]))
  for (const name of ownedVariables) if (name !== "PATH") delete process.env[name]
  directory = mkdtempSync(join(tmpdir(), "ktx-opencode-server-"))
  writeFileSync(join(directory, "package.json"), '{"type":"commonjs"}\n')
  writeFileSync(join(directory, "ktx"), fakeKtx, { mode: 0o755 })
  writeFileSync(join(directory, "ktx.db"), "")
  log = join(directory, "calls.jsonl")
  process.env.PATH = directory + delimiter + (saved.PATH ?? "")
  process.env.FAKE_KTX_LOG = log
  process.env.FAKE_KTX_DB = join(directory, "ktx.db")
})

afterEach(() => {
  for (const [name, value] of Object.entries(saved)) {
    if (value === undefined) delete process.env[name]
    else process.env[name] = value
  }
  rmSync(directory, { recursive: true, force: true })
})

const calls = (command: string): { args: string[]; payload: string }[] => {
  let text = ""
  try { text = readFileSync(log, "utf8") } catch { return [] }
  return text.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line))
    .filter((call) => call.args.join(" ") === command)
}

const plugin = async (): Promise<any> => KtxPlugin({} as never)

const quiet = (t: TestContext) => ({
  error: t.mock.method(console, "error", () => {}),
  warn: t.mock.method(console, "warn", () => {}),
})

const messages = (mock: { mock: { calls: { arguments: unknown[] }[] } }): string[] =>
  mock.mock.calls.map((call) => call.arguments.map(String).join(" "))

const assistantMessage = { type: "message.updated", properties: { info: {
  role: "assistant", sessionID: "ses_1", id: "msg_1", providerID: "openai", modelID: "gpt-test",
} } }
const modelStep = { type: "message.part.updated", properties: { part: {
  type: "step-finish", sessionID: "ses_1", messageID: "msg_1", id: "prt_1", reason: "stop",
  cost: 0.25, tokens: { input: 10, output: 5, reasoning: 0, cache: { read: 2, write: 1 } },
} } }

test("ktx on PATH registers the telemetry and compression hooks", async (t) => {
  quiet(t)
  const hooks = await plugin()
  assert.equal(typeof hooks.event, "function")
  assert.equal(typeof hooks["tool.execute.after"], "function")
})

test("without ktx on PATH the plugin registers no hooks and says so", async (t) => {
  const logged = quiet(t)
  process.env.PATH = directory + "-missing"
  assert.deepEqual(await plugin(), {})
  assert.match(messages(logged.warn).join("\n"), /\[ktx\] ktx candidate verification failed; plugin disabled/)
})

test("compression replaces tool output with the ktx hook envelope", async (t) => {
  quiet(t)
  const hooks = await plugin()
  const output = { title: "bash", output: "original output", metadata: {} }
  await hooks["tool.execute.after"]({ tool: "bash", sessionID: "ses_1", callID: "call_1" }, output)
  assert.equal(output.output, "compressed")
  const [hook] = calls("hook")
  const record = JSON.parse(hook.payload)
  assert.equal(record.runtime, "opencode")
  assert.equal(record.input.callID, "call_1")
  assert.equal(record.output.output, "original output")
})

test("compression leaves tool output unchanged when ktx hook fails", async (t) => {
  const logged = quiet(t)
  process.env.FAKE_KTX_HOOK = "fail"
  const hooks = await plugin()
  const output = { title: "bash", output: "original output", metadata: {} }
  await hooks["tool.execute.after"]({ tool: "bash", sessionID: "ses_1", callID: "call_1" }, output)
  assert.equal(output.output, "original output")
  assert.equal(calls("hook").length, 1)
  assert.match(messages(logged.error).join("\n"), /\[ktx\] hook failed \(exit 3\): hook failed/)
})

test("an ordinary wrapper session owns evidence, so the hooks call no ktx", async (t) => {
  quiet(t)
  const hooks = await plugin()
  process.env.KTX_SESSION_ID = "aaaaaaaaaaaaaaaa"
  const output = { title: "bash", output: "original output", metadata: {} }
  await hooks["tool.execute.after"]({ tool: "bash", sessionID: "ses_1", callID: "call_1" }, output)
  await hooks.event({ event: modelStep })
  assert.equal(output.output, "original output")
  assert.equal(calls("hook").length + calls("telemetry record --ack").length, 0)
})

test("model-step telemetry records once and retires the delivery on a matching acknowledgement", async (t) => {
  const logged = quiet(t)
  const hooks = await plugin()
  await hooks.event({ event: assistantMessage })
  await hooks.event({ event: modelStep })
  await hooks.event({ event: modelStep })
  const records = calls("telemetry record --ack")
  assert.equal(records.length, 1)
  const observation = JSON.parse(records[0].payload)
  assert.equal(observation.ordinal, 1)
  assert.equal(observation.provider, "openai")
  assert.equal(observation.model, "gpt-test")
  assert.deepEqual(observation.cost, { state: "measured", value: 0.25 })
  assert.deepEqual(observation.tokens, {
    input: { state: "measured", value: 10 }, output: { state: "measured", value: 5 },
    reasoning: { state: "measured", value: 0 }, cache_read: { state: "measured", value: 2 },
    cache_write: { state: "measured", value: 1 },
  })
  assert.deepEqual(messages(logged.error), [])
})

test("model-step telemetry keeps the delivery for retry when the acknowledgement does not match", async (t) => {
  const logged = quiet(t)
  process.env.FAKE_KTX_RUNTIME_VERSION = "0.0.0"
  const hooks = await plugin()
  await hooks.event({ event: modelStep })
  await hooks.event({ event: modelStep })
  const records = calls("telemetry record --ack")
  assert.equal(records.length, 2)
  assert.equal(records[0].payload, records[1].payload)
  assert.match(messages(logged.error).join("\n"), /\[ktx\] telemetry persistence not acknowledged; awaiting redelivery/)
})

test("a normal session leaves the benchmark read-description treatment inactive", async (t) => {
  quiet(t)
  const hooks = await plugin()
  assert.equal("tool.definition" in hooks, false)
})

test("the read-description treatment refuses to start outside an owned benchmark", async (t) => {
  quiet(t)
  process.env.KTX_BENCH_READ_DESCRIPTION = "candidate"
  await assert.rejects(plugin(), /read-description treatment requires an owned benchmark arm/)
  assert.equal(calls("hook").length, 0)
})
