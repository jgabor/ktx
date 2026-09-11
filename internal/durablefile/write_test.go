package durablefile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

func TestWriteCheckedConflictAndRenameFailure(t *testing.T) {
	for _, failRename := range []bool{false, true} {
		t.Run(fmt.Sprint(failRename), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hooks.json")
			original := []byte(`{"hooks":{"SessionStart":[{"hooks":[{"command":"keep"}]}]}}`)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			previous := renameFile
			t.Cleanup(func() { renameFile = previous })
			checked, renamed := false, false
			renameFile = func(staged, target string) error {
				renamed = true
				if !checked {
					t.Error("rename preceded check")
				}
				data, err := os.ReadFile(staged)
				if err != nil || string(data) != `{"complete":true}` {
					t.Errorf("incomplete staged file: %s %v", data, err)
				}
				return syscall.EINTR
			}
			err := WriteChecked(path, []byte(`{"complete":true}`), 0o644, func() error {
				checked = true
				if !failRename {
					return errors.New("observed external edit")
				}
				return nil
			})
			if err == nil || !checked || renamed != failRename {
				t.Fatalf("checks bypassed: err=%v checked=%v renamed=%v", err, checked, renamed)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("shared hooks changed: %s %v", got, err)
			}
			staged, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".ktx-write-*"))
			if err != nil || len(staged) != 0 {
				t.Fatalf("staging leaked: %v %v", staged, err)
			}
		})
	}
}
