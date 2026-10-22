# ktx

Make your coding agent measurable and cheaper.

ktx is a command-line tool that records what your coding agent does and
shrinks the tool output it sends to the model. Every original is stored, so a
compressed result can always be restored byte for byte. ktx ships as one Go
binary and works without a daemon, an SDK, or code changes to your agent.

## What it does

- Records each session and tool call, with tokens and cost whenever the agent
  runtime reports them. `ktx stats` ranks the tool calls that use the most
  tokens.
- Compresses tool output in flight through adapters for OpenCode and Codex.
  The stages are small and predictable, such as stripping color codes, keeping
  the ends of long logs, and compacting JSON.
- Stores every original output. `ktx read` and `ktx decompress` return the
  exact bytes, and an optional one-line hint tells the agent how to fetch them.
- Lets you check a change before you adopt it. You can replay recorded sessions
  offline, run controlled live comparisons, and use `ktx optimize` to qualify,
  apply, and roll back a config.
- Keeps recordings on your machine. ktx goes online only to fetch the
  [models.dev](https://models.dev) price list when its cache is empty, or when
  you start a live benchmark.

## Results

On July 17, 2026, ktx cut the delivered output of 8,321 tool calls from 47.8
million to 26.0 million characters, about 46% less. At four characters per
token that is roughly 5.47 million tokens, worth about $27.33 at the
`openai/gpt-5.6-sol` uncached-input list price. The dollar figure is an
estimate from list prices.

In a validated five-pair OpenCode 1.18.5 comparison, the OpenCode-reported
cached-input share was about 72% for the candidate configuration and about 53%
for the baseline, under the retained protocol. The
[full report](docs/reports/cache-reuse-live-validation.md) gives the exact
scope. Runtime counts like these can't show what happens inside a provider's
cache, so ktx makes no claims about that.

More results are in the [reports](docs/README.md#reports).

## Install

ktx runs on Linux. Building it needs Go 1.26.10 or newer.

```bash
go install github.com/jgabor/ktx/cmd/ktx@latest
```

To build from a checkout, run `mage build` (or
`go build -o build/ktx ./cmd/ktx`). The binary lands in `build/ktx`.

Other operating systems are not supported. The
[Linux qualification report](docs/reports/native-linux-qualification.md) lists
the tested host.

## Quick start

### 1. Record a session

Put `ktx run` in front of any agent command:

```bash
ktx run opencode run --format json "What is 2+2?"
ktx list --format text
```

`ktx run` shows the agent's normal output, then a one-line summary.
`ktx list` shows your recorded sessions:

```text
ID                AGENT    STARTED                         ENDED                TOOL_CALLS  TOKENS  COST  MODEL
vhlttrsjwnahnrlm  /bin/sh  2026-08-23T14:32:19.140759196Z  2026-08-23T14:32:19  0           -       -     -
```

Look at one session with `ktx get <session-id> --format text`. Once an adapter
records tool calls, `ktx stats` ranks them by the tokens they delivered.

When `ktx run` wraps a command in a terminal, it needs a few Linux features:
readable procfs, subreaper support, `pidfd_open`, and `waitid(P_PIDFD)`. Most
Linux 5.4+ kernels have them. If one is missing, ktx stops with a clear error
before it starts your command. Runs outside a terminal don't need them. The
[wrapper guide](.agents/skills/ktx/references/cli.md#terminal-cancellation)
explains cancellation and terminal handling.

### 2. Compress tool output

Install the adapter for your agent:

```bash
ktx init opencode
# or
ktx init codex
```

Copy [`ktx.example.toml`](ktx.example.toml) to
`$XDG_CONFIG_HOME/ktx/ktx.toml`, then check what is active:

```bash
ktx config show --json
```

After each tool call, the adapter hands the output to ktx. ktx saves the
original, runs your pipeline, and gives the agent the smaller result. Without
a config file, output passes through unchanged.

Lossy profiles can set `recover_footer = true`. Then, whenever compression
saves space, the agent sees a short hint like this:

```text
[ktx] output compressed: 2815 bytes (56% reduction) • get full output with `ktx read ugrpgrcvdrbhbaby`
```

The original and the compressed copy are stored separately, and the hint is
never part of the stored bytes. Retrying the same tool call reuses the first
stored copy instead of replacing it.

The OpenCode plugin can also record model-step token usage. See the
[OpenCode guide](docs/guides/opencode-telemetry.md) for plugin locations,
updates, and troubleshooting.

### 3. Tune your config safely

`ktx optimize` suggests a better pipeline and shows the evidence. The default
run is offline: no model calls and no config changes.

```bash
ktx optimize --format text
ktx optimize show <result-id> --format text
```

The report lists what was tested, the limits, whether the result can be
applied, and the next commands to run. Today `ktx optimize` changes only
`pipeline.default.stages` and keeps the rest of your config byte for byte.

To make a result ready to apply, run a paid live check. It needs a model,
Docker, a prebuilt `ktx-bench` image, and OpenCode login:

```bash
ktx optimize --live --model provider/name --format text  # replace provider/name
ktx optimize apply <ready-result-id> --dry-run
ktx optimize apply <ready-result-id>
ktx optimize rollback <ready-result-id>
```

The live check runs built-in test tasks instead of your own workload. It
stops after 24 benchmark agent launches, counting setup and retries; the cap
does not limit what your provider charges. Nothing changes until you run
`apply`, and `rollback` restores your original file.
`ktx optimize --help` covers prerequisites and exit codes, and the
[optimization guide](docs/guides/pipelines.md#configuration-optimization) covers
the evidence rules.

## Commands

| Command                   | What it does                                          |
| ------------------------- | ----------------------------------------------------- |
| `run`                     | Wrap an agent command and record the session          |
| `list` / `get`            | List sessions, or show one by ID                      |
| `stats`                   | Rank tool calls by delivered tokens                   |
| `hook`                    | Compress tool output in flight (called by adapters)   |
| `read`                    | Fetch a stored record by ID as raw JSON               |
| `compress` / `decompress` | Compress a record's output, or restore it exactly     |
| `optimize`                | Screen, qualify, apply, or roll back pipeline configs |
| `init`                    | Install the OpenCode or Codex adapter                 |
| `config`                  | Show the active pipeline configuration                |
| `plugin`                  | Manage compression plugins                            |
| `telemetry`               | Record and inspect optional model-step token usage    |
| `prices`                  | Manage the local models.dev price cache               |
| `db`                      | Database path, reports, and maintenance               |
| `write`                   | Store a tool-call record from sidecar input           |
| `doctor`                  | Check that ktx is healthy                             |
| `bench`                   | Run benchmarks, experiments, and reports              |

Run `ktx <command> --help` for details.

## Configuration and data

Pipelines live in `$XDG_CONFIG_HOME/ktx/ktx.toml`. A pipeline is an ordered
list of stages such as `unwrap-result`, `strip-ansi`, `normalize`, `keep-ends`,
`token-budget`, and `replace`. `[[route]]` rules pick a profile by tool name,
command, subcommand, or exit status. Start from
[`ktx.example.toml`](ktx.example.toml) and see the
[pipelines guide](docs/guides/pipelines.md) for recipes.

Recordings live in `$XDG_DATA_HOME/ktx/ktx.db`. Set `KTX_DB` to use a different
file; in `ktx db` commands, `--path <file>` overrides both. ktx prepares a
supported older database automatically the first time a command needs it, and
query commands are read-only after that. If the database is newer than your
ktx, install a newer ktx. The
[database safety contract](docs/reference/database-safety.md) has the details.

File logging is optional and off by default. See the
[logging guide](docs/guides/logging.md).

## Documentation

- [Documentation index](docs/README.md): every guide, contract, and report.
- [Development and benchmarking](docs/development.md): build from source, run
  the checks, and measure compression with `ktx bench`.
- [`ARCHITECTURE.md`](ARCHITECTURE.md): design intent.
- [`AGENTS.md`](AGENTS.md): repository conventions for coding agents.

## License

ktx is released under the [MIT License](LICENSE). Third-party material is
listed in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
