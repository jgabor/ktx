---
name: ktx
description: >-
  Operate, configure, troubleshoot, analyze, benchmark, or develop
  github.com/jgabor/ktx, the coding-agent context measurement and compression
  CLI. Use for its OpenCode or Codex adapters, agent wrapper, pipelines,
  recovery, stats, live telemetry, SQLite database, benchmarks, or repository.
  Load before searching implementation source. Do not use for Android/Kotlin
  KTX libraries or Khronos KTX texture files.
---

# ktx

ktx is one Go binary with no daemon or library integration requirement. It
observes coding-agent tool calls, stores evidence in SQLite, optionally reduces
tool output through deterministic pipelines, preserves originals for recovery,
and reports live or benchmark measurements.

Linux is the supported operating system. Non-Linux execution is unsupported or
unverified; platform-specific source and historical compilation results do not
establish native qualification. The terminal-only capability floor is below.

## Operating Method

1. Classify the request before running a command:
   - Operator task against explicitly requested live data.
   - Read-only inspection of an explicit database or retained experiment.
   - Write-capable collection, compression, migration, or live benchmark.
   - Repository development or verification.
2. Resolve the executable and data scope:
   - In this checkout, build with `mage build` and invoke `./build/ktx`.
   - For installed operator use, invoke `ktx`.
   - Print the currently resolved database path with `ktx db path`; it honors
     `KTX_DB`.
   - Set `KTX_DB="$KTX_OWNED_DB"` only after preconfirming the target for
     non-`db` commands. The `db` namespace additionally accepts
     `ktx db --path "$KTX_OWNED_DB" <subcommand>`.
3. Prefer structured, bounded output:
   - Use `--format json` for automation and exact analysis.
   - Use `--format text` for human inspection.
   - Raw I/O verbs such as `read`, `write`, `compress`, and `decompress` have
     command-specific output and do not accept a normal format flag.
4. Use `ktx <command> --help` for documented public syntax not covered here.
   Some current help surfaces omit nested behavior; use the focused references
   and project docs before reading source.
5. Preserve evidence boundaries. Unknown is not zero, no artifact is not
   automatically failure, and an aggregate is not proof of provider behavior.

Persisted tool outcomes are `is_error=false` success, `is_error=true` failure,
and NULL or an invalid stored value unknown. `exit_code` is diagnostic after
persistence.
Success routing requires explicit success; explicit failure bypasses the
pipeline unchanged; unknown may use `any` or default behavior but is not
success. OpenCode and Codex authority is version-scoped, flat sidecar
`is_error` is a caller assertion, labels do not strengthen authority, and
historical unsupported evidence remains unknown.

For `ktx write`, missing `is_error` persists as unknown. Non-boolean `is_error`
is malformed and rejected before persistence.

## Fast Paths

### Install or build

```bash
go install github.com/jgabor/ktx/cmd/ktx@latest

# In this checkout:
mage build
./build/ktx --version
```

Never run bare `go build ./cmd/ktx` in this repository; it leaves a binary in
the repository root. Use `mage build` or
`go build -o build/ktx ./cmd/ktx`.

### Install a runtime adapter

```bash
ktx init opencode
ktx init opencode --show

ktx init codex
ktx init codex --show
```

Use `--project` to scope installation. OpenCode uninstall checks both global and
project candidate files; Codex uninstall honors the selected global or project
scope. Both remove only ktx's adapter entries. After installing the OpenCode
plugin, restart OpenCode. After installing the Codex hook, run
`/hooks` in Codex to trust it.

### Wrap an agent and inspect the session

```bash
ktx run opencode run --format json "What is 2+2?"
ktx list --format text
ktx get <session-id> --format json
```

Add `--record-stream` when later database replay is required. `ktx run` forwards
child stdout in chunks with ScanLines-compatible normalization: CRLF becomes
LF, a trailing CR is removed, and output without a final newline gains one.
This is not byte-exact passthrough. Lines above 64 MiB are fully forwarded but
skipped for event parsing with a warning. The wrapper exits with the child's
exit code unless forwarding or collection fails. Forwarding failure returns
exit 4, stops the direct child, and publishes no incomplete stream recording;
already persisted evidence remains.

Terminal-mode wrapping on Linux requires readable procfs, subreaper support,
`pidfd_open`, and `waitid(P_PIDFD)` (normally Linux 5.4+, subject to backports and
syscall policy). Unavailable preparation returns an explicit error before the
child starts, including with redirected stdin and a controlling terminal.
Non-terminal runs and unrelated commands do not acquire this requirement.
See [terminal cancellation](references/cli.md#terminal-cancellation) for owned
descendant cleanup, caller preservation, and interrupted evidence behavior.

### Inspect live context reduction

```bash
ktx stats --limit 10 --format text
ktx stats summary --window 72h --format json
ktx stats breakdown --window 72h --format json
ktx stats live-validation --from <UTC> --to <UTC> --collection <id> --ingress run --candidate keep-ends:73:1 --format json
```

`stats` ranks residual delivered context. `stats summary` is the compact,
versioned report over one interval. `stats breakdown` reports bounded,
token-weighted delivery groups by safe command dimensions, profile, route,
disposition, and output-size band; it omits model-step telemetry. Tool-call/
artifact and model-telemetry views are queried independently without cross-view
transactional consistency; they may originate from the same wrapper stream or
from separate adapter callbacks. Summary `prompt_cache_token_hit_rate_pct` is
runtime-reported cache-read token evidence in percentage units, not a provider
request hit rate. It pools all accepted providers and models under the supported
runtime contract by token volume rather than averaging their percentages.

`stats live-validation` is a schema-14, read-only report for one explicit
OpenCode collection and half-open UTC window. It validates the pinned controlled
candidate artifacts and fails closed on missing delivery, disposition, recovery,
run closure, latency, or continuity authority. A blocked report is still emitted
and exits 3. Do not run the explicit collection or migrate an operator database
without separate authorization.

### Inspect records and model telemetry

```bash
ktx list --format json
ktx get <tool-call-or-session-id> --format json
ktx read <tool-call-or-session-id>
ktx telemetry list --session <session-id> --limit 1000 --format json
```

### Inspect health and paths

```bash
ktx db path
ktx doctor --format json
ktx db report --output -
ktx config show --format json
ktx plugin list --json
```

### Explicit reversible compression

```bash
compressed_id=$(ktx compress <tool-call-id> | jq -r .)
ktx decompress "$compressed_id"
```

`decompress` returns the original source output bytes, not the compressed bytes.
For in-context recovery from a hook footer, use the footer's
`ktx read <source-id>` command.

## Non-Negotiable Safety

- Every database-backed command automatically prepares a supported older schema.
  Query and inspection operations are read-only after readiness.
- Write-capable commands can create their selected database; readiness and
  writes use transactional forward migration.
- Development, tests, CI, migration rehearsal, and benchmark debugging must
  never access the ambient live database. Use a fresh disposable application
  root and unset incoming `KTX_DB`.
- Query the ambient live database only for an explicit operator request, and
  use read-only ktx commands or `sqlite3 -readonly`.
- `KTX_DB` selects a path; it does not prove that the path is disposable.
- Keep `ktx db migrate` for explicit diagnosis and disposable rehearsal; it is
  not a prerequisite for a supported older schema.
- If the database is newer than the binary, install a newer compatible binary.
  Never lower SQLite `user_version`.
- Replay fixtures and whole experiment directories are sensitive. Workspaces,
  databases, streams, stderr, and session snapshots may contain prompts,
  repository content, identifiers, and secrets. Review a derivative before
  sharing; do not rewrite authoritative retained evidence.

See [Database and telemetry](references/database-telemetry.md) for the command
classes, schema, metric formulas, and claim limits.

## Core Evidence Rules

- For benchmark cost selection, follow the
  [canonical cohort cost contract](../../../docs/pipelines.md#cohort-cost-selection).
  Prefer usable complete reported cost, otherwise a labeled reproducible
  whole-cohort API estimate. Never require invoice, subscription, access-mode,
  or upstream billing proof. Classify artifact validity, cost availability,
  target success, and recommendation eligibility separately. Unknown cost is
  not zero; use explicit current views, not historical fields, for this policy.

- Tool-output token values from `ktx stats` are estimates:
  `ceil(character_count / 4)` per call before aggregation.
- A compressed-artifact count is coverage, not a success rate. Calls without an
  artifact use raw output and may be intentional passthrough or anti-worsening.
- `prompt_cache_token_hit_rate_pct` is compact runtime-reported cache-read token
  evidence in percentage units. A measured zero stays zero; missing, malformed,
  and not-applicable values stay unavailable and are never reconstructed. It is
  token-weighted across all accepted providers and models, not a provider request
  hit rate.
- Missing, unknown, malformed, and measured zero are distinct states. Never
  reconstruct a missing field from neighboring totals.
- Originals are append-only. The first accepted source evidence and first
  persisted compressed artifact remain authoritative across exact retries.

## Reference Routing

The focused references are derived task summaries, not independent authorities;
the fast paths above are only an index. Read only the reference needed for the
task:

- [CLI workflows](references/cli.md): command map, output contracts, common
  recipes, paths, and exit behavior.
- [Pipelines](references/pipelines.md): stages, routing, recovery footers,
  plugins, identity, retries, and configuration examples.
- [Database and telemetry](references/database-telemetry.md): read/write safety,
  schema, stats semantics, cache accounting, and truthful live analysis.
- [Benchmarks and development](references/benchmarks-development.md): compare,
  validate, replay, retained artifacts, build, test, and repository workflow.

## Project Authorities

Before repository work, read `AGENTS.md` and follow its declared read order and
documentation-maintenance procedure in full. Project memory owns intent, scope,
recorded choices, and executable work. Implementation and tests establish
current behavior. Public documentation, command help, and examples are contract
surfaces that must stay synchronized with that behavior. This skill and its
references are derived task summaries; when they conflict with those sources,
investigate and correct the summary rather than treating it as canonical.

- `AGENTS.md`: repository rules, build commands, database safety, and benchmark
  policy.
- Project vision: product direction and principles.
- `ARCHITECTURE.md`: conceptual intent and unresolved questions.
- Recorded decisions: settled choices; do not infer omitted detail from a
  degraded listing.
- `TODO.md`: executable backlog; do not infer implementation work from prose in
  architecture documents.
- `README.md`: product quick start and supported public workflows.
- `ktx.example.toml`: complete commented pipeline and routing example.
- `docs/pipelines.md`: pipeline, stats, replay, and benchmark contracts.
- `docs/database-safety.md`: authoritative database command classes.
- `docs/opencode-telemetry.md`: OpenCode plugin and model-step contract.

## Common Mistakes

- Do not analyze retained benchmark experiments when the user asks for live `ktx stats` or the
  operator database. Benchmarks and product telemetry are different sources.
- Do not run tests or development commands against the ambient live database.
- Do not treat `prompt_cache_token_hit_rate_pct` as a provider request hit,
  cache-effectiveness, or billing result.
- Do not add runtime model-step cost/cache values to `tool_calls`; they have a
  separate evidence scope.
- Do not interpret missing error state as success or missing cost as zero.
- Do not assume every call should create a compressed artifact.
- Do not overwrite an installed OpenCode plugin manually; regenerate it with
  `ktx init opencode`, then restart OpenCode.
- Do not treat `ktx init ... --show` as proof that a runtime loaded the adapter;
  it reports files found on disk.
