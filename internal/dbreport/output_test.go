package dbreport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteJSONRejectedDestinationRemainsIntact(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "report.json")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "last-successful")
	if err := os.WriteFile(marker, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(destination, map[string]any{"new": true}); err == nil {
		t.Fatal("WriteJSON succeeded for directory destination")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "report" {
		t.Fatalf("destination changed: %q, %v", data, err)
	}
}
