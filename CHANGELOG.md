# Changelog

## [Unreleased]

### Added

- `ktx stats summary` schema 6 reports the first and last contributing model-telemetry captures, observed span, contributing KTX session and model-step counts, and explicitly unavailable requested-window coverage for observed provider prompt/prefix-cache token subsets.

### Fixed

- `ktx hook` now takes delivery footer policy from the selected retained artifact's recorded structured-recovery provenance. Exact retries preserve repaired JSON and NDJSON validity and ordinary retained-artifact footer behavior; legacy artifacts without provenance remain readable and keep conservative behavior without inferred reconstruction.
