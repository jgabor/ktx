# Archive materialization resource evidence

2026-09-07. Scope: plan `hljjrlykxb`, task `czzdcnqozn`, archive portions of
PERF4 and S7 only. Implementation and verification evidence, not evaluation,
audit, lifecycle completion, or decision satisfaction. Task remains in progress.
Canonical progress: `kfawtcedqh` (typed CLI append).

## Changes and preserved contracts

- `loadExperimentEvidence` visits the ledger under its unchanged 16 MiB Scanner
  bound. It materializes each unique attempt once and discards duplicate
  projections after comparing orchestration and recording required divergences.
  It preserves first-seen attempt order, per-attempt record ordinals, grouped
  divergence order, and all accepted records. No count or total-file cap exists.
- The in-memory runner materializer indexes its supplied records rather than
  making additional full-Attempt maps and duplicate slices. The permissive
  collection loader remains available to discovery/compatibility consumers.
- For schema-2/3 streams with declared hashes and database artifacts, the hash
  pass also computes call accounting. The hash drains to EOF even when the
  existing call Scanner stops early. Real read errors cannot become hashes.
  Materialization reuses only that operation's observation, not a global cache.
- Usage/scoring remain a separate pass: usage skips oversized records and
  continues, whereas call accounting's Scanner stops. Neither behavior is
  unified. Stream dispatch decodes only the event type first, avoiding copied
  `content`/`part` payloads for tool, text, and ignored events. Step metadata still
  receives its full specialized decoding; this does not claim fewer decoder
  calls for every event type.
- Manifest, retained-input/config and artifact checks still precede credit.
  Earlier attempts can be privately interpreted before a later bad ledger
  record is found, but errors publish neither a partial summary nor validation
  success. Missing/unknown versions, null records, conflicting authoritative
  duplicates, missing evidence, mismatched hashes, and positive target rules
  remain enforced. All reads remain rooted; private SQLite main/WAL copies and
  executed config/image attestation are unchanged.
- Rich usage, model steps, identities, sequence evidence, scoring, and separate
  stream/persistence call authority remain. Normal bench's aggregate-only
  contract and completed recording work (`absisndpqx`, `zyeasphydk`) are untouched.
  Compatibility readers remain until the later retirement owner acts.

## Fixed generated workload measurements

Native Linux `7.2.2-1-cachyos`, amd64; actual toolchain
`go1.27.0-X:nodwarf5` (module minimum remains Go 1.26.5). No daemon, provider,
paid call, retained live archive, ambient database, or schema change. Fixture
setup uses explicit disposable owner databases and is excluded from allocation
measurement. Each verification invocation has fresh HOME, XDG and TMP roots
and unset KTX_DB. Go build/module caches are explicitly external.

`TestMaterializationResourceProbe` was added and run before production edits,
then rerun against the final implementation. Measurements are process-local
`runtime.MemStats` TotalAlloc/Mallocs deltas around `RegenerateSummary`, not
timing claims. The same generated 4,041-byte ignored-event line is repeated to
the stated size, then the same 346-byte two-model-step scoring stream is
appended. Growing ledgers repeat an unchanged cacheless compatibility attempt;
there is one unique output attempt and no divergence payload in that workload.

| Workload | Allocated bytes before | After | Allocations before | After |
| --- | ---: | ---: | ---: | ---: |
| 1 MiB stream | 2,602,696 | 1,527,712 | 3,510 | 3,258 |
| 8 MiB stream | 17,707,696 | 9,085,056 | 10,291 | 8,202 |
| 16 MiB stream | 35,026,440 | 17,766,656 | 18,653 | 14,443 |
| 100 ledger records | 1,597,088 | 1,221,192 | 10,572 | 10,629 |
| 1,000 ledger records | 15,130,184 | 10,460,536 | 100,931 | 100,761 |
| 10,000 ledger records | 169,257,680 | 102,870,920 | 1,002,638 | 1,002,715 |

The stream's largest allocation volume falls 49.3%; the ledger's falls 39.2%.
Ledger malloc counts are essentially unchanged, not an improvement claim.
Cumulative allocations still grow with records decoded. The opt-in probe now
asserts the pre-edit summary hashes and measured workload budgets (24 MiB
allocated for the 16 MiB stream; 128 MiB for 10,000 duplicate records). Those
are test budgets, not runtime acceptance restrictions. A final untraced rerun
passed both budgets (17,766,664 and 102,871,328 bytes respectively).

Linux `strace -f -yy -e trace=openat,read,lseek` counted descriptor-annotated
stream reads, joining unfinished/resumed calls. Exclude setup's one read-open
and 346 bytes; table below is regeneration only, not DB-copy or ledger I/O.

| Actual stream bytes | Read bytes before | After | Opens before → after | Rewinds |
| ---: | ---: | ---: | ---: | ---: |
| 1,046,965 | 3,140,604 | 2,093,930 | 2 → 2 | 1 → 0 |
| 8,385,421 | 25,155,972 | 16,770,842 | 2 → 2 | 1 → 0 |
| 16,774,537 | 50,323,320 | 33,549,074 | 2 → 2 | 1 → 0 |

Before: hash, usage, and call-accounting passes; call accounting stops 291 bytes
before EOF on this fixture's missing call identity. After: two complete passes,
with hashing draining the early-stopped call reader. Opens are unchanged; the
saved pass was a seek, not another open. Hashless and legacy compatibility
paths retain their existing appropriate passes. There is no universal
single-pass claim.

All stream sizes produce identical summary SHA-256:
`ab50dcbe9eb77593e2676f39bdba6d35e41046f34f22c34b297814d75cdb1957`.
All ledger sizes produce identical summary SHA-256:
`721b8b56c957602d928cfd2380da6683f078d3b6cac6e969d24eed88432572e9`.

## Remaining proportional bounds

This is **not constant RAM**. Transient ledger decoding is proportional to the
largest record under the existing 16 MiB Scanner limit; usage/call working
records retain their existing 64 MiB contracts. Required live metadata remains
proportional to unique attempts, rich model-step/sequence evidence, distinct
call identities and callback fingerprints, and projection divergences. A
duplicate with different rich projected metadata can legitimately add that
metadata to the divergence output. Summary construction/serialization remains
output-sized. In-memory runner callers still own their supplied attempt slice.

Removed storage is the aggregate collection of duplicate ledger projections
and additional full-Attempt indexing copies, plus uninterpreted per-event raw
payload copies. This does not remove output-sized attempt/model-step evidence.
Private SQLite scratch remains proportional to the selected main/WAL artifacts.
`/tmp` is tmpfs here: allocation deltas are not peak RSS, total host-memory,
filesystem-cache, or physical-device-latency measurements. No RSS qualification
or worst-case constant-memory bound is claimed.

## Correctness and authoritative checks

- `TestHashStreamCallsEquivalence`: exact bytes/hash and call authority for
  duplicates, retries, errors, malformed JSON, missing identity, CRLF and EOF
  without newline. Boundary controls at 64 MiB−1, 64 MiB, 64 MiB+1 compare the
  unchanged Scanner and verify complete hashing; injected read errors survive.
- `TestStreamingLedgerMatchesMaterializedCollection`: collection versus visitor
  equivalence for interleaved duplicate IDs, projection drift, rich model steps,
  scoring/usage/authority, grouped diagnostic ordering, and exact summary bytes.
- Late null, malformed JSON and over-limit records reject regeneration and
  validation without changing existing summary bytes. Decode compatibility
  controls retain duplicate/case-variant keys, malformed optional metadata,
  null/zero states, and ownership after source-buffer reuse.
- All five current-producer rows in
  [the entry matrix](experiment-entry-contract-matrix.md) pass. Current exact
  summaries 1/2/3, prompt-cache summary 5, recovery summary 7, rich metadata,
  and compatibility/golden tests remain; no golden/history bytes were rewritten.

Commands (each `safe` invocation independently allocates roots):

```bash
safe() {
  root=$(mktemp -d /tmp/opencode/materialization.XXXXXX)
  mkdir -p "$root"/{home,data,config,cache,state,runtime,tmp}
  env -u KTX_DB HOME="$root/home" XDG_DATA_HOME="$root/data" \
    XDG_CONFIG_HOME="$root/config" XDG_CACHE_HOME="$root/cache" \
    XDG_STATE_HOME="$root/state" XDG_RUNTIME_DIR="$root/runtime" \
    TMPDIR="$root/tmp" TMP="$root/tmp" TEMP="$root/tmp" \
    GOPATH=~/.local/share/go GOCACHE=~/.cache/go-build "$@"
}
safe go test ./internal/experiment ./internal/usage -count=1
safe env KTX_MATERIALIZATION_PROBE=1 strace -f -yy \
  -e trace=openat,read,lseek -o /tmp/opencode/materialization-final.trace \
  go test ./internal/experiment -run '^TestMaterializationResourceProbe$' -count=1 -v
safe go test -race ./internal/experiment ./internal/usage -count=1
safe mage check
safe mage build
safe env KTX_ENTRY_CONTRACT_BINARY="$PWD/build/ktx" go test ./internal/experiment \
  -run 'TestEntry|TestExecution|TestArchive' -count=1
safe env KTX_MATERIALIZATION_PROBE=1 go test ./internal/experiment \
  -run '^TestMaterializationResourceProbe$' -count=1 -v
git diff --check
```

All pass. The first full check found an ineffectual probe assignment; it was
removed. Final full `mage check`: race-backed Go suite, 14 Python controls,
generated-adapter overlap, vet, lint **0 issues**, build; verification boundary
`bf0c26cd5a245b0f`, 13,911 attempts, **forbidden=0**, `ktx_db=unset`.
The latest local build exercised public list/show/summarize/validate on generated
controlled archives, including entry rejection, confinement and execution
identity controls. Each CLI child also gets fresh HOME/XDG/TMP roots.

No implementation hardblock remains. Lists remain degraded, exact gets work,
profile is absent, and startup recipe/changelog metadata is incomplete. No
profile repair, staging, commit, push, task completion, evaluation, audit, or
satisfaction mutation is part of this work.
