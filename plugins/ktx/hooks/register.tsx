// ktx: shows ktx compression figures in Claude Code.
//
// On session start it registers /ktx, checks that the ktx on PATH is at least
// the release this plugin needs, and polls `ktx stats summary` every 10
// seconds. An older ktx release turns the mod off: the band and /ktx show one
// line saying to upgrade. A band above the prompt shows one summary line for
// the current window.
// `/ktx` toggles a one-line pane with the same summary and buttons for the 15,
// 30, and 60 minute windows; `/ktx 30` picks a window and opens the pane. The
// band steps aside while the pane is drawn. Whether the pane is open is kept
// across sessions, and an interactive session opens it again on start. The
// figures cover every runtime ktx records, not only this session.

import { atom, read, update } from "claude-code";
import type { EngineInterface, Register, Timer } from "claude-code";

import type { KtxStatsSnapshot, KtxStatsWindow } from "../types/index.d.ts";
import {
  defaultKtxStatsWindow,
  errorMessage,
  ktxStatsWindows,
  outdatedLine,
  parseKtxSummaryRun,
  parseWindowArgument,
  summaryLine,
} from "./stats.ts";

const KTX_EXECUTABLE = "ktx";
const PANE = "ktx";
// The $.store key that keeps whether the person left the pane open.
const PANE_OPEN_KEY = "paneOpen";
const POLL_MS = 10_000;
const TIMEOUT_MS = 5_000;

const selectedWindow = atom(
  { plugin: "ktx", key: "window" } as const,
  defaultKtxStatsWindow,
);
const lastSnapshot = atom({ plugin: "ktx", key: "snapshot" } as const, null);
const paneOpen = atom({ plugin: "ktx", key: "paneOpen" } as const, false);
const outdated = atom({ plugin: "ktx", key: "outdated" } as const, null);

// Module variables reset when the module reloads; the drawn state lives in
// $.state and survives.
let poll: Timer | undefined;
let inFlight: Promise<void> | undefined;

// Runs one summary for the selected window. A failure keeps the last good
// data for that window and marks it stale; nothing about a failure is kept
// for later polls, which always run the command again.
async function refreshOnce($: EngineInterface): Promise<void> {
  const window = await read($, selectedWindow);
  let next: KtxStatsSnapshot;
  try {
    const result = await $.process.run(
      [
        KTX_EXECUTABLE,
        "stats",
        "summary",
        "--window",
        `${window}m`,
        "--format",
        "json",
      ],
      { timeoutMs: TIMEOUT_MS },
    );
    next = { window, data: parseKtxSummaryRun(result, window), error: null };
  } catch (cause) {
    const previous = await read($, lastSnapshot);
    next = {
      window,
      data: previous?.window === window ? previous.data : null,
      error: errorMessage(cause),
    };
  }
  if ((await read($, selectedWindow)) !== window) return;
  await update($, lastSnapshot, () => next);
}

// Starts a refresh unless one is running; polls never overlap.
function refresh($: EngineInterface): Promise<void> {
  inFlight ??= refreshOnce($)
    .catch(() => undefined)
    .finally(() => {
      inFlight = undefined;
    });
  return inFlight;
}

async function selectWindow(
  $: EngineInterface,
  window: KtxStatsWindow,
): Promise<void> {
  await update($, selectedWindow, () => window);
  await inFlight;
  await refresh($);
}

// Opens the pane and notes whether a surface draws it. An open the person did
// not ask for waits undrawn on a narrow terminal; the band stays until then.
async function openPane($: EngineInterface): Promise<void> {
  const opened = await $.ui.open({ id: PANE, title: "ktx", rows: 1 });
  await update($, paneOpen, () => opened.isPlaced);
}

// Checks the ktx on PATH, then starts polling and restores the pane. An older
// release leaves only the outdated line. A failed check starts as usual, so
// the band reports why ktx is unavailable.
async function start($: EngineInterface): Promise<void> {
  let line: string | null = null;
  try {
    const result = await $.process.run([KTX_EXECUTABLE, "--version"], {
      timeoutMs: TIMEOUT_MS,
    });
    if (result.exitCode === 0) line = outdatedLine(result.stdout);
  } catch {
    // The poll reports the same failure.
  }
  await update($, outdated, () => line);
  if (line !== null) return;
  if (poll === undefined) {
    poll = $.clock.every(POLL_MS, () => {
      void refresh($);
    });
    void refresh($);
  }
  // A failed restore leaves the pane closed; the band still runs.
  const wasOpen = await $.store.get(PANE_OPEN_KEY).catch(() => false);
  if (wasOpen === true) await openPane($).catch(() => undefined);
}

export const register: Register = (on) => {
  on("session.start", async ($, e, next) => {
    // Session start never waits on ktx.
    if (e.isInteractive) void start($).catch(() => undefined);
    // Another plugin or skill may already own /ktx; the band still runs.
    await $.command
      .register({
        name: "ktx",
        description: "Show ktx compression stats for all runtimes",
      })
      .catch(() => undefined);
    return next(e);
  });

  on("command.run", { command: "ktx" }, async ($, e) => {
    const line = await read($, outdated);
    if (line !== null) return { text: line };
    const window = parseWindowArgument(e.args);
    if (window === null) return { text: "Usage: /ktx [15|30|60]" };
    // A pane that waits undrawn counts as closed, so /ktx shows it.
    const isShown = (await $.ui.panes()).some(
      (pane) => pane.id === PANE && pane.isPlaced,
    );
    if (window === undefined && isShown) {
      await $.ui.close({ id: PANE });
      return { text: "ktx stats pane closed." };
    }
    if (window !== undefined) await selectWindow($, window);
    await openPane($);
    await $.store.set(PANE_OPEN_KEY, true);
    if (window === undefined) void refresh($);
    return {
      text: `ktx stats pane opened (${await read($, selectedWindow)}m).`,
    };
  });

  // A close by the person or by /ktx is remembered; an unload is not theirs.
  on("ui.close", async ($, e, next) => {
    const closed = await next(e);
    if (e.id === PANE) {
      await update($, paneOpen, () => false);
      if (e.origin.kind !== "unload")
        await $.store.set(PANE_OPEN_KEY, false).catch(() => undefined);
    }
    return closed;
  });

  // The band yields to a survey and to the drawn pane, and draws nothing until
  // the first poll. An outdated ktx shows its line, since no poll runs.
  on("ui.render", { component: "AbovePrompt" }, async ($, e, next) => {
    if (e.props.hasSurvey) return next(e);
    const { Text } = $.ui.resolve(e);
    const line = await read($, outdated);
    if (line !== null) return <Text dimColor>{line}</Text>;
    const snapshot = await read($, lastSnapshot);
    if (snapshot === null || (await read($, paneOpen))) return next(e);
    const window = await read($, selectedWindow);
    return <Text dimColor>{summaryLine(snapshot, window)}</Text>;
  });

  on("ui.render", { component: "Pane", requestId: PANE }, async ($, e) => {
    const { Box, Button, Text } = $.ui.resolve(e);
    const window = await read($, selectedWindow);
    const snapshot = await read($, lastSnapshot);
    return (
      <Box flexDirection="row" gap={1}>
        <Text>{summaryLine(snapshot, window)}</Text>
        {ktxStatsWindows.map((choice) => (
          <Button
            key={`window-${choice}`}
            label={`${choice}m`}
            variant={choice === window ? "primary" : "secondary"}
            onPress={() => void selectWindow($, choice)}
          />
        ))}
      </Box>
    );
  });
};
