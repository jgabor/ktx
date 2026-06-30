# ktx Architecture

Aspirational architecture for ktx. This document describes **what the system is for** and **how its parts relate at a conceptual level**. It does not commit to implementation choices. Those are captured as open deliberations at the end; resolving them is what will refine this document over time.

For product direction, see `.agentera/vision.yaml`. For recorded choices already made in deliberation, see `.agentera/decisions.yaml`. For delivery sequencing, see `ROADMAP.md`. For agent workflow and document maintenance, see `AGENTS.md`.

---

## Purpose

ktx is a single, runtime-agnostic command-line layer that coding agents can adopt without importing a library or running a background service. It exists to make agent work **measurable** and **cheaper**: every meaningful interaction leaves evidence, and compression reduces what reaches the model without hiding the cost of doing so.

The architecture serves two audiences at once:

- **The agent runtime** — transparent interception, minimal friction, predictable behavior.
- **The agent-native developer** — inspectable records, reversible compression, and a benchmark number that can be cited and disputed.

Compression is the demonstrator. Measurement is the contract. The benchmark is the headline.

---

## Architectural goals

| Goal                                  | Meaning                                                                                                            |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| **Invisible until needed**            | Normal agent workflows proceed unchanged; value appears in summaries, queries, and benchmarks—not in ceremony.     |
| **Evidence over claims**              | Savings and cost are derived from stored observations, not from marketing or one-off demos.                        |
| **Reversibility before aggression**   | Compression may be strong only where originals can be recovered on demand.                                         |
| **Stable surface, evolving dispatch** | The user-facing CLI stays boring and stable; what happens behind the router can improve without breaking adoption. |
| **One artifact**                      | A single binary the runtime invokes—no daemon requirement, no library coupling.                                    |

---

## Conceptual system

At the highest level, ktx is a loop:

```
observe → classify → transform → measure → persist → report
```

**Observe** — Capture what the agent (or its tools) produced: command output, tool payloads, file reads, static instructions, session history.

**Classify** — Decide which kind of content is being handled so the right compression strategy applies. The router wins; individual algorithms are interchangeable behind it.

**Transform** — Apply compression (and optional memory-oriented distillation) through built-in behavior, user configuration, or both.

**Measure** — Attach quantitative evidence: tokens in, tokens out, cost, integrity fingerprints. Numbers, not adjectives.

**Persist** — Write per-interaction and per-session evidence; records are queryable later via the database.

**Report** — Surface run summaries, inspection queries, and aggregated benchmark results—including the headline number that proves savings.

All integration paths feed this loop. The loop is the system; adoption mode only changes _where_ observation begins.

Default database file: `$XDG_DATA_HOME/ktx/ktx.db`. Override path with `ktx db --path <file>` (Decision 8).

---

## Integration topology

Agents differ in how they can host external tooling. ktx accommodates multiple adoption paths that converge on the same internal loop and the same database:

| Mode        | Role                                                                                                                                                                              |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Wrapper** | ktx launches or wraps the agent process; observation starts at invocation. Suited to non-interactive and pipe-friendly workflows. Agent output to the caller remains transparent. |
| **Sidecar** | An already-running agent sends events to ktx; observation starts when data arrives. Suited to richer runtimes and custom integrations.                                            |
| **Hooks**   | ktx is wired into the agent's configuration; observation runs silently inside long-lived sessions (including TUIs). Suited where wrapping the process is impractical.             |

Architecturally, these are **ingress variants**, not separate products. Inspection, compression, reversibility, and benchmarking must not depend on which path recorded the data.

---

## The five compression layers

Context waste appears at different stages of an agent session. ktx aims to address all of them through one dispatch model—not five separate tools.

| Layer                      | What it targets                                      | Architectural intent                                                                                           |
| -------------------------- | ---------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| **1 — Shell**              | Command-line stdout and stderr                       | Filter noise before tool results enter context. Highest volume, lowest semantic risk when done conservatively. |
| **2 — Tool**               | Large structured tool and MCP payloads               | Compact or sandbox bulky results; retrieve detail only when the agent asks.                                    |
| **3 — Code**               | File reads, diffs, exploration dumps                 | Prefer outlines and targeted excerpts over full-file dumps.                                                    |
| **4 — Instructions**       | Always-on project memory (rules, skills, agent docs) | Shrink static context loaded every session without dropping safety-critical constraints.                       |
| **5 — Session and memory** | Conversation history and cross-session carry         | Distill what persists across turns and sessions so compression compounds over time.                            |

Each layer maps to a **pipeline profile** selected by classification—not to a separate binary or long-running service. Built-in behavior provides defaults; user extension refines or replaces stages without forking the CLI surface.

Memory is not a separate product feature; it is how compression **sticks** across sessions. Layer 5 closes the loop Layer 1–4 open.

---

## Core components (conceptual)

These are logical roles, not implementation modules. Names may change as deliberation proceeds.

### Ingress

Accepts observations from wrapper, sidecar, or hook paths. Normalizes them into a common internal representation before classification.

### Router

Inspects incoming bytes (or metadata about them) and selects a pipeline profile. This is the deliberate center of gravity: ktx optimizes for **correct dispatch**, not for owning the best compression algorithm in every category.

### Pipeline

Executes an ordered sequence of transformation stages—built-in, configured, or external—producing compressed output and failure semantics when a stage cannot run safely.

### Reversibility layer

Retains originals (or sufficient material to reconstruct them) keyed for retrieval on demand. Enables aggressive compression elsewhere without silent loss.

### Persistence

Writes session-scoped and call-scoped records to the database: inputs, outputs, metrics, and references into the reversibility layer. Exposed as mechanical I/O (`read` / `write`) and higher-level query (`get` / `list`).

### Measurement

Computes or ingests token and cost figures in a form that can be audited and reconstructed. Feeds both per-call records and benchmark aggregation.

### Benchmark

Aggregates persisted evidence into comparable headline metrics—cost per correct answer, savings over baseline, and related reports—using a published methodology.

### Extension registry

A discoverable set of user-supplied behaviors that pipelines can invoke. Distinct from hook installation (which wires ktx into an agent runtime) and from persistence (which holds evidence).

---

## I/O and query separation

Architecturally, ktx distinguishes **mechanical I/O** from **derived inspection**:

- **I/O** (`read`, `write`) moves records faithfully—raw, structured, suitable for pipelines and audits. Always raw JSON; no output-format flag.
- **Query** (`get`, `list`, `bench`) aggregates, filters, and formats for humans or downstream tools. JSON by default; `--format text` for humans.

Compression produces new records from existing ones; decompression recovers prior bytes through the public surface. Benchmark reads from the same evidence as inspection—no parallel truth.

Database path defaults to `$XDG_DATA_HOME/ktx/ktx.db`. Use `ktx db --path <file>` to override (Decision 8).

---

## Quality attributes

| Attribute                | Target behavior                                                                                                                                                       |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Transparency**         | Wrapped agents behave like unwrapped agents from the caller's perspective; side effects of measurement stay out of the agent's primary I/O where silence is required. |
| **Conservative failure** | When compression cannot run safely, pass through unchanged and record that fact—never substitute guessed content.                                                     |
| **Empirical default**    | Machine-readable output is the default; human-readable views are opt-in. Exit codes encode outcome class.                                                             |
| **Local-first**          | Evidence and reversibility material remain under the developer's control on disk; no required cloud account for core operation.                                       |
| **Boring extension**     | Prefer universal, inspectable extension mechanisms over embedded domain-specific languages or opaque magic.                                                           |

---

## Relationship to milestones

Architecture capability grows with `ROADMAP.md` milestones—not ahead of them:

| Milestone | Architectural capability unlocked                                            |
| --------- | ---------------------------------------------------------------------------- |
| **M1**    | First observed agent interaction persisted with identity, input, and output. |
| **M2**    | Per-call cost published in verifiable form.                                  |
| **M3**    | Reproducible cost-per-correct benchmark from persisted evidence.             |
| **M4**    | Reversible compression via the public surface.                               |
| **M5**    | User-defined compression behavior effective without rebuilding the binary.   |

Early milestones may implement a subset of layers and ingress paths; the aspirational model still describes the whole system so partial delivery does not fork the design.

---

## Resolved tension (intentional)

ktx will not win every compression benchmark against specialized tools. It wins by **dispatching** the right strategy per layer, **recording** what happened, and **proving** savings and cost in a form others can cite.

The CLI surface stays narrow. The space behind the router stays wide.

---

## Decided

### Database terminology and path (Decision 8)

Prose uses **record** for the unit of evidence and **database** for the on-disk SQLite file—not _record store_ or _write to the store_. Prefer verb-led phrasing (_persist the record_, _write the record_, _read the record_); name the database only when the path matters. The global `--store` flag is dropped. Database path override is `ktx db --path <file>` (default `$XDG_DATA_HOME/ktx/ktx.db`). Mechanical persistence vs derived inspection is **I/O vs query**, not store vs query.

### PoC wrapper target (Decision 10)

OpenCode is the first wrapper integration target. Non-interactive mode uses `ktx run opencode run --format json`; the wrapper parses the JSON event stream and stores each tool event as an append-only `tool_calls` row. Interactive TUI mode records session bounds only through the wrapper; individual tool calls are captured via an OpenCode plugin hook subscribing to `tool.execute.after`. To prevent double-recording when both paths are active, `ktx run` exports `KTX_SESSION_ID` into the child environment; the hook checks for this variable and becomes a noop when present.

### Session identity propagation (Decision 1 revised)

`ktx hook` is a direct child of the agent process; ppid = agent PID, stable for the TUI's lifetime. Agent-runtime adapters translate native hook models into the universal stdin/stdout protocol: OpenCode uses a thin TS plugin that shells out to `ktx hook` via Bun's `$` API (lowfat pattern: `lowfat opencode install` writes a TS adapter that shells out to the binary); Claude Code invokes `ktx hook` directly via PreToolUse/PostToolUse config. The session file at `$XDG_RUNTIME_DIR/ktx/sessions/<ppid>.json` stores `session_id` (nanoid), `ppid`, and `ppid_start_time`. A file is stale when the ppid is dead or `ppid_start_time` no longer matches (PID reuse); a stale file triggers close-old + create-new on the next call. `KTX_SESSION_ID` env var noop (Decision 10) prevents double-recording when wrapper and hook are both active.

### Session lifecycle (Decisions 1, 7, 9 revised)

A session opens on the first tool call and is updated incrementally (roll-up counters/timestamps) on each subsequent tool call. Close is path-specific — there is no explicit "last tool call" event. The wrapper closes immediately on child process exit (`ktx run` is the parent). The hook path closes lazily on the next call after ppid death (D1 staleness contract); orphaned sessions (agent died, no subsequent call) are reconciled by a future `ktx doctor --fix` or `ktx db --prune` command. The sidecar path closes on an explicit `event_type=session_close` payload to `ktx write` (D9), carrying just `session_id` + `timestamp`. The `event_type` field defaults to `tool_call`, preserving backward compatibility.

### Database path semantics (Decision 8 revised)

`ktx db --path <file>` is per-invocation only — no config persistence. Env var `KTX_DB` for CI/scripts. Precedence: `--path` flag > `KTX_DB` env > `$XDG_DATA_HOME/ktx/ktx.db` default. `ktx db` subcommands: `ktx db path` (print current path), future `ktx db prune` (orphaned session cleanup, D1 revised). `ktx db` without args defaults to `ktx db path`.

### Hook install scope (Decisions 1, 2 revised)

`ktx init` is the top-level verb for runtime adapter installation (rtk pattern: `rtk init -g --opencode`), replacing the former `ktx hook` namespace. Not all runtimes use hooks — OpenCode uses in-process TS plugins, Codex would use AGENTS.md injection — so `init` is broader than `hook install`. PoC targets OpenCode only: `ktx init opencode` writes a TS plugin to `~/.config/opencode/plugins/ktx.ts` (global) or `.opencode/plugins/ktx.ts` (`--project`). The plugin subscribes to `tool.execute.after` only for M1–M3 (passive recording); `tool.execute.before` is deferred to M4 (compression/transform). For M1, the adapter calls `ktx write` directly with the tool_call JSON (D9); the bidirectional `ktx hook` command (stdin JSON → stdout JSON response, D1) is introduced at M4 when ktx needs to return a transformed response. Flags: `--project` (project scope, default global), `--uninstall` (remove adapter), `--show` (verify installation).

### Cost measurement mechanics (Decision 11)

**Token source: provider-reported.** ktx extracts `tokens_in`/`tokens_out`/`model`/`provider`/`cost`/`latency_ms` from the runtime's event stream (OpenCode `--format json` or sidecar payload) and persists them as-is; NULL when absent, while ktx never re-tokenizes stored strings — "boring before clever" forbids a non-stdlib tokenizer dep and the provider count is what the user is billed. **Dollar model: https://models.dev**, the same source OpenCode uses so prices match what the user is billed (no drift), with a local cache at `$XDG_DATA_HOME/ktx/prices.json` keyed by `(provider, model)` with input and output USD-per-million-token rates; ktx fetches on cache miss and the fetch failure is a soft failure (cost not derived, stored cost still surfaced, no error), while cache TTL and refresh policy are deferred to a future resonera. **Reconstructability contract:** cost is reconstructable when `tokens_in`, `tokens_out`, and `model` are all non-NULL against the cached `(provider, model)` price via `cost = (tokens_in * in_price + tokens_out * out_price) / 1_000_000` in USD, and `ktx get` / `ktx list` show stored values verbatim — no derivation at read time. **Auto-populate policy:** ktx auto-fills `id` (D3 nanoid if absent), `session_id` (auto-create bare session per D9), `content_hash` (always recomputed from stored input+output — ktx owns integrity, not the caller), and `timestamp` (clock if absent), while ktx does **not** auto-fill any M2 field (`tokens_in`/`out`, `cost`, `latency_ms`, `model`, `provider` come from the runtime or the caller), NULL is preserved when absent and `0` is preserved when explicitly set (absent vs zero survive the round-trip per plan task 3 acceptance), and auto-compute of cost from tokens+model is deferred to a post-M2 resonera per the plan.

### Benchmark methodology (Decision 12)

v1 benchmark methodology is exact-match scoring against a known good output, single-trial (N=1), single-task, with the methodology file committed at `.agentera/bench/methodology-v1.yaml` (referenced by `methodology_version: 'v1'` on every `bench_runs` row); the headline is `cost_per_correct = cost_USD / max(score, 1.0e-9)` in USD to six decimal places (the `1e-9` epsilon prevents division by zero when score=0 and the resulting large value correctly signals that an incorrect answer is unboundedly expensive per correct, per the vision's "cost is the contract"), `ktx bench report` is JSON default with `--format text` for humans and emits a top-level `headline` (number) and a `rows` array (each row carries `id`, `agent`, `model`, `task`, `score`, `cost`, `methodology_version`, `timestamp`), and a `--methodology <version>` filter recomputes the headline on the filtered set. Documented limitation: temperature>0 runs are not bit-reproducible (OpenCode 1.17.8 empirical finding, `m2-acceptance.md` AC1), and exact-match scoring rejects any token-level variance; future methodology versions may introduce fuzzy matching and multi-trial aggregation. Out of scope for v1: statistical significance, `ktx bench compare` (post-M3), compression impact on bench (M4), cache TTL/auto-refresh (D11 deferral), second scoring function.

### Default compression pipeline, reversibility layer, and failure contract (Decision 14)

M4 default compression pipeline = passthrough (identity) when no `ktx.toml` is present: `ktx compress <id>` produces a new row in a new `compressed` table whose `output` equals the source's `output` and whose `source_id` references the original `tool_calls.id`; `ktx decompress <compressed-id>` resolves `source_id` and returns the source's original output bytes verbatim — the public-surface reversal satisfying M4 acceptance. Built-in Go stages (strip-ansi, dedup-blank, head, truncate, normalize) exist but are OFF unless configured in `ktx.toml` (D5).

Reversibility layer = the database. `tool_calls` holds append-only originals (D7 INSERT-only, D9 content_hash for integrity, D3 nanoid addressability); originals are never deleted. Compressed artifacts live in a separate `compressed` table (NOT in `tool_calls`, which M3 bench cost-aggregates — injecting zero-cost non-evidence rows would corrupt the cost-summed evidence set) with a `source_id` reference. No separate CCR hash store, no in-band `hash=…` markers, no retrieve tool, no BM25 sub-retrieval — reversal is out-of-band by id. Both the hook path (D1: `ktx hook` persists original via `ktx write`, returns compressed to agent) and the explicit-verb path (`ktx compress`/`ktx decompress`) share this mechanism.

Conservative-on-failure contract = runtime-enforced by the pipeline runner (not a stage-author convention — lowfat's failure mode is explicitly rejected). Three invariants: (1) empty-output guard — a stage producing empty output from non-empty input is discarded, input passes through; (2) stage-execution-error passthrough — a stage that errors (panic recovered, non-zero exit for external, timeout) passes its input through unchanged; (3) unknown-stage skipped with stderr warning. Holds for built-in Go stages and external shell-macro stages (D5) alike.

Pipeline shape = linear ordered stages; content-type routing (D5, byte-level, no ML) selects which linear profile applies (profile selection, not strategy-object-per-type dispatch) but is dormant for M4 (single passthrough default profile, no content-type detection ships). No aggressiveness model for M4 (no tiers, no knobs, no adaptive pressure). ML deps = hard no for ktx's core (no ONNX, no Magika, no embedded weights, no model-download subsystem); ML compressors appear only as user-configured external stages behind the router, per the vision's 'the router wins, the algorithm loses.'

Deferred to separate deliberations: ~~source-tool-failure protection (requires a D9 exit_code / is_error field)~~ — **resolved by Decision 15** (exit_code + is_error fields, pre-pipeline identity-row gate); content-type detection heuristics (TODO.md:29, M5 prerequisite), aggressiveness tiers (TODO.md:47, post-M4), plugin registry semantics (TODO.md:30, M5 prerequisite — D5's ktx.toml shell-macro model is the v1 extension story). See Decision 14.

### D9 failure-signal extension (Decision 15)

The flat JSON tool_call payload (D9) gains two optional nullable fields: `exit_code` (*int64, diagnostic) and `is_error` (*bool, canonical gating signal). Both follow the M2 nullable-pointer convention (nil = absent = no failure signal = treated as success; &false = explicitly successful; &true = failed; &0 = explicitly exited zero; &nonzero = exit-code-based failure). `is_error` is canonical: the pipeline runner / compressLogic checks `is_error` only; `exit_code` is never used for gating — it is diagnostic evidence persisted for inspection (ktx get surfaces it). The two fields are independent: a tool that throws (no exit code) can set `is_error=&true` with `exit_code=nil`; a bash tool sets `exit_code=&N` and `is_error=&(N!=0)` at parse time.

Source-tool-failure gate: when `is_error` is true, `ktx compress` writes an IDENTITY compressed row (output == source.output, pipeline JSON == `"[]"`, stderr warning) and prints the compressed id (stable surface). This is a PRE-PIPELINE GATE — distinct from D14's three IN-PIPELINE invariants (empty-output guard, stage-error passthrough, unknown-stage skip). The gate fires before `runner.Run`; the invariants fire during execution. The two concerns are not conflated: the gate decides WHETHER to attempt compression; the invariants decide what happens WHEN a stage fails.

Runtime mapping: OpenCode (concrete) — `exit_code` from `part.state.metadata.exit` (int64); `is_error` derived at parse time (`exit_code != 0` when present, nil when absent). Flat-fallback (defensive) — `exit_code` from top-level `exit_code`/`exit`/`code`; `is_error` from top-level `is_error`/`error`; derive `is_error` from `exit_code` when `is_error` absent but `exit_code` present. Claude Code — deferred (not knowable from codebase; flat-fallback covers it today). Sidecar (`ktx write`) — caller sets `exit_code`/`is_error` directly in the JSON payload; ktx persists as-is.

Schema migration: idempotent `ALTER TABLE ADD COLUMN` (`exit_code INTEGER`, `is_error INTEGER`) on `tool_calls` via PRAGMA table_info, same pattern as M2/bench_runs/compressed. SQLite stores `is_error` as INTEGER 0/1/NULL; the Go layer converts via `sql.NullInt64` to `*bool`. Backward-compatible: existing rows get NULL for both (absent = success). See Decision 15.

---

## Open deliberations

The items below are **not** architecture yet. They are questions for future resonera sessions. PoC/MVP blockers are cross-linked in [`TODO.md`](TODO.md). When an item is resolved, follow [AGENTS.md — Documentation maintenance](AGENTS.md#documentation-maintenance).

### Ingress and session lifecycle

- [x] How session identity propagates across hook invocations in long-lived TUI agents. **Decided**: `ktx hook` is a direct child of the agent process (ppid = agent PID, stable for TUI lifetime). Agent-runtime adapters translate native hook models into the universal stdin/stdout protocol — OpenCode uses a thin TS plugin shelling out via Bun's `$` (lowfat pattern); Claude Code invokes `ktx hook` directly via PreToolUse/PostToolUse config. Session file at `$XDG_RUNTIME_DIR/ktx/sessions/<ppid>.json` stores session_id, ppid, ppid_start_time. Staleness = liveness check (ppid alive?) + PID-reuse guard (start-time match). See Decision 1 revised.
- [x] When a session opens, updates, and closes—and what event marks "last tool call." **Decided**: Open = first tool_call. Update = per-call roll-up. Close is path-specific, not a "last tool call" event: wrapper closes immediately on child process exit; hook closes lazily on next call after ppid death (D1 staleness) + future `ktx doctor --fix`/`ktx db --prune` for orphans; sidecar closes on explicit `event_type=session_close` payload to `ktx write` (D9). See Decisions 1, 7, 9 revised.
- [x] Which agent runtime(s) the first wrapper integration targets and how output is captured without breaking transparency. **Decided**: OpenCode first; non-interactive via `--format json` parsing; interactive via wrapper bounds + OpenCode plugin hook; dedup via `KTX_SESSION_ID`. See Decision 10.
- [x] Hook install scope: which agents, which config surfaces, which events trigger silent recording. **Decided**: `ktx init` (rtk pattern) replaces `ktx hook` namespace. PoC targets OpenCode only: `ktx init opencode` writes a TS plugin to `~/.config/opencode/plugins/ktx.ts` (global) or `.opencode/plugins/ktx.ts` (`--project`). Plugin subscribes to `tool.execute.after` only (M1–M3); `tool.execute.before` deferred to M4 (compression). For M1, the adapter calls `ktx write` directly; `ktx hook` (bidirectional protocol) is introduced at M4. Flags: `--project`, `--uninstall`, `--show`. See Decisions 1, 2 revised.
- [ ] Whether a foreground proxy mode belongs in the architecture (wire-level compression) without violating the no-daemon principle.

### Record model and persistence

- [x] Exact shape of a tool-call document for sidecar writes versus wrapper capture. **Decided**: flat JSON tool_call with required fields (id, session_id, tool, input, output, content_hash, timestamp) and optional M2 fields (tokens_in, tokens_out, cost, latency_ms, model, provider). ktx auto-creates bare session if session_id absent. See Decision 9.
- [ ] How session roll-up counters relate to append-only call evidence for benchmark aggregation.
- [ ] Retention, compaction, and pruning policy for originals versus compressed artifacts.
- [x] D9 failure-signal extension: add an `exit_code` and/or `is_error` field to the flat JSON tool_call payload (D9) so the pipeline runner can apply source-tool-failure protection (headroom's protect_error_outputs) and skip compression on failed-tool output. **Decided** (Decision 15): two nullable pointer fields — `exit_code` (*int64, diagnostic) and `is_error` (*bool, canonical gating signal); when `is_error` is true, compressLogic writes an identity compressed row (pre-pipeline gate, distinct from D14's in-pipeline invariants); OpenCode maps `exit_code` from `part.state.metadata.exit` and derives `is_error` at parse time; flat-fallback reads defensively; Claude Code deferred. See Decision 15.
- [x] Whether `ktx db --path` applies per-invocation only, persists in config, or both; env-var override for CI (`KTX_DB` or equivalent). **Decided**: per-invocation only, no config persistence. Env var `KTX_DB` for CI. Precedence: `--path` flag > `KTX_DB` env > default. `ktx db` subcommands: `ktx db path` (print current), future `ktx db prune` (orphan cleanup, D1 revised). See Decision 8 revised.

### Router and classification

- [ ] Taxonomy of content types the router recognizes and the order of fallback when classification is ambiguous.
- [ ] How layer (shell, tool, code, instructions, session) maps to router profiles—one-to-one or many-to-one.
- [ ] Signals available at ingress beyond raw bytes (command name, tool name, MIME hints, agent metadata).

### Pipeline and compression

- [x] Default built-in stage set per layer for PoC versus MVP—what ships without user configuration. **Decided** (Decision 14): M4 default pipeline is passthrough (identity) when no ktx.toml is present; built-in Go stages (strip-ansi, dedup-blank, head, truncate, normalize) exist but are OFF unless configured. Reversibility layer = the database: tool_calls holds append-only originals (D7, D9, D3); compressed artifacts live in a separate `compressed` table with a source_id reference (NOT in tool_calls, which M3 bench cost-aggregates); `ktx decompress <id>` resolves source_id and returns the source's original output bytes verbatim. No CCR hash store, no in-band markers, no retrieve tool — reversal is out-of-band by id. Both the hook path (D1: database always holds the original) and the explicit-verb path share this mechanism. Conservative-on-failure contract = runtime-enforced by the pipeline runner (three invariants: empty-output guard, stage-execution-error passthrough, unknown-stage skipped), NOT a stage-author convention. Pipeline shape = linear ordered stages; content-type routing (D5, byte-level, no ML) selects which linear profile applies but is dormant for M4. No aggressiveness model for M4. ML deps = hard no for ktx core; ML only behind the router as user-configured external stages. Source-tool-failure protection, content-type detection, aggressiveness tiers, and plugin registry semantics are deferred (see open items below). See Decision 14.
- [ ] Pipeline stage contract: inputs, outputs, failure modes, and conservative pass-through rules. **Partially decided** (Decision 14): the runner-level conservative-on-failure contract is decided (empty-output guard, stage-execution-error passthrough, unknown-stage skip). The per-stage input/output contract (bytes in, bytes out, error-or-nil for built-in and shell-macro stages alike) is stated but the full stage interface (metadata, ordering guarantees, terminal-vs-continuable stages) is an M4 implementation detail. Source-tool-failure protection (headroom's protect_error_outputs) is now decided (Decision 15: pre-pipeline identity-row gate on `is_error=true`, distinct from D14's in-pipeline invariants), pending implementation.
- [x] Where reversibility is captured in the pipeline (every stage versus pipeline boundary only). **Decided** (Decision 14): reversibility is captured at the pipeline boundary, not per-stage. The tool_calls table holds append-only originals (D7, D9, D3); compressed artifacts in a separate `compressed` table reference the source by id. No per-stage reversal — `ktx decompress <id>` returns the source's original bytes regardless of how many lossy stages ran. See Decision 14.
- [ ] Aggressiveness tiers (lite, full, ultra) or a single conservative default for v1.
- [ ] Relationship between the user extension registry and configured pipeline stages—one abstraction or two.
- [ ] Whether external processes are the only extension path or some stages must remain in-process for latency and determinism.

### Memory and layer 5

- [ ] Operational definition of the memory stage: sync versus async, inline versus on session close.
- [ ] What gets distilled, where it is stored, and how the next session consumes it.
- [ ] Boundary between session-history compression and cross-session memory—same pipeline or separate profiles.
- [ ] Optional external distillation tools: allowed roles without violating boring-before-clever constraints.

### Measurement and benchmark

- [x] Token counting source: provider-reported, local tokenizer, or hybrid—and reconstructability requirements for M2. **Decided**: provider-reported; ktx extracts tokens_in/out from the runtime's event stream (or sidecar payload) and persists as-is; NULL when absent. ktx does not re-tokenize. Reconstructability contract: cost is reconstructable when tokens_in, tokens_out, and model are all non-NULL against the cached (provider, model) price; ktx get/list show stored values verbatim (no derivation at read time). See Decision 11.
- [x] Dollar model: list prices, user overrides, or provider-agnostic units. **Decided**: https://models.dev (the same source OpenCode uses, so prices match what the user is billed); local cache at $XDG_DATA_HOME/ktx/prices.json; ktx fetches on cache miss; fetch failure is a soft failure. See Decision 11.
- [x] Benchmark methodology v1: agent, model, task, ground truth, scoring, and published artifact shape. **Decided**: exact-match scoring, single-trial (N=1), single-task, methodology file at `.agentera/bench/methodology-v1.yaml`, headline `cost_per_correct = cost / max(score, 1e-9)` USD; `ktx bench report` JSON default + `--format text`. See Decision 12.
- [x] Benchmark execution harness and comparison layer: how ktx benchmarks itself accurately, automatically, and reliably above D12's scoring contract. **Decided**: two-tier model. Tier 1 (ktx-native): `ktx bench run --replay <session-id>` uses a recorded real OpenCode session, replays through ktx's pipeline with internal A/B comparison (baseline vs compressed) in a single invocation; deterministic, fast, per-commit CI gate; pre-M4 measures wrapper overhead and cost accuracy, post-M4 shows compression delta. Tier 2 (openeval): openeval runs live agent scenarios with with/without-ktx variation arms; starts with shipped example-fixtures, upgrades to real SWE tasks later; scheduled CI, pass@k + cost-per-passed-task. ktx is unaware of openeval; openeval sees ktx as a wrapper command. See Decision 13.
- [x] Wrapper-capture validation gate prior to D13 Tier 1 replay implementation. **Decided**: sequenced — validate wrapper capture against real opencode before recording contract or `--replay` codepath. Validation = `ktx run opencode run --format json -m <methodology-pinned-model> <methodology-pinned-prompt>` (opencode has a non-interactive JSONL emission mode; rmux/TUI driving unnecessary). Acceptance bar = full D12 conformance broken into five per-layer [PASS]/[FAIL] assertions emitted by a shell script (session-row+agent / token evidence on captured JSONL / cost evidence on captured JSONL / parser 1-to-1 with persisted tool_calls / opencode answer exact-match against methodology expected_output). Recording contract deferred until validation produces evidence; on pass, the resulting session id IS the candidate replay fixture source (no portable fixture-file format needed for M5). M3 bench run's literal `ktx run opencode <prompt>` invocation is suspected-broken against real opencode (TUI default subcommand); that fix is a separate TODO. **Amended (Decision 17, provisional)**: L2 and L3 layer assertions read from the captured JSONL stream that `ktx run` tees to the validation script's stdout, not from per-tool_call rows persisted to ktx.db — OpenCode 1.17.8 emits tokens/cost/model on `step_finish` (step-aggregate), not on `tool_use` (per-tool); the wrapper persists only `tool_use` rows, so they are NULL-by-design against real OpenCode (m2-acceptance.md §3). L2: at least one `type=step_finish` line on the captured stream has `part.tokens.input > 0` AND `part.tokens.output > 0`. L3: either (a) at least one `step_finish` line carries `part.cost` as a number, or (b) D11 derivation is reconstructable from the captured step_finish tokens plus the methodology-pinned model identifier against the `ktx prices` cache, script-computed derived cost > 0, cache-miss treated as a real L3 FAIL per D11 environmental-failure policy. L1, L4, L5 unchanged. The wrapper-side step_finish-enrichment gap (back-fill tool_call rows with step_finish tokens/cost/model) is a deferred follow-up, not a gate failure. **Amended (Decision 18, firm)**: D16's 'must NOT modify ktx source under internal/ or cmd/' constraint is relaxed to permit a minimal, tightly-scoped edit in `internal/cli/dispatch.go`: skip `extractFormat` (flags.go:34) when the registered verb's `PassthroughStdout` opt is true (only `run` today) — otherwise ktx steals `--format json` from `ktx run opencode run --format json ...` before the agent sees it. **Amended (Decision 19, firm)**: a second wrapper bug was found after D18: argv doubling in `internal/cli/run.go` executeRun. `runCommandFunc` passed the whole `ctx.Args` (including the agent name at position 0) as `agentArgs`; `executeRun` then called `runCmd(ctx, agent, agentArgs...)` which is `exec.CommandContext(ctx, "opencode", "opencode", "run", ...)` — Go's exec uses `agent` as argv[0] and `agentArgs` as argv[1..n], so opencode received a doubled `opencode` as argv[1], which yargs treated as the default `[project]` positional, emitting top-level help and exiting 1. Surgical fix: defensively strip `agentArgs[0]` when it equals `agent` before passing to `runCmd`; both runCommandFunc and runRunWith paths benefit. Regression evidence: scripts/validate-bench.sh produces 5/5 [PASS] against real opencode 1.17.8 (neuralwatt/glm-5.2-short-fast-flex, 'What is 2+2?'); ktx test suite green; the D18 --format-passthrough regression test still passes. See Decisions 16, 17, 18, 19. **Amended (Decision 20, provisional)**: Recording contract settled — the captured JSONL stream persists as a new INSERT-only `recorded_streams` table in ktx.db (id, session_id FK, bench_run_id FK, stream_text, content_hash, stream_format, captured_at), following D14's `compressed` table template (new artifact type → new ktx.db table, not a file, not a column on an existing table). Capture trigger is `ktx bench run` ONLY; `ktx run` (wrapper subprocess) stays a D1 transparent proxy and does NOT persist the stream. `--replay <session-id>` loads the recorded_streams row, re-parses through parser.ParseEvent (parser accuracy: tool_use count match), re-aggregates via extractStreamMetrics (cost-derivation accuracy: re-derived tokens/cost match original bench_runs row), emits a consistency headline (pre-M4: replay vs original; post-M4: baseline vs compressed via the same stream re-processed through the D14 compression pipeline). Schema migration: idempotent CREATE TABLE IF NOT EXISTS in db.Open schemaStatements, self-healing for pre-D20 databases. D17's wrapper-vs-sidecar tension RESOLVED for the --replay codepath — the wrapper path persists the stream that `--replay` reads; sidecar not needed for v1 replay. D17's L3(b) D11 derivation overlap RESOLVED — the L3(b) derivation branch IS the pre-M4 `--replay` consistency check; the two concerns unified by design. D16's candidate fixture (bench_run id `ccsbkoluqgkgtelc`, paired session_id `sdeloqgthcgasfld`) — no recorded_streams companion exists (stream was discarded in-memory before D20's persistence step landed); a fresh `ktx bench run opencode <model> <task>` invocation produces a D20-compliant replay fixture. Portability (export/import for CI runners) deferred to CI wiring per YAGNI. M4 A/B persistence gap acknowledged: `compressed.source_id` requires a persisted tool_call but `--replay` parses in-memory; "no schema change" claim is aspirational not contractual. Confidence is provisional (design-only, no implementation evidence). See Decision 20.
- [ ] How per-layer savings roll into session and benchmark headline numbers without double-counting.

### CLI surface and ergonomics

- [x] Final command tree: flat verbs versus namespaces for managed resources (plugins, hooks, bench). **Decided**: hybrid — flat verbs for actions (run, write, read, get, list, compress, decompress, init); namespaces for managed resources (plugin add/list/remove, bench run/report/compare); `ktx init` top-level for adapter installation (supersedes former `ktx hook` namespace). See Decisions 2, 1 revised.
- [x] I/O versus query command split and which operations honor output formatting flags. **Decided**: I/O commands (read, write) always raw JSON, no --format. Query commands (get, list, bench) JSON default, --format text for humans. See Decisions 2, 4, 8.
- [x] Global flags, default output encoding, and error envelope shape for agent consumers. **Decided**: four global flags (--format, --quiet/-q, --help/-h, --version/-v). JSON default. Errors: machine-parseable JSON on stderr; exit codes 0=ok, 1=usage, 2=not-found, 3=conflict, 4=internal. See Decisions 4, 8.
- [ ] Diagnostics command scope: `ktx doctor` basic scope settled (read-only health check for database, config resolution, ID format — Decision 2). Extended scope open: `--fix` for orphaned session reconciliation (Decision 1 revised), `ktx db --prune` for cleanup, pipeline sanity checks. Non-blocking for M1–M3.

### Configuration

- [ ] Config file format, search path, and precedence between project and user scope.
- [ ] How pipeline definitions reference built-in stages versus external scripts.
- [ ] Whether all compress/decompress behavior lives in config with zero per-invocation flags—confirm or revise.

### Security and trust

- [ ] Threat model for executing user-configured pipeline stages (shell, scripts, plugins).
- [ ] Redaction requirements for secrets in originals and compressed output.
- [ ] Integrity and audit expectations beyond per-record content fingerprints.

---

## Document maintenance

See [AGENTS.md — Documentation maintenance](AGENTS.md#documentation-maintenance).

Until deliberations are resolved, treat this file as the map of intent—not the blueprint.
