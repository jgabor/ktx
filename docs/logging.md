# Structured diagnostic logging

Ktx can retain deliberate diagnostic events in one local file as typed JSONL.
No [logs] section means file logging is off. A present section defaults to the
inclusive `error` level. Valid levels are `debug`, `info`, `warn`, `error`, and
`off`. The file sink is best effort and independent of stderr diagnostics.

```toml
[logs]
level = "error"
path = "ktx/ktx.log"
```

When `path` is omitted, the destination is
`$XDG_STATE_HOME/ktx/ktx.log`, or `~/.local/state/ktx/ktx.log` when
`XDG_STATE_HOME` is unset. Relative paths resolve beneath the state directory.
By contrast, absolute paths are explicit overrides. `ktx config show --format json` and
`ktx config show --format text` report the effective enabled state, level, and
destination without creating the destination.

Each record is one typed JSON object followed by a newline. Records are at most
16 KiB. The active file is at most 5 MiB, and rotation retains one previous
file. Ktx creates directories with private access and keeps the destination and
coordination file private to the current user. It rejects non-regular targets
and destinations that alias the active database.

Logging makes one best-effort coordination attempt with a nominal 25 ms
internal target and no retries. Contention drops only the file event. Tests use
a 50 ms wall-clock safety ceiling for scheduler tolerance. It is not a hard real-time guarantee.
Sink errors do not change stdout, stderr, or the
command exit code.

## Database-unavailable event

The first production record has `event` equal to `database-unavailable` and
exactly these typed fields:

- `timestamp` (RFC 3339 timestamp string)
- `level` (`error`)
- `event` (`database-unavailable`)
- `runtime_surface` (string)
- `executable_path` (string)
- `executable_sha256` (string)
- `database_path` (string)
- `failure_class` (string)
- `operation_phase` (string)
- `elapsed_wait_nanoseconds` (integer)
- `correction` (string)

Ktx writes this event before the OpenCode adapter parses the command result.
Therefore, an enabled sink retains the typed event even if adapter parsing
falls back to a generic `[ktx] hook failed (exit 4): ...` stderr diagnostic.

Records do not include tool payloads, database content, SQL, raw driver errors,
credentials, secrets, arbitrary wrapped errors, or undeclared fields. Paths
and hashes are sensitive retained identifiers, not anonymous telemetry. Protect
and delete the log files according to the sensitivity of those identifiers.
