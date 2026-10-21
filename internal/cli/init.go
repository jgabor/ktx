// `ktx init` — the runtime adapter installer.
// It writes a thin adapter that an agent runtime loads natively, so events
// flow into the ktx store without requiring the user to wrap the agent.
//
// Three integration paths share one store: the wrapper
// (`ktx run`) parses --format json, the sidecar (`ktx write`) accepts
// caller-supplied payloads, and adapters (`ktx init`) install native
// extension points in the agent runtime. OpenCode uses an in-process
// TypeScript plugin; Codex uses a PostToolUse command hook merged into
// the shared ~/.codex/hooks.json.
//
// `ktx init opencode` installs a plugin that subscribes to `event` for
// optional model-step telemetry and `tool.execute.after` for tool-result
// compression. It delegates both paths to the ktx binary.
// The plugin noops when KTX_SESSION_ID is set so the wrapper
// path remains the single source of truth — both paths active means
// one of them wins.
//
// `ktx init codex` merges a PostToolUse hook entry into
// ~/.codex/hooks.json (global) or ./.codex/hooks.json (project).
// The hooks.json file is shared with other Codex hooks, so install
// parses, appends ktx's entry (matcher ".*", command "ktx hook"),
// and writes the merged file back — never overwriting unrelated
// hooks. Uninstall removes only ktx's entry and leaves the rest
// intact. The ktx entry is identified by the literal command
// string "ktx hook" inside the PostToolUse array; re-running
// install is a no-op when the entry is already present.
//
// Flags:
//
//	--project      install to <cwd>/.codex/hooks.json (Codex) or
//	               <cwd>/.opencode/plugins/ktx.ts (OpenCode); project scope
//	--uninstall    remove the installed adapter (Codex: scoped target;
//	               OpenCode: checks both locations)
//	--show         report whether the adapter is installed and where
//
// Default (no flag) installs to the global scope:
// $XDG_CONFIG_HOME/opencode/plugins/ktx.ts for OpenCode, or
// ~/.codex/hooks.json for Codex. All three flags are mutually
// exclusive actions; combining --project with --uninstall/--show
// still scopes the action to that location.
//
// Path resolution (Codex, global): ~/.codex/hooks.json, where ~ is
// resolved via os.UserHomeDir (no XDG for Codex's home config).
// Path resolution (Codex, project): ./.codex/hooks.json relative to
// the invocation's CWD.
//
// Path resolution (OpenCode, global):
// $XDG_CONFIG_HOME/opencode/plugins/ktx.ts (XDG Base Directory spec;
// falls back to $HOME/.config/... when XDG_CONFIG_HOME is unset, per
// the spec default).
//
// Path resolution (OpenCode, project): ./.opencode/plugins/ktx.ts
// relative to the invocation's CWD.
//
// The command parses its runtime and action flags without opening the database.
package cli

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ktxPluginFileName is the name of the OpenCode plugin file installed by
// `ktx init opencode`. The .ts extension is required by OpenCode's plugin
// loader; a .js sibling would not be loaded by the same code path.
const ktxPluginFileName = "ktx.ts"

const (
	opencodeTelemetryEvidence      = "OpenCode-normalized optional model-step token evidence"
	opencodeTelemetryClaimBoundary = "not raw provider responses; no cache hit, cache miss, native request, or cache isolation claims"
	opencodeRefreshCommand         = "ktx init opencode"
	opencodeTroubleshooting        = "docs/guides/opencode-telemetry.md#troubleshooting"
)

// initParsedArgs carries command-specific arguments to initCommandFunc.
type initParsedArgs struct {
	flags   initFlags
	runtime string
}

// ktxOpencodePluginTemplate is the TypeScript source written to the
// OpenCode plugin directory. It is embedded as a Go raw string so the
// installed plugin is byte-for-byte identical across invocations and
// platforms; the version bump on the Go side automatically refreshes
// the installed file when users re-run `ktx init opencode`.
//
// Behaviour:
//   - Listens on `event`, normalizes accepted OpenCode model-step events, and
//     shells out to `ktx telemetry record --ack`. Only a matching durable ACK
//     retires a delivery; failures leave the first payload available for retry
//     without altering runtime events.
//   - Listens on `tool.execute.after` and shells out to
//     `ktx hook` via Bun.spawn, sending the raw callback object
//     (input + output) tagged with `runtime: "opencode"`. The binary owns
//     tool-result extraction, ID generation, and timestamps.
//   - `ktx hook` persists the original and returns a JSON envelope:
//     {"output": string, "compressed_id": string|null, "source_id": string}.
//   - The plugin mutates `output.output` to the envelope's `output`
//     field (the compressed bytes the agent sees).
//   - On any ktx hook failure (non-zero exit, unparseable stdout,
//     ktx-not-in-PATH), the plugin leaves output.output unchanged —
//     conservative failure at the plugin level.
//   - Ordinary telemetry and tool-result callbacks share one queue, so one
//     callback finishes its ktx work before the next callback starts it.
//   - Noops when KTX_SESSION_ID is present for an ordinary wrapper run. An
//     explicit collection keeps the hook active under wrapper-owned lifecycle.
//
// Candidate check: the plugin factory resolves one ktx from PATH, canonicalizes
// its path, and invokes that pinned path. If resolution fails, no hooks are
// registered and a one-line warning makes the disabled state and correction visible.
const ktxOpencodePluginTemplate = `import type { Plugin } from "@opencode-ai/plugin"
import { lstat, realpath, stat } from "node:fs/promises"
import { resolve } from "node:path"

interface KtxEnvelope {
  output: string
  compressed_id: string | null
  source_id: string
	ktx_session_id?: string
	runtime_session_id?: string
	call_id?: string
	recovery_cue?: boolean
}

interface KtxSchemaIncompatibility {
  code: "schema-incompatible"
  actual_schema: number
  maximum_schema: number
  database_path: string
  executable_path: string
  message: string
  correction: string
}

interface KtxDatabaseUnavailable {
  code: "database-unavailable"
  database_path: string
  executable_path: string
  failure_class: "coordinator-timeout" | "sqlite-lock-exhaustion"
  operation_phase: "open" | "migration" | "source" | "artifact" | "disposition" | "query"
  elapsed_wait_nanoseconds: number
  message: string
  correction: string
}

interface KtxDeadlineExceeded {
  code: "runtime-startup-timeout" | "operation-timeout" | "migration-timeout"
  database_path: string
  executable_path: string
  operation_phase: "open" | "migration" | "source" | "artifact" | "disposition" | "query"
  elapsed_wait_nanoseconds: number
  message: string
  correction: string
}

interface KtxCommandResult {
  stdout: string
  stderr: string
  exitCode: number
}

// An optional command may exit successfully without storing anything. Only a
// versioned acknowledgement for the submitted evidence retires its retry.
const telemetryAcknowledged = (result: KtxCommandResult | null, expected: any): boolean => {
  if (!result || result.exitCode !== 0) return false
  try {
    const ack = JSON.parse(result.stdout)
    const observation = ack?.observation
    if (ack?.schema !== "ktx.telemetry-ack.v1" || ack.ok !== true || ack.durable !== true ||
        !observation || observation.runtime !== "opencode" || observation.runtime_version !== "1.18.5" ||
        observation.source !== "event_hook") return false
    for (const key of ["session_id", "message_id", "part_id", "ordinal", "reason"]) {
      if (observation[key] !== expected[key]) return false
    }
    if (observation.provider !== (expected.provider ?? null) || observation.model !== (expected.model ?? null)) return false
    const sameMeasurement = (actual: any, want: any): boolean =>
      actual !== null && typeof actual === "object" && actual.state === want.state && actual.value === want.value
    if (!sameMeasurement(observation.cost, expected.cost)) return false
    for (const key of ["input", "output", "reasoning", "cache_read", "cache_write"]) {
      if (!sameMeasurement(observation.tokens?.[key], expected.tokens[key])) return false
    }
    return true
  } catch {
    return false
  }
}

const parseSchemaIncompatibility = (
  stderr: string, databasePath: string, executablePath: string,
): KtxSchemaIncompatibility | null => {
  try {
    const value = JSON.parse(stderr) as { error?: Partial<KtxSchemaIncompatibility> }
    const error = value?.error
    if (!error || error.code !== "schema-incompatible" ||
        !Number.isSafeInteger(error.actual_schema) || !Number.isSafeInteger(error.maximum_schema) ||
        (error.actual_schema as number) < 0 || (error.maximum_schema as number) < 0 ||
        (error.actual_schema as number) <= (error.maximum_schema as number) ||
        error.database_path !== databasePath || error.executable_path !== executablePath ||
        !nonblank(error.message) || !nonblank(error.correction)) return null
    return error as KtxSchemaIncompatibility
  } catch {
    return null
  }
}

const parseDatabaseUnavailable = (
  stderr: string, databasePath: string, executablePath: string,
): KtxDatabaseUnavailable | null => {
  try {
    const value = JSON.parse(stderr) as { error?: Partial<KtxDatabaseUnavailable> }
    const error = value?.error
    if (!error || error.code !== "database-unavailable" ||
        error.database_path !== databasePath || error.executable_path !== executablePath ||
        (error.failure_class !== "coordinator-timeout" && error.failure_class !== "sqlite-lock-exhaustion") ||
        !["open", "migration", "source", "artifact", "disposition", "query"].includes(error.operation_phase || "") ||
        !Number.isSafeInteger(error.elapsed_wait_nanoseconds) || (error.elapsed_wait_nanoseconds as number) < 0 ||
        !nonblank(error.message) || !nonblank(error.correction)) return null
    return {
      code: "database-unavailable", database_path: error.database_path,
      executable_path: error.executable_path,
      failure_class: error.failure_class, operation_phase: error.operation_phase,
      elapsed_wait_nanoseconds: error.elapsed_wait_nanoseconds,
      message: error.message, correction: error.correction,
    } as KtxDatabaseUnavailable
  } catch {
    return null
  }
}

const parseDeadlineExceeded = (
  stderr: string, databasePath: string, executablePath: string,
): KtxDeadlineExceeded | null => {
  try {
    const value = JSON.parse(stderr) as { error?: Partial<KtxDeadlineExceeded> }
    const error = value?.error
    if (!error ||
        (error.code !== "runtime-startup-timeout" && error.code !== "operation-timeout" && error.code !== "migration-timeout") ||
        error.database_path !== databasePath || error.executable_path !== executablePath ||
        !nonblank(error.operation_phase) ||
        !Number.isSafeInteger(error.elapsed_wait_nanoseconds) || (error.elapsed_wait_nanoseconds as number) < 0 ||
        !nonblank(error.message) || !nonblank(error.correction)) return null
    return error as KtxDeadlineExceeded
  } catch {
    return null
  }
}

const ktxRequiredCommand = async (
  executablePath: string, args: string[], payload: string,
): Promise<unknown> => {
  const stream = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(payload))
      controller.close()
    },
  })
  const proc = Bun.spawn([executablePath, ...args], { env: process.env, stdin: stream, stdout: "pipe", stderr: "pipe" })
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited,
  ])
  if (exitCode !== 0) throw new Error("ktx command exited " + exitCode + ": " + stderr.trim())
  const result = JSON.parse(stdout)
  if (!result || typeof result !== "object") throw new Error("malformed ktx acknowledgement")
  return result
}

const metadata = async (path: string, follow: boolean): Promise<Record<string, unknown>> => {
  try {
    const value = await (follow ? stat(path) : lstat(path))
    const field = (name: string): string | number | null => {
      const candidate = (value as unknown as Record<string, unknown>)[name]
      if (typeof candidate === "bigint") return candidate.toString()
      return typeof candidate === "number" && Number.isFinite(candidate) ? candidate : null
    }
    return {
      exists: true,
      dev: field("dev"), ino: field("ino"), mode: field("mode"), nlink: field("nlink"),
      uid: field("uid"), gid: field("gid"), rdev: field("rdev"), size: field("size"),
      blksize: field("blksize"), blocks: field("blocks"),
      mtime_ms: field("mtimeMs"), ctime_ms: field("ctimeMs"),
      birthtime_ms: field("birthtimeMs"),
    }
  } catch (error) {
    if ((error as { code?: string })?.code === "ENOENT") return { exists: false }
    throw error
  }
}

const databaseFingerprint = async (databasePath: string): Promise<string> => {
  const databaseLink = await metadata(databasePath, false)
  if (databaseLink.exists !== true) throw new Error("database disappeared before fingerprinting")
  const physicalDatabase = await realpath(databasePath)
  const paths = new Set([
    databasePath, databasePath + "-wal", databasePath + "-shm",
    physicalDatabase, physicalDatabase + "-wal", physicalDatabase + "-shm",
  ])
  const entries: unknown[] = []
  for (const path of [...paths].sort()) {
    entries.push({ path, link: await metadata(path, false), target: await metadata(path, true) })
  }
  return JSON.stringify(entries)
}

const requiredEventKind = (event: any): string => {
  const partType = event?.properties?.part?.type
  if (event?.type === "message.part.updated" && partType === "step-finish") return "model_step"
  if ((event?.type === "message.part.updated" && partType === "retry") ||
      (event?.type === "session.status" && event?.properties?.status?.type === "retry")) return "retry"
  if ((event?.type === "message.part.updated" && partType === "compaction") || event?.type === "session.compacted") return "compaction"
  if (event?.type === "session.error" || event?.type === "server.instance.disposed") return "interruption"
  return "other"
}

const requiredAck = (value: unknown, expected: {
  sessionID: string, intervalID: string, epochID: string, epochOrdinal: number,
  kind: string, contract: string, afterOrdinal?: number,
}): number => {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("malformed ktx acknowledgement")
  const ack = value as Record<string, unknown>
  const ordinal = ack.ordinal
  const ordinalMatches = Number.isSafeInteger(ordinal) && (ordinal as number) > 0 &&
    (expected.afterOrdinal === undefined ||
      (Number.isSafeInteger(expected.afterOrdinal) && expected.afterOrdinal >= 0 &&
       expected.afterOrdinal < Number.MAX_SAFE_INTEGER && ordinal === expected.afterOrdinal + 1))
  if (ack.ok !== true || ack.session_id !== expected.sessionID || ack.interval_id !== expected.intervalID ||
      ack.epoch_id !== expected.epochID || ack.epoch_ordinal !== expected.epochOrdinal ||
      ack.kind !== expected.kind || ack.contract !== expected.contract ||
      !ordinalMatches) {
    throw new Error("ktx acknowledgement contract mismatch")
  }
  return ordinal as number
}

const canonicalID = (value: unknown): value is string =>
  typeof value === "string" && value.length === 16 && /^[a-z]+$/.test(value)

const hookEnvelope = (value: unknown): KtxEnvelope => {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("malformed ktx hook envelope")
  const envelope = value as Record<string, unknown>
  if (typeof envelope.output !== "string" ||
      !(envelope.compressed_id === null || canonicalID(envelope.compressed_id)) ||
      !canonicalID(envelope.source_id) ||
      !(envelope.ktx_session_id === undefined || canonicalID(envelope.ktx_session_id)) ||
      !(envelope.runtime_session_id === undefined || typeof envelope.runtime_session_id === "string") ||
      !(envelope.call_id === undefined || typeof envelope.call_id === "string") ||
      !(envelope.recovery_cue === undefined || typeof envelope.recovery_cue === "boolean") ||
      (envelope.recovery_cue === true && envelope.compressed_id === null)) {
    throw new Error("ktx hook envelope contract mismatch")
  }
  // Unknown fields are ignored; only the current delivery contract is projected.
  return {
    output: envelope.output,
    compressed_id: envelope.compressed_id as string | null,
    source_id: envelope.source_id,
    ktx_session_id: envelope.ktx_session_id as string,
    runtime_session_id: envelope.runtime_session_id as string,
    call_id: envelope.call_id as string,
	recovery_cue: envelope.recovery_cue as boolean | undefined,
  }
}

const requiredEnvelope = (value: unknown, input: any, sessionID: string): KtxEnvelope => {
  const envelope = hookEnvelope(value)
  if (envelope.ktx_session_id !== sessionID ||
      !nonblank(input?.sessionID) || envelope.runtime_session_id !== input.sessionID ||
      !nonblank(input?.callID) || envelope.call_id !== input.callID) {
    throw new Error("ktx hook envelope contract mismatch")
  }
  return envelope
}

const nonblank = (value: unknown): value is string =>
  typeof value === "string" && value.trim().length > 0

type MeasurementState = "measured" | "unknown" | "malformed"
type Measurement = { state: MeasurementState; value: number | null }

const objectMeasurementState = (value: unknown): MeasurementState => {
  if (value === undefined || value === null) return "unknown"
  return typeof value === "object" && !Array.isArray(value) ? "measured" : "malformed"
}

const integerMeasurement = (value: unknown, container: MeasurementState): Measurement => {
  if (container !== "measured") return { state: container, value: null }
  if (value === undefined || value === null) return { state: "unknown", value: null }
  if (Number.isSafeInteger(value) && (value as number) >= 0) return { state: "measured", value: value as number }
  return { state: "malformed", value: null }
}

const costMeasurement = (value: unknown): Measurement => {
  if (value === undefined || value === null) return { state: "unknown", value: null }
  if (typeof value === "number" && Number.isFinite(value) && value >= 0) return { state: "measured", value }
  return { state: "malformed", value: null }
}

const requiredEventSnapshot = (event: any): string => {
  const part = event?.properties?.part
  if (event?.type !== "message.part.updated" || part?.type !== "step-finish") return JSON.stringify(event)
  // JSON maps nonfinite numbers to null (unknown). Strings retain the existing
  // malformed measurement classification without changing unrelated metadata.
  const number = (value: any): any =>
    typeof value === "number" && !Number.isFinite(value) ? String(value) : value
  const tokens = part.tokens
  const cache = tokens?.cache
  return JSON.stringify({ ...event, properties: { ...event.properties, part: {
    ...part, cost: number(part.cost),
    tokens: objectMeasurementState(tokens) === "measured" ? {
      ...tokens, input: number(tokens.input), output: number(tokens.output), reasoning: number(tokens.reasoning),
      cache: objectMeasurementState(cache) === "measured"
        ? { ...cache, read: number(cache.read), write: number(cache.write) } : number(cache),
    } : number(tokens),
  } } })
}

const KtxPlugin: Plugin = async ({ $ }) => {
  const required = process.env.KTX_BENCH_COLLECTOR_MODE === "required"
  let ktxExecutable = ""
  // Resolve one candidate and pin its canonical path for this plugin instance.
  try {
    const candidate = Bun.which("ktx", { PATH: process.env.PATH })
    if (!candidate) throw new Error("ktx binary not found in PATH")
    ktxExecutable = await realpath(candidate)
  } catch (e) {
    const correction = "reinstall ktx at a readable stable path, run ktx init opencode, and restart OpenCode"
    if (required) throw new Error("ktx candidate verification failed: " + String(e) + "; " + correction)
    console.warn("[ktx] ktx candidate verification failed; plugin disabled: " + String(e) + "; " + correction)
    return {}
  }

  const openBackedArgs = (args: string[]): string[] => args

  const runOrdinaryProcess = async (args: string[], payload?: string): Promise<KtxCommandResult> => {
    let stream: ReadableStream<Uint8Array> | "ignore" = "ignore"
    if (payload !== undefined) {
      stream = new ReadableStream({
        start(controller) {
          controller.enqueue(new TextEncoder().encode(payload))
          controller.close()
        },
      })
    }
    const proc = Bun.spawn([ktxExecutable, ...args], { env: process.env, stdin: stream, stdout: "pipe", stderr: "pipe" })
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited,
    ])
    return { stdout, stderr, exitCode }
  }

  const databasePaths = new Map<string, string>()
  const schemaByTarget = new Map<string, { key: string; fingerprint: string }>()
  const emittedSchemaKeys = new Set<string>()
  let ordinaryQueue: Promise<void> = Promise.resolve()

  const currentDatabasePath = async (): Promise<string> => {
    const signature = JSON.stringify([
      process.env.KTX_DB || "", process.env.XDG_DATA_HOME || "",
      process.env.HOME || "", process.env.USERPROFILE || "",
      process.env.HOMEDRIVE || "", process.env.HOMEPATH || "", process.cwd(),
    ])
    const cached = databasePaths.get(signature)
    if (cached) return cached
    const result = await runOrdinaryProcess(["db", "path"])
    if (result.exitCode !== 0) throw new Error("ktx db path exited " + result.exitCode + ": " + result.stderr.trim())
    const reported = result.stdout.trim()
    if (!nonblank(reported) || reported.includes("\n")) throw new Error("ktx db path returned a malformed path")
    const canonical = resolve(reported)
    databasePaths.set(signature, canonical)
    return canonical
  }

  const ordinaryCommand = (args: string[], payload: string, label: string, runtimeSurface: "opencode-hook" | "opencode-telemetry"): Promise<KtxCommandResult | null> => {
    let result: KtxCommandResult | null = null
    const next = ordinaryQueue.then(async () => {
      const databasePath = await currentDatabasePath()
      const target = databasePath
      const cached = schemaByTarget.get(target)
      if (cached) {
        try {
          if (await databaseFingerprint(databasePath) === cached.fingerprint) {
            emittedSchemaKeys.add(cached.key)
            return
          }
        } catch {
          // A failed metadata probe cannot prove the target is unchanged.
        }
        schemaByTarget.delete(target)
      }
      let fingerprintBefore: string | null = null
      try {
        fingerprintBefore = await databaseFingerprint(databasePath)
      } catch {
        // Suppression requires a complete stable observation around the command.
      }
		const commandResult = await runOrdinaryProcess(openBackedArgs(args), payload)
      let fingerprintAfter: string | null = null
      try {
        fingerprintAfter = await databaseFingerprint(databasePath)
      } catch {
        // Command diagnostics remain authoritative when metadata cannot be observed.
      }
      const unavailable = parseDatabaseUnavailable(
        commandResult.stderr.trim(), databasePath, ktxExecutable,
      )
      if (unavailable) {
        console.error(JSON.stringify({ error: { ...unavailable, runtime_surface: runtimeSurface } }))
        return
      }
      const deadline = parseDeadlineExceeded(
        commandResult.stderr.trim(), databasePath, ktxExecutable,
      )
      if (deadline) {
        console.error(commandResult.stderr.trim())
        return
      }
      if (commandResult.exitCode === 5) {
        const incompatibility = parseSchemaIncompatibility(
          commandResult.stderr.trim(), databasePath, ktxExecutable,
        )
        if (incompatibility) {
          const key = JSON.stringify([
            incompatibility.code, incompatibility.executable_path,
            incompatibility.database_path, incompatibility.actual_schema,
            incompatibility.maximum_schema,
          ])
          if (fingerprintBefore !== null && fingerprintBefore === fingerprintAfter) {
            schemaByTarget.set(target, { key, fingerprint: fingerprintBefore })
          } else {
            schemaByTarget.delete(target)
          }
          if (!emittedSchemaKeys.has(key)) {
            emittedSchemaKeys.add(key)
            console.error(commandResult.stderr.trim())
          }
          return
        }
      }
      if (commandResult.exitCode !== 0) {
        console.error("[ktx] " + label + " failed (exit " + commandResult.exitCode + "): " + commandResult.stderr.trim())
        return
      }
      result = commandResult
    }).catch((e) => {
      console.error("[ktx] " + label + " error: " + String(e))
    })
    ordinaryQueue = next
    return next.then(() => result)
  }

	const requiredSessionID = process.env.KTX_BENCH_SESSION_ID || ""
	const requiredIntervalID = process.env.KTX_BENCH_EVENT_INTERVAL_ID || ""
	const requiredContract = process.env.KTX_BENCH_EVENT_CONTRACT || ""
  const epochID = crypto.randomUUID()
	const readDescriptionHook = benchmarkReadDescription(required, epochID)
	let globalOrdinal = 0
  if (required) {
	const ack = await ktxRequiredCommand(ktxExecutable, openBackedArgs(["telemetry", "collector-open"]), JSON.stringify({ epoch_id: epochID }))
    globalOrdinal = requiredAck(ack, { sessionID: requiredSessionID, intervalID: requiredIntervalID, epochID, epochOrdinal: 0, kind: "collector_open", contract: requiredContract })
  }

  const identities = new Map<string, { provider: string; model: string }>()
  const observed = new Map<string, { evidence: string } & (
    { accepted: false; payload: string } | { accepted: true }
  )>()
  let ordinal = 0
	let epochOrdinal = 0
  const pendingRequired = new Map<string, { event: string; payload: string; ordinal: number; after: number; kind: string }>()
  let eventQueue: Promise<void> = Promise.resolve()

  const collectEvent = async (event: any, capturedAt: string): Promise<void> => {
    if (!event || typeof event !== "object") return
    if (event.type === "message.updated") {
      const info = event.properties?.info
      if (!info || typeof info !== "object" || info.role !== "assistant" ||
          !nonblank(info.sessionID) || !nonblank(info.id) ||
          !nonblank(info.providerID) || !nonblank(info.modelID)) return
      const key = JSON.stringify([info.sessionID, info.id])
      const prior = identities.get(key)
      if (prior && (prior.provider !== info.providerID || prior.model !== info.modelID)) return
      identities.set(key, { provider: info.providerID, model: info.modelID })
      return
    }
    if (event.type !== "message.part.updated") return

    const part = event.properties?.part
    const tokens = part?.tokens
    const cache = tokens?.cache
    if (!part || typeof part !== "object" || part.type !== "step-finish" ||
        !nonblank(part.sessionID) || !nonblank(part.messageID) ||
        !nonblank(part.id) || !nonblank(part.reason)) return

    const key = JSON.stringify([part.sessionID, part.messageID, part.id])
    const identity = identities.get(JSON.stringify([part.sessionID, part.messageID]))
    const tokensState = objectMeasurementState(tokens)
    const cacheState = tokensState === "measured" ? objectMeasurementState(cache) : tokensState
    const observation: any = {
      session_id: part.sessionID,
      message_id: part.messageID,
      part_id: part.id,
      reason: part.reason,
      cost: costMeasurement(part.cost),
      tokens: {
        input: integerMeasurement(tokens?.input, tokensState),
        output: integerMeasurement(tokens?.output, tokensState),
        reasoning: integerMeasurement(tokens?.reasoning, tokensState),
        cache_read: integerMeasurement(cache?.read, cacheState),
        cache_write: integerMeasurement(cache?.write, cacheState),
      },
    }
    const evidence = JSON.stringify(observation)
    let delivery = observed.get(key)
    if (delivery) {
      if (delivery.evidence !== evidence) throw new Error("conflicting model-step redelivery")
      if (delivery.accepted) return
    } else {
      observation.ordinal = ++ordinal
      observation.captured_at = capturedAt
      if (identity) {
        observation.provider = identity.provider
        observation.model = identity.model
      }
      delivery = { evidence, payload: JSON.stringify(observation), accepted: false }
      observed.set(key, delivery)
    }
		const result = await ordinaryCommand(["telemetry", "record", "--ack"], delivery.payload, "telemetry record", "opencode-telemetry")
		if (result?.stderr.trim()) console.error(result.stderr.trim())
    if (telemetryAcknowledged(result, JSON.parse(delivery.payload))) observed.set(key, { evidence: delivery.evidence, accepted: true })
    else if (result) console.error("[ktx] telemetry persistence not acknowledged; awaiting redelivery")
  }

  return {
    "event": async ({ event }: any) => {
      // An ordinary wrapper owns all evidence when it supplies the session.
      if (process.env.KTX_SESSION_ID && !process.env.KTX_COLLECTION_ID) return
	  if (required) {
		const snapshot = requiredEventSnapshot(event)
		const capturedAt = new Date().toISOString()
		const next = eventQueue.then(async () => {
          const retained = JSON.parse(snapshot)
          const part = retained?.properties?.part
          const key = requiredEventKind(retained) === "model_step"
            ? JSON.stringify([part.sessionID, part.messageID, part.id]) : snapshot
          let pending = pendingRequired.get(key)
          if (pending && pending.event !== snapshot) throw new Error("conflicting model-step redelivery")
          if (!pending) {
            const ordinal = ++epochOrdinal
            pending = { event: snapshot, ordinal, after: globalOrdinal, kind: requiredEventKind(retained),
              payload: JSON.stringify({ epoch_id: epochID, epoch_ordinal: ordinal, after_ordinal: globalOrdinal, captured_at: capturedAt, event: retained }) }
            pendingRequired.set(key, pending)
          }
		  const ack = await ktxRequiredCommand(ktxExecutable, openBackedArgs(["telemetry", "event-record"]), pending.payload)
		  const acknowledged = requiredAck(ack, { sessionID: requiredSessionID, intervalID: requiredIntervalID, epochID, epochOrdinal: pending.ordinal, kind: pending.kind, contract: requiredContract, afterOrdinal: pending.after })
          globalOrdinal = Math.max(globalOrdinal, acknowledged)
          pendingRequired.delete(key)
		})
		eventQueue = next.catch(() => undefined)
		await next
		return
	  }
      // Snapshot before queueing: runtime-owned objects may change while an
      // earlier callback is awaiting persistence.
      const capturedAt = new Date().toISOString()
      let snapshot: any
      try { snapshot = structuredClone(event) }
      catch (e) { console.error("[ktx] telemetry collector error: " + String(e)); return }
      const next = eventQueue.then(() => collectEvent(snapshot, capturedAt)).catch((e) => {
        console.error("[ktx] telemetry collector error: " + String(e))
      })
      eventQueue = next
      await next
    },
    ...(readDescriptionHook ? { "tool.definition": readDescriptionHook } : {}),
    "tool.execute.after": async (input: any, output: any) => {
      // Noop when an ordinary wrapper is active.
      if (process.env.KTX_SESSION_ID && !process.env.KTX_COLLECTION_ID) return

      const record = {
        runtime: "opencode",
        input: input,
        output: output,
      }

	  let envelope: KtxEnvelope | null
	  if (required || process.env.KTX_COLLECTION_ID) {
		const next = eventQueue.then(async () => {
		  const strict = await ktxRequiredCommand(ktxExecutable, openBackedArgs(["hook"]), JSON.stringify(record))
		  return requiredEnvelope(strict, input, required ? requiredSessionID : process.env.KTX_SESSION_ID || "")
		})
		eventQueue = next.then(() => undefined, () => undefined)
		envelope = await next
	  } else {
		const next = eventQueue.then(async () => {
		  const result = await ordinaryCommand(["hook"], JSON.stringify(record), "hook", "opencode-hook")
		  if (!result) return null
		  try {
			return hookEnvelope(JSON.parse(result.stdout))
		  } catch (e) {
			console.error("[ktx] hook error: " + String(e))
			return null
		  }
		})
		eventQueue = next.then(() => undefined, () => undefined)
		envelope = await next
	  }
      if (envelope) {
        // Mutate output.output to the compressed bytes.
        // The agent sees the compressed output instead of the original.
        if (output && typeof output === "object" && "output" in output) {
          output.output = envelope.output
		  if ((required || process.env.KTX_COLLECTION_ID) && envelope.recovery_cue) {
			if (!envelope.compressed_id) throw new Error("ktx recovery cue lacks artifact identity")
			const ack = await ktxRequiredCommand(ktxExecutable, openBackedArgs(["telemetry", "cue-delivered"]), JSON.stringify({source_id:envelope.source_id,artifact_id:envelope.compressed_id,session_id:envelope.ktx_session_id,runtime_session_id:envelope.runtime_session_id,call_id:envelope.call_id}))
			const value = ack as Record<string, unknown>
			const ownerID = required ? requiredIntervalID : process.env.KTX_COLLECTION_ID
			if (value?.ok !== true || value.collection_id !== ownerID || value.session_id !== envelope.ktx_session_id || value.source_id !== envelope.source_id || value.artifact_id !== envelope.compressed_id) throw new Error("ktx recovery cue acknowledgement mismatch")
		  }
        }
      }
      // On failure (envelope === null), output is left unchanged.
      // The agent sees the original tool output (conservative failure).
    },
  }
}

export default KtxPlugin
` + benchmarkReadDescriptionTemplate

// initFlags captures the flags shared across all `ktx init <runtime>`
// subcommands. Each flag selects a distinct action; combining them is
// rejected at parse time.
type initFlags struct {
	project   bool
	uninstall bool
	show      bool
	showHelp  bool
}

// runInitWith executes `ktx init` with injected output streams.
// stdout/stderr are injected so the install/uninstall/show flow can
// be exercised without touching os.Stdout/Stderr or the real home
// directory. The body calls parseInitArgs,
// then dispatches on the runtime name and action flags.
func runInitWith(args []string, opts Options, stdout, stderr io.Writer) int {
	return runDispatchWithOptions("init", args, opts, stdout, stderr)
}

func executeInit(f initFlags, runtime, format string, stdout, stderr io.Writer) (any, error) {
	if f.showHelp {
		printInitHelp(stdout)
		return nil, nil
	}

	if runtime == "" {
		return nil, usageError("ktx init: missing runtime name; usage: ktx init <runtime> [flags] (supported: opencode, codex)")
	}

	// At most one action flag is meaningful; mixing --uninstall/--show with
	// --project is permitted (--project scopes the action), but stacking
	// --uninstall + --show is not.
	if f.uninstall && f.show {
		return nil, usageError("ktx init: --uninstall and --show are mutually exclusive")
	}

	switch runtime {
	case "opencode":
		return nil, initOpencodeLogic(&f, format, stdout, stderr)
	case "codex":
		return nil, initCodexLogic(&f, format, stdout, stderr)
	default:
		return nil, usageError("ktx init: unknown runtime %q; supported: opencode, codex", runtime)
	}
}

// initOpencodeLogic routes to the install/uninstall/show handler for
// the OpenCode adapter. It resolves the candidate plugin paths once
// and passes them down so the handlers do not repeat path logic.
// Returns the typed error to surface (nil on success).
func initOpencodeLogic(f *initFlags, format string, stdout, stderr io.Writer) error {
	globalPath, err := opencodeGlobalPluginPath()
	if err != nil {
		return internalError("ktx init opencode: resolve global plugin path: %v", err)
	}
	// The project path is always resolved (it does not require user
	// lookup), but only consulted for --show/--uninstall or when --project
	// scopes the install.
	projectPath := opencodeProjectPluginPath()

	switch {
	case f.show:
		return initOpencodeShow(globalPath, projectPath, format, stdout, stderr)
	case f.uninstall:
		return initOpencodeUninstall(globalPath, projectPath, format, stdout, stderr)
	default:
		// Default action is install. --project (when set) scopes the
		// install to the project directory; otherwise the global dir
		// is the install target.
		target := globalPath
		if f.project {
			target = projectPath
		}
		return initOpencodeInstall(target, globalPath, projectPath, format, stdout)
	}
}

// opencodeGlobalPluginPath returns the path OpenCode loads plugins from
// when configured globally: $XDG_CONFIG_HOME/opencode/plugins/<file>.
// The XDG Base Directory Specification says $HOME/.config is the
// default XDG_CONFIG_HOME when the env var is unset, so the fallback
// mirrors the spec. The home directory is resolved via os.UserHomeDir.
func opencodeGlobalPluginPath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "opencode", "plugins", ktxPluginFileName), nil
}

// opencodeProjectPluginPath returns the project-scoped plugin path,
// always relative to the current working directory. The CWD is captured
// at call time rather than process start.
func opencodeProjectPluginPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		// Falling back to "." keeps the path parseable for the caller's
		// downstream tooling even if the CWD is unreadable. The caller
		// will surface the underlying os error when the file is opened.
		return filepath.Join(".", ".opencode", "plugins", ktxPluginFileName)
	}
	return filepath.Join(cwd, ".opencode", "plugins", ktxPluginFileName)
}

// initOpencodeInstall writes the TS plugin file to target. The parent
// directory is created with os.MkdirAll so first-time installs in a
// fresh OpenCode config tree succeed without a separate setup step.
// The file is written with 0644 — readable by the agent process,
// writable by the user. Any failure (mkdir, write) surfaces as an
// internal error with the path included for the caller to act on.
func initOpencodeInstall(target, globalPath, projectPath, format string, stdout io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return internalError("ktx init opencode: create directory %q: %v", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(ktxOpencodePluginTemplate), 0o644); err != nil {
		return internalError("ktx init opencode: write plugin %q: %v", target, err)
	}

	writeInstallReport(stdout, target, globalPath, projectPath, format)
	return nil
}

// initOpencodeUninstall removes the plugin from whichever location has
// it (global checked first, then project). When neither has the file,
// the exit code is not-found (2) so callers can detect "nothing to do"
// without inspecting stdout. When both have it, both are removed — the
// user invoked `--uninstall` once and expects a clean state.
func initOpencodeUninstall(globalPath, projectPath, format string, stdout, stderr io.Writer) error {
	removed := []string{}
	for _, p := range []string{globalPath, projectPath} {
		if err := os.Remove(p); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return internalError("ktx init opencode: remove %q: %v", p, err)
		}
		removed = append(removed, p)
	}
	if len(removed) == 0 {
		return notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath)
	}
	writeUninstallReport(stdout, removed, format)
	return nil
}

// initOpencodeShow reports where the plugin is installed. Both locations
// are inspected; the first match wins for the primary report, but a
// note is emitted (text mode) or a second field is included (JSON mode)
// if both locations have the file — that is a configuration smell the
// user should know about.
//
// Exit codes: 0 when at least one location has the file, 2 when neither
// has it. The 2 exit code is the standard "not-found" class so
// scripts can branch on it.
func initOpencodeShow(globalPath, projectPath, format string, stdout, stderr io.Writer) error {
	hasGlobal, err := fileExists(globalPath)
	if err != nil {
		return internalError("ktx init opencode: inspect plugin %q: %v", globalPath, err)
	}
	hasProject, err := fileExists(projectPath)
	if err != nil {
		return internalError("ktx init opencode: inspect plugin %q: %v", projectPath, err)
	}

	if !hasGlobal && !hasProject {
		return notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath)
	}

	primary := globalPath
	if !hasGlobal && hasProject {
		primary = projectPath
	}
	var also []string
	if hasGlobal && hasProject {
		also = append(also, projectPath)
	}
	writeShowReport(stdout, primary, also, globalPath, projectPath, format)
	return nil
}

// fileExists reports whether path is a regular file (or symlink to a
// regular file). Missing paths are absent; other inspection failures
// are returned so callers do not mistake unreadable state for absence.
func fileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("not a regular file")
	}
	return true, nil
}

// writeInstallReport emits the installed file, both candidate locations, the
// generated source revision, and the hooks declared by that source. It reports
// file generation only and explicitly leaves runtime activation unverified.
func writeInstallReport(w io.Writer, target, globalPath, projectPath, format string) {
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte(ktxOpencodePluginTemplate)))
	hooks := []string{"event", "tool.execute.after"}
	if format == FormatText {
		fmt.Fprintf(w, "ktx opencode plugin installed at %s\n", target)
		fmt.Fprintf(w, "generated revision: %s\n", revision)
		fmt.Fprintf(w, "declared hooks: %s\n", strings.Join(hooks, ", "))
		fmt.Fprintf(w, "hook roles: event=%s; tool.execute.after=tool-result compression\n", opencodeTelemetryEvidence)
		fmt.Fprintf(w, "telemetry claim boundary: %s\n", opencodeTelemetryClaimBoundary)
		fmt.Fprintf(w, "candidate locations: global=%s project=%s\n", globalPath, projectPath)
		fmt.Fprintf(w, "refresh command: %s\n", opencodeRefreshCommand)
		fmt.Fprintf(w, "troubleshooting: %s\n", opencodeTroubleshooting)
		fmt.Fprintln(w, "runtime activation: not verified")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"installed":           true,
		"path":                target,
		"candidate_locations": map[string]string{"global": globalPath, "project": projectPath},
		"generated_revision":  revision,
		"declared_hooks":      hooks,
		"hook_roles": map[string]string{
			"event":              opencodeTelemetryEvidence,
			"tool.execute.after": "tool-result compression",
		},
		"telemetry_claim_boundary": opencodeTelemetryClaimBoundary,
		"refresh_command":          opencodeRefreshCommand,
		"troubleshooting":          opencodeTroubleshooting,
		"runtime_activation":       "not_verified",
	})
}

// writeUninstallReport emits the uninstall confirmation. When multiple
// files were removed, all of them are listed so the user can audit what
// `ktx init opencode --uninstall` actually did.
func writeUninstallReport(w io.Writer, removed []string, format string) {
	if format == FormatText {
		if len(removed) == 1 {
			fmt.Fprintf(w, "ktx opencode plugin removed from %s\n", removed[0])
			return
		}
		fmt.Fprintf(w, "ktx opencode plugin removed from %d locations: %s\n", len(removed), strings.Join(removed, ", "))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"uninstalled": true,
		"paths":       removed,
	})
}

// writeShowReport emits the --show result. The primary path is the
// first one matched (global first, then project); the also list names
// any other locations where the plugin is also installed, so the user
// can decide whether to clean up.
func writeShowReport(w io.Writer, primary string, also []string, globalPath, projectPath, format string) {
	if format == FormatText {
		fmt.Fprintf(w, "ktx opencode plugin installed at %s\n", primary)
		for _, p := range also {
			fmt.Fprintf(w, "  (also installed at %s)\n", p)
		}
		return
	}
	payload := map[string]any{
		"installed":           true,
		"path":                primary,
		"candidate_locations": map[string]string{"global": globalPath, "project": projectPath},
	}
	if len(also) > 0 {
		payload["also"] = also
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// parseInitDispatchArgs consumes the runtime positional and init flags.
func parseInitDispatchArgs(args []string) ([]string, any, error) {
	format, rest, err := extractFormatChecked(args)
	if err != nil {
		return nil, nil, err
	}
	f, runtime, err := parseInitArgs(rest)
	if err != nil {
		return nil, nil, err
	}
	var common []string
	if format != "" {
		common = []string{"--format=" + format}
	}
	return common, initParsedArgs{flags: f, runtime: runtime}, nil
}

// initCommandFunc is the dispatch wrapper. The dispatcher already
// parsed the init-specific arguments and carries them on the context.
//
// DatabaseNone: ktx init never touches the ktx database — it
// only writes/reads/removes plugin files on disk.
func initCommandFunc(ctx *DispatchContext) (any, error) {
	parsed, ok := ctx.ParsedArgs.(initParsedArgs)
	if !ok {
		return nil, internalError("ktx init: command arguments were not parsed")
	}
	return executeInit(parsed.flags, parsed.runtime, ctx.Format, ctx.Stdout, ctx.Stderr)
}

// init registers `ktx init` with the dispatch module. After init runs,
// the verb is reachable through runWith.
func init() {
	Register("init", RegisterOptions{
		Description: "install a runtime adapter",
		ParseArgs:   parseInitDispatchArgs,
		Help:        printInitHelp,
	}, initCommandFunc)
}

// parseInitArgs walks args and separates recognised flags from the
// runtime positional. The first non-flag token is the runtime; any
// further positional tokens are reported as unexpected so the caller
// can produce a clear usage error. Unknown flags are also rejected —
// typos like `--projct` should surface immediately rather than being
// silently ignored. The error message distinguishes the two cases so
// the user can tell a typo from a stray positional argument.
func parseInitArgs(args []string) (initFlags, string, error) {
	var f initFlags
	var runtime string
	escaped := false
	for _, a := range args {
		if a == "--" && !escaped {
			escaped = true
			continue
		}
		name, value, hasValue := strings.Cut(a, "=")
		if escaped {
			name = ""
		}
		var target *bool
		switch name {
		case "--project":
			target = &f.project
		case "--uninstall":
			target = &f.uninstall
		case "--show":
			target = &f.show
		case "--help", "-h":
			target = &f.showHelp
		default:
			if !escaped && len(a) > 1 && a[0] == '-' {
				return f, "", fmt.Errorf("unknown flag %q", a)
			}
			if runtime == "" {
				runtime = a
				continue
			}
			return f, "", fmt.Errorf("unexpected argument %q", a)
		}
		*target = true
		if hasValue {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return f, "", fmt.Errorf("invalid %s boolean %q", name, value)
			}
			*target = parsed
		}
	}
	return f, runtime, nil
}

// printInitHelp writes the `ktx init` help. The flag table mirrors the
// dispatcher's action contract: --project scopes, --uninstall removes,
// --show reports.
func printInitHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx init — install a runtime adapter")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx init <runtime> [flags]")
	fmt.Fprintln(w)
	printOwnedArgumentHelp(w)
	fmt.Fprintln(w, "Supported runtimes:")
	fmt.Fprintln(w, "  opencode   install the OpenCode TS plugin adapter")
	fmt.Fprintln(w, "             (a standalone ktx.ts file at $XDG_CONFIG_HOME/opencode/plugins/)")
	fmt.Fprintln(w, "  codex      merge a PostToolUse hook into ~/.codex/hooks.json or ./.codex/hooks.json")
	fmt.Fprintln(w, "             (shared hooks file; other Codex hooks are preserved on install/uninstall)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --project      install to project scope")
	fmt.Fprintln(w, "                 opencode: ./.opencode/plugins/ktx.ts")
	fmt.Fprintln(w, "                 codex:    ./.codex/hooks.json")
	fmt.Fprintln(w, "                 default scope is global ($XDG_CONFIG_HOME/opencode/plugins/ktx.ts")
	fmt.Fprintln(w, "                 or ~/.codex/hooks.json respectively)")
	fmt.Fprintln(w, "  --uninstall    remove the installed adapter")
	fmt.Fprintln(w, "                 opencode: checks both global and project locations")
	fmt.Fprintln(w, "                 codex:    scopes to the target (--project for project scope,")
	fmt.Fprintln(w, "                           otherwise global); only the ktx PostToolUse entry is removed")
	fmt.Fprintln(w, "  --show         report whether the adapter is installed and where")
	fmt.Fprintln(w, "  --format       json or text; boolean action/scope flags also accept =false")
	fmt.Fprintln(w, "  -h, --help     show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  action succeeded (install, uninstall of at least one file, show found)")
	fmt.Fprintln(w, "  1  usage error (unknown runtime, conflicting flags)")
	fmt.Fprintln(w, "  2  not found (--show found nothing, --uninstall found nothing)")
	fmt.Fprintln(w, "  4  internal error (adapter state could not be inspected or changed)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The OpenCode plugin subscribes to event for optional model-step telemetry and")
	fmt.Fprintln(w, "tool.execute.after for tool-result compression. It delegates to `ktx telemetry")
	fmt.Fprintln(w, "record` and `ktx hook` respectively. With KTX_SESSION_ID alone, the plugin")
	fmt.Fprintln(w, "noops so the ordinary wrapper remains the single evidence owner. Explicit")
	fmt.Fprintln(w, "collection also sets KTX_COLLECTION_ID and keeps the hooks active.")
	fmt.Fprintln(w, "The wrapper records identifiable OpenCode step_finish model telemetry itself")
	fmt.Fprintln(w, "while the generated hooks noop, preventing duplicate evidence.")
	fmt.Fprintf(w, "Telemetry is %s. It is %s.\n", opencodeTelemetryEvidence, opencodeTelemetryClaimBoundary)
	fmt.Fprintf(w, "Re-run `%s` to refresh the generated global plugin, or add --project\n", opencodeRefreshCommand)
	fmt.Fprintf(w, "for the project plugin. Troubleshooting: %s\n", opencodeTroubleshooting)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The Codex hook pins the installing executable's canonical path on every")
	fmt.Fprintln(w, "PostToolUse callback. It omits Codex's optional timeout so KTX owns deadline")
	fmt.Fprintln(w, "attribution. Install is idempotent; uninstall removes only ktx's entry and")
	fmt.Fprintln(w, "preserves other hooks (SessionStart, etc.). After install, run /hooks")
	fmt.Fprintln(w, "in Codex to trust the ktx hook before it fires — untrusted hooks do not execute.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  ktx init opencode              # global install")
	fmt.Fprintln(w, "  ktx init opencode --project    # project install")
	fmt.Fprintln(w, "  ktx init opencode --show       # check status")
	fmt.Fprintln(w, "  ktx init opencode --uninstall  # remove")
	fmt.Fprintln(w, "  ktx init codex                 # merge ktx PostToolUse hook into ~/.codex/hooks.json")
	fmt.Fprintln(w, "  ktx init codex --project       # merge into ./.codex/hooks.json")
	fmt.Fprintln(w, "  ktx init codex --show          # check both global and project scopes")
	fmt.Fprintln(w, "  ktx init codex --uninstall     # remove ktx entry from global hooks.json")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
