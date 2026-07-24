# Retained historical OpenAI and OpenCode cache investigation

This document preserves a completed source/offline investigation. It does not
describe a supported collection path: current telemetry is collected only from
the agent runtime, and no direct-provider probe or credential path is future
work. The historical investigation considered:

- the priced direct case is defined as OpenAI public Responses API
  `gpt-5.6-luna` with reasoning effort `high`;
- routine OpenCode observation uses `opencode/big-pickle` without a priced
  variant;
- a bounded proposal that fixed request, attempt, spend, wall-time, token,
  artifact, and redaction ceilings before any provider request.

The retained no-network preflight read the proposal, secret-free credential
metadata from `opencode auth list`, environment-variable presence, the local OpenCode
executable version, and nothing from a provider endpoint. Its report always
states `network_activity: none` and `billable_requests: 0`. Missing credentials,
an absent or mismatched OpenCode executable, a model-policy violation, or a
ceiling below the calculated worst case leaves the path unavailable before a
billable request. Model entitlement remains
`unverified_without_provider_request` until an approved live action runs.

## ChatGPT authentication boundary

The installed OpenCode credential metadata reports OpenAI authentication type
`oauth`; no token value is inspected or retained. OpenCode 1.18.4's
[`CodexAuthPlugin`](https://github.com/anomalyco/opencode/blob/v1.18.4/packages/opencode/src/plugin/openai/codex.ts)
consumes that credential inside OpenCode. Its fetch adapter adds the ChatGPT
account header and rewrites `/v1/responses` to the ChatGPT Codex backend. A
request through that adapter is therefore OpenCode-mediated evidence, not a
direct public Responses API request.

The [OpenAI API authentication reference](https://developers.openai.com/api/reference/overview/)
documents API keys and workload-identity access tokens for direct application
requests. It does not document reusing a ChatGPT subscription OAuth credential
against the public Responses API. OpenAI also states that
[ChatGPT subscriptions do not include API usage](https://help.openai.com/en/articles/6950777-what-is-chatgpt-plus).
The direct native path is consequently retained as source-backed unavailable;
an unsupported extraction or replay of OpenCode's OAuth tokens is not attempted.

The preflight report names the exact historical unavailable condition without
retaining a credential value. No live provider call followed. The ignored
artifact destination and redaction rules are retained as provenance, not as a
runbook or approval path.

## Field preservation

`internal/cacheevidence` retains the historical provider-format fixture and
OpenCode JSONL as separate offline sources, then adds ordered observations. It
does not replace either source with a common object. Provider-format and
runtime-normalized input, cache read,
cache write or creation, output, identity, control, and retry availability can
therefore be inspected independently. OpenCode `step-finish` remains a model
step because it does not expose an authoritative provider request identity.

The sanitized fixtures under `tools/bench/fixtures/` exercise this contract
offline. They are structural examples, not live evidence. The OpenAI field
semantics and GPT-5.6 cache-write multiplier come from the
[Prompt Caching guide](https://platform.openai.com/docs/guides/prompt-caching)
and current [API pricing](https://openai.com/api/pricing). OpenCode event and
retry behavior is pinned to
[1.18.4 at `49c69c5`](https://github.com/anomalyco/opencode/tree/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e).

Methodology v5 remains opt-in. This path does not change historical experiment
artifacts or upgrade observed evidence to confirmed isolation.

## Historical OpenCode-mediated Luna attempt

An approved OpenCode-mediated probe used the existing
`ktx-bench:opencode-1.18.4` image (`d40f07d…`) with
`openai/gpt-5.6-luna`, variant `high`, a synthetic exact-response prompt, no
tools, one configured agent step, read-only authentication, and no intentional
retries. Raw output and normalized status are retained separately under the
ignored `.bench-data/cache-evidence/openai-opencode-luna-high-20260724/opencode-mediated`
directory; the tracked summary is
`tools/bench/evidence/openai-opencode-mediated-luna-probe.json`.
That redacted tracked record includes both exact shell commands, exit statuses,
one-second-resolution elapsed measurements, sanitized startup errors, all
retained artifact names and SHA-256 values, the redaction command and result,
and explicit unavailable cost/cache fields. Its test verifies the tracked
contract everywhere and also checks every ignored artifact hash when the local
evidence directory is present.

Both permitted OpenCode invocations failed during local container startup. The
first could not create OpenCode's `repos` directory; the second passed that
mount check but could not create `/home/node/.local/state` under the read-only
root. Both exited before a model step and emitted empty JSONL. The two-invocation
ceiling was therefore exhausted without another call. Reported and estimated
cost, token/cache fields, model-step identity, controls, and retry metadata are
unavailable rather than zero. The result supports no cache hit, miss, fill,
reuse, or isolation claim and does not alter the unsupported-direct conclusion.
