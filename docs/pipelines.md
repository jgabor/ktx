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

| Pipeline ID              | File                          | Stages                                             | Intent                                                          |
| ------------------------ | ----------------------------- | -------------------------------------------------- | --------------------------------------------------------------- |
| `passthrough`            | `passthrough.toml`            | `[]`                                               | Live baseline; no compression                                   |
| `sanitize`               | `sanitize.toml`               | `strip-ansi`, `dedup-blank`, `normalize`           | Safe control; near-passthrough cost on clean output             |
| `keep-ends-mid`          | `keep-ends-mid.toml`          | sanitize + `keep-ends:100:50`                      | **Recommended general recipe**; best $/correct with full pass   |
| `conservative-a`         | `conservative-a.toml`         | sanitize + `keep-ends:50:20` + `token-budget:2000` | Lossy without recovery; partial pass on mid-fact without footer |
| `conservative-a-recover` | `conservative-a-recover.toml` | same as conservative-a + `recover_footer=true`     | Lossy + recovery; full pass via `ktx read`                      |
| `aggressive-head`        | `aggressive-head.toml`        | sanitize + `head:30`                               | Max savings; high mid/tail fact risk                            |
| `aggressive-keep-ends`   | `aggressive-keep-ends.toml`   | sanitize + `keep-ends:30:10`                       | Aggressive but keeps head+tail                                  |

The live A/B defaults are `pipelines/passthrough.toml` and
`pipelines/conservative-a.toml`. Override them with `--baseline-config` or
`--candidate-config` pointing at any named pipeline file.

Live A/B vocabulary: each run compares a **baseline** pipeline to a **candidate**
pipeline (summary keys `pipelines.baseline` / `pipelines.candidate`).

### Recommended recipes

Product default when no config is found remains passthrough (empty stages).
The recipes below match `ktx.example.toml` and live ranking
(`expected_cost_per_correct` on full-pass pipelines), not offline per-call ratio alone.

| Recipe                                  | Stages (and flags)                                                        | When to use                                                                         |
| --------------------------------------- | ------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Sanitize only                           | `strip-ansi`, `dedup-blank`, `normalize`                                  | Safe default in `ktx.example.toml`; no content loss; little savings on clean output |
| **Keep-ends mid (recommended general)** | sanitize + `keep-ends:100:50`                                             | Best measured $/correct with full pass; no `token-budget`                           |
| Aggressive head                         | sanitize + `head:30`                                                      | Max token/cost cut; accepts mid/tail loss                                           |
| Lossy + recovery                        | sanitize + `keep-ends:50:20` + `token-budget:2000` (footer on by default) | Narrow windows/budget; agents can `ktx read` via default recovery footer            |
| Build logs                              | sanitize + `grep-v:^debug:` + `keep-ends:100:50`                          | Prefer wider keep-ends over a tight budget when recovery is undesirable             |

Product defaults when fields are omitted: pipeline timeout **2s**,
`recover_footer` **true**. Set `recover_footer = false` to suppress the
footer. Do not promote bare lossy budgets without recovery as a general
default: live ranking showed partial fact loss when the footer was off.

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
| Exit status    | `exit_status`    | `"success"` (no error), `"failure"` (errored), or `"any"` (the default when the field is empty or absent).                                                                                                                                                                          |

`profile` names the target `[pipeline.<name>]` table and is required.
Profile references are validated at config-load time — every
`profile =` value must name an existing profile or the config fails to
load. The router itself never errors at runtime.

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

### When to use routing

- **Different tools, different needs** — command stdout (`bash`) often
  tolerates aggressive `head`/`keep-ends`; file reads (`read`) may need
  wider `keep-ends` to preserve mid-file facts needed for navigation.
- **Failure versus success** — route errored tool calls to a
  conservative profile so an `is_error` signal is never stripped by
  aggressive truncation before the agent sees it.
- **Command-specific recipes** — route `git log` output to a head-based
  profile while leaving other `bash` output on the default profile.

Routing selects among the recipes above per call; it does not replace
them. See `ktx.example.toml` for commented `[[route]]` examples.

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

| Task ID                  | Skill     | Token band   | Stresses                                                                           | Scoring               |
| ------------------------ | --------- | ------------ | ---------------------------------------------------------------------------------- | --------------------- |
| `fact-midfile-v1`        | read      | 30–60k       | mid-file loss under `head:N`                                                       | multi-fact fractional |
| `log-tail-error-v1`      | bash      | 40–80k       | tail errors under pure head                                                        | multi-fact fractional |
| `crossfile-nav-v1`       | navigate  | **100–200k** | incomplete coverage + mid/tail                                                     | multi-fact fractional |
| `ephemeral-midfact-v1`   | bash      | 20–80k       | mid-loss when facts live only in tool stdout (no on-disk re-grep); recovery footer | multi-fact fractional |
| `grep-noise-v1`          | find      | 40–80k       | needle lost in noise                                                               | multi-fact fractional |
| `failure-passthrough-v1` | bash fail | small        | failure signal stripped                                                            | multi-fact fractional |

`crossfile-nav-v1` is the headline heavy task. Smaller tasks diagnose _why_ an
pipeline wins or loses (head vs keep-ends vs budget).

Planted facts use the form `FACT_NAME=value` and are listed in each task file
and in `tools/bench/corpus/MANIFEST.json`.

## Metrics contract

All metrics are derived from retained experiment evidence
(`manifest.json` + `attempts.jsonl`). No LLM-as-judge.

### Per-attempt

| Field                               | Meaning                                                                                 |
| ----------------------------------- | --------------------------------------------------------------------------------------- |
| `score`                             | For multi-fact: `matched_facts / total_facts` in `[0, 1]`. For exact-match: `0` or `1`. |
| `full_pass`                         | `score == 1.0`                                                                          |
| `facts_matched` / `facts_missed`    | Multi-fact detail                                                                       |
| `tokens_in` / `tokens_out` / `cost` | From stream `step_finish` usage when complete                                           |
| `tool_call_count`                   | Count of `tool_use` events                                                              |
| `session_id`                        | Linked only when exactly one new session appears                                        |

### Per-pipeline aggregates (evaluated attempts)

An attempt is **evaluated** when:
`status=completed`, session linked, stream complete, and
`tool_call_count >= minimum_tool_calls`.

Partial fact scores are kept; they are not discarded.

| Aggregate                   | Formula                                                           |
| --------------------------- | ----------------------------------------------------------------- |
| `mean_fact_score`           | mean of `score` over evaluated attempts                           |
| `full_pass_rate`            | fraction of evaluated attempts with `full_pass`                   |
| `mean_cost`                 | mean cost over evaluated attempts                                 |
| `expected_cost_per_correct` | `mean_cost / max(full_pass_rate, 1e-9)`                           |
| `mean_tokens_in`            | mean input tokens over evaluated attempts                         |
| `tool_call_inflation`       | `candidate.mean_tool_calls / baseline.mean_tool_calls` (live A/B) |

`expected_cost_per_correct` is the ranking headline (tilth geometric form).
A cheaper pipeline that loses facts ranks worse than a slightly costlier full-pass pipeline.

### Secondary quality signals

| Signal                        | Interpretation                                 |
| ----------------------------- | ---------------------------------------------- |
| Tool-call inflation > 1       | Agent bounced / re-fetched under compression   |
| Mid-fact miss + head-fact hit | `head:N` / narrow window damage                |
| Tail-fact miss                | Missing keep-ends / tail window too small      |
| `failure-passthrough-v1` miss | Failure path incorrectly compressed or dropped |

## How to run

### Live quality ranking (one candidate pipeline)

```bash
# Default: passthrough vs conservative-a
ktx bench compare \
  --model neuralwatt/glm-5.2-short-fast-flex \
  --task-file tools/bench/tasks/crossfile-nav-v1.toml \
  --rounds 3

# Compare a different candidate
ktx bench compare \
  --model neuralwatt/glm-5.2-short-fast-flex \
  --task-file tools/bench/tasks/crossfile-nav-v1.toml \
  --baseline-config tools/bench/pipelines/passthrough.toml \
  --candidate-config tools/bench/pipelines/aggressive-head.toml \
  --rounds 3
```

Run the full matrix in-process:

```bash
ktx bench rank \
  --model neuralwatt/glm-5.2-short-fast-flex \
  --task-file tools/bench/tasks/crossfile-nav-v1.toml \
  --pipelines sanitize,keep-ends-mid,conservative-a,aggressive-head
```

Prefer `crossfile-nav-v1` for headline evidence; use the smaller tasks to
attribute quality failures.

### Regenerate corpus

```bash
ktx bench corpus generate
```

Committed corpus files must stay in sync with task fact lists. After
regeneration, run `make check`; the corpus manifest is covered by a byte-level
golden test and must remain consistent with every task TOML file.

## Non-goals

- LLM-as-judge scoring
- Statistical confidence intervals
- Content-type taxonomy routing (separate follow-up; command-aware routing by tool name, command prefix, and exit status is available — see [Command-aware routing](#command-aware-routing))
- Changing the product default pipeline (still opt-in empty/passthrough)
