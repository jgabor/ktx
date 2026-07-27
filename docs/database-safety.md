# Database safety contract

ktx database access is forward-only. A compatible installed binary may apply
an ordinary safe migration when an Open-backed command first opens an older
database. Development, test, CI, and rehearsal work must never discover or
open an ambient live database.

## Command classes

The implementation primitive, not whether a command sounds observational,
determines its class.

| Class                                     | Surfaces                                                                                                                                                                                                                                                                                                              | Database behavior                                                                                                                                                                                          |
| ----------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| DB-free                                   | Path resolution; help, version, init, config, and plugin management; `binary-capabilities`; `db path`; unavailable `db prune`; prices; replay-file; `bench compare --preflight`; corpus generation; `bench agent --identity`, `bench agent --capabilities`, and `bench agent` without a collector; evidence preflight | Does not create, open, or migrate a database. DB-free commands may still have documented file, network, or process effects.                                                                                |
| Genuinely read-only                       | `doctor`; `db report`; `bench export`, `bench summarize`, and `bench validate`                                                                                                                                                                                                                                        | Opens an existing database read-only. It does not create an absent database or migrate any schema.                                                                                                         |
| Creation and existing-upgrade capable     | `get`, `read`, `list`, `stats`, `decompress`, `write`, `compress`, `hook`, `run`; telemetry database commands; normal `bench run`, replay, and report; collector-enabled `bench agent`                                                                                                                                | Uses the normal Open path. It may create a missing database and may migrate a supported older database.                                                                                                    |
| Isolated owner databases                  | Normal `bench compare` and `bench rank`                                                                                                                                                                                                                                                                               | Creates a fresh database for each attempt and rejects attempt-directory reuse. These are not supported workflows for upgrading an existing attempt database. Nested collector access is still Open-backed. |
| Forbidden ambient-live development access | Any command or test that can resolve or open the normal user application database during development, CI, test, or rehearsal                                                                                                                                                                                          | Do not run it until the binary is deliberately installed and verified and live access is explicitly authorized. Setting one shared `KTX_DB` for a full suite is also forbidden.                            |

`doctor` reports a supported older schema as `degraded` with an upgrade-required
check and exits 0. It reports a newer schema, malformed database, or other hard
failure as `error` and exits 4. It never creates or migrates a database. Exit 5
is not yet implemented for general schema-incompatibility handling.

## Hermetic development and CI verification

`mage test`, `mage check`, and `mage verify` are the standard verification entry
points. Each outer invocation automatically allocates one unique disposable
application root, removes `KTX_DB`, and runs the complete nested target inside
that boundary. `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, and
`XDG_STATE_HOME` are distinct directories beneath the root. Nested targets use
the same boundary rather than recursively allocating or escaping it, and the
root is removed when the command exits.

Before redirecting application state, Mage captures the effective Go module and
build caches, Bun install cache, and golangci-lint cache. It passes `GOCACHE`,
`GOMODCACHE`, `BUN_INSTALL_CACHE_DIR`, and `GOLANGCI_LINT_CACHE` explicitly and
rejects any cache path inside the disposable application root. Language and
tool caches therefore remain reusable while application state does not.

The boundary also enables a verification-only database access audit. Production
database path resolution, read-only inspection, and Open-backed access record
their intended operation and path before filesystem or SQLite side effects.
The audit compares paths lexically with the ambient default database and any
incoming `KTX_DB`; it never stats, opens, or copies a forbidden path to prove
non-access. The target fails closed if audit recording fails or any forbidden
attempt is recorded. On success Mage emits one bounded summary containing the
invocation identifier, operation counts, and `forbidden=0`.

Re-entry into the compiled Mage target treats its environment marker only as a
hint. Before any project command runs, Mage validates the absent `KTX_DB`, the
boundary identifier, root and five distinct application directories, contained
audit path, bounded forbidden-path payload, external cache paths, and a
single-use parent capability stored inside the disposable root. Missing,
malformed, inherited, or already-consumed boundary state fails closed.

Focused manual Go commands that bypass these Mage entry points still need an
equivalent fresh disposable root. Standard local and CI verification should use:

```sh
mage test
mage check
mage verify
```

Migration rehearsal uses only an explicit seeded temporary database copied
into a new temporary root. The seed must be a generated fixture or an operator-provided,
consistent non-live snapshot. It must not be the ambient application database.

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
  ./build/ktx list --format json
```

The first command inspects the seeded database without changing it. The second
command names that same temporary database and rehearses the normal Open-backed
migration. Inspect and test the temporary database after rehearsal. Do not
substitute an ambient path.

## Upgrade and recovery order

1. Build `./build/ktx`. Do not install yet.
2. Run hermetic verification and seeded migration rehearsal with that local
   binary. No step may open the live database.
3. Run `./build/ktx binary-capabilities` and record its canonical executable
   path, SHA-256, build version, and maximum supported schema.
4. Install or select the verified artifact using the deployment's normal
   procedure.
5. Run the effective installed binary's `binary-capabilities` command by its
   canonical absolute path. Verify that its digest and schema capability match
   the rehearsed artifact before granting live access.
6. Deliberately run the first live Open-backed command. Ordinary safe
   auto-migration remains enabled at this boundary.

If a live database has schema Y and the effective binary supports only X where
Y is greater than X, stop. Do not write the database, run a downgrade, or
restore an older executable as a recovery. Do not lower `PRAGMA user_version`.
Install and
verify a binary that supports schema Y or newer, then repeat the effective
binary identity check before a deliberate live open.

For the executable identity fields and failure behavior, see
[Binary compatibility identity](binary-capabilities.md).
