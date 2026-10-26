// `ktx init opencode` installs the ktx OpenCode package through the opencode
// CLI.
//
// The package is published to npm as @jgabor/ktx. Its server target
// compresses tool output and records model-step telemetry, and its TUI target
// shows ktx statistics in the sidebar; both run the ktx on PATH. Install runs
// `opencode plugin -g <spec>`, or `opencode plugin <spec>` in the current
// folder with --project, which adds the package to OpenCode's server and TUI
// plugin config. The spec is the package pinned to this binary's release, so
// a ktx upgrade followed by `ktx init opencode` replaces an older pin. A build
// without a release number installs the unpinned package, and keeps any one
// ktx entry the scope already has, such as a checkout folder.
//
// OpenCode's plugin command can only add entries, so ktx removes its own
// entries itself before it adds a different one, and --uninstall removes
// them. Every other entry, comment and byte of each config file stays as it
// was. Install also removes the ktx.ts plugin file earlier ktx versions
// generated, globally and in the current project, once the package is in
// place. Without opencode on PATH, install changes nothing and prints the
// commands.
//
// Exit codes follow the rest of `ktx init`: 0 on success, 1 on usage error,
// 2 when nothing is installed (--show, --uninstall), 4 on internal error.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jgabor/ktx/internal/opencodeplugin"
	"github.com/jgabor/ktx/internal/version"
)

const (
	opencodeTelemetryEvidence      = "OpenCode-normalized optional model-step token evidence"
	opencodeTelemetryClaimBoundary = "not raw provider responses; no cache hit, cache miss, native request, or cache isolation claims"
	opencodeTroubleshooting        = "docs/guides/opencode-telemetry.md#troubleshooting"
	opencodeRestartNote            = "restart OpenCode to load the plugin"
)

// opencodeScopeReport is the JSON form of one scope.
type opencodeScopeReport struct {
	Scope           string                 `json:"scope"`
	ConfigDir       string                 `json:"config_dir"`
	Entries         []opencodeplugin.Entry `json:"entries"`
	GeneratedPlugin string                 `json:"generated_plugin,omitempty"`
}

func reportOpencodeScope(state opencodeplugin.State) opencodeScopeReport {
	report := opencodeScopeReport{Scope: state.Scope.Name, ConfigDir: state.Scope.Dir, Entries: state.Entries()}
	if state.Legacy {
		report.GeneratedPlugin = state.Scope.Legacy
	}
	return report
}

// initOpencodeLogic routes to the install, uninstall, or show handler. Every
// config file it may touch is read first, so a malformed one stops the
// command before anything changes.
func initOpencodeLogic(f *initFlags, format string, stdout, _ io.Writer) error {
	scopes, err := opencodeplugin.Scopes()
	if err != nil {
		return internalError("ktx init opencode: %v", err)
	}
	states := make([]opencodeplugin.State, 0, len(scopes))
	for _, scope := range scopes {
		state, err := opencodeplugin.Inspect(scope, "")
		if err != nil {
			return internalError("ktx init opencode: %v", err)
		}
		states = append(states, state)
	}
	global, project := states[0], states[1]
	switch {
	case f.show:
		if f.project {
			return initOpencodeShow([]opencodeplugin.State{project}, format, stdout)
		}
		return initOpencodeShow(states, format, stdout)
	case f.uninstall:
		if f.project {
			return initOpencodeUninstall([]opencodeplugin.State{project}, format, stdout)
		}
		return initOpencodeUninstall(states, format, stdout)
	case f.project:
		return initOpencodeInstall(project, global, states, format, stdout)
	default:
		return initOpencodeInstall(global, project, states, format, stdout)
	}
}

// runOpencode runs the opencode CLI without a shell and returns its output.
func runOpencode(name string, args ...string) (string, string, error) {
	command := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

// opencodeCommand is the command line that adds spec to a scope.
func opencodeCommand(scope opencodeplugin.Scope, spec string) string {
	return "opencode " + strings.Join(scope.InstallArgs(spec), " ")
}

// unpinnedKeeps reports whether a ktx without a release number keeps the
// scope's ktx entry: only one checkout folder, as `mage install` sets, is
// kept. A release pin or anything else is replaced by the unpinned package.
func unpinnedKeeps(state opencodeplugin.State) (string, bool) {
	if state.PointsAt(opencodeplugin.PackageName) {
		return opencodeplugin.PackageName, true
	}
	current, ok := state.Current()
	if !ok || !filepath.IsAbs(current) {
		return "", false
	}
	return current, true
}

// unpinnedNote says why a build without a release number installs or keeps
// what it does.
func unpinnedNote(kept string, replaced ...string) string {
	if kept != "" {
		return fmt.Sprintf("this ktx (%s) has no release number, so the existing entry %s is kept", version.Version, kept)
	}
	instead := ""
	if len(replaced) > 0 {
		instead = " in place of " + strings.Join(replaced, ", ")
	}
	return fmt.Sprintf("this ktx (%s) has no release number, so the unpinned %s is installed%s; OpenCode loads the version it fetches", version.Version, opencodeplugin.PackageName, instead)
}

// initOpencodeInstall makes the package the one ktx entry of target, then
// removes the plugin files earlier ktx versions generated in every scope.
func initOpencodeInstall(target, other opencodeplugin.State, all []opencodeplugin.State, format string, stdout io.Writer) error {
	spec, pinned := opencodeplugin.SpecFor(version.Version)
	kept := ""
	satisfied := target.PointsAt(spec)
	if !pinned {
		kept, satisfied = unpinnedKeeps(target)
	}
	replaced := []string{}
	if !satisfied {
		for _, entry := range target.Entries() {
			if !slices.Contains(replaced, entry.Spec) {
				replaced = append(replaced, entry.Spec)
			}
		}
	}
	var legacy []string
	for _, state := range all {
		if state.Legacy {
			legacy = append(legacy, state.Scope.Legacy)
		}
	}

	installed := spec
	if satisfied && !pinned {
		installed = kept
	}

	// Without opencode on PATH install changes nothing. A scope that already
	// has the right entry and no generated plugin to remove has nothing to
	// change, so it reports as installed.
	opencode, err := exec.LookPath("opencode")
	if err != nil && (!satisfied || len(legacy) > 0) {
		writeOpencodeManualInstall(stdout, format, target, spec, installed, pinned, satisfied, legacy)
		return nil
	}
	changed := false
	if !satisfied {
		if changed, err = opencodeplugin.Point(target, opencode, spec, "", runOpencode); err != nil {
			if errors.Is(err, opencodeplugin.ErrRestored) {
				return internalError("ktx init opencode: %v", err)
			}
			return internalError("ktx init opencode: %v\n  Recovery: remove every %s entry from the plugin lists in %s, then run `%s`%s", err,
				opencodeplugin.PackageName, strings.Join(target.Scope.ConfigFiles(), ", "), opencodeCommand(target.Scope, spec), projectFolderNote(target.Scope))
		}
	}
	removed := []string{}
	for _, path := range legacy {
		if err := opencodeplugin.RemoveGenerated(path); err != nil {
			return internalError("ktx init opencode: %s is installed, but %v; remove that file by hand so ktx does not run twice per tool call", opencodeplugin.PackageName, err)
		}
		removed = append(removed, path)
	}
	note := ""
	switch {
	case !pinned && satisfied:
		note = unpinnedNote(kept)
	case !pinned:
		note = unpinnedNote("", replaced...)
	}
	ktxPath := ktxOnPath()

	if format == FormatText {
		if changed {
			fmt.Fprintf(stdout, "ktx OpenCode plugin installed: %s (%s, %s)\n", installed, target.Scope.Name, target.Scope.Dir)
		} else if len(removed) == 0 {
			fmt.Fprintf(stdout, "ktx OpenCode plugin already installed: %s (%s, %s; no change)\n", installed, target.Scope.Name, target.Scope.Dir)
		} else {
			fmt.Fprintf(stdout, "ktx OpenCode plugin already installed: %s (%s, %s)\n", installed, target.Scope.Name, target.Scope.Dir)
		}
		if note != "" {
			fmt.Fprintf(stdout, "note: %s\n", note)
		}
		for _, path := range removed {
			fmt.Fprintf(stdout, "removed the plugin an earlier ktx generated at %s\n", path)
		}
		for _, entry := range other.Entries() {
			fmt.Fprintf(stdout, "note: the %s config also lists %s in %s\n", other.Scope.Name, entry.Spec, entry.File)
		}
		writeOpencodeKtxPath(stdout, ktxPath)
		if changed || len(removed) > 0 {
			fmt.Fprintf(stdout, "Note: %s\n", opencodeRestartNote)
		}
		fmt.Fprintf(stdout, "troubleshooting: %s\n", opencodeTroubleshooting)
		return nil
	}
	payload := map[string]any{
		"installed":                 true,
		"package":                   opencodeplugin.PackageName,
		"spec":                      installed,
		"pinned":                    pinned,
		"scope":                     target.Scope.Name,
		"config_dir":                target.Scope.Dir,
		"changed":                   changed,
		"removed_generated_plugins": removed,
		"other_scope":               reportOpencodeScope(other),
		"ktx_on_path":               ktxPath,
		"ktx_version":               version.Version,
		"troubleshooting":           opencodeTroubleshooting,
		"runtime_activation":        "not_verified",
	}
	if !changed {
		payload["already_installed"] = true
	}
	if note != "" {
		payload["note"] = note
	}
	_ = json.NewEncoder(stdout).Encode(payload)
	return nil
}

// projectFolderNote says where a project command must run.
func projectFolderNote(scope opencodeplugin.Scope) string {
	if scope.Global {
		return ""
	}
	return " in " + filepath.Dir(scope.Dir)
}

// opencodeEntriesForceKeeps returns the target's ktx entries that
// `opencode plugin -f` would leave beside the new one: every entry when one
// of them is a folder or a config list holds more than one.
func opencodeEntriesForceKeeps(target opencodeplugin.State) []opencodeplugin.Entry {
	if len(target.Server) > 1 || len(target.TUI) > 1 {
		return target.Entries()
	}
	for _, entry := range target.Entries() {
		if entry.Spec != opencodeplugin.PackageName && !strings.HasPrefix(entry.Spec, opencodeplugin.PackageName+"@") {
			return target.Entries()
		}
	}
	return nil
}

// writeOpencodeManualInstall prints the commands install would run when
// opencode is not on PATH. -f replaces an older pin of the package; entries
// it would not replace are listed for removal first. A target that already
// has the right entry is reported as installed, with only the removal of
// generated plugin files left to do.
func writeOpencodeManualInstall(w io.Writer, format string, target opencodeplugin.State, spec, installed string, pinned, satisfied bool, legacy []string) {
	steps := []string{}
	removeFirst := []opencodeplugin.Entry{}
	if !satisfied {
		removeFirst = append(removeFirst, opencodeEntriesForceKeeps(target)...)
		args := []string{"plugin"}
		if target.Scope.Global {
			args = append(args, "-g")
		}
		step := "opencode " + strings.Join(append(args, "-f", spec), " ")
		if !target.Scope.Global {
			step = "cd " + shellQuote(filepath.Dir(target.Scope.Dir)) + " && " + step
		}
		steps = append(steps, step)
	}
	for _, path := range legacy {
		steps = append(steps, "rm "+shellQuote(path))
	}
	note := ""
	if !pinned && !satisfied {
		note = unpinnedNote("")
	}
	if satisfied {
		if format == FormatText {
			fmt.Fprintf(w, "opencode not found on PATH; nothing changed. ktx OpenCode plugin already installed: %s (%s, %s)\n", installed, target.Scope.Name, target.Scope.Dir)
			fmt.Fprintln(w, "Remove the plugin an earlier ktx generated with:")
			for _, step := range steps {
				fmt.Fprintf(w, "  %s\n", step)
			}
			fmt.Fprintf(w, "Then %s.\n", opencodeRestartNote)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"installed":         true,
			"already_installed": true,
			"reason":            "opencode not found on PATH",
			"package":           opencodeplugin.PackageName,
			"spec":              installed,
			"pinned":            pinned,
			"scope":             target.Scope.Name,
			"next_steps":        steps,
		})
		return
	}
	if format == FormatText {
		fmt.Fprintln(w, "opencode not found on PATH; nothing changed. Install the ktx OpenCode plugin with:")
		if len(removeFirst) > 0 {
			fmt.Fprintln(w, "  first remove these ktx entries, which `opencode plugin -f` does not replace:")
			for _, entry := range removeFirst {
				fmt.Fprintf(w, "    %s in %s\n", entry.Spec, entry.File)
			}
		}
		for _, step := range steps {
			fmt.Fprintf(w, "  %s\n", step)
		}
		if note != "" {
			fmt.Fprintf(w, "note: %s\n", note)
		}
		fmt.Fprintf(w, "Then %s.\n", opencodeRestartNote)
		return
	}
	payload := map[string]any{
		"installed":            false,
		"reason":               "opencode not found on PATH",
		"package":              opencodeplugin.PackageName,
		"spec":                 spec,
		"pinned":               pinned,
		"scope":                target.Scope.Name,
		"next_steps":           steps,
		"remove_entries_first": removeFirst,
	}
	if note != "" {
		payload["note"] = note
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// shellQuote single-quotes a path for a POSIX shell when it needs quoting.
func shellQuote(path string) string {
	if path != "" && strings.Trim(path, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return path
	}
	return `'` + strings.ReplaceAll(path, `'`, `'"'"'`) + `'`
}

func writeOpencodeKtxPath(w io.Writer, path string) {
	if path == "" {
		fmt.Fprintln(w, "ktx on PATH: not found; the plugin runs the ktx on PATH, so add it before using OpenCode")
		return
	}
	fmt.Fprintf(w, "ktx on PATH: %s\n", path)
}

// opencodeChecked names the config folders a not-found message covers.
func opencodeChecked(states []opencodeplugin.State) string {
	parts := make([]string, 0, len(states))
	for _, state := range states {
		parts = append(parts, state.Scope.Name+" "+state.Scope.Dir)
	}
	return strings.Join(parts, " and ")
}

// initOpencodeShow reports ktx's entries in each scope, any plugin file an
// earlier ktx generated, and whether the entries match this binary. It exits
// 2 when no scope has a ktx entry.
func initOpencodeShow(states []opencodeplugin.State, format string, stdout io.Writer) error {
	found := false
	var legacy []string
	for _, state := range states {
		found = found || state.HasEntries()
		if state.Legacy {
			legacy = append(legacy, state.Scope.Legacy)
		}
	}
	if !found {
		hint := ""
		if len(legacy) > 0 {
			hint = "; an earlier ktx generated plugin is still at " + strings.Join(legacy, " and ") + ", so run `ktx init opencode` to replace it"
		}
		return notFoundError("ktx init opencode: %s is not installed in OpenCode (checked %s); run `ktx init opencode`%s", opencodeplugin.PackageName, opencodeChecked(states), hint)
	}
	spec, pinned := opencodeplugin.SpecFor(version.Version)
	var mismatched []string
	for _, state := range states {
		if !state.HasEntries() {
			continue
		}
		matches := state.PointsAt(spec)
		if !pinned {
			_, matches = unpinnedKeeps(state)
		}
		if !matches {
			mismatched = append(mismatched, state.Scope.Name)
		}
	}
	expected := spec
	if !pinned {
		expected = opencodeplugin.PackageName + " or one checkout folder (this ktx has no release number)"
	}
	ktxPath := ktxOnPath()
	if format == FormatText {
		fmt.Fprintf(stdout, "ktx OpenCode plugin: %s\n", opencodeplugin.PackageName)
		for _, state := range states {
			if !state.HasEntries() {
				fmt.Fprintf(stdout, "%s (%s): not installed\n", state.Scope.Name, state.Scope.Dir)
			}
			for _, entry := range state.Entries() {
				fmt.Fprintf(stdout, "%s (%s): %s in %s\n", state.Scope.Name, state.Scope.Dir, entry.Spec, entry.File)
			}
			if state.Legacy {
				fmt.Fprintf(stdout, "earlier generated plugin: %s; run `ktx init opencode` to remove it\n", state.Scope.Legacy)
			}
		}
		fmt.Fprintf(stdout, "expected for this ktx: %s\n", expected)
		for _, name := range mismatched {
			fix := "ktx init opencode"
			if name == "project" {
				fix += " --project"
			}
			fmt.Fprintf(stdout, "warning: the %s entries do not match this ktx; run `%s` to replace them\n", name, fix)
		}
		writeOpencodeKtxPath(stdout, ktxPath)
		fmt.Fprintf(stdout, "this ktx: %s\n", version.Version)
		return nil
	}
	scopes := make([]opencodeScopeReport, 0, len(states))
	for _, state := range states {
		scopes = append(scopes, reportOpencodeScope(state))
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{
		"installed":          true,
		"package":            opencodeplugin.PackageName,
		"scopes":             scopes,
		"expected_spec":      spec,
		"pinned":             pinned,
		"matches":            len(mismatched) == 0,
		"mismatched_scopes":  append([]string{}, mismatched...),
		"ktx_on_path":        ktxPath,
		"ktx_version":        version.Version,
		"runtime_activation": "not_verified",
	})
	return nil
}

// initOpencodeUninstall removes ktx's entries from every config file of the
// scopes, and the plugin files earlier ktx versions generated there. Every
// other entry stays. It exits 2 when there is nothing to remove.
func initOpencodeUninstall(states []opencodeplugin.State, format string, stdout io.Writer) error {
	removedEntries := []opencodeplugin.Entry{}
	removedFiles := []string{}
	for _, state := range states {
		if !state.HasEntries() && !state.Legacy {
			continue
		}
		for _, file := range state.Scope.ConfigFiles() {
			specs, err := opencodeplugin.RemoveKtxEntries(file, "")
			for _, spec := range specs {
				removedEntries = append(removedEntries, opencodeplugin.Entry{File: file, Spec: spec})
			}
			if err != nil {
				return internalError("ktx init opencode: %v", err)
			}
		}
		if state.Legacy {
			if err := opencodeplugin.RemoveGenerated(state.Scope.Legacy); err != nil {
				return internalError("ktx init opencode: %v", err)
			}
			removedFiles = append(removedFiles, state.Scope.Legacy)
		}
	}
	if len(removedEntries) == 0 && len(removedFiles) == 0 {
		return notFoundError("ktx init opencode: %s is not installed in OpenCode (checked %s)", opencodeplugin.PackageName, opencodeChecked(states))
	}
	if format == FormatText {
		for _, entry := range removedEntries {
			fmt.Fprintf(stdout, "removed %s from %s\n", entry.Spec, entry.File)
		}
		for _, path := range removedFiles {
			fmt.Fprintf(stdout, "removed the plugin an earlier ktx generated at %s\n", path)
		}
		fmt.Fprintln(stdout, "Note: restart OpenCode to unload the plugin")
		return nil
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{
		"uninstalled":               true,
		"package":                   opencodeplugin.PackageName,
		"removed_entries":           removedEntries,
		"removed_generated_plugins": removedFiles,
	})
	return nil
}
