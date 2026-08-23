package durablefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesCompleteFileAndPreservesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("complete"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete" {
		t.Fatalf("read = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestWriteRejectedReplacementLeavesDestinationValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o644); err == nil {
		t.Fatal("Write succeeded for directory destination")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("destination changed: %v, %v", info, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".ktx-write-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, err)
	}
}

func TestWriteInterruptedBeforeRenameLeavesLastArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, []byte("last successful"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalRename := renameFile
	renameFile = func(string, string) error { return errors.New("interrupted") }
	t.Cleanup(func() { renameFile = originalRename })
	if err := Write(path, []byte("partial replacement"), 0o644); err == nil {
		t.Fatal("Write succeeded")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "last successful" {
		t.Fatalf("read = %q, %v", data, err)
	}
}
