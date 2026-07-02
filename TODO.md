# ktx TODO

Action-oriented blockers for PoC and MVP delivery.

The full architectural deliberation backlog is canonical in [ARCHITECTURE.md — Open deliberations](ARCHITECTURE.md#open-deliberations). Items below cross-link there; resolve via the workflow in [AGENTS.md — Documentation maintenance](AGENTS.md#documentation-maintenance).

## ⇶ Critical

_None — reserved for future critical items._

## ⇉ Degraded

_None — reserved for future degraded items._

## → Normal

- [ ] **Wrapper-side step_finish enrichment (deferred follow-up from D17)** · OpenCode 1.17.8 emits tokens/cost/model on `step_finish` (step-aggregate), not on `tool_use` (per-tool); the wrapper persists only `tool_use` rows, so M2 cost fields stay NULL per-row. `--replay` bypasses this (re-parses the stream), so this is NOT blocking — revisit only if a future use case needs per-tool_call cost rows (e.g., cost-by-tool breakdown, per-tool compression savings). → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [ ] **openeval ktx-impact scenario** · Per Decision 13 Tier 2: openeval runs live agent scenarios with with/without-ktx variation arms. Start with shipped example-fixtures (hello-verify, edit-file), upgrade to real SWE tasks later. External: openeval is a separate project at `~/git/openeval`; depends on the user check-in for openeval's variation-arm API. Not started. → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [ ] **CI wiring: per-commit replay gate + scheduled openeval** · Per-commit deterministic gate runs `ktx bench run --replay <session-id>`. Scheduled CI (nightly or pre-release) runs the openeval live agent scenario. Blocked on: openeval scenario TODO and portability (`ktx bench export` + `--replay <file>` for CI runners — currently ktx.db-local only per D20). Not started. → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [ ] **openeval telemetry check for ktx compression metrics** · Check openeval's telemetry for ktx-compression-specific metrics (compression ratio, per-layer savings); file feature requests at the openeval repo if missing. External: blocked on the openeval scenario TODO landing. Not started. → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [ ] **Content-type detection heuristics** — JSON vs logs vs code vs prose from bytes; ambiguity fallback order → [Router and classification](ARCHITECTURE.md#router-and-classification)

## → Deepening

Codebase-health deepening opportunities. The 2026-06-21 architecture review's three items are resolved (see ✓ Resolved). Remaining candidates from the post-deepening inspektera audit:

- [ ] Extract `internal/methodology/` from `internal/cli/bench.go` (240 LOC of hand-rolled YAML parsing)
- [ ] Generalise `extractFormat` to `extractFlag` and collapse the three hand-rolled flag walkers (`parseInitArgs`, `dbPreParse`, `parseRunArgs` + `filterKtxFlags`)

See `.agentera/decisions.yaml` for the next plan's scope.

## ⇢ Annoying

- [ ] Multi-agent memory / cross-session carry → [Memory and layer 5](ARCHITECTURE.md#memory-and-layer-5)
- [ ] Audit ledger or hash-chain beyond per-record content hash → [Security and trust](ARCHITECTURE.md#security-and-trust)
- [ ] Provider-specific adapters
- [ ] `ktx doctor` diagnostic depth → [CLI surface and ergonomics](ARCHITECTURE.md#cli-surface-and-ergonomics)
- [ ] Foreground proxy mode (wire-level compression) → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [ ] Commit `VISION.md` at project root (`.agentera/vision.yaml` exists)
- [ ] Satisfy review on recorded decisions 1–10

## ✓ Resolved

Resolved items live in `decisions.yaml` (reasoning) and `.agentera/verification/*` (evidence). Listed here as breadcrumbs; see git history for full detail.

- [x] ~~Database on-disk layout~~ · Decision 6, superseded by D8 → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~Run record schema~~ · Decision 7 → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~Database terminology and CLI path~~ · Decision 8 → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~`ktx db --path` semantics~~ · Decision 8 revised → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~Session identity for hooks~~ · Decision 1 revised → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [x] ~~Session close detection~~ · Decisions 1, 7, 9 revised → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [x] ~~`ktx write` payload shape~~ · Decision 9 → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~PoC wrapper target~~ · Decision 10 → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [x] ~~Hook install scope~~ · Decisions 1, 2 revised, 22 → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [x] ~~Cost measurement mechanics~~ · Decision 11 → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~Benchmark methodology~~ · Decisions 12, 13 → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~Benchmark harness — record a real OpenCode session as replay fixture~~ · Decisions 16, 17, 18, 19 → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~`--replay` flag with internal A/B comparison (D13 Tier 1)~~ · Decision 20 (+ D22 Phase 2 A/B compression section) → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~Wrapper-capture `--format` passthrough bug~~ · Decision 18 → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~M3 bench run invocation fix~~ · Decisions 18, 19 → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~`extractAgentOutput` wrong JSON path~~ · resolved in the extractStreamMetrics session → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~Wrapper argv-doubling bug~~ · Decision 19 → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~`decisions.yaml` D14 schema violation~~ · commit `4f54e10`
- [x] ~~Default compression pipeline~~ · Decision 14 → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [x] ~~D9 failure-signal field~~ · Decision 15 → [Record model and persistence](ARCHITECTURE.md#record-model-and-persistence)
- [x] ~~Plugin registry vs TOML pipeline~~ · Decision 21 → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [x] ~~`ktx config show --json` companion verb~~ · Decision 21 → [Configuration](ARCHITECTURE.md#configuration)
- [x] ~~D22 Phase 2 — deterministic stages + savings measurement~~ · Decision 22 → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [x] ~~Cost-deriver consolidation~~ · plan `.agentera/archive/PLAN-2026-06-21-deepen-cost-db-parser.yaml`
- [x] ~~`internal/db` file reorganisation~~ · plan `.agentera/archive/PLAN-2026-06-21-deepen-cost-db-parser.yaml`
- [x] ~~Event-parser extraction~~ · plan `.agentera/archive/PLAN-2026-06-21-deepen-cost-db-parser.yaml`

## Full deliberation index

| Section                       | ARCHITECTURE.md                                       |
| ----------------------------- | ----------------------------------------------------- |
| Ingress and session lifecycle | [link](ARCHITECTURE.md#ingress-and-session-lifecycle) |
| Record model and persistence  | [link](ARCHITECTURE.md#record-model-and-persistence)  |
| Router and classification     | [link](ARCHITECTURE.md#router-and-classification)     |
| Pipeline and compression      | [link](ARCHITECTURE.md#pipeline-and-compression)      |
| Memory and layer 5            | [link](ARCHITECTURE.md#memory-and-layer-5)            |
| Measurement and benchmark     | [link](ARCHITECTURE.md#measurement-and-benchmark)     |
| CLI surface and ergonomics    | [link](ARCHITECTURE.md#cli-surface-and-ergonomics)    |
| Configuration                 | [link](ARCHITECTURE.md#configuration)                 |
| Security and trust            | [link](ARCHITECTURE.md#security-and-trust)            |
