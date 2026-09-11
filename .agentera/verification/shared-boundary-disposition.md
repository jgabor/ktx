# Shared-boundary disposition

2026-09-07 · assessment `iefdatbpxu` · plan `hljjrlykxb` · decision
[`rholpeffdq`](../entities/decisions/decision/rholpeffdq.yaml), under standing
authority [`yohiahsqjv`](../entities/decisions/decision/yohiahsqjv.yaml).
Runtime implementation and current-fixture qualification by `wxwwonhjgy` are
recorded below. Task lifecycle completion remains with its assigned owner.
This is not task completion, an audit, or decision satisfaction.

## Evidence scope

Use the supplied verified analysis `ses_f82f541daffe8EMThxNT7TXjuF`, not a repeated
broad survey. The historical [coverage ledger](review-remediation-coverage.md#architectural-and-verification-observations)
remains unchanged. Targeted source reads in this docs-only assessment reconfirmed
the shared hook boundary, ACK/payload ordering, serialized callbacks, required
pending deletion, and DB ordinal comparison. Counts and the probe below are
inherited analysis, not new executions or whole-repository measurements.

## Scoped dispositions

| Finding | Remaining cost and evidence | Selected disposition |
| --- | --- | --- |
| D1 | Large CLI/experiment/DB packages concentrate navigation and ownership work; prior physical LOC includes blanks/comments and does not establish an executable-coverage deficit or a useful split boundary. | No broad split. Size alone does not justify migration or new abstractions. |
| D2 | Parser imports `db.ToolCall` as a business record, without SQL; router reads `Tool`, `Input`, `IsError`. `Parser.ParseEvent` has two production consumers (`bench_fixture_export`, `componentbench`), `ParseEventWithCorrelation` one runtime consumer (`run`); `router.Resolve` has three external consumers plus internal `Match`. Domain-field changes require coordinating normalization, routing and persistence; SQL ownership is not leaking into parser/router. | Keep the shared record. Independent DTOs add conversion ownership. Domain-package extraction with aliases is possible, but has no measured benefit here. The 187 gopls workspace/test references are not a count of mandatory edits under alias extraction. |
| D3 | Go/TypeScript normalization and deduplication require cross-language conformance, not a speculative shared runtime. Hook/schema, stats/report, flag handling and experiment execution/validation already have correctness owners in the ledger. The latest telemetry evidence there records shared fixtures, durable ACK/redelivery, snapshot and nonfinite-value conformance. | No new framework or duplicated ownership. Preserve prior correctness/conformance work and its historical verification scope; this assessment neither reruns nor globally certifies it. |
| D5 | Runtime-specific extraction already joins common processing after `internal/cli/hook.go:503`; retained artifacts bypass replacement transforms at `:556`. Session/source/artifact ownership already has a shared path. | No hook-flow rebuild. Reconcile the overstated existing backlog, leaving conformance and lifecycle closure with the assigned owners. |
| S9 | Two Bun invocation helpers share about eight spawn/wait lines but differ in payload handling and failure policies. | Keep the two helpers. A third helper adds a policy seam without demonstrated maintenance savings. |
| S10 | Optional `observed` entries retained exact evidence and a larger serialized persistence payload after ACK, although only the evidence and accepted bit were read afterward. Assistant identity associations and exact deduplication indices still grow with observed identities. | Acknowledged payload release implemented and qualified below; same key, exact evidence and `accepted=true` remain. Assessment closed under the selected tradeoff: runtime-instance-lifetime indices, linear state and no restart durability. This is not a fully bounded memory fix or lifecycle completion. |

## Why the payload can be released, but membership cannot

At `internal/cli/init.go:615-618`, an existing entry first compares exact evidence
for conflict and then returns if accepted. Only unaccepted delivery reaches
`ordinaryCommand` and ACK comparison (`:629-631`); the subprocess has finished
before acceptance is marked. Callbacks are serialized by `eventQueue` (`:670-674`).
Thus valid ACK can release the payload without removing the ability to reject
conflicting redelivery or suppress an exact retry.

Failed, unacknowledged, uncertain and in-flight deliveries must retain their first
payload, ordinal and capture time. Assistant associations are unchanged; required
collector pending payloads are already deleted after ACK (`:655-658`). No digest
substitution, membership eviction, new ordinal/time, reset, or late-identity loss
is selected. Eviction could assign a different ordinal on redelivery: database
retry matching includes ordinal in `internal/db/session_telemetry.go:83-95,129-150`.

The inherited 256-pair probe counted **244,672 retained string bytes**, excluding
objects, maps and runtime overhead. Payload is larger than its associated exact
evidence, so the derived lower bound on released strings is **greater than
122,336 bytes**. This is not an actual post-implementation measurement, heap
reduction or RSS claim. Removing the redundant payload reduces retention, not
asymptotic identity-store growth. A bounded identity-store protocol/schema is
only a separately justified future discussion, not a feature selected now.

## Qualification owned by `wxwwonhjgy`

The existing three cleanup/no-change/conformance acceptance criteria remain
unchanged. The selected cleanup removes duplicate acknowledged-payload retention;
the table supplies the explicit no-change evidence for the other boundaries.

- Valid ACK leaves payload absent/released but the same key, exact evidence and accepted state.
- Exact acknowledged redelivery invokes no subprocess; conflicting redelivery still errors.
- Failed, uncertain and in-flight deliveries preserve first payload, ordinal and capture time.
- Overlapping delivery converges on one persisted row; assistant associations and required-collector state remain unchanged.
- Measure actual before/after retained strings for the same 256-pair workload, or a scoped heap probe, and distinguish it from the derived bound above.
- Run targeted generated-adapter/ACK/redelivery and shared conformance checks with the local build and disposable application/database roots. Broaden for failure or scope, not for a docs-only claim.

No product files, new types, dependencies, schema, caps or runtime changes belonged
to the initial assessment. `ARCHITECTURE.md` needs no conceptual change: the choice
preserves the existing ingress and persistence map and creates no unresolved
architectural question. No historical findings or satisfaction records are rewritten.

## Implementation qualification — `wxwwonhjgy`, 2026-09-07

`internal/cli/init.go` now represents optional delivery as an inline discriminated
union: unaccepted entries require a string payload; accepted entries have only
exact evidence and `accepted: true`. Only validated durable ACK replaces the
entry at the same key with the accepted representation. Exact conflict comparison
still precedes the accepted early return. No new protocol, cap, schema, digest,
ordinal/time reset, membership eviction, helper or package extraction was added.
Assistant associations and the required collector's pending path are untouched.

`telemetry_retention_local_test.go` installs the generated adapter with the local
binary, then adds a test-only accessor to the actual collector maps. Real Bun
callbacks invoke the pinned local CLI against owned databases. The tests prove:

- In-flight and uncertain ACK state keeps the same first pending object, payload,
  ordinal and capture time. A lost ACK occurs after a real committed write.
- A validated ACK leaves exact evidence and accepted membership but no payload
  property. An accepted exact retry spawns no subprocess, while an accepted
  conflict still emits its conflict diagnostic and changes neither membership
  nor the accepted entry.
- Overlapping retries leave one durable row with the original time, ordinal,
  cost and unknown provider/model. A late assistant association remains available
  to a later distinct part without enriching the first row or resetting order.
- All 14 existing ACK/redelivery scenarios retain their assertions; added map
  assertions verify payload retention on failure and removal on acceptance.
  Required failures retain pending payloads and required ACKs delete them, without
  populating optional membership. The 160-case Go and 160-case generated-adapter
  measurement suites and the nonfinite snapshot regression still pass.

### Actual paired retained-string measurement

The new repeatable fixture delivers 256 assistant/step pairs, sequentially, with
one runtime session and distinct message/part IDs. Both variants use exactly that
fixture, freshly installed generated code and fresh owned databases. The control
restores only the former post-ACK `delivery.accepted = true` operation; the
candidate runs the generated release operation. Each variant persists exactly
256 rows with ordinals 1–256 and retains 256 members plus 256 assistant identities.

| Retained string fields (UTF-8 bytes) | Former-operation control | Released payload |
| --- | ---: | ---: |
| Exact evidence | 89,892 | 89,892 |
| Serialized payload | 113,592 | 0 |
| Assistant provider/model | 3,328 | 3,328 |
| Both maps' keys | 15,286 | 15,286 |
| **Total** | **222,098** | **108,506** |

**Actual measured decrease: 113,592 summed string-field bytes.** The transport
counts and forwards chunks without retaining a payload corpus. Both variants
write 113,592 payload bytes; the test compares their non-payload totals and proves
that the retained total decreases by exactly the acknowledged payload total.
This sums logical retained string fields, not unique physical string allocations;
it excludes map/object overhead, heap and RSS. No GC/heap claim is made.

The inherited 244,672-byte probe above has no retained executable fixture in the
supplied artifacts or temporary probes checked in this session. Its exact workload
and accounting were therefore **not reproduced**. Its greater-than-122,336-byte
release bound remains inherited and derived, not a measured result. Do not compare
that total/bound numerically to this different, explicitly defined paired fixture
or claim historical baseline qualification. Current paired retention and durable
semantics are verified; exact historical-fixture reproduction remains unavailable.

### Checks and remaining ownership

Every verification invocation used a fresh disposable HOME, XDG data/config/cache/
state/runtime and TMP root, unset `KTX_DB`, `GOPROXY=off`, and owned offline fixture
databases. The local binary was built with `mage build` before CLI tests, with
`KTX_ADAPTER_CLI=~/git/ktx/build/ktx`. No provider or daemon was invoked.

Passing commands under that isolated environment:

```sh
mage build
go test ./internal/cli -run '^TestTelemetryAdapterLocal(PayloadRelease|PairedRetention|DurableRedelivery)$' -count=1 -v
go test -race ./internal/telemetry/... ./internal/cli ./internal/db -run 'Test(Telemetry.*|WrapperTelemetry.*|Collector.*|ContractFixtures|SessionTelemetry.*|RuntimeEvent.*|RequiredCollector.*|Opencode.*|GeneratedOpenCode.*|InitAndTelemetryHelpDocumentOpenCodeIntegration)$' -count=1
mage check
go test ./internal/cli -run '^TestTelemetryAdapterLocal(PayloadRelease|PairedRetention)$' -count=1 -v
env -u KTX_ADAPTER_CLI go test ./internal/cli -run '^TestTelemetryAdapterLocal(PayloadRelease|PairedRetention)$' -count=1 -v
git diff --check
```

The no-local-binary invocation verifies the intended skip, not adapter behavior.
Full `mage check` passed Go tests, 14 Python tests, the required generated-adapter
overlap gate, vet, lint (0 issues) and build. Final boundary `39bb512510a8e445`
recorded 22,724 DB attempts, forbidden=0, `ktx_db=unset`. Focused race includes canonical
pinning, envelopes, runtime controls, required collection and shared conformance.

S10's boundary assessment is closed with the explicit selected tradeoff, not a
fully bounded memory repair: exact evidence and identity maps remain linear and
runtime-instance-local, not restart-durable. The table's D1/D2/D3/D5/S9 no-change
dispositions stand. No separate bounded-store feature is selected. Existing
[workspace coverage](workspace-coverage-2026-09-07.md) remains its unchanged
point-in-time baseline, not coverage of this final source state; `bqnxtbzjou` owns
the final metadata refresh. No task status, evaluation, audit or satisfaction was
mutated, and no staging, commit or push was performed.
