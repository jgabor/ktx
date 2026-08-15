//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package logging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRejectsPublicAndSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "ktx.db")
	if err := os.WriteFile(databasePath, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}

	public := filepath.Join(root, "public.log")
	if err := os.WriteFile(public, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o644); err != nil {
		t.Fatal(err)
	}
	AppendDatabaseUnavailable(Config{Level: Error, Path: public}, databasePath, testEvent(databasePath))
	if got, _ := os.ReadFile(public); string(got) != "before" {
		t.Fatalf("public file changed: %q", got)
	}

	link := filepath.Join(root, "events.log")
	if err := os.Symlink(databasePath, link); err != nil {
		t.Fatal(err)
	}
	AppendDatabaseUnavailable(Config{Level: Error, Path: link}, databasePath, testEvent(databasePath))
	if got, _ := os.ReadFile(databasePath); string(got) != "database" {
		t.Fatalf("symlink target changed: %q", got)
	}
}
