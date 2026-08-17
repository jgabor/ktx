# Changelog

## [Unreleased]

### Added

- `ktx stats summary` schema 9 and `ktx stats breakdown` schema 3 report requested-window latency states, runtime provenance, measured coverage and time bounds, and millisecond distributions; unavailable latency remains null.
- Schema 10 preserves latency values while recording explicit measured, unknown, or malformed state, milliseconds, and runtime authority; migrated history remains non-authoritative.
- `view_image` uses an explicit empty passthrough profile by default, and the example configuration exposes the same route.
- Optional `[logs]` configuration now makes one best-effort private typed JSONL append for database-unavailable diagnostics without changing stderr behavior.
- Current prompt-cache comparisons use methodology v6 and `cache-reuse-v2` to emit token-weighted summary schema 5; methodology v5, summary schemas 1–4, and `cache-reuse-v1` remain historical evidence.
- Schema-5 prompt-cache benchmark summaries now publish a fail-closed, quality-adjusted API-equivalent cost guard; valid rejected candidates remain evidence but are not recommended by rank.
- `ktx stats summary` schema 8 now token-weights `prompt_cache_token_hit_rate_pct` across all accepted providers and models under the supported runtime contract without changing the response shape. Measured zero remains zero, incomplete evidence remains null, and retained schema 6 summaries remain unchanged historical evidence.
- `mage engineeringVerify` retains component and real-process load reports plus a hash manifest. It requires load correctness, exact reconciliation, and supported-lock checks while keeping timing descriptive.
- Engineering verification now retains a separate blocked-decompression process report with output barriers, byte-exact output evidence, hook timing and outcome, participant classes, and exact storage reconciliation.
- Engineering reports now record first-class wall-clock timing, focused `ktx read` measurements, and a reproducible noise-aware comparison contract while preserving retained schema-1 evidence.
- Real-process load reports now compare matched writers-only and mixed-agent workloads with distinct sessions, exact reconciliation, supported failure rates, and actual and percentage contention penalties.

### Fixed

- Database-unavailable diagnostics now identify the callback surface, pinned executable, database target, stable failure class, operation phase, elapsed wait, and observed Linux coordination blockers without exposing driver text or persisted content.
- Supported read-only database handles now coordinate with writers, and supported writers coordinate each SQLite transaction across processes.
- OpenCode ordinary telemetry and hook callbacks now share one arrival-order queue, and each write-capable database handle uses one process-local SQLite connection.
- SQLite lock exhaustion now returns `database-unavailable` with the canonical database path and retry guidance instead of driver lock diagnostics.
- `ktx read` now releases supported read ownership after retrieving a record, so blocked output no longer delays an unrelated supported writer.
- `ktx hook` now takes delivery footer policy from the selected retained artifact's recorded structured-recovery provenance. Exact retries preserve repaired JSON and NDJSON validity and ordinary retained-artifact footer behavior; legacy artifacts without provenance remain readable and keep conservative behavior without inferred reconstruction.
