# Schema readiness contract

Schema readiness is an internal database-open responsibility. A user or runtime
must not run a separate migration command before a supported database-backed
operation. This is the required release contract; implementation and lifecycle
verification are tracked separately.

## Database state matrix

| Surface                                                                                                                                         | Missing database                                                   | Current schema                                | Supported older schema                                                                   | Newer than the binary                         |
| ----------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ | --------------------------------------------- | ---------------------------------------------------------------------------------------- | --------------------------------------------- |
| DB-free commands                                                                                                                                | No database access                                                 | No database access                            | No database access                                                                       | No database access                            |
| Query commands (`get`, `read`, `list`, `stats`, `decompress`, `bench report`, telemetry queries)                                                | Use an ephemeral empty current schema; create no persistent files  | Open read-only and run the operation          | Prepare the database automatically, then run the operation with current-schema semantics | Fail before writes with `schema-incompatible` |
| Existing-artifact inspection (`doctor`, `db report`, `bench export`, `bench validate`)                                                          | Preserve the surface's missing-input result and create no database | Inspect read-only                             | Prepare automatically, then inspect                                                      | Fail before writes with `schema-incompatible` |
| Evidence writers (`write`, `compress`, `hook`, `run`, telemetry collectors, normal benchmark run and replay, collector-enabled benchmark agent) | Create the current schema, then run                                | Open without migration, then run              | Prepare automatically, then run                                                          | Fail before writes with `schema-incompatible` |
| Isolated compare and rank owners                                                                                                                | Create one fresh database per attempt                              | Reused attempt directories remain unsupported | Reused attempt directories remain unsupported                                            | Reused attempt directories remain unsupported |

Preparation is automatic for every supported older schema. It can perform
internal writes even when the requested operation is a query or inspection.
After preparation, query and inspection operations retain their normal
read-only behavior. The former rule that queries and `doctor` report
`schema-migration-required` and require `ktx db migrate` is superseded.
`ktx db migrate` can remain an explicit diagnostic or rehearsal surface, but it
is never a prerequisite for normal use.

Schema 15 preparation commits at most 128 compressed rows per unit and advances
its primary-key cursor in the same transaction. Valid timestamps receive their
sortable form, malformed timestamps remain null but still advance the cursor,
and new artifacts populate both timestamp forms. One final transaction removes
the cursor and publishes schema 16. It does not build a history-sized index.

Ambient Open-backed access still requires the per-invocation runtime digest
proof before path inspection, directory creation, SQLite open, or preparation.
Explicit disposable or operator-owned paths retain their existing ownership
rules. A schema newer than the binary always fails before writes. Recovery is
forward-only: install a binary that supports the schema, and never lower
`PRAGMA user_version`.

## Deadline ownership

KTX owns every startup, contention, operation, and migration deadline. A
generated callback waits for the KTX child result and does not impose a second
timer. One exhausted wait produces one attribution:

| Exhausted wait                  | Stable attribution                                |
| ------------------------------- | ------------------------------------------------- |
| KTX process startup             | `runtime-startup-timeout`                         |
| Supported-process coordination  | `database-unavailable` / `coordinator-timeout`    |
| SQLite lock retry               | `database-unavailable` / `sqlite-lock-exhaustion` |
| Requested operation             | `operation-timeout`                               |
| Schema preparation or migration | `migration-timeout`                               |

The first exhausted KTX-owned boundary is authoritative. Outer adapters must
not replace, duplicate, or relabel it. Callback lifetime is therefore bounded
only by the KTX-owned result.

## Readiness release budget

The approved policy uses one isolated controlled host, one warmup and five
measured samples per workload, and the worst measured value across the
current-schema and production-shaped workloads. Readiness latency, maximum
individual lock, and peak RSS retain their approved limits. The user-approved
fixed journal limit is 50,000 bytes and the fixed total-completion limit is
400,000,000 ns; these two values supersede only their prior numeric limits.

The controlled host was `allspark`: Linux 7.2.0-1-cachyos, AMD Ryzen 7 9800X3D
(8 cores, 16 logical CPUs), 32,671,715,328 bytes RAM, and the repository on a
Btrfs NVMe volume. Samples were pinned to logical CPU 2 with `GOMAXPROCS=1`.
The measured binary was `build/ktx`, SHA-256
`cf046b33ee1c6b8160b3b873a695b2b0bbc07ee18e5a204f309194ab88602ac0`,
build version `dev`, maximum schema 16.

The minimal workload was a 180,224-byte current-schema database with one session
and one tool call. The production-shaped current database was 3,205,054,464 bytes with
3,201 tool calls, 3,200,000,004 payload bytes, and timestamps distributed from
2024-01-01 through 2026-01-01. Its SHA-256 was
`5a518c5da4d1dd4621e045b5d954485e8637455d619eb650a906afb18354922b`.
The schema-15 migration fixture was 3,205,734,400 bytes with 3,200 compressed
artifact timestamps distributed from 2024-01-01 through 2025-12-31; its
SHA-256 was
`5015e3ed63a39dc22cf33f2c3cf4a9b1ee53fa38bc1f0103e9f8af8b3dacd120`.
Both fixtures passed `PRAGMA integrity_check`.

Fixture generation is deterministic. Initialize schema 16 with one `ktx write`,
then insert 3,200 tool calls whose IDs are a `p` prefix plus a 15-character
base-26 sequence, whose payload is `CAST(zeroblob(1000000) AS TEXT)`, and whose
sortable timestamp is `1704067200000000000 + i × 19719224757757` for
`i = 0..3199`. The schema-15 fixture is a reflink copy with one compressed row
per generated source using the same timestamp, after which
`compressed.timestamp_unix_nano` is dropped and `user_version` is set to 15.

The current-schema probe ran `ktx list --format json`. The migration probe ran
`ktx db --path <copy> migrate` against a fresh disposable copy for each sample
and verified schema 16 afterward. `strace -ttt -e trace=fcntl,flock` measured
each successful exclusive-lock interval; the sample records the maximum
individual interval rather than the span from the first lock through the last
unlock. A 0.5 ms poll measured peak RSS and journal/WAL/SHM growth, and a
monotonic clock measured readiness and completion. For a current-schema no-op,
readiness completion and command completion are the same boundary. For
migration, readiness ends at the first writer-lock acquisition and completion
ends at process exit.

[`notes/schema-readiness-baseline.json`](notes/schema-readiness-baseline.json)
retains every warmup and measured sample, command stdout, fixture input and
hash, boundary definition, observed worst value, fixed limit, and limit basis. Regenerate
it from an isolated disposable root on the recorded host:

```sh
root=$(mktemp -d)
mkdir -p "$root/home" "$root/data" "$root/config" "$root/cache" "$root/state" "$root/tmp"
env -u KTX_DB \
  HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" TMPDIR="$root/tmp" \
  python3 tools/schema_readiness_evidence.py prepare \
    --binary ./build/ktx \
    --work-root "$root/work"

env -u KTX_DB \
  HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" TMPDIR="$root/tmp" \
  python3 tools/schema_readiness_evidence.py smoke \
    --binary ./build/ktx \
    --work-root "$root/work"

env -u KTX_DB \
  HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" TMPDIR="$root/tmp" \
  python3 tools/schema_readiness_evidence.py measure \
    --binary ./build/ktx \
    --work-root "$root/work" \
    --output docs/notes/schema-readiness-baseline.json
```

The harness requires Linux `/proc`, `taskset`, `strace`, Python SQLite, and GNU
`cp`. Copies use `cp --reflink=auto --sparse=always`: they use copy-on-write on
the controlled host when available and safely fall back to a sparse copy when
the work root, including `/tmp`, does not support reflinks. The harness creates
a distinct HOME and all XDG roots for every KTX invocation, unsets inherited
`KTX_DB`, generates only disposable databases beneath `--work-root`, parses
timestamped `fcntl` and `flock` events, polls RSS and SQLite sidecars every 0.5
ms, and compares each measured metric with the approved limits retained below.
`prepare` writes `prepared-fixtures.json` only after fixture integrity checks and
hashes complete. `smoke` and `measure` reuse those fixtures. Each phase and
sample reports progress to stderr, including the maximum individual exclusive
lock interval observed for each sample. Child output and syscall traces spool to
temporary files while resource polling runs, so trace volume cannot block child
completion.

| Metric               |   Observed worst | Limit authority          |    Release limit |
| -------------------- | ---------------: | ------------------------ | ---------------: |
| Readiness latency    |    24,039,592 ns | Unchanged approved limit |    35,322,974 ns |
| Exclusive lock hold  |    41,398,048 ns | Unchanged approved limit |   101,037,622 ns |
| Journal growth       |     33,344 bytes | User-approved override   |     50,000 bytes |
| Peak resident memory | 21,581,824 bytes | Unchanged approved limit | 26,557,440 bytes |
| Total completion     |   123,590,682 ns | User-approved override   |   400,000,000 ns |

A schema release must reproduce the host, fixture, sample count, instrumentation,
and fixed-limit comparison above. Any measured value above its limit blocks
release. The journal and total-completion overrides do not reinterpret earlier
raw observations. Correctness, interruption recovery, and evidence integrity
remain independent mandatory gates.

Prepare the disposable 3.2 GB fixture with the `prepare` command above. Then run
the explicit release gate, which is intentionally not part of `mage check`:

```bash
env -u KTX_DB \
  KTX_SCHEMA_RELEASE_WORK_ROOT="$root/work" \
  KTX_SCHEMA_RELEASE_REPORT="docs/notes/schema-readiness-release.json" \
  mage schemaReleaseGate
```

The target fails if either path or any prerequisite is missing. It builds KTX,
verifies the schema-15 historical fixture revision and hash, verifies the exact
3.2 GB row and timestamp policy, runs the generated OpenCode adapter overlap
gate, kills a migration after a durable cursor advance and restarts it, and
measures all five fixed limits. It publishes one compact JSON report atomically
after validation. A failed run writes `<report>.failed.json` and does not replace
the last passing report.
