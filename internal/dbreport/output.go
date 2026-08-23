package dbreport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jgabor/ktx/internal/durablefile"
)

func MarshalJSON(report map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func WriteJSON(path string, report map[string]any) error {
	data, err := MarshalJSON(report)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := durablefile.Write(path, data, 0o644); err != nil {
		return fmt.Errorf("replace report: %w", err)
	}
	return nil
}
