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

### Focused `ktx read` measurements

The report also measures the production `ktx read` path against fixed
`ktx-read-output` source records with 1024, 65536, 1048576, and 2097152-byte
outputs. Each focused result retains its workload name, byte count, generator
seed, SHA-256, latency, allocated bytes, and allocation count. The runner sets
`KTX_DB` only to a database beneath its own temporary root for this operation;
it does not resolve, create, or alter product telemetry.

### Focused `ktx decompress` measurements

The report measures the production `ktx decompress` path against fixed
`ktx-decompress-output` records with recovered outputs of 1024, 65536,
1048576, and 2097152 bytes. Each result retains the stable workload name,
kind, byte count, generator seed, SHA-256, latency, allocated bytes, and
allocation count. The source and compressed rows exist only in a database
beneath the runner's temporary root.

## Result contract

The JSON document has `schema_version: 3`. It records UTC RFC3339Nano
`started_at`, `finished_at`, and non-negative `duration_nanoseconds` values.
Each ordinary benchmark retains its versioned
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
