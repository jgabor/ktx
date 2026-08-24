# ktx

ktx is a runtime-agnostic CLI for measuring and reducing coding-agent context
cost. It is one Go binary: no daemon and no library integration requirement.

- **Wrap any agent** â `ktx run` records any CLI process as a session, no
  agent changes needed.
- **Compress in flight** â OpenCode and Codex adapters shrink tool output
  before it reaches the model; originals stay recoverable.
- **Measure before trusting** â per-tool-call token statistics, offline
  replay, and cache-aware live comparisons show whether compression saves
  context.

Recorded content stays on your machine. Outbound requests happen only on a
cold pricing-cache miss (a [models.dev](https://models.dev) catalog fetch) or
when you explicitly run a live benchmark against a named provider.

## Install

Building from source requires Go 1.26 or newer.

```bash
go install github.com/jgabor/ktx/cmd/ktx@latest
```

Build from a checkout with `mage build`. This produces `build/ktx`. Without
Mage, use `go build -o build/ktx ./cmd/ktx`.

## Quick start

### Wrap a process

```bash
: "${KTX_RUNTIME_SHA256:?set this to the verified binary-capabilities digest}"
ktx --expect-runtime-sha256 "$KTX_RUNTIME_SHA256" run opencode run --format json "What is 2+2?"
ktx --expect-runtime-sha256 "$KTX_RUNTIME_SHA256" list --format text
: "${KTX_TARGET_SESSION_ID:?set this to a session ID returned by list}"
ktx --expect-runtime-sha256 "$KTX_RUNTIME_SHA256" get "$KTX_TARGET_SESSION_ID" --format text
```

`ktx run` prints the wrapped process output, then a one-line session summary.
`ktx list` shows recorded sessions:

```text
ID                AGENT    STARTED                         ENDED                TOOL_CALLS  TOKENS  COST  MODEL
vhlttrsjwnahnrlm  /bin/sh  2026-08-23T14:32:19.140759196Z  2026-08-23T14:32:19  0           -       -     -
```

`ktx get` prints one session's details; `ktx stats` ranks tool calls by
delivered tokens once adapters have recorded them.

### Compress tool output in flight

Install the adapter for your runtime:

```bash
ktx init opencode
# or
ktx init codex
```

Copy [`ktx.example.toml`](ktx.example.toml) to
`$XDG_CONFIG_HOME/ktx/ktx.toml`, then inspect the active configuration:

```bash
ktx config show --json
```

The adapter calls `ktx hook` after each tool execution: ktx persists the
original, runs the selected pipeline, and returns the smaller result to the
agent. With no configuration file, the pipeline is passthrough.

Lossy profiles can opt into `recover_footer = true`. When compression succeeds
and the result plus footer remains smaller than the original, the agent
receives a recovery cue such as:

```text
[ktx] output compressed: 2815 bytes (56% reduction) â¢ get full output with `ktx read ugrpgrcvdrbhbaby`
```

Originals and compressed artifacts remain stored separately; the footer is not
part of the persisted compressed bytes. KTX assigns its own stable source IDs
to persisted tool calls; runtime session and tool-call identifiers are
correlation inputs, not record IDs. Exact retries converge on the first
persisted artifact and cannot replace it. Use source IDs from inspection or a
recovery cue with commands such as `ktx read` and `ktx compress`.

The generated OpenCode plugin also records optional runtime model-step token
evidence. ktx collects telemetry only from the agent runtime; provider and
model identity come from runtime events or explicit runtime configuration,
with provenance. See the [OpenCode integration and telemetry
runbook](docs/opencode-telemetry.md) for plugin locations, both hooks,
wrapper coexistence, refresh, offline verification, claim boundaries, and
troubleshooting.

## Commands

| Command                   | Purpose                                                                                                                |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `run`                     | Wrap an invocation and record the session                                                                              |
| `hook`                    | In-flight tool-output compression, called by runtime adapters                                                          |
| `list` / `get`            | List sessions; query one by ID (formatted)                                                                             |
| `stats`                   | Per-tool-call statistics ranked by delivered tokens                                                                    |
| `read`                    | Fetch a persisted record by ID (raw JSON)                                                                              |
| `compress` / `decompress` | Compress a record's output; reverse it verbatim                                                                        |
| `init`                    | Install a runtime adapter                                                                                              |
| `plugin`                  | Manage compression plugins                                                                                             |
| `config`                  | Inspect active pipeline configuration                                                                                  |
| `db`                      | Database path, reporting, and maintenance (`db migrate`)                                                               |
| `telemetry`               | Record and inspect optional model-step token evidence                                                                  |
| `prices`                  | Manage the local models.dev pricing cache                                                                              |
| `write`                   | Persist a tool-call record from sidecar input                                                                          |
| `doctor`                  | Check ktx health                                                                                                       |
| `binary-capabilities`     | Print the installed build's path, SHA-256, version, and max supported database schema; used to pin a binary in scripts |
| `bench`                   | Benchmark runs, experiments, and reports                                                                               |

Run `ktx <command> --help` for details.

## Configuration and data

The default database is `$XDG_DATA_HOME/ktx/ktx.db`; set `KTX_DB` to select a
different database for normal commands. Within the `ktx db` namespace,
`--path <file>` is a per-invocation override and takes precedence over
`KTX_DB`.

Pipeline configuration lives at `$XDG_CONFIG_HOME/ktx/ktx.toml`. Configured
pipelines are ordered deterministic stages. Built-ins include `unwrap-result`,
`strip-ansi`, `normalize`, `keep-ends`, `token-budget`, and `replace`. Named
profiles can be selected through first-match `[[route]]` rules using tool name,
command prefix, subcommand, and exit status.

See [`ktx.example.toml`](ktx.example.toml) for a working configuration and
[`docs/pipelines.md`](docs/pipelines.md) for stage, routing, and benchmark
guidance. Originals remain addressable through `ktx read` and `ktx
decompress`. For the optional structured diagnostic file sink, see
[`docs/logging.md`](docs/logging.md).

Database access is forward-only. Every database-backed command automatically
prepares a supported older schema before running. Query and inspection commands
use enforced read-only SQLite access after readiness; an absent database keeps
its logical-empty or missing-input behavior. A database newer than the binary
fails before writes and recovers only by installing a compatible binary. The
[database safety contract](docs/database-safety.md) defines command classes,
hermetic development, seeded migration rehearsal, and the readiness boundary.

Access to the ambient default database also requires the verified executable
digest on each invocation:

```text
ktx --expect-runtime-sha256 <digest-from-binary-capabilities> <database-command>
```

Explicit `KTX_DB` and `db --path` targets are exempt. Generated OpenCode and
Codex callbacks pass the digest for their pinned binary automatically and add no
outer timeout.

## Benchmarking

Benchmark stages, task loading, scoring, and summaries run as Go code inside
the ktx binary; comparisons need no external scripts or separate evaluator
runtime. Named
pipelines, tasks, methodologies, and corpus fixtures live under
[`tools/bench`](tools/bench).

The current opt-in comparison uses methodology v6, summary schema 5, and
`cache-reuse-v2` to report the token-weighted prompt-cache hit rate under the
pinned contract. It does not authorize provider-request or provider-internal
cache claims.

```bash
./build/ktx bench compare --preflight --build --format json

./build/ktx bench compare \
  --build \
  --methodology tools/bench/methodology-v6-prompt-cache-token-hit-rate.yaml \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task-file tools/bench/tasks/crossfile-nav-v1.toml \
  --cache-control observed \
  --warmup 1 \
  --rounds 5
```

Preflight returns before task loading. It makes no model call, requires no
authentication, and creates no experiment artifact; with `--build` it first
builds the image from local source. Every normal comparison runs the same
gate before its first attempt, requiring attested image identity, plugin
digest, attempt ownership, and callback and interval integrity before auth
readiness. Live runs accept any provider except `github-copilot/*`.

Experiments retain manifests, attempts, pricing snapshots, and deterministic
summaries. Validate retained evidence with:

```bash
ktx bench validate --experiment .bench-data/<experiment-id>
```

### Offline replay

Replay a database capture by session ID without another agent call:

```bash
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to replay}"
ktx bench run --replay "$KTX_TARGET_SESSION_ID"
```

For strict, portable verification, export a benchmark-parented capture and
replay the resulting file without the source database:

```bash
: "${KTX_OWNED_DB:?set this to an explicitly operator-owned database path}"
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to export}"
KTX_DB="$KTX_OWNED_DB" ktx bench export --output replay-v1.json "$KTX_TARGET_SESSION_ID"
ktx bench run --replay-file replay-v1.json
```

Exit classes: informational cost/cache drift exits 0; integrity or parser
failure exits 4; a missing session exits 2. File replay uses only the fixture
and a controlled built-in pipeline; any divergence is a strict failure.
Fixture contents include raw recorded stream data, so review them for secrets
and personal data before sharing. The fixture schema, refresh requirements,
and threat boundary are in the [runbook](docs/pipelines.md).

### Retained evidence

Historical results are evidence, not supported workflows:

- A retained OpenCode 1.18.5 Luna/high comparison passed validation with five
  complete measured pairs; candidate cached-input share was higher than
  baseline under the retained protocol. This supports no significance, causal,
  or provider-native cache claim. Values, provenance, and failed archives:
  [live validation report](docs/cache-reuse-live-validation.md).
- One historical ten-attempt runtime compression telemetry comparison:
  [report](docs/runtime-compression-telemetry-evidence.md).
- The completed external campaign (eight attempts, four pairs): historical
  evidence with limits and ownership map:
  [campaign report](docs/skillsbench-campaign.md).
- The Layer 1 cache and cost benchmark, retained for provenance:
  [benchmark](benchs/layer-1-cache-cost.md).

A rolling 24-hour live snapshot (July 17, 2026, 8,321 tool calls) reduced
delivered tool-output context from 47.8 million to 26.0 million characters,
about 5.47 million tokens avoided under a four-characters-per-token estimate.
At the models.dev uncached-input price for `openai/gpt-5.6-sol`, that is about
$27.33 of gross input cost avoided. This is a model-priced estimate, not a
billing claim, and aggregate runtime counts cannot establish provider-internal
cache hits, misses, fills, eviction, or prefix composition.

## Development

Development and CI verification must not resolve an ambient live database.
The standard Mage verification targets automatically allocate a per-invocation
disposable `HOME` and XDG root, unset `KTX_DB`, audit database access attempts,
and keep reusable language and tool caches separate. The
[database safety contract](docs/database-safety.md) defines the boundary.

```bash
mage replayFixture # bounded offline gate for the retained portable fixture
mage verify        # authoritative local and CI verification
mage build         # build build/ktx
```

For optional extended engineering evidence, run
`KTX_ENGINEERING_EVIDENCE_DIR=<new-directory> mage engineeringVerify` with a
new directory. It retains component, real-process load, and blocked-decompression
reports in a hash manifest and requires correctness, exact reconciliation, and
supported-lock checks. It does not add a timing gate.

Schema releases have a separate production-scale gate. First prepare the
disposable 3.2 GB fixture as documented in
[`docs/schema-readiness.md`](docs/schema-readiness.md#readiness-release-budget),
then run:

```bash
env -u KTX_DB \
  KTX_SCHEMA_RELEASE_WORK_ROOT="$root/work" \
  KTX_SCHEMA_RELEASE_REPORT="docs/notes/schema-readiness-release.json" \
  mage schemaReleaseGate
```

This gate is not part of `mage check`. The named report is current passing
authority only after a complete pass; the latest failed attempt uses
`docs/notes/schema-readiness-release.json.attempt.failed.json` and never replaces
passing authority.

`mage verify` runs `mage check` (test, vet, lint, and build), `govulncheck`,
and the portable replay gate. Tracked CI runs this same command on every push
and pull request. Verification requires both `golangci-lint` and
`govulncheck`.

## Documentation

| Document                                                                     | Covers                                                     |
| ---------------------------------------------------------------------------- | ---------------------------------------------------------- |
| [`docs/pipelines.md`](docs/pipelines.md)                                     | Stages, routing, benchmarks, replay fixture contract       |
| [`docs/opencode-telemetry.md`](docs/opencode-telemetry.md)                   | Adapter install, hooks, telemetry runbook                  |
| [`docs/database-safety.md`](docs/database-safety.md)                         | Command classes, hermetic development, migration rehearsal |
| [`docs/logging.md`](docs/logging.md)                                         | Optional structured diagnostic file sink                   |
| [`docs/binary-capabilities.md`](docs/binary-capabilities.md)                 | Binary identity and schema capability contract             |
| [`docs/component-benchmarks.md`](docs/component-benchmarks.md)               | Component-level measurements                               |
| [`docs/critical-path-performance.md`](docs/critical-path-performance.md)     | Critical-path performance contract                         |
| [`docs/cache-reuse-metric-contract.md`](docs/cache-reuse-metric-contract.md) | Cache reuse metric definition                              |

Additional engineering reports (load measurements, external validation,
candidate qualification, cache-evidence studies) live under
[`docs/`](docs/). Architectural intent is documented in
[`ARCHITECTURE.md`](ARCHITECTURE.md); repository working conventions are in
[`AGENTS.md`](AGENTS.md).
