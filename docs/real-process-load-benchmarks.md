# Real-process load measurements

`mage loadBench` measures disposable real `./build/ktx` processes. It is a
developer-only harness, not a `ktx` command. It does not change `ktx bench`
cost or quality semantics, and its timings are descriptive rather than gates.

`mage decompressProcessBench` emits a separate schema-2 engineering report for
one deliberately backpressured 2 MiB `ktx decompress` process and an unrelated
supported hook. It retains output-start and backpressure barriers, whether the
hook was observed before output release, hook latency and outcome, byte-exact
reader output, participant classes, and exact source, artifact, disposition,
and session reconciliation.
Only `ok` and the supported `database-unavailable` outcome validate; malformed
identity, missing reconciliation, and other failures reject the report. This
target does not alter the load-report schema.

For a retained load report paired with component measurements, use the
[durable engineering verification path](critical-path-performance.md#durable-engineering-verification).

Run it from the repository root:

```sh
mage loadBench > real-process-load.json
mage decompressProcessBench > blocked-decompression.json
```

Mage rebuilds `./build/ktx` before starting the harness. The harness uses that
binary for every hook, wrapper, and read-only query process. It creates a new
temporary parent root, then a new owned database and HOME/XDG root for every
warm-up, measured round, and scenario. Each worker receives only an explicit
`KTX_DB` below its own root.

## Report contract

The JSON report has `schema_version: 3`, one warm-up round, and five measured
rounds. It records UTC RFC3339Nano `started_at`, `finished_at`, and non-negative
`duration_nanoseconds`, the local binary path and SHA-256, Go version, operating
system, architecture, processor, disposable environment names, fixed payload
names, seeds, byte counts, and hashes.

One-shot `ktx hook` processes use concurrency 1, 2, 4, 8, 16, and 32. Every
round also starts a supported read-only `ktx db report --output -` process.
Supported readers hold shared local-database ownership until their read-only
handle closes, so a supported writer waits before starting its SQLite
transaction.
Sustained `ktx run` streams are reported as a separate surface with eight
newline-delimited tool events per stream. Neither surface represents the
in-process component measurements in [component benchmarks](component-benchmarks.md).

The report also contains matched `writers-only` and `mixed-agent` workloads.
Each round initializes a fresh empty database without a hook source, then starts
four identified writer streams from one barrier. Each writer has a distinct
agent and session identity and submits eight unique one-shot hooks sequentially;
the four streams run concurrently. Exactly 32 hooks must complete and reconcile.
The mixed workload starts the same writers and one identified reader stream from
the same barrier. The reader performs eight read-only reports against that
database. Writer and reader attempts, completions, process-bounded outcomes,
latencies, failure codes, and metrics remain separate.

Across the five measured rounds, each workload reports actual hook p50, p95,
p99, and maximum latency, throughput, and failure rate. The matched contention
summary reports the writers-only and mixed values, the actual penalty, and its
percentage of the writers-only value. For latency and failure rate, penalty is
mixed minus writers-only. For throughput, penalty is writers-only minus mixed.
A percentage is zero when both values are zero, and `null` when a nonzero
penalty has a zero writers-only denominator. Timing is descriptive and is not a
hard gate.

Each round reports throughput, p50, p95, p99, maximum process latency,
retries, cumulative lock-wait time, stable failure-code counts, and exact
expected-versus-persisted tool-call reconciliation. `retries` counts harness
duplicate-delivery submissions. `cumulative_lock_wait_nanoseconds` sums worker
elapsed time for results classified `database-unavailable` and the declared
duration of deliberately held foreign locks; ktx does not expose its internal
retry count or lock-wait duration.

Ordinary rounds bootstrap one unmeasured hook source so the supported read-only
query has a current database from the start. `bootstrap_tool_calls` makes that
row explicit in each reconciliation total. The compatible-migration race uses
one bootstrap source, then deliberately makes its database stale before its
concurrent supported openers run.

The harness also runs bounded scenarios for:

- concurrent supported hooks split across canonical and hard-link database paths;
- a deliberately foreign held reader;
- a deliberately foreign held writer;
- `foreign-writer-reader-exhaustion`: a foreign writer held beyond the
  supported read-only wait, classified apart from supported concurrency;
- concurrent compatible migrations against a stale-compatible database with
  exact schema, marker, and integrity post-state checks;
- duplicate OpenCode hook delivery; and
- `supported-process-termination-ownership-recovery`: termination of a hook
  process while it owns supported-writer coordination and is blocked by a
  foreign writer, followed by a successful supported recovery write.

It separately records a real-process `blocked_reader_target`. A 2 MiB `ktx
read` output is deliberately backpressured after its first byte, then a
one-shot hook runs before that output is released. The target records the
blocked-reader outcome and hook latency independently, plus its exact
reconciliation result. It does not make a timing claim.

Supported writers coordinate each database transaction through the local
database file. The operating system releases that ownership when the process
ends. SQLite retries remain a defense against foreign clients, not the
supported-writer ownership boundary. The path-alias scenario reconciles each
accepted record against the same physical database file.

Scenario participants identify `supported-ktx` or `foreign-sqlite`. A foreign
holder result is never counted as a supported-concurrency failure. Each
scenario includes a bounded outcome, a process timeout, and a reconciliation
oracle. `mage loadBench` reports the result descriptively; the durable
engineering path rejects a mismatch, an unexpected supported lock failure, or
a missing required scenario without applying a timing threshold.

The durable engineering validator also requires both agent workloads. It
rejects missing hooks, failed readers, duplicate agent or session identities,
missing required participants, any supported-participant failure code in these
workloads, and expected-versus-persisted reconciliation mismatches.

## Safety checks

The harness rejects a non-empty incoming `KTX_DB` before it creates workers or
emits a successful report. It also validates all fixed fixture names, seeds,
sizes, and hashes before process startup. These checks prevent an ambient
database or an invalid fixture from becoming a measurement.
