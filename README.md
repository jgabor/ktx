# ktx

ktx is a runtime-agnostic CLI for measuring and reducing coding-agent context
cost. It is one Go binary: no daemon and no library integration requirement.

## Install

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

The adapter calls `ktx hook` after tool execution. ktx persists the original,
runs the selected pipeline, and returns the smaller result to the agent. With no
configuration file, the pipeline is passthrough.

The generated OpenCode plugin also records optional runtime model-step token
evidence. ktx collects telemetry only from the agent runtime. Provider and
model identity may come from runtime events or explicit runtime configuration,
with provenance; provider documentation and pricing catalogs are reference and
estimate inputs, not telemetry. See the [OpenCode integration and telemetry
runbook](docs/opencode-telemetry.md) for plugin locations, both hooks, wrapper
coexistence, refresh, offline verification, claim boundaries, and
troubleshooting.

Lossy profiles can opt into `recover_footer = true`. When compression succeeds
and the result plus footer remains smaller than the original, the agent receives
a compact `ktx read <source-id>` recovery cue. The original and compressed
artifact remain stored separately; the footer is not part of the persisted
compressed bytes.

KTX assigns its own source IDs to persisted tool calls. Runtime session and
tool-call identifiers are correlation inputs, not record IDs. Recognized
OpenCode and Codex events with a complete, verified runtime/session/call tuple
derive the same source ID on exact retry. An event selecting a recognized
adapter path but missing a complete tuple receives a fresh NanoID; caller IDs
are preserved only for legacy flat payloads without a recognized runtime-adapter
discriminator. The first compressed artifact is authoritative once persisted.
A racing retry may receive source output while no artifact exists yet, but later
retries return the first artifact and cannot replace it. Use the source ID from
inspection or a recovery cue with commands such as `ktx read` and `ktx compress`.

## Configuration and data

The default database is `$XDG_DATA_HOME/ktx/ktx.db`; set `KTX_DB` to select a
different database for normal commands. Within the `ktx db` namespace,
`--path <file>` is a per-invocation override and takes precedence over `KTX_DB`.

Pipeline configuration lives at `$XDG_CONFIG_HOME/ktx/ktx.toml`. Configured
pipelines are ordered deterministic stages. Built-ins include `unwrap-result`,
`strip-ansi`, `normalize`, `keep-ends`, `token-budget`, and `replace`. Named
profiles can be selected through first-match `[[route]]` rules using tool name,
command prefix, subcommand, and exit status.

See [`ktx.example.toml`](ktx.example.toml) for a working configuration and
[`docs/pipelines.md`](docs/pipelines.md) for stage, routing, and benchmark
guidance. Originals remain addressable through `ktx read` and `ktx decompress`.

Database access is forward-only. Query commands use enforced read-only SQLite
access and never create or migrate persistent state. Write-capable commands may
create a database and transactionally migrate an older supported schema. If a
query reports `schema-migration-required`, run `ktx db migrate`. See the
[database safety contract](docs/database-safety.md) for command classes,
hermetic development, and seeded migration rehearsal. The
[critical-path performance and availability contract](docs/critical-path-performance.md)
keeps component, process, product, and concurrency evidence distinct and defines
hook acceptance and database-unavailable behavior. Recover from a database newer
than the binary by installing a binary that supports that schema or newer. Never
lower `PRAGMA user_version`.

### Live prompt-cache token hit rate

`ktx stats summary --format json` emits schema 7. Its
`prompt_cache_token_hit_rate` is a token-weighted rate:
`cache_read / (uncached_input + cache_read + cache_write)`. The text view renders
it as `PROMPT_CACHE_TOKEN_HIT_RATE_PCT`. It is measured only for complete,
non-negative OpenCode 1.18.5 buckets from one runtime-emitted provider/model
population. It preserves zero cache read as a measured zero rate and otherwise
reports a machine-readable unavailable reason. The result includes raw buckets,
runtime/provider/model scope, and observed-subset temporal scope.
It is not a provider request hit rate.
It is not filesystem, memory, database, HTTP, or other I/O caching.
See the [pipeline runbook](docs/pipelines.md#residual-waste-discovery) for the
complete contract and claim limits.

### Live cost views

Schema 7 also keeps two live cost views separate. `runtime_reported_cost_usd`
is only the runtime-emitted cost evidence. A literal `0` remains measured, even
when an estimate is nonzero. `api_equivalent_estimated_cost_usd` is a distinct,
cache-aware API price estimate that requires runtime-emitted provider/model
identity, all five token buckets (uncached input, cache read, cache write, output,
and reasoning), and a retained local price record. It never fills reported cost,
backfills identity from configuration, fetches pricing, or claims billing meaning.
The JSON output includes exact micro-USD aggregation, components, selected rates,
pricing-record provenance, and separate identity groups when an aggregate cannot
truthfully pool models. The text view labels the columns
`RUNTIME_REPORTED_COST_USD` and `API_EQUIVALENT_ESTIMATED_COST_USD`.

## Benchmarking

`ktx bench` commands use Go implementations shipped in the binary. Named
pipelines, tasks, methodologies, and corpus fixtures live under
[`tools/bench`](tools/bench).

A cache-aware live comparison separates cold observation, warm-up, and measured
attempts:

```bash
./build/ktx bench compare --preflight --format json

./build/ktx bench compare --preflight --build --format json

./build/ktx bench compare \
  --build \
  --methodology tools/bench/methodology-v5-cache-aware.yaml \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task-file tools/bench/tasks/crossfile-nav-v1.toml \
  --cache-control observed \
  --warmup 1 \
  --rounds 5
```

The generic preflight returns before task loading. It makes no model call,
requires no authentication, and creates no experiment artifact. It inspects the
existing image; with `--build`, it first checks the local source and builds the
image. Preflight then requires the image to attest the pinned
OpenCode version, generated plugin digest, explicit attempt owner, serialized
fail-closed callbacks, complete interval lifecycle, epochs, and normalized event
kinds. It also requires strict acknowledgements, exact wrapper/event model-step
identity, fail-closed interval validation, and capability attestation before auth
readiness. Every normal comparison runs the same gate before its first attempt.

The retained OpenCode 1.18.5 Luna/high comparison passed validation with five
complete measured pairs and full quality on both sides. Its candidate mean
OpenCode-reported cached-input share was higher than baseline under the retained
protocol. This supports no significance, causal, generic cache-effectiveness,
or provider-native cache claim. The
[live validation report](docs/cache-reuse-live-validation.md) records the exact
values, provenance, estimates, hashes, and earlier failed archives.

Experiments retain manifests, attempts, pricing snapshots, and deterministic
summaries. Validate retained evidence with:

```bash
ktx bench validate --experiment .bench-data/<experiment-id>
```

Future prompt-cache token hit-rate summaries are opt-in through
`tools/bench/methodology-v6-prompt-cache-hit-rate.yaml`. Its explicit
`cache-reuse-v2` manifest marker produces schema 5. Each side rate is
`sum(cache_read) / sum(uncached_input + cache_read + cache_write)` over the
accepted complete pairs, not an average of attempt rates. The comparison also
publishes the percentage-point delta, a deterministic whole-pair bootstrap
interval, quality, blockers, and raw evidence. It can render only an authorized
higher or lower token-weighted prompt-cache hit rate. It does not report
provider-internal state or request-hit, TTL, eviction, eligibility, isolation,
or prefix behavior. Existing benchmark artifacts remain on their retained
schemas and contracts.

Recommendation also requires quality-preserving `pipeline_acceptance`: the
candidate must have a strictly lower API-equivalent estimated cost per correct
without reducing same-pair mean fact score or full-pass rate. A valid rejected
candidate remains evidence but is not eligible for recommendation or crown
selection.

The [retained runtime compression telemetry report](docs/runtime-compression-telemetry-evidence.md)
shows one historical ten-attempt comparison, its field-by-field availability,
and an offline deterministic validation.

Replay a database capture by session ID without another agent call:

```bash
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to replay}"
ktx bench run --replay "$KTX_TARGET_SESSION_ID"
```

This compatibility path preserves the existing database-backed envelope and
exit behavior: `PASS` and informational cost/cache `DRIFT` exit 0, integrity or
parser `FAIL` exits 4, and a missing session exits 2. A parentless recorded
stream is supported, but has no original benchmark comparison.

For strict, portable verification, export a benchmark-parented capture and
replay the resulting file without the source database:

```bash
: "${KTX_OWNED_DB:?set this to an explicitly operator-owned database path}"
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to export}"
KTX_DB="$KTX_OWNED_DB" ktx bench export --output replay-v1.json "$KTX_TARGET_SESSION_ID"
ktx bench run --replay-file replay-v1.json
```

`KTX_OWNED_DB` declares which path the operator owns; it does not prove that the
path is disposable or safe to modify.

`ktx bench export <session-id>` writes the same canonical version 1 fixture to
stdout by default; `--output -` makes stdout delivery explicit. File replay
uses only the fixture and a controlled built-in pipeline. Any integrity,
parser, usage, cost, cache, or compression divergence is a strict failure.
Fixture contents include raw recorded stream data, so review them for secrets,
personal data, machine-local identifiers, and repository content before
sharing or committing them. Historical direct-provider fixtures may remain for
offline compatibility and provenance, but are not a supported collection path.
See the runbook for the fixture schema, source and
refresh requirements, absent-versus-zero semantics, threat boundary, and exit
classes.

Live runs accept any provider except `github-copilot/*`. The complete runbook is
[`docs/pipelines.md`](docs/pipelines.md). The [Layer 1 cache and cost
benchmark](benchs/layer-1-cache-cost.md) is a historical benchmark retained for
provenance; current telemetry claims are runtime-observed only.

The completed external campaign is retained as historical evidence, not as a
supported workflow. The native KTX commands above are the current benchmark
surface. The correction accepts one complete campaign with eight attempts and
four pairs; the five-pair gate is incomplete and has no current decision. See
the [historical campaign report](docs/skillsbench-campaign.md) for the result,
limits, ownership, and evidence map.

The result covers one named task. It establishes neither official suite
identity nor general agent skill, provider cost, unrun-task coverage, or
permission to execute again. KTX owns its native task and scoring contracts,
databases, summaries, comparison, validation, and ranking. The external
evaluator owns agent execution, authentication, environment setup, and its
dependency lifecycle.

### Evidence from live use

A rolling 24-hour snapshot on July 17, 2026 covered 8,321 tool calls. ktx reduced
the delivered tool-output context from 47.8 million to 26.0 million characters,
avoiding about 5.47 million tokens under its four-characters-per-token estimate.
At the models.dev uncached-input price for `openai/gpt-5.6-sol`, that is about
$27.33 of gross input cost avoided if each saved token would have been included
once. This is a model-priced estimate rather than a billing claim: ordinary live
calls did not retain enough cache telemetry to measure cache-adjusted savings.
Aggregate runtime token counts cannot establish provider-internal cache hits,
misses, fills, eviction, request boundaries, isolation, or prefix composition.

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

For retained, descriptive component and contention reports outside product
SQLite, choose a new directory and run:

```bash
KTX_ENGINEERING_EVIDENCE_DIR="$PWD/docs/notes/engineering-run" mage engineeringVerify
```

This extended developer path retains report and manifest files. It does not add
a timing gate, replace `mage check`, or change `ktx bench` evidence. It does
require load-report correctness, exact reconciliation, and supported-lock
checks.

`mage verify` runs `mage check` (test, vet, lint, and build), `govulncheck`, and
the portable replay gate. Tracked CI runs this same command on every push and
pull request. Verification requires both `golangci-lint` and `govulncheck`.
Architectural intent is documented in [`ARCHITECTURE.md`](ARCHITECTURE.md);
repository working conventions are in [`AGENTS.md`](AGENTS.md).
