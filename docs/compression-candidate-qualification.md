# Compression candidate assessment

This document records the final 2026-08-01 assessment of three deterministic
compression ideas derived from the pi-blackhole research. It is an archival
evidence record, not an active implementation plan.

## Disposition

No candidate was adopted. No stage, route, profile, default, CLI surface, or
product behavior changed.

| Candidate                           |                                                         Measured opportunity | Fidelity result                                                 | Final disposition                                                                                                    |
| ----------------------------------- | ---------------------------------------------------------------------------: | --------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Adjacent duplicate folding          |     920 eligible outputs; 619,006 net bytes; 0.0181217519% of retained bytes | 920/920 byte-exact recoveries; ineligible outputs unchanged     | Not adopted; paired model correctness and counterfactual tool-call equivalence were unavailable                      |
| Common-path-prefix factoring        | 1,175 eligible outputs; 1,399,507 net bytes; 0.0409713616% of retained bytes | 1,175/1,175 byte-exact recoveries; ineligible outputs unchanged | Not adopted; model quality, counterfactual tool-call behavior, and end-to-end latency acceptability were unavailable |
| Additional known-wrapper projection |           Zero raw matches, eligible outputs, or additional stable contracts | No candidate existed to compare                                 | Not broadened; existing strict projection boundaries remain unchanged                                                |

Exact recovery was necessary but insufficient adoption evidence. The two
transform candidates saved bytes, but their corpus-wide impact was small and
the retained-only replay could not establish model or tool-use equivalence.
Path factoring had the strongest byte signal, but not enough evidence to justify
implementation or rollout. Wrapper projection had no additional opportunity.

No post-change study was triggered because no change was adopted or rolled out.

## Evidence boundary

The frozen retained-history qualification covered:

- 98,639 local OpenCode sessions;
- 809,234 selected tool calls; and
- 3,415,817,650 selected output bytes.

The population represents retained local history at one frozen boundary, not a
complete wall-clock observation window. Unknown outcomes remained unknown and
were excluded from success-only eligibility. No provider was invoked, no raw
tool output was emitted into the tracked evidence, and the rejected prospective
capture contributed no evidence.

The private corpus and purpose-built capture, extraction, recognition, and
comparison executables are intentionally not retained in the repository. The
JSON files below are frozen aggregate receipts, not a rerunnable workflow. Their
tool and product-surface hashes describe the assessment snapshot and must not be
treated as proof about the current tree.

## Retained evidence

| Artifact                                                                                                       | SHA-256                                                            | Purpose                                                                                     |
| -------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ | ------------------------------------------------------------------------------------------- |
| [`compression-candidate-qualification.json`](../tools/bench/evidence/compression-candidate-qualification.json) | `6e9830154d786bf552d04d111d383c3ae0567b36b1d8879d21b95f188b439d05` | Frozen population, coverage, outcomes, and initial candidate qualification                  |
| [`duplicate-folding-comparison.json`](../tools/bench/evidence/duplicate-folding-comparison.json)               | `556bb70deb1d881ae6998a2b7ac1f34c834db45237503e8c6986ec5367ebdf22` | Opportunity, exact recovery, passthrough, encoder cost, and non-adoption result             |
| [`common-path-prefix-comparison.json`](../tools/bench/evidence/common-path-prefix-comparison.json)             | `f1301fbcbd75f9447d25e7a8fd39ead8fa0fd1a6f04e7a31e86bcd36fa33ffce` | Strict path recognition, exact recovery, passthrough, encoder cost, and non-adoption result |
| [`wrapper-projection-assessment.json`](../tools/bench/evidence/wrapper-projection-assessment.json)             | `3b3ffcc2ac35f0c5a40e1be3e1fdc84617ad6dfd9fb1992774b0875662c18bb1` | Bounded contract search and no-broadening result                                            |

Each candidate receipt references the qualification aggregate SHA-256. The
tracked receipts contain aggregate counts and provenance only.

## Revisit conditions

- Reconsider duplicate folding only if a narrow, high-volume route shows
  materially greater concentration and a paired model treatment proves task and
  tool-call equivalence.
- Reconsider path factoring only with route-specific savings plus paired model,
  tool-call, and end-to-end latency evidence.
- Reconsider wrapper projection only when a new producer exposes a documented,
  versioned, stable envelope with representative retained examples.

Until those conditions hold, the evidence favors non-adoption rather than more
assessment work.
