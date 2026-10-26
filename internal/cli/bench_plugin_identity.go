package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/version"
)

// benchPluginIdentity describes the ktx binary and the OpenCode package
// installed in a benchmark image.
type benchPluginIdentity struct {
	KTXCommit         string   `json:"ktx_commit"`
	KTXCommitModified bool     `json:"ktx_commit_modified"`
	PluginPackage     string   `json:"opencode_plugin_package"`
	PluginCommit      string   `json:"opencode_plugin_commit"`
	PluginSHA256      string   `json:"opencode_plugin_sha256"`
	Plugins           []string `json:"opencode_plugins"`
}

// readBenchPluginIdentity takes the commit and change state stamped into the
// binary at build time, and reads the installed package's name, commit and
// content hash from benchcontract.PluginInstallDir. Plugins lists every
// plugin OpenCode would load from the global configuration: specs from the
// server and TUI config files and files in the global plugin folders. Missing
// identity values stay empty so the caller can name them; unreadable
// configuration is an error.
func readBenchPluginIdentity(commit, commitModified, configHome, installDir, commitFile string) (benchPluginIdentity, error) {
	identity := benchPluginIdentity{KTXCommit: commit, KTXCommitModified: commitModified == "true", Plugins: []string{}}
	if data, err := os.ReadFile(filepath.Join(installDir, "package.json")); err == nil {
		var manifest struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &manifest) == nil {
			identity.PluginPackage = manifest.Name
		}
	}
	if data, err := os.ReadFile(commitFile); err == nil {
		identity.PluginCommit = strings.TrimSpace(string(data))
	}
	if digest, err := benchcontract.PluginDirSHA256(installDir); err == nil {
		identity.PluginSHA256 = digest
	}
	directory := filepath.Join(configHome, "opencode")
	seen := map[string]bool{}
	for _, name := range []string{"config.json", "opencode.json", "opencode.jsonc", "tui.json", "tui.jsonc"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return identity, err
		}
		var config struct {
			Plugin []string `json:"plugin"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return identity, fmt.Errorf("read OpenCode plugin list from %s: %w", name, err)
		}
		for _, spec := range config.Plugin {
			seen[spec] = true
		}
	}
	for _, folder := range []string{"plugin", "plugins"} {
		entries, err := os.ReadDir(filepath.Join(directory, folder))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return identity, err
		}
		for _, entry := range entries {
			seen["file://"+filepath.Join(directory, folder, entry.Name())] = true
		}
	}
	for spec := range seen {
		identity.Plugins = append(identity.Plugins, spec)
	}
	slices.Sort(identity.Plugins)
	return identity, nil
}

// readInstalledBenchPlugin reads the identity of the running image. Tests
// replace it to simulate an image.
var readInstalledBenchPlugin = installedBenchPluginIdentity

// installedBenchPluginIdentity reads the identity of the running image.
func installedBenchPluginIdentity() (benchPluginIdentity, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return benchPluginIdentity{}, fmt.Errorf("resolve user home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return readBenchPluginIdentity(version.Commit, version.CommitModified, configHome, benchcontract.PluginInstallDir, benchcontract.PluginCommitFile)
}

// sameCommitPackage reports whether exactly the expected package is the only
// plugin and comes from the binary's commit.
func (identity benchPluginIdentity) sameCommitPackage() bool {
	return identity.KTXCommit != "" && identity.PluginCommit == identity.KTXCommit &&
		identity.PluginPackage == benchcontract.PluginPackage && identity.PluginSHA256 != "" &&
		slices.Equal(identity.Plugins, []string{benchcontract.PluginSpec})
}
