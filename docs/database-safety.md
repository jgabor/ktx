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

Every full verification invocation needs a newly allocated application root.
Unset `KTX_DB`; set `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`,
`XDG_CACHE_HOME`, and `XDG_STATE_HOME` beneath that root. Capture reusable tool
cache paths before redirecting those application variables, then pass the
paths explicitly. Current Go uses `GOCACHE` and `GOMODCACHE`; Bun uses
`BUN_INSTALL_CACHE_DIR`; golangci-lint uses `GOLANGCI_LINT_CACHE`.

```sh
ktx_tool_gocache=$(go env GOCACHE)
ktx_tool_gomodcache=$(go env GOMODCACHE)
ktx_tool_cache_home=${XDG_CACHE_HOME:-"$HOME/.cache"}
ktx_tool_bun_cache=${BUN_INSTALL_CACHE_DIR:-"$ktx_tool_cache_home/.bun/install/cache"}
ktx_tool_lint_cache=${GOLANGCI_LINT_CACHE:-"$ktx_tool_cache_home/golangci-lint"}

ktx_verify() (
  root=$(mktemp -d "${TMPDIR:-/tmp}/ktx-verify.XXXXXX") || exit
  trap 'rm -rf "$root"' EXIT HUP INT TERM
  mkdir -p "$root/home" "$root/data" "$root/config" "$root/cache" "$root/state" || exit

  for tool_cache in \
    "$ktx_tool_gocache" "$ktx_tool_gomodcache" \
    "$ktx_tool_bun_cache" "$ktx_tool_lint_cache"
  do
    case "$tool_cache" in
      "$root"|"$root"/*)
        printf 'tool cache must be outside disposable root: %s\n' "$tool_cache" >&2
        exit 1
        ;;
    esac
  done

  env -u KTX_DB \
    HOME="$root/home" \
    XDG_DATA_HOME="$root/data" \
    XDG_CONFIG_HOME="$root/config" \
    XDG_CACHE_HOME="$root/cache" \
    XDG_STATE_HOME="$root/state" \
    GOCACHE="$ktx_tool_gocache" \
    GOMODCACHE="$ktx_tool_gomodcache" \
    BUN_INSTALL_CACHE_DIR="$ktx_tool_bun_cache" \
    GOLANGCI_LINT_CACHE="$ktx_tool_lint_cache" \
    "$@"
)

ktx_verify go test ./internal/cli -run '^TestDatabaseSafetyGuidanceContract$' -count=1
ktx_verify mage check
```

Each `ktx_verify` call runs in a subshell, allocates a distinct root, starts its
command with `KTX_DB` removed, and cleans up afterward. Use the same function
for other focused commands and `mage verify`. The explicit tool-cache variables
keep Go compilation and module caches, Bun's install cache, and golangci-lint's
cache reusable outside the disposable application root. Current tests enforce
the doctor contract and this published procedure; Mage and CI do not yet
allocate these roots automatically.

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
