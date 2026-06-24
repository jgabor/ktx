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

**Verified 2026-06-20** by tasks 1-6 (D12 methodology, bench_runs migration, ktx bench run, ktx bench report, M3 acceptance verification). Headline 0.000825 USD/correct for the methodology-pinned (opencode, claude-sonnet-4-20250514, addition_v1) triple; reproducible across two independent invocations. Pre-M3 databases self-heal (bench_runs created with all 10 columns on first M3-binary open). M2 regression preserved. Full verification log: `.agentera/verification/m3-acceptance.md`.

## M4 — Reversible compression

Compressed outputs are always reversible to the original on demand.

Done when: a compressed output can be retrieved as the original bytes via the public surface.

**Verified 2026-06-24** by tasks 1-7 (compressed table migration, db persistence layer, pipeline runner + 5 built-in stages, hand-rolled TOML config loader, ktx compress + ktx decompress verbs, M4 acceptance verification). Identity path (no ktx.toml) verified end-to-end; configured-stage path (`head:5`) proves compress(source) != source AND decompress(compress(source)) == source verbatim. D14 conservative-on-failure invariants (empty-output guard, stage-error passthrough incl. panic recovery, unknown-stage skip) runtime-enforced by the pipeline runner. `ktx hook` bidirectional transform deferred to M5 (D14 explicit deferral); explicit-verb path alone satisfies the ROADMAP M4 acceptance criterion. M3 regression preserved (additive schema + verbs; no change to tool_calls/sessions/bench_runs shape or cost path). Full verification log: `.agentera/verification/m4-acceptance.md`.

## M5 — User-extensible compression

User-defined compression behavior is loadable at runtime.

Done when: a user can supply a configuration that extends or replaces the default compression behavior, and the extension takes effect without code changes.
