# Benchmarks and Repository Development

This derived task summary is not a canonical contract. Project memory owns
intent and scope, implementation and tests establish current behavior, and
public docs/help must remain synchronized contract surfaces. Use this reference
for retained experiments, replay, build/test commands, and repository-safe
engineering work.

## Common Benchmark Workflows

| Minimal interface                                                                | Database and other effects                                                                                                                                                                 |
| -------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `ktx bench run <agent> <model> <task>`                                           | Write-capable; runs an agent and can use network/model calls and incur provider charges.                                                                                                   |
| `ktx bench run --replay <session-id>`                                            | Write-capable creation/migration open; no agent/model calls.                                                                                                                               |
| `ktx bench run --replay-file <path>`                                             | DB-free, offline retained-file verification; no model calls.                                                                                                                               |
| `ktx bench report [<id>] [--spec <id>]`                                          | Genuinely read-only database query.                                                                                                                                                        |
| `ktx bench export [--output <path\|->] <session-id>`                             | Read-only source database; writes a file when `--output` names a path, otherwise writes fixture bytes to stdout.                                                                           |
| `ktx bench compare --preflight --format json`                                    | DB-free; returns before task loading, makes no model/provider calls, and creates no experiment artifact. It inspects the existing benchmark image.                                         |
| `ktx bench compare --preflight --build --format json`                            | DB-free and makes no model/provider calls or experiment artifacts, but `--build` mutates Docker image/build-cache state and may use the network to fetch base-image and dependency inputs. |
| `ktx bench compare --model <model> [--variant <name>] --task <id> ...`           | Creates retained experiment files and fresh isolated owner databases; uses network/model calls and can incur provider charges.                                                             |
| `ktx bench screen --population <path> --config <path>... --anchor-config <path>` | DB-free mechanical configuration screening; reads named files and makes no model calls. External plugins are rejected.                                                                     |
| `ktx bench rank --model <model> [--variant <name>] --task <id> ...`              | Creates retained experiment files and fresh isolated owner databases; uses network/model calls and can incur provider charges.                                                             |
| `ktx bench list`                                                                 | Lists experiments from the fixed `.ktx/experiments` root.                                                                                                                                  |
| `ktx bench show <id>`                                                            | Reads one experiment manifest by ID from the fixed root.                                                                                                                                   |
| `ktx bench validate <id>`                                                        | Read-only retained-artifact validation by ID; does not rewrite the archive.                                                                                                                |
| `ktx bench agent --capabilities`                                                 | DB-free offline capability report; no model calls.                                                                                                                                         |
| `ktx bench evidence preflight --spec <path> [--output <path>]`                   | DB-free, no-network preflight; an output path writes a report file.                                                                                                                        |
| `ktx bench evidence gemini-preflight --spec <path> [--output <path>]`            | DB-free, no-network preflight; an output path writes a report file.                                                                                                                        |
| `ktx bench corpus generate [--output <directory>]`                               | DB-free but writes generated corpus files.                                                                                                                                                 |
| `ktx bench summarize <id>`                                                       | Does not run model calls or write the ktx database, but rewrites derived `summary.json` evidence in the retained experiment.                                                               |

DB-free means only that the command does not open the ktx database. It does not
mean filesystem-safe, network-free, side-effect-free, or free of provider cost.

Named methodologies, tasks, pipelines, and corpus fixtures live under
`tools/bench/`.

## Live Comparison

Run offline preflight before paid work. Normal comparison runs preflight too.
Current prompt-cache comparisons use methodology v6 and emit summary schema 5
under `cache-reuse-v2`. Claims about provider-internal behavior remain forbidden.
Methodology v5, summary schemas 1–4, and `cache-reuse-v1` remain historical
evidence rather than the current entry point.

```bash
./build/ktx bench compare --preflight --format json

./build/ktx bench compare --preflight --build --format json

./build/ktx bench compare \
  --build \
  --spec prompt-cache-token-hit-rate \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task crossfile-nav \
  --cache-control observed \
  --warmup 1 \
  --rounds 5
```

Provider policy: do not run paid/live experiments with `github-copilot/*`.

The generic preflight returns before task loading. It requires no authentication
or model call and creates no experiment artifact. The `--build` form adds local
source checking and benchmark-image creation before capability attestation.

Provider-internal isolation is not currently supported. `--cache-control
isolated` fails before an agent call, while `auto` records `observed`. No built-in
OpenCode runtime/provider adapter proves arm isolation.

Ordinary runtime telemetry is optional and fail-open. Required benchmark
collection is fail-closed: one explicit attempt owner, complete interval,
contiguous acknowledged callback order, stable identity, and consistent wrapper
and event-hook projections are validation gates.

Recovery-aware comparisons use methodology v7 and current summary schema 7;
complete schema-6 archives remain historical byte-exact evidence. Recovery
requires a delivered cue and later exact `ktx read <source-id>` linkage under
the same attempt owner. Older required-event contracts do not establish cue
absence as measured zero; those recovery fields are unavailable and unranked.

A cache-aware comparison separates cold observation, warm-up, and measured-warm
attempts. Only protocol-valid measured pairs support direct comparative claims.
Failed or invalid archives remain failed history and must not inherit claims from
a later successful rerun.

## Retained Evidence

Experiments normally retain:

- `manifest.json`: task, runtime, model, pipeline, methodology, image, repository,
  and pricing provenance.
- `attempts.jsonl`: one normalized attempt record per line.
- `summary.json`: deterministic derived summary.
- `pricing.json`: retained price snapshot for estimates, not telemetry.
- Per-attempt streams, stderr, database, session snapshots, and workspaces.

Validate without rewriting the archive:

```bash
ktx bench validate <experiment-id>
```

Treat `valid`, `summary_byte_match`, target counts, attempt checks, complete pair
counts, and methodology reasons as separate gates. A successful task answer does
not make invalid telemetry valid.

Treat the entire retained experiment directory as sensitive. Workspaces may
contain a full repository snapshot; databases, streams, stderr, and session
snapshots can contain prompts, outputs, identifiers, or secrets. Create and
review a derivative for sharing rather than modifying authoritative retained
evidence.

## Replay

Database replay requires a recorded stream:

```bash
ktx run --record-stream opencode run --format json "prompt"
ktx bench run --replay <session-id>
```

Replay makes zero agent calls, but database replay uses the normal
creation/migration-capable database open. Confirm the target, preferably through
an explicit `KTX_DB`, before running it. Informational usage/cost/cache drift can
exit zero; integrity or parser failure exits four.

Portable strict replay:

```bash
: "${KTX_OWNED_DB:?set an explicitly operator-owned database path}"
: "${KTX_TARGET_SESSION_ID:?set a benchmark-parented session ID}"
KTX_DB="$KTX_OWNED_DB" ktx bench export --output replay-v1.json "$KTX_TARGET_SESSION_ID"
ktx bench run --replay-file replay-v1.json
```

`--replay-file` is DB-free and uses only the fixture plus a controlled built-in
pipeline; this is the safer verification path when no database comparison is
needed.

Replay files include raw stream content. Review for secrets, personal data,
machine identifiers, and repository content before sharing or committing.

## Benchmark Claims

- Runtime-reported cost and price-derived API-equivalent cost are separate.
- Full-pass quality and cost/context movement must be reported together.
- Cold/warm-up setup evidence is separate from measured-warm headline evidence.
- Cached-input share is descriptive runtime evidence, not significance,
  causality, provider cache behavior, or generic cache effectiveness.
- Fewer than five measured attempts per side is exploratory under the current
  cache-aware methodology.
- Do not pool incompatible tasks, runtime versions, or invalid archives.

## Build and Verification

```bash
mage build      # build build/ktx
mage check      # test + vet + lint + build
mage verify     # check + vulnerability scan + portable replay gate
mage install    # install to GOPATH/bin
```

Without Mage:

```bash
go build -o build/ktx ./cmd/ktx
```

Never run bare `go build ./cmd/ktx` in the checkout.

`mage verify` is the authoritative local/CI gate. It runs test, vet, lint,
build, `govulncheck`, and the portable replay gate. It requires both
`golangci-lint` and `govulncheck` and enforces the ambient-live database
boundary.

For optional extended engineering evidence, run
`KTX_ENGINEERING_EVIDENCE_DIR=<new-directory> mage engineeringVerify` with a
new directory. It retains reports plus a hash manifest and requires load
correctness, exact reconciliation, and supported-lock checks while keeping
timing descriptive; it does not replace `mage check`.

Schema releases require a separate production-scale gate. Prepare the
disposable 3.2 GB fixture with `tools/schema_readiness_evidence.py prepare` as
documented in `docs/schema-readiness.md`, then run:

```bash
env -u KTX_DB \
  KTX_SCHEMA_RELEASE_WORK_ROOT="$root/work" \
  KTX_SCHEMA_RELEASE_REPORT="docs/notes/schema-readiness-release.json" \
  mage schemaReleaseGate
```

This is intentionally outside `mage check`. The named report becomes current
passing authority only on a complete pass. Its `.attempt.failed.json` sibling
is the latest failed attempt and never passing authority.

## Focused Manual Verification

If bypassing Mage, allocate a unique disposable application root for each outer
invocation, unset incoming `KTX_DB`, and set distinct HOME and XDG paths. Never
run tests, doctor, or migration rehearsal against the ambient live database.

Migration rehearsal must use an explicitly seeded temporary copy. `KTX_DB`
alone does not prove that a path is disposable.

## Repository Workflow

Before modifying behavior, read `AGENTS.md` and follow its declared read order
and documentation-maintenance procedure in full. Then inspect existing tests and
public docs and keep changes small, deterministic, stdlib-first, and reversible.

Do not implement an unresolved architectural choice by guessing. Record or run
the required discussion task instead.

## Documentation Synchronization

When public behavior changes, check all affected surfaces:

- Command help and exit codes.
- `README.md` quick starts.
- `ktx.example.toml` comments and examples.
- `docs/pipelines.md` contracts.
- `docs/database-safety.md` command classification.
- `docs/opencode-telemetry.md` adapter/event semantics.
- This skill and its references.

Prefer generated or validated contracts over duplicated prose when the project
provides one. Keep project-tracking labels out of shipped code, help, errors,
fixtures, and user-facing configuration.
