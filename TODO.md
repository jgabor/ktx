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

Codebase-health deepening opportunities surfaced in the 2026-06-21 architecture review. The three items below have all been completed by archived plans:

- [x] ~~**Cost-deriver consolidation**~~ · resolved 2026-06-21 by plan `.agentera/archive/PLAN-2026-06-21-deepen-cost-db-parser.yaml` — `costDeriver` deep module (`internal/db/cost.go`, 165 LOC) with `PriceSource` interface and `CostInputs` struct; `(*DB).deriveCost` and `(*DB).deriveBenchCost` are thin wrappers; bench caller pre-filters zero-tokens case. Test coverage: `internal/db/cost_test.go` (24 table-driven subtests covering every D11 branch). Round-trip equivalence proven against pre-refactor regression net.
- [x] ~~**`internal/db` file reorganisation**~~ · resolved 2026-06-21 by the same plan — package files re-cut by concern: `open.go` (485), `session.go` (264), `record.go` (538), `bench.go` (359), `prices.go` (486), `inspect.go` (285), `cost.go` (165). `db.go`, `persist.go`, `rollup.go` removed. Public API unchanged; every regression test passes byte-identical.
- [x] ~~**Event-parser extraction**~~ · resolved 2026-06-21 by the same plan — `internal/parser` package with `ParseEvent(line []byte) (*db.ToolCall, bool)` as the single source of truth for the ktx tool_call shape. 24 table-driven subtests in `parser_test.go` covering OpenCode tool_use, fallback flat shape, and 5 non-tool event shapes. `internal/cli/run.go` reduced 820 → 458 LOC. M4 hook path will import the same package.

Future deepening candidates (post-2026-06-21 audit): see `.agentera/decisions.yaml` for the next plan's scope. Top of the backlog per the post-deepening inspektera audit: extract `internal/methodology/` from `internal/cli/bench.go` (240 LOC of hand-rolled YAML parsing); generalise `extractFormat` to `extractFlag` and collapse the three hand-rolled flag walkers (`parseInitArgs`, `dbPreParse`, `parseRunArgs` + `filterKtxFlags`).

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
