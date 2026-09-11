# Pipeline evaluation matrix

Named pipeline configs, tasks, and metrics for ranking compression configurations.
This is the evaluation contract for live comparison (`ktx bench compare`) and
ranking (`ktx bench rank`). Product defaults remain opt-in via
`$XDG_CONFIG_HOME/ktx/ktx.toml`; nothing here changes shipped behavior.

Methodology source notes (adapted, not copied):

| Source                     | What we took                                                                              |
| -------------------------- | ----------------------------------------------------------------------------------------- |
| tilth/benchmark            | cost-per-correct headline; multi-skill tasks; per-task reporting; geometric expected cost |
| lean-ctx                   | bounce / tool-call inflation as a quality regression signal                               |
| headroom                   | recovery path measurement when `recover_footer` is enabled                                |
| prior pipeline experiments | rank work by tool volume; conservative failure passthrough as a control task              |

## Named pipelines

Configs live in `tools/bench/pipelines/`. Stage lists are the source of truth.
This document is the authority for current recipe recommendations. The
commented copies in `ktx.example.toml` are a retained configuration snapshot,
not a second recommendation authority; archived benchmark material is also
historical.

| Pipeline ID              | File                          | Stages                                             | Intent                                                          |
| ------------------------ | ----------------------------- | -------------------------------------------------- | --------------------------------------------------------------- |
| `passthrough`            | `passthrough.toml`            | `[]`                                               | Live baseline; no compression                                   |
| `sanitize`               | `sanitize.toml`               | `strip-ansi`, `dedup-blank`, `normalize`           | Safe control; near-passthrough cost on clean output             |
| `keep-ends-mid`          | `keep-ends-mid.toml`          | sanitize + `keep-ends:100:50`                      | **Recommended general recipe**; best $/correct with full pass   |
| `conservative-a`         | `conservative-a.toml`         | sanitize + `keep-ends:50:20` + `token-budget:2000` | Lossy without recovery; partial pass on mid-fact without footer |
| `conservative-a-recover` | `conservative-a-recover.toml` | same as conservative-a + `recover_footer=true`     | Lossy + recovery; full pass via `ktx read`                      |
| `aggressive-head`        | `aggressive-head.toml`        | sanitize + `head:30`                               | Max savings; high mid/tail fact risk                            |
| `aggressive-keep-ends`   | `aggressive-keep-ends.toml`   | sanitize + `keep-ends:30:10`                       | Aggressive but keeps head+tail                                  |

The live A/B defaults are the registered pipeline IDs `passthrough` and
`conservative-a`. Select another registered ID through `--baseline` or
`--candidate`.

Live A/B vocabulary: each run compares a **baseline** pipeline to a **candidate**
pipeline (summary keys `pipelines.baseline` / `pipelines.candidate`).

### Recommended recipes

Product default when no config is found remains passthrough (empty stages).
The truncation windows below track live ranking (`expected_cost_per_correct` on
full-pass pipelines), not offline per-call ratio alone. The configured example
also applies the structured projection described below; the named benchmark
pipelines remain unchanged for historical comparison.

The configured default in `ktx.example.toml` prepends `unwrap-result` and
`json-compact` to the sanitize stages and `keep-ends:100:50`. `unwrap-result`
recognizes only strict, successful, single-text JSON envelopes: a content-only
Context7 object with one
`content` text item; a Kagi object with that item, `isError:false`, FastMCP
`wrap_result:true` metadata, and an identical `structuredContent.result`; or a
Codex `webrun` array with one `input_text` item. Malformed or trailing JSON,
unknown fields, error or empty results, invalid metadata, multiple or non-text
items, and mismatched Kagi duplicates are byte-identical no-ops. It runs first,
so later line-oriented stages can process decoded text. The final structure
guard does not retain that projection when the pipeline result is no longer
valid JSON; it substitutes compact faithful JSON or passes through the original.

| Recipe                                  | Stages (and flags)                                                        | When to use                                                                   |
| --------------------------------------- | ------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| Sanitize only                           | `strip-ansi`, `dedup-blank`, `normalize`                                  | No content loss; little savings on clean output                               |
| **Keep-ends mid (recommended general)** | `unwrap-result` + `json-compact` + sanitize + `keep-ends:100:50`          | Configured example default; JSON structure remains protected after truncation |
| Aggressive head                         | sanitize + `head:30`                                                      | Max token/cost cut; accepts mid/tail loss                                     |
| Lossy + recovery                        | sanitize + `keep-ends:50:20` + `token-budget:2000`; `recover_footer=true` | Narrow windows/budget; agents can recover with `ktx read`                     |
| Build logs                              | sanitize + `grep-v:^debug:` + `keep-ends:100:50`                          | Prefer wider keep-ends over a tight budget when recovery is undesirable       |

Product defaults when fields are omitted: pipeline timeout **2s** and
`recover_footer` **false**. The configured example explicitly enables the
footer. Do not promote bare lossy budgets without recovery as a general
default: live ranking showed partial fact loss when the footer was off.

Unknown configuration keys are rejected before route selection or transformation,
including misspelled selectors and `recover_footer`. Profile names remain
user-defined, and stage names are still resolved by the runner rather than the
config loader. The optional `[logs]` section is validated separately: invalid
logging settings disable logging without invalidating the pipeline.

### JSON compaction

`json-compact` removes only insignificant whitespace using Go's standard
library JSON lexer; it does not decode and re-marshal values. A single complete
JSON value is accepted, including JSON whitespace around that value. Object
member order and duplicates, numeric spelling, UTF-8 text, and string escapes
are preserved exactly apart from whitespace outside strings.

When the full input is not one value, `json-compact` accepts NDJSON only when
every physical line is independently one complete JSON value. Blank NDJSON
records, including whitespace-only records, reject the entire input unchanged.
A single JSON value ending in `\n` loses that newline because it is surrounding
JSON whitespace. In contrast, a multi-record NDJSON input retains one final
`\n`. Invalid JSON, trailing non-whitespace text, and ambiguous multi-record
input all pass through unchanged. Put the stage before line-oriented cleanup
stages when JSON syntax must be retained.

The pipeline runner also protects recognized JSON after every configured stage
has finished. If the final result breaks a complete JSON value, any record in a
multi-record NDJSON stream, or the JSON prefix before a text trailer, the runner
uses a compact faithful rendering of the original structure instead. NDJSON
keeps its original final-newline convention, and a text trailer remains
byte-identical and in order. The normal anti-worsening gate still applies: when
the faithful rendering is not smaller than the original input, the original
bytes pass through and no transformation is retained.

Pipeline stages run sequentially in a goroutine. Each stage receives the byte
slice returned by the previous stage; the runner does not copy stage buffers.
Stages that need ownership must copy their input. On the configured timeout,
the caller immediately returns the original bytes. The pipeline goroutine is
not canceled and can continue until its current work finishes, but its result
is ignored and is not persisted.

When this guard repairs a result, hook delivery does not append a recovery
footer because that decoration would invalidate complete JSON or add invalid
physical NDJSON records. The compressed artifact records that recovery fact.
Initial delivery and exact trusted retries apply the selected retained
artifact's fact, so a discarded retry candidate cannot change footer behavior.
Historical artifacts without the fact remain readable and retain the
conservative ordinary footer behavior; ktx does not infer the fact from stored
content. Ordinary transformed output retains the configured footer and its
footer-aware size gate.

This guard does not route by content or change stage results that remain valid
for the recognized shape. Invalid UTF-8, invalid JSON, blank-record NDJSON,
adjacent JSON streams without NDJSON record boundaries, and other ambiguous
inputs retain the existing conservative behavior.

`recover_footer` controls only the agent-visible `ktx read <source_id>` cue and
the footer-aware gate that accounts for that cue's bytes. It does not control
routing, stage execution, compression, or artifact persistence. A dedicated
semantic-preserving JSON profile can therefore suppress the cue:

```toml
[pipeline.json-compact]
stages = ["json-compact"]
recover_footer = false

[[route]]
tool = "todowrite"
profile = "json-compact"
```

Footer suppression is appropriate here only because `json-compact` omits no
semantic content. Do not copy `recover_footer = false` to profiles containing
`head`, `truncate`, `keep-ends`, `token-budget`, `grep`, `grep-v`, other
content-dropping filters, or unknown external stages.

## Command-aware routing

Named profiles become more useful when different tool calls need
different compression. A `[[route]]` block in `ktx.toml` maps tool-call
metadata to a profile; the router picks the first matching rule and falls
back to the `default` profile when none match.

### Signals

Each route rule can match on up to four signals, combined with AND
semantics — every non-empty condition must hold:

| Signal         | Field            | Meaning                                                                                                                                                                                                                                                                             |
| -------------- | ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Tool name      | `tool`           | Exact tool name (e.g. `"bash"`, `"read"`). Empty matches any tool.                                                                                                                                                                                                                  |
| Command prefix | `command_prefix` | Prefix match on the command string (e.g. `"git"` matches `"git status"`). Empty matches any command. Only applies to tools whose input carries a `command` field; a rule with a command prefix never matches a tool call without one.                                               |
| Subcommand     | `subcommand`     | Exact match on the first non-flag token after the binary name in the command string (e.g. `"status"` for `git status`, `"log"` for `git --no-pager log`). Tokens starting with `-` are skipped. Empty matches any subcommand. Same `command`-field requirement as `command_prefix`. |
| Outcome        | `exit_status`    | `"success"` requires persisted `is_error=false`; `"failure"` requires `is_error=true`; `"any"` (the default when empty or absent) also matches unknown.                                                                                                                             |

`profile` names the target `[pipeline.<name>]` table and is required.
Profile references and non-empty `exit_status` values are validated at
config-load time. Every `profile =` value must name an existing profile, and
`exit_status` must be exactly `success`, `failure`, or `any`; case variants,
surrounding whitespace, and misspellings reject the configuration instead of
acting as wildcard routes. Empty or absent `exit_status` normalizes to `any`.
The router itself never errors at runtime.

Persisted outcome classification has three states. `is_error=false` is explicit
success, `is_error=true` is explicit failure, and NULL or any invalid stored
value is unknown. Persisted `exit_code` is diagnostic only; it never controls
reporting or routing. A success route requires explicit success, a failure route
requires explicit failure, and `any` includes success, failure, and unknown.
Explicit failure returns the original output unchanged before pipeline
execution. Unknown may use an `any` route or the ordinary default fallback, but
it is not success.

Outcome authority depends on ingress:

- The OpenCode 1.18.5 wrapper applies this precedence: a terminal error is
  failure; otherwise a supported shell exit decides success or failure;
  otherwise a completed tool result is success; otherwise the outcome is
  unknown. Error text is retained. The generated OpenCode hook has only the
  supported shell-exit signal; absent, malformed, or unsupported exit evidence
  is unknown.
- A trusted, schema-conforming Codex 0.144.4 `PostToolUse` occurrence is success.
  Its unconstrained nested `tool_response` fields have no outcome authority and
  cannot override the event. A nonconforming or untrusted payload is unknown.
- Flat sidecar `is_error` is an explicit caller assertion. The caller must
  establish its authority. Missing `is_error` persists as unknown; non-boolean
  `is_error` is malformed and rejected before persistence. A runtime, model, or
  provider label does not make the assertion or any otherwise unsupported
  evidence authoritative.
- Historical records without explicit outcome evidence remain unknown. ktx does
  not reconstruct outcomes from exit codes, artifacts, labels, or output text.

### Subcommand vs command prefix

`subcommand` and `command_prefix` both inspect the `command` string
in a tool call's input, but they answer different questions and have
different precision:

- `command_prefix` is a raw prefix match on the whole command string.
  `command_prefix = "git"` matches `"git status"`, `"git log --oneline"`,
  and `"git rebase"`. `command_prefix = "git status"` would miss a call
  like `"git --no-pager status"` (the prefix diverges at the second
  token) and would also match `"git status-extra"` verbatim, since the
  prefix check does not tokenize.
- `subcommand` is an exact match on the first non-flag token after the
  binary name. The binary name (first token) is dropped; tokens
  starting with `-` are treated as flags and skipped; the first
  remaining token is the subcommand. So `"git --no-pager status"`
  extracts `"status"`, and `"git diff --stat"` extracts `"diff"`.
  Empty matches any subcommand.

Both fields require the tool call's input to carry a `command`
field (the same JSON field `command_prefix` reads); if absent, a rule
with a non-empty `subcommand` never matches that call. The two can
also appear on the same rule and combine with AND semantics, like the
other signals.

Use `subcommand` when you want to key on the verb the user ran —
route `git status`, `git diff`, and `git log` to different profiles
even when flags move around (`git --no-pager log` still routes as
`log`). Use `command_prefix` when the routing signal is the binary
itself — `ls`, `find`, `make` — or commands whose first post-binary
token is usually a flag or path rather than a stable verb.

### First-match-wins

Rules are evaluated in file order. The first rule whose conditions all
hold selects the profile, and evaluation stops. Put specific rules
before broad ones. A rule with every match field empty matches every
tool call, so it acts as a catch-all — place it last to override the
default fallback for unmatched calls.

### Default fallback

When no rule matches (or there are no `[[route]]` blocks at all), ktx
uses the `default` profile. The `default` profile is always present;
when `[pipeline.default]` is absent from the config its stages are
empty, which is passthrough — the output is returned unchanged. So
behavior without a config is unchanged: no routing, no compression.

Persisted compressed artifacts record the selected profile, route provenance
(`matched` or `default_fallback`), and retained stages in one JSON object. A
route explicitly targeting `default` is therefore distinguishable from using
`default` because no route matched.

### When to use routing

- **Different tools, different needs** — command stdout (`bash`) often
  tolerates aggressive `head`/`keep-ends`; file reads (`read`) may need
  wider `keep-ends` to preserve mid-file facts needed for navigation.
- **Failure versus success** — explicit failures already bypass every pipeline
  unchanged. Use success-only routes only for explicit success; use `any` or the
  default fallback when ordinary handling should also include unknown calls.
- **Command-specific recipes** — route `git log` output to a head-based
  profile while leaving other `bash` output on the default profile.

Routing selects among the recipes above per call; it does not replace
them. See `ktx.example.toml` for commented `[[route]]` examples.

### Filter packs

A filter pack is the combination of a route rule, a profile, and the
stages that make that profile effective for a specific command. The
`replace` stage is the tool that makes filter packs practical for
patterns that `grep`/`grep-v` alone cannot handle — shortening
repeated path prefixes, stripping timestamp prefixes, or redacting
secrets before further compression runs.

The `replace` stage takes the form `replace:pattern::replacement`.
The `::` (double colon) separates the regex pattern from the
replacement string. An empty replacement removes matches entirely.
The anti-worsening gate discards the pipeline result when the
replacement does not shrink the output (same length or no match).

Worked example — a filter pack for `find` output that shortens
absolute paths and keeps only the first 50 lines:

```toml
[pipeline.find]
stages = ["strip-ansi", "normalize", "replace:/home/user/project/::./", "head:50"]

[[route]]
tool = "bash"
command_prefix = "find"
profile = "find"
```

The `replace` stage runs after `strip-ansi` and `normalize` clean
the input but before `head:50` truncates it, so the path
shortening contributes to the line budget. If the paths are not
present (no match), the anti-worsening gate discards the
pipeline result and the original passes through unchanged.

### Residual-waste discovery

`ktx stats` ranks tool/command groups by delivered token
volume so you know which commands to target with filter packs or
tighter compression. It surfaces both compressed and uncompressed
calls — uncompressed calls appear with delivered tokens equal to
raw tokens, so the command is useful before any profiles are
configured.

```bash
ktx stats
ktx stats --format text
ktx stats --limit 5
ktx stats summary --window 30m --format json
ktx stats breakdown --window 30m --format json
ktx stats live-validation --from 2026-08-01T00:00:00Z --to 2026-08-04T00:00:00Z \
  --collection collectionabcdef --ingress run --candidate keep-ends:73:1 --format json
```

Groups with fewer than 2 calls or fewer than 50 delivered tokens
are excluded as trivial. The output is ranked by total delivered
tokens (descending). Use this to find the commands where a filter
pack or a more aggressive profile would have the highest impact. Command labels
use the fixed reporting vocabulary described below; other inputs use an empty
command label rather than exposing raw command text.

`ktx stats summary` emits a compact closed aggregate for periodically polled
clients. Its JSON contract contains the window, headline call and token totals,
a runtime-reported `prompt_cache_token_hit_rate_pct`, agent-attribution coverage,
and latency evidence. The
schema is closed: further fields require an intentional schema-version change.
`ktx stats breakdown` owns the detailed delivery groups.
`ktx stats live-validation` is a purpose-specific, read-only report for frozen
explicit-collection windows. Its closed `ktx.stats.live-validation/v1` JSON
contract reports each hard gate as `qualified` or `blocked`, uses only linked
future evidence, and validates the checked-in controlled matrix and receipt.
Installed operation may override those artifact paths with `--matrix` and
`--receipt`. A blocked report exits with the conflict status after emitting the
report. Readiness can prepare a supported older schema; the report itself is
read-only afterward.
Summary schema 10 omits outcomes, delivery rates, cost views, and cache detail.
Callers that need delivery groups should invoke the breakdown schema 4 command.

The summary and breakdown commands use an inclusive trailing window (`[from,
to]`) ending at one captured wall-clock time. Only calls with non-NULL output and
an RFC3339 timestamp in that window are counted. All three stats views use each
call's first compressed artifact by timestamp, breaking timestamp ties by the
lexicographically smallest artifact ID; calls without an artifact use their raw
output. Later recompressions cannot replace delivered evidence and are never
double-counted.

For hook-processed calls, `ktx stats breakdown` reports the retained compression
disposition separately from artifact coverage. The source-keyed disposition is a
monotonic convergence projection, not a retry-occurrence ledger: the first
non-artifact observation remains authoritative until a first compressed artifact
exists, which promotes the projection to `artifact_persisted`; it never
downgrades afterward. Exact retry races may transiently return raw output before
that artifact exists, and this report does not reconstruct those deliveries.
Historical calls, collection-only ingress, explicit compression without a hook
observation, and other calls without retained disposition evidence report
`unobserved` rather than an inferred outcome.

Summary `prompt_cache_token_hit_rate_pct` is in percentage units from 0 to 100
when `measured`; measured zero remains `0`. Unavailable states retain the state
and use `null` for `value`. `contributing_model_steps` is a count. The percentage
is `sum(cache_read) / sum(uncached_input + cache_read + cache_write) * 100`
across all accepted OpenCode 1.18.5 model steps in the window. It pools all
accepted providers and models, including missing provider/model identities, by
token volume; it does not average
per-step, per-model, or per-provider percentages. Every contributing step must
have all three measured buckets. This is token-weighted runtime reporting, not a
provider request hit rate or cache-internal claim. The breakdown does not query
or emit model-step telemetry.

Both windowed commands classify every qualifying call's persisted latency as
`measured`, `unknown`, or `malformed`. Measured observations have `runtime`
authority and millisecond units. The distribution contains measured values
only. Coverage is measured calls divided by all qualifying calls, including
unknown and malformed calls. Contributing bounds cover measured calls only;
timestamps are ordered as instants and emitted as canonical UTC RFC3339Nano.
When no measured value exists, latency state is `unavailable`, contributing
bounds and distribution values are `null`, and the distribution count is zero.
Unsupported ingress remains unknown; ktx does not infer local timing.

Agent-attribution coverage uses this same output-bearing call population.
`identified_calls` includes only sessions whose attribution authority is
`identified`, established by trusted ingress. Every other call is unknown,
including calls from historical or unverified sessions whose agent label is
non-empty. Identified plus unknown calls reconcile exactly to `total_calls` and
`calls.total_with_output`. `coverage_pct` is identified calls divided by total
calls. It is `null` when the population is empty, not a fabricated 0% or 100%.
Text output reports the same counts, percentage, and `trusted_ingress` authority
boundary.

The default window is `30m`; `--window` accepts a positive Go duration. The
current summary JSON contract is schema 10. Retained schema 6 summaries keep
their original bytes and meaning; this read-only query neither migrates nor
rewrites them. The schema 10 contract is:

```json
{
  "schema_version": 10,
  "window": {
    "seconds": 1800,
    "from": "2026-07-21T11:30:00Z",
    "to": "2026-07-21T12:00:00Z"
  },
  "calls": {
    "total_with_output": 51,
    "with_compressed_artifact": 42
  },
  "tokens": {
    "raw_estimated": 37370,
    "delivered_estimated": 14200
  },
  "prompt_cache_token_hit_rate_pct": {
    "state": "measured",
    "value": 70,
    "contributing_model_steps": 6
  },
  "agent_attribution": {
    "authority": "trusted_ingress",
    "identified_calls": 42,
    "unknown_calls": 9,
    "total_calls": 51,
    "coverage_pct": 82.35294117647058
  },
  "latency": {
    "state": "measured",
    "unit": "milliseconds",
    "authority": "runtime",
    "measured_calls": 42,
    "unknown_calls": 8,
    "malformed_calls": 1,
    "total_calls": 51,
    "coverage_pct": 82.35294117647058,
    "contributing_from": "2026-07-21T11:31:00.123456789Z",
    "contributing_to": "2026-07-21T11:59:30Z",
    "distribution_ms": {
      "count": 42,
      "min": 0,
      "p50": 12,
      "p95": 40,
      "max": 55,
      "average": 14.5
    }
  }
}
```

The breakdown JSON contract is separate so its diagnostic rows cannot enlarge
the summary:

```json
{
  "schema_version": 4,
  "generated_at": "2026-07-21T12:00:00Z",
  "window": {
    "seconds": 1800,
    "from": "2026-07-21T11:30:00Z",
    "to": "2026-07-21T12:00:00Z"
  },
  "calls": {
    "total_with_output": 51,
    "with_compressed_artifact": 42
  },
  "outcomes": {
    "successful_calls": 40,
    "error_calls": 5,
    "unknown_calls": 6,
    "known_calls": 45,
    "total_calls": 51,
    "error_rate_pct_of_known": 11.11111111111111,
    "outcome_coverage_pct": 88.23529411764706
  },
  "agent_attribution": {
    "authority": "trusted_ingress",
    "identified_calls": 42,
    "unknown_calls": 9,
    "total_calls": 51,
    "coverage_pct": 82.35294117647058
  },
  "tokens": {
    "raw_estimated": 37370,
    "delivered_estimated": 14200,
    "raw_estimated_per_second": 20.761111111111113,
    "delivered_estimated_per_second": 7.888888888888889,
    "reduction_percent": 62.001605565962
  },
  "delivery_breakdown": {
    "detailed_group_limit": 100,
    "distinct_groups": 2,
    "omitted_distinct_groups": 0,
    "groups": [
      {
        "tool": "bash",
        "command": "git",
        "subcommand": "status",
        "profile": "git-status",
        "route": "matched",
        "disposition": "artifact_persisted",
        "raw_output_size_band": "1k-10k",
        "calls": 42,
        "with_compressed_artifact": 42,
        "raw_estimated": 30000,
        "delivered_estimated": 6830
      },
      {
        "tool": "read",
        "command": null,
        "subcommand": null,
        "profile": null,
        "route": null,
        "disposition": "unobserved",
        "raw_output_size_band": "1k-10k",
        "calls": 9,
        "with_compressed_artifact": 0,
        "raw_estimated": 7370,
        "delivered_estimated": 7370
      }
    ],
    "other": {
      "calls": 0,
      "with_compressed_artifact": 0,
      "raw_estimated": 0,
      "delivered_estimated": 0
    }
  },
  "latency": {
    "state": "measured",
    "unit": "milliseconds",
    "authority": "runtime",
    "measured_calls": 42,
    "unknown_calls": 8,
    "malformed_calls": 1,
    "total_calls": 51,
    "coverage_pct": 82.35294117647058,
    "contributing_from": "2026-07-21T11:31:00.123456789Z",
    "contributing_to": "2026-07-21T11:59:30Z",
    "distribution_ms": {
      "count": 42,
      "min": 0,
      "p50": 12,
      "p95": 40,
      "max": 55,
      "average": 14.5
    }
  }
}
```

The breakdown's `reduction_percent` is `(raw_estimated - delivered_estimated) /
raw_estimated * 100`, and is `null` when `raw_estimated` is zero. Calls with an
empty (but non-NULL) output are included in the call counts and contribute zero
tokens. `with_compressed_artifact` measures retained-artifact coverage, not
whether compression reduced output size.

The breakdown aggregate `outcomes` population is exactly the same output-bearing calls
with valid RFC3339 timestamps in the inclusive window. Classification uses only
persisted `is_error`: `false` is successful, `true` is error, and NULL or invalid
is unknown. The formulas are:

```text
known_calls = successful_calls + error_calls
total_calls = known_calls + unknown_calls
error_rate_pct_of_known = error_calls / known_calls * 100
outcome_coverage_pct = known_calls / total_calls * 100
```

For an empty population, both percentages are JSON `null` and text `-`. For an
all-unknown population, error rate is `null`/`-` and coverage is `0`. For an
all-known population with no errors, error rate is `0` and coverage is `100`.
Breakdown groups do not carry per-group outcome rates.

Summary schema 10 and breakdown schema 4 supersede their previous structured
schemas without a compatibility bridge. Retained schema 6 summaries remain
unchanged historical evidence. Summary text output has one literal row with its
window, calls, token totals, `PROMPT_CACHE_TOKEN_HIT_RATE_PCT`, contributing
model-step count, attribution counts and authority, plus latency state,
authority, coverage, bounds, and distribution; it has no separate schema
version. Breakdown text also has no separate schema version.

Breakdown delivery groups are ranked by delivered estimate and capped at 100.
`other` contains the exact totals for omitted groups, so detailed groups plus
`other` reconcile exactly to the headline call count, artifact count, raw
estimate, and delivered estimate. Estimates apply `ceil(characters / 4)` to
each call before grouping. Command and subcommand are derived at query time from
a fixed reporting vocabulary so arbitrary arguments never become labels.
Commands are limited to `cat`, `find`, `git`, `grep`, `ls`, and `rg`; only the
`git` subcommands `diff`, `log`, and `status` are reported. Other command and
subcommand values are `null`. Profile and route are retained operational
metadata and are `null` for `unobserved` or otherwise unavailable evidence.
`default_fallback` is the unmatched-routing signal; it is orthogonal to
disposition because a fallback profile can still persist an artifact.

Disposition values are `artifact_persisted`, `intentional_passthrough`,
`anti_worsening_rejected`, and report-only `unobserved`.
`intentional_passthrough` includes configured passthrough and conservative raw
delivery when processing cannot safely produce an artifact. Pipeline and
recovery-footer size-gate rejections are `anti_worsening_rejected`. Raw output
size bands use character counts: `empty` (0), `1-1k` (1-1024), `1k-10k`
(1025-10240), `10k-100k` (10241-102400), and `100k+` (102401 or more).
Disposition does not replace existing delivery semantics: artifact coverage and
delivered estimates use the first retained artifact, including artifacts whose
delivery provenance is unavailable.

### Database evidence report

`ktx db report --output -` emits read-only report schema 3. Unlike the live
window reports, its outcome population is every row in `tool_calls`, including
calls without output or a valid timestamp. Its `failure_metrics` uses the same
three-state `is_error` classification and the same known and total denominator
formulas. Percentages retain the report's existing two-decimal rounding. Empty
and all-unknown populations have a JSON `null` error rate; an empty population
also has `null` coverage, while a non-empty all-unknown population has coverage
`0`. Malformed stored `is_error` values remain unknown. `exit_codes` is a
diagnostic distribution only and does not change outcome classification.

Report schema 3 supersedes report schema 2 without a compatibility bridge. The
report is JSON-only, so it has no separate text schema. Readiness can prepare a
supported older schema; the report then opens the selected database read-only.

## Portable offline replay

Replay has two explicit selectors with different compatibility contracts. They
are mutually exclusive; a path-like value passed to `--replay` remains a
session ID and is never interpreted as a file.

### Database session replay

```bash
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to replay}"
ktx bench run --replay "$KTX_TARGET_SESSION_ID"
```

This path opens the configured database, loads the recorded stream by session
ID, and preserves the established output envelope and exit behavior. It checks
the stream hash and parser count, re-aggregates usage, cost, and cache evidence,
and measures compression without making an agent call. A benchmark-parented
stream compares replayed values with its original benchmark row. A parentless
stream remains supported, but `original_bench_run` is null and cost comparison
is unavailable; it is not equivalent to full portable verification.

Database replay loads `ktx.toml` once for the entire operation. The legacy
`compression.pipeline` field remains the configured default-stage projection
for existing consumers. `compression.pipeline_runs` is the authoritative,
bounded execution report: up to 100 distinct selected-profile,
`matched`/`default_fallback` route, and retained-stage tuples appear with their
call counts. `pipeline_runs_truncated` and `pipeline_runs_unreported_calls`
report omitted calls after that limit. Failure gates and anti-worsening
rejections report an empty retained stage list. A malformed configuration exits
1 before emitting a replay result rather than masquerading as passthrough.
Portable fixture replay remains configuration-independent and does not emit
this product-routing projection.

`PASS` exits 0. A usage, cost, or cache mismatch is informational `DRIFT` and
also exits 0 so existing consumers remain compatible. Hash or parser mismatch
is `FAIL` and exits 4. A missing recorded stream exits 2; selector and format
errors exit 1; database and output failures exit 4.

### Version 1 fixture contract

```bash
: "${KTX_REPLAY_FILE:?set this to the portable replay fixture path}"
ktx bench run --replay-file "$KTX_REPLAY_FILE"
```

File replay is strict and offline: it does not open a database, load user
configuration or plugins, contact a model or network service, start a
container, or require credentials. Version 1 is one JSON value with no unknown
fields and these required sections:

| Section                | Required evidence                                                                                                                                   |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `version`              | Integer `1`.                                                                                                                                        |
| `benchmark`            | Non-empty `methodology_version`, `agent`, `model`, and `task`, plus a finite `score`.                                                               |
| `stream`               | `format`, raw JSONL `content`, and its SHA-256 digest as exactly 64 lowercase hexadecimal characters. Export currently requires `opencode-jsonl-1`. |
| `expected.parser`      | Non-negative `tool_use_count`.                                                                                                                      |
| `expected.usage`       | `tokens_in`, `tokens_out`, `cost`, `cache_read`, and `cache_write`; every key is required and each value may be null.                               |
| `expected.compression` | Ordered pipeline identity, baseline and compressed byte counts, bytes saved, and ratio.                                                             |

Null means the stream did not report a value. Zero means it explicitly
reported zero; replay and export preserve this distinction for usage, cost,
and cache evidence. The version 1 compression pipeline is the controlled
built-in sequence `strip-ansi`, `dedup-blank`, `normalize`. User configuration
and external stages cannot alter fixture evaluation. Error tool outputs remain
uncompressed, and the ordinary conservative and smaller-only pipeline behavior
still applies.

Malformed or unsupported fixtures fail before evaluation with exit 1,
`failure_class=invalid-replay-fixture`, and a correction. A well-formed fixture
always emits inspectable replay evidence; any hash, parser, usage, cost, cache,
pipeline, or compression expectation mismatch produces `consistency=FAIL`,
`failure_class=replay-fixture-verification-failed`, and exit 4. Output write
failures also exit 4. Only a complete match exits 0; unlike database replay,
fixture drift is never informational.

### Authoritative export and refresh

Version 1 export accepts only a recorded stream with one consistent session
and benchmark parent. The stored stream hash and parser count must match, and
stream-derived tokens, cost, and cache values must agree with the benchmark
row. Parentless streams, broken links, unsupported stream formats, and
inconsistent evidence are rejected before publication with exit 1 and
`failure_class=invalid-replay-fixture-source`. A missing session exits 2.

Export from one read-only database snapshot. The default destination is stdout:

```bash
: "${KTX_OWNED_DB:?set this to an explicitly operator-owned database path}"
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to export}"
KTX_DB="$KTX_OWNED_DB" ktx bench export "$KTX_TARGET_SESSION_ID" > replay-v1.json
# Equivalent explicit stdout delivery:
KTX_DB="$KTX_OWNED_DB" ktx bench export --output - "$KTX_TARGET_SESSION_ID"
```

`KTX_OWNED_DB` is an operator ownership declaration, not proof that the path is
disposable or safe to modify.

Prefer `--output` when publishing a file; publication is atomic and successful
file delivery writes only a diagnostic to stderr:

```bash
: "${KTX_OWNED_DB:?set this to an explicitly operator-owned database path}"
: "${KTX_TARGET_SESSION_ID:?set this to the database session ID to export}"
KTX_DB="$KTX_OWNED_DB" ktx bench export \
  --output /tmp/portable-replay-v1.json "$KTX_TARGET_SESSION_ID"
```

The output may not alias the source database by path, symlink, or hard link.
Export never mutates the source database and includes no invocation timestamp,
database path, session ID, or benchmark-run ID, so unchanged authoritative
evidence produces byte-identical fixture bytes.

To refresh the retained fixture, export to a temporary path with the command
above; run `ktx bench run --replay-file /tmp/portable-replay-v1.json`; review
the complete embedded stream and metadata for credentials, personal or user
content, repository data, and machine-local names or paths; update
`internal/cli/testdata/README.md` with the source and privacy review; then copy
the reviewed bytes to `internal/cli/testdata/portable-replay-v1.json`. Do not
hand-edit expectations: regenerate them from the authoritative capture. Finish
with `mage replayFixture` and `mage verify`.

The fixture SHA-256 and strict expectations detect accidental content or code
drift. They do not protect against a committer changing both content and
expectations; privacy, provenance, and synchronized expectation changes remain
code-review responsibilities. The exporter performs consistency validation,
not redaction.

`mage replayFixture` is the bounded offline gate for the retained fixture and
runs it twice plus strict negative cases. `mage verify` is the authoritative
repository command: it runs test, vet, lint, build, and then that fixture gate.
Tracked CI invokes `mage verify` for every push and pull request.

## Task suite

Tasks live in `tools/bench/tasks/*.toml`. The synthetic corpus is generated by
`ktx bench corpus generate` into `tools/bench/corpus/` (~609 KB,
~152k tokens chars/4 if fully ingested).

### Workspace isolation

Corpus tasks set `workspace_mode = "corpus"`. Each comparison attempt mounts
**only** the listed `corpus_files` (`.txt` fixtures) under
`/work/tools/bench/corpus/`. Answer keys are never mounted:

- `MANIFEST.json`
- `tools/bench/tasks/*` (fact lists in prompts/specs)
- `internal/experiment/corpus.go`
- `internal/experiment/testdata/`
- `docs/pipelines.md`

Repo-inspection tasks may set `workspace_mode = "repo"` (full snapshot with the
same answer-key exclusions).

| Task ID               | Skill     | Token band   | Stresses                                                                           | Scoring               |
| --------------------- | --------- | ------------ | ---------------------------------------------------------------------------------- | --------------------- |
| `fact-midfile`        | read      | 30–60k       | mid-file loss under `head:N`                                                       | multi-fact fractional |
| `log-tail-error`      | bash      | 40–80k       | tail errors under pure head                                                        | multi-fact fractional |
| `crossfile-nav`       | navigate  | **100–200k** | incomplete coverage + mid/tail                                                     | multi-fact fractional |
| `ephemeral-midfact`   | bash      | 20–80k       | mid-loss when facts live only in tool stdout (no on-disk re-grep); recovery footer | multi-fact fractional |
| `structured-diff`     | bash      | 30–80k       | JSON structure plus mid-body code-diff semantics and a tail result                 | multi-fact fractional |
| `grep-noise`          | find      | 40–80k       | needle lost in noise                                                               | multi-fact fractional |
| `failure-passthrough` | bash fail | small        | failure signal stripped                                                            | multi-fact fractional |

`crossfile-nav` is the headline heavy task. Smaller tasks diagnose _why_ a
pipeline wins or loses (head vs keep-ends vs budget).

The native risk map is explicit: `log-tail-error` and
`ephemeral-midfact` cover large-log loss; `crossfile-nav` covers
cross-file navigation; `structured-diff` covers structured JSON and code or
diff comprehension; and `failure-passthrough` covers stderr from a non-zero
command.
These are named task contracts, not evidence of general agent skill or complete
benchmark-suite coverage. A measured result supports context or cost and
deterministic-result claims only for the task and protocol that produced it.
`structured-diff` currently has deterministic contract, generation, and
scorer coverage but no model-backed result.

Scoring expectations live in each task file. Corpus-backed planted facts also
appear in `tools/bench/corpus/MANIFEST.json`; ephemeral and derived expectations
remain only in their task contract and are not mounted into the task workspace.

## Metrics contract

All metrics are derived from retained experiment evidence
(`manifest.json` + `attempts.jsonl`). No LLM-as-judge.

### Per-attempt

| Field                                    | Meaning                                                                                           |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `score`                                  | For multi-fact: `matched_facts / total_facts` in `[0, 1]`. For exact-match: `0` or `1`.           |
| `full_pass`                              | `score == 1.0`                                                                                    |
| `facts_matched` / `facts_missed`         | Multi-fact detail                                                                                 |
| `tokens_in` / `tokens_out` / `reasoning` | SUM from stream `step_finish` model-step usage when present on every model step                   |
| `cost`                                   | Runtime-reported cost retained verbatim, including literal zero                                   |
| `estimated_api_cost`                     | Nullable cache-aware estimate from the experiment's retained models.dev pricing snapshot          |
| `cache_read` / `cache_write`             | SUM of `part.tokens.cache.{read,write}` across `step_finish` model steps                          |
| `tool_call_count`                        | Count of retained tool-call rows for the explicit attempt owner                                   |
| `session_id`                             | Precreated singleton owner, verified closed after execution                                       |
| `role`                                   | Cache-aware trial role: `cold_observation`, `warmup`, or `measured_warm`                          |
| `cache_control`                          | Cache-aware mode/source, namespace hash, phase/order/gap, model/provider/variant/runtime metadata |

### Recovery-aware configuration comparison

`ktx bench compare` uses registered baseline and candidate pipeline IDs as the
comparison boundary. Recovery-aware evaluation uses two gates:

1. **Offline screening** runs each configuration over one frozen retained-output
   population through the production pipeline runner. It reports paired
   delivery, anti-worsening, recovery-cue exposure, and footer-cost results
   without model calls. This mechanical evidence cannot establish agent
   recovery decisions or task quality.
2. **Behavioral comparison** runs only shortlisted configurations through the
   existing paired task protocol. Correctness is a gate. Among configurations
   that meet it, the primary outcome is complete runtime-emitted total tokens
   per correct task.

Tool delivery, runtime token fields, cost, quality, recovery, and tool calls
retain separate field authorities. A recovery requires a delivered cue and a
later exact `ktx read <source-id>` match in the same attempt. Per-arm and paired
results report:

- attempts with any linked recovery;
- delivered cues, distinct cues followed, and cue-follow coverage;
- recovery attempts, repeated attempts for one cue, and byte-exact outcomes;
- total tool calls; and
- exact repeated normalized tool calls within an attempt.

An arm with no delivered cues has an unavailable cue-follow rate, not zero.
Exact-repeat summaries expose counts and opaque equality only, never tool
arguments or semantic-overlap claims. Pair cycles descriptively; do not claim
significance, provider behavior, or causal decomposition.

The first acceptance matrix uses passthrough plus `keep-ends:50:10`,
`keep-ends:73:1`, `keep-ends:100:10`, and `keep-ends:100:50`. The summary
contract remains stage-agnostic so the same comparison can evaluate complete
configurations containing `json-compact`, `strip-ansi`, or other stages.

Methodology v7 emits summary schema 7. Its `recovery_aware_comparison` block
keeps field authority explicit, reports each arm and descriptive cycle pairs,
and ranks only quality-qualified arms with complete runtime-emitted input,
output, and reasoning totals. Missing or malformed runtime totals make the
primary outcome unavailable with a reason and exclude the arm from
recommendation. Current incomplete summaries retain the same block with an
unavailable state and explicit unmet-target reason. Validation reports
structural validity separately from target completion. Retired schema-6
summaries are static history, not supported validation or regeneration inputs.

Each schema-7 arm also reports `call_accounting`. `stream` and `persistence`
retain separate authoritative call and error counts. `persistence_coverage` is
the fraction of distinct stream calls with paired persisted rows, and
`discrepancies.counts` is limited to `stream_call_without_persisted_row` and
`persisted_row_without_stream_call`. Exact duplicate stream events are reported
separately from non-identical retry events; neither increases the distinct call
count. Pairing uses the stream-derived persisted source identity within each
attempt, not the different runtime and KTX owner session identities. If either
artifact is unavailable or conflicting, its counts and cross-authority coverage
are null;
the summary does not reconstruct them from the other artifact. Stream-observed
errors remain stream evidence and do not become persisted outcomes without a
paired row. Persisted rows with no authoritative error state are counted under
`unknown_error_state_count`, not as successes.

Validation never rewrites an archive. Unsupported entry versions fail explicitly;
tracked historical reports are preserved without current-validation guarantees.

Run the mechanical gate with `ktx bench screen --population <json>`, repeated
`--config <ktx.toml>`, and one `--anchor-config <ktx.toml>`. The anchor is the
`100:50` acceptance candidate; it advances first only when net delivered-byte
savings are positive and every controlled fact passes. At most two other
qualified configurations follow in descending net savings order. Every
configuration receives the same calls. Raw and delivered bytes and per-call
token estimates, disposition counts, cue counts, and footer bytes reconcile in
schema `ktx.bench.configuration-screen.v1`.

The command reads only the named population and configuration files. It does
not open a database, persist derived features, use credentials, or contact a
model. Output omits source content, paths, call identifiers, and configuration
names. External plugins are rejected because their process boundary could
expose source content; built-in stage order, routing, failure, anti-worsening,
JSON structure, and recovery-footer gates use the production runner.
Each population call may include up to 64 KiB of optional `input` JSON metadata;
it is supplied to routing for command-prefix and subcommand matching but is
never copied into the report. Omitting `input` preserves the original empty-input
behavior of existing populations.

### Per-pipeline aggregates (evaluated attempts)

An attempt is **evaluated** when:
`status=completed`, session linked, stream complete, and
`tool_call_count >= minimum_tool_calls`.

Summary regeneration does not add the copied measurements in
`attempts.jsonl` to retained evidence. All current attempts require the
standard `database` and `stream` authorities: tool-call rows own the call count
and the raw runtime stream owns completion, answer scoring, token, cache,
reasoning, and runtime-reported cost measurements. The manifest owns cohort,
pipeline, methodology, identity, and pricing-snapshot inputs. The first attempt
record owns only orchestration facts and artifact links that cannot be obtained
from those lower-level artifacts.

A repeated attempt record with the same identity and orchestration facts is
idempotent. Differences confined to copied measurements are reported as
projection divergence and do not change the regenerated summary. A repeated
identity with different orchestration facts is an error; the first record is
not replaced. Attempt schemas 1 and 2 are unsupported; copied measurements and
session counters never substitute for missing retained authorities.
For a timeout, failure, invalid result, or unlinked attempt with no session
identity, regeneration still materializes stream-owned fields and assigns no
tool-call evidence; the copied call count is ignored. A completed attempt must
have a retained session and readable database authority.

Partial fact scores are kept; they are not discarded.

For current summaries, every applicable metric is a deterministic
distribution with `count`, sorted `values`, `min`, `p25`, `median`, `mean`,
`p75`, `max`, and `population_standard_deviation`. Quantiles use linear
interpolation at rank `q*(n-1)` in the sorted values. Population standard
deviation divides by `n`. Empty distributions have `count=0` and null
descriptive values; a singleton has standard deviation zero.

| Aggregate                        | Formula                                                                                                                     |
| -------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `mean_fact_score`                | mean of `score` over evaluated attempts                                                                                     |
| `full_pass_rate`                 | fraction of evaluated attempts with `full_pass`                                                                             |
| `mean_cost`                      | mean runtime-reported cost over evaluated attempts                                                                          |
| `estimated_api_cost`             | distribution of independently estimated API-equivalent cost when pricing is complete                                        |
| `estimated_api_cost_per_correct` | `mean_estimated_api_cost / max(full_pass_rate, 1e-9)`                                                                       |
| `expected_cost_per_correct`      | `mean_cost / max(full_pass_rate, 1e-9)`                                                                                     |
| `mean_tokens_in`                 | mean input tokens over evaluated attempts                                                                                   |
| `tool_call_inflation`            | `candidate.mean_tool_calls / baseline.mean_tool_calls` (live A/B)                                                           |
| `cache_read` / `cache_write`     | SUM over `step_finish.part.tokens.cache.{read,write}` per attempt; per-side distribution and baseline-minus-candidate delta |
| `total_logical_input`            | `tokens_in + cache_read + cache_write` when all three observations are available                                            |

`expected_cost_per_correct` is the ranking headline (tilth geometric form).
A cheaper pipeline that loses facts ranks worse than a slightly costlier full-pass pipeline.

`cache_read` / `cache_write` are runtime-reported token counts emitted on
`step_finish.part.tokens.cache`. An OpenCode `step_finish` is a runtime model
step, not a provider request. For every reported model step, tokens.total
== tokens.input + tokens.output + tokens.reasoning + tokens.cache.read +
tokens.cache.write, confirming cache values are event-local deltas (not
cumulative). The summary includes them only
when an attempt contains cache evidence (a runtime that does not emit cache
fields leaves them NULL without changing the summary schema). The
per-side distribution mirrors tokens/cost; `baseline_minus_candidate.cache_read`
and `baseline_minus_candidate.cache_write` give the absolute baseline-minus-candidate
delta so cache effects between strategies are observable alongside token and
cost deltas. ktx does not derive a cache-miss ratio from these counts.

Estimated API cost prices uncached input, cache reads, cache writes, output,
and reasoning independently. The selected base or context-tier `cost.reasoning`
rate is used when present; if absent, reasoning deterministically uses that
record's output rate. An explicit zero reasoning rate remains zero. It never
replaces the runtime-reported `cost`.
If ordinary input/output pricing is absent, or non-zero cache usage has no
matching cache rate, the estimate is NULL. A missing cache rate is harmless
when that cache count is zero; cache reads are never charged at the ordinary
input rate. New experiments retain the exact resolved pricing record and its
SHA-256, and summary regeneration uses that snapshot rather than live data.
If the warm local cache lacks the model or cache rates, run `ktx prices update`.
The retained pricing snapshot alone does not enable estimated-cost summary
fields: those appear only when at least one attempt has a non-NULL estimate.
Current cache-aware methodology v6 runs under the pinned OpenCode 1.18.5 contract
and uses summary schema 5 even when all estimates are NULL. Exact-match uses
methodology `v8-exact-match` and summary schema 8 regardless of cache or pricing
availability; recovery-aware comparison uses methodology v7 and summary schema 7.

Reported cost and estimated API cost have independent completeness gates.
Estimation requires complete input, output, reasoning, cache-read, and
cache-write token evidence; missing reported cost remains NULL but does not
block an estimate. For every methodology, validation checks the snapshot hash
and requires its embedded provider/model and pricing metadata to match the
manifest model. Copied estimates without that retained pricing basis are never
trusted. Regeneration ignores prior summary contents and re-derives estimates
from retained usage and pricing; cache-aware validation requires pricing.

When a model-step numeric field is missing or malformed, the affected
attribution aggregate remains null and carries `state: missing` or
`state: malformed` plus `evidence_gaps` entries naming the field and
`source_event_ordinal`. Validation exposes the same gaps on the affected attempt.
Contractually unsupported telemetry remains `state: unavailable` and does not
acquire a fabricated source-event gap.

### Cache source-event evidence and claims

For the field-by-field differences between benchmark recorded-stream evidence
and ordinary unwrapped OpenCode session telemetry, see the
[current retained cache-observability matrix](opencode-telemetry.md#current-retained-cache-observability-matrix).

The source-event contract is the vocabulary for cache-aware evidence before it
is rolled up to an attempt. It does not change the existing attempt-level
fields or make an aggregate complete from partial telemetry. Each source event
has a one-based ordinal in capture order and independently carries available
uncached input, output, cache read, cache write, reasoning, cost, identity,
cache-control, and retry metadata. Cache-aware attempts retain these ordered
observations in `model_steps`. Historical attempt schema 2 carries wrapper-only
observations. New attempt schema 3 also binds the attempt to one explicit KTX
session and a current event-hook interval. Cache-less and legacy attempts omit
the additive fields. Ordinary observations are optional telemetry:
missing, empty, partial, or malformed observations do not invalidate an attempt.
When available, they are useful for inspecting event order and field availability;
they are not an integrity or attestation mechanism.

Live source events are runtime model-step observations only. The schema value
`model_step` identifies such an observation; it is never a provider request
count. A retained `native_request` record is historical/offline compatibility
evidence from an unsupported former collection path. It is not accepted as live
telemetry or as a supported source for future provider collection.

A source event records each numeric field as `measured`, `unknown`, or
`malformed`. A measured zero is a measurement, not an absence. Missing and
JSON-null fields are unknown. Malformed values are retained as malformed and
never converted to a measurement. An attempt-level field is complete only when
every relevant source event measured that field; otherwise its total is
unknown. This prevents a sum over a subset of source events from appearing as
a complete attempt total.

Provider language such as _hit_ and _miss_ does not expand these claim
boundaries:

| Claim               | Evidence boundary                                                                                          |
| ------------------- | ---------------------------------------------------------------------------------------------------------- |
| `cache_reuse`       | The source reports cache reuse, such as a cache-read token count. It is not a provider-internal hit count. |
| `cache_write`       | The runtime reports cache-write tokens. It does not establish a provider-internal fill.                    |
| `no_reported_reuse` | The runtime reports zero reuse. It does not establish an internal miss or request eligibility.             |
| `unknown`           | The source omitted, nullified, malformed, or cannot authoritatively classify the relevant telemetry.       |

Every source/version capability entry keeps runtime-emitted telemetry, identity
inferred from explicit runtime configuration, provider documentation, pricing
estimates, and historical/offline fixtures separate, with provenance.
Only the first two are current collection inputs. None proves provider-internal
hits, misses, fills, eviction, request boundaries, isolation, or prefix
composition.

Current summaries annotate Tukey outliers using `1.5 * IQR` fences. With
fewer than four observations, detection is marked `insufficient_sample` and no
point is flagged. Every flagged point remains in `values`, means, standard
deviations, paired deltas, and all other aggregates. Outlier reporting is not an
exclusion mechanism.

Evaluated baseline and candidate attempts are paired by cycle, not execution
order. `paired_baseline_minus_candidate` reports each cycle's baseline-minus-
candidate deltas, aggregate mean and median paired deltas, paired count, and
unpaired attempt IDs/cycles. The existing `baseline_minus_candidate` object
remains the unpaired mean-minus-mean compatibility view. Samples with fewer
than five evaluated attempts are labeled `exploratory`; samples of five or
more are `descriptive`. The label does not block smoke runs.

### Secondary quality signals

| Signal                        | Interpretation                                 |
| ----------------------------- | ---------------------------------------------- |
| Tool-call inflation > 1       | Agent bounced / re-fetched under compression   |
| Mid-fact miss + head-fact hit | `head:N` / narrow window damage                |
| Tail-fact miss                | Missing keep-ends / tail window too small      |
| `failure-passthrough` miss    | Failure path incorrectly compressed or dropped |

## How to run

### Canonical benchmark workflow

Use versionless semantic IDs for specs, tasks, baselines, and candidates. Use
experiment IDs for every retained-experiment lifecycle command. Focused help is
the current inventory of registered IDs:

```bash
./build/ktx bench compare --help
./build/ktx bench rank --help
```

Run the offline gate, then create an experiment with either compare or rank:

```bash
./build/ktx bench compare --preflight --spec prompt-cache-token-hit-rate --task crossfile-nav --format json

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

# Or rank registered candidates with the same semantic selectors:
./build/ktx bench rank \
  --spec recovery-aware-comparison \
  --model <model> \
  --task <task-id> \
  --baseline passthrough \
  --candidate conservative-a,keep-ends-mid
```

The generic preflight does not open the ktx database, use authentication, call
a model, or create an experiment archive. Adding `--build` can use the network
and changes local Docker image and build-cache state. Compare and rank create an
archive under `.ktx/experiments/<id>`, use fresh isolated owner databases, use
the network, call the selected provider, and can incur provider charges.

On SIGINT or SIGTERM, compare and rank stop scheduling and clean up their owned
process groups and containers, including capability probes. Cleanup has its own
deadline and runs even after cancellation. A cleanup error stops the comparison
or campaign and reports the container name; inspect that container before
starting another run. Per-attempt timeouts still retain the failed attempt and
may retry within `--max-attempts` after successful cleanup. Interrupted archives
can be incomplete; retained files are not a completed comparison result.

Inspect and maintain the resulting experiment by ID:

```bash
./build/ktx bench list
./build/ktx bench show <experiment-id>
./build/ktx bench validate <experiment-id>
./build/ktx bench summarize <experiment-id>
./build/ktx bench validate <experiment-id>
```

List, show, and validate read retained archives without rewriting them and do
not open the ktx database. Summarize does not call a provider or open the ktx
database, but it rewrites the archive's derived `summary.json`; validate again
after regeneration. Treat the whole experiment archive as sensitive. It can
contain repository snapshots, prompts, outputs, streams, stderr, identifiers,
databases, and secrets. Review a derivative before sharing it.

Archive references must be relative and remain inside the selected archive,
including when symlinks change during inspection. Relative in-archive symlinks
are supported; workspace tree hashes retain link text without reading targets.
Special file types such as FIFOs, sockets, and devices are rejected explicitly.
Database inspection uses disposable copies of confined database and committed
WAL bytes; schema readiness and SQLite sidecar operations affect only those
copies. Empty or zero-header rollback journals are accepted, but a nonzero
rollback-journal header is rejected rather than allowing recovery to follow an
embedded super-journal pathname. Use a completed owner snapshot for inspection.

Pipeline configuration is captured once for execution. Retained pipeline-input
copies come from that capture, not a second read of the source files. Validation
and regeneration hash the actual retained configuration mounted by Docker and
require it to match both the manifest identity and any retained input copy.
Missing or changed execution configurations fail validation even when the input
copy is intact. Separate copies in an otherwise supported current archive must
agree; no historical-format fallback is provided.

Tracked reports and other labeled historical evidence are static history, not
active workflow inputs. The current validator does not guarantee support for
historical archive formats. Do not rewrite historical evidence to make it pass
current validation. The supported experiment contracts are:

| Registered spec               | Methodology                      | Manifest | Attempt | Summary |
| ----------------------------- | -------------------------------- | -------- | ------- | ------- |
| `exact-match`                 | `v8-exact-match`                 | 1        | 3       | 8       |
| `prompt-cache-token-hit-rate` | `v6-prompt-cache-token-hit-rate` | 1        | 3       | 5       |
| `recovery-aware-comparison`   | `v7-recovery-aware-comparison`   | 1        | 3       | 7       |

Compare, rank's pair producer, and inline-task runner use the same retained-source
contract. Registered input snapshots and execution configuration/image identities
remain required; inline task scoring is embedded without a task-file snapshot.
Methodologies v1/v4/v5, attempt schemas 1–2, summary schemas 1–4 and 6, and older
collector contracts are no longer interpreted. List/show may inspect their
metadata, but inspection does not confer validation authority. The inactive
cache probe is removed; historical reports and user-owned archives are not
rewritten or deleted.

### Cache-aware comparison

There is no implicit benchmark spec. The current explicit cache-aware spec uses
methodology v6, summary schema 5, and `cache-reuse-v2`.
It reports the token-weighted prompt-cache hit rate under the pinned contract;
provider-request and provider-internal cache claims remain forbidden.

```bash
./build/ktx bench compare --preflight --spec prompt-cache-token-hit-rate --task crossfile-nav --format json

./build/ktx bench compare --preflight --build --spec prompt-cache-token-hit-rate --task crossfile-nav --format json

./build/ktx bench compare \
  --build \
  --spec prompt-cache-token-hit-rate \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task crossfile-nav \
  --cache-control observed \
  --warmup 1 \
  --rounds 5
```

The generic preflight returns before task loading. It is offline, requires no
auth or model, creates no experiment artifact, and exits nonzero with
deterministic capability reasons.
With `--build`, it checks the local Docker source before building and then asks
only the resulting image for `bench agent --capabilities`. Normal comparisons
run the same gate automatically before checking auth, creating an experiment,
or starting an attempt. Images that do not advertise strict acknowledgements,
retained duplicate callbacks, accepted-model-step projection, callback-queue
continuation, exact model-step identity, fail-closed interval validation, and
this ordering are rejected even if they advertise the older base collector
fields.

Each comparison resolves the selected image once to its full `sha256:` image ID.
The capability probe and all attempts on both sides use that ID, so later tag
changes do not change the selected image. The original tag remains a manifest
label only. A missing or invalid immutable image identity fails preflight before
auth or attempts; this is benchmark image identity, not KTX executable hashing.

Preflight image builds and probes use `--attempt-timeout` and
`--termination-grace` for each command, not as an overall preflight deadline.
Build diagnostics share a 64 KiB budget; probe and cleanup stderr each retain
at most 64 KiB, followed by a truncation notice. Excess diagnostics are drained
without failing an otherwise successful command. Image-ID inspection and
benchmark capability JSON stdout each accept at most 65,536 bytes, including
whitespace. Larger responses stop the producer and return an explicit size
error before trimming or decoding; no truncated response is parsed. This limit
does not apply to tool output, recordings, session lists, Git status, or build
logs. Container cleanup keeps its independent 30-second timeout and one-second
grace even after a probe times out or exceeds its response limit.

Paid comparisons can freeze cost authority with `--pricing-snapshot <pricing.json>`.
The command validates the retained provider/model before auth or attempts, skips
mutable catalog resolution, and copies the exact input bytes into the archive.
Add `--pricing-sha256 <hex>` to require a previously recorded identity. Supplying
the same full comparison arguments with `--preflight` validates the task,
methodology, configurations, image contract, model, variant, and pricing pin
without reading auth, creating an experiment, or starting an attempt. Omitting
the pin preserves the existing catalog-backed workflow.

Each fresh attempt database starts with exactly one `agent=opencode` owner and
one open event interval. The container receives that owner, interval, contract,
and required-collector mode explicitly. The generated plugin opens a random
collector epoch before exposing callbacks, serializes callback commands, and
waits for valid acknowledgements. The supervisor alone seals a complete or
incomplete interval and closes the owner. An open, incomplete, replaced, or
extra owner or interval aborts the experiment before another attempt.

On a complete interval, regeneration derives model-step order and positive
retry, reset, segment, concurrency, and identity coverage from normalized
runtime-event rows through the last accepted model step. Every acknowledged callback
occurrence has a contiguous global ordinal. Exact duplicate model-step
occurrences remain in the ledger but are excluded from accepted measurement and
wrapper identity projection. A conflicting duplicate remains classified,
preserves first telemetry, and prevents complete sealing. Restarts are explicit
extra epochs. Raw callbacks, error bodies, prompts, credentials, and cache
namespaces are not retained. New attempts must have one consistently closed
owner and one complete, contiguous interval with at least one epoch and model
step. Wrapper
and event-hook model steps must have the same native session/message/part tuples
in the same order. Any missing, partial, reordered, duplicated, mismatched, open,
incomplete, contradictory, or corrupt required evidence makes attempt and
experiment validation fail; regeneration cannot promote it into valid evidence.

The retained OpenCode 1.18.5 Luna/high run has five complete, protocol-compliant
measured pairs with full quality on both sides. Its candidate mean
OpenCode-reported cached-input share was higher than baseline under the retained
protocol. That is the supported claim; the result supports no significance,
causal, or provider-native cache wording. Exact values, secondary estimates,
provenance, validation, hashes, and failed archives are in the
[live validation report](cache-reuse-live-validation.md).

Each strategy runs one `cold_observation`, the requested number of `warmup`
attempts, and then `--rounds` `measured_warm` attempts. Setup attempts remain in
`attempts.jsonl` and retain their artifacts, but do not count toward the target
or headline quality/cost distributions. Measured cycles continue alternating
side order. Fewer than five measured attempts is labeled `exploratory`.

Provider-internal isolation is unsupported. `--cache-control isolated` fails
before an agent call, and `auto` records `observed`. Phase names describe the
role of a trial; they do not prove a provider cache hit or miss. Retained cache
metadata contains only a namespace hash. The raw namespace, when required by an
adapter, exists only in ephemeral runtime configuration/command state.
`idle_gap_ms` records the intentional harness delay and is zero when no delay is
imposed; it is not inferred from orchestration overhead.

No built-in OpenCode runtime/provider adapter currently establishes arm
separation or provider-internal isolation. OpenCode 1.18.5 source shows that
`provider.options.setCacheKey=true` derives `promptCacheKey` from the OpenCode
session id and that per-model options can override that value, but source-level
configurability alone does not prove effective separation. `auto` therefore
records OpenCode runs as observed-only, and explicit isolation fails before an
agent attempt. Isolated metadata is checked only in supported current-format
evidence; its labels do not enable isolation for a new run.

Current cache-aware summaries expose distributions for each phase
while keeping `measured_warm` as the headline, a cold-to-warm estimated-cost rebuild
penalty, and the first cumulative measured turn where candidate estimated API
cost is below baseline after setup cost. Unavailable estimates produce null
values with reasons. Prompt-cache summaries also expose token-weighted hit rate,
controlled measured-cycle pairs, and deterministic claim limits. Consecutive-step
reuse remains separately labeled estimated evidence, not direct cache authority. TTFT is null:
the OpenCode 1.18.5 JSONL format does not provide a reliable request-start
timestamp.

Current methodology-v6 schema-5 summaries use the `cache-reuse-v2`
`prompt_cache_token_hit_rate`, `prompt_cache_token_hit_rate_comparison`, and
`prompt_cache_token_hit_rate_statement_authorization` names. Complete paired
evidence reports both means, candidate-minus-baseline percentage-point delta,
pair count, interval, same-pair quality, and blocking reasons.

Schema-5 also emits `pipeline_cost_guard` plus top-level `accepted` and
`acceptance_reasons`. It accepts only a quality-preserving candidate whose
complete paired API-equivalent estimated cost per correct is strictly lower
after six-decimal rounding. Reported runtime cost remains separate, including a
literal zero. A valid `accepted:false` comparison is successful evidence; rank
retains it but marks it ineligible for recommendation and keeps it below the
baseline and accepted candidates.

Validation is methodology-aware. For a new OpenCode 1.18.5 v6 run it requires
summary schema 5, retained pricing metadata and its hash-bound snapshot, exactly
one cold observation and the configured warm-up count per side, explicit roles,
cache metadata consistent with the manifest, and measured-warm-only target
counts. Supported isolated evidence must use one stable namespace hash per side
and different hashes across sides. Observed evidence must not retain a namespace
hash. Isolated manifest and attempt hashes must be exactly 64
lowercase hexadecimal characters, agree for every attempt within a side, and
differ across sides. This format check ensures only the SHA-256 digest—not the
raw namespace—can occupy retained namespace metadata. Retired methodologies and
summary formats remain static historical evidence without validation guarantees.

All cache-reuse views are derived from runtime-reported model-step fields.
Cold, warm-up, and measured phase labels do not prove provider cache hits,
misses, fills, eviction, eligibility, prefixes, isolation, or TTL behavior. The
paired interval is descriptive and supports no significance or causal claim.

### Live quality ranking (one candidate pipeline)

```bash
# Default: passthrough vs conservative-a
ktx bench compare \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task crossfile-nav \
  --rounds 3

# Compare a different candidate
ktx bench compare \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task crossfile-nav \
  --baseline passthrough \
  --candidate aggressive-head \
  --rounds 3
```

Run the full matrix in-process:

```bash
ktx bench rank \
  --model openai/gpt-5.6-luna \
  --variant high \
  --task crossfile-nav \
  --candidate sanitize,keep-ends-mid,conservative-a,aggressive-head
```

Prefer `crossfile-nav` for headline evidence; use the smaller tasks to
attribute quality failures.

### Regenerate corpus

```bash
ktx bench corpus generate
```

Committed corpus files must stay in sync with task fact lists. After
regeneration, run `mage check`; the corpus manifest is covered by a byte-level
golden test and must remain consistent with every task TOML file.

## Non-goals

- LLM-as-judge scoring
- P-values, significance claims, causal claims, or other inferential claims
- Automatic exclusion of outliers
- Content-type taxonomy routing (separate follow-up; command-aware routing by tool name, command prefix, subcommand, and exit status is available — see [Command-aware routing](#command-aware-routing))
- Changing the product default pipeline (still opt-in empty/passthrough)
