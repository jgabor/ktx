// `ktx init` — the runtime adapter installer (Decisions 1 revised, 2 revised).
// It writes a thin adapter that an agent runtime loads natively, so events
// flow into the ktx store without requiring the user to wrap the agent.
//
// Three integration paths share one store (D1 revised): the wrapper
// (`ktx run`) parses --format json, the sidecar (`ktx write`) accepts
// caller-supplied payloads, and adapters (`ktx init`) install native
// hook points in the agent runtime. Not all runtimes use hooks (D2
// revised) — OpenCode uses in-process TS plugins, Codex would use
// AGENTS.md injection — so `init` is broader than the former `hook
// install` namespace it replaces.
//
// PoC scope (Decision 1 revised, OpenCode focus): only `ktx init opencode`
// is implemented. The plugin subscribes to `tool.execute.after` and calls
// `ktx write` per event (Decision 9 flat JSON). The plugin noops when
// KTX_SESSION_ID is set (Decision 10) so the wrapper path remains the
// single source of truth — both paths active means one of them wins.
//
// Flags:
//
//	--project      install to <cwd>/.opencode/plugins/ktx.ts (project scope)
//	--uninstall    remove the installed plugin (checks both locations)
//	--show         report whether the plugin is installed and where
//
// Default (no flag) installs to ~/.config/opencode/plugins/ktx.ts (global).
// All three flags are mutually exclusive actions; combining --project
// with --uninstall/--show still scopes the action to that location.
//
// Path resolution (global): $HOME/.config/opencode/plugins/ktx.ts
//
//	(matches OpenCode's documented plugin directory).
//
// Path resolution (project): ./.opencode/plugins/ktx.ts relative to the
//
//	invocation's CWD.
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

// ktxOpencodePluginTemplate is the TypeScript source written to the
// OpenCode plugin directory. It is embedded as a Go raw string so the
// installed plugin is byte-for-byte identical across invocations and
// platforms; the version bump on the Go side automatically refreshes
// the installed file when users re-run `ktx init opencode`.
//
// Behaviour:
//   - Listens on `tool.execute.after` (the only event in scope for M1–M3;
//     `tool.execute.before` is deferred to M4 for compression).
//   - Noops when KTX_SESSION_ID is present (D10 dedup): when the user is
//     also running the agent under `ktx run`, the wrapper owns persistence
//     and the plugin would otherwise double-record.
//   - Pipes a flat JSON tool_call payload (D9) to `ktx write` via Bun's $
//     shell API. `session_id` is left empty so ktx write resolves session
//     identity by ppid file lookup (Decision 1 revised, hook path).
//   - `content_hash` is left empty so ktx write can compute it (D3: SHA-256
//     of output) — the plugin avoids re-implementing hash logic in TS.
//   - Generates a fresh 16-char lowercase alpha nanoid matching the
//     alphabet of Decision 3. Uses crypto.getRandomValues for randomness
//     and indexes into the alphabet; modulo bias (26 vs 256) is accepted
//     for PoC scope — the entropy is well above the ~75 bits Decision 3
//     targets. (Bun's crypto.randomUUID produces hex, which violates D3.)
//
// Bun's `$` template literal (https://bun.sh/docs/runtime/shell) is the
// lowest-friction way for an in-process TS plugin to invoke a sibling
// binary; .stdin() pipes the payload, .quiet() suppresses command echo,
// .nothrow() keeps the agent alive if ktx is briefly unavailable.
const ktxOpencodePluginTemplate = `import type { Plugin } from "@opencode-ai/plugin"

const KTX_ID_ALPHABET = "abcdefghijklmnopqrstuvwxyz"
const ktxGenerateID = (): string => {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  let id = ""
  for (let i = 0; i < 16; i++) id += KTX_ID_ALPHABET[bytes[i] % 26]
  return id
}

const KtxPlugin: Plugin = async () => {
  return {
    "tool.execute.after": async (input: any, output: any) => {
      // Noop when wrapper is active (D10 dedup)
      if (process.env.KTX_SESSION_ID) return

      const toolName = input.tool || "unknown"
      const toolInput = JSON.stringify(input.args || {})
      const toolOutput = typeof output === "string" ? output : JSON.stringify(output)

      const record = {
        id: ktxGenerateID(),
        session_id: "",
        tool: toolName,
        input: toolInput,
        output: toolOutput,
        content_hash: "",
        timestamp: new Date().toISOString(),
      }

      // Call ktx write via Bun shell
      await $` + "`ktx write`" + `.stdin(JSON.stringify(record)).quiet().nothrow()
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

func runInit(args []string, opts Options) int {
	return runInitWith(args, opts, os.Stdout, os.Stderr)
}

// runInitWith is the testable core of `ktx init`. stdout/stderr are
// injected so the install/uninstall/show flow can be exercised without
// touching os.Stdout/Stderr or the real home directory.
func runInitWith(args []string, opts Options, stdout, stderr io.Writer) int {
	// Stdlib flag.FlagSet stops at the first non-flag argument, which
	// breaks `ktx init opencode --show` (the runtime name is positional).
	// parseInitArgs walks the arg list manually so flags can appear
	// anywhere; the recognised flag set is small and stable so a hand
	// parser is cheaper than dragging in a third-party CLI library.
	f, runtime, err := parseInitArgs(args)
	if err != nil {
		printError(stderr, usageError("ktx init: %v", err), opts.Format)
		return ExitUsage
	}
	if f.showHelp {
		printInitHelp(stdout)
		return ExitOK
	}

	if runtime == "" {
		printError(stderr, usageError("ktx init: missing runtime name; usage: ktx init <runtime> [flags] (supported: opencode)"), opts.Format)
		return ExitUsage
	}

	// At most one action flag is meaningful; mixing --uninstall/--show with
	// --project is permitted (--project scopes the action), but stacking
	// --uninstall + --show is not.
	if f.uninstall && f.show {
		printError(stderr, usageError("ktx init: --uninstall and --show are mutually exclusive"), opts.Format)
		return ExitUsage
	}

	switch runtime {
	case "opencode":
		return initOpencode(&f, opts, stdout, stderr)
	default:
		printError(stderr, usageError("ktx init: unknown runtime %q; supported: opencode", runtime), opts.Format)
		return ExitUsage
	}
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

// initOpencode routes to the install/uninstall/show handler for the
// OpenCode adapter. It resolves the candidate plugin paths once and
// passes them down so the handlers do not repeat path logic.
func initOpencode(f *initFlags, opts Options, stdout, stderr io.Writer) int {
	globalPath, err := opencodeGlobalPluginPath()
	if err != nil {
		printError(stderr, internalError("ktx init opencode: resolve global plugin path: %v", err), opts.Format)
		return ExitInternal
	}
	// The project path is always resolved (it does not require user
	// lookup), but only consulted for --show/--uninstall or when --project
	// scopes the install.
	projectPath := opencodeProjectPluginPath()

	switch {
	case f.show:
		return initOpencodeShow(globalPath, projectPath, opts, stdout, stderr)
	case f.uninstall:
		return initOpencodeUninstall(globalPath, projectPath, opts, stdout, stderr)
	default:
		// Default action is install. --project (when set) scopes the
		// install to the project directory; otherwise the global dir
		// is the install target.
		target := globalPath
		if f.project {
			target = projectPath
		}
		return initOpencodeInstall(target, opts, stdout, stderr)
	}
}

// opencodeGlobalPluginPath returns the path OpenCode loads plugins from
// when configured globally: $HOME/.config/opencode/plugins/<file>. The
// home directory is resolved via os.UserHomeDir so the result matches
// OpenCode's expectations on every platform (XDG_CONFIG_HOME override
// is intentionally not consulted here — OpenCode documents ~/.config
// directly).
func opencodeGlobalPluginPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "plugins", ktxPluginFileName), nil
}

// opencodeProjectPluginPath returns the project-scoped plugin path,
// always relative to the current working directory. The CWD is captured
// at call time (not process start) so the path is correct even after a
// future chdir; for M1 the wrapper never chdirs.
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
func initOpencodeInstall(target string, opts Options, stdout, stderr io.Writer) int {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		printError(stderr, internalError("ktx init opencode: create directory %q: %v", filepath.Dir(target), err), opts.Format)
		return ExitInternal
	}
	if err := os.WriteFile(target, []byte(ktxOpencodePluginTemplate), 0o644); err != nil {
		printError(stderr, internalError("ktx init opencode: write plugin %q: %v", target, err), opts.Format)
		return ExitInternal
	}

	writeInstallReport(stdout, target, opts.Format)
	return ExitOK
}

// initOpencodeUninstall removes the plugin from whichever location has
// it (global checked first, then project). When neither has the file,
// the exit code is not-found (2) so callers can detect "nothing to do"
// without inspecting stdout. When both have it, both are removed — the
// user invoked `--uninstall` once and expects a clean state.
func initOpencodeUninstall(globalPath, projectPath string, opts Options, stdout, stderr io.Writer) int {
	removed := []string{}
	for _, p := range []string{globalPath, projectPath} {
		if err := os.Remove(p); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			printError(stderr, internalError("ktx init opencode: remove %q: %v", p, err), opts.Format)
			return ExitInternal
		}
		removed = append(removed, p)
	}
	if len(removed) == 0 {
		printError(stderr, notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath), opts.Format)
		return ExitNotFound
	}
	writeUninstallReport(stdout, removed, opts.Format)
	return ExitOK
}

// initOpencodeShow reports where the plugin is installed. Both locations
// are inspected; the first match wins for the primary report, but a
// note is emitted (text mode) or a second field is included (JSON mode)
// if both locations have the file — that is a configuration smell the
// user should know about.
//
// Exit codes: 0 when at least one location has the file, 2 when neither
// has it. The 2 exit code is the standard "not-found" class (D4) so
// scripts can branch on it.
func initOpencodeShow(globalPath, projectPath string, opts Options, stdout, stderr io.Writer) int {
	hasGlobal := fileExists(globalPath)
	hasProject := fileExists(projectPath)

	if !hasGlobal && !hasProject {
		printError(stderr, notFoundError("ktx init opencode: plugin not installed (checked global %q and project %q)", globalPath, projectPath), opts.Format)
		return ExitNotFound
	}

	primary := globalPath
	if !hasGlobal && hasProject {
		primary = projectPath
	}
	var also []string
	if hasGlobal && hasProject {
		also = append(also, projectPath)
	}
	writeShowReport(stdout, primary, also, opts.Format)
	return ExitOK
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

// printInitHelp writes the `ktx init` help. The flag table mirrors the
// dispatcher's action contract: --project scopes, --uninstall removes,
// --show reports.
func printInitHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx init — install a runtime adapter (Decision 2 revised)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx init <runtime> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Supported runtimes:")
	fmt.Fprintln(w, "  opencode   install the OpenCode TS plugin adapter")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --project      install to project scope (./.opencode/plugins/ktx.ts)")
	fmt.Fprintln(w, "                 default scope is global (~/.config/opencode/plugins/ktx.ts)")
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
	fmt.Fprintln(w, "`ktx write` for each event (Decision 9 flat JSON). When KTX_SESSION_ID is")
	fmt.Fprintln(w, "set in the environment (Decision 10), the plugin noops so the wrapper")
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
