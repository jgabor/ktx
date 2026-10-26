import { spawn, type ChildProcess } from "node:child_process";
import { constants } from "node:fs";
import { access, realpath, stat } from "node:fs/promises";
import { delimiter, isAbsolute, resolve } from "node:path";

const COMMAND_TIMEOUT_MS = 5_000;
const MAX_STDOUT_BYTES = 64 * 1024;
const MAX_STDERR_BYTES = 8 * 1024;
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const TIMESTAMP_TOLERANCE_MS = 1;

export const ktxStatsWindows = [15, 30, 60] as const;
export type KtxStatsWindow = (typeof ktxStatsWindows)[number];
export const defaultKtxStatsWindow: KtxStatsWindow = 15;

export function nextKtxStatsWindow(window: KtxStatsWindow): KtxStatsWindow {
  const index = ktxStatsWindows.indexOf(window);
  return ktxStatsWindows[(index + 1) % ktxStatsWindows.length];
}

export type KtxPromptCacheTokenHitRatePct =
  | { state: "measured"; value: number; contributingModelSteps: number }
  | {
      state: "missing" | "malformed" | "not_applicable";
      value: null;
      contributingModelSteps: number;
    };

export type KtxSummary = {
  schemaVersion: 10;
  window: {
    seconds: number;
    from: string;
    to: string;
  };
  calls: {
    totalWithOutput: number;
    withCompressedArtifact: number;
  };
  tokens: {
    rawEstimated: number;
    deliveredEstimated: number;
  };
  promptCacheTokenHitRatePct: KtxPromptCacheTokenHitRatePct;
};

export type SidebarMetrics = {
  savedTokens: string;
  savedPercentage: string;
  rawTokens: string;
  deliveredTokens: string;
  totalCalls: string;
  compressedCalls: string;
  compressionBar: {
    savedPercentage: number;
  };
};

export type PromptCacheTokenHitRateMetrics = {
  state: KtxPromptCacheTokenHitRatePct["state"];
  display: string;
};

export type ThroughputTone = "muted" | "text" | "primary" | "warning" | "error";

type RecordValue = Record<string, unknown>;

function invalid(path: string, expected: string): never {
  throw new Error(`Invalid ktx stats summary: ${path} must be ${expected}.`);
}

function isRecord(value: unknown): value is RecordValue {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requiredRecord(value: unknown, path: string): RecordValue {
  if (!isRecord(value)) invalid(path, "an object");
  return value;
}

function requiredTimestamp(value: unknown, path: string): string {
  if (
    typeof value !== "string" ||
    !RFC3339.test(value) ||
    !Number.isFinite(Date.parse(value))
  )
    invalid(path, "a valid RFC3339 timestamp string");
  return value;
}

function requiredFiniteNumber(value: unknown, path: string): number {
  if (typeof value !== "number" || !Number.isFinite(value))
    invalid(path, "a finite number");
  return value;
}

function requiredNonNegativeNumber(value: unknown, path: string): number {
  const number = requiredFiniteNumber(value, path);
  if (number < 0) invalid(path, "a non-negative number");
  return number;
}

function requiredCount(value: unknown, path: string): number {
  const count = requiredNonNegativeNumber(value, path);
  if (!Number.isSafeInteger(count)) invalid(path, "a non-negative safe integer");
  return count;
}

function validatePromptCacheTokenHitRatePct(
  value: unknown,
): KtxPromptCacheTokenHitRatePct {
  const hitRate = requiredRecord(value, "prompt_cache_token_hit_rate_pct");
  const contributingModelSteps = requiredCount(
    hitRate.contributing_model_steps,
    "prompt_cache_token_hit_rate_pct.contributing_model_steps",
  );
  if (hitRate.state === "measured") {
    const measured = requiredNonNegativeNumber(
      hitRate.value,
      "prompt_cache_token_hit_rate_pct.value",
    );
    if (measured > 100)
      invalid(
        "prompt_cache_token_hit_rate_pct.value",
        "a percentage from 0 to 100",
      );
    return { state: "measured", value: measured, contributingModelSteps };
  }
  if (
    hitRate.state === "missing" ||
    hitRate.state === "malformed" ||
    hitRate.state === "not_applicable"
  ) {
    if (hitRate.value !== null)
      invalid(
        "prompt_cache_token_hit_rate_pct.value",
        `null when state is ${hitRate.state}`,
      );
    if (contributingModelSteps === 0 && hitRate.state !== "missing")
      invalid(
        "prompt_cache_token_hit_rate_pct.state",
        "missing when contributing_model_steps is zero",
      );
    return {
      state: hitRate.state,
      value: null,
      contributingModelSteps,
    };
  }
  invalid(
    "prompt_cache_token_hit_rate_pct.state",
    '"measured", "missing", "malformed", or "not_applicable"',
  );
}

/** Validates the current ktx summary schema before any sidebar data is used. */
export function validateKtxSummary(
  value: unknown,
  expectedWindow?: KtxStatsWindow,
): KtxSummary {
  const root = requiredRecord(value, "response");
  const schemaVersion = root.schema_version;
  if (schemaVersion !== 10) invalid("schema_version", "10");

  const window = requiredRecord(root.window, "window");
  const seconds = requiredFiniteNumber(window.seconds, "window.seconds");
  if (seconds <= 0) invalid("window.seconds", "a positive finite number");
  if (expectedWindow !== undefined && seconds !== expectedWindow * 60)
    invalid("window.seconds", `equal to the requested ${expectedWindow}m window`);
  const from = requiredTimestamp(window.from, "window.from");
  const to = requiredTimestamp(window.to, "window.to");
  const elapsedMs = Date.parse(to) - Date.parse(from);
  if (elapsedMs < 0)
    invalid("window.to", "at or after window.from");
  if (Math.abs(elapsedMs - seconds * 1_000) > TIMESTAMP_TOLERANCE_MS)
    invalid("window", "an interval matching window.seconds");
  const calls = requiredRecord(root.calls, "calls");
  const totalWithOutput = requiredCount(calls.total_with_output, "calls.total_with_output");
  const withCompressedArtifact = requiredCount(
    calls.with_compressed_artifact,
    "calls.with_compressed_artifact",
  );
  if (withCompressedArtifact > totalWithOutput)
    invalid(
      "calls.with_compressed_artifact",
      "no greater than calls.total_with_output",
    );

  const tokens = requiredRecord(root.tokens, "tokens");
  const rawEstimated = requiredCount(
    tokens.raw_estimated,
    "tokens.raw_estimated",
  );
  const deliveredEstimated = requiredCount(
    tokens.delivered_estimated,
    "tokens.delivered_estimated",
  );
  if (rawEstimated === 0 && deliveredEstimated !== 0)
    invalid("tokens.delivered_estimated", "zero when tokens.raw_estimated is zero");
  if (totalWithOutput === 0 && (rawEstimated !== 0 || deliveredEstimated !== 0))
    invalid("tokens", "zero totals when calls.total_with_output is zero");
  if (withCompressedArtifact === 0 && deliveredEstimated !== rawEstimated)
    invalid(
      "tokens.delivered_estimated",
      "equal to tokens.raw_estimated when no call has a compressed artifact",
    );

  const promptCacheTokenHitRatePct = validatePromptCacheTokenHitRatePct(
    root.prompt_cache_token_hit_rate_pct,
  );

  return {
    schemaVersion,
    window: { seconds, from, to },
    calls: { totalWithOutput, withCompressedArtifact },
    tokens: {
      rawEstimated,
      deliveredEstimated,
    },
    promptCacheTokenHitRatePct,
  };
}

/** Formats an estimated quantity for compact telemetry displays. */
export function formatCompactEstimate(value: number): string {
  const units = ["", "k", "M", "B", "T"];
  const sign = value < 0 ? "-" : "";
  let unit = 0;
  let scaled = Math.abs(value);
  while (scaled >= 1_000 && unit < units.length - 1) {
    scaled /= 1_000;
    unit += 1;
  }
  let rounded = Math.round(scaled);
  if (rounded >= 1_000 && unit < units.length - 1) {
    rounded = Math.round(rounded / 1_000);
    unit += 1;
  }
  return `${sign}${rounded}${units[unit]}`;
}

/** Returns rounded post-compression token throughput for the prompt footer. */
export function deliveredTokensPerSecond(summary: KtxSummary | null): number | null {
  if (!summary) return null;
  return Math.round(summary.tokens.deliveredEstimated / summary.window.seconds);
}

export function throughputTone(tokensPerSecond: number | null): ThroughputTone {
  if (tokensPerSecond === null || tokensPerSecond < 100) return "muted";
  if (tokensPerSecond < 200) return "text";
  if (tokensPerSecond < 300) return "primary";
  if (tokensPerSecond < 400) return "warning";
  return "error";
}

/** Formats post-compression token throughput for the prompt footer. */
export function formatDeliveredTokensPerSecond(
  summary: KtxSummary | null,
): string {
  const value = deliveredTokensPerSecond(summary);
  return value === null ? "" : `${value} tokens/s`;
}

export function formatReductionPercent(value: number | null): string {
  return value === null ? "—" : `${Math.round(value)}%`;
}

/** Produces the compact prompt-cache token hit-rate view used by the sidebar. */
export function formatPromptCacheTokenHitRateMetrics(
  hitRate: KtxPromptCacheTokenHitRatePct,
): PromptCacheTokenHitRateMetrics {
  const display =
    hitRate.state === "measured"
      ? `${Math.round(hitRate.value)}%`
      : hitRate.state === "missing"
        ? "—"
        : hitRate.state === "malformed"
          ? "invalid"
          : "n/a";
  return {
    state: hitRate.state,
    display,
  };
}

export function isNoActivity(summary: KtxSummary): boolean {
  return summary.calls.totalWithOutput === 0;
}

/** Calculates the saved share for the full-width compression bar. */
export function formatCompressionBar(
  rawEstimated: number,
  deliveredEstimated: number,
): SidebarMetrics["compressionBar"] {
  if (rawEstimated === 0) return { savedPercentage: 0 };
  return {
    savedPercentage: Math.min(
      100,
      Math.max(0, ((rawEstimated - deliveredEstimated) / rawEstimated) * 100),
    ),
  };
}

/** Produces the compression-focused metrics used by the sidebar panel. */
export function formatSidebarMetrics(summary: KtxSummary): SidebarMetrics | null {
  if (isNoActivity(summary)) return null;
  const { calls, tokens } = summary;
  const reductionPercent =
    tokens.rawEstimated === 0
      ? null
      : ((tokens.rawEstimated - tokens.deliveredEstimated) / tokens.rawEstimated) *
        100;
  const savedTokens = tokens.rawEstimated - tokens.deliveredEstimated;
  return {
    savedTokens: formatCompactEstimate(savedTokens),
    savedPercentage: formatReductionPercent(reductionPercent),
    rawTokens: formatCompactEstimate(tokens.rawEstimated),
    deliveredTokens: formatCompactEstimate(tokens.deliveredEstimated),
    totalCalls: String(calls.totalWithOutput),
    compressedCalls: String(calls.withCompressedArtifact),
    compressionBar: formatCompressionBar(
      tokens.rawEstimated,
      tokens.deliveredEstimated,
    ),
  };
}

function conciseDiagnostic(output: string): string {
  const line = output.trim().replaceAll(/\s+/g, " ");
  return line ? `: ${line.slice(0, 240)}` : "";
}

function abortError(): Error {
  return new Error("ktx stats refresh was cancelled.");
}

export type KtxCommandOptions = {
  executable?: string;
  timeoutMs?: number;
  maxStdoutBytes?: number;
  signal?: AbortSignal;
  window?: KtxStatsWindow;
};

async function firstExecutableFile(candidates: string[]): Promise<string> {
  for (const candidate of candidates) {
    try {
      if (!(await stat(candidate)).isFile()) continue;
      await access(candidate, constants.X_OK);
      return await realpath(candidate);
    } catch {}
  }
  throw new Error("ktx was not found on PATH. Install ktx or add it to PATH.");
}

/**
 * Mirrors a PATH search, as the server target does: the first regular,
 * executable file named name in a non-empty entry of pathValue wins. Returns
 * its canonical path.
 */
export function findOnPath(name: string, pathValue: string): Promise<string> {
  return firstExecutableFile(
    pathValue.split(delimiter).filter((directory) => directory !== "").map((directory) => resolve(directory, name)),
  );
}

function resolveExecutable(executable: string): Promise<string> {
  if (isAbsolute(executable) || executable.includes("/")) return firstExecutableFile([resolve(executable)]);
  return findOnPath(executable, process.env.PATH ?? "");
}

let pinnedKtxExecutable: Promise<string> | undefined;

function getKtxExecutable(executable?: string): Promise<string> {
  if (executable) return resolveExecutable(executable);
  // A failed lookup is not kept, so ktx installed later is found without a
  // restart.
  pinnedKtxExecutable ??= resolveExecutable("ktx").catch((error: unknown) => {
    pinnedKtxExecutable = undefined;
    throw error;
  });
  return pinnedKtxExecutable;
}

function runKtx(
  executable: string,
  args: string[],
  options: Required<Pick<KtxCommandOptions, "timeoutMs" | "maxStdoutBytes">> &
    Pick<KtxCommandOptions, "signal"> & { label: string },
): Promise<string> {
  return new Promise((resolve, reject) => {
    if (options.signal?.aborted) {
      reject(abortError());
      return;
    }

    let child: ChildProcess;
    try {
      child = spawn(executable, args, {
        shell: false,
        stdio: ["ignore", "pipe", "pipe"],
      });
    } catch {
      reject(
        new Error("Unable to start ktx. Check that it is installed and on PATH."),
      );
      return;
    }

    const stdout: Buffer[] = [];
    const stderr: Buffer[] = [];
    let stdoutBytes = 0;
    let stderrBytes = 0;
    let settled = false;
    const finish = (fn: () => void) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      options.signal?.removeEventListener("abort", onAbort);
      fn();
    };
    const stop = () => {
      if (!child.killed) child.kill();
    };
    const fail = (message: string) => finish(() => reject(new Error(message)));
    const onAbort = () => {
      stop();
      fail(abortError().message);
    };
    const timer = setTimeout(() => {
      stop();
      fail(`${options.label} timed out. Check ktx and retry.`);
    }, options.timeoutMs);

    options.signal?.addEventListener("abort", onAbort, { once: true });
    child.stdout?.on("data", (chunk: Buffer) => {
      stdoutBytes += chunk.length;
      if (stdoutBytes > options.maxStdoutBytes) {
        stop();
        fail(`${options.label} output was too large. Update ktx and retry.`);
        return;
      }
      stdout.push(chunk);
    });
    child.stderr?.on("data", (chunk: Buffer) => {
      if (stderrBytes >= MAX_STDERR_BYTES) return;
      const remaining = MAX_STDERR_BYTES - stderrBytes;
      stderr.push(chunk.subarray(0, remaining));
      stderrBytes += Math.min(chunk.length, remaining);
    });
    child.on("error", (cause: NodeJS.ErrnoException) => {
      if (cause.code === "ENOENT")
        fail("ktx was not found on PATH. Install ktx or add it to PATH.");
      else fail(`Unable to start ${options.label}. Check ktx and retry.`);
    });
    child.on("close", (code) => {
      if (settled) return;
      if (code !== 0) {
        const diagnostic =
          Buffer.concat(stderr).toString("utf8") ||
          Buffer.concat(stdout).toString("utf8");
        fail(
          `${options.label} failed (exit ${code ?? "unknown"})${conciseDiagnostic(diagnostic)}`,
        );
        return;
      }
      const text = Buffer.concat(stdout).toString("utf8");
      finish(() => resolve(text));
    });
  });
}

/** Proves the executable identity, then returns only schema-validated summary data. */
export async function readKtxSummary(
  options: KtxCommandOptions = {},
): Promise<KtxSummary> {
  const executable = await getKtxExecutable(options.executable);
  const timeoutMs = options.timeoutMs ?? COMMAND_TIMEOUT_MS;
  const maxStdoutBytes = options.maxStdoutBytes ?? MAX_STDOUT_BYTES;
  const window = options.window ?? defaultKtxStatsWindow;
  const text = await runKtx(
    executable,
    [
      "stats",
      "summary",
      "--window",
      `${window}m`,
      "--format",
      "json",
    ],
    {
      timeoutMs,
      maxStdoutBytes,
      signal: options.signal,
      label: "ktx stats summary",
    },
  );
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    throw new Error("ktx stats summary returned invalid JSON. Update ktx and retry.");
  }
  try {
    return validateKtxSummary(parsed, window);
  } catch {
    throw new Error("ktx returned invalid stats data. Update ktx and retry.");
  }
}

/** Returns what the pinned ktx executable prints for `ktx --version`. */
export async function readKtxVersion(
  options: Omit<KtxCommandOptions, "window"> = {},
): Promise<string> {
  const executable = await getKtxExecutable(options.executable);
  const text = await runKtx(executable, ["--version"], {
    timeoutMs: options.timeoutMs ?? COMMAND_TIMEOUT_MS,
    maxStdoutBytes: options.maxStdoutBytes ?? MAX_STDOUT_BYTES,
    signal: options.signal,
    label: "ktx --version",
  });
  return text.trim();
}

/** The oldest ktx release this package runs against. */
export const minimumKtxVersion = "v0.1.1";

/**
 * Returns [major, minor, patch] for an exact release version such as v1.2.3,
 * or null for anything else: dev, pseudo-versions, prereleases and builds
 * with metadata.
 */
export function parseReleaseVersion(
  text: string,
): [number, number, number] | null {
  const match = /^v(\d+)\.(\d+)\.(\d+)$/.exec(text.trim());
  return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null;
}

/**
 * The line the sidebar shows instead of its figures when the ktx on PATH is
 * an older release than this package needs, or null when the version passes.
 * A version that is not an exact release passes.
 */
export function outdatedLine(
  found: string,
  minimum: string = minimumKtxVersion,
): string | null {
  const have = parseReleaseVersion(found);
  const need = parseReleaseVersion(minimum);
  if (have === null || need === null) return null;
  for (let i = 0; i < 3; i++) {
    if (have[i]! > need[i]!) return null;
    if (have[i]! < need[i]!)
      return `ktx ${found.trim()} is older than ${minimum}; upgrade ktx`;
  }
  return null;
}

export type KtxStatsState = {
  data: KtxSummary | null;
  error: string | null;
  loading: boolean;
  window: KtxStatsWindow;
  /** Set when ktx is older than this package needs; statistics stay off. */
  outdated: string | null;
};

export type KtxStatsStore = {
  getState: () => KtxStatsState;
  subscribe: (listener: (state: KtxStatsState) => void) => () => void;
  refresh: () => Promise<void>;
  setWindow: (window: KtxStatsWindow) => Promise<void>;
  startPolling: () => void;
  /** Checks the ktx version, then refreshes and polls unless ktx is too old. */
  start: () => Promise<void>;
  dispose: () => void;
};

export type KtxStatsStoreOptions = {
  read?: (signal: AbortSignal, window: KtxStatsWindow) => Promise<KtxSummary>;
  readVersion?: (signal: AbortSignal) => Promise<string>;
  pollMs?: number;
  window?: KtxStatsWindow;
  setInterval?: (handler: () => void, ms: number) => ReturnType<typeof setInterval>;
  clearInterval?: (handle: ReturnType<typeof setInterval>) => void;
};

/** Holds the last valid result, coalesces refreshes, and owns polling cleanup. */
export function createKtxStatsStore(
  options: KtxStatsStoreOptions = {},
): KtxStatsStore {
  const read: (
    signal: AbortSignal,
    window: KtxStatsWindow,
  ) => Promise<KtxSummary> =
    options.read ?? ((signal, window) => readKtxSummary({ signal, window }));
  const readVersion =
    options.readVersion ?? ((signal) => readKtxVersion({ signal }));
  const pollMs = options.pollMs ?? 10_000;
  const setPollingInterval = options.setInterval ?? setInterval;
  const clearPollingInterval = options.clearInterval ?? clearInterval;
  const listeners = new Set<(state: KtxStatsState) => void>();
  let state: KtxStatsState = {
    data: null,
    error: null,
    loading: false,
    window: options.window ?? defaultKtxStatsWindow,
    outdated: null,
  };
  let inFlight:
    | {
        operation: Promise<void>;
        controller: AbortController;
        window: KtxStatsWindow;
      }
    | null = null;
  let interval: ReturnType<typeof setInterval> | null = null;
  let versionCheck: AbortController | null = null;
  let started = false;
  let disposed = false;

  const notify = () => listeners.forEach((listener) => listener(state));
  const update = (next: KtxStatsState) => {
    state = next;
    notify();
  };

  // Statistics wait for the version check; start refreshes once it passes.
  const refresh = (): Promise<void> => {
    if (disposed || state.outdated || versionCheck) return Promise.resolve();
    const requestedWindow = state.window;
    if (inFlight?.window === requestedWindow) return inFlight.operation;
    inFlight?.controller.abort();

    const controller = new AbortController();
    update({ ...state, loading: true });
    const operation = read(controller.signal, requestedWindow)
      .then((data) => {
        if (!disposed && state.window === requestedWindow)
          update({
            data,
            error: null,
            loading: true,
            window: requestedWindow,
            outdated: state.outdated,
          });
      })
      .catch((cause: unknown) => {
        if (
          !disposed &&
          state.window === requestedWindow &&
          !controller.signal.aborted
        )
          update({
            ...state,
            error:
              cause instanceof Error
                ? cause.message
                : "Unable to refresh ktx stats.",
            loading: true,
          });
      })
      .finally(() => {
        if (inFlight?.operation !== operation) return;
        inFlight = null;
        if (!disposed && state.window === requestedWindow)
          update({ ...state, loading: false });
      });
    inFlight = { operation, controller, window: requestedWindow };
    return operation;
  };

  const setWindow = (window: KtxStatsWindow): Promise<void> => {
    if (disposed || state.outdated) return Promise.resolve();
    if (state.window === window) return refresh();
    update({
      data: null,
      error: null,
      loading: true,
      window,
      outdated: null,
    });
    return refresh();
  };

  const startPolling = () => {
    if (!disposed && !state.outdated && !interval)
      interval = setPollingInterval(() => void refresh(), pollMs);
  };

  const start = async (): Promise<void> => {
    if (disposed || started) return;
    started = true;
    const controller = new AbortController();
    versionCheck = controller;
    let line: string | null = null;
    try {
      line = outdatedLine(await readVersion(controller.signal));
    } catch {
      // A failed version read is left to the first refresh to report.
    }
    if (versionCheck === controller) versionCheck = null;
    if (disposed) return;
    if (line !== null) {
      inFlight?.controller.abort();
      inFlight = null;
      update({ ...state, data: null, error: null, loading: false, outdated: line });
      return;
    }
    void refresh();
    startPolling();
  };

  return {
    getState: () => state,
    subscribe: (listener) => {
      listeners.add(listener);
      listener(state);
      return () => listeners.delete(listener);
    },
    refresh,
    setWindow,
    startPolling,
    start,
    dispose: () => {
      if (disposed) return;
      disposed = true;
      versionCheck?.abort();
      versionCheck = null;
      if (interval) clearPollingInterval(interval);
      interval = null;
      inFlight?.controller.abort();
      inFlight = null;
      listeners.clear();
    },
  };
}
