// ktx-stats: shows ktx compression figures in Claude Code.
//
// On session start it registers /ktx and polls `ktx stats summary` every
// 10 seconds through the ktx executable pinned when the plugin was generated.
// A band above the prompt shows the current window; /ktx opens a
// pane with the same figures and buttons for the 15, 30, and 60 minute
// windows. The figures cover every runtime ktx records, not only this session.

import { atom, read, update } from "claude-code";
import type { EngineInterface, Register, Timer } from "claude-code";

import type { KtxStatsSnapshot, KtxStatsWindow } from "../types/index.d.ts";
import {
  defaultKtxStatsWindow,
  errorMessage,
  ktxStatsWindows,
  paneLines,
  parseKtxSummaryRun,
  bandLine,
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

  on("command.run", { command: "ktx" }, async ($) => {
    await $.ui.open({ id: PANE, title: "ktx" });
    void refresh($);
    return { text: "ktx stats pane opened." };
  });

  // The band yields to a survey and draws nothing until the first poll.
  on("ui.render", { component: "AbovePrompt" }, async ($, e, next) => {
    const snapshot = await read($, lastSnapshot);
    if (e.props.hasSurvey || snapshot === null) return next(e);
    const { Text } = $.ui.resolve(e);
    const window = await read($, selectedWindow);
    return <Text dimColor>{bandLine(snapshot, window)}</Text>;
  });

  on("ui.render", { component: "Pane", requestId: PANE }, async ($, e) => {
    const { Box, Button, Text } = $.ui.resolve(e);
    const window = await read($, selectedWindow);
    const snapshot = await read($, lastSnapshot);
    const barWidth = Math.max(10, Math.min(40, e.props.bodyColumns - 2));

    return (
      <Box flexDirection="column">
        <Text bold>ktx · last {window}m · all runtimes</Text>
        {paneLines(snapshot, window, barWidth).map((line) =>
          line.tone === "error" ? (
            <Text color="error">{line.text}</Text>
          ) : (
            <Text dimColor={line.tone === "dim"}>{line.text}</Text>
          ),
        )}
        <Box flexDirection="row" gap={1}>
          {ktxStatsWindows.map((choice) => (
            <Button
              key={`window-${choice}`}
              label={`${choice}m`}
              variant={choice === window ? "primary" : "secondary"}
              onPress={() => void selectWindow($, choice)}
            />
          ))}
        </Box>
      </Box>
    );
  });
};
