# Bounded OpenAI and OpenCode cache evidence

The OpenAI/OpenCode evidence path starts with a no-network preflight. It is
specific to the current evidence question rather than a provider adapter:

- the priced direct case is defined as OpenAI public Responses API
  `gpt-5.6-luna` with reasoning effort `high`;
- routine OpenCode observation uses `opencode/big-pickle` without a priced
  variant;
- the proposal fixes request, attempt, spend, wall-time, token, artifact, and
  redaction ceilings before any provider request.

Run the gate from the repository root:

```bash
mage build
build/ktx bench evidence preflight \
  --spec tools/bench/evidence/openai-opencode-proposal.json \
  --output tools/bench/evidence/openai-opencode-preflight.json
```

The command reads the proposal, secret-free credential metadata from
`opencode auth list`, environment-variable presence, the local OpenCode
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

The preflight report names the exact unavailable condition without retaining a
credential value. This unavailable outcome requires no API key and no live
provider call. Live artifacts, if a later supported path is separately
approved, belong below the ignored
`.bench-data/cache-evidence/` destination declared in the proposal. Credentials
and authorization headers are omitted; cache keys and sensitive correlation
identifiers are omitted or retained only as SHA-256; prompts are synthetic.

## Field preservation

`internal/cacheevidence` retains the provider-native record and OpenCode JSONL
as separate raw sources, then adds ordered observations. It does not replace
either source with a common object. Native and normalized input, cache read,
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
