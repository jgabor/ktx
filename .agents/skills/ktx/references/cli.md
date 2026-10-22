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

| Command      | Purpose                                                            | Database behavior                                          |
| ------------ | ------------------------------------------------------------------ | ---------------------------------------------------------- |
| `run`        | Wrap an agent process and record events                            | Write-capable                                              |
| `init`       | Install or remove OpenCode, Codex, or Claude Code adapters         | DB-free                                                    |
| `hook`       | Adapter ingress: persist and compress a tool result                | Write-capable                                              |
| `write`      | Persist a flat sidecar tool-call record                            | Write-capable                                              |
| `list`       | List sessions                                                      | Read-only                                                  |
| `get`        | Formatted lookup of tool call or session                           | Read-only                                                  |
| `read`       | Raw JSON lookup of tool call or session                            | Read-only                                                  |
| `compress`   | Explicitly compress one persisted tool call                        | Write-capable                                              |
| `decompress` | Return original bytes referenced by compressed ID                  | Read-only                                                  |
| `config`     | Inspect active profiles and routes                                 | DB-free                                                    |
| `plugin`     | Manage external compression stages                                 | DB-free                                                    |
| `stats`      | Rank residual context or emit window summary and breakdown reports | Read-only                                                  |
| `telemetry`  | Record or inspect optional model-step observations                 | Mixed                                                      |
| `doctor`     | Diagnose an existing database and installation                     | Read-only inspection                                       |
| `db`         | Path, read-only report, and explicit migration                     | Mixed                                                      |
| `prices`     | Inspect or update the models.dev price cache                       | DB-free, network on update                                 |
| `bench`      | Run, compare, validate, replay, and report benchmarks              | Mixed                                                      |
| `optimize`   | Screen configs, explicitly qualify one, inspect/apply/rollback     | Offline DB-free; live isolated owners; retained inspection |

Use `ktx <command> --help` for the syntax and outcomes it documents. Not every
help surface lists all nested flags or exit codes; project docs and this skill
may cover those parts more completely.

## Paths and Precedence

For `init`, `db`, `stats`, `bench`, `plugin`, and `optimize`, owned options may surround
the runtime or subcommand. Value-taking options consume their next token even
when it looks like `--help` or `--format`; use `--name=value` for clarity.
The first standalone `--` ends option handling, and the command still validates
its remaining operands. Boolean flags such as `--help` accept `=false`.
Local `--format` overrides the global format, with the last local value winning;
an empty final format is invalid. Raw I/O and fixed JSON outputs retain their
command-specific output contracts.

```bash
ktx init --project=false opencode --show --help=false
ktx db path --path=--help
ktx stats --window 30m summary --format json
ktx bench --spec exact-match report
ktx plugin add myfilter cat --description=--format
```

`write` reports exit 4 if it commits evidence but cannot deliver the ID to
stdout. Its diagnostic identifies the committed record/session; this is not a
rollback. Inspect that identity before retrying. Do not invent a new ID to
repeat a committed observation; duplicate tool-call IDs remain rejected.

| Data                    | Default                                                          |
| ----------------------- | ---------------------------------------------------------------- |
| Database                | `$XDG_DATA_HOME/ktx/ktx.db`, falling back under `~/.local/share` |
| Pipeline config         | `$XDG_CONFIG_HOME/ktx/ktx.toml`                                  |
| Price cache             | `$XDG_DATA_HOME/ktx/prices.json`                                 |
| OpenCode global plugin  | `$XDG_CONFIG_HOME/opencode/plugins/ktx.ts`                       |
| OpenCode project plugin | `./.opencode/plugins/ktx.ts`                                     |
| Codex global hooks      | `~/.codex/hooks.json`                                            |
| Codex project hooks     | `./.codex/hooks.json`                                            |
| Claude Code plugins     | `$XDG_DATA_HOME/ktx/claude-code/marketplace`                     |

Database path precedence is:

```text
ktx db --path <file> ... > KTX_DB > $XDG_DATA_HOME/ktx/ktx.db
```

The `--path` override belongs to the `db` namespace. Use `KTX_DB` to select a
database for normal commands.

## Configuration optimization

```bash
ktx optimize --format text
ktx optimize show <result-id> --format text
# Only with explicit paid consent and a chosen model:
ktx optimize --live --model provider/name --format text
ktx optimize apply <ready-result-id> --dry-run
ktx optimize apply <ready-result-id>
ktx optimize rollback <ready-result-id>
```

Replace `provider/name`. Offline generated evidence is never apply-ready. Only
`pipeline.default.stages` may change; unrelated settings/routes, unsupported
plugins/stages, or missing routing authority block before paid evaluation.
Built-in proposals preserve unrelated config bytes. A supplied `--candidate` is
one full config. `--config` selects an existing target; without it, effective
config/absence is captured. Mutations cannot retarget and refuse later edits.

Live requires Docker, a compatible prebuilt `ktx-bench` image with pinned runtime/
collector preflight, and `$HOME/.local/share/opencode/auth.json`. Assets are
embedded, so no checkout is needed. The default 24 benchmark launch allowance
includes setup/retries, not provider requests or USD. Five complete qualified
pairs and runtime-token improvement establish only the controlled cohort. Cost
availability is independent; `--pricing-snapshot` is optional. Commands never
prompt; reports include next commands. Exit 0 means report produced, even for
blocked/incomplete; inspect outcome/eligibility. Errors use exits 1 usage, 2 missing
result, 3 conflict, 4 integrity/I/O.

Apply/rollback retries are no-ops. Rollback restores original bytes/mode/group or
absence only when later edits are absent. Rolled-back IDs are retired. Another
cycle uses the current incumbent without rewriting old evidence. Private
archives/recovery live under `.ktx/optimizations`. Checksums are drift checks, not
same-owner authentication; directory leases require cooperating external editors
for full lost-update protection. See the [canonical contract](../../../../docs/guides/pipelines.md#configuration-optimization).

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

Each callback invokes the pinned binary and adds no outer timeout.

Restart OpenCode after installation. Regenerate the plugin and restart OpenCode
only to pick up generated source changes. The plugin pins one canonical absolute
binary path for its lifetime. OpenCode telemetry
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
The generated command pins the installing executable's canonical absolute path
and omits Codex's optional timeout. Regenerate it only to pick up generated
source changes or to update the pin after moving the executable. Show and
uninstall also recognize relocated, unmodified generated inner hooks: exactly
the command type, ktx status message, and a single shell-quoted canonical absolute
path followed by ` hook`. Copied hooks in that exact generated form are managed
entries, not proof of an executable's identity. Other programs without that
generated shape, customized commands, and unrelated sibling fields remain.

On Linux, install and uninstall hold an exclusive `flock` on the resolved parent
directory across read, merge, and publication (five-second acquisition limit).
Participating editors must hold the same directory lock for their entire update;
do not lock the replaceable `hooks.json` inode. Ktx stages and syncs a complete
replacement, preserves existing permission bits, and rejects malformed documents
and symlink document targets rather than overwriting them. A symlinked parent
directory is resolved and checked for replacement. Show remains read-only and
does not acquire a mutation lock or create directories.

Ktx checks document identity, bytes, permissions, modification time, and parent
identity before publishing or deleting. Observed external edits abort with retry
guidance. This is not filesystem compare-and-swap: a noncooperating editor can
still race the final check-to-rename/remove window. Avoid simultaneous edits from
such tools. An interrupted update leaves a complete old or new document, not a
truncated shared file; power-loss durability of the directory entry is not
guaranteed by this file-sync protocol.

### Claude Code

```bash
ktx init claude-code
ktx init claude-code --show
ktx init claude-code --uninstall
```

Install generates a local plugin marketplace named `ktx-local` with two
plugins and never edits Claude Code settings. `ktx` holds one `PostToolUse`
hook (matcher `Bash`) that runs the pinned canonical binary in exec form as
`<ktx> hook --runtime claude-code` with no timeout. `ktx-stats` is a mod that
polls `ktx stats summary` every 10 seconds and shows a status line and a `/ktx`
pane; its figures cover every runtime. Register once with
`claude plugin marketplace add <folder>`, `claude plugin install ktx@ktx-local`,
and `claude plugin install ktx-stats@ktx-local`; the install output prints the
exact commands. After regenerating, run `/reload-plugins` in Claude Code.

`--project` is a usage error. A `.ktx-generated.json` marker records the
revision, pinned path, and generated files. Show reads only the marker and
those files, reports staleness, exits 2 when nothing is generated, and never
verifies Claude Code registration. Uninstall removes only listed files and the
`.claude-plugin/types/` folders, keeps and warns about other files, and prints
`claude plugin marketplace remove ktx-local`. See the
[Claude Code guide](../../../../docs/guides/claude-code.md).

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
- The session closes after child/output settlement.
- On normal completion, ktx exits with the child's exit code.
- `--record-stream` retains JSONL for `ktx bench run --replay <session-id>`.

Wrapper `--quiet/-q`, `--help/-h`, `--record-stream`, and `--collection=<id>`
work before or after the executable, up to the first `--`. That separator is
removed; every later token is literal child argv. To pass a literal `--` to the
child, supply a second one. For example:

```bash
ktx run --quiet opencode -- --help
ktx run /bin/echo -- --record-stream
ktx run <agent> -- --prompt --quiet
```

The wrapper does not infer child-option arity: without the escape boundary,
`--quiet` in `--prompt --quiet` is still a wrapper flag. Attached child values
such as `--prompt=--quiet` remain intact. `--format` is ktx-owned only before
`run`; after the executable, both it and its value are child arguments (use the
boundary if that value looks like a wrapper flag).

Booleans are bare (true) or attached with `=`; they never consume the next
token, so `ktx run --record-stream false` launches the executable named `false`.
`--quiet` and `--help` accept Go boolean spellings (`true/false`, `1/0`,
`t/f`, `T/F`, `TRUE/FALSE`, `True/False`). Recording accepts only
`true/false/1/0`. Empty or malformed explicit booleans are usage errors. The last
boolean wins, including false overriding an earlier or global quiet flag.
Collection retains its `--collection=<16-letter lowercase ID>` form; duplicate,
empty, or malformed explicit IDs are rejected. These wrapper usage errors and
wrapper help return before database setup or child execution.

### Terminal cancellation

Terminal-mode `ktx run` is supported on Linux and requires readable procfs,
subreaper support, `pidfd_open`, and `waitid(P_PIDFD)`. These capabilities are
normally available on Linux 5.4 or newer, subject to backports and syscall
policy; ktx checks capabilities, not a kernel-version threshold. If required
preparation is unavailable, it returns an explicit `prepare descendant ownership`
error (exit 4) before starting the wrapped child. This is not a DB-free preflight:
session setup can already have occurred. There is no unsafe cleanup fallback.

Terminal mode includes redirected stdin when a controlling terminal remains
available through `/dev/tty`. The wrapper keeps inherited stdin and the caller's
terminal process group, without transferring terminal ownership or changing
termios. It does not send cancellation signals to the caller's shared group.
Instead, it signals the wrapped leader and individually kills and reaps owned
adopted descendants using identity-checked child waits and pidfds. The caller
and unrelated same-group processes are not cleanup targets.

On SIGINT or SIGTERM cancellation, shutdown offers a one-second SIGTERM grace
period before SIGKILL and stdout-reader closure. Terminal descendant settlement
has one additional second; the collector and supervisor settle before session
and continuity closure. Committed evidence remains available, interruption is
reported, and an interrupted run is not published as a complete stream recording.
The wrapper restores its prior subreaper state. These are shutdown budgets, not
a general runtime deadline or containment guarantee for escaped or
privilege-changing work, uninterruptible kernel tasks, or indefinitely blocked
external output sinks.

The terminal capability requirement does not extend to non-terminal runs or
unrelated commands. Without a controlling terminal, the wrapper retains its
separate owned-process-group cancellation path. Non-Linux terminal execution
is unsupported or unverified, not validated portability.

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

`ktx hook --runtime claude-code` reads the Claude Code 2.1.304 `PostToolUse`
payload. Trusted occurrences are success, except interrupted results, which are
unknown. The hook records Bash `stdout` and, when the pipeline transforms it,
writes `hookSpecificOutput.updatedToolOutput`: the original `tool_response`
with only `stdout` replaced. Otherwise it writes `{}`. `claude-code` is the only
accepted `--runtime` value; any other, missing, or repeated value is a usage
error before the database opens. In this mode a not-found failure exits `1`,
never `2`, because Claude Code shows hook stderr to the model on exit `2`.

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
ktx stats live-validation --from <UTC> --to <UTC> --collection <id> --ingress run --candidate keep-ends:73:1 --format json
ktx telemetry list --session <id> --limit 1000 --format json
```

`stats` excludes trivial groups with fewer than two calls or fewer than 50
delivered estimated tokens. `stats summary` accepts positive Go durations such
as `30m`, `24h`, or `72h`. `stats breakdown` uses the same window syntax and
reports bounded delivery groups by safe command dimensions, profile, route,
disposition, and raw-output-size band; it does not include cache telemetry.
Summary schema 10 is a compact closed object with window, call totals, estimated
token totals, `prompt_cache_token_hit_rate_pct`, attribution coverage, and
latency evidence. Breakdown JSON schema 4 retains aggregate outcomes and
delivery groups. Both use the same output-bearing call population, count only
trusted ingress authority as identified, and leave empty coverage null. Summary text renders only its literal fields and
has no separate schema version. `prompt_cache_token_hit_rate_pct` is runtime-reported
cache-read token evidence in percentage units, not a provider request hit rate.
It keeps measured zero as zero; missing, malformed, and not-applicable evidence
stays unavailable with a null value and is never reconstructed. It is
token-weighted across all accepted providers and models under the supported
runtime contract; it is not an average of their percentages. The summary emits no cache buckets, formula, ratio,
identity, cost, or pricing provenance. Latency reports runtime authority,
millisecond units, requested-window measured/unknown/malformed counts, measured
coverage and UTC contributing bounds, and a measured-only distribution. With no
measured values, state is unavailable and bounds and distribution values are null. Retained
schema 6 summaries remain unchanged historical evidence.

`stats live-validation` requires schema 14 and an explicit collection created by
`ktx run --collection=<id> opencode ...`. It reads one half-open `[from,to)`
window, validates the pinned `keep-ends:73:1` controlled artifacts, and reports
delivery, disposition, outcome, distinct-cue recovery, wrapper-run, latency, and
continuity gates. It emits a structured blocked report and exits 3 when any
required population or authority is insufficient. The command is read-only.

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
```

- `doctor` and `db report` automatically prepare a supported older schema, then
  inspect the database read-only.
- `db report` emits JSON report schema 3 over all tool calls. Outcome
  classification and formulas match live stats; percentages use two-decimal
  rounding, malformed values are unknown, and exit codes remain diagnostic.
- `db migrate` is a forward-only diagnostic and rehearsal surface, not a normal
  prerequisite. The example requires the operator to preconfirm ownership of
  the selected target; never substitute the ambient live database for
  development, test, CI, or rehearsal.
- `prices update` performs a network fetch and atomically rewrites the cache.

## Output and Exit Conventions

- `0`: success. Some commands also use `0` for informational drift or degraded
  read-only diagnosis; inspect structured status.
- `1`: usage or input contract error.
- `2`: not found where applicable.
- `3`: conflict.
- `4`: internal, integrity, verification, or hard health failure.
- `5`: schema incompatible on database access.

`ktx run` forwards the child process exit code, so it can return values outside
this ktx error taxonomy. Command-specific help is authoritative for the outcomes
it documents. Write data to stdout and diagnostics to stderr; do not parse human
summaries when structured JSON exists.
