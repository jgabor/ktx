# OpenCode 1.18.5 cache-reuse live validation

## Status

The bounded live run successfully exercised schema-4 collection and exact
attempt-level direct cached-input share. It is **blocked for controlled
comparison and Pi-style secondary evidence**. The validator returned
`valid=false` because measured targets were not met, and the retained wrapper
stream cannot positively establish every temporal boundary class required by
the comparison contract.

No provider request, cache hit, cache miss, isolation, prefix, TTL, missed-cost,
significance, or causal conclusion follows from this run.

The offline integration that this run blocked is now implemented: each attempt
has one precreated owner, required OpenCode callbacks are synchronously retained
in a normalized fail-closed interval with explicit epochs, and compare performs
offline image capability preflight before artifact creation. This does not alter
or upgrade the retained run below. A new bounded live comparison and validation
are still mandatory before controlled or secondary evidence is eligible.

## Vehicle and scope

| Field               | Retained value                                                                                         |
| ------------------- | ------------------------------------------------------------------------------------------------------ |
| Date                | 2026-07-26                                                                                             |
| Source commit       | `e6ace6d2e425cd35840df557ba23eb46d950b6b4`                                                             |
| Host executable     | `build/ktx`, version `dev`, SHA-256 `2f28fb238d373c62cc659b61dbe510e6068a47e26687df167c64dd1b025df381` |
| Benchmark image     | `ktx-bench:oc-1.18.5-e6ace6d`                                                                          |
| Image ID            | `sha256:53c75e15e1b5be2bbf8b89007391e8bea4a6230d238e4f4814110dc2827cb237`                              |
| Image identity      | KTX `0.1.0-dev`; OpenCode `1.18.5`                                                                     |
| Model context       | `openai/gpt-5.6-luna`, variant `high`                                                                  |
| Cache mode          | `observed`                                                                                             |
| Methodology         | `v5-cache-aware`                                                                                       |
| Local artifact root | `.bench-data/cache-reuse-luna-high-oc-1-18-5-20260726`                                                 |

The image was built from a clean `git archive` of the named commit, not from an
installed or published KTX copy. The run was capped at 14 attempts: one cold
observation, one warm-up, and five requested measured attempts per side, with
no spare retry budget.

## Validation result

| Layer                 | Result                                                                                 |
| --------------------- | -------------------------------------------------------------------------------------- |
| Collection            | 14/14 attempts retained; every process exited 0 and every answer passed all five facts |
| Artifact integrity    | All 14 attempt artifact checks passed; no methodology reason was reported              |
| Regeneration          | `summary_byte_match=true`                                                              |
| Summary schema        | 4                                                                                      |
| Measured target       | Baseline 2/5; candidate 4/5; `target_met=false`                                        |
| Validator             | `valid=false`, exit 1                                                                  |
| Controlled comparison | 0 complete pairs                                                                       |
| Pi-style estimate     | 0 eligible; 41 ineligible                                                              |

The ineligible Pi units were 14 first steps and 27 consecutive pairs with
`retry_coverage_unavailable`. The controlled comparison retained
`protocol_noncompliant`, `fewer_than_five_complete_pairs`, and
`temporal_coverage_unavailable` as blockers.

Five attempts created multiple KTX sessions. Their copied attempt projection
reported ten tool calls, while materialization correctly retained an
authoritative count of zero because no single session owned the attempt. These
five tool-count differences were the only projection divergences.

## Direct accounting

All 14 attempts retained measured uncached input, cache read, cache write,
logical input, and exact direct share. Across the archive, runtime events
reported 1,057,280 cache-read tokens and measured cache write as literal zero.
The attempt-level direct descriptive statement is authorized. No comparative,
directional, success, inconclusive, or secondary statement is authorized.

Runtime-reported cost was zero under the OpenCode OAuth adapter. The separately
labeled models.dev estimate totaled $1.923378; it is not runtime billing
telemetry.

## Retained hashes

| Artifact         | SHA-256                                                            |
| ---------------- | ------------------------------------------------------------------ |
| `manifest.json`  | `e77e7287756740d148eaa626daf47cc9a553f5da071a6bc90a188813b668cc2b` |
| `attempts.jsonl` | `697c3f8dba23370e727c85f6c7e9ccc3eebcc62f0cdf89972e82e9cace94456a` |
| `summary.json`   | `416666541479526e1086ff7053d4dbc38cf301b4f1d44b34e88e5fe79c9ed107` |
| `pricing.json`   | `a65bf830edb66cce0b66bff57c9139e8a0fe43a364707d8a4fcfd1f8fbac9c7c` |

## Required next validation

1. Run the explicit no-model preflight against the freshly built image and
   retain its capability result.
2. Run one bounded live comparison only after offline verification and audit.
3. Validate retained owner and interval closure, target counts, controlled
   pairs, secondary eligibility, hashes, and byte-identical regeneration.

Until that live validation passes, the plan remains blocked and the historical
direct-only conclusions above remain the latest live evidence.
