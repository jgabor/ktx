package binarycap

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectCanonicalizesAndHashesExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ktx-real")
	contents := []byte("fixture executable bytes\n")
	if err := os.WriteFile(target, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "ktx")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got, err := Inspect(link, "test-version", 8)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(contents))
	if got.Schema != Schema || got.ExecutablePath != target || got.ExecutableSHA256 != wantDigest ||
		got.BuildVersion != "test-version" || got.MaxDatabaseSchema != 8 {
		t.Fatalf("identity = %#v", got)
	}
}

func TestInspectRejectsUnverifiableExecutables(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(*testing.T) string
		want string
	}{
		{
			name: "missing target",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") },
			want: "canonicalize executable path",
		},
		{
			name: "directory is not an executable file",
			path: func(t *testing.T) string { return t.TempDir() },
			want: "not a regular file",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Inspect(test.path(t), "test-version", 8); err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("Inspect error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidDigestRequiresCanonicalSHA256(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if !ValidDigest(valid) {
		t.Fatal("canonical digest rejected")
	}
	for _, invalid := range []string{"", valid[:63], valid + "0", "G" + valid[1:], "A" + valid[1:]} {
		if ValidDigest(invalid) {
			t.Fatalf("invalid digest accepted: %q", invalid)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
