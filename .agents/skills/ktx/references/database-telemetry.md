# Database, Stats, and Telemetry

This derived task summary is not a canonical contract. Project memory owns
intent and scope, implementation and tests establish current behavior, and
public docs/help must remain synchronized contract surfaces. This reference
separates safe database access from write-capable operations and defines what
live telemetry can support.

## Database Command Classes

### DB-free

Help, version, adapter/config/plugin management, `binary-capabilities`, `db
path`, prices path/update, replay-file, comparison preflight, corpus generation,
and evidence preflight do not open the ktx database.

### Genuinely read-only

`get`, `read`, `list`, `stats`, `decompress`, `bench report`, and
`telemetry list` open an existing current-schema database with SQLite `mode=ro`
and `query_only`. They never create or migrate persistent state.

An absent database is logically empty for collection queries. Read-only commands
do not create the database, parent directory, WAL, or SHM file.

### Existing-artifact inspection

`doctor`, `db report`, `bench export`, `bench validate`, and retained-summary
operations inspect existing evidence without migration.

### Write or migration capable

`write`, `compress`, `hook`, `run`, `telemetry record`, normal benchmark run or
replay, and collector commands can create a database and transactionally migrate
an older supported schema. `db migrate` is the explicit upgrade command.

Normal benchmark `compare` and `rank` create fresh isolated owner databases per
attempt and reject attempt-directory reuse.

## Safe Scope Rules

- Development, test, CI, and migration rehearsal must never discover or inspect
  the ambient live database.
- Standard `mage test`, `mage check`, and `mage verify` create disposable HOME
  and XDG roots and unset `KTX_DB`.
- For focused manual verification, do the same. For migration rehearsal, copy a
  seed into a disposable explicit path first.
- A user request to analyze their live telemetry is an operator task, not a
  development test. Keep it read-only and aggregate-safe.
- Do not copy prompts, raw outputs, session IDs, or identifiers into reports
  unless explicitly required and safe to disclose.

## Schema Overview

| Table                     | Authority                                                                         |
| ------------------------- | --------------------------------------------------------------------------------- |
| `sessions`                | KTX session identity, agent label, lifecycle, call count                          |
| `tool_calls`              | Append-only original tool inputs/outputs and optional call metadata               |
| `compressed`              | Derived output referencing one source tool call and pipeline metadata             |
| `bench_runs`              | Persisted benchmark result rows                                                   |
| `recorded_streams`        | Optional retained agent JSONL for replay/export                                   |
| `session_telemetry`       | OpenCode-normalized model-step token, cost, identity, state, and capture evidence |
| `runtime_event_intervals` | Required benchmark collector interval ownership and lifecycle                     |
| `runtime_events`          | Required benchmark callback order and duplicate classification                    |

Original tool calls are append-only. Compressed artifacts remain separate so an
original is always recoverable.

## Schema Compatibility

Read-only queries require the current schema and never migrate. An older schema
returns exit `5` with `schema-migration-required`. Confirm the path, then run:

```bash
: "${KTX_OWNED_DB:?set a preconfirmed operator-owned database path}"
ktx db --path "$KTX_OWNED_DB" migrate
```

The explicit target guard is an operator-scope check, not a product policy
change. Normal write-capable commands retain their safe transactional forward
migration behavior. Never use an ambient live database for development, test,
CI, or migration rehearsal.

A newer schema returns `schema-incompatible`. Install a binary supporting that
schema or newer. Never lower `PRAGMA user_version`.

Use DB-free identity diagnostics when binary/schema provenance matters:

```bash
ktx binary-capabilities --format json
```

## Stats Semantics

```bash
ktx stats --limit 20 --format json
ktx stats summary --window 72h --format json
ktx stats breakdown --window 72h --format json
```

`stats` groups and ranks by delivered estimated tokens. Uncompressed calls use
raw output as delivered output. Trivial groups with fewer than two calls or 50
delivered estimated tokens are excluded.

`stats summary`:

- Captures one inclusive trailing interval `[from, to]`.
- Selects calls with non-null output and RFC3339 timestamps in that interval.
- Uses the first compressed artifact by timestamp and smallest ID tie-breaker.
- Uses raw output when no artifact exists.
- Does not double-count recompressions.
- Reports `prompt_cache_token_hit_rate_pct` in percentage units from 0 to 100
  when `measured`; measured zero remains `0`.
- Unavailable states retain the state and use `null` for the value.
- `contributing_model_steps` is a count.
- The percentage pools all accepted providers and models under OpenCode 1.18.5,
  including missing provider/model identities, by summing token buckets. It does
  not average per-identity percentages.
- The percentage is token-weighted runtime reporting, not a provider request
  hit rate or cache-internal claim.

`stats breakdown` reports the same window call and token totals with up to 100
token-weighted delivery groups. Groups use safe tool, command, subcommand,
profile, route, disposition, and raw-output-size dimensions; `other` reconciles
omitted groups exactly. It does not query or emit model-step telemetry.
`unobserved` means no retained disposition evidence exists; do not infer a cause
from absent artifact coverage.

Summary schema 10 is compact. The breakdown schema 4 includes aggregate `outcomes`.
Both report trusted ingress attribution coverage over the same output-bearing
call population. Historical and unverified sessions remain unknown. Text output
has no separate schema version.
Retained schema 6 summaries remain historical bytes and are not migrated or
rewritten by the read-only summary query. The breakdown population is the same
calls counted by `total_with_output`: non-null output and a valid RFC3339
timestamp in the inclusive window. Persisted `is_error=false` is success, `true`
is error, and NULL or invalid is unknown. Persisted `exit_code` is diagnostic
only.

Both schemas classify latency as measured, unknown, or malformed. Measured
values have runtime authority and millisecond units. Coverage uses all calls in
the requested window; contributing UTC RFC3339Nano bounds and the distribution
use measured calls only. Mixed offsets are ordered as instants. With no measured
values, state is unavailable and bounds and distribution values are null.
Unsupported ingress remains unknown; ktx does not infer timing.

Breakdown formulas:

```text
known_calls = successful_calls + error_calls
total_calls = known_calls + unknown_calls
error_rate_pct_of_known = error_calls / known_calls * 100
outcome_coverage_pct = known_calls / total_calls * 100
```

Empty outcome percentages are JSON `null` and text `-`. An all-unknown
population has a null error rate and zero coverage. An all-known population with
no errors has zero error rate and 100 percent coverage. Breakdown has no
per-group outcome rates. Text has no separate schema version.

`with_compressed_artifact` is coverage, not a success rate. A missing artifact
does not state why raw output was delivered.

`ktx db report` emits JSON report schema 3 over every tool-call row. Its
`failure_metrics` uses the same classification and formulas, with existing
two-decimal rounding. Malformed outcomes remain unknown; `exit_codes` remains
diagnostic. Empty/all-unknown rates follow the live null/zero rules. The report
is read-only and has no text schema. Schema 3 supersedes its predecessor without
a compatibility bridge.

## Model-Step States

Each numeric telemetry field independently has one state:

- `measured`: a valid emitted value, including zero.
- `unknown`: missing or null.
- `malformed`: present but invalid.

These are source-observation states. Window totals are `measured` only when every
contributing row measured that field; an incomplete aggregate is serialized as
`missing`, while malformed takes precedence. Values are not reconstructed.

Runtime-reported cost is the `cost` value emitted by the supported runtime and
retained by ktx without reinterpretation. Treat it as the runtime's authoritative
reported cost for normal analysis; do not reopen its upstream derivation before
aggregating or comparing it. This authority does not make it a verified provider
invoice or bank charge. Keep it separate from price-derived estimated cost,
which applies retained `prices.json` rates to complete runtime-observed token
fields.

## OpenCode Contract

The generated OpenCode plugin currently supports the reviewed OpenCode 1.18.5
event contract. It accepts identifiable `message.part.updated` events whose part
type is `step-finish` and retains:

- Runtime session, message, and part identity.
- Local accepted-event ordinal.
- Reason and capture provenance.
- Uncached input, output, reasoning, cache read, cache write, and cost states.
- Optional correlated provider and model identity.

The plugin has no runtime event timestamp for these events; `captured_at` is ktx
collection time. Ordinal is local capture order, not provider request order.

When wrapper collection is active, generated hooks no-op and the wrapper records
the same normalized model-step contract to avoid duplicates.

## Claim Boundaries

Runtime model-step evidence does not prove:

- Provider requests or request counts.
- Retry identity or one model step per provider request.
- Provider cache hits, misses, fills, eviction, eligibility, isolation, or TTL.
- Provider-visible prefix composition or request boundaries.
- Billing meaning of a literal runtime-reported cost zero.
- Causal attribution of token movement to compression.

Character-derived tool-output estimates and runtime-reported model tokens are
different evidence classes. Keep them parallel; do not add or subtract them.

## Truthful Live Analysis Workflow

1. Capture the authoritative summary first:

```bash
ktx stats summary --window 72h --format json
```

2. Reuse its exact `window.from` and `window.to` for direct read-only database
   breakdowns.
3. Query only aggregate-safe fields unless raw content is explicitly needed.
4. Attempt to reconcile summary totals before publishing derived breakdowns.
   The CLI summary and later SQL query are non-transactional views, not one
   common snapshot, so equality is not guaranteed and is not exact reproduction.
5. Report metadata coverage before reporting error, cost, latency, agent, or
   cache KPIs.
6. State that the database is live and later queries may differ. Do not infer
   provider behavior or causally attribute movement to compression.

For direct SQL, use an explicit read-only open:

```bash
: "${KTX_READONLY_DB:?set a preconfirmed database target}"
sqlite3 -readonly "$KTX_READONLY_DB" "PRAGMA user_version;"
```

Set `KTX_READONLY_DB` from an explicit operator-owned target before opening it;
do not discover an ambient live database during development or verification.
`-readonly` constrains this SQLite open but does not establish target ownership.
Do not use direct SQL to bypass schema compatibility, migration, or claim
boundaries enforced by the CLI.
