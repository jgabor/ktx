package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplayFixtureRejectedDestinationRemainsIntact(t *testing.T) {
	assertRejectedDurableDestination(t, writeFileAtomically, "replay fixture")
}

func assertRejectedDurableDestination(t *testing.T, write func(string, []byte) error, want string) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "output.json")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "last-successful")
	if err := os.WriteFile(marker, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := write(destination, []byte("replacement")); err == nil {
		t.Fatal("write succeeded for directory destination")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != want {
		t.Fatalf("destination changed: %q, %v", data, err)
	}
}
