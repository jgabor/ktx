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

//go:embed methodology-*.yaml
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
	name := "methodology-" + version + ".yaml"
	data, err := methodologies.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "tools/bench/" + name, fs.ErrNotExist
		}
		return nil, "tools/bench/" + name, err
	}
	return data, "tools/bench/" + name, nil
}
