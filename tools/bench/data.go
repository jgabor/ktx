// Package benchdata embeds the published benchmark methodologies in the ktx
// binary while keeping their source files available to external tooling.
package benchdata

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
)

//go:embed methodology-v1.yaml methodology-v2-tooluse.yaml methodology-v3.yaml specs/eval-matrix.yaml specs/cache-aware.yaml specs/prompt-cache-token-hit-rate.yaml
var methodologies embed.FS

// ReadMethodology returns a published methodology and its repository-relative
// source path. An empty version selects v1.
func ReadMethodology(version string) ([]byte, string, error) {
	if version == "" {
		version = "v1"
	}
	if path.Base(version) != version || version == "." {
		return nil, "", fmt.Errorf("invalid methodology version %q", version)
	}
	name := map[string]string{
		"v4-eval-matrix":                 "specs/eval-matrix.yaml",
		"v5-cache-aware":                 "specs/cache-aware.yaml",
		"v6-prompt-cache-token-hit-rate": "specs/prompt-cache-token-hit-rate.yaml",
	}[version]
	if name == "" {
		name = "methodology-" + version + ".yaml"
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
