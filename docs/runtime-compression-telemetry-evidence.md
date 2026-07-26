# Retained runtime compression telemetry

This report assesses the historical ten-attempt experiment
`.bench-data/runtime-telemetry-v5-luna-high-20260725-a0c268a`. It is an offline
regeneration of retained OpenCode 1.18.0 evidence, not a new benchmark run. No
model or provider call was made during this assessment.

## Evidence status

`available` means the named retained authority contains a usable observation.
An explicit numeric zero is available evidence, not an unavailable value.
`unavailable` means the runtime/configuration contract does not expose the
observation. `malformed` means a value is present but unusable. `missing` means
an expected field or artifact is absent. These states are not interchangeable.

| Observation or evidence family                                                                                                                        | Classification | Retained provenance and limit                                                                                                                                          |
| ----------------------------------------------------------------------------------------------------------------------------------------------------- | -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Experiment, attempt, pipeline, cohort, and task identity                                                                                              | available      | `manifest.json` and the first orchestration records in `attempts.jsonl`; ten ordered attempts, including three measured attempts per side                              |
| Runtime/configuration identity                                                                                                                        | available      | Manifest and attempt cache-control context: configured `openai/gpt-5.6-luna`, variant `high`, OpenCode `1.18.0`, observed mode, and per-pipeline configuration SHA-256 |
| Runtime-native session, message, part, and event order                                                                                                | available      | Raw `attempts/*/stream.jsonl` events and linked session evidence; this establishes runtime-local identity and event order only                                         |
| Provider/model identity emitted on each model step                                                                                                    | missing        | All 20 normalized `model_steps[].identity` objects are empty. Configured identity remains available at manifest/attempt scope and is not backfilled into source events |
| Uncached input, output, reasoning, cache read, cache write, and reported cost                                                                         | available      | All 20 raw `step_finish` events and normalized model steps contain measured values. All cache-write and reported-cost values are explicit zero                         |
| Attempt totals for runtime usage/cache/cost                                                                                                           | available      | Raw stream fields are complete for every contributing model step, so all ten attempt totals are usable                                                                 |
| Tool-output delivery bytes                                                                                                                            | available      | First retained tool-call source rows and first compressed artifacts in `attempts/*/ktx.db`; ten of ten observations are measured                                       |
| Task quality                                                                                                                                          | available      | Retained runtime output scored against `manifest.json#task`; ten of ten attempts score 1 and pass                                                                      |
| Price-derived cost estimate                                                                                                                           | available      | Complete retained runtime usage plus `pricing.json` and its manifest SHA-256; ten of ten estimates are usable                                                          |
| Submitted benchmark prompt and launch order                                                                                                           | available      | Exact sanitized task configuration in `manifest.json` and attempt `cache_control.execution_order`; neither describes a provider-visible composed prefix                |
| Instruction segment, tool-schema segmentation, compaction/truncation events, and before/after context counts                                          | unavailable    | The retained runtime events expose none of these segment or mutation observations; token movement is not substituted                                                   |
| Reliable request start and time to first token                                                                                                        | unavailable    | `manifest.json#cache_control` records that retained stream timestamps do not provide a reliable request start                                                          |
| Cache adapter source/version and isolation controls                                                                                                   | unavailable    | Observed mode records source as `unavailable`; runtime version is not relabeled as adapter version                                                                     |
| Provider request count/boundaries, cache hits/misses/fills/eviction, eligibility, prefix composition, isolation, and billing meaning of reported zero | unavailable    | Not exposed by runtime-only evidence. Configured identity, prices, token movement, and runtime session continuity do not establish these fields                        |
| Causal share of compression in runtime or estimated-cost movement                                                                                     | unavailable    | Delivery and runtime movement are correlated observations; the experiment does not identify causal shares                                                              |

Contractual unavailability is a retained result. It does not make this evidence
insufficient and does not trigger collection. Validation found no malformed
contributing field and no missing authority behind an accepted claim. If either
were present, the affected claim would be withheld rather than reconstructed;
the one observed missing family, source-event provider/model identity, supports
no source-event identity claim.

## Measured comparison

The comparison keeps delivery, runtime usage/cache, quality, runtime-reported
cost, and estimated cost parallel and non-additive.

| Evidence class                                              |   Baseline |  Candidate | Baseline minus candidate |
| ----------------------------------------------------------- | ---------: | ---------: | -----------------------: |
| Delivered bytes per measured attempt                        |     50,118 |      1,819 |          48,299 (96.37%) |
| Mean measured uncached input tokens                         | 21,277.667 |  3,782.667 |                   17,495 |
| Mean measured cache-read tokens                             |     11,264 |  9,386.667 |                1,877.333 |
| Mean measured cache-write tokens                            |          0 |          0 |                        0 |
| Mean measured logical input (`uncached input + cache read`) | 32,541.667 | 13,169.333 |               19,372.333 |
| Measured full passes                                        |        3/3 |        3/3 |                        0 |
| Mean runtime-reported cost                                  |          0 |          0 |                        0 |
| Mean measured price-derived cost                            |  $0.025400 |  $0.007413 |                $0.017987 |

Runtime-reported zero cost is literal telemetry and has no asserted billing
meaning. Price-derived cost is an estimate. Cold-observation plus warm-up
estimates are separately $0.055148 baseline and $0.013600 candidate. The full
ten-attempt estimate is $0.167186; it is budget context, not an additional
savings component. The derived first-measured-turn cumulative comparison is
$0.081830 baseline versus $0.018916 candidate.

## Offline verification

The local `build/ktx` binary reported version `dev`. Two successive
`bench compare --summarize` invocations, run with no model configured and an
intentionally nonexistent OpenCode auth path, produced bytes identical to each
other and to retained `summary.json`:

```text
sha256  93d6262b6f976cd6535aef45b95ef51589c90b2793e7583a88c3b83b1b162c80
```

`bench validate` reported `valid=true`, `summary_byte_match=true`,
`target_met=true`, no methodology reasons, no projection divergences, all ten
attempt checks valid, and measured valid counts of baseline 3 and candidate 3.
The checked-in focused mutation tests additionally require missing authority,
malformed fields, and contradictory duplicate authority to withhold the
affected aggregate and report the evidence gap.
