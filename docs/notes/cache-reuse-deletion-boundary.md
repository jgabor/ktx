# Cache-reuse deletion boundary

This inventory separates current runtime cache accounting from retired cache probes. It contains paths and contracts only, not retained evidence content.

## Retain

- `internal/cache/reuse/sequence/`: runtime sequence coverage, boundary assessment, cache-share estimation, and their tests. Experiment stream parsing, regeneration, and recovery-aware comparisons import this package directly.
- `internal/experiment/`: current prompt-cache metric aggregation, regeneration, cache-share comparison, and secondary recovery estimate code and tests.
- `internal/cli/stats.go` and its focused stats tests: runtime and version filtering now import `internal/cache/reuse/sequence` directly.
- `tools/bench/specs/prompt-cache-token-hit-rate.yaml`, `tools/bench/contracts/cache-reuse.json`, and their semantic registry entries: current methodology and summary schema contracts. Keep internal schema versions and historical summary compatibility.
- `docs/cache-reuse-live-validation.md` and `docs/cache-reuse-metric-contract.md`: current runtime-derived metric documentation.

## Delete in the probe-removal task

- The remaining Go files and tests directly in `internal/cache/reuse/`. They implement direct-provider proposal loading, no-network preflight, provider-format normalization, and historical probe/fixture checks. The active `sequence/` child is excluded.
- `internal/cli/bench_evidence.go`, its command dispatch/help entries, and focused tests. These expose the retired evidence preflight commands.
- `docs/cache-evidence-*.md` and `docs/cache-evidence-opencode-runtime-*.md`.
- Probe-only files under `tools/bench/evidence/` and provider-format files under `tools/bench/fixtures/`, after confirming no non-probe retained-history check still owns them.
- `tools/bench/cache-reuse-contract-v1.json` and probe generated-path checks. Do not delete the current `tools/bench/contracts/cache-reuse.json` contract.

## Boundary check

Outside the retired CLI preflight surface, production imports resolve only to `internal/cache/reuse/sequence`. Current semantic specs and registry entries do not import or invoke the probe package. The next task can delete the probe package root while retaining its `sequence/` child and the current contract paths listed above.
