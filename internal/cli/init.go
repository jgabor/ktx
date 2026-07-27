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
	opencodeTroubleshooting        = "docs/opencode-telemetry.md#troubleshooting"
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
//     shells out to `ktx telemetry record`. Collection and persistence failures
//     do not alter runtime events.
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
//   - Noops when KTX_SESSION_ID is present: when the user
//     is also running the agent under `ktx run`, the wrapper owns
//     persistence and the plugin would otherwise double-record.
//
// Presence check: the plugin factory verifies the ktx
// binary is in PATH before registering either hook. If
// ktx is missing, no hooks are registered and a one-line warn makes
// the disabled state visible — without this, every tool call would
// pay a `ktx hook` spawn cost and silently drop its payload.
const ktxOpencodePluginTemplate = `import type { Plugin } from "@opencode-ai/plugin"

interface KtxEnvelope {
  output: string
  compressed_id: string | null
  source_id: string
	ktx_session_id?: string
	runtime_session_id?: string
	call_id?: string
}

const ktxHook = async (payload: string): Promise<KtxEnvelope | null> => {
  try {
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode(payload))
        controller.close()
      },
    })
    const proc = Bun.spawn(["ktx", "hook"], {
      stdin: stream,
      stdout: "pipe",
      stderr: "pipe",
    })
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
      proc.exited,
    ])
    if (exitCode !== 0) {
      console.error("[ktx] hook failed (exit " + exitCode + "): " + stderr.trim())
      return null
    }
    return JSON.parse(stdout) as KtxEnvelope
  } catch (e) {
    console.error("[ktx] hook error: " + String(e))
    return null
  }
}

const ktxTelemetry = async (payload: string): Promise<void> => {
  try {
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode(payload))
        controller.close()
      },
    })
    const proc = Bun.spawn(["ktx", "telemetry", "record"], {
      stdin: stream,
      stdout: "pipe",
      stderr: "pipe",
    })
    const [stderr, exitCode] = await Promise.all([
      new Response(proc.stderr).text(),
      proc.exited,
    ])
    if (stderr.trim()) console.error(stderr.trim())
    if (exitCode !== 0) console.error("[ktx] telemetry record failed (exit " + exitCode + ")")
  } catch (e) {
    console.error("[ktx] telemetry record error: " + String(e))
  }
}

const ktxRequiredCommand = async (args: string[], payload: string): Promise<unknown> => {
  const stream = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(payload))
      controller.close()
    },
  })
  const proc = Bun.spawn(["ktx", ...args], { stdin: stream, stdout: "pipe", stderr: "pipe" })
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => { proc.kill(); reject(new Error("ktx command timed out")) }, 10000)
  })
  try {
    const [stdout, stderr, exitCode] = await Promise.race([
      Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]),
      timeout,
    ])
    if (exitCode !== 0) throw new Error("ktx command exited " + exitCode + ": " + stderr.trim())
    const result = JSON.parse(stdout)
    if (!result || typeof result !== "object") throw new Error("malformed ktx acknowledgement")
    return result
  } finally {
    if (timer) clearTimeout(timer)
  }
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

const requiredEnvelope = (value: unknown, input: any, sessionID: string): KtxEnvelope => {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("malformed ktx hook envelope")
  const envelope = value as Record<string, unknown>
  if (typeof envelope.output !== "string" ||
      !(envelope.compressed_id === null || typeof envelope.compressed_id === "string") ||
      !nonblank(envelope.source_id) || envelope.ktx_session_id !== sessionID ||
      !nonblank(input?.sessionID) || envelope.runtime_session_id !== input.sessionID ||
      !nonblank(input?.callID) || envelope.call_id !== input.callID) {
    throw new Error("ktx hook envelope contract mismatch")
  }
  return {
    output: envelope.output,
    compressed_id: envelope.compressed_id as string | null,
    source_id: envelope.source_id,
    ktx_session_id: envelope.ktx_session_id as string,
    runtime_session_id: envelope.runtime_session_id as string,
    call_id: envelope.call_id as string,
  }
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

const KtxPlugin: Plugin = async ({ $ }) => {
  // Verify ktx is in PATH before registering the hook; otherwise every
  // tool call would pay a ktx hook spawn cost and silently noop.
  try {
    await $` + "`which ktx`" + `.quiet()
  } catch {
    console.warn("[ktx] ktx binary not found in PATH \u2014 plugin disabled")
    return {}
  }

  const required = process.env.KTX_BENCH_COLLECTOR_MODE === "required"
	const requiredSessionID = process.env.KTX_BENCH_SESSION_ID || ""
	const requiredIntervalID = process.env.KTX_BENCH_EVENT_INTERVAL_ID || ""
	const requiredContract = process.env.KTX_BENCH_EVENT_CONTRACT || ""
  const epochID = crypto.randomUUID()
	let globalOrdinal = 0
  if (required) {
    const ack = await ktxRequiredCommand(["telemetry", "collector-open"], JSON.stringify({ epoch_id: epochID }))
    globalOrdinal = requiredAck(ack, { sessionID: requiredSessionID, intervalID: requiredIntervalID, epochID, epochOrdinal: 0, kind: "collector_open", contract: requiredContract })
  }

  const identities = new Map<string, { provider: string; model: string }>()
  const seen = new Set<string>()
  let ordinal = 0
	let epochOrdinal = 0
  let eventQueue: Promise<void> = Promise.resolve()

  const collectEvent = async (event: any): Promise<void> => {
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
    if (seen.has(key)) return
    seen.add(key)
    ordinal++
    const identity = identities.get(JSON.stringify([part.sessionID, part.messageID]))
    const tokensState = objectMeasurementState(tokens)
    const cacheState = tokensState === "measured" ? objectMeasurementState(cache) : tokensState
    const observation: any = {
      session_id: part.sessionID,
      message_id: part.messageID,
      part_id: part.id,
      ordinal,
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
    if (identity) {
      observation.provider = identity.provider
      observation.model = identity.model
    }
    await ktxTelemetry(JSON.stringify(observation))
  }

  return {
    "event": async ({ event }: any) => {
      // The wrapper owns all evidence when it supplies the session.
      if (process.env.KTX_SESSION_ID) return
	  if (required) {
		epochOrdinal++
		const acceptedOrdinal = epochOrdinal
		const next = eventQueue.then(async () => {
		  const ack = await ktxRequiredCommand(["telemetry", "event-record"], JSON.stringify({ epoch_id: epochID, epoch_ordinal: acceptedOrdinal, after_ordinal: globalOrdinal, event }))
		  globalOrdinal = requiredAck(ack, { sessionID: requiredSessionID, intervalID: requiredIntervalID, epochID, epochOrdinal: acceptedOrdinal, kind: requiredEventKind(event), contract: requiredContract, afterOrdinal: globalOrdinal })
		})
		eventQueue = next.catch(() => undefined)
		await next
		return
	  }
      const next = eventQueue.then(() => collectEvent(event)).catch((e) => {
        console.error("[ktx] telemetry collector error: " + String(e))
      })
      eventQueue = next
      await next
    },
    "tool.execute.after": async (input: any, output: any) => {
      // Noop when wrapper is active
      if (process.env.KTX_SESSION_ID) return

      const record = {
        runtime: "opencode",
        input: input,
        output: output,
      }

	  let envelope: KtxEnvelope | null
	  if (required) {
		const next = eventQueue.then(async () => {
		  const strict = await ktxRequiredCommand(["hook"], JSON.stringify(record))
		  return requiredEnvelope(strict, input, requiredSessionID)
		})
		eventQueue = next.then(() => undefined, () => undefined)
		envelope = await next
	  } else {
		envelope = await ktxHook(JSON.stringify(record))
	  }
      if (envelope) {
        // Mutate output.output to the compressed bytes.
        // The agent sees the compressed output instead of the original.
        if (output && typeof output === "object" && "output" in output) {
          output.output = envelope.output
        }
      }
      // On failure (envelope === null), output is left unchanged.
      // The agent sees the original tool output (conservative failure).
    },
  }
}

export default KtxPlugin
`

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
	_, err := initLogic(args, opts.Format, stdout, stderr)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// initLogic installs, removes, or inspects a runtime adapter.
// parseInitArgs handles
// `ktx init opencode --show` correctly (the runtime name is
// positional, so stdlib flag would stop at it). The walker is
// shared with the dispatch argument parser.
func initLogic(args []string, format string, stdout, stderr io.Writer) (any, error) {
	f, runtime, err := parseInitArgs(args)
	if err != nil {
		return nil, usageError("ktx init: %v", err)
	}
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
			printError(stderr, internalError("ktx init opencode: remove %q: %v", p, err), format)
			return internalError("ktx init opencode: remove %q: %v", p, err)
		}
		removed = append(removed, p)
	}
	if len(removed) == 0 {
		printError(stderr, notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath), format)
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
	hasGlobal := fileExists(globalPath)
	hasProject := fileExists(projectPath)

	if !hasGlobal && !hasProject {
		printError(stderr, notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath), format)
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
	writeShowReport(stdout, primary, also, format)
	return nil
}

// fileExists reports whether path is a regular file (or symlink to a
// regular file). It does not follow symlinks for the existence check —
// os.Stat follows them, which is what we want: a broken symlink reports
// as not-existing.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
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
func writeShowReport(w io.Writer, primary string, also []string, format string) {
	if format == FormatText {
		fmt.Fprintf(w, "ktx opencode plugin installed at %s\n", primary)
		for _, p := range also {
			fmt.Fprintf(w, "  (also installed at %s)\n", p)
		}
		return
	}
	payload := map[string]any{
		"installed": true,
		"path":      primary,
	}
	if len(also) > 0 {
		payload["also"] = also
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// parseInitDispatchArgs consumes the runtime positional and init flags.
func parseInitDispatchArgs(args []string) ([]string, any, error) {
	f, runtime, err := parseInitArgs(args)
	if err != nil {
		return nil, nil, err
	}
	return nil, initParsedArgs{flags: f, runtime: runtime}, nil
}

// initCommandFunc is the dispatch wrapper. The dispatcher already
// parsed the init-specific arguments and carries them on the context.
//
// NeedsDB=false: ktx init never touches the ktx database — it
// only writes/reads/removes plugin files on disk.
func initCommandFunc(ctx *DispatchContext) (any, error) {
	parsed, ok := ctx.ParsedArgs.(initParsedArgs)
	if !ok {
		return nil, internalError("ktx init: command arguments were not parsed")
	}
	f := parsed.flags
	runtime := parsed.runtime

	if f.showHelp {
		printInitHelp(ctx.Stdout)
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
		return nil, initOpencodeLogic(&f, ctx.Format, ctx.Stdout, ctx.Stderr)
	case "codex":
		return nil, initCodexLogic(&f, ctx.Format, ctx.Stdout, ctx.Stderr)
	default:
		return nil, usageError("ktx init: unknown runtime %q; supported: opencode, codex", runtime)
	}
}

// init registers `ktx init` with the dispatch module. After init runs,
// the verb is reachable through runWith.
func init() {
	Register("init", RegisterOptions{
		Description: "install a runtime adapter",
		NeedsDB:     false,
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
	for _, a := range args {
		switch a {
		case "--project":
			f.project = true
		case "--uninstall":
			f.uninstall = true
		case "--show":
			f.show = true
		case "--help", "-h":
			f.showHelp = true
		default:
			if len(a) > 1 && a[0] == '-' {
				return f, "", fmt.Errorf("unknown flag %q", a)
			}
			if runtime == "" {
				runtime = a
				continue
			}
			return f, "", fmt.Errorf("unexpected argument %q", a)
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
	fmt.Fprintln(w, "  -h, --help     show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  action succeeded (install, uninstall of at least one file, show found)")
	fmt.Fprintln(w, "  1  usage error (unknown runtime, conflicting flags)")
	fmt.Fprintln(w, "  2  not found (--show found nothing, --uninstall found nothing)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The OpenCode plugin subscribes to event for optional model-step telemetry and")
	fmt.Fprintln(w, "tool.execute.after for tool-result compression. It delegates to `ktx telemetry")
	fmt.Fprintln(w, "record` and `ktx hook` respectively. When KTX_SESSION_ID is")
	fmt.Fprintln(w, "set in the environment, the plugin noops so the wrapper")
	fmt.Fprintln(w, "path remains the single source of truth.")
	fmt.Fprintln(w, "The wrapper records identifiable OpenCode step_finish model telemetry itself")
	fmt.Fprintln(w, "while the generated hooks noop, preventing duplicate evidence.")
	fmt.Fprintf(w, "Telemetry is %s. It is %s.\n", opencodeTelemetryEvidence, opencodeTelemetryClaimBoundary)
	fmt.Fprintf(w, "Re-run `%s` to refresh the generated global plugin, or add --project\n", opencodeRefreshCommand)
	fmt.Fprintf(w, "for the project plugin. Troubleshooting: %s\n", opencodeTroubleshooting)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The Codex hook entry is identified by command \"ktx hook\" inside the")
	fmt.Fprintln(w, "PostToolUse array of hooks.json. Install is idempotent (no-op when the")
	fmt.Fprintln(w, "entry is already present); uninstall removes only ktx's entry and")
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
