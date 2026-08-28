# OpenCode telemetry contract

ktx supports the OpenCode **1.18.5** plugin event contract at commit
[`e5cc278`](https://github.com/anomalyco/opencode/tree/e5cc278dec9294a627a7b05f47ce6a564408c1a2).
Later OpenCode versions are not covered until their event types are reviewed.
This is a runtime-only telemetry contract: live observations come from OpenCode
events. Provider/model identity may be runtime-emitted or inferred from explicit
runtime configuration with provenance. Provider documentation, pricing data,
and historical provider fixtures are context rather than telemetry.

## Install

`ktx init opencode` is the generator and installer for the standalone `ktx.ts`
plugin. It writes the generated source to one of OpenCode's candidate plugin
locations:

| Scope   | Command                       | Candidate location                                                                                  |
| ------- | ----------------------------- | --------------------------------------------------------------------------------------------------- |
| Global  | `ktx init opencode`           | `$XDG_CONFIG_HOME/opencode/plugins/ktx.ts`, falling back to `$HOME/.config/opencode/plugins/ktx.ts` |
| Project | `ktx init opencode --project` | `./.opencode/plugins/ktx.ts`                                                                        |

Re-run the same install command after upgrading ktx to pick up generated plugin
source changes. Installation is idempotent and overwrites only the selected
generated `ktx.ts` file. A database schema increase alone does not invalidate an
installed plugin. `ktx init opencode --show` reports candidate files found on
disk; it does not claim that OpenCode loaded or activated either file.

At startup, the generated plugin resolves and canonicalizes one `ktx` candidate
and pins that absolute path for every later callback. This prevents a later
`PATH` change from selecting a different binary.

The generated plugin declares two OpenCode hooks:

| Hook                 | Role                                                                                               |
| -------------------- | -------------------------------------------------------------------------------------------------- |
| `tool.execute.after` | Sends tool output to `ktx hook` for persistence and optional compression.                          |
| `event`              | Normalizes accepted model-step events and sends optional token evidence to `ktx telemetry record`. |

The wrapper's accepted OpenCode `tool_use` shape also normalizes
`part.state.time.start` and `end` as tool-call latency. A valid non-reversed
pair is measured runtime evidence in milliseconds, including measured zero.
Missing `state.time` is unknown. A partial, non-numeric, or reversed pair is
malformed runtime evidence and has no numeric latency. OpenCode plugin
`tool.execute.after` callbacks, Codex events, and flat wrapper events do not
supply this timing contract, so they remain unknown unless a supported runtime
event supplies latency. ktx does not measure elapsed time locally or reconstruct
historical authority.

When OpenCode runs under `ktx run`, the wrapper sets `KTX_SESSION_ID`. The
plugin then noops both hooks, leaving the wrapper as the single evidence path.
The wrapper normalizes identifiable `step_finish` records into the same model-
step evidence contract while continuing to pass the original JSONL through
unchanged. Without the wrapper, the plugin handles both hooks. The persisted
runtime tuple deduplicates a model step seen through both paths, and the first
accepted row—including its collection provenance—remains authoritative.

`ktx run --collection=<16-letter-id> opencode ...` is a separate explicit
same-population path. It keeps stdout as the wrapper's transparent JSONL
pass-through but leaves the pinned OpenCode plugin active. The plugin and hook
own transformed delivery and the append-only source row; the wrapper owns the
session lifecycle and retains runtime timing separately only after that
hook-owned source resolves to the same session and native source identity.
Reuse the same collection ID across runs to retain all run intervals together.
Database-target or ingress disagreement, an invalid ID, missing hook ownership,
contradictory correlation, and duplicate timing fail the collection closed.
Continuity reports every between-run gap, interruption, conflicting boundary,
and missing closure; missing activity is never inferred as coverage.

When that explicit path delivers a lossy artifact with a recovery cue, the
generated adapter acknowledges delivery only after assigning the hook envelope
output. Future-only metadata links the delivery to its collection, wrapper
session, trusted OpenCode identity, source, and first artifact. A later trusted
OpenCode bash callback is linked as a recovery attempt only for the exact
`ktx read <source-id>` command and an explicit exit result. Successful output is
parsed as the stored source record and compared with the retained original;
byte-exactness is never inferred from malformed, composed, unlinked, unknown,
or failed activity. Repeated attempts remain separate while sharing one cue.

On the unwrapped ordinary path, the plugin accepts `event` and
`tool.execute.after` callbacks through one arrival-order queue. It starts no
later callback's ktx work until the earlier callback finishes. A hook changes
the agent-visible output only after `ktx hook` returns its envelope, whose
source evidence was accepted before any transformed output is returned.

Benchmark collection does not repurpose `KTX_SESSION_ID`. It supplies the exact
precreated `KTX_BENCH_SESSION_ID` owner with a required event interval and
contract. In that mode hooks remain active, never read or write parent-process
session files, serialize every callback, and reject ownership, acknowledgement,
or persistence failures instead of failing open. A collector-open acknowledgement
must carry its positive allocated global ordinal. Every event acknowledgement
must carry exactly the preceding acknowledged global ordinal plus one; gaps,
backwards values, and non-safe-integer values fail the callback. Every callback
occurrence, including an exact duplicate model step, receives and retains one
global ordinal. The ledger classifies model steps as accepted, exact duplicate,
or conflicting duplicate. Exact duplicates acknowledge success but do not add a
second model-step measurement. Conflicting duplicates retain the occurrence,
preserve the first measurement, make the interval non-sealable as complete, and
fail the callback. A failed callback does not leave the serialized plugin queue
permanently rejected, so a later callback can expose an epoch or global-order
gap.
Acknowledgements and tool envelopes must also match the exact owner, interval,
epoch, epoch ordinal, normalized kind, native tool-call identity, and contract
before a callback succeeds or tool output changes. A later, sequential collector
epoch is an explicit restart boundary; callbacks from an older epoch after that
boundary make the interval incomplete. Go normalizes callback kinds; raw
callback and error bodies are not retained.

Required event contract `opencode-event-v3` also owns recovery evidence. After
the adapter applies a recovery cue, it acknowledges the delivery against the
same precreated interval and session; a later exact recovery command is linked
only under that owner. Older required-event contracts do not establish missing
cue rows as measured zero, so their recovery outcome is unavailable. Explicit
collection ownership keeps its existing database-target and ingress checks.

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

### Cost interpretation

ktx retains `step-finish.part.cost` without reinterpretation. For routine
analysis, this is the authoritative runtime-reported cost: the cost OpenCode
reported for the model step. Its upstream derivation does not need to be
resolved before the value can be aggregated, compared, or described as
OpenCode's reported cost. It is not a verified provider invoice or bank charge.

Price-derived estimated cost is separate. It multiplies complete
runtime-observed token fields by the applicable retained `prices.json` rates.
Neither value replaces the other, and a literal runtime-reported zero remains
zero even when the price-derived estimate is nonzero.

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
[`EventMessagePartUpdated`, `StepFinishPart`, and `AssistantMessage` types](https://github.com/anomalyco/opencode/blob/v1.18.5/packages/sdk/js/src/gen/types.gen.ts)
and the plugin's typed [`event` hook](https://github.com/anomalyco/opencode/blob/v1.18.5/packages/plugin/src/index.ts).
Those source blobs are byte-identical to the pinned 1.18.4 versions.

## Identity, deduplication, and ordering

- OpenCode's `sessionID`, `messageID`, and `part.id` are native identifiers
  local to OpenCode. They are not assumed to be globally unique or used as KTX
  record IDs.
- The persisted deduplication key is the exact
  `(runtime, sessionID, messageID, part.id)` tuple. The canonical `opencode`
  runtime namespace is assigned by the trusted telemetry command, not accepted
  from plugin input. Equal native tuples from another runtime remain distinct.
- Runtime version is provenance for the contract that interpreted the event,
  not identity. Re-delivery under a different OpenCode version does not create
  another logical observation. Provider, model, ordinal, KTX session ownership,
  measurements, and capture time are also evidence rather than key fields.
- The first valid occurrence is authoritative; later same-runtime occurrences
  with equal authoritative evidence are idempotent. On ordinary optional paths,
  a changed ordinal, identity, measurement, field state, reason, or runtime-
  version provenance is reported as a conflict and leaves the first row
  unchanged. A duplicate from the other collection path does not replace the
  first collection provenance. Required benchmark equality is scoped below.
- Outside required benchmark collection, accepted model steps receive
  contiguous local ordinals in plugin callback acceptance order, after
  duplicates and rejected events are removed.
  OpenCode 1.18.5 supplies no event sequence or event timestamp in this plugin
  event. The ordinal is local capture order, not a provider request number or
  provider timestamp.
- Concurrent callbacks must be serialized at the collector boundary before an
  ordinal is assigned. No ordering is inferred from lexicographic IDs.
- Required benchmark collection uses the ledger's global callback ordinal for
  accepted telemetry order. Duplicate equality ignores that allocated ordinal
  while comparing the stable runtime tuple and every normalized reason,
  token/cost value and state, provider/model value, and runtime-version field.
  Ordinary optional telemetry retains its existing stricter ordinal comparison.
- OpenCode exposes no provider request ID, retry relationship, or documented
  one-to-one model-step-to-provider-request mapping on these events. ktx does
  not synthesize them and does not label a model step as a provider request.

## Rejection and claim boundary

Malformed JSON, unsupported event or part types, a missing/non-object
`properties`, a missing/non-object `part`, and blank required IDs or reason are
rejected without an observation. Once an identifiable model step is accepted,
each numeric leaf is retained independently: a non-negative emitted number,
including zero, is `measured`; missing or null is `unknown`; and an invalid,
negative, non-finite, fractional token count, or unsafe integer is `malformed`.
The generated hook sends these states explicitly. A missing or null `tokens` or
`tokens.cache` container makes its children `unknown`; a non-object container
makes its children `malformed`, matching wrapper normalization.
Missing provider/model correlation remains absent. A later identity event or
configured identity does not backfill the accepted source event.

The cache values are runtime per-model-step token observations. They are
optional evidence, not raw provider responses. They do not establish provider
requests or request counts, retry identity, provider-internal cache hits,
misses, fills, eviction, isolation, request boundaries, or prefix composition.

## Current retained cache-observability matrix

The two current OpenCode evidence paths retain different contracts. Fields
described as direct below belong to one observation; attempt and manifest
context applies to a benchmark run but is not source-event evidence.

| Capability                          | Benchmark recorded stream and `model_steps`                                                                                                                                                                                                                                                                                               | Ordinary unwrapped session telemetry plugin                                                                                                                                                                                                                               |
| ----------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Source and version                  | Source is the OpenCode JSONL `step_finish` stream. OpenCode runtime version is attempt/manifest context (`cache_control.runtime_version` and `manifest.identities.opencode`), not a direct `model_steps` field.                                                                                                                           | The generated plugin and ingestion contract are pinned to OpenCode 1.18.5. Persisted observations directly carry `runtime=opencode` and `runtime_version=1.18.5`, assigned at ingestion.                                                                                  |
| Ordinal                             | One-based recorded-stream order of identifiable `step_finish` events after malformed JSON and other event types are skipped. An identifiable step with missing or malformed usage still consumes an ordinal.                                                                                                                              | One-based contiguous callback acceptance order after structurally rejected events and duplicates are removed. The wrapper applies the same rule to its OpenCode stream. It is local capture order, not a provider request number or timestamp.                            |
| Uncached input and cache tokens     | Direct per-step `tokens.input` and `tokens.cache.{read,write}` fields become `uncached_input`, `cache_read`, and `cache_write`. Explicit zero is `measured`; missing or JSON null is `unknown`; malformed is `malformed`. An attempt total is present only when every model step measured that field.                                     | The normalized database row uses the same per-field states. Missing or malformed one field does not erase measured siblings or the model step. Explicit zero remains measured.                                                                                            |
| Cache-control metadata              | Not reported on a `step_finish`; each `model_steps[].control` is empty. Mode, namespace hash, phase, execution order, idle gap, and associated model/provider/variant/runtime version live on attempt `cache_control`, with experiment-wide context in manifest `cache_control`.                                                          | Unavailable: the accepted plugin events and persisted observation have no cache-control field, and there is no attempt/manifest context on this path.                                                                                                                     |
| Provider, runtime, and model        | Optional provider/model are read directly from each stream part. Runtime and runtime version are not direct source-event fields; selected model plus provider/model/runtime-version control context live on the manifest or attempt.                                                                                                      | Runtime/version are assigned directly at ingestion. Provider/model are optional correlated context from a preceding matching assistant `message.updated`; both remain absent if it was not already accepted.                                                              |
| Retry and provider-request identity | Unavailable. `step_finish` supplies neither retry metadata nor authoritative provider-request identity; live `model_steps` remains `model_step`. A retained `native_request` schema record is historical/offline compatibility only, not supported collection.                                                                            | Unavailable. OpenCode 1.18.5 supplies no provider request ID or retry relationship on these events.                                                                                                                                                                       |
| Retained evidence and inspection    | The raw JSONL stream is retained and hashed; follow `attempts.jsonl` → `artifacts.stream` to inspect it. Normalized ordered observations are retained in that attempt's `model_steps`; `ktx bench validate <experiment-id>` checks retained artifacts and regeneration under `.ktx/experiments`.                                          | The normalized database observation retains runtime/version and `event_hook` or `wrapper_stream` collection provenance. Raw plugin events are not retained; wrapper bytes remain separately opt-in through `--record-stream`. Use the read-only inspection command below. |
| Incomplete or malformed behavior    | Malformed JSON and non-step events are skipped. An identifiable step is retained while each missing/null/malformed usage leaf is independently `unknown`/`malformed`; incomplete fields suppress only their attempt totals. Malformed optional `model_steps` loaded from `attempts.jsonl` are discarded without invalidating the attempt. | The same identifiable-step and independent-field rules apply. Structurally rejected records produce no row. Recording and persistence failures fail open and may leave no row.                                                                                            |
| Claim limit                         | Values are runtime stream model-step observations. They do not prove provider requests or request counts, provider-request/retry identity, provider-internal cache outcomes, request boundaries, isolation, or prefix composition.                                                                                                        | Values are runtime model-step observations, not raw provider responses. They carry the same claim limits.                                                                                                                                                                 |

## Inspect and verify offline

List retained observations for a ktx session without contacting a provider:

```bash
: "${KTX_TARGET_SESSION_ID:?set this to the ktx session ID to inspect}"
ktx telemetry list --session "$KTX_TARGET_SESSION_ID" --format text
```

Run the focused offline fixture matrix from a checkout:

```bash
mage opencodeTelemetryFixture
```

The target requires Bun to execute the generated TypeScript plugin, but requires
no network access. It covers accepted, rejected,
explicit-zero, missing, duplicate, wrapped, unwrapped, cross-runtime,
migration-failure, and concurrent behavior. The migration cases include fresh
databases, representative historical schema versions, populated version 4, and
transaction rollback.

## Database migration and failure behavior

Every database-backed telemetry command automatically prepares a supported
older schema before running. `telemetry list` opens read-only after readiness;
no manual migration command is a prerequisite. The migration preserves
unrelated session, tool-call, benchmark, compressed, and recorded-stream
evidence. Existing telemetry values become measured fields, retain their
runtime namespace and runtime-version provenance, and receive `legacy`
collection provenance because their original ingress was not stored.
Version-7 runtime event rows migrate as accepted model steps where applicable;
they cannot be reinterpreted as duplicate callback occurrences that were never
stored. Existing tool calls receive no reconstructed compression disposition;
statistics report them as `unobserved` until new hook evidence is retained.
New source and artifact rows retain independent closed-vocabulary producer
provenance with first-wins retry authority. Existing rows retain NULL producer
provenance; migration does not infer a command from stored content. Schema 11
does not rename physical compressed storage or add artifact kinds.

Migration is one SQLite transaction. Invalid legacy provenance, a copy error,
or another migration failure rolls back the table shape, rows, associations,
unrelated evidence, indexes, and schema version. There is no automatic downgrade
from version 11; rollback here means the failed upgrade leaves the prior database
intact so the cause can be corrected and the command retried. Back up the
database file before upgrading if an operator needs release-level rollback.

Failure behavior depends on the command:

- `ktx telemetry record` is optional recording. Database open, migration, and
  insert failures emit a diagnostic on stderr, produce no stdout, and fail open
  so the OpenCode operation continues.
- `ktx telemetry list` is read-only inspection. Database open or schema failure
  exits nonzero, emits the error on stderr, and produces no partial stdout.

## Troubleshooting

- Run `ktx init opencode --show` to inspect both candidate plugin locations. A
  file report is not proof of runtime activation.
- Re-run `ktx init opencode` or `ktx init opencode --project` to regenerate the
  file in the intended scope. If both scopes contain `ktx.ts`, remove
  the unintended copy with `ktx init opencode --uninstall`, then reinstall the
  intended scope.
- Ensure a readable `ktx` is on OpenCode's startup `PATH`. If it is absent, the
  plugin reports that it is disabled and registers neither hook. Reinstall ktx
  at a readable stable path and restart OpenCode.
- The plugin pins one canonical absolute path for one factory instance.
  Regenerate the plugin to pick up generated source changes. Replacing the
  executable or increasing the database schema does not require adapter
  regeneration.
- Every callback invokes the pinned executable. The plugin waits for the KTX
  result and does not impose a competing callback timeout.
- Ordinary `ktx run` intentionally disables the plugin hooks through
  `KTX_SESSION_ID`. The explicit `--collection=<id>` mode is the only wrapper
  path that keeps them active; it requires the generated pinned plugin and
  fails closed when ownership or correlation is absent.
- Telemetry and tool-hook failures are written to stderr and fail open. Tool
  output remains unchanged; optional telemetry persistence does not fail the
  OpenCode operation. A `database-unavailable` failure emits one bounded
  diagnostic with the callback surface, canonical database path, stable failure
  class, operation phase, elapsed wait,
  and correction. Other failures keep their existing diagnostics.
- If an event shape is rejected after an OpenCode upgrade, compare it with the
  pinned version above before changing the contract. Do not infer missing token
  values or provider-request semantics.
