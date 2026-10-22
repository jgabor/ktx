// Runs the ktx-stats mod's pure helpers (hooks/stats.ts) under Node with type
// stripping, against the embedded source that `ktx init claude` ships.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jgabor/ktx/internal/cli/claudecode"
	"github.com/jgabor/ktx/internal/testjs"
)

const statsDriver = `import * as s from "./hooks/stats.ts";

const attempt = (f) => {
  try {
    return { ok: f() };
  } catch (cause) {
    return { error: cause.message };
  }
};

const valid = (minutes) => {
  const to = Date.parse("2026-10-22T12:00:00Z");
  return {
    schema_version: 10,
    window: {
      seconds: minutes * 60,
      from: new Date(to - minutes * 60_000).toISOString(),
      to: new Date(to).toISOString(),
    },
    calls: { total_with_output: 300, with_compressed_artifact: 120 },
    tokens: { raw_estimated: 1_200_000, delivered_estimated: 650_000 },
    prompt_cache_token_hit_rate_pct: { state: "missing", value: null, contributing_model_steps: 0 },
  };
};
const change = (edit) => {
  const value = valid(15);
  edit(value);
  return attempt(() => s.validateKtxSummary(value, 15)).error ?? "accepted";
};
const idle = {
  ...valid(15),
  calls: { total_with_output: 0, with_compressed_artifact: 0 },
  tokens: { raw_estimated: 0, delivered_estimated: 0 },
};
const data = s.validateKtxSummary(valid(15), 15);
const out = {
  compact: [0, 999, 1000, 1234, 9949, 9950, 12345, 999499, 999500, 1200000, 650000, 2.5e9, 3e12, 5e15, -1500].map(
    s.formatCompactEstimate,
  ),
  saved: [s.savedPercentage(0, 0), s.savedPercentage(100, 150), s.savedPercentage(100, -50), Math.round(s.savedPercentage(1200000, 650000) * 100) / 100],
  savedText: [s.formatSavedPercent(0, 0), s.formatSavedPercent(1200000, 650000)],
  bars: [s.textBar(46, 10), s.textBar(150, 4), s.textBar(-5, 4), s.textBar(0, 0)],
  valid: data,
  valid60: attempt(() => s.validateKtxSummary(valid(60), 60)).ok?.windowSeconds,
  nanoseconds: attempt(() =>
    s.validateKtxSummary(
      { ...valid(15), window: { seconds: 900, from: "2026-10-22T11:45:00.563329946Z", to: "2026-10-22T12:00:00.563329946Z" } },
      15,
    ),
  ).ok?.from,
  invalid: {
    array: attempt(() => s.validateKtxSummary([], 15)).error,
    schema: change((v) => (v.schema_version = 9)),
    window: attempt(() => s.validateKtxSummary(valid(30), 15)).error,
    gap: change((v) => (v.window.from = "2026-10-22T11:44:00Z")),
    order: change((v) => {
      v.window.from = "2026-10-22T12:15:00Z";
    }),
    timestamp: change((v) => (v.window.to = "2026-10-22 12:00:00")),
    compressed: change((v) => (v.calls.with_compressed_artifact = 301)),
    negative: change((v) => (v.calls.total_with_output = -1)),
    fraction: change((v) => (v.tokens.raw_estimated = 1.5)),
    rawZero: change((v) => {
      v.tokens.raw_estimated = 0;
      v.tokens.delivered_estimated = 1;
    }),
    noCalls: change((v) => {
      v.calls.total_with_output = 0;
      v.calls.with_compressed_artifact = 0;
    }),
    uncompressed: change((v) => (v.calls.with_compressed_artifact = 0)),
    cacheState: change((v) => (v.prompt_cache_token_hit_rate_pct.state = "guess")),
    cacheValue: change((v) => {
      v.prompt_cache_token_hit_rate_pct = { state: "measured", value: 101, contributing_model_steps: 2 };
    }),
  },
  run: [
    attempt(() => s.parseKtxSummaryRun({ exitCode: 1, stdout: "", stderr: "database\n is locked" }, 15)).error,
    attempt(() => s.parseKtxSummaryRun({ exitCode: 0, stdout: "not json", stderr: "" }, 15)).error,
    attempt(() => s.parseKtxSummaryRun({ exitCode: 0, stdout: JSON.stringify(valid(30)), stderr: "" }, 15)).error,
    attempt(() => s.parseKtxSummaryRun({ exitCode: 0, stdout: JSON.stringify(valid(15)), stderr: "" }, 15)).ok?.totalCalls,
  ],
  band: [
    s.bandLine(null, 15),
    s.bandLine({ window: 15, data: null, error: "ktx stats summary failed (exit 1)" }, 15),
    s.bandLine({ window: 15, data: s.validateKtxSummary(idle, 15), error: null }, 15),
    s.bandLine({ window: 15, data, error: null }, 15),
    s.bandLine({ window: 15, data, error: "timed out" }, 15),
    s.bandLine({ window: 15, data, error: null }, 30),
  ],
  pane: [
    s.paneLines({ window: 15, data, error: "timed out" }, 15, 10),
    s.paneLines(null, 15, 10),
    s.paneLines({ window: 15, data: s.validateKtxSummary(idle, 15), error: null }, 15, 10),
  ],
  windows: s.ktxStatsWindows,
};
console.log(JSON.stringify(out));
`

func TestClaudeCodeStatsHelpersUnderNode(t *testing.T) {
	node := testjs.Runtime(t, "node", false)
	dir := t.TempDir()
	testjs.WriteModulePackage(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "driver.mjs"), []byte(statsDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hooks/stats.ts", "types/index.d.ts"} {
		data, err := claudecode.Files.ReadFile("plugins/ktx-stats/" + name)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(node, testjs.DisableExperimentalWarning, "driver.mjs")
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("node driver: %v\n%s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("node driver wrote to stderr: %s", stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("driver output: %v\n%s", err, stdout.String())
	}

	expect := func(key string, want any) {
		t.Helper()
		if !reflect.DeepEqual(got[key], want) {
			t.Errorf("%s = %#v\nwant %#v", key, got[key], want)
		}
	}
	expect("compact", []any{"0", "999", "1k", "1.2k", "9.9k", "10k", "12k", "999k", "1M", "1.2M", "650k", "2.5B", "3T", "5000T", "-1.5k"})
	expect("saved", []any{0.0, 0.0, 100.0, 45.83})
	expect("savedText", []any{"—", "46%"})
	expect("bars", []any{"█████░░░░░", "████", "░░░░", "░"})
	expect("valid", map[string]any{
		"windowSeconds": 900.0, "from": "2026-10-22T11:45:00.000Z", "to": "2026-10-22T12:00:00.000Z",
		"totalCalls": 300.0, "compressedCalls": 120.0, "rawTokens": 1200000.0, "deliveredTokens": 650000.0,
	})
	expect("valid60", 3600.0)
	expect("nanoseconds", "2026-10-22T11:45:00.563329946Z")
	expect("windows", []any{15.0, 30.0, 60.0})
	expect("invalid", map[string]any{
		"array":        "response must be an object",
		"schema":       "schema_version must be 10",
		"window":       "window.seconds must be equal to the requested 15m window",
		"gap":          "window must be an interval matching window.seconds",
		"order":        "window.to must be at or after window.from",
		"timestamp":    "window.to must be a valid RFC3339 timestamp string",
		"compressed":   "calls.with_compressed_artifact must be no greater than calls.total_with_output",
		"negative":     "calls.total_with_output must be a non-negative safe integer",
		"fraction":     "tokens.raw_estimated must be a non-negative safe integer",
		"rawZero":      "tokens.delivered_estimated must be zero when tokens.raw_estimated is zero",
		"noCalls":      "tokens must be zero totals when calls.total_with_output is zero",
		"uncompressed": "tokens.delivered_estimated must be equal to tokens.raw_estimated when no call has a compressed artifact",
		"cacheState":   `prompt_cache_token_hit_rate_pct.state must be "measured", "missing", "malformed", or "not_applicable"`,
		"cacheValue":   "prompt_cache_token_hit_rate_pct.value must be a percentage from 0 to 100",
	})
	expect("run", []any{
		"ktx stats summary failed (exit 1): database is locked",
		"ktx stats summary returned invalid JSON",
		"ktx returned invalid stats data: window.seconds must be equal to the requested 15m window",
		300.0,
	})
	expect("band", []any{
		"ktx 15m: loading stats…",
		"ktx 15m: unavailable (ktx stats summary failed (exit 1))",
		"ktx: no calls in 15m",
		"ktx 15m: 46% saved · 1.2M→650k tok · 120/300 calls",
		"ktx 15m (stale): 46% saved · 1.2M→650k tok · 120/300 calls",
		"ktx 30m: loading stats…",
	})
	line := func(text, tone string) any { return map[string]any{"text": text, "tone": tone} }
	expect("pane", []any{
		[]any{
			line("Stale: timed out", "error"),
			line("Saved      550k tokens (46%)", "normal"),
			line("█████░░░░░", "normal"),
			line("Tokens     1.2M → 650k", "dim"),
			line("Calls      120 compressed / 300 total", "dim"),
		},
		[]any{line("Loading ktx stats…", "dim")},
		[]any{line("No calls in 15m", "dim")},
	})
}
