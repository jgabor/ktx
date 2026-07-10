# TODO

The full architectural deliberation backlog is canonical in [ARCHITECTURE.md — Open deliberations](ARCHITECTURE.md#open-deliberations). Items below cross-link there; resolve via the workflow in [AGENTS.md — Documentation maintenance](AGENTS.md#documentation-maintenance).

## ⇶ Critical

_None — reserved for future critical items._

## ⇉ Degraded

_None — reserved for future degraded items._

## → Normal — Compression benchmarking

The immediate goal is to measure, compare, and rank compression pipelines using ktx's own infrastructure. The corpus, tools, and A/B comparison already exist; the work below turns them into a repeatable methodology.

- [ ] **Pipeline configurations for evaluation** · Define and document a set of pipeline configurations to test. Known from compression testing on real data: Pipeline A (`strip-ansi,dedup-blank,keep-ends:50:20,token-budget:2000`) gives 80-88% reduction; Pipeline B (`strip-ansi,dedup-blank,head:30`) gives 87-97% reduction. Need intermediate configs (e.g., `keep-ends:100:50` without token-budget) and per-tool-type configs (different treatment for `read` vs `bash` output). Document in `ktx.example.toml` or a new `docs/pipelines.md`.
- [ ] **Replay fixture portability** · `ktx bench run --replay <file>` reads the fixture directly without the capture machine's ktx.db. Needed for CI gates and sharing fixtures across machines. Deferred per D20/YAGNI; build when CI or cross-machine comparison demands it, not before. → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] **Live A/B harness: session-linked experiment evidence** · Completed multi-tool experiment `20260710T094624Z-0195d07-370898` with 3 valid, session-linked runs per arm and no exclusions. Baseline mean input tokens/cost: 12084.67/$0.011637; treatment: 2926.33/$0.005588. → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
  - **Evidence boundary:** SQLite remains product evidence only. The harness owns experiment identity, arm assignment, configuration hashes, execution order, retries, and aggregate results. Each counted attempt references the exact persisted `session_id`; no arm is inferred from tokens, timestamps, or database contents.
  - [x] **1. Repair configuration isolation and add a preflight.** Mount each arm at `$XDG_CONFIG_HOME/ktx/ktx.toml` (`/home/node/.config/ktx/ktx.toml` in the benchmark image), replacing stale `config.toml` and cwd-discovery assumptions in `scripts/live-ab.sh`, `scripts/compare-pipelines.sh`, `tools/bench/ktx-runner`, `Dockerfile.benchmark`, and the arm-config headers. Before spending a model call, run `ktx config show --json` inside the same container and require its stage list to match the selected arm.
  - [x] **2. Create one immutable experiment directory.** On startup, create `.bench-data/<experiment-id>/` without reusing or overwriting an earlier run. Write `manifest.json` before the first attempt with: experiment ID, creation time, task ID, prompt, expected output, minimum tool calls, model, target completions per arm, maximum attempts, timeout, arm execution policy, repository commit plus dirty state, image ID, ktx/OpenCode identities, and SHA-256 plus parsed stages for each arm config.
  - [x] **3. Isolate and link every attempt.** Give each attempt its own directory and data mount: `attempts/<arm>-<ordinal>/{ktx.db,stream.jsonl,stderr.log}`. After the container exits, query that database through `KTX_DB=<path> ktx list --format json`; accept exactly one new session and record its `session_id`. Mark zero or multiple sessions as `unlinked` rather than choosing by timestamp. Record nullable `bench_run_id` and `recorded_stream_id` only when the existing database linkage supplies them; do not manufacture either.
  - [x] **4. Persist append-only attempt records.** Append one object per attempted run to `attempts.jsonl` with experiment/attempt IDs, explicit arm, cycle and ordinal, start/end timestamps, status (`completed`, `timeout`, `failed`, `unlinked`, or `invalid`), process exit code, session ID, score, tool-call count, tokens, cost, answer excerpt, config hash, artifact paths, and stdout/stderr content hashes. Preserve evidence for every failure and never count a partial stream as completed.
  - [x] **5. Bound execution and balance ordering.** Add configurable per-attempt timeout, termination grace period, target valid completions (default 3 per arm), and maximum attempts. Alternate arm order by cycle (`baseline→treatment`, then `treatment→baseline`) to reduce time/order bias. Continue until both arms reach the target or the attempt budget is exhausted; return nonzero when either arm is short.
  - [x] **6. Derive reproducible output.** Generate `summary.json` exclusively from `manifest.json` and `attempts.jsonl`, considering only completed, linked attempts that satisfy the expected answer and minimum tool-call count. Include status counts, contributing attempt/session IDs, per-arm sample size and distributions for tokens/cost/tool calls, baseline-minus-treatment deltas, pass rates, and explicit exclusions. Keep the text table as a rendering of the same JSON, not a separate aggregation path.
  - [x] **7. Harden adjacent harness paths.** Replace `validate-bench.sh` timestamp-proximity session selection with before/after session-set comparison (or the same per-attempt database isolation), and preserve the real `timeout`/child exit code instead of reading `$?` after `!`. Make `compare-pipelines.sh` use an isolated `XDG_CONFIG_HOME` and include session ID, config hash, and parsed stages in JSON output.
  - [x] **8. Add offline harness tests.** Stub Docker/ktx and use retained JSONL fixtures to cover: successful paired cycles, timeout plus retry, failed process, incomplete stream, wrong answer, fewer than the minimum tool calls, zero/multiple sessions, exhausted attempt budget, rerun non-overwrite, arm-order alternation, config-preflight mismatch, and deterministic summary regeneration. Validate with `bash -n`, ShellCheck when available, the harness tests, `go test ./...`, and `go vet ./...`.
  - **Result:** The successful experiment has at least 3 valid, session-linked attempts per arm; every aggregate names its contributing attempts and sessions; the configured treatment was proven before execution; raw evidence survived all failure paths; reruns create new experiment directories; `summary.json` regenerated byte-for-byte from retained inputs; and no SQLite schema change was introduced.

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
- [x] ~~Phase 2 A/B verification with configured pipeline~~ · Verified on session `wwyxmfiiwkmgpmvx`: tier1-cleanup fires pipeline, ratio 0.996 (normalize trims whitespace); aggressive pipeline 89.9% reduction. Consistency/integrity/parser PASS.
- [x] ~~Corpus expansion: capture 3-5 tool-rich replay fixtures~~ · 5 fixtures captured via `ktx run --record-stream`: `ifgbcsfewlnbwkhd` (3 reads, 62KB stream), `bqdaufpnvkxxpzhp` (1 bash, 32KB), `boefqygqblmbojcr` (bash+read, 123KB stream, 60KB baseline), `plwfwucqexmrsoui` (2 bash, 4KB), `pntrhflgnjduvzfu` (3 bash, 11KB). Comparison script verified across all fixtures.
- [x] ~~Multi-pipeline comparison script~~ · `scripts/compare-pipelines.sh` runs 5 pipeline configs (passthrough, tier1-cleanup, conservative, aggressive-head, aggressive-keep-ends) against any session-id, outputs text table or JSON.
- [x] ~~Live A/B cost comparison (initial)~~ · v3 task (read_summary_v1): baseline N=3 cost $0.0061-0.0096 score=1; treatment N=2 cost $0.0053-0.0059 score=1. Tool output 23695→3947 chars (83% compression). Quality preserved. Follow-up TODO opened for longer multi-tool tasks.
- [x] ~~Recovery footer + amended anti-worsening gate (D23)~~ · ktx hook composes an agent-visible recovery footer (source_id + original size + reduction ratio + `ktx read <source_id>`) when transformation fires and the config flag is enabled. Anti-worsening gate amended to include footer bytes (`len(compressed)+len(footer) < len(input)`). Off by default; opt in via `[pipeline.default] recover_footer = true` in ktx.toml. Stored compressed rows stay content-only; `ktx read <source_id>` returns the original tool_call record for layering recovery. → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [x] ~~Config file format, search path, and precedence between project and user scope~~ · D5 amendment: project-root (cwd) scope dropped; single user-level scope at `$XDG_CONFIG_HOME/ktx/ktx.toml` (renamed from `config.toml`). → [Configuration](ARCHITECTURE.md#configuration)
