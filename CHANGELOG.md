# Changelog

## [Unreleased]

### Added

- Future benchmark manifests can explicitly activate schema 5 through
  `v6-prompt-cache-hit-rate` with `cache-reuse-v2`, providing token-weighted
  prompt-cache hit-rate evidence and fail-closed higher/lower statement
  authorization.
- Activated schema 5 benchmark summaries now include a fail-closed,
  quality-preserving API-equivalent estimated-cost-per-correct acceptance guard
  that requires a strictly lower candidate result, retained pricing provenance,
  and recommendation exclusion for valid rejected candidates.
- `ktx stats summary` schema 7 adds a provider/model-scoped, token-weighted prompt-cache token hit rate with raw buckets, runtime-emitted provenance, observed-subset scope, and explicit unavailable reasons.
- `ktx stats summary` schema 7 now exposes distinct runtime-reported and cache-aware API-equivalent estimated USD costs, including literal-zero preservation, retained pricing provenance, exact micro-USD aggregation, and identity-scoped unavailable results.
- `mage loadBench` measures disposable real-process hook and wrapper behavior with versioned contention and reconciliation reports.
- `mage componentBench` emits versioned, isolated component benchmark distributions without changing public `ktx bench` semantics.
- `mage engineeringVerify` retains paired component and real-process reports plus a hash manifest in a new caller-owned directory. It requires load-report correctness, exact reconciliation, and supported-lock checks while keeping timing descriptive.
- `ktx stats summary` schema 6 reports the first and last contributing model-telemetry captures, observed span, contributing KTX session and model-step counts, and explicitly unavailable requested-window coverage for observed provider prompt/prefix-cache token subsets.

### Fixed

- Supported read-only database handles now share local ownership with writers, preventing supported reader/writer SQLite lock overlap while retaining read-only behavior.
- Supported ktx writers now coordinate each SQLite transaction across processes through the database file, including equivalent path aliases and forced process termination.
- OpenCode ordinary telemetry and hook callbacks now share one arrival-order queue, and each write-capable database handle uses one process-local SQLite connection.
- SQLite lock exhaustion now returns `database-unavailable` with the canonical database path and retry guidance instead of driver lock diagnostics.
- `ktx hook` now takes delivery footer policy from the selected retained artifact's recorded structured-recovery provenance. Exact retries preserve repaired JSON and NDJSON validity and ordinary retained-artifact footer behavior; legacy artifacts without provenance remain readable and keep conservative behavior without inferred reconstruction.
