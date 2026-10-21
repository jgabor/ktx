# Historical external benchmark campaign

This report summarizes a retained external release-validation effort comprising
ten campaign roots. It is historical evidence, not a current KTX workflow and
not permission to run an evaluator or provider. The repository used the
BenchFlow/SkillsBench names for the campaign; those labels and the retained task
digest do not establish official suite identity.

For current benchmark work, use the native [`ktx bench compare`](../development.md#benchmarking)
workflow. The tracked-only historical identity check is documented in
[`external-validation.md`](external-validation.md).

## Corrected result

[`skillsbench-campaign-errata.json`](../../tools/bench/evidence/skillsbench-campaign-errata.json)
is the current authority for completeness and metrics. It accepts only the Luna
replication campaign with complete terminal evidence. Three earlier completeness
claims are removed.

| Fact                             |                                          Corrected value |
| -------------------------------- | -------------------------------------------------------: |
| Complete campaigns               |                                                  1 of 10 |
| Complete attempts                |                                                        8 |
| Complete pairs                   |                                                        4 |
| Pair rewards                     | equal in 4 of 4 pairs; all 8 corrected-set rewards are 1 |
| Candidate delivered bytes        |                            lower in 2 pairs, higher in 2 |
| Candidate total tokens           |                            lower in 3 pairs, higher in 1 |
| External provider price and cost |                                              unavailable |
| Five-pair gate                   |                       incomplete; 4 observed, 5 required |
| Corrected gate decision          |                                                     none |

The historical five-pair publication recorded `STOP`. That publication remains
unchanged as provenance, but it is not the current result. The correction has
only four complete pairs, so the predeclared gate cannot decide. It provides no
basis for further external execution.

The complete campaign used one externally sourced task (`3d-scan-calc`),
BenchFlow 0.6.5, OpenCode 1.18.5, and `openai/gpt-5.6-luna` with variant `high`.
The exact evaluator, task, runtime, adapter, KTX, execution, and digest identities
are in the [direct identity lock](../../tools/bench/external-identity/lock.json).

## Claim limits

The corrected result is descriptive evidence for one task and four pairs. It
does not establish:

- official benchmark-suite identity or coverage beyond the retained task;
- general agent skill, causal effects, statistical significance, or broader
  performance;
- provider-native cache behavior, provider price, or billing cost;
- reproducible external execution from tracked files alone; or
- authorization for another evaluator, model, provider, or paid run.

Generated tool behavior changed original output volume between arms. Direct
delivered-byte differences therefore do not isolate within-arm compression.
Missing or inconsistent evidence remains unavailable; it never becomes success
or zero.

## Ownership

| KTX owns                             | The external evaluator owns         |
| ------------------------------------ | ----------------------------------- |
| Native task and scoring contracts    | Agent execution                     |
| KTX databases and collected evidence | Authentication                      |
| Deterministic summaries              | Python and environment setup        |
| Comparison, validation, and ranking  | Evaluator packages and dependencies |

KTX does not own a general-purpose agent-evaluation harness. Historical external
inputs remain evidence for this campaign only.

## Evidence map

| Evidence                                                                                                                                                                                                                                             | Authority                                                                          |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| [Correction](../../tools/bench/evidence/skillsbench-campaign-errata.json)                                                                                                                                                                            | Current completeness, metrics, directional counts, and gate state                  |
| [Authority inventory](../../tools/bench/evidence/skillsbench-authority-inventory.json)                                                                                                                                                               | Ownership, retention classes, precedence, and maintained-check boundary            |
| [Archive index](../../tools/bench/evidence/skillsbench-oauth-campaign-archive.json)                                                                                                                                                                  | Raw campaign identities, locations, topology, dependency facts, and limitations    |
| [Raw digest manifest](../../tools/bench/evidence/skillsbench-oauth-campaign-artifacts.sha256)                                                                                                                                                        | SHA-256 binding for 927 ignored raw files; it cannot restore them                  |
| [Final index](../../tools/bench/evidence/skillsbench-final-index.json), [five-pair gate](../../tools/bench/evidence/skillsbench-five-pair-gate.json), and [diversity disposition](../../tools/bench/evidence/skillsbench-diversity-disposition.json) | Immutable historical publications superseded by the correction for current claims  |
| [Direct identity lock](../../tools/bench/external-identity/lock.json)                                                                                                                                                                                | Exact historical identities and bounded claim limits                               |
| [Archive manifest](../../tools/bench/archive/skillsbench-campaign/manifest.json)                                                                                                                                                                     | Former paths, archived paths, and unchanged digests for inert historical machinery |
| [Sanitized proof derivative](../../tools/bench/evidence/skillsbench-proof-derivative.json)                                                                                                                                                           | Tracked projection of bounded proof facts; not raw or metric authority             |

## Archive boundary

The raw campaign archive contains ten campaign roots, 927 regular files, 457
directories, and 20 KTX databases. It is Git-ignored and checkout-local. The
tracked index and digest manifest identify its bytes but cannot reconstruct
missing raw files or external execution.

Historical publications and proof snapshots remain byte-for-byte unchanged.
Some snapshots retain bounded machine-local paths and temporary roots. The
sanitized derivative excludes those values and links back to the original
checksum manifests; it does not replace them.

Archived source and scripts use inert `.txt` suffixes. Manual recovery is an
archival operation only: it requires a disposable checkout, restoration to the
former paths in the archive manifest, and the exact ignored inputs required by
the procedure, up to all 927 raw files. See the
[archive README](../../tools/bench/archive/skillsbench-campaign/README.md) for that
boundary. Routine repository verification uses the direct tracked identity
check instead.
