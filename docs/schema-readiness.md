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
current-schema and production-shaped workloads. Each release limit is
`ceil(worst × 1.25)`.

The controlled host was `allspark`: Linux 7.2.0-1-cachyos, AMD Ryzen 7 9800X3D
(8 cores, 16 logical CPUs), 32,671,715,328 bytes RAM, and the repository on a
Btrfs NVMe volume. Samples were pinned to logical CPU 2 with `GOMAXPROCS=1`.
The measured binary was `build/ktx`, SHA-256
`755802504a70244d0826d6f0ac2f0be5836de0da9872970763aa9f5148a6e4e6`,
build version `dev`, maximum schema 16.

The minimal workload was a current-schema database with one session and one
tool call. The production-shaped current database was 3,205,058,560 bytes with
3,201 tool calls, 3,200,000,004 payload bytes, and timestamps distributed from
2024-01-01 through 2026-01-01. Its SHA-256 was
`858fe02afbc2f633773d7e07e61be2c44ac797ce7adf2954719d3a4f88547a95`.
The schema-15 migration fixture was 3,205,734,400 bytes with 3,200 compressed
artifact timestamps distributed from 2024-01-01 through 2025-12-31; its
SHA-256 was
`dfdd559652a2bcd78bc57c0512687c2f6e0f2c95e3508d1d37ed6a360a3b9dcf`.
Both fixtures passed `PRAGMA integrity_check`.

Fixture generation is deterministic. Initialize schema 16 with one `ktx write`,
then insert 3,200 tool calls whose IDs are a `p` prefix plus a 15-character
base-26 sequence, whose payload is `CAST(zeroblob(1000000) AS TEXT)`, and whose
sortable timestamp is `1704067200000000000 + i × 19719224757757` for
`i = 0..3199`. The schema-15 fixture is a reflink copy with one compressed row
per generated source using the same timestamp, after which
`compressed.timestamp_unix_nano` is dropped and `user_version` is set to 15.

The current-schema probe ran `ktx list --format json`. The migration probe ran
`ktx db --path <copy> migrate` against a fresh reflink copy for each sample and
verified schema 16 afterward. `strace -ttt -e trace=fcntl,flock` measured lock
ownership, a 0.5 ms poll measured peak RSS and journal/WAL/SHM growth, and a
monotonic clock measured readiness and completion. For a current-schema no-op,
readiness completion and command completion are the same boundary. For
migration, readiness ends at writer-lock acquisition and completion ends at
process exit.

The measured samples below list readiness latency, lock hold, journal growth,
peak RSS, and total completion in that order. Times are nanoseconds and sizes
are bytes.

| Workload               | Sample 1                                                    | Sample 2                                                  | Sample 3                                                    | Sample 4                                                  | Sample 5                                                  |
| ---------------------- | ----------------------------------------------------------- | --------------------------------------------------------- | ----------------------------------------------------------- | --------------------------------------------------------- | --------------------------------------------------------- |
| Minimal current        | 13,697,760 / 2,239,943 / 0 / 17,010,688 / 13,697,760        | 24,319,919 / 2,457,857 / 0 / 16,859,136 / 24,319,919      | 49,336,542 / 2,279,043 / 0 / 16,945,152 / 49,336,542        | 22,889,939 / 5,064,011 / 0 / 17,149,952 / 22,889,939      | 8,495,093 / 2,036,095 / 0 / 17,018,880 / 8,495,093        |
| 3.2 GB current         | 29,218,953 / 19,984,007 / 0 / 20,975,616 / 29,218,953       | 31,259,052 / 21,013,975 / 0 / 21,024,768 / 31,259,052     | 31,854,021 / 20,081,997 / 0 / 21,016,576 / 31,854,021       | 33,014,875 / 20,643,950 / 0 / 20,996,096 / 33,014,875     | 29,646,883 / 20,068,884 / 0 / 21,172,224 / 29,646,883     |
| 3.2 GB schema 15 to 16 | 36,203,146 / 195,309,877 / 8,720 / 19,341,312 / 239,080,509 | 15,775,681 / 38,783,073 / 8,720 / 19,148,800 / 56,821,674 | 20,876,646 / 410,818,100 / 8,720 / 19,128,320 / 433,687,887 | 11,390,209 / 28,412,104 / 8,720 / 19,144,704 / 41,659,050 | 39,354,563 / 49,569,130 / 8,720 / 19,169,280 / 90,622,441 |

Warmups were excluded from limit derivation. Their readiness/lock/RSS/total
values were 14,851,320/2,265,930/17,137,664/14,851,320 for minimal current,
30,834,899/20,131,111/21,037,056/30,834,899 for 3.2 GB current, and
13,370,752/466,166,973/19,238,912/481,251,267 for migration. Warmup journal
growth was zero, zero, and 8,720 bytes respectively.

| Metric               |   Observed worst |                 Derivation |    Release limit |
| -------------------- | ---------------: | -------------------------: | ---------------: |
| Readiness latency    |    49,336,542 ns |  `ceil(49,336,542 × 1.25)` |    61,670,678 ns |
| Exclusive lock hold  |   410,818,100 ns | `ceil(410,818,100 × 1.25)` |   513,522,625 ns |
| Journal growth       |      8,720 bytes |       `ceil(8,720 × 1.25)` |     10,900 bytes |
| Peak resident memory | 21,172,224 bytes |  `ceil(21,172,224 × 1.25)` | 26,465,280 bytes |
| Total completion     |   433,687,887 ns | `ceil(433,687,887 × 1.25)` |   542,109,859 ns |

A schema release must reproduce the host, fixture, sample count, instrumentation,
and derivation above. Any measured value above its limit blocks release. A zero
journal baseline would produce a zero-byte limit; this baseline observed a
nonzero migration journal. Correctness, interruption recovery, and evidence
integrity remain independent mandatory gates.
