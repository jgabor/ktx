# Midfile anchor call-evidence diagnosis

## Classification

The retained `v7-midfile-anchor-20260827` archive was read without modifying
its streams or databases. Native OpenCode session/call tuples pair every
persisted hook row to a raw `tool_use` event.

| Scope                       | Raw events | Persisted rows | Paired | Discrepancies |
| --------------------------- | ---------: | -------------: | -----: | ------------: |
| Baseline measured attempts  |         19 |             15 |     15 |             4 |
| Candidate measured attempts |         61 |             61 |     61 |             0 |

Each baseline discrepancy is a distinct `read` event with an authoritative raw
stream error state, one in each of measured attempts 2–5. No persisted row has
the corresponding native tuple. There are no duplicate raw tuples or unpaired
persisted rows in either measured arm.

The stream is authoritative for those four observed tool errors. A persisted
row is authoritative only for evidence accepted through the hook source. The
retained archive has no callback-delivery log, so it cannot distinguish an
absent `tool.execute.after` callback from rejection before persistence. That
distinction, including any retry history, is unavailable rather than inferred.

## Reproduction and boundary

An isolated hook test covers successful and failed callbacks, an omitted
callback, and exact duplicate delivery. Successful and failed callbacks each
persist one row with their explicit shell outcome; omitting the callback
persists no row; an exact duplicate converges to one row. This establishes the
KTX boundary: persistence starts at callback delivery, not at raw stream
observation.

The four-row divergence is therefore caused by raw error events reaching the
wrapper stream without corresponding evidence reaching successful hook
persistence. Retained evidence cannot narrow that gap to runtime callback
absence or hook rejection. The smallest safe correction boundary is downstream
call accounting: pair by the native tuple, retain stream-authoritative errors,
retain hook-authoritative persisted evidence, and mark callback provenance
unavailable when neither delivery nor rejection is recorded. Do not synthesize
database rows, infer success, collapse retries, or count duplicates twice.

No summary-accounting implementation is included here.
