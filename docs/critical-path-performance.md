# Critical-path performance and availability contract

This contract defines the engineering evidence used to change ktx's critical
path. It does not add a public performance command, a daemon, or a new product
benchmark. Timing stays descriptive until a controlled host, workload, sample
policy, variance rule, and numeric budget are approved.

## Evidence classes

The following classes answer different questions. A result in one class does
not substitute for a result in another.

| Evidence class           | Required boundary                                                                                                                                | Does not establish                                                             |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------ |
| Component ceilings       | A fixed in-process operation and input, measured by a component benchmark. Record operation-local latency, allocation, or throughput.            | Real-process latency, product cost or quality, or concurrent correctness.      |
| Real-process latency     | A locally built `./build/ktx` process, measured from invocation through its completed output under a declared workload.                          | A component ceiling, task quality, provider cost, or contention reliability.   |
| Product cost and quality | The existing `ktx bench` cost and quality evidence, with its retained artifacts and unchanged scoring semantics.                                 | Local process latency, component performance, or a provider performance claim. |
| Concurrency reliability  | A deterministic supported-participant contention scenario that checks accepted records, retry convergence, and the rendered availability result. | A latency ceiling, product cost, or task quality result.                       |

The developer-only [component benchmark measurements](component-benchmarks.md)
runner supplies the first evidence class. It does not change `ktx bench`.
The developer-only [real-process load measurements](real-process-load-benchmarks.md)
runner supplies the second class and reports current contention behavior without
changing it.

## Durable engineering verification

Use a new caller-chosen directory to retain the component and real-process
reports together:

```sh
mkdir -p docs/notes
KTX_ENGINEERING_EVIDENCE_DIR="$PWD/docs/notes/engineering-run" mage engineeringVerify
```

The directory must not already exist. The target runs `mage componentBench`,
`mage loadBench`, and `mage decompressProcessBench` in separate fresh Mage
roots. It writes `component-bench.json`, `real-process-load.json`,
`blocked-decompression.json`, and `manifest.json`. These versioned reports and
the schema-2 manifest record UTC RFC3339Nano
`started_at`, `finished_at`, and non-negative `duration_nanoseconds`. The
manifest records each report's schema version and SHA-256; the reports retain
their environment and workload identities. Retained schema-1 evidence remains
unchanged and has no inferred timing. The target does not write
product SQLite state.

The current component report is schema 3, the load report is schema 3, and the
blocked-decompression report is schema 2. The manifest remains schema 2.

This is extended engineering evidence, not a timing gate or a replacement for
`mage check`. It fails on reporter validation, the load harness's mandatory
correctness, reconciliation, and supported-lock checks, or an invalid
blocked-decompression database-availability result. It does not compare timing
values to a threshold. Run `mage check` separately for the repository's
ordinary test, mixed-version, and contract coverage. The target is not part of
`mage verify` or tracked CI until a stable host and timing policy are approved.
It does not change `ktx bench` cost or quality evidence, or make a provider
claim.

### Focused read comparison

`tools/readleasecompare` is a developer-only retained-evidence comparator for
the bounded `ktx read` ownership experiment. It accepts exactly ten new
engineering-evidence directories in `B-C-C-B-B-C-C-B-B-C` order. Its explicit
mapping gates only the four focused `ktx read` workload medians (latency,
allocated bytes, and allocation count), one-shot hook concurrency-1 p50,
concurrency-32 p99 and throughput, and the separately recorded blocked-reader
target. It uses the median run center and `1.4826 * MAD / baseline_center` for
normalized baseline noise. All other component measurements remain diagnostic.
New reports use comparison schema 2 and policy `read-lease-v2`. Existing schema-1
`read-lease-v1` reports remain historical evidence and are not reinterpreted.

The policy permits 5 percent practical regression for focused-read latency,
one-shot hook concurrency-1 p50, and concurrency-32 throughput; it permits 10
percent for focused-read allocated bytes and allocation count and for
concurrency-32 p99. Each applicable maximum or minimum also includes measured
baseline noise. Baseline noise is limited to 5 percent for every metric except
concurrency-32 p99, whose limit is 10 percent. Stable metrics retain the existing
`pass` or `regression` result. Every metric reports its allowed maximum or
minimum, including metrics whose baseline exceeds the noise limit.

Allocation metrics always remain retention gates using their practical
allowance plus measured baseline noise. On sequence 1, any timing metric above
its baseline-noise limit is `inconclusive` and
permits exactly one complete fresh sequence. On sequence 2, a noisy timing metric is a
`pass_non_authorizing_noisy` only when the candidate center remains within its
practical allowance plus measured baseline noise. A candidate center outside
that allowance is a `regression`. Sequence 2 never permits another run. A noisy
final-sequence timing pass is non-authorizing: it permits retention with the
other gates, but it cannot support a speed or latency-improvement claim.

The retained internal hard gates are exact reconciliation, supported-failure
behavior, byte identity, stable per-arm binary identity, and every candidate
blocked-reader target. The final comparison also requires external `read_only`,
`absent_database`, `stale_schema`, `newer_schema`,
`output_failure_recovery`, `termination_recovery`,
`mage_engineering_verify`, and `mage_check` gates. Any failed hard gate rejects
the candidate and overrides timing or noise. A final sequence retains only when
all hard gates and allocation metrics pass, all stable timing metrics pass,
every candidate blocked-reader target passes, and every noisy metric remains
within its allowance.

Build the comparator locally, then give it the retained directories in their
actual order:

```sh
go build -buildvcs=false -o build/readleasecompare ./tools/readleasecompare
./build/readleasecompare --sequence 1 \
  --run B=/tmp/evidence-baseline-1 \
  --run C=/tmp/evidence-candidate-1 \
  --run C=/tmp/evidence-candidate-2 \
  --run B=/tmp/evidence-baseline-2 \
  --run B=/tmp/evidence-baseline-3 \
  --run C=/tmp/evidence-candidate-3 \
  --run C=/tmp/evidence-candidate-4 \
  --run B=/tmp/evidence-baseline-4 \
  --run B=/tmp/evidence-baseline-5 \
  --run C=/tmp/evidence-candidate-5 \
  --output /tmp/read-lease-comparison.json
```

The first sequence permits one complete fresh sequence only when its measured
verdict is inconclusive and all internal hard gates pass. Every invocation may
supply each external hard-gate value with repeated `--gate name=true|false`
arguments. Before final retention, the comparator requires every named
external hard-gate value with repeated `--gate name=true|false` arguments. A
failed external gate produces `reject` even when timing is inconclusive; omitted
final gates cannot produce `retain`.

## Participants and availability

Each participant owns only the span below. No participant may claim another
participant's outcome.

| Participant                  | Ownership span                                                                                                                               | Required outcome                                                                                                                                                                                                                                                                                                                                            |
| ---------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Supported writers            | `write`, `compress`, `hook`, `run`, telemetry recording, and benchmark collection while they create or change ktx evidence.                  | Coordinate each database transaction through the local database file, then commit their own evidence transaction or report unavailability after bounded retries. The operating system releases ownership when a process ends. An accepted source remains durable, and an eligible exact retry does not duplicate that source or its authoritative artifact. |
| Supported read-only commands | `get`, `read`, `list`, `stats`, `decompress`, `bench report`, telemetry inspection, `doctor`, `db report`, and retained-artifact inspection. | Hold shared local-database ownership while they open and query only an existing database in read-only mode. They do not create or migrate product state.                                                                                                                                                                                                    |
| Compatible migrations        | A write-capable opener and explicit `ktx db migrate` while an older supported schema is upgraded.                                            | Own one forward transactional migration. It either commits the complete migration or rolls back without changing schema version or evidence.                                                                                                                                                                                                                |
| Mixed-version binaries       | The binary that opens a database and compares its supported schema range.                                                                    | A binary never writes a newer database it cannot understand. It reports schema incompatibility and directs recovery by moving the binary forward, never by lowering the database version.                                                                                                                                                                   |
| Foreign SQLite clients       | Their own process, lock, transaction, and release schedule.                                                                                  | ktx has no progress guarantee while a foreign client retains contention. After bounded retries, ktx reports database availability without exposing driver diagnostics.                                                                                                                                                                                      |

### Closed command vocabulary

The command that first accepts a source or artifact is selected from the
invocation surface, never from payload fields or content. The closed producer
labels are:

| Invocation surface                   | Source label     | Artifact label      |
| ------------------------------------ | ---------------- | ------------------- |
| `ktx write`                          | `write`          | not applicable      |
| `ktx hook`                           | `hook`           | `hook`              |
| `ktx run`                            | `run`            | not applicable      |
| `ktx compress`                       | not applicable   | `compress`          |
| component benchmark fixture creation | `componentbench` | `componentbench`    |
| load benchmark fixture creation      | not applicable   | `loadbench-fixture` |

Historical or otherwise unattributed rows use `unknown`; they are not inferred
from row content. Source and artifact labels are independent because a later
explicit compression can create an artifact for a source accepted elsewhere.
Schema 11 stores the nullable label on each source and artifact row. The label
is part of the row's existing acceptance transaction. Exact and concurrent
retries keep the first row's value and do not fill a NULL value. The migration
adds no provenance index and leaves historical values NULL. Full structured
source and artifact rows expose the label; content-only output is unchanged.

The closed labels for supported ktx processes that acquire database
coordination are `write`, `compress`, `hook`, `run`, `get`, `read`, `list`,
`stats`, `decompress`, `bench`, `telemetry record`, `telemetry list`, `doctor`,
`db migrate`, and `db report`. For every label, verified live identity maps to
that label. A foreign executable, unavailable identity, ambiguous identity, PID
reuse, or an unsupported command maps to `unknown`. Arguments and payload
content never supply or repair a label. Linux coordinator-timeout diagnostics
emit the verified label, or explicit `unknown`, beside each observed holder's
PID and held mode.

## Hook acceptance

Hook processing has three separate acceptance points, in this order:

For a current-schema delivery that can change evidence, parsing, configuration
loading, cost derivation, route selection, and transformation finish before one
OS-level exclusive coordination window begins. Source, artifact, and
disposition then use the three separate SQLite commits below while that one
window remains held. This is phased durability, not one atomic transaction.
Final source validation runs inside the window so a preflight race cannot
replace first-wins evidence. Pre-source rejection and a fully satisfied exact
retry use bounded reads without exclusive coordination or a write transaction.
Database creation and stale-schema migration retain their lifecycle ownership.

| Acceptance             | Commit and ownership                                                                                                                                                               | Required delivery behavior                                                                                                                                                                                                  |
| ---------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Source acceptance      | The source tool-call row and its session roll-up commit. This is the first acceptance point.                                                                                       | A pre-source failure is unaccepted. No source, artifact, or disposition is claimed, and the adapter preserves the original tool output.                                                                                     |
| Artifact acceptance    | A compressed artifact commits only after its accepted source exists. The first accepted artifact is authoritative.                                                                 | A later artifact-phase failure does not undo the accepted source. The adapter preserves the original tool output; an eligible exact retry can reuse or finish the authoritative artifact without duplicate source evidence. |
| Disposition acceptance | The source-keyed disposition projection commits after source acceptance and, when present, artifact acceptance. It may promote to the authoritative artifact but never replace it. | A later disposition failure does not retract an already committed source or artifact. The adapter preserves the original tool output, and a retry remains safe.                                                             |

Each committed record is durable and retry-safe. The accepted source is
persisted before any lossy delivery. These phases are not interchangeable: a
source record alone does not prove artifact or disposition acceptance.

Ktx has no durable spool. An event blocked before source acceptance is
unaccepted, and ktx makes no zero-loss claim for that event. Ktx makes no progress guarantee while a foreign SQLite client holds a lock indefinitely.

## Database-unavailable result

When bounded SQLite lock retries end without an accepted operation, the result
uses this stable surface:

- error code `database-unavailable`;
- process exit `4`;
- canonical `database_path`, `executable_path`, and `executable_sha256`;
- `failure_class` set to `coordinator-timeout` or
  `sqlite-lock-exhaustion`;
- `operation_phase` set to `open`, `migration`, `source`, `artifact`,
  `disposition`, or `query`;
- non-negative `elapsed_wait_nanoseconds` and a non-empty `correction` in JSON;
- a text message that identifies the database and tells the caller to wait for
  the other database client to finish, then retry.

The rendered result does not include driver lock wording. It applies to a
read-only open, write open, compatible migration, source persistence, artifact
persistence, and disposition persistence when the operation surfaces the
availability failure.

For ordinary OpenCode callbacks, the generated plugin validates those fields
against its pinned executable and database target, adds `runtime_surface` as
`opencode-hook` or `opencode-telemetry`, and emits one sanitized diagnostic.
It does not copy unrecognized fields from command stderr.

## Development verification storage

Each development verification invocation receives a fresh disposable root.
Its `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`,
`XDG_STATE_HOME`, and temporary-directory root are distinct directories beneath
that root. Inherited `KTX_DB` is removed before the command starts. Ktx database
access during the invocation accepts only database paths beneath that root and
rejects the ambient live database before SQLite or filesystem effects. The
manual wrapper sets `KTX_VERIFY_ROOT` to enforce that boundary.

Use the exact setup in the [database safety contract](database-safety.md#hermetic-development-and-ci-verification).
