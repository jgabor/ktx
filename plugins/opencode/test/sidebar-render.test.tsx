/** @jsxImportSource @opentui/solid */

import assert from "node:assert/strict";
import test from "node:test";
import { RGBA } from "@opentui/core";
import { testRender } from "@opentui/solid";
import {
  PromptCacheTokenHitRateSidebar,
  SessionPromptThroughput,
} from "../tui.tsx";
import { validateKtxSummary } from "../ktx-stats.ts";

const color = RGBA.fromInts(255, 255, 255, 255);
const theme = new Proxy(
  {
    text: color,
    textMuted: color,
    secondary: color,
    backgroundElement: color,
  },
  { get: (target, key: string) => target[key as keyof typeof target] ?? color },
);

test("renders post-ktx throughput in the session prompt", async () => {
  const summary = validateKtxSummary({
    schema_version: 10,
    window: {
      seconds: 900,
      from: "2026-07-21T13:42:06.532Z",
      to: "2026-07-21T13:57:06.532Z",
    },
    calls: { total_with_output: 1, with_compressed_artifact: 1 },
    tokens: { raw_estimated: 200_000, delivered_estimated: 110_700 },
    prompt_cache_token_hit_rate_pct: {
      state: "measured",
      value: 75,
      contributing_model_steps: 1,
    },
    agent_attribution: {
      authority: "trusted_ingress",
      identified_calls: 1,
      unknown_calls: 0,
      total_calls: 1,
      coverage_pct: 100,
    },
    latency: {
      state: "unavailable",
      unit: "milliseconds",
      authority: "runtime",
      measured_calls: 0,
      unknown_calls: 1,
      malformed_calls: 0,
      total_calls: 1,
      coverage_pct: 0,
      contributing_from: null,
      contributing_to: null,
      distribution_ms: {
        count: 0,
        min: null,
        p50: null,
        p95: null,
        max: null,
        average: null,
      },
    },
  });
  const setup = await testRender(
    () => <SessionPromptThroughput theme={theme as never} summary={summary} />,
    { width: 20, height: 1 },
  );

  try {
    await setup.renderOnce();
    assert.equal(setup.captureCharFrame().trim(), "123 tokens/s");
  } finally {
    setup.renderer.destroy();
  }
});

test("renders the current prompt-cache rate as one aligned row", async () => {
  const setup = await testRender(
    () => (
      <PromptCacheTokenHitRateSidebar
        theme={theme as never}
          metrics={{
            state: "measured",
            display: "75%",
        }}
      />
    ),
    { width: 32, height: 3 },
  );

  try {
      await setup.renderOnce();
      const row = setup.captureCharFrame().split("\n")[0];
    assert.equal(row, "Prompt cache                 75%");
  } finally {
    setup.renderer.destroy();
  }
});

test("preserves the cache value before the label at narrow widths", async () => {
  const setup = await testRender(
    () => (
      <PromptCacheTokenHitRateSidebar
        theme={theme as never}
          metrics={{
            state: "measured",
            display: "75%",
        }}
      />
    ),
    { width: 24, height: 3 },
  );

  try {
      await setup.renderOnce();
      const row = setup.captureCharFrame().split("\n")[0];
      assert.match(row, /75%$/);
  } finally {
    setup.renderer.destroy();
  }
});
