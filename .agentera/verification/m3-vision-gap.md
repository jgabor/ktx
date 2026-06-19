# M3 vision-gap analysis

Generated: 2026-06-19
Milestone: M3 — Cost-per-correct benchmark
Source: post-M2 acceptance (main at `cb2859a`, M2 plan `.agentera/archive/PLAN-2026-06-19-m2-cost-publication.yaml`, M2 verification `.agentera/verification/m2-acceptance.md`).
Purpose: Feed planera. Defines what M3 must close, what decisions are prerequisites, and what is out of scope.

---

## 1. M3 acceptance (verbatim from `ROADMAP.md`)

> ## M3 — Cost-per-correct benchmark
>
> A cost-per-correct-answer number is published for at least one agent on at least one task.
>
> Done when: the number is reproducible from the published methodology.

Two halves, both required:
- **"Published"** — a concrete, citeable number for a concrete (agent, model, task) triple.
- **"Reproducible from the published methodology"** — anyone following the published method on the published inputs must get the same number.

---

## 2. Current state (M2 lineage)

What M2 left us with that is reusable for M3:
- **M2 columns on `tool_calls`**: `tokens_in`, `tokens_out`, `cost`, `latency_ms`, `model`, `provider` (nullable; absent vs zero round-trip via pointer-typed `store.ToolCall`).
- **`ktx read` / `ktx get` / `ktx list`**: cost is reconstructable from `tokens_in + tokens_out + model` against a known dollar model. The data model is sufficient; the dollar model is not yet wired in (D11 implementation gap).
- **`ktx list` per-session rollup**: `TOKENS` (sum of `tokens_in+tokens_out` over non-NULL rows), `COST` (sum of non-NULL `cost`), `MODEL` (MIN over non-NULL `model`). Reusable aggregation primitive.
- **Sidecar path is the canonical M2 source** for tokens/cost/model: the wrapper path leaves these NULL for OpenCode `tool_use` events (OpenCode 1.17.8 emits them on `step_finish`, not `tool_use`; documented in `.agentera/verification/m2-acceptance.md` "Surprises" §3 and `plan.surprises`).
- **`ktx doctor` m2_columns check**: schema presence verified; doctor reports `ok` once the M2 binary has opened the DB.
- **D11 recorded**: provider-reported tokens, `https://models.dev` dollar model, local cache at `$XDG_DATA_HOME/ktx/prices.json`, reconstructability contract, auto-populate policy. The *decision* is firm; the *implementation* is not on disk.

What is stubbed or absent for M3:
- No `ktx bench` subcommand. The D2 command surface (`bench run/report/compare`) is decided but unimplemented.
- No price cache file, no HTTP fetch, no auto-derive of cost. `ktx get` and `ktx list` show stored values verbatim (D11 contract — no derivation at read time).
- No "correctness" dimension. There is no `ground_truth`, no scoring, no task definition in the database. The schema is pure observation; M3 is the first time ktx needs to know whether a call did the right thing.
- No published methodology. D12 is an open deliberation in `ARCHITECTURE.md` ("Benchmark methodology v1: agent, model, task, ground truth, scoring, and published artifact shape") and a Normal-severity TODO item.
- No M2 surface that distinguishes "cost per call" from "cost per correct call." `ktx list` rolls up cost; it does not divide it by correctness.

---

## 3. Gap analysis (per M3 acceptance bullet)

### Bullet 1: "A cost-per-correct-answer number is published for at least one agent on at least one task."

What is missing:
- **A correctness signal.** The database has no column or table that records whether a call's output was correct. M3 must add one (or an external artifact) that pairs a `tool_call` (or session) with a ground-truth judgment.
- **A task definition.** A "task" needs an identifier, a prompt, and a ground-truth. Currently nothing in the schema names a task.
- **A scoring function.** How does a stored `output` get classified as correct? Deterministic match? LLM-as-judge? Test-suite pass? Each has different cost/fidelity tradeoffs.
- **A bench command that publishes the number.** `ktx bench run` is the producer (consumes a methodology, runs N trials on a task with a chosen agent+model, writes evidence). `ktx bench report` is the surface (the headline number). `ktx bench compare` is a future operation against baselines; not required for the M3 acceptance.
- **A concrete first run.** M3's acceptance demands a number for at least one (agent, model, task). The methodology must be runnable end-to-end on the developer's machine.

Depends on:
- **D12 (benchmark methodology)**: must specify which (agent, model, task, scoring) the headline number covers, and how the methodology is published (artifact shape, version, reproducibility inputs).
- **D11 implementation**: cost must be computable from stored data without the user supplying it manually. The D11 dollar model (models.dev) and the reconstructability contract are enough; the implementation (fetch + cache + derive) is what makes `bench run` not require the user to pre-fill `cost` on every `ktx write`.

Minimum surface to close:
1. A `bench_runs` (or similarly named) table that records one benchmark run: id, task id, agent, model, cost_total, correct_count, total_count, timestamp, methodology_version (or methodology reference).
2. A `bench` subcommand with `bench run` (executes the task N times through `ktx run`, captures the result, scores it, writes the run row, prints the headline) and `bench report` (reads the headline from a chosen run id, JSON default with `--format text`).
3. The cost half of the headline: D11 implementation so `bench run` can sum `cost` across the underlying tool_calls without the user pre-pricing.
4. The correctness half: a scoring function and a way to attach a ground-truth to a task. M3 v1 can ship a single deterministic scoring function (e.g., exact-match against a known good output) to satisfy the headline; richer scorers are a post-M3 resonera.

### Bullet 2: "Done when: the number is reproducible from the published methodology."

What is missing:
- A *written* methodology that lists, for a given published number, every input that produced it: the agent binary and version, the model ID, the task prompt and expected output, the scoring function, the trial count, the database path or commit of evidence, and the ktx binary version.
- A `bench report` output that surfaces the methodology reference alongside the number. The methodology itself does not have to live in the database; it can be a sibling file (`methodology/v1.yaml` or `docs/bench/v1.md`) referenced by ID from the run row.
- A reproducibility test in the M3 verification: two independent runs of `bench run` on the same inputs produce the same headline (modulo non-determinism the methodology names and acknowledges).

Depends on:
- **D12**: same as above. The methodology is the artifact; D12 is the decision that produces it.
- A reproducibility contract the M3 verification can enforce. The M2 verification proved reconstructability from stored data; M3 must prove the headline can be regenerated by anyone following the methodology.

Minimum surface to close:
1. A `methodology` field on the bench run row (text or path) that names the methodology version that produced it.
2. A published methodology file (e.g., `.agentera/bench/v1.yaml` or `docs/bench/methodology-v1.md`) that the run row references and that a third party can read and follow.
3. A verification step in `.agentera/verification/m3-acceptance.md` that re-runs `bench run` and confirms the headline is the same as the published one within the non-determinism the methodology acknowledges (likely zero — ktx is local and the agent is deterministic given the same prompt + model + tool_calls, assuming the sidecar supplies M2 fields).

---

## 4. Prerequisite decisions

### 4.1 D12 (benchmark methodology) — REQUIRED before any M3 code

Level of detail needed: enough that `bench run` can be implemented mechanically from the decision, with no further deliberation. Specifically:
- **Task shape**: how a task is named, where its prompt and ground-truth live (a file? a directory? a URL?). One task is enough for M3 acceptance; the decision should commit to that minimum.
- **Scoring v1**: a single deterministic scoring function. Exact match against a known good output is the boring default; an LLM-judge is the flexible default. D12 must pick one for v1 and acknowledge the other is a post-M3 resonera.
- **Trial count**: how many runs per (agent, model, task). One is technically enough to publish a number; N>1 lets `bench compare` exist later. D12 should commit to N≥3 or document the rationale for N=1.
- **Methodology versioning**: how methodology changes are named and referenced from the run row.
- **Published artifact shape**: what `bench report --format text` and `bench report` (JSON) emit. This is a CLI surface question; should land in the same D12 because it is the contract by which the number is "reproducible from the published methodology."

D12 is a single resonera session. The decision's confidence should be `firm` on the v1 surface; post-M3 deliberations can extend (multi-task, multi-model comparison, statistical significance).

### 4.2 D11 implementation — REQUIRED before M3 ships, can land in parallel with D12

D11 is already recorded. What is missing is *implementation*: HTTP fetch from `https://models.dev`, cache file at `$XDG_DATA_HOME/ktx/prices.json`, and a derivation step that fills `cost` from `tokens_in + tokens_out + model` when the caller did not supply it.

Whether this lands before or alongside `bench run`:
- **Before `bench run`**: cleaner. The bench command reads persisted `cost`; the M2 surface already supports `cost` round-trip; the bench run simply needs the D11 implementation to be able to backfill cost for runs where the runtime did not supply it (e.g., OpenCode tool_use rows where M2 fields are NULL).
- **Alongside `bench run`**: possible but adds a coordination surface. `bench run` would need to either (a) run after D11 implementation has touched the database, or (b) call the D11 derivation inline as it scores.

Recommended: implement D11 first, then `bench run`. D11 implementation is the smaller of the two work items; landing it first unblocks `bench run` from a "where does cost come from" question and gives the verification log a clean "cost is on the row" assertion to make.

D11 also defers:
- **Cache TTL and refresh policy** (D11 §"Dollar model"). M3 needs a sensible default (e.g., fetch on first use, never auto-refresh within a run, manual `ktx prices update` to force refresh). This can be a small resonera *inside* M3 or a documented default with a post-M3 follow-up — the verification does not need cache staleness to be a first-class feature for M3 acceptance.
- **`ktx doctor` flag for cost coverage** (D11 §"Auto-populate policy"). The doctor may flag records where all three of `(tokens_in, tokens_out, model)` are NULL as a normal-severity warning. Not blocking M3; can be folded in as a stretch or deferred.

### 4.3 Other open deliberations M3 surfaces

- **"How per-layer savings roll into session and benchmark headline numbers without double-counting"** (`ARCHITECTURE.md` Open deliberations — Measurement and benchmark). M3 is about cost-per-correct, not about savings. The savings question can be deferred past M3. If M3 wants to compare "with ktx" vs "without ktx" for the same task, the comparison is `bench compare` — out of M3 acceptance scope.
- **"How session roll-up counters relate to append-only call evidence for benchmark aggregation"** (`ARCHITECTURE.md` Open deliberations — Record model and persistence). The M2 `ktx list` rollup pattern (SQLite aggregates that skip NULL) generalizes to `bench run` rollups. No new deliberation needed for M3; the pattern is established. Mark this as ready to move from "open" to "decided" once `bench run` lands, citing M3 as the deciding milestone.
- **`ktx bench` command surface** (D2). Already decided. The subcommand shape is `run/report/compare`; `run` and `report` are M3's contract, `compare` is a future operation.

### 4.4 Cosmetic carry-overs (not blocking)

- D11 `feeds_into` line in `decisions.yaml` reads `kTX write` (uppercase X). Trivial typo; fix in the same commit that touches decisions.yaml.
- `CHANGELOG.md` absent. Worth adding when M3 ships (or earlier) but not blocking planera.
- M1 acceptance log step 10 payload drift (out of M2 scope per the M2 verification). Doc follow-up; not blocking M3.

---

## 5. Recommended planera scope

**One M3 plan**, structured as four sequential phases, with explicit acceptance criteria for each. Splitting into M3a/M3b sub-milestones would be artificial: D12 (methodology) and D11 (implementation) are both small, and `bench run`/`report` cannot land without both. The phases are:

1. **D12 resonera** — record the benchmark methodology. Output: `.agentera/decisions.yaml` D12 entry with confidence: firm; a published methodology file (path TBD by D12) at v1. No code. Closes when D12 is recorded and the methodology file exists.

2. **D11 implementation** — fetch `https://models.dev` on first use, write to `$XDG_DATA_HOME/ktx/prices.json`, derive `cost` from `tokens_in + tokens_out + model` when the caller omitted it, soft-fail on fetch error (stored cost still surfaced, no error), idempotent cache write. Schema is unchanged (the M2 columns are sufficient). Closes when the M2 sidecar scenario 2 (missing M2) followed by D11 derivation produces a non-NULL `cost` matching the published Anthropic list prices within rounding.

3. **`ktx bench run` and `ktx bench report`** — `bench run` consumes the methodology + a (agent, model, task) triple, runs N trials via the existing `ktx run` wrapper path (or sidecar, methodology decides), scores each trial per the methodology's scoring function, writes a `bench_runs` row with the headline, prints the headline to stdout (one-line summary to stderr per D1 wrapper convention). `bench report` reads a `bench_runs` row by id and emits the headline + methodology reference (JSON default, `--format text`). Schema add: `bench_runs` table. Closes when a real run produces a publishable headline and `bench report --format text` renders it with the methodology reference visible.

4. **M3 acceptance verification** — `.agentera/verification/m3-acceptance.md`. Two independent `bench run` invocations on the same inputs produce the same headline (modulo methodology-acknowledged non-determinism). M2 regression preserved. The M2 plan's M3-readiness paragraph is satisfied: cost is reconstructable *and* a per-correct number is published *and* the methodology is on disk.

Justification for one plan over two sub-milestones:
- D12 (resonera) and D11 (implementation) are both < 1 day of work. Splitting into M3a/M3b would add an acceptance ceremony for each without changing the work.
- `bench run` cannot be implemented without both: without D12, `bench run` has nothing to score against; without D11, the headline lacks the cost half.
- The M3 acceptance is a single line in `ROADMAP.md`. One plan → one close is the right shape.

---

## 6. Out-of-scope for M3

To prevent M3 from growing teeth, the following are explicitly NOT in M3:
- **`bench compare`** — future operation against a baseline or a previous run. D2 names the subcommand; M3 ships `run` and `report` only.
- **Per-layer savings roll-up** — M4+ territory. M3 is cost-per-correct, not savings.
- **Compression impact on the benchmark** — M4+. M3 measures cost with no compression in the loop.
- **Multi-task / multi-model sweeps** — methodology v1 is one task, one model, one agent. Sweeps are a v2.
- **Statistical significance / confidence intervals** — M3 is the headline number, not a method paper. Trial count is whatever the methodology commits to (likely N≥3 for variance check, but no formal CI).
- **Cross-session memory in the benchmark** — M5+. The bench consumes a fresh task each time.
- **Auto-refresh of the price cache** — D11 explicitly defers. M3 ships a fetch-on-first-use with a manual refresh hook; the TTL/refresh policy is a post-M3 resonera.
- **A second scoring function** (e.g., LLM-as-judge alongside exact match) — pick one in D12, document the other as a future option.
- **`ktx doctor` cost-coverage warning** — D11 mentions it as a future possibility. Not required for M3.
- **Tilth-style external artifact format** (separate JSON, signed, etc.) — `bench report` JSON is the artifact. A more elaborate format is post-M3 if the headline gains users.
- **CHANGELOG.md, M1 log step 10 fix, D11 `kTX` typo** — cosmetic; fold into the M3 commit if cheap, otherwise defer.

---

## 7. Carry-forward from M2 Surprises / Drift

Items from `.agentera/verification/m2-acceptance.md` "Surprises" and `plan.surprises` that M3 should resolve or accept:

1. **Wrapper M2 extraction gap (OpenCode emits tokens on `step_finish`, not `tool_use`)**. The M2 acceptance documented this as a "not a regression" because the sidecar path is the live M2 source. For M3: `bench run` must produce a number for a real (agent, model, task) run, and the methodology must name whether the run is fed via the wrapper (where M2 fields stay NULL for OpenCode tool_use rows) or the sidecar (where the caller can supply M2 fields per the D11 contract). The D12 methodology should commit to one. If the methodology uses the wrapper path, the M3 verification must document that cost is derived post-hoc via the D11 implementation, not captured at write time. **Recommended**: methodology v1 uses the sidecar path for the (agent, model, task) under test, because it is the only path that supplies M2 fields per-call for OpenCode today.

2. **Auto-compute cost from tokens+model** (D11 §"Auto-populate policy", deferred to post-M2 resonera). Now M3 territory. This is the implementation of D11's reconstructability contract at write time, not at read time. M3's D11 implementation phase should resolve this.

3. **Cache TTL and refresh policy** (D11 §"Dollar model", deferred to a future resonera). M3 needs a default; can be a documented "fetch on first use, never auto-refresh" plus a manual `ktx prices update` command, with a post-M3 resonera for the policy. **Recommended**: ship the simplest default in M3; defer the policy.

4. **M1 acceptance log step 10 payload drift** (M2 Surprises §1). Out of M2 scope. Worth fixing when the M1 log is next touched. Not blocking M3.

5. **`ktx doctor` m2_columns check** is the only M2 diagnostic; the verification log notes that "doctor flags pre-M2 databases" is the warning state, but `store.Open` migrates on first open so the warning is rarely observable. This is fine for M3. No carry-forward.

6. **DRY risk on `m2Columns` / `m2Schema`** (in M2 health check). Noted but not blocking. Worth consolidating during M3 if the bench schema adds another column-list constant, but not a M3 deliverable.

7. **D11 `feeds_into` typo** (`kTX write`). Fix in the next decisions.yaml touch.

---

## Summary for planera

- **M3 acceptance is one line in ROADMAP.md.** It demands a published headline number that anyone can reproduce from a written methodology.
- **Two prerequisite decisions shape the work**: D12 (methodology — record in resonera before any code) and D11 implementation (carry out the recorded decision — required before `bench run` can produce a cost-bearing headline).
- **One M3 plan, four phases**: D12 → D11 implementation → bench commands → verification. Splitting into M3a/M3b would be ceremony without changing the work.
- **Out of scope is long**: `bench compare`, per-layer savings, compression impact, multi-task sweeps, statistical significance, cache auto-refresh, tilth-style external artifacts. The list is the contract that keeps M3 from leaking into M4/M5.
- **Carry-forward**: the M2 wrapper gap means D12 should commit to the sidecar path for the headline (agent, model, task) under test; the D11 deferred items (auto-compute, cache TTL) become M3 work or M3 defaults.
