# Recording resource lifecycle — scoped decision evidence

2026-09-06. Active task `jfgymxttel`, plan `hljjrlykxb`; implementation owner
`mljcarlewi`. Documentation and disposable measurements only. PERF1 is **not
fixed**, and neither task is completed by this report. Complete canonical choice:
[`zyeasphydk`](../entities/decisions/decision/zyeasphydk.yaml). The earlier
[`jyykravpup`](../entities/decisions/decision/jyykravpup.yaml) and progress
`szjxgyhosj` remain unchanged provisional history. The completion below resolves
their benchmark coverage gate, not their implementation or satisfaction status.
Completion progress: [`vrcmswcope`](../entities/progress/progress_cycle/vrcmswcope.yaml).

## Authority and corrected feasibility

Standing authority `yohiahsqjv` permits measured internal budgets and private
temporary materialization, but not smaller accepted inputs, a schema change,
authoritative partial replay, or automatic deletion of the last evidence copy.
Linux scope `ygcysqxcfr` and terminal capability boundary `llshvynohy` remain.
Completed producer/session ownership and its receipt (`jgvodxuxul`, progress
`vjwekaemvb`) remain authoritative; capture does not mint a substitute receipt.
Archive confinement and the existing entry-contract matrix remain unchanged.

The supplied analyze session `ses_f8a23cf36ffer1wnt7IAN5Xjry` corrected an initial
overbroad impossibility conclusion. The correction is valid: **bounded recording
collection is feasible without changing TEXT storage**, but does not make
publication or replay input-independent in memory. This report does not repeat
the broad driver investigation or endorse the initial impossibility claim.

Locally confirmed seams:

- `internal/cli/run.go`: `streamBuf` collects the whole forwarded stream;
  `captureStreamForRun` converts bytes to a string and hashes it.
- `internal/cli/bench_run.go:218–275,341–350`: normal bench consumes stdout for
  aggregate tokens/cost/cache/reasoning, first model/provider, final answer and
  exact-match score, and completed recording publication. Both stdout and stderr
  are unbounded `bytes.Buffer`s. `streamMetrics.modelSteps` is assigned at line
  581 but normal bench never consumes it; an equivalence test does. Rich usage
  evidence remains required by experiment consumers, not this normal bench path.
- `internal/usage/usage.go:189,222,345`: the aggregator retains each ModelStep,
  then Result copies the slice. Aggregate-only interpretation can instead retain
  constant-count sums/counters and bounded metadata; it must preserve exact
  arithmetic, including positive/negative cancellation, not saturate sums early.
- `bench_run.go:448–493,501–505` and `usage.go:423–492`: answer extraction uses a
  4 MiB Scanner that **stops** after an oversized line and returns its last prior
  candidate; usage uses a 64 MiB record reader that **skips and continues**.
  Separate bounded passes preserve both existing behaviors; do not silently fix
  scoring while removing buffers. Latest answer candidates and first model/provider
  are constant-count and bounded by the respective record limits.
- `bench_run.go:201–207`: only wrapper failure prints the warning, then (if stderr
  is nonempty) `--- wrapper stderr ---\n`, raw stderr, and
  `--- end wrapper stderr ---\n`. There is **no TrimSpace and no inserted newline**
  between stderr and the closing delimiter. Thus a non-newline-terminated stderr
  ends directly against that delimiter. `TrimSpace` belongs to exact-match scoring,
  not diagnostic forwarding. Byte identity here concerns captured stderr; stdout
  still has the wrapper's existing normalization.
- `internal/db/recorded_streams.go`: `InsertRecordedStream` binds all seven
  columns in a normal write transaction; `GetRecordedStreamBySession` scans the
  whole TEXT. `runBenchReplayImpl` then hashes, parses, computes metrics and
  measures compression. These are the actual current names, not a presumed
  separate incremental replay API.
- Go docs for pinned `modernc.org/sqlite` v1.55.0 expose `Limit(*sql.Conn, id,
  newVal)`, not a public incremental blob handle. Native SQLite blob APIs do
  not grant safe access to the driver's private handle. Unsafe extraction,
  driver replacement and schema migration are not selected.

Let **L** be the existing effective `SQLITE_LIMIT_LENGTH`, in bytes, queried
with `sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_LENGTH, -1)` on the **reserved
publication connection**. Do not change the limit. A connection from an unrelated
pool is not authority for the INSERT. Capture may use the existing compile
ceiling (1,000,000,000 bytes) provisionally; publication must use its actual
connection and preserve existing writer admission, transaction/retry and schema
checks. A retry using another connection must query that connection too. Do not
hold an exclusive database writer lease throughout child execution.

Raw stream bytes greater than L cannot persist through this whole-value INSERT.
That is an existing impossibility boundary, not permission to cap forwarding or
benchmark acceptance. For **every N <= L**, attempt the ordinary INSERT:
row headers and the other six columns also count, so subtracting a guessed
metadata allowance would discard possible prior successes. Empty and nonzero-exit
completed recordings remain supported. Forwarding keeps its existing newline
normalization and parser boundaries, not a new byte-exact promise.

## Bounded offline measurements

Command: `python /tmp/opencode/recording-resource-probe.py`; additional dense
step sample: `python /tmp/opencode/recording-resource-steps.py`.
The scripts compiled this checkout's CLI test package with an ephemeral Go
overlay and ran each phase in a fresh process. No runtime source file was edited.
Build command: `go test -c -overlay=/tmp/opencode/recording-resource-overlay.json
-o /tmp/opencode/recording-resource-probe.test ./internal/cli`.

Every build/test invocation had fresh disposable HOME and all five specified
HOME/XDG variables (HOME plus data/config/cache/state), `KTX_DB` unset, explicit
test-owned DB paths, `GOPROXY=off`, `GOTOOLCHAIN=local`, `GOMAXPROCS=2`, and existing
external Go build/module caches. No provider, real archive or ambient database
was used. Replay opened only the explicitly seeded disposable publication DB,
read-only, under a different fresh application environment. Fixtures were removed
only because they were generated disposable test data, not retained evidence.

Safety: checked available RAM and temporary filesystem capacity before every
representative phase; stop thresholds were 2 GiB available RAM and 512 MiB free
temporary space. Observed available RAM was 6,171,492,352–6,255,730,688 bytes;
temporary free space was at least 5,166,895,104 bytes. Swap was almost full, so
fixtures were restricted to **1, 8 and 16 MiB**, sequentially. `/tmp` is **tmpfs**:
spooled pages consume system memory even when excluded from process RSS. This is
not evidence of disk-device latency or of reduced total host memory pressure.

Each fixture repeats a 256-byte newline-terminated JSON text event, answer `4`,
with padding. Capture writes 64 KiB chunks. Buffer mode uses `bytes.Buffer`;
spool mode uses a private 0600 file and `io.MultiWriter(file, sha256.New())`,
then sync/close. Publication first creates a private spool and current-schema
DB/session, then times `os.ReadFile` plus actual `captureStreamForRun`/INSERT.
Get measures actual whole-row retrieval plus hash verification. Replay calls
actual `runBenchReplayImpl` with discarded output. Metrics calls actual
`extractStreamMetrics`, `extractAgentOutput`, and `scoreExactMatch`.

**Peak process RSS (KiB, Linux getrusage Maxrss; one sample per cell):**

| Bytes | Buffer capture | Spool + hash | ReadFile + publication | Get + hash | Full replay | Metrics + scoring |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,048,576 | 15,392 | 15,392 | 23,128 | 17,512 | 22,636 | 18,584 |
| 8,388,608 | 30,116 | 15,392 | 61,008 | 32,124 | 60,268 | 31,936 |
| 16,777,216 | 46,416 | 15,392 | 102,300 | 48,704 | 102,484 | 47,872 |

At 16 MiB, Go TotalAlloc deltas were respectively 33,489,248; 1,360;
50,347,760; 33,556,728; 161,619,128; and 118,673,848 bytes. TotalAlloc is cumulative
allocation during the phase, **not peak live memory**. RSS includes process
startup and preceding setup in that process; unlike allocation/timing counters,
its high-water mark cannot be reset. The 64 KiB input chunk and runtime baseline
precede the allocation counter. Publication/get/replay/metrics elapsed times were
28/16/157/149 ms; tmpfs, single samples, no latency promise.

An additional **8,388,608-byte / 32,768-event step_finish** fixture supplied
input/output 1, reasoning/cache 0, reported cost 0, and offline model/provider
strings. Publication/get/replay/metrics RSS was **60,996 / 32,324 / 70,136 /
58,012 KiB**; TotalAlloc deltas **25,182,048 / 16,784,960 / 176,082,464 /
139,166,048 bytes**. All four phases passed; this sample exercises retained
model steps but not tool-call compression expansion or all JSON shapes.

The initial harness attempt failed before measurement because the generated line
was 257 rather than 256 bytes. Correcting one padding byte yielded **18/18**
representative phase passes, the separate boundary pass, and **4/4** dense-step
passes. No failed attempt is claimed as measurement evidence.

The reserved-connection limit probe returned **L = 1,000,000,000**. On that same
connection, a **test-only** lowered L of 4,096 accepted 3,800 stream bytes but
rejected 4,096 and 4,097 bytes with SQLite code 18. This checks the row-overhead
distinction cheaply; it neither lowers any product limit nor tests a gigabyte
fixture. `go test ./internal/db -run
'TestInsertRecordedStream|TestGetRecordedStream|TestRecordedStream' -count=1`
also passed under fresh isolated roots. No full suite or local CLI end-to-end
run is claimed; the measurement executable is the locally compiled test binary.

## Selected lifecycle and testable budgets

The complete choice is **firm under standing authority**, with implementation
qualification still pending. Use **one private stdout spool**, also the recording
spool, plus incremental hash. After the existing private completion receipt and
intact spool completion, run sequential aggregate-only usage and answer reader
passes. No `ModelSteps` retention in normal bench; preserve rich usage behavior
for experiment consumers. Retain whole-value publication/replay and TEXT APIs.
These are qualification thresholds, not measured worst-case guarantees or input
admission limits. A miss blocks qualification, not accepted input.

Let N be captured stdout bytes, E captured stderr bytes, and R the largest retained
record in the interpretation pass (at most 64 MiB; answer Scanner remains 4 MiB).
L limits persistability, **not benchmark capture or interpretation**. A bench
spool larger than L remains complete and scoreable; only DB recording is unavailable.

| Resource | Selected budget and verification |
| --- | --- |
| Capture and diagnostic forwarding | Reusable 64 KiB transfer slabs per active stream, plus hash state, no whole-stream Go buffer. Preserve the <= 8 MiB recording-only heap overhead target; combined stdout/stderr capture overhead also <= 8 MiB over matched control at 1/8/16 MiB. Existing wrapper parser/tool-event work is measured separately. Reader forwarding must not materialize stderr. |
| Normal benchmark interpretation | Sequential passes with constant-count aggregates/metadata/answer candidates, no per-step history. Peak RSS target <= 128 MiB + 16R; at R=64 MiB this is 1,152 MiB, a conservative unqualified threshold, not a low-RAM promise. Retained-heap growth <= 8 MiB across fixed-record 1/8/16 MiB dense streams; verify absence of event-count retention structurally as well as measuring. Keep exact sums and numeric acceptance semantics. |
| Active capture storage | Bench retains N+E payload bytes plus fixed private metadata and filesystem overhead; N is not capped by L. One stdout copy, not a recording spool plus a second scoring spool. Successful unused stderr scratch may be removed. Standalone optional recording may stop at L (compile ceiling before reservation), retaining its partial copy and continuing forwarding. Temporary disk scales with input; no new hard cap. |
| Publication, including verification | Peak RSS target <= 128 MiB + 16N for N persisted stream bytes, counting spool materialization, byte/string copies, SQLite and post-commit read-back. Run sequentially, do not keep extra whole-buffer snapshots. At the compile ceiling this target is 16,134,217,728 bytes (~15.03 GiB), not low RSS. |
| Whole-value replay | Peak RSS target <= 256 MiB + 64N, including metrics/model steps, parsed tool calls and scoring/compression work, for persistable N<=L. At the compile ceiling: 64,268,435,456 bytes (~59.85 GiB). Current small samples fit; adversarial shapes and full composition remain unverified. This is no longer the normal benchmark interpretation budget. |
| Isolated publication storage | Working allowance <= 64 MiB + 4L additional bytes for one recording's spool, SQLite row/index/page overhead and journal/WAL, measured against a quiescent matched empty fixture. Not a quota on accepted sizes, preexisting DBs, unrelated tool rows, pinned WAL, or retained failures. |

Measure capture storage and publication work separately; do not count the shared
stdout spool twice when reporting their combined lifetime. For N>L there is no
whole-value publication allocation; the completed private spool remains retained.
These large proportional thresholds deliberately preserve compatibility and
leave room beyond the representative peaks. Their coefficients are conservative
engineering choices, **not a proof that all JSON/SQLite/runtime cases fit**.
Future verification must sample text, dense model steps, tool events, large
records, missing/zero/malformed usage, complete nonzero exit, and the combined
publication/read-back lifetime. Test near-boundary logic using a lowered limit
only in test-owned connections; do not stress this host near the compile ceiling.
If budgets fail, correct contract-preserving work first. Only an actual necessary
contract change needs new approval; an implementation choice is not a new approval
gate. Do not silently introduce an input cap or schema migration.

### Ownership and interruption

| State or boundary | Owner and required outcome |
| --- | --- |
| Create and collect | The actual run/attempt owner creates private storage (0700 owner directory, 0600 exclusive files). Standalone material lives under the owned application data area, not generic `/tmp`; benchmark material stays inside the already-confined owner/archive boundary. Descriptor/root-relative operations retain existing symlink/path protections. No external provenance-path escape. |
| Producer completed | Existing collection/settlement and exact session-completion receipt decide producer completion, including ordinary nonzero child exit. The spool owner syncs/closes bytes and records size/hash/completion provenance privately. A spool or hash cannot substitute for the producer receipt. |
| Interpret and publish | Bench first requires intact captured stdout and the existing completed producer receipt. Aggregate-only and answer passes preserve field completeness, tool-row/stream precedence, first model/provider, and exact score. That owner attempts normal recording INSERT for completed N<=L; the DB transaction owns atomic publication. Optional recording failure remains a warning in both run and otherwise valid bench. Required bench stdout capture/read failure is different: return an explicit failure before a headline/bench row, never score partial data or attach to historical sessions. |
| Commit verified | Verify the exact inserted row ID, owner/linkage, format, byte count and recomputed hash against the spool, not merely existence or a copied hash. Only then may the same owner unlink its redundant spool/metadata. This verification's whole-value memory belongs in the publication budget. |
| Publication fails or commit outcome uncertain | Keep the completed private spool and its provenance. Report completed bytes retained but database replay unavailable/unverified. No unconditional deferred deletion. Reconcile an uncertain commit by exact identity before retry or cleanup; never overwrite another row or promote ambiguity. |
| Completed bench N>L | Continue capture, metrics and scoring from the single intact spool. Recording is unavailable, not the benchmark. Retain the completed owner-private stdout and completion metadata; never delete merely because SQLite cannot publish it. |
| Interrupted, optional-recording size crossing, spool write/sync/quota failure | Never publish partial bytes as complete. Preserve surviving private bytes and prior committed evidence, and report incomplete/unavailable truthfully. Actual interruption keeps existing process/session settlement authority. Optional run recording failure does not stop normal forwarding; required bench stdout capture failure cannot be disguised as a mere replay warning. Bench crossing L is not capture failure. |
| Stderr scratch | Preserve the existing failed-wrapper warning/delimiters and raw byte order by bounded reader, including complete ordinary nonzero exit. No whitespace trimming, added newline, success forwarding or empty-stderr delimiters. Successful unused stderr scratch may be deleted because it is not recording evidence. Failed diagnostic scratch may be removed only after complete verified forwarding; a failed capture/read/write reports diagnostics incomplete and retains surviving scratch. It does not invalidate independently intact stdout metrics; completion receipt and required stdout checks still govern benchmark results. |
| Recovery | The original private owner namespace retains recoverable files. A subsequent explicit owner-controlled recovery inspection may verify identity, receipt, bytes/hash and DB outcome; a completed spool is not automatically replay-visible, and an interrupted spool cannot be promoted. No new recovery command or auto-resume mechanism is selected here. |
| Retention and cleanup | The owner may delete only verified redundant temporary copies. Unknown/orphaned/partial/failed-publication last copies are never age-pruned or automatically deleted. Manual durable-evidence removal remains user-owned. A generic temp-directory sweeper or archive cleanup must not erase the last copy. |

A crash before the completion receipt is durable can leave a complete-looking
file that must remain **unknown/incomplete**, not a completed replay. Failure to
create storage leaves recording unavailable, not an empty successful recording.
Sync is a durability operation, not a guarantee against storage hardware failure.
No arbitrary capture duration, retry extension or new cancellation timeout is
introduced; existing database and process lifecycle deadlines remain authoritative.

## Resolved coverage, pending implementation qualification

The earlier provisional gate conflated input-sized temporary disk with input-sized
working memory and assumed normal bench needed retained rich model-step evidence.
The supplied verified analysis and source review remove those assumptions. One
shared spool plus aggregate-only/answer reader passes is sufficient for the selected
capture/interpretation scope. No new public contract question remains here. The
[discussion anchor](../../TODO.md#discuss-benchmark-recording-resource-coverage)
now links the completed structured choice rather than an unresolved architecture
question. Other unrelated architecture questions and their gates remain untouched.

This is **not a full PERF1 fix**: no runtime implementation has occurred. Rich
experiment interpretation and whole-value replay are not claimed constant-memory.
Neither are SQLite publication, total host RAM on tmpfs, temporary disk, all wrapper
tool-event processing, or retained history across arbitrarily many operations.

## Implementation qualification cases

Owner `mljcarlewi` must qualify the following with offline fixtures and the locally
built binary where lifecycle behavior is involved. These are required future checks,
not new passing evidence. Preserve existing owner-receipt/process-settlement tests.

| Case | Required result |
| --- | --- |
| Lowered temporary SQLite L | Use the actual test-owned publication connection with L=4,096; retain the prior 3,800/4,096/4,097-byte row-overhead evidence. Run a large accepted multi-record bench well beyond L (e.g. 8 MiB), with final answer and usage after the crossing. Score, cost, tokens/cache/reasoning, model/provider and headline equal the unchanged helper/control results. No recording row; recording-unavailable warning only; completed stdout spool remains private, exact and owner-linked. The bench still succeeds. Never lower product limits. |
| Ordinary publication/replay | Empty stream, small stream, normal nonzero exit, and completed N<=L all attempt ordinary INSERT, including row-overhead rejection near L. Verify persisted bytes/hash/linkage and existing replay behavior. Re-query L on replacement connection; preserve retry/schema/admission boundaries. Do not allocate publication data for N>L. |
| Dense step capture/interpretation | Fixed-size step_finish records at 1/8/16 MiB, with stdout and stderr measured separately and together. Assert no growing ModelSteps collection or stdout/stderr buffer in the normal path, constant-count aggregate state, and the capture/interpretation thresholds above. Compare all used fields against current rich helpers, retaining rich experiment tests. Cumulative TotalAlloc may scale with work; it is not retained heap. |
| Usage equivalence | Missing/null/zero/malformed/fractional/precision-boundary values, exact positive/negative cancellation, incomplete fields, malformed containers, first model/provider including whitespace, reported zero cost and tool-row fallbacks. Preserve float addition order and independent first-model/provider behavior; do not replace it with trimmed rich-step metadata. Include CRLF, no-final-newline, and exact 64 MiB versus oversized records followed by valid usage; oversized usage records skip and later usage counts. Bound memory/capacity before large-record fixtures. |
| Scoring equivalence | Current helper is the oracle: plain text, blank/whitespace lines, CRLF, EOF without newline, flat content and nested part.text/part.content precedence, malformed JSON and empty candidates. Test immediately below/at/above the actual 4 MiB Scanner boundary, with a prior answer and a later valid answer. An oversized line stops scoring and keeps the prior candidate, even though usage may continue. Keep exact post-TrimSpace score; do not infer a new exact-size acceptance rule from the scanner buffer value. |
| Large stderr | Success, ordinary completed nonzero exit, startup/no-receipt failure: 1/8/16 MiB stderr, empty data, leading/trailing whitespace, CRLF, NUL, and final newline present/absent. On failure compare warning/delimiter order and stderr bytes against existing formatting; absent final newline directly precedes closing delimiter. On success no forwarded stderr. Measure bounded reader/capture heap; successful unused scratch is removable, not recording evidence. |
| Capture/read failure | Inject create, short write, ENOSPC/quota, sync, close, reopen and actual I/O read failures. Optional run recording warns and continues normal forwarding/settlement. Missing required bench stdout produces an explicit error, no misleading headline/bench row, and no complete recording. A bounded-parser stop/skip is not an I/O failure: preserve scoring's oversized-line outcome, but detect spool read errors rather than silently treating them as EOF. A diagnostic failure is labeled incomplete with surviving scratch retained, not presented as exact forwarding. |
| Interruption and receipt | Cancellation and crash before/after spool close, producer receipt, and private completion metadata durability. Missing/uncertain completion remains unknown/incomplete even if the file looks complete; never promote partial bytes to replay. Keep completed ordinary nonzero exit scoreable and prior committed evidence untouched. |
| Publication, recovery and cleanup | Inject INSERT/commit/read-back failure, uncertain commit, mismatched row ID/linkage/format/size/hash, and cleanup failure. Retain stdout until exact committed-copy verification; reconcile uncertain identity before retry. Over-limit and failed-publication completed spools remain recoverable only by explicit owner-controlled inspection, not automatic replay. Protect partial/orphaned last copies, directory/file modes and path confinement; successful stderr-scratch cleanup is the explicit non-evidence exception. |

Use fresh HOME/XDG, unset KTX_DB, and explicitly seeded owned DBs per verification;
no provider/daemon or ambient DB. Keep the existing RAM/tmp-space safety checks,
run large-record cases sequentially only where safe, and do not run a gigabyte
fixture on this host. Qualification must measure composition as well as isolated
phases; full tests broaden only after targeted checks. A schema-release gate is
not selected because the schema is unchanged.

Also distinguish bounded active workspace from accumulated durable history:
preserving every failed/partial last copy across arbitrarily many runs has no
finite aggregate storage bound under current retention authority. The selected
budgets are per recording/operation, not a global retention quota. A requirement
for global bounded history would require separate authority, not automatic prune.

No product implementation, new public flags, commands, framework, schema, input
caps, task completion, evaluation, audit or satisfaction update occurred. Profile
grounding is absent; degraded list projections are not authority over exact gets.
The coordinator retains lifecycle completion and final commit ownership.

## Implemented capture and qualification — 2026-09-07

Task `mljcarlewi`, plan `hljjrlykxb`; implements `zyeasphydk`. The preceding
2026-09-06 sections remain historical decision evidence, not measurements of this
implementation. Task status stays **in_progress** for its coordinator. No task
completion, evaluation, audit, satisfaction, staging or commit is asserted here.
CLI-published implementation progress: `absisndpqx`.

### Runtime change and ownership

- `internal/cli/recording_spool.go`, `run.go` and `bench_run.go` use one private
  stdout file for capture, interpretation and recording, with incremental SHA-256
  and byte counting. Capture/diagnostic copies use reusable 64 KiB slabs. Files
  are exclusive 0600 creations inside fresh 0700 application-data owner roots;
  subsequent file operations use `os.Root`. Normal bench has no experiment
  archive; experiment archive confinement and its producer remain unchanged.
- Normal benchmark interpretation makes separate aggregate-only usage and answer
  passes. `usage.NewAggregateOnly` changes history retention only: the same
  `parseModelStep`, exact integer sums, floating addition order and completeness
  rules remain. Rich zero-value `Aggregator` and replay APIs retain ModelSteps.
  First model/provider and tool-row fallbacks are unchanged. Usage still skips
  oversized 64 MiB records; scoring still stops at its 4 MiB Scanner boundary.
  Draining after a scoring stop checks I/O/hash only, never changes its answer.
- Stderr is private scratch. Existing failed-wrapper warning, delimiters and raw
  byte order remain, including completed nonzero exits. Unused successful scratch
  is removable; failed delivery/capture preserves surviving scratch and warns.
- Optional run capture failures latch without failing stdout delivery or the
  producer's normal exit. Required bench capture/read failures return before any
  headline or benchmark row. Existing private completion receipts and interruption
  settlement remain the producer authority; file close is not that authority.
- `DB.PublishRecordedStream` reserves its connection only during publication,
  inside existing writer admission and deadlines, rechecks managed schema and
  queries `sqlite.Limit(conn, SQLITE_LIMIT_LENGTH, -1)`. Every N<=L attempts the
  ordinary seven-column INSERT, with no guessed overhead allowance. N>L performs
  no whole-value load and affects recording only. Connection replacement gets its
  own effective limit; existing transaction retry rules remain.
- Completion metadata is synced with size/hash and the actual supplied session
  receipt. The completed private copy remains until exact committed row ID,
  linkage, format, timestamp, size and recomputed hash match. Read-back also
  reconciles an uncertain publication identity. Cleanup failure is distinguished
  from unavailable recording. No automatic promotion, orphan pruning, new CLI,
  schema, cap, daemon, unsafe SQLite handle or global history quota was added.

### Fresh functional verification

All product builds and tests used fresh disposable HOME, data/config/cache/state
XDG roots and TMPDIR, `KTX_DB` unset, explicit test-owned databases, external Go
caches, `GOPROXY=off`, and `GOTOOLCHAIN=local`. No provider, paid call, daemon,
ambient live database or live archive was used. Build scratch stayed under
`/tmp/opencode`; the preexisting nested `build/tmp/report-check.m94NRl` module
was preserved.

- Regression-first `TestAggregateOnlyPreservesEvidenceWithoutHistory` failed
  before implementation (missing constructor), then passed with exact evidence
  equivalence after each input and no retained ModelSteps.
- `TestPublishRecordedStreamActualConnectionLimit`: L=4,096 on the test-owned
  publication connection accepts empty/3,800 bytes, attempts and rejects 4,096
  through normal SQLite row overhead, and never loads 4,097 or 8 MiB. Replacement
  connection and newer-schema tests pass without changing production limits.
- `mage build` preceded local CLI tests. Six normal bench cases (1/8/16 MiB,
  exits 0/23) and the same six cases in a temporary lowered-L local build passed.
  Final usage/answer occur beyond L; exact expected headline, score, cost, tokens,
  cache, reasoning and model/provider agree. Lowered-L cases have no recording
  row, one exact private stdout copy, and correct file/directory modes.
- The temporary build changed only the reserved publication connection's limit
  to 4,096 before the existing query. Separate temporary builds closed the capture
  descriptor or returned a closed read descriptor. Both required-failure CLI
  cases failed explicitly with no headline, benchmark row or recording row.
  All temporary instrumentation was removed and the normal CLI rebuilt before
  final checks. Test-only switches are confined to `_test.go` files.
- Standalone locally built CLI cases passed for empty output, normalized stdout
  with exit 23, and INSERT rejection retaining the completed private copy.
  Actual `executeRun` optional-create failure preserves stdout, exit 23 and its
  16-byte private receipt. Short-write, ENOSPC, sync, close, reopen and actual
  read errors are covered without filling a filesystem.
- Ownership tests cover actual deferred-FK COMMIT failure, duplicate exact
  committed identity reconciliation, failed read-back, permission-denied cleanup,
  changed row ID/format/hash and same-length changed bytes with an unchanged
  stored hash. Root-confined reopen rejects an outside symlink. Diagnostic tests
  include whitespace, CRLF, NUL, missing final newline and delivery failure.
- The existing five local CLI owner cases pass, now also checking that missing
  receipts leave private bytes without completion metadata. Existing local CLI
  signal cases pass. Separate SIGKILL child-process cases preserve bytes during
  capture, after close, and after completion metadata; only the last carries
  completion metadata. These are process-crash tests, not hardware durability
  proofs or a substitute for the producer-receipt tests.
- Focused `go test -race ./internal/usage ./internal/db ./internal/cli -run
  'TestAggregateOnly|TestPublishRecordedStream|TestRecording|TestBenchOwnedSessionLocalCLI|TestRun.*Signal'
  -count=1 -v` passed with local CLI environment selectors. The separate SIGKILL
  race run passed. Opt-in resource/fault cases ran separately, not as silent skips
  counted as passes in these commands.
- Final **`mage check` passed** on the combined source/test tree: Go tests, Python
  evidence tests, vet/lint/build and required adapter gate; zero lint issues and
  verification audit `forbidden=0` (13,915 attempts). An earlier full run reached
  lint and failed one formatting rule; it was corrected before the passing runs.

### Fresh resource samples

`TestRecordingResourcePhase` was compiled with `go test -c` into
`/tmp/opencode/recording-resources-final.test`. Each phase/size ran in a fresh
process and application environment, `GOMAXPROCS=2`. Fixtures repeat a 256-byte
step_finish record; capture exercises two private streams and bounded diagnostic
forwarding. Working capture files hold N+E bytes; there is no second stdout copy.
Publication includes load, INSERT and exact read-back; replay uses the existing
whole-value `runBenchReplayImpl`. **15/15 final phase samples passed.**

Peak process RSS, bytes (single samples; setup high-water cannot be reset):

| Payload bytes per stream | Matched control | Combined capture | Interpretation | Publication + verification | Whole replay |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1,048,576 | 13,692,928 | 14,020,608 | 18,841,600 | 25,800,704 | 27,623,424 |
| 8,388,608 | 13,627,392 | 13,955,072 | 19,120,128 | 63,803,392 | 79,400,960 |
| 16,777,216 | 13,627,392 | 14,413,824 | 19,329,024 | 107,745,280 | 138,395,648 |

Combined capture TotalAlloc is **268,576 bytes at every size**, versus 139,888
for its matched control: 128,688 extra allocated bytes in the entire measured
phase, below the 8 MiB collector-heap allowance even without equating cumulative
allocation to peak live memory. Interpretation retained-heap deltas after GC are
17,320 / 17,448 / 23,520 bytes; growth across 1–16 MiB is **6,200 bytes**, below
8 MiB. Its TotalAlloc grows with work (13,171,024 / 103,371,736 / 206,414,712),
which is not event-history retention. Fixed-record interpretation RSS fits
128 MiB+16R for R=256; publication and replay fit 128 MiB+16N and 256 MiB+64N.

Isolated publication used test-only L=33,554,432. Additional file-length storage
after publication and the 100-microsecond sampled high-water were respectively
1,048,576 / 8,396,800 / 16,793,600 bytes, excluding the stdout spool and measured
against a quiescent seeded database. All are below 64 MiB+4L = 201,326,592 bytes.
This is sampled filesystem evidence, not a worst-case physical-device guarantee.

Four additional large-record cases passed through `go test`: exact 64 MiB and
64 MiB+1, each at EOF and followed by valid usage. Scoring keeps the prior answer,
while usage accepts/skips at its existing boundary. The qualified process began
with RSS high-water 38,449,152 bytes and peaked at **269,619,200 bytes**, below
128 MiB+16R for R=64 MiB. A direct exec-shaped launch failed this resource gate;
diagnosis showed **1,614,270,464 bytes already in its initial high-water before
fixture work**, unchanged at failure. That launch is not passing evidence. The
Go test child launch supplied the clean-baseline qualification above.

Before final bounded samples, available RAM was 7,481,286,656 bytes and temporary
free space 13,241,286,656 bytes; the large-record checks also had over 7 GB available
RAM and 13 GB temporary space. No gigabyte fixture or OOM stress was run. `/tmp`
is tmpfs: spool pages consume host memory outside these process-RSS observations.
No device-latency, total-host-memory, compile-ceiling stress, exhaustive crash
interleaving or constant-RSS publication/replay claim follows. These are the
approved engineering qualification samples, not new runtime rejection limits.
Retained history across arbitrarily many failed operations remains unbounded by
design. Exact entity reads remain authority over degraded list projections;
profile grounding remains absent. Coordinator lifecycle ownership is unchanged.
