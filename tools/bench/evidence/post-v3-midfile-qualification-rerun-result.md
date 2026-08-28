# Post-v3 fact-midfile qualification rerun

## Result

The pinned-pricing rerun used exactly 36 provider attempts, 814 seconds of
attempt span (859 seconds wall span), and USD 0.321562 of pinned price-derived
estimated spend. Runtime-reported cost was USD 0. Every archive retained the
same pricing bytes and SHA-256
`585b11de38f7fa5a7797f003f6b54ba7ced2c5292d7c8a0fcd6bedda7538df13`.

`100:50` and `100:10` each reached 5/5 valid measured attempts per arm, five
quality-qualified same-cycle pairs, complete runtime totals, and byte-exact
validation. `73:1` retained all 12 attempts but one baseline attempt had
divergent wrapper/event-hook model-step counts. It therefore has 4/5 baseline
valid attempts, four complete pairs, and no recommendation eligibility.

The prior failed campaign remains unchanged in
[`post-v3-midfile-qualification-result.json`](post-v3-midfile-qualification-result.json).
Full rerun authority and unavailable fields are in
[`post-v3-midfile-qualification-rerun-result.json`](post-v3-midfile-qualification-rerun-result.json).

## Accepted and partial rows

| Candidate | Valid B/C | Pairs | Correct B/C | Runtime tokens/correct B/C | Delivered bytes B/C | Raw calls B/C | Persisted calls B/C | Coverage B/C | Cues B/C | Followed / linked recovery | Validation   |
| --------- | --------: | ----: | ----------: | -------------------------: | ------------------: | ------------: | ------------------: | -----------: | -------: | -------------------------: | ------------ |
| `100:50`  |       5/5 |     5 |         5/5 |        53,902.6 / 36,594.4 |   502,551 / 379,117 |         14/53 |               13/53 |  92.86%/100% |     0/12 |                      0 / 0 | accepted     |
| `73:1`    |       4/5 |     4 |         4/5 |     unavailable / 14,976.6 |   503,119 / 128,265 |         18/34 |               15/34 |  83.33%/100% |     0/26 |                      0 / 0 | target-unmet |
| `100:10`  |       5/5 |     5 |         5/5 |        49,921.4 / 15,510.0 |   566,195 / 158,996 |         20/26 |               15/26 |     75%/100% |     0/21 |                      0 / 0 | accepted     |

Candidate original-byte totals were 732,327, 500,677, and 719,167. Raw-only
baseline calls were 1, 3, and 5; candidate raw and persisted calls reconciled.
Persisted-only calls and exact repeats were zero throughout. All 36 processes
completed; the single invalid evidence attempt was retained and no replacement
or retry was run.

Baseline cue counts were zero: there was no recovery opportunity. Candidate
cues were measured, but none was followed. Follow count is therefore measured
zero while linked recovery outcome and byte-exactness remain unavailable—not
zero-cost recovery.

## Recommendation

For `fact-midfile-v1` only, prefer `keep-ends:100:10` over the other accepted
row. Its candidate used 15,510 runtime tokens per correct task versus 36,594.4
for `100:50`; both passed quality and byte-exact archive validation. The lower
candidate value for `73:1` cannot rank because its baseline and pair targets
were unmet. No default or configuration changed. No provider-internal, causal,
significance, or billing claim is made.
