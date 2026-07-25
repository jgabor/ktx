# TODO

This document is the canonical executable backlog for implementation, evidence, benchmark, documentation, verification, and deliberation work. [`ARCHITECTURE.md`](ARCHITECTURE.md) owns conceptual architecture and unresolved architectural questions; every such question has one cross-linked deliberation task below. Git history, recorded decisions, and retained evidence own completion history.

## Immediately actionable

- [ ] **Retained runtime-telemetry comparison** — Retain a methodology-v5 comparison across selected runtime, model, and provider configurations using only runtime observations and provider/model identity provenance. Report raw runtime-emitted `cache_read` and `cache_write` distributions, runtime-reported and estimated costs, quality, setup cost, and break-even evidence. Keep provider documentation and pricing as context, label historical fixtures unsupported for current collection, and make no provider-internal cache, request-boundary, isolation, or prefix-composition claim. → [Benchmark](ARCHITECTURE.md#benchmark)
- [ ] **JSON structure guard** — Detect valid JSON that an earlier stage made structurally invalid and render a smaller faithful representation from the parsed value when possible. Cover single values, NDJSON, and JSON followed by a text trailer; preserve the current pipeline gate and conservative failure behavior. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Route exit-status validation** — Align configuration validation with the documented `success`, `failure`, and `any` values so misspellings cannot silently behave as wildcard routes. Preserve current routing until this separately scoped change is implemented and tested. → [Router](ARCHITECTURE.md#router)
- [ ] **Unified hook processing flow** — Separate Codex, OpenCode, and flat payload extraction from the common session resolution, source persistence, compression, artifact persistence, recovery-cue, and envelope-rendering flow. Preserve trusted-ingress identity, first-evidence ownership, and first-compressed-artifact authority. → [Ingress](ARCHITECTURE.md#ingress)
- [ ] **Structured experiment summary model** — Replace internal `map[string]any` traversal between summary generation, validation, ranking, and rendering with a defined Go representation. Preserve serialized schemas and byte-for-byte regeneration of retained experiments. → [Benchmark](ARCHITECTURE.md#benchmark)
- [ ] **Authoritative benchmark regeneration** — Materialize each benchmark attempt and aggregate once from retained tool-call rows, raw runtime streams, the first append-only orchestration envelope, manifest, and pricing snapshot. Treat session counters, copied measurement fields, summaries, rankings, and reports as validation-only projections; reject conflicting duplicates; preserve byte-for-byte regeneration for retained legacy schemas. → [Benchmark](ARCHITECTURE.md#benchmark)
- [ ] **Single pipeline resolution per operation** — Load configuration once for each hook or replay operation, resolve routing and profile settings through one path, and report the profiles and stages that ran. Do not expand the stage contract. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Read-only pipeline diagnostics** — Extend `ktx doctor` with pipeline and configuration sanity checks while preserving its read-only contract. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Consolidated CLI argument handling** — Give run, init, db, stats, bench, plugin, and shared format handling one consistent interpretation of owned flags, consumed values, and passthrough arguments. Cover flags before and after a wrapped executable. → [I/O and query separation](ARCHITECTURE.md#io-and-query-separation)
- [ ] **Recorded-decision satisfaction review** — Review recorded choices that lack current satisfaction evidence against shipped behavior, tests, documentation, and retained benchmark evidence; record the review in the decision history without rewriting prior choices.

## Architectural deliberations

Each task must run a structured deliberation, append the resulting choice to the recorded decision history, remove the resolved question from `ARCHITECTURE.md`, complete this task, and add or unblock only the implementation work selected by the choice.

<a id="deliberate-prefix-observability-and-derived-evidence"></a>

- [ ] **Prefix observability and derived evidence** — Run structured deliberation within the runtime-only boundary and record which runtime-emitted fields or explicit runtime configuration may support privacy-safe stable/changing-context observations, hashes and counts, segment treatment, compaction-event treatment, and privacy limits without claiming provider prefix composition or request boundaries. → [Question](ARCHITECTURE.md#prefix-observability-and-derived-evidence)

<a id="deliberate-provider-cache-control-ownership"></a>

- [ ] **Provider cache-control ownership** — Run structured deliberation within runtime-only collection and record product/runtime configuration ownership, adapter versioning, capability negotiation, and fallback behavior without extending runtime observations into provider-internal cache or request claims. → [Question](ARCHITECTURE.md#provider-cache-control-ownership)

<a id="deliberate-savings-attribution"></a>

- [ ] **Savings attribution** — Run structured deliberation and record the attribution and aggregation contract for runtime observations, identity provenance, and reference/estimate sources without double-counting or inferring provider-internal cache or request behavior. → [Question](ARCHITECTURE.md#savings-attribution)

<a id="deliberate-evidence-maintenance-boundary"></a>

- [ ] **Evidence maintenance boundary** — Run structured deliberation and record the choice for explicit maintenance of non-authoritative artifacts while retained originals and authoritative first evidence remain immutable and `ktx doctor` remains read-only. → [Question](ARCHITECTURE.md#evidence-maintenance-boundary)

<a id="deliberate-pipeline-stage-semantics"></a>

- [ ] **Pipeline stage semantics** — Run structured deliberation and record the choice for metadata, ordering, and terminal-versus-continuable behavior beyond the existing runner contract. → [Question](ARCHITECTURE.md#pipeline-stage-semantics)

<a id="deliberate-universal-preprocessing-and-filter-pack-composition"></a>

- [ ] **Universal preprocessing and filter-pack composition** — Run structured deliberation and record the composition, precedence, and inspection choice, considering wildcard preprocessors and declarative filter packs only as alternatives. → [Question](ARCHITECTURE.md#universal-preprocessing-and-filter-pack-composition)

<a id="deliberate-instruction-and-tool-schema-normalization"></a>

- [ ] **Instruction and tool-schema normalization** — Run structured deliberation and record the semantic-safety, canonicalization, invalidation, and evidence choice. → [Question](ARCHITECTURE.md#instruction-and-tool-schema-normalization)

<a id="deliberate-instruction-indexing-and-retrieval"></a>

- [ ] **Instruction indexing and retrieval** — Run structured deliberation and record the retrieval unit, safety floor, freshness, and recovery choice. → [Question](ARCHITECTURE.md#instruction-indexing-and-retrieval)

<a id="deliberate-layer-5-boundary"></a>

- [ ] **Layer 5 boundary** — Run structured deliberation and record the history/memory boundary, lifecycle, storage, consumption, and external-tool choice. → [Question](ARCHITECTURE.md#layer-5-boundary)

<a id="deliberate-trust-redaction-and-audit"></a>

- [ ] **Trust, redaction, and audit** — Run structured deliberation and record the stage-execution threat model, redaction boundary, integrity requirements, and audit contract. → [Question](ARCHITECTURE.md#trust-redaction-and-audit)

## Triggered/deferred

- [ ] **Wrapper `step_finish` enrichment** — Trigger only if a concrete use case requires per-tool cost evidence. Model-step cache and cost telemetry remains benchmark/recorded-stream evidence and must not be attached to `tool_calls`. → [Measurement](ARCHITECTURE.md#measurement)
- [ ] **Structured parsers for high-volume tools** — Trigger only when retained evidence shows specific tools dominate residual spend. Preserve failure blocks, exact occurrence counts, and the smallest faithful representation before the global gate. → [Pipeline](ARCHITECTURE.md#pipeline)
