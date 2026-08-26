# Recovery-aware configuration comparison, methodology v7

## Result

No change. The campaign stayed within all ceilings, but only four of nine
comparisons reached five complete pairs and validated byte-exactly. The
remaining five archives are retained as partial evidence; their schema-6
summaries omit `recovery_aware_comparison`, so the validator rejects them.
Complete matrix quality and runtime totals are therefore unavailable. No
default or configuration changed.

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

| Task / candidate     | Correct B/C | Runtime tokens per correct B/C | Delivered bytes B/C | Calls B/C | Linked recovery B/C | Exact repeats B/C | Estimated USD |
| -------------------- | ----------: | -----------------------------: | ------------------: | --------: | ------------------: | ----------------: | ------------: |
| crossfile / `100:50` |         5/5 |           115,306.4 / 21,815.4 | 1,406,771 / 222,594 |     49/46 |                 0/0 |               0/0 |      1.730277 |
| crossfile / `73:1`   |         5/5 |           114,334.8 / 12,941.0 | 1,406,819 / 108,065 |     46/50 |                 0/0 |               0/0 |      1.525623 |
| crossfile / `100:10` |         5/5 |           114,465.8 / 15,298.2 | 1,406,667 / 161,054 |     48/46 |                 0/0 |               0/0 |      1.674126 |
| midfile / `100:50`   |         5/5 |            56,879.0 / 44,984.8 |   652,625 / 482,571 |     15/61 |                 0/0 |               0/0 |      0.863057 |

Candidate original-byte totals were respectively 1,406,819; 1,406,875;
1,406,819; and 1,026,460. Recovery cue-follow rate is unavailable when no cue
was delivered; it is not zero. Runtime-reported cost was USD 0 for every
attempt. Estimated spend uses the retained pricing snapshot and is not a bill.

## Partial comparisons and ceilings

| Archive                        | Valid measured B/C | Attempts | Estimated USD | Status                     |
| ------------------------------ | -----------------: | -------: | ------------: | -------------------------- |
| `v7-midfile-73-1-20260827`     |                4/3 |       12 |      0.703320 | partial, validator rejects |
| `v7-midfile-100-10-20260827`   |                4/4 |       12 |      0.422610 | partial, validator rejects |
| `v7-ephemeral-anchor-20260827` |                5/4 |       12 |      0.164079 | partial, validator rejects |
| `v7-ephemeral-73-1-20260827`   |                2/4 |       12 |      0.161736 | partial, validator rejects |
| `v7-ephemeral-100-10-20260827` |                4/4 |       12 |      0.193388 | partial, validator rejects |

All 108 provider attempts completed at the process level; none was recorded as
invalid, failed, timed out, or unlinked. Some completed attempts nevertheless
lacked accepted measured evidence. The retained summaries do not provide a
reason, so this report does not infer one. Their quality-qualified runtime,
delivery, recovery, call, and repeat totals are unavailable.

The campaign ran from 22:29:09Z to 22:46:10Z (1,021 seconds), used 108/120
attempts, reported USD 0 runtime cost, and estimated USD 7.438216 against the
USD 25 ceiling. Starting replacement comparisons would have required at least
12 attempts each, so no further paid run could begin without risking the
120-attempt ceiling. Elapsed time remained below six hours.

## Recommendation

Keep current defaults. A separate follow-up may address partial schema-6
validation and rerun only under a newly approved attempt budget. The accepted
rows show qualified savings, but they do not cover every predeclared task and
configuration, so they are insufficient for a configuration recommendation.
No p-values, outlier removal, provider-causal claim, or provider-internal claim
is made.
