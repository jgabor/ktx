# Bounded Anthropic and OpenCode cache evidence

This evidence path selects the Anthropic public Messages API
`POST /v1/messages`, header `anthropic-version: 2023-06-01`, and model alias
`claude-sonnet-5`. Official source material was retrieved on 2026-07-24. The
machine-readable capability record is
`tools/bench/evidence/anthropic-capability.json`.

## Official semantics

The current [prompt-caching guide](https://platform.claude.com/docs/en/build-with-claude/prompt-caching),
[Messages reference](https://platform.claude.com/docs/en/api/messages),
[version policy](https://platform.claude.com/docs/en/api/versioning), and
[error reference](https://platform.claude.com/docs/en/api/errors) establish the
following scope:

- `cache_control` supports top-level automatic placement or explicit content
  block breakpoints. `ephemeral` is the only cache type.
- `usage.cache_creation_input_tokens` is cache creation and
  `usage.cache_read_input_tokens` is cache read. `usage.input_tokens` is the
  uncached suffix; total input is the sum of all three fields.
- Claude Sonnet 5 requires 1,024 cacheable tokens on the public API. A shorter
  marked prompt is processed without an error. Creation and read are reported
  as zero; that observation does not identify an internal miss reason.
- The default TTL is five minutes and use refreshes it without an additional
  write charge. `ttl: "1h"` requests the priced one-hour TTL.
- the Message body `id` and response `request-id` header are distinct retained
  identities. OpenCode session, message, and part IDs do not replace either.
- official SDKs retry transient connection failures, rate limits, and 5xx
  responses twice by default with exponential backoff and `retry-after`.
  Evidence probes disable that behavior: one attempt per request.

The guide also states that a concurrent follower cannot use a new entry until
the first response begins. This task makes no concurrency, isolation, cache
namespace, or provider-internal hit/miss claim.

## Native versus OpenCode evidence

Provider-native JSON and OpenCode JSONL are retained as separate sources. The
normalizer adds observations and never rewrites either input. OpenCode 1.18.4
is pinned to commit
[`49c69c5`](https://github.com/anomalyco/opencode/tree/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e);
its [AI SDK bridge](https://github.com/anomalyco/opencode/blob/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e/packages/opencode/src/session/llm/ai-sdk.ts)
maps cache-read and cache-write usage into its runtime event, and its
[processor](https://github.com/anomalyco/opencode/blob/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e/packages/opencode/src/session/processor.ts)
persists the resulting step.

| Native field                                  | OpenCode step-finish | Treatment                                             |
| --------------------------------------------- | -------------------- | ----------------------------------------------------- |
| `usage.input_tokens`                          | `tokens.input`       | renamed; integer, not rounded                         |
| `usage.cache_creation_input_tokens`           | `tokens.cache.write` | renamed; integer, not rounded                         |
| `usage.cache_read_input_tokens`               | `tokens.cache.read`  | renamed; integer, not rounded                         |
| `usage.output_tokens`                         | `tokens.output`      | renamed; integer, not rounded                         |
| creation split by 5-minute/1-hour TTL         | unavailable          | dropped                                               |
| Message `id`; response `request-id`           | unavailable          | dropped; runtime IDs are not substitutes              |
| `cache_control`, breakpoint, TTL, eligibility | unavailable          | dropped                                               |
| attempt and retry ceiling                     | unavailable          | dropped                                               |
| provider-reported cost                        | unavailable          | OpenCode cost is runtime-derived and remains separate |

The positive fixture preserves a measured-zero cache read beside a measured
1,024-token creation. The below-minimum fixture omits cache fields so the
normalizer preserves them as unavailable, not zero. These are sanitized
structural fixtures, not live provider evidence.

## Bounded access result

The no-request gate is:

```bash
mage build
build/ktx bench evidence preflight \
  --spec tools/bench/evidence/anthropic-opencode-proposal.json \
  --output tools/bench/evidence/anthropic-opencode-preflight.json
```

It inspects only credential presence/type and `opencode --version`. It fixes a
maximum of four native requests, four attempts, USD 0.035 worst-case spend, two
minutes wall time, the ignored artifact destination, and redaction policy. It
always reports `network_activity: none` and `billable_requests: 0`.

On 2026-07-24, `ANTHROPIC_API_KEY` was absent and `opencode auth list` contained
no Anthropic API credential. OpenCode 1.18.4 itself was present. Therefore no
model request was proposed for execution, no approval was requested, and both
live paths remain unavailable. Official-source evidence, offline fixtures, and
live-unavailable state remain separately labeled.
