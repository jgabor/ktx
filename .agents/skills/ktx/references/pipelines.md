# Pipelines, Routing, and Recovery

This derived task summary is not a canonical contract. Project memory owns
intent and scope, implementation and tests establish current behavior, and
public docs/help must remain synchronized contract surfaces. Pipeline
configuration is opt-in at `$XDG_CONFIG_HOME/ktx/ktx.toml`. With no
configuration, the `default` profile exists with no stages and output passes
through unchanged.

Use the CLI for configured stage lists and a partial route projection:

```bash
ktx config show --format json
ktx plugin list --json
```

`config show` does not report the loaded path, timeout, recovery-footer values,
or route subcommands; text mode omits routes. It rejects an invalid configuration
rather than showing a partial projection. Inspect the selected `ktx.toml`
directly for those safety-relevant fields and treat `config show` as partial, not
proof of the complete resolved configuration.

## Shipped Broad Opt-In Example

The shipped configured example includes this broad, lossy recipe:

```toml
[pipeline.default]
timeout = "2s"
recover_footer = true
stages = [
  "unwrap-result",
  "json-compact",
  "strip-ansi",
  "dedup-blank",
  "normalize",
  "keep-ends:100:50",
]
```

This is an existing risk-bearing example, not a generally safe starting point,
a new default, or a policy change. The shipped product default remains
passthrough. Enabling it is an explicit operator choice. `keep-ends` drops
content; assess each tool and route, retain originals, and keep a recovery
footer unless the profile is demonstrably semantic-preserving.

## Built-In Stages

| Stage                          | Behavior                                                                                                                   |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------------- |
| `unwrap-result`                | Strictly project one recognized successful Kagi, Context7, or Codex text envelope; unknown or malformed input is unchanged |
| `json-compact`                 | Remove insignificant whitespace from one complete JSON value or strict NDJSON without re-marshaling                        |
| `strip-ansi`                   | Remove ANSI escape sequences                                                                                               |
| `dedup-blank`                  | Collapse repeated blank lines                                                                                              |
| `normalize`                    | Normalize line endings and trim trailing line whitespace while preserving outer blank lines                                |
| `head:N`                       | Keep the first N lines; default N is 10                                                                                    |
| `truncate:N`                   | Keep the first N bytes and append a truncation marker; default N is 4096                                                   |
| `keep-ends:N:M`                | Keep first N and last M lines with an omission marker                                                                      |
| `grep:pattern`                 | Keep matching lines; invalid regex passes through                                                                          |
| `grep-v:pattern`               | Remove matching lines; invalid regex passes through                                                                        |
| `token-budget:N`               | Approximate N tokens from byte length/4, prefer a preceding newline, and append a marker; a long line can be cut mid-line  |
| `replace:pattern::replacement` | Replace every regex match; an empty replacement removes matches                                                            |

Put `unwrap-result` before line-oriented stages. The final structure guard does
not retain its text projection when the composed result is no longer valid JSON.
Put `json-compact` before line-oriented cleanup when JSON syntax must remain
valid. `json-compact` is lexical: it preserves object member order, duplicate
members, number spelling, and string bytes/escapes.

## Runtime Safety Gates

The runner, not individual stages, enforces:

1. Empty-output guard: discard a stage result that empties non-empty input.
2. Stage-error passthrough: an error or recovered panic passes input onward.
3. Unknown-stage skip: warn and continue with unchanged input.
4. Pipeline anti-worsening: discard the complete result unless it is smaller.
5. JSON structure guard: replace malformed results from recognized complete
   JSON, NDJSON, or a JSON prefix with compact faithful structure before the
   anti-worsening decision. Hook delivery omits recovery footers from repaired
   structure; ordinary results retain footer behavior and footer-aware gating.

For hook execution, a profile timeout protects the agent response deadline. The
default is two seconds. On timeout, the hook returns the original and emits a
diagnostic, but it does not cancel or sandbox stage execution. An external stage
process can continue in the background and can retain its normal filesystem,
network, and process privileges.

Persisted outcomes are three-state: `is_error=false` is explicit success,
`is_error=true` is explicit failure, and NULL or invalid is unknown. Persisted
`exit_code` is diagnostic only. A success route requires explicit success; a
failure route requires explicit failure; `any` or an empty selector includes all
three states. Explicit failure returns original output unchanged before the
pipeline. Unknown may follow `any` or default ordinary behavior, but it is not
success.

The OpenCode 1.18.5 wrapper uses terminal error, then supported shell exit, then
completed result, else unknown. Its hook has only supported shell exit evidence;
absent, malformed, or unsupported evidence is unknown. A trusted,
schema-conforming Codex 0.144.4 `PostToolUse` occurrence is success; unconstrained
nested `tool_response` fields cannot override it. Nonconforming or untrusted
payloads are unknown. Flat sidecar `is_error` is a caller assertion whose
authority the caller must establish. Missing `is_error` persists as unknown;
non-boolean `is_error` is malformed and rejected before persistence. Runtime,
model, and provider labels do not strengthen authority, and historical missing
evidence remains unknown.

## Recovery Footer

`recover_footer = true` adds an agent-visible source ID and
`ktx read <source-id>` cue only when compression occurred. A second gate requires
compressed content plus footer to remain smaller than the original.

The persisted compressed artifact never contains the footer. The footer controls
only the delivered envelope and footer-aware gate; it does not select routes or
decide whether stages run.

Do not disable recovery for profiles containing content-dropping stages such as
`head`, `truncate`, `keep-ends`, `token-budget`, `grep`, `grep-v`, or an unknown
external plugin unless loss is explicitly accepted.

## Routing

Routes are evaluated in file order; first match wins. Unmatched calls use the
`default` profile.

Supported selectors:

- `tool`: exact tool name.
- `command_prefix`: raw command prefix.
- `subcommand`: first non-flag token after the executable.
- `exit_status`: documented values are `success`, `failure`, or `any`.
- `profile`: required target profile.

Empty selector fields are wildcards. Put specific routes before broad routes.
Empty `exit_status` normalizes to `any`. The loader rejects every other
non-empty exit-status value, including misspellings and case variants.

```toml
[pipeline.default]
stages = ["strip-ansi", "dedup-blank", "normalize", "keep-ends:100:50"]
recover_footer = true

[pipeline.git-log]
stages = ["strip-ansi", "normalize", "head:20"]
recover_footer = true

[pipeline.json]
stages = ["json-compact"]
recover_footer = false

[[route]]
tool = "bash"
command_prefix = "git"
subcommand = "log"
exit_status = "success"
profile = "git-log"

[[route]]
tool = "todowrite"
profile = "json"
```

Use `subcommand` for commands with flags before the verb, such as
`git --no-pager log`; a raw prefix can miss these forms.

The example success route matches only explicit success. Use an `any` route or
default fallback when unknown calls should receive ordinary configured handling.

## Filter Packs and Residual Waste

A filter pack is a route, profile, and stage list designed for one command
shape. Use `ktx stats breakdown` to choose work by residual delivered volume and
retained disposition rather than guessing.

```bash
ktx stats breakdown --window 72h --format text
```

Before tightening a route, inspect representative retained originals and verify:

- Error blocks and exact occurrence counts survive.
- Head, middle, and tail facts required by the task survive or are recoverable.
- Tool-call inflation does not erase context savings.
- The anti-worsening gate still rejects no-op or larger results.

## External Plugins

```bash
ktx plugin add myfilter /absolute/path/to/filter --description "purpose"
ktx plugin list --json
ktx plugin remove myfilter
```

Plugins read input bytes on stdin and emit output bytes on stdout. They run as
external commands, so treat configuration as executable code. Runner failure
guards still apply, but they are not a sandbox or secret-redaction boundary.
`plugin add` and `plugin remove` modify the active plugin configuration;
`plugin list` is the inspection-only form.

Plugin registration is last-writer-wins after built-ins. A user plugin named
`head`, for example, shadows the built-in `head` stage until removed. Prefer
unique names and inspect `ktx plugin list --json` for collisions before trusting
a familiar stage token.

## Identity and Retry Semantics

- Recognized trusted OpenCode/Codex native tuples can derive deterministic source
  IDs. Runtime IDs are correlation inputs, not ktx record IDs.
- Missing, malformed, or unverified native tuples receive a fresh NanoID.
- Exact eligible retries converge on the first source evidence.
- The first KTX session owner and first compressed artifact remain authoritative.
- Later metadata does not enrich first evidence.
- Changed authoritative evidence is a conflict and leaves the first row intact.

Do not implement retry handling by replacing originals or recompressing an
already authoritative first artifact.
