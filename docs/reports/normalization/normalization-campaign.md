# Read-description paid campaign

## Measured campaign, 2026-09-30

The frozen revision-2 schedule finished: **12/12 pairs, 24/24 attempts**, six
first-use and six repeated-use pairs. Repetition 1 ran baseline first;
repetition 2 ran candidate first, with unchanged arm labels. No warmups,
replacement attempts, extra controls, or paid setup calls were made. All
processes completed. The earlier premeasurement stop is preserved below as
history, not the current producer state.

**Quality gate failed in both arms. No savings case was demonstrated.** Each
arm passed 8/12 whole attempts; config and paged cases passed every phase.
All window cases returned `2000: a-2000` (and `2000: b-2000` on the repeated
phase), rather than the frozen exact answers `a-2000` / `b-2000`. The registered
read itself succeeded, omitted offset/limit, returned exactly 2000 numbered
lines, and had the correct last value. This is a shared answer-format failure,
not evidence of a candidate-only tool-behavior regression. The scorer and answer
keys were not changed. Whole-attempt passes: 16/24; phase passes: 24/36.

### Pair results

Scores are baseline / candidate, irrespective of execution order. USD values
are pinned-price estimates summed over the whole attempt, including both turns
for repeated tasks. They are not provider invoices.

| Pair | Repetition | Case       | First arm | Scores | Baseline USD | Candidate USD |
| ---- | ---------- | ---------- | --------- | ------ | ------------ | ------------- |
| 1    | 1          | A first    | baseline  | 1 / 1  | 0.001072     | 0.000693      |
| 2    | 1          | B first    | baseline  | 1 / 1  | 0.000573     | 0.000702      |
| 3    | 1          | C first    | baseline  | 0 / 0  | 0.002070     | 0.002061      |
| 4    | 1          | A repeated | baseline  | 1 / 1  | 0.000787     | 0.000399      |
| 5    | 1          | B repeated | baseline  | 1 / 1  | 0.000440     | 0.000789      |
| 6    | 1          | C repeated | baseline  | 0 / 0  | 0.004145     | 0.004203      |
| 7    | 2          | A first    | candidate | 1 / 1  | 0.000703     | 0.000555      |
| 8    | 2          | B first    | candidate | 1 / 1  | 0.000204     | 0.000563      |
| 9    | 2          | C first    | candidate | 0 / 0  | 0.002086     | 0.002199      |
| 10   | 2          | A repeated | candidate | 1 / 1  | 0.000925     | 0.000768      |
| 11   | 2          | B repeated | candidate | 1 / 1  | 0.000947     | 0.000420      |
| 12   | 2          | C repeated | candidate | 0 / 0  | 0.002484     | 0.004341      |

Estimated totals: baseline **USD 0.016436**, candidate **USD 0.017693**;
campaign **USD 0.034129**, below the USD 1 operational stop threshold.
Candidate was 7.65% more expensive and strictly cheaper in 6/12 pairs, failing
both frozen cost conditions (at least 1% cheaper overall and 9/12 cheaper
pairs). Failed-quality attempts are included in these totals, not discarded.
All 24 attempts have complete compatible estimated-cost coverage; all five
token fields on every model step are measured, non-null and nonnegative.
Runtime-reported cost is separately **USD 0**, observed in all attempts. Zero
does not mean free. Per-attempt estimates retain the producer's six-decimal
precision.

| Stratum      | Pairs | Baseline estimated USD | Candidate estimated USD | Full-pass attempts |
| ------------ | ----- | ---------------------- | ----------------------- | ------------------ |
| First use    | 6     | 0.006708               | 0.006773                | 8/12               |
| Repeated use | 6     | 0.009728               | 0.010920                | 8/12               |

Retained observations total 72 model steps and 36 completed read calls, with
no extra tool calls. Baseline / candidate token totals: uncached input
137622 / 152200; output 694 / 688; cache read 229376 / 212992; cache write
0 / 0; reasoning 64 / 0. These are runtime observations, not provider-internal
cache or reasoning claims. Per-step coverage includes reported cost on 72/72
steps. Phase diagnostics and per-attempt durations remain in the receipt.

### Evidence and validation

- All 24 attempt evidence checks passed; all 12 summaries matched regenerated
  authority byte-for-byte, with no projection divergence. All 12 pairs are
  evidence-valid and correctness-scored; only eight pairs meet full quality.
- `bench compare` and `bench validate` returned exit 0 for pairs 1, 2, 4, 5,
  7, 8, 10, 11. Both returned exit 1 for the four window pairs because the
  quality target was unmet. Their individual attempt checks remain valid.
  `bench list` and `bench show` returned exit 0 after every pair.
- No summary was rewritten. No evidence failure or operational stop occurred.
  The producer checked retained usage/cost authority between arms; each next
  pair received `--estimated-cost-limit` equal to USD 1 minus the complete
  cumulative estimates of prior pairs. Quality failures alone did not stop or
  trigger replacement runs.
- Paid sequence: **09:48:19.938953–09:57:47.922160 UTC**, 2026-09-30,
  **567.98 seconds**, within the four-hour limit. Each attempt used a 270-second
  timeout and 30-second termination grace. Offline image-build wall time was
  not separately measured; it incurred no model API calls.

Postpublication checks passed:
`python /tmp/opencode/verify-normalization-campaign.py` verified all retained
artifact hashes, frozen inputs, schedule/order, complete measured model-step
usage, scorer counts, window arguments/output boundary, and a privacy-key
allowlist check on the sanitized receipt. `git diff --check` passed. No
unchanged full `mage check` was rerun. No new regression test was needed because
this step changed no product code or scoring contract.

The sanitized [structured receipt](normalization-campaign-results.json) contains
the frozen manifest, all twelve experiment IDs, actual command arrays and exit
codes, invocation times/environment roots, per-attempt hashes, ordered phase
scores, bounded synthetic answer/tool diagnostics, model-step token/cache/cost
observations, and paired deltas. Raw sensitive archives remain intact under
`.ktx/experiments/`, using IDs with prefix
`read-normalization-20260930t094819z-`; they are ignored local evidence, not
published data. Local invocation logs and the original frozen manifest are in
`/tmp/opencode/normalization-campaign/`. No credentials or raw server logs are
published.

### Provenance and commands

The local binary matched the supplied audited build, so no redundant build or
full test-suite invocation was needed. Full-suite authority remains the supplied
`/tmp/opencode/producer-controls/cost-step-mage-check-final.log` exit-0 receipt;
that result was not rerun or independently reestablished here.

Before measurement, the local binary's `bench compare --preflight --build`
passed with the complete treatment inputs and baseline-first controls:
`eligible=true`, zero model attempts, no experiment artifact, OpenCode 1.18.5,
collector `opencode-event-v3`. Retained preflight files:
`/tmp/opencode/ktx-campaign-preflight.UmazAG/preflight.{json,log}`.
Current help documented both new producer controls. The immutable image was
selected explicitly for every paid invocation.

| Identity                                          | SHA-256 or value                                                          |
| ------------------------------------------------- | ------------------------------------------------------------------------- |
| Frozen premeasurement manifest                    | `be4f94e0f6fc879236798ec25d30f32dc91d4087fa7e87b868d1df2094d7ba0f`        |
| Local driver binary                               | `a979185e86b25a3e3320b326e469413d82fc921b490fa7d91a04184f79bc71aa`        |
| Immutable benchmark image                         | `sha256:91cb49ca84d023682b13018c722c5c5e76a3ba0d95751f7acb7574e441edc50c` |
| Image-built binary (retained attempt attestation) | `bf71e8887f35554d032aaf3966c6b429cdcd4cd6ba72753530e3bd27ad667806`        |
| Generated collector                               | `b45cafae09b084aefe6fd9d0d749e21e2fd177252bad40040021f6b12c09ff46`        |
| Shared registration                               | `7d6e7ec5f4ad84f017793cd80c41fa707fe3e72efbf8702fb053ae12f7f5d827`        |
| Pinned pricing                                    | `99fbdcbf38233c00f574dd03469849914414babf654aee3a49d3776c9fa6c6cf`        |

The image builds its own binary, so its byte hash differs from the local driver.
The premeasurement freeze bound the immutable image; the explicit image-binary
hash above comes from retained attempt attestation, not an added paid probe.
All subsequent identities matched. Exact protocol, Dockerfile, task, fixture,
scorer and transform hashes are in the structured receipt. Original/candidate
description and parameter identities were validated from retained hook evidence.
Pricing is the unchanged models.dev snapshot retrieved 2026-09-28T16:17:02Z,
with USD-per-million-token base rates input 0.1, output 0.5, cache read 0.01,
cache write 0.125, and its retained context tier. Both arms use the same captured
registration and price bytes. No mutable price refresh was used.

Each CLI invocation used a new disposable HOME and distinct XDG data/config/
cache/state paths, `TMPDIR=/tmp/opencode`, and `KTX_DB` unset. Comparisons ran
from this checkout with explicit repository archive scope, fresh owned databases,
and the supported `--auth` flag. The producer made its checked private read-only
auth copy; the operator did not read auth bodies or inspect a database directly.

Command template (exact expanded arrays are in the receipt):

```bash
./build/ktx bench compare --spec exact-match \
  --model openai/gpt-6-luna --variant high --task "$case_id" \
  --baseline passthrough --candidate passthrough \
  --runtime-config /tmp/opencode/normalization-campaign/inputs/registration.json \
  --read-description-treatment read-three-bullets-v1 \
  --pricing-snapshot /tmp/opencode/normalization-campaign/inputs/pricing.json \
  --pricing-sha256 99fbdcbf38233c00f574dd03469849914414babf654aee3a49d3776c9fa6c6cf \
  --cache-control observed --target-valid 1 --max-attempts 2 \
  --first-arm "$first_arm" --estimated-cost-limit "$remaining_usd" \
  --attempt-timeout 4m30s --termination-grace 30s \
  --image sha256:91cb49ca84d023682b13018c722c5c5e76a3ba0d95751f7acb7574e441edc50c \
  --experiment-id "$unique_id" \
  --auth ~/.local/share/opencode/auth.json --format json
./build/ktx bench list --format json
./build/ktx bench show --format json "$unique_id"
./build/ktx bench validate --format json "$unique_id"
```

### Boundaries

The four historical qualification attempts remain separate: the successful
pair's retained estimate is USD 0.001165, while the earlier two failed attempts'
costs remain unknown. Campaign plus known qualification estimates total USD
0.035294, **not** a complete all-work or provider-billing total. Hidden provider
work and retries remain unknown. No visible retry events occurred in retained
turn streams; repeated tasks used their prescribed same-session follow-up,
not a retry or recovery call.

These results establish hook application and observed runtime/tool semantics,
not provider-final schema, provider identity attestation or cache isolation.
First/repeated use does not mean provider-cold/warm. Upstream source-file reload
remains untested under revision 2. No product code, protocol, objective,
protected project state, or prior archive was changed. This is the measurement
receipt, not an architectural choice, deployment approval, or lifecycle update.

## Historical premeasurement stop, 2026-09-29

2026-09-29. No campaign attempt has been launched. The frozen revision-2
[protocol](normalization-experiment-protocol.md) schedules 12 pairs / 24
attempts: six cases in the prescribed order for repetition 1 (baseline first),
then the same six for repetition 2 (candidate first). Started: 0 pairs / 0
attempts; valid: 0 pairs. No quality score, estimated or reported campaign API
cost, cache observation, or savings result exists. The earlier qualification
archives remain separate and do not count toward this campaign.

## Producer gap before paid execution

The current `bench compare` treatment accepts only `--target-valid 1
--max-attempts 2`. Every new experiment starts its first cycle baseline then
candidate (`internal/experiment/runner.go`); candidate-first is available only
in an even cycle, which this treatment cannot reach. Twelve distinct experiment
IDs therefore cannot execute the second repetition's required candidate-first
order. Swapping pipeline labels or treatment arms would change the frozen
comparison, not solve the gap.

The protocol also requires checking cumulative pinned-price estimated spend and
estimate coverage **after every attempt**, stopping before the next if the USD 1
threshold is reached or cost is unavailable. One synchronous `bench compare`
invocation starts both arms without an operator checkpoint. It estimates an
attempt locally, but the CLI does not expose an inter-arm budget stop or a
resumable one-arm invocation. Post-pair `show`/`validate` cannot retroactively
stop a second arm. The four-hour wall-clock and 24-attempt maxima can be
enforced by the operator, but they do not repair these two boundaries.

The minimal correction is an owned producer interface for the frozen
candidate-first pair and a fail-closed estimated-cost/coverage checkpoint before
each next attempt (including between arms), preserving the existing arm labels,
per-attempt archives, validation and no-replacement policy. Verify that interface
offline before launching paid work. No selector, task, treatment, pricing, or
protocol change is proposed here. No measurement manifest was frozen because
the required schedule and stop rule are not executable; do not treat this note
as one.

## Checks and scope

- Built the local binary with `mage build` in a fresh disposable HOME/XDG root
  with `KTX_DB` unset. `build/ktx` SHA-256:
  `758a2fb951137693c112fc5947cab095024df901a514c8b7c6297dc9f3144651`.
- Invoked that binary's `bench compare --help` in a separate disposable
  HOME/XDG root with `KTX_DB` unset; all six registered task IDs and
  `exact-match` were listed. Cross-checked actual option parsing and runner
  schedule against `docs/guides/pipelines.md` and the frozen protocol.
- No `--preflight --build` was run, so a current benchmark image, collector,
  runtime, auth, pricing and model identity have **not** been reattested for
  measurement. No provider request or archive was created by this check.
  Earlier audited offline and qualification results are not new campaign data.

Status: blocked before paid measurement, not a failed semantic comparison or a
cost result. Resume only after the producer gap is corrected and the required
build/preflight, manifest freeze and identity checks pass.
