// `ktx init codex` installs the ktx Codex plugin through the codex CLI.
//
// The plugin is served as ktx@ktx from the marketplace in .agents/plugins of
// the jgabor/ktx repository. Its PostToolUse hook runs `ktx hook` and its
// ktx:stats skill runs `ktx stats summary`, both found on PATH. ktx never edits
// Codex configuration: install runs `codex plugin marketplace add` and
// `codex plugin add`, --uninstall runs `codex plugin remove` and
// `codex plugin marketplace remove`, and --show reads `codex plugin list` and
// `codex plugin marketplace list`. A marketplace named ktx that already exists
// is kept, whatever its source. Without codex on PATH, install prints the
// commands instead.
//
// Trust model: Codex does not run a plugin hook until the user has reviewed
// and trusted it in /hooks, so every install report says so. A fresh install
// that records nothing is the expected state before that step.
//
// Earlier ktx versions merged a PostToolUse hook into ~/.codex/hooks.json or
// ./.codex/hooks.json instead. Install and uninstall remove those entries, and
// only those, so the plugin hook is the one ktx hook Codex runs; other hooks
// in the files stay as they are. Identification accepts only the exact shapes
// earlier versions generated (see isKtxCodexHook). It never runs a command or
// probes an executable to establish ownership.
//
// Exit codes follow the rest of `ktx init`: 0 on success, 1 on usage error,
// 2 when nothing is installed (--show, --uninstall), 4 on internal error.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jgabor/ktx/internal/version"
)

const (
	codexMarketplaceName   = "ktx"
	codexMarketplaceSource = "jgabor/ktx"
	codexPluginID          = "ktx@ktx"
	codexTrustNote         = "start a new Codex session, run /hooks and trust the ktx hook; Codex does not run a hook until it is trusted"
	codexTroubleshooting   = "docs/guides/codex.md#troubleshooting"
)

// codexHooksFileName is the hooks file earlier ktx versions merged their
// hook into, globally under ~/.codex and per project under ./.codex.
const codexHooksFileName = "hooks.json"

// ktxHookCommand is the command the plugin hook runs, and the bare command
// the earliest ktx versions merged into hooks.json.
const ktxHookCommand = "ktx hook"

// ktxHookStatusMessage is the status Codex shows while the hook runs. Every
// generated hook carried it, so it is part of the legacy identification.
const ktxHookStatusMessage = "ktx: recording tool call"

// codexPostToolUseKey is the "hooks" key that holds PostToolUse entries.
const codexPostToolUseKey = "PostToolUse"

// codexMarketplace is one entry of `codex plugin marketplace list --json`.
type codexMarketplace struct {
	Name   string `json:"name"`
	Source struct {
		SourceType string `json:"sourceType"`
		Source     string `json:"source"`
	} `json:"marketplaceSource"`
}

// codexInstalledPlugin is one entry of `codex plugin list --json`.
type codexInstalledPlugin struct {
	PluginID string `json:"pluginId"`
	Enabled  bool   `json:"enabled"`
	Source   struct {
		Path string `json:"path"`
	} `json:"source"`
}

// codexState is what Codex reports about the ktx marketplace and plugin; nil
// means absent.
type codexState struct {
	marketplace *codexMarketplace
	plugin      *codexInstalledPlugin
}

// codexLegacyHooks counts the ktx entries an earlier ktx left in one hooks file.
type codexLegacyHooks struct {
	Path    string `json:"path"`
	Entries int    `json:"entries"`
}

// codexInstallSteps are the commands install runs, printed when ktx cannot
// run them itself.
func codexInstallSteps() []string {
	return []string{
		"codex plugin marketplace add " + codexMarketplaceSource,
		"codex plugin add " + codexPluginID,
	}
}

// initCodexLogic routes to the install, uninstall, or show handler.
func initCodexLogic(f *initFlags, format string, stdout, _ io.Writer) error {
	if f.project {
		return usageError("ktx init codex: --project is not supported; the plugin is installed for the user through Codex")
	}
	files, err := codexLegacyHookFiles()
	if err != nil {
		return internalError("ktx init codex: %v", err)
	}
	switch {
	case f.show:
		return initCodexShow(files, format, stdout)
	case f.uninstall:
		return initCodexUninstall(files, format, stdout)
	default:
		return initCodexInstall(files, format, stdout)
	}
}

// runCodex runs the codex CLI without a shell and returns its stdout. Codex
// writes warnings to stderr, so only stdout is parsed. A non-zero exit is an
// error that carries stderr.
func runCodex(codex string, args ...string) ([]byte, error) {
	command := exec.Command(codex, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return nil, fmt.Errorf("codex %s: %v: %s", strings.Join(args, " "), err, text)
		}
		return nil, fmt.Errorf("codex %s: %v", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// readCodexState reads the ktx marketplace and plugin from Codex.
func readCodexState(codex string) (codexState, error) {
	var state codexState
	raw, err := runCodex(codex, "plugin", "marketplace", "list", "--json")
	if err != nil {
		return state, err
	}
	var marketplaces struct {
		Marketplaces []codexMarketplace `json:"marketplaces"`
	}
	if err := json.Unmarshal(raw, &marketplaces); err != nil {
		return state, fmt.Errorf("parse codex plugin marketplace list: %w", err)
	}
	for _, marketplace := range marketplaces.Marketplaces {
		if marketplace.Name == codexMarketplaceName {
			state.marketplace = &marketplace
			break
		}
	}
	raw, err = runCodex(codex, "plugin", "list", "--json")
	if err != nil {
		return state, err
	}
	var plugins struct {
		Installed []codexInstalledPlugin `json:"installed"`
	}
	if err := json.Unmarshal(raw, &plugins); err != nil {
		return state, fmt.Errorf("parse codex plugin list: %w", err)
	}
	for _, plugin := range plugins.Installed {
		if plugin.PluginID == codexPluginID {
			state.plugin = &plugin
			break
		}
	}
	return state, nil
}

// describeCodexMarketplace names where a marketplace reads its plugins.
func describeCodexMarketplace(marketplace *codexMarketplace) string {
	if marketplace == nil {
		return "not added"
	}
	return strings.TrimSpace(marketplace.Source.SourceType + " " + marketplace.Source.Source)
}

// codexMarketplaceReport is the JSON form of a marketplace, or nil.
func codexMarketplaceReport(marketplace *codexMarketplace) any {
	if marketplace == nil {
		return nil
	}
	return map[string]any{
		"name":        marketplace.Name,
		"source_type": marketplace.Source.SourceType,
		"source":      marketplace.Source.Source,
	}
}

// codexPluginLayoutWarning reports when Codex installed ktx@ktx from a folder
// other than plugins/codex. Codex falls back to the Claude Code plugin layout
// when a marketplace has no Codex marketplace file, and that plugin's hook
// does not speak the Codex hook contract.
func codexPluginLayoutWarning(plugin *codexInstalledPlugin) string {
	if plugin == nil || plugin.Source.Path == "" {
		return ""
	}
	folder := filepath.Clean(plugin.Source.Path)
	if filepath.Base(folder) == "codex" && filepath.Base(filepath.Dir(folder)) == "plugins" {
		return ""
	}
	return fmt.Sprintf("Codex installed %s from %s, not from the Codex plugin folder plugins/codex; the marketplace has no Codex plugin, so remove it with `ktx init codex --uninstall` and point it at a ktx source that has one", codexPluginID, plugin.Source.Path)
}

// initCodexInstall adds the ktx marketplace and installs ktx@ktx unless Codex
// already has them, then removes ktx hooks an earlier ktx merged into hooks
// files. The hooks files are read before anything changes, so a malformed
// file stops install with Codex untouched.
func initCodexInstall(files []string, format string, stdout io.Writer) error {
	legacy, err := findCodexLegacyHooks(files)
	if err != nil {
		return internalError("ktx init codex: %v", err)
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		removed, err := removeCodexLegacyHooks(legacy)
		if err != nil {
			return internalError("ktx init codex: %v", err)
		}
		writeCodexManualInstall(stdout, format, removed)
		return nil
	}
	state, err := readCodexState(codex)
	if err != nil {
		return internalError("ktx init codex: %v", err)
	}
	addedMarketplace := state.marketplace == nil
	if addedMarketplace {
		if _, err := runCodex(codex, "plugin", "marketplace", "add", codexMarketplaceSource); err != nil {
			return internalError("ktx init codex: %v", err)
		}
	}
	installedPlugin := state.plugin == nil
	if installedPlugin {
		if _, err := runCodex(codex, "plugin", "add", codexPluginID); err != nil {
			return internalError("ktx init codex: %v", err)
		}
	}
	changed := addedMarketplace || installedPlugin
	if changed {
		if state, err = readCodexState(codex); err != nil {
			return internalError("ktx init codex: %v", err)
		}
	}
	// Without the plugin in Codex's list, or with a plugin from another
	// layout, nothing replaces the earlier hook, so stop before removing it.
	if state.plugin == nil {
		return internalError("ktx init codex: `codex plugin list` does not show %s after install; earlier ktx hooks are kept. Run `codex plugin list` to check, then run `ktx init codex` again", codexPluginID)
	}
	warning := codexPluginLayoutWarning(state.plugin)
	if warning != "" {
		return internalError("ktx init codex: %s", warning)
	}
	removed, err := removeCodexLegacyHooks(legacy)
	if err != nil {
		// Both hooks run once the plugin hook is trusted, so say what to remove.
		return internalError("ktx init codex: %s is installed, but %v; remove that ktx entry by hand so ktx does not run twice per tool call", codexPluginID, err)
	}
	enabled := state.plugin != nil && state.plugin.Enabled
	ktxPath := ktxOnPath()

	if format == FormatText {
		if changed {
			fmt.Fprintf(stdout, "ktx Codex plugin installed: %s\n", codexPluginID)
		} else {
			fmt.Fprintf(stdout, "ktx Codex plugin already installed: %s (no change)\n", codexPluginID)
		}
		fmt.Fprintf(stdout, "marketplace: %s (%s)\n", codexMarketplaceName, describeCodexMarketplace(state.marketplace))
		if state.plugin != nil && !enabled {
			fmt.Fprintf(stdout, "plugin is disabled; set enabled = true under [plugins.%q] in Codex's config.toml\n", codexPluginID)
		}
		writeCodexLegacyRemoval(stdout, removed)
		writeCodexKtxPath(stdout, ktxPath)
		fmt.Fprintf(stdout, "Note: %s\n", codexTrustNote)
		fmt.Fprintf(stdout, "troubleshooting: %s\n", codexTroubleshooting)
		return nil
	}
	payload := map[string]any{
		"installed":            true,
		"plugin":               codexPluginID,
		"enabled":              enabled,
		"marketplace":          codexMarketplaceReport(state.marketplace),
		"added_marketplace":    addedMarketplace,
		"installed_plugin":     installedPlugin,
		"removed_legacy_hooks": removed,
		"ktx_on_path":          ktxPath,
		"ktx_version":          version.Version,
		"note":                 codexTrustNote,
		"troubleshooting":      codexTroubleshooting,
		"runtime_activation":   "not_verified",
	}
	if !changed {
		payload["already_installed"] = true
	}
	_ = json.NewEncoder(stdout).Encode(payload)
	return nil
}

// writeCodexManualInstall prints the install commands when codex is not on
// PATH.
func writeCodexManualInstall(w io.Writer, format string, removed []codexLegacyHooks) {
	steps := codexInstallSteps()
	if format == FormatText {
		fmt.Fprintln(w, "codex not found on PATH; install the ktx plugin from Codex with:")
		for _, step := range steps {
			fmt.Fprintf(w, "  %s\n", step)
		}
		fmt.Fprintf(w, "Then %s.\n", codexTrustNote)
		writeCodexLegacyRemoval(w, removed)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"installed":            false,
		"reason":               "codex not found on PATH",
		"plugin":               codexPluginID,
		"next_steps":           steps,
		"note":                 codexTrustNote,
		"removed_legacy_hooks": removed,
	})
}

func writeCodexKtxPath(w io.Writer, path string) {
	if path == "" {
		fmt.Fprintln(w, "ktx on PATH: not found; the plugin runs the ktx on PATH, so add it before using Codex")
		return
	}
	fmt.Fprintf(w, "ktx on PATH: %s\n", path)
}

func writeCodexLegacyRemoval(w io.Writer, removed []codexLegacyHooks) {
	for _, file := range removed {
		fmt.Fprintf(w, "removed the earlier ktx hook from %s (%d %s)\n", file.Path, file.Entries, pluralEntry(file.Entries))
	}
}

// initCodexShow reports the ktx plugin as Codex lists it, and any hook an
// earlier ktx left in a hooks file. It exits 2 when ktx@ktx is not installed
// or codex is not on PATH.
func initCodexShow(files []string, format string, stdout io.Writer) error {
	legacy, err := findCodexLegacyHooks(files)
	if err != nil {
		return internalError("ktx init codex: %v", err)
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		return notFoundError("ktx init codex: codex not found on PATH; cannot inspect the ktx plugin%s", codexLegacyHint(legacy))
	}
	state, err := readCodexState(codex)
	if err != nil {
		return internalError("ktx init codex: %v", err)
	}
	if state.plugin == nil {
		return notFoundError("ktx init codex: %s is not installed; run `ktx init codex`%s", codexPluginID, codexLegacyHint(legacy))
	}
	warning := codexPluginLayoutWarning(state.plugin)
	ktxPath := ktxOnPath()
	if format == FormatText {
		fmt.Fprintf(stdout, "ktx Codex plugin: %s\n", codexPluginID)
		fmt.Fprintf(stdout, "enabled: %t\n", state.plugin.Enabled)
		fmt.Fprintf(stdout, "marketplace: %s (%s)\n", codexMarketplaceName, describeCodexMarketplace(state.marketplace))
		if warning != "" {
			fmt.Fprintf(stdout, "warning: %s\n", warning)
		}
		for _, file := range legacy {
			fmt.Fprintf(stdout, "earlier ktx hook: %s (%d %s); run `ktx init codex` to remove it\n", file.Path, file.Entries, pluralEntry(file.Entries))
		}
		writeCodexKtxPath(stdout, ktxPath)
		fmt.Fprintf(stdout, "this ktx: %s\n", version.Version)
		return nil
	}
	payload := map[string]any{
		"installed":          true,
		"plugin":             codexPluginID,
		"enabled":            state.plugin.Enabled,
		"marketplace":        codexMarketplaceReport(state.marketplace),
		"legacy_hooks":       legacy,
		"ktx_on_path":        ktxPath,
		"ktx_version":        version.Version,
		"runtime_activation": "not_verified",
	}
	if warning != "" {
		payload["warning"] = warning
	}
	_ = json.NewEncoder(stdout).Encode(payload)
	return nil
}

// codexLegacyHint names hooks files that still hold an earlier ktx hook, for
// not-found messages.
func codexLegacyHint(legacy []codexLegacyHooks) string {
	if len(legacy) == 0 {
		return ""
	}
	paths := make([]string, 0, len(legacy))
	for _, file := range legacy {
		paths = append(paths, file.Path)
	}
	return "; an earlier ktx hook is still merged into " + strings.Join(paths, " and ")
}

// initCodexUninstall removes ktx@ktx, the ktx marketplace, and hooks an
// earlier ktx merged into hooks files, whichever exist. It exits 2 when none
// does. Without codex on PATH it removes only the earlier hooks.
func initCodexUninstall(files []string, format string, stdout io.Writer) error {
	legacy, err := findCodexLegacyHooks(files)
	if err != nil {
		return internalError("ktx init codex: %v", err)
	}
	var state codexState
	codex, lookErr := exec.LookPath("codex")
	if lookErr == nil {
		if state, err = readCodexState(codex); err != nil {
			return internalError("ktx init codex: %v", err)
		}
	}
	manual := fmt.Sprintf("remove the plugin from Codex with `codex plugin remove %s` and `codex plugin marketplace remove %s`", codexPluginID, codexMarketplaceName)
	if state.plugin == nil && state.marketplace == nil && len(legacy) == 0 {
		if lookErr != nil {
			return notFoundError("ktx init codex: codex not found on PATH; %s", manual)
		}
		return notFoundError("ktx init codex: neither %s, the %s marketplace, nor an earlier ktx hook is installed", codexPluginID, codexMarketplaceName)
	}
	if state.plugin != nil {
		if _, err := runCodex(codex, "plugin", "remove", codexPluginID); err != nil {
			return internalError("ktx init codex: %v", err)
		}
	}
	if state.marketplace != nil {
		if _, err := runCodex(codex, "plugin", "marketplace", "remove", codexMarketplaceName); err != nil {
			return internalError("ktx init codex: %v", err)
		}
	}
	removed, err := removeCodexLegacyHooks(legacy)
	if err != nil {
		return internalError("ktx init codex: %v", err)
	}
	if format == FormatText {
		if state.plugin != nil {
			fmt.Fprintf(stdout, "ktx Codex plugin removed: %s\n", codexPluginID)
		}
		if state.marketplace != nil {
			fmt.Fprintf(stdout, "marketplace removed: %s\n", codexMarketplaceName)
		}
		writeCodexLegacyRemoval(stdout, removed)
		if lookErr != nil {
			fmt.Fprintf(stdout, "codex not found on PATH; %s\n", manual)
		}
		return nil
	}
	payload := map[string]any{
		"uninstalled":          true,
		"plugin":               codexPluginID,
		"removed_plugin":       state.plugin != nil,
		"removed_marketplace":  state.marketplace != nil,
		"removed_legacy_hooks": removed,
	}
	if lookErr != nil {
		payload["note"] = "codex not found on PATH; " + manual
	}
	_ = json.NewEncoder(stdout).Encode(payload)
	return nil
}

// codexLegacyHookFiles returns the hooks files earlier ktx versions wrote:
// ~/.codex/hooks.json and ./.codex/hooks.json. ~ is resolved through
// os.UserHomeDir because that is where those versions wrote, whatever
// CODEX_HOME says.
func codexLegacyHookFiles() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory: %w", err)
	}
	files := []string{filepath.Join(home, ".codex", codexHooksFileName)}
	if cwd, err := os.Getwd(); err == nil {
		if project := filepath.Join(cwd, ".codex", codexHooksFileName); project != files[0] {
			files = append(files, project)
		}
	}
	return files, nil
}

// findCodexLegacyHooks reads each hooks file and counts its ktx entries.
// Missing files are skipped; unreadable or malformed ones are errors, so
// callers stop before changing anything.
func findCodexLegacyHooks(files []string) ([]codexLegacyHooks, error) {
	found := []codexLegacyHooks{}
	for _, path := range files {
		doc, exists, err := loadCodexHooks(path)
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", path, err)
		}
		if !exists {
			continue
		}
		if _, count := filterKtxEntries(codexPostToolUseArray(codexHooksObject(doc))); count > 0 {
			found = append(found, codexLegacyHooks{Path: path, Entries: count})
		}
	}
	return found, nil
}

// removeCodexLegacyHooks removes the ktx entries from each file in legacy
// and returns what it removed.
func removeCodexLegacyHooks(legacy []codexLegacyHooks) ([]codexLegacyHooks, error) {
	removed := []codexLegacyHooks{}
	for _, file := range legacy {
		count, err := removeCodexLegacyHook(file.Path)
		if err != nil {
			return removed, fmt.Errorf("remove the earlier ktx hook from %q: %w", file.Path, err)
		}
		if count > 0 {
			removed = append(removed, codexLegacyHooks{Path: file.Path, Entries: count})
		}
	}
	return removed, nil
}

// removeCodexLegacyHook removes ktx's inner PostToolUse hooks from the hooks
// file at target and returns how many it removed. Sibling hooks that share a
// matcher entry stay.
//
// When removal empties the PostToolUse array, the key is dropped; when the
// hooks object becomes empty, it is dropped; and when nothing is left in the
// document, the file is deleted.
//
// The mutation holds a directory lease across read, filter and checked
// publication, and keeps the file's permissions.
func removeCodexLegacyHook(target string) (int, error) {
	mutation, err := beginCodexMutation(target, false)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer mutation.close()
	doc, exists, err := decodeCodexHooks(mutation.raw, mutation.info != nil)
	if err != nil || !exists {
		return 0, err
	}
	hooksMap := codexHooksObject(doc)
	filtered, removed := filterKtxEntries(codexPostToolUseArray(hooksMap))
	if removed == 0 {
		return 0, nil
	}
	if len(filtered) == 0 {
		delete(hooksMap, codexPostToolUseKey)
	} else {
		hooksMap[codexPostToolUseKey] = filtered
	}
	if len(hooksMap) == 0 {
		delete(doc, "hooks")
	}
	if len(doc) == 0 {
		if err := mutation.remove(); err != nil {
			return 0, fmt.Errorf("remove empty file: %w", err)
		}
		return removed, nil
	}
	// Other tools' shell commands stay readable: no \u0026 for & and the like.
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return 0, fmt.Errorf("marshal: %w", err)
	}
	if err := mutation.publish(out.Bytes()); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}
	return removed, nil
}

// loadCodexHooks reads and parses the hooks file at path into a generic map,
// so unrelated hook categories survive without schema changes here. exists is
// false when the file is absent.
func loadCodexHooks(path string) (map[string]any, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return decodeCodexHooks(raw, true)
}

func decodeCodexHooks(raw []byte, exists bool) (map[string]any, bool, error) {
	if !exists {
		return nil, false, nil
	}
	var doc map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, false, fmt.Errorf("parse hooks.json: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, false, fmt.Errorf("parse hooks.json: trailing data")
	}
	if doc == nil {
		return nil, false, fmt.Errorf("hooks.json must be an object")
	}
	if value, exists := doc["hooks"]; exists {
		hooks, ok := value.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("hooks must be an object")
		}
		if value, exists := hooks[codexPostToolUseKey]; exists {
			if _, ok := value.([]any); !ok {
				return nil, false, fmt.Errorf("PostToolUse must be an array")
			}
		}
	}
	return doc, true, nil
}

// codexHooksObject returns the "hooks" object from doc, or an empty map when
// absent. The document reader rejects a present non-object value.
func codexHooksObject(doc map[string]any) map[string]any {
	m, ok := doc["hooks"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return m
}

// codexPostToolUseArray returns the PostToolUse array from hooksMap, or nil.
func codexPostToolUseArray(hooksMap map[string]any) []any {
	arr, _ := hooksMap[codexPostToolUseKey].([]any)
	return arr
}

// isKtxCodexHook reports whether an inner hook object is one an earlier
// `ktx init codex` generated. Each version wrote a command hook with ktx's
// status message and one of three commands:
//
//   - the bare `ktx hook`, with a timeout in the first versions;
//   - a shell-quoted canonical absolute path followed by
//     ` --expect-runtime-sha256 <digest> hook`;
//   - a shell-quoted canonical absolute path followed by ` hook`.
//
// Any other field, or any other command, is user-owned and stays.
func isKtxCodexHook(hook map[string]any) bool {
	if hook["type"] != "command" || hook["statusMessage"] != ktxHookStatusMessage {
		return false
	}
	command, _ := hook["command"].(string)
	switch len(hook) {
	case 3:
	case 4:
		if _, ok := hook["timeout"].(json.Number); !ok || command != ktxHookCommand {
			return false
		}
	default:
		return false
	}
	if command == ktxHookCommand {
		return true
	}
	rest, ok := strings.CutSuffix(command, " hook")
	if !ok {
		return false
	}
	if quoted, digest, found := strings.Cut(rest, " --expect-runtime-sha256 "); found {
		if !isSHA256Hex(digest) {
			return false
		}
		rest = quoted
	}
	if !strings.HasPrefix(rest, "'") || !strings.HasSuffix(rest, "'") || len(rest) < 2 {
		return false
	}
	path := strings.ReplaceAll(rest[1:len(rest)-1], `'"'"'`, `'`)
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsRune(path, 0) && quoteCodexHookPath(path) == rest
}

// quoteCodexHookPath single-quotes path for a POSIX shell, as earlier
// versions did when they pinned the hook to an executable.
func quoteCodexHookPath(path string) string {
	return `'` + strings.ReplaceAll(path, `'`, `'"'"'`) + `'`
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// filterKtxEntries returns a copy of arr with ktx's inner hook objects
// removed, along with the count removed. An outer matcher entry is kept
// when it still contains another hook and removed only when no hooks remain
// and it carries nothing but ktx's matcher.
func filterKtxEntries(arr []any) ([]any, int) {
	if len(arr) == 0 {
		return nil, 0
	}
	out := make([]any, 0, len(arr))
	removed := 0
	for _, entry := range arr {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			out = append(out, entry)
			continue
		}
		inner, ok := entryMap["hooks"].([]any)
		if !ok {
			out = append(out, entry)
			continue
		}
		filtered := make([]any, 0, len(inner))
		removedFromEntry := 0
		for _, hook := range inner {
			hookMap, ok := hook.(map[string]any)
			if ok && isKtxCodexHook(hookMap) {
				removed++
				removedFromEntry++
				continue
			}
			filtered = append(filtered, hook)
		}
		if removedFromEntry == 0 {
			out = append(out, entry)
			continue
		}
		if len(filtered) == 0 && len(entryMap) == 2 && entryMap["matcher"] == ".*" {
			continue
		}
		updated := make(map[string]any, len(entryMap))
		for key, value := range entryMap {
			updated[key] = value
		}
		updated["hooks"] = filtered
		out = append(out, updated)
	}
	return out, removed
}

// pluralEntry returns "entry" when n == 1, "entries" otherwise.
func pluralEntry(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}
