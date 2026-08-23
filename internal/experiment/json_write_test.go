package experiment

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteJSONRejectedDestinationRemainsIntact(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "summary.json")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "last-successful")
	if err := os.WriteFile(marker, []byte("experiment"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(destination, map[string]any{"new": true}); err == nil {
		t.Fatal("WriteJSON succeeded for directory destination")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "experiment" {
		t.Fatalf("destination changed: %q, %v", data, err)
	}
}
