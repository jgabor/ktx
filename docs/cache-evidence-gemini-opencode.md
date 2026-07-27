# Retained historical Gemini and OpenCode cache investigation

This document preserves a completed official-source and offline-fixture
investigation. It is not a supported direct-provider collection path, and no
provider credential or probe work follows from it. The historical scope was
pinned to the Gemini Developer API `v1beta`
`cachedContents` and `generateContent` REST resources, `gemini-2.5-flash`, and
OpenCode 1.18.4 at commit `49c69c5`. Official source material was retrieved on
2026-07-24. It is provider-specific; it does not add a universal provider
adapter.

## Official semantics

The legacy generateContent [context-caching guide](https://ai.google.dev/gemini-api/docs/generate-content/caching)
distinguishes implicit caching from explicit cache objects. Explicit caching
creates reusable input with a caller-selected TTL; omitted TTL defaults to one
hour. The cache is deleted at expiry. For `gemini-2.5-flash`, the retrieved
guide gives a 2,048-token minimum, says cached tokens remain part of model token
limits, and says create/get/list plus cached `GenerateContent` responses expose
cached-token metadata.

The `v1beta` [cache API reference](https://ai.google.dev/api/caching) defines
`cachedContents.create`, `get`, `list`, `update`, and `delete`. `CachedContent`
has a server resource `name`, immutable model/content/tool/system fields,
`createTime`, `updateTime`, an always-returned `expireTime`, and
`usageMetadata.totalTokenCount`. Only a SHA-256 of the resource name is allowed
in retained evidence. TTL or `expireTime` can be updated; content and model
cannot.

The [generateContent API reference](https://ai.google.dev/api/generate-content)
defines `usageMetadata.promptTokenCount` as the effective prompt size including
cached content and separately exposes `cachedContentTokenCount`,
`candidatesTokenCount`, `thoughtsTokenCount`, and `totalTokenCount` when the
service reports them. Missing optional fields are unavailable, not zero.

The [troubleshooting guide](https://ai.google.dev/gemini-api/docs/troubleshooting)
says official SDKs retry transient timeouts, network failures, HTTP 429, and
5xx responses with exponential backoff by default; its Python example permits
up to four retries. The proposed probe instead uses direct REST with one attempt
per native request. Gemini responses do not report an authoritative attempt
number, so attempt metadata is probe-owned. OpenCode `step-finish` reports no
retry count or provider request identity.

The retained pricing ceiling uses the 2026-07-24
[Gemini API pricing](https://ai.google.dev/gemini-api/docs/pricing) for
`gemini-2.5-flash`: $0.30 per million input tokens, $2.50 per million output
tokens, and $1.00 per million cached tokens per hour of storage. The ceiling is
an upper bound, not observed cost.

## OpenCode normalization boundary

OpenCode 1.18.4's pinned
[`gemini.ts`](https://github.com/anomalyco/opencode/blob/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e/packages/llm/src/protocols/gemini.ts)
subtracts `cachedContentTokenCount` from `promptTokenCount`, renames the result
to non-cached input, renames cached tokens to cache-read input, and carries
candidate and thought tokens into visible output and reasoning. Its pinned
[cache recording test](https://github.com/anomalyco/opencode/blob/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e/packages/llm/test/provider/gemini-cache.recorded.test.ts)
states that explicit `CachedContent` is out-of-band and intentionally not
wired. The normalized `step-finish` therefore lacks cache-object name, TTL,
creation-token count, explicit control, Gemini response ID, resolved model
version, provider request identity, and retry count. It may emit a measured
`cache.write: 0`; that runtime value does not turn absent native cache-creation
telemetry into a measured native zero.

`internal/cache/reuse` retains the sanitized Gemini source and OpenCode JSONL
separately. `field_accounting` names every split, rename, dropped field,
weakened identity, and unavailable field. No token value is rounded. The
fixtures under `tools/bench/fixtures/` are synthetic structural examples, not
live observations. The unavailable fixture retains the exact synthetic error
without creating zero-valued observations.

## Historical no-call result

The retained preflight read environment-variable presence, secret-free
`opencode auth list` metadata, and `opencode --version`. It made no network or model request. The
retained 2026-07-24 result has `network_activity: none` and
`billable_requests: 0`: `GEMINI_API_KEY` is absent, while Google OAuth metadata
and OpenCode 1.18.4 are present. Direct Gemini is therefore unavailable and
OpenCode model entitlement remains `unverified_without_provider_request`.
No live probe ran.

The historical proposal's limits and ignored artifact destination remain
retained provenance, not a future approval path. This evidence makes no cache
hit ratio, miss, fill, isolation, namespace, request-boundary, prefix-composition,
tool-call attribution, or provider-internal eviction claim. Current collection
uses runtime observations only.
