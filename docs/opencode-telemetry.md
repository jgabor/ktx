# OpenCode telemetry contract

ktx supports the OpenCode **1.18.4** plugin event contract at commit
[`49c69c5`](https://github.com/anomalyco/opencode/tree/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e).
Later OpenCode versions are not covered until their event types are reviewed.

## Install and refresh

`ktx init opencode` is the generator and installer for the standalone `ktx.ts`
plugin. It writes the generated source to one of OpenCode's candidate plugin
locations:

| Scope   | Command                       | Candidate location                                                                                  |
| ------- | ----------------------------- | --------------------------------------------------------------------------------------------------- |
| Global  | `ktx init opencode`           | `$XDG_CONFIG_HOME/opencode/plugins/ktx.ts`, falling back to `$HOME/.config/opencode/plugins/ktx.ts` |
| Project | `ktx init opencode --project` | `./.opencode/plugins/ktx.ts`                                                                        |

Re-run the same install command to refresh the generated plugin after upgrading
ktx. Installation is idempotent and overwrites only the selected generated
`ktx.ts` file. `ktx init opencode --show` reports candidate files found on disk;
it does not claim that OpenCode loaded or activated either file.

The generated plugin declares two OpenCode hooks:

| Hook                 | Role                                                                                               |
| -------------------- | -------------------------------------------------------------------------------------------------- |
| `tool.execute.after` | Sends tool output to `ktx hook` for persistence and optional compression.                          |
| `event`              | Normalizes accepted model-step events and sends optional token evidence to `ktx telemetry record`. |

When OpenCode runs under `ktx run`, the wrapper sets `KTX_SESSION_ID`. The
plugin then noops both hooks, leaving the wrapper as the single evidence path.
The wrapper does not replace the event hook's model-step telemetry, so that
optional evidence is not recorded while wrapped. Without the wrapper, the
plugin handles both hooks. This allows the installed plugin and wrapper to
coexist without double-recording.

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
fields must be non-negative safe JSON integers no greater than `2^53 - 1`;
`cost` must be a non-negative JSON number. Explicit zero is a measurement, not
missing data. Additional fields are ignored so additive OpenCode metadata does
not invalidate the event.

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
They are optional evidence, not raw provider responses. They do not establish
cache hits, cache misses, native requests or request counts, provider request
identity, retry identity, or cache isolation.

## Inspect and verify offline

List retained observations for a ktx session without contacting a provider:

```bash
ktx telemetry list --session <ktx-session-id> --format text
```

Run the focused offline fixture matrix from a checkout:

```bash
mage opencodeTelemetryFixture
```

The target requires Bun to execute the generated TypeScript plugin, but requires
no provider credentials or network access. It covers complete and explicit-zero
observations; missing and malformed fields; duplicate handling; wrapped and
unwrapped plugin behavior; and concurrent callback and persistence behavior.

## Troubleshooting

- Run `ktx init opencode --show` to inspect both candidate plugin locations. A
  file report is not proof of runtime activation.
- Re-run `ktx init opencode` or `ktx init opencode --project` to refresh the
  generated file in the intended scope. If both scopes contain `ktx.ts`, remove
  the unintended copy with `ktx init opencode --uninstall`, then reinstall the
  intended scope.
- Ensure `ktx` is on OpenCode's `PATH`. If it is absent, the plugin reports that
  it is disabled and registers neither hook.
- If OpenCode is wrapped by `ktx run`, `KTX_SESSION_ID` intentionally disables
  the plugin hooks. Test the unwrapped path without `ktx run` when diagnosing
  plugin loading.
- Telemetry and tool-hook failures are written to stderr and fail open. Tool
  output remains unchanged; optional telemetry persistence does not fail the
  OpenCode operation.
- If an event shape is rejected after an OpenCode upgrade, compare it with the
  pinned version above before changing the contract. Do not infer missing token
  values or provider-request semantics.
