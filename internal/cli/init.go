// `ktx init` — the runtime adapter installer.
// It installs the ktx plugin of an agent runtime through that runtime's own
// plugin command, so events flow into the ktx store without requiring the
// user to wrap the agent.
//
// Three integration paths share one store: the wrapper
// (`ktx run`) parses --format json, the sidecar (`ktx write`) accepts
// caller-supplied payloads, and adapters (`ktx init`) install native
// extension points in the agent runtime. OpenCode gets the @jgabor/ktx npm
// package (see init_opencode.go); Codex and Claude Code install the published
// ktx@ktx plugin through their own CLIs (see init_codex.go and
// init_claude_code.go).
//
// Flags:
//
//	--project      install into the current project (OpenCode only; a usage
//	               error for Codex and Claude Code)
//	--uninstall    remove the installed plugin
//	--show         report whether the plugin is installed and where
//
// --uninstall and --show are mutually exclusive; combining --project with
// either scopes the OpenCode action to the current project.
//
// The command parses its runtime and action flags without opening the database.
package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// initParsedArgs carries command-specific arguments to initCommandFunc.
type initParsedArgs struct {
	flags   initFlags
	runtime string
}

// initFlags captures the flags shared across all `ktx init <runtime>`
// subcommands. Each flag selects a distinct action; combining them is
// rejected at parse time.
type initFlags struct {
	project   bool
	uninstall bool
	show      bool
	showHelp  bool
}

// runInitWith executes `ktx init` with injected output streams.
// stdout/stderr are injected so the install/uninstall/show flow can
// be exercised without touching os.Stdout/Stderr or the real home
// directory. The body calls parseInitArgs,
// then dispatches on the runtime name and action flags.
func runInitWith(args []string, opts Options, stdout, stderr io.Writer) int {
	return runDispatchWithOptions("init", args, opts, stdout, stderr)
}

func executeInit(f initFlags, runtime, format string, stdout, stderr io.Writer) (any, error) {
	if f.showHelp {
		printInitHelp(stdout)
		return nil, nil
	}

	if runtime == "" {
		return nil, usageError("ktx init: missing runtime name; usage: ktx init <runtime> [flags] (supported: opencode, codex, claude)")
	}

	// At most one action flag is meaningful; mixing --uninstall/--show with
	// --project is permitted (--project scopes the action), but stacking
	// --uninstall + --show is not.
	if f.uninstall && f.show {
		return nil, usageError("ktx init: --uninstall and --show are mutually exclusive")
	}

	switch runtime {
	case "opencode":
		return nil, initOpencodeLogic(&f, format, stdout, stderr)
	case "codex":
		return nil, initCodexLogic(&f, format, stdout, stderr)
	case "claude":
		return nil, initClaudeCodeLogic(&f, format, stdout, stderr)
	default:
		return nil, usageError("ktx init: unknown runtime %q; supported: opencode, codex, claude", runtime)
	}
}

// parseInitDispatchArgs consumes the runtime positional and init flags.
func parseInitDispatchArgs(args []string) ([]string, any, error) {
	format, rest, err := extractFormatChecked(args)
	if err != nil {
		return nil, nil, err
	}
	f, runtime, err := parseInitArgs(rest)
	if err != nil {
		return nil, nil, err
	}
	var common []string
	if format != "" {
		common = []string{"--format=" + format}
	}
	return common, initParsedArgs{flags: f, runtime: runtime}, nil
}

// initCommandFunc is the dispatch wrapper. The dispatcher already
// parsed the init-specific arguments and carries them on the context.
//
// DatabaseNone: ktx init never touches the ktx database — it only runs
// runtime CLIs and reads or edits their plugin config.
func initCommandFunc(ctx *DispatchContext) (any, error) {
	parsed, ok := ctx.ParsedArgs.(initParsedArgs)
	if !ok {
		return nil, internalError("ktx init: command arguments were not parsed")
	}
	return executeInit(parsed.flags, parsed.runtime, ctx.Format, ctx.Stdout, ctx.Stderr)
}

// init registers `ktx init` with the dispatch module. After init runs,
// the verb is reachable through runWith.
func init() {
	Register("init", RegisterOptions{
		Description: "install a runtime adapter",
		ParseArgs:   parseInitDispatchArgs,
		Help:        printInitHelp,
	}, initCommandFunc)
}

// parseInitArgs walks args and separates recognised flags from the
// runtime positional. The first non-flag token is the runtime; any
// further positional tokens are reported as unexpected so the caller
// can produce a clear usage error. Unknown flags are also rejected —
// typos like `--projct` should surface immediately rather than being
// silently ignored. The error message distinguishes the two cases so
// the user can tell a typo from a stray positional argument.
func parseInitArgs(args []string) (initFlags, string, error) {
	var f initFlags
	var runtime string
	escaped := false
	for _, a := range args {
		if a == "--" && !escaped {
			escaped = true
			continue
		}
		name, value, hasValue := strings.Cut(a, "=")
		if escaped {
			name = ""
		}
		var target *bool
		switch name {
		case "--project":
			target = &f.project
		case "--uninstall":
			target = &f.uninstall
		case "--show":
			target = &f.show
		case "--help", "-h":
			target = &f.showHelp
		default:
			if !escaped && len(a) > 1 && a[0] == '-' {
				return f, "", fmt.Errorf("unknown flag %q", a)
			}
			if runtime == "" {
				runtime = a
				continue
			}
			return f, "", fmt.Errorf("unexpected argument %q", a)
		}
		*target = true
		if hasValue {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return f, "", fmt.Errorf("invalid %s boolean %q", name, value)
			}
			*target = parsed
		}
	}
	return f, runtime, nil
}

// printInitHelp writes the `ktx init` help. The flag table mirrors the
// dispatcher's action contract: --project scopes, --uninstall removes,
// --show reports.
func printInitHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx init — install a runtime adapter")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx init <runtime> [flags]")
	fmt.Fprintln(w)
	printOwnedArgumentHelp(w)
	fmt.Fprintln(w, "Supported runtimes:")
	fmt.Fprintln(w, "  opencode   install the @jgabor/ktx OpenCode package through the opencode CLI")
	fmt.Fprintln(w, "             (compression, telemetry and a statistics sidebar that run the ktx on PATH)")
	fmt.Fprintln(w, "  codex      install the ktx@ktx Codex plugin through the codex CLI")
	fmt.Fprintln(w, "             (a PostToolUse hook and a ktx:stats skill that run the ktx on PATH; no --project)")
	fmt.Fprintln(w, "  claude     install the ktx@ktx Claude Code plugin through the claude CLI")
	fmt.Fprintln(w, "             (a PostToolUse hook and a stats mod that run the ktx on PATH; no --project)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --project      opencode: install into the current project (./.opencode) instead of")
	fmt.Fprintln(w, "                 the global config ($XDG_CONFIG_HOME/opencode); with --show or")
	fmt.Fprintln(w, "                 --uninstall, act on the current project only")
	fmt.Fprintln(w, "                 codex and claude: a usage error")
	fmt.Fprintln(w, "  --uninstall    remove the installed plugin")
	fmt.Fprintln(w, "                 opencode: removes ktx's entries and earlier generated plugin files,")
	fmt.Fprintln(w, "                           globally and in the current project")
	fmt.Fprintln(w, "                 codex:    removes ktx@ktx, the ktx marketplace, and earlier ktx hooks")
	fmt.Fprintln(w, "                 claude:   uninstalls ktx@ktx and removes the ktx marketplace")
	fmt.Fprintln(w, "  --show         report whether the plugin is installed and where")
	fmt.Fprintln(w, "  --format       json or text; boolean action/scope flags also accept =false")
	fmt.Fprintln(w, "  -h, --help     show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  action succeeded (install, or commands printed when the runtime CLI is")
	fmt.Fprintln(w, "     missing; uninstall removed something; show found the plugin)")
	fmt.Fprintln(w, "  1  usage error (unknown runtime, conflicting flags)")
	fmt.Fprintln(w, "  2  not found (--show found nothing, --uninstall found nothing)")
	fmt.Fprintln(w, "  4  internal error (plugin state could not be inspected or changed)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The OpenCode package is published to npm as @jgabor/ktx. Install runs")
	fmt.Fprintln(w, "`opencode plugin -g @jgabor/ktx@<version>` (without -g, in the current folder,")
	fmt.Fprintln(w, "for --project), pinned to this ktx's release, so re-running `ktx init opencode`")
	fmt.Fprintln(w, "after a ktx upgrade replaces the older pin. A ktx without a release number")
	fmt.Fprintln(w, "installs the unpinned @jgabor/ktx, replacing a release pin, and keeps only a")
	fmt.Fprintln(w, "checkout folder entry from `mage install`. OpenCode's plugin command can only")
	fmt.Fprintln(w, "add entries, so ktx removes its own entries before it adds a different one and")
	fmt.Fprintln(w, "for --uninstall; every other entry and comment stays. Install also removes the")
	fmt.Fprintln(w, "ktx.ts file earlier ktx versions generated in plugins/ of the global or project")
	fmt.Fprintln(w, "config. Without opencode on PATH it changes nothing and prints the commands.")
	fmt.Fprintln(w, "The npm install needs a ktx release that includes the package; until then, use")
	fmt.Fprintln(w, "`mage install` from a checkout. Restart OpenCode afterwards.")
	fmt.Fprintln(w, "The package's server target subscribes to event for optional model-step")
	fmt.Fprintln(w, "telemetry and tool.execute.after for tool-result compression, through `ktx")
	fmt.Fprintln(w, "telemetry record` and `ktx hook`. With KTX_SESSION_ID alone, the plugin noops")
	fmt.Fprintln(w, "so the ordinary wrapper remains the single evidence owner. Explicit collection")
	fmt.Fprintln(w, "also sets KTX_COLLECTION_ID and keeps the hooks active. The wrapper records")
	fmt.Fprintln(w, "identifiable OpenCode step_finish model telemetry itself while the plugin")
	fmt.Fprintln(w, "hooks noop, preventing duplicate evidence.")
	fmt.Fprintf(w, "Telemetry is %s. It is %s.\n", opencodeTelemetryEvidence, opencodeTelemetryClaimBoundary)
	fmt.Fprintf(w, "--show reads OpenCode's plugin config. Troubleshooting: %s\n", opencodeTroubleshooting)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The Codex plugin is served as ktx@ktx from github.com/jgabor/ktx. Its hook runs")
	fmt.Fprintln(w, "`ktx hook` after every tool call, and its ktx:stats skill runs `ktx stats")
	fmt.Fprintln(w, "summary`; both use the ktx on PATH. Install runs `codex plugin marketplace add")
	fmt.Fprintln(w, "jgabor/ktx` and `codex plugin add ktx@ktx`, skipping whichever is already")
	fmt.Fprintln(w, "present, and keeps an existing ktx marketplace whatever its source. It also")
	fmt.Fprintln(w, "removes the hook earlier ktx versions merged into ~/.codex/hooks.json or")
	fmt.Fprintln(w, "./.codex/hooks.json and keeps every other hook there. Without codex on PATH it")
	fmt.Fprintln(w, "prints the commands. Afterwards start a new Codex session, run /hooks and trust")
	fmt.Fprintln(w, "the ktx hook; Codex does not run a hook until it is trusted.")
	fmt.Fprintf(w, "--show reads `codex plugin list`. Troubleshooting: %s\n", codexTroubleshooting)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The Claude Code plugin is published as ktx@ktx from github.com/jgabor/ktx. Its")
	fmt.Fprintln(w, "hook runs `ktx hook --runtime claude-code` after each Bash call with no timeout,")
	fmt.Fprintln(w, "and its mod runs `ktx stats summary`; both use the ktx on PATH. Install runs")
	fmt.Fprintln(w, "`claude plugin marketplace add jgabor/ktx` and `claude plugin install ktx@ktx`,")
	fmt.Fprintln(w, "skipping whichever is already present, and keeps an existing ktx marketplace")
	fmt.Fprintln(w, "whatever its source. Without claude on PATH it prints the commands. ktx never")
	fmt.Fprintln(w, "edits Claude Code settings files. Afterwards run /reload-plugins in Claude Code.")
	fmt.Fprintf(w, "--show reads `claude plugin list`. Troubleshooting: %s\n", claudeCodeTroubleshooting)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  ktx init opencode              # install @jgabor/ktx globally through the opencode CLI")
	fmt.Fprintln(w, "  ktx init opencode --project    # install it into the current project")
	fmt.Fprintln(w, "  ktx init opencode --show       # check the installed entries")
	fmt.Fprintln(w, "  ktx init opencode --uninstall  # remove ktx's entries")
	fmt.Fprintln(w, "  ktx init codex                 # install ktx@ktx through the codex CLI")
	fmt.Fprintln(w, "  ktx init codex --show          # check the installed plugin")
	fmt.Fprintln(w, "  ktx init codex --uninstall     # remove ktx@ktx and its marketplace")
	fmt.Fprintln(w, "  ktx init claude                # install ktx@ktx through the claude CLI")
	fmt.Fprintln(w, "  ktx init claude --show         # check the installed plugin")
	fmt.Fprintln(w, "  ktx init claude --uninstall    # uninstall ktx@ktx and remove its marketplace")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
