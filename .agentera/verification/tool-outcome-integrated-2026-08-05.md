# Integrated tool-outcome verification — 2026-08-05

## Scope and isolation

- Source: `d0f9cbb` plus the pre-existing uncommitted plan-task state for
  `fckvcbciac` and `mvggstggsy`; no product source was modified before the
  build.
- Build: `~/.local/share/go/bin/mage` 1.17.2 with Go
  1.26.5-X:nodwarf5; `mage build` produced `./build/ktx` version `dev`, SHA-256
  `c60cd248f93580b6fcfa2066c986fed0db0f4d6c72f4ba2190d7e94dd351856b`.
- All development commands removed incoming `KTX_DB` and used a new disposable
  `HOME`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, and
  `XDG_STATE_HOME`. Product report workflows then selected only their owned
  temporary database with `KTX_DB` and, for `db report`, the same explicit
  `--path`. No ambient database path or content was inspected.
- No provider or model was invoked. OpenCode and Codex claims below are bounded
  to source tests for the documented OpenCode 1.18.5 and Codex 0.144.4 event
  contracts.

## Disposable integrated fixture

The aggregate-safe fixture writer used `./build/ktx write` to create current
schema databases, then used SQLite only on those owned fixtures to establish
states that strict sidecar input cannot create. The mixed database had eight
tool calls: six output-bearing calls in a fixed trailing one-hour window, one
output-bearing call outside it, and one call with `NULL` output. The in-window
population contained two explicit successes, one explicit failure, one plain
unknown, one malformed stored `is_error`, and one exit-code-only unknown. Empty,
all-known, and all-unknown databases were separate current-schema fixtures.

The final assertion harness invoked only `./build/ktx` and ran these commands
for each fixture under the isolation prefix described above:

```sh
./build/ktx stats summary --window 1h --format json
./build/ktx stats summary --window 1h --format text
./build/ktx stats breakdown --window 1h --format json
./build/ktx stats breakdown --window 1h --format text
./build/ktx db --path "$OWNED_DB" report --output -
```

Results:

| Population  | Stats success/error/unknown |                    Stats error/known |        Stats coverage | Report success/error/unknown | Report error/known | Report coverage |
| ----------- | --------------------------: | -----------------------------------: | --------------------: | ---------------------------: | -----------------: | --------------: |
| Mixed       |                   2 / 1 / 3 | 33.33333333333333% JSON, 33.33% text | 50% JSON, 50.00% text |                    2 / 1 / 5 |             33.33% |           37.5% |
| Empty       |                   0 / 0 / 0 |                         `null` / `-` |          `null` / `-` |                    0 / 0 / 0 |             `null` |          `null` |
| All known   |                   1 / 1 / 0 |                         50% / 50.00% |        100% / 100.00% |                    1 / 1 / 0 |                50% |            100% |
| All unknown |                   0 / 0 / 3 |                         `null` / `-` |            0% / 0.00% |                    0 / 0 / 3 |             `null` |              0% |

`stats summary` reported schema 5 and `stats breakdown` schema 2. Their calls,
outcomes, and token totals matched exactly for every population. Mixed calls
reconciled as known `3 = 2 + 1`, total `6 = 3 + 3`, and
`calls.total_with_output = outcomes.total_calls = 6`; raw and delivered token
estimates both remained 24 with no artifact. Breakdown groups contained only
delivery fields and no outcome aggregates.

`db report` reported schema 3 over all tool calls. The mixed report reconciled
known `3 = 2 + 1` and total `8 = 3 + 5`; malformed stored state remained
unknown, exit codes remained diagnostic, and percentages rounded to two
decimals. Empty and all-unknown reports exercised zero denominators. Default
report output retained only the database basename and contained neither an
absolute database path nor the disposable root.

Read-only preservation evidence:

| Database    | SHA-256 before and after                                           | `user_version` before/after |
| ----------- | ------------------------------------------------------------------ | --------------------------: |
| Mixed       | `c0c9e65c8eab1b956f6ba292e8846913c8bf7c6c269339a21c7a3204b7b6fd92` |                       9 / 9 |
| Empty       | `d3ad7d0822fa708c2b34d3018f4a18efdfe3465b10ec888bc2018430c4ece1e4` |                       9 / 9 |
| All known   | `912c19c51fa2e79c5a53b2274ddcf887c6f60e0e1d8133f83c4e6f0c679eeb04` |                       9 / 9 |
| All unknown | `56c6548336c0dfca632caa1af776634f4134aa34ac2482a81f0ccbea437f7764` |                       9 / 9 |

## Focused and repository checks

The final focused run used `env -u KTX_DB` and disposable XDG roots. Test-owned
databases remained per-test temporary files:

```sh
go test ./internal/parser -run '^TestParseEvent_FailureSignal$' -count=1
go test ./internal/router -run '^Test(Match_ExitStatus|Match_ExitStatusFirstMatchBoundary|Match_ExitFailureRule)$' -count=1
go test ./internal/db -run '^TestStatsSummaryRows(ClassifiesCanonicalOutcome)?$' -count=1
go test ./internal/dbreport -run '^Test(EmptyReportGoldenAndReadOnly|ReportAggregatesProductEvidence|ReportOutcomeMetrics)$' -count=1
go test ./internal/cli -run '^Test(Hook_FailureGate|OpenCodeHook_OutcomeCapture|OpenCodeOutcome_FirstAcceptedMetadataWins|Hook_Codex_(ObjectWithAggregatedOutput|BareStringOutput|ExactRetryIgnoresChangedNestedResponseMetadata|RuntimeLabelDoesNotTrustFlatPayload)|StatsWindowLogic|StatsCommandDispatchesWindowViews|StatsBreakdownEndToEnd|ToolOutcomeContractSurfacesMatchBehavior|OutcomeReportContractsPublishCurrentSchemasAndDenominators|RunWriteWith_FailureSignalFields|WriteHelpMatchesIsErrorDecoding)$' -count=1
go test ./internal/router -run '^TestDocumentedContractReferenceValidation$' -count=1
```

All focused packages passed. These checks cover OpenCode wrapper precedence,
hook outcome capture and first-evidence authority; Codex event-level success and
nested-response neutrality; explicit-success-only routing, ordinary any/default
handling of unknown, explicit failure routing, and unchanged failed output even
with a lossy profile; report edge populations; and synchronized public
contracts.

One earlier focused invocation incorrectly forced all CLI subtests to share one
owned `KTX_DB`; an exact-retry test then observed four rows instead of its one
test-owned row. This was a harness isolation error, not a product defect. The
final focused run above removed `KTX_DB`, allowed each test to allocate its own
temporary database, and passed. No source correction followed that failure.

The full isolated repository check was:

```sh
env -u KTX_DB HOME="$HOME_ROOT" XDG_DATA_HOME="$DATA_ROOT" \
  XDG_CONFIG_HOME="$CONFIG_ROOT" XDG_CACHE_HOME="$CACHE_ROOT" \
  XDG_STATE_HOME="$STATE_ROOT" XDG_RUNTIME_DIR="$RUNTIME_ROOT" \
  GOCACHE="$CACHE_ROOT/go-build" mage check
```

Result: PASS across all packages, vet, lint, and build. The database-boundary
audit reported 953 attempts, 953 operations, `forbidden=0`, external caches,
and `ktx_db=unset`; lint reported `0 issues`.

## Examples, documentation, and state

- The focused contract tests validated outcome help, `ktx.example.toml`, the
  pipeline runbook, the ktx agent guide, report schema declarations, formulas,
  strict sidecar decoding, and retained router characterization.
- A read-only checker parsed seven JSON fences (including one documented JSON
  object fragment), checked 106 local Markdown link targets across 26 root,
  docs, and ktx-skill files, and found zero issues.
- Help from the newly built binary was inspected for `write`, `hook`, `stats
summary`, `stats breakdown`, and `db`; it declared the tested schema versions,
  populations, outcome authority, formulas, and text/JSON semantics.
- Agentera 3.0.0-dev.41 `doctor` returned `up_to_date`. Typed plan retrieval
  confirmed plan `ldknsjplye` remains open, `fckvcbciac` is complete with an
  independent PASS evaluation, and `mvggstggsy` remains in progress.

## Verdict and limitation

Integrated implementation verification passed and found no reproducible product
defect. No product correction was made. The required independent implementation
audit and any resulting bounded correction/re-review remain owned by
orchestration; this artifact does not self-approve that criterion and does not
close the task, TODO item, or plan.
