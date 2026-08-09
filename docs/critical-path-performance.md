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

## Participants and availability

Each participant owns only the span below. No participant may claim another
participant's outcome.

| Participant                  | Ownership span                                                                                                                               | Required outcome                                                                                                                                                                                                    |
| ---------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Supported writers            | `write`, `compress`, `hook`, `run`, telemetry recording, and benchmark collection while they create or change ktx evidence.                  | Commit their own evidence transaction or report unavailability after bounded retries. An accepted source remains durable, and an eligible exact retry does not duplicate that source or its authoritative artifact. |
| Supported read-only commands | `get`, `read`, `list`, `stats`, `decompress`, `bench report`, telemetry inspection, `doctor`, `db report`, and retained-artifact inspection. | Open only the existing current-schema database in read-only mode. They do not create or migrate product state.                                                                                                      |
| Compatible migrations        | A write-capable opener and explicit `ktx db migrate` while an older supported schema is upgraded.                                            | Own one forward transactional migration. It either commits the complete migration or rolls back without changing schema version or evidence.                                                                        |
| Mixed-version binaries       | The binary that opens a database and compares its supported schema range.                                                                    | A binary never writes a newer database it cannot understand. It reports schema incompatibility and directs recovery by moving the binary forward, never by lowering the database version.                           |
| Foreign SQLite clients       | Their own process, lock, transaction, and release schedule.                                                                                  | ktx has no progress guarantee while a foreign client retains contention. After bounded retries, ktx reports database availability without exposing driver diagnostics.                                              |

## Hook acceptance

Hook processing has three separate acceptance points, in this order:

| Acceptance             | Commit and ownership                                                                                                                                                               | Required delivery behavior                                                                                                                                                                                                  |
| ---------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Source acceptance      | The source tool-call row and its session roll-up commit. This is the first acceptance point.                                                                                       | A pre-source failure is unaccepted. No source, artifact, or disposition is claimed, and the adapter preserves the original tool output.                                                                                     |
| Artifact acceptance    | A compressed artifact commits only after its accepted source exists. The first accepted artifact is authoritative.                                                                 | A later artifact-phase failure does not undo the accepted source. The adapter preserves the original tool output; an eligible exact retry can reuse or finish the authoritative artifact without duplicate source evidence. |
| Disposition acceptance | The source-keyed disposition projection commits after source acceptance and, when present, artifact acceptance. It may promote to the authoritative artifact but never replace it. | A later disposition failure does not retract an already committed source or artifact. The adapter preserves the original tool output, and a retry remains safe.                                                             |

Each committed record is durable and retry-safe. The accepted source is
persisted before any lossy delivery. These phases are not interchangeable: a
source record alone does not prove artifact or disposition acceptance.

## Database-unavailable result

When bounded SQLite lock retries end without an accepted operation, the result
uses this stable surface:

- error code `database-unavailable`;
- process exit `4`;
- canonical `database_path` and a non-empty `correction` in JSON; and
- a text message that identifies the database and tells the caller to wait for
  the other database client to finish, then retry.

The rendered result does not include driver lock wording. It applies to a
read-only open, write open, compatible migration, source persistence, artifact
persistence, and disposition persistence when the operation surfaces the
availability failure.

## Development verification storage

Each development verification invocation receives a fresh disposable root.
Its `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`,
`XDG_STATE_HOME`, and temporary-directory root are distinct directories beneath
that root. Inherited `KTX_DB` is removed before the command starts. Ktx database
access during the invocation accepts only database paths beneath that root and
rejects the ambient live database before SQLite or filesystem effects. The
manual wrapper sets `KTX_VERIFY_ROOT` to enforce that boundary.

Use the exact setup in the [database safety contract](database-safety.md#hermetic-development-and-ci-verification).
