# Database safety contract

ktx database access is forward-only. Normal database-backed operations must
prepare every supported older schema automatically. Query and inspection
operations remain read-only after readiness, but readiness itself may migrate
an older database. Development, tests, CI, and rehearsal must never access the
ambient live database. The authoritative state matrix, deadline ownership, and
release budgets are in the [schema readiness contract](schema-readiness.md).

Critical-path evidence classes, participant ownership, hook acceptance, and
the database-unavailable result are defined in the
[critical-path performance and availability contract](critical-path-performance.md).

## Command classes

| Class                                     | Surfaces                                                                                                                                        | Database behavior                                                                                                                                                                                                                                                                                                                              |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| DB-free                                   | Help, version, init, config and plugin management, `db path`, prices, replay-file, comparison preflight, and corpus generation                  | Does not create, inspect, open, or migrate an ambient database.                                                                                                                                                                                                                                                                                |
| Query operation                           | `get`, `read`, `list`, `stats`, `decompress`; `bench report`; `telemetry list`                                                                  | An absent database is represented by an ephemeral empty current schema. A supported older database is prepared automatically, then the requested operation opens with SQLite `mode=ro` and `query_only`.                                                                                                                                       |
| Existing-artifact inspection              | `doctor`; `db report`; `bench export` and `bench validate`                                                                                      | Preserves command-specific missing-input behavior. A supported older database is prepared automatically, then inspection is read-only.                                                                                                                                                                                                         |
| Creation and migration capable            | `write`, `compress`, `hook`, `run`; telemetry record and collector commands; normal benchmark run and replay; collector-enabled benchmark agent | May create a missing database and transactionally migrate an older supported schema before writing. A current-schema hook first opens without creation, migration, or exclusive coordination, then rechecks the schema inside its evidence-writer window. Missing and older supported databases fall back to the normal coordinated Open path. |
| Explicit migration                        | `ktx db migrate`                                                                                                                                | Retained for diagnosis and rehearsal, not as a prerequisite for normal use. It upgrades an existing older database and does not create an absent database.                                                                                                                                                                                     |
| Isolated owner databases                  | Normal benchmark compare and rank                                                                                                               | Creates a fresh database for each attempt and rejects attempt-directory reuse.                                                                                                                                                                                                                                                                 |
| Forbidden ambient-live development access | Any development, test, CI, or rehearsal command that could discover the normal user database                                                    | Use a fresh disposable application root and, for migration rehearsal, an explicitly seeded temporary database.                                                                                                                                                                                                                                 |

Path precedence remains explicit flag, then `KTX_DB`, then the default
`$XDG_DATA_HOME/ktx/ktx.db`. `KTX_DB` selects a path; it does not prove that the
path is disposable or safe.

## Query behavior

Typed queries require the current schema. Readiness prepares a supported older
database automatically before the query opens read-only. No supported stale
schema returns `schema-migration-required`, and no normal workflow requires a
manual migration command. This supersedes the previous read-only/manual-
migration policy. An absent database remains absent and uses the existing
logical-empty behavior.

After readiness releases its exclusive lease, the reopened query reads the
schema version again under its fresh shared lease. An intervening newer upgrade
returns the typed schema-incompatible error instead of exposing a stale query
handle. The shared lease remains held for the query handle's lifetime.

An absent database is logically empty for read-only commands. `list`, `stats`,
and `bench report` return their empty results; record lookup, decompression, and
session telemetry inspection return their normal not-found result. No parent
directory, database, WAL, or SHM file is created.

## Forward migration

Normal write-capable commands and `ktx db migrate` use the same migration code.
The implementation reads `PRAGMA user_version`, returns immediately for the
current schema, and rejects a newer schema. An older supported schema is checked
again under an immediate SQLite transaction. Most historical migrations publish
their changes and the new version in one transaction. Schema 15 preparation is
the exception: it commits resumable row batches while `user_version` remains 15.
Each failed or interrupted transaction rolls back only its current unit; prior
committed units remain valid and the next database-backed invocation resumes
from the durable cursor. Final publication of schema 16 is atomic.

Every ordinary managed writer rechecks `user_version` inside its exclusive
coordination lease and each retried SQLite transaction, before evidence mutation.
A current-schema hook rechecks it after acquiring its exclusive evidence-writer
window and before source acceptance. If a newer binary upgraded the database
after either writer opened it, the stale writer returns the normal typed
schema-incompatible error and commits no new evidence. Migration's internal
transactions remain separate so supported automatic upgrades still work;
in-memory and explicitly caller-owned raw inspection handles keep their existing
contracts.

Schema 9 adds `compression_dispositions`, a source-keyed operational projection
containing only selected profile, route provenance, bounded disposition,
optional authoritative artifact ID, and observation time. Migration does not
backfill historical calls or infer outcomes from artifacts. Missing rows remain
`unobserved` in statistics. Runtime recording may promote a retained
non-artifact disposition to the first authoritative artifact, but never
downgrades or replaces artifact authority.

Schema 10 adds explicit latency state, unit, and authority to `tool_calls`.
Migration preserves historical numeric latency, including zero, but labels it
unknown with unknown authority because the old schema did not retain enough
proof to establish runtime authority. Missing history is also unknown. New
runtime-authoritative evidence uses milliseconds and distinguishes measured,
unknown, and malformed observations.

Schema 11 adds nullable, independent closed-vocabulary producer provenance to
newly accepted source and compressed-artifact rows. Exact retries preserve the
first accepted producer. Migration leaves historical provenance NULL and does
not infer a producer from stored content. Source and artifact ownership remain
independent; the migration adds no provenance index, does not rename physical
compressed storage, and does not add artifact kinds.

Schema 12 adds explicit attribution authority to sessions. Existing labels are
preserved but migrate as `unknown`; migration does not infer attribution from
historical text. New rows can distinguish identified attribution from unknown
authority independently of the label.

Schema 13 adds two metadata-only tables. `collection_intervals` retains the
SQLite-resolved database target, normalized selected supported-ingress set, and
opening time. `ingress_continuity` retains only interval, ingress and instance
identity, started/stopped/interrupted state, observation time, and process
authority. Migration creates both tables empty: it does not backfill lifecycle
evidence or infer continuity from historical sessions, tool calls, or other
activity. The wrapper records its owned `run` process boundary; hook and write
activity is not promoted into continuity evidence. These tables are created
only by readiness, normal write-capable operations, or explicit migration.
After readiness, an operation using a read-only open never creates, migrates,
backfills, or updates them.
Continuity requires authoritative stopped or interrupted closure evidence that
spans the requested end. A start without that evidence reports a
`missing_closure` gap clipped to the requested span.

Schema 14 adds future-only `runtime_source_observations`,
`recovery_cue_deliveries`, and `recovery_attempts`. Runtime observations link one
explicit collection and wrapper session to an existing OpenCode source owned by
the hook, and retain runtime timing plus delivered byte count separately from
the append-only source row. Cue delivery is acknowledged only after the
generated adapter assigns a
lossy hook envelope carrying a recovery cue. Recovery attempts link exact
supported `ktx read <source-id>` shell calls with authoritative outcomes to that
delivery and retain byte-exactness for successful reads. Repeated attempts stay
separate. Migration creates all three tables empty and leaves historical timing,
delivery, and recovery unknown.
Explicit collection setup verifies the SQLite-resolved database target and the
exact `run` ingress set before reuse; mismatches are rejected. Read-only opens
require schema 14.

Schema 15 adds nullable nanosecond epoch forms of tool-call and session-telemetry
timestamps plus indexes used by bounded stats queries. Migration parses valid
RFC3339 timestamps into the sortable columns and leaves malformed timestamps
NULL, without changing the retained text or other evidence. Bounded `stats`,
`stats summary`, and `stats breakdown` windows select on these indexed numeric
columns while preserving the existing inclusive time boundary and malformed
timestamp exclusion behavior.

Schema 16 adds a nullable nanosecond epoch form of each compressed-artifact
timestamp. Migration derives valid values only from retained timestamp text and
leaves malformed values NULL without changing the text. Artifact
authority orders by sortable time and then artifact ID, so equivalent timestamp
formats and equal instants have one deterministic order. Migration does not
create or infer artifacts, dispositions, recovery evidence, or runtime evidence.
It scans the compressed primary key forward in fixed-size units and advances its
migration-specific cursor in the same transaction as each update. New artifact
writes populate the sortable value during partial progress. Once no key remains
after the cursor, one final transaction removes the cursor and publishes schema 16. No compressed-timestamp index is created because current queries first
select by source and the existing source index narrows candidates before
timestamp and ID ordering.

Supported Linux ktx readers and writers coordinate on the physical database file with
writer-preferred admission. Once a writer is waiting, later supported readers
wait behind it while existing readers finish. Linux uses byte-range `fcntl`
admission with `flock` ownership. Retained BSD and Windows implementations use
separate byte ranges; they are not current support or native-verification
promises. This coordination creates no sidecar or database
state, and genuinely read-only commands remain read-only.

`doctor` remains read-only for a current schema. For a supported older schema,
readiness prepares the database automatically before inspection. It no longer
reports a manual migration prerequisite.

Doctor appends informational `database_file_size`, `database_wal_file_size`,
and `database_shm_file_size` checks in both JSON and text. They stat only the
selected database path and its `-wal`/`-shm` companions: `observed` gives file
size in bytes (including zero), `absent` means the file was not found, and
`unavailable` means stat failed or the path was not a regular file. Missing or
unavailable measurements are not zero and do not introduce health thresholds
or change existing failure behavior. Classified readiness/security failures
still return their existing error envelope instead of these checks.

These independent observations may change concurrently; they are not an atomic
SQLite snapshot, logical payload totals, allocated physical storage, or an
estimate of reclaimable space. The `database_storage_scope` check points to
`ktx db report`, run separately with the same database selection, for record
totals and logical payload bytes. Doctor does not invoke that report, run
aggregate queries, scan directories, or perform cleanup or discretionary
maintenance. Readiness behavior is unchanged.

Doctor also performs static pipeline configuration diagnostics without writing
configuration or creating config directories. It reports the active config
path, ordered routes and stages, and warnings for runtime-skipped stages or
supported plugin overrides; it never applies stages or executes/probes plugin
commands. Absent config and empty pipelines are valid passthrough. Invalid or
inaccessible config is an error finding, visible alongside ordinary database
findings even when the database is missing (which doctor does not create).
Classified security/readiness failures keep their existing single error
envelope and precedence. These diagnostics do not repair data, maintain
evidence, or establish plugin viability or observed route usage. See
[pipeline diagnostics](pipelines.md#read-only-pipeline-diagnostics).

## Newer schemas

A binary that finds database schema `Y` above its maximum schema `X` exits `5`
with code `schema-incompatible` before writes. The diagnostic includes:

```json
{
  "error": {
    "code": "schema-incompatible",
    "actual_schema": 13,
    "maximum_schema": 12,
    "database_path": "/canonical/path/to/ktx.db",
    "executable_path": "/canonical/path/to/ktx",
    "correction": "..."
  }
}
```

Recover by installing a binary that supports schema `Y` or newer. Never lower
`PRAGMA user_version`.

The generated OpenCode plugin emits the first matching schema-incompatible
diagnostic in one loaded plugin instance. It suppresses repeated ordinary
callbacks only while the pinned executable path and a bounded metadata fingerprint
of the database, WAL, and SHM files remain unchanged. Required benchmark
collection remains fail-closed and does not suppress failures.

## Hermetic development and CI verification

`mage test`, `mage check`, `mage verify`, `mage replayFixture`, and
`mage opencodeTelemetryFixture` allocate one unique disposable
application root per outer invocation. They unset inherited `KTX_DB` and assign
distinct `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`,
`XDG_STATE_HOME`, and `TMPDIR` directories below that root. Each process in the
nested target inherits those roots. Go, Bun, and lint caches remain outside the
application root through explicit `GOCACHE`, `GOMODCACHE`,
`BUN_INSTALL_CACHE_DIR`, and `GOLANGCI_LINT_CACHE` values.

The verification boundary records database resolution, inspection, read-only
open, and normal open attempts before filesystem or SQLite effects. It rejects
any attempt to touch the captured ambient default database or incoming
`KTX_DB`. It also rejects database paths outside the disposable root, so every
resolved database path is beneath that root. It prints a bounded summary with
`forbidden=0` on success.

The pre-push test hook uses `mage test`, including Go race tests and Mage
regressions. Its parallel lint and vulnerability checks do not fix or stage
files; automatic fixes remain in the serial pre-commit hook.

For routine verification, install the Go version declared in `go.mod`, Mage,
and `golangci-lint`, with Bun and Python 3 on `PATH` (`python3` must resolve).
`mage check` runs the Python schema-readiness verifier unit tests as well as
the Go suite and required Bun generated-adapter gate. That gate currently
requires Linux. `mage verify` adds the retained replay fixture and
`govulncheck`, which must also be on `PATH`.
CI explicitly installs these prerequisites. The Python unit tests are offline;
they do not run the production-scale schema release gate.

Focused manual commands that bypass Mage need an equivalent fresh disposable
root. Define and use `ktx_verify` for each such command:

```sh
ktx_verify() (
  root=$(mktemp -d "${TMPDIR:-/tmp}/ktx-verify.XXXXXX") || return
  trap 'rm -rf "$root"' EXIT HUP INT TERM
  mkdir -p "$root/home" "$root/data" "$root/config" "$root/cache" "$root/state" "$root/tmp"
  env -u KTX_DB \
    HOME="$root/home" XDG_DATA_HOME="$root/data" \
    XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
    XDG_STATE_HOME="$root/state" TMPDIR="$root/tmp" \
    KTX_VERIFY_ROOT="$root" \
    "$@"
)
```

The development database boundary rejects a database outside
`KTX_VERIFY_ROOT`. Never point development verification at the live database.

## Migration rehearsal

Rehearse only against an explicitly seeded temporary database:

```sh
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/home" "$root/data" "$root/config" "$root/cache" "$root/state"
install -m 0600 /path/to/seeded-ktx.db "$root/rehearsal.db"

env -u KTX_DB HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" KTX_DB="$root/rehearsal.db" \
  ./build/ktx doctor --format json

env -u KTX_DB HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" KTX_DB="$root/rehearsal.db" \
  ./build/ktx db migrate
```

Inspect the temporary database after migration. Do not copy, stat, open, or
relabel the ambient live database as a rehearsal target.
