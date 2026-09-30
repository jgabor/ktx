# Normalization conclusion

**User-approved choice: no normalizer now; retain ordinary instruction bytes and
tool definitions unchanged.** The campaign does not justify read-description
deduplication. The bounded choice is recorded in project memory and the matching
architectural discussion is closed. No production normalizer is adopted.

## Authorities and scope

See the [audited campaign report](normalization-campaign.md) and
[structured receipt](normalization-campaign-results.json) for results, the
[frozen protocol, revision 2](normalization-experiment-protocol.md) for gates,
[offline observations](normalization-offline-gates.md) for runtime controls, and
[feasibility source boundaries](normalization-feasibility.md#pinned-application-boundaries)
for instruction assembly and downstream schema limits. The
[model registration](normalization-model-registration.json) and receipt manifest
scope measurement to OpenCode 1.18.5 with `openai/gpt-6-luna`, variant `high`.
Launch provenance is not independent provider identity telemetry.

## Findings by transformation

| Transformation                    | Evidence and recommendation                                                                                                                                                                                                               |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Read-description deduplication    | Only the three frozen bullets were removed, retaining parameter wording at the hook. **Do not adopt this candidate:** full quality and directional cost gates failed. This does not rule out every description transformation.            |
| General instruction normalization | No worthwhile general candidate was demonstrated. The disabled-tools example is a synthetic control, not equivalent instruction semantics. **Defer; keep instructions unchanged.** Preservation and economic benefit remain inconclusive. |
| Schema ordering/canonicalization  | The runtime owns later schema conversion and tool ordering; provider-final bytes are unavailable. **Defer; keep definitions unchanged.** No qualified candidate or savings case exists.                                                   |

## Quality and accounting

All **12 pairs / 24 attempts** completed with valid evidence and complete
compatible estimated-cost coverage. Evidence validity is not quality success:
each arm passed **8/12**, combined **16/24**, against the frozen **24/24**
requirement. Every window attempt scored zero in both arms: answers included
`2000: a-2000` (or `2000: b-2000`) rather than the exact unprefixed keys.
The actual read correctly returned the 2000-line default window. No
candidate-only regression was established. Do not rescore, relax the oracle,
exclude window cases, or pool only passing attempts to claim a winner.

Estimated USD, including failed-quality attempts:

| Baseline | Candidate | Total    | Candidate change | Cheaper pairs |
| -------- | --------- | -------- | ---------------- | ------------- |
| 0.016436 | 0.017693  | 0.034129 | +7.647846%       | 6/12          |

This fails both cost gates: at least 1% aggregate savings and 9/12 cheaper pairs.
All five token fields were measured on every one of 72 model steps;
runtime-reported cost was zero on 72/72 steps, **not proof of free usage**.
Prices come from pinned models.dev data, not invoices or guaranteed fees.
The USD 1 estimated-spend stop was operational, not a hard billing cap.

Keep qualification attempts separate: two failed with unknown cost;
two succeeded with estimated USD 0.001165. Campaign plus known qualification
estimates total USD 0.035294, not a complete all-work charge. Paid campaign time
was **567.983 seconds**, within four hours; offline build duration was unmeasured.
No break-even is demonstrated, including implementation or maintenance cost.
First/repeated use is observed session order, not cold/warm provider cache.
No provider-prefix, causality or significance claim follows.
Confidence is high that evidence is insufficient for adoption, low for
generalized claims of harm.

## Benchmark machinery, not product adoption

[Benchmark documentation](pipelines.md#canonical-benchmark-workflow) describes
retained model registration, opt-in pinned read-hook treatment/provenance,
same-session follow-ups, whole-attempt preservation scoring, actual first-arm
ordering, and complete-coverage estimated-spend stops with retained partial
evidence. Ordinary adapter use does not apply the treatment.

Revision 2's update gate demonstrated fail-closed changed read-hook ingress
using the actual pinned tool and synthetic offline model. Upstream source-file
reload remains untested; this is not a production update guarantee or security
boundary. Production ownership and update/invalidation requirements remain
unresolved, so any future adoption is conditional on qualifying them.

Passing `mage check`, runtime gate and preflight/smoke receipts are reused through
the authorities, not rerun for this docs-only synthesis. Evidence remains unchanged.

## Approved user choice

**No normalizer now: keep ordinary instructions/tool definitions unchanged,
do not adopt the tested deduplication, defer unqualified instruction/schema
transforms, retain benchmark tools/evidence, and close the architectural question
on that bounded basis without claiming every transform is ruled out.**

The user approved this choice and discussion closure on 2026-09-30. This is
approval of bounded non-adoption, not confirmation of final decision satisfaction
or authority for production rollout. The runtime continues to own current schema
ordering and serialization; do not add a KTX canonicalizer without concrete
evidence. Revisit only with concrete input overhead, a qualified runtime/update
boundary, and a correctness-gated whole-task cost study. No further paid campaign
or speculative backlog is proposed.
