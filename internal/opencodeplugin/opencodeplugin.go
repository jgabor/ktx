// Package opencodeplugin reads and edits the ktx entries in OpenCode's plugin
// config, for `ktx init opencode` and the mage install targets.
//
// OpenCode lists plugins in the plugin array of its server config
// (opencode.json or opencode.jsonc) and its TUI config (tui.json or
// tui.jsonc), globally and per project. `opencode plugin` adds entries but
// cannot remove them, so this package removes ktx's own entries, and only
// those, keeping every other entry and every other byte of each file. It also
// recognizes the plugin file that earlier ktx versions generated.
package opencodeplugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// PackageName is the npm name of the ktx OpenCode package.
const PackageName = "@jgabor/ktx"

// GeneratedMarkers are strings every plugin file that earlier ktx versions
// generated for OpenCode contains. A ktx.ts without them is someone else's
// file and is left alone.
var GeneratedMarkers = []string{"KTX_SESSION_ID", "tool.execute.after"}

// Scope is one place OpenCode reads plugin config from: the global config
// folder or a project.
type Scope struct {
	Name   string
	Global bool
	// Dir is the folder `opencode plugin` writes this scope's config to.
	Dir string
	// Server and TUI are the config files whose plugin arrays OpenCode loads
	// server and TUI plugins from. `opencode plugin` writes to whichever
	// exists, or creates one.
	Server, TUI []string
	// Legacy is where earlier ktx versions wrote a generated plugin file.
	Legacy string
}

// ConfigFiles returns the server and then the TUI config files.
func (s Scope) ConfigFiles() []string {
	return append(append([]string{}, s.Server...), s.TUI...)
}

// InstallArgs returns the `opencode` arguments that add spec to this scope.
// A project scope's command runs in the project folder.
func (s Scope) InstallArgs(spec string) []string {
	if s.Global {
		return []string{"plugin", "-g", spec}
	}
	return []string{"plugin", spec}
}

// GlobalDir returns OpenCode's global config folder: $XDG_CONFIG_HOME/opencode,
// or ~/.config/opencode when XDG_CONFIG_HOME is unset.
func GlobalDir() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "opencode"), nil
}

// Scopes returns the global scope and the project scope of the current
// folder, the two places `opencode plugin` writes to.
func Scopes() ([]Scope, error) {
	dir, err := GlobalDir()
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return []Scope{GlobalScope(dir), ProjectScope(cwd)}, nil
}

// GlobalScope is the global scope whose config folder is dir.
func GlobalScope(dir string) Scope {
	return Scope{
		Name: "global", Global: true, Dir: dir,
		Server: []string{filepath.Join(dir, "config.json"), filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc")},
		TUI:    []string{filepath.Join(dir, "tui.json"), filepath.Join(dir, "tui.jsonc")},
		Legacy: filepath.Join(dir, "plugins", "ktx.ts"),
	}
}

// ProjectScope covers the project's .opencode folder, where `opencode plugin`
// writes, and the config files at the project root, so a ktx entry there is
// not left loading beside the new one.
func ProjectScope(project string) Scope {
	dir := filepath.Join(project, ".opencode")
	scope := Scope{Name: "project", Dir: dir, Legacy: filepath.Join(dir, "plugins", "ktx.ts")}
	for _, folder := range []string{dir, project} {
		scope.Server = append(scope.Server, filepath.Join(folder, "opencode.json"), filepath.Join(folder, "opencode.jsonc"))
		scope.TUI = append(scope.TUI, filepath.Join(folder, "tui.json"), filepath.Join(folder, "tui.jsonc"))
	}
	return scope
}

// Entry is one ktx entry in an OpenCode plugin array.
type Entry struct {
	File string `json:"file"`
	Spec string `json:"spec"`
}

// State is what one scope has of ktx.
type State struct {
	Scope       Scope
	Server, TUI []Entry
	// Legacy reports whether Scope.Legacy is a plugin file an earlier ktx
	// generated.
	Legacy bool
}

// HasEntries reports whether the scope has any ktx entry.
func (s State) HasEntries() bool { return len(s.Server)+len(s.TUI) > 0 }

// Entries returns the server and then the TUI entries.
func (s State) Entries() []Entry {
	return append(append([]Entry{}, s.Server...), s.TUI...)
}

// PointsAt reports whether the scope has exactly one ktx server entry and one
// ktx TUI entry, both spec.
func (s State) PointsAt(spec string) bool {
	return len(s.Server) == 1 && len(s.TUI) == 1 && SameSpec(s.Server[0], spec) && SameSpec(s.TUI[0], spec)
}

// Current returns the one spec the scope's ktx entries point at, a folder
// for a path entry, when it has exactly one server entry and one TUI entry
// and both agree.
func (s State) Current() (string, bool) {
	if len(s.Server) != 1 || len(s.TUI) != 1 {
		return "", false
	}
	spec := s.Server[0].Spec
	if folder := specPath(spec, filepath.Dir(s.Server[0].File)); folder != "" {
		spec = folder
	}
	return spec, s.PointsAt(spec)
}

// Inspect reads the scope's config files and generated plugin file. A path
// entry naming checkoutPackage counts as ktx's even when that folder has no
// package.json. It changes nothing.
func Inspect(scope Scope, checkoutPackage string) (State, error) {
	state := State{Scope: scope}
	for _, kind := range []struct {
		files   []string
		entries *[]Entry
	}{{scope.Server, &state.Server}, {scope.TUI, &state.TUI}} {
		for _, file := range kind.files {
			_, array, err := ReadPluginArray(file)
			if err != nil {
				return state, err
			}
			if array == nil {
				continue
			}
			for _, element := range array.Elements {
				if IsKtxSpec(element.Spec, filepath.Dir(file), checkoutPackage) {
					*kind.entries = append(*kind.entries, Entry{File: file, Spec: element.Spec})
				}
			}
		}
	}
	generated, err := IsGeneratedPlugin(scope.Legacy)
	if err != nil {
		return state, err
	}
	state.Legacy = generated
	return state, nil
}

// IsGeneratedPlugin reports whether path is a plugin file that an earlier ktx
// generated.
func IsGeneratedPlugin(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read OpenCode plugin %s: %w", path, err)
	}
	for _, marker := range GeneratedMarkers {
		if !strings.Contains(string(data), marker) {
			return false, nil
		}
	}
	return true, nil
}

// RemoveGenerated removes the plugin file an earlier ktx generated at path.
func RemoveGenerated(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the plugin an earlier ktx generated at %s: %w", path, err)
	}
	return nil
}

// specPath returns the folder a path plugin spec names, resolving a relative
// path against base, or "" for a spec that is not a path.
func specPath(spec, base string) string {
	switch {
	case strings.HasPrefix(spec, "file://"):
		parsed, err := url.Parse(spec)
		if err != nil || parsed.Path == "" {
			return ""
		}
		return filepath.Clean(filepath.FromSlash(parsed.Path))
	case filepath.IsAbs(spec):
		return filepath.Clean(spec)
	case strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../"):
		return filepath.Join(base, spec)
	}
	return ""
}

// IsKtxSpec reports whether a plugin spec names the ktx package: the npm
// package, with or without a version, the checkoutPackage folder, or another
// folder whose package.json names the package. A relative path resolves
// against base, the folder of the config file that lists it.
func IsKtxSpec(spec, base, checkoutPackage string) bool {
	if spec == PackageName || strings.HasPrefix(spec, PackageName+"@") {
		return true
	}
	folder := specPath(spec, base)
	if folder == "" {
		return false
	}
	if checkoutPackage != "" && folder == filepath.Clean(checkoutPackage) {
		return true
	}
	data, err := os.ReadFile(filepath.Join(folder, "package.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(data, &manifest) == nil && manifest.Name == PackageName
}

// SameSpec reports whether an entry's spec is the target spec: the same
// folder for path specs, the same text otherwise.
func SameSpec(entry Entry, target string) bool {
	if folder := specPath(entry.Spec, filepath.Dir(entry.File)); folder != "" {
		return folder == specPath(target, "")
	}
	return entry.Spec == target
}

// releaseVersion matches an exact release version such as v1.2.3. Development
// builds, pseudo-versions, prereleases and builds with metadata do not match.
var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// IsReleaseVersion reports whether a ktx version is an exact release.
func IsReleaseVersion(version string) bool {
	return releaseVersion.MatchString(version)
}

// ReleaseSpec is the npm spec of the package for a ktx release: the release
// version without its leading v.
func ReleaseSpec(version string) string {
	return PackageName + "@" + strings.TrimPrefix(version, "v")
}

// SpecFor returns the spec `ktx init opencode` installs for a ktx version: the
// package pinned to the release, or the unpinned package for a version
// without a release number.
func SpecFor(version string) (spec string, pinned bool) {
	if IsReleaseVersion(version) {
		return ReleaseSpec(version), true
	}
	return PackageName, false
}

// Runner runs a command and returns its stdout and stderr.
type Runner func(name string, args ...string) (stdout, stderr string, err error)

// ErrRestored marks a Point failure after which every config file of the
// scope was put back as it was before Point ran.
var ErrRestored = errors.New("every OpenCode config file is back as it was")

// Point makes spec the one ktx entry in the scope's server and TUI config. A
// scope that already points at spec is left as it is and Point reports no
// change. Otherwise it removes every ktx entry in the scope and runs
// `opencode plugin` to add spec, then checks that exactly one server entry
// and one TUI entry for spec remain. On failure it puts every config file
// back as it was, removing files that did not exist before, and wraps
// ErrRestored. When that also fails, the error names the files, and the scope
// can be left without a ktx entry; the caller says how to recover.
func Point(state State, opencode, spec, checkoutPackage string, run Runner) (bool, error) {
	if state.PointsAt(spec) {
		return false, nil
	}
	saved, err := saveFiles(state.Scope.ConfigFiles())
	if err != nil {
		return false, err
	}
	if err := point(state.Scope, opencode, spec, checkoutPackage, run); err != nil {
		if restoreErr := saved.restore(); restoreErr != nil {
			return false, fmt.Errorf("%w; restoring the config failed: %v", err, restoreErr)
		}
		return false, fmt.Errorf("%w; %w", err, ErrRestored)
	}
	return true, nil
}

func point(scope Scope, opencode, spec, checkoutPackage string, run Runner) error {
	for _, file := range scope.ConfigFiles() {
		if _, err := RemoveKtxEntries(file, checkoutPackage); err != nil {
			return err
		}
	}
	args := scope.InstallArgs(spec)
	if stdout, stderr, err := run(opencode, args...); err != nil {
		detail := commandOutput(stderr)
		if detail == "" {
			detail = commandOutput(stdout)
		}
		return fmt.Errorf("opencode %s: %w: %s", strings.Join(args, " "), err, detail)
	}
	after, err := Inspect(scope, checkoutPackage)
	if err != nil {
		return err
	}
	if !after.PointsAt(spec) {
		return fmt.Errorf("after `opencode %s` the config holds %d ktx server entries and %d ktx TUI entries, want exactly one of each for %s", strings.Join(args, " "), len(after.Server), len(after.TUI), spec)
	}
	return nil
}

// terminalEscape matches the color and cursor sequences opencode writes.
var terminalEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// commandOutput reduces opencode's terminal output to its text: escape
// sequences, the box-drawing frame around each line and empty lines go, and
// the remaining lines are joined with "; ". opencode prints install errors
// in that frame on stdout.
func commandOutput(output string) string {
	var lines []string
	for line := range strings.SplitSeq(terminalEscape.ReplaceAllString(output, ""), "\n") {
		line = strings.TrimFunc(line, func(r rune) bool {
			return unicode.IsSpace(r) || (r >= 0x2500 && r <= 0x25FF)
		})
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "; ")
}

// savedFile is a config file's content and mode before Point changed it.
type savedFile struct {
	path   string
	exists bool
	data   []byte
	mode   os.FileMode
}

type savedFiles []savedFile

func saveFiles(paths []string) (savedFiles, error) {
	saved := make(savedFiles, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			saved = append(saved, savedFile{path: path})
			continue
		}
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		saved = append(saved, savedFile{path: path, exists: true, data: data, mode: info.Mode().Perm()})
	}
	return saved, nil
}

// restore puts each file back as saved and removes files that did not exist.
// It returns an error naming every file it could not restore.
func (saved savedFiles) restore() error {
	var problems []error
	for _, file := range saved {
		current, err := os.ReadFile(file.path)
		switch {
		case !file.exists && errors.Is(err, os.ErrNotExist):
			continue
		case !file.exists:
			if err := os.Remove(file.path); err != nil {
				problems = append(problems, err)
			}
			continue
		case err == nil && bytes.Equal(current, file.data):
			continue
		}
		if err := writeFileAtomically(file.path, file.data, file.mode); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", file.path, err))
		}
	}
	return errors.Join(problems...)
}

// RemoveKtxEntries removes every ktx entry from the plugin array of one
// config file, leaves every other byte of the file as it was, and returns the
// specs it removed. An absent file or a file without a plugin array is left
// alone.
func RemoveKtxEntries(file, checkoutPackage string) ([]string, error) {
	var removed []string
	for {
		data, array, err := ReadPluginArray(file)
		if err != nil || array == nil {
			return removed, err
		}
		index := -1
		for i, element := range array.Elements {
			if IsKtxSpec(element.Spec, filepath.Dir(file), checkoutPackage) {
				index = i
				break
			}
		}
		if index < 0 {
			return removed, nil
		}
		info, err := os.Stat(file)
		if err != nil {
			return removed, err
		}
		if err := writeFileAtomically(file, RemoveArrayElement(data, *array, index), info.Mode().Perm()); err != nil {
			return removed, fmt.Errorf("update OpenCode config %s: %w", file, err)
		}
		removed = append(removed, array.Elements[index].Spec)
	}
}

// writeFileAtomically replaces path's content through a renamed temporary
// file. A symlinked path keeps its link: the file it points at is replaced.
func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	// After the rename the temporary name is gone and Remove fails harmlessly.
	defer func() { _ = os.Remove(temporary.Name()) }()
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
