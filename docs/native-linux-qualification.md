# Native Linux qualification

Qualification date: 2026-09-07. This is a point-in-time checkout verification,
not a release certificate or a claim about every Linux environment.

## Supported scope and host

Linux is the supported operating system. Non-Linux execution is unsupported or
unverified. Retained source, compilation results, and historical reports do not
establish current native support outside Linux.

Terminal-mode `ktx run` requires readable procfs, subreaper support,
`pidfd_open`, and `waitid(P_PIDFD)`, normally available on Linux 5.4 or newer
subject to backports and syscall policy. Missing preparation produces an explicit
error before spawning the wrapped child. This includes a controlling terminal
with redirected stdin. Non-terminal runs and unrelated commands are outside this
capability floor; no kernel-version gate was added.

The actual verification host reported:

```text
/etc/os-release: Arch Linux, ID=arch, BUILD_ID=rolling
uname -a: Linux allspark 7.2.2-1-cachyos #1 SMP PREEMPT_DYNAMIC Fri, 28 Aug 2026 14:27:15 +0000 x86_64 GNU/Linux
go version: go1.27.0-X:nodwarf5 linux/amd64
bun --version: 1.4.0
```

Native evidence below covers this Linux/amd64 host only. Other architectures,
older kernels, WSL, container-hosted deployment, BSD, macOS and Windows were not
executed or qualified. Offline fake-Docker tests are not native container proof.
No cross-compilation was used as execution evidence.

## Current promises and source inventory

| Surface                                                 | Disposition                                                                                                      |
| ------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| README installation and wrapper guidance                | Linux-only product support; terminal-only capability floor and pre-spawn rejection                               |
| Agent guide and ktx skill                               | Same support boundary; required Linux failures cannot be waived by compilation                                   |
| Database safety and critical-path runbooks              | Supported Linux admission/ownership; retained BSD/Windows implementation facts explicitly separated from support |
| Wrapper skill reference                                 | Existing terminal cancellation, redirected stdin and non-Linux caveats retained                                  |
| Benchmark development skill reference                   | Linux-only native verification; no deletion workaround for a failed gate                                         |
| CI workflow                                             | Ubuntu runner remains; this local run does not claim a fresh hosted CI result                                    |
| Historical reports, changelog dates and fixture goldens | Unchanged by this qualification; historical facts are not upgraded to current proof                              |

`go list` on this host confirmed the following selected and ignored source
families. File presence is an implementation inventory, not a build or runtime
promise for every named target.

| Area                              | Selected here                                                                  | Retained alternatives not executed                                                                                                               |
| --------------------------------- | ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| Database locking                  | `writer_lock_unix.go` (Linux build constraint), `writer_lock_observe_linux.go` | `writer_lock_bsd.go` (Darwin/DragonFly/FreeBSD/NetBSD/OpenBSD), `writer_lock_windows.go`, `writer_lock_other.go`, `writer_lock_observe_other.go` |
| Wrapper process ownership         | `run_process_linux.go`, `run_descendants_linux.go`                             | `run_process_other.go`                                                                                                                           |
| Codex shared-hook mutation        | `init_codex_mutation_linux.go`                                                 | `init_codex_mutation_other.go`                                                                                                                   |
| Archive confinement               | `archive_file_linux.go`                                                        | `archive_file_other.go`                                                                                                                          |
| Diagnostic file access            | `file_unix.go` (Linux and BSD/Darwin family)                                   | `file_windows.go`                                                                                                                                |
| Component filesystem measurements | `filesystem_linux.go`                                                          | `filesystem_other.go`                                                                                                                            |

No production code, schema, platform implementation, runtime framework or
capability minimum was changed. One existing opt-in test's fake image identity
was corrected from an invalid placeholder to a syntactically valid synthetic
SHA-256 image ID. Before that correction, all four local benchmark signal cases
failed at `image_capability_unavailable`, before reaching cancellation. Afterward
all four reached and passed their ownership checks. No container was started.

## Isolation and commands

Every verification invocation received a newly allocated disposable `HOME`,
`XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME`,
`XDG_RUNTIME_DIR`, and temporary directory (`TMPDIR`, `TMP`, `TEMP`). Inherited
`KTX_DB` was unset. Manual tests used `KTX_VERIFY_ROOT` for the disposable boundary;
fixtures explicitly created their own databases. Go tool caches were reused,
not application state. Mage established its own nested verified boundary.

The commands below were prefixed with an isolation wrapper implementing that
contract. They used the assigned checkout, not an installed product binary.
All selected tests used `-count=1`; ordinary `mage check` retained its standard
cache behavior for unaffected pure packages. No provider, paid run, daemon,
ambient database or real experiment archive was accessed.

```sh
go test -race -count=1 -v ./internal/db \
  -run 'Test(Supported|WaitingWriter|OpenQueryKeeps|NewerSchemaCoordination|ManagedSchema|OpenLiteralDatabaseTarget|InitialReadinessFailure|PeriodicStatsReaders|OpenCurrentSchema)' \
  -timeout=180s

go test -race -count=1 -v ./internal/cli ./internal/experiment \
  -run 'Test(RunSignals|RunForegroundStdin|RunTerminalLifecycle|RunOutput|ContainerCleanupOwnership|OSExecutor|RunnerStopsAfterCleanupFailureOrCancellation|InspectionResponseStopsInfiniteWriter)' \
  -timeout=180s

mage check
mage build

env KTX_RUN_SIGNAL_CLI="$PWD/build/ktx" \
  KTX_LIFECYCLE_CLI="$PWD/build/ktx" KTX_CODEX_CLI="$PWD/build/ktx" \
  go test -race -count=1 -v ./internal/cli \
  -run '^Test(RunSignals|RunForegroundStdin|RunTerminalLifecycle|BenchLocalCLISignals|CodexSharedLocalCLI)$' \
  -timeout=180s

env KTX_ADAPTER_CLI="$PWD/build/ktx" \
  go test -race -count=1 -v ./internal/cli ./internal/telemetry/opencode \
  -run 'Test(OpencodeEnvelopeLocalCLI|TelemetryAdapterLocal(Conformance|NonfiniteSnapshot|DurableRedelivery|PayloadRelease)|WrapperTelemetryLocalDurableRedelivery|TelemetryDurableSharedConformance|Contract)' \
  -timeout=180s
```

After the synthetic test-fixture correction, `mage check` and `mage build` were
rerun successfully, followed by the complete local process selection again.
The adapter selection passed with the same production source before that final
rebuild; the final Mage gate also rebuilt and executed its required adapter test.

## Results

| Boundary                                | Fresh native result                                                                                                                                                                                                               |
| --------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Database admission and aliases          | PASS: waiting writer before later hard-link reader; supported reader/writer exclusion, path aliases, process-exit lock recovery and periodic-reader non-starvation                                                                |
| Database readiness and reopening        | PASS: compatible cross-process migration, reopened shared-lease schema recheck, held compatible leases, managed transaction and writer-category rechecks                                                                          |
| Read-only and newer-schema preservation | PASS: current open avoids write ownership; newer-schema read/write coordination preserves storage; literal special-character targets and failed-readiness cleanup                                                                 |
| Non-terminal signals                    | PASS: eight SIGINT/SIGTERM cases across graceful, resistant, leader-first and closed-stdout children; committed evidence retained and incomplete recording absent                                                                 |
| Terminal lifecycle                      | PASS: normal input, redirected stdin, actual terminal Ctrl-C, TERM, leader-first and nested resistant descendants; unrelated caller-group sibling survives, terminal settings/foreground group preserved, terminal remains usable |
| Output failure                          | PASS: normalization, oversized output, short/error sinks, broken pipe and previously committed evidence preservation                                                                                                              |
| Benchmark ownership                     | PASS: owned process-group termination and leader-first cases, canceled-command non-start, exact container-name cleanup, cleanup-failure stop; four local compare/rank SIGINT/SIGTERM cases use only fake Docker                   |
| Shared Codex hook                       | PASS: actual local CLI shared-hook ownership fixture                                                                                                                                                                              |
| Generated adapter                       | PASS: local envelopes, Go/Bun conformance, malformed/nonfinite measurements, durable redelivery and acknowledged payload release; required readiness-overlap test executes with Bun                                               |
| Full repository gate                    | PASS: Go race/Mage tests, 14 offline Python verifier tests, required generated-adapter overlap, vet, lint (`0 issues`) and build                                                                                                  |

The final Mage database-boundary summary was:

```text
target=check attempts=17949
operations=exclusive_coordination:7440,inspect:2496,open:1997,open_read_only:4581,resolve:1435
forbidden=0 caches=external ktx_db=unset
ktx generated-adapter gate: required overlap check passed
```

Additional disposable Python/PTY probes invoked the unmodified `build/ktx` after
the final build. `strace -f -e inject=...` injected each of
`pidfd_open:error=ENOSYS`, `pidfd_open:error=EPERM`, `waitid:error=EINVAL` and
`prctl:error=EPERM`. Every terminal case exited 4 with an explicit
`prepare descendant ownership` error and no child-start marker. The first probe
draft expected the wrong diagnostic phrase (`prepare child ownership`); fixing
that probe assertion required no product change. A non-terminal run under
`pidfd_open:error=ENOSYS` still exited 0 and forwarded its output.

A real closed stdout pipe made the local wrapper exit 4, reap its direct child
and leave zero recorded-stream rows. A newly seeded version-0 SQLite file migrated
through `build/ktx db --path <disposable-path> migrate` to schema 16. A separately
seeded version-999 file exited 5 with `schema-incompatible` and unchanged database
bytes. No schema number was lowered and no live source was copied.

Disposable raw command logs were under `/tmp/opencode/platform-qualification.*`:
`smoxaJ` (database), `ME0BrJ` (process units), `S4BESf` (initial full gate),
`mmch3a` (stale fake-ID failure plus passing wrapper cases), `MkMmYb` (corrected
benchmark cases), `luXJ9m` (adapter conformance), `WdRHfR` (final full gate),
`1xv0Mw` (final build), `dUGsO5` (final local process cases), and `iUfzNl` (final
CLI probes). These paths are disposable diagnostics, not durable dependencies;
the commands, assertions, results and limitations are retained here.

## Limits and completion boundary

There is no unresolved required Linux gate failure in this qualification.
Required failures or missing native evidence must still block subsequent
completion; narrowing support does not waive Linux correctness.

Unavailable procfs was not fault-injected in this run; its existing preparation
check was inspected, and readable procfs plus successful subreaper/pidfd waiting
were exercised natively. Fault injection verifies selected denial paths, not all
possible syscall policies. Timing is descriptive, not a platform-wide bound.

`mage verify`, a fresh hosted CI run and the production-scale schema release gate
were not run. No persistent schema changed, and the small synthetic migration
and Python verifier unit tests supply no schema-release authority. Existing
coverage measurements remain their original point-in-time baseline; this work
does not refresh coverage or declare overall plan, audit or release completion.
