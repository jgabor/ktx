//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package logging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotationMakesActiveAndPreviousPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxFileBytes); err != nil {
		t.Fatal(err)
	}

	New(Error, path, "").Write(testEvent{level: Error})

	for _, candidate := range []string{path, path + ".1"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode of %s = %#o, want 0600", candidate, got)
		}
	}
}
