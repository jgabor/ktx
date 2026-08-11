# Changelog

## [Unreleased]

### Added

- Current prompt-cache comparisons use methodology v6 and `cache-reuse-v2` to emit token-weighted summary schema 5; methodology v5, summary schemas 1–4, and `cache-reuse-v1` remain historical evidence.
- Schema-5 prompt-cache benchmark summaries now publish a fail-closed, quality-adjusted API-equivalent cost guard; valid rejected candidates remain evidence but are not recommended by rank.
- `ktx stats summary` schema 8 is a compact closed object with window, call and token totals, and runtime-reported `prompt_cache_token_hit_rate_pct` in percentage units; measured zero remains zero and unavailable values remain null. Retained schema 6 summaries remain unchanged historical evidence.

### Fixed

- `ktx hook` now takes delivery footer policy from the selected retained artifact's recorded structured-recovery provenance. Exact retries preserve repaired JSON and NDJSON validity and ordinary retained-artifact footer behavior; legacy artifacts without provenance remain readable and keep conservative behavior without inferred reconstruction.
