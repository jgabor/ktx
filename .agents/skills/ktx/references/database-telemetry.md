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
- Estimates tokens per call as `ceil(character_count / 4)` before summing.
- Selects model-step telemetry independently by `captured_at`.
- Does not read benchmark aggregates.

`stats breakdown` reports the same window call and token totals with up to 100
token-weighted delivery groups. Groups use safe tool, command, subcommand,
profile, route, disposition, and raw-output-size dimensions; `other` reconciles
omitted groups exactly. It does not query or emit model-step telemetry.
`unobserved` means no retained disposition evidence exists; do not infer a cause
from absent artifact coverage.

Headline formulas:

```text
reduction_percent = (raw_estimated - delivered_estimated) / raw_estimated * 100
logical_input = uncached_input + cache_read + cache_write
cached_input_share = cache_read / logical_input
```

`with_compressed_artifact` is coverage, not a success rate. A missing artifact
does not state why raw output was delivered.

## Model-Step States

Each numeric telemetry field independently has one state:

- `measured`: a valid emitted value, including zero.
- `unknown`: missing or null.
- `malformed`: present but invalid.

These are source-observation states. Window totals are `measured` only when every
contributing row measured that field; an incomplete aggregate is serialized as
`missing`, while malformed takes precedence. Values are not reconstructed.

Cached-input share is `not_applicable` when measured logical input is zero. It
uses pooled window totals, not an average of per-step percentages.

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

Always report runtime/version and contributing model-step count beside a cache
metric. `stats summary` does not currently emit contributing session count,
first/last capture, or requested-window coverage. Separate SQL can describe the
observed capture span and counts within the summary interval, but it cannot
prove collector lifecycle or requested-window coverage. Without independent
collector-lifecycle evidence, report coverage as unavailable and label the
observations as a subset rather than a window-wide KPI.

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
