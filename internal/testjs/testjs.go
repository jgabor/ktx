// Package testjs resolves the JavaScript runtimes that tests use to execute
// generated TypeScript.
//
// A runtime is resolved once per test binary, from the environment the binary
// started with, to the absolute path of its real executable. Tests that later
// replace HOME, XDG directories, or PATH therefore keep the same runtime, and
// version-manager shims never run under a disposable home, where they can
// reinstall themselves or print setup output.
package testjs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// DisableExperimentalWarning precedes the script in every Node invocation.
// Type stripping and node:sqlite print experimental warnings that would
// otherwise reach stderr assertions.
const DisableExperimentalWarning = "--disable-warning=ExperimentalWarning"

// Overrides name the variables that select an explicit runtime executable.
var Overrides = map[string]string{"node": "KTX_TEST_NODE", "bun": "KTX_TEST_BUN"}

const probeTimeout = 30 * time.Second

var startup = os.Environ()

type resolution struct {
	path     string
	explicit bool
	err      error
}

var (
	mu       sync.Mutex
	resolved = map[string]resolution{}
)

// Runtime returns the absolute path of the real node or bun executable. The
// corresponding override variable selects the executable explicitly; otherwise
// the startup PATH is searched. Node must be 22.18 or newer, or 23.6 or newer
// on the 23 line, so that .ts files run without flags.
//
// An unavailable runtime skips the test unless required is true. An explicit
// override that cannot be used always fails the test.
func Runtime(t testing.TB, name string, required bool) string {
	t.Helper()
	mu.Lock()
	result, ok := resolved[name]
	if !ok {
		result = resolve(startup, name)
		resolved[name] = result
	}
	mu.Unlock()
	if result.err != nil {
		if required || result.explicit {
			t.Fatalf("%s runtime unavailable: %v", name, result.err)
		}
		t.Skipf("%s runtime unavailable: %v", name, result.err)
	}
	return result.path
}

// WriteModulePackage marks dir as an ES module package, so Node loads the .ts
// files below it as modules.
func WriteModulePackage(t testing.TB, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"type":"module"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resolve(env []string, name string) resolution {
	variable, ok := Overrides[name]
	if !ok {
		return resolution{err: fmt.Errorf("unknown runtime %q", name)}
	}
	candidate := lookupEnv(env, variable)
	explicit := candidate != ""
	if explicit {
		if !filepath.IsAbs(candidate) {
			return resolution{explicit: true, err: fmt.Errorf("%s=%q is not an absolute path", variable, candidate)}
		}
	} else {
		var err error
		if candidate, err = lookPath(lookupEnv(env, "PATH"), name); err != nil {
			return resolution{err: fmt.Errorf("%w; set %s to its absolute path", err, variable)}
		}
	}
	path, err := probe(env, name, candidate)
	if err != nil {
		return resolution{explicit: explicit, err: fmt.Errorf("%s: %w", candidate, err)}
	}
	return resolution{path: path, explicit: explicit}
}

func lookupEnv(env []string, name string) string {
	value := ""
	for _, entry := range env {
		if key, rest, ok := strings.Cut(entry, "="); ok && key == name {
			value = rest
		}
	}
	return value
}

func lookPath(path, name string) (string, error) {
	for _, directory := range filepath.SplitList(path) {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}

// probe asks the runtime for its own executable path and versions. Shims
// resolve to the executable they would launch.
func probe(env []string, name, candidate string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, candidate, "-p", "JSON.stringify([process.execPath, process.versions.node, process.versions.bun || null])")
	command.Env = env
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("probe failed: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("probe failed: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var report [3]*string
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &report); err != nil || report[0] == nil || report[1] == nil {
		return "", fmt.Errorf("unexpected probe output %q", output)
	}
	path, nodeVersion, bunVersion := *report[0], *report[1], report[2]
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("runtime reported relative executable path %q", path)
	}
	switch name {
	case "bun":
		if bunVersion == nil {
			return "", errors.New("executable is not Bun")
		}
	case "node":
		if bunVersion != nil {
			return "", errors.New("executable is Bun, not Node")
		}
		if !supportedNode(nodeVersion) {
			return "", fmt.Errorf("node %s cannot run .ts files without flags; need 22.18+ or 23.6+", nodeVersion)
		}
	}
	return path, nil
}

func supportedNode(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	switch {
	case major >= 24:
		return true
	case major == 23:
		return minor >= 6
	case major == 22:
		return minor >= 18
	}
	return false
}
