# Runtime-visible prefix characterization

This characterization is limited to retained OpenCode 1.18.4 runtime events. It
uses no direct-provider evidence and makes no provider-visible prefix claim. The
machine-readable record is
`tools/bench/evidence/opencode-runtime-prefix-characterization.json`.

## Observed evidence

| Signal                       | Availability | Retained evidence                                                                                                                                                                                                         |
| ---------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Runtime session relationship | Observed     | Cache-control event ordinals 2, 3, and 6 have one session-identity hash; ordinal 4 has a distinct hash. This establishes OpenCode session continuity or separation only.                                                  |
| Submitted-prompt identity    | Observed     | Ordinals 1–4 have one prompt SHA-256; ordinals 5–6 have another. These identify submitted synthetic user text, not a composed prefix.                                                                                     |
| Execution order              | Observed     | Cache-control ordinals preserve sequential runtime event order. Temporal-control events preserve launch order and order within each invocation; completion order is unavailable.                                          |
| Token and cache measurements | Observed     | Successful cache-control and temporal-control `step_finish` excerpts retain uncached input, cache read, cache write, output, reasoning where emitted, and cost. Measured zero remains distinct from an unavailable field. |

The characterization references the existing normalized records and sanitized
source excerpts by path and SHA-256 rather than copying their measurements. The
source excerpts are sufficient to reproduce the relationships, hashes, order,
and availability labels offline.

## Unavailable evidence

The retained OpenCode event source and JSONL records do not expose the
provider-visible prefix, a cache breakpoint, tool-schema segmentation, cache
TTL, native provider request identity or count, or cache isolation. Each remains
`unavailable`; token movement, runtime session continuity, shell launch order,
and submitted-prompt hashes do not upgrade any of these labels.

The evidence retains only existing sanitized counts, measurements, SHA-256
values, and tracked source references. It contains no prompt content,
credentials, raw session or cache namespaces, message or part identifiers, or
additional source excerpts. Verification requires neither retained experiments nor
provider credentials and performs no model or provider call.
