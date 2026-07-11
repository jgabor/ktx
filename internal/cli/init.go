// `ktx init` — the runtime adapter installer.
// It writes a thin adapter that an agent runtime loads natively, so events
// flow into the ktx store without requiring the user to wrap the agent.
//
// Three integration paths share one store: the wrapper
// (`ktx run`) parses --format json, the sidecar (`ktx write`) accepts
// caller-supplied payloads, and adapters (`ktx init`) install native
// extension points in the agent runtime. OpenCode uses an in-process
// TypeScript plugin.
//
// `ktx init opencode` installs a plugin that subscribes to
// `tool.execute.after` and calls `ktx hook` per event,
// sending the raw callback object tagged with runtime="opencode".
// The plugin noops when KTX_SESSION_ID is set so the wrapper
// path remains the single source of truth — both paths active means
// one of them wins.
//
// Flags:
//
//	--project      install to <cwd>/.opencode/plugins/ktx.ts (project scope)
//	--uninstall    remove the installed plugin (checks both locations)
//	--show         report whether the plugin is installed and where
//
// Default (no flag) installs to $XDG_CONFIG_HOME/opencode/plugins/ktx.ts
// (global). All three flags are mutually exclusive actions; combining
// --project with --uninstall/--show still scopes the action to that
// location.
//
// Path resolution (global): $XDG_CONFIG_HOME/opencode/plugins/ktx.ts
// (XDG Base Directory spec; falls back to $HOME/.config/... when
// XDG_CONFIG_HOME is unset, per the spec default).
//
// Path resolution (project): ./.opencode/plugins/ktx.ts relative to the
// invocation's CWD.
//
// The command parses its runtime and action flags without opening the database.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ktxPluginFileName is the name of the OpenCode plugin file installed by
// `ktx init opencode`. The .ts extension is required by OpenCode's plugin
// loader; a .js sibling would not be loaded by the same code path.
const ktxPluginFileName = "ktx.ts"

// initParsedArgs carries command-specific arguments to initCommandFunc.
type initParsedArgs struct {
	flags   initFlags
	runtime string
}

// ktxOpencodePluginTemplate is the TypeScript source written to the
// OpenCode plugin directory. It is embedded as a Go raw string so the
// installed plugin is byte-for-byte identical across invocations and
// platforms; the version bump on the Go side automatically refreshes
// the installed file when users re-run `ktx init opencode`.
//
// Behaviour:
//   - Listens on `tool.execute.after` and shells out to
//     `ktx hook` via Bun.spawn, sending the raw callback object
//     (input + output) tagged with `runtime: "opencode"`. No
//     domain logic lives in the plugin: the binary owns all
//     extraction, ID generation, and timestamps.
//   - `ktx hook` persists the original and returns a JSON envelope:
//     {"output": string, "compressed_id": string|null, "source_id": string}.
//   - The plugin mutates `output.output` to the envelope's `output`
//     field (the compressed bytes the agent sees).
//   - On any ktx hook failure (non-zero exit, unparseable stdout,
//     ktx-not-in-PATH), the plugin leaves output.output unchanged —
//     conservative failure at the plugin level.
//   - Noops when KTX_SESSION_ID is present: when the user
//     is also running the agent under `ktx run`, the wrapper owns
//     persistence and the plugin would otherwise double-record.
//
// Presence check: the plugin factory verifies the ktx
// binary is in PATH before registering the tool.execute.after hook. If
// ktx is missing, no hooks are registered and a one-line warn makes
// the disabled state visible — without this, every tool call would
// pay a `ktx hook` spawn cost and silently drop its payload.
const ktxOpencodePluginTemplate = `import type { Plugin } from "@opencode-ai/plugin"

interface KtxEnvelope {
  output: string
  compressed_id: string | null
  source_id: string
}

const ktxHook = async (payload: string): Promise<KtxEnvelope | null> => {
  try {
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode(payload))
        controller.close()
      },
    })
    const proc = Bun.spawn(["ktx", "hook"], {
      stdin: stream,
      stdout: "pipe",
      stderr: "pipe",
    })
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
      proc.exited,
    ])
    if (exitCode !== 0) {
      console.error("[ktx] hook failed (exit " + exitCode + "): " + stderr.trim())
      return null
    }
    return JSON.parse(stdout) as KtxEnvelope
  } catch (e) {
    console.error("[ktx] hook error: " + String(e))
    return null
  }
}

const KtxPlugin: Plugin = async ({ $ }) => {
  // Verify ktx is in PATH before registering the hook; otherwise every
  // tool call would pay a ktx hook spawn cost and silently noop.
  try {
    await $` + "`which ktx`" + `.quiet()
  } catch {
    console.warn("[ktx] ktx binary not found in PATH \u2014 plugin disabled")
    return {}
  }

  return {
    "tool.execute.after": async (input: any, output: any) => {
      // Noop when wrapper is active
      if (process.env.KTX_SESSION_ID) return

      const record = {
        runtime: "opencode",
        input: input,
        output: output,
      }

      const envelope = await ktxHook(JSON.stringify(record))
      if (envelope) {
        // Mutate output.output to the compressed bytes.
        // The agent sees the compressed output instead of the original.
        if (output && typeof output === "object" && "output" in output) {
          output.output = envelope.output
        }
      }
      // On failure (envelope === null), output is left unchanged.
      // The agent sees the original tool output (conservative failure).
    },
  }
}

export default KtxPlugin
`

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
	_, err := initLogic(args, opts.Format, stdout, stderr)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// initLogic installs, removes, or inspects a runtime adapter.
// parseInitArgs handles
// `ktx init opencode --show` correctly (the runtime name is
// positional, so stdlib flag would stop at it). The walker is
// shared with the dispatch argument parser.
func initLogic(args []string, format string, stdout, stderr io.Writer) (any, error) {
	f, runtime, err := parseInitArgs(args)
	if err != nil {
		return nil, usageError("ktx init: %v", err)
	}
	if f.showHelp {
		printInitHelp(stdout)
		return nil, nil
	}

	if runtime == "" {
		return nil, usageError("ktx init: missing runtime name; usage: ktx init <runtime> [flags] (supported: opencode)")
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
	default:
		return nil, usageError("ktx init: unknown runtime %q; supported: opencode", runtime)
	}
}

// initOpencodeLogic routes to the install/uninstall/show handler for
// the OpenCode adapter. It resolves the candidate plugin paths once
// and passes them down so the handlers do not repeat path logic.
// Returns the typed error to surface (nil on success).
func initOpencodeLogic(f *initFlags, format string, stdout, stderr io.Writer) error {
	globalPath, err := opencodeGlobalPluginPath()
	if err != nil {
		return internalError("ktx init opencode: resolve global plugin path: %v", err)
	}
	// The project path is always resolved (it does not require user
	// lookup), but only consulted for --show/--uninstall or when --project
	// scopes the install.
	projectPath := opencodeProjectPluginPath()

	switch {
	case f.show:
		return initOpencodeShow(globalPath, projectPath, format, stdout, stderr)
	case f.uninstall:
		return initOpencodeUninstall(globalPath, projectPath, format, stdout, stderr)
	default:
		// Default action is install. --project (when set) scopes the
		// install to the project directory; otherwise the global dir
		// is the install target.
		target := globalPath
		if f.project {
			target = projectPath
		}
		return initOpencodeInstall(target, format, stdout)
	}
}

// opencodeGlobalPluginPath returns the path OpenCode loads plugins from
// when configured globally: $XDG_CONFIG_HOME/opencode/plugins/<file>.
// The XDG Base Directory Specification says $HOME/.config is the
// default XDG_CONFIG_HOME when the env var is unset, so the fallback
// mirrors the spec. The home directory is resolved via os.UserHomeDir.
func opencodeGlobalPluginPath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "opencode", "plugins", ktxPluginFileName), nil
}

// opencodeProjectPluginPath returns the project-scoped plugin path,
// always relative to the current working directory. The CWD is captured
// at call time rather than process start.
func opencodeProjectPluginPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		// Falling back to "." keeps the path parseable for the caller's
		// downstream tooling even if the CWD is unreadable. The caller
		// will surface the underlying os error when the file is opened.
		return filepath.Join(".", ".opencode", "plugins", ktxPluginFileName)
	}
	return filepath.Join(cwd, ".opencode", "plugins", ktxPluginFileName)
}

// initOpencodeInstall writes the TS plugin file to target. The parent
// directory is created with os.MkdirAll so first-time installs in a
// fresh OpenCode config tree succeed without a separate setup step.
// The file is written with 0644 — readable by the agent process,
// writable by the user. Any failure (mkdir, write) surfaces as an
// internal error with the path included for the caller to act on.
func initOpencodeInstall(target, format string, stdout io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return internalError("ktx init opencode: create directory %q: %v", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(ktxOpencodePluginTemplate), 0o644); err != nil {
		return internalError("ktx init opencode: write plugin %q: %v", target, err)
	}

	writeInstallReport(stdout, target, format)
	return nil
}

// initOpencodeUninstall removes the plugin from whichever location has
// it (global checked first, then project). When neither has the file,
// the exit code is not-found (2) so callers can detect "nothing to do"
// without inspecting stdout. When both have it, both are removed — the
// user invoked `--uninstall` once and expects a clean state.
func initOpencodeUninstall(globalPath, projectPath, format string, stdout, stderr io.Writer) error {
	removed := []string{}
	for _, p := range []string{globalPath, projectPath} {
		if err := os.Remove(p); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			printError(stderr, internalError("ktx init opencode: remove %q: %v", p, err), format)
			return internalError("ktx init opencode: remove %q: %v", p, err)
		}
		removed = append(removed, p)
	}
	if len(removed) == 0 {
		printError(stderr, notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath), format)
		return notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath)
	}
	writeUninstallReport(stdout, removed, format)
	return nil
}

// initOpencodeShow reports where the plugin is installed. Both locations
// are inspected; the first match wins for the primary report, but a
// note is emitted (text mode) or a second field is included (JSON mode)
// if both locations have the file — that is a configuration smell the
// user should know about.
//
// Exit codes: 0 when at least one location has the file, 2 when neither
// has it. The 2 exit code is the standard "not-found" class so
// scripts can branch on it.
func initOpencodeShow(globalPath, projectPath, format string, stdout, stderr io.Writer) error {
	hasGlobal := fileExists(globalPath)
	hasProject := fileExists(projectPath)

	if !hasGlobal && !hasProject {
		printError(stderr, notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath), format)
		return notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath)
	}

	primary := globalPath
	if !hasGlobal && hasProject {
		primary = projectPath
	}
	var also []string
	if hasGlobal && hasProject {
		also = append(also, projectPath)
	}
	writeShowReport(stdout, primary, also, format)
	return nil
}

// fileExists reports whether path is a regular file (or symlink to a
// regular file). It does not follow symlinks for the existence check —
// os.Stat follows them, which is what we want: a broken symlink reports
// as not-existing.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// writeInstallReport emits the install confirmation in the requested
// format. JSON mode wraps the result in an `installed` envelope so the
// caller can match on it; text mode is a single human-readable line.
func writeInstallReport(w io.Writer, target, format string) {
	if format == FormatText {
		fmt.Fprintf(w, "ktx opencode plugin installed at %s\n", target)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"installed": true,
		"path":      target,
	})
}

// writeUninstallReport emits the uninstall confirmation. When multiple
// files were removed, all of them are listed so the user can audit what
// `ktx init opencode --uninstall` actually did.
func writeUninstallReport(w io.Writer, removed []string, format string) {
	if format == FormatText {
		if len(removed) == 1 {
			fmt.Fprintf(w, "ktx opencode plugin removed from %s\n", removed[0])
			return
		}
		fmt.Fprintf(w, "ktx opencode plugin removed from %d locations: %s\n", len(removed), strings.Join(removed, ", "))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"uninstalled": true,
		"paths":       removed,
	})
}

// writeShowReport emits the --show result. The primary path is the
// first one matched (global first, then project); the also list names
// any other locations where the plugin is also installed, so the user
// can decide whether to clean up.
func writeShowReport(w io.Writer, primary string, also []string, format string) {
	if format == FormatText {
		fmt.Fprintf(w, "ktx opencode plugin installed at %s\n", primary)
		for _, p := range also {
			fmt.Fprintf(w, "  (also installed at %s)\n", p)
		}
		return
	}
	payload := map[string]any{
		"installed": true,
		"path":      primary,
	}
	if len(also) > 0 {
		payload["also"] = also
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// parseInitDispatchArgs consumes the runtime positional and init flags.
func parseInitDispatchArgs(args []string) ([]string, any, error) {
	f, runtime, err := parseInitArgs(args)
	if err != nil {
		return nil, nil, err
	}
	return nil, initParsedArgs{flags: f, runtime: runtime}, nil
}

// initCommandFunc is the dispatch wrapper. The dispatcher already
// parsed the init-specific arguments and carries them on the context.
//
// NeedsDB=false: ktx init never touches the ktx database — it
// only writes/reads/removes plugin files on disk.
func initCommandFunc(ctx *DispatchContext) (any, error) {
	parsed, ok := ctx.ParsedArgs.(initParsedArgs)
	if !ok {
		return nil, internalError("ktx init: command arguments were not parsed")
	}
	f := parsed.flags
	runtime := parsed.runtime

	if f.showHelp {
		printInitHelp(ctx.Stdout)
		return nil, nil
	}

	if runtime == "" {
		return nil, usageError("ktx init: missing runtime name; usage: ktx init <runtime> [flags] (supported: opencode)")
	}

	// At most one action flag is meaningful; mixing --uninstall/--show with
	// --project is permitted (--project scopes the action), but stacking
	// --uninstall + --show is not.
	if f.uninstall && f.show {
		return nil, usageError("ktx init: --uninstall and --show are mutually exclusive")
	}

	switch runtime {
	case "opencode":
		return nil, initOpencodeLogic(&f, ctx.Format, ctx.Stdout, ctx.Stderr)
	default:
		return nil, usageError("ktx init: unknown runtime %q; supported: opencode", runtime)
	}
}

// init registers `ktx init` with the dispatch module. After init runs,
// the verb is reachable through runWith.
func init() {
	Register("init", RegisterOptions{
		Description: "install a runtime adapter",
		NeedsDB:     false,
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
	for _, a := range args {
		switch a {
		case "--project":
			f.project = true
		case "--uninstall":
			f.uninstall = true
		case "--show":
			f.show = true
		case "--help", "-h":
			f.showHelp = true
		default:
			if len(a) > 1 && a[0] == '-' {
				return f, "", fmt.Errorf("unknown flag %q", a)
			}
			if runtime == "" {
				runtime = a
				continue
			}
			return f, "", fmt.Errorf("unexpected argument %q", a)
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
	fmt.Fprintln(w, "Supported runtimes:")
	fmt.Fprintln(w, "  opencode   install the OpenCode TS plugin adapter")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --project      install to project scope (./.opencode/plugins/ktx.ts)")
	fmt.Fprintln(w, "                 default scope is global ($XDG_CONFIG_HOME/opencode/plugins/ktx.ts)")
	fmt.Fprintln(w, "  --uninstall    remove the plugin file (checks both locations)")
	fmt.Fprintln(w, "  --show         report whether the plugin is installed and where")
	fmt.Fprintln(w, "  -h, --help     show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  action succeeded (install, uninstall of at least one file, show found)")
	fmt.Fprintln(w, "  1  usage error (unknown runtime, conflicting flags)")
	fmt.Fprintln(w, "  2  not found (--show found nothing, --uninstall found nothing)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The installed plugin subscribes to tool.execute.after and shells out to")
	fmt.Fprintln(w, "`ktx hook` for each event, sending the raw callback object tagged")
	fmt.Fprintln(w, "with runtime=\"opencode\". When KTX_SESSION_ID is")
	fmt.Fprintln(w, "set in the environment, the plugin noops so the wrapper")
	fmt.Fprintln(w, "path remains the single source of truth.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  ktx init opencode              # global install")
	fmt.Fprintln(w, "  ktx init opencode --project    # project install")
	fmt.Fprintln(w, "  ktx init opencode --show       # check status")
	fmt.Fprintln(w, "  ktx init opencode --uninstall  # remove")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
