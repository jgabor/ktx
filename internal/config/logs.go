package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// LogConfig is the validated optional [logs] table. Path is empty when the
// default state-directory destination is selected.
type LogConfig struct {
	Level string
	Path  string
}

type tomlLogs struct {
	Level *string `toml:"level"`
	Path  *string `toml:"path"`
}

// LoadLogs resolves logging independently from ordinary configuration. Callers
// disable logging when this optional section is invalid.
func LoadLogs() (*LogConfig, error) {
	path, exists := ConfigPath()
	if !exists {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Logs toml.Primitive `toml:"logs"`
	}
	metadata, err := toml.Decode(string(data), &decoded)
	if err != nil {
		return nil, err
	}
	if !metadata.IsDefined("logs") {
		return nil, nil
	}
	var raw tomlLogs
	if err := metadata.PrimitiveDecode(decoded.Logs, &raw); err != nil {
		return nil, fmt.Errorf("[logs]: %w", err)
	}
	for _, key := range metadata.Undecoded() {
		if len(key) > 0 && key[0] == "logs" {
			return nil, fmt.Errorf("[logs] unknown setting %q", key.String())
		}
	}
	if raw.Level == nil || *raw.Level == "" {
		return nil, fmt.Errorf("[logs] level is required")
	}
	switch *raw.Level {
	case "debug", "info", "warn", "error", "off":
	default:
		return nil, fmt.Errorf("[logs] level %q is invalid", *raw.Level)
	}
	path = ""
	if raw.Path != nil {
		path = *raw.Path
		if path == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("[logs] path must be absolute")
		}
		path = filepath.Clean(path)
	}
	return &LogConfig{Level: *raw.Level, Path: path}, nil
}
