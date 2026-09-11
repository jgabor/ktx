# Workspace test/evidence baseline

Owner: `maeswzqwit`, plan `hljjrlykxb`. Date: 2026-09-07.

## Disposition and limits

**78.5292% weighted Go statement coverage: 15,932 / 20,288 statements.**
This is a fresh Linux/amd64 point-in-time workspace measurement, not a final
whole-plan claim, a branch/path-coverage claim, or a quota. It includes Mage
and tool packages, uncovered entrypoints, and the test-helper package.
The pending `wxwwonhjgy` small JavaScript accepted-payload cleanup is not
covered by a claim of final source-state verification. `bqnxtbzjou` owns final
source-state synchronization. No lifecycle status, evaluation, satisfaction,
commit, staging, or publication was performed by this worker.

Ledger D10's historical absence of native Go fuzz targets and fresh workspace
coverage is addressed by this retained baseline and two test-only targets.
It does **not** mean every malformed-input boundary is now fuzzed, or that
coverage has become a routine CI measurement. Existing owning-task before/after
regressions remain the primary behavioral proof. No runtime source changed.

## Source identity and denominator

Toolchain: `go1.27.0-X:nodwarf5 linux/amd64`; Bun 1.4.0; Python 3.14.7.
Package selection: `go list -tags=mage -json ./...` in the assigned checkout.
There are **33 selected packages**, 31 with executable statements and two with
no statements. The selected production/test Go input set contains **343 files**.

Fingerprints, checked repeatedly after measurement and unchanged:

- Selected Go files: `3732ea5ddf0c1f43d6877c4c0e80cfd0e093157d4e51e82798f2dca05fa3802d`.
- Existing non-memory Git-listed files, including untracked/nonignored files:
  **528 files**, `123a86f049f64d7173feae396b5d821849d85488a89038f1396e24c5e6ac501d`.
- Passing raw profile SHA-256:
  `4747538f5b32ea6d82affb67c9e246cedb9d78791906fd4f31bb3ac53d3c8b86`.

Fingerprint algorithm: sort unique repository-relative paths, then SHA-256 the
concatenation of each UTF-8 path, NUL, and the raw 32-byte SHA-256 of its bytes.
Selected Go inputs are each package's `GoFiles`, `CgoFiles`, `TestGoFiles`, and
`XTestGoFiles`. The broader set uses
`git ls-files -z --cached --others --exclude-standard`, retaining existing files
and excluding `.agentera/` and `.pyc`. Ignored build/crash scratch stays ignored
and untouched. These fingerprints identify an uncommitted checkout, not a Git
revision; they do not assert an atomic pre/post snapshot of concurrent work.

Coverage uses `-coverpkg=./...` to collect cross-package execution. Merge repeated
profile records by exact file/range, assert identical statement weights, and sum
execution counts. Count each range's statements once, as covered if its summed
count is positive. Do not sum the raw duplicate denominator or average package
percentages. `go tool cover -func` independently reports **78.5%** (rounded).
Dependencies/stdlib, test bodies, Python and JavaScript are outside this Go
statement denominator. Physical test LOC and earlier targeted ~91% measurements
are not substitutes for this result.

| Package relative to module | Covered | Statements | % |
| --- | ---: | ---: | ---: |
| root (Mage) | 263 | 599 | 43.91 |
| cmd/ktx | 0 | 1 | 0.00 |
| internal/benchcontract | 9 | 9 | 100.00 |
| internal/benchregistry | 23 | 24 | 95.83 |
| internal/cache/reuse/sequence | 218 | 234 | 93.16 |
| internal/cachemetric | 7 | 7 | 100.00 |
| internal/canon | 147 | 166 | 88.55 |
| internal/ci | 0 | 0 | N/A |
| internal/cli | 5603 | 6792 | 82.49 |
| internal/cli/testutil | 19 | 21 | 90.48 |
| internal/config | 132 | 147 | 89.80 |
| internal/correlation | 11 | 18 | 61.11 |
| internal/db | 2511 | 3065 | 81.92 |
| internal/dbreport | 319 | 384 | 83.07 |
| internal/durablefile | 20 | 28 | 71.43 |
| internal/evidence | 42 | 48 | 87.50 |
| internal/experiment | 4356 | 4923 | 88.48 |
| internal/identity | 49 | 49 | 100.00 |
| internal/logging | 54 | 70 | 77.14 |
| internal/methodology | 51 | 55 | 92.73 |
| internal/parser | 186 | 199 | 93.47 |
| internal/pipeline | 186 | 193 | 96.37 |
| internal/pipeline/stages | 411 | 420 | 97.86 |
| internal/router | 56 | 57 | 98.25 |
| internal/telemetry | 41 | 51 | 80.39 |
| internal/telemetry/opencode | 158 | 175 | 90.29 |
| internal/usage | 143 | 151 | 94.70 |
| internal/version | 0 | 0 | N/A |
| tools/bench | 7 | 11 | 63.64 |
| tools/bench/external-identity | 167 | 207 | 80.68 |
| tools/componentbench | 328 | 497 | 66.00 |
| tools/loadbench | 264 | 1280 | 20.63 |
| tools/readleasecompare | 151 | 407 | 37.10 |
| **Total** | **15932** | **20288** | **78.53** |

### Exact platform and child-process boundaries

The nine `IgnoredGoFiles` excluded by Linux selection are:

- `internal/cli/init_codex_mutation_other.go`, `run_process_other.go`;
- `internal/db/writer_lock_bsd.go`, `writer_lock_observe_other.go`,
  `writer_lock_other.go`, `writer_lock_windows.go`;
- `internal/experiment/archive_file_other.go`;
- `internal/logging/file_windows.go`;
- `tools/componentbench/filesystem_other.go`.

No non-Linux behavior or native runner is claimed. Separately built local CLI
children are not coverage-instrumented. Re-executed Go test helpers do not pass
`-test.gocoverdir`/`-test.coverprofile` back to the parent; their child counters
are not merged. Shell, Bun and Python child execution is behavioral evidence,
not Go counter coverage. Parent-side orchestration and assertions remain in the
test run. In particular, `cmd/ktx`'s zero is retained rather than replacing it
with inferred coverage from local-binary integration tests.

An initial unfiltered instrumented run failed three tests. The passing coverage
rerun excludes **exactly**:

1. `TestHermeticVerificationEnvironment` (root): a direct `go test` invocation
   does not carry the genuine Mage-created disposable-boundary marker.
2. `TestHermeticTargetsUseSingleBoundary` (root): same distinction for the
   invocation identifier. No marker or consumed capability was fabricated.
3. `TestSessionCachePartialPublication` (`internal/db`): its intentional Linux
   file-size limit makes the re-executed instrumented test helper fail writing
   Go's coverage metadata after its test reports PASS (`file too large`).

All three passed in the unfiltered race-enabled `mage check`. They are not
disabled in source or routine verification; no runtime bug was exposed or
waived. Their production statements remain in the coverage denominator.

## Test inventory and owning-task malformed-input evidence

A lexical inventory of existing nonignored Go test files found **1,057 named
`Test*` functions, 5 `Benchmark*`, 0 `Example*`, and 2 `Fuzz*`** after this change.
These are declarations, not executed-test/subtest counts. There were no native
`Fuzz*` targets before this worker's two additions and no `testing/quick` use
found. Generated/property-style tests must not be relabeled as native fuzzing.

Existing deterministic generation/oracle examples:

- `TestIncrementalJSONRecognitionOracle`: 5 fixed inputs, 15×4×15 composed
  cases and 1,000 seeded generated cases (1,905 total); retains whole-input
  recognition behavior, including malformed UTF-8/JSON and trailers.
- `TestIncrementalLinesMatchSplitOracle`: 8 fixed plus 3,000 seeded generated
  inputs, four fixed operations, byte equality, input immutability and aliasing.
- `TestLazyRoutesMatchRepeatedOracle`: 15 inputs × 3 outcome states × 3 tool
  labels × 6 route suffixes = 810 comparisons, including malformed, duplicate,
  case-varied and large JSON command fields.
- `TestEventIndexMatchesFullScan`: fixed generation/shuffle of 1,728 facts,
  independent full-scan interval oracle and concurrent readers.
- Generated adapter tests and shared durable-number/callback fixtures execute
  fixed Python/Bun/Go contracts. They are not mutation campaigns.

| Owning work | Existing primary proof / malformed boundary | Native-fuzz status |
| --- | --- | --- |
| `xyhknqphfs` managed schema | `internal/db/managed_schema_linux_test.go`: stale/future schema, lease handoff, categories, context, caller-owned exclusion | Deliberately not fuzzed: DB/lease execution |
| `sgqaoojspr`, `ubegqqsptl` entry/retirement | `entry_contract_test.go`: producer regeneration matrix, missing/incomplete evidence, unsupported versions, current cache contract; retained chronology in entry-contract matrix | Full archive entry/loading not fuzzed |
| `bqpenkdmho` confinement | `archive_confinement_linux_test.go`: traversal/symlinks, unsupported types, race, WAL/journals, sockets and database ownership | Filesystem execution not fuzzed; pure archive-name parsing remains a gap |
| `yawngvokny` ingress | `ingress_fields_test.go`: absent/null/nonstring/empty output and native IDs, actual persistence, local CLI, cross-ingress identity | New pure native identity target supplements, not replaces, provenance/ingress tests |
| `atbnjovpeu` config | `TestLoad_UnknownKeys`, route/footer selector tests and hook regressions | TOML/config malformed-input mutation remains a gap |
| `btxcvatnmt` stage validity | `compress_provenance_test.go`, `evidence_validity_test.go`: JSON escaping, explicit false, overflow counts | New JSON recognition oracle is narrower; no provenance-specific target or arbitrary stage execution |
| `momdpkraiq`, remaining argument owner | `run_args_test.go`, `remaining_args_test.go`: argument ownership, help, validation before readiness and local CLI | Pure argument mutation remains a gap; no arbitrary dispatch from fuzz data |
| Other major repaired units | Durable telemetry/parity, source retries, recording/resource lifecycle, adapter mutation, target ownership, report authority, pricing/session cache and allocation/sequence regressions are included in the current suite; their before/after evidence remains in the owning ledger sections and linked reports | No general fuzz claim for these stateful behaviors |

See [the scoped remediation ledger](review-remediation-coverage.md),
[entry/retirement matrix](experiment-entry-contract-matrix.md),
[recording evidence](recording-resource-lifecycle.md),
[archive materialization](archive-materialization.md),
[pipeline allocation](pipeline-allocation.md) and
[sequence evidence](sequence-ordering.md). This worker did not recreate prior
red/green demonstrations or reimplement the already-repaired entrypoints.

### Native targets added and executed

| Target | Fixed seeds | Assertions | Mutation run |
| --- | ---: | --- | --- |
| `internal/pipeline/FuzzJSONRecognitionOracle` | 9 | Recognition shape/faithful bytes equal retained whole-input oracle; input unchanged. The oracle shares the low-level compact helper, so it is not an independent oracle for every JSON primitive. | PASS; 37,990 total executions, 145 new interesting inputs |
| `internal/identity/FuzzDeriveNativeIdentity` | 7 | Independent UTF-8/nonempty predicate, canonical output, determinism, ASCII-boundary-whitespace invariance | PASS; 12,629 total executions, 41 new interesting inputs |

Each campaign used `-fuzztime=5s -parallel=1 -timeout=60s`, outer timeout 120s,
and `GOMAXPROCS=2`. Go reported ~6 seconds including shutdown per campaign.
Execution counts include seeds/repeats; they are not distinct-input or
mutant-only counts. Both logs explicitly reached “now fuzzing with 1 workers”.
The combined **50,619 executions** therefore describe genuine bounded mutation
runs, not merely seed-only checks. No failure corpus was produced.

Both targets cap only test-generated input at 4,096 bytes (sum of three fields
for identity); larger test inputs skip without restricting product acceptance.
They perform only in-memory recognition/hashing: no filesystem, CLI dispatch,
provider, arbitrary command, registry or stage execution from fuzz data. All
**16 fixed seeds** also passed focused checks, ordinary coverage, and the
race-enabled `mage check` seed path. Routine checks do not mutate fuzz inputs.
Large-input/resource behavior remains the existing fixed-fixture regressions'
responsibility; these small campaigns cannot prove exhaustive safety.

## Commands, isolation and results

Every verification invocation used a newly allocated `/tmp/opencode` root,
unset `KTX_DB`, fresh HOME, XDG_DATA_HOME, XDG_CONFIG_HOME, XDG_CACHE_HOME,
XDG_STATE_HOME, and fresh TMPDIR/TMP/TEMP. Go build/module caches alone were
explicitly external reusable tool caches; application state was never reused.
`GOPROXY=off`, `GOTOOLCHAIN=local`, `GOMAXPROCS=2`, and
`PYTHONDONTWRITEBYTECODE=1` were set for checks. Test-owned offline temporary
databases were the only DB scope. No provider, daemon, live archive or ambient
database was invoked. Npm access was limited to the requested Agentera CLI.

Each command below ran through that disposable wrapper; profile/package-list
outputs and analysis helpers stayed under `/tmp/opencode`, not product paths.

```sh
timeout 180s go test ./internal/identity ./internal/pipeline \
  -run '^(Fuzz|TestIncrementalJSONRecognitionOracle)' -count=1 -timeout=90s
timeout 120s go test ./internal/pipeline -run '^$' \
  -fuzz '^FuzzJSONRecognitionOracle$' -fuzztime=5s -parallel=1 -timeout=60s
timeout 120s go test ./internal/identity -run '^$' \
  -fuzz '^FuzzDeriveNativeIdentity$' -fuzztime=5s -parallel=1 -timeout=60s
timeout 900s mage check
timeout 900s go test -tags=mage -coverpkg=./... -covermode=atomic \
  -coverprofile=/tmp/opencode/ktx-workspace.cover -count=1 -p=2 -parallel=2 \
  -timeout=5m \
  -skip='^(TestHermeticVerificationEnvironment|TestHermeticTargetsUseSingleBoundary|TestSessionCachePartialPublication)$' ./...
go tool cover -func=/tmp/opencode/ktx-workspace.cover
```

All displayed commands passed. The preceding coverage attempt used the same
coverage command without `-skip` and failed for the three documented reasons;
its partial profile is not authority. Coverage reran tests with `-count=1`.

`mage check` built `build/ktx` before integration use, ran
`go test -race -tags=mage ./...` (some unchanged packages reused Go's test
cache), passed **14/14 Python verifier tests**, passed the **required Bun
generated-adapter readiness-overlap gate**, and passed vet and lint (**0
issues**). Its genuine hermetic audit was boundary `a5bed8582d69490d`,
**17,937 attempts**, **0 forbidden**, `ktx_db=unset`, external tool caches.
Operation counts: exclusive_coordination 7,440; inspect 2,490; open 1,997;
open_read_only 4,575; resolve 1,435. Auxiliary coverage used real disposable
paths but did not claim a Mage audit receipt. No standalone runtime smoke was
needed for this test-only change; the routine gate exercised the local build.

Remaining limits: **4,356 uncovered Go statements**, especially Mage/tool
orchestration and child-only runtime paths; no native mutation for the full
provenance/config/argument/archive parsers; no non-Linux run, hosted CI, provider
run, or production-scale schema release gate. Python/Bun passing is functional
gate evidence, not measured language coverage. No arbitrary quota or broad
testing framework was introduced.

Agentera exact plan/decision retrieval works; startup reported blocked
`unsafe_active` TODO reconciliation and a missing shared-skill path under the
fresh HOME, rather than clean summaries. The supplied absent-profile caveat
remains. No unrelated state repair was attempted. There is no newly exposed
runtime-correctness blocker; final plan verification and the pending cleanup
remain outside this task's authority.
