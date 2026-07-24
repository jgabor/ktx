# Bounded OpenAI and OpenCode cache evidence

The OpenAI/OpenCode evidence path starts with a no-network preflight. It is
specific to the current evidence question rather than a provider adapter:

- priced direct evidence uses OpenAI Responses API `gpt-5.6-luna` with
  reasoning effort `high`;
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

The command reads the proposal, credential presence, the local OpenCode
executable version, and nothing from a provider endpoint. Its report always
states `network_activity: none` and `billable_requests: 0`. Missing credentials,
an absent or mismatched OpenCode executable, a model-policy violation, or a
ceiling below the calculated worst case leaves the path unavailable before a
billable request. Model entitlement remains
`unverified_without_provider_request` until an approved live action runs.

The preflight report names the exact unavailable condition without retaining a
credential value. Live artifacts belong below the ignored
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
