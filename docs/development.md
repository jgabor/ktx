# Developing and benchmarking ktx

This page is for people who build ktx from source, run its checks, or measure
compression with the built-in benchmarks. For everyday use, start with the
[README](../README.md).

## Build and verify

Build from a checkout with Mage. The binary lands in `build/ktx`:

```bash
mage build         # build build/ktx
mage verify        # the same checks CI runs
mage replayFixture # quick offline check of the retained replay fixture
```

Without Mage, use `go build -o build/ktx ./cmd/ktx`. Avoid a bare
`go build ./cmd/ktx`, which drops the binary in the repository root.

`mage verify` runs `mage check` (test, vet, lint, and build), `govulncheck`,
and the portable replay gate. CI runs this same command on every push and pull
request. You need both `golangci-lint` and `govulncheck` installed.

The checks also need Node and Bun. Tests run the generated OpenCode plugin on
Node 22.18 or newer (23.6 or newer on the 23 line), and the generated-adapter
gate runs it on Bun, which OpenCode uses to load plugins. Mage resolves both
executables before it isolates the run and passes them on as `KTX_TEST_NODE`
and `KTX_TEST_BUN`, so version-manager shims keep working. Set either variable
to an absolute path to pick a specific executable. Without Node, the plugin
tests are skipped; without Bun, `mage check` fails at the gate. If the first
`node` on `PATH` is older than 22.18, the plugin tests fail; set
`KTX_TEST_NODE` to a newer executable.

### Database isolation

Development and CI checks never touch your everyday ktx database. The standard
Mage targets give each run its own throwaway `HOME` and XDG folders, unset
`KTX_DB`, audit every database access, and keep reusable language and tool
caches separate. The
[database safety contract](reference/database-safety.md) spells out the rules.

### Extra engineering evidence

For a deeper, optional check, run
`KTX_ENGINEERING_EVIDENCE_DIR=<new-directory> mage engineeringVerify` with a
new directory. It keeps component, real-process load, and blocked-decompression
reports in a hash manifest and requires correctness, exact reconciliation, and
supported-lock checks. It does not add a timing gate.

### Schema release gate

Schema releases have their own production-scale gate. It is not part of
`mage check`. First prepare the disposable 3.2 GB fixture as described in the
[schema readiness contract](reference/schema-readiness.md#readiness-release-budget),
then run:

```bash
env -u KTX_DB \
  KTX_SCHEMA_RELEASE_WORK_ROOT="$root/work" \
  KTX_SCHEMA_RELEASE_REPORT="docs/evidence/schema-readiness/schema-readiness-release.json" \
  mage schemaReleaseGate
```

The named report counts as a pass only after a complete run. A failed attempt
is written to
`docs/evidence/schema-readiness/schema-readiness-release.json.attempt.failed.json`
and never replaces the passing report.

## Benchmarking

Everything a benchmark needs runs inside the ktx binary: stages, task loading,
scoring, and summaries. No external scripts or separate evaluator are needed.
Named pipelines, tasks, methodologies, and corpus fixtures live in
[`tools/bench`](../tools/bench).

### Screen configurations offline

Compare complete configurations without a database, credentials, or model
calls:

```bash
./build/ktx bench screen \
  --population tools/bench/evidence/configuration-screen-population-v1.json \
  --config tools/bench/pipelines/passthrough.toml \
  --config tools/bench/screen-configs/keep-ends-73-1.toml \
  --anchor-config tools/bench/screen-configs/keep-ends-100-50.toml \
  --format json
```

Repeat `--config` for each configuration. The JSON holds only aggregate
measurements, hashes, and opaque configuration IDs. External plugins are
rejected, so screening never sends source content to another process.

### Run a live comparison

Live comparisons call a real model, so they cost money. Start with the free
preflight, then run the comparison:

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

Preflight returns before task loading. It makes no model call, needs no
login, opens no ktx database, and creates no experiment files. With `--build`
it can use the network and changes Docker image and build-cache state. Compare
and rank create archives, use fresh isolated databases, call the provider over
the network, and can incur charges.

Compare and rank need an explicit registered `--spec`. Focused command help
lists every current spec, task, baseline, and candidate ID. Current
prompt-cache comparisons use methodology v6, summary schema 5, and
`cache-reuse-v2`. They make no provider-internal cache claims. The
[canonical benchmark workflow](guides/pipelines.md#canonical-benchmark-workflow)
walks through the full lifecycle.

### Inspect experiments

Experiments keep manifests, attempts, pricing snapshots, and deterministic
summaries under `.ktx/experiments/<id>`. Refer to them by ID only:

```bash
ktx bench list
ktx bench show <experiment-id>
ktx bench validate <experiment-id>
ktx bench summarize <experiment-id>
ktx bench validate <experiment-id>
```

List, show, and validate only read the archive and never open the ktx
database. Summarize rewrites the derived `summary.json` without calling a
provider or opening the ktx database.

Archives can contain repository snapshots, prompts, outputs, streams, stderr,
identifiers, databases, and secrets. Review anything you derive from them
before sharing. Historical reports are static evidence; the current binary is
not guaranteed to accept their archives.

### Replay offline

Replay a recorded session without calling the agent again:

```bash
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to replay}"
ktx bench run --replay "$KTX_TARGET_SESSION_ID"
```

For a strict, portable check, export a benchmark capture to a file and replay
that file without the source database:

```bash
: "${KTX_OWNED_DB:?set this to an explicitly operator-owned database path}"
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to export}"
KTX_DB="$KTX_OWNED_DB" ktx bench export --output replay-v1.json "$KTX_TARGET_SESSION_ID"
ktx bench run --replay-file replay-v1.json
```

Exit codes: cost or cache drift is informational and exits 0, an integrity or
parser failure exits 4, and a missing session exits 2. File replay uses only
the fixture and a controlled built-in pipeline; any divergence fails. Fixtures
contain raw recorded stream data, so check them for secrets and personal data
before sharing. The fixture format and refresh steps are in the
[pipelines guide](guides/pipelines.md#portable-offline-replay).

## Where to go next

- [Documentation index](README.md) for every guide, contract, and report.
- [`ARCHITECTURE.md`](../ARCHITECTURE.md) for the design intent.
- [`AGENTS.md`](../AGENTS.md) for repository working conventions.
