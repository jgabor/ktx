# Real-process load measurements

`mage loadBench` measures disposable real `./build/ktx` processes. It is a
developer-only harness, not a `ktx` command. It does not change `ktx bench`
cost or quality semantics, and its timings are descriptive rather than gates.

Run it from the repository root:

```sh
mage loadBench > real-process-load.json
```

Mage rebuilds `./build/ktx` before starting the harness. The harness uses that
binary for every hook, wrapper, and read-only query process. It creates a new
temporary parent root, then a new owned database and HOME/XDG root for every
warm-up, measured round, and scenario. Each worker receives only an explicit
`KTX_DB` below its own root.

## Report contract

The JSON report has `schema_version: 1`, one warm-up round, and five measured
rounds. It records the local binary path and SHA-256, Go version, operating
system, architecture, processor, disposable environment names, fixed payload
names, seeds, byte counts, and hashes.

One-shot `ktx hook` processes use concurrency 1, 2, 4, 8, 16, and 32. Every
round also starts a supported read-only `ktx db report --output -` process.
Sustained `ktx run` streams are reported as a separate surface with eight
newline-delimited tool events per stream. Neither surface represents the
in-process component measurements in [component benchmarks](component-benchmarks.md).

Each round reports throughput, p50, p95, p99, maximum process latency,
retries, cumulative lock-wait time, stable failure-code counts, and exact
expected-versus-persisted tool-call reconciliation. `retries` counts harness
duplicate-delivery submissions. `cumulative_lock_wait_nanoseconds` sums worker
elapsed time for results classified `database-unavailable` and the declared
duration of deliberately held foreign locks; ktx does not expose its internal
retry count or lock-wait duration.

Ordinary rounds bootstrap one unmeasured hook source so the supported read-only
query has a current database from the start. `bootstrap_tool_calls` makes that
row explicit in each reconciliation total. The compatible-migration race is
the exception: it starts with no database and has zero bootstrap rows.

The harness also runs bounded scenarios for:

- concurrent supported hooks split across canonical and hard-link database paths;
- a deliberately foreign held reader;
- a deliberately foreign held writer;
- concurrent compatible migrations into a fresh database;
- duplicate OpenCode hook delivery; and
- `supported-process-termination-ownership-recovery`: termination of a hook
  process while it owns supported-writer coordination and is blocked by a
  foreign writer, followed by a successful supported recovery write.

Supported writers coordinate each database transaction through the local
database file. The operating system releases that ownership when the process
ends. SQLite retries remain a defense against foreign clients, not the
supported-writer ownership boundary. The path-alias scenario reconciles each
accepted record against the same physical database file.

Scenario participants identify `supported-ktx` or `foreign-sqlite`. A foreign
holder result is never counted as a supported-concurrency failure. Each
scenario includes a bounded outcome, a process timeout, and a reconciliation
oracle. A mismatch is evidence about current behavior, not a harness crash.

## Safety checks

The harness rejects a non-empty incoming `KTX_DB` before it creates workers or
emits a successful report. It also validates all fixed fixture names, seeds,
sizes, and hashes before process startup. These checks prevent an ambient
database or an invalid fixture from becoming a measurement.
