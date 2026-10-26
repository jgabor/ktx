/** @jsxImportSource @opentui/solid */
import { afterEach, expect, spyOn, test } from "bun:test";
import { RGBA } from "@opentui/core";
import { testRender } from "@opentui/solid";
import type { TuiPlugin } from "@opencode-ai/plugin/tui";
import * as stats from "../ktx-stats.ts";
// Exercise the published Solid transform, including reactive prop getters.
const { default: plugin } = await import(new URL("../tui.js", import.meta.url).href) as typeof import("../tui.tsx");

const color = RGBA.fromInts(255, 255, 255, 255);
const theme = {
  text: color, textMuted: color, primary: color, warning: color, error: color,
  selectedListItemText: color, backgroundElement: color, borderSubtle: color,
};
const originalCreate = stats.createKtxStatsStore;
let restore = () => {};
afterEach(() => restore());

// The version check runs before polling starts; let it settle.
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

function instrumentStore(version: string | Error = "dev") {
  let signal: AbortSignal | undefined;
  let started = 0;
  let stopped = 0;
  let unsubscribed = 0;
  let reads = 0;
  let resolveRead: (summary: stats.KtxSummary) => void = () => {};
  const store = originalCreate({
    read: (input) => {
      reads++;
      signal = input;
      return new Promise((resolve) => { resolveRead = resolve; });
    },
    readVersion: async () => {
      if (version instanceof Error) throw version;
      return version;
    },
    setInterval: () => { started++; return 1 as never; },
    clearInterval: () => { stopped++; },
  });
  const subscribe = store.subscribe;
  store.subscribe = (listener) => {
    const unsubscribe = subscribe(listener);
    return () => { unsubscribed++; unsubscribe(); };
  };
  const spy = spyOn(stats, "createKtxStatsStore").mockReturnValue(store);
  restore = () => { store.dispose(); spy.mockRestore(); };
  return {
    store,
    get reads() { return reads; },
    async provideSummary(tokensPerSecond = 123) {
      const seconds = store.getState().window * 60;
      resolveRead({
        schemaVersion: 10,
        window: { seconds, from: "2026-09-30T12:00:00Z", to: new Date(Date.UTC(2026, 8, 30, 12, 0, seconds)).toISOString() },
        calls: { totalWithOutput: 1, withCompressedArtifact: 1 },
        tokens: { rawEstimated: seconds * 300, deliveredEstimated: seconds * tokensPerSecond },
        promptCacheTokenHitRatePct: { state: "measured", value: 75, contributingModelSteps: 1 },
      });
      await store.refresh();
    },
    assertStarted() { expect(started).toBe(1); expect(signal?.aborted).toBe(false); },
    assertNotPolling() { expect(started).toBe(0); expect(reads).toBe(0); },
    assertDisposed(aborted = true) {
      expect(signal?.aborted).toBe(aborted);
      expect(stopped).toBe(1);
      expect(unsubscribed).toBe(1);
    },
  };
}

async function load(version?: string | Error) {
  const resources = instrumentStore(version);
  let cleanup = () => {};
  let registration: any;
  let cleared = 0;
  const host = { clearSelection: () => {} };
  const api = {
    theme: { current: theme }, renderer: { clearSelection() { cleared++; host.clearSelection(); } },
    lifecycle: { onDispose(fn: () => void) { cleanup = fn; } },
    slots: { register(value: unknown) { registration = value; return "slot-id"; } },
  } as unknown as Parameters<TuiPlugin>[0];
  await plugin.tui(api, undefined, {} as never);
  await settle();
  return {
    resources, registration, host,
    get cleared() { return cleared; },
    cleanup: () => cleanup(),
  };
}

test("keeps the sidebar slots and order and disposes polling and in-flight reads once", async () => {
  const loaded = await load();
  const { resources, registration, host, cleanup } = loaded;
  expect(plugin.id).toBe("ktx");
  expect(registration.order).toBe(600);
  expect(Object.keys(registration.slots)).toEqual(["sidebar_content", "session_prompt_right"]);
  resources.assertStarted();
  const view = await testRender(() => registration.slots.sidebar_content(), { width: 32, height: 8 });
  host.clearSelection = () => view.renderer.clearSelection();
  try {
    await view.renderOnce();
    expect(view.captureCharFrame()).toContain("ktx");
    expect(view.captureCharFrame()).toContain("last 15m");
    const row = view.captureCharFrame().split("\n").findIndex((line) => line.includes("last 15m"));
    await view.mockMouse.moveTo(27, row);
    await view.mockMouse.pressDown(27, row, undefined, { delayMs: 10 });
    expect(loaded.cleared).toBe(1);
    expect(resources.store.getState().window).toBe(15);
    await view.mockMouse.release(27, row, undefined, { delayMs: 10 });
    expect(resources.store.getState().window).toBe(30);
    await view.mockMouse.release(27, row);
    expect(resources.store.getState().window).toBe(30);
    await view.mockMouse.pressDown(27, row);
    await view.mockMouse.moveTo(0, 0);
    await view.mockMouse.release(0, 0);
    expect(resources.store.getState().window).toBe(30);
  } finally { view.renderer.destroy(); cleanup(); }
  cleanup();
  resources.assertDisposed();
});

test("shows statistics and throughput once the summary arrives", async () => {
  const { resources, registration, cleanup } = await load();
  await resources.provideSummary();
  const view = await testRender(() => (
    <box flexDirection="column">
      {registration.slots.sidebar_content()}
      <box flexDirection="row" width={32}>
        <text>Other plugin</text>
        {registration.slots.session_prompt_right()}
      </box>
    </box>
  ), { width: 32, height: 16 });
  try {
    await view.renderOnce();
    const frame = view.captureCharFrame();
    expect(frame).toContain("Compressed");
    expect(frame).toContain("Prompt cache");
    expect(frame).toMatch(/Other plugin\s*123 tokens\/s/);
  } finally { view.renderer.destroy(); cleanup(); }
});

test("an older ktx release shows one upgrade line and never polls", async () => {
  const { resources, registration, cleanup } = await load("v0.1.0");
  resources.assertNotPolling();
  const line = `ktx v0.1.0 is older than ${stats.minimumKtxVersion}; upgrade ktx`;
  expect(resources.store.getState().outdated).toBe(line);
  const view = await testRender(() => (
    <box flexDirection="column">
      {registration.slots.sidebar_content()}
      <box flexDirection="row" width={60}>
        {registration.slots.session_prompt_right()}
      </box>
    </box>
  ), { width: 60, height: 8 });
  try {
    await view.renderOnce();
    const frame = view.captureCharFrame();
    expect(frame).toContain("ktx");
    expect(frame.split(line).length - 1).toBe(1);
    expect(frame).not.toContain("last 15m");
    expect(frame).not.toContain("tokens/s");
    expect(frame).not.toContain("Loading");
    await resources.store.setWindow(30);
    await resources.store.refresh();
    resources.store.startPolling();
    resources.assertNotPolling();
  } finally { view.renderer.destroy(); cleanup(); }
});

for (const version of [stats.minimumKtxVersion, "v1.0.0", "dev", "v0.1.1-0.20261022200737-5c06df354e8c+dirty"]) {
  test(`ktx ${version} shows statistics`, async () => {
    const { resources, registration, cleanup } = await load(version);
    resources.assertStarted();
    expect(resources.store.getState().outdated).toBeNull();
    await resources.provideSummary();
    const view = await testRender(() => registration.slots.sidebar_content(), { width: 32, height: 12 });
    try {
      await view.renderOnce();
      expect(view.captureCharFrame()).toContain("Compressed");
      expect(view.captureCharFrame()).not.toContain("upgrade ktx");
    } finally { view.renderer.destroy(); cleanup(); }
  });
}

test("a failed version read still polls so the refresh reports the failure", async () => {
  const { resources, cleanup } = await load(new Error("ktx was not found on PATH."));
  resources.assertStarted();
  expect(resources.store.getState().outdated).toBeNull();
  cleanup();
});
