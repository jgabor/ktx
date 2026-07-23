# OpenCode telemetry contract

ktx supports the OpenCode **1.18.4** plugin event contract at commit
[`49c69c5`](https://github.com/anomalyco/opencode/tree/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e).
Later OpenCode versions are not covered until their event types are reviewed.

## Accepted events

A model-step observation comes from one `message.part.updated` event with this
shape:

```json
{
  "type": "message.part.updated",
  "properties": {
    "part": {
      "id": "prt_...",
      "sessionID": "ses_...",
      "messageID": "msg_...",
      "type": "step-finish",
      "reason": "stop",
      "cost": 0,
      "tokens": {
        "input": 116,
        "output": 3,
        "reasoning": 2,
        "cache": { "read": 15232, "write": 0 }
      }
    }
  }
}
```

The session, message, and part IDs are authoritative fields on the part. Token
fields must be non-negative safe JSON integers; `cost` must be a non-negative
JSON number. Explicit zero is a measurement, not missing data. Additional
fields are ignored so additive OpenCode metadata does not invalidate the
event.

Provider and model are not fields on `StepFinishPart`. They are available from
a preceding assistant `message.updated` event whose `properties.info.id` and
`properties.info.sessionID` match the part:

```json
{
  "type": "message.updated",
  "properties": {
    "info": {
      "id": "msg_...",
      "sessionID": "ses_...",
      "role": "assistant",
      "providerID": "openai",
      "modelID": "gpt-5"
    }
  }
}
```

If that identity event has not already been accepted, provider and model remain
absent. A later event does not backfill an already accepted observation.

The pinned shapes come from OpenCode's generated
[`EventMessagePartUpdated`, `StepFinishPart`, and `AssistantMessage` types](https://github.com/anomalyco/opencode/blob/v1.18.4/packages/sdk/js/src/gen/types.gen.ts)
and the plugin's typed [`event` hook](https://github.com/anomalyco/opencode/blob/v1.18.4/packages/plugin/src/index.ts).

## Identity, deduplication, and ordering

- The deduplication key is the exact `(sessionID, messageID, part.id)` tuple.
  The first valid occurrence is authoritative; later occurrences are ignored,
  even if their payload differs.
- Accepted model steps receive contiguous local ordinals in plugin callback
  acceptance order, after duplicates and rejected events are removed.
  OpenCode 1.18.4 supplies no event sequence or event timestamp in this plugin
  event. The ordinal is local capture order, not a provider request number or
  provider timestamp.
- Concurrent callbacks must be serialized at the collector boundary before an
  ordinal is assigned. No ordering is inferred from lexicographic IDs.
- OpenCode exposes no provider request ID, retry relationship, or documented
  one-to-one model-step-to-provider-request mapping on these events. ktx does
  not synthesize them and does not label a model step as a native request.

## Rejection and claim boundary

Malformed JSON, unsupported event or part types, a missing/non-object
`properties`, a missing/non-object `part`, blank required IDs, missing token or
cache objects, missing required measurements, and malformed measurements are
rejected without a partial observation. Missing provider/model correlation is
the sole accepted identity limitation and remains absent.

The cache values are OpenCode-normalized per-model-step token observations.
They do not establish cache hits, misses, native request counts, provider
request identity, retry identity, or cache isolation.
