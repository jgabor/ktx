# Integrated tool-outcome verification — corrected 2026-08-05

## Correction and scope

This evidence replaces the pre-audit result after the first independent
integrated audit accepted two findings:

1. Live stats scanned raw malformed `is_error` storage into `sql.NullInt64`, so
   SQLite text and blob values made summary and breakdown unavailable.
2. `docs/pipelines.md` still directed schema-4 summary consumers to the
   version-1 breakdown command.

The stats query now projects `is_error` only when SQLite reports integer storage
and the value is exactly 0 or 1. Every other storage class or value projects SQL
`NULL` and remains unknown before the Go integer scan. The documentation now
states the summary schema 5 and breakdown schema 2 relationship, and its contract
test rejects the complete obsolete guidance.

No ingress, routing, database-report behavior, schema migration, compatibility
bridge, TODO state, plan closure, provider call, or model call changed. Source
scope was commit `a6ae67d` plus the persisted uncommitted audit FAIL and this
bounded correction.

## Build and isolation

- Build tool: `~/.local/share/go/bin/mage` 1.17.2 with Go
  1.26.5-X:nodwarf5.
- Authoritative build command: `mage build`.
- All later product invocations used only `./build/ktx`.
- Corrected binary: version `dev`, SHA-256
  `1d93c5c26fd08bfc306b5a231653b2b0d9f6f17d46c2bda97040217fda0729eb`.
- Each verification workflow removed incoming `KTX_DB`, used a new disposable
  `HOME` and every XDG root, and selected only explicitly owned temporary
  databases. No ambient database path or content was inspected.

## Reproducible integrated fixture

The checked deterministic harness is retained at
`.agentera/verification/tool_outcome_integrated_check.py`. It contains every
aggregate-safe fixture input, mutation, product command, and assertion. It
creates current schema databases with the newly built CLI, deliberately removes
only the malformed fixture's `is_error` affinity so exact text `"0"` and `"1"`
can exist as text, confirms every stored SQLite type, and then invokes only the
provided local binary.

Exact final command, under the isolated environment prefix above:

```sh
python3 .agentera/verification/tool_outcome_integrated_check.py \
  --binary ./build/ktx \
  --root "$FRESH_OWNED_ROOT"
```

For every mixed and edge database, the harness runs:

```sh
./build/ktx stats summary --window 1h --format json
./build/ktx stats summary --window 1h --format text
./build/ktx stats breakdown --window 1h --format json
./build/ktx stats breakdown --window 1h --format text
./build/ktx db --path "$OWNED_DB" report --output -
```

The mixed database has 13 calls: 11 output-bearing calls in the fixed one-hour
window, one output-bearing call outside it, and one call with `NULL` output. The
window contains two explicit successes, one explicit failure, malformed integer
7, real 0.5, text `"malformed"`, text `"0"`, text `"1"`, blob, `NULL`, and
exit-code-only evidence. All eight noncanonical or absent states remain unknown.
Separate current-schema databases cover empty, all-known, and all-unknown
populations.

## Corrected integrated results

| Population  | Stats success/error/unknown |                    Stats error/known |                       Stats coverage | Report success/error/unknown | Report error/known | Report coverage |
| ----------- | --------------------------: | -----------------------------------: | -----------------------------------: | ---------------------------: | -----------------: | --------------: |
| Mixed       |                   2 / 1 / 8 | 33.33333333333333% JSON, 33.33% text | 27.27272727272727% JSON, 27.27% text |                   2 / 1 / 10 |             33.33% |          23.08% |
| Empty       |                   0 / 0 / 0 |                         `null` / `-` |                         `null` / `-` |                    0 / 0 / 0 |             `null` |          `null` |
| All known   |                   1 / 1 / 0 |                         50% / 50.00% |                       100% / 100.00% |                    1 / 1 / 0 |                50% |            100% |
| All unknown |                   0 / 0 / 3 |                         `null` / `-` |                           0% / 0.00% |                    0 / 0 / 3 |             `null` |              0% |

`stats summary` emitted schema 5 and `stats breakdown` emitted schema 2 in all
cases. Mixed stats reconcile known `3 = 2 + 1`, total `11 = 3 + 8`, and
`calls.total_with_output = outcomes.total_calls = 11`. Summary and breakdown
calls, outcomes, and tokens match exactly. Raw and delivered estimates both
remain 39 with no artifact. Detailed groups contain no outcome fields.

`db report` remains schema 3 over all calls. Mixed report totals reconcile known
`3 = 2 + 1` and total `13 = 3 + 10`; malformed storage stays unknown and exit
codes stay diagnostic. Default output retains only the database basename and
contains neither an absolute path nor the disposable root. Empty and all-unknown
reports exercise zero denominators.

Read-only report preservation:

| Database    | SHA-256 before and after                                           | `user_version` before/after |
| ----------- | ------------------------------------------------------------------ | --------------------------: |
| Mixed       | `81dd5582a09927cc73553e882c6194fb0c4589d40cc00efe331ab814aab9f079` |                       9 / 9 |
| Empty       | `d3ad7d0822fa708c2b34d3018f4a18efdfe3465b10ec888bc2018430c4ece1e4` |                       9 / 9 |
| All known   | `83025548174fdd2fa5547f558d92de884dc8d452abfe716c86df60e2c260b124` |                       9 / 9 |
| All unknown | `9d752f3922bbbdf954ea22f17702d3a319e932e377bc6de2a711ea422816f834` |                       9 / 9 |

## Tests and public contracts

Final focused commands, each with incoming `KTX_DB` unset and disposable state:

```sh
go test ./internal/db -run '^TestStatsSummaryRows(ClassifiesCanonicalOutcome|NormalizesOutcomeStorageClasses)?$' -count=1
go test ./internal/cli -run '^Test(Hook_FailureGate|OpenCodeHook_OutcomeCapture|OpenCodeOutcome_FirstAcceptedMetadataWins|Hook_Codex_(ObjectWithAggregatedOutput|BareStringOutput|ExactRetryIgnoresChangedNestedResponseMetadata|RuntimeLabelDoesNotTrustFlatPayload)|StatsWindowLogic|StatsCommandDispatchesWindowViews|StatsBreakdownEndToEnd|ToolOutcomeContractSurfacesMatchBehavior|OutcomeReportContractsPublishCurrentSchemasAndDenominators|RunWriteWith_FailureSignalFields|WriteHelpMatchesIsErrorDecoding)$' -count=1
go test ./internal/parser -run '^TestParseEvent_FailureSignal$' -count=1
go test ./internal/router -run '^Test(Match_ExitStatus|Match_ExitStatusFirstMatchBoundary|Match_ExitFailureRule|DocumentedContractReferenceValidation)$' -count=1
go test ./internal/dbreport -run '^Test(EmptyReportGoldenAndReadOnly|ReportAggregatesProductEvidence|ReportOutcomeMetrics)$' -count=1
```

All focused checks passed. DB coverage includes integer 0/1 and malformed
integer, real, ordinary text, exact text `"0"`/`"1"`, blob, and `NULL`. CLI
coverage proves both JSON and text summary/breakdown remain available, preserve
totals and formulas, and omit per-group outcomes for malformed integer, real,
text, blob, `NULL`, and exit-code-only evidence. Existing ingress, routing,
failure-passthrough, report, and first-authority checks also passed.

The retained script compiled successfully. A read-only documentation checker
parsed seven JSON fences, including one documented fragment, and validated 106
local Markdown targets across 26 files with zero issues. Newly built local help
passed for `write`, `hook`, `stats summary`, `stats breakdown`, and `db`.

The final full command was:

```sh
env -u KTX_DB HOME="$HOME_ROOT" XDG_DATA_HOME="$DATA_ROOT" \
  XDG_CONFIG_HOME="$CONFIG_ROOT" XDG_CACHE_HOME="$CACHE_ROOT" \
  XDG_STATE_HOME="$STATE_ROOT" XDG_RUNTIME_DIR="$RUNTIME_ROOT" \
  GOCACHE="$CACHE_ROOT/go-build" mage check
```

Result: PASS across all packages, vet, lint, and build. Lint reported `0 issues`.
The database-boundary audit reported 954 attempts and operations,
`forbidden=0`, external caches, and `ktx_db=unset`. The post-check local binary
hash remained the corrected hash above.

## State and limitation

The persisted independent FAIL remains attached to task `mvggstggsy`, which
remains in progress. This bounded correction does not self-approve the required
independent re-review and does not close the task, TODO item, or plan. Final
completion still requires orchestration to persist the re-review result and
adjudicate any remaining accepted material issue.
