# Bounded candidate evaluation — 2026-08-19

## Disposition

**ONE PROVISIONAL CANDIDATE RETAINED.** All three bounded candidates ran over all seven retained matrix cases under the same immutable rules. `keep-ends:73:1` passed every gate with positive mechanical net savings. No product route, profile, default, or runtime behavior changed.

The machine-readable receipt is `tools/bench/evidence/targeted-residual-candidate-evaluation.json`, SHA-256 `d9624b7e0ab97c71b6488c3fcb22c4c3fb9b67bd190fd9e472b262b6dcf14b26`.

## Candidate inventory and outcomes

All candidates route only explicit successful `read` calls. Failure, unknown-outcome, and other-tool cases bypass the transform and return the original bytes without a transformed artifact. Routing uses no size or content predicate.

| Candidate                 | Existing stage   | Safety result                                                                 |                           Mechanical result after cue | Disposition |
| ------------------------- | ---------------- | ----------------------------------------------------------------------------- | ----------------------------------------------------: | ----------- |
| Broad head truncation     | `head:10`        | Failed required-fact/count gates in 3 of 4 eligible cases                     | 13,125 bytes saved (13,440 gross minus 315 cue bytes) | Rejected    |
| Strict JSON compaction    | `json-compact`   | Passed all seven cases; non-JSON and already-compact JSON remained byte-exact |                                               0 bytes | Rejected    |
| Balanced end preservation | `keep-ends:73:1` | Passed all fact, exact-count, error, recovery, and passthrough gates          |   1,534 bytes saved (1,745 gross minus 211 cue bytes) | Provisional |

`json-compact` is the partial-applicability alternative: applicability is decided inside the existing transform by strict complete-JSON/NDJSON parsing. Non-applicable input is returned byte-exact. The router still uses only tool and documented exit status. The generated structured case is already compact, so the pipeline anti-worsening gate returns it unchanged and no recovery cue is emitted.

## Full matrix results

| Case                     | `head:10`                                | Exact failed gate                                                          | `json-compact`                           | `keep-ends:73:1`                                   |
| ------------------------ | ---------------------------------------- | -------------------------------------------------------------------------- | ---------------------------------------- | -------------------------------------------------- |
| `eligible-head`          | Fail; 4,096 → 640 bytes                  | `REPEATED_FACT` count 1, required 3                                        | Pass unchanged; 4,096 bytes              | Pass unchanged; 4,096 bytes                        |
| `eligible-middle`        | Fail; 5,120 → 640 bytes                  | `MIDDLE_REQUIRED` count 0, required 1; `REPEATED_FACT` count 1, required 4 | Pass unchanged; 5,120 bytes              | Pass; 5,120 → 4,759 bytes; 105-byte cue; 256 net   |
| `eligible-tail`          | Fail; 6,144 → 640 bytes                  | `TAIL_REQUIRED` count 0, required 1; `REPEATED_FACT` count 1, required 5   | Pass unchanged; 6,144 bytes              | Pass; 6,144 → 4,760 bytes; 106-byte cue; 1,278 net |
| `eligible-structured`    | Pass unchanged; 7,168 bytes              | None                                                                       | Pass unchanged; 7,168 bytes              | Pass unchanged; 7,168 bytes                        |
| `reject-failure`         | Pass byte-exact passthrough; 4,096 bytes | None                                                                       | Pass byte-exact passthrough; 4,096 bytes | Pass byte-exact passthrough; 4,096 bytes           |
| `reject-tool`            | Pass byte-exact passthrough; 4,096 bytes | None                                                                       | Pass byte-exact passthrough; 4,096 bytes | Pass byte-exact passthrough; 4,096 bytes           |
| `reject-unknown-outcome` | Pass byte-exact passthrough; 4,096 bytes | None                                                                       | Pass byte-exact passthrough; 4,096 bytes | Pass byte-exact passthrough; 4,096 bytes           |

All cases retained byte-exact originals for recovery. Error state/text were untouched because the failure case bypassed both transforms. No case was omitted.

## Evidence identity and limits

- Corrected matrix report: `3310b77416220aed4feeda986181074d502d39489da3a432c5dbd1d49282da72`.
- Matrix JSON: `7eece02dc19887eafb17403bc0a0c11c9d737a3d08d55fe226470f095c548a32`.
- Evaluator test after candidate coverage: `505e60e7768c148b01210361be88f495ddb04d8b9257e0c7055f7bc46d1b5b39`.

The evaluator uses the shipped built-in registry and runner, including the anti-worsening gate, and the production recovery-footer helper. Cue accounting uses UTF-8 byte lengths and the deterministic valid 16-character `evaluationsrcxxx` source ID. This is generated mechanical evidence only. It does not measure tool-call effects, latency, live behavior, historical content, provider behavior, or rollout impact. Provisional retention is not rollout approval.
