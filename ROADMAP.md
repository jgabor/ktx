# ktx Roadmap

The shape of the work, in tightly scoped milestones. Architecture and design decisions for each milestone are made in dedicated deliberation (resonera) before the milestone begins; they are not committed to this document.

## M0 — Foundation

VISION.md, ROADMAP.md, and the inspirera research are on disk. No code.

Done when: VISION.md and ROADMAP.md are committed and the inspirera research is captured.

## M1 — First cost-aware invocation

An agent call runs through ktx and ktx records the run.

Done when: an agent invocation produces a run in the record store that includes the agent identity, the input, and the output.

## M2 — Cost publication

Per-call cost is published in a verifiable form.

Done when: any call to ktx can be inspected and the cost is reconstructable from the published data.

## M3 — Cost-per-correct benchmark

A cost-per-correct-answer number is published for at least one agent on at least one task.

Done when: the number is reproducible from the published methodology.

## M4 — Reversible compression

Compressed outputs are always reversible to the original on demand.

Done when: a compressed output can be retrieved as the original bytes via the public surface.

## M5 — User-extensible compression

User-defined compression behavior is loadable at runtime.

Done when: a user can supply a configuration that extends or replaces the default compression behavior, and the extension takes effect without code changes.
