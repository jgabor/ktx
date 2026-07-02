# D22 Phase 2 acceptance verification log

Generated: 2026-07-01
Plan: `.agentera/plan.yaml` — "D22 Phase 2 — deterministic compression stages + savings measurement"
Binary: built from working tree (`go build -o ktx ./cmd/ktx`)

Decision referenced: D22 (firm) + D14 (conservative-on-failure, extended with a fourth invariant)

Scope: six tasks — configurable timeout, anti-worsening gate, four deterministic built-in stages, A/B measurement in `--replay`, real-session end-to-end, documentation sync.

```
===========================================================================
BUILD
===========================================================================
$ go build ./... && go build -o ktx ./cmd/ktx
build exit: 0

$ go vet ./...
vet exit: 0

$ go test ./... -count=1 -race
ok  github.com/jgabor/ktx/internal/cli             2.218s
ok  github.com/jgabor/ktx/internal/cli/testutil    1.008s
ok  github.com/jgabor/ktx/internal/config          1.010s
ok  github.com/jgabor/ktx/internal/db              1.944s
ok  github.com/jgabor/ktx/internal/parser          1.008s
ok  github.com/jgabor/ktx/internal/pipeline        1.009s
ok  github.com/jgabor/ktx/internal/pipeline/stages  1.009s
race exit: 0 (no data races)

===========================================================================
REGRESSION (M1–M5 + Phase 1)
===========================================================================
All pre-existing tests pass unchanged in behavior. The anti-worsening gate
(Task 2) required four test-fixture adjustments because the plan's AC6
premise ("existing tests that produce smaller output are unaffected")
under-counted tests whose fake stages produced equal-or-larger output —
these are fixture artifacts, not intentional behavior, and each fix
preserves the original test intent:

  - TestRun_Invariant3_UnknownStageSkip: lengthened input ("input" →
    "the input value") so the fake "KNOWN" output (6 B) is genuinely
    smaller than input (15 B). Intent (unknown skipped, known runs,
    warning emitted) unchanged.
  - TestExternalStage_LastWriterWins: lengthened input so /bin/echo's
    output is smaller than input. Intent (external overrides builtin)
    unchanged.
  - TestExternalStage_CatIsIdentity: cat passthrough, so the gate now
    discards it (output==input, Ran empty). The "external stage runs"
    intent is covered by TestExternalStage_EchoWithArg (echo shrinks,
    so the gate lets it through). Updated assertion + cross-reference.
  - TestExternalStage_EmptyOutputGuard: added a downstream Go "shrink"
    stage so the empty-output guard's pass-through is observable past
    the gate (result < input → gate does not clear Ran). Intent (guard
    fires for external /bin/true) preserved.

Plugin-list tests updated 5→9 builtins (+4 Phase 2 stages); config-show
unaffected (reads cfg.Profiles, no hardcoded counts).

===========================================================================
TASK 1 — TIMEOUT AC (configurable pipeline timeout via ktx.toml)
===========================================================================
config.go: [pipeline.default] timeout parses as a Go duration string;
default 30s when omitted or no ktx.toml (config.DefaultPipelineTimeout);
non-positive/unparseable → *ParseError at load time. hookLogic reads
cfg.PipelineTimeout (compressPipeline signature unchanged).

Tests (internal/config/config_test.go, internal/cli/hook_test.go):
  - TestLoad_PipelineTimeout/timeout='10s'_parses_to_10s     PASS
  - TestLoad_PipelineTimeout/no_timeout_field_→_default_30s  PASS
  - TestLoad_PipelineTimeout/no_ktx.toml_→_default_30s       PASS
  - TestLoad_PipelineTimeout/garbage_timeout_→_*ParseError    PASS
  - TestLoad_PipelineTimeout/non-positive_timeout_→_*ParseError PASS
  - TestLoad_PipelineTimeout/duration_with_minutes_parses_(2m) PASS
  - TestHook_ConfigurableTimeout (5ms fires, not 30s)         PASS
  - TestHook_DefaultTimeoutWhenOmitted (both paths)           PASS

AC1 (10s fires) · AC2 (default 30s when omitted) · AC3 (garbage → ParseError)
· AC4 (no ktx.toml → 30s) · AC5 (regression) — all PASS.

===========================================================================
TASK 2 — ANTI-WORSENING GATE AC (fourth D14 invariant, runner-level)
===========================================================================
pipeline.go Runner.Run: after the stage loop, if len(input) > 0 AND
len(res.Out) >= len(input), discard the result (res.Out = input,
res.Ran = nil). Clears Ran so compressPipeline's transformed=false
(compressed_id=null, no row) — the mechanism Task 5 AC3 requires.
Runs only when input non-empty; equal length is "not better."

Tests (internal/pipeline/runner_test.go):
  - TestRun_AntiWorseningGate/pipeline_grows_output_→_gate_discards   PASS
  - TestRun_AntiWorseningGate/pipeline_shrinks_→_gate_does_NOT_fire    PASS
  - TestRun_AntiWorseningGate/equal_length_→_gate_fires               PASS
  - TestRun_AntiWorseningGate/empty_input_→_gate_does_NOT_fire        PASS
  - TestRun_AntiWorseningGate/multi-stage_net_shrink_→_no_fire        PASS

AC1 (grows → discard) · AC2 (shrinks → keep) · AC3 (equal → discard) ·
AC4 (empty input → no fire) · AC5 (per-stage 1–3 unchanged) · AC6
(regression, fixtures adjusted) — all PASS.

Design note (under-specified by the plan, resolved here): the plan said
only "res.Out = input." For compressed_id=null (Task 5 AC3), the gate
also clears res.Ran — a discarded result records no stages that ran, so
compressPipeline's existing transformed = (out != input) || (pipeJSON !=
"[]") yields false. compressPipeline's signature is unchanged (Runner-only
change, plan-compliant).

===========================================================================
TASK 3 — NEW STAGES AC (four built-in stages, stdlib-only)
===================================================================
stages/: keep-ends:N:M (head+tail+omit marker), grep:pattern,
grep-v:pattern (regex line filter; invalid regex → Builder error → D14
invariant-2 passthrough), token-budget:N (line-safe truncation,
(len+3)/4 estimate, snap to last newline). All registered via
RegisterBuiltins; BuiltinStageNames() now lists 9 stages.

Tests (internal/pipeline/stages/stages_test.go):
  - TestKeepEnds (50:20→71 lines, 60 lines unchanged, no-trailing-newline,
    malformed arg, empty input)                            PASS
  - TestGrep (keep matching, anchored, invalid regex error,
    registry bad-arg, empty pattern, no-matches→nil, empty) PASS
  - TestGrepV (reject matching, registry resolves, invalid regex,
    reject-all→nil)                                       PASS
  - TestTokenBudget (100→~400 chars + marker "from 500",
    under-budget unchanged, default 2000, malformed, empty,
    no-newline input)                                      PASS
  - TestRegisterBuiltins (all 9 resolve; Phase 2 names present;
    grep/grep-v bare → recognised-bad-arg)                PASS
  - TestPhase2Pipeline_EndToEnd (real chain shrinks verbose build log) PASS

Plugin/config AC:
  - internal/cli/plugin_test.go TestPluginList_NoUserPlugins   PASS
    (9 builtins, all builtin=true removable=false, incl. the four).
  - internal/cli/config_cli_test.go TestConfigShow_JSON_Phase2Stages PASS
    (stages appear in pipeline.profiles.default.stages).

AC1–10 (keep-ends 71 lines / 60 unchanged, grep keeps 3, grep invalid→
passthrough, grep-v rejects debug, token-budget ~400 chars+marker, under-
budget unchanged, plugin list shows all four, config show shows them,
table-driven tests, regression) — all PASS.

===========================================================================
TASK 4 — A/B MEASUREMENT AC (compression section in --replay)
===========================================================================
bench.go runBenchReplayImpl: re-parsed tool_use events run through
compressPipeline (timeout=0, off-line, zero agent calls); envelope gains
a "compression" section {baseline_bytes, compressed_bytes, ratio,
bytes_saved, pipeline}. measureReplayCompression reuses compressPipeline
(DRY); D15 gate counts is_error calls toward baseline only; the anti-
worsening gate guarantees compressed <= baseline (ratio <= 1.0).

Tests (internal/cli/bench_test.go):
  - TestRunBenchReplay_Compression (head:2 shrinks, ratio<1, saved>0,
    pipeline=[head:2])                                     PASS
  - TestRunBenchReplay_CompressionIdentity (no ktx.toml: ratio=1.0,
    saved=0, pipeline=[])                                  PASS
  - TestRunBenchReplay_CompressionD15Gate (is_error → ratio=1.0,
    compressed==baseline)                                  PASS
  - TestRunBenchReplay_CompressionEmptyStream (baseline=0, ratio=1.0)PASS
  - Pre-existing TestRunBenchReplay_Integration/NotFound         PASS
    (compression field additive; existing envelope shape unchanged).

AC1 (compression section present) · AC2 (passthrough ratio=1.0) · AC3
(timeout=0) · AC4 (D15 baseline-only) · AC5 (gate fires → compressed==
original) · AC6 (regression) — all PASS.

===========================================================================
TASK 5 — REAL-SESSION AC (end-to-end with deterministic pipeline)
===================================================================
Function-level (internal/cli/hook_test.go + bench_test.go) — exercises
the exact code path the OpenCode plugin's tool.execute.after calls
(hookLogic → DB → decompress). A live interactive OpenCode agent is not
available in this build environment, but hookLogic IS the plugin's entry
point, so the in-flight compression path is exercised identically.

  - TestHook_RealSessionEndToEnd/AC1: verbose build log (ANSI + 100
    debug lines + fatal-at-bottom) → agent sees compressed (fewer bytes;
    ANSI gone; debug gone; bottom error preserved); original in DB;
    GetSourceOutput(compressed.source_id) returns original verbatim. PASS
  - TestHook_RealSessionEndToEnd/AC3: short input → anti-worsening gate
    → output==original, compressed_id=null.                         PASS
  - TestHook_RealSessionEndToEnd/AC4: is_error=true → D15 gate →
    original unchanged, compressed_id=null, failure warning.         PASS
  - TestRunBenchReplay_RealWorldPipeline (AC2): replay with the real
    pipeline → ratio < 1.0, bytes_saved > 0.                         PASS

BINARY-LEVEL SMOKE TEST (built ktx; temp DB + ktx.toml with the real
pipeline strip-ansi+grep-v:^debug:+keep-ends:50:20+token-budget:2000;
verbose build-log tool_call piped to `ktx hook`; then `ktx decompress`):

  RESULT: decompress(compress(x)) == x              : True
  RESULT: compressed < original                     : True
  RESULT: original bytes                           : 3146
  RESULT: agent saw (compressed) bytes             : 838   (~73% reduction)
  RESULT: ANSI stripped from compressed             : True
  RESULT: debug stripped from compressed            : True
  RESULT: bottom error preserved (keep-ends tail)   : True
  compressed_id: fwezaqzzlzaucxpf (a compressed row was created)

AC1 (agent sees compressed; original in DB; decompress verbatim) · AC2
(replay ratio < 1.0) · AC3 (short input → gate, compressed_id=null) ·
AC4 (is_error → original unchanged) — all PASS.

===========================================================================
TASK 6 — DOCUMENTATION SYNC
===========================================================================
  - ARCHITECTURE.md: D14 invariants extended to four (anti-worsening
    gate); D22 timeout sentence updated (configurable, no longer
    "deferred to Phase 2"); D22 Phase 2 paragraph added (four stages,
    A/B surface, gate); D14 stage set mentions Phase 2 additions.
  - TODO.md: --replay A/B step-4 deferral closed (Phase 2 landed it);
    new [x] Phase 2 entry; no duplicate open items (content-type routing
    and aggressiveness tiers remain open as Phase 3 / YAGNI).
  - ROADMAP.md: M5 section gained a Phase 2 note (in-flight compression
    now meaningful + measurable).
  - plan.yaml: status: complete; critic_issues + surprises record the
    one fixture-adjustment deviation.

===========================================================================
VERIFICATION SUMMARY
===========================================================================
Phase 2 ships the "cheaper" half of ktx's stated goal: meaningful
deterministic compression (four stdlib-only stages, no ML, no new deps)
behind an anti-worsening gate that never makes output worse, with
configurable critical-path timeout and an off-line A/B measurement
surface. A real-world pipeline (strip-ansi + grep-v + keep-ends +
token-budget) shrinks a verbose build log ~73% (3146 → 838 bytes) while
preserving the bottom-of-log error, and the original is recoverable
verbatim via `ktx decompress`. With no ktx.toml, behavior is byte-
identical to Phase 1 (passthrough, ratio=1.0, compressed_id=null).

OVERALL: PASS
- Build: PASS (go build, go vet, go test -race all clean).
- Tasks 1–6 acceptance criteria: PASS.
- D22 firm design rules unchanged (single subprocess, envelope shape,
  compressed-row-only-when-transformed, two-layer conservative failure,
  sync compression, D10 noop). D14 invariants 1–3 unchanged per-stage;
  invariant 4 (gate) fires on the pipeline result.
- Constraints honored: AGENTS.md "boring before clever" (stdlib only),
  "router wins, algorithm loses" (stages are user-configured, no content
  routing), DRY (measureReplayCompression reuses compressPipeline), YAGNI
  (no content-type routing / aggressiveness tiers / per-profile timeouts
  — all Phase 3).
```
