# Post-v3 fact-midfile qualification

## Result

No change. The three runs used 36/48 attempts, 890 seconds of attempt span
(962 seconds wall span), and USD 0.358616 of price-derived estimated spend.
Runtime-reported cost was USD 0. Two archives validated with 5/5 measured
attempts per arm and five complete quality-qualified pairs. The `100:50`
archive had the same measured and pair counts, but validation rejected its
unavailable comparison projection as an invalid target completion.

More importantly, all three archives retained current pricing snapshots rather
than the frozen pricing identity. That drift was detected after the calls. The
runs are retained, but none can support the frozen campaign recommendation.
Defaults and configuration remain unchanged.

Machine-readable detail, including every authority hash and unavailable field,
is in
[`post-v3-midfile-qualification-result.json`](post-v3-midfile-qualification-result.json).

## Comparisons

| Candidate | Valid B/C | Pairs | Correct B/C | Runtime tokens/correct B/C | Delivered bytes B/C | Raw calls B/C | Persisted calls B/C | Coverage B/C | Cues B/C | Followed / recoveries | Validation |
| --------- | --------: | ----: | ----------: | -------------------------: | ------------------: | ------------: | ------------------: | -----------: | -------: | --------------------: | ---------- |
| `100:50`  |       5/5 |     5 |         5/5 |        46,219.4 / 49,833.8 |   502,772 / 480,091 |         17/67 |               14/67 |  82.35%/100% |     0/14 |                 0 / 0 | rejected   |
| `73:1`    |       5/5 |     5 |         5/5 |        52,334.2 / 23,675.0 |   577,612 / 224,250 |         20/80 |               14/80 |     70%/100% |     0/22 |                 0 / 0 | valid      |
| `100:10`  |       5/5 |     5 |         5/5 |        51,477.6 / 28,347.6 |   581,079 / 292,853 |         18/46 |               16/46 |  88.89%/100% |     0/33 |                 0 / 0 | valid      |

Candidate original-byte totals were 823,675, 538,199, and 862,305. Exact
repeat counts were zero for both arms in every archive. All 36 attempts
completed; there were no invalid retries, failures, timeouts, or unlinked
attempts. Raw-only baseline calls were 3, 6, and 2; candidate raw and persisted
calls reconciled exactly.

Baseline cue counts were zero, which means no recovery opportunity—not zero
recovery cost. Candidate cues were delivered, but none was followed by an exact
same-attempt `ktx read`; linked outcome and byte-exactness are therefore
unavailable rather than failed or zero-cost recovery.

## Recommendation

No change. If considered outside the failed frozen campaign, the two complete
rows descriptively favor `73:1` over `100:10` for this task only, but pricing
authority drift prevents that observation from becoming a campaign
recommendation. No provider-internal, causal, significance, or billing claim is
made.
