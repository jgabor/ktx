package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
)

// checkoutPluginCapabilities renders the plugin identity fields an image
// built from root reports: the checkout's commit and package content hash.
func checkoutPluginCapabilities(t *testing.T, root string) string {
	t.Helper()
	commit, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skipf("benchmark preflight needs a git checkout: %v", err)
	}
	digest, err := benchcontract.PluginFilesSHA256(filepath.Join(root, filepath.FromSlash(benchcontract.PluginSourceDir)), benchcontract.PluginFiles)
	if err != nil {
		t.Fatal(err)
	}
	return pluginCapabilities(strings.TrimSpace(string(commit)), digest)
}

func pluginCapabilities(commit, digest string) string {
	return fmt.Sprintf(`"ktx_commit":%q,"ktx_commit_modified":false,"opencode_plugin_package":%q,"opencode_plugin_commit":%q,"opencode_plugin_sha256":%q,"opencode_plugins":[%q]`, commit, benchcontract.PluginPackage, commit, digest, benchcontract.PluginSpec)
}

// writeFixturePluginSource gives a fixture checkout the package files the
// benchmark image installs and returns their content hash.
func writeFixturePluginSource(root string) (string, error) {
	directory := filepath.Join(root, filepath.FromSlash(benchcontract.PluginSourceDir))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	for name, content := range map[string]string{"package.json": `{"name":"` + benchcontract.PluginPackage + `"}` + "\n", "server.ts": "export default async () => ({})\n"} {
		path := filepath.Join(directory, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return benchcontract.PluginFilesSHA256(directory, benchcontract.PluginFiles)
}

func TestReadBenchPluginIdentity(t *testing.T) {
	commit := strings.Repeat("d", 40)
	setup := func(t *testing.T) (string, string, string) {
		t.Helper()
		root := t.TempDir()
		install := filepath.Join(root, "opt", "opencode-plugin")
		if err := os.MkdirAll(install, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{"package.json": `{"name":"` + benchcontract.PluginPackage + `"}`, "server.ts": "export {}\n"} {
			if err := os.WriteFile(filepath.Join(install, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		commitFile := filepath.Join(root, "opt", "opencode-plugin.commit")
		if err := os.WriteFile(commitFile, []byte(commit+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		config := filepath.Join(root, "config")
		if err := os.MkdirAll(filepath.Join(config, "opencode", "plugins"), 0o755); err != nil {
			t.Fatal(err)
		}
		spec := `{"plugin":["` + benchcontract.PluginSpec + `"]}`
		for _, name := range []string{"opencode.jsonc", "tui.json"} {
			if err := os.WriteFile(filepath.Join(config, "opencode", name), []byte(spec), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return config, install, commitFile
	}
	t.Run("one package from the binary's commit", func(t *testing.T) {
		config, install, commitFile := setup(t)
		identity, err := readBenchPluginIdentity(commit, "true", config, install, commitFile)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := benchcontract.PluginDirSHA256(install)
		if !identity.sameCommitPackage() || identity.PluginSHA256 != want || !identity.KTXCommitModified {
			t.Fatalf("identity = %+v", identity)
		}
	})
	t.Run("a binary without a stamped commit", func(t *testing.T) {
		config, install, commitFile := setup(t)
		identity, err := readBenchPluginIdentity("", "", config, install, commitFile)
		if err != nil {
			t.Fatal(err)
		}
		if identity.sameCommitPackage() || identity.KTXCommit != "" || identity.KTXCommitModified {
			t.Fatalf("identity = %+v", identity)
		}
	})
	t.Run("a generated plugin file is a second plugin", func(t *testing.T) {
		config, install, commitFile := setup(t)
		if err := os.WriteFile(filepath.Join(config, "opencode", "plugins", "ktx.ts"), []byte("export {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		identity, err := readBenchPluginIdentity(commit, "true", config, install, commitFile)
		if err != nil {
			t.Fatal(err)
		}
		if identity.sameCommitPackage() || len(identity.Plugins) != 2 {
			t.Fatalf("identity = %+v", identity)
		}
	})
	t.Run("a package from another commit", func(t *testing.T) {
		config, install, commitFile := setup(t)
		if err := os.WriteFile(commitFile, []byte(strings.Repeat("e", 40)), 0o644); err != nil {
			t.Fatal(err)
		}
		identity, err := readBenchPluginIdentity(commit, "true", config, install, commitFile)
		if err != nil {
			t.Fatal(err)
		}
		if identity.sameCommitPackage() {
			t.Fatalf("identity = %+v", identity)
		}
	})
	t.Run("an unreadable plugin list", func(t *testing.T) {
		config, install, commitFile := setup(t)
		if err := os.WriteFile(filepath.Join(config, "opencode", "opencode.jsonc"), []byte("// comment\n{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readBenchPluginIdentity(commit, "true", config, install, commitFile); err == nil {
			t.Fatal("an unparsed plugin list was accepted")
		}
	})
}
