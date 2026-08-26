// `ktx init codex` adapter — merges a PostToolUse command hook into
// Codex's shared hooks.json file.
//
// Unlike OpenCode, where `ktx init opencode` writes a standalone
// TypeScript plugin file that ktx owns entirely, Codex loads all of
// its hooks from a single shared JSON document (~/.codex/hooks.json
// at the global scope or ./.codex/hooks.json at the project scope).
// Other sources may have already written SessionStart, PreToolUse,
// or PostToolUse entries the user depends on. Install therefore
// parses the existing file, appends ktx's PostToolUse entry only
// when it is not already present, and writes the merged document
// back. Uninstall removes only generated ktx command entries
// and leaves the rest of the file alone.
//
// Identification accepts the generated pinned command and the older literal
// "ktx hook" form so reinstall replaces stale entries and uninstall remains
// compatible with existing installations.
//
// Trust model: Codex does not execute a hook until the user has
// reviewed and trusted it via the /hooks command inside the Codex
// session. The install output reminds the user to complete that
// step — silently firing hooks would be surprising, and a freshly
// installed hook that "does nothing" looks like a bug.
//
// Exit codes follow the rest of the init subcommand: 0 on success
// (install, idempotent install, uninstall of a present entry, show
// found), 2 on not-found (--show saw no ktx hook, --uninstall had
// no ktx entry to remove), 1 on usage error, 4 on internal error.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// codexHooksFileName is the on-disk file Codex reads its hooks from.
// Codex's loader expects exactly this name; renaming the file makes
// the hooks invisible to the runtime.
const codexHooksFileName = "hooks.json"

// ktxHookCommand is the older unpinned command and the stable logical command
// shown in reports. New installations write a pinned absolute command.
const ktxHookCommand = "ktx hook"

// ktxHookStatusMessage is the inline status Codex surfaces while the
// hook is running. It documents the side-effect (ktx recording the
// tool call) so users running Codex interactively can attribute the
// brief execution delay.
const ktxHookStatusMessage = "ktx: recording tool call"

// codexPostToolUseKey is the JSON object key under "hooks" where the
// ktx PostToolUse entry is stored. Held as a constant because both
// the install writer and the detect/remove readers must agree on it.
const codexPostToolUseKey = "PostToolUse"

// codexTrustReviewNote is appended to the install report. Codex will
// not fire the hook until the user runs /hooks and trusts it, so a
// fresh install that "does nothing" is the expected pre-trust state
// and needs to be called out.
const codexTrustReviewNote = "run /hooks in Codex and trust the ktx hook before it fires — untrusted hooks do not execute"

// initCodexLogic routes to the install/uninstall/show handler for the
// Codex adapter. It resolves the candidate hooks.json paths once and
// passes them down so the handlers do not repeat path logic.
//
// Scope rules match the rest of `ktx init`:
//   - install: --project scopes to the project file; otherwise global.
//   - uninstall: --project scopes to the project file; otherwise
//     global. Unlike the OpenCode uninstall (which removes from both
//     scopes in one invocation), Codex uninstall is scoped so a
//     global cleanup does not silently wipe a project-local entry
//     the user might be experimenting with.
//   - show: always inspects both scopes so the user gets the full
//     picture regardless of where they are scoped.
//
// Returns the typed error to surface (nil on success).
func initCodexLogic(f *initFlags, format string, stdout, stderr io.Writer) error {
	globalPath, err := codexGlobalHooksPath()
	if err != nil {
		return internalError("ktx init codex: resolve global hooks path: %v", err)
	}
	projectPath := codexProjectHooksPath()

	switch {
	case f.show:
		return initCodexShow(globalPath, projectPath, format, stdout, stderr)
	case f.uninstall:
		target := globalPath
		if f.project {
			target = projectPath
		}
		return initCodexUninstall(target, format, stdout, stderr)
	default:
		target := globalPath
		if f.project {
			target = projectPath
		}
		return initCodexInstall(target, format, stdout)
	}
}

// codexGlobalHooksPath returns ~/.codex/hooks.json. ~ is resolved via
// os.UserHomeDir rather than $HOME so the lookup behaves correctly on
// systems where $HOME is unset but the user database is populated.
// Codex's home config layout predates XDG, so there is no
// XDG_CONFIG_HOME fallback to honor.
func codexGlobalHooksPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".codex", codexHooksFileName), nil
}

// codexProjectHooksPath returns the project-scoped hooks.json path,
// always relative to the current working directory. The CWD is
// captured at call time rather than process start so a test or script
// that changes directories between calls sees the new location.
func codexProjectHooksPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Join(".", ".codex", codexHooksFileName)
	}
	return filepath.Join(cwd, ".codex", codexHooksFileName)
}

// initCodexInstall merges ktx's PostToolUse entry into the hooks.json
// at target. When the file does not exist, a fresh document is
// created. When the file exists and already contains an entry whose
// inner hooks include the exact current pinned command, install is idempotent:
// the file is left untouched on disk and the report is marked
// already-installed so the caller can distinguish a no-op from a
// fresh write.
//
// The merge is deliberately additive: only the PostToolUse array is
// appended to. SessionStart, PreToolUse, and any other Codex hook
// categories the user has configured are preserved verbatim. Because
// Go's json.Marshal sorts map keys alphabetically, the on-disk key
// order of pre-existing entries may shift on a fresh write — but the
// content is preserved and Codex reads by key, not by position.
//
// The parent directory is created with os.MkdirAll so first-time
// installs in a fresh ~/.codex tree succeed without a separate setup
// step. The file is written with 0644 — readable by the agent
// process, writable by the user.
func initCodexInstall(target, format string, stdout io.Writer) error {
	command, err := codexPinnedHookCommand()
	if err != nil {
		return err
	}
	doc, exists, err := loadCodexHooks(target)
	if err != nil {
		return internalError("ktx init codex: read %q: %v", target, err)
	}
	if !exists {
		doc = map[string]any{}
	}

	hooksMap := codexHooksObject(doc)
	postToolUse := codexPostToolUseArray(hooksMap)

	if codexArrayHasCommand(postToolUse, command) {
		writeCodexInstallReport(stdout, target, format, true)
		return nil
	}

	postToolUse, _ = filterKtxEntries(postToolUse)
	postToolUse = append(postToolUse, ktxCodexPostToolUseEntry(command))
	hooksMap[codexPostToolUseKey] = postToolUse
	doc["hooks"] = hooksMap

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return internalError("ktx init codex: create directory %q: %v", filepath.Dir(target), err)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return internalError("ktx init codex: marshal %q: %v", target, err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(target, out, 0o644); err != nil {
		return internalError("ktx init codex: write %q: %v", target, err)
	}

	writeCodexInstallReport(stdout, target, format, false)
	return nil
}

// initCodexUninstall removes ktx's inner PostToolUse hooks from the
// hooks.json at target. Install and uninstall use the same command
// predicate, so they agree on identity without removing sibling hooks
// that share the same matcher entry.
//
// When removing ktx entries empties the PostToolUse array, the
// PostToolUse key is dropped from the hooks object so the file does
// not carry a dangling empty array. When the hooks object itself
// becomes empty (no other hook categories present), the file is
// deleted entirely — an empty hooks.json is dead weight and a fresh
// install will recreate it on demand. Non-empty hooks objects are
// written back with the remaining entries preserved.
//
// Exit codes: 0 when at least one ktx entry was removed; 2 when the
// file does not exist or contained no ktx entry (nothing to do).
func initCodexUninstall(target, format string, stdout, stderr io.Writer) error {
	doc, exists, err := loadCodexHooks(target)
	if err != nil {
		return internalError("ktx init codex: read %q: %v", target, err)
	}
	if !exists {
		printError(stderr, notFoundError("ktx init codex: no ktx hook in %q (file not found)", target), format)
		return notFoundError("ktx init codex: no ktx hook in %q (file not found)", target)
	}

	hooksMap := codexHooksObject(doc)
	postToolUse := codexPostToolUseArray(hooksMap)

	filtered, removed := filterKtxEntries(postToolUse)
	if removed == 0 {
		printError(stderr, notFoundError("ktx init codex: no ktx hook entry in %q", target), format)
		return notFoundError("ktx init codex: no ktx hook entry in %q", target)
	}

	if len(filtered) == 0 {
		delete(hooksMap, codexPostToolUseKey)
	} else {
		hooksMap[codexPostToolUseKey] = filtered
	}

	if len(hooksMap) == 0 {
		delete(doc, "hooks")
	}

	if len(doc) == 0 || (len(doc) == 1 && doc["hooks"] == nil) {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return internalError("ktx init codex: remove empty %q: %v", target, err)
		}
		writeCodexUninstallReport(stdout, target, format, removed)
		return nil
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return internalError("ktx init codex: marshal %q: %v", target, err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(target, out, 0o644); err != nil {
		return internalError("ktx init codex: write %q: %v", target, err)
	}

	writeCodexUninstallReport(stdout, target, format, removed)
	return nil
}

// initCodexShow reports whether ktx's PostToolUse entry is present in
// the global hooks.json, the project hooks.json, or both. Both files
// are always inspected regardless of --project so the user sees the
// complete installed state in one command.
//
// Exit codes: 0 when at least one scope has ktx's entry; 2 when
// neither has it. The 2 exit code is the standard "not-found" class
// so scripts can branch on it. A scope whose file exists but lacks
// ktx's entry counts as not-found for that scope.
func initCodexShow(globalPath, projectPath, format string, stdout, stderr io.Writer) error {
	hasGlobal, err := codexFileHasKtxHook(globalPath)
	if err != nil {
		return internalError("ktx init codex: inspect %q: %v", globalPath, err)
	}
	hasProject, err := codexFileHasKtxHook(projectPath)
	if err != nil {
		return internalError("ktx init codex: inspect %q: %v", projectPath, err)
	}

	if !hasGlobal && !hasProject {
		return notFoundError("ktx init codex: ktx hook not installed (checked global %q and project %q)", globalPath, projectPath)
	}

	writeCodexShowReport(stdout, globalPath, projectPath, hasGlobal, hasProject, format)
	return nil
}

// loadCodexHooks reads and parses the hooks.json at path into a
// generic map (the on-disk shape is intentionally loose so unrelated
// Codex hook categories the user may have added are preserved without
// schema changes here). The exists flag is false when the file is
// absent so callers can distinguish "not installed" from "empty file"
// — an empty file is treated as an existing (if unusual) hooks.json.
func loadCodexHooks(path string) (map[string]any, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("parse hooks.json: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, true, nil
}

// codexHooksObject returns the "hooks" object from doc, initializing
// an empty map when absent so callers can append without nil checks.
// When the existing value is not an object (a malformed file), an
// empty map is returned — the installer will overwrite the bad value
// with a well-formed hooks object rather than crash.
func codexHooksObject(doc map[string]any) map[string]any {
	v, ok := doc["hooks"]
	if !ok || v == nil {
		return map[string]any{}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return m
}

// codexPostToolUseArray returns the existing PostToolUse array from
// hooksMap, or nil when absent. Callers treat nil as "empty, will be
// created on append".
func codexPostToolUseArray(hooksMap map[string]any) []any {
	v, ok := hooksMap[codexPostToolUseKey]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	return arr
}

// codexArrayHasKtx reports whether any entry in arr is ktx's, per the
// command-string predicate. A PostToolUse entry is ktx's when its
// inner "hooks" array contains any hook object whose "command" field
// equals ktxHookCommand. Non-map entries and missing fields are
// skipped rather than treated as errors — a malformed sibling entry
// from another source should not block install or removal.
func codexArrayHasKtx(arr []any) bool {
	for _, entry := range arr {
		if codexEntryIsKtx(entry) {
			return true
		}
	}
	return false
}

func codexArrayHasCommand(arr []any, command string) bool {
	for _, entry := range arr {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := m["hooks"].([]any)
		for _, hook := range inner {
			hookMap, ok := hook.(map[string]any)
			if ok && hookMap["command"] == command {
				return true
			}
		}
	}
	return false
}

// codexEntryIsKtx reports whether a single PostToolUse entry is ktx's.
// The entry is expected to be a map with a "hooks" array of inner
// hook objects, but the function defensively handles non-map entries
// and missing keys so a malformed sibling entry does not interfere
// with detection.
func codexEntryIsKtx(entry any) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	inner, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); isKtxHookCommand(cmd) {
			return true
		}
	}
	return false
}

func isKtxHookCommand(command string) bool {
	expected, err := codexPinnedHookCommand()
	return err == nil && command == expected
}

func codexPinnedHookCommand() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", internalError("ktx init codex: resolve executable path: %v", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", internalError("ktx init codex: canonicalize executable path: %v", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", internalError("ktx init codex: make executable path absolute: %v", err)
	}
	return `'` + strings.ReplaceAll(filepath.Clean(path), `'`, `'"'"'`) + `' hook`, nil
}

// codexFileHasKtxHook reports whether the hooks.json at path exists
// and contains ktx's PostToolUse entry. Missing files are absent; read
// and parse failures are returned so callers do not replace the binary
// after an incomplete inspection.
func codexFileHasKtxHook(path string) (bool, error) {
	doc, exists, err := loadCodexHooks(path)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	hooksMap := codexHooksObject(doc)
	return codexArrayHasKtx(codexPostToolUseArray(hooksMap)), nil
}

// filterKtxEntries returns a copy of arr with ktx's inner hook objects
// removed, along with the count removed. An outer matcher entry is kept
// when it still contains another hook and removed only when no hooks remain.
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
			command, _ := hookMap["command"].(string)
			if ok && isKtxHookCommand(command) {
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
		if len(filtered) == 0 {
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

// ktxCodexPostToolUseEntry builds the ktx PostToolUse entry to append
// to the hooks.json array. The shape mirrors Codex's documented
// PostToolUse schema:
//
//	{
//	  "matcher": ".*",
//	  "hooks": [
//	    {"type":"command","command":"'<canonical ktx path>' hook",
//	     "statusMessage":"ktx: recording tool call"}
//	  ]
//	}
//
// The matcher ".*" runs the hook on every tool call; ktx's noop guard
// inside `ktx hook` decides whether to persist. Returned as
// map[string]any so json.MarshalIndent produces the same key order as
// a hand-written entry would (alphabetical, since Go sorts map keys).
func ktxCodexPostToolUseEntry(command string) map[string]any {
	return map[string]any{
		"matcher": ".*",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       command,
				"statusMessage": ktxHookStatusMessage,
			},
		},
	}
}

// writeCodexInstallReport emits the install confirmation in the
// requested format. The trust-review note is always included: Codex
// will not fire the hook until the user has trusted it via /hooks,
// so a user who skips that step will see "installed" but no events
// recorded — calling that out at install time prevents a confusing
// follow-up bug report.
//
// When alreadyInstalled is true, the file was not rewritten (the
// entry was already present). The report distinguishes this from a
// fresh install so scripts that diff stdout can tell whether the
// install actually wrote.
func writeCodexInstallReport(w io.Writer, target, format string, alreadyInstalled bool) {
	if format == FormatText {
		if alreadyInstalled {
			fmt.Fprintf(w, "ktx codex hook already installed at %s (no change)\n", target)
		} else {
			fmt.Fprintf(w, "ktx codex hook installed at %s\n", target)
		}
		fmt.Fprintf(w, "  Note: %s\n", codexTrustReviewNote)
		return
	}
	payload := map[string]any{
		"installed": true,
		"path":      target,
		"note":      codexTrustReviewNote,
	}
	if alreadyInstalled {
		payload["already_installed"] = true
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// writeCodexUninstallReport emits the uninstall confirmation. removed
// is the count of ktx entries stripped from the PostToolUse array;
// under normal use it is 1, but a user who hand-edited the file could
// have duplicates, and the count surfaces that without failing.
func writeCodexUninstallReport(w io.Writer, target, format string, removed int) {
	if format == FormatText {
		fmt.Fprintf(w, "ktx codex hook removed from %s (%d entr%s)\n", target, removed, pluralEntry(removed))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"uninstalled": true,
		"path":        target,
		"removed":     removed,
	})
}

// writeCodexShowReport emits the --show result. Both scopes are
// reported independently so the user can see the full installed
// state; the primary path (global by default, falling back to
// project) is listed first.
func writeCodexShowReport(w io.Writer, globalPath, projectPath string, hasGlobal, hasProject bool, format string) {
	if format == FormatText {
		if hasGlobal {
			fmt.Fprintf(w, "ktx codex hook installed at %s (global), command: %s\n", globalPath, ktxHookCommand)
		}
		if hasProject {
			fmt.Fprintf(w, "ktx codex hook installed at %s (project), command: %s\n", projectPath, ktxHookCommand)
		}
		return
	}
	payload := map[string]any{
		"installed": true,
		"command":   ktxHookCommand,
		"scopes":    []string{},
	}
	scopes := []string{}
	if hasGlobal {
		scopes = append(scopes, "global")
		payload["global_path"] = globalPath
	}
	if hasProject {
		scopes = append(scopes, "project")
		payload["project_path"] = projectPath
	}
	payload["scopes"] = scopes
	_ = json.NewEncoder(w).Encode(payload)
}

// pluralEntry returns "entry" when n == 1, "entries" otherwise.
// Trivial helper kept local so the uninstall report's pluralization
// stays consistent without a wider utility.
func pluralEntry(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}
