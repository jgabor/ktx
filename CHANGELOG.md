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
- `ktx stats summary` schema 6 reports the first and last contributing model-telemetry captures, observed span, contributing KTX session and model-step counts, and explicitly unavailable requested-window coverage for observed provider prompt/prefix-cache token subsets.

### Fixed

- `ktx hook` now takes delivery footer policy from the selected retained artifact's recorded structured-recovery provenance. Exact retries preserve repaired JSON and NDJSON validity and ordinary retained-artifact footer behavior; legacy artifacts without provenance remain readable and keep conservative behavior without inferred reconstruction.
