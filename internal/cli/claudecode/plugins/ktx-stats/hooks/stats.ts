// Pure helpers for the ktx-stats mod: strict validation of
// `ktx stats summary --format json` output and the text the band above the
// prompt and the /ktx pane draw. Nothing here uses the engine, so it also runs under Node.

import type {
  KtxStatsSnapshot,
  KtxStatsWindow,
  KtxSummary,
} from "../types/index.d.ts";

export const ktxStatsWindows: readonly KtxStatsWindow[] = [15, 30, 60];
export const defaultKtxStatsWindow: KtxStatsWindow = 15;

const RFC3339 =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const TIMESTAMP_TOLERANCE_MS = 1;

type RecordValue = Record<string, unknown>;

function invalid(path: string, expected: string): never {
  throw new Error(`${path} must be ${expected}`);
}

function requiredRecord(value: unknown, path: string): RecordValue {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    invalid(path, "an object");
  return value as RecordValue;
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

function requiredCount(value: unknown, path: string): number {
  const count = requiredFiniteNumber(value, path);
  if (count < 0 || !Number.isSafeInteger(count))
    invalid(path, "a non-negative safe integer");
  return count;
}

function validatePromptCacheHitRate(value: unknown): void {
  const path = "prompt_cache_token_hit_rate_pct";
  const hitRate = requiredRecord(value, path);
  const steps = requiredCount(
    hitRate.contributing_model_steps,
    `${path}.contributing_model_steps`,
  );
  if (hitRate.state === "measured") {
    const measured = requiredFiniteNumber(hitRate.value, `${path}.value`);
    if (measured < 0 || measured > 100)
      invalid(`${path}.value`, "a percentage from 0 to 100");
    return;
  }
  if (
    hitRate.state === "missing" ||
    hitRate.state === "malformed" ||
    hitRate.state === "not_applicable"
  ) {
    if (hitRate.value !== null)
      invalid(`${path}.value`, `null when state is ${hitRate.state}`);
    if (steps === 0 && hitRate.state !== "missing")
      invalid(`${path}.state`, "missing when contributing_model_steps is zero");
    return;
  }
  invalid(
    `${path}.state`,
    '"measured", "missing", "malformed", or "not_applicable"',
  );
}

/** Validates schema 10 of `ktx stats summary --format json` for a window. */
export function validateKtxSummary(
  value: unknown,
  expectedWindow: KtxStatsWindow,
): KtxSummary {
  const root = requiredRecord(value, "response");
  if (root.schema_version !== 10) invalid("schema_version", "10");

  const window = requiredRecord(root.window, "window");
  const seconds = requiredFiniteNumber(window.seconds, "window.seconds");
  if (seconds !== expectedWindow * 60)
    invalid(
      "window.seconds",
      `equal to the requested ${expectedWindow}m window`,
    );
  const from = requiredTimestamp(window.from, "window.from");
  const to = requiredTimestamp(window.to, "window.to");
  const elapsedMs = Date.parse(to) - Date.parse(from);
  if (elapsedMs < 0) invalid("window.to", "at or after window.from");
  if (Math.abs(elapsedMs - seconds * 1_000) > TIMESTAMP_TOLERANCE_MS)
    invalid("window", "an interval matching window.seconds");

  const calls = requiredRecord(root.calls, "calls");
  const totalWithOutput = requiredCount(
    calls.total_with_output,
    "calls.total_with_output",
  );
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
    invalid(
      "tokens.delivered_estimated",
      "zero when tokens.raw_estimated is zero",
    );
  if (totalWithOutput === 0 && (rawEstimated !== 0 || deliveredEstimated !== 0))
    invalid("tokens", "zero totals when calls.total_with_output is zero");
  if (withCompressedArtifact === 0 && deliveredEstimated !== rawEstimated)
    invalid(
      "tokens.delivered_estimated",
      "equal to tokens.raw_estimated when no call has a compressed artifact",
    );

  validatePromptCacheHitRate(root.prompt_cache_token_hit_rate_pct);

  return {
    windowSeconds: seconds,
    from,
    to,
    totalCalls: totalWithOutput,
    compressedCalls: withCompressedArtifact,
    rawTokens: rawEstimated,
    deliveredTokens: deliveredEstimated,
  };
}

export type KtxRunResult = {
  exitCode: number;
  stdout: string;
  stderr: string;
};

function conciseDiagnostic(output: string): string {
  const line = output.trim().replaceAll(/\s+/g, " ");
  return line ? `: ${line.slice(0, 240)}` : "";
}

/** Turns one `ktx stats summary` run into validated data, or throws why not. */
export function parseKtxSummaryRun(
  result: KtxRunResult,
  window: KtxStatsWindow,
): KtxSummary {
  if (result.exitCode !== 0)
    throw new Error(
      `ktx stats summary failed (exit ${result.exitCode})${conciseDiagnostic(result.stderr || result.stdout)}`,
    );
  let parsed: unknown;
  try {
    parsed = JSON.parse(result.stdout);
  } catch {
    throw new Error("ktx stats summary returned invalid JSON");
  }
  try {
    return validateKtxSummary(parsed, window);
  } catch (cause) {
    throw new Error(`ktx returned invalid stats data: ${errorMessage(cause)}`);
  }
}

export function errorMessage(cause: unknown): string {
  if (cause instanceof Error && cause.message) return cause.message;
  if (typeof cause === "string" && cause) return cause;
  return "unable to refresh ktx stats";
}

/** Formats an estimate with k, M, B, or T, keeping one decimal below 10. */
export function formatCompactEstimate(value: number): string {
  const units = ["", "k", "M", "B", "T"];
  const sign = value < 0 ? "-" : "";
  let unit = 0;
  let scaled = Math.abs(value);
  while (scaled >= 1_000 && unit < units.length - 1) {
    scaled /= 1_000;
    unit += 1;
  }
  if (unit > 0 && scaled < 9.95) {
    return `${sign}${Math.round(scaled * 10) / 10}${units[unit]}`;
  }
  let rounded = Math.round(scaled);
  if (rounded >= 1_000 && unit < units.length - 1) {
    rounded = Math.round(rounded / 1_000);
    unit += 1;
  }
  return `${sign}${rounded}${units[unit]}`;
}

/** The saved share of raw tokens, clamped to 0..100; 0 when raw is 0. */
export function savedPercentage(raw: number, delivered: number): number {
  if (raw === 0) return 0;
  return Math.min(100, Math.max(0, ((raw - delivered) / raw) * 100));
}

/** The saved share as text: a rounded percent, or "—" when raw is 0. */
export function formatSavedPercent(raw: number, delivered: number): string {
  return raw === 0 ? "—" : `${Math.round(((raw - delivered) / raw) * 100)}%`;
}

/** A text bar of width cells, filled to the clamped percentage. */
export function textBar(percentage: number, width: number): string {
  const cells = Math.max(1, Math.floor(width));
  const share = Math.min(100, Math.max(0, percentage));
  const filled = Math.round((share / 100) * cells);
  return "█".repeat(filled) + "░".repeat(cells - filled);
}

export type KtxSummaryView = {
  savedTokens: string;
  savedPercent: string;
  barPercentage: number;
  rawTokens: string;
  deliveredTokens: string;
  compressedCalls: string;
  totalCalls: string;
};

/** The figures the band and pane draw; null when no call had output. */
export function summaryView(summary: KtxSummary): KtxSummaryView | null {
  if (summary.totalCalls === 0) return null;
  const { rawTokens: raw, deliveredTokens: delivered } = summary;
  return {
    savedTokens: formatCompactEstimate(raw - delivered),
    savedPercent: formatSavedPercent(raw, delivered),
    barPercentage: savedPercentage(raw, delivered),
    rawTokens: formatCompactEstimate(raw),
    deliveredTokens: formatCompactEstimate(delivered),
    compressedCalls: String(summary.compressedCalls),
    totalCalls: String(summary.totalCalls),
  };
}

export function shortError(error: string, maxLength: number): string {
  return error.length > maxLength
    ? `${error.slice(0, Math.max(1, maxLength - 1))}…`
    : error;
}

/** The one line drawn in the band above the prompt. */
export function bandLine(
  snapshot: KtxStatsSnapshot | null,
  window: KtxStatsWindow,
): string {
  const current =
    snapshot !== null && snapshot.window === window ? snapshot : null;
  if (current === null || (current.data === null && current.error === null))
    return `ktx ${window}m: loading stats…`;
  if (current.data === null)
    return `ktx ${window}m: unavailable (${shortError(current.error ?? "", 80)})`;
  const stale = current.error !== null ? " (stale)" : "";
  const view = summaryView(current.data);
  if (view === null) return `ktx: no calls in ${window}m${stale}`;
  return `ktx ${window}m${stale}: ${view.savedPercent} saved · ${view.rawTokens}→${view.deliveredTokens} tok · ${view.compressedCalls}/${view.totalCalls} calls`;
}

export type PaneLine = { text: string; tone: "normal" | "dim" | "error" };

/** The rows of the /ktx pane below its heading, for a bar of barWidth cells. */
export function paneLines(
  snapshot: KtxStatsSnapshot | null,
  window: KtxStatsWindow,
  barWidth: number,
): PaneLine[] {
  const current =
    snapshot !== null && snapshot.window === window ? snapshot : null;
  const lines: PaneLine[] = [];
  if (current?.error) {
    const prefix = current.data ? "Stale: " : "Unavailable: ";
    lines.push({
      text: prefix + shortError(current.error, 160),
      tone: "error",
    });
  }
  if (!current?.data) {
    if (!current?.error)
      lines.push({ text: "Loading ktx stats…", tone: "dim" });
    return lines;
  }
  const view = summaryView(current.data);
  if (view === null) {
    lines.push({ text: `No calls in ${window}m`, tone: "dim" });
    return lines;
  }
  lines.push(
    {
      text: `Saved      ${view.savedTokens} tokens (${view.savedPercent})`,
      tone: "normal",
    },
    { text: textBar(view.barPercentage, barWidth), tone: "normal" },
    {
      text: `Tokens     ${view.rawTokens} → ${view.deliveredTokens}`,
      tone: "dim",
    },
    {
      text: `Calls      ${view.compressedCalls} compressed / ${view.totalCalls} total`,
      tone: "dim",
    },
  );
  return lines;
}
