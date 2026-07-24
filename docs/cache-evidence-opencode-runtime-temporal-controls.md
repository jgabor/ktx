# OpenCode runtime temporal controls

This evidence makes one narrow claim: OpenCode execution can be described as
sequential or orchestrator-requested concurrent only by counting and ordering
the runtime-visible `step_finish` events produced by each invocation. It does
not establish provider-native concurrency, cache isolation, or cache
effectiveness.

The sequential side is reused rather than run again. Its tracked normalized
record is `tools/bench/evidence/opencode-runtime-cache-controls.json` (SHA-256
`1eaa07c89027c31e92b1c1094a33fc5e1df589c435979e45c3af41f230af1075`),
and its tracked source excerpts are
`tools/bench/evidence/opencode-runtime-cache-controls/source-events.jsonl`
(SHA-256
`738504be1936b021a37e640367a71d0e12ee538e2cf062005d62ff741c56efe8`).
Those records contain three successful invocations with one model step each in
execution ordinals 2, 3, and 6. Ordinals 2 then 3 are the retained sequential
same-session pair.

The concurrent side is one comparison authorized by
`tools/bench/evidence/opencode-runtime-temporal-controls-preflight.json`. It is
limited to two OpenCode invocations, two successful model steps, zero
intentional retries, 60 seconds, and zero dollars using OpenCode 1.18.4 and
`opencode/big-pickle`. The two fixed, no-tool invocations are launched without
waiting for either to finish. Tracked sanitized excerpts retain invocation
launch order, event order within each invocation, model-step count,
and measurements. Raw JSONL, stderr, execution configuration, and prompt
content remain ignored.

The comparison used exactly two invocations and two invocation attempts. Each
invocation emitted one `step_finish`, for two successful runtime-visible model
steps. Invocation elapsed times were 25 and 20 seconds; total comparison wall
time was 25 seconds. Intentional retries, direct-provider calls, intentional TTL
wait, and spend were all zero. Both invocations exited successfully. The shell
launch order was 1 then 2; collection order was 1 then 2. Completion order is
unavailable because collection order is not promoted into a timestamp or
ownership claim.

## Classification boundary

- `observed` means a sanitized runtime event contains the stated measurement.
- `unavailable` means OpenCode does not expose the field in its JSONL event.
- `failed-before-model-step` means an invocation ended without `step_finish`.
- `unknown` means the retained runtime data cannot classify the result.

Provider request identity, provider request count, retry relationships,
authoritative timestamps, concurrency ownership, provider-visible prefix,
cache key, and separation are unavailable. Shell launch order describes the
comparison setup, not provider or runtime ownership. Runtime event order is
authoritative only within each captured invocation. No hit ratio,
provider-internal miss, cache-isolation, or cache-effectiveness claim is made.

OpenCode 1.18.4's published `step_finish` event does not expose cache TTL or an
expiry timestamp. TTL is therefore source-qualified unavailable for this
runtime path. The documented source is the pinned event publisher at
<https://github.com/anomalyco/opencode/blob/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e/packages/core/src/session/runner/publish-llm-event.ts>.
No TTL wait is attempted because the comparison's complete wall-time ceiling is
60 seconds and there is no runtime-visible TTL to test within it.

The tracked evidence is designed to reproduce all measurements, hashes,
ordering, and classifications without provider credentials or ignored
`.bench-data` contents. It retains no prompt content, credentials, session or
cache namespaces, or message and part identifiers.
