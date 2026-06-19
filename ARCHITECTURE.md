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

**Token counting source: provider-reported.** ktx extracts `tokens_in`/`tokens_out`/`model`/`provider`/`cost`/`latency_ms` from the runtime's event stream (OpenCode `--format json` or sidecar payload) and persists them as-is; NULL when absent. ktx never re-tokenizes stored strings — "boring before clever" forbids a non-stdlib tokenizer dep and the provider count is what the user is billed.

**Dollar model: https://models.dev.** Same source OpenCode uses, so prices match what the user is billed (no drift). Local cache at `$XDG_DATA_HOME/ktx/prices.json` keyed by `(provider, model)` with input and output USD-per-million-token rates. ktx fetches on cache miss; fetch failure is a soft failure (cost not derived, stored cost still surfaced, no error). Cache TTL and refresh policy are deferred to a future resonera.

**Reconstructability contract.** Cost is reconstructable when `tokens_in`, `tokens_out`, and `model` are all non-NULL against the cached `(provider, model)` price. Formula: `cost = (tokens_in * in_price + tokens_out * out_price) / 1_000_000` in USD. `ktx get` / `ktx list` show stored values verbatim — no derivation at read time. The contract is the *property* "with these three non-NULL, anyone with the cached price can compute cost," not a ktx feature.

**Auto-populate policy.** ktx auto-fills: `id` (D3 nanoid if absent), `session_id` (auto-create bare session per D9), `content_hash` (always recomputed from stored input+output — ktx owns integrity, not the caller), `timestamp` (clock if absent). ktx does **not** auto-fill any M2 field — `tokens_in`/`out`, `cost`, `latency_ms`, `model`, `provider` come from the runtime (wrapper) or the caller (sidecar). NULL is preserved when absent; `0` is preserved when explicitly set (absent vs zero survive the round-trip per plan task 3 acceptance). Auto-compute of cost from tokens+model is deferred to a post-M2 resonera per the plan. `ktx doctor` may flag records where all three of `(tokens_in, tokens_out, model)` are NULL as a normal-severity warning; severity and command scope are a future resonera.

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
- [x] Whether `ktx db --path` applies per-invocation only, persists in config, or both; env-var override for CI (`KTX_DB` or equivalent). **Decided**: per-invocation only, no config persistence. Env var `KTX_DB` for CI. Precedence: `--path` flag > `KTX_DB` env > default. `ktx db` subcommands: `ktx db path` (print current), future `ktx db prune` (orphan cleanup, D1 revised). See Decision 8 revised.

### Router and classification

- [ ] Taxonomy of content types the router recognizes and the order of fallback when classification is ambiguous.
- [ ] How layer (shell, tool, code, instructions, session) maps to router profiles—one-to-one or many-to-one.
- [ ] Signals available at ingress beyond raw bytes (command name, tool name, MIME hints, agent metadata).

### Pipeline and compression

- [ ] Default built-in stage set per layer for PoC versus MVP—what ships without user configuration.
- [ ] Pipeline stage contract: inputs, outputs, failure modes, and conservative pass-through rules.
- [ ] Where reversibility is captured in the pipeline (every stage versus pipeline boundary only).
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
- [ ] Benchmark methodology v1: agent, model, task, ground truth, scoring, and published artifact shape.
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
