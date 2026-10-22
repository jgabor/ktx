import type { On } from "claude-code";
import { expect, mock, test } from "claude-code/testing";
import type { TestBody } from "claude-code/testing";

const PANE = {
  plugin: "ktx-stats",
  component: "Pane",
  requestId: "ktx-stats",
  props: {
    title: "ktx",
    isFocused: false,
    bodyColumns: 40,
    placement: "dock",
    scroll: { offset: 0, bodyRows: 20 },
    view: {},
  },
} as const;

const BAND = {
  plugin: "ktx-stats",
  component: "AbovePrompt",
  props: {
    hasSurvey: false,
    isWorking: false,
    maxRows: 10,
    bodyColumns: 80,
    scroll: { offset: 0, bodyRows: 10 },
    view: {},
  },
} as const;

// ktx writes window bounds with nanosecond fractions.
function summary(minutes: number): string {
  const to = Date.parse("2026-10-22T12:00:00Z");
  const stamp = (ms: number) =>
    new Date(ms).toISOString().replace(".000Z", ".563329946Z");
  return JSON.stringify({
    schema_version: 10,
    window: {
      seconds: minutes * 60,
      from: stamp(to - minutes * 60_000),
      to: stamp(to),
    },
    calls: { total_with_output: 300, with_compressed_artifact: 120 },
    tokens: { raw_estimated: 1_200_000, delivered_estimated: 650_000 },
    prompt_cache_token_hit_rate_pct: {
      state: "missing",
      value: null,
      contributing_model_steps: 0,
    },
  });
}

type Engine = {
  argv: (readonly string[])[];
  failing: boolean;
};

// Hooks beneath the plugin stand for the engine: they answer the calls the
// mod makes on `$` and record what it ran and showed.
function engine(on: On, refuseCommand = false): Engine {
  const state: Engine = { argv: [], failing: false };
  on("command.register", () =>
    refuseCommand ? { deny: "taken" } : { value: { command: "ktx" } },
  );
  on("ui.open", () => ({ value: { isPlaced: true } }));
  on("session.start", (_, e) => ({ cwd: e.cwd }));
  on("process.run", (_, e) => {
    state.argv.push(e.argv);
    const window = Number(String(e.argv[4]).replace("m", ""));
    return {
      value: state.failing
        ? {
            exitCode: 1,
            stdout: "",
            stderr: "database is locked",
            isStdoutTruncated: false,
            isStderrTruncated: false,
          }
        : {
            exitCode: 0,
            stdout: summary(window),
            stderr: "",
            isStdoutTruncated: false,
            isStderrTruncated: false,
          },
    };
  });
  return state;
}

// The text the band above the prompt draws, or undefined when it passes.
async function band($: Parameters<TestBody>[0]): Promise<string | undefined> {
  const ui = await $.ui.mount({ ...BAND, surface: "terminal" });
  const text = (await ui.find({ type: "Text", text: /^ktx/ }))?.text;
  await ui.unmount();
  return text;
}

test("the poll shows the band and the pane", async ($, on) => {
  const clock = mock.clock(on);
  const host = engine(on);

  await $.session.start({ cwd: "/", surface: "terminal", isInteractive: true });
  await clock.settle();

  expect(host.argv[0]?.slice(1)).toEqual([
    "stats",
    "summary",
    "--window",
    "15m",
    "--format",
    "json",
  ]);
  expect(await band($)).toBe(
    "ktx 15m: 46% saved · 1.2M→650k tok · 120/300 calls",
  );

  for (const surface of ["terminal", "desktop"] as const) {
    const ui = await $.ui.mount({ ...PANE, surface });
    expect((await ui.find({ type: "Text", text: /^Saved/ }))?.text).toBe(
      "Saved      550k tokens (46%)",
    );
    expect((await ui.find({ type: "Text", text: /all runtimes/ }))?.text).toBe(
      "ktx · last 15m · all runtimes",
    );
    await ui.unmount();
  }
});

test("a failed poll keeps the last good data as stale", async ($, on) => {
  const clock = mock.clock(on);
  const host = engine(on);

  await $.session.start({ cwd: "/", surface: "terminal", isInteractive: true });
  await clock.settle();
  host.failing = true;
  await clock.advance(10_000);

  expect(host.argv.length).toBe(2);
  expect(await band($)).toBe(
    "ktx 15m (stale): 46% saved · 1.2M→650k tok · 120/300 calls",
  );
  const ui = await $.ui.mount({ ...PANE, surface: "terminal" });
  expect((await ui.find({ type: "Text", text: /^Stale/ }))?.text).toContain(
    "database is locked",
  );
  await ui.unmount();

  host.failing = false;
  await clock.advance(10_000);
  expect(host.argv.length).toBe(3);
  expect(await band($)).toBe(
    "ktx 15m: 46% saved · 1.2M→650k tok · 120/300 calls",
  );
});

test("a window button switches the window", async ($, on) => {
  const clock = mock.clock(on);
  const host = engine(on);

  await $.session.start({ cwd: "/", surface: "terminal", isInteractive: true });
  await clock.settle();

  const ui = await $.ui.mount({ ...PANE, surface: "terminal" });
  await ui.press({ key: "window-30" });
  await clock.settle();

  expect(host.argv.at(-1)?.[4]).toBe("30m");
  expect(await band($)).toBe(
    "ktx 30m: 46% saved · 1.2M→650k tok · 120/300 calls",
  );
  expect((await ui.find({ type: "Text", text: /all runtimes/ }))?.text).toBe(
    "ktx · last 30m · all runtimes",
  );
  await ui.unmount();
});

test("a refused /ktx registration keeps the band", async ($, on) => {
  const clock = mock.clock(on);
  const host = engine(on, true);

  await $.session.start({ cwd: "/", surface: "terminal", isInteractive: true });
  await clock.settle();

  expect(host.argv.length).toBe(1);
  expect(await band($)).toBe(
    "ktx 15m: 46% saved · 1.2M→650k tok · 120/300 calls",
  );
});

test("a session without a person does not poll", async ($, on) => {
  const clock = mock.clock(on);
  const host = engine(on);

  await $.session.start({ cwd: "/", surface: null, isInteractive: false });
  await clock.advance(30_000);

  expect(host.argv.length).toBe(0);
});
