// ktx-stats: shows ktx compression figures in Claude Code.
//
// On session start it registers /ktx and polls `ktx stats summary` every
// 10 seconds through the ktx executable pinned when the plugin was generated.
// A band above the prompt shows one summary line for the current window.
// `/ktx` toggles a one-line pane with the same summary and buttons for the 15,
// 30, and 60 minute windows; `/ktx 30` picks a window and opens the pane. The
// band steps aside while the pane is open. The figures cover every runtime
// ktx records, not only this session.

import { atom, read, update } from "claude-code";
import type { EngineInterface, Register, Timer } from "claude-code";

import type { KtxStatsSnapshot, KtxStatsWindow } from "../types/index.d.ts";
import {
  defaultKtxStatsWindow,
  errorMessage,
  ktxStatsWindows,
  parseKtxSummaryRun,
  parseWindowArgument,
  summaryLine,
} from "./stats.ts";

const KTX_EXECUTABLE: string = "__KTX_EXECUTABLE__";
const PANE = "ktx-stats";
const POLL_MS = 10_000;
const TIMEOUT_MS = 5_000;

const selectedWindow = atom(
  { plugin: "ktx-stats", key: "window" } as const,
  defaultKtxStatsWindow,
);
const lastSnapshot = atom(
  { plugin: "ktx-stats", key: "snapshot" } as const,
  null,
);
const paneOpen = atom({ plugin: "ktx-stats", key: "paneOpen" } as const, false);

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

export const register: Register = (on) => {
  on("session.start", async ($, e, next) => {
    if (e.isInteractive && poll === undefined) {
      poll = $.clock.every(POLL_MS, () => {
        void refresh($);
      });
      void refresh($);
    }
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
    const window = parseWindowArgument(e.args);
    if (window === null) return { text: "Usage: /ktx [15|30|60]" };
    const isOpen = (await $.ui.panes()).some((pane) => pane.id === PANE);
    if (window === undefined && isOpen) {
      await $.ui.close({ id: PANE });
      return { text: "ktx stats pane closed." };
    }
    if (window !== undefined) await selectWindow($, window);
    await $.ui.open({ id: PANE, title: "ktx", rows: 1 });
    await update($, paneOpen, () => true);
    if (window === undefined) void refresh($);
    return {
      text: `ktx stats pane opened (${await read($, selectedWindow)}m).`,
    };
  });

  on("ui.close", async ($, e, next) => {
    const closed = await next(e);
    if (e.id === PANE) await update($, paneOpen, () => false);
    return closed;
  });

  // The band yields to a survey and to the open pane, and draws nothing until
  // the first poll.
  on("ui.render", { component: "AbovePrompt" }, async ($, e, next) => {
    const snapshot = await read($, lastSnapshot);
    if (e.props.hasSurvey || snapshot === null || (await read($, paneOpen)))
      return next(e);
    const { Text } = $.ui.resolve(e);
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
