# Frozen read-description preservation and cost protocol

Frozen 2026-09-28, **before treatment implementation or measured runs**. This
is a bounded experiment design, not a result, production normalizer, or change
to existing benchmark methodology. The earlier two-attempt qualification in
[feasibility](normalization-feasibility.md) and its
[receipt](normalization-qualification-20260928.json) establishes container,
collector and model-launch feasibility only; it is not a treatment pair. Keep
its failed and successful archives unchanged.

## Premeasurement clarification — 2026-09-29, revision 2

The original 2026-09-28 update-gate wording below is retained as history. Its
stronger reading—reloading edited upstream description/schema source files
inside an immutable pinned executable—has **not been satisfied**. Before any
normalization measurements, the authorized clarification narrows that gate to
pinned-source identity, actual-runtime rejection of changed **read
`tool.definition` ingress**, and stale fixture/transform rejection. Upstream
source-file reload remains **untested**, not passed or supported by this gate.
This is an experimental evidence-scope clarification, not a production
architecture or deployment choice.

The executable gate must run the unchanged generated KTX collector plugin in
OpenCode 1.18.5. An earlier test-only hook changes an otherwise retained read
description line or a read parameter's AST description. The actual KTX hook
must reject that changed ingress without valid read treatment evidence. Retain
the observed hook order; a different synthetic tool or a changed mock alone
does not qualify. Pinned
[`registry.ts:305–329`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/tool/registry.ts#L305-L329)
and
[`plugin/index.ts:281–293`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/plugin/index.ts#L281-L293)
establish the sequential hook boundary. This represents source/schema drift
**at that boundary only**, not an upstream edit/reload or provider-final schema
test. A mounted candidate mutation separately tests fixture/transform identity;
an unchanged restart must preserve definition identities.

The default-window oracle must also execute the real registered read tool in
both arms against the exact public corpus below. A separate test-only OpenAI
configuration may use a loopback synthetic Responses transport inside the
disposable pinned container, disconnected after public dependency setup, with
no credentials or external provider requests. It emits typed read calls and
synthetic completions, never fabricated read output. Observe actual arguments,
completed tool results and same-session order. This is deterministic offline
runtime/tool evidence, **not live Luna semantics or qualification**. Local
transport configuration must remain unavailable to paid comparison inputs.

All treatment bytes, six cases, scoring rules, quality/cost thresholds, the
24-attempt paid budget, paid model/runtime constraints and live semantic gates
below are unchanged. The existing typed scorer is in
`internal/experiment/read_preservation.go`, its frozen tasks are
`tools/bench/tasks/read-{config,paged,window}-{first,repeated}.toml`, and
producer/regeneration and broken-control tests remain required alongside the
opt-in `TestReadDescriptionRuntimeBoundary` gate. There is no normalization
measurement or savings conclusion in this clarification.

Premeasurement implementation correction from the real-tool gate: the pinned
read hook records a **pre-delivery unknown** database outcome, while the later
CLI `tool_use` event carries explicit `completed` status. The scorer requires
that terminal success plus exact retained argument/output agreement for each
ordered phase; NULL alone is never success, and an explicit database failure
or missing/failed terminal result rejects the attempt. This fixes the mock
fixture's invented `is_error=false` assumption, not the successful-read rule.
The window corpus and `a-2000` / `b-2000` answer keys need no correction.
See [offline gate observations](normalization-offline-gates.md) for commands,
runtime identities, ordered hook observations, scorer controls and limitations.

## Treatment, authority and limits

Only the **read tool's top-level description** is eligible. Baseline is the
exact UTF-8 file [`normalization-read-original.txt`](normalization-read-original.txt)
(SHA-256 `98ee843341c2dab2227add0019e48d4b2f0f00f9b042b853d1ee52bb34e6363d`);
candidate is [`normalization-read-candidate.txt`](normalization-read-candidate.txt)
(SHA-256 `d4ebb60002e2b98eb8337341e5572548ab283f7e44cad29aa5335f28e18d61a2`).
Both include one terminal LF. Original bytes match pinned OpenCode 1.18.5
[`tool/read.txt`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/tool/read.txt)
at commit `e5cc278dec9294a627a7b05f47ce6a564408c1a2` (byte hash checked
2026-09-28). The candidate deletes **only** these complete lines, in place:

```text
- The filePath parameter should be an absolute path.
- By default, this tool returns up to 2000 lines from the start of the file.
- The offset parameter is the line number to start from (1-indexed).
```

Pinned [`tool/read.ts`](https://github.com/anomalyco/opencode/blob/e5cc278dec9294a627a7b05f47ce6a564408c1a2/packages/opencode/src/tool/read.ts)
has corresponding parameter descriptions for absolute filePath, 1-indexed
offset and default limit 2000. These **must remain identical** in both arms;
so must validators, requiredness, defaults, all other tool descriptions,
tool-output pipeline (`passthrough`), tools enabled, tasks, runtime config and
model settings. The intent is to test whether deleting redundant wording
reduces paid whole-task cost without losing successful read behavior. Source
duplication alone does not establish that the provider sees both copies.
Loss of emphasis, missing downstream parameter wording, and altered tool
choice are plausible regressions. Restore the untouched original on request.

The selected application boundary is the pinned runtime's `tool.definition`
hook, **description field for `read` only**. The hook receives a tool definition
before OpenCode appends runtime descriptions, converts parameters and applies
provider-specific schema transformations; OpenCode later sorts tool names.
Do not canonicalize JSON, sort tools, patch the runtime's source, or claim this
snapshot equals final provider-visible schema. Implementation must prove at
the hook that the baseline/candidate input bytes and resulting `read`
description match the fixtures exactly; other tools and parameter descriptions
must match across arms. Retain bounded synthetic/public-source-only per-attempt
hook ingress/egress evidence, parameter-description identities, source/version,
transform version/config identity and actual application count/identity. Do
not persist assembled private instructions, user context, request payloads or
credentials. Capture the downstream boundary only if a supported,
privacy-safe observation exists; otherwise report **post-hook schema unknown**.
Pinned source plus tested task behavior can support only hook-level application
and observed semantic outcomes, not final-schema/prefix equivalence. If the
hook cannot actually apply or identify the intended change on each arm, stop
before paid measurement.

**Instruction family: no-go.** The synthetic disabled-tools/“Do not use tools”
example is a control of different semantics, not a worthwhile general
instruction rewrite. No instruction treatment, instruction paid arm, or
general normalizer is authorized by this protocol. Reconsider only on a
separately evidenced, named recurring overhead with a safe transformation,
source/precedence and update boundary, and independent semantic preservation
cases; revise the protocol before running it. This leaves the architecture
question open rather than quietly declaring that family tested.

## Frozen task cases and scoring

The next implementation supplies new **synthetic** isolated corpus/task
fixtures; task keys and this document are never mounted in the agent workspace.
All paths below are inside the isolated `/work/tools/bench/corpus/` mount.
Generate bytes deterministically as specified, retain exact resulting bytes
and hashes, and reject drift before measured runs. For each case run the
first-use task and a repeated-use task with the same first request plus a
second read of a distinct file **in the same agent session**. “First” means
first observed read invocation of that session; “repeated” means a later read
invocation in that session, not a provider-cold/warm state. Score each whole
attempt, not just the last answer. Prompts must request the listed answer
format exactly, require use of `read`, forbid file edits and any non-read tool,
and give paths/offsets but **not** answer values. Do not print answer keys
through tool or system material. `read` output is not compressed by either arm.

| Case                       | Mounted inputs and exact request                                                                                                                                                                                                                                                                                         | Full-pass oracle (both phases in repeated task)                                                                                                                                                                                                                                         |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A: config lookup           | `owner.txt`: `owner=iris\n`; `deployment.txt`: `deployment=blue\n`. Read the named absolute path and reply with only its `key=value` line. Repeated phase reads deployment after owner.                                                                                                                                  | Exact `owner=iris`, then `deployment=blue`; successful read of each named file, in order. Absolute paths are provided, but if the runtime resolves a relative argument to the same file, do **not** score that as a semantic failure; record it as tool-argument diagnostic.            |
| B: paged log               | `log-a.txt`: `start\nnoise-a\nnoise-b\nFACT=quartz\nend-a\nend-b\n`; `log-b.txt`: `start\nnoise-c\nnoise-d\nFACT=amber\nend-c\nend-d\n`. Request `offset=4, limit=2` on each named absolute path, return only the FACT line.                                                                                             | Exact `FACT=quartz`, then `FACT=amber`, with successful `read` calls using the specified numeric offset and limit on the respective files; no decoy answer.                                                                                                                             |
| C: default-window boundary | `window-a.txt`, `window-b.txt`: 2001 LF-terminated lines per file; line _n_ is respectively `a-NNNN\n` or `b-NNNN\n` with decimal _n_ padded to four digits (0001–2001). Ask for the **last numbered line returned by a single default read** of the named absolute path; explicitly omit limit and do not page further. | Exact `a-2000`, then `b-2000`; one successful `read` per file with no `limit` or `offset`, and no second page. If pinned runtime truncation yields a different observable last line, fail offline fixture preflight and revise before paying, rather than silently changing the oracle. |

“Repeated phase” is a separately prompted follow-up in the _same_ session
after the first response, not a freshly launched attempt. It reuses the same
schema but reads a new file; score both phase responses, tool calls, errors,
order and forbidden behavior. The six contracts (A/B/C × first/repeated) have
equal weight. A full pass needs exact phase answers, successful required reads,
no forbidden non-read tools, no extra read of another file, no edit, and no
collector or process failure. Parse typed tool-call arguments and retained
responses, not model prose claiming a tool was used. An answer-only exact-match
score is insufficient. A harmless relative spelling resolved to the requested
absolute path is not itself a failure in A/C; incorrect target or result is.
If a requested tool call is missing or unobservable, score failure/unknown,
never assume compliance. Keep answer-only score as a diagnostic.

Original revision-1 gate (the update-path claim is qualified by revision 2 above):

Before paid runs, offline tests must pass the exact fixtures and score a
deliberately wrong fact, omitted/mis-offset read, forbidden tool/edit and
missing second phase as failures. Verify that the candidate removes precisely
the three lines and that baseline is identical to the source; inspect hook
observations for both arms. Exercise source update (alter an otherwise retained
description line), schema update (alter a parameter-description fixture), and
transform update (change removal set/version): refreshed hook ingress/egress
must change or the comparison must reject stale identities. A source or schema
change for which there is no supported runtime input/update path is **unsupported**,
not “passed” by changing a mock alone; block any claim about that update path
and revise before a preservation/savings recommendation. Demonstrate an
unchanged restart has stable identities. These are offline and do not authorize
paid hook probes. Live scoring is still required for model semantics.

## Paid schedule and decision rule

Run only after the offline gates, identical arm setup and retained-evidence
validation pass. All paid attempts use `Dockerfile.benchmark`, OpenCode 1.18.5,
`--model openai/gpt-6-luna --variant high`, the retained
[`normalization-model-registration.json`](normalization-model-registration.json),
the same pinned compatible price snapshot and `--cache-control observed`.
Use isolated attempt owners, read-only auth mount, fresh HOME/XDG roots,
unset `KTX_DB`, and a new archive ID. Build the local binary before invoking
changed comparison code. No model/runtime fallback, uncontrolled request,
warmup, benchmark replacement retry or tool-output pipeline variation.
Record actual binary, image, Dockerfile, runtime/collector, plugin, registration,
price, hook, task and input hashes and launch argv. Model/variant provenance is
explicit launch and registration only: model-step telemetry was null in
qualification, not provider attestation. Keep the old experiments and metric
contracts unchanged; this is an **additive cost-first evaluation**, not a
reinterpretation of exact-match, prompt-cache or recovery-aware summaries.

Two repetitions of each of six contracts produce **12 paired comparisons / 24
agent attempts maximum**. Within each repetition order A-first, B-first,
C-first, A-repeated, B-repeated, C-repeated; repetition 1 is baseline then
candidate for every case, repetition 2 candidate then baseline. Each attempt
starts a fresh isolated session, with two turns only for repeated tasks.
This balances arm ordering, not cache isolation. No optional additional cases
or replacement of a bad pair. Log all scheduled, started, valid, invalid,
failed and interrupted attempts, by case, arm and order. A valid pair has both
complete owner/collector records, applied fixture proof, full tool/answer
scoring and complete compatible estimated-cost inputs; invalid pairs remain
in the attempted denominator and are never silently dropped. The attempted
denominator is 12 scheduled pairs (24 attempts); report started and valid
counts separately, plus 6 first-use and 6 repeated-use pairs. Retain failures
and their incurred usage/cost, including partial and retry work when visible.
No automatic retries; provider-internal retry count remains unknown if not
observed. Do not label first-use provider-cold or later calls cache-isolated.

Finite operational bounds: at most 24 agent attempts, each
`--attempt-timeout 4m30s --termination-grace 30s` (up to two hours of attempt
windows), four hours wall clock for the paid sequence, and **USD 1 cumulative
pinned-price estimated-spend stop threshold** including any observed failed or
partial work. Check after every attempt; stop before the next attempt once
estimated spend reaches USD 1, a cost field becomes unavailable, or projection
cannot be reconciled. This is not a guaranteed provider invoice cap; hidden
provider requests may incur additional cost. Stop immediately for lost auth,
model/variant or image mismatch, unproven application, collector divergence,
privacy leakage, fixture drift or inability to score; preserve
the partial archive and classify as inconclusive. A wrong answer on a fully
observed attempt is a scored failure, **not** a reason to replace or stop the
schedule. Resume an interrupted sequence only with demonstrably unchanged
inputs and remaining budget, never overwrite an attempt.

Primary whole-task metric is the **sum of pinned-price API-equivalent USD over
all scheduled work per arm**, including failures, retries and any attributable
setup/rebuild API charges; offline build time is reported separately rather
than fabricated as API cost. Compute from every observed model step's input,
output, cache read/write and reasoning usage using the identical retained
model-compatible pricing and context tier; require 24/24 complete estimate
coverage and 12/12 valid, correctness-scored pairs for a positive savings
recommendation. Report partial totals and explicit unpriced work otherwise;
unknown is not zero. Keep runtime-reported USD (including literal zero)
separate, and report latency, input/output/reasoning tokens, cache observations,
tool calls, retries/recovery and setup costs as diagnostics, never as a proxy
for correct-work savings. Report both phase outcomes, per-case paired deltas
and first/repeated strata, aggregate sums, and initial fixed treatment cost
or break-even if it is actually measured; unknown deployment/maintenance cost
means no production break-even claim.

Only a **scoped experimental savings case** is eligible if both arms pass all
24 task attempts, each of the six case/mode groups passes in both repetitions,
complete cost coverage holds, candidate total estimated USD is at least **1%**
below baseline total and at least **9 of 12** pairs are strictly cheaper in
the candidate arm. This is a practical directional gate, not statistical
significance, invoice savings or permission to deploy. If correctness fails,
report regression and the full cost record; if cost gate fails despite intact
quality, report no demonstrated cost case (including a measured tie). Missing
application, update, quality or cost coverage yields **inconclusive**, not
positive savings. Even a positive scoped result remains conditional on a
real product owner/update path and the user's separate architectural decision.

Next work: implement only bounded comparison inputs/provenance, preservation
scorers/update probes, then run this schedule after its gates. If supported
same-session follow-up, source/schema update visibility, or hook evidence is
unavailable, state the precise unsupported boundary and stop/revise rather
than substituting a different test. No private-history capture, provider-final
prefix claim, broad grid or automatic deployment is implied.
