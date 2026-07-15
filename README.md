# ktx

ktx is a runtime-agnostic CLI for measuring and reducing coding-agent context
cost. It is one Go binary: no daemon and no library integration requirement.

## Install

```bash
go install github.com/jgabor/ktx/cmd/ktx@latest
```

Build from a checkout with `go build -o ktx ./cmd/ktx`.

## Quick start

```bash
ktx --help
ktx run opencode run --format json "What is 2+2?"
ktx list --format text
ktx get <session-id> --format text
```

The default database is `$XDG_DATA_HOME/ktx/ktx.db`; override it with `KTX_DB`.
Pipeline configuration lives at `$XDG_CONFIG_HOME/ktx/ktx.toml`. Start from
[`ktx.example.toml`](ktx.example.toml).

Without a configuration file, compression is passthrough. Configured pipelines
are ordered lists of deterministic stages such as `strip-ansi`, `keep-ends`,
and `token-budget`. Originals remain addressable through `ktx read` and
`ktx decompress`.

Codex hook records use KTX-owned IDs containing exactly 16 lowercase letters.
Codex runtime identifiers such as `exec-*` values are inputs to identity
derivation, not record IDs. Use the canonical source ID printed by the Codex
hook or returned by KTX inspection for `read`, `get`, `compress`, and recovery
commands.

## Benchmarking

`ktx bench run`, `report`, `compare`, `rank`, `validate`, and `corpus generate`
use Go implementations shipped in the binary. Named pipelines, tasks, and
corpus fixtures live under [`tools/bench`](tools/bench); the runbook is
[`docs/pipelines.md`](docs/pipelines.md). Live runs accept any provider
except `github-copilot/*`.

## Development

```bash
make check
```

This runs formatting checks, Go tests, and vet. Architectural intent is
documented in [`ARCHITECTURE.md`](ARCHITECTURE.md).
