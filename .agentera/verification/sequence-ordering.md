# Operation-local sequence ordering evidence

2026-09-07. Scope: plan `hljjrlykxb`, task `idtauctgpl`, PERF4 sequence
portion only. Implementation evidence, not task evaluation, lifecycle completion,
audit, or decision satisfaction. The task remains in progress.
Canonical progress: `wprbkdxlta` (typed CLI append).

## Change and equivalence

`sequence.EventIndex` privately copies and sorts the complete event slice once.
Each assessment binary-searches its lower ordinal, visits the relevant events,
and stops at the exclusive upper ordinal. `secondaryEstimateAttempt` reuses one
index across its pairs. `attemptTemporalConfounds` reuses one across its attempt
prefix and adjacent-pair checks. Existing one-shot functions remain available.
No global cache, mutable shared state, dependency, schema, or input cap was added.

The exact existing `sort.Slice` comparator and full input are retained: ordinal,
boundary, then session. Fully tied entries still undergo the same unstable full
sort, not a separately sorted subset or a newly stable sort. Pair boundary scans
remain `(prior,current)`; attempt boundary scans remain `[coverage-start,last)`.
Coverage declarations still cover the closed endpoint interval. Coverage tuple,
session, model-step order, missing/unknown, gap and contradictory-provenance rules
are unchanged, as are result maps, estimates, reason precedence and methodology.
This says nothing about provider-internal cache behavior.

`TestEventIndexMatchesFullScan` compares reused concurrent assessments against a
test-only copy of the original full copy/sort/filter boundary traversal. Its
fixed shuffled input includes all boundary classes, unknown classes, all three
session states, exact sort-key ties with differing provenance, missing and
mismatched provenance, events outside each window and at its endpoints. It
checks pair windows with complete, missing and partial coverage and attempt
prefix windows, input immutability, and snapshot ownership after caller reuse.

`TestManyPairSequenceResultBytes` exercises both actual callers concurrently,
checks 255 eligible pairs and no temporal confounds, and preserves the pre-edit
secondary row JSON SHA-256:
`e00b2fa98b2f11902c68dcf275829cc49fa4c5f7b5c83c61605eadcc23560363`.
The retained sequence's facts, events and coverage remain byte-identical.
Existing sequence/estimate, gap, current-methodology, rich metadata, legacy/golden
and exact-regeneration tests also pass; no historical fixture was rewritten.

## Fixed actual-caller benchmark

Native Linux/amd64, AMD Ryzen 7 9800X3D; Go toolchain used by this checkout.
`BenchmarkManyPairSequenceCallers` constructs 256 model steps and 2,826 shuffled
events with seed 73 before timing. Every iteration invokes the complete actual
caller, including index construction, result allocation and eligibility checks.
The same test and benchmark were added and run before production edits.

Three runs, `-benchtime=1s -count=3 -benchmem`; these are elapsed ns/op and
cumulative allocated bytes/count, not CPU-profile samples or peak RSS:

| Caller | Version | ns/op (three runs) | B/op (three runs) | allocs/op (three runs) |
| --- | --- | --- | --- | --- |
| Secondary | Before | 68,138,648; 68,128,121; 67,856,276 | 82,619,430; 82,617,299; 82,617,657 | 17,880; 17,877; 17,877 |
| Secondary | After | 905,890; 910,333; 914,542 | 1,420,524; 1,420,492; 1,420,493 | 16,860; 16,860; 16,860 |
| Temporal | Before | 64,382,640; 65,053,350; 64,887,163 | 82,224,639; 82,224,664; 82,224,639 | 1,546; 1,546; 1,546 |
| Temporal | After | 435,657; 430,486; 434,470 | 708,249; 708,249; 708,249 | 526; 526; 526 |

Median elapsed time falls about 74.8× and 149.3× respectively; allocated bytes
fall about 98.3% and 99.1%. Deterministic call-path counts for this workload are
255 full event copies/sorts → 1 for secondary, and 256 → 1 for temporal (one
prefix plus 255 pairs). These counts follow the exercised loop and constructor,
not a sampling-profiler estimate. Event work changes from repeated
`O(P E log E)` to `O(E log E + P log E + visited-events)` per operation.

The index still requires `O(E)` memory. Rich output metadata and coverage remain
proportional. Model-step fact lookup still scans the existing facts, and each
coverage lookup still scans declarations; this is not an overall linear-time
or constant-memory claim. The two actual operations each own their index rather
than persisting a cache on an attempt. Archive work from `kfawtcedqh` is unchanged.

## Verification

Every verification invocation uses independent disposable HOME, XDG and TMP
roots, with KTX_DB unset. Database tests/producer controls use explicitly owned
disposable databases. Build/module caches are explicitly external. No provider,
daemon, paid run, ambient/live database or retained live archive was accessed.
Scratch lives under new `sequence-work.*` directories; prior crash scratch is
untouched. The disposable runner used this recipe:

```bash
safe() {
  root=$(mktemp -d /tmp/opencode/sequence-work.XXXXXX)
  mkdir -p "$root"/{home,data,config,cache,state,runtime,tmp}
  env -u KTX_DB HOME="$root/home" XDG_DATA_HOME="$root/data" \
    XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
    XDG_STATE_HOME="$root/state" XDG_RUNTIME_DIR="$root/runtime" \
    TMPDIR="$root/tmp" TMP="$root/tmp" TEMP="$root/tmp" \
    GOPATH=~/.local/share/go GOCACHE=~/.cache/go-build "$@"
}
safe go test ./internal/cache/reuse/sequence ./internal/experiment \
  -run 'TestManyPairSequenceResultBytes|TestSequence|TestSecondaryEstimate' -count=1 -v
safe go test ./internal/experiment -run '^$' \
  -bench '^BenchmarkManyPairSequenceCallers$' -benchmem -benchtime=1s -count=3
safe go test ./internal/cache/reuse/sequence ./internal/experiment -count=1
safe go test -race ./internal/cache/reuse/sequence ./internal/experiment -count=1
safe mage check
safe mage build
safe env KTX_ENTRY_CONTRACT_BINARY="$PWD/build/ktx" go test ./internal/experiment \
  -run 'TestEntry|TestExecution|TestArchive' -count=1 -v
git diff --check
```

All final checks pass. The first full check reported one staticcheck suggestion
in the new test (`if` chain → tagged switch); corrected before the passing full
rerun. Final `mage check`: race-backed suite, 14 Python controls, adapter-overlap
gate, vet, lint **0 issues**, build; verification boundary `be5bc4ae4dbf3c60`,
13,911 attempts, **forbidden=0**, KTX_DB unset. The latest-built CLI passed all
five current-producer rows (exact unknown/cache/priced, prompt-cache, recovery),
exact regeneration/evidence validation, rejection, confinement and executed
identity controls. Public list/show/summarize/validate run on generated archives.

Agentera lists remain degraded, exact gets work, profile grounding is absent,
and startup recipe/changelog/optimization-report summaries are incomplete.
No unrelated repair or lifecycle inference was made. No implementation hardblock
remains; coordinator retains evaluation, completion and final commit ownership.
