# Changelog

## [Unreleased]

### Added

- Current prompt-cache comparisons use methodology v6 and `cache-reuse-v2` to emit token-weighted summary schema 5; methodology v5, summary schemas 1–4, and `cache-reuse-v1` remain historical evidence.
- Schema-5 prompt-cache benchmark summaries now publish a fail-closed, quality-adjusted API-equivalent cost guard; valid rejected candidates remain evidence but are not recommended by rank.
- `ktx stats summary` schema 8 is a compact closed object with window, call and token totals, and runtime-reported `prompt_cache_token_hit_rate_pct` in percentage units; measured zero remains zero and unavailable values remain null. Retained schema 6 summaries remain unchanged historical evidence.
- `mage engineeringVerify` retains paired component and real-process reports plus a hash manifest. It requires load correctness, exact reconciliation, and supported-lock checks while keeping timing descriptive.

### Fixed

- Supported read-only database handles now coordinate with writers, and supported writers coordinate each SQLite transaction across processes.
- OpenCode ordinary telemetry and hook callbacks now share one arrival-order queue, and each write-capable database handle uses one process-local SQLite connection.
- SQLite lock exhaustion now returns `database-unavailable` with the canonical database path and retry guidance instead of driver lock diagnostics.
- `ktx hook` now takes delivery footer policy from the selected retained artifact's recorded structured-recovery provenance. Exact retries preserve repaired JSON and NDJSON validity and ordinary retained-artifact footer behavior; legacy artifacts without provenance remain readable and keep conservative behavior without inferred reconstruction.
