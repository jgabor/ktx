# Codex integration

This guide shows how to connect ktx to Codex: install the plugin, trust its
hook, see ktx statistics, keep the plugin current, and fix common problems.
One `ktx` plugin does both jobs. Its hook compresses tool output before Codex
reads it. Its `ktx:stats` skill shows ktx's savings when you ask for them.

ktx targets the Codex 0.163.0 plugin and hook contract.

## Install

Install ktx first, so it is on your `PATH`, then install the plugin:

```bash
go install github.com/jgabor/ktx/cmd/ktx@latest
ktx init codex
```

This needs a ktx release that includes the plugin. Until one ships,
`go install …@latest` gives v0.1.0, whose `ktx init codex` still merges a hook
into `hooks.json`. To use the plugin from a checkout before then, build ktx
onto your `PATH`, add the checkout as the `ktx` marketplace, and install:

```bash
mage install
codex plugin marketplace add /path/to/ktx
ktx init codex
```

`mage install` changes Codex only once ktx is set up there, so a first install
needs these steps; later `mage install` runs keep the marketplace on the
checkout.

`ktx init codex` runs two `codex` commands for you:

```bash
codex plugin marketplace add jgabor/ktx
codex plugin add ktx@ktx
```

It skips a step that is already done, so running it again changes nothing. If
a marketplace named `ktx` already exists, it is kept whatever its source. If
`codex` isn't on your `PATH`, ktx prints the two commands for you to run. ktx
installs through Codex's CLI; the only files it changes itself are hooks files
an earlier ktx wrote into (see below). There is no `--project` scope.

The marketplace serves one plugin, `ktx`, from `plugins/codex` of the ktx
repository. It has two parts:

| Part  | What it does                                                     |
| ----- | ---------------------------------------------------------------- |
| Hook  | A `PostToolUse` hook for every tool that runs `ktx hook`         |
| Skill | `ktx:stats`, which runs `ktx stats summary` and shows the output |

Both parts run the `ktx` on your `PATH`, so upgrading ktx needs no plugin
change. Codex reads this plugin's own manifest and hooks file, never the
Claude Code plugin in the same repository.

## Trust the hook

Codex runs a plugin hook only after you trust it. Start a new Codex session,
run `/hooks`, and trust the `ktx` hook. Until then the plugin is installed but
records and compresses nothing. Trust is tied to the hook's content, so a
changed hook needs trust again.

## Hooks from earlier ktx versions

Earlier ktx versions merged a `PostToolUse` hook into `~/.codex/hooks.json` or
`./.codex/hooks.json` instead of installing a plugin. Once the plugin hook is
trusted, a merged hook would run ktx a second time for each tool call.
`ktx init codex` therefore removes those entries and reports each file it
changed:

```text
removed the earlier ktx hook from /home/you/.codex/hooks.json (1 entry)
```

It removes only hooks in the exact shapes earlier versions wrote: the bare
`ktx hook` command, or a quoted absolute path to ktx followed by `hook` or by
`--expect-runtime-sha256 <digest> hook`, each with the status message
`ktx: recording tool call`. Every other hook in those
files stays as it is, and a file left empty is deleted. A file that isn't
valid JSON stops the install before anything changes. The file is rewritten
as indented JSON with sorted keys; its content is otherwise unchanged. Codex
records hook trust by the hook's position in the file, so a hook that came
after a removed entry may show as untrusted in `/hooks` and need trust again.
Codex may keep the old hook's trust entry in its `config.toml`; it no longer
applies to anything.

## Statistics

Codex has no panel for plugins, so ktx statistics come on request. Ask Codex
for ktx stats, or invoke the `ktx:stats` skill. It runs `ktx stats summary` and
shows the output unchanged. The figures cover every runtime ktx records
(OpenCode, Codex, Claude Code, and `ktx run`), not only the current Codex
session.

## Check the install

```bash
ktx init codex --show
```

This reports whether `ktx@ktx` is installed and enabled, where its marketplace
reads from, which `ktx` is on your `PATH`, the version of the ktx you ran, and
any hook an earlier ktx left in a hooks file. It exits 2 when the plugin isn't
installed. It can't tell whether a running session loaded the plugin or
whether you trusted the hook.

## Updates

Codex reports the version of a plugin from a Git or local marketplace as
`local`, so the plugin is identified by the commit it was installed from. To
get the latest plugin from GitHub:

```bash
codex plugin marketplace upgrade ktx
codex plugin add ktx@ktx
```

`codex plugin add` refreshes Codex's copy in place. Start a new Codex session
afterwards, and trust the hook again in `/hooks` if Codex asks.

## Uninstall

```bash
ktx init codex --uninstall
```

This runs `codex plugin remove ktx@ktx` and
`codex plugin marketplace remove ktx` for whichever is present, and removes
any hook an earlier ktx merged into a hooks file. It exits 2 when there is
nothing to remove.

## Developing the plugin

The plugin lives in `plugins/codex` of the ktx repository, and the marketplace
file in `.agents/plugins/marketplace.json`. Two mage targets switch a machine
between your checkout and GitHub:

```bash
mage install         # build ktx from the checkout; ktx marketplace = the checkout
mage installRelease  # go install the latest release; ktx marketplace = jgabor/ktx
```

With `mage install`, the plugin and the ktx binary come from the same
checkout. With `mage installRelease`, they can differ: the binary comes from
the latest release tag, but the `jgabor/ktx` marketplace follows the
repository's default branch, as the Claude Code marketplace does. The plugin
only runs the `ktx` on your `PATH`, so a newer plugin works with the released
binary as long as the hook protocol is unchanged.

Each target changes Codex only when it is already set up for ktx: it has the
`ktx` marketplace, `ktx@ktx`, or a hook an earlier ktx merged into a hooks
file. A `ktx` marketplace that points elsewhere is removed, after `ktx@ktx`,
and the target's own source is added. Then `codex plugin add ktx@ktx`
refreshes the installed copy; for the GitHub marketplace,
`codex plugin marketplace upgrade ktx` runs first. When an earlier merged hook
exists, the target then runs the installed `ktx init codex` to remove it.
Without `codex` on `PATH`, both targets skip Codex.

`mage installRelease` checks the latest release before it installs anything.
It asks the Go module proxy for the release's module files, the same files
`go install` builds from, and requires the Codex marketplace and plugin among
them. If they are missing, it stops with nothing changed. Otherwise it installs
exactly that release.

## Troubleshooting

- Run `ktx init codex --show` to check that `ktx@ktx` is installed and enabled,
  and that `ktx` is on your `PATH`. If it is missing, run `ktx init codex`.
- If nothing is recorded, check `/hooks` in Codex: the `ktx` hook must be
  trusted. A new or changed hook needs trust again.
- If install fails, or `--show` warns, because Codex installed `ktx@ktx` from
  a folder other than `plugins/codex`, the marketplace has no Codex plugin and
  Codex fell back to the Claude Code plugin. Install then leaves earlier
  merged hooks in place. Run `ktx init codex --uninstall`, then point the
  marketplace at a ktx source that has `.agents/plugins/marketplace.json`.
- If install fails because `codex plugin list` does not show `ktx@ktx` after
  `codex plugin add`, ktx keeps earlier merged hooks, so recording goes on.
  Check `codex plugin list`, then run `ktx init codex` again.
- If `ktx` isn't on the `PATH` Codex runs with, the hook fails and ktx records
  nothing. Add the folder that holds ktx (for `go install`,
  usually `~/go/bin`) to your `PATH` and restart Codex.
- If `ktx init codex` reports that it could not remove an earlier hook, remove
  that ktx entry from the named hooks file by hand, so ktx doesn't run twice
  for each tool call.
- If the hook seems to do nothing, check that your pipeline config compresses
  the tool's output, through `[pipeline.default]` or a route. Without a config
  file, output passes through unchanged.
