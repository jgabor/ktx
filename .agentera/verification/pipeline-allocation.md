# Pipeline allocation and router interpretation

2026-09-07. Scope: plan `hljjrlykxb`, in-progress task `itocjemrfw`;
PERF2 and the router portion of PERF4 only. Standing authority `yohiahsqjv`
allows measured internal budgets while retaining accepted inputs. This is
implementation evidence, not task evaluation, satisfaction, or lifecycle closure.

## Change and preserved contracts

- Empty runners return before JSON recognition, with the same complete Result.
- NDJSON recognition visits physical lines using `bytes.Cut`, without a
  whole-input slice array. Single JSON, NDJSON, then JSON-plus-trailer recognition
  order, blank-record rejection, UTF-8 checks, structural fallback and the final
  anti-worsening gate remain unchanged. No scanner or new record-size limit.
- Head locates only the required delimiters; keep-ends counts delimiters and
  locates retained ranges from each end. Its overflow-safe subtraction remains.
- Dedup-blank and normalize emit bytes incrementally into a fresh output buffer.
  Only space/tab are blank/trimmed, CRLF and lone CR behavior is unchanged, and
  the input remains unmodified. Head and keep-ends also retain fresh-slice output.
- Each Resolve owns lazy command/subcommand state. The existing JSON decoder and
  `strings.Fields` semantics remain; 64 prefix-plus-subcommand rules interpret
  the large JSON input once rather than 128 times. Tool mismatch and wildcard
  routes do not parse; prefix mismatch does not split the command. First match,
  absent/malformed metadata, duplicate/case-insensitive JSON fields, and nullable
  status semantics remain unchanged. No global cache or new framework.
- Earlier provenance escaping, null unwrap, and artifact-retry bypass changes
  were not altered. No schema, configuration, stage semantic, or input cap change.

## Fixed before/after measurements

Linux amd64, Go 1.26.5 module toolchain, AMD Ryzen 7 9800X3D. Same checkout,
benchmark source and host; before means the starting dirty checkout, including
prior fixes, not HEAD. Each invocation used fresh HOME, the four XDG application
roots (data/config/cache/state), TMPDIR and KTX_VERIFY_ROOT, with KTX_DB
unset. Dependencies came from existing Go caches with GOPROXY/GOSUMDB off.
No database or provider was used by these package benchmarks.

Command: `go test ./internal/pipeline/... ./internal/router -run '^$'
-bench 'Benchmark(LineWork|ManyRoutes)$' -benchmem -benchtime=100ms -count=3`.
Values below are the median of three runs, independently per column. Timing is
Go benchmark elapsed ns/op, not process CPU accounting or a production latency
promise. Input construction and registry/route setup are outside timing.

Inputs: newlines = 1,048,576 LF bytes; plain = 65,536 copies of
`plain output line\n` (1,179,648 bytes); structured = 16,384 copies of
`{ "value": 123 }\n` (278,528 bytes). Every stage row runs the whole Runner,
including its unchanged structural guard. Head/keep counts are 10/10.

| Input / operation | Before B/op | After B/op | Before ns/op | After ns/op |
| --- | ---: | ---: | ---: | ---: |
| newlines / empty | 30,419,872 | 0 | 6,349,242 | 5.648 |
| newlines / head | 55,594,253 | 5,246,091 | 11,603,933 | 1,323,037 |
| newlines / keep-ends | 55,597,506 | 5,248,787 | 11,225,604 | 1,339,169 |
| newlines / dedup-blank | 80,760,002 | 6,294,563 | 15,006,088 | 6,107,728 |
| newlines / normalize | 84,176,066 | 6,294,463 | 17,303,129 | 4,086,271 |
| plain / empty | 2,763,793 | 0 | 449,895 | 5.654 |
| plain / head | 4,346,724 | 1,182,349 | 816,672 | 124,538 |
| plain / keep-ends | 4,350,382 | 1,183,351 | 797,141 | 133,350 |
| plain / dedup-blank | 8,582,237 | 2,362,654 | 1,583,107 | 555,881 |
| plain / normalize | 9,762,821 | 2,362,677 | 2,472,306 | 1,056,997 |
| structured / empty | 4,509,252 | 0 | 2,482,997 | 5.647 |
| structured / head | 2,658,819 | 1,854,931 | 1,356,374 | 1,060,835 |
| structured / keep-ends | 2,662,979 | 1,857,445 | 1,309,377 | 1,069,554 |
| structured / dedup-blank | 5,935,559 | 3,984,557 | 2,596,461 | 2,204,987 |
| structured / normalize | 6,214,117 | 3,984,553 | 2,931,167 | 2,307,697 |
| 64 command routes / 1 MiB ignored JSON field | 135,556,728 | 1,059,106 | 91,546,627 | 730,185 |
| 64 irrelevant-tool routes / same input | 0 | 0 | 176.4 | 177.2 |

Newline/head allocation drops 90.56%; newline/dedup 92.21%; normalize 92.52%;
large-input routing 99.22%. Empty runner is a constant-time return, so its
reported input-throughput figure is not a scanning throughput measurement.
Structured processing still allocates per compacted record; this work does not
claim optimal cold-case allocation or eliminate JSON parser work.

## Durable budgets and behavior evidence

`TestLineWorkAllocationBudgets` measures TotalAlloc after warmup and GC with a
1 MiB newline input: empty runner budget 0 B; each of the four stage runners
budget 8 MiB. Observed 0; 5,246,176; 5,248,832; 6,294,720; 6,294,720 B. The
8 MiB ceiling enforces at least 84.9% improvement over the smallest old staged
baseline, allowing allocator/runtime variation without a wall-clock test.
`TestManyRoutesAllocationBudget` permits 2 MiB, observed 1,064,360 B; this
enforces at least 98.4% reduction from the old 135.6 MB workload.

Old-behavior oracles retain independent split/join and repeated-decode
algorithms in tests. They compare 3,000 seeded mixed-byte line inputs plus
explicit empty/CR/LF/long cases, JSON atom combinations and 1,000 seeded
combinations, valid records above 2 MiB, and routing across unknown/success/
failure, metadata variants and ordered suffixes. Runner checks compare full
Result provenance and fallback ordering, not only output length. Tests also
check input immutability, fresh stage output, lazy irrelevant-field handling,
and no state reuse between Resolve calls.

Frozen characterization files and digests were not edited. Existing router
source references remain explicitly historical in its characterization test;
live behavioral assertions still execute. No blanket source-hash bypass was
added. The full characterization/component tests pass with the changed source.

## Checks

- Targeted pipeline/stages/router package tests: pass.
- Allocation budgets, verbose: pass.
- Focused `go test -race` for pipeline/stages/router/CLI, selecting incremental,
  lazy route, budget, provenance, retry, keep-ends and unwrap tests: pass.
- `mage build`: pass; local `./build/ktx compress --help`: pass (help only;
  not claimed as an end-to-end equivalence test).
- Full `mage check`: pass, including Go race tests, vet, lint (0 issues), build,
  14 offline Python tests, generated-adapter overlap gate. Isolation summary:
  17,284 database-boundary attempts, forbidden=0, KTX_DB unset.

Each verification command ran in a new disposable root; database-backed suite
tests owned their temporary databases. No ambient database, paid run, network
benchmark, migration rehearsal or schema-release gate was needed or attempted.
Toolchain caches alone were reused. Local work roots were
`/tmp/opencode/line-work.*`; they are disposable, not retained authority.

No hard implementation blocker remains. Decision list optional details are
degraded; exact task and `yohiahsqjv` reads succeeded. Profile grounding is absent
and was not regenerated. Plan status stays in progress; lifecycle completion,
evaluation, satisfaction and final commits belong to the assigned owner.
