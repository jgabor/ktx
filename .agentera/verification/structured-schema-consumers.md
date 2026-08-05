# Structured-schema consumer verification

Scope: plan `ldknsjplye`, task `tuevuojzxd`. This record persists the completed
Analyze inventory; it does not add a compatibility surface.

## Retained dependency inventory

- The live producers own the current contracts: stats summary schema 5 and
  breakdown schema 2 are constants in `internal/cli/stats.go:37-42`; their exact
  top-level and outcome keys are asserted in
  `internal/cli/stats_test.go:638-680`. Database report schema 3 is emitted in
  `internal/dbreport/report.go:19-44`, and its version, failure-metric keys, and
  reconciliation populations are asserted in
  `internal/dbreport/report_test.go:145-166`.
- The only prior-version dependencies were producer-owned tests and the
  deterministic empty database-report digest. The digest is consumed only by
  its producer test (`internal/dbreport/report_test.go:46-52`). Commit `3d4e6d5`
  advanced and tested the stats contracts; commit `5f7ec6b` advanced and tested
  the database report and updated
  `internal/dbreport/testdata/empty-report.golden.sha256`.
- The completed inventory found no retained stats JSON fixture and no shell,
  Python, JavaScript, or TypeScript script that invokes or parses `stats
summary`, `stats breakdown`, or `db report`. Contract-specific keys occur only
  in producer code/tests and human documentation, not in a downstream
  machine-readable artifact.
- `docs/pipelines.md:262-268,306-354` is stale human/public documentation. It is
  owned by later task `fckvcbciac`, not by this compatibility task.
  `.agents/skills/ktx/SKILL.md:95-100,121` and its references are operator
  guidance, not executable consumers.
- `tools/bench/methodology-v5-cache-aware.yaml:98` uses `summary_schema: 4` in
  the benchmark experiment-summary namespace. It is unrelated to the live
  stats `schema_version` contract.
- No runtime code parses prior report versions, selects a version range, emits
  legacy projections, or requires aliases. External consumers are unknown and
  explicitly out of scope.

## Verification and disposition

With `KTX_DB` unset and a fresh disposable `HOME`, every XDG root, and Go cache:

```text
go test -count=1 ./internal/cli ./internal/db ./internal/dbreport
ok github.com/jgabor/ktx/internal/cli       6.249s
ok github.com/jgabor/ktx/internal/db        31.724s
ok github.com/jgabor/ktx/internal/dbreport  0.091s
```

No retained executable consumer requires stats schemas 4/1 or database report
schema 2. Compatibility tests are therefore not applicable: there is no proven
parser for either a supported or incompatible-version case. Adding a projection,
alias, legacy mode, or speculative external bridge would weaken the closed new
contracts without preserving a proven consumer.

After independent evaluation, supersede `tuevuojzxd` with the completed
replacement tasks `ongnhqfzkf` and `zjqwiriduj`. Orchestration owns that
transition; this evidence commit intentionally leaves `tuevuojzxd` in progress.
