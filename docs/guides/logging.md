# Diagnostic logging

When ktx can't open its database, it can write a short record to a log file so
you can see what went wrong. File logging is off unless you turn it on.

ktx can append database-unavailable diagnostics to one typed JSONL file.
Without a `[logs]` section, file logging is off. A `[logs]` section must set
`level`. Valid values are `debug`, `info`, `warn`, `error`, and `off`.

```toml
[logs]
level = "error"
path = "/absolute/path/to/ktx.log"
```

If you leave out `path`, ktx uses `$XDG_STATE_HOME/ktx/ktx.log`, or
`~/.local/state/ktx/ktx.log` when `XDG_STATE_HOME` is unset. A path you set
must be absolute. Invalid logging settings turn file logging off, and ordinary
commands are unaffected. A TOML syntax error still makes configuration loading
fail.

ktx builds one record with a closed set of fields. It makes one best-effort
attempt to append it, with no retry. If writing the log fails, stdout, stderr,
exit status, and database behavior stay the same. New log files are private.
ktx refuses to write to:

- a target that is not a regular file
- a target that would follow a symlink
- an existing POSIX file with group or other permissions
- a destination that aliases the active database

The `database-unavailable` record contains only:

- `timestamp`
- `level`
- `event`
- `ktx_command`
- `executable_path`
- `database_path`
- `failure_class`
- `operation_phase`
- `elapsed_wait_nanoseconds`

Coordinator-timeout records also contain `coordinator_requested_mode`
(`shared` or `exclusive`) and `coordinator_snapshot_available`. On Linux, if
ktx gets a `/proc/locks` snapshot, it can add up to eight
`coordinator_blockers`. Each blocker contains only a `pid`, the held `mode`,
and a `command` label from a closed list. ktx reports `unknown` unless the live
process still owns the matching lock, has the same process lifetime, uses the
current executable, and names a supported command. ktx never stores the
arguments or command lines it inspects.

If the snapshot is available but no process holds a matching lock, the record
has no blockers. If the snapshot is unavailable, `coordinator_snapshot_available`
is false. Other failure classes keep the nine-field record above.

Records do not include tool payloads, database content, SQL, raw driver errors,
credentials, secrets, or arbitrary fields. Database paths are sensitive
retained identifiers.
