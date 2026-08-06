# Integrated tool-outcome verification — merge-readiness correction 2026-08-06

## Correction and scope

This evidence refreshes the retained result after an independent merge-readiness
audit found that database reporting did not enforce the same outcome storage
contract as live stats. The earlier correction already addressed two findings:

1. Live stats scanned raw malformed `is_error` storage into `sql.NullInt64`, so
   SQLite text and blob values made summary and breakdown unavailable.
2. `docs/pipelines.md` still directed schema-4 summary consumers to the
   version-1 breakdown command.

The database report now also requires `typeof(is_error) = 'integer'` before an
exact 0 or 1 can count as known. The guard applies to measurement coverage,
success and failure totals, known-denominator rates, tool groups, agent groups,
and daily groups. Exact SQLite REAL 0.0 and 1.0 therefore remain unknown, just
like REAL 0.5, out-of-domain integers, text, blobs, and `NULL`.

No ingress, routing, schema migration, compatibility bridge, TODO state, plan
closure, provider call, or model call changed. The reopened plan and in-progress
correction task remain open for independent evaluation.

## Build and isolation

- Build tool: `~/.local/share/go/bin/mage` 1.17.2 with Go
  1.26.5-X:nodwarf5.
- Authoritative build command: `mage build`.
- All later product invocations used only `./build/ktx`.
- Corrected binary: version `dev`, SHA-256
  `15d8e5afabbd8bd3b07ded4e362813399400f600a3ffa77509c94a0ff65d65fc`.
- Each verification workflow removed incoming `KTX_DB`, used a new disposable
  `HOME` and every XDG root, and selected only explicitly owned temporary
  databases. No ambient database path or content was inspected.

## Reproducible integrated fixture

The checked deterministic harness is retained at
`.agentera/verification/tool_outcome_integrated_check.py`. It contains every
aggregate-safe fixture input, mutation, product command, and assertion. It
creates current schema databases with the newly built CLI, deliberately removes
only the malformed fixture's `is_error` affinity so exact REAL 0.0 and 1.0 and
exact text `"0"` and `"1"` can retain their storage classes, confirms every
stored SQLite type, and then invokes only the provided local binary.

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

The mixed database has 15 calls: 13 output-bearing calls in the fixed one-hour
window, one output-bearing call outside it, and one call with `NULL` output. The
window contains two explicit successes, one explicit failure, malformed integer
7, REAL 0.0, REAL 1.0, REAL 0.5, text `"malformed"`, text `"0"`, text `"1"`,
blob, `NULL`, and exit-code-only evidence. The harness proves the three REAL
values have SQLite storage class `real`. All ten noncanonical or absent states
remain unknown. Separate current-schema databases cover empty, all-known, and
all-unknown populations.

## Corrected integrated results

| Population  | Stats success/error/unknown |                    Stats error/known |                        Stats coverage | Report success/error/unknown | Report error/known | Report coverage |
| ----------- | --------------------------: | -----------------------------------: | ------------------------------------: | ---------------------------: | -----------------: | --------------: |
| Mixed       |                  2 / 1 / 10 | 33.33333333333333% JSON, 33.33% text | 23.076923076923077% JSON, 23.08% text |                   2 / 1 / 12 |             33.33% |             20% |
| Empty       |                   0 / 0 / 0 |                         `null` / `-` |                          `null` / `-` |                    0 / 0 / 0 |             `null` |          `null` |
| All known   |                   1 / 1 / 0 |                         50% / 50.00% |                        100% / 100.00% |                    1 / 1 / 0 |                50% |            100% |
| All unknown |                   0 / 0 / 3 |                         `null` / `-` |                            0% / 0.00% |                    0 / 0 / 3 |             `null` |              0% |

`stats summary` emitted schema 5 and `stats breakdown` emitted schema 2 in all
cases. Mixed stats reconcile known `3 = 2 + 1`, total `13 = 3 + 10`, and
`calls.total_with_output = outcomes.total_calls = 13`. Summary and breakdown
calls, outcomes, and tokens match exactly. Raw and delivered estimates both
remain 44 with no artifact. Detailed live groups contain no outcome fields.

`db report` remains schema 3 over all calls. Mixed report totals reconcile known
`3 = 2 + 1` and total `15 = 3 + 12`; malformed storage stays unknown and exit
codes stay diagnostic. Its measurement coverage is 20%, its tool group reports
one error and 12 unknown outcomes, and its agent and daily groups each report
only the canonical INTEGER 1 as an error. Default output retains only the
database basename and contains neither an absolute path nor the disposable root.
Empty and all-unknown reports exercise zero denominators. Stats and reports
agree exactly for the empty, all-known, and all-unknown databases where their
populations are identical.

Read-only report preservation:

| Database    | SHA-256 before and after                                           | `user_version` before/after |
| ----------- | ------------------------------------------------------------------ | --------------------------: |
| Mixed       | `b94352c3a34a8db774046c90e4567c3809ce759c6491139c8e82521212220332` |                       9 / 9 |
| Empty       | `d3ad7d0822fa708c2b34d3018f4a18efdfe3465b10ec888bc2018430c4ece1e4` |                       9 / 9 |
| All known   | `cd58fcebba165d629d99579c1697c6cd7d1ec469a0d35a19e02459f2e568cf93` |                       9 / 9 |
| All unknown | `60de6dfb155040b0170bcf9813b8706735d905020b38ff4850e161ec556b8792` |                       9 / 9 |

## Tests and public contracts

Final focused commands, each with incoming `KTX_DB` unset and disposable state:

```sh
go test ./internal/dbreport -run '^Test(EmptyReportGoldenAndReadOnly|ReportAggregatesProductEvidence|ReportOutcomeMetrics|ReportOutcomeStorageClasses)$' -count=1
go test ./internal/dbreport -count=1
python3 -m py_compile .agentera/verification/tool_outcome_integrated_check.py
```

All focused checks passed. DB coverage includes integer 0/1 and malformed
integer, exact REAL 0.0/1.0, REAL 0.5, ordinary text, exact text `"0"`/`"1"`,
blob, and `NULL`. The direct SQLite fixture asserts each storage class before
report generation and checks summary, coverage, tool, agent, and daily report
surfaces. The retained script compiled successfully. The integrated CLI checks
prove both JSON and text summary/breakdown remain available, preserve totals and
formulas, and agree with database reporting when the declared populations are
identical. The full check below covered existing ingress, routing,
failure-passthrough, report, first-authority, documentation, and help checks.

The final full command was:

```sh
env -u KTX_DB HOME="$FRESH_ROOT/home" XDG_DATA_HOME="$FRESH_ROOT/data" \
  XDG_CONFIG_HOME="$FRESH_ROOT/config" XDG_CACHE_HOME="$FRESH_ROOT/cache" \
  XDG_STATE_HOME="$FRESH_ROOT/state" XDG_RUNTIME_DIR="$FRESH_ROOT/runtime" \
  GOCACHE="$FRESH_ROOT/go-build" GOMODCACHE="$(go env GOMODCACHE)" mage check
```

Result: PASS across all packages, vet, lint, and build. Lint reported `0 issues`.
The database-boundary audit reported 955 attempts and operations,
`forbidden=0`, external caches, and `ktx_db=unset`. The post-check local binary
hash remained the corrected hash above.

## State and limitation

Correction task `nypdistlxh` remains in progress without evaluation. This
bounded correction does not self-approve the required independent re-review and
does not close the task, TODO item, or plan. Final completion still requires an
independent evaluator to assess the corrected implementation and retained
evidence.
