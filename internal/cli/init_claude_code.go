// `ktx init claude` installs the ktx Claude Code plugin through the claude CLI.
//
// The plugin is published as ktx@ktx from the marketplace at the root of the
// jgabor/ktx repository. Its Bash PostToolUse hook runs
// `ktx hook --runtime claude-code` and its mod runs `ktx stats summary`, both
// found on PATH. ktx never edits Claude Code settings files: install runs
// `claude plugin marketplace add` and `claude plugin install`, --uninstall runs
// the matching remove commands, and --show reads `claude plugin list` and
// `claude plugin marketplace list`. A marketplace named ktx that already
// exists is kept, whatever its source. Without claude on PATH, install prints
// the commands instead.
//
// Exit codes follow the rest of `ktx init`: 0 on success, 1 on usage error,
// 2 when nothing is installed (--show, --uninstall), 4 on internal error.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/jgabor/ktx/internal/version"
)

const (
	claudeCodeMarketplaceName   = "ktx"
	claudeCodeMarketplaceSource = "jgabor/ktx"
	claudeCodePluginID          = "ktx@ktx"
	claudeCodeReloadNote        = "run /reload-plugins in Claude Code (or restart it) to load the plugin"
	claudeCodeTroubleshooting   = "docs/guides/claude-code.md#troubleshooting"
)

// claudeCodeMarketplace is one entry of `claude plugin marketplace list --json`.
type claudeCodeMarketplace struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Repo   string `json:"repo,omitempty"`
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
}

// claudeCodeInstalledPlugin is one entry of `claude plugin list --json`.
type claudeCodeInstalledPlugin struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// claudeCodeState is what Claude Code reports about the ktx marketplace and
// plugin; nil means absent.
type claudeCodeState struct {
	marketplace *claudeCodeMarketplace
	plugin      *claudeCodeInstalledPlugin
}

// claudeCodeInstallSteps are the commands install runs, printed when ktx
// cannot run them itself.
func claudeCodeInstallSteps() []string {
	return []string{
		"claude plugin marketplace add " + claudeCodeMarketplaceSource,
		"claude plugin install " + claudeCodePluginID,
	}
}

// initClaudeCodeLogic routes to the install, uninstall, or show handler.
func initClaudeCodeLogic(f *initFlags, format string, stdout, _ io.Writer) error {
	if f.project {
		return usageError("ktx init claude: --project is not supported; the plugin is installed for the user through Claude Code")
	}
	switch {
	case f.show:
		return initClaudeCodeShow(format, stdout)
	case f.uninstall:
		return initClaudeCodeUninstall(format, stdout)
	default:
		return initClaudeCodeInstall(format, stdout)
	}
}

// runClaude runs the claude CLI without a shell and returns its stdout. A
// non-zero exit is an error that carries stderr.
func runClaude(claude string, args ...string) ([]byte, error) {
	command := exec.Command(claude, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return nil, fmt.Errorf("claude %s: %v: %s", strings.Join(args, " "), err, text)
		}
		return nil, fmt.Errorf("claude %s: %v", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// readClaudeCodeState reads the ktx marketplace and plugin from Claude Code.
func readClaudeCodeState(claude string) (claudeCodeState, error) {
	var state claudeCodeState
	raw, err := runClaude(claude, "plugin", "marketplace", "list", "--json")
	if err != nil {
		return state, err
	}
	var marketplaces []claudeCodeMarketplace
	if err := json.Unmarshal(raw, &marketplaces); err != nil {
		return state, fmt.Errorf("parse claude plugin marketplace list: %w", err)
	}
	for _, marketplace := range marketplaces {
		if marketplace.Name == claudeCodeMarketplaceName {
			state.marketplace = &marketplace
			break
		}
	}
	raw, err = runClaude(claude, "plugin", "list", "--json")
	if err != nil {
		return state, err
	}
	var plugins []claudeCodeInstalledPlugin
	if err := json.Unmarshal(raw, &plugins); err != nil {
		return state, fmt.Errorf("parse claude plugin list: %w", err)
	}
	for _, plugin := range plugins {
		if plugin.ID == claudeCodePluginID {
			state.plugin = &plugin
			break
		}
	}
	return state, nil
}

// describeClaudeCodeMarketplace names where a marketplace reads its plugins.
func describeClaudeCodeMarketplace(marketplace *claudeCodeMarketplace) string {
	switch {
	case marketplace == nil:
		return "not added"
	case marketplace.Repo != "":
		return marketplace.Source + " " + marketplace.Repo
	case marketplace.Path != "":
		return marketplace.Source + " " + marketplace.Path
	case marketplace.URL != "":
		return marketplace.Source + " " + marketplace.URL
	default:
		return marketplace.Source
	}
}

// ktxOnPath reports the ktx the plugin will run, or "" when none is on PATH.
func ktxOnPath() string {
	path, err := exec.LookPath("ktx")
	if err != nil {
		return ""
	}
	return path
}

// initClaudeCodeInstall adds the ktx marketplace and installs ktx@ktx unless
// Claude Code already has them.
func initClaudeCodeInstall(format string, stdout io.Writer) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		writeClaudeCodeManualInstall(stdout, format)
		return nil
	}
	state, err := readClaudeCodeState(claude)
	if err != nil {
		return internalError("ktx init claude: %v", err)
	}
	addedMarketplace := state.marketplace == nil
	if addedMarketplace {
		if _, err := runClaude(claude, "plugin", "marketplace", "add", claudeCodeMarketplaceSource); err != nil {
			return internalError("ktx init claude: %v", err)
		}
	}
	installedPlugin := state.plugin == nil
	if installedPlugin {
		if _, err := runClaude(claude, "plugin", "install", claudeCodePluginID); err != nil {
			return internalError("ktx init claude: %v", err)
		}
	}
	changed := addedMarketplace || installedPlugin
	if changed {
		if state, err = readClaudeCodeState(claude); err != nil {
			return internalError("ktx init claude: %v", err)
		}
	}
	enabled := state.plugin != nil && state.plugin.Enabled
	ktxPath := ktxOnPath()

	if format == FormatText {
		if changed {
			fmt.Fprintf(stdout, "ktx Claude Code plugin installed: %s\n", claudeCodePluginID)
		} else {
			fmt.Fprintf(stdout, "ktx Claude Code plugin already installed: %s (no change)\n", claudeCodePluginID)
		}
		fmt.Fprintf(stdout, "marketplace: %s (%s)\n", claudeCodeMarketplaceName, describeClaudeCodeMarketplace(state.marketplace))
		if !enabled {
			fmt.Fprintf(stdout, "plugin is disabled; enable it with: claude plugin enable %s\n", claudeCodePluginID)
		}
		writeClaudeCodeKtxPath(stdout, ktxPath)
		if changed {
			fmt.Fprintf(stdout, "Note: %s\n", claudeCodeReloadNote)
		}
		fmt.Fprintf(stdout, "troubleshooting: %s\n", claudeCodeTroubleshooting)
		return nil
	}
	payload := map[string]any{
		"installed":          true,
		"plugin":             claudeCodePluginID,
		"enabled":            enabled,
		"marketplace":        state.marketplace,
		"added_marketplace":  addedMarketplace,
		"installed_plugin":   installedPlugin,
		"ktx_on_path":        ktxPath,
		"ktx_version":        version.Version,
		"troubleshooting":    claudeCodeTroubleshooting,
		"runtime_activation": "not_verified",
	}
	if changed {
		payload["note"] = claudeCodeReloadNote
	} else {
		payload["already_installed"] = true
	}
	_ = json.NewEncoder(stdout).Encode(payload)
	return nil
}

// writeClaudeCodeManualInstall prints the install commands when claude is not
// on PATH.
func writeClaudeCodeManualInstall(w io.Writer, format string) {
	steps := claudeCodeInstallSteps()
	if format == FormatText {
		fmt.Fprintln(w, "claude not found on PATH; install the ktx plugin from Claude Code with:")
		for _, step := range steps {
			fmt.Fprintf(w, "  %s\n", step)
		}
		fmt.Fprintf(w, "Then %s.\n", claudeCodeReloadNote)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"installed":  false,
		"reason":     "claude not found on PATH",
		"plugin":     claudeCodePluginID,
		"next_steps": steps,
		"note":       claudeCodeReloadNote,
	})
}

func writeClaudeCodeKtxPath(w io.Writer, path string) {
	if path == "" {
		fmt.Fprintln(w, "ktx on PATH: not found; the plugin runs the ktx on PATH, so add it before using Claude Code")
		return
	}
	fmt.Fprintf(w, "ktx on PATH: %s\n", path)
}

// initClaudeCodeShow reports the ktx plugin as Claude Code lists it. It exits 2
// when ktx@ktx is not installed or claude is not on PATH.
func initClaudeCodeShow(format string, stdout io.Writer) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return notFoundError("ktx init claude: claude not found on PATH; cannot inspect the ktx plugin")
	}
	state, err := readClaudeCodeState(claude)
	if err != nil {
		return internalError("ktx init claude: %v", err)
	}
	if state.plugin == nil {
		return notFoundError("ktx init claude: %s is not installed; run `ktx init claude`", claudeCodePluginID)
	}
	ktxPath := ktxOnPath()
	if format == FormatText {
		fmt.Fprintf(stdout, "ktx Claude Code plugin: %s\n", claudeCodePluginID)
		fmt.Fprintf(stdout, "enabled: %t\n", state.plugin.Enabled)
		fmt.Fprintf(stdout, "marketplace: %s (%s)\n", claudeCodeMarketplaceName, describeClaudeCodeMarketplace(state.marketplace))
		writeClaudeCodeKtxPath(stdout, ktxPath)
		fmt.Fprintf(stdout, "this ktx: %s\n", version.Version)
		return nil
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{
		"installed":          true,
		"plugin":             claudeCodePluginID,
		"enabled":            state.plugin.Enabled,
		"marketplace":        state.marketplace,
		"ktx_on_path":        ktxPath,
		"ktx_version":        version.Version,
		"runtime_activation": "not_verified",
	})
	return nil
}

// initClaudeCodeUninstall uninstalls ktx@ktx and removes the ktx marketplace,
// whichever Claude Code has. It exits 2 when it has neither.
func initClaudeCodeUninstall(format string, stdout io.Writer) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return notFoundError("ktx init claude: claude not found on PATH; remove the plugin from Claude Code with `claude plugin uninstall %s` and `claude plugin marketplace remove %s`", claudeCodePluginID, claudeCodeMarketplaceName)
	}
	state, err := readClaudeCodeState(claude)
	if err != nil {
		return internalError("ktx init claude: %v", err)
	}
	if state.plugin == nil && state.marketplace == nil {
		return notFoundError("ktx init claude: neither %s nor the %s marketplace is installed", claudeCodePluginID, claudeCodeMarketplaceName)
	}
	if state.plugin != nil {
		if _, err := runClaude(claude, "plugin", "uninstall", claudeCodePluginID); err != nil {
			return internalError("ktx init claude: %v", err)
		}
	}
	if state.marketplace != nil {
		if _, err := runClaude(claude, "plugin", "marketplace", "remove", claudeCodeMarketplaceName); err != nil {
			return internalError("ktx init claude: %v", err)
		}
	}
	if format == FormatText {
		if state.plugin != nil {
			fmt.Fprintf(stdout, "ktx Claude Code plugin uninstalled: %s\n", claudeCodePluginID)
		}
		if state.marketplace != nil {
			fmt.Fprintf(stdout, "marketplace removed: %s\n", claudeCodeMarketplaceName)
		}
		return nil
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{
		"uninstalled":         true,
		"plugin":              claudeCodePluginID,
		"removed_plugin":      state.plugin != nil,
		"removed_marketplace": state.marketplace != nil,
	})
	return nil
}
