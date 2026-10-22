# OpenCode integration

This guide shows how to connect ktx to OpenCode: install the plugin, see what
it records, and fix common problems. The plugin compresses tool output and
can also record token usage for each model step.

ktx supports the OpenCode 1.18.5 plugin event contract at commit
[`e5cc278`](https://github.com/anomalyco/opencode/tree/e5cc278dec9294a627a7b05f47ce6a564408c1a2).
Later OpenCode versions are not covered until their event types are reviewed.

All live telemetry comes from OpenCode events. Provider and model identity
either comes from the runtime or is inferred from explicit runtime
configuration, with its source recorded. Provider documentation, pricing data,
and historical provider fixtures add context but are not telemetry.

## Install

`ktx init opencode` generates the standalone `ktx.ts` plugin and installs it.
It writes the file to one of OpenCode's candidate plugin locations:

| Scope   | Command                       | Candidate location                                                                                  |
| ------- | ----------------------------- | --------------------------------------------------------------------------------------------------- |
| Global  | `ktx init opencode`           | `$XDG_CONFIG_HOME/opencode/plugins/ktx.ts`, falling back to `$HOME/.config/opencode/plugins/ktx.ts` |
| Project | `ktx init opencode --project` | `./.opencode/plugins/ktx.ts`                                                                        |

After you upgrade ktx, run the same install command again to pick up changes
to the generated plugin. Installing is idempotent and overwrites only the
selected `ktx.ts` file. A database schema upgrade alone does not invalidate an
installed plugin. `ktx init opencode --show` reports the candidate files it
finds on disk. It does not show whether OpenCode loaded or activated either
file.

At startup, the plugin finds one `ktx` candidate, resolves it to a canonical
absolute path, and pins that path for every later callback. A later `PATH`
change can't switch it to a different binary.

The generated plugin declares two OpenCode hooks:

| Hook                 | Role                                                                                               |
| -------------------- | -------------------------------------------------------------------------------------------------- |
| `tool.execute.after` | Sends tool output to `ktx hook` for persistence and optional compression.                          |
| `event`              | Normalizes accepted model-step events and sends optional token evidence to `ktx telemetry record`. |

For the OpenCode `tool_use` shape it accepts, the wrapper also reads
`part.state.time.start` and `end` as tool-call latency:

- A valid pair that is not reversed is measured runtime evidence in
  milliseconds. Zero counts as measured.
- Missing `state.time` is unknown.
- A partial, non-numeric, or reversed pair is malformed runtime evidence and
  has no numeric latency.

OpenCode plugin `tool.execute.after` callbacks, Codex events, and flat wrapper
events don't supply this timing, so their latency stays unknown unless a
supported runtime event supplies it. ktx does not measure elapsed time itself
or reconstruct historical authority.

When OpenCode runs under `ktx run`, the wrapper sets `KTX_SESSION_ID`. The
plugin then turns both hooks into no-ops, so the wrapper is the only evidence
path. The wrapper converts identifiable `step_finish` records into the same
model-step evidence contract and still passes the original JSONL through
unchanged. Without the wrapper, the plugin handles both hooks. If a model step
arrives through both paths, the stored runtime tuple removes the duplicate. The
first accepted row stays authoritative, and so does its collection provenance.

`ktx run --collection=<16-letter-id> opencode ...` is a separate, explicit
same-population path. stdout stays the wrapper's unchanged JSONL pass-through,
but the pinned OpenCode plugin stays active. The plugin and hook own the
transformed delivery and the append-only source row. The wrapper owns the
session lifecycle. It keeps runtime timing separately, and only after the
hook-owned source resolves to the same session and native source identity. To
keep all run intervals together, reuse the same collection ID across runs.

The collection fails closed on any of these:

- the database target or ingress disagrees
- the ID is invalid
- hook ownership is missing
- correlation is contradictory
- timing is duplicated

Continuity reports every gap between runs, interruption, conflicting boundary,
and missing closure. Missing activity is never treated as coverage.

When this explicit path delivers a lossy artifact with a recovery cue, the
generated adapter acknowledges delivery only after it assigns the hook envelope
output. Metadata that applies only to new deliveries links the delivery to its
collection, wrapper session, trusted OpenCode identity, source,
and first artifact. A later trusted OpenCode bash callback counts as a recovery
attempt only if it runs the exact `ktx read <source-id>` command and has an
explicit exit result. On success, ktx parses the output as the stored source
record and compares it with the retained original. Byte-exactness is never
inferred from malformed, composed, unlinked, unknown, or failed activity.
Repeated attempts stay separate but share one cue.

On the ordinary unwrapped path, the plugin puts `event` and
`tool.execute.after` callbacks into one queue in arrival order. It does not
start ktx work for a callback until the earlier callback finishes. A hook
changes the output the agent sees only after `ktx hook` returns its envelope.
ktx accepts the source evidence in that envelope before it returns any
transformed output.

Benchmark collection does not reuse `KTX_SESSION_ID`. Instead, it supplies the
exact precreated `KTX_BENCH_SESSION_ID` owner, with a required event interval
and contract. In that mode, the hooks:

- stay active
- never read or write parent-process session files
- serialize every callback
- reject ownership, acknowledgement, or persistence failures instead of failing
  open

A collector-open acknowledgement must carry its allocated global ordinal, which
must be positive. Every event acknowledgement must carry exactly the previous
acknowledged global ordinal plus one. Gaps, backwards values, and values that
are not safe integers fail the callback.

Every callback occurrence gets and keeps one global ordinal, even an exact
duplicate model step. The ledger sorts model steps into three groups:

- Accepted.
- Exact duplicate. It acknowledges success but does not add a second model-step
  measurement.
- Conflicting duplicate. The occurrence is kept and the first measurement is
  preserved, but the interval can no longer be sealed as complete and the
  callback fails.

A failed callback does not leave the serialized plugin queue stuck in a
rejected state, so a later callback can still expose an epoch or global-order
gap.

An unacknowledged callback keeps its original payload, observation time, and
epoch tuple so it can be redelivered unchanged. A retry does not allocate a
second tuple, but an acknowledged duplicate callback still does. Retries keep
the strict global-order checks. They do not repair an interval after later
callbacks expose an ordering gap. Missing or malformed required measurements
are still stored as evidence, but the callback fails instead of acknowledging
them as benchmark-ready.

Before a callback succeeds or tool output changes, acknowledgements and tool
envelopes must also match the exact owner, interval, epoch, epoch ordinal,
normalized kind, native tool-call identity, and contract. A later, sequential
collector epoch marks an explicit restart boundary. Callbacks from an older
epoch that arrive after that boundary make the interval incomplete. The Go side
normalizes callback kinds, and raw callback and error bodies are not retained.

The required event contract `opencode-event-v3` also covers recovery evidence.
After the adapter applies a recovery cue, it acknowledges the delivery against
the same precreated interval and session. A later exact recovery command is
linked only under that owner. Under older required-event contracts, missing cue
rows do not count as a measured zero, so their recovery outcome is unavailable.
Explicit collection ownership keeps its existing database-target and ingress
checks.

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

The session, message, and part IDs on the part are authoritative. Token fields
must be non-negative safe JSON integers no greater than `2^53 - 1`. `cost` must
be a non-negative JSON number. An explicit zero is a measurement, not missing
data. Extra fields are ignored, so new OpenCode metadata does not invalidate
the event.

### Cost interpretation

ktx keeps `step-finish.part.cost` as reported, without reinterpreting it. For
routine analysis, this is the authoritative runtime-reported cost: the cost
OpenCode reported for the model step. You don't need to know how OpenCode
derived it before you aggregate it, compare it, or describe it as OpenCode's
reported cost. It is not a verified provider invoice or bank charge.

The price-derived estimated cost is a separate value. It multiplies the
complete runtime-observed token fields by the matching retained `prices.json`
rates. Neither value replaces the other. A literal runtime-reported zero stays
zero even when the price-derived estimate is nonzero.

Provider and model are not fields on `StepFinishPart`. They come from an
earlier assistant `message.updated` event whose `properties.info.id` and
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

If ktx has not already accepted that identity event, provider and model stay
absent. A later event does not backfill an observation that was already
accepted.

The pinned shapes come from OpenCode's generated
[`EventMessagePartUpdated`, `StepFinishPart`, and `AssistantMessage` types](https://github.com/anomalyco/opencode/blob/v1.18.5/packages/sdk/js/src/gen/types.gen.ts)
and the plugin's typed [`event` hook](https://github.com/anomalyco/opencode/blob/v1.18.5/packages/plugin/src/index.ts).
Those source files are byte-identical to the pinned 1.18.4 versions.

## Identity, deduplication, and ordering

- OpenCode's `sessionID`, `messageID`, and `part.id` are native identifiers
  local to OpenCode. ktx does not assume they are globally unique and does not
  use them as ktx record IDs.
- The stored deduplication key is the exact
  `(runtime, sessionID, messageID, part.id)` tuple. The trusted telemetry
  command assigns the canonical `opencode` runtime namespace. Plugin input
  can't set it. Equal native tuples from another runtime stay distinct.
- Runtime version records which contract interpreted the event. It is
  provenance, not identity. Redelivery under a different OpenCode version does
  not create another logical observation. Provider, model, ordinal, ktx session
  ownership, measurements, and capture time are also evidence, not key fields.
- The first valid occurrence is authoritative. Later occurrences from the same
  runtime with equal authoritative evidence are idempotent. On ordinary
  optional paths, a changed ordinal, identity, measurement, field state,
  reason, or runtime-version provenance is reported as a conflict and leaves
  the first row unchanged. A duplicate from the other collection path does not
  replace the first collection provenance. Equality for required benchmark
  collection is covered below.
- Outside required benchmark collection, observed model steps get contiguous
  local ordinals in first-observation order, after duplicates and rejected
  events are removed. In OpenCode 1.18.5, this plugin event has no event
  sequence or event timestamp. The ordinal is local capture order, not a
  provider request number or provider timestamp.
- Observation is not durable acceptance. The plugin sends `telemetry record
--ack` and requires a versioned `ktx.telemetry-ack.v1` response with `ok: true`,
  `durable: true`, and matching normalized evidence, including exact runtime,
  runtime-version, and collection-source provenance. Exit code zero alone is
  not enough, because optional persistence can fail without failing the agent.
  `telemetry record` without `--ack` keeps its quiet, best-effort interface.
- If a write fails or its outcome is uncertain, the collector keeps the first
  normalized payload and local capture time. An identical redelivery retries
  that payload, even when callbacks overlap. A conflict can't replace or add to
  it. An exact database duplicate also acknowledges persistence without adding
  a second measurement. There is no automatic retry loop. Pending evidence and
  late identity associations last only as long as the collector instance. A
  process restart does not recover collector memory that was not yet
  persisted. ktx does not claim that any memory eviction is safe for callbacks
  that arrive arbitrarily late.
- Concurrent callbacks must be serialized at the collector boundary before an
  ordinal is assigned. ktx never infers order from how IDs sort.
- Required benchmark collection orders accepted telemetry by the ledger's
  global callback ordinal. When it checks two occurrences for equality, it
  ignores that allocated ordinal. It still compares the stable runtime tuple
  and every normalized reason, token/cost value and state, provider/model
  value, and runtime-version field. Ordinary optional telemetry keeps its
  existing stricter ordinal comparison.
- These events carry no provider request ID, no retry relationship, and no
  documented one-to-one mapping from model step to provider request. ktx does
  not make them up and does not label a model step as a provider request.

## Rejection and claim boundary

ktx rejects these without recording an observation: malformed JSON,
unsupported event or part types, a missing or non-object `properties`, a
missing or non-object `part`, and blank required IDs or reason.

Once an identifiable model step is accepted, ktx keeps each numeric leaf on
its own:

- A non-negative emitted number, including zero, is `measured`.
- A missing or null value is `unknown`.
- An invalid, negative, non-finite, fractional token count, or unsafe integer
  is `malformed`.

The generated hook sends these states explicitly. If a `tokens` or
`tokens.cache` container is missing or null, its children are `unknown`. If it
is not an object, its children are `malformed`. This matches how the wrapper
normalizes. Numeric strings are malformed. Integral JSON decimal and exponent
forms use the same safe-integer measurement rules as the runtime's numeric
values. Missing provider/model correlation stays absent. A later identity event
or configured identity does not backfill the accepted source event.

The cache values are token counts the runtime observed for each model step.
They are optional evidence, not raw provider responses. They do not prove
provider requests or request counts, retry identity, provider-internal cache
hits, misses, fills, eviction, isolation, request boundaries, or prefix
composition.

## Current retained cache-observability matrix

The two current OpenCode evidence paths keep different contracts. A field
described as direct below belongs to one observation. Attempt and manifest
context applies to a benchmark run, but it is not source-event evidence.

| Capability                          | Benchmark recorded stream and `model_steps`                                                                                                                                                                                                                                                                                               | Ordinary unwrapped session telemetry plugin                                                                                                                                                                                                                               |
| ----------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Source and version                  | Source is the OpenCode JSONL `step_finish` stream. OpenCode runtime version is attempt/manifest context (`cache_control.runtime_version` and `manifest.identities.opencode`), not a direct `model_steps` field.                                                                                                                           | The generated plugin and ingestion contract are pinned to OpenCode 1.18.5. Persisted observations directly carry `runtime=opencode` and `runtime_version=1.18.5`, assigned at ingestion.                                                                                  |
| Ordinal                             | One-based recorded-stream order of identifiable `step_finish` events after malformed JSON and other event types are skipped. An identifiable step with missing or malformed usage still consumes an ordinal.                                                                                                                              | One-based contiguous first-observation order after structurally rejected events and duplicates are removed. The wrapper applies the same rule. Failed persistence leaves the original ordinal pending for redelivery; it is not a provider request number or timestamp.   |
| Uncached input and cache tokens     | Direct per-step `tokens.input` and `tokens.cache.{read,write}` fields become `uncached_input`, `cache_read`, and `cache_write`. Explicit zero is `measured`; missing or JSON null is `unknown`; malformed is `malformed`. An attempt total is present only when every model step measured that field.                                     | The normalized database row uses the same per-field states. Missing or malformed one field does not erase measured siblings or the model step. Explicit zero remains measured.                                                                                            |
| Cache-control metadata              | Not reported on a `step_finish`; each `model_steps[].control` is empty. Mode, namespace hash, phase, execution order, idle gap, and associated model/provider/variant/runtime version live on attempt `cache_control`, with experiment-wide context in manifest `cache_control`.                                                          | Unavailable: the accepted plugin events and persisted observation have no cache-control field, and there is no attempt/manifest context on this path.                                                                                                                     |
| Provider, runtime, and model        | Optional provider/model are read directly from each stream part. Runtime and runtime version are not direct source-event fields; selected model plus provider/model/runtime-version control context live on the manifest or attempt.                                                                                                      | Runtime/version are assigned directly at ingestion. Provider/model are optional correlated context from a preceding matching assistant `message.updated`; both remain absent if it was not already accepted.                                                              |
| Retry and provider-request identity | Unavailable. `step_finish` supplies neither retry metadata nor authoritative provider-request identity; live `model_steps` remains `model_step`. A retained `native_request` schema record is historical/offline compatibility only, not supported collection.                                                                            | Unavailable. OpenCode 1.18.5 supplies no provider request ID or retry relationship on these events.                                                                                                                                                                       |
| Retained evidence and inspection    | The raw JSONL stream is retained and hashed; to inspect it, follow `attempts.jsonl` to `artifacts.stream`. Normalized ordered observations are retained in that attempt's `model_steps`; `ktx bench validate <experiment-id>` checks retained artifacts and regeneration under `.ktx/experiments`.                                        | The normalized database observation retains runtime/version and `event_hook` or `wrapper_stream` collection provenance. Raw plugin events are not retained; wrapper bytes remain separately opt-in through `--record-stream`. Use the read-only inspection command below. |
| Incomplete or malformed behavior    | Malformed JSON and non-step events are skipped. An identifiable step is retained while each missing/null/malformed usage leaf is independently `unknown`/`malformed`; incomplete fields suppress only their attempt totals. Malformed optional `model_steps` loaded from `attempts.jsonl` are discarded without invalidating the attempt. | The same identifiable-step and independent-field rules apply. Structurally rejected records produce no row. Recording and persistence failures fail open and may leave no row.                                                                                            |
| Claim limit                         | Values are runtime stream model-step observations. They do not prove provider requests or request counts, provider-request/retry identity, provider-internal cache outcomes, request boundaries, isolation, or prefix composition.                                                                                                        | Values are runtime model-step observations, not raw provider responses. They carry the same claim limits.                                                                                                                                                                 |

## Inspect and verify offline

List retained observations for a ktx session without contacting a provider:

```bash
: "${KTX_TARGET_SESSION_ID:?set this to the ktx session ID to inspect}"
ktx telemetry list --session "$KTX_TARGET_SESSION_ID" --format text
```

From a checkout, run the offline fixture matrix:

```bash
mage opencodeTelemetryFixture
```

The target needs Bun to run the generated TypeScript plugin, but it needs no
network access. It covers accepted, rejected, explicit-zero, missing,
duplicate, wrapped, unwrapped, cross-runtime, migration-failure, and concurrent
behavior. The migration cases include fresh
databases, representative historical schema versions, populated version 4, and
transaction rollback.

## Database migration and failure behavior

Every database-backed telemetry command automatically prepares a supported
older schema before it runs. `telemetry list` opens the database read-only once
it is ready. You don't need to run a migration command first.

Migration keeps unrelated session, tool-call, benchmark, compressed, and
recorded-stream evidence. Existing telemetry values become measured fields and
keep their runtime namespace and runtime-version provenance. They get `legacy`
collection provenance because their original ingress was not stored. Where it
applies, version-7 runtime event rows migrate as accepted model steps. They
can't be reinterpreted as duplicate callback occurrences, because those were
never stored. Existing tool calls get no reconstructed compression
disposition. Statistics report them as `unobserved` until new hook evidence is
retained.

New source and artifact rows each record their producer provenance from a
closed vocabulary. On retry, the first value wins. Existing rows keep NULL
producer provenance, because migration does not guess a command from stored
content. Schema 11 does not rename physical compressed storage or add artifact
kinds.

Migration runs as one SQLite transaction. Invalid legacy provenance, a copy
error, or any other migration failure rolls back the table shape, rows,
associations, unrelated evidence, indexes, and schema version. There is no
automatic downgrade from version 11. Here, rollback means a failed upgrade
leaves the previous database intact, so you can fix the cause and run the
command again. If you may need to roll back to an earlier release, back up the
database file before upgrading.

Failure behavior depends on the command:

- `ktx telemetry record` is optional recording. If opening the database,
  migration, or the insert fails, it prints a diagnostic on stderr, writes
  nothing to stdout, and fails open so the OpenCode operation continues.
- `ktx telemetry list` is read-only inspection. On a database open or schema
  failure, it exits nonzero, prints the error on stderr, and writes no partial
  stdout.

## Troubleshooting

- Run `ktx init opencode --show` to check both candidate plugin locations.
  Finding a file does not prove that OpenCode activated it.
- Run `ktx init opencode` or `ktx init opencode --project` again to regenerate
  the file in the scope you want. If both scopes contain `ktx.ts`, remove the
  unwanted copy with `ktx init opencode --uninstall`, then reinstall the scope
  you want.
- Make sure a readable `ktx` is on the `PATH` that OpenCode starts with. If it
  is missing, the plugin reports that it is disabled and registers neither
  hook. Reinstall ktx at a stable, readable path and restart OpenCode.
- Each plugin factory instance pins one canonical absolute path. Regenerate
  the plugin to pick up changes to the generated source. You don't need to
  regenerate the adapter after replacing the executable or upgrading the
  database schema.
- Every callback runs the pinned executable. The plugin waits for the ktx
  result and does not add a competing callback timeout.
- Ordinary `ktx run` turns off the plugin hooks on purpose, through
  `KTX_SESSION_ID`. The explicit `--collection=<id>` mode is the only wrapper
  path that keeps them on. It requires the generated pinned plugin and fails
  closed when ownership or correlation is missing.
- Telemetry and tool-hook failures go to stderr and fail open. Tool output stays
  unchanged, and optional telemetry persistence does not fail the OpenCode
  operation. A `database-unavailable` failure prints one bounded diagnostic with
  the callback surface, canonical database path, stable failure class,
  operation phase, elapsed wait, and correction. Other failures keep their
  existing diagnostics.
- If ktx rejects an event shape after an OpenCode upgrade, compare it with the
  pinned version above before you change the contract. Do not infer missing
  token values or provider-request semantics.
