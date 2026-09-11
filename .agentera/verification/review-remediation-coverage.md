# Review remediation coverage

Current-status addendum: [2026-09-07 approved cutover and final reconciliation](final-cutover-reconciliation-2026-09-07.md)
rechecks all 48 dispositions, exact owner/progress references and fresh final
gates. The authorized active-matrix cutover removes the prior marker blocker
while preserving historical bytes and hash assertions. Technical verification
passes; coordinator-owned evaluation and lifecycle closure remain separate.
Original baseline and dated owner observations below remain history;
pending-owner wording there is not current lifecycle authority.

Baseline: 2026-09-05. Plan: `hljjrlykxb`, **Review remediation — trustworthy
evidence and bounded execution**. Ledger owner: `chdzwfbxaz`, **Establish finding
coverage**. Scope: docs-only inventory, not remediation or task evaluation.

## Authority and evidence boundaries

The source inventory is the coordinator-supplied final review inventory dated
2026-09-05, transcribed below with repository-relative references. Historical
`REVIEW.md` (2026-08-23) is unchanged and is not current authority. At baseline all **48
inventory entries were unremediated**: 16 numbered findings, 8 additional gaps,
4 performance categories, 10 architectural/verification observations, and 10
scoped supplemental faults. Split ownership within a category does not create
another finding.

- **R — reproduced (prior review):** four scenarios, findings 1, 2, 4, 6.
  Observed outputs are preserved below; no reproduction was rerun for this ledger.
- **SC — source-confirmed (prior review):** code-path evidence, not execution
  proof. Applies to the other 12 numbered findings, all 8 additional gaps and
  all 10 supplemental faults (30 entries).
- **SD — source-derived cost:** four performance categories; estimates are not
  runtime measurements.
- **AR — architectural/verification review:** ten observations; source analysis
  or historical metrics, not newly measured coverage or native execution proof.

References identify review locations, not proof of repair. This task resolves
paths and checks line bounds, exact owner identities and coverage; it does not
reaudit every source claim. The supplied shorthand screen.go line 116 resolves
to `internal/cli/bench_screen.go:116` (read-before-size-check). Other abbreviated
paths below are expanded to their actual repository locations.

Prior reproduction sources were `/tmp/opencode/ktx-review-validation-overlay_test.go`
and `/tmp/opencode/ktx-storage-review.jo3bzm/storage_review_test.go`, with overlay
JSON siblings. These are ephemeral pointers, not durable runtime proof or a
dependency of this ledger. No database, provider, Docker cleanup, or product
test invocation is required to consume it.

## Task ordinal translation and ownership

Exact retrieval authority: `npx -y agentera@next state plan get --id hljjrlykxb`.
The writer removed task ordinals while leaving Task-number references in scope
and unknowns. The ordinal reconstruction below uses the ordered prerequisite
list of the final synchronization task, followed by that task itself; its gate
names agree with the retained unknowns. Do not use alphabetically ordered or
degraded list summaries as ordinal order. Each owner in the inventory is a bare
task ID in this table; ownership means responsibility for future disposition,
not approval to execute it now.

| Ordinal | Bare ID | Exact task name |
| --- | --- | --- |
| 1 | chdzwfbxaz | Establish finding coverage |
| 2 | tcrffftvxl | Align safe verification entrypoints |
| 3 | htbcyjluxa | Correct database target lifecycle |
| 4 | xyhknqphfs | Recheck managed schema leases |
| 5 | sgqaoojspr | Validate experiment entry contracts |
| 6 | bqpenkdmho | Confine archive access |
| 7 | tqmgzzesya | Bind attested execution inputs |
| 8 | pzztseunxc | Own benchmark process lifecycle |
| 9 | qlhcdoeyhr | Forward oversized wrapper output |
| 10 | dwndsjjdkx | Bound wrapper cancellation |
| 11 | yawngvokny | Preserve ingress output and identity |
| 12 | luwgmsjiin | Validate adapter response envelopes |
| 13 | ogjqsbptji | Acknowledge durable telemetry |
| 14 | atbnjovpeu | Reject unknown configuration keys |
| 15 | btxcvatnmt | Preserve stage evidence validity |
| 16 | lscowhzqtf | Preserve reporting authority |
| 17 | itzxdynanp | Order continuity chronologically |
| 18 | linrevynph | Preserve session cache ownership |
| 19 | mebgweiffm | Preserve Codex shared-hook ownership |
| 20 | hlzmkuhsht | Bypass complete retry stages |
| 21 | kxklkicklv | Consolidate wrapper argument ownership |
| 22 | momdpkraiq | Align remaining command arguments |
| 23 | jgvodxuxul | Bind benchmark session ownership |
| 24 | hatnrvkhpu | Persist cold pricing discovery |
| 25 | jfgymxttel | Decide recording resource lifecycle |
| 26 | mljcarlewi | Bound approved recording behavior |
| 27 | itocjemrfw | Reduce pipeline allocation work |
| 28 | otiqrxtwkw | Bound preflight and screening resources |
| 29 | czzdcnqozn | Bound archive materialization work |
| 30 | idtauctgpl | Reduce cache-sequence repeated work |
| 31 | ubegqqsptl | Retire superseded compatibility |
| 32 | iefdatbpxu | Decide shared-boundary debt disposition |
| 33 | wxwwonhjgy | Apply selected boundary cleanup |
| 34 | zcvdpwllcs | Verify promised native platforms |
| 35 | maeswzqwit | Audit coverage and boundary tests |
| 36 | bqnxtbzjou | Verify completion and synchronize state |

Retained unknowns remain gates, not resolved design:

- Task 5 → Task 31: inventory current producers/consumers and their supported
  regeneration matrix before converting producers and removing retired readers.
- Task 25 → Task 26: record the recording-budget, interruption, publication,
  recovery, retention and cleanup choice before implementation.
- Tasks 28–29: preserve accepted sizes or obtain approval before restricting them.
- Task 32 → Task 33: select bounded cleanup or evidence-backed no-change;
  extraction is not predetermined.
- Task 34: inventory promises and runners; absent required native evidence blocks
  completion. Compilation does not substitute for native locking/lifecycle proof.

## Numbered final findings

| Finding | Priority / evidence | Fault and source references | Owning task IDs |
| --- | --- | --- | --- |
| 1 | P1 / R | Database path URI mismatch: `internal/db/database.go:204`, `internal/db/writer_lock.go:123`. Requested `uri?tag.db` remained 0 bytes; SQLite sibling `uri` was 180224 bytes, schema 16, foreign_keys 0. Coordination protected a different file. Repair verified; see scoped database lifecycle evidence below. | htbcyjluxa |
| 2 | P1 / R | Empty experiment accepted: `internal/experiment/validate.go:11`, `internal/experiment/validate.go:107`, `internal/experiment/summary.go:327`. Manifest only `{experiment_id:empty}`; WriteSummary/ValidateExperiment yielded `valid=true`, `target_met=true`, counts 0, `attempts=[]`. Current disposition: entry-contract repair verified, evaluation pending. Shared summarize/validate loading now rejects missing/unsupported versions, nonpositive targets, missing required inputs and empty/malformed ledgers; existing artifact/hash checks exclude damaged attempts before aggregation. Permissive list/show and active legacy representations remain. Public local-CLI regressions, focused race and full mage check pass. See [producer/consumer and regeneration matrix](experiment-entry-contract-matrix.md), the gate for later retirement `ubegqqsptl`; original reproduction history is preserved. | sgqaoojspr |
| 3 | P1 / SC | Paid interrupt cleanup: `internal/cli/bench_experiment.go:329`, `internal/cli/bench_experiment.go:497`, `internal/experiment/executor.go:47`, `internal/experiment/runner.go:851`, `internal/experiment/runner.go:855`. Parent background context leaves Docker cleanup unreachable; cleanup errors ignored. | pzztseunxc |
| 4 | P1 / R | Long-ID collision: `internal/experiment/id.go:8`, `internal/experiment/runner.go:821`, `internal/experiment/runner.go:1051`. Accepted strings `a*127+b` and `a*127+c` produce the same 120-character name (`ktx-` plus 116 a's). Destructive Docker cleanup consequence is source-traced only, never executed. | pzztseunxc |
| 5 | P1 / SC | Wrapper deadlock: `internal/cli/run.go:267`, `internal/cli/run.go:321`, `internal/cli/run.go:511`, `internal/cli/run.go:608`. Above 64 MiB scanner stops draining then Wait; descendants can retain stdout after direct-child kill. Output and cancellation repair evidence remains below. The continuation audit passed lifecycle criteria but rejected the then-unapproved terminal capability floor. User approval llshvynohy now supplies that support authority; rmbdxwfguf documents and verifies the existing behavior only. dwndsjjdkx remains blocked with both failed evaluations intact; replacement evaluation/completion and any later supersession remain coordinator-owned. | qlhcdoeyhr (output); dwndsjjdkx (blocked history); rmbdxwfguf (approved documentation/verification replacement) |
| 6 | P1 / R | Stale writer: `internal/db/database.go:639`, `internal/db/database.go:662`. Open handle at schema 16, explicit raw test-only bump to 17; InsertSession returned nil and persisted 1 row. Additive version simulation, not full concurrent migration. Historical proof retained; see [scoped lease recheck evidence](#findings-6-and-a1--managed-schema-lease-rechecks). | xyhknqphfs |
| 7 | P1 / SC | Execution/validation configuration mismatch: `internal/experiment/runner.go:562`, `internal/experiment/runner.go:607`, `internal/experiment/runner.go:843`, `internal/experiment/validate.go:149`; separate configuration and input copies. Scoped implementation and offline regression proof: [execution input binding](#findings-7-and-14--execution-input-binding). Evaluation/completion remain owner-assigned. | tqmgzzesya |
| 8 | P2 / SC | Archive escape through parent paths or symlinks before confinement/database access: `internal/experiment/regeneration.go:60`, `internal/experiment/validate.go:63`, `internal/experiment/pricing.go:91`. Current scoped repair and runtime proof: [archive confinement](#finding-8--archive-confinement); baseline SC classification retained. | bqpenkdmho |
| 9 | P2 / SC | Empty output interpreted as missing, replaced by callback JSON: `internal/cli/hook.go:139`, `internal/cli/hook.go:280`, `internal/cli/init.go:615` (baseline references). Scoped extraction repair verified: present string fields, including empty, retain priority; absent/null and nonstring schema fallbacks remain unchanged. `internal/cli/ingress_fields_test.go` exercises 25 OpenCode/Codex cases through actual hook persistence and the locally rebuilt CLI. Before repair, OpenCode returned callback JSON and Codex persisted lower-priority text or serialized JSON; afterward visible responses and persisted originals agree with the extraction contract. Empty complete tuples converge across hook/wrapper in both orders, retaining the exact first source row, producer, session ownership and one rollup. Focused race checks and full isolated `mage check` pass (audit `84471a039f7ceac7`, 17597 attempts, forbidden=0). Fresh HOME/XDG/TMP roots, KTX_DB unset, disposable offline databases; no provider or daemon calls. Adapter envelope validation and completed-artifact retry bypass remain excluded; evaluation/completion remain owner-assigned. | yawngvokny |
| 10 | P2 / SC | Telemetry marked seen before durable acceptance; optional silent success: `internal/cli/init.go:532`, `internal/telemetry/opencode/contract.go:97`, `internal/cli/run.go:529`, `internal/cli/telemetry.go:364`. | ogjqsbptji |
| 11 | P2 / SC | Adapter assertion is not runtime envelope validation: zero-exit `{}`, `[]`, `null` can corrupt delivery or silently bypass diagnostics; `internal/cli/init.go:602` (baseline reference). Scoped repair verified; task remains in progress and lifecycle disposition stays owner-assigned. Shared runtime validation checks current required fields, canonical persisted IDs, nullability and cue artifact presence before mutation/acknowledgement. Ordinary failures preserve original output/title/metadata and diagnose on stderr; required collection retains fail-closed identity bindings and canonical executable-path pinning. Explicit empty strings and unknown future fields remain accepted. Generated Bun regressions failed before repair and pass afterward for malformed JSON, null/array/object/scalar envelopes, missing/wrong-type fields, malformed canonical IDs, cue-without-artifact and valid empty/nonempty/null-artifact controls. Existing required acknowledgement and pinned-path negative controls pass. After mage build, the installed generated adapter called build/ktx for real empty, nonempty passthrough and transformed-JSON delivery. Focused race and full isolated mage check passed, including required Bun overlap gate, 14 Python checks, vet/lint/build and zero lint issues; verification boundary 7b4b677b3482a14f reported forbidden=0. Initial 120-second shell timeout is not passing evidence; fresh longer rerun passed. Fresh HOME/XDG/TMP and KTX_DB unset at verification invocation boundaries; disposable offline databases, no provider/daemon calls. Prior dirty init/CLI flags preserved; no telemetry dedup, Codex installation, hash/protocol authority, schema, caps or framework changes. Standing decisions yohiahsqjv and ygcysqxcfr exact gets retain bounded-repair and Linux-only scope; profile grounding remains absent without refresh. Progress jfhbcjldhj records substantive verification and remaining startup caveats. | luwgmsjiin |
| 12 | P2 / SC | Undecoded config typo keys broaden selectors: `internal/config/config.go:373`, `internal/router/router.go:74`. Current disposition: repaired, coordinator audit pending. Shared parser rejects undecoded keys before constructing profiles/routes; independent fail-soft logs validation and stage-name compatibility remain unchanged. Loader regressions failed before repair for seven typo/unknown-table cases through both Load and LoadFile; three hook regressions also failed before repair. All pass after repair. Local rebuilt CLI rejects selector/footer typos with exit 1 and no replacement output; valid control still transforms. Twelve shipped example/pipeline/screen configs, missing config, unknown stage and custom plugin smoke checks pass. Fresh isolated mage check passed (7549 audited attempts, forbidden=0). Frozen characterization source references remain historical; no routing or schema semantics changed. | atbnjovpeu |
| 13 | P2 / SC | Historically unknown numeric latency counted measured: `internal/dbreport/report.go:188`, `internal/dbreport/report.go:205`, `internal/dbreport/report.go:257`. | lscowhzqtf |
| 14 | P2 / SC | Mutable image tag used rather than retained preflight identity: `internal/experiment/runner.go:648`, `internal/experiment/runner.go:710`, `internal/experiment/runner.go:845`. Scoped implementation and offline regression proof: [execution input binding](#findings-7-and-14--execution-input-binding). Evaluation/completion remain owner-assigned. | tqmgzzesya |
| 15 | P2 / SC | Bench-run concurrent/historical latest-session fallback: `internal/cli/bench_run.go:192`, `internal/cli/bench_run.go:437`. | jgvodxuxul |
| 16 | P2 / SC | Hook retry executes stages after authoritative preflight: `internal/cli/hook.go:538`, `internal/cli/hook.go:550`, `internal/cli/hook.go:562` (baseline references). Scoped repair verified; lifecycle disposition remains owner-assigned. Retained artifacts now bypass stage construction/execution, resolving only current route/footer policy. Source-only retries can still produce their first artifact; missing dispositions reconcile through the existing writer. `hook_retry_stages_test.go` failed before repair with external marker `xx` instead of `x`; afterward both runtime envelopes pass same/changed/bad-stage, inherited routed footer, retained-output size gate, malformed-config, conflict-before-stage, unchanged metadata, source-only promotion and disposition-repair checks. Complete retries retain zero writer observations; existing first-insertion concurrency, structured/legacy footer and dispatch coordination controls pass under focused race. One obsolete candidate-artifact observation expectation changed in `hook_foreign_lock_test.go`. Local `mage build` plus eight hook CLI invocations proved one external execution per runtime, unchanged envelopes and 1/1/1 evidence. Full isolated `mage check` passed: 14 Python tests, adapter overlap, vet/lint/build, zero lint issues; audit `a2f888a05cd97e13`, 17272 attempts, forbidden=0. Fresh HOME/XDG/TMP roots, KTX_DB unset at invocation boundaries, disposable offline fixtures, no providers/daemons. Initial CLI smoke fixture omitted required Codex native fields; corrected without product changes. Progress `pvnsatvoyz`; task stays in progress. Prior ingress-empty/native-ID work preserved; no schema, flags, framework, evaluation, audit or satisfaction changes. | hlzmkuhsht |

## Additional final gaps

All eight are P2, source-confirmed, not reproduced here.

| Finding | Evidence | Fault and source references | Owning task IDs |
| --- | --- | --- | --- |
| A1 | SC | OpenQuery readiness reopen can retain stale version: `internal/db/database.go:443`. See [scoped lease recheck evidence](#findings-6-and-a1--managed-schema-lease-rechecks); original source-review classification retained. | xyhknqphfs |
| A2 | SC | Session-cache truncation corrupts lifetime ownership: `internal/db/session.go:68`, `internal/db/session.go:90` (historical references). Scoped repair verified; evaluation/completion remain owner-assigned. Session publication now uses the existing durable-file replacement; managed resolvers hold the existing DB lease through read/create/publication, without nested writer acquisition or new lockfiles. Malformed JSON, invalid IDs and incomplete parent metadata remain byte-exact with explicit ownership-verification/recovery guidance; no identity salvage. New Linux regressions failed before repair: injected partial publication truncated the original to 16 bytes, valid malformed shapes silently reassociated, and six same-parent processes accepted six owners. They pass afterward, including source association, hook reuse, private permissions, lifetime/missing-session/database-scope controls and newer-schema rejection. Existing injected rename-failure regression also preserves original bytes. Focused race checks and full mage check passed (audit e86809340c69dd81: 13797 attempts, forbidden=0). Locally rebuilt CLI accepted seven same-parent sources under one owner, retained historical source rows, rejected malformed cache with exit 4 without mutation, accepted explicit verified recovery, and preserved missing-hint exit 2. Fresh HOME/XDG, KTX_DB unset and explicitly disposable offline DBs only; only intentional parent cache/DB targets shared among child processes. No schema or historical evidence changes. | linrevynph |
| A3 | SC | Codex shared-hooks truncation: `internal/cli/init_codex.go:179`, `internal/cli/init_codex.go:244`. Atomic publication alone does not cure concurrent lost updates. Scoped implementation now holds a Linux directory lease across read/merge/checked durable publication or removal; observed external edits abort, malformed documents and unrelated fields remain intact. Original regressions and direct-write/uncoordinated negative controls fail; restored interruption, 24-process mutation, external-edit, race and full `mage check` gates pass (`forbidden=0`). Noncooperating final-window races remain explicitly outside the guarantee. [Evidence and limits](codex-shared-hooks.md); lifecycle disposition remains owner-controlled. | mebgweiffm |
| A4 | SC | Missing native callID replaced with part.id: `internal/parser/parser.go:242`, `internal/correlation/correlation.go:161` (baseline references). Scoped repair retains legacy/display fallback separately and exports only native callID for correlation. Actual wrapper persistence and locally built CLI fixtures failed before repair for absent, empty, null, numeric and object callID values with a repeated part ID (one source instead of two); all pass afterward with two distinct fresh sources and unchanged forwarded bytes. Whitespace-only native ID remains a fresh-fallback control. Existing malformed-schema, generic-flat/caller-authority, complete-tuple, conflict and concurrent-retry suites pass under race; no derivation, alias, schema or historical-row changes. Verification is shared with finding 9; task stays in progress and lifecycle disposition remains owner-assigned. | yawngvokny |
| A5 | SC | Control characters make provenance JSON invalid: `internal/pipeline/pipeline.go:485`. Repair verified; see scoped stage evidence below. | btxcvatnmt |
| A6 | SC | Lexical timestamp ordering fails offsets/fractional precision: `internal/db/continuity.go:216`. Current disposition: repaired; evaluation/completion remain owner-assigned. The original source-review classification is retained. Evaluation now stable-sorts once-parsed instants with existing rowid append order for ties; no schema, backfill, or stored timestamp normalization. Real on-disk EvaluateContinuity regressions reproduced 11 failures before repair and pass afterward: offsets (10:00+02 before 09Z), fractions, equivalent timestamp spellings, append-order ties, genuine gaps/conflicts, and interruption boundaries. Whole-second UTC, missing closure, pre-deployment unknown and malformed timestamp rejection remain covered. Repeated evaluations assert original opened_at and every observed_at string and event order unchanged. Locally built CLI stats live-validation changed the offset fixture from a false missing hour to 3600/3600 covered seconds with no gaps; report preserves seeded DB bytes, while overall exit 3 remains truthful for an unqualified one-hour fixture. Focused race including managed-schema/schema-15-partial controls and full mage check passed; verification audit ee6c3dddd705fca1: 13693 attempts, forbidden=0, KTX_DB unset. Fresh HOME/XDG roots and disposable offline DBs only. | itzxdynanp |
| A7 | SC | Integrity reporting omits rows.Err: `internal/dbreport/report.go:574`. | lscowhzqtf |
| A8 | SC | Independent flag scanners mishandle `--quiet=false`, `--help=false`, literal child arguments and `--`: `internal/cli/run.go:96`, `internal/cli/run.go:150`, `internal/cli/run.go:459`. | kxklkicklv; momdpkraiq (remaining surfaces) |

## Performance categories

Here PERF1–PERF4 correspond to the supplied performance P1–P4 labels, **not
severity levels**. These four entries are SD, not measured runtime claims.

| Finding | Evidence | Cost and source references | Owning task IDs |
| --- | --- | --- | --- |
| PERF1 | SD | Unbounded whole-stream, completion-only recording: `internal/cli/run.go:316`, `internal/cli/run.go:347`, `internal/cli/bench_run.go:187`; memory growth and interrupted loss. | jfgymxttel (decision); mljcarlewi (approved implementation) |
| PERF2 | SD | Historical line-split allocation sites: `internal/pipeline/pipeline.go:291`, `internal/pipeline/json_guard.go:95`, `internal/pipeline/stages/head.go:70`. About 10 MiB newline input implied about 240 MiB slice descriptors on 64-bit systems; derived estimate, not RSS measurement. Implementation follow-up: incremental line processing and empty-runner bypass; measured 1 MiB newline/head 55,594,253 → 5,246,091 B/op, empty runner 30,419,872 → 0 B/op. Allocation gates and byte oracles pass; see [pipeline allocation evidence](pipeline-allocation.md). Disposition and task lifecycle remain owner-controlled. | itocjemrfw |
| PERF3 | SD | Cold discovery refreshes memory but not disk: `internal/db/cost.go:229`, `internal/db/prices.go:534`; repeated processes may repeat the 5-second-timeout fetch path. Not a measured repeated five-second delay. Current repair uses existing `RefreshAndWrite`; controlled-fixture regressions fail before and pass after, including fresh cache/process-singleton reuse and recoverable failures. See scoped evidence below; task remains in progress for its lifecycle owner. | hatnrvkhpu |
| PERF4 | SD | Repeated unchanged parsing `internal/router/router.go:78`; per-pair event sorting `internal/cache/reuse/sequence/sequence.go:177`; multiple stream passes `internal/experiment/regeneration.go:69`. Archive-only follow-up: combined hash/call accounting removes the rewind pass; the fixed 16 MiB stream reads 50,323,320 → 33,549,074 bytes and allocates 35,026,440 → 17,766,656 bytes with identical summary SHA-256. See [archive materialization evidence](archive-materialization.md). Sequence-only follow-up: actual 256-step callers reuse one private ordering; secondary full copies/sorts fall 255 → 1, temporal 256 → 1; median elapsed 68.128 → 0.910 ms and 64.887 → 0.434 ms with unchanged estimate bytes. See [sequence ordering evidence](sequence-ordering.md). Router-only follow-up: private lazy per-Resolve extraction reduces 64 prefix/subcommand rules from 128 JSON interpretations to 1; 1 MiB ignored field workload 135,556,728 → 1,059,106 B/op, 91.547 → 0.730 ms, unchanged routing oracle and zero-allocation irrelevant-tool path. See [pipeline allocation evidence](pipeline-allocation.md). Other owners and global disposition are unchanged. | itocjemrfw (router); idtauctgpl (sequence); czzdcnqozn (archive) |

## Architectural and verification observations

| Finding | Evidence | Observation and source references | Owning task IDs |
| --- | --- | --- | --- |
| D1 | AR | Prior-review non-test physical LOC: cli 13248, experiment 8365, db 7921 = 80.8% of runtime; all tracked Go 43251 non-test / 53794 test, including blanks/comments. Sources: `internal/cli`, `internal/experiment`, `internal/db`, tracked Go corpus. Not recomputed, not executable coverage or a splitting target. | iefdatbpxu (assessment); maeswzqwit (fresh coverage) |
| D2 | AR | DB-owned domain records couple parser/router to concrete schema: `internal/parser/parser.go:10`, `internal/router/router.go:17`. Extraction or no-change is decision-gated. | iefdatbpxu; wxwwonhjgy |
| D3 | AR | Semantic duplication: Go/TS normalization/dedup (`internal/telemetry/opencode/contract.go:97`, `internal/cli/init.go:532`); hook/ordinary schema (`internal/cli/hook.go:538`, `internal/db/database.go:639`); stats/report authority (`internal/dbreport/report.go:188`, `internal/db/records.go:566`); flag scans (`internal/cli/run.go:96`); execution/validation copies (`internal/experiment/runner.go:607`, `internal/experiment/validate.go:149`). Map existing owners, do not invent a framework. | ogjqsbptji; xyhknqphfs; lscowhzqtf; kxklkicklv; momdpkraiq; tqmgzzesya; iefdatbpxu; wxwwonhjgy |
| D4 | AR | Retired compatibility was active: `internal/experiment/summary.go:339`, `internal/experiment/regeneration.go:349`. Scoped implementation verified: active exact-match converted to retained-source attempt 3/summary 8 before removal; all registered modes and inline producer pass. Obsolete entry/schema/collector readers, formatting fallbacks, and the remaining inactive cache-probe executable are removed. The 106-file historical inventory is byte-unchanged. [Current matrix, chronology, and checks](experiment-entry-contract-matrix.md#current-cutover-state--2026-09-07) retain the prior baseline as history. Lifecycle evaluation and satisfaction remain owner-controlled. | sgqaoojspr (matrix); ubegqqsptl (retirement) |
| D5 | AR | Unified-hook backlog overstates missing shared flow: `TODO.md:30`, `TODO.md:105`; actual common processing starts at `internal/cli/hook.go:503`, with source persistence at `internal/cli/hook.go:607`. Do not rebuild shared persistence. | iefdatbpxu; wxwwonhjgy |
| D6 | AR | Raw unisolated tests, no race/mage and parallel fix-lint: `.lefthook.yml:43`; fixture bypass `magefile.go:535`. Current disposition: repaired, coordinator audit pending. Pre-push uses `mage test` without a Go-only filter; parallel checks no longer fix or stage. Telemetry fixture uses `hermeticTarget`. New boundary/wiring regressions failed before repair and passed after; isolated `mage opencodeTelemetryFixture` passed with 61 audited attempts, `forbidden=0`; isolated `mage check` passed with 7540 attempts, `forbidden=0`. | tcrffftvxl |
| D7 | AR | Fourteen Python verifier tests not in routine verification: `tools/test_schema_readiness_evidence.py:15`, `magefile.go:654`. Count is prior-review evidence, not a new run. Current disposition: repaired, coordinator audit pending. `check` now runs `python3 -m unittest -v tools.test_schema_readiness_evidence`; `verify` inherits it. Fresh isolated focused execution and `mage check` each passed all 14 tests. New routine-wiring regression failed before repair and passed after. No production-scale release gate was run. | tcrffftvxl |
| D8 | AR | Bun required by `magefile.go:636`, not provisioned by `.github/workflows/ci.yml:19`. Current disposition: repaired, coordinator audit pending. CI now installs Bun via `oven-sh/setup-bun@v2` and Python 3.12 via `actions/setup-python@v5`; verification comment names the Python tests and Bun gate. New CI prerequisite regression failed before repair and passed after. Local required Bun overlap gate passed inside isolated `mage check`; hosted CI not run. Prerequisites documented in `docs/database-safety.md`. | tcrffftvxl |
| D9 | AR | Linux/BSD/Windows locking differs; Ubuntu-only CI `.github/workflows/ci.yml:19`; schema gate rejects non-Linux `magefile.go:616`. Portable/native scoped proof is needed; compiled does not mean natively tested. | zcvdpwllcs |
| D10 | AR | No tracked fuzzers or routine coverage measurement in prior-review tracked test/verification inventory (`magefile.go:654`). Targeted original-review coverage: pipeline 91.9%, stages 97.7%, router 82%, config 84.5%; not whole-repository coverage. Historical observation retained. Current test/evidence disposition: [2026-09-07 workspace baseline](workspace-coverage-2026-09-07.md) measures 15,932/20,288 weighted Go statements (78.5292%), including Mage/tools across 33 Linux packages. Two pure-boundary native fuzz targets add 16 routine seeds and pass bounded mutation campaigns; generated/oracle tests are inventoried separately. Exact platform/child-process and three coverage-only test exclusions, remaining parser-fuzz gaps, and passing unfiltered mage race/Python/Bun/vet/lint evidence are explicit. Coverage remains an auxiliary measurement, not a new routine gate. Pending wxwwonhjgy cleanup and bqnxtbzjou final source synchronization prevent a final whole-plan claim; lifecycle authority is unchanged. Production schema gate remains separate. | maeswzqwit (fresh audit); tcrffftvxl (routine entrypoints) |

## Scoped supplemental faults

These ten SC entries are already planned; do not widen their scope.

| Finding | Evidence | Fault and source references | Owning task IDs |
| --- | --- | --- | --- |
| S1 | SC | GetToolCall treats invalid `is_error != 0` as failure while stats correctly treats it unknown: `internal/db/records.go:566`, `internal/db/records.go:619`. | lscowhzqtf |
| S2 | SC | Initial migration failure leaks Open/OpenExisting pool: `internal/db/database.go:220`, `internal/db/database.go:276`. Repair verified; see scoped database lifecycle evidence below. | htbcyjluxa |
| S3 | SC | unwrap-result accepts `isError:null` as boolean success: `internal/pipeline/stages/unwrap_result.go:95`. Repair verified; see scoped stage evidence below. | btxcvatnmt |
| S4 | SC | keepends count-sum overflow leads to recovered panic: `internal/pipeline/stages/keep_ends.go:51`, `internal/pipeline/stages/keep_ends.go:84`. Repair verified; see scoped stage evidence below. | btxcvatnmt |
| S5 | SC | Codex ownership tied to current executable path leaves relocation duplicates: `internal/cli/init_codex.go:398`. Scoped implementation recognizes the exact current pin or a narrowly validated unmodified generated inner-hook shape after relocation; no old executable probing, unsupported fields, aliases or broad substring deletion. Initial relocation regression fails before; tests using two actual local-built executable paths plus a symlink pass show/read-only, uninstall-before-reinstall, duplicate collapse, canonical pinning and merged-sibling preservation. [Evidence and limits](codex-shared-hooks.md); lifecycle disposition remains owner-controlled. | mebgweiffm |
| S6 | SC | Discarded output errors hide acknowledgement failure: `internal/cli/run.go:523`, `internal/cli/write.go:203`. Preserve post-commit evidence on delivery failure. | qlhcdoeyhr (wrapper); momdpkraiq (remaining CLI acknowledgement) |
| S7 | SC | Unbounded control output/input: `internal/cli/bench_screen.go:116`, `internal/experiment/runner.go:1081`, `internal/experiment/json.go:72`. Existing limits should reject before allocation; new accepted-size restrictions require approval. Preflight/screen repair verified: regular oversized populations reject before payload allocation; other readers stop at 64 MiB + 1 with the exact 64 MiB boundary preserved. Builds/probes inherit existing timeout/grace; stderr and shared build diagnostics truncate and drain at 64 KiB. User-approved decision `bjqvkvgenm` caps only image-ID and capability JSON responses at 65,536 bytes, retains one sentinel byte, cancels overflow producers, settles owned cleanup, and returns explicit size errors before parsing. Generic output remains uncapped. Regressions failed before and pass after; focused race, eight local CLI response checks, corrected configured-grace signal tests, and isolated `mage check` pass (`forbidden=0`). See scoped evidence below. Archive follow-up now streams duplicate ledger projections without new input caps; [measured bounds and checks](archive-materialization.md) preserve output-sized metadata. S7's global disposition remains owner-controlled. | otiqrxtwkw (preflight/screen); czzdcnqozn (archive materialization) |
| S8 | SC | Executor leader can exit before descendants: `internal/experiment/executor.go:57`. | pzztseunxc |
| S9 | SC | Duplicate adapter subprocess mechanics: `internal/cli/init.go:230`, `internal/cli/init.go:377`. Assess selected boundary, not mandatory abstraction. | iefdatbpxu; wxwwonhjgy |
| S10 | SC | Collector map lifetime: `internal/cli/init.go:504`, `internal/cli/init.go:532`, `internal/telemetry/opencode/contract.go:40`, `internal/telemetry/opencode/contract.go:69`. Require lifecycle evidence; do not invent eviction that breaks late identities. Current boundary assessment closed under decision `rholpeffdq`: only validated optional ACK releases serialized payload; exact evidence, membership, late assistant identities and required pending semantics remain. Actual paired 256-pair control measured 222,098 → 108,506 summed string-field bytes (113,592 released), not heap/RSS. The inherited 244,672-byte fixture was not reproduced; its >122,336 bound remains derived. State remains linear and not restart-durable; this is not a fully bounded fix or lifecycle completion. See [qualification](shared-boundary-disposition.md#implementation-qualification--wxwwonhjgy-2026-09-07). | ogjqsbptji (telemetry lifecycle); iefdatbpxu; wxwwonhjgy (selected boundary) |

## Scoped wrapper output repair evidence

2026-09-05, owner `qlhcdoeyhr`, plan `hljjrlykxb`. Implementation and verified
evidence only; coordinator retains task evaluation/completion. CLI-published
progress: `sxvzdoznys` (publication order 224). Original review references above
remain historical.

- **Finding 5 — partial:** the scanner/output portion is repaired. A real child
  emitting a 64 MiB + 64 KiB line previously stalled until the test watchdog
  killed it, losing the line and following sentinels. Fixed-size 64 KiB reads now
  forward complete output with ScanLines-compatible CRLF/final-CR/final-newline
  normalization. The existing 64 MiB budget bounds event candidates only;
  larger lines produce a skip-parsing diagnostic without stopping forwarding.
  The regression checks a full-stream SHA-256, following sentinels, child exit
  7, and persistence of the next valid control event. No accepted-output cap
  was added. **Still open:** `dwndsjjdkx` owns SIGINT/SIGTERM and descendant-held
  stdout cancellation; this work does not claim grandchild termination.
- **S6 — partial:** the wrapper portion now checks short writes and errors,
  including newline writes and EPIPE on actual fd 1. Go's default SIGPIPE exit
  was separately reproduced; a scoped notification registration lets EPIPE
  reach cleanup instead. On forwarding/collection failure the reader closes,
  the direct child is killed/reaped, continuity is interrupted, the session
  closes, and exit 4 is reported. Failed forwarding publishes no incomplete
  recording; previously committed source evidence remains. **Still open:**
  `momdpkraiq` owns other CLI acknowledgements, including `write`.

Verification used fresh disposable HOME/XDG roots with `KTX_DB` unset per
invocation, explicit temporary test databases, and no provider calls:

- New oversized/short/error subprocess regressions: fail before, pass after.
  Broken-pipe subprocess regression: SIGPIPE exit before, exit 4 with closed
  session after. Normalization, EOF-valued sink errors, read errors, and
  preservation of committed evidence are covered in `run_output_test.go`.
- Focused race: `go test -race ./internal/cli -run 'TestRunOutput|TestExecuteRun|TestWrapper|TestRunRun' -count=1 -timeout=120s` passes (3.382 s), including existing model-step telemetry authority tests.
- Forwarding-only benchmark at 65/130 MiB: 134,223,568 / 134,218,008 B/op
  (one iteration each), demonstrating allocation independent of oversized-line
  length. Candidate capacity is capped at 64 MiB; recording remains separately
  unbounded and is not covered by this measurement or repair.
- `mage build` passes before local `build/ktx` smoke. The CLI forwards
  67,174,455 normalized bytes with SHA-256
  `062d93415c6c7b8fa807666f17a341aacbf724473b52f9837715beaad419682d`, returns
  child exit 7, and persists the following control. `/dev/full` and closed-pipe
  sinks return 4 with a diagnostic, absent direct-child PID, closed session,
  interrupted continuity, and no recorded stream.
- Full `mage check` passes: Go tests, 14 Python verifier tests, generated-adapter
  gate, vet/lint/build, 0 issues; verification boundary `388d14bd2bb0c2fd`,
  7719 database attempts, `forbidden=0`, `ktx_db=unset`.

Only Linux was exercised. No schema, accepted-size restriction, signal-tree
redesign, or recording-memory policy was introduced. Exact task/decision gets
work; bounded summaries are degraded, profile grounding is absent, startup
recipe/changelog caveats remain, and satisfaction is user-owned. No task
lifecycle, audit/evaluation, staging, commit, or release-publication action was taken.

## Scoped database lifecycle repair evidence

2026-09-05, owner `htbcyjluxa`; implementation and verification only, with task
evaluation/completion reserved for the coordinator. Original inventory evidence
and historical schema fixtures remain unchanged.

- **Finding 1:** `Open` now builds an escaped file URL from the coordinator's
  absolute literal path. `TestOpenLiteralDatabaseTarget` covers relative
  creation with plain, `?`, `#`, encoded-looking `%23`, literal `%`, and combined
  query-looking filenames. Before repair, `?` and `#` opened sibling `uri`,
  `%23` opened a decoded sibling, and the combined spelling failed URI parsing.
  After repair all pass: the SQLite-resolved file and requested file have the
  same identity, only target/WAL/SHM names exist, a marker survives existing and
  read-only reopening, foreign keys are enabled and reject an orphan insert,
  and an independent coordinator on SQLite's resolved filename is blocked by
  the opening coordinator's exclusive lease.
- **S2:** `Open` and `OpenExisting` now close an allocated pool when the initial
  readiness/coordination callback returns an error, preserving the original
  error wrapping. `TestInitialReadinessFailureReleasesResources` seeds a
  disposable newer database at the actual supported version plus seven; three
  rejected opens per API retain the exact actual/maximum typed error and permit
  lock reacquisition. Before repair each rejection added an open target
  descriptor. After repair `/proc/self/fd` shows no retained target or sidecar
  descriptors. This direct resource proof is Linux-only.

Every verification invocation unsets `KTX_DB` and assigns fresh disposable
HOME/XDG roots. External Go caches are reused, not application data. Checks:

- Focused regressions: fail before / pass after.
- `go test -race ./internal/db -run 'Test(Open|InitialReadinessFailureReleasesResources|Migration)' -count=1`: pass.
- `mage build`: pass before local CLI smoke.
- `mage check`: pass, including existing compatibility/immutable-history tests,
  14 Python verifier tests, generated-adapter overlap gate, vet/lint/build;
  database audit 7634 attempts, `forbidden=0`, `ktx_db=unset`.
- Rebuilt `build/ktx` smoke: creation through `compress` with a missing source
  under a literal `?/#/%` data path returns expected exit 2 and creates the
  intended schema only; `db --path <seed?tag#%23.db> migrate` upgrades an
  explicitly seeded disposable version-zero database and passes integrity check.
- `git diff --check`: pass.

No schema/version/migration algorithm, stale-writer recheck, compatibility
fixture, provider/live database, or other finding was changed. The production
scale schema release gate and native non-Linux checks were not run. Bounded
state summaries remain degraded while exact gets work; profile grounding is
absent, recipe metadata/changelog startup caveats remain, and decision
satisfaction is user-owned. No task lifecycle/evaluation/audit or Git
staging/commit/publication was performed.

## Scoped benchmark process lifecycle repair evidence

2026-09-05, owner `pzztseunxc`, plan `hljjrlykxb`. Implementation and verification
only; task evaluation/completion and satisfaction remain with their assigned
owners. Original inventory references and prior-review classifications above
are unchanged. Wrapper findings are outside this disposition. CLI-published
progress: `ivkophbxat` (publication order 221).

- **Finding 3:** compare/rank (including compare preflight) now propagate
  SIGINT/SIGTERM cancellation. The executor rejects an already-canceled context,
  and comparison/attempt boundaries stop further scheduling. Docker attempts
  and capability probes have fresh execution-specific names and explicit,
  independently timed cleanup. Cleanup transport errors, nonzero exits, and
  timeout results are diagnosed and stop subsequent attempts/candidates.
  `TestRunnerStopsAfterCleanupFailureOrCancellation` originally ran four
  attempts despite cleanup errors/nonzero exits or parent cancellation; it now
  stops after one. Cleanup-timeout results are also covered. The locally built
  CLI's `TestBenchLocalCLISignals` failed before repair for compare/rank under
  both SIGINT and SIGTERM, leaving the fake Docker client alive. All four now
  pass and check cleanup of the exact owned capability-probe name.
- **Finding 4:** overlength names retain a full-input SHA-256 suffix rather than
  dropping identity bytes. A fresh random execution nonce also separates the
  same experiment/attempt identity used in different archive roots.
  `TestContainerIdentityKeepsFullInput` reproduces the original maximum-length
  collision before repair and passes afterward. `TestContainerCleanupOwnership`
  exercises overlapping fake owners with distinct maximum-length IDs and with
  identical IDs; neither cleanup consumes the other's ownership. This is
  offline fake-Docker proof, not a destructive daemon experiment.
- **S8:** executor completion kills residual group members even after leader
  exit; cancellation does not skip SIGKILL escalation when only the leader has
  exited. `Cmd.WaitDelay` bounds inherited copied-output pipes.
  `TestOSExecutorReapsLeaderFirstGroup` reproduced the surviving descendant
  before repair; leader-first and TERM-triggered leader exits now pass with
  direct and copied output. Linux `/proc` distinguishes termination/zombies
  from live descendants; this is not proof of native BSD/Windows behavior.

Checks used fresh disposable HOME/XDG roots and `env -u KTX_DB` per invocation;
test databases were explicitly temporary. Only fake Docker and local subprocesses
were used: no Docker daemon, provider, paid call, or ambient-live database access.

- `mage build`: pass before baseline CLI tests and again before repaired CLI tests.
- `go test ./internal/experiment -count=1`: pass after repair; existing bounded
  timeout retries, paid-attempt accounting, evidence validation and cache-aware
  methodology tests remain unchanged and pass.
- `KTX_LIFECYCLE_CLI="$PWD/build/ktx" go test ./internal/cli -run '^TestBenchLocalCLISignals$' -count=1`:
  fail before / pass after, four cases; repaired run 20.109 seconds.
- `KTX_LIFECYCLE_CLI="$PWD/build/ktx" go test -race ./internal/experiment ./internal/cli -run 'TestContainer|TestOSExecutor|TestRunnerStopsAfterCleanupFailureOrCancellation|TestRunnerAlternatesAndCleansContainers|TestBenchLocalCLISignals|TestBenchComparePreflightIsDBFreeAndKeepsMachineOutputSeparated' -count=1`:
  pass, 2.450 / 21.158 seconds.
- Full `mage check`: first failed because an existing fake-Docker fixture did
  not accept capability cleanup. After that scoped fixture update and a passing
  targeted rerun, the full gate passed: Go tests, 14 Python verifier tests,
  generated-adapter overlap, vet/lint/build; `0 issues`, database boundary
  `78bfcd5173c2b8ce`, 7678 attempts, `forbidden=0`, `ktx_db=unset`.

No schema, public flags, persisted evidence schema, or cache methodology changed.
Attempt timeouts still retain failed-attempt evidence and retry within the
existing attempt budget after successful cleanup; parent cancellation or failed
cleanup aborts scheduling. Incomplete interrupted archives are not promoted to
completed results. Non-cancelled preflight resource budgets, native-platform
verification, wrapper lifecycle work, and recovery from uncatchable SIGKILL or
host failure are not claimed here. State summaries remain degraded, exact get
works, profile grounding is absent, and satisfaction is user-owned. No task
completion/evaluation/audit or Git staging/commit/publication was performed.

## Existing backlog reconciliation and fixed decisions

Exact `state todo get` retrieved these records; public TODO references were
checked. This ledger links overlaps without creating another canonical todo or
changing its lifecycle:

| Existing bare todo ID | Public references | Relationship and disposition owner |
| --- | --- | --- |
| jytspihtzs | `TODO.md:33`, `TODO.md:96` | Consolidated CLI argument handling is the existing umbrella for A8/D3 and tasks 21–22 (`kxklkicklv`, `momdpkraiq`), not another implementation queue. S6 acknowledgement is an explicitly supplied remaining-CLI responsibility; do not lose it while concentrating on flags. |
| lejmjmiqzt | `TODO.md:35`, `TODO.md:97` | Recorded-decision satisfaction review overlaps the final reconciliation responsibility (`bqnxtbzjou`), particularly D4. Retain separate user-owned satisfaction authority; ledger coverage and eventual repairs do not confirm satisfaction. |
| tafkrzkqbh | `TODO.md:30`, `TODO.md:105` | Unified hook processing flow already shares validation/session/source/artifact lifecycle after ingress extraction. D5, tasks 32–33 (`iefdatbpxu`, `wxwwonhjgy`) own bounded remaining-debt assessment or no-change. Finding 16 still needs the distinct retry-stage bypass, not a persistence rewrite. |

Repeated public appearances above are links to existing work, not evidence of
additional independent obligations. The completed single-window coordination
entry at `TODO.md:41` does not establish that ordinary stale-writer leases or
complete-retry stage bypass are fixed. Similarly, the completed benchmark owner
entry at `TODO.md:43` does not repair the separate bench-run fallback in finding 15.

Exact decision retrieval confirms these constraints; none is reopened or marked
satisfied:

- `wajoflwpoe`: clean cutover removes retired readers/paths/probes, not a new
  compatibility layer. D4's producer matrix must protect current emitters until
  covered and converted. Tracked historical evidence stays byte-unchanged.
- `yxlsglvpof`: trusted ingress requires a complete usable native tuple; incomplete
  or unverified tuples use fresh identity. First source, session association and
  persisted artifact remain authoritative (A4, 10, 16, S10).
- `algmjykxaq`: no executable hashing, digest flags or per-callback digest proof.
  Adapter fixes retain canonical absolute executable-path pinning instead.

## Scoped stage evidence — Preserve stage evidence validity

Owner `btxcvatnmt`, plan `hljjrlykxb`; implementation verified on 2026-09-05,
not task completion, evaluation, audit, or user-confirmed satisfaction. Original
finding names, source references, and prior-review classes above are preserved.

- **A5:** `encoding/json` now escapes provenance strings. New
  `internal/cli/compress_provenance_test.go` loads valid TOML, runs compress,
  reads the persisted artifact, decodes profile/route/stages/fallback, compares
  the exact decoded stage argument and output, and recovers the original through
  decompress. Ordinary replacement passes; tab and multiple control characters
  failed JSON decoding before repair and pass afterward.
- **S3:** nullable boolean decoding requires explicit false in the Kagi shape.
  `TestUnwrapResultOutcomeValidity` checks false, true, null, string, and numeric
  flags directly and through the runner. Text `42` is itself valid JSON, proving
  the structure guard cannot mask the regression: null incorrectly projected
  and retained unwrap-result before repair; afterward it remains byte-identical.
- **S4:** keep-ends compares against remaining line count without adding counts.
  `TestKeepEndsExtremeCounts` checks ordinary truncation, max-int head, tail,
  and both, plus zero, negative, and out-of-range parse rejection. The max-int
  head previously emitted a recovered runner panic and panicked directly;
  all cases now pass without panic. Supported positive counts remain accepted.

Checks use a fresh `root=$(mktemp -d /tmp/opencode/ktx-stage-<label>.XXXXXX)`
per invocation and the following prefix (external Go caches contain build data,
not application state):

```sh
env -u KTX_DB HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" GOCACHE=~/.cache/go-build \
  GOMODCACHE=~/go/pkg/mod <command>
```

Exact commands and results:

```sh
# Before repair: fails for all three faults described above.
go test ./internal/pipeline/stages ./internal/cli -run 'Test(UnwrapResultOutcomeValidity|KeepEndsExtremeCounts|CompressControlCharacterProvenance)$' -count=1
# After repair: PASS.
go test ./internal/pipeline/... ./internal/cli -run 'Test(UnwrapResult|KeepEnds|CompressControlCharacterProvenance|EncodePipeline|EncodeArtifactPipeline|RunCompressWith_PipelineModifiesOutput)' -count=1
# Focused race: PASS.
go test -race ./internal/pipeline/... ./internal/cli -run 'Test(UnwrapResult|KeepEnds|CompressControlCharacterProvenance|EncodePipeline|EncodeArtifactPipeline|RunCompressWith_PipelineModifiesOutput)' -count=1
mage check
mage build
./build/ktx compress --help
./build/ktx bench run --replay-file internal/cli/testdata/portable-replay-v1.json
git diff --check
```

Full check passed tests, vet, lint (0 issues), build and safety audit:
`attempts=7582`, `forbidden=0`, `ktx_db=unset`. Local build and help passed;
portable replay returned `consistency=PASS`, 2/2 parsed calls, 70 → 56 bytes.
Replay is configuration-independent compatibility evidence, not coverage of
the changed stages. Compress has no DB-free execution form; its persisted
behavior is tested only with explicitly created per-test databases. No ambient
database, provider call, schema change, JSON-guard redesign, or release gate.
An initial regression-test draft had runner API compile errors; those were
corrected before the behavioral fail-before run. State summaries remain
degraded, exact get works, profile grounding is absent, and satisfaction is
user-owned. Earlier config/verification changes and lifecycle state are untouched.

## Cold pricing discovery evidence — hatnrvkhpu

Scope: PERF3 only, 2026-09-05. Derivation now requests `RefreshAndWrite`
through its price-source interface, reusing the existing atomic publication
and cold-flag transition. No changes to `Refresh`, file-write mechanics,
schema, financial calculations, caller-supplied cost authority, or warm-miss
policy. Tests use an in-memory HTTP fixture transport, never models.dev or a
provider; the persistence integration test uses an explicitly created in-memory DB.

New `internal/db/cost_cache_test.go` evidence:

- `TestCostDeriverColdDiscovery`: known and missing-model initial requests each
  publish once. A fresh `Prices` reload retains the complete price/provenance;
  warm known/missing lookups neither fetch nor change file identity/mtime.
- `TestCostDeriverColdDiscoveryFailure`: simulated timeout, HTTP 502, malformed
  JSON, parent-file obstruction and rename obstruction leave the triggering
  derivation unknown. Fetch failures remain retryable without a published file;
  publication failures retain fetched data and cold state, preserve the blocker,
  and permit explicit write recovery. Recovered files load warm, derive known
  prices, keep missing prices unknown, and leave no temporary-write files.
- `TestPersistToolCallColdDiscoveryReload`: discarding the process singleton
  and disabling the fixture fetch still persists the known cost and NULL for a
  missing model, with exactly one total fetch.

Each verification invocation used a newly allocated
`root=$(mktemp -d /tmp/opencode/ktx-pricing-<label>.XXXXXX)` and this prefix:

```sh
env -u KTX_DB HOME="$root/home" XDG_DATA_HOME="$root/data" \
  XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
  XDG_STATE_HOME="$root/state" GOCACHE=~/.cache/go-build \
  GOMODCACHE=~/.local/share/go/pkg/mod GOPROXY=off <command>
```

Verified commands and results:

```sh
# Before repair: FAIL, both success cases and all five failure/recovery cases.
go test ./internal/db -run '^TestCostDeriverColdDiscovery' -count=1
# Original executable behavior restored temporarily: FAIL, reloaded known cost NULL.
go test ./internal/db -run '^TestPersistToolCallColdDiscoveryReload$' -count=1
# After repair: PASS.
go test ./internal/db -run 'Test(CostDeriver|Prices|PersistToolCall_CostDerivation)' -count=1
# Final focused race: PASS, including all three new tests.
go test -race ./internal/db -run 'Test(CostDeriver|Prices|PersistToolCall.*(Cost|ColdDiscovery))' -count=1
mage check
git diff --check
gofmt -l internal/db/cost.go internal/db/cost_test.go internal/db/prices.go internal/db/cost_cache_test.go
```

Full `mage check` passed build, race-enabled Go tests, all 14 Python verifier
tests, the required generated-adapter overlap check, vet and lint (0 issues).
It used the same isolation prefix plus HTTP/HTTPS/ALL proxies at
`http://127.0.0.1:9` and `NO_PROXY=localhost,127.0.0.1,::1`.
Safety output: `attempts=7634`, `forbidden=0`, `ktx_db=unset`.
Formatting and diff checks passed. The build is `./build/ktx`; no installed
binary, real provider, real pricing endpoint, production-scale release gate,
or performance timing experiment was invoked. Fresh `Prices` and singleton
reset prove retained-cache reuse; a separate multi-OS-process pricing smoke
was not run. No repeated-five-second timing claim is made.

State summaries are degraded; exact task/decision gets work. Profile grounding
is absent (recovery: use Profile to generate PROFILE.md, then retry grounding),
and satisfaction remains user-owned. Startup recipe/changelog metadata caveats
remain outside scope. Earlier changes and ledger history are preserved; no
completion, evaluation, audit, staging or commit was performed.

## Scoped preflight/screen resource evidence — otiqrxtwkw

This verifies the preflight/screen portion only, not task evaluation or
whole-finding completion.
The existing 64 MiB population contract is enforced before reading oversized
regular-file payloads and during all reads with a 64 MiB + 1 sentinel. No new
population size or format restriction was added. Existing process-group escalation,
random container ownership, and independent cleanup remain intact.

Before repair, `TestBenchScreenOversizeAllocation` measured 268,488,280 bytes
allocated for a 256 MiB sparse file. `TestPreflightProbeResources` found zero
timeout/grace on both probes and retained a 2,097,192-byte failure diagnostic.
Both fail-before tests pass after repair. Additional regressions cover infinite
read consumption, below/exact/above boundaries, hung noisy fake commands,
successful noisy commands/builds, and concurrent diagnostic draining.

Fresh HOME/XDG roots and unset KTX_DB were used for each verification invocation;
tests needing databases use disposable targets. `mage build`, focused tests,
focused `go test -race`, and final `mage check` passed. The final check includes
14 Python tests and the required generated-adapter overlap gate; its database
boundary recorded 7,678 operations and `forbidden=0`. The first full check
failed only gofumpt's octal spelling rule in the new fake-command test; fixed
before the passing rerun. No Docker daemon, provider or real-network experiment
was used; Go dependency fetching was disabled.

Local `build/ktx` smoke results: 256 MiB population rejected in 0.002 s with
108 diagnostic bytes; valid population padded to exactly 64 MiB succeeded in
0.097 s; fake hung/noisy preflight returned ineligible in 0.114 s with 249 JSON
bytes. All three created no database. The disposable smoke driver is
`/tmp/opencode/ktx-resource-smoke.py`; retained Go tests are the durable evidence.
Initial smoke-driver assumptions about the usage exit code and required spec
were corrected before these passing results.

The former parsed-stdout gate is now explicitly user-approved and recorded by
the canonical decision writer as `bjqvkvgenm`: **Approve 64 KiB (Recommended)**
for each image-ID inspection and benchmark capability JSON response. The
alternatives were 64 KiB, 1 MiB, and defer. Supplied fixture evidence is 685 bytes
for capabilities and 13 bytes for image inspection (about 95x capability
headroom); a normal sha256 image ID plus newline at about 72 bytes is inference,
not measurement. This is not a general redaction/limits decision or satisfaction
confirmation.

The continuation adds `response.go` and response resource/lifecycle regressions.
Only those two raw stdout responses retain at most 65,537 bytes and reject above
65,536 before trimming/decoding. Overflow cancels the producer, then existing
process-group escalation and independent exact-container cleanup settle before
return. Size and cleanup errors are preserved together. The preflight caller
propagates size errors instead of hiding them as generic capability unavailability.
General `runner.output`, Git status, session lists, tool output and recordings
remain unchanged; build diagnostics retain their separate truncation/drain policy.

Fresh fail-before regressions showed both probes accepting 65,537 bytes; a
32 MiB response retained all 33,554,432 bytes and allocated 67,126,656 bytes on
the image path and 33,568,976 on the capability path. A second fail-before check
showed preflight hiding the size error. After repair, exact 65,536 acceptance,
65,537 rejection, 65,537-byte sentinel retention, less than 2 MiB allocation for
the same huge input, malformed within-limit behavior, uncapped generic output,
and explicit size/cleanup errors all pass. Infinite noisy fake writers ignoring
TERM/PIPE are stopped before the two-second attempt timeout; leader/descendant
exit and exact container cleanup are checked. Focused tests and focused race pass.

The freshly built CLI passed eight offline fake-Docker response checks: normal
and exactly 65,536 bytes succeeded on both paths; 65,537 and infinite responses
returned explicit JSON size errors in 0.023–0.035 s (129–154 output bytes).
Every invocation used fresh HOME/XDG roots and unset KTX_DB; none created a DB.
Disposable driver: `/tmp/opencode/ktx-response-smoke.py`. Opt-in local compare/rank
SIGINT/SIGTERM tests initially failed because their eight-second watchdog was
shorter than the now-inherited 30-second default grace. The test invocation now
sets the existing `--termination-grace 50ms`; all four pass in 0.294 s total.
No product grace change was needed. Final isolated `mage check`, with those local
CLI tests enabled, passed: 14 Python tests, generated-adapter overlap, lint with
zero issues, build, and 7,678 DB operations with `forbidden=0`. No daemon, provider
or real-network experiment ran; Go dependency fetching was disabled.

Continuation progress: `vgrbvwjyts`. Archive owner `czzdcnqozn` is untouched;
task `otiqrxtwkw` stays in progress and S7 stays open for archive materialization
and coordinator disposition.

State caveats: lists are degraded while exact gets work; profile is absent;
startup verification-recipe/changelog metadata is incomplete. No profile repair,
satisfaction confirmation, evaluation, audit, completion, staging or commit was
performed. Satisfaction and lifecycle completion remain with their owners.

## Scoped wrapper cancellation evidence — paused on terminal contract

Owner: `dwndsjjdkx`, plan `hljjrlykxb`. Finding **5**, descendant/cancellation
portion only. This is partial implementation evidence, not finding closure,
task evaluation, completion, audit, or decision satisfaction. Prior output-owner
work and progress `sxvzdoznys` remain intact. Own progress: `layixuabaq`.

The uncommitted candidate in `internal/cli/run.go` and `run_process*.go` uses a
Linux-owned process group. Both wrapper SIGINT and SIGTERM send **TERM**, not
necessarily the incoming signal, followed by a one-second internal grace and
group KILL. WNOWAIT observes leader exit without reaping it; all group signaling
finishes before `cmd.Wait`, preventing PID/group reuse in that interval. Pipe
closure settles the synchronous collector before evidence/session closure.
Committed tool calls remain; interrupted runs publish no complete recording.
There is no new accepted-size cap, schema, public flag, or benchmark refactor.

Regression-first evidence: the original graceful TERM case returned 255 instead
of the trap's 37, never exercised its trap, and published one recording. The
original leader-first/SIGINT case exceeded the five-second watchdog. The new
`internal/cli/run_signal_linux_test.go` exercises INT and TERM across graceful,
resistant descendant, leader-first, and closed-stdout cases. All eight pass on
the local build, with shutdown **1.001–1.003 seconds**. Descendants no longer run,
an unrelated separate-group process survives, the session closes, continuity is
interrupted, committed evidence survives, and no recording is published.

Actual candidate status behavior: normal child numeric status is retained
(including the prior oversized-output regression's exit 7). Graceful trap and
closed-stdout cancellation cases return 37. Escalated reader closure is a
collection failure and returns 4; if EOF wins that race, the child's status is
retained instead. The final local descendant cases all returned 4. A child killed
by signal retains the existing `ExitCode() == -1` convention (255 at OS exit)
unless collection failure overrides it; this last mapping is source-derived,
not a new killed-leader runtime test.

**Contract blocker:** moving a child out of the terminal's foreground group makes
its inherited stdin read stop with SIGTTIN. A local-build PTY probe emitted
`READY`, echoed supplied `hello`, then remained stopped (`/proc/.../stat` state
`T`) past three seconds. Durable regression `TestRunForegroundStdin` reproduces
this with an actual controlling PTY. It fails both against the local build and
under the race detector. The candidate is **not ready to ship**. Per the user's
real-contract-risk pause instruction, no terminal foreground/restore or Ctrl-C
semantics were selected, and no accepted stdin restriction was introduced as
policy. The branch remains in progress for its owner to resolve that contract.

Final checks used fresh HOME/XDG roots and unset KTX_DB for every invocation;
each subprocess DB was beneath its newly owned disposable application root:

- `mage build`: pass before local CLI tests.
- Focused `go test -race ./internal/cli` (wrapper/output/signal/TTY selection):
  fails only `TestRunForegroundStdin`, 14.976 s; no race diagnostic.
- Local-built CLI signal/TTY selection: eight signal cases pass; terminal stdin
  fails; overall command fails, 12.161 s.
- Full `mage check`: fails at Go tests on the terminal regression. Boundary
  `5de1975515520bf7`: 7,754 audited attempts, forbidden=0, KTX_DB unset. Later
  Python/adapter/vet/lint/build stages are not claimed to have run in this check.

Remaining limits: terminal contract unresolved; no native non-Linux proof;
escaped process groups and indefinitely blocked external output sinks are not
proven bounded; uninterruptible kernel tasks have no user-space hard-exit
guarantee. Normal runs retain unlimited output-drain time and the pre-existing
recording buffer. Linux-only scope and internal-budget authority do not confirm
decision satisfaction. Exact state retrieval works; degraded summaries, absent
profile, and startup recipe/changelog caveats remain. No provider/paid run,
ambient DB access, task completion, evaluation, audit, staging, or commit occurred.

## Scoped wrapper cancellation correction — checks passing, audit pending

Owner: `dwndsjjdkx`, plan `hljjrlykxb`, finding **5** cancellation portion only.
Corrective progress: `bhznczrixc`. The preceding failed-candidate evidence and
immutable progress `layixuabaq` remain unchanged; they describe the prior attempt,
not this candidate's current verification state. Task status remains
`in_progress`; this update performs no evaluation, completion, audit or decision
satisfaction confirmation.

The failed assumption was that every child could enter a new process group
without affecting terminal input. Before correction,
`go test ./internal/cli -run '^TestRunForegroundStdin$' -count=1 -v` reproduced
the failure in **4.009 s**. Merely transferring the foreground terminal to the
child would instead hide terminal Ctrl-C from the wrapper when that child ignores
SIGINT. Neither an input restriction nor changed Ctrl-C handling is needed.

The correction leaves the inherited group intact when `/dev/tty` identifies a
controlling terminal, including redirected stdin followed by an agent's own
`/dev/tty` read. No terminal ownership, termios or signal-disposition mutation is
introduced. Only the direct child is an owned signal target in this shared-group
mode: TERM, one-second grace, KILL, and owned-reader closure. Resistant terminal
descendants may survive; they cannot keep the wrapper waiting on stdout after
cancellation. There is **no group-wide escalation in terminal mode**, and no
claim that all descendants are terminated. This avoids killing caller/sibling
processes in that group. Non-terminal runs retain the prior verified owned-group
cleanup, leader WNOWAIT pinning and collector-before-evidence settlement.

Unexpected terminal-discovery errors produce a warning and safely retain the
inherited group, rather than requiring `/dev/tty` access as a new accepted-input
prerequisite. Reader-close errors now join the shutdown diagnostic. The
non-Linux stub changes only its signature; native non-Linux behavior is not
claimed verified. No generic process tracker, terminal relay, public flag,
recording-budget change, schema or external runtime was added.

New `TestRunTerminalLifecycle` cases exercise a real controlling PTY with TOSTOP:
normal stdin, redirected stdin plus `/dev/tty`, actual terminal VINTR with an
INT-ignoring child, and wrapper TERM. The caller remains alive, foreground group
and termios stay identical, and the caller reads new input after wrapper exit.
Normal exit **7** and complete recording are retained. Cancellation exercises the
TERM trap, retains committed evidence, closes the session, marks continuity
interrupted and publishes no complete recording. A deliberately resistant
descendant holds stdout beyond leader exit; the test caller, not product code,
adopts and cleans it up after proving bounded settlement. The eight existing
non-terminal signal cases now explicitly start without a controlling terminal,
so their group-cleanup premise also holds under interactive developer shells.

Verification used a fresh `mktemp` root under `/tmp/opencode` for every invocation,
unset `KTX_DB`, and fresh application variables (HOME, XDG_DATA_HOME,
XDG_CONFIG_HOME, XDG_CACHE_HOME, XDG_STATE_HOME).
Each subprocess database was beneath its explicit newly owned temporary test
root. Go commands used explicit existing build/module caches and
`GOPROXY=off GOSUMDB=off`; no provider, daemon, paid run or ambient DB was used.

- `go test ./internal/cli -run '^TestRun(ForegroundStdin|TerminalLifecycle)$' -count=1 -v`:
  pass, **2.079 s**. The previously failing stdin regression passes.
- `go test -race ./internal/cli -run '^Test(ExecuteRun|Run(Output|Signals|ForegroundStdin|TerminalLifecycle))' -count=1 -v`:
  pass, **18.770 s**, including eight signals, normal numeric exit,
  oversized forwarding, broken pipe, error/short sinks and terminal lifecycle.
- `mage build`: pass before local CLI execution.
- `KTX_RUN_SIGNAL_CLI=$PWD/build/ktx go test ./internal/cli -run '^TestRun(Signals|ForegroundStdin|TerminalLifecycle)$' -count=1 -v`:
  pass, **10.240 s**. Eight signal shutdowns: **1.001–1.004 s**; terminal
  Ctrl-C/TERM settlement: **1.002–1.003 s**. Owned non-terminal descendants stop;
  the unrelated separate-group process and shared-terminal caller survive.
- Full `mage check`: **pass**, through race tests, all 14 Python tests, required
  generated-adapter overlap, vet, lint (**0 issues**) and build. Boundary
  `0da3707030d26e6f`: **7,862 attempts, forbidden=0, KTX_DB unset**.
- `python3 /tmp/opencode/ktx-wrapper-smoke.py`: local CLI pass for **67,174,455**
  normalized bytes, SHA-256
  `062d93415c6c7b8fa807666f17a341aacbf724473b52f9837715beaad419682d`, exit 7 and
  following control persistence; `/dev/full` and broken pipe return 4, reap the
  direct child, close the session and retain no incomplete recording. This
  existing disposable driver is not durable proof authority.
- gopls diagnostics: none. `git diff --check`: pass.

Residual limits remain explicit: shared-terminal or escaped-group descendants
are not promised terminated; indefinitely blocked external output sinks and
uninterruptible kernel tasks are not proven bounded. Normal output drain time
and the existing recording buffer are unchanged. The coordinator owns the
requested next audit and task disposition. All unrelated checkout changes and
historical proof were preserved; no staging, commit, push or ref change occurred.

## Scoped terminal descendant correction — bounded post-audit attempt

Owner: `dwndsjjdkx`, plan `hljjrlykxb`, finding **5** cancellation portion only.
New canonical progress: `fxjcqsvqgw`. Prior `layixuabaq`, `bhznczrixc`, and the
preceding evidence sections remain unchanged. Audit
`ses_f8cbe912fffeP1TTHhSdaW6k7S` rejected the prior candidate's ownership
criterion: closing the reader did not discharge ownership of the resistant
terminal descendant. Its survival assertion and caller-side success cleanup
encoded the defect. Persisted evaluation `wrapper-cancel-audit-20260905-1`,
failure count **1**, remains unchanged. This is correction evidence, not a new
evaluation, audit verdict or task completion.

Regression first: changed the PTY assertion to require the owned descendant to
be stopped before wrapper exit, with test kills used only on failure.
`go test ./internal/cli -run '^TestRunTerminalLifecycle/(ctrl-c|term)$' -count=1 -v`
then failed both cases against the prior candidate (**2.074 s**) with
`owned terminal descendant survived wrapper shutdown`.

The correction adds `internal/cli/run_descendants_linux.go` and connects it to
the existing wrapper lifecycle. A terminal wrapper becomes a Linux subreaper
**before spawning** its agent. This retains kernel ownership when the leader
exits first, without transferring or signaling the inherited terminal group.
After the leader exits, `/proc` supplies only candidate PIDs. Non-reaping
`waitid` first checks child ownership; a pidfd-based recheck then binds child
ownership, individual KILL and reaping to the same identity. A stale or foreign
PID is not signal authority. The existing leader remains unreaped until the
supervisor settles and `cmd.Wait` runs.

Repeated nonblocking sweeps handle children adopted after a resistant parent
is killed. Reader closure remains at the original **one-second** grace deadline;
descendant settlement has **one additional second**, rather than an unbounded
wait on one child. The collector and process supervisor still settle before
session/continuity closure. Interrupted runs retain committed evidence and
publish no complete recording. Preparation and settlement errors are reported;
the wrapper restores its prior subreaper state. The non-terminal group path
remains intact, and the non-Linux seam is a no-op. No generic process tracker,
terminal relay, schema, public flag, input-size cap or recording-budget change
was added.

Six PTY cases now cover normal stdin, redirected stdin plus `/dev/tty`, actual
VINTR with an INT-ignoring child, TERM, an already-exited **unreaped** leader,
and a nested resistant descendant. Cancellation asserts stopped descendants
and settlement within **two seconds**. The caller and an unrelated sibling in
the same terminal group remain alive. TOSTOP writes, foreground group, termios,
caller read-after-exit, normal numeric exit **7**, complete normal recording,
interrupted continuity and committed tool evidence are checked. Descendant kills
in the test are failure cleanup only. The wrapper subprocess also checks
subreaper state restoration after `executeRun` returns.

All verification invocations used fresh HOME, XDG_DATA_HOME, XDG_CONFIG_HOME,
XDG_CACHE_HOME and XDG_STATE_HOME roots with `KTX_DB` unset. Subprocess databases
were explicit disposable test roots. Go commands used existing explicit
build/module caches plus `GOPROXY=off GOSUMDB=off`. No provider, daemon, paid run,
ambient DB, schema rehearsal or production-scale gate was used.

- Targeted PTY/foreground selection: pass, **4.207 s**. Final two-second budget
  assertions also pass in the following final race, CLI and full checks.
- `go test -race ./internal/cli -run '^Test(ExecuteRun|Run(Output|Signals|ForegroundStdin|TerminalLifecycle))' -count=1 -v`:
  pass, **23.438 s**. Includes eight non-terminal signals, normal exit,
  oversized output, broken pipe, short/error sinks and the six PTY cases.
- `mage build`: pass before local CLI execution.
- `KTX_RUN_SIGNAL_CLI=$PWD/build/ktx go test ./internal/cli -run '^TestRun(Signals|ForegroundStdin|TerminalLifecycle)$' -count=1 -v`:
  pass, **12.355 s**. Terminal cancellation/settlement **1.012–1.015 s**;
  eight non-terminal signal cases **1.001–1.005 s**. The owned resistant and
  nested terminal descendants stop without caller or unrelated sibling cleanup.
- Final `mage check`: pass through race tests, all **14** Python tests,
  generated-adapter overlap, vet, lint (**0 issues**) and build. Boundary
  `b38cf747842494ec`: **7,884 attempts, forbidden=0, KTX_DB unset**.
- `python3 /tmp/opencode/ktx-wrapper-smoke.py`: pass against the local binary;
  **67,174,455** normalized bytes with unchanged expected SHA-256
  `062d93415c6c7b8fa807666f17a341aacbf724473b52f9837715beaad419682d`, exit 7 and
  following control persistence; `/dev/full` and broken pipe return 4, reap the
  direct child, close the session and publish no incomplete recording. The
  existing disposable driver is not durable proof authority.
- gopls diagnostics: none. `git diff --check`: pass.

Actual limits: terminal ownership preparation requires procfs plus Linux
subreaper and pidfd child-wait support (the latter available since Linux 5.4).
Unavailable preparation fails before spawning, rather than silently reverting
to unsafe PID signaling or accepted descendant survival. Older kernels,
restricted syscall environments and native non-Linux execution were not verified.
This is not arbitrary escaped-process or privilege-changing-work containment;
uninterruptible kernel tasks and indefinitely blocked external output sinks
retain their existing limits. Normal drain time and recording memory behavior
are unchanged. The single bounded correction stops here; the coordinator owns
disposition. No old progress, unrelated work, task status or evaluation was
rewritten; no staging, commit, push, ref change or audit was performed.

## Finding 5 — approved terminal support documentation and verification

Owner: replacement `rmbdxwfguf`, plan `hljjrlykxb`. This is **docs-only** work,
not another runtime correction. User-approved amendment `llshvynohy` accepts
readable procfs, subreaper support, `pidfd_open`, and `waitid(P_PIDFD)`, normally
Linux 5.4+ subject to backports and syscall policy, with explicit pre-spawn
failure when preparation is unavailable. The user declined older/restricted
environment compatibility work and deferral. Linux-only `ygcysqxcfr` and
standing boundaries `yohiahsqjv` otherwise remain in force.

The current README, wrapper reference, and skill now describe that requirement,
including redirected stdin with a controlling terminal, and exclude non-terminal
runs and unrelated commands. The reference documents bounded grace/settlement,
identity-checked individual descendant cleanup, caller terminal preservation,
and committed versus complete-recording evidence. No runtime code, fallback,
capability check, test, schema, help string, or accepted input limit is changed.

Acceptance mapping:

1. Bounded grace, owned descendants and evidence closure: current supervisor and
   collector ordering match the retained focused/race/local CLI evidence in
   `fxjcqsvqgw`; the guide describes the one-second grace and additional
   one-second terminal descendant-settlement budget without promising arbitrary
   escaped-process or blocked-sink containment.
2. Terminal/caller preservation and no descendant-held stdout hang: the six
   existing PTY cases exercise normal and redirected input, Ctrl-C, TERM,
   leader-first and nested descendants, caller/sibling survival, TOSTOP,
   foreground group, termios, evidence and recording assertions.
3. Approved support scope: preparation precedes `cmd.Start`; the retained
   continuation audit `ses_f8cbe912fffeP1TTHhSdaW6k7S` fault-injected
   `pidfd_open=ENOSYS` and observed explicit pre-spawn failure on normal terminal
   input. Under the new approval this is expected behavior, not a runtime defect.
4. Failed history: exact retrieval confirms `dwndsjjdkx` remains **blocked**,
   attempt count **2**, failure count **2**, last verdict **fail**. Neither that
   record nor historical progress/evidence is rewritten. The coordinator may
   supersede it only after replacement PASS and completion; no evaluation,
   lifecycle, TODO resolution or decision-satisfaction write belongs to this work.

Retained full-check authority: `fxjcqsvqgw` records **7,884** audited operations,
forbidden **0**; the later independent combined-tree check in `rtyakjcilg`
records **8,712**, forbidden **0**. These are prior runs, not new checks in this
documentation cycle.

Fresh verification for this documentation cycle:

- `mage build`: pass before invoking `./build/ktx`.
- `KTX_RUN_SIGNAL_CLI=$PWD/build/ktx go test ./internal/cli -run '^TestRun(Signals|ForegroundStdin|TerminalLifecycle)$' -count=1 -v`:
  pass, **12.314 s**, all eight non-terminal signal cases, foreground stdin and
  six PTY cases. Terminal cancellation/settlement **1.012–1.013 s**, below the
  existing two-second assertion; normal/redirected cases retain numeric exit 7
  and complete recordings. Caller/sibling, termios/foreground-group and
  interrupted evidence assertions pass without success-path descendant kills.
- Disposable Python PTY probe with local CLI and
  `strace -f -e inject=pidfd_open:error=ENOSYS`: pass, explicit exit **4** with
  `prepare descendant ownership: open process identity: function not implemented`.
  The injected syscall is confirmed in the trace and the wrapped shell's
  startup marker is absent. This repeats the unsupported-preparation observation,
  not an implementation retry. The disposable probe is not durable test authority.
- Documentation scope and local links reviewed against the exact approval and
  current preparation/supervision/evidence ordering; `git diff --check`: pass.

Build and runtime verification used fresh disposable HOME, XDG_DATA_HOME,
XDG_CONFIG_HOME, XDG_CACHE_HOME and XDG_STATE_HOME per invocation, `KTX_DB` unset, and only
temporary owned databases. Go used explicit existing caches with
`GOPROXY=off GOSUMDB=off`. No provider, paid run, ambient DB, new runtime check,
full-suite repeat, lifecycle/evaluation/audit/satisfaction mutation, staging or
commit was performed. Unavailable procfs/subreaper/waitid variants were not
separately fault-injected; no older/restricted-host compatibility is claimed.
The four acceptance mappings and fresh evidence are ready for coordinator review;
the replacement remains in progress and the original remains blocked.

## Finding 8 — archive confinement

Owner: `bqpenkdmho`, plan `hljjrlykxb`, 2026-09-06. Current disposition:
**implemented and verified on Linux; task remains in_progress for the
coordinator's evaluation and lifecycle ownership.** This adds current evidence
without replacing the baseline source review or another owner's proof.
Canonical progress: `xfqwiuhzro` (typed CLI append passed; progress lint passed).

The interpretation boundary now holds an `os.Root` throughout summarize and
validate. Archive-relative path checks precede filesystem operations, but are
not treated as authorization for an ordinary later open: reads, metadata
operations, workspace traversal, and atomic summary replacement remain rooted.
File descriptors are type-checked before reading; nonblocking opens also prevent
a FIFO replacement from hanging a read. The official `os.Root` contract covers
concurrent symlink changes on Linux, not a general process sandbox or a coherent
snapshot of concurrently edited content.

Actual consumers covered:

- `entry_contract.go`, `summary.go`, and `validate.go`: manifest, attempt ledger,
  retained inputs, declared artifact presence/hashes, workspace trees, existing
  summary, and summary replacement. The prior entry/version/ledger contract and
  exclusion of damaged-attempt credit remain in place.
- `regeneration.go`, `stream.go`, and `call_accounting.go`: authoritative and
  current-producer legacy stream paths consume confined open descriptors, not
  reopened archive-controlled strings. `json.go` supplies the reader-based
  ledger decoder used by both archive entry and `LoadAttempts`.
- `pricing.go`: retained pricing reads are rooted. Explicit producer pricing
  source inputs retain their separate caller-selected source-path contract.
- `archive_database.go`: SQLite receives only private temporary main/WAL copies.
  Actual opened database descriptors determine physical sidecar names, including
  in-root database aliases. WAL/SHM/journal names are confined before access;
  the private SHM index is rebuilt. Readiness and coordination cannot open or
  migrate an archive original. Temporary copies are removed after SQLite closes.
- `internal/cli/bench_lifecycle.go`: the discovery loader uses confined regular
  file opens while preserving permissive list/show of incomplete manifests.

Owned relative symlinks remain readable. Workspace link text is hashed without
following its target, preserving the producer's hash format. FIFOs, sockets,
devices, and other unsupported file types produce explicit errors. Empty or
zero-header rollback journals remain usable. Nonzero rollback-journal headers
are explicitly rejected: SQLite recovery can consult an embedded super-journal
pathname outside the copied database directory. Recovery of such interrupted
archives is not claimed; completed owner snapshots remain the supported input.

**Before/after evidence.** The new `TestArchiveConfinement` failed before the
product change in 15 of 18 path cases through actual `RegenerateSummary` and
`ValidateExperiment` calls. Inotify on test-owned targets observed external
stream, database, pricing, input, and workspace opens/reads; several absolute
paths instead survived as damaged evidence rather than being rejected. After
the repair all 18 cases pass through both Go entries and the local-built CLI,
with no external target open/read/modify events. Additional regressions cover
fixed manifest/ledger/summary symlinks, parent-directory replacement, database
sidecars, workspace link text, FIFO/socket diagnostics, and concurrent stream
and workspace symlink replacement. SQLite access-log assertions observe only
private query targets, their cleanup, and unchanged source database bytes.
Committed WAL data remains queryable through an in-root database alias; cold
journals and normal owned links pass actual summarize/validate controls.

**Verified commands and outcomes.** Every verification invocation used a new
disposable HOME and XDG data/config/cache/state/runtime root, `env -u KTX_DB`,
and explicit test-owned databases. Go module/build caches alone were reused.

- Before: `go test ./internal/experiment -run '^TestArchiveConfinement$' -count=1`
  — failed as described above, before implementation.
- `go test ./internal/experiment ./internal/cli -count=1` — passed.
- `go test -race ./internal/experiment ./internal/cli -run 'TestArchiveConfinement|TestEntry|TestBenchLifecycle' -count=1`
  — passed on the final test set (experiment 33.949s; CLI 1.025s).
- `mage build`, then
  `KTX_ENTRY_CONTRACT_BINARY=~/git/ktx/build/ktx go test ./internal/experiment -run 'TestArchiveConfinement|TestEntryProducerRegenerationMatrix' -count=1 -v`
  — passed. Each child CLI invocation also gets fresh HOME/XDG and unset KTX_DB.
  All five current producer rows retain repeatable summary bytes and valid
  controls, including public list/show/summarize/validate. The existing
  `experiment-entry-contract-matrix.md` is untouched.
- Final `mage check` — passed tests/race, vet, lint (zero issues), build, 14
  Python checks, and the required generated-adapter overlap check. Its physical
  DB verification reported 12,608 operations and **forbidden=0**, KTX_DB unset.
- An earlier full check found five lint issues introduced by this change; they
  were fixed, `mage lint` passed, and full check was rerun successfully. The
  first CLI no-access probe placed directory targets inside the independently
  scanned experiment-discovery root. Moving those unrelated fixtures outside
  that root removed the discovery false positives; the final CLI probe passes.

No schema change, new byte/record cap, durable evidence deletion, executable
hashing, provider/model call, daemon, paid run, or real-archive rehearsal was
used. Private database copying serves confinement, not the separate archive
performance task `czzdcnqozn`; configuration/image attestation `tqmgzzesya` is
unchanged. Previous uncommitted work, tracked history, the producer matrix, and
pre-existing `tools/__pycache__` remain in place. State summaries remain degraded,
exact gets work, profile is absent, and satisfaction is user-owned. No task
completion, evaluation, audit, staging, commit, or push was performed.

## Finding 15 disposition — explicit benchmark session ownership

Owner `jgvodxuxul`, plan `hljjrlykxb`; implementation evidence recorded in
canonical progress `vjwekaemvb` on 2026-09-06. This disposition is limited to
finding 15, not an audit, task evaluation, or lifecycle completion. The baseline
inventory and all earlier dispositions remain historical evidence.

`internal/cli/bench_run.go` no longer scans sessions or falls back to old matching
history. Each actual wrapper execution receives a private inherited descriptor;
`internal/cli/run.go` writes its newly opened session ID only after completed
collection. The descriptor and selector are not forwarded to the agent. Bench
uses that exact owner for aggregation and stream linkage. Missing completion
receipts produce an explicit error before any benchmark row or replay is
recorded; completed nonzero child exits remain scoreable. Existing source-row
ownership, cost authority, stdout normalization, cancellation and standalone
completion-only recording are unchanged. No public flag or schema was added.

Verification:

- `mage build` passed before both local-CLI before/after runs.
- `KTX_BENCH_OWNER_CLI=$PWD/build/ktx go test ./internal/cli -run
  '^TestBenchOwnedSessionLocalCLI$' -count=1 -v`: **4/4 failed before**, **4/4
  passed after**. The baseline chose a same-agent session inserted during the
  run instead of the actual wrapper owner; a forced wrapper session-insert
  failure reused old history and returned a false score-zero headline. Missing
  agent startup also returned a false headline.
- Added interruption coverage, then ran `go test -race ./internal/cli -run
  'TestBenchOwnedSessionLocalCLI|TestRunOutput|TestRunSignals|TestRun.*Record|TestRun.*Receipt'
  -count=1 -v` with both `KTX_BENCH_OWNER_CLI` and `KTX_RUN_SIGNAL_CLI` pointing
  to the local build: **passed**, including 5/5 ownership cases, stdout checks
  and 8/8 standalone signal cases. Successful owned runs reconcile score 1,
  tokens 11/7, cost 0.25, two source rows/session count, exact stream/hash and
  benchmark linkage. Unrelated sessions receive no recording. Historical
  recording bytes remain unchanged; failed/incomplete executions add neither
  benchmark rows nor recordings.
- `KTX_BENCH_OWNER_CLI=$PWD/build/ktx mage check`: **passed** full race tests,
  14 Python tests, generated-adapter overlap, vet, lint (0 issues), and build.
  Database verification reported 12,694 operations, **forbidden=0**, KTX_DB unset.
- `git diff --check`: **passed**; canonical progress lint and append: **passed**.

Each verification invocation used fresh disposable HOME/XDG roots, ambient
KTX_DB unset, and external Go tool caches. Fake-agent invocations seeded explicit
disposable databases; no provider, paid run, daemon, or ambient-live DB was used.
The production schema is unchanged, so no schema-release gate was needed.
Unbounded bench stdout/spooling (`jfgymxttel`/`mljcarlewi`) and argument ownership
(`kxklkicklv`) remain deferred. Approved wrapper cancellation and archive
confinement, the archive entry matrix, prior uncommitted state, and pycache remain
untouched. State lists remain degraded, exact gets work, profile is absent, and
satisfaction remains user-owned. The coordinator owns completion and commits.

## PERF1 planning disposition — recording lifecycle, not a fix

2026-09-06; active decision owner `jfgymxttel`, implementation owner `mljcarlewi`.
Complete decision [`zyeasphydk`](../entities/decisions/decision/zyeasphydk.yaml)
resolves the coverage gate under standing authority `yohiahsqjv`: one private
stdout/recording spool, incremental hash, bounded aggregate-only usage and answer
reader passes for normal bench, and private stderr scratch with exact failed-path
forwarding. Prior decision [`jyykravpup`](../entities/decisions/decision/jyykravpup.yaml)
and progress `szjxgyhosj` remain unchanged provisional history. **PERF1 remains
open, not fixed**; implementation and qualification still belong to `mljcarlewi`.

[Scoped measurement and lifecycle evidence](recording-resource-lifecycle.md)
records 18/18 small representative phase passes, 4/4 dense-step passes, and the
actual-connection SQLite limit probe. At 16 MiB, spool/hash RSS was 15,392 KiB,
buffer capture 46,416 KiB, publication 102,300 KiB and full replay 102,484 KiB.
The compile/effective limit of 1,000,000,000 bytes is not a measured fixture size.
Selected large proportional budgets are future qualification thresholds, not
proven worst-case guarantees or new runtime acceptance limits. `/tmp` was tmpfs;
process RSS does not include all spooled host memory.

The verified analyze continuation and local source review establish that normal
bench does not consume retained ModelSteps. Input-sized private disk is authorized,
not an impossibility gate. Bench N>L keeps its complete spool and unchanged metrics
and score, while recording alone is unavailable. Publication and whole-TEXT replay
retain L-derived proportional memory; capture/interpretation have separate finite
working-memory thresholds. Preserve scoring's 4 MiB oversized-line stop versus
usage's 64 MiB skip, and stderr's no-trim/no-added-newline delimiter behavior.
The resolved architecture question is removed; its [TODO anchor](../../TODO.md#discuss-benchmark-recording-resource-coverage)
now links the choice, with implementation pending. The evidence document defines
lowered-L, dense-step, large-stderr, capture/read-failure, interruption and verified
commit/retention cases. Existing measurements were not repeated or upgraded into
new passing evidence. No smaller cap, schema change, global retention quota or
automatic last-copy deletion is selected.

No runtime code, other finding disposition, historical evidence, task status,
evaluation, audit or satisfaction was changed. Finding 15 and its completed
producer/session ownership receipt `vjwekaemvb` remain authoritative. Lifecycle
completion and commits remain with their assigned owner.

## PERF1 implementation disposition — bounded capture and qualified interpretation

2026-09-07; implementation owner `mljcarlewi`, plan `hljjrlykxb`, exact decision
`zyeasphydk`. The preceding planning disposition remains historical. Selected
runtime work and scoped qualification are now implemented; **task status remains
in_progress and lifecycle completion belongs to the coordinator**. This is not
an audit, evaluation or decision-satisfaction update, nor a global bounded-history
or constant-RSS claim.
CLI-published implementation progress: `absisndpqx`.

Normal run/bench capture uses owner-private stdout spooling and incremental hash;
bench shares that copy with aggregate-only usage/answer interpretation and keeps
accepted metrics beyond SQLite L. Private stderr retains existing failed-wrapper
bytes and formatting. Optional recording failures do not stop forwarding;
required bench stdout capture/read failure cannot publish a headline. Publication
queries the actual reserved connection, preserves writer/schema/retry boundaries,
and only exact committed-row identity/linkage/format/size/recomputed-hash verification
permits stdout cleanup. Missing receipt, failed/uncertain publication, partial
capture and over-limit completion retain their last private copies. No public
cap, schema, recovery command, archive escape or automatic pruning was added.

[Fresh implementation evidence](recording-resource-lifecycle.md#implemented-capture-and-qualification--2026-09-07)
records 15/15 separate resource samples, six normal and six lowered-L local bench
cases, local required-failure and standalone cases, large-record boundaries,
ownership/interruption/SIGKILL cases, targeted race checks and final passing
`mage check` with zero forbidden DB accesses. At 16 MiB, combined-capture RSS is
14,413,824 bytes, interpretation 19,329,024, publication/read-back 107,745,280 and
whole replay 138,395,648. Combined capture adds 128,688 allocated bytes over its
matched control; fixed-record retained-heap growth is 6,200 bytes. Publication
and replay remain proportional to N<=L; private storage remains N+E plus metadata.
Failed formatting and contaminated-initial-RSS attempts are disclosed, not counted
as passing evidence. Existing historical evidence and all other finding
dispositions remain unchanged.

## A8 scoped implementation — wrapper argument ownership

2026-09-06; owner `kxklkicklv`, plan `hljjrlykxb`. **A8 remains partial:** this
implements `run` only; `momdpkraiq` still owns the remaining CLI surfaces. No
task status, evaluation, audit or decision satisfaction is asserted here.
Build progress: `tqcdnveppy`, appended through the typed CLI writer.

`internal/cli/run.go` now resolves quiet, help, recording and collection in one
ordered pass. `internal/cli/dispatch.go` does not reinterpret that pass's child
argv. Wrapper flags retain documented before/after-executable positions until
the first `--`; that separator is removed, and later tokens remain literal.
Booleans never consume a following token, honor explicit false, and reject empty
or malformed attached values. Collection retains its attached `=id` contract.
Global format value consumption remains with the unchanged global parser.
No arbitrary child-option arity is inferred: flag-like child values require the
documented escape boundary or an attached child value. Executables named
`true`, `false`, `0`, `1`, and escaped `--quiet` are regression-tested.

Evidence from this implementation:

- Before the implementation, the initial `TestRunArgumentOwnership/dispatch`
  table failed ten cases: after-executable false, last quiet false, before/after
  escape boundaries, escaped unknown-option values, last help false, and four
  empty/malformed quiet/help cases. This was an in-process baseline, not a
  baseline built-binary run.
- Afterward, the expanded table in `internal/cli/run_args_test.go` passed all
  **52 cases through dispatch and the same 52 through the mage-built CLI**.
  Real executable children capture NUL-delimited argv; assertions cover exact
  output, child exit 23, machine-readable usage errors, DB-free help/errors,
  recording rows, collection IDs, consumed ktx values, and malformed input.
- `mage build` passed before built-CLI verification. The actual invocation
  `./build/ktx run /bin/echo -- --record-stream` printed the literal flag and a
  normal JSON summary with exit 0. Built `run --help` documents the escape and
  arity limitations. CLI reference and changelog now match.
- Focused `go test -race ./internal/cli -run
  'TestRun|TestExecuteRun|TestDispatch|TestBenchOwnedSessionLocalCLI' -count=1`
  passed with `KTX_TEST_RUN_BINARY`, `KTX_RUN_SIGNAL_CLI`, and
  `KTX_BENCH_OWNER_CLI` pointing at `build/ktx`. This exercised existing normal
  stdout, Linux signal/TTY lifecycle, and private benchmark owner-receipt tests.
- Full `mage check` passed: Go race tests, Python evidence tests, generated
  adapter gate, vet, lint (zero issues), and build. Its verification boundary
  reported 12,907 database attempts, **forbidden=0**, `ktx_db=unset`.
  `git diff --check` passed.

Each verification invocation used fresh disposable HOME/XDG roots with KTX_DB
unset; Go dependency resolution was offline. Tests used only owned temporary
databases and fake children, with no provider or paid calls. Prior uncommitted
process/output/receipt work and untracked state were preserved. Recording
resource lifecycle (`jfgymxttel` and dependent work) is unrelated and unchanged;
no schema, accepted-size limit, framework or public alias was introduced.

State list summaries remain degraded while exact gets work; profile grounding
is absent. Startup verification-recipe and changelog-summary caveats remain;
these do not replace the directly run checks above. Satisfaction is user-owned,
and lifecycle completion and commits remain with their assigned owner.

## A8/S6 scoped implementation — remaining command arguments and write acknowledgement

2026-09-06; owner `momdpkraiq`, plan `hljjrlykxb`. The assigned remaining
**A8 implementation is covered** across init, db, stats, bench, plugin and
shared format handling, alongside the preceding run work. **S6's assigned
non-wrapper write paths are implemented** for tool calls and session close.
This records implementation evidence, not task completion, evaluation, audit,
or decision satisfaction.
Build progress: `bgmqnrqhff`, appended through the typed CLI writer.

Known value-taking options are bound before independent common/command scans;
flag-looking values stay literal. Namespace ordering supports options around
subcommands without guessing child option arity. The first `--` stops ownership;
remaining operands still follow each command's existing syntax. Init boolean
flags accept explicit false. Common help/quiet honor occurrence order, local
format overrides global format, and an empty final format is a usage error.
Stats request validation and bench report selector/arity validation precede
query readiness. Valid queries retain read-only handles after readiness. Bench
semantic IDs, lifecycle ownership, fixed/raw output contracts and failure codes
remain unchanged apart from the assigned delivery-failure reporting.

Both write paths check acknowledgement errors and short writes after commit,
return exit 4 with the committed identity, and never claim rollback. Duplicate
tool-call IDs remain rejected; session-close retry remains supported. Tests
confirm one durable observation after acknowledgement loss and retry.

Verification:

- The initial new regression run failed 14 argument cases and all four write
  sink cases before implementation. This was a dispatch baseline, not a
  baseline built-CLI run. Later cases expanded coverage without claiming they
  were all observed failing before the change.
- `TestRemainingOwnedArguments` passes **47 dispatch and 47 local-built CLI
  cases**. Coverage includes normal/negative consumed help/format literals,
  missing/unknown options, explicit false, namespace placement, escaped tails,
  format precedence, and persisted plugin-description values.
- Six malformed requests beat an explicitly seeded corrupt temporary database
  without changing its bytes or creating WAL/SHM files. Three built-CLI queries
  (stats, db report, bench report) preserve a seeded current database byte-for-byte.
  Five help surfaces match the tested ownership guidance; four post-commit sink
  cases cover error/short-write × tool-call/session-close.
- Focused CLI tests passed. After `mage build`, the final command
  `go test -race ./internal/cli -run 'TestRemaining|TestWriteAcknowledgementFailureKeepsCommit|TestRunArgumentOwnership|TestBenchOwnedSessionLocalCLI' -count=1`
  passed with `KTX_TEST_RUN_BINARY` and `KTX_BENCH_OWNER_CLI` selecting `build/ktx`.
  The existing wrapper's 52 dispatch/52 binary cases and private completion
  receipt tests passed without changing run.go or its child boundary.
- Full `mage check` passed: Go race tests, 14 Python evidence tests, generated
  adapter gate, vet, lint (zero issues), and build. Boundary `d9908fbde4ea074a`
  reported 13,019 attempts, **forbidden=0**, `ktx_db=unset`. An earlier full run
  reached lint and failed on the shared printID caller and new-file octal style;
  both were corrected before the passing run. `git diff --check` passed.

Every verification invocation used fresh disposable HOME/XDG roots and unset
KTX_DB at entry; tests explicitly selected only their newly seeded temporary
databases. There were no provider/paid calls, daemon operations, schema changes,
accepted-size caps, new public aliases, staging or commits. All prior uncommitted
work and pycache were retained.

Residual boundaries: compression's legacy `printID` caller is unchanged; this
entry does not claim a blanket output-error sweep beyond the assigned write
acknowledgements. Recording-resource policy remains unrelated and blocked.
Exact TODO `jytspihtzs` was reconciled as the existing seven-surface umbrella,
not duplicated or closed; its lifecycle remains with the assigned owner.
Degraded list summaries, absent profile grounding, and startup recipe/changelog
summary caveats remain. No broader remediation ledger rows were updated.

## Findings 7 and 14 — execution input binding

Owner: `tqmgzzesya`, plan `hljjrlykxb`; implementation evidence dated 2026-09-06.
Canonical progress: `aiphewpnby` (typed CLI append).
Task remains `in_progress`; no evaluation, audit, satisfaction, or completion
state is written by this worker.

- **7:** `snapshotConfigs` captures each source path once, retaining permissions
  and deriving stage metadata/hash from those bytes. `snapshotInputs` copies the
  captured execution configuration, not the mutable source. The common evidence
  loader hashes both actual Docker-mounted retained configs through `archive.hash`
  and requires agreement with manifest identities and retained pipeline copies.
  Existing legitimate independent copies remain readable; no historical rewrite
  or archive schema change. Missing identity or files and changed bytes fail.
- **14:** image inspection accepts only a full lowercase `sha256:` plus 64 hex
  image ID. The capability probe and every attempt use that ID; the original tag
  remains a manifest label. Inspection failure or invalid/missing identity keeps
  `image_capability_unavailable` and returns before auth, archive creation or
  attempts. This benchmark artifact requirement does not restore KTX executable
  hashing removed under `algmjykxaq`.

Regression evidence in `execution_identity_test.go` failed before repair:
changed/deleted actual mounted files passed validation/regeneration while the
other input copy stayed intact; source edits between snapshots changed input
identity; fake capability and both-side attempt commands used the mutable tag;
invalid identities reached capability execution. These cases pass after repair.
Additional checks cover disagreeing independently hash-valid copies, source
deletion after capture, source permissions, no archive creation on invalid image
identity, and full-ID reuse after retagging following inspect and between sides.
The Linux confinement matrix now includes execution-config parent, absolute and
symlink escapes, with inotify proving external targets remain unopened.

Checks and results (each verification invocation entered with fresh disposable
HOME/XDG roots and unset KTX_DB; test databases were explicit disposable owners):

- `go test ./internal/experiment -run '^TestExecution' -count=1`: initial expected
  failures above; repaired regressions and `go test ./internal/experiment -count=1`
  passed. Synthetic config-less fixtures and abbreviated fake image IDs were
  updated, not accepted through a reader bypass.
- `mage build`: passed before using `~/git/ktx/build/ktx`.
- With `KTX_ENTRY_CONTRACT_BINARY=~/git/ktx/build/ktx`,
  `go test -race ./internal/experiment ./internal/cli -run 'Test(Execution|Entry|ArchiveConfinement|InspectionResponse|PreflightProbeResources|BenchComparePreflight)' -count=1`
  passed. Local CLI rejects mounted-file mutation/deletion; all five current
  producer regeneration rows remain byte-exact. Entry-contract/damaged-target
  and rooted archive/SQLite controls remain passing.
- Final `mage check` passed: Go race suite, 14 Python evidence checks, generated
  adapter overlap gate, vet, lint (zero issues), and build. Verification boundary
  `0aa8ed191d9498e1` reported 13,523 attempts, **forbidden=0**, KTX_DB unset.
  The first full run found one CLI fake still returning `sha256:image`; replacing
  that invalid placeholder with a full fake ID fixed it before the passing run.
- `git diff --check` passed. Prior uncommitted work, archived formats, golden
  evidence and tracked historical bytes were preserved.

No real Docker daemon, provider or paid calls; offline fakes establish command
selection, not runtime or image provenance. No new dependencies, framework,
schema, recording-resource decision, performance claim or broader threat work.
Degraded state summaries still require exact gets; profile remains absent and
decision satisfaction user-owned. No implementation blocker remains in this
scope; lifecycle completion and commits stay with their assigned owner.

## Findings 6 and A1 — managed schema lease rechecks

Owner: `xyhknqphfs`, plan `hljjrlykxb`; implementation evidence dated 2026-09-06.
Canonical progress: `vkfixtazox` (typed CLI append, draft lint passed).
Task remains `in_progress`; evaluation, completion, audit, satisfaction and final
source/artifact-separated commits remain with their assigned owners.

- **6:** `DB.withWriteTx` now checks `user_version` inside the exclusive
  coordination lease and every retried SQLite transaction, before invoking any
  evidence callback. Newer versions return `SchemaTooNewError`. Hook windows use
  the same context-aware version reader and retain their current-schema check.
  Migration still calls the raw transaction helper. Ordinary schema-15
  partial-state writes remain supported; rejecting every non-current version
  failed the existing partial-readiness regression and was corrected.
- **A1:** `OpenQuery` reads the actual version after readiness reopens read-only,
  under the new shared lease, instead of assigning the expected version. It
  rejects incompatible versions and closes the failed handle; successful typed
  queries retain their shared lease and read-only capability.

Before/after proof in `internal/db/managed_schema_linux_test.go` and
`internal/cli/run_schema_test.go`:

- A stale `InsertSession` queues behind an exclusive lease; the test-owned
  upgrader publishes 17 while holding that lease. Before repair it returned nil;
  after repair it returns the typed newer error and commits no session.
- A separate process acquires writer admission while schema-14 readiness owns
  its exclusive lease. It verifies readiness published 16, then publishes the
  test-only 17 before the query can acquire its fresh shared lease. Before repair
  `OpenQuery` returned a handle with nil error; after repair it rejects. Existing
  coordinator admission plus a test-only SQLite connection rendezvous establish
  order, not sleeps. No production test hooks were added. A same-process gate
  cannot prove this ordering because Linux fcntl admission is process-owned.
- Twenty ordinary persistence methods plus the hook window reject before
  callback mutation; `total_changes()` remains unchanged. Categories include
  sessions/rollups, source retries, artifacts/retries, dispositions, benchmark
  headlines, recordings, telemetry, collections/ingress, runtime-source and
  recovery observations, and runtime event creation/append/seal.
- The actual long-lived wrapper parser forwards output after upgrade but reports
  the persistence warning, with its committed source count and rollup staying
  at one. Before repair it persisted another source. Compatible query leases,
  read-only mutation rejection, context deadlines, memory handles and explicit
  caller-owned raw handles have regression controls.

Checks: focused race command and exact scope are retained in progress
`vkfixtazox`; both packages passed (DB 18.314 s, CLI 3.753 s). Final `mage check`
passed the Go race suite, 14 Python checks, generated-adapter overlap gate, vet,
zero-issue lint and build. Its verification boundary `c5117959571507c0` recorded
13,608 accesses and **forbidden=0**, with KTX_DB unset. The earlier full checks
caught the partial-readiness restriction and an interface-format lint issue;
both were fixed before the final pass. `git diff --check` passed.

`mage build` passed before local `build/ktx` offline smoke: schema-14 fixture
`db report` automatically prepared 16; repeated report preserved database bytes
and sidecar inventory; explicit migration independently prepared 14→16. A
test-owned raw bump to 17 returned exit 5 / `schema-incompatible`, actual 17 /
maximum 16, without changing database bytes or version. Every invocation used
fresh HOME/XDG roots and unset KTX_DB; all databases were disposable seeded
fixtures. No ambient database, provider, Docker or production-scale gate ran.

No schema/version change, new migration, release authority or stronger future
schema claim is introduced. Prior filename-URI/pool cleanup and rooted private
archive inspection remain intact. Historical reproduction above is not replaced
by these additive simulations. State summaries remain degraded, exact get works,
and profile remains absent. No implementation blocker remains in this scope.

## Findings 13, S1 and A7 — reporting authority crash recovery

2026-09-07, owner `lscowhzqtf`, plan `hljjrlykxb`. Current disposition for these
three entries only: **repaired in the candidate and verified; coordinator
evaluation/completion remains pending**. Original inventory and other owners'
history remain unchanged. Canonical recovery progress: `pyfgddgpzr`, appended
through the typed CLI after checks. OpenCode interrupted the earlier implementation;
its checks remain **unknown**, not failed or passed. Exact progress
`bujtshsfpn` concerns the preceding cache task and is not evidence for this task.
The recovered two-file production diff was reviewed and retained unchanged;
`internal/dbreport/authority_test.go` was strengthened rather than replaced.

- **13:** one SQL expression admits only measured, runtime-authoritative,
  nonnegative integer milliseconds for coverage, distributions and tool/agent
  means. Unknown numeric history remains stored but excluded. Measured zero
  remains a measurement; unknown-only groups retain null distributions/means.
  Tests cover wrong state/unit/authority, missing, negative, real (including
  integral real), text/blob and non-finite values. SQLite stores NaN as NULL.
- **S1:** the getter scans the stored outcome without numeric coercion and
  recognizes only integer 0/1. NULL, integer 2, real, text and blob remain
  unknown without losing other record fields or failing the whole read.
  Report failure, coverage and grouped counts agree; exit code stays diagnostic.
- **A7:** the integrity loop checks `Rows.Err()` before returning counts.
  A SQLite view produces one successfully scanned row with a valid hash, then
  raises an iteration error. Both extraction and full report generation reject
  partial success. The built CLI returns exit 4 with no report on stdout.

New before/after evidence, not reconstructed interrupted-run history:

- The three recovered tests first passed unchanged. After expansion, four
  regression tests pass, including unknown-versus-zero subcases and byte-exact
  database preservation. Existing report-schema/key golden checks also pass.
- A disposable Go overlay replaced only the two production files with their
  pre-repair blobs: `records.go` = `491942e22f5a2e928d33776f74948a257ccf4f0a`,
  `report.go` = `1e053d11706dba8c96416c46f1525907cda7ea0e`. All four new tests
  failed as expected: 15/17 latency values counted measured with NaN/Inf in the
  distribution; invalid outcomes were coerced or caused scan errors; integrity
  returned one valid row and nil error despite failing on the second row.
  No checkout reset/revert or other production overlay was used.
- Targeted race passed:
  `go test -race ./internal/dbreport ./internal/db -run 'TestReport|TestEmptyReport|TestStoredOutcome|TestIntegrityIteration|TestLatencyEvidence|TestToolCall|TestGetToolCall' -count=1 -timeout=120s`.
- `mage build` passed before CLI use. Twelve independently seeded current-schema
  CLI checks passed: one report, ten typed gets, and one mid-iteration failure.
  The report counted 2/10 measured latencies with a 10 ms mean and outcomes of
  eight unknown, one success, one failure. Every invocation preserved database
  bytes and created no WAL/SHM files.
- The first new `mage check` failed because Go discovered abandoned compiler
  packages under ignored `build/tmp/report-check.m94NRl`. A local ignored
  `go.mod` at that scratch root excludes only that generated tree from parent
  package discovery, preserving its contents; no product/Mage behavior changed.
  The rerun passed Go race tests, 14 Python tests, generated-adapter overlap,
  vet, lint (zero issues), and build. Boundary `e3aa33e092f1d747` recorded
  13,813 attempts, **forbidden=0**, `ktx_db=unset`. `git diff --check` and the
  new test file's formatting check passed.

All verification used fresh HOME/XDG/TMP roots, unset inherited KTX_DB and
explicit disposable fixture owners, with Go dependency fetching disabled.
No ambient database, provider/paid run, daemon, schema/backfill, price policy,
public JSON key change, new cap, or framework was introduced. No implementation
blocker remains in this scope. State lists remain degraded, exact gets work,
profile is absent and satisfaction is user-owned. Task status stays
`in_progress`; audit/evaluation/completion, staging and commits were not performed.

## Baseline ledger verification and handoff

Docs-only verification checks all 48 row identities, evidence-class totals
(4 R / 30 SC / 4 SD / 10 AR), source path/line bounds, and ownership against the
36 exact task IDs/names and ordinal order. The ledger's global reconciliation
owner is `bqnxtbzjou`; verification entrypoint, native-platform and fresh-coverage
owners are tasks 2, 34 and 35 respectively. They do not imply those gates ran.

Recorded checks for this documentation cycle: a read-only Python structural check
against exact plan retrieval passed for 48 unique findings, the stated evidence
totals, all 36 ordinal/ID/name matches, 34 finding owners plus ledger and final
reconciliation owners, and 111 unique repository path:line references existing
and within file bounds. Exact retrieval still reports this task `in_progress`.
`git diff --check` and tracked-diff checks for REVIEW.md and plan entities passed.
The checker was disposable (`/tmp/opencode/check-review-ledger.py`); these results
do not depend on retaining it. Line-bound checks do not re-prove source semantics.

Behavioral verification: **N/A: docs-only**. No build, test suite, database,
provider call, runtime reproduction, coverage run or native-platform gate is
claimed. Metrics and reproduction outputs above remain prior-review evidence.
No acceptance restriction, schema change, recording decision, boundary extraction
or new eviction policy is selected here.

Startup caveats: profile unavailable; list summaries degraded/missing names or
records (including stale pending summaries for this in-progress task); exact get
works. Startup also reports malformed/missing verification recipe metadata and
incomplete changelog headings; neither is repaired by this coverage task. Decision
satisfaction remains user-owned. The coordinator owns task evaluation/completion
and later synchronization; untracked plan records and historical REVIEW remain
untouched. Future dispositions must add actual repair or approved no-change
evidence before closing any inventory entry.

## Scoped archive materialization evidence

2026-09-07, owner `czzdcnqozn`; PERF4/S7 archive portions only. See
[resource, equivalence, and verification evidence](archive-materialization.md).
Canonical progress: `kfawtcedqh`.
The visitor retains unique attempts and output-required divergences, not every
duplicate ledger projection. Existing on-read record limits and accepted input
contracts remain unchanged; no count/file-length cap was added. For 10,000
unchanged duplicate records, allocated bytes fall 169,257,680 → 102,870,920 with
identical summary bytes; cumulative malloc count remains essentially unchanged.
Required attempt, rich model-step, call and divergence metadata remains
proportional, not constant RAM. The prior statement that archive materialization
was untouched is historical; this section supplies that owner's evidence without
changing S7's global status or the preflight/screen owner's work.

All five current-producer rows and latest local-built public consumers pass;
entry/target-credit, rooted-read/private SQLite-copy and actual-execution
identity controls remain. Focused race and final full `mage check` pass (0 lint
issues; 13,911 verification attempts; forbidden=0). No schema, paid/provider or
daemon run, ambient DB, staging, commit, completion/evaluation/audit or
satisfaction mutation. Lifecycle disposition remains with the assigned owner.

## Required telemetry serialization correction — ogjqsbptji

2026-09-07; canonical progress `zywgykpyqd`. Bounded follow-up to implementation
progress `phveyhldcx` and accepted audit `telemetry-ack-audit-1` only. The task
remains `in_progress`; its existing attempt count 1, failure count 1 and failed
verdict are preserved. No new evaluation or completion is recorded here.

The prior required callback serialized raw JavaScript measurements with
`JSON.stringify`, turning NaN, infinities and overflow into null. Required
collection still failed closed, but persisted **unknown** instead of
**malformed**, unlike Go and optional collection. A measurement-only snapshot
helper now encodes nonfinite numbers using existing malformed string forms.
It covers cost, tokens, input/output/reasoning, cache, and cache read/write.
True null remains unknown. Finite numbers and numeric strings retain their
existing states; unrelated metadata keeps ordinary JSON serialization.
No schema, new public field, Go-normalizer change or framework was needed.

Shared `durable_numbers.json` fixtures expand nine values across eight paths:
NaN, positive/negative infinity, positive/negative overflow, null, large finite,
safe integer and numeric string. Go's JSON-only boundary uses the existing
malformed string form for NaN; Bun constructs actual nonfinite JavaScript
objects. Each collector suite executes 160 policy cases: the original 16 plus
144 new cases. Before repair, all 40 nonfinite required cases reproduced the
persisted-state mismatch; Go, optional and control cases passed. After repair,
both suites pass through the freshly built local CLI and real generated adapter.
An additional real Bun/local-CLI test verifies snapshot-before-queue ownership,
no runtime-object mutation, unchanged unrelated/aliased metadata, first capture
time and exact retry payload retention, null-versus-malformed retry conflicts,
one persisted row and required fail-closed behavior.

Verification used a fresh HOME/XDG/runtime/TMP root per invocation, unset
`KTX_DB`, explicitly owned offline fixture databases and `GOPROXY=off`.
`KTX_ADAPTER_CLI=~/git/ktx/build/ktx` enabled real adapter checks.
Commands (each under that isolated environment):

```sh
mage build
# Before repair: expected 40 required-case failures. After repair: pass.
go test ./internal/cli -run 'TestTelemetry(DurableSharedConformance|AdapterLocalConformance)$' -count=1
# After repair: pass, including all 14 original ACK/redelivery scenarios.
go test ./internal/cli -run 'TestTelemetry(DurableSharedConformance|AdapterLocalConformance|AdapterLocalNonfiniteSnapshot|AdapterLocalDurableRedelivery)$' -count=1
go test -race ./internal/telemetry/... ./internal/cli ./internal/db -run 'Test(Telemetry.*|WrapperTelemetry.*|Collector.*|ContractFixtures|SessionTelemetry.*|RuntimeEvent.*|RequiredCollector.*|OpencodeGeneratedAdapterEventAndToolIntegration)$' -count=1
mage check
git diff --check
```

Focused race checks passed. Full `mage check` passed Go tests, 14 Python tests,
vet, required generated-adapter overlap, lint (0 issues) and build. Boundary
`7d96f91b58aad4c1` recorded 20,101 database attempts, **forbidden=0**,
`ktx_db=unset`. Earlier full-check invocations hit the 120-second tool timeout
and then a test-only gofumpt issue; the formatting correction preceded the full
passing run. Those verification events do not alter the task's audit history.

Strict ACK validation, first-evidence authority, durable retry/concurrency and
failure policies remain unchanged. No ambient DB, provider call, staging,
commit, evaluation, audit, satisfaction or completion mutation. S10 memory
growth and crash/restart retention remain unchanged for the next assessment;
this correction adds no eviction or other boundary implementation.

## Scoped Linux platform qualification

2026-09-07, owner `zcvdpwllcs` in plan `hljjrlykxb`, current task name **Verify
supported Linux platform**. D9 disposition is scoped native evidence plus the
user-approved Linux-only promise narrowing (`ygcysqxcfr`), with terminal-only
capability authority from `llshvynohy`. The original inventory row and historical
BSD/Windows implementation and compilation facts above remain unchanged. This
is not an audit, evaluation, decision-satisfaction or lifecycle-completion entry.

The [native qualification report](../../docs/native-linux-qualification.md)
records the support/source-family inventory, exact host (Arch Linux, amd64,
`7.2.2-1-cachyos`), commands, assertions, failures/correction, and evidence limits.
Current README, agent guide, ktx skill and benchmark reference, database-safety
and critical-path runbooks now distinguish Linux support from retained non-Linux
implementation. Existing wrapper documentation already states the approved
procfs/subreaper/pidfd/waitid capability floor and pre-spawn failure scope.

Fresh `-race -count=1` selections passed database writer preference/admission,
aliases, read-only/current-schema behavior, compatible migration, reopened leases,
and newer-schema rejection; process signal/TTY/descendant/caller preservation,
output failure, owned fake-container cleanup; and local generated-adapter durable
ACK/conformance with Bun 1.4.0. Actual `build/ktx` runs passed terminal lifecycle,
benchmark compare/rank cancellation, shared Codex hooks, broken stdout and
synthetic migration/newer-schema probes. Injected pidfd/waitid/prctl denials
rejected before spawn; non-terminal pidfd denial remained successful. Procfs
unavailability itself was not fault-injected; the preparation check was inspected.

The opt-in benchmark signal test initially failed all four cases before fake
Docker execution because `sha256:offline` no longer passed image preflight.
One test-only synthetic identity replacement restored the intended boundary;
all four cases then passed. No production or non-Linux source was removed or
changed, and no schema change or new platform gate was introduced.

Final `mage check` passed tests, 14 offline Python checks, required generated
adapter overlap, vet, lint (0 issues), and build; boundary `cfdd309eee261080`
recorded **17,949 operations, forbidden=0, KTX_DB unset**. A subsequent fresh
`mage build` and local process/probe rerun passed. After report/link edits,
`go test -count=1 ./internal/cli -run 'Test.*(Documentation|Docs|Skill|Contract)'
-timeout=120s` and `git diff --check` passed. Each invocation used fresh disposable
HOME/XDG/runtime/TMP roots; fixtures alone selected explicitly owned databases.
No provider, real daemon/container, ambient database or real archive was accessed.

No required Linux hardblock remains in this scoped qualification. There is no
new native proof for other architectures, older kernels, WSL, containers or
non-Linux systems, and no `mage verify`, hosted CI or schema-release authority.
The existing pre-cleanup coverage baseline stays point-in-time;
`bqnxtbzjou` retains final coverage refresh and overall synchronization ownership.
Task status remains in progress for its assigned owner. Agentera build prime
was blocked by isolated-HOME skill-path drift and unsafe TODO summary, while
`state plan get --id hljjrlykxb` returned the exact task; no unrelated app,
profile or TODO reconciliation repair was attempted.
