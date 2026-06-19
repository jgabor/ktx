# ktx Roadmap

The shape of the work, in tightly scoped milestones. Architecture and design decisions for each milestone are made in dedicated deliberation (resonera) before the milestone begins; they are not committed to this document.

## M0 — Foundation

VISION.md, ROADMAP.md, and the inspirera research are on disk. No code.

Done when: VISION.md and ROADMAP.md are committed and the inspirera research is captured.

## M1 — First cost-aware invocation

An agent call runs through ktx and ktx records the run.

Done when: an agent invocation produces a record in the database that includes the agent identity, the input, and the output.

**Verified 2026-06-18** by task 8 (ktx doctor + end-to-end acceptance run). Full verification log: `.agentera/verification/m1-acceptance.md`. Wrapper path verified end-to-end with a mock agent emitting OpenCode `--format json` events; adapter path verified by inspecting the installed plugin content and piping its payload shape to `ktx write`. Both paths produce Decision-3-conformant record IDs. Two scope-adjacent fixes applied during verification: `ktx run` now always generates a fresh ktx nanoid for tool_call IDs (previously inherited the runtime's callID, which violated Decision 3), and the OpenCode plugin template now generates 16-char lowercase alpha IDs via `crypto.getRandomValues` (previously used a hex `crypto.randomUUID()` slice, also non-conformant).

## M2 — Cost publication

Per-call cost is published in a verifiable form.

Done when: any call to ktx can be inspected and the cost is reconstructable from the published data.

**Verified 2026-06-19** by tasks 2–6 (schema migration, ktx write persistence, OpenCode event extraction, ktx get/list surface, M2 acceptance verification). Cost reconstructable from published data via `cost = (tokens_in * in_price + tokens_out * out_price) / 1_000_000` against `https://models.dev` (D11). M1 regression preserved (11/11 steps). OpenCode 1.17.8 emits `tokens`/`cost`/`model` only on `step_finish`; the wrapper extracts `latency_ms` and `provider`, sidecar is the live M2 acceptance path for the rest. Full verification log: `.agentera/verification/m2-acceptance.md`.

## M3 — Cost-per-correct benchmark

A cost-per-correct-answer number is published for at least one agent on at least one task.

Done when: the number is reproducible from the published methodology.

## M4 — Reversible compression

Compressed outputs are always reversible to the original on demand.

Done when: a compressed output can be retrieved as the original bytes via the public surface.

## M5 — User-extensible compression

User-defined compression behavior is loadable at runtime.

Done when: a user can supply a configuration that extends or replaces the default compression behavior, and the extension takes effect without code changes.
