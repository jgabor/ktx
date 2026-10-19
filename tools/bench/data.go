// Package benchdata embeds the published benchmark methodologies in the ktx
// binary while keeping their source files available to external tooling.
package benchdata

import (
	"embed"
	"errors"
	"io/fs"
)

//go:embed specs/*.yaml
var methodologies embed.FS

//go:embed optimize/*
var optimization embed.FS

// OptimizationAssets returns the bounded controlled qualification inputs.
// These are deliberately independent from the mechanical screening generators.
func OptimizationAssets() (map[string][]byte, error) {
	assets := map[string][]byte{}
	for _, name := range []string{"qualification.toml", "qualification.txt"} {
		data, err := optimization.ReadFile("optimize/" + name)
		if err != nil {
			return nil, err
		}
		assets[name] = data
	}
	return assets, nil
}

// ReadMethodology returns a registered benchmark spec and its repository-relative path.
func ReadMethodology(id string) ([]byte, string, error) {
	if id == "" {
		id = "exact-match"
	}
	name := map[string]string{
		"exact-match":                 "specs/exact-match.yaml",
		"prompt-cache-token-hit-rate": "specs/prompt-cache-token-hit-rate.yaml",
		"recovery-aware-comparison":   "specs/recovery-aware-comparison.yaml",
	}[id]
	if name == "" {
		return nil, "", fs.ErrNotExist
	}
	data, err := methodologies.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "tools/bench/" + name, fs.ErrNotExist
		}
		return nil, "tools/bench/" + name, err
	}
	return data, "tools/bench/" + name, nil
}
