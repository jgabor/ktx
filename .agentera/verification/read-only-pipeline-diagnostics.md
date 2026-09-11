# Read-only pipeline diagnostics verification

Date: 2026-09-11. Plan `znipvgptpr`, task `zordiuymzk`, TODO `lghvhvsvyb`.

## Delivered behavior

Doctor distinguishes absent, valid, invalid, and inaccessible pipeline
configuration. Ordered route checks include all supported predicates, target
stages, and the default fallback. Static stage checks report unknown tokens and
invalid built-in arguments; plugin checks report shadowing, duplicate names,
and the effective last-writer-wins definition. Plugin overrides own argument
handling. Neither stage application nor plugin command probes execute.

Existing JSON/text envelopes, status and exit conventions, classified-failure
precedence, and runtime routing remain unchanged. Missing configuration and
empty pipelines are supported passthrough. Current-schema database inspection
and configuration inspection remain read-only. Supported older databases retain
automatic readiness, and missing databases are not created.

## Verification

All database-backed verification used explicitly disposable state, with ambient
KTX_DB unset and new HOME/XDG application roots for each invocation. Go/tool
caches were reused separately; no ambient live database was accessed.

| Check | Result |
| --- | --- |
| `go test ./internal/cli ./internal/config ./internal/pipeline/... -run 'Doctor\|Config\|Registry' -count=1 -v` | PASS; permission test executed. The filter selected no pipeline-package tests; the full check covers those packages. |
| `mage build` | PASS; local `build/ktx` produced. |
| `mage check` | PASS: Go tests, 14 Python verifier tests, generated-adapter overlap gate, vet, lint. Database audit: 17,969 attempts, zero forbidden accesses. |
| Local binary, absent config and missing database, JSON/text | Expected exit 4 with independent pipeline findings; no application directories created. |
| Local binary, current database and duplicate `head` plugins, JSON/text | Degraded, exit 0; final plugin wins, `head:abc` is accepted as an external token, unknown stage warns. |
| Local binary, current database and invalid TOML, JSON/text | Error finding, exit 4; one stdout report and no duplicate stderr envelope. |
| Current-database smoke snapshots and sentinel commands | All four invocations preserved bytes and directory entries; no plugin executed. |
| Implementation audit | No blocking findings; no correction required. |

Regression cases live in `internal/cli/doctor_pipeline_test.go`, with the
existing schema/access-audit matrices retained in `doctor_test.go`. They cover
configuration errors, predicate visibility, token parsing and overrides,
deterministic output, read-only snapshots, and security/readiness precedence.

## Local evidence and audit provenance

- Implementation verification index: `/tmp/opencode/ktx-doctor-verification.md`.
- Full passing check: `/tmp/opencode/ktx-doctor-check.HaBxYi/check.log`.
- Focused tests: `/tmp/opencode/ktx-doctor-focused.65D2xy/focused.log`.
- Coordinator smoke driver: `/tmp/opencode/ktx-doctor-coordinator-smoke.py`.
- Coordinator smoke result and stdout/stderr files:
  `/tmp/opencode/ktx-doctor-coordinator-wxbb7hij/result.json` and siblings.
- Read-only audit session: `ses_f6e9c6bf4ffetoLafTbKOadLA4`. Reviewed implementation,
  tests, docs, and passing logs; no additional implementation test run or mutation.

These temporary paths identify this local verification, not permanent release
archives. Earlier implementation attempts included compile, audit-boundary,
and formatting failures that were fixed, plus one incomplete full-check attempt
that reached the shell timeout. The index retains those logs. The first
coordinator seed named a nonexistent session and was rejected before doctor ran;
the corrected disposable fixture and all four doctor cases passed.

## Limits

This is static inspection, not plugin operational qualification, route-use
measurement, a schema release, or decision-satisfaction confirmation. No paid
benchmark or production-scale schema gate was needed. No installation, commit,
push, or release was performed. Agentera's changelog scanner reports an
incomplete release boundary for the existing Unreleased-only changelog; this
cycle adds an Unreleased entry without inventing release history.
