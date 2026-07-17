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
ktx get <session-id> --format text
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

## Benchmarking

`ktx bench run`, `report`, `compare`, `rank`, `validate`, and `corpus generate`
use Go implementations shipped in the binary. Named pipelines, tasks,
methodologies, and corpus fixtures live under [`tools/bench`](tools/bench).

A cache-aware live comparison separates cold observation, warm-up, and measured
attempts:

```bash
ktx bench compare \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task-file tools/bench/tasks/ephemeral-midfact-v1.toml \
  --methodology tools/bench/methodology-v5-cache-aware.yaml \
  --cache-control isolated \
  --warmup 1 \
  --rounds 3
```

Experiments retain manifests, attempts, pricing snapshots, and deterministic
summaries. Validate retained evidence with:

```bash
ktx bench validate --experiment .bench-data/<experiment-id>
```

Live runs accept any provider except `github-copilot/*`. The complete runbook is
[`docs/pipelines.md`](docs/pipelines.md); the latest published exploratory result
is the [Layer 1 cache and cost benchmark](benchs/layer-1-cache-cost.md).

### Evidence from live use

A rolling 24-hour snapshot on July 17, 2026 covered 8,321 tool calls. ktx reduced
the delivered tool-output context from 47.8 million to 26.0 million characters,
avoiding about 5.47 million tokens under its four-characters-per-token estimate.
At the models.dev uncached-input price for `openai/gpt-5.6-sol`, that is about
$27.33 of gross input cost avoided if each saved token would have been included
once. This is a model-priced estimate rather than a billing claim: ordinary live
calls did not retain enough cache telemetry to measure cache-adjusted savings or
changes in prompt-cache hit behavior.

## Development

```bash
mage check      # test, vet, lint, and build
mage build      # build build/ktx
```

`mage check` requires `golangci-lint`. Architectural intent is documented in
[`ARCHITECTURE.md`](ARCHITECTURE.md); repository working conventions are in
[`AGENTS.md`](AGENTS.md).
