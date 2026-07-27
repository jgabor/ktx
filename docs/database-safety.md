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

Open-backed commands whose target comes from the default XDG/HOME resolution
require the root flag `--expect-runtime-sha256 <digest>`. ktx hashes the
canonical running executable and compares it with that digest before directory
creation, database inspection, SQLite open, or migration. The proof is required
on every ambient Open-backed invocation; ktx writes no receipt or trust state.

A nonempty `KTX_DB` and an API or command that supplies a direct database path
are explicit operator ownership declarations. They remain usable without the
runtime proof so disposable tests, rehearsals, and benchmark attempt owners can
operate. This is a provenance boundary, not a safety oracle: ktx does not prove
that a `KTX_DB` path is temporary, non-live, or safe. The operator owns that
claim. Never put an ambient live path in `KTX_DB` to bypass the proof.

`doctor` reports a supported older schema as `degraded` with an upgrade-required
check and exits 0. It reports a newer schema, malformed database, or other hard
failure as `error` and exits 4. It never creates or migrates a database. Doctor
is a read-only health surface; it does not use the Open-backed exit contract
below.

`db report --output <file>` rejects an output path that names the source
database directly or through relative spelling, symlink, or hard link. The
check occurs before the read-only database open or report generation, so report
publication cannot replace its source.

An Open-backed command that finds database schema `Y` above the binary's maximum
schema `X` exits `5` with error code `schema-incompatible`. Its JSON diagnostic
on stderr has these stable fields:

```json
{
  "error": {
    "code": "schema-incompatible",
    "message": "...",
    "actual_schema": 9,
    "maximum_schema": 8,
    "database_path": "/canonical/path/to/ktx.db",
    "executable_path": "/canonical/path/to/ktx",
    "executable_sha256": "<64 lowercase hexadecimal characters>",
    "build_version": "<version>",
    "correction": "..."
  }
}
```

The generated OpenCode plugin emits the first valid matching diagnostic in one
loaded plugin instance. Later ordinary callbacks for the same error code,
binary digest, canonical database path, actual schema, and maximum schema skip
the Open-backed call and leave tool output unchanged only while a bounded
metadata fingerprint of the database and its SQLite WAL/SHM sidecars remains
unchanged. Fingerprinting uses filesystem metadata, follows a database symlink
for target and sidecar coverage, and never opens or reads SQLite. A changed,
missing, or uninspectable target invalidates containment so one Open-backed call
can discover a distinct exact key. A different database target is resolved
again without opening it. Fingerprint fields are invalidation signals and are
not part of the deduplication key. Malformed exit-5 output and other errors
remain visible and are not cached. Required benchmark collection bypasses both
containment and fingerprinting and fails closed on every incompatible callback.
A new plugin instance starts with empty identity and containment state.

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

## Ambient proof failure contract

The root proof flag accepts exactly 64 lowercase hexadecimal characters.
Malformed input is a usage error, exits `1`, and fails during root parsing before
database target resolution. A missing proof for an ambient Open-backed command
exits `3` with `failure_class=runtime-proof-required`. A digest mismatch exits
`3` with `failure_class=runtime-proof-mismatch`, the expected and actual
digests, and the canonical executable path. An executable that cannot be
resolved or hashed exits `4` with
`failure_class=runtime-proof-unverifiable`. These failures emit structured JSON
on stderr by default, no result on stdout, and do not inspect, open, create, or
migrate the database. A later schema incompatibility exits `5` only after the
proof succeeds; missing or mismatched proof therefore retains precedence.

The correction is:

```sh
ktx binary-capabilities
/absolute/path/to/verified/ktx binary-capabilities --expect-sha256 <digest>
/absolute/path/to/verified/ktx --expect-runtime-sha256 <digest> <open-backed-command>
```

## Upgrade and recovery order

1. Build `./build/ktx`. Do not install yet into the user's environment.
2. Run hermetic verification and seeded migration rehearsal with that local
   binary. No step may open the live database.
3. Run `./build/ktx binary-capabilities` and record its canonical executable
   path, SHA-256, build version, and maximum supported schema.
4. First test installation with isolated `GOBIN` and `GOPATH`, then install or
   select the verified artifact using the deployment's normal procedure.
5. Run the effective installed binary's `binary-capabilities` command by its
   canonical absolute path. Verify that its digest and schema capability match
   the rehearsed artifact before granting live access.
6. Run the effective installed binary's read-only `doctor` preflight. It does
   not need the runtime proof and never creates or migrates a database.
7. Deliberately run the first ambient Open-backed command with
   `--expect-runtime-sha256 <verified-digest>`. Carry the same fresh proof on
   every later ambient Open-backed invocation. Ordinary safe auto-migration
   remains enabled at this boundary.

Implementation and CI apply tests perform this sequence only with disposable
HOME/XDG roots, explicit temporary databases, and a temporary installation.
They do not perform the live open described above.

If a live database has schema Y and the effective binary supports only X where
Y is greater than X, stop. Do not write the database, run a downgrade, or
restore an older executable as a recovery. Do not lower `PRAGMA user_version`.
Install and
verify a binary that supports schema Y or newer, then repeat the effective
binary identity check before a deliberate live open.

For the executable identity fields and failure behavior, see
[Binary compatibility identity](binary-capabilities.md).
