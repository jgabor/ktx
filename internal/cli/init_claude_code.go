// `ktx init claude` generates a local Claude Code plugin marketplace.
//
// ktx never edits Claude Code settings. Instead it writes a marketplace folder
// under $XDG_DATA_HOME/ktx/claude-code/marketplace that the user registers once
// with `claude plugin marketplace add` and `claude plugin install`. Claude Code
// reads relative-path plugins from that folder itself, so regenerating the
// files and running /reload-plugins picks up a refreshed plugin.
//
// The ktx plugin declares one PostToolUse command hook for the Bash tool. The
// hook runs the installing executable's canonical path in exec form, with the
// arguments `hook --runtime claude-code`, and sets no timeout. The ktx-stats
// mod polls `ktx stats summary` through the same pinned path and shows a band
// above the prompt and a /ktx pane.
//
// A marker file, .ktx-generated.json, records the template revision, the
// pinned executable, and every generated file. --show reads only the marker
// and the files it lists; --uninstall removes only those files plus the type
// folders Claude Code writes into loaded plugins, and leaves anything else.
//
// Exit codes follow the rest of `ktx init`: 0 on success, 1 on usage error,
// 2 when nothing is generated (--show, --uninstall), 4 on internal error.
package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgabor/ktx/internal/cli/claudecode"
	"github.com/jgabor/ktx/internal/durablefile"
)

const (
	claudeCodeMarketplaceName  = "ktx-local"
	claudeCodeMarkerFile       = ".ktx-generated.json"
	claudeCodeMarkerSchema     = 1
	claudeCodeRefreshCommand   = "ktx init claude"
	claudeCodeHookStatus       = "ktx: compressing tool output"
	claudeCodeReloadNote       = "after regenerating, run /reload-plugins in Claude Code (or restart it)"
	claudeCodeTroubleshooting  = "docs/guides/claude-code.md#troubleshooting"
	claudeCodeRemoveCommand    = "claude plugin marketplace remove " + claudeCodeMarketplaceName
	claudeCodeTypesFolder      = ".claude-plugin/types"
	claudeCodeRevisionExecPath = "/ktx"
)

// claudeCodeExecutablePlaceholder is the JSON string literal in plugin
// sources that generation replaces with the pinned executable path.
const claudeCodeExecutablePlaceholder = `"__KTX_EXECUTABLE__"`

// claudeCodeHookArgs is the argument vector the generated hook passes to ktx.
var claudeCodeHookArgs = []string{"hook", "--runtime", "claude-code"}

// claudeCodePlugin describes one plugin in the generated marketplace.
// templates are paths relative to the plugin folder, read from
// claudecode.Files under plugins/<name>/.
type claudeCodePlugin struct {
	name        string
	description string
	role        string
	templates   []string
}

var claudeCodePlugins = []claudeCodePlugin{
	{
		name:        "ktx",
		description: "Compresses Bash tool output through ktx before Claude reads it.",
		role:        "Bash output compression",
		templates:   []string{".claude-plugin/plugin.json"},
	},
	{
		name:        "ktx-stats",
		description: "Shows ktx compression stats in a band above the prompt and a /ktx pane.",
		role:        "KPI band and /ktx pane",
		templates: []string{
			".claude-plugin/plugin.json",
			"hooks/hooks.json",
			"hooks/register.tsx",
			"hooks/stats.ts",
			"types/index.d.ts",
		},
	},
}

// claudeCodeExecutable resolves the canonical path the generated files pin.
// Tests replace it to exercise unusual paths.
var claudeCodeExecutable = func() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", internalError("ktx init claude: resolve executable path: %v", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", internalError("ktx init claude: canonicalize executable path: %v", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", internalError("ktx init claude: make executable path absolute: %v", err)
	}
	return filepath.Clean(path), nil
}

type claudeCodeFile struct {
	path string
	data []byte
}

type claudeCodeMarker struct {
	SchemaVersion int      `json:"schema_version"`
	Generator     string   `json:"generator"`
	Revision      string   `json:"revision"`
	KtxExecutable string   `json:"ktx_executable"`
	Files         []string `json:"files"`
}

type claudeCodeMarketplaceOwner struct {
	Name string `json:"name"`
}

type claudeCodeMarketplaceEntry struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description"`
}

type claudeCodeMarketplaceDoc struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Owner       claudeCodeMarketplaceOwner   `json:"owner"`
	Plugins     []claudeCodeMarketplaceEntry `json:"plugins"`
}

type claudeCodeCommandHook struct {
	Type          string   `json:"type"`
	Command       string   `json:"command"`
	Args          []string `json:"args"`
	StatusMessage string   `json:"statusMessage"`
}

type claudeCodeHookMatcher struct {
	Matcher string                  `json:"matcher"`
	Hooks   []claudeCodeCommandHook `json:"hooks"`
}

type claudeCodeHooksDoc struct {
	Description string                             `json:"description"`
	Hooks       map[string][]claudeCodeHookMatcher `json:"hooks"`
}

// initClaudeCodeLogic routes to the install, uninstall, or show handler.
func initClaudeCodeLogic(f *initFlags, format string, stdout, stderr io.Writer) error {
	if f.project {
		return usageError("ktx init claude: --project is not supported; the plugins are generated once under $XDG_DATA_HOME/ktx/claude-code and installed through Claude Code")
	}
	root, err := claudeCodeMarketplaceRoot()
	if err != nil {
		return internalError("ktx init claude: resolve marketplace folder: %v", err)
	}
	switch {
	case f.show:
		return initClaudeCodeShow(root, format, stdout)
	case f.uninstall:
		return initClaudeCodeUninstall(root, format, stdout, stderr)
	default:
		return initClaudeCodeInstall(root, format, stdout)
	}
}

// claudeCodeMarketplaceRoot returns $XDG_DATA_HOME/ktx/claude-code/marketplace,
// with the XDG default of ~/.local/share when XDG_DATA_HOME is unset.
func claudeCodeMarketplaceRoot() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "ktx", "claude-code", "marketplace"), nil
}

// renderClaudeCodeFiles returns every generated file for the given pinned
// executable, sorted by path. The marker is not included.
func renderClaudeCodeFiles(executable string) ([]claudeCodeFile, error) {
	literal, err := marshalClaudeCodeJSON(executable, "")
	if err != nil {
		return nil, err
	}
	literal = bytes.TrimSuffix(literal, []byte("\n"))

	marketplace := claudeCodeMarketplaceDoc{
		Name:        claudeCodeMarketplaceName,
		Description: "Plugins generated by `ktx init claude`.",
		Owner:       claudeCodeMarketplaceOwner{Name: "ktx"},
	}
	files := []claudeCodeFile{}
	for _, plugin := range claudeCodePlugins {
		marketplace.Plugins = append(marketplace.Plugins, claudeCodeMarketplaceEntry{
			Name:        plugin.name,
			Source:      "./plugins/" + plugin.name,
			Description: plugin.description,
		})
		for _, template := range plugin.templates {
			data, err := claudecode.Files.ReadFile(path.Join("plugins", plugin.name, template))
			if err != nil {
				return nil, err
			}
			data = bytes.ReplaceAll(data, []byte(claudeCodeExecutablePlaceholder), literal)
			files = append(files, claudeCodeFile{path: path.Join("plugins", plugin.name, template), data: data})
		}
	}
	hooks, err := marshalClaudeCodeJSON(claudeCodeHooksDoc{
		Description: "ktx compresses Bash tool output before Claude reads it.",
		Hooks: map[string][]claudeCodeHookMatcher{
			"PostToolUse": {{
				Matcher: "Bash",
				Hooks: []claudeCodeCommandHook{{
					Type:          "command",
					Command:       executable,
					Args:          claudeCodeHookArgs,
					StatusMessage: claudeCodeHookStatus,
				}},
			}},
		},
	}, "  ")
	if err != nil {
		return nil, err
	}
	files = append(files, claudeCodeFile{path: "plugins/ktx/hooks/hooks.json", data: hooks})
	manifest, err := marshalClaudeCodeJSON(marketplace, "  ")
	if err != nil {
		return nil, err
	}
	files = append(files, claudeCodeFile{path: ".claude-plugin/marketplace.json", data: manifest})
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

// marshalClaudeCodeJSON encodes value without HTML escaping, ending in a newline.
func marshalClaudeCodeJSON(value any, indent string) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", indent)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// claudeCodeRevision identifies the generated sources independent of the
// pinned executable, so a binary at another path does not change it.
func claudeCodeRevision() (string, error) {
	files, err := renderClaudeCodeFiles(claudeCodeRevisionExecPath)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, file := range files {
		fmt.Fprintf(hash, "%s\x00%d\x00", file.path, len(file.data))
		hash.Write(file.data)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func claudeCodePluginNames() []string {
	names := make([]string, 0, len(claudeCodePlugins))
	for _, plugin := range claudeCodePlugins {
		names = append(names, plugin.name)
	}
	return names
}

// claudeCodeNextSteps lists the commands that register the marketplace and
// install each plugin in Claude Code.
func claudeCodeNextSteps(root string) []string {
	steps := []string{"claude plugin marketplace add " + claudeCodeShellWord(root)}
	for _, name := range claudeCodePluginNames() {
		steps = append(steps, "claude plugin install "+name+"@"+claudeCodeMarketplaceName)
	}
	return steps
}

// claudeCodeShellWord quotes s for a POSIX shell when it holds anything
// beyond a conservative set of safe characters.
func claudeCodeShellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return `'` + strings.ReplaceAll(s, `'`, `'"'"'`) + `'`
}

// readClaudeCodeMarker loads the marker. A missing marker reports exists false;
// an unreadable or malformed marker is an error so no action proceeds on
// guessed ownership.
func readClaudeCodeMarker(root string) (claudeCodeMarker, bool, error) {
	var marker claudeCodeMarker
	raw, err := os.ReadFile(filepath.Join(root, claudeCodeMarkerFile))
	if errors.Is(err, fs.ErrNotExist) {
		return marker, false, nil
	}
	if err != nil {
		return marker, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&marker); err != nil {
		return marker, false, fmt.Errorf("parse %s: %w", claudeCodeMarkerFile, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return marker, false, fmt.Errorf("parse %s: trailing data", claudeCodeMarkerFile)
	}
	if marker.SchemaVersion != claudeCodeMarkerSchema {
		return marker, false, fmt.Errorf("%s: unsupported schema_version %d", claudeCodeMarkerFile, marker.SchemaVersion)
	}
	for _, file := range marker.Files {
		if !claudeCodeOwnedPath(file) {
			return marker, false, fmt.Errorf("%s: file %q is not a generated path", claudeCodeMarkerFile, file)
		}
	}
	return marker, true, nil
}

// claudeCodeOwnedPath reports whether a marker entry names a file inside the
// marketplace folder in the generated layout.
func claudeCodeOwnedPath(name string) bool {
	if name == "" || name == claudeCodeMarkerFile || !filepath.IsLocal(filepath.FromSlash(name)) || path.Clean(name) != name {
		return false
	}
	return name == ".claude-plugin/marketplace.json" || strings.HasPrefix(name, "plugins/")
}

// initClaudeCodeInstall writes every generated file whose bytes differ, removes
// files an earlier generation listed and this one does not, and writes the
// marker last. A run that changes nothing leaves every file untouched.
func initClaudeCodeInstall(root, format string, stdout io.Writer) error {
	executable, err := claudeCodeExecutable()
	if err != nil {
		return err
	}
	previous, hadMarker, err := readClaudeCodeMarker(root)
	if err != nil {
		return internalError("ktx init claude: read %q: %v", filepath.Join(root, claudeCodeMarkerFile), err)
	}
	files, err := renderClaudeCodeFiles(executable)
	if err != nil {
		return internalError("ktx init claude: render plugins: %v", err)
	}
	revision, err := claudeCodeRevision()
	if err != nil {
		return internalError("ktx init claude: render plugins: %v", err)
	}

	changed := false
	current := map[string]bool{}
	names := make([]string, 0, len(files))
	for _, file := range files {
		current[file.path] = true
		names = append(names, file.path)
		wrote, err := writeClaudeCodeFile(filepath.Join(root, filepath.FromSlash(file.path)), file.data)
		if err != nil {
			return internalError("ktx init claude: write %q: %v", file.path, err)
		}
		changed = changed || wrote
	}
	if hadMarker {
		for _, old := range previous.Files {
			if current[old] {
				continue
			}
			target := filepath.Join(root, filepath.FromSlash(old))
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return internalError("ktx init claude: remove outdated %q: %v", old, err)
			}
			pruneClaudeCodeDirs(root, filepath.Dir(target))
			changed = true
		}
	}
	marker, err := marshalClaudeCodeJSON(claudeCodeMarker{
		SchemaVersion: claudeCodeMarkerSchema,
		Generator:     claudeCodeRefreshCommand,
		Revision:      revision,
		KtxExecutable: executable,
		Files:         names,
	}, "  ")
	if err != nil {
		return internalError("ktx init claude: encode marker: %v", err)
	}
	wrote, err := writeClaudeCodeFile(filepath.Join(root, claudeCodeMarkerFile), marker)
	if err != nil {
		return internalError("ktx init claude: write marker: %v", err)
	}
	changed = changed || wrote

	writeClaudeCodeInstallReport(stdout, root, executable, revision, names, !changed, format)
	return nil
}

// writeClaudeCodeFile atomically replaces target with data unless it already
// holds exactly those bytes. It reports whether it wrote.
func writeClaudeCodeFile(target string, data []byte) (bool, error) {
	existing, err := os.ReadFile(target)
	if err == nil && bytes.Equal(existing, data) {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	if err := durablefile.Write(target, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// pruneClaudeCodeDirs removes dir and its empty parents up to, not including,
// root. A directory that still holds anything stops the walk.
func pruneClaudeCodeDirs(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

type claudeCodeShowState struct {
	marker  claudeCodeMarker
	current string
	reasons []string
}

// inspectClaudeCode compares the generated folder with what this binary would
// generate for the pinned executable recorded in the marker. The executable
// running --show is never compared with the pinned one.
func inspectClaudeCode(root string, marker claudeCodeMarker) (claudeCodeShowState, error) {
	state := claudeCodeShowState{marker: marker, reasons: []string{}}
	current, err := claudeCodeRevision()
	if err != nil {
		return state, err
	}
	state.current = current
	if marker.Revision != current {
		state.reasons = append(state.reasons, "generated by a different ktx version")
	}
	if info, err := os.Stat(marker.KtxExecutable); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		state.reasons = append(state.reasons, "pinned ktx executable is missing or not executable: "+marker.KtxExecutable)
	}
	expected := map[string][]byte{}
	if marker.Revision == current {
		files, err := renderClaudeCodeFiles(marker.KtxExecutable)
		if err != nil {
			return state, err
		}
		for _, file := range files {
			expected[file.path] = file.data
		}
	}
	for _, name := range marker.Files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if errors.Is(err, fs.ErrNotExist) {
			state.reasons = append(state.reasons, "generated file is missing: "+name)
			continue
		}
		if err != nil {
			return state, err
		}
		if want, ok := expected[name]; ok && !bytes.Equal(data, want) {
			state.reasons = append(state.reasons, "generated file was changed: "+name)
		}
	}
	return state, nil
}

// initClaudeCodeShow reports the generated marketplace from its marker.
// It exits 0 when generated, stale or not, and 2 when no marker exists.
func initClaudeCodeShow(root, format string, stdout io.Writer) error {
	marker, exists, err := readClaudeCodeMarker(root)
	if err != nil {
		return internalError("ktx init claude: inspect %q: %v", root, err)
	}
	if !exists {
		return notFoundError("ktx init claude: plugins not generated (no %s in %q)", claudeCodeMarkerFile, root)
	}
	state, err := inspectClaudeCode(root, marker)
	if err != nil {
		return internalError("ktx init claude: inspect %q: %v", root, err)
	}
	stale := len(state.reasons) > 0
	if format == FormatText {
		fmt.Fprintf(stdout, "ktx claude-code plugins generated at %s\n", root)
		fmt.Fprintf(stdout, "marketplace: %s\n", claudeCodeMarketplaceName)
		fmt.Fprintf(stdout, "pinned ktx: %s\n", marker.KtxExecutable)
		fmt.Fprintf(stdout, "generated revision: %s\n", marker.Revision)
		if stale {
			fmt.Fprintf(stdout, "stale: yes; run `%s` to refresh\n", claudeCodeRefreshCommand)
			for _, reason := range state.reasons {
				fmt.Fprintf(stdout, "  - %s\n", reason)
			}
		} else {
			fmt.Fprintln(stdout, "stale: no")
		}
		fmt.Fprintln(stdout, "Claude Code registration: not verified")
		return nil
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{
		"installed":          true,
		"path":               root,
		"marketplace":        claudeCodeMarketplaceName,
		"ktx_executable":     marker.KtxExecutable,
		"generated_revision": marker.Revision,
		"current_revision":   state.current,
		"files":              marker.Files,
		"stale":              stale,
		"stale_reasons":      state.reasons,
		"refresh_command":    claudeCodeRefreshCommand,
		"runtime_activation": "not_verified",
	})
	return nil
}

// initClaudeCodeUninstall removes the files the marker lists, the type folders
// Claude Code writes into loaded plugins, the marker, and directories left
// empty. Other files stay and are reported.
func initClaudeCodeUninstall(root, format string, stdout, stderr io.Writer) error {
	marker, exists, err := readClaudeCodeMarker(root)
	if err != nil {
		return internalError("ktx init claude: inspect %q: %v", root, err)
	}
	if !exists {
		return notFoundError("ktx init claude: plugins not generated (no %s in %q)", claudeCodeMarkerFile, root)
	}
	removed := 0
	plugins := map[string]bool{}
	for _, name := range marker.Files {
		if parts := strings.Split(name, "/"); len(parts) > 2 && parts[0] == "plugins" {
			plugins[parts[1]] = true
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return internalError("ktx init claude: inspect %q: %v", name, err)
		}
		if info.IsDir() {
			continue
		}
		if err := os.Remove(target); err != nil {
			return internalError("ktx init claude: remove %q: %v", name, err)
		}
		removed++
	}
	for plugin := range plugins {
		types := filepath.Join(root, "plugins", plugin, filepath.FromSlash(claudeCodeTypesFolder))
		info, err := os.Lstat(types)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := os.RemoveAll(types); err != nil {
			return internalError("ktx init claude: remove %q: %v", types, err)
		}
	}
	if err := os.Remove(filepath.Join(root, claudeCodeMarkerFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return internalError("ktx init claude: remove marker: %v", err)
	}

	left := []string{}
	dirs := []string{}
	_ = filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, current)
			return nil
		}
		relative, _ := filepath.Rel(root, current)
		left = append(left, filepath.ToSlash(relative))
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
	if parent := filepath.Dir(root); filepath.Base(parent) == "claude-code" {
		_ = os.Remove(parent)
	}

	if format == FormatText {
		fmt.Fprintf(stdout, "ktx claude-code plugins removed from %s (%d file%s)\n", root, removed, pluralS(removed))
		fmt.Fprintf(stdout, "Also run: %s\n", claudeCodeRemoveCommand)
		for _, name := range left {
			fmt.Fprintf(stderr, "ktx: warning: left %s in place; ktx did not generate it\n", filepath.Join(root, filepath.FromSlash(name)))
		}
		return nil
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{
		"uninstalled":  true,
		"path":         root,
		"removed":      removed,
		"left_files":   left,
		"next_command": claudeCodeRemoveCommand,
	})
	return nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// writeClaudeCodeInstallReport emits the generated folder, the pinned
// executable, and the commands that register the plugins in Claude Code.
func writeClaudeCodeInstallReport(w io.Writer, root, executable, revision string, files []string, unchanged bool, format string) {
	steps := claudeCodeNextSteps(root)
	if format == FormatText {
		if unchanged {
			fmt.Fprintf(w, "ktx claude-code plugins already up to date at %s (no change)\n", root)
		} else {
			fmt.Fprintf(w, "ktx claude-code plugins generated at %s\n", root)
		}
		fmt.Fprintf(w, "generated revision: %s\n", revision)
		fmt.Fprintf(w, "pinned ktx: %s\n", executable)
		roles := make([]string, 0, len(claudeCodePlugins))
		for _, plugin := range claudeCodePlugins {
			roles = append(roles, plugin.name+" ("+plugin.role+")")
		}
		fmt.Fprintf(w, "plugins: %s\n", strings.Join(roles, ", "))
		fmt.Fprintln(w, "Register them once in Claude Code:")
		for _, step := range steps {
			fmt.Fprintf(w, "  %s\n", step)
		}
		fmt.Fprintf(w, "Note: %s\n", claudeCodeReloadNote)
		fmt.Fprintf(w, "troubleshooting: %s\n", claudeCodeTroubleshooting)
		fmt.Fprintln(w, "runtime activation: not verified")
		return
	}
	payload := map[string]any{
		"installed":          true,
		"path":               root,
		"marketplace":        claudeCodeMarketplaceName,
		"plugins":            claudeCodePluginNames(),
		"ktx_executable":     executable,
		"generated_revision": revision,
		"files":              files,
		"next_steps":         steps,
		"note":               claudeCodeReloadNote,
		"refresh_command":    claudeCodeRefreshCommand,
		"troubleshooting":    claudeCodeTroubleshooting,
		"runtime_activation": "not_verified",
	}
	if unchanged {
		payload["already_installed"] = true
	}
	_ = json.NewEncoder(w).Encode(payload)
}
