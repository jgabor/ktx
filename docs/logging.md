# Structured diagnostic logging

Ktx can append database-unavailable diagnostics to one typed JSONL file. No
`[logs]` section means file logging is off. A section requires `level`; valid
values are `debug`, `info`, `warn`, `error`, and `off`.

```toml
[logs]
level = "error"
path = "/absolute/path/to/ktx.log"
```

When `path` is omitted, ktx uses `$XDG_STATE_HOME/ktx/ktx.log`, or
`~/.local/state/ktx/ktx.log` when `XDG_STATE_HOME` is unset. A configured path
must be absolute. Invalid logging settings disable file logging without changing
ordinary commands. A TOML syntax error still makes configuration loading fail.

Ktx marshals one closed record and makes one best-effort append attempt with no
retry. Sink errors do not change stdout, stderr, exit status, or database
behavior. New files are private. Ktx rejects non-regular targets,
symlink-following targets, existing POSIX files with group or other permissions,
and destinations that alias the active database.

The `database-unavailable` record contains only:

- `timestamp`
- `level`
- `event`
- `ktx_command`
- `executable_path`
- `executable_sha256`
- `database_path`
- `failure_class`
- `operation_phase`
- `elapsed_wait_nanoseconds`

Coordinator-timeout records also contain `coordinator_requested_mode`
(`shared` or `exclusive`) and `coordinator_snapshot_available`. On Linux, a
successful `/proc/locks` snapshot can add up to eight `coordinator_blockers`,
each containing only `pid` and held `mode`. An available snapshot with no
matching holder has no blockers; an unavailable snapshot reports
`coordinator_snapshot_available` as false. Other failure classes keep the
ten-field record above.

Records do not include tool payloads, database content, SQL, raw driver errors,
credentials, secrets, or arbitrary fields. Paths and hashes are sensitive
retained identifiers.
