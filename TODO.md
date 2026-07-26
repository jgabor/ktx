# TODO

This document is the canonical executable backlog for implementation, evidence, benchmark, documentation, verification, and discussion work. [`ARCHITECTURE.md`](ARCHITECTURE.md) owns conceptual architecture and unresolved architectural questions; every such question has one cross-linked discussion task below. Git history, recorded decisions, and retained evidence own completion history.

## Immediately actionable

- [ ] **JSON structure guard** — Detect valid JSON that an earlier stage made structurally invalid and render a smaller faithful representation from the parsed value when possible. Cover single values, NDJSON, and JSON followed by a text trailer; preserve the current pipeline gate and conservative failure behavior. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Route exit-status validation** — Align configuration validation with the documented `success`, `failure`, and `any` values so misspellings cannot silently behave as wildcard routes. Preserve current routing until this separately scoped change is implemented and tested. → [Router](ARCHITECTURE.md#router)
- [ ] **Unified hook processing flow** — Separate Codex, OpenCode, and flat payload extraction from the common session resolution, source persistence, compression, artifact persistence, recovery-cue, and envelope-rendering flow. Preserve trusted-ingress identity, first-evidence ownership, and first-compressed-artifact authority. → [Ingress](ARCHITECTURE.md#ingress)
- [ ] **Structured experiment summary model** — Replace internal `map[string]any` traversal between summary generation, validation, ranking, and rendering with a defined Go representation. Preserve serialized schemas and byte-for-byte regeneration of retained experiments. → [Benchmark](ARCHITECTURE.md#benchmark)
- [x] **Authoritative benchmark regeneration** — Materialize each benchmark attempt and aggregate once from retained tool-call rows, raw runtime streams, the first append-only orchestration envelope, manifest, and pricing snapshot. Treat session counters, copied measurement fields, summaries, rankings, and reports as validation-only projections; reject conflicting duplicates; preserve byte-for-byte regeneration for retained legacy schemas. → [Benchmark](ARCHITECTURE.md#benchmark)
- [x] **Field-scoped savings attribution reporting** — Report tool-delivery bytes, runtime-emitted uncached and cache token fields, phase-separated setup and measured quality/cost, runtime-reported cost, and price-derived estimates with field authority, evidence class, provenance, comparison unit, and truthful unavailable labels. Keep logical totals and break-even as derived views; never add cross-field deltas or infer provider-internal cache, request, or billing semantics. → [Benchmark](ARCHITECTURE.md#benchmark)
- [ ] **Single pipeline resolution per operation** — Load configuration once for each hook or replay operation, resolve routing and profile settings through one path, and report the profiles and stages that ran. Do not expand the stage contract. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Read-only pipeline diagnostics** — Extend `ktx doctor` with pipeline and configuration sanity checks while preserving its read-only contract. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Consolidated CLI argument handling** — Give run, init, db, stats, bench, plugin, and shared format handling one consistent interpretation of owned flags, consumed values, and passthrough arguments. Cover flags before and after a wrapped executable. → [I/O and query separation](ARCHITECTURE.md#io-and-query-separation)
- [ ] **Recorded-decision satisfaction review** — Review recorded choices that lack current satisfaction evidence against shipped behavior, tests, documentation, and retained benchmark evidence; record the review in the decision history without rewriting prior choices.

## Architectural discussions

Each task must run a structured discussion, append the resulting choice to the recorded decision history, remove the resolved question from `ARCHITECTURE.md`, complete this task, and add or unblock only the implementation work selected by the choice.

<a id="discuss-runtime-context-observability-and-derived-evidence"></a>

- [ ] **Runtime context observability and derived evidence** — Run a structured discussion within the runtime-only boundary and record which runtime-emitted fields or explicit runtime configuration ktx should retain for privacy-safe stable/changing-context observations, segment hashes and counts, compaction treatment, privacy limits, and truthful unavailable labels. Do not treat the resulting evidence as provider prefix composition or request-boundary evidence. → [Question](ARCHITECTURE.md#runtime-context-observability-and-derived-evidence)

<a id="discuss-runtime-cache-telemetry-configuration"></a>

- [ ] **Runtime cache telemetry configuration** — Run a structured discussion within runtime-only collection and record which runtime-emitted cache fields and explicit runtime configuration ktx should retain in benchmark manifests and attempts, runtime-adapter version provenance, and truthful unavailable labels for missing or unsupported fields. Do not add provider cache control, capability negotiation, or provider-request semantics. → [Question](ARCHITECTURE.md#runtime-cache-telemetry-configuration)

<a id="discuss-evidence-maintenance-boundary"></a>

- [ ] **Evidence maintenance boundary** — Run a structured discussion and record the choice for explicit maintenance of non-authoritative artifacts while retained originals and authoritative first evidence remain immutable and `ktx doctor` remains read-only. → [Question](ARCHITECTURE.md#evidence-maintenance-boundary)

<a id="discuss-pipeline-stage-semantics"></a>

- [ ] **Pipeline stage semantics** — Run a structured discussion and record the choice for metadata, ordering, and terminal-versus-continuable behavior beyond the existing runner contract. → [Question](ARCHITECTURE.md#pipeline-stage-semantics)

<a id="discuss-universal-preprocessing-and-filter-pack-composition"></a>

- [ ] **Universal preprocessing and filter-pack composition** — Run a structured discussion and record the composition, precedence, and inspection choice, considering wildcard preprocessors and declarative filter packs only as alternatives. → [Question](ARCHITECTURE.md#universal-preprocessing-and-filter-pack-composition)

<a id="discuss-instruction-and-tool-schema-normalization"></a>

- [ ] **Instruction and tool-schema normalization** — Run a structured discussion and record the semantic-safety, canonicalization, invalidation, and evidence choice. → [Question](ARCHITECTURE.md#instruction-and-tool-schema-normalization)

<a id="discuss-instruction-indexing-and-retrieval"></a>

- [ ] **Instruction indexing and retrieval** — Run a structured discussion and record the retrieval unit, safety floor, freshness, and recovery choice. → [Question](ARCHITECTURE.md#instruction-indexing-and-retrieval)

<a id="discuss-layer-5-boundary"></a>

- [ ] **Layer 5 boundary** — Run a structured discussion and record the history/memory boundary, lifecycle, storage, consumption, and external-tool choice. → [Question](ARCHITECTURE.md#layer-5-boundary)

<a id="discuss-trust-redaction-and-audit"></a>

- [ ] **Trust, redaction, and audit** — Run a structured discussion and record the stage-execution threat model, redaction boundary, integrity requirements, and audit contract. → [Question](ARCHITECTURE.md#trust-redaction-and-audit)

## Triggered/deferred

- [ ] **Wrapper `step_finish` enrichment** — Trigger only if a concrete use case requires per-tool cost evidence. Runtime-emitted model-step cache and cost fields remain benchmark/recorded-stream evidence and must not be attached to `tool_calls`; missing fields remain unavailable. → [Measurement](ARCHITECTURE.md#measurement)
- [ ] **Structured parsers for high-volume tools** — Trigger only when retained evidence shows specific tools dominate residual spend. Preserve failure blocks, exact occurrence counts, and the smallest faithful representation before the global gate. → [Pipeline](ARCHITECTURE.md#pipeline)
