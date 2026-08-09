# Database safety contract

ktx database access is forward-only. Query commands are genuinely read-only;
commands that persist evidence may create a database and transactionally migrate
an older supported schema. Development, tests, CI, and rehearsal must never
access the ambient live database.

Critical-path evidence classes, participant ownership, hook acceptance, and
the database-unavailable result are defined in the
[critical-path performance and availability contract](critical-path-performance.md).

## Command classes

| Class                                     | Surfaces                                                                                                                                                                  | Database behavior                                                                                                                                                                                                                                                   |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| DB-free                                   | Help, version, init, config and plugin management, `binary-capabilities`, `db path`, prices, replay-file, comparison preflight, corpus generation, and evidence preflight | Does not create, inspect, open, or migrate an ambient database.                                                                                                                                                                                                     |
| Genuinely read-only query                 | `get`, `read`, `list`, `stats`, `decompress`; `bench report`; `telemetry list`                                                                                            | Opens an existing current-schema database with SQLite `mode=ro` and `query_only`. It never creates or migrates persistent state. An absent database is represented by an ephemeral empty current schema so normal empty and not-found results do not create a file. |
| Existing-artifact inspection              | `doctor`; `db report`; `bench export` and `bench validate`                                                                                                                | Uses read-only access to an existing database or retained artifact and preserves its command-specific missing-input behavior.                                                                                                                                       |
| Creation and migration capable            | `write`, `compress`, `hook`, `run`; telemetry record and collector commands; normal benchmark run and replay; collector-enabled benchmark agent                           | Uses the normal Open path. It may create a missing database and transactionally migrate an older supported schema before performing the requested write.                                                                                                            |
| Explicit migration                        | `ktx db migrate`                                                                                                                                                          | Upgrades an existing older database through the same transactional migration path. It does not create an absent database.                                                                                                                                           |
| Isolated owner databases                  | Normal benchmark compare and rank                                                                                                                                         | Creates a fresh database for each attempt and rejects attempt-directory reuse.                                                                                                                                                                                      |
| Forbidden ambient-live development access | Any development, test, CI, or rehearsal command that could discover the normal user database                                                                              | Use a fresh disposable application root and, for migration rehearsal, an explicitly seeded temporary database.                                                                                                                                                      |

Path precedence remains explicit flag, then `KTX_DB`, then the default
`$XDG_DATA_HOME/ktx/ktx.db`. `KTX_DB` selects a path; it does not prove that the
path is disposable or safe.

## Query behavior

Read-only query commands require the current schema because their typed queries
are defined against that schema. They never migrate as a side effect. If the
database is older, they exit `5` with code `schema-migration-required`, the
actual and required versions, the canonical database path, and this correction:

```sh
ktx db migrate
```

Use `ktx db --path <file> migrate` for a per-invocation path or set `KTX_DB` for
an explicitly selected database. The command reports `from_schema`, `to_schema`,
and whether a migration ran. An absent database returns not-found and remains
absent.

An absent database is logically empty for read-only commands. `list`, `stats`,
and `bench report` return their empty results; record lookup, decompression, and
session telemetry inspection return their normal not-found result. No parent
directory, database, WAL, or SHM file is created.

## Forward migration

Normal write-capable commands and `ktx db migrate` use the same migration code.
The implementation reads `PRAGMA user_version`, returns immediately for the
current schema, and rejects a newer schema. An older supported schema is checked
again under an immediate SQLite transaction, migrated, and assigned the current
version atomically. A failed migration rolls back schema, rows, indexes,
associations, and `user_version`.

Schema 9 adds `compression_dispositions`, a source-keyed operational projection
containing only selected profile, route provenance, bounded disposition,
optional authoritative artifact ID, and observation time. Migration does not
backfill historical calls or infer outcomes from artifacts. Missing rows remain
`unobserved` in statistics. Runtime recording may promote a retained
non-artifact disposition to the first authoritative artifact, but never
downgrades or replaces artifact authority.

`doctor` remains a read-only preflight. It reports an older schema as degraded
and never upgrades it.

## Newer schemas

A binary that finds database schema `Y` above its maximum schema `X` exits `5`
with code `schema-incompatible` before writes. The diagnostic includes:

```json
{
  "error": {
    "code": "schema-incompatible",
    "actual_schema": 10,
    "maximum_schema": 9,
    "database_path": "/canonical/path/to/ktx.db",
    "executable_path": "/canonical/path/to/ktx",
    "executable_sha256": "<64 lowercase hexadecimal characters>",
    "build_version": "<version>",
    "correction": "..."
  }
}
```

Recover by installing a binary that supports schema `Y` or newer. Never lower
`PRAGMA user_version`.

The generated OpenCode plugin emits the first matching schema-incompatible
diagnostic in one loaded plugin instance. It suppresses repeated ordinary
callbacks only while the executable identity and a bounded metadata fingerprint
of the database, WAL, and SHM files remain unchanged. Required benchmark
collection remains fail-closed and does not suppress failures.

## Hermetic development and CI verification

`mage test`, `mage check`, and `mage verify` allocate one unique disposable
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

For executable identity fields used in diagnostics, see
[Binary compatibility identity](binary-capabilities.md).
