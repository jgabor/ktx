# TODO

The full architectural deliberation backlog is canonical in [ARCHITECTURE.md — Open deliberations](ARCHITECTURE.md#open-deliberations). Items below cross-link there; resolve via the workflow in [AGENTS.md — Documentation maintenance](AGENTS.md#documentation-maintenance).

## ⇶ Critical

_None — reserved for future critical items._

## ⇉ Degraded

_None — reserved for future degraded items._

## → Normal — Compression benchmarking

The immediate goal is to measure, compare, and rank compression pipelines using ktx's own infrastructure. The corpus, tools, and A/B comparison already exist; the work below turns them into a repeatable methodology.

- [ ] **Phase 2 A/B verification with configured pipeline** · The existing `--replay` on session `wwyxmfiiwkmgpmvx` shows `ratio: 1.0, pipeline: []` (passthrough — no ktx.toml was active during capture). Run `ktx bench run --replay wwyxmfiiwkmgpmvx` with a ktx.toml configured (e.g., `strip-ansi,dedup-blank,keep-ends:50:20,token-budget:2000`); confirm `pipeline` is non-empty and `ratio < 1.0`. This is a smoke test, not a full benchmark.
- [ ] **Corpus expansion: capture 3-5 tool-rich replay fixtures** · The current D20 fixture has 1 `read` tool call (23.7KB). Need diverse fixtures: multi-file reads, verbose bash output, large diffs, mixed content types. Capture via `ktx bench run --methodology v3 opencode <model> <task>` with tasks designed to trigger different tool patterns. Each fixture must have a `recorded_streams` companion (D20). Existing hook-path sessions with large output are not replay-eligible (no recorded_streams) unless re-captured with `ktx run --record-stream`. Not blocking the comparison script (can start with the one fixture), but needed for meaningful conclusions.
- [ ] **Multi-pipeline comparison script** · A script that runs `ktx bench run --replay <session-id>` with N different ktx.toml configurations against the same recorded session and tabulates the results: pipeline name, baseline_bytes, compressed_bytes, ratio, bytes_saved, reversibility (decompress == original). Output as JSON and text table. Reuses existing `--replay` A/B comparison (D22 Phase 2); the script automates the N-run loop and formats the output. No new Go code needed — shell + jq suffices for v1. Can start with the one fixture; blocked on corpus expansion for diverse content.
- [ ] **Pipeline configurations for evaluation** · Define and document a set of pipeline configurations to test. Known from compression testing on real data: Pipeline A (`strip-ansi,dedup-blank,keep-ends:50:20,token-budget:2000`) gives 80-88% reduction; Pipeline B (`strip-ansi,dedup-blank,head:30`) gives 87-97% reduction. Need intermediate configs (e.g., `keep-ends:100:50` without token-budget) and per-tool-type configs (different treatment for `read` vs `bash` output). Document in `ktx.toml.example` or a new `docs/pipelines.md`.
- [ ] **Live A/B cost comparison: compression vs passthrough** · `ktx bench run opencode <model> <task>` with `ktx init opencode` installed and a ktx.toml configured produces a session where tool_call outputs are compressed in-flight (D22). The agent sees compressed context, reducing tokens_in on subsequent LLM calls. Compare `ktx bench report` cost between a baseline run (no ktx.toml, passthrough) and a compression-configured run. This is the real "does compression save money" test. Non-deterministic (temperature>0 — D12 documented limitation), so report as a range or delta, not an exact number. Blocked on: corpus expansion (need a task that uses enough tools for the cost delta to be measurable above noise).
- [ ] **Replay fixture portability** · `ktx bench export <session-id>` writes the recorded stream + original bench_runs metrics to a portable `.jsonl` file; `ktx bench run --replay <file>` reads the fixture directly without the capture machine's ktx.db. Needed for CI gates and sharing fixtures across machines. Deferred per D20/YAGNI; build when CI or cross-machine comparison demands it, not before. → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)

## → Normal — Architecture and platform

- [ ] **Content-type detection heuristics** — JSON vs logs vs code vs prose from bytes; ambiguity fallback order. Prerequisite for per-content-type pipeline profiles (M5 routing). Single default profile works for initial benchmarking; this enables the next stage of comparison. → [Router and classification](ARCHITECTURE.md#router-and-classification)
- [ ] **Wrapper-side step_finish enrichment (deferred follow-up from D17)** · OpenCode 1.17.8 emits tokens/cost/model on `step_finish` (step-aggregate), not on `tool_use` (per-tool); the wrapper persists only `tool_use` rows, so M2 cost fields stay NULL per-row. `--replay` bypasses this (re-parses the stream), so this is NOT blocking — revisit only if a future use case needs per-tool_call cost rows (e.g., cost-by-tool breakdown, per-tool compression savings). → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [ ] **CI wiring: per-commit replay gate** · Per-commit deterministic gate runs `ktx bench run --replay <session-id>` against a committed fixture. Blocked on replay fixture portability (above). → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)

## ⇢ Annoying

- [ ] Extract `internal/methodology/` from `internal/cli/bench.go` (240 LOC of hand-rolled YAML parsing)
- [ ] Generalise `extractFormat` to `extractFlag` and collapse the three hand-rolled flag walkers (`parseInitArgs`, `dbPreParse`, `parseRunArgs` + `filterKtxFlags`)
- [ ] Audit ledger or hash-chain beyond per-record content hash → [Security and trust](ARCHITECTURE.md#security-and-trust)
- [ ] Provider-specific adapters
- [ ] `ktx doctor` diagnostic depth → [CLI surface and ergonomics](ARCHITECTURE.md#cli-surface-and-ergonomics)
- [ ] Foreground proxy mode (wire-level compression) → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [ ] Satisfy review on recorded decisions 1–10
- [ ] Multi-agent memory / cross-session carry → [Memory and layer 5](ARCHITECTURE.md#memory-and-layer-5)

## ✓ Resolved

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
