# TODO

This document is the canonical executable backlog for implementation, evidence, benchmark, documentation, verification, and deliberation work. [`ARCHITECTURE.md`](ARCHITECTURE.md) owns conceptual architecture and unresolved architectural questions; every such question has one cross-linked deliberation task below. Git history, recorded decisions, and retained evidence own completion history.

## Immediately actionable

- [ ] **Cache-observability capability matrix** — Before attempt-level aggregation, characterize and retain each request/model-step observation by source and version: request ordinal; cache read/write; uncached input; applicable cache-control metadata; provider, runtime, and model identity; available retry data; and absent-versus-zero completeness. Preserve the raw provider/runtime token evidence so cold/warm sequences remain inspectable; do not invent a generic hit ratio, infer provider-internal misses, or attach cache evidence to `tool_calls`. → [Prefix observability and derived evidence](ARCHITECTURE.md#prefix-observability-and-derived-evidence)
- [ ] **Cache negative controls** — Retain and compare same-prefix/same-key, same-prefix/different-key, changed-prefix/same-key, cold-fill-repeat, pre/post-TTL, direct-provider/OpenCode, sequential/concurrent, and one-request/multi-request attempts. Treat existing OpenAI `promptCacheKey` isolation claims as unproven until these controls establish them; distinguish runtime routing or key partitioning from true provider cache isolation. → [Benchmark](ARCHITECTURE.md#benchmark)
- [ ] **Provider-native cache-control probes** — Build source- and version-specific evidence for OpenAI explicit breakpoints, mode, and read/write fields; Anthropic explicit `cache_control` plus creation/read fields; and Gemini explicit cache objects plus cached-token metadata. Where feasible, compare direct-provider results with runtime-normalized evidence. This is capability and evidence work, not a universal adapter design. → [Provider cache-control ownership](ARCHITECTURE.md#provider-cache-control-ownership)
- [ ] **Provider-visible prefix characterization** — Characterize trusted sources for stable-prefix and changing-tail hashes/counts; instruction and tool-schema segments; breakpoint placement; compaction or truncation events; TTL; and the privacy boundary. Feed the resulting evidence to the prefix-observability deliberation without selecting its contract. → [Prefix observability and derived evidence](ARCHITECTURE.md#prefix-observability-and-derived-evidence)
- [ ] **Retained cache-aware provider comparison** — After the capability matrix, negative controls, native probes, and prefix characterization, retain a methodology-v5 comparison across selected cache-aware providers. Use isolated mode only when negative controls confirm a runtime/provider adapter; otherwise label evidence observed-only. Report raw `cache_read` and `cache_write` distributions, reported and estimated costs, quality, setup cost, and break-even evidence without a generic cache-hit-ratio headline or provider-internal hit/miss claim. → [Benchmark](ARCHITECTURE.md#benchmark)
- [ ] **JSON structure guard** — Detect valid JSON that an earlier stage made structurally invalid and render a smaller faithful representation from the parsed value when possible. Cover single values, NDJSON, and JSON followed by a text trailer; preserve the current pipeline gate and conservative failure behavior. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Routing-candidate evidence characterization** — Use retained calls and benchmark fixtures to measure candidate content signals, ambiguous cases, residual spend, and fallback outcomes. Publish evidence for deliberation without selecting a taxonomy or profile mapping. → [Router taxonomy and profile selection](ARCHITECTURE.md#router-taxonomy-and-profile-selection)
- [ ] **Unified hook processing flow** — Separate Codex, OpenCode, and flat payload extraction from the common session resolution, source persistence, compression, artifact persistence, recovery-cue, and envelope-rendering flow. Preserve trusted-ingress identity, first-evidence ownership, and first-compressed-artifact authority. → [Ingress](ARCHITECTURE.md#ingress)
- [ ] **Structured experiment summary model** — Replace internal `map[string]any` traversal between summary generation, validation, ranking, and rendering with a defined Go representation. Preserve serialized schemas and byte-for-byte regeneration of retained experiments. → [Benchmark](ARCHITECTURE.md#benchmark)
- [ ] **Single pipeline resolution per operation** — Load configuration once for each hook or replay operation, resolve routing and profile settings through one path, and report the profiles and stages that ran. Do not expand the stage contract. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Read-only pipeline diagnostics** — Extend `ktx doctor` with pipeline and configuration sanity checks while preserving its read-only contract. → [Pipeline](ARCHITECTURE.md#pipeline)
- [ ] **Consolidated CLI argument handling** — Give run, init, db, stats, bench, plugin, and shared format handling one consistent interpretation of owned flags, consumed values, and passthrough arguments. Cover flags before and after a wrapped executable. → [I/O and query separation](ARCHITECTURE.md#io-and-query-separation)
- [ ] **Recorded-decision satisfaction review** — Review recorded choices that lack current satisfaction evidence against shipped behavior, tests, documentation, and retained benchmark evidence; record the review in the decision history without rewriting prior choices.

## Architectural deliberations

Each task must run a structured deliberation, append the resulting choice to the recorded decision history, remove the resolved question from `ARCHITECTURE.md`, complete this task, and add or unblock only the implementation work selected by the choice.

<a id="deliberate-prefix-observability-and-derived-evidence"></a>

- [ ] **Prefix observability and derived evidence** — Run structured deliberation and record trusted sources, hashes and counts, stable-prefix versus tail evidence, compaction-event treatment, and privacy limits. → [Question](ARCHITECTURE.md#prefix-observability-and-derived-evidence)

<a id="deliberate-provider-cache-control-ownership"></a>

- [ ] **Provider cache-control ownership** — Run structured deliberation and record product/runtime ownership, adapter versioning, capability negotiation, and fallback behavior outside the settled benchmark contract and confirmed benchmark adapters. → [Question](ARCHITECTURE.md#provider-cache-control-ownership)

<a id="deliberate-savings-attribution"></a>

- [ ] **Savings attribution** — Run structured deliberation and record the attribution and aggregation contract without double-counting or replacing raw cache distributions with a generic hit ratio. → [Question](ARCHITECTURE.md#savings-attribution)

<a id="deliberate-benchmark-evidence-aggregation"></a>

- [ ] **Benchmark evidence aggregation** — Run structured deliberation and record the choice for how call evidence, session roll-ups, and experiment artifacts compose. → [Question](ARCHITECTURE.md#benchmark-evidence-aggregation)

<a id="deliberate-evidence-maintenance-boundary"></a>

- [ ] **Evidence maintenance boundary** — Run structured deliberation and record the choice for explicit maintenance of non-authoritative artifacts while retained originals and authoritative first evidence remain immutable and `ktx doctor` remains read-only. → [Question](ARCHITECTURE.md#evidence-maintenance-boundary)

<a id="deliberate-router-taxonomy-and-profile-selection"></a>

- [ ] **Router taxonomy and profile selection** — Run structured deliberation and record the taxonomy, signal, layer-to-profile, ambiguity, and fallback choice using the routing-candidate evidence. → [Question](ARCHITECTURE.md#router-taxonomy-and-profile-selection)

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
