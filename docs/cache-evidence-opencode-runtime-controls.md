# OpenCode runtime cache controls

This evidence path uses OpenCode 1.18.4 and the free
`opencode/big-pickle` model. It observes runtime-provided fields only. It does
not call a provider directly, export authentication, or treat OpenCode session
routing as provider cache isolation.

The supported claim is only: **OpenCode runtime reported cache reuse in these
retained observations.** Runtime-managed separation, provider-internal
isolation, provider hit/miss, cache effectiveness, and generalization to other
versions, models, or runs are unavailable.

The original retained no-call preflight is
`tools/bench/evidence/opencode-runtime-cache-controls-preflight.json`. It fixed
four OpenCode invocations and four invocation attempts, zero intentional
retries, zero spend, and a 300-second wall limit. After the first audit,
`opencode-runtime-cache-controls-preflight-2.json` authorized at most two more
runtime invocations, two successful model steps, zero intentional retries, 120
seconds, and zero dollars. Both preflights completed without network activity
or a model call.

## Control interpretation

The original ceiling produced one startup failure, two successful model steps
in session A with the same stable prompt, and a non-retryable `No provider
available` result for a new session B. The second session-A step reported
`cache.read=7808`; its cold predecessor reported measured zero. The correction
used two additional invocations. A fresh-state resume failed with `Session not
found` before a model step. The next invocation reused the ignored runtime state
and submitted the changed synthetic prompt in session A; it produced one
`step_finish` with `cache.read=14144`, `cache.write=0`, uncached input 6377,
output 6, and cost 0.

The additional accounting is exactly two invocations, one successful model
step, zero intentional retries, zero dollars, and 12 seconds against maxima of
two invocations, two successful model steps, zero retries, zero dollars, and
120 seconds. Combined with the original run, the retained record has six
invocations, three successful model steps, zero intentional retries, zero
dollars, and 33 seconds.

These controls have narrower meanings than their convenient labels:

- Equal or changed hashed user prompts are evidence about submitted user text,
  not the provider-visible composed prefix. Session history, system text, and
  runtime transforms can change that prefix.
- Equal hashed session identifiers establish OpenCode session continuity.
  Distinct hashes establish separate OpenCode sessions. Neither establishes a
  provider cache key or provider cache isolation.
- A `step_finish` is an ordered `model_step`. The one/multi cases refer only to
  runtime-visible model-step/request sequencing. A step does not expose a
  provider request identifier, retry relationship, or documented one-to-one
  provider-request mapping. Requested turn counts and observed model-step counts
  remain separate from native provider request count, which is unavailable.
- A measured positive `tokens.cache.read` is reported as cache reuse. A
  measured positive `tokens.cache.write` is reported as cache fill. Measured
  zero read is no reported reuse, not a provider-internal miss. Known
  non-eligibility is ineligible. Missing, malformed, or inapplicable fields are
  unknown. All three live steps exposed `cache.write` as measured zero, so no
  positive runtime-reported fill occurred and no fill is inferred. A runtime
  that omitted that field would retain it as unavailable. No generic hit ratio
  is calculated.

OpenCode 1.18.4 source shows that the `opencode` provider receives runtime
session and request headers, while request preparation composes system,
history, and user messages. The JSONL `step_finish` output does not preserve
those headers, a cache key, an explicit cache breakpoint, the composed prefix,
or native retry/request identity. Those capabilities remain unavailable in the
matrix even if aggregate token values move.

Raw JSONL and stderr remain separate below ignored destinations. Sanitized
tracked excerpts, exact new-probe accounting, and executable configuration live
under `tools/bench/evidence/opencode-runtime-cache-controls/`. They omit raw
session, message, part, cache namespace, credential, and prompt content. The
mandatory regression test derives every live classification and measurement
from those excerpts, regenerates synthetic prompt hashes, verifies tracked
artifact hashes and redaction, and checks that authentication is passed through
without reading its value. Ignored-raw hash checks are a separate optional test.

The first original invocation exposed an additional writable path requirement:
`/home/node/.config/opencode`. The remaining invocations mounted that path as a
temporary writable filesystem. This was a startup-path correction, not a model
or provider retry. The bounded correction now retains the distinct changed
submitted prompt. A successful fresh-session model step remains unavailable.
Provider-visible prefix, breakpoint, key, isolation, positive fill, and native
provider request identity/count also remain unavailable and are not inferred
from token movement.
