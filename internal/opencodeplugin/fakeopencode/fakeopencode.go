// Package fakeopencode answers `opencode plugin` the way OpenCode 1.19 does,
// on real config files, for tests that must not run OpenCode.
package fakeopencode

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jgabor/ktx/internal/opencodeplugin"
)

// Plugin edits the config of one global config folder and one project.
type Plugin struct {
	// Global is the global config folder and Project the project's .opencode
	// folder.
	Global, Project string
	// Duplicate adds every new spec twice, as a broken install would.
	Duplicate bool
}

// Run answers `opencode plugin [-g] <spec>`: the same spec already configured
// is left alone, an npm spec whose package name is already listed is left
// alone, and any other spec, such as a second folder of the same package, is
// appended to the server and TUI config. A global install creates
// opencode.jsonc and a project install opencode.json when neither exists.
func (p Plugin) Run(args []string) error {
	if len(args) < 2 || args[0] != "plugin" {
		return fmt.Errorf("unexpected call: %s", strings.Join(args, " "))
	}
	dir, created := p.Project, "opencode.json"
	rest := args[1:]
	if rest[0] == "-g" {
		dir, created = p.Global, "opencode.jsonc"
		rest = rest[1:]
	}
	if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
		return fmt.Errorf("unexpected call: %s", strings.Join(args, " "))
	}
	spec := rest[0]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, names := range [][]string{{"opencode.json", "opencode.jsonc", created}, {"tui.json", "tui.jsonc", "tui.json"}} {
		file := filepath.Join(dir, names[2])
		for _, existing := range names[:2] {
			if _, err := os.Stat(filepath.Join(dir, existing)); err == nil {
				file = filepath.Join(dir, existing)
				break
			}
		}
		if err := p.add(file, spec); err != nil {
			return err
		}
	}
	return nil
}

func (p Plugin) add(file, spec string) error {
	data, array, err := opencodeplugin.ReadPluginArray(file)
	if err != nil {
		return err
	}
	if data == nil {
		return os.WriteFile(file, []byte("{\n  \"plugin\": [\n    \""+spec+"\"\n  ]\n}\n"), 0o644)
	}
	if array == nil {
		return errors.New("fake opencode expects a plugin array")
	}
	name := opencodeplugin.PackageName
	for _, element := range array.Elements {
		sameName := !strings.HasPrefix(spec, "/") && (element.Spec == name || strings.HasPrefix(element.Spec, name+"@"))
		if opencodeplugin.SameSpec(opencodeplugin.Entry{File: file, Spec: element.Spec}, spec) || sameName {
			return nil
		}
	}
	insert := `"` + spec + `"`
	if p.Duplicate {
		insert += `, "` + spec + `"`
	}
	at := array.Open + 1
	if len(array.Elements) > 0 {
		at = array.Elements[len(array.Elements)-1].End
		insert = ", " + insert
	}
	return os.WriteFile(file, []byte(string(data[:at])+insert+string(data[at:])), 0o644)
}
