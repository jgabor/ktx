# Experiment entry contracts and regeneration matrix

## Current cutover state — 2026-09-07

Owner: `ubegqqsptl`, plan `hljjrlykxb`; authority: exact firm decision
`wajoflwpoe` and the user's selected cutover constraints. Implementation and
verification evidence only: no task evaluation, completion, audit, or decision
satisfaction was performed. The sections below **Historical baseline** retain
the pre-cutover inventory and are not current support promises.
Canonical progress: `cboljpsdor` (typed CLI append; task remains in_progress).

### Current producer/consumer matrix

| Producer/control | Methodology | Manifest | Attempt | Summary | Result |
| --- | --- | --- | --- | --- | --- |
| Registered exact-match, unknown rates/no cache telemetry | v8-exact-match | 1 | 3 | 8 | Pass |
| Registered exact-match, unknown rates/cache telemetry | v8-exact-match | 1 | 3 | 8 | Pass |
| Registered exact-match, available prices/cache telemetry | v8-exact-match | 1 | 3 | 8 | Pass |
| Registered prompt-cache-token-hit-rate | v6-prompt-cache-token-hit-rate | 1 | 3 | 5 | Pass |
| Registered recovery-aware-comparison | v7-recovery-aware-comparison | 1 | 3 | 7 | Pass |
| Inline-task exact-match, embedded scoring/no task-file snapshot | v8-exact-match | 1 | 3 | 8 | Pass |

`TestEntryProducerRegenerationMatrix` now checks registry coverage, not just a
fixed unexplained list. It uses the real `Runner.Compare` writers with the
offline fake executor, generated stream/collector evidence, and fresh explicit
owner databases. Every row retains its produced summary bytes through two
write/validate cycles with `valid=true` and `target_met=true`. Replacing copied
attempt metrics and setting session `tool_call_count=999` leaves summary bytes
unchanged and reports projection divergences. This proves retained-source
authority, not provider authenticity or live runtime behavior.

The inline control now runs by default instead of skipping when its optional
fixture root is unset. Public local-built list/show/summarize/validate pass for
all six controls, with fresh HOME/XDG/TMP and unset KTX_DB for every CLI call.
Rank still uses the same Compare pair producer; ranking/consumer tests pass.
The embedded spec consumer used by bench run/report now observes the exact-match
revision; its source-independent load test passes. No persistent SQLite schema
or scoring rule changed.

### Conversion preceded retirement

- Before edits, local `mage build` and the existing five-row matrix passed:
  active exact-match emitted attempt 1 and summaries 1–3. The inline test skipped
  because its opt-in root was absent; that skip was not counted as coverage.
- Exact-match was converted to the existing schema-3 stream/database/closed-owner
  materializer and a fixed summary-8 shape before reader removal. The five rows
  plus the now-enabled inline control passed before retirement. The matrix was
  rerun after each reader-removal group before the next group.
- Removed attempt-1/2 entry interpretation, legacy stream-projection fallback,
  methodology v1/v4/v4-eval-matrix/v5-cache-aware entry support, summary-1–4/6
  decoding and obsolete formatting dispatch, and old collector classification/
  contradiction-column fallbacks. Unsupported methodology definitions are also
  rejected at producer preparation rather than producing unusable archives.
- Pricing regeneration no longer inspects copied attempt estimates or the old
  summary to decide authority. Retained metadata/hash/embedded-model checks and
  parser/cost semantics remain; missing pricing cannot invent estimates, and
  cache-aware validation still requires pricing. Current retained sources can
  reconstruct a corrupt derived summary without interpreting its old format.
- Removed `tools/bench/evidence/opencode-runtime-cache-controls/probe.sh`, the
  remaining inactive executable with a `.bench-data/cache-evidence` default.
  The public retired command and direct-probe package were already absent.
  Probe configuration/results remain static history. No user/ignored archive
  was deleted, and no storage alias or retired command was restored.
- Shared cache arithmetic remains because current prompt-cache production uses
  it; these in-memory calculations are not legacy archive readers. Permissive
  manifest list/show and raw typed decoding do not bypass archive-entry checks.

### Checks and history preservation

- Final local-built entry tests pass: five registered rows, inline producer,
  36 entry mutations (including retired methodologies/attempt schemas), nine
  unsupported summary versions, nine damaged-current-evidence cases, and the
  required prompt-cache contract control. Rejected entries preserve summary
  bytes; metadata remains inspectable.
- Focused `go test -race ./internal/experiment ./internal/cache/reuse/sequence
  -count=1` passed, including local CLI checks. Full `mage check` passed after
  correcting the embedded-spec test's old version assertion and one test-only
  gofumpt blank-line finding. Go race tests, 14 Python tests, generated-adapter
  overlap, vet, lint, and build passed; boundary reported 17,147 operations,
  `forbidden=0`, `ktx_db=unset`.
- The opt-in materialization resource controls now compare against newly
  produced current-source baselines rather than rewriting historical hashes.
  Existing limits passed: 16 MiB stream allocated 18,117,400 bytes (<24 MiB);
  10,000 duplicate current envelopes allocated 107,288,464 bytes (<128 MiB).
- Before/after SHA-256 inventory: 106 tracked files across all `docs/` except
  the edited current guide `docs/pipelines.md`, `tools/bench/evidence/` except
  the retired executable, and all tracked `testdata/`. Zero changed historical
  files, including all summary/ranking goldens. Canonical JSON inventory digest
  (sorted path-to-file-SHA map, compact JSON) was identical before and after:
  `36de0101fab23b161900d2c19041e9f603f80d16bba5613da307c650f2c710af`.
- All verification used fresh disposable HOME/XDG/TMP roots, unset KTX_DB, and
  generated explicit owner databases. No provider, daemon, live archive,
  production-scale schema gate, Git staging, or commit was used. `git diff
  --check` passed. Existing unrelated dirty work and state/crash scratch remain.

No implementation hardblock remains. State list/startup summaries remain degraded
and profile grounding absent; exact task/decision retrieval worked. Lifecycle
completion and decision satisfaction remain with their assigned owners.

## Historical baseline — pre-cutover evidence only

Date: 2026-09-05. Owner: `sgqaoojspr`, plan `hljjrlykxb`. This is the
producer/consumer gate for later retirement task `ubegqqsptl`, not retirement
approval, task evaluation, or user-confirmed decision satisfaction.

## Boundary and repair

`RegenerateSummary` (also `WriteSummary`) and `ValidateExperiment` now share
`loadExperimentEvidence` in `internal/experiment/entry_contract.go`. Before
interpretation they require:

- Manifest schema 1, an experiment ID, an explicitly supported methodology,
  a positive target, and task expected output or nonempty expected facts with
  nonnegative minimum tool calls. Missing/null numeric versions decode to zero
  and fail; there is no default methodology inference.
- For the registered-input contract (nonempty methodology ID), a retained YAML
  spec and TOML input for each selected pipeline. Prompt-cache methodology also
  requires its JSON `cache-reuse` contract. Each supplied input still undergoes
  the existing metadata, duplicate, archive-relative path and hash checks.
- A nonempty attempt ledger with explicit supported attempt schemas 1, 2 or 3,
  attempt IDs, and baseline/candidate sides. Missing files, empty ledgers, null
  records and unknown/missing versions cannot become empty successful evidence.

The existing attempt artifact-presence/hash checks now run before aggregation
in both entry paths. An attempt with missing or hash-mismatched declared
artifacts cannot contribute to evaluated or valid counts. Partial but readable
evidence can still produce a diagnostic summary; a missing arm cannot meet the
positive per-arm target. Entry-contract rejection leaves existing summary
bytes unchanged, and does not create a summary in a manifest-only archive.

`bench list` and `bench show` retain their intentionally permissive discovery
loader in `internal/cli/bench_lifecycle.go`. Their success means inspection,
not evidence validity. Raw typed aggregation/summary decoding helpers remain
internal consumers, not substitutes for archive-entry validation.

This is a consistency/interpretation contract, not archive authenticity or
confinement. Explicit unregistered compatibility inputs remain supported;
they do not acquire a registered-input inventory merely from a methodology
version. Inline tasks remain accepted without task-file snapshots. The follow-up
archive confinement work (`bqpenkdmho`) uses rooted reads/private SQLite copies.
Execution-input binding (`tqmgzzesya`, 2026-09-06) additionally requires both
retained execution configs to hash to their manifest identities and agree with
any pipeline input copies. New input copies derive from the execution capture;
legitimate older independent copies remain readable without rewriting history.
Synthetic fixtures now retain actual config files. The same five current-producer
rows and explicit compatibility tests below pass, including local-built CLI
regeneration. No archive schema, byte/record/size cap, database schema, or wrapper
lifecycle change is introduced by this follow-up.

## Active producer and consumer inventory

| Surface | Representation / consumer | Evidence |
| --- | --- | --- |
| `bench compare` | Registered selectors → `Runner.Compare`; manifest 1; retained spec, pipeline(s), task file, and prompt-cache contract where applicable | `internal/cli/bench_experiment.go`, `runner.go:createManifest`, `snapshotInputs`, `runAttempt` |
| `bench rank` | Calls the same registered `Runner.Compare` for each pair; stores pair projections and a derived ranking, not a distinct experiment manifest format | `bench_experiment.go:runBenchRank`, `ranking.go:BuildRanking` |
| Inline-task runner | Same manifest/ledger writer, but embeds task scoring without a task-file snapshot | `offline_fixture_test.go:TestSemanticIDOfflineExperimentFixture` |
| Summarize | Retained input checks → ledger/field-authority materialization → pricing → `BuildSummary` → durable summary replacement | `summary.go:RegenerateSummary`; new shared loader |
| Validate | Same evidence entry/materialization, structural summary check, exact regenerated-byte comparison, attempt and methodology checks, positive per-arm targets | `validate.go:ValidateExperiment`; `RequireValidExperiment` wraps it |
| List/show | Manifest discovery/inspection only; incomplete archives remain visible | Public CLI regression controls |
| Ranking/rendering | Consume the generated typed summary; no separate archive reader is introduced | `ranking.go`, `summary_contract.go`, `RenderSummary`; existing ranking/contract tests |

Public compare/rank were not run against a provider or daemon. The matrix uses
the actual runner/writers with the existing fake executor, retained generated
stream evidence, and freshly created explicit owner databases. Task bytes are
a disposable exact-match test task; shipped spec/pipeline/contract bytes are
copied to the disposable source. These are contract controls, not benchmark
performance or live runtime evidence. Rank's producer relationship is
source-confirmed; its existing consumer tests pass in the full suite.

## Historical pre-cutover producer regeneration matrix

All five rows pass `TestEntryProducerRegenerationMatrix`. For each, producer
summary bytes survive two write/validate cycles exactly; validation reports
`valid=true`, `target_met=true`. Local-built public list/show/summarize/validate
also pass for each row, with fresh HOME/XDG roots and unset KTX_DB per command.

| Active spec | Methodology | Attempt schema | Summary schema | Distinguishing input |
| --- | --- | --- | --- | --- |
| exact-match | v1 | 1 | 1 | Snapshot with unknown price rates; no optional cache telemetry |
| exact-match | v1 | 1 | 2 | Unknown price rates; emitted cache telemetry |
| exact-match | v1 | 1 | 3 | Available price rates and estimated cost |
| prompt-cache-token-hit-rate | v6-prompt-cache-token-hit-rate | 3 | 5 | Pinned collector identity; observed cache control and retained cache-reuse contract |
| recovery-aware-comparison | v7-recovery-aware-comparison | 3 | 7 | Pinned collector identity; observed cache control and retained owner/stream authorities |

**Retirement gate:** attempt schema 1, cacheless/cache-bearing aggregation,
schema 1–3 summary readers and legacy stream reconciliation are still used
by active exact-match production. Do not delete them based on their names.

## Historical compatibility present before retirement

Archive materialization follow-up (`czzdcnqozn`, 2026-09-07) reran all five
producer rows and local-built public consumers after streaming ledger
interpretation and combined stream hash/call accounting. Summary bytes and
authority remain unchanged. The loader now retains unique attempts and required
divergences rather than every duplicate projection. Existing record limits,
rich metadata, confinement, config/image binding and compatibility readers are
preserved; [resource and equivalence evidence](archive-materialization.md)
states the remaining proportional metadata bounds. This is not retirement or
lifecycle-completion authority.

The entry allowlist also retains `v4`, `v4-eval-matrix`, and `v5-cache-aware`.
This does not restore a validation guarantee to historical tracked archives.
The following generated fixtures distinguish existing reader coverage from
current public production; they passed without rewriting golden/history bytes:

| Representation | Regeneration / validation authority |
| --- | --- |
| v4 + attempt 1 | `TestLegacyRegenerationPreservesSerializedBytes`: repeatable incomplete diagnostic summary with a positive target |
| v4 + attempt 2 with database/stream | `TestAuthoritativeRegeneration*`, `TestRegenerationUsesRetainedInputsAfterSourceDisappears`: authoritative projections/deduplication preserved; a one-arm fixture is now correctly target-unmet |
| v4-eval-matrix + explicit attempt 1 | Pricing regeneration tests, `TestSummaryV3ValidationRegeneration`, cache stream reconciliation; complete valid controls retain their summaries |
| v5-cache-aware, observed/isolated | `TestValidateV5ObservedAndMalformedEvidence`: valid controls and malformed evidence remain distinguished |
| v5-cache-aware + supported direct-cache runtime | `TestDirectCachedInputShareRegeneratesAndValidates`: summary 4 still regenerates exactly |
| v7 + attempt 2 retained authorities | `TestRecoveryAwareArchiveValidatesAndRegeneratesByteExact`: existing compatibility fixture retains summary 7 regeneration |
| Historical summary 6 under recovery-aware methodology | Existing complete/incomplete schema-six rejection tests remain passing; no new producer or regeneration promise |

Unversioned synthetic fixture entries were updated to declare their intended
schemas/scoring. Tests that formerly used target zero to make one-arm evidence
globally valid now assert valid attempt diagnostics but `target_met=false` and
`valid=false`. A stale-cache test now supplies the correct stream hash rather
than a placeholder. No tracked fixture/report bytes were rewritten.

## Regression and verification evidence

- Before repair, local CLI reproduced manifest-only/no-ledger success with
  `valid=true`, `target_met=true`, zero counts. Version and required-input
  mutation tests also failed before implementation. A further current-producer
  control reproduced `target_met=true` after deleting the exact-match stream.
- After repair: 30 entry-rejection cases preserve list/show and summary bytes;
  one missing prompt-cache contract case rejects both entrypoints; nine
  incomplete-evidence cases (three specs × missing arm, missing stream, altered
  stream hash) cannot meet targets. The literal manifest-only case cannot
  create summary authority. All pass through both API entrypoints and the
  local public CLI.
- `mage build` passed before public CLI invocation. Focused
  `go test -race ./internal/experiment -count=1`, with
  `KTX_ENTRY_CONTRACT_BINARY` pointing to the local build and a fresh
  `KTX_OFFLINE_FIXTURE_ROOT`, passed including the five-row matrix and inline
  producer control. No race diagnostic.
- Full `mage check` passed after correcting one test-only gofumpt finding.
  Go tests, generated-adapter overlap gate, Python checks, vet/lint/build pass;
  the passing verification boundary reported 8,712 audited operations and
  `forbidden=0`, `ktx_db=unset`. No schema-release gate was needed or run.

State-summary limitations remain: exact task reads work; bounded startup/list
summaries omit or mislabel some detail. Profile grounding is absent. Decision
satisfaction is user-owned. Wrapper task `dwndsjjdkx` remains separately blocked
on kernel-capability approval despite its recorded host checks; this task does
not change that status. There is no implementation blocker for this entry
repair; task evaluation and lifecycle completion remain with their owner.
