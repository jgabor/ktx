# Claude Code integration

This guide shows how to connect ktx to Claude Code: install the plugin, see
what it compresses and shows, and fix common problems. One `ktx` plugin does
both jobs. Its hook compresses Bash output before Claude reads it. Its mod
shows ktx's savings in a band above the prompt and in a `/ktx` pane.

ktx targets the Claude Code 2.1.304 hook and plugin contract, and the plugin
passes `claude plugin validate` on that version. Its one warning, that the
plugin has no version, is deliberate (see below). The mod needs Claude
Code 2.1.287 or newer. Its API is still early access, so a later Claude Code
release can change it.

## Install

Install ktx first, so it is on your `PATH`, then install the plugin:

```bash
go install github.com/jgabor/ktx/cmd/ktx@latest
ktx init claude
```

`ktx init claude` runs two `claude` commands for you:

```bash
claude plugin marketplace add jgabor/ktx
claude plugin install ktx@ktx
```

It skips a step that is already done, so running it again changes nothing. If
a marketplace named `ktx` already exists, it is kept whatever its source. If
`claude` isn't on your `PATH`, ktx prints the two commands for you to run. You
can also run them yourself, or use `/plugin` inside Claude Code. ktx never edits
your Claude Code settings files. Start a new Claude Code session, or run
`/reload-plugins` in an open one.

The marketplace holds one plugin, `ktx`, with two parts:

| Part | What it does                                                      |
| ---- | ----------------------------------------------------------------- |
| Hook | A `PostToolUse` hook for the Bash tool that compresses its output |
| Mod  | Polls `ktx stats summary` and shows a band and a `/ktx` pane      |

Both parts run the `ktx` on your `PATH`. The hook runs it in exec form, with no
shell, as `ktx hook --runtime claude-code`, and sets no timeout. After you
upgrade ktx, start a new Claude Code session or run `/reload-plugins`.

The plugin has no version number of its own: Claude Code identifies it by the
commit it was installed from, so each change on the main branch is an update.
Run `claude plugin update ktx@ktx` to get it, or turn on auto-update for the
`ktx` marketplace under `/plugin` > Marketplaces.

The plugin needs ktx v0.1.0 or newer. At session start the mod runs
`ktx --version`. If that prints an older release, the mod stops polling, and the
band and `/ktx` show one line instead, for example
`ktx v0.0.9 is older than v0.1.0; upgrade ktx`. A build without a release
version, such as `dev` or a Go pseudo-version, passes.

`ktx init claude --show` reports whether `ktx@ktx` is installed and enabled,
where its marketplace reads from, which `ktx` is on your `PATH`, and the version
of the ktx you ran. It exits 2 when the plugin isn't installed. It can't tell
whether a running session loaded the plugin. There is no `--project` scope for
Claude Code.

## What gets compressed

Only Bash calls, and only their standard output. ktx stores the original,
runs your pipeline, and hands Claude a copy of the tool result with `stdout`
replaced. Every other field stays as Claude Code sent it. Routes in
`ktx.toml` match these calls with `tool = "Bash"`, which is case-sensitive.

ktx leaves a call unchanged and records it as passthrough when it can't
replace the output exactly: a background command, an image result, an
interrupted call, or output that isn't plain text. Failed calls
(`PostToolUseFailure`) are not recorded yet. If the hook fails, Claude reads
the original output.

When a lossy profile sets `recover_footer = true`, Claude sees the usual
one-line hint with the `ktx read <id>` command that returns the original.

## Band and /ktx pane

The mod runs `ktx stats summary --window 15m --format json` every 10 seconds
through the `ktx` on your `PATH`, with a 5 second timeout. It shows one dim line
in a band above the prompt:

```text
ktx 15m: 46% saved · 1.2M→650k tok · 120/300 calls
```

That reads as: 46% of the raw output tokens were saved, 1.2 million raw tokens
became 650 thousand, and 120 of 300 calls with output had a compressed copy.
With no calls in the window it shows `ktx: no calls in 15m`. If a refresh
fails, the mod keeps the last good figures and marks them `(stale)` until a
later refresh works.

The band appears after the first refresh and steps aside while Claude Code
shows a survey. Only one plugin can draw the band at a time, so another mod
that draws its own band can hide it. The pane always works.

Type `/ktx` to open a one-line pane with the same line and buttons for the 15,
30, and 60 minute windows. Type `/ktx` again to close it, which helps when
clicks don't reach the terminal, for example over SSH. `/ktx 30` (or `/ktx 30m`)
switches to that window and opens the pane. The band steps aside while the
pane is open. The mod polls only in interactive sessions, not in `claude -p`
runs.

The mod remembers whether you last opened or closed the pane, in any session.
If your last `/ktx` opened it, each new interactive session opens it again on
start. The memory is shared, so closing the pane in one session keeps it closed
in the next new session even if another session still shows it. Claude
Code draws a pane it opens on its own only on a wide enough terminal, so on a
narrow one the band stays until you type `/ktx`. Closing the pane, with `/ktx`
or by hand, keeps it closed in later sessions. The window choice is not kept;
each session starts at 15 minutes.

## Status line without the mod

If your Claude Code version has no mods, or you prefer your own status line,
add ktx to the `statusLine` command you already use. This snippet prints the
same saved share. It needs `jq`:

```bash
ktx stats summary --window 15m --format json 2>/dev/null | jq -r '
  if .calls.total_with_output == 0 then "ktx: no calls in 15m"
  elif .tokens.raw_estimated == 0 then "ktx 15m: — saved"
  else "ktx 15m: \(100 * (.tokens.raw_estimated - .tokens.delivered_estimated)
    / .tokens.raw_estimated | round)% saved" end'
```

Claude Code runs the status line command often, and each run reads the ktx
database, so keep the window short.

## Uninstall

```bash
ktx init claude --uninstall
```

This runs `claude plugin uninstall ktx@ktx` and
`claude plugin marketplace remove ktx` for whichever is present. It exits 2
when neither is.

## Developing the plugin

The plugin lives in `plugins/ktx` of the ktx repository. Two mage targets
switch a machine between your checkout and the release, so the plugin and the
ktx binary always come from the same place:

```bash
mage install         # build ktx from the checkout; ktx marketplace = the checkout
mage installRelease  # go install the latest release; ktx marketplace = jgabor/ktx
```

Each target removes a `ktx` marketplace that points elsewhere, after
uninstalling `ktx@ktx`, then adds its own and installs the plugin. With your
checkout as the marketplace, Claude Code loads `plugins/ktx` in place, so your
edits apply after `/reload-plugins`. Without `claude` on `PATH`, both targets
skip the plugin. Both also refresh installed OpenCode and Codex adapters with
the binary they install.

## Troubleshooting

- Run `ktx init claude --show` to check that `ktx@ktx` is installed and
  enabled, and that `ktx` is on your `PATH`. If it is missing, run
  `ktx init claude` and then `/reload-plugins`.
- If `ktx` isn't on the `PATH` Claude Code runs with, every Bash call shows a
  hook error and the band says `unavailable`. Add the folder that holds ktx
  (for `go install`, usually `~/go/bin`) to your `PATH` and restart Claude Code.
- If the band says that ktx is older than the plugin needs, upgrade ktx with
  `go install github.com/jgabor/ktx/cmd/ktx@latest` and run `/reload-plugins`.
- Start Claude Code with `claude --debug` to see hook runs and mod load errors.
- The band and pane figures cover every runtime ktx records (OpenCode,
  Codex, Claude Code, and `ktx run`), not only the current Claude Code session.
- ktx doesn't collect model token or cost telemetry from Claude Code yet. Bash
  calls and their compression are recorded, but per-step model usage is not.
- If the band says `unavailable`, run the same
  `ktx stats summary --window 15m --format json` command yourself to see the
  error.
- If the hook seems to do nothing, check that your pipeline config compresses
  Bash output, through `[pipeline.default]` or a `tool = "Bash"` route. Without
  a config file, output passes through unchanged. Output where every line is
  JSON, such as `seq`, stays whole unless a stage keeps it valid JSON.
