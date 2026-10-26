import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import test from "node:test";
import {
  createKtxStatsStore,
  deliveredTokensPerSecond,
  findOnPath,
  formatCompactEstimate,
  formatCompressionBar,
  formatDeliveredTokensPerSecond,
  formatPromptCacheTokenHitRateMetrics,
  formatReductionPercent,
  formatSidebarMetrics,
  minimumKtxVersion,
  nextKtxStatsWindow,
  outdatedLine,
  readKtxSummary,
  readKtxVersion,
  throughputTone,
  validateKtxSummary,
  type KtxSummary,
} from "../ktx-stats.ts";

function fixture(): Record<string, unknown> {
  return {
    schema_version: 10,
    window: {
      seconds: 1800,
      from: "2026-07-21T13:27:06.532Z",
      to: "2026-07-21T13:57:06.532Z",
    },
    calls: { total_with_output: 62, with_compressed_artifact: 42 },
    tokens: { raw_estimated: 37_440, delivered_estimated: 14_220 },
    prompt_cache_token_hit_rate_pct: {
      state: "measured",
      value: 75,
      contributing_model_steps: 6,
    },
    agent_attribution: {
      authority: "trusted_ingress",
      identified_calls: 62,
      unknown_calls: 0,
      total_calls: 62,
      coverage_pct: 100,
    },
    latency: {
      state: "unavailable",
      unit: "milliseconds",
      authority: "runtime",
      measured_calls: 0,
      unknown_calls: 62,
      malformed_calls: 0,
      total_calls: 62,
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
  };
}

function summary(): KtxSummary {
  return validateKtxSummary(fixture());
}

test("validates and maps the compact schema 10 summary", () => {
  assert.deepEqual(summary(), {
    schemaVersion: 10,
    window: {
      seconds: 1800,
      from: "2026-07-21T13:27:06.532Z",
      to: "2026-07-21T13:57:06.532Z",
    },
    calls: { totalWithOutput: 62, withCompressedArtifact: 42 },
    tokens: { rawEstimated: 37_440, deliveredEstimated: 14_220 },
    promptCacheTokenHitRatePct: {
      state: "measured",
      value: 75,
      contributingModelSteps: 6,
    },
  });
});

test("fails closed on unknown schemas, percentage corruption, and unavailable states", () => {
  for (const schemaVersion of [9, 11]) {
    const unknown = fixture();
    unknown.schema_version = schemaVersion;
    assert.throws(() => validateKtxSummary(unknown), /schema_version must be 10/);
  }

  for (const [state, contributingModelSteps] of [
    ["missing", 0],
    ["malformed", 1],
    ["not_applicable", 1],
  ] as const) {
    const unavailable = fixture();
    unavailable.prompt_cache_token_hit_rate_pct = {
      state,
      value: null,
      contributing_model_steps: contributingModelSteps,
    };
    assert.deepEqual(
      validateKtxSummary(unavailable).promptCacheTokenHitRatePct,
      { state, value: null, contributingModelSteps },
    );
  }

  const zero = fixture();
  zero.prompt_cache_token_hit_rate_pct = {
    state: "measured",
    value: 0,
    contributing_model_steps: 1,
  };
  assert.deepEqual(validateKtxSummary(zero).promptCacheTokenHitRatePct, {
    state: "measured",
    value: 0,
    contributingModelSteps: 1,
  });

  const unavailableWithValue = fixture();
  unavailableWithValue.prompt_cache_token_hit_rate_pct = {
    state: "missing",
    value: 0,
    contributing_model_steps: 1,
  };
  assert.throws(
    () => validateKtxSummary(unavailableWithValue),
    /prompt_cache_token_hit_rate_pct.value must be null when state is missing/,
  );

  const emptyNotApplicable = fixture();
  emptyNotApplicable.prompt_cache_token_hit_rate_pct = {
    state: "not_applicable",
    value: null,
    contributing_model_steps: 0,
  };
  assert.throws(
    () => validateKtxSummary(emptyNotApplicable),
    /prompt_cache_token_hit_rate_pct.state must be missing when contributing_model_steps is zero/,
  );

  const over100 = fixture();
  over100.prompt_cache_token_hit_rate_pct = {
    state: "measured",
    value: 100.1,
    contributing_model_steps: 1,
  };
  assert.throws(
    () => validateKtxSummary(over100),
    /prompt_cache_token_hit_rate_pct.value must be a percentage from 0 to 100/,
  );
});

test("validates compact token and window invariants", () => {
  const malformed = fixture();
  (malformed.tokens as Record<string, unknown>).raw_estimated = "37440";
  assert.throws(
    () => validateKtxSummary(malformed),
    /tokens.raw_estimated must be a finite number/,
  );

  const noCalls = fixture();
  noCalls.calls = { total_with_output: 0, with_compressed_artifact: 0 };
  assert.throws(
    () => validateKtxSummary(noCalls),
    /tokens must be zero totals when calls.total_with_output is zero/,
  );

  const interval = fixture();
  (interval.window as Record<string, unknown>).from =
    "2026-07-21T13:28:06.532Z";
  assert.throws(
    () => validateKtxSummary(interval),
    /window must be an interval matching window.seconds/,
  );
});

test("derives compact sidebar values locally", () => {
  const parsed = summary();
  assert.deepEqual(formatSidebarMetrics(parsed), {
    savedTokens: "23k",
    savedPercentage: "62%",
    rawTokens: "37k",
    deliveredTokens: "14k",
    totalCalls: "62",
    compressedCalls: "42",
    compressionBar: {
      savedPercentage: ((37_440 - 14_220) / 37_440) * 100,
    },
  });
  assert.deepEqual(
    formatPromptCacheTokenHitRateMetrics(parsed.promptCacheTokenHitRatePct),
    {
      state: "measured",
      display: "75%",
    },
  );
  assert.deepEqual(
    formatPromptCacheTokenHitRateMetrics({
      state: "measured",
      value: 0,
      contributingModelSteps: 1,
    }),
    {
      state: "measured",
      display: "0%",
    },
  );
  assert.deepEqual(
    formatPromptCacheTokenHitRateMetrics({
      state: "missing",
      value: null,
      contributingModelSteps: 84,
    }),
    {
      state: "missing",
      display: "—",
    },
  );
  assert.equal(formatReductionPercent(null), "—");
  assert.equal(formatCompactEstimate(999.96), "1k");
  assert.equal(formatDeliveredTokensPerSecond(null), "");
  assert.equal(formatDeliveredTokensPerSecond(parsed), "8 tokens/s");
  assert.equal(
    formatDeliveredTokensPerSecond({
      ...parsed,
      tokens: { ...parsed.tokens, deliveredEstimated: 221_400 },
    }),
    "123 tokens/s",
  );
  assert.deepEqual(formatCompressionBar(100, 50), {
    savedPercentage: 50,
  });
});

test("rounds throughput and assigns theme tones at each boundary", () => {
  assert.equal(deliveredTokensPerSecond(summary()), 8);
  assert.equal(deliveredTokensPerSecond(null), null);
  assert.equal(throughputTone(99), "muted");
  assert.equal(throughputTone(100), "text");
  assert.equal(throughputTone(199), "text");
  assert.equal(throughputTone(200), "primary");
  assert.equal(throughputTone(299), "primary");
  assert.equal(throughputTone(300), "warning");
  assert.equal(throughputTone(399), "warning");
  assert.equal(throughputTone(400), "error");
});

test("cycles supported ktx stats windows in order", () => {
  assert.equal(nextKtxStatsWindow(15), 30);
  assert.equal(nextKtxStatsWindow(30), 60);
  assert.equal(nextKtxStatsWindow(60), 15);
});

test("coalesces refreshes and retains valid data when a later refresh fails", async () => {
  let reads = 0;
  let resolveFirst: ((value: KtxSummary) => void) | undefined;
  const store = createKtxStatsStore({
    read: () => {
      reads += 1;
      if (reads === 1)
        return new Promise<KtxSummary>((resolve) => {
          resolveFirst = resolve;
        });
      return Promise.reject(new Error("ktx is temporarily unavailable"));
    },
  });

  const first = store.refresh();
  const second = store.refresh();
  assert.equal(first, second);
  resolveFirst?.(summary());
  await first;
  await store.refresh();
  assert.deepEqual(store.getState().data, summary());
  assert.equal(store.getState().error, "ktx is temporarily unavailable");
});

test("reads stats directly through the selected executable", async () => {
  const directory = await mkdtemp(join(tmpdir(), "opencode-ktx-test-"));
  const executable = join(directory, "ktx");
  const summaryArgs = [
    "stats",
    "summary",
    "--window",
    "30m",
    "--format",
    "json",
  ];
  const script = `#!/usr/bin/env node
const args = process.argv.slice(2);
if (JSON.stringify(args) === ${JSON.stringify(JSON.stringify(summaryArgs))}) {
  process.stdout.write(${JSON.stringify(JSON.stringify(fixture()))});
} else {
  process.stderr.write("unexpected arguments: " + JSON.stringify(args));
  process.exitCode = 2;
}
`;

  try {
    await writeFile(executable, script, { mode: 0o755 });
    assert.deepEqual(
      await readKtxSummary({ executable, window: 30 }),
      summary(),
    );
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("reports a missing executable with an actionable command error", async () => {
  await assert.rejects(
    readKtxSummary({ executable: "/definitely/not/ktx" }),
    /ktx was not found on PATH/,
  );
});

test("a PATH search skips empty entries and folders named ktx", async () => {
  const root = await mkdtemp(join(tmpdir(), "opencode-ktx-test-"));
  const originalCwd = process.cwd();
  try {
    // An empty PATH entry would mean the working folder; its ktx must not win.
    await writeFile(join(root, "ktx"), "#!/bin/sh\n", { mode: 0o755 });
    const folder = join(root, "folder");
    await mkdir(join(folder, "ktx"), { recursive: true });
    const bin = join(root, "bin");
    await mkdir(bin);
    await writeFile(join(bin, "ktx"), "#!/bin/sh\n", { mode: 0o755 });
    process.chdir(root);
    assert.equal(await findOnPath("ktx", `${delimiter}${folder}${delimiter}${bin}`), await realpath(join(bin, "ktx")));
    await assert.rejects(findOnPath("ktx", `${delimiter}${folder}`), /ktx was not found on PATH/);
  } finally {
    process.chdir(originalCwd);
    await rm(root, { recursive: true, force: true });
  }
});

// The next test finds ktx on PATH only if this failed lookup was not kept.
test("a failed PATH lookup is not kept", async () => {
  const empty = await mkdtemp(join(tmpdir(), "opencode-ktx-test-"));
  const originalPath = process.env.PATH;
  try {
    process.env.PATH = empty;
    await assert.rejects(readKtxSummary(), /ktx was not found on PATH/);
  } finally {
    if (originalPath === undefined) delete process.env.PATH;
    else process.env.PATH = originalPath;
    await rm(empty, { recursive: true, force: true });
  }
});

test("pins the canonical executable resolved from PATH", async () => {
  const directory = await mkdtemp(join(tmpdir(), "opencode-ktx-test-"));
  const original = join(directory, "ktx-original");
  const replacement = join(directory, "ktx-replacement");
  const link = join(directory, "ktx");
  const invocations = join(directory, "invocations");
  const originalPath = process.env.PATH;
  const defaultWindowFixture = fixture();
  defaultWindowFixture.window = {
    seconds: 900,
    from: "2026-07-21T13:42:06.532Z",
    to: "2026-07-21T13:57:06.532Z",
  };
  const expected = validateKtxSummary(defaultWindowFixture, 15);
  const summaryArgs = [
    "stats",
    "summary",
    "--window",
    "15m",
    "--format",
    "json",
  ];
  const fakeKtx = (marker: string) => `#!/usr/bin/env node
const args = process.argv.slice(2);
if (JSON.stringify(args) === ${JSON.stringify(JSON.stringify(summaryArgs))}) {
  require("node:fs").appendFileSync(${JSON.stringify(invocations)}, ${JSON.stringify(`${marker}\n`)});
  process.stdout.write(${JSON.stringify(JSON.stringify(defaultWindowFixture))});
} else {
  process.stderr.write("unexpected arguments: " + JSON.stringify(args));
  process.exitCode = 2;
}
`;

  try {
    await writeFile(original, fakeKtx("original"), { mode: 0o755 });
    await writeFile(replacement, fakeKtx("replacement"), { mode: 0o755 });
    await symlink(original, link);
    process.env.PATH = `${directory}${originalPath ? delimiter + originalPath : ""}`;

    assert.deepEqual(await readKtxSummary(), expected);
    await rm(link);
    await symlink(replacement, link);
    assert.deepEqual(await readKtxSummary(), expected);
    assert.equal(await readFile(invocations, "utf8"), "original\noriginal\n");
  } finally {
    if (originalPath === undefined) delete process.env.PATH;
    else process.env.PATH = originalPath;
    await rm(directory, { recursive: true, force: true });
  }
});

test("turns statistics off only for a release below the minimum", () => {
  assert.equal(minimumKtxVersion, "v0.1.1");
  for (const found of ["v0.1.0", "v0.0.9", "v0.1.0\n"])
    assert.equal(
      outdatedLine(found),
      `ktx ${found.trim()} is older than v0.1.1; upgrade ktx`,
    );
  for (const found of [
    "v0.1.1",
    "v0.1.2",
    "v0.2.0",
    "v1.0.0",
    "dev",
    "v0.1.1-0.20261022200737-5c06df354e8c+dirty",
    "v0.0.0-20261022200737-5c06df354e8c",
    "v0.1.0-rc.1",
    "",
  ])
    assert.equal(outdatedLine(found), null, found);
});

test("reads the ktx version and reports a failing version command", async () => {
  const directory = await mkdtemp(join(tmpdir(), "opencode-ktx-test-"));
  const good = join(directory, "ktx-good");
  const bad = join(directory, "ktx-bad");
  try {
    await writeFile(
      good,
      `#!/usr/bin/env node
if (JSON.stringify(process.argv.slice(2)) === '["--version"]') process.stdout.write("v0.1.0\\n");
else process.exitCode = 2;
`,
      { mode: 0o755 },
    );
    await writeFile(
      bad,
      `#!/usr/bin/env node
process.stderr.write("unknown flag");
process.exitCode = 2;
`,
      { mode: 0o755 },
    );
    assert.equal(await readKtxVersion({ executable: good }), "v0.1.0");
    await assert.rejects(
      readKtxVersion({ executable: bad }),
      /ktx --version failed \(exit 2\): unknown flag/,
    );
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

function startedStore(readVersion: () => Promise<string>) {
  const calls = { reads: 0, intervals: 0 };
  const store = createKtxStatsStore({
    read: async () => {
      calls.reads += 1;
      return summary();
    },
    readVersion,
    window: 30,
    setInterval: () => {
      calls.intervals += 1;
      return 1 as never;
    },
    clearInterval: () => {},
  });
  return { store, calls };
}

test("start keeps statistics off when ktx is an older release", async () => {
  const { store, calls } = startedStore(async () => "v0.1.0");
  await store.start();
  assert.equal(
    store.getState().outdated,
    "ktx v0.1.0 is older than v0.1.1; upgrade ktx",
  );
  assert.equal(store.getState().loading, false);
  await store.refresh();
  await store.setWindow(60);
  store.startPolling();
  assert.deepEqual(calls, { reads: 0, intervals: 0 });
  assert.equal(store.getState().window, 30);
  store.dispose();
});

test("start polls when the version passes or cannot be read", async () => {
  for (const readVersion of [
    async () => "v0.1.1",
    async () => "dev",
    async () => {
      throw new Error("ktx was not found on PATH.");
    },
  ]) {
    const { store, calls } = startedStore(readVersion);
    await store.start();
    await store.refresh();
    assert.equal(store.getState().outdated, null);
    assert.deepEqual(store.getState().data, summary());
    assert.deepEqual(calls, { reads: 1, intervals: 1 });
    store.dispose();
  }
});

test("a window change during the version check cannot bring statistics back", async () => {
  let resolveVersion: (version: string) => void = () => {};
  const { store, calls } = startedStore(
    () => new Promise<string>((resolve) => {
      resolveVersion = resolve;
    }),
  );
  const started = store.start();
  await store.setWindow(60);
  await store.refresh();
  resolveVersion("v0.1.0");
  await started;
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(
    store.getState().outdated,
    "ktx v0.1.0 is older than v0.1.1; upgrade ktx",
  );
  assert.equal(store.getState().data, null);
  assert.deepEqual(calls, { reads: 0, intervals: 0 });
  store.dispose();
});

test("a window change during a passing version check refreshes that window once", async () => {
  let resolveVersion: (version: string) => void = () => {};
  const { store, calls } = startedStore(
    () => new Promise<string>((resolve) => {
      resolveVersion = resolve;
    }),
  );
  const started = store.start();
  await store.setWindow(60);
  resolveVersion("v0.1.1");
  await started;
  await store.refresh();
  assert.equal(store.getState().outdated, null);
  assert.equal(store.getState().window, 60);
  assert.deepEqual(calls, { reads: 1, intervals: 1 });
  store.dispose();
});
