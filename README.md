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

Linux is the supported operating system. Non-Linux execution is unsupported or
unverified; retained platform-specific source and past cross-compilation are not
native qualification. See the [native Linux qualification](docs/native-linux-qualification.md)
for the tested host and evidence limits. The terminal-only capability requirement
is described below.

Building from source requires Go 1.26 or newer.

```bash
go install github.com/jgabor/ktx/cmd/ktx@latest
```

Build from a checkout with `mage build`. This produces `build/ktx`. Without
Mage, use `go build -o build/ktx ./cmd/ktx`.

## Quick start

### Wrap a process

```bash
ktx run opencode run --format json "What is 2+2?"
ktx list --format text
: "${KTX_TARGET_SESSION_ID:?set this to a session ID returned by list}"
ktx get "$KTX_TARGET_SESSION_ID" --format text
```

`ktx run` prints the wrapped process output, then a one-line session summary.
`ktx list` shows recorded sessions:

```text
ID                AGENT    STARTED                         ENDED                TOOL_CALLS  TOKENS  COST  MODEL
vhlttrsjwnahnrlm  /bin/sh  2026-08-23T14:32:19.140759196Z  2026-08-23T14:32:19  0           -       -     -
```

`ktx get` prints one session's details; `ktx stats` ranks tool calls by
delivered tokens once adapters have recorded them.

Terminal-mode `ktx run` is supported on Linux and requires readable procfs,
subreaper support, `pidfd_open`, and `waitid(P_PIDFD)` for safe descendant
cleanup. These capabilities are normally available on Linux 5.4 or newer,
subject to backports and syscall policy; the kernel version alone is not a
guarantee. If required preparation is unavailable, ktx returns an explicit
error before starting the wrapped child. This includes a controlling terminal
with redirected stdin. The requirement does not extend to non-terminal runs
or unrelated commands. See the [wrapper guide](.agents/skills/ktx/references/cli.md#terminal-cancellation)
for cancellation, terminal preservation, and evidence behavior.

### Optimize configuration without guessing

```bash
ktx optimize --format text                  # offline, no model calls or config writes
ktx optimize show <result-id> --format text
```

The report gives the captured target, tested scope, bounds, eligibility, reasons,
and next commands. JSON is the default. Offline generated cases can screen a
proposal, but cannot make it apply-ready. This version changes only
`pipeline.default.stages`; supplied route, logging, timeout/recovery, or other
profile changes block before paid evaluation. Built-in proposals preserve the
other configuration bytes and settings.

If you explicitly authorize paid controlled qualification, choose a permitted
model and have Docker, a compatible prebuilt `ktx-bench` image, and OpenCode auth:

```bash
ktx optimize --live --model provider/name --format text  # replace provider/name
ktx optimize apply <ready-result-id> --dry-run
ktx optimize apply <ready-result-id>
ktx optimize rollback <ready-result-id>
```

Live qualification uses embedded controlled assets, not representative user
workload. Its default bound is 24 benchmark agent launches, including setup and
retries, not provider requests or a bill cap. No configuration is auto-applied.
`ktx optimize --help` explains prerequisites, optional price snapshots, refusal
conditions, and exit codes. See [the optimization contract](docs/pipelines.md#configuration-optimization)
for the evidence and recovery limits.

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

| Command                   | Purpose                                                       |
| ------------------------- | ------------------------------------------------------------- |
| `run`                     | Wrap an invocation and record the session                     |
| `hook`                    | In-flight tool-output compression, called by runtime adapters |
| `list` / `get`            | List sessions; query one by ID (formatted)                    |
| `stats`                   | Per-tool-call statistics ranked by delivered tokens           |
| `read`                    | Fetch a persisted record by ID (raw JSON)                     |
| `compress` / `decompress` | Compress a record's output; reverse it verbatim               |
| `init`                    | Install a runtime adapter                                     |
| `plugin`                  | Manage compression plugins                                    |
| `config`                  | Inspect active pipeline configuration                         |
| `db`                      | Database path, reporting, and maintenance (`db migrate`)      |
| `telemetry`               | Record and inspect optional model-step token evidence         |
| `prices`                  | Manage the local models.dev pricing cache                     |
| `write`                   | Persist a tool-call record from sidecar input                 |
| `doctor`                  | Check ktx health                                              |
| `bench`                   | Benchmark runs, experiments, and reports                      |

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

## Benchmarking

Benchmark stages, task loading, scoring, and summaries run as Go code inside
the ktx binary; comparisons need no external scripts or separate evaluator
runtime. Named
pipelines, tasks, methodologies, and corpus fixtures live under
[`tools/bench`](tools/bench).

Screen complete configurations without a database, credentials, or model calls:

```bash
./build/ktx bench screen \
  --population tools/bench/evidence/configuration-screen-population-v1.json \
  --config tools/bench/pipelines/passthrough.toml \
  --config tools/bench/screen-configs/keep-ends-73-1.toml \
  --anchor-config tools/bench/screen-configs/keep-ends-100-50.toml \
  --format json
```

Repeat `--config` for each complete configuration. The JSON contains only
aggregate measurements, hashes, and opaque configuration identities. External
plugins are rejected so screening cannot send source content to another process.

The [canonical benchmark workflow](docs/pipelines.md#canonical-benchmark-workflow)
covers semantic-ID discovery, offline preflight, compare or rank, and the full
experiment lifecycle. Compare and rank require an explicit registered `--spec`;
focused command help lists all current spec, task, baseline, and candidate IDs.
Current prompt-cache comparisons use methodology v6, summary schema 5, and
`cache-reuse-v2`.

```bash
./build/ktx bench compare --preflight --build --spec prompt-cache-token-hit-rate --task crossfile-nav --format json

./build/ktx bench compare \
  --spec prompt-cache-token-hit-rate \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task crossfile-nav \
  --baseline passthrough \
  --candidate conservative-a \
  --cache-control observed \
  --warmup 1 \
  --rounds 5
```

Preflight returns before task loading. It makes no model call, requires no
authentication, opens no ktx database, and creates no experiment artifact.
With `--build`, it can use the network and changes Docker image and build-cache
state. Compare and rank create archives, use fresh isolated owner databases,
make network and provider calls, and can incur charges. Live runs accept any
provider except `github-copilot/*`.

Experiments retain manifests, attempts, pricing snapshots, and deterministic
summaries under `.ktx/experiments/<id>`. Use only the experiment ID:

```bash
ktx bench list
ktx bench show <experiment-id>
ktx bench validate <experiment-id>
ktx bench summarize <experiment-id>
ktx bench validate <experiment-id>
```

List, show, and validate are read-only archive inspection and do not open the
ktx database. Summarize rewrites derived `summary.json` but makes no provider
call and does not open the ktx database. Archives can contain repository
snapshots, prompts, outputs, streams, stderr, identifiers, databases, and
secrets. Review a derivative before sharing. Labeled historical reports remain
static evidence and have no current-validation guarantee.

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
| [`docs/component-benchmarks.md`](docs/component-benchmarks.md)               | Component-level measurements                               |
| [`docs/critical-path-performance.md`](docs/critical-path-performance.md)     | Critical-path performance contract                         |
| [`docs/cache-reuse-metric-contract.md`](docs/cache-reuse-metric-contract.md) | Cache reuse metric definition                              |

Additional engineering reports (load measurements, external validation,
candidate qualification) live under
[`docs/`](docs/). Architectural intent is documented in
[`ARCHITECTURE.md`](ARCHITECTURE.md); repository working conventions are in
[`AGENTS.md`](AGENTS.md).
