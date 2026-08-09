# Component benchmark measurements

`mage componentBench` is a developer-only measurement runner. It does not add a
`ktx` command, change `ktx bench` cost or quality semantics, or make a product
performance claim.

For a retained component report paired with the real-process report, use the
[durable engineering verification path](critical-path-performance.md#durable-engineering-verification).

Run it through Mage so it receives a fresh disposable application root:

```sh
mage componentBench > component-bench.json
```

For a focused manual run, use the `ktx_verify` wrapper from the
[database safety contract](database-safety.md#hermetic-development-and-ci-verification):

```sh
ktx_verify go run ./tools/componentbench > component-bench.json
```

## Workloads and cases

The runner generates fixed 1024-byte, 65536-byte, and 1048576-byte workloads
for `plain-text`, `json`, and `ndjson`. Each result records the stable workload
name, byte count, generator seed, and SHA-256 hash.

It measures these components for every workload:

| Component           | Ordinary case                     | Adverse case                            |
| ------------------- | --------------------------------- | --------------------------------------- |
| Parser              | Accepted tool event               | Malformed event                         |
| Configuration       | Valid TOML                        | Malformed TOML                          |
| Router              | Matched route                     | Malformed command input                 |
| Each built-in stage | Valid stage input                 | Malformed input                         |
| Complete pipeline   | Transforming pipeline             | Passthrough and failing-stage pipelines |
| Persistence         | Accepted temporary-database write | Rejected temporary-database write       |

Persistence uses only a database created beneath the runner's temporary root.
It does not resolve `KTX_DB`, create product telemetry, or alter the product
database schema.

## Result contract

The JSON document has `schema_version: 1`. Each benchmark also has a versioned
`schema_version` and identifies its component, case, workload, and processed
bytes per operation. A sample reports:

- operations and processed bytes;
- latency in nanoseconds per operation;
- throughput in bytes per second; and
- allocated bytes and allocations per operation.

The document includes Go version, operating system, architecture, processor,
and filesystem identity. It records one warm-up sample and five measured
samples per benchmark, plus minimum, median, maximum, and mean distributions.
Compare distributions only when the workload and machine identity match. The
results expose variance; they are not hard timing gates on uncontrolled hosts.
