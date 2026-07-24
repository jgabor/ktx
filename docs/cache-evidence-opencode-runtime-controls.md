# OpenCode runtime cache controls

This evidence path uses OpenCode 1.18.4 and the free
`opencode/big-pickle` model. It observes runtime-provided fields only. It does
not call a provider directly, export authentication, or treat OpenCode session
routing as provider cache isolation.

The retained no-call preflight is
`tools/bench/evidence/opencode-runtime-cache-controls-preflight.json`. It fixes
four OpenCode invocations and four invocation attempts, zero intentional
retries, zero spend, a 300-second wall limit, 55 seconds per invocation, the
ignored raw-artifact destination, and the redaction policy before model
network activity. The container check runs with networking disabled and makes
both OpenCode state roots writable.

## Control interpretation

The four-invocation ceiling produced one startup failure, two successful turns
in session A with the same stable prompt, and a non-retryable `No provider
available` result for a new session B with that same prompt. The second session
A turn reported `cache.read=7808`; its cold predecessor reported measured zero.
This is runtime-reported reuse after a cold observation, not provider cache-key
isolation. The successful turns establish same-session continuity, repeated
submitted text, and an ordered one-turn/two-turn sequence. The fresh-session
attempt establishes a distinct runtime session but produced no model step.

These controls have narrower meanings than their convenient labels:

- Equal hashed user prompts are evidence about submitted user text, not the
  provider-visible composed prefix. Session history, system text, and runtime
  transforms can change that prefix.
- Equal hashed session identifiers establish OpenCode session continuity.
  Distinct hashes establish separate OpenCode sessions. Neither establishes a
  provider cache key or provider cache isolation.
- A `step_finish` is an ordered `model_step`. It does not expose a provider
  request identifier, retry relationship, or documented one-to-one native
  request mapping. Requested turn counts and observed model-step counts remain
  separate.
- A measured positive `tokens.cache.read` is reported as cache reuse. A
  measured positive `tokens.cache.write` is reported as cache fill. Measured
  zero read is no reported reuse, not a provider-internal miss. Known
  non-eligibility is ineligible. Missing, malformed, or inapplicable fields are
  unknown. No generic hit ratio is calculated.

OpenCode 1.18.4 source shows that the `opencode` provider receives runtime
session and request headers, while request preparation composes system,
history, and user messages. The JSONL `step_finish` output does not preserve
those headers, a cache key, an explicit cache breakpoint, the composed prefix,
or native retry/request identity. Those capabilities remain unavailable in the
matrix even if aggregate token values move.

Raw JSONL and stderr remain separate below the ignored destination. The
tracked normalized record contains only hashes of prompt/session identifiers,
ordered measurements, field-completeness states, artifact hashes, redaction
results, and explicit unavailable outcomes.

The first invocation exposed an additional writable path requirement:
`/home/node/.config/opencode`. The remaining invocations mounted that path as a
temporary writable filesystem. This was one startup-path correction inside the
original four-invocation ceiling, not a model or provider retry. Because the
ceiling was then exhausted, the distinct changed prompt and a successful
fresh-session opposite remain explicitly unavailable; they are not inferred
from token movement.
