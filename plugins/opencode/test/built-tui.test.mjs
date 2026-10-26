import { expect, test } from "bun:test";
import { readFile } from "node:fs/promises";
import { RGBA } from "@opentui/core";
import { createComponent, testRender } from "@opentui/solid";
import { createSignal } from "solid-js";
import plugin, { SessionPromptThroughput } from "../tui.js";

test("package entry is external-runtime-only and updates without host JSX compilation", async () => {
  const pkg = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
  expect(pkg.exports["./tui"].import).toBe("./tui.js");
  const sdk = "@opencode-ai/plugin";
  expect(pkg.peerDependencies).not.toHaveProperty(sdk);
  expect(pkg.peerDependenciesMeta ?? {}).not.toHaveProperty(sdk);
  expect(pkg.dependencies ?? {}).not.toHaveProperty(sdk);
  expect(pkg.devDependencies).toHaveProperty(sdk);
  expect(plugin.id).toBe("ktx");
  expect(typeof plugin.tui).toBe("function");
  expect(plugin).not.toHaveProperty("setup");
  const compiled = await readFile(new URL("../tui.js", import.meta.url), "utf8");
  expect(compiled).not.toContain("@opencode/plugin");
  expect(compiled).not.toContain("@opencode-ai/plugin");
  expect(compiled).toContain('from "@opentui/solid"');
  expect(compiled).toContain('from "solid-js"');

  const color = RGBA.fromInts(255, 255, 255, 255);
  const theme = { text: color, textMuted: color, primary: color, warning: color, error: color };
  const [summary, setSummary] = createSignal(null);
  const view = await testRender(() => createComponent(SessionPromptThroughput, {
    theme,
    get summary() { return summary(); },
  }), { width: 20, height: 1 });
  try {
    await view.renderOnce();
    expect(view.captureCharFrame().trim()).toBe("");
    setSummary({ window: { seconds: 900 }, tokens: { deliveredEstimated: 110_700 } });
    await view.renderOnce();
    expect(view.captureCharFrame().trim()).toBe("123 tokens/s");
    setSummary({ window: { seconds: 900 }, tokens: { deliveredEstimated: 221_400 } });
    await view.renderOnce();
    expect(view.captureCharFrame().trim()).toBe("246 tokens/s");
  } finally { view.renderer.destroy(); }
});
