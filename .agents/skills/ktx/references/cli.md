# ktx CLI Workflows

This derived task summary is not a canonical contract. Project memory owns
intent and scope, implementation and tests establish current behavior, and
public docs/help must remain synchronized contract surfaces. Use this reference
for public commands and routine operator workflows. JSON is the default for
most data-returning commands; request it explicitly in scripts so intent remains
clear.

## Global Surface

```text
ktx [--format text|json] [--quiet|-q] <command> [args]
```

| Command               | Purpose                                                            | Database behavior          |
| --------------------- | ------------------------------------------------------------------ | -------------------------- |
| `run`                 | Wrap an agent process and record events                            | Write-capable              |
| `init`                | Install or remove OpenCode/Codex adapters                          | DB-free                    |
| `hook`                | Adapter ingress: persist and compress a tool result                | Write-capable              |
| `write`               | Persist a flat sidecar tool-call record                            | Write-capable              |
| `list`                | List sessions                                                      | Read-only                  |
| `get`                 | Formatted lookup of tool call or session                           | Read-only                  |
| `read`                | Raw JSON lookup of tool call or session                            | Read-only                  |
| `compress`            | Explicitly compress one persisted tool call                        | Write-capable              |
| `decompress`          | Return original bytes referenced by compressed ID                  | Read-only                  |
| `config`              | Inspect active profiles and routes                                 | DB-free                    |
| `plugin`              | Manage external compression stages                                 | DB-free                    |
| `stats`               | Rank residual context or emit window summary and breakdown reports | Read-only                  |
| `telemetry`           | Record or inspect optional model-step observations                 | Mixed                      |
| `doctor`              | Diagnose an existing database and installation                     | Read-only inspection       |
| `db`                  | Path, read-only report, and explicit migration                     | Mixed                      |
| `prices`              | Inspect or update the models.dev price cache                       | DB-free, network on update |
| `binary-capabilities` | Report executable identity and max schema                          | DB-free                    |
| `bench`               | Run, compare, validate, replay, and report benchmarks              | Mixed                      |

Use `ktx <command> --help` for the syntax and outcomes it documents. Not every
help surface lists all nested flags or exit codes; project docs and this skill
may cover those parts more completely.

## Paths and Precedence

| Data                    | Default                                                          |
| ----------------------- | ---------------------------------------------------------------- |
| Database                | `$XDG_DATA_HOME/ktx/ktx.db`, falling back under `~/.local/share` |
| Pipeline config         | `$XDG_CONFIG_HOME/ktx/ktx.toml`                                  |
| Price cache             | `$XDG_DATA_HOME/ktx/prices.json`                                 |
| OpenCode global plugin  | `$XDG_CONFIG_HOME/opencode/plugins/ktx.ts`                       |
| OpenCode project plugin | `./.opencode/plugins/ktx.ts`                                     |
| Codex global hooks      | `~/.codex/hooks.json`                                            |
| Codex project hooks     | `./.codex/hooks.json`                                            |

Database path precedence is:

```text
ktx db --path <file> ... > KTX_DB > $XDG_DATA_HOME/ktx/ktx.db
```

The `--path` override belongs to the `db` namespace. Use `KTX_DB` to select a
database for normal commands.

## Adapter Workflows

### OpenCode

```bash
ktx init opencode
ktx init opencode --project
ktx init opencode --show
ktx init opencode --uninstall
```

`--project` scopes installation. OpenCode uninstall checks both global and
project plugin candidates regardless of `--project` and removes generated ktx
plugins from both locations when present.

The generated plugin has two hooks:

- `tool.execute.after` invokes `ktx hook` for persistence/compression.
- `event` invokes `ktx telemetry record` for accepted model steps.

Re-run the installer after replacing ktx, then restart OpenCode. The plugin pins
one verified absolute binary path and digest for its lifetime. OpenCode telemetry
is currently contracted against OpenCode 1.18.5; later event contracts require
review before making equivalent claims.

When `KTX_SESSION_ID` is present, generated hooks no-op and the `ktx run`
wrapper remains the single evidence path.

### Codex

```bash
ktx init codex
ktx init codex --project
ktx init codex --show
ktx init codex --uninstall
```

Install merges one `PostToolUse` entry and preserves unrelated hooks. After
install, run `/hooks` in Codex to trust the hook. Codex uninstall honors the
selected scope: global by default or project with `--project`.

## Wrapper Collection

```bash
ktx run opencode run --format json "prompt"
ktx run --record-stream opencode run --format json "prompt"
ktx --quiet run opencode run --format json "prompt"
```

- Child stdout is scanned line by line, each scanned line is forwarded, and a
  newline is written after it. Output without a final newline can gain one; this
  is not byte-exact passthrough.
- Identifiable tool events become `tool_calls`.
- Identifiable OpenCode model steps become separate session telemetry.
- The session closes when the child exits.
- ktx exits with the child's exit code.
- `--record-stream` retains JSONL for `ktx bench run --replay <session-id>`.

## Inspection

```bash
ktx list --format text
ktx list --format json
ktx get <id> --format json
ktx get <id> --format text
ktx read <id>
```

`get` and `read` resolve tool calls first, then sessions. They do not address
compressed rows. `get` is formatted; `read` emits raw stored JSON. Not found is
normally exit `2`.

## Sidecar Input

```bash
ktx write < tool-call.json
```

For a `tool_call`, the current implementation requires `id`, `tool`, and
`timestamp`. `session_id` can be resolved from the parent process; a missing
`content_hash` is derived when output is non-empty; `input` and `output` may be
empty. Optional evidence includes `tokens_in`, `tokens_out`, `cost`,
`latency_ms`, `model`, `provider`, `exit_code`, `is_error`, and `runtime`.

For `event_type="session_close"`, `session_id` and `timestamp` are required. The
command prints the affected ID as raw JSON.

Supply only metadata whose authority the sidecar can establish. Explicit
`is_error` is a caller assertion; a runtime, model, or provider label does not
strengthen it. Persisted false is success and true is failure. Missing
`is_error` persists as unknown; non-boolean `is_error` is malformed and rejected
before persistence. `exit_code` is diagnostic only. Let ktx derive
`content_hash` unless the caller independently verifies it. Prefer trusted
adapters or wrappers when native identity is available; flat sidecar identity
has weaker provenance.

For OpenCode 1.18.5, wrapper outcome precedence is terminal error, supported
shell exit, completed result, then unknown. The hook has only supported shell
exit evidence. Trusted schema-conforming Codex 0.144.4 `PostToolUse` occurrence
is success; nested `tool_response` outcome-looking fields are unconstrained and
non-overriding. Nonconforming, untrusted, unsupported, and historical missing
evidence remains unknown.

## Compression and Recovery

```bash
ktx compress <tool-call-id>
ktx decompress <compressed-id>
ktx read <source-id>
```

- `compress` prints a new compressed ID as raw JSON.
- `decompress` writes the source tool call's original output bytes verbatim.
- `read` returns the stored source record as raw JSON and is the recovery command
  used in hook footers.
- No stage inverse runs. Reversibility comes from the retained original.

## Stats and Telemetry

```bash
ktx stats --limit 20 --format json
ktx stats summary --window 30m --format json
ktx stats breakdown --window 30m --format json
ktx telemetry list --session <id> --limit 1000 --format json
```

`stats` excludes trivial groups with fewer than two calls or fewer than 50
delivered estimated tokens. `stats summary` accepts positive Go durations such
as `30m`, `24h`, or `72h`. `stats breakdown` uses the same window syntax and
reports bounded delivery groups by safe command dimensions, profile, route,
disposition, and raw-output-size band; it does not include cache telemetry.
Summary JSON schema 7 and breakdown JSON schema 2 report aggregate outcomes over
the same output-bearing, valid-timestamp calls in the inclusive window. Error
rate divides errors by known outcomes; coverage divides known by total. Text
renders those current fields without a separate schema version. Breakdown has
no per-group rates, and cache/model-step selection remains independent. Summary
`cache_reuse` is provider prompt/prefix-cache token evidence, not filesystem,
memory, database, HTTP, or other I/O caching. It reports observed first/last
capture, span, KTX session and model-step counts; requested-window coverage is
unavailable with `collector_lifecycle_evidence_unavailable` and scope
`observed_subset`. Its `prompt_cache_token_hit_rate` is `token_weighted`:
`sum(cache_read) / sum(uncached_input + cache_read + cache_write)`. The rate
reports runtime-emitted model-step source, raw buckets, temporal scope, and
token-fraction unit; it is not a provider request hit rate. It is measured only
for one complete runtime-emitted provider/model pair and complete buckets.
`runtime_reported_cost_usd` separately sums runtime-emitted cost and preserves a
literal zero. `api_equivalent_estimated_cost_usd` is a separately labeled USD
estimate that requires complete uncached-input, cache-read, cache-write, output,
and reasoning fields, one runtime-emitted identity, and a matching local price
cache record. It does not fetch prices, backfill identity, or replace reported
cost; unavailable estimate reasons and price-record provenance remain explicit.
Retained schema 6 summaries remain unchanged historical evidence.

Ordinary `telemetry record` is an adapter-facing best-effort write command. It
reports failures without failing the agent operation. Required benchmark
collection is different: callbacks, acknowledgements, interval ownership, and
event order are fail-closed. `telemetry list` is read-only.

## Health, Database, and Prices

```bash
ktx db path
ktx doctor --format json
ktx db report --output -
: "${KTX_OWNED_DB:?set a preconfirmed operator-owned database path}"
ktx db --path "$KTX_OWNED_DB" migrate
ktx prices path
ktx prices update --format json
ktx binary-capabilities --format json
```

- `doctor` diagnoses but never creates, migrates, or repairs.
- `db report` inspects an existing database read-only.
- `db report` emits JSON report schema 3 over all tool calls. Outcome
  classification and formulas match live stats; percentages use two-decimal
  rounding, malformed values are unknown, and exit codes remain diagnostic.
- `db migrate` is explicit and forward-only. The example requires the operator
  to preconfirm ownership of the selected target; never substitute the ambient
  live database for development, test, CI, or rehearsal.
- `prices update` performs a network fetch and atomically rewrites the cache.
- `binary-capabilities` reports canonical path, SHA-256, build version, and
  maximum supported schema without opening a database.

## Output and Exit Conventions

- `0`: success. Some commands also use `0` for informational drift or degraded
  read-only diagnosis; inspect structured status.
- `1`: usage or input contract error.
- `2`: not found where applicable.
- `3`: conflict, including a binary digest mismatch where applicable.
- `4`: internal, integrity, verification, or hard health failure.
- `5`: schema migration required or schema incompatible on database access.

`ktx run` forwards the child process exit code, so it can return values outside
this ktx error taxonomy. Command-specific help is authoritative for the outcomes
it documents. Write data to stdout and diagnostics to stderr; do not parse human
summaries when structured JSON exists.
