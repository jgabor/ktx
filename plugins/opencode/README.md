# @jgabor/ktx

The OpenCode plugin for [ktx](https://github.com/jgabor/ktx). It compresses
tool output, records model-step telemetry, and shows ktx statistics in the
OpenCode sidebar. Every feature runs the `ktx` binary on your `PATH`; this
package carries no copy of ktx.

## Install

Install ktx first, then let ktx add this package to OpenCode:

```bash
go install github.com/jgabor/ktx/cmd/ktx@latest
ktx init opencode            # globally
ktx init opencode --project  # or only in the current project
```

`ktx init opencode` runs `opencode plugin` with this package pinned to your
ktx release, so ktx v0.2.0 loads `@jgabor/ktx@0.2.0`. After you upgrade ktx,
run `ktx init opencode` again to move the pin. `ktx init opencode --show`
reports the entry and whether it matches the ktx on `PATH`;
`ktx init opencode --uninstall` removes it.

## Targets

- `./server` (`server.ts`): compression and telemetry hooks.
- `./tui` (`tui.js`): the statistics sidebar.

The package supports OpenCode `>=1.18.5 <2`. Each release of this package is
published from the same commit as the ktx release with the same version.

## License

MIT. See [LICENSE](LICENSE).
