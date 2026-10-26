import type { Plugin } from "@opencode-ai/plugin"
import childProcess from "node:child_process"
import { access, constants, lstat, realpath, stat } from "node:fs/promises"
import os from "node:os"
import { delimiter, resolve } from "node:path"

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

// Runs ktx directly, without a shell. ktx may exit before it reads stdin, so a
// stdin write error is ignored and the exit status stays authoritative. A
// signal exit reports 128 plus the signal number; a spawn failure rejects.
const spawnKtx = (
  executablePath: string, args: string[], payload?: string,
): Promise<KtxCommandResult> => new Promise((settle, reject) => {
  const child = childProcess.spawn(executablePath, args, {
    env: process.env, stdio: [payload === undefined ? "ignore" : "pipe", "pipe", "pipe"],
  })
  const stdout: Buffer[] = []
  const stderr: Buffer[] = []
  child.stdout.on("data", (chunk: Buffer) => stdout.push(chunk))
  child.stderr.on("data", (chunk: Buffer) => stderr.push(chunk))
  child.on("error", reject)
  child.on("close", (code: number | null, signal: string | null) => {
    const decode = (chunks: Buffer[]): string => new TextDecoder().decode(Buffer.concat(chunks))
    const signals = os.constants.signals as Record<string, number>
    settle({ stdout: decode(stdout), stderr: decode(stderr), exitCode: code ?? 128 + (signals[signal ?? ""] ?? 0) })
  })
  child.stdin?.on("error", () => {})
  if (payload !== undefined) child.stdin?.end(payload)
})

// Mirrors a PATH search: the first regular, executable file named ktx wins.
const findKtx = async (): Promise<string | null> => {
  for (const directory of (process.env.PATH || "").split(delimiter)) {
    if (!directory) continue
    const candidate = resolve(directory, "ktx")
    try {
      if (!(await stat(candidate)).isFile()) continue
      await access(candidate, constants.X_OK)
      return candidate
    } catch {
      // Missing or non-executable entries do not stop the search.
    }
  }
  return null
}

const ktxRequiredCommand = async (
  executablePath: string, args: string[], payload: string,
): Promise<unknown> => {
  const { stdout, stderr, exitCode } = await spawnKtx(executablePath, args, payload)
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

const KtxPlugin: Plugin = async () => {
  const required = process.env.KTX_BENCH_COLLECTOR_MODE === "required"
  let ktxExecutable = ""
  // Resolve one candidate and pin its canonical path for this plugin instance.
  try {
    const candidate = await findKtx()
    if (!candidate) throw new Error("ktx binary not found in PATH")
    ktxExecutable = await realpath(candidate)
  } catch (e) {
    const correction = "reinstall ktx at a readable stable path, run ktx init opencode, and restart OpenCode"
    if (required) throw new Error("ktx candidate verification failed: " + String(e) + "; " + correction)
    console.warn("[ktx] ktx candidate verification failed; plugin disabled: " + String(e) + "; " + correction)
    return {}
  }

  const openBackedArgs = (args: string[]): string[] => args

  const runOrdinaryProcess = (args: string[], payload?: string): Promise<KtxCommandResult> =>
    spawnKtx(ktxExecutable, args, payload)

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

import { appendFileSync, readFileSync } from "node:fs"
import { createHash } from "node:crypto"
const benchmarkReadDescription = (required: boolean, epoch: string) => {
  const side = process.env.KTX_BENCH_READ_DESCRIPTION
  if (!side) return undefined
  if (!required || !["baseline", "candidate"].includes(side)) throw new Error("read-description treatment requires an owned benchmark arm")
  const directory = "/home/node/.local/share/ktx/read-description"
  const digest = (text: string) => createHash("sha256").update(text).digest("hex")
  const original = readFileSync(directory + "/original.txt", "utf8")
  const candidate = readFileSync(directory + "/candidate.txt", "utf8")
  if (digest(original) !== "98ee843341c2dab2227add0019e48d4b2f0f00f9b042b853d1ee52bb34e6363d" || digest(candidate) !== "d4ebb60002e2b98eb8337341e5572548ab283f7e44cad29aa5335f28e18d61a2") throw new Error("read-description fixture identity mismatch")
  const tools = new Set(["invalid", "question", "bash", "read", "glob", "grep", "task", "webfetch", "todowrite", "skill", "apply_patch"])
  let ordinal = 0
  return async (input: any, output: any) => {
    if (!tools.has(input.toolID)) throw new Error("unexpected tool in read-description experiment")
    const before = output.description
    const parameters = output.parameters
    const jsonSchema = output.jsonSchema
    const ast = JSON.stringify(parameters?.ast)
    if (typeof before !== "string" || typeof ast !== "string" || before.length > 65536 || ast.length > 65536) throw new Error("unavailable or oversized public tool definition")
    if (input.toolID === "read") {
      if (before !== original || digest(ast) !== "f90c59de1b9ad91f6d52892c3beae2e73fca5e290ec87f5999b74655633be355" || jsonSchema !== undefined) throw new Error("read-description source or parameter schema changed")
      if (side === "candidate") {
        let transformed = before
        for (const line of [
          "- The filePath parameter should be an absolute path.\n",
          "- By default, this tool returns up to 2000 lines from the start of the file.\n",
          "- The offset parameter is the line number to start from (1-indexed).\n",
        ]) {
          if (transformed.split(line).length !== 2) throw new Error("read-description removal mismatch")
          transformed = transformed.replace(line, "")
        }
        if (transformed !== candidate) throw new Error("read-description transform mismatch")
        output.description = transformed
      }
    }
    const record = {
      version: "read-three-bullets-v1", boundary: "tool.definition", runtime: "1.18.5",
      owner: process.env.KTX_BENCH_SESSION_ID, interval: process.env.KTX_BENCH_EVENT_INTERVAL_ID,
      epoch, ordinal: ++ordinal, side, tool: input.toolID,
      before_sha256: digest(before), after_sha256: digest(output.description),
      parameters_sha256: digest(ast), json_schema_sha256: digest(JSON.stringify(jsonSchema ?? null)),
      parameters_unchanged: parameters === output.parameters && ast === JSON.stringify(output.parameters?.ast),
      json_schema_unchanged: jsonSchema === output.jsonSchema,
      ...(input.toolID === "read" ? {before, after: output.description, parameters_ast: ast} : {}),
    }
    appendFileSync(directory + "/hooks.jsonl", JSON.stringify(record) + "\n", {mode: 0o600})
  }
}
