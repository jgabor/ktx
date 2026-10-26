package benchcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writePluginFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func hexSHA256(data string) string {
	digest := sha256.Sum256([]byte(data))
	return hex.EncodeToString(digest[:])
}

func TestPluginFilesSHA256MatchesSha256sumListing(t *testing.T) {
	dir := writePluginFiles(t, map[string]string{"package.json": "{}\n", "server.ts": "export {}\n"})
	listing := hexSHA256("{}\n") + "  package.json\n" + hexSHA256("export {}\n") + "  server.ts\n"
	want := hexSHA256(listing)
	got, err := PluginFilesSHA256(dir, []string{"server.ts", "package.json"})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
	folder, err := PluginDirSHA256(dir)
	if err != nil || folder != want {
		t.Fatalf("folder hash = %s, %v; want %s", folder, err, want)
	}
}

func TestPluginDirSHA256DetectsExtraFilesAndRejectsLinks(t *testing.T) {
	dir := writePluginFiles(t, map[string]string{"package.json": "{}\n", "server.ts": "export {}\n"})
	files, err := PluginFilesSHA256(dir, PluginFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tui.js"), []byte("extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	folder, err := PluginDirSHA256(dir)
	if err != nil {
		t.Fatal(err)
	}
	if folder == files {
		t.Fatal("an extra installed file did not change the package hash")
	}
	if err := os.Symlink("server.ts", filepath.Join(dir, "link.ts")); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginDirSHA256(dir); err == nil {
		t.Fatal("a symlink in the installed package was accepted")
	}
}

func TestPluginFilesSHA256RejectsInvalidNames(t *testing.T) {
	dir := writePluginFiles(t, map[string]string{"server.ts": "export {}\n"})
	for _, names := range [][]string{nil, {"server.ts", "server.ts"}, {"../server.ts"}, {"missing.ts"}} {
		if _, err := PluginFilesSHA256(dir, names); err == nil {
			t.Errorf("names %q accepted", names)
		}
	}
}
