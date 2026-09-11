# Final reconciliation — blocked, not closed

Owner `bqnxtbzjou`, plan `hljjrlykxb`, 2026-09-07. This is a current verification
addendum, not an evaluation, decision-satisfaction review, or completion receipt.
The original 48-finding baseline and each dated owner report remain history.
This addendum supersedes their pending-owner wording only as a current status
inventory; it does not rewrite their original observations.

## Concrete acceptance blocker

The zero-shipped-project-marker requirement conflicts with immutable historical
evidence already in the tree:

- `tools/bench/evidence/targeted-residual-evidence-matrix.json`, lines 23, 30,
  35 and 40, contains `.agentera/verification/` provenance paths.
- `internal/pipeline/evidence_integrity_test.go`, lines 36–46 and 74, names those
  project-memory reports and checks exact source paths and the hash chain.
- The historical JSON is byte-equal to the pre-cutover baseline. Removing its
  references would rewrite historical evidence; changing only the test would
  neither remove the JSON markers nor preserve its current integrity contract.

No exception, historical rewrite, test deletion, obfuscation of marker strings,
or relocation was performed. The coordinator/user must resolve the boundary:
explicitly exempt immutable historical provenance from the zero-marker condition,
or authorize a separately specified relocation preserving the original bytes
and integrity verification. This worker cannot silently choose either.
Unicode surrogate comments (`U+D800`) are false positives, not project markers.
The docs Markdown scan found no matching project markers. This is not a claim
that the unfinished whole-tree marker audit passed.

## Fresh gates and exact invocation boundary

Every command below was prefixed by
`bash /tmp/opencode/platform-safe.sh`. The existing wrapper allocates a fresh
HOME, all XDG roots including runtime, TMPDIR/TMP/TEMP, unsets KTX_DB, and sets
KTX_VERIFY_ROOT to the disposable root. Only Go tool/module caches are reused.
Tests create their own offline databases; the locally built binary is used.
No provider, paid call, ambient DB, archive deletion, schema release, staging,
commit, ref change, health publication, evaluation or lifecycle closure occurred.
Public npm/tooling and vulnerability-database network were not disabled.

| Command | Result | Disposable root suffix / evidence |
| --- | --- | --- |
| `mage build` | PASS | `6Gl0Tm` |
| `mage check` | Interrupted by outer 120-second timeout; NOT PASS | `M5dkVZ` |
| `mage check` (600-second outer timeout) | PASS, lint 0, 14 Python checks, required generated adapter | `Cfdk9E`; boundary `1ac7529816c8a1f6`, 17,937 attempts, forbidden=0 |
| `mage verify` (600-second outer timeout) | PASS, including govulncheck: no vulnerabilities found, retained replay | `Iy6PXM`; boundary `1d0eb9f0f5fb3ce0`, 17,954 attempts, forbidden=0 |
| Local-binary race command below | PASS, CLI 132.972s, experiment 70.351s, DB 63.693s | `Cpi6Bs` |
| Coverage command below | PASS with exactly three coverage-only exclusions | `vppOsQ` |
| `python3 /tmp/opencode/final-proof.py` | PASS calculation and repeated selected-source fingerprint | `xuCU7D`, `7Susw9` |
| `git diff --check` | PASS | after state repair |

Full root prefix: `/tmp/opencode/platform-qualification.`; each contains
`stdout.log` and `stderr.log`. Local-binary command:

```sh
env KTX_RUN_SIGNAL_CLI=~/git/ktx/build/ktx \
 KTX_LIFECYCLE_CLI=~/git/ktx/build/ktx \
 KTX_CODEX_CLI=~/git/ktx/build/ktx \
 KTX_ADAPTER_CLI=~/git/ktx/build/ktx \
 KTX_RECORDING_CLI=~/git/ktx/build/ktx \
 KTX_INGRESS_CLI=~/git/ktx/build/ktx \
 KTX_BENCH_OWNER_CLI=~/git/ktx/build/ktx \
 KTX_TEST_RUN_BINARY=~/git/ktx/build/ktx \
 go test -race -count=1 ./internal/cli ./internal/experiment ./internal/db -timeout=300s

go test -tags=mage -coverpkg=./... \
 -coverprofile=/tmp/opencode/ktx-final-workspace.cover \
 -skip '^(TestHermeticVerificationEnvironment|TestHermeticTargetsUseSingleBoundary|TestSessionCachePartialPublication)$' \
 -count=1 -timeout=300s ./...
```

The full native package run includes current producer/inline entry contracts,
confinement, attestation, stdout, terminal/process ownership, generated adapter
durability, payload retention and representative existing resource tests.
It does not enable the opt-in large/gigabyte recording campaign or fault-build
selectors, and does not rerun every prior owner's bespoke measurement driver.
Current producer contracts remain exact v8/3/8, prompt v6/3/5, recovery v7/3/7;
the passing suite is not provider or live-container evidence. Native scope is
the same Linux/amd64 host documented in `docs/native-linux-qualification.md`.

## Current matched coverage, not a replacement historical baseline

Fresh weighted coverage is **15,932 / 20,288 = 78.52917981072555%**. The numerical
result equals the older 78.5292% baseline, but its source identity differs:

- 33 selected packages; **344** selected production/test Go files.
- Selected-source SHA-256:
  `4ac71d4019960cc602aaa91752e2f0073a7c32bee9eebcfe94d159a052340ed0`.
- Fresh raw profile SHA-256:
  `e1d2bc6a52dc754006b86c9c19e7fa140ef4b97ac142f7322f5441c52ae004e8`.

Selection, path/NUL/content-digest fingerprint and weighted block merging use
the algorithm in `workspace-coverage-2026-09-07.md`. The selected-source digest
was repeated unchanged after measurement; no atomic concurrent snapshot is
claimed. This worker changed no Go inputs. JavaScript payload release and child
executables are behavioral evidence, not Go statement coverage.

The exact three exclusions retain the original documented reasons: two require
genuine Mage boundary authority, and the intentional file-size-limit helper
cannot publish instrumented coverage metadata. All three are included in the
fresh unfiltered passing Mage check/verify. Their statements remain counted.
Two native fuzz targets and 16 seeds run in the suite; the prior bounded mutation
campaigns (50,619 combined executions) remain historical, not freshly rerun.
No broad parser/archive/config/argument fuzz-coverage claim is made.

## All 48 findings: current owner-backed dispositions

Canonical exact plan retrieval (`state plan get --id hljjrlykxb`, root `yFh0kX`)
shows **35 other active tasks complete/PASS**, the old cancellation task
`dwndsjjdkx` superseded with **two failed evaluations preserved**, and this task
in progress without evaluation. The evaluation provenance explicitly names the
owner progress records. The original telemetry failed evaluation also remains.
The list below maps every original finding to that current owner authority;
the detailed before/after proof remains in the original ledger sections and
linked reports. It is not 48 newly issued evaluations.

| Finding | Current disposition and substantive evidence | Owner(s) |
| --- | --- | --- |
| 1 | Repaired: literal physical database target, FK/coordination tests | htbcyjluxa |
| 2 | Repaired: required current entry contracts and producer matrix | sgqaoojspr |
| 3 | Repaired: cancellation and owned cleanup failure controls | pzztseunxc |
| 4 | Repaired: collision-resistant owned execution identities | pzztseunxc |
| 5 | Repaired/approved support: oversized draining and terminal descendant lifecycle | qlhcdoeyhr, rmbdxwfguf; superseded dwndsjjdkx history |
| 6 | Repaired: schema recheck under managed leases | xyhknqphfs |
| 7 | Repaired: execution configuration hash binding | tqmgzzesya |
| 8 | Repaired: confined paths/files/SQLite sidecars | bqpenkdmho |
| 9 | Repaired: empty output extraction and durable identity | yawngvokny |
| 10 | Repaired: durable ACK and nonfinite/parity controls | ogjqsbptji |
| 11 | Repaired: generated response-envelope validation | luwgmsjiin |
| 12 | Repaired: unknown configuration rejection before transformation | atbnjovpeu |
| 13 | Repaired: latency authority states | lscowhzqtf |
| 14 | Repaired: immutable preflight image identity binding | tqmgzzesya |
| 15 | Repaired: exact benchmark owner, no latest-session fallback | jgvodxuxul |
| 16 | Repaired: authoritative artifact retries bypass stages | hlzmkuhsht |
| A1 | Repaired: readiness reopen lease recheck | xyhknqphfs |
| A2 | Repaired: durable session-cache publication and lifetime ownership | linrevynph |
| A3 | Repaired: cooperating shared-hook serialization and checked publication | mebgweiffm |
| A4 | Repaired: native call ID distinct from display fallback | yawngvokny |
| A5 | Repaired: JSON provenance escaping | btxcvatnmt |
| A6 | Repaired: instant ordering with append-order ties | itzxdynanp |
| A7 | Repaired: iteration errors propagated | lscowhzqtf |
| A8 | Repaired: argument ownership across wrapper and other commands | kxklkicklv, momdpkraiq |
| PERF1 | Selected recording implementation and resource qualification passed | jfgymxttel, mljcarlewi |
| PERF2 | Repaired: incremental line allocation, byte oracle/budget gates | itocjemrfw |
| PERF3 | Repaired: cold price discovery persisted, failure/reuse controls | hatnrvkhpu |
| PERF4 | Repaired: lazy routing, shared event ordering and streamed materialization | itocjemrfw, idtauctgpl, czzdcnqozn |
| D1 | Approved no split target; historical LOC not executable coverage | iefdatbpxu, maeswzqwit |
| D2 | Approved no domain-record extraction | iefdatbpxu, wxwwonhjgy |
| D3 | Existing owners repair semantic boundaries; no new framework selected | iefdatbpxu and mapped repair owners |
| D4 | Current producer conversion and retired-reader removal verified | sgqaoojspr, ubegqqsptl |
| D5 | Prior shared-hook conformance; do not rebuild common persistence | iefdatbpxu, wxwwonhjgy |
| D6 | Repaired: hermetic verification entrypoints and nonmutating checks | tcrffftvxl |
| D7 | Repaired: all 14 Python verifier tests in fresh check/verify | tcrffftvxl |
| D8 | Repaired: Bun/Python prerequisites; required local adapter gate passes | tcrffftvxl |
| D9 | Linux-only support qualification; no non-Linux native claim | zcvdpwllcs |
| D10 | Fresh matched coverage and limited native fuzz evidence, gaps explicit | maeswzqwit; current measurement here |
| S1 | Repaired: malformed stored outcome remains unknown | lscowhzqtf |
| S2 | Repaired: failed initial readiness closes owned pool | htbcyjluxa |
| S3 | Repaired: null is not boolean success | btxcvatnmt |
| S4 | Repaired: keep-ends count overflow safety | btxcvatnmt |
| S5 | Repaired: exact generated Codex ownership across relocation | mebgweiffm |
| S6 | Repaired: wrapper and other CLI delivery errors preserve committed evidence | qlhcdoeyhr, momdpkraiq |
| S7 | Repaired: approved bounded control responses and streamed archive loading | otiqrxtwkw, czzdcnqozn |
| S8 | Repaired: leader/descendant execution lifecycle | pzztseunxc |
| S9 | Approved no mandatory subprocess abstraction | iefdatbpxu, wxwwonhjgy |
| S10 | Selected optional-ACK payload release; exact retry/assistant/required state retained | ogjqsbptji, iefdatbpxu, wxwwonhjgy |

Counts: **40 repaired/implemented**, **2 qualification/evidence dispositions**
(D9/D10), **5 approved no-change/prior-conformance/mapped-owner dispositions**
(D1/D2/D3/D5/S9), **1 scoped payload-release disposition** (S10) = **48**.
These are owner-backed dispositions, not final closure: **0 final finding
closures issued by this worker**. S10 retains **linear instance-lifetime
indices**, is **not restart-durable**, and is **not constant-memory**. Its paired
string-field measurement (222,098 → 108,506 bytes) is not heap/RSS proof.

## History, schema and unfinished reconciliation

The retained 106-file pre/post-cutover proof remains dated and unchanged. A fresh
comparison of that inventory finds three changed **mutable current guides**:
`docs/critical-path-performance.md`, `docs/database-safety.md`, and
`docs/opencode-telemetry.md`. The other **103 files are byte-equal**, including
historical measurements and goldens; no baseline hashes were rewritten. The old
blanket history checker therefore exits nonzero today; that is not a historical
measurement mutation. Its fresh output is retained under root `heHksB`.
No ignored archive or crash-scratch deletion was performed.

`git diff -- internal/db/schema.go internal/db/migrations` is empty. Existing
schema tests and readiness checks pass; no persistent schema change was made
by this worker. Production-scale schema-release qualification was not run or
claimed. Further final whole-diff schema audit remains part of unfinished
reconciliation rather than inferred from this limited diff command alone.

The canonical TODO repair preview diagnosed exactly two stale projections
(`lejmjmiqzt`, `tafkrzkqbh`), with no public Markdown change. After inspecting
`state todo explain --verb repair`, the exact effect
`0110f4acd873545116b1bfc900578154d2b4a774d36f80157f287866a6b4f1f3`
was applied successfully (transaction `e49684e6962aa748ec2e0e0d`). This synchronizes
existing public-owner text only; it does not resolve either TODO or approve
decision satisfaction. Fresh-HOME shared-skill drift remains an orientation
caveat; exact plan retrieval works without repairing app/profile state.

**Not finished because of the hard blocker:** canonical TODO resolutions and
remaining editorial reconciliation (argument handling/shared hooks/recording
still carry stale pending prose), exhaustive progress-reference existence/content
audit, full marker audit, final schema-diff review and remaining targeted-gate
inventory review. Do not claim all acceptance criteria passed. Existing unrelated
architectural questions stay open; `lejmjmiqzt` satisfaction remains user-owned.
The coordinator retains final audit, task/plan completion, evaluations, health,
archival, satisfaction approval and commits.
