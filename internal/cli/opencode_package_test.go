package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/telemetry/opencode"
	"github.com/jgabor/ktx/internal/testjs"
)

// The OpenCode package in plugins/opencode is the only plugin source, and the
// binary carries no copy of it. These checks keep the binary free of the
// package, and keep the values the package shares with the binary equal on
// both sides.

const (
	opencodePackageServerPath   = "../../plugins/opencode/server.ts"
	opencodePackageManifestPath = "../../plugins/opencode/package.json"
)

// opencodePackageSharedValues lists, in their source context, the values the
// package hardcodes that the binary defines as constants.
func opencodePackageSharedValues() []string {
	return []string{
		`ack?.schema !== "` + telemetryAckSchema + `"`,
		`observation.runtime !== "` + opencode.Runtime + `" || observation.runtime_version !== "` + opencode.RuntimeVersion + `"`,
		`const required = process.env.` + benchcontract.CollectorModeEnv + ` === "` + benchcontract.CollectorRequired + `"`,
		`const requiredSessionID = process.env.` + benchcontract.SessionEnv + ` || ""`,
		`const requiredIntervalID = process.env.` + benchcontract.IntervalEnv + ` || ""`,
		`const requiredContract = process.env.` + benchcontract.ContractEnv + ` || ""`,
		`const side = process.env.` + benchcontract.ReadDescriptionEnv + "\n",
		`const directory = "` + benchcontract.ReadDescriptionDir + `"`,
		`digest(original) !== "` + benchcontract.ReadDescriptionOriginalSHA256 + `"`,
		`digest(candidate) !== "` + benchcontract.ReadDescriptionCandidateSHA256 + `"`,
		`digest(ast) !== "` + benchcontract.ReadParametersASTSHA256 + `"`,
		`version: "` + benchcontract.ReadDescriptionTreatment + `", boundary: "tool.definition", runtime: "` + benchcontract.RuntimeVersion + `"`,
		`owner: process.env.` + benchcontract.SessionEnv + `, interval: process.env.` + benchcontract.IntervalEnv + `,`,
	}
}

// checkOpencodePackageSharedValues requires each shared value exactly once, so
// a change on either side, or a second hardcoded copy, fails.
func checkOpencodePackageSharedValues(source string) error {
	var problems []error
	for _, want := range opencodePackageSharedValues() {
		if count := strings.Count(source, want); count != 1 {
			problems = append(problems, fmt.Errorf("OpenCode package source has %d copies of %q, want 1", count, want))
		}
	}
	return errors.Join(problems...)
}

// checkBinaryCarriesNoPlugins reports a package the binary links from the
// plugins folder. Go embeds only files at or below the embedding package, so a
// binary that links no package there carries no plugin source.
func checkBinaryCarriesNoPlugins(root string, packageDirs []string) error {
	pluginsDir := filepath.Join(root, "plugins")
	var problems []error
	for _, dir := range packageDirs {
		if relative, err := filepath.Rel(pluginsDir, dir); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			problems = append(problems, fmt.Errorf("the ktx binary links %s, which can embed a plugin source", dir))
		}
	}
	return errors.Join(problems...)
}

// opencodePluginOnlyMarkers are lines of the OpenCode package's server target
// that no ktx Go source needs.
var opencodePluginOnlyMarkers = []string{
	"output.output = envelope.output",
	"benchmarkReadDescription",
	"runOrdinaryProcess",
}

// checkSourcesCarryNoPlugin reports a source or embedded file of the binary
// that holds plugin code, such as the server target pasted in as a string.
func checkSourcesCarryNoPlugin(files map[string]string) error {
	var problems []error
	for name, content := range files {
		for _, marker := range opencodePluginOnlyMarkers {
			if strings.Contains(content, marker) {
				problems = append(problems, fmt.Errorf("%s carries OpenCode plugin code (%q)", name, marker))
			}
		}
	}
	return errors.Join(problems...)
}

func TestBinaryCarriesNoOpencodePlugin(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	list := exec.Command("go", "list", "-deps", "-f", "{{.Dir}}\t{{join .GoFiles \"\\t\"}}\t{{join .EmbedFiles \"\\t\"}}", "./cmd/ktx")
	list.Dir = root
	output, err := list.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var dirs []string
	files := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "\t")
		dir := fields[0]
		dirs = append(dirs, dir)
		if relative, err := filepath.Rel(root, dir); err != nil || strings.HasPrefix(relative, "..") {
			continue
		}
		for _, name := range fields[1:] {
			if name == "" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			files[filepath.Join(dir, name)] = string(data)
		}
	}
	if !slices.Contains(dirs, filepath.Join(root, "internal", "cli")) || files[filepath.Join(root, "internal", "cli", "init_opencode.go")] == "" {
		t.Fatalf("go list did not report the binary's packages and files: %q", output)
	}
	if err := checkBinaryCarriesNoPlugins(root, dirs); err != nil {
		t.Fatal(err)
	}
	if err := checkSourcesCarryNoPlugin(files); err != nil {
		t.Fatal(err)
	}

	for _, linked := range []string{filepath.Join(root, "plugins"), filepath.Join(root, "plugins", "opencode")} {
		if err := checkBinaryCarriesNoPlugins(root, append(slices.Clone(dirs), linked)); err == nil {
			t.Fatalf("a binary linking %s was accepted", linked)
		}
	}
	if err := checkBinaryCarriesNoPlugins(root, []string{filepath.Join(root, "pluginsx"), filepath.Join(root, "internal", "opencodeplugin")}); err != nil {
		t.Fatalf("packages outside the plugins folder were rejected: %v", err)
	}

	// The markers must stay in the plugin, and the plugin pasted into a source
	// as a string must fail.
	server := testjs.OpenCodeServer(t)
	for _, marker := range opencodePluginOnlyMarkers {
		if !strings.Contains(server, marker) {
			t.Fatalf("the OpenCode package server target no longer contains %q", marker)
		}
	}
	pasted := "package cli\n\nvar opencodePlugin = " + strconv.Quote(server) + "\n"
	if err := checkSourcesCarryNoPlugin(map[string]string{"pasted.go": pasted}); err == nil {
		t.Fatal("a source carrying the OpenCode plugin as a string was accepted")
	}
}

func TestOpencodePackageSharedValuesMatchBinary(t *testing.T) {
	source, err := os.ReadFile(opencodePackageServerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOpencodePackageSharedValues(string(source)); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ name, from, to string }{
		{"telemetry runtime version", `observation.runtime_version !== "` + opencode.RuntimeVersion + `"`, `observation.runtime_version !== "0.0.0"`},
		{"benchmark treatment", `version: "` + benchcontract.ReadDescriptionTreatment + `"`, `version: "read-other-v1"`},
		{"benchmark arm variable", `process.env.` + benchcontract.ReadDescriptionEnv, `process.env.KTX_BENCH_OTHER`},
		{"second copy", `digest(ast) !== "`, `digest(ast) !== "` + benchcontract.ReadParametersASTSHA256 + `" || digest(ast) !== "`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(string(source), test.from) {
				t.Fatalf("source lacks %q", test.from)
			}
			changed := strings.Replace(string(source), test.from, test.to, 1)
			if err := checkOpencodePackageSharedValues(changed); err == nil {
				t.Fatal("disagreeing shared value was accepted")
			}
		})
	}
}

// checkOpencodePackageEngines requires the package to claim exactly the
// OpenCode 1.x line whose plugin contract ktx supports, and to depend on no
// package from OpenCode's later plugin SDK.
func checkOpencodePackageEngines(manifest []byte) error {
	var pkg struct {
		Engines              map[string]string `json:"engines"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(manifest, &pkg); err != nil {
		return err
	}
	var problems []error
	if got, want := pkg.Engines["opencode"], ">="+opencode.RuntimeVersion+" <2"; got != want {
		problems = append(problems, fmt.Errorf("engines.opencode = %q, want %q", got, want))
	}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.PeerDependencies, pkg.OptionalDependencies} {
		for name := range deps {
			if strings.HasPrefix(name, "@opencode/") {
				problems = append(problems, fmt.Errorf("package depends on %s", name))
			}
		}
	}
	return errors.Join(problems...)
}

func TestOpencodePackageClaimsOnlyTheSupportedLine(t *testing.T) {
	manifest, err := os.ReadFile(opencodePackageManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOpencodePackageEngines(manifest); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, from, to string }{
		{"later major line", ` <2"`, ` <2 || >=2.0.20 <3"`},
		{"later plugin SDK", `"devDependencies": {`, `"devDependencies": {"@opencode/plugin": "2.0.20",`},
		{"other lower bound", `">=` + opencode.RuntimeVersion, `">=1.17.11`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !bytes.Contains(manifest, []byte(test.from)) {
				t.Fatalf("manifest lacks %q", test.from)
			}
			changed := bytes.Replace(manifest, []byte(test.from), []byte(test.to), 1)
			if err := checkOpencodePackageEngines(changed); err == nil {
				t.Fatal("a manifest claiming more than the supported line was accepted")
			}
		})
	}
}
