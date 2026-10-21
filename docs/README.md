# ktx documentation

New to ktx? Start with the [project README](../README.md). It covers
installing ktx, recording your first session, and turning on compression.

The docs are grouped by what you want to do:

- **Guides** show how to use a feature.
- **Reference** pages are the exact rules ktx follows. Tests check that they
  stay true.
- **Reports** are dated results from past experiments. They are kept as
  evidence and are not updated.

## Guides

| Guide                                                | What it covers                                                                        |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------- |
| [Pipelines and benchmarks](guides/pipelines.md)      | Compression stages, routing, recipes, config optimization, replay, and benchmark runs |
| [OpenCode integration](guides/opencode-telemetry.md) | Installing the OpenCode plugin, what it records, and troubleshooting                  |
| [Diagnostic logging](guides/logging.md)              | The optional log file for database-unavailable errors                                 |
| [Development and benchmarking](development.md)       | Building from source, running the checks, and measuring compression with `ktx bench`  |

## Reference

| Contract                                                                  | What it defines                                                               |
| ------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| [Database safety](reference/database-safety.md)                           | Which commands read or write the database, and how development stays isolated |
| [Schema readiness](reference/schema-readiness.md)                         | How ktx upgrades older databases automatically, and the release gate          |
| [Cache reuse metric](reference/cache-reuse-metric-contract.md)            | How prompt-cache reuse is measured and what it can claim                      |
| [Critical-path performance](reference/critical-path-performance.md)       | Performance and availability rules for the hook and recording path            |
| [Component benchmarks](reference/component-benchmarks.md)                 | The developer-only component measurement runner                               |
| [Real-process load benchmarks](reference/real-process-load-benchmarks.md) | The developer-only real-process load harness                                  |

## Reports

| Report                                                                             | Result                                                                    |
| ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| [Cache reuse live validation](reports/cache-reuse-live-validation.md)              | Five-pair OpenCode 1.18.5 comparison of cached-input share                |
| [Runtime compression telemetry](reports/runtime-compression-telemetry-evidence.md) | Ten-attempt OpenCode run with compression telemetry                       |
| [Layer 1 cache and cost](reports/layer-1-cache-cost.md)                            | Early benchmark of shell-output compression and cache cost                |
| [Native Linux qualification](reports/native-linux-qualification.md)                | The tested Linux host and what it does and does not prove                 |
| [External benchmark campaign](reports/skillsbench-campaign.md)                     | A completed external release-validation campaign                          |
| [External campaign identity](reports/external-validation.md)                       | The tracked lock check for that campaign                                  |
| [Compression candidates](reports/compression-candidate-qualification.md)           | Three compression ideas that were assessed and not adopted                |
| [Routing candidates](reports/routing-candidate-characterization.md)                | Evidence behind command-aware routing                                     |
| [Runtime-visible prefix](reports/runtime-visible-prefix-characterization.md)       | What OpenCode runtime events show about prompt prefixes                   |
| [Instruction normalization](reports/normalization/normalization-conclusion.md)     | Why no instruction normalizer was adopted, with the full campaign records |

Machine-readable schema readiness results live in
[`evidence/schema-readiness/`](evidence/schema-readiness/).
