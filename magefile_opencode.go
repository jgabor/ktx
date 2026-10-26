//go:build mage

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jgabor/ktx/internal/opencodeplugin"
)

// OpencodePackageContents checks that npm would pack the built OpenCode
// package with its README, license, both targets and every file they import.
// Run mage opencodePackage first. npm packs a copy, so the checkout is left
// as it is.
func OpencodePackageContents() error {
	return opencodePackageContents(opencodePackageDir)
}

// opencodePackFiles are the files besides the targets and their imports that
// the published package must carry.
var opencodePackFiles = []string{"package.json", "README.md", "LICENSE"}

// opencodePlaceholderVersion is the version a copy without one is packed at:
// npm packs nothing without a version, and the package records its version
// only when it is published.
const opencodePlaceholderVersion = "0.0.0-check"

func opencodePackageContents(dir string) error {
	if err := requireJSRuntime("node", "to pack the OpenCode package with npm"); err != nil {
		return err
	}
	if err := opencodePackageReady(dir); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("", "ktx-opencode-pack-")
	if err != nil {
		return fmt.Errorf("create OpenCode package copy: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := copyPackageFolder(dir, stage); err != nil {
		return fmt.Errorf("copy OpenCode package: %w", err)
	}
	if err := ensurePackageVersion(stage); err != nil {
		return err
	}
	command := npmCommand("pack", "--dry-run", "--json", "--ignore-scripts")
	command.Dir = stage
	command.Stderr = os.Stderr
	out, err := command.Output()
	if err != nil {
		return fmt.Errorf("npm pack the OpenCode package: %w", err)
	}
	var packs []struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(out, &packs); err != nil || len(packs) != 1 {
		return fmt.Errorf("read npm pack output for the OpenCode package: want one package, got %q", out)
	}
	packed := make([]string, 0, len(packs[0].Files))
	for _, file := range packs[0].Files {
		packed = append(packed, file.Path)
	}
	if err := opencodePackProblems(stage, packed); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "OpenCode package: npm packs %s\n", strings.Join(packed, ", "))
	return nil
}

// relativeImport matches the module specifier of a static import, a
// re-export or a dynamic import that names a file relative to the importer.
var relativeImport = regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*\(\s*|\bimport\s+)["'](\.\.?/[^"']+)["']`)

// opencodePackProblems reports what the packed file list lacks: the files
// every published package carries, the ./server and ./tui targets that the
// manifest in dir names, and every file a packed target imports by a relative
// path, followed through each imported file in turn.
func opencodePackProblems(dir string, packed []string) error {
	inPack := make(map[string]bool, len(packed))
	for _, name := range packed {
		inPack[name] = true
	}
	targets, problems := opencodePackageTargets(dir)
	for _, name := range opencodePackFiles {
		if !inPack[name] {
			problems = append(problems, fmt.Errorf("npm would not pack the OpenCode package's %s", name))
		}
	}
	visited := map[string]bool{}
	var queue []string
	for _, target := range targets {
		if !inPack[target.file] {
			problems = append(problems, fmt.Errorf("npm would not pack the OpenCode package's %s target (%s); check the files list in package.json", target.export, target.file))
			continue
		}
		queue = append(queue, target.file)
	}
	for len(queue) > 0 {
		file := queue[0]
		queue = queue[1:]
		if visited[file] {
			continue
		}
		visited[file] = true
		source, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			problems = append(problems, fmt.Errorf("read packed OpenCode package file %s: %w", file, err))
			continue
		}
		for _, match := range relativeImport.FindAllStringSubmatch(string(source), -1) {
			imported := path.Join(path.Dir(file), match[1])
			if !inPack[imported] {
				problems = append(problems, fmt.Errorf("npm would not pack %s, which the OpenCode package's %s imports; check the files list in package.json", imported, file))
				continue
			}
			queue = append(queue, imported)
		}
	}
	return errors.Join(problems...)
}

// copyPackageFolder copies the regular files under dir to stage, leaving out
// installed dependencies.
func copyPackageFolder(dir, stage string) error {
	return filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		target := filepath.Join(stage, relative)
		switch {
		case entry.IsDir() && entry.Name() == "node_modules":
			return filepath.SkipDir
		case entry.IsDir():
			return os.MkdirAll(target, 0o700)
		case !entry.Type().IsRegular():
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

// ensurePackageVersion gives the manifest in dir the placeholder version when
// it has none.
func ensurePackageVersion(dir string) error {
	file := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read OpenCode package manifest: %w", err)
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("decode OpenCode package manifest: %w", err)
	}
	if _, ok := manifest["version"]; ok {
		return nil
	}
	manifest["version"] = json.RawMessage(strconv.Quote(opencodePlaceholderVersion))
	data, err = json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o600)
}

// npmCommand runs npm. When npm sits beside the node executable captured
// before the disposable HOME took effect, it runs npm's script with that
// node, so a version-manager shim is not started under the disposable HOME;
// otherwise it runs the npm on PATH.
func npmCommand(args ...string) *exec.Cmd {
	if node := os.Getenv(jsRuntimeVariables["node"]); node != "" {
		script, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(node), "npm"))
		if err == nil && strings.HasSuffix(script, ".js") {
			return exec.Command(node, append([]string{script}, args...)...)
		}
	}
	return exec.Command("npm", args...)
}

// opencodeSetup is what OpenCode has of ktx, in the scopes where ktx is set
// up.
type opencodeSetup struct {
	opencode string
	// checkoutPackage is this checkout's package folder. An entry pointing
	// there counts as ktx's even when the folder has no package.json.
	checkoutPackage string
	scopes          []opencodeplugin.State
}

// readOpencodeSetup reads what OpenCode has of ktx. It returns nil, after a
// note, when opencode is not on PATH or no scope has a ktx entry or a plugin
// file an earlier ktx generated: the targets switch OpenCode only where ktx is
// already set up. It changes nothing.
func readOpencodeSetup(opencode string, scopes []opencodeplugin.Scope, checkoutPackage string, stdout io.Writer) (*opencodeSetup, error) {
	if opencode == "" {
		fmt.Fprintln(stdout, "opencode not found on PATH; skipping the OpenCode plugin")
		return nil, nil
	}
	setup := opencodeSetup{opencode: opencode, checkoutPackage: checkoutPackage}
	for _, scope := range scopes {
		state, err := opencodeplugin.Inspect(scope, checkoutPackage)
		if err != nil {
			return nil, err
		}
		if state.HasEntries() || state.Legacy {
			setup.scopes = append(setup.scopes, state)
		}
	}
	if len(setup.scopes) == 0 {
		fmt.Fprintln(stdout, "OpenCode: ktx is not set up; skipping the OpenCode plugin (run `ktx init opencode` to install it)")
		return nil, nil
	}
	return &setup, nil
}

// useOpencodePlugin points every set-up scope at spec, a package folder or a
// pinned npm spec. A scope that already has exactly one ktx entry for spec in
// its server and TUI config is left as it is. Otherwise every ktx entry in
// the scope is removed, keeping every other entry and the rest of each file
// byte for byte, and `opencode plugin` adds spec. A plugin file an earlier ktx
// generated is removed afterwards.
func useOpencodePlugin(setup opencodeSetup, spec string, runner installRunner, stdout io.Writer) error {
	for _, state := range setup.scopes {
		scope := state.Scope
		changed, err := opencodeplugin.Point(state, setup.opencode, spec, setup.checkoutPackage, opencodeplugin.Runner(runner))
		if errors.Is(err, opencodeplugin.ErrRestored) {
			return fmt.Errorf("point the OpenCode %s config at %s: %w", scope.Name, spec, err)
		}
		if err != nil {
			return fmt.Errorf("point the OpenCode %s config at %s: %w\n  Recovery: remove every %s entry from the plugin lists in %s, then run `%s`", scope.Name, spec, err,
				opencodeplugin.PackageName, strings.Join(scope.ConfigFiles(), ", "), recoveryCommand("opencode", scope.InstallArgs(spec)...))
		}
		if state.Legacy {
			if err := opencodeplugin.RemoveGenerated(scope.Legacy); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "OpenCode (%s): removed the plugin an earlier ktx generated at %s\n", scope.Name, scope.Legacy)
			changed = true
		}
		if changed {
			fmt.Fprintf(stdout, "OpenCode (%s): %s now loads from %s; restart OpenCode\n", scope.Name, opencodeplugin.PackageName, spec)
		} else {
			fmt.Fprintf(stdout, "OpenCode (%s): %s already loads from %s\n", scope.Name, opencodeplugin.PackageName, spec)
		}
	}
	return nil
}

// requireOpencodePackageOnNPM checks that the npm registry has the package
// for a ktx release, so installRelease never swaps the binary for a release
// OpenCode can't be pointed at. It returns the pinned spec.
func requireOpencodePackageOnNPM(runner installRunner, version string) (string, error) {
	spec := opencodeplugin.ReleaseSpec(version)
	out, stderr, err := runner("npm", "view", spec, "version", "--json")
	if err == nil && strings.Contains(out, strconv.Quote(strings.TrimPrefix(version, "v"))) {
		return spec, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("npm not found on PATH; it is needed to confirm that %s is published before switching OpenCode, so mage installRelease changed nothing", spec)
	}
	detail := strings.TrimSpace(stderr)
	if strings.Contains(out+stderr, "E404") {
		detail = "not found on the npm registry"
	} else if detail == "" && err != nil {
		detail = err.Error()
	} else if detail == "" {
		detail = "npm view printed no matching version"
	}
	return "", fmt.Errorf("the latest ktx release, %s, has no OpenCode package on npm (%s: %s), so mage installRelease changed nothing; keep OpenCode on this checkout with `mage install` until the package is published", version, spec, detail)
}
