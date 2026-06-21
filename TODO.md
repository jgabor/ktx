# ktx TODO

Action-oriented blockers for PoC and MVP delivery. Milestone sequencing is in [ROADMAP.md](ROADMAP.md).

The full architectural deliberation backlog is canonical in [ARCHITECTURE.md — Open deliberations](ARCHITECTURE.md#open-deliberations). Items below cross-link there; resolve via the workflow in [AGENTS.md — Documentation maintenance](AGENTS.md#documentation-maintenance).

## ⇶ Critical

- [ ] No critical items currently open

## ⇉ Degraded

- [ ] No degraded items currently open

## → Normal

- [x] ~~**Database on-disk layout**~~ · resolved by Decision 6; Decision 8 supersedes `--store` → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~**Run record schema**~~ · resolved by Decision 7 → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~**Database terminology and CLI path**~~ · resolved by Decision 8 → [Decided — Database terminology](ARCHITECTURE.md#database-terminology-and-path-decision-8)
- [x] ~~**Session identity for hooks**~~ · resolved by Decision 1 revised → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [x] ~~**Session close detection**~~ · resolved by Decision 1 revised → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [x] ~~**`ktx write` payload shape**~~ · resolved by Decision 9 → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~**PoC wrapper target**~~ · resolved by Decision 10 → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [x] ~~**Hook install scope**~~ · resolved by Decision 1/2 revised → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [x] ~~**Cost measurement mechanics**~~ · resolved by Decision 11 — provider-reported tokens, https://models.dev price source with local cache, reconstructability from tokens+model against cached price, ktx auto-fills id/session/content_hash/timestamp only (M2 fields from caller). Decision notes a cosmetic typo in the `feeds_into` line (`kTX` → `ktx`) and a 10-full-detail-cap review pressure on the decisions artifact that needs a manual `agentera` compaction pass. → [Decided — Cost measurement mechanics](ARCHITECTURE.md#cost-measurement-mechanics-decision-11)
- [ ] **Benchmark methodology** — agent/model/task for v1, ground truth, scoring, `bench run/report/compare` outputs (tilth-style cost-per-correct) → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [ ] **Default compression pipeline** — built-in behavior when no user config; reversibility layer for M4; conservative-on-failure contract → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [ ] **Content-type detection heuristics** — JSON vs logs vs code vs prose from bytes; ambiguity fallback order → [Router and classification](ARCHITECTURE.md#router-and-classification)
- [ ] **Plugin registry vs TOML pipeline** — what a `ktx plugin` is concretely and how it relates to shell-macro pipeline stages → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [x] ~~**`ktx db --path` semantics**~~ · resolved by Decision 8 revised → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)

## → Deepening

Codebase-health deepening opportunities surfaced in the 2026-06-21 architecture review. Independent of the active plan (`.agentera/plan.yaml`); each is a single-module deepening that compounds AI-navigability and testability.

The 2026-06-21 plan (`.agentera/plan.yaml` — "Lock in regression net and deepen subcommand dispatch") landed a parallel deepening: the `internal/cli/dispatch.go` deep module now owns subcommand flag-set plumbing, database open/close, error envelope rendering, and exit-code mapping; every one of the 10 subcommands registers via `dispatch.Register(verb, opts, do)` in ≤ 10 LOC of glue; the legacy `commands` slice is empty. The 2026-06-21 cleanup plan (`.agentera/plan.yaml` — "Close Degraded finding and coverage gaps") added `dbpkg.OpenWithoutMigration(*sql.DB) (*DB, error)` as a public seam for test fixtures and any future production callers that need to operate on an externally-managed `*sql.DB` without applying the migration; the dispatch layer's pre-M3 doctor fixture now uses this seam, eliminating the `unsafe.Pointer` struct-layout coupling in `dispatch_namespace_test.go`. The three items below remain valid as future candidates; they target internal/db's cost deriver / file reorg and the JSON event parser, none of which was touched by either plan.

- [ ] **Cost-deriver consolidation** — `deriveCost` (`internal/db/persist.go:419`) and `deriveBenchCost` (`internal/db/bench.go:110`) are near-identical write-time cost derivations; collapse into one `costDeriver` deep module behind a unified `(tokensIn, tokensOut, model, provider)` input so the D11 contract is testable as a pure function and any future rate source changes one place. Locality + leverage.
- [ ] **`internal/db` file reorganisation** — package files are cut by table (`persist.go` = tool_calls, `bench.go` = bench_runs, `rollup.go` = m2 rollups) rather than by concern; re-cutting along `open.go` / `session.go` / `record.go` / `bench.go` / `prices.go` / `inspect.go` reduces per-file surface and improves locality for adding new record types. Subjective; no functional change. The `OpenWithoutMigration` seam (above) is now available, so the reorg no longer has to preserve an unsafe.Pointer escape hatch for test fixtures.
- [ ] **Event-parser extraction** — JSON event → ToolCall translation (≈400 LOC of pure functions: `parseToolEvent`, `buildToolCall`, pointer helpers, `normaliseTimestamp`) lives inside `internal/cli/run.go` mixed with subprocess orchestration; extracting `internal/parser/` makes the parser directly testable and reusable across the M4 hook path (D1: `ktx hook` receives the same JSON payload shape). Leverage: parser becomes the single source of truth for "what shape is a ktx tool_call".

## ⇢ Annoying

- [ ] Multi-agent memory / cross-session carry → [Memory and layer 5](ARCHITECTURE.md#memory-and-layer-5)
- [ ] Audit ledger or hash-chain beyond per-record content hash → [Security and trust](ARCHITECTURE.md#security-and-trust)
- [ ] Aggressiveness tiers (lite / full / ultra) → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [ ] Provider-specific adapters
- [ ] `ktx doctor` diagnostic depth → [CLI surface and ergonomics](ARCHITECTURE.md#cli-surface-and-ergonomics)
- [ ] Foreground proxy mode (wire-level compression) → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [ ] Commit `VISION.md` at project root (M0; `.agentera/vision.yaml` exists)
- [ ] Satisfy review on recorded decisions 1–10

## Full deliberation index

| Section                       | ARCHITECTURE.md                                       |
| ----------------------------- | ----------------------------------------------------- |
| Ingress and session lifecycle | [link](ARCHITECTURE.md#ingress-and-session-lifecycle) |
| Record model and persistence  | [link](ARCHITECTURE.md#record-model-and-persistence)  |
| Router and classification     | [link](ARCHITECTURE.md#router-and-classification)     |
| Pipeline and compression      | [link](ARCHITECTURE.md#pipeline-and-compression)      |
| Memory and layer 5            | [link](ARCHITECTURE.md#memory-and-layer-5)            |
| Measurement and benchmark     | [link](ARCHITECTURE.md#measurement-and-benchmark)     |
| CLI surface and ergonomics    | [link](ARCHITECTURE.md#cli-surface-and-ergonomics) |
| Configuration                 | [link](ARCHITECTURE.md#configuration)                 |
| Security and trust            | [link](ARCHITECTURE.md#security-and-trust)            |
| Deepening opportunities       | [link](TODO.md#-deepening)                            |
