# Binary compatibility identity

`ktx binary-capabilities` identifies the executable that handled the command
without resolving or opening a database. The default JSON response is the
versioned `ktx.binary-capabilities/v1` contract:

```json
{
  "schema": "ktx.binary-capabilities/v1",
  "executable_path": "/absolute/canonical/path/to/ktx",
  "executable_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "build_version": "0.1.0-dev",
  "max_database_schema": 8
}
```

`executable_path` is the absolute path after resolving symlinks.
`executable_sha256` is the lowercase SHA-256 of the bytes at that canonical
path. `build_version` is the version embedded at build time, and
`max_database_schema` comes from the database package's schema owner; querying
it does not inspect a database.

Use `--format text` for a human-readable rendering. First run `mage build`, exact
hermetic `mage verify`, and the seeded migration rehearsal from the
[database safety contract](database-safety.md#hermetic-development-and-ci-verification).
Do not rebuild after that evidence is established. The following operator-only
sequence records the already verified artifact without opening a database,
installs it, identifies the effective installed candidate, compares that
candidate's bytes independently, and only then grants an ambient invocation:

```sh
KTX_VERIFIED_ARTIFACT=./build/ktx
KTX_VERIFIED_SHA256=$(sha256sum "$KTX_VERIFIED_ARTIFACT" | cut -d ' ' -f 1)
"$KTX_VERIFIED_ARTIFACT" binary-capabilities --expect-sha256 "$KTX_VERIFIED_SHA256"

# Install only after isolated verification and rehearsal are complete.
mage install
KTX_VERIFIED_BIN=$(command -v ktx)
test -n "$KTX_VERIFIED_BIN" && test -f "$KTX_VERIFIED_BIN" && test -x "$KTX_VERIFIED_BIN"
case "$KTX_VERIFIED_BIN" in /*) ;; *) printf '%s\n' 'installed ktx path is not absolute' >&2; exit 1;; esac
KTX_INSTALLED_SHA256=$(sha256sum "$KTX_VERIFIED_BIN" | cut -d ' ' -f 1)
test "$KTX_INSTALLED_SHA256" = "$KTX_VERIFIED_SHA256"
"$KTX_VERIFIED_BIN" binary-capabilities --expect-sha256 "$KTX_VERIFIED_SHA256"
"$KTX_VERIFIED_BIN" --expect-runtime-sha256 "$KTX_VERIFIED_SHA256" list
```

Development, tests, CI, and migration rehearsal must stop before the install
and ambient steps; they use disposable HOME/XDG roots and explicit temporary
database owners instead. A checkout-derived digest, or a digest merely reported
by the candidate itself, is not ambient authorization. Every ambient call must
present the independently established digest for the exact installed binary it
invokes.

The comparison succeeds with exit `0` and adds this object to the JSON result:

```json
"comparison": {
  "expected_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "status": "equal"
}
```

A different digest is a stale or different executable. It exits `3`, writes no
stdout, and writes a JSON `conflict` error to stderr containing
`expected_sha256`, `actual_sha256`, `executable_path`, and this correction:
install or select the verified executable and retry using its canonical
absolute path. An expected digest that is not exactly 64 lowercase hexadecimal
characters is a usage error and exits `1` with the accepted syntax.

If ktx cannot resolve its executable path, follow a symlink, open the
executable, or hash all executable bytes, identity is unverifiable. The command
exits `4`, writes no stdout, and reports the failed operation plus the
correction: reinstall the executable at a readable stable path and retry it by
absolute path. No failure falls back to `PATH` or to database inspection.

## Proof for ambient Open-backed commands

`--expect-sha256` verifies an artifact without database access.
`--expect-runtime-sha256 <digest>` is the separate root flag that authorizes an
Open-backed command to use the ambient default database target:

```sh
: "${KTX_VERIFIED_BIN:?set this to the verified installed ktx path}"
: "${KTX_VERIFIED_SHA256:?set this to its independently verified SHA-256}"
"$KTX_VERIFIED_BIN" binary-capabilities --expect-sha256 "$KTX_VERIFIED_SHA256"
"$KTX_VERIFIED_BIN" --expect-runtime-sha256 "$KTX_VERIFIED_SHA256" list
```

The command hashes the same canonical running executable through the identity
owner above, then compares the exact bytes before database inspection, open,
directory creation, or migration. The proof is per invocation and creates no
receipt or persistent trust state. Explicit `KTX_DB` and direct-path targets are
operator ownership declarations and do not require it; they are not
independently verified as disposable or safe.

Malformed proof text exits `1`. Missing or mismatched ambient proof exits `3`.
An unverifiable running executable exits `4`. A valid proof preserves the normal
Open and forward-migration behavior. A database schema newer than the binary's
maximum fails before writes with exit `5` and error code
`schema-incompatible`. The structured error carries `actual_schema`,
`maximum_schema`, `database_path`, `executable_path`, `executable_sha256`, and
`build_version`, plus `message` and `correction`. See the complete envelope in
[Database safety contract](database-safety.md#command-classes). Recovery is
forward-only: install and verify a binary supporting the actual schema or newer,
then retry with its verified digest. Never lower `PRAGMA user_version`.

## OpenCode candidate pinning

The plugin generated by `ktx init opencode` resolves `ktx` once when the plugin
factory starts, canonicalizes that one candidate, hashes its executable bytes, and
queries that exact path for `binary-capabilities`. It registers hooks only when
the response schema and fields are valid and the reported canonical path and
digest equal the independently observed candidate. Every later hook and
telemetry invocation uses the pinned canonical absolute path, so a later
`PATH` change cannot select another executable. Every Open-backed callback also
passes that independently observed digest through
`--expect-runtime-sha256`.

Candidate path, digest, callback queue, and incompatibility containment belong
to that factory instance. Initializing another factory cannot replace the first
factory's pinned executable or share its suppression state.

An absent or unverifiable candidate disables the ordinary plugin and reports a
working reinstall-and-refresh correction. Required benchmark collection fails
plugin startup instead. Re-run `ktx init opencode` after replacing ktx so the
generated integration stays current.
