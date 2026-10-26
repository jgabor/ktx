//go:build mage

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/opencodeplugin"
	"github.com/jgabor/ktx/internal/opencodeplugin/fakeopencode"
)

// fakeOpencodeCLI answers `opencode plugin` the way OpenCode 1.19 does, on
// real config files (see fakeopencode), answers `npm view`, and hands every
// other call to next.
type fakeOpencodeCLI struct {
	global, project string
	published       bool
	duplicate       bool
	failOn          string
	calls           []string
	next            installRunner
}

func (f *fakeOpencodeCLI) run(name string, args ...string) (string, string, error) {
	call := strings.Join(args, " ")
	switch name {
	case "npm":
		f.calls = append(f.calls, "npm "+call)
		if f.published && call == "view @jgabor/ktx@0.2.0 version --json" {
			return "\"0.2.0\"\n", "", nil
		}
		return `{"error":{"code":"E404"}}`, "npm error code E404", errors.New("exit 1")
	case "/bin/opencode":
		f.calls = append(f.calls, "opencode "+call)
		if call == f.failOn {
			return "", "network unreachable", errors.New("exit 1")
		}
		if err := (fakeopencode.Plugin{Global: f.global, Project: f.project, Duplicate: f.duplicate}).Run(args); err != nil {
			return "", err.Error(), err
		}
		return "", "", nil
	}
	if f.next == nil {
		return "", "unexpected call", errors.New("exit 2")
	}
	return f.next(name, args...)
}

// opencodeFixture is a disposable global config folder, project and package
// folders for the OpenCode tests.
type opencodeFixture struct {
	global, project, checkout, otherCheckout string
	scopes                                   []opencodeplugin.Scope
}

func newOpencodeFixture(t *testing.T) opencodeFixture {
	t.Helper()
	root := t.TempDir()
	f := opencodeFixture{
		global:        filepath.Join(root, "config", "opencode"),
		project:       filepath.Join(root, "project"),
		checkout:      filepath.Join(root, "ktx", "plugins", "opencode"),
		otherCheckout: filepath.Join(root, "old-ktx", "plugins", "opencode"),
	}
	for _, dir := range []string{f.global, f.project, f.checkout, f.otherCheckout} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{f.checkout, f.otherCheckout} {
		writeFile(t, filepath.Join(dir, "package.json"), `{"name":"@jgabor/ktx","exports":{"./server":"./server.ts"}}`)
	}
	f.scopes = []opencodeplugin.Scope{opencodeplugin.GlobalScope(f.global), opencodeplugin.ProjectScope(f.project)}
	return f
}

func (f opencodeFixture) fake() *fakeOpencodeCLI {
	return &fakeOpencodeCLI{global: f.global, project: filepath.Join(f.project, ".opencode")}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// pluginSpecs lists every spec in the plugin arrays of files, in order.
func pluginSpecs(t *testing.T, files ...string) []string {
	t.Helper()
	specs := []string{}
	for _, file := range files {
		_, array, err := opencodeplugin.ReadPluginArray(file)
		if err != nil {
			t.Fatal(err)
		}
		if array != nil {
			for _, element := range array.Elements {
				specs = append(specs, element.Spec)
			}
		}
	}
	return specs
}

// requireOneKtxEntry fails unless the files hold exactly one ktx entry, for
// want, and the other entries are others, in order.
func requireOneKtxEntry(t *testing.T, checkout, want string, others []string, files ...string) {
	t.Helper()
	var ktx, rest []string
	for _, file := range files {
		for _, spec := range pluginSpecs(t, file) {
			if opencodeplugin.IsKtxSpec(spec, filepath.Dir(file), checkout) {
				if !opencodeplugin.SameSpec(opencodeplugin.Entry{File: file, Spec: spec}, want) {
					t.Errorf("ktx entry %q in %s, want %q", spec, file, want)
				}
				ktx = append(ktx, spec)
			} else {
				rest = append(rest, spec)
			}
		}
	}
	if len(ktx) != 1 {
		t.Errorf("ktx entries = %q, want exactly one %q", ktx, want)
	}
	if !reflect.DeepEqual(rest, others) && !(len(rest) == 0 && len(others) == 0) {
		t.Errorf("other entries = %q, want %q", rest, others)
	}
}

// otherSpecs lists the specs in files that are not ktx entries, in order.
func otherSpecs(t *testing.T, checkout string, files ...string) []string {
	t.Helper()
	var specs []string
	for _, file := range files {
		for _, spec := range pluginSpecs(t, file) {
			if !opencodeplugin.IsKtxSpec(spec, filepath.Dir(file), checkout) {
				specs = append(specs, spec)
			}
		}
	}
	return specs
}

// legacyOpencodePlugin is the first plugin file ktx generated for OpenCode.
const legacyOpencodePlugin = "import type { Plugin } from \"@opencode-ai/plugin\"\n\nconst KtxPlugin: Plugin = async () => {\n  return {\n    \"tool.execute.after\": async (input: any, output: any) => {\n      if (process.env.KTX_SESSION_ID) return\n    },\n  }\n}\n\nexport default KtxPlugin\n"

func TestReadOpencodeSetup(t *testing.T) {
	f := newOpencodeFixture(t)
	writeFile(t, filepath.Join(f.global, "opencode.json"), `{"plugin": ["other-plugin", "/elsewhere/plugin"]}`)
	userPlugin := filepath.Join(f.global, "plugins", "ktx.ts")
	writeFile(t, userPlugin, "export default async () => ({})\n")
	var output strings.Builder
	setup, err := readOpencodeSetup("/bin/opencode", f.scopes, f.checkout, &output)
	if err != nil || setup != nil || !strings.Contains(output.String(), "OpenCode: ktx is not set up") {
		t.Fatalf("not set up = %+v, %v, %q", setup, err, output.String())
	}
	if readFile(t, userPlugin) != "export default async () => ({})\n" {
		t.Fatal("reading changed someone else's ktx.ts")
	}

	writeFile(t, filepath.Join(f.project, ".opencode", "plugins", "ktx.ts"), legacyOpencodePlugin)
	writeFile(t, filepath.Join(f.global, "tui.json"), `{"plugin": ["`+f.otherCheckout+`"]}`)
	setup, err = readOpencodeSetup("/bin/opencode", f.scopes, f.checkout, &output)
	if err != nil || setup == nil || len(setup.scopes) != 2 {
		t.Fatalf("set up = %+v, %v", setup, err)
	}
	if global := setup.scopes[0]; len(global.Server) != 0 || len(global.TUI) != 1 || global.Legacy {
		t.Fatalf("global = %+v", global)
	}
	if project := setup.scopes[1]; len(project.Server)+len(project.TUI) != 0 || !project.Legacy {
		t.Fatalf("project = %+v", project)
	}

	output.Reset()
	if setup, err := readOpencodeSetup("", f.scopes, f.checkout, &output); setup != nil || err != nil || !strings.Contains(output.String(), "opencode not found on PATH") {
		t.Fatalf("without opencode = %+v, %v, %q", setup, err, output.String())
	}
	writeFile(t, filepath.Join(f.global, "opencode.json"), `{"plugin": [`)
	if _, err := readOpencodeSetup("/bin/opencode", f.scopes, f.checkout, &output); err == nil {
		t.Fatal("a malformed config passed")
	}
}

func TestUseOpencodePluginSwitchesSources(t *testing.T) {
	const release = "@jgabor/ktx@0.2.0"
	type seed map[string]string // path relative to the fixture -> content
	cases := []struct {
		name   string
		seed   func(f opencodeFixture) seed
		target func(f opencodeFixture) string
		calls  func(f opencodeFixture, target string) []string
	}{
		{
			name: "release to checkout",
			seed: func(f opencodeFixture) seed {
				return seed{
					"global/opencode.json": "{\n  // mine\n  \"plugin\": [\n    \"other-plugin\",\n    \"@jgabor/ktx@0.1.0\",\n    [\"tuple-plugin\", {\"a\": 1}],\n  ],\n}\n",
					"global/tui.json":      `{"plugin": ["other-tui", "@jgabor/ktx@0.1.0"]}`,
				}
			},
			target: func(f opencodeFixture) string { return f.checkout },
			calls: func(f opencodeFixture, target string) []string {
				return []string{"opencode plugin -g " + target}
			},
		},
		{
			name: "checkout to release",
			seed: func(f opencodeFixture) seed {
				return seed{
					"global/opencode.jsonc": `{"plugin": ["other-plugin", "` + f.checkout + `"]}`,
					"global/tui.json":       `{"plugin": ["` + f.checkout + `", "other-tui"]}`,
				}
			},
			target: func(opencodeFixture) string { return release },
			calls: func(f opencodeFixture, target string) []string {
				return []string{"opencode plugin -g " + target}
			},
		},
		{
			name: "duplicate entries become one",
			seed: func(f opencodeFixture) seed {
				return seed{
					".opencode/opencode.json": `{"plugin": ["file://` + f.otherCheckout + `", "other-plugin", "@jgabor/ktx", "` + f.checkout + `"]}`,
					".opencode/tui.json":      `{"plugin": ["` + f.checkout + `", "` + f.checkout + `", "other-tui"]}`,
					"opencode.json":           `{"plugin": ["@jgabor/ktx@0.1.0"]}`,
				}
			},
			target: func(f opencodeFixture) string { return f.checkout },
			calls: func(f opencodeFixture, target string) []string {
				return []string{"opencode plugin " + target}
			},
		},
		{
			name: "earlier generated plugin only, both scopes",
			seed: func(f opencodeFixture) seed {
				return seed{"global/plugins/ktx.ts": legacyOpencodePlugin, ".opencode/plugins/ktx.ts": legacyOpencodePlugin}
			},
			target: func(opencodeFixture) string { return release },
			calls: func(f opencodeFixture, target string) []string {
				return []string{"opencode plugin -g " + target, "opencode plugin " + target}
			},
		},
		{
			name: "already on the checkout",
			seed: func(f opencodeFixture) seed {
				return seed{
					"global/opencode.json": `{"plugin": ["other-plugin", "file://` + f.checkout + `"]}`,
					"global/tui.json":      `{"plugin": ["` + f.checkout + `", "other-tui"]}`,
				}
			},
			target: func(f opencodeFixture) string { return f.checkout },
			calls:  func(opencodeFixture, string) []string { return nil },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newOpencodeFixture(t)
			seeded := c.seed(f)
			resolve := func(relative string) string {
				if rest, ok := strings.CutPrefix(relative, "global/"); ok {
					return filepath.Join(f.global, rest)
				}
				return filepath.Join(f.project, relative)
			}
			for relative, content := range seeded {
				writeFile(t, resolve(relative), content)
			}
			before := map[string]string{}
			for relative := range seeded {
				before[relative] = readFile(t, resolve(relative))
			}
			var output strings.Builder
			setup, err := readOpencodeSetup("/bin/opencode", f.scopes, f.checkout, &output)
			if err != nil || setup == nil {
				t.Fatalf("setup = %+v, %v", setup, err)
			}
			others := map[string][2][]string{}
			for _, state := range setup.scopes {
				others[state.Scope.Name] = [2][]string{otherSpecs(t, f.checkout, state.Scope.Server...), otherSpecs(t, f.checkout, state.Scope.TUI...)}
			}
			fake := f.fake()
			target := c.target(f)
			if err := useOpencodePlugin(*setup, target, fake.run, &output); err != nil {
				t.Fatal(err)
			}
			if want := c.calls(f, target); !reflect.DeepEqual(fake.calls, want) {
				t.Fatalf("calls = %q, want %q", fake.calls, want)
			}
			for _, state := range setup.scopes {
				scope := state.Scope
				otherServer, otherTUI := others[scope.Name][0], others[scope.Name][1]
				requireOneKtxEntry(t, f.checkout, target, otherServer, scope.Server...)
				requireOneKtxEntry(t, f.checkout, target, otherTUI, scope.TUI...)
				if _, err := os.Stat(scope.Legacy); !os.IsNotExist(err) {
					t.Errorf("generated plugin %s remains: %v", scope.Legacy, err)
				}
			}
			if c.calls(f, target) == nil {
				for relative, content := range before {
					if readFile(t, resolve(relative)) != content {
						t.Errorf("%s changed although it already pointed at the target", relative)
					}
				}
				if !strings.Contains(output.String(), "already loads from "+target) {
					t.Fatalf("output = %q", output.String())
				}
				return
			}
			if !strings.Contains(output.String(), "now loads from "+target) {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}

func TestUseOpencodePluginKeepsOtherEntriesAndComments(t *testing.T) {
	f := newOpencodeFixture(t)
	server := filepath.Join(f.global, "opencode.json")
	writeFile(t, server, "{\n  // mine\n  \"plugin\": [\n    \"other-plugin\",\n    \"@jgabor/ktx@0.1.0\",\n    [\"tuple-plugin\", {\"a\": 1}],\n  ],\n}\n")
	writeFile(t, filepath.Join(f.global, "tui.json"), `{"plugin": ["@jgabor/ktx@0.1.0"]}`)
	setup, err := readOpencodeSetup("/bin/opencode", f.scopes, f.checkout, &strings.Builder{})
	if err != nil || setup == nil {
		t.Fatalf("setup = %+v, %v", setup, err)
	}
	if err := useOpencodePlugin(*setup, f.checkout, f.fake().run, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  // mine\n  \"plugin\": [\n    \"other-plugin\",\n    [\"tuple-plugin\", {\"a\": 1}], \"" + f.checkout + "\",\n  ],\n}\n"
	if got := readFile(t, server); got != want {
		t.Fatalf("server config = %q, want %q", got, want)
	}
	requireOneKtxEntry(t, f.checkout, f.checkout, []string{"other-plugin", "tuple-plugin"}, server)
}

func TestUseOpencodePluginFailsOnADuplicateEntry(t *testing.T) {
	f := newOpencodeFixture(t)
	writeFile(t, filepath.Join(f.global, "opencode.json"), `{"plugin": ["@jgabor/ktx@0.1.0"]}`)
	writeFile(t, filepath.Join(f.global, "tui.json"), `{"plugin": ["@jgabor/ktx@0.1.0"]}`)
	setup, err := readOpencodeSetup("/bin/opencode", f.scopes, f.checkout, &strings.Builder{})
	if err != nil || setup == nil {
		t.Fatalf("setup = %+v, %v", setup, err)
	}
	fake := f.fake()
	fake.duplicate = true
	var output strings.Builder
	err = useOpencodePlugin(*setup, f.checkout, fake.run, &output)
	if err == nil || !strings.Contains(err.Error(), "2 ktx server entries and 2 ktx TUI entries") {
		t.Fatalf("duplicate entry = %v", err)
	}
	if strings.Contains(output.String(), "now loads") {
		t.Fatalf("a duplicate entry claimed success: %q", output.String())
	}
}

func TestUseOpencodePluginFailureRestoresTheConfig(t *testing.T) {
	f := newOpencodeFixture(t)
	legacy := filepath.Join(f.global, "plugins", "ktx.ts")
	writeFile(t, legacy, legacyOpencodePlugin)
	server := filepath.Join(f.global, "opencode.json")
	const pinned = `{"plugin": ["other", "@jgabor/ktx@0.1.0"]}`
	writeFile(t, server, pinned)
	setup, err := readOpencodeSetup("/bin/opencode", f.scopes, f.checkout, &strings.Builder{})
	if err != nil || setup == nil {
		t.Fatalf("setup = %+v, %v", setup, err)
	}
	fake := f.fake()
	fake.failOn = "plugin -g @jgabor/ktx@0.2.0"
	var output strings.Builder
	err = useOpencodePlugin(*setup, "@jgabor/ktx@0.2.0", fake.run, &output)
	if err == nil {
		t.Fatal("switch succeeded")
	}
	for _, required := range []string{"network unreachable", "every OpenCode config file is back as it was"} {
		if !strings.Contains(err.Error(), required) {
			t.Errorf("failure lacks %q: %v", required, err)
		}
	}
	if strings.Contains(err.Error(), "Recovery:") {
		t.Errorf("a restored config still asks for recovery: %v", err)
	}
	if got := readFile(t, server); got != pinned {
		t.Fatalf("server config = %q, want the earlier pin kept", got)
	}
	if output.Len() != 0 {
		t.Fatalf("failure claimed success: %q", output.String())
	}
	if readFile(t, legacy) != legacyOpencodePlugin {
		t.Fatal("the generated plugin was removed although the switch failed")
	}
}

// opencodeReleaseFixture sets up OpenCode on this checkout's package in the
// global scope and returns the runtimes installRelease sees, with codex and
// claude off PATH.
func opencodeReleaseFixture(t *testing.T) (opencodeFixture, releaseRuntimes) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	f := newOpencodeFixture(t)
	writeFile(t, filepath.Join(f.global, "opencode.json"), `{"plugin": ["other-plugin", "`+f.checkout+`"]}`)
	writeFile(t, filepath.Join(f.global, "tui.json"), `{"plugin": ["`+f.checkout+`"]}`)
	return f, releaseRuntimes{opencode: "/bin/opencode", opencodeScopes: f.scopes, opencodePackage: f.checkout}
}

func TestInstallReleasePinsOpencodeToTheRelease(t *testing.T) {
	f, runtimes := opencodeReleaseFixture(t)
	codex := fakeCodexCLI{releaseZip: writeReleaseZip(t, "plugins/opencode/package.json")}
	fake := f.fake()
	fake.published, fake.next = true, codex.run
	var output strings.Builder
	if err := installRelease(fake.run, runtimes, &output); err != nil {
		t.Fatal(err)
	}
	if want := []string{"go install github.com/jgabor/ktx/cmd/ktx@v0.2.0"}; !reflect.DeepEqual(codex.changes, want) {
		t.Fatalf("changes = %q, want %q", codex.changes, want)
	}
	if want := []string{"npm view @jgabor/ktx@0.2.0 version --json", "opencode plugin -g @jgabor/ktx@0.2.0"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %q, want %q", fake.calls, want)
	}
	requireOneKtxEntry(t, f.checkout, "@jgabor/ktx@0.2.0", []string{"other-plugin"}, filepath.Join(f.global, "opencode.json"))
	requireOneKtxEntry(t, f.checkout, "@jgabor/ktx@0.2.0", nil, filepath.Join(f.global, "tui.json"))
	if !strings.Contains(output.String(), "now loads from @jgabor/ktx@0.2.0") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestInstallReleaseStopsWhenTheReleaseHasNoOpencodePackage(t *testing.T) {
	for name, c := range map[string]struct {
		zip       []string
		published bool
		want      string
	}{
		"release without the package": {[]string{".claude-plugin/marketplace.json"}, true, "v0.2.0, has no OpenCode plugin (missing plugins/opencode/package.json)"},
		"package not on npm":          {[]string{"plugins/opencode/package.json"}, false, "v0.2.0, has no OpenCode package on npm (@jgabor/ktx@0.2.0: not found on the npm registry)"},
	} {
		t.Run(name, func(t *testing.T) {
			f, runtimes := opencodeReleaseFixture(t)
			before := readFile(t, filepath.Join(f.global, "opencode.json")) + readFile(t, filepath.Join(f.global, "tui.json"))
			codex := fakeCodexCLI{releaseZip: writeReleaseZip(t, c.zip...)}
			fake := f.fake()
			fake.published, fake.next = c.published, codex.run
			var output strings.Builder
			err := installRelease(fake.run, runtimes, &output)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "changed nothing") {
				t.Fatalf("installRelease = %v", err)
			}
			if len(codex.changes) != 0 {
				t.Fatalf("installRelease changed something before stopping: %q", codex.changes)
			}
			for _, call := range fake.calls {
				if strings.HasPrefix(call, "opencode") {
					t.Fatalf("installRelease ran %q before stopping", call)
				}
			}
			if after := readFile(t, filepath.Join(f.global, "opencode.json")) + readFile(t, filepath.Join(f.global, "tui.json")); after != before {
				t.Fatalf("OpenCode config changed: %q", after)
			}
		})
	}
}

func TestInstallReleaseLeavesOpencodeAloneWhenNotSetUp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	f := newOpencodeFixture(t)
	server := filepath.Join(f.global, "opencode.json")
	writeFile(t, server, `{"plugin": ["other-plugin"]}`)
	codex := fakeCodexCLI{}
	fake := f.fake()
	fake.next = codex.run
	var output strings.Builder
	if err := installRelease(fake.run, releaseRuntimes{opencode: "/bin/opencode", opencodeScopes: f.scopes, opencodePackage: f.checkout}, &output); err != nil {
		t.Fatal(err)
	}
	if want := []string{"go install github.com/jgabor/ktx/cmd/ktx@latest"}; !reflect.DeepEqual(codex.changes, want) {
		t.Fatalf("changes = %q, want %q", codex.changes, want)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("OpenCode calls = %q", fake.calls)
	}
	if readFile(t, server) != `{"plugin": ["other-plugin"]}` || !strings.Contains(output.String(), "OpenCode: ktx is not set up") {
		t.Fatalf("config = %q output = %q", readFile(t, server), output.String())
	}
	entries, err := os.ReadDir(f.global)
	if err != nil || len(entries) != 1 {
		t.Fatalf("global config folder = %v, %v", entries, err)
	}
}
