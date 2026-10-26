package benchcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// PluginPackage is the OpenCode package name the benchmark image installs.
	PluginPackage = "@jgabor/ktx"
	// PluginSourceDir is the package folder, relative to the repository root.
	PluginSourceDir = "plugins/opencode"
	// PluginInstallDir is where the benchmark image places the package.
	PluginInstallDir = "/opt/ktx/opencode-plugin"
	// PluginSpec is the only OpenCode plugin spec the benchmark image registers.
	PluginSpec = "file://" + PluginInstallDir
	// PluginCommitFile records the commit the image copied the package from.
	PluginCommitFile = "/opt/ktx/opencode-plugin.commit"
)

// PluginFiles are the package files the benchmark image installs: the
// manifest and the server target. A headless benchmark never starts the TUI.
var PluginFiles = []string{"package.json", "server.ts"}

// PluginFilesSHA256 returns the package content hash of the named regular
// files under root. See PluginListingSHA256 for the definition.
func PluginFilesSHA256(root string, names []string) (string, error) {
	digests := make(map[string]string, len(names))
	for _, name := range names {
		if _, duplicate := digests[name]; duplicate {
			return "", fmt.Errorf("plugin package lists %s twice", name)
		}
		if err := checkPluginFileName(name); err != nil {
			return "", err
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("plugin package file %s is not a regular file", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256(data)
		digests[name] = hex.EncodeToString(digest[:])
	}
	return PluginListingSHA256(digests)
}

// PluginListingSHA256 returns the package content hash from each file's
// lowercase hex SHA-256, keyed by slash-separated relative path. The hash is
// the SHA-256 of a sha256sum-style listing: one line per file in byte order
// of its path, each the file digest, two spaces, the path, and a newline. It
// equals `sha256sum package.json server.ts | sha256sum` run in the folder.
func PluginListingSHA256(digests map[string]string) (string, error) {
	if len(digests) == 0 {
		return "", errors.New("plugin package has no files")
	}
	names := make([]string, 0, len(digests))
	for name, digest := range digests {
		if err := checkPluginFileName(name); err != nil {
			return "", err
		}
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return "", fmt.Errorf("plugin package file %s has an invalid digest", name)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	listing := sha256.New()
	for _, name := range names {
		fmt.Fprintf(listing, "%s  %s\n", digests[name], name)
	}
	return hex.EncodeToString(listing.Sum(nil)), nil
}

func checkPluginFileName(name string) error {
	if name == "" || strings.ContainsAny(name, "\n\\") || !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("plugin package file name %q is not a local slash-separated path", name)
	}
	return nil
}

// PluginDirSHA256 hashes every file of an installed package folder with the
// PluginFilesSHA256 definition. Extra files change the hash; subdirectories,
// symlinks, and other non-regular entries are rejected.
func PluginDirSHA256(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return "", fmt.Errorf("plugin folder entry %s is not a regular file", entry.Name())
		}
		names = append(names, entry.Name())
	}
	return PluginFilesSHA256(dir, names)
}
