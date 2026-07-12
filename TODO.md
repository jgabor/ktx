# TODO

The full architectural deliberation backlog is canonical in [ARCHITECTURE.md — Open deliberations](ARCHITECTURE.md#open-deliberations). Items below cross-link there; resolve via the workflow in [AGENTS.md — Documentation maintenance](AGENTS.md#documentation-maintenance).

## ⇶ Critical

_None — reserved for future critical items._

## ⇉ Degraded

_None — reserved for future degraded items._

## → Normal — Compression benchmarking

The immediate goal is to measure, compare, and rank compression pipelines using ktx's own infrastructure. The corpus, tools, and A/B comparison already exist; the work below turns them into a repeatable methodology.

- [ ] **Replay fixture portability** · `ktx bench run --replay <file>` reads the fixture directly without the capture machine's ktx.db. Needed for CI gates and sharing fixtures across machines. Deferred per D20/YAGNI; build when CI or cross-machine comparison demands it, not before. → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)

## → Normal — Layer 1 compression opportunities

Implementation candidates for lossless, semantics-preserving compression at the tool-output boundary.

- [ ] **JSON structure guard stage** · Add a stage that detects when valid JSON input has been structurally corrupted by earlier stages (e.g. `head` cutting a JSON array mid-element) and re-compacts from the parsed tree. Recognizes single-object, NDJSON, and with-trailer shapes. Opt-in behind a `json_guard = true` profile setting initially. Prevents semantic corruption that the byte-level anti-worsening gate cannot catch. → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [ ] **Wildcard preprocessor pipeline** · Add a `[pipeline."*"]` table whose stages are prepended to every profile's stage list, so always-on stages like `strip-ansi` or future `redact-secrets` fire universally without duplicating them in every profile. Today a user who forgets to add `strip-ansi` to one profile loses ANSI cleanup for that tool call. → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [ ] **Declarative filter packs** · Ship a small declarative format for predictable text: strip-ansi → scoped regex replacements → keep/drop lines → head/tail with omission markers → emit only when smaller. Covers most noise-stripping without bespoke Go stages. The `replace` stage and command-aware routing are now shipped; remaining work is a packaged rules format to share/distribute filter packs without manual TOML copying. → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [ ] **Structured parsers for high-volume tools** · Parse known output formats before rendering compactly: group search results by file with per-file and global caps, deduplicate repeated diagnostics with exact occurrence counts, keep failure blocks intact while collapsing successful cases, and select the smallest faithful representation before the global anti-worsening gate (only use grouped form when smaller than flat). Build only for commands demonstrated by ktx evidence to dominate residual token spend. → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)

## → Normal — Architecture and platform

- [ ] **Content-type detection heuristics** — JSON vs logs vs code vs prose from bytes; ambiguity fallback order. Prerequisite for per-content-type pipeline profiles (M5 routing). Single default profile works for initial benchmarking; this enables the next stage of comparison. → [Router and classification](ARCHITECTURE.md#router-and-classification)
- [ ] **Wrapper-side step_finish enrichment (deferred follow-up from D17)** · OpenCode 1.17.8 emits tokens/cost/model on `step_finish` (step-aggregate), not on `tool_use` (per-tool); the wrapper persists only `tool_use` rows, so M2 cost fields stay NULL per-row. `--replay` bypasses this (re-parses the stream), so this is NOT blocking — revisit only if a future use case needs per-tool_call cost rows (e.g., cost-by-tool breakdown, per-tool compression savings). → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [ ] **CI wiring: per-commit replay gate** · Per-commit deterministic gate runs `ktx bench run --replay <session-id>` against a committed fixture. Blocked on replay fixture portability (above). → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)

## ⇢ Annoying

- [ ] Generalise `extractFormat` to `extractFlag` and collapse the three hand-rolled flag walkers (`parseInitArgs`, `dbPreParse`, `parseRunArgs` + `filterKtxFlags`)
- [ ] Audit ledger or hash-chain beyond per-record content hash → [Security and trust](ARCHITECTURE.md#security-and-trust)
- [ ] Provider-specific adapters
- [ ] `ktx doctor` diagnostic depth → [CLI surface and ergonomics](ARCHITECTURE.md#cli-surface-and-ergonomics)
- [ ] Foreground proxy mode (wire-level compression) → [Ingress and session lifecycle](ARCHITECTURE.md#ingress-and-session-lifecycle)
- [ ] Satisfy review on recorded decisions 1–10
- [ ] Multi-agent memory / cross-session carry → [Memory and layer 5](ARCHITECTURE.md#memory-and-layer-5)

## ✓ Resolved

- [x] ~~Residual-waste discovery + replace stage~~ · `ktx stats` command ranks tool/command groups by delivered token waste (LEFT JOIN from tool_calls to compressed, uncompressed calls surface with delivered == raw). `replace` stage adds regex substitution (`replace:pattern::replacement`) following the existing Builder pattern. Filter pack pattern documented in `docs/pipelines.md`. → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression) and [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
- [x] ~~Subcommand extraction in router~~ · Added `Subcommand` field to `RouteRule`; router extracts first non-flag token after binary name. Flag-skipping handles `git --no-pager log`. Malformed input fails the condition, consistent with CommandPrefix. Recommended profiles for git (status, diff, log) and bash (ls, find) added to `ktx.example.toml`. → [Router and classification](ARCHITECTURE.md#router-and-classification)
- [x] ~~Embedded default profiles~~ · Decided against binary-embedded profiles. Recommended profiles in `ktx.example.toml` as commented blocks users copy manually is sufficient. D14 (passthrough default) preserved without modification.

- [x] ~~Command-aware filter routing~~ · Layer 1 router implemented: `[[route]]` TOML blocks with tool/command_prefix/exit_status matching, first-match-wins with default fallback. Per-profile timeout and recover_footer with default inheritance. Pipeline JSON records selected profile. `internal/router/router.go`, `internal/config/config.go`, `internal/cli/compress.go`. → [Router and classification](ARCHITECTURE.md#router-and-classification)

- [x] ~~Pipeline configurations for evaluation~~ · Named pipelines in `tools/bench/pipelines/` (`passthrough`, `sanitize`, `keep-ends-mid`, `conservative-a`, `aggressive-head`, `aggressive-keep-ends`), planted-fact corpus + multi-skill task suite in `tools/bench/corpus/` and `tools/bench/tasks/`, metrics contract and runbook in `docs/pipelines.md` (methodology `v4-eval-matrix`). Live harness supports `--task-file`, multi-fact fractional scoring, and pipeline config overrides (`--baseline-config` / `--candidate-config`). Per-tool-type routing deferred to content-type/tool routing follow-up.
- [x] ~~Live A/B harness: session-linked experiment evidence~~ · Completed multi-tool experiment `20260710T094624Z-0195d07-370898` with 3 valid, session-linked runs per pipeline side and no exclusions. Baseline mean input tokens/cost: 12084.67/$0.011637; candidate: 2926.33/$0.005588. → [Measurement and benchmark](ARCHITECTURE.md#measurement-and-benchmark)
  - **Evidence boundary:** SQLite remains product evidence only. The harness owns experiment identity, pipeline side assignment (`baseline`/`candidate`), configuration hashes, execution order, retries, and aggregate results. Each counted attempt references the exact persisted `session_id`; no side is inferred from tokens, timestamps, or database contents.
  - [x] ~~1. Repair configuration isolation and add a preflight.~~ Mount each pipeline config at `$XDG_CONFIG_HOME/ktx/ktx.toml` (`/home/node/.config/ktx/ktx.toml` in the benchmark image). Before spending a model call, the Go benchmark entrypoint requires the active stage list to match the selected pipeline.
  - [x] ~~2. Create one immutable experiment directory.~~ On startup, create `.bench-data/<experiment-id>/` without reusing or overwriting an earlier run. Write `manifest.json` before the first attempt with: experiment ID, creation time, task ID, prompt, expected output, minimum tool calls, model, target completions per pipeline side, maximum attempts, timeout, pipeline execution policy, repository commit plus dirty state, image ID, ktx/OpenCode identities, and SHA-256 plus parsed stages for each pipeline config.
  - [x] ~~3. Isolate and link every attempt.~~ Give each attempt its own directory and data mount: `attempts/<side>-<ordinal>/{ktx.db,stream.jsonl,stderr.log}`. After the container exits, query that database through `KTX_DB=<path> ktx list --format json`; accept exactly one new session and record its `session_id`. Mark zero or multiple sessions as `unlinked` rather than choosing by timestamp. Record nullable `bench_run_id` and `recorded_stream_id` only when the existing database linkage supplies them; do not manufacture either.
  - [x] ~~4. Persist append-only attempt records.~~ Append one object per attempted run to `attempts.jsonl` with experiment/attempt IDs, explicit side (`baseline`/`candidate`), cycle and ordinal, start/end timestamps, status (`completed`, `timeout`, `failed`, `unlinked`, or `invalid`), process exit code, session ID, score, tool-call count, tokens, cost, answer excerpt, config hash, artifact paths, and stdout/stderr content hashes. Preserve evidence for every failure and never count a partial stream as completed.
  - [x] ~~5. Bound execution and balance ordering.~~ Add configurable per-attempt timeout, termination grace period, target valid completions (default 3 per side), and maximum attempts. Alternate side order by cycle (`baseline→candidate`, then `candidate→baseline`) to reduce time/order bias. Continue until both sides reach the target or the attempt budget is exhausted; return nonzero when either side is short.
  - [x] ~~6. Derive reproducible output.~~ Generate `summary.json` exclusively from `manifest.json` and `attempts.jsonl`, considering only completed, linked attempts that satisfy the expected answer and minimum tool-call count. Include status counts, contributing attempt/session IDs, per-side sample size and distributions for tokens/cost/tool calls, baseline-minus-candidate deltas, pass rates, and explicit exclusions. Keep the text table as a rendering of the same JSON, not a separate aggregation path.
  - [x] ~~7. Harden adjacent harness paths.~~ Replace `validate-bench.sh` timestamp-proximity session selection with before/after session-set comparison (or the same per-attempt database isolation), and preserve the real `timeout`/child exit code instead of reading `$?` after `!`. Make `compare-pipelines.sh` use an isolated `XDG_CONFIG_HOME` and include session ID, config hash, and parsed stages in JSON output.
  - [x] ~~8. Add offline harness tests.~~ Stub Docker/ktx and use retained JSONL fixtures to cover: successful paired cycles, timeout plus retry, failed process, incomplete stream, wrong answer, fewer than the minimum tool calls, zero/multiple sessions, exhausted attempt budget, rerun non-overwrite, side-order alternation, config-preflight mismatch, and deterministic summary regeneration. Validate with `bash -n`, ShellCheck when available, the harness tests, `go test ./...`, and `go vet ./...`.
  - **Result:** The successful experiment has at least 3 valid, session-linked attempts per side; every aggregate names its contributing attempts and sessions; the configured candidate pipeline was proven before execution; raw evidence survived all failure paths; reruns create new experiment directories; `summary.json` regenerated byte-for-byte from retained inputs; and no SQLite schema change was introduced.
- [x] ~~Extract `internal/methodology/` from `internal/cli/bench.go`~~ · published methodologies are embedded from `tools/bench/` so the installed binary is checkout-independent
- [x] ~~Single-language benchmark implementation~~ · Corpus generation, experiment evidence, live comparison, ranking, validation, and read-only database reporting now ship in the Go binary; Python tooling and launchers removed.
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
- [x] ~~Phase 2 A/B verification with configured pipeline~~ · Verified on session `wwyxmfiiwkmgpmvx`: sanitize (noise cleanup) fires pipeline, ratio 0.996 (normalize trims whitespace); aggressive pipeline 89.9% reduction. Consistency/integrity/parser PASS.
- [x] ~~Corpus expansion: capture 3-5 tool-rich replay fixtures~~ · 5 fixtures captured via `ktx run --record-stream`: `ifgbcsfewlnbwkhd` (3 reads, 62KB stream), `bqdaufpnvkxxpzhp` (1 bash, 32KB), `boefqygqblmbojcr` (bash+read, 123KB stream, 60KB baseline), `plwfwucqexmrsoui` (2 bash, 4KB), `pntrhflgnjduvzfu` (3 bash, 11KB). Comparison script verified across all fixtures.
- [x] ~~Multi-pipeline comparison~~ · `ktx bench rank` runs named configurations in-process and emits text plus retained JSON rankings.
- [x] ~~Live A/B cost comparison (initial)~~ · v3 task (read_summary_v1): baseline N=3 cost $0.0061-0.0096 score=1; candidate N=2 cost $0.0053-0.0059 score=1. Tool output 23695→3947 chars (83% compression). Quality preserved. Follow-up TODO opened for longer multi-tool tasks.
- [x] ~~Recovery footer + amended anti-worsening gate (D23)~~ · ktx hook composes an agent-visible recovery footer (source_id + original size + reduction ratio + `ktx read <source_id>`) when transformation fires and the config flag is enabled. Anti-worsening gate amended to include footer bytes (`len(compressed)+len(footer) < len(input)`). Off by default; opt in via `[pipeline.default] recover_footer = true` in ktx.toml. Stored compressed rows stay content-only; `ktx read <source_id>` returns the original tool_call record for layering recovery. → [Pipeline and compression](ARCHITECTURE.md#pipeline-and-compression)
- [x] ~~Config file format, search path, and precedence between project and user scope~~ · D5 amendment: project-root (cwd) scope dropped; single user-level scope at `$XDG_CONFIG_HOME/ktx/ktx.toml` (renamed from `config.toml`). → [Configuration](ARCHITECTURE.md#configuration)
