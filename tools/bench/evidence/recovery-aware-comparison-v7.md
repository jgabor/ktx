# Recovery-aware configuration comparison, methodology v7

## Result

No change. The campaign stayed within all ceilings, but only four of nine
comparisons reached five complete pairs and validated byte-exactly. The
remaining five archives are retained as partial evidence; their schema-6
summaries omit `recovery_aware_comparison`, so the validator rejects them.
The audit correction run did not reach a complete pair. Complete matrix and
recovery evidence are therefore unavailable. No default or configuration
changed.

The machine-readable authority for the campaign projection is
[`recovery-aware-comparison-v7.json`](recovery-aware-comparison-v7.json).
Raw experiment archives remain under their experiment IDs and are not copied
into this report.

## Offline gate and shortlist

The benchmark image build and preflight passed with zero model attempts. The
frozen screen used population SHA-256
`986b7a14a0ce5768ea0fe496dfce9e4c03a200c7f68d2460308fb3baa18ae9fd`.
The predeclared rule selected the qualified `100:50` anchor, then the two
highest-saving qualified alternatives:

| Configuration      | Config SHA-256                                                     | Net bytes saved | Controlled facts |
| ------------------ | ------------------------------------------------------------------ | --------------: | ---------------- |
| `keep-ends:100:50` | `bbf0c6277e3360df864daa27383ce913eb03c147dee141a29ec26bc834f69248` |           6,653 | pass             |
| `keep-ends:73:1`   | `8bf31632404adcd20809f7a934732f36f770a1d7affc31a165c68f19b5bd2ab0` |          11,517 | pass             |
| `keep-ends:100:10` | `2ac5f3fafdfcf73240f1eca068ec6ecfc793acd62cceee9690cfada099a44843` |           9,213 | pass             |

`keep-ends:50:10` saved 12,669 bytes but failed the controlled-fact gate and
did not advance. Passthrough saved zero bytes and remained the baseline.

## Accepted comparisons

All accepted rows used methodology SHA-256
`d099eb84c59cb76e45caf83d1d7e0d394cfa924afcdf98ab2342c53bf0d5b6e8`
and passthrough config SHA-256
`c2c96472876aee9fd79522bbe9a26174e9a3a76e7b56135e095fcfbae57625c1`.
Each has 12 actual attempts (one cold observation and five measured attempts
per arm), 5/5 valid measured attempts per arm, five complete pairs, and a
validator result of `valid=true, summary_byte_match=true`.

| Task / candidate     | Correct B/C | Runtime tokens per correct B/C | Delivered bytes B/C | Calls B/C | Recovery B/C | Exact repeats B/C | Estimated USD |
| -------------------- | ----------: | -----------------------------: | ------------------: | --------: | -----------: | ----------------: | ------------: |
| crossfile / `100:50` |         5/5 |           115,306.4 / 21,815.4 | 1,406,771 / 222,594 |     49/46 |  unavailable |               0/0 |      1.730277 |
| crossfile / `73:1`   |         5/5 |           114,334.8 / 12,941.0 | 1,406,819 / 108,065 |     46/50 |  unavailable |               0/0 |      1.525623 |
| crossfile / `100:10` |         5/5 |           114,465.8 / 15,298.2 | 1,406,667 / 161,054 |     48/46 |  unavailable |               0/0 |      1.674126 |
| midfile / `100:50`   |         5/5 |            56,879.0 / 44,984.8 |   652,625 / 482,571 |     15/61 |  unavailable |               0/0 |      0.863057 |

Candidate original-byte totals were respectively 1,406,819; 1,406,875;
1,406,819; and 1,026,460. These archives used the prior required-event
contract, which did not acknowledge benchmark-owned cue delivery. Their zero
cue rows are not recovery measurements; cue exposure, follow, and linked
recovery are unavailable. Runtime-reported cost was USD 0 for every attempt.
Estimated spend uses the retained pricing snapshot and is not a bill.

## Audit correction and bounded qualification

Required benchmark mode now binds recovery evidence to the precreated attempt
and event interval. The generated adapter acknowledges an applied cue in both
explicit-collection and required benchmark modes. Hook handling and later exact
`ktx read <id>` linkage resolve that same owner and fail on mismatch. Required
event contract `opencode-event-v3` marks zero-cue observations as measured;
older archives without that authority materialize recovery as unavailable.
Legacy schema-6 bytes remain byte-exact under validation.

Focused tests cover the generated-adapter process, required-mode delivery and
linked byte-exact recovery, interval-owner mismatch, mounted database paths,
and unavailable old-archive evidence. Repository verification and the rebuilt
offline preflight passed. The rebuilt plugin SHA-256 is
`a2eb6388e6e74543d902d2db56e055227dd0e36781aa5e6db7993e56b0f62959`.

The bounded qualification archive is
`v7-crossfile-73-1-cuefix-20260827`. It attempted two provider runs. The first
is retained as invalid; the second reached the provider but failed required
hook ownership before an attempt record or summary could be finalized. The
failure exposed an over-strict database-target check across the host/container
mount seam; the interval identity and retained owner already provide the
required isolation. That check was corrected and regression-tested. Cue
exposure, follow, recovery, quality, and runtime totals for this archive are
unavailable; it has zero complete pairs and cannot validate without a summary.

Those two attempts consumed the remaining budget down to 10 attempts. A fresh
five-pair comparison requires at least 12, so no second paid run was started.

## Partial comparisons and ceilings

| Archive                        | Valid measured B/C | Attempts | Estimated USD | Status                     |
| ------------------------------ | -----------------: | -------: | ------------: | -------------------------- |
| `v7-midfile-73-1-20260827`     |                4/3 |       12 |      0.703320 | partial, validator rejects |
| `v7-midfile-100-10-20260827`   |                4/4 |       12 |      0.422610 | partial, validator rejects |
| `v7-ephemeral-anchor-20260827` |                5/4 |       12 |      0.164079 | partial, validator rejects |
| `v7-ephemeral-73-1-20260827`   |                2/4 |       12 |      0.161736 | partial, validator rejects |
| `v7-ephemeral-100-10-20260827` |                4/4 |       12 |      0.193388 | partial, validator rejects |

All 108 provider attempts in the initial matrix completed at the process level;
none was recorded as invalid, failed, timed out, or unlinked. Some completed attempts nevertheless
lacked accepted measured evidence. The retained summaries do not provide a
reason, so this report does not infer one. Their quality-qualified runtime,
delivery, recovery, call, and repeat totals are unavailable.

Including correction qualification, the cumulative campaign used 110/120
provider attempts: 108 completed, one invalid, and one lifecycle failure before
an attempt record. It used 1,644 seconds,
reported USD 0 runtime cost, and estimated USD 7.455092 against the USD 25
ceiling. The correction run contributed 623 seconds and USD 0.016876 estimated
spend, including a price-derived USD 0.007942 estimate reconstructed from the
complete runtime fields of the lifecycle-failed run. Elapsed time remained
below six hours.

## Recommendation

Keep current defaults. A separate follow-up may rerun qualification only under
a newly approved attempt budget. Prior recovery evidence is unavailable and
the correction run did not qualify, so no recovery-aware configuration
recommendation is supported.
No p-values, outlier removal, provider-causal claim, or provider-internal claim
is made.
