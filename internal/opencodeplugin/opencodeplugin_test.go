package opencodeplugin_test

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

// legacyPlugin is the first plugin file ktx generated for OpenCode.
const legacyPlugin = "import type { Plugin } from \"@opencode-ai/plugin\"\n\nconst KtxPlugin: Plugin = async () => {\n  return {\n    \"tool.execute.after\": async (input: any, output: any) => {\n      if (process.env.KTX_SESSION_ID) return\n    },\n  }\n}\n\nexport default KtxPlugin\n"

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

func TestGeneratedPluginMarkers(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		path string
		want bool
	}{
		"the package server target":  {filepath.Join("..", "..", "plugins", "opencode", "server.ts"), true},
		"the first generated plugin": {filepath.Join(dir, "first.ts"), true},
		"someone else's ktx.ts":      {filepath.Join(dir, "user.ts"), false},
		"absent":                     {filepath.Join(dir, "absent.ts"), false},
	}
	writeFile(t, cases["the first generated plugin"].path, legacyPlugin)
	writeFile(t, cases["someone else's ktx.ts"].path, "export default async () => ({})\n")
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := opencodeplugin.IsGeneratedPlugin(c.path)
			if err != nil || got != c.want {
				t.Fatalf("IsGeneratedPlugin = %t, %v; want %t", got, err, c.want)
			}
		})
	}
}

func TestRemoveArrayElementKeepsOtherBytes(t *testing.T) {
	const jsonc = "{\n  // keep me\n  \"plugin\": [\n    \"a\",\n    [\"t\", {\"x\": 1}], /* tuple */\n    \"k\",\n  ],\n}\n"
	cases := []struct {
		name, in, want string
		index          int
	}{
		{"middle", `{"plugin": ["a", "k", "b"]}`, `{"plugin": ["a", "b"]}`, 1},
		{"first", `{"plugin": ["k", "a"], "x": 1}`, `{"plugin": ["a"], "x": 1}`, 0},
		{"last", `{"plugin": ["a", "k"]}`, `{"plugin": ["a"]}`, 1},
		{"only", "{\"plugin\": [\n  \"k\"\n]}", `{"plugin": []}`, 0},
		{"only, trailing comma", `{"plugin": ["k",]}`, `{"plugin": []}`, 0},
		{"tuple", `{"plugin": [["k", {"a": [1]}], "a"]}`, `{"plugin": ["a"]}`, 0},
		{"comments and trailing commas", jsonc, "{\n  // keep me\n  \"plugin\": [\n    \"a\",\n    [\"t\", {\"x\": 1}], /* tuple */\n  ],\n}\n", 2},
		{"trailing comment of the previous entry", "{\"plugin\": [\n  \"keep-a\", // about keep-a\n  \"@jgabor/ktx\"\n]}", "{\"plugin\": [\n  \"keep-a\" // about keep-a\n]}", 1},
		{"leading comment of the next entry", "{\"plugin\": [\n  \"@jgabor/ktx\", // ktx\n  // comment about next\n  \"next\"\n]}", "{\"plugin\": [\n  // comment about next\n  \"next\"\n]}", 0},
		{"inline comments of other entries", `{"plugin": ["a" /* a */, "k", /* b */ "b"]}`, `{"plugin": ["a" /* a */, /* b */ "b"]}`, 1},
		{"only entry on its own line", "{\"plugin\": [\n  \"k\", // ktx\n]}", `{"plugin": []}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			array, err := opencodeplugin.FindPluginArray([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			got := string(opencodeplugin.RemoveArrayElement([]byte(c.in), *array, c.index))
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			if _, err := opencodeplugin.FindPluginArray([]byte(got)); err != nil {
				t.Fatalf("result does not parse: %v", err)
			}
		})
	}
	array, err := opencodeplugin.FindPluginArray([]byte(jsonc))
	if err != nil {
		t.Fatal(err)
	}
	var specs []string
	for _, element := range array.Elements {
		specs = append(specs, element.Spec)
	}
	if want := []string{"a", "t", "k"}; !reflect.DeepEqual(specs, want) {
		t.Fatalf("specs = %q, want %q", specs, want)
	}
	for _, bad := range []string{`{"plugin": "k"}`, `{"plugin": ["k"`, `{"plugin": [] /* open`, `["k"]`, `{"plugin": [x]}`, `{"plugin": []} extra`} {
		if _, err := opencodeplugin.FindPluginArray([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestIsKtxSpec(t *testing.T) {
	root := t.TempDir()
	otherCheckout := filepath.Join(root, "old-ktx", "plugins", "opencode")
	writeFile(t, filepath.Join(otherCheckout, "package.json"), `{"name":"@jgabor/ktx"}`)
	missingCheckout := filepath.Join(t.TempDir(), "gone")
	for spec, want := range map[string]bool{
		"@jgabor/ktx":                true,
		"@jgabor/ktx@0.2.0":          true,
		otherCheckout:                true,
		"file://" + otherCheckout:    true,
		"./old-ktx/plugins/opencode": true,
		"@jgabor/ktx-other":          false,
		"opencode-ktx":               false,
		"/elsewhere/plugin":          false,
		filepath.Dir(otherCheckout):  false,
	} {
		if got := opencodeplugin.IsKtxSpec(spec, root, missingCheckout); got != want {
			t.Errorf("IsKtxSpec(%q) = %t, want %t", spec, got, want)
		}
	}
	if !opencodeplugin.IsKtxSpec(missingCheckout, "/", missingCheckout) {
		t.Error("the checkout package folder did not count when it has no package.json")
	}
	if opencodeplugin.IsKtxSpec("./", ".", "") {
		t.Error("without a checkout package, a folder without package.json counted")
	}
}

func TestSpecFor(t *testing.T) {
	for version, want := range map[string]struct {
		spec   string
		pinned bool
	}{
		"v0.2.0":                             {"@jgabor/ktx@0.2.0", true},
		"v12.30.4":                           {"@jgabor/ktx@12.30.4", true},
		"dev":                                {"@jgabor/ktx", false},
		"v0.0.0-20261022101500-0123456789ab": {"@jgabor/ktx", false},
		"v0.2.0-rc.1":                        {"@jgabor/ktx", false},
		"v0.2.0+dirty":                       {"@jgabor/ktx", false},
		"0.2.0":                              {"@jgabor/ktx", false},
		"":                                   {"@jgabor/ktx", false},
	} {
		spec, pinned := opencodeplugin.SpecFor(version)
		if spec != want.spec || pinned != want.pinned {
			t.Errorf("SpecFor(%q) = %q, %t; want %q, %t", version, spec, pinned, want.spec, want.pinned)
		}
	}
}

func TestCurrent(t *testing.T) {
	dir := t.TempDir()
	server, tui := filepath.Join(dir, "opencode.json"), filepath.Join(dir, "tui.json")
	folder := filepath.Join(dir, "pkg")
	for _, c := range []struct {
		name   string
		state  opencodeplugin.State
		want   string
		wantOK bool
	}{
		{"one pin in both", opencodeplugin.State{Server: []opencodeplugin.Entry{{server, "@jgabor/ktx@0.1.0"}}, TUI: []opencodeplugin.Entry{{tui, "@jgabor/ktx@0.1.0"}}}, "@jgabor/ktx@0.1.0", true},
		{"one folder, two spellings", opencodeplugin.State{Server: []opencodeplugin.Entry{{server, "./pkg"}}, TUI: []opencodeplugin.Entry{{tui, "file://" + folder}}}, folder, true},
		{"pins disagree", opencodeplugin.State{Server: []opencodeplugin.Entry{{server, "@jgabor/ktx@0.1.0"}}, TUI: []opencodeplugin.Entry{{tui, "@jgabor/ktx@0.2.0"}}}, "", false},
		{"no TUI entry", opencodeplugin.State{Server: []opencodeplugin.Entry{{server, "@jgabor/ktx"}}}, "", false},
		{"two server entries", opencodeplugin.State{Server: []opencodeplugin.Entry{{server, "@jgabor/ktx"}, {server, "@jgabor/ktx"}}, TUI: []opencodeplugin.Entry{{tui, "@jgabor/ktx"}}}, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := c.state.Current()
			if ok != c.wantOK || (ok && got != c.want) {
				t.Fatalf("Current = %q, %t; want %q, %t", got, ok, c.want, c.wantOK)
			}
		})
	}
}

// pointFixture is a global config folder whose server config holds an older
// pin beside another plugin.
func pointFixture(t *testing.T) (opencodeplugin.State, fakeopencode.Plugin) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "opencode")
	writeFile(t, filepath.Join(dir, "opencode.jsonc"), "{\n  // mine\n  \"plugin\": [\n    \"other-plugin\",\n    \"@jgabor/ktx@0.1.0\",\n  ],\n}\n")
	writeFile(t, filepath.Join(dir, "tui.json"), `{"plugin": ["@jgabor/ktx@0.1.0", "other-tui"]}`)
	state, err := opencodeplugin.Inspect(opencodeplugin.GlobalScope(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	return state, fakeopencode.Plugin{Global: dir}
}

func TestPointReplacesTheKtxEntryOnly(t *testing.T) {
	state, fake := pointFixture(t)
	var calls []string
	run := func(name string, args ...string) (string, string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", "", fake.Run(args)
	}
	changed, err := opencodeplugin.Point(state, "opencode", "@jgabor/ktx@0.2.0", "", run)
	if err != nil || !changed {
		t.Fatalf("Point = %t, %v", changed, err)
	}
	if want := []string{"opencode plugin -g @jgabor/ktx@0.2.0"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %q, want %q", calls, want)
	}
	if got, want := readFile(t, filepath.Join(fake.Global, "opencode.jsonc")), "{\n  // mine\n  \"plugin\": [\n    \"other-plugin\", \"@jgabor/ktx@0.2.0\",\n  ],\n}\n"; got != want {
		t.Fatalf("server config = %q, want %q", got, want)
	}
	if got, want := readFile(t, filepath.Join(fake.Global, "tui.json")), `{"plugin": ["other-tui", "@jgabor/ktx@0.2.0"]}`; got != want {
		t.Fatalf("TUI config = %q, want %q", got, want)
	}

	after, err := opencodeplugin.Inspect(state.Scope, "")
	if err != nil {
		t.Fatal(err)
	}
	calls = nil
	if changed, err := opencodeplugin.Point(after, "opencode", "@jgabor/ktx@0.2.0", "", run); err != nil || changed || len(calls) != 0 {
		t.Fatalf("second Point = %t, %v, calls %q", changed, err, calls)
	}
}

// configContents reads every config file of a scope; an absent file maps to
// "absent".
func configContents(t *testing.T, scope opencodeplugin.Scope) map[string]string {
	t.Helper()
	contents := map[string]string{}
	for _, file := range scope.ConfigFiles() {
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			contents[file] = "absent"
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		contents[file] = string(data)
	}
	return contents
}

func TestPointFailsWhenOpencodeFailsOrDuplicates(t *testing.T) {
	t.Run("opencode fails", func(t *testing.T) {
		state, _ := pointFixture(t)
		before := configContents(t, state.Scope)
		run := func(string, ...string) (string, string, error) {
			return "", "network unreachable\n", errors.New("exit status 1")
		}
		_, err := opencodeplugin.Point(state, "opencode", "@jgabor/ktx@0.2.0", "", run)
		if err == nil || !errors.Is(err, opencodeplugin.ErrRestored) || err.Error() != "opencode plugin -g @jgabor/ktx@0.2.0: exit status 1: network unreachable; every OpenCode config file is back as it was" {
			t.Fatalf("Point = %v", err)
		}
		if after := configContents(t, state.Scope); !reflect.DeepEqual(after, before) {
			t.Fatalf("config after failure = %q, want %q", after, before)
		}
	})
	t.Run("a duplicate entry", func(t *testing.T) {
		state, fake := pointFixture(t)
		before := configContents(t, state.Scope)
		fake.Duplicate = true
		run := func(_ string, args ...string) (string, string, error) { return "", "", fake.Run(args) }
		_, err := opencodeplugin.Point(state, "opencode", "@jgabor/ktx@0.2.0", "", run)
		if err == nil || !errors.Is(err, opencodeplugin.ErrRestored) || !strings.Contains(err.Error(), "2 ktx server entries and 2 ktx TUI entries") {
			t.Fatalf("Point = %v", err)
		}
		if after := configContents(t, state.Scope); !reflect.DeepEqual(after, before) {
			t.Fatalf("config after failure = %q, want %q", after, before)
		}
	})
	t.Run("opencode reports the error on stdout", func(t *testing.T) {
		state, _ := pointFixture(t)
		// What opencode 1.19 prints when npm has no such package.
		const stdout = "┌  Install plugin @jgabor/ktx\n\x1b[?25l│\n■  Install failed\n\x1b[?25h│\n■  Could not install \"@jgabor/ktx\"\n│\n■  404 Not Found - GET https://registry.npmjs.org/@jgabor%2fktx - Not found\n│\n└  Done\n\n"
		run := func(string, ...string) (string, string, error) {
			return stdout, "\x1b[0m\n", errors.New("exit status 1")
		}
		_, err := opencodeplugin.Point(state, "opencode", "@jgabor/ktx@0.2.0", "", run)
		want := `opencode plugin -g @jgabor/ktx@0.2.0: exit status 1: Install plugin @jgabor/ktx; Install failed; Could not install "@jgabor/ktx"; 404 Not Found - GET https://registry.npmjs.org/@jgabor%2fktx - Not found; Done; every OpenCode config file is back as it was`
		if err == nil || err.Error() != want {
			t.Fatalf("Point = %v, want %s", err, want)
		}
	})
	t.Run("a file opencode created is removed", func(t *testing.T) {
		state, fake := pointFixture(t)
		before := configContents(t, state.Scope)
		created := filepath.Join(fake.Global, "opencode.json")
		run := func(string, ...string) (string, string, error) {
			if err := os.WriteFile(created, []byte(`{"plugin": ["@jgabor/ktx@0.2.0"]}`), 0o644); err != nil {
				return "", "", err
			}
			return "", "interrupted\n", errors.New("exit status 1")
		}
		if _, err := opencodeplugin.Point(state, "opencode", "@jgabor/ktx@0.2.0", "", run); !errors.Is(err, opencodeplugin.ErrRestored) {
			t.Fatalf("Point = %v", err)
		}
		if after := configContents(t, state.Scope); !reflect.DeepEqual(after, before) {
			t.Fatalf("config after failure = %q, want %q", after, before)
		}
	})
	t.Run("a failed restore names the file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores folder permissions")
		}
		state, fake := pointFixture(t)
		run := func(string, ...string) (string, string, error) {
			if err := os.Chmod(fake.Global, 0o555); err != nil {
				return "", "", err
			}
			return "", "network unreachable\n", errors.New("exit status 1")
		}
		t.Cleanup(func() { _ = os.Chmod(fake.Global, 0o755) })
		_, err := opencodeplugin.Point(state, "opencode", "@jgabor/ktx@0.2.0", "", run)
		if err == nil || errors.Is(err, opencodeplugin.ErrRestored) || !strings.Contains(err.Error(), "restoring the config failed") || !strings.Contains(err.Error(), filepath.Join(fake.Global, "tui.json")) {
			t.Fatalf("Point = %v", err)
		}
	})
}

func TestRemoveKtxEntriesKeepsASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "opencode.json")
	writeFile(t, target, `{"plugin": ["other", "@jgabor/ktx@0.1.0"]}`)
	link := filepath.Join(dir, "config", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := opencodeplugin.RemoveKtxEntries(link, ""); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v, %v", info, err)
	}
	if got := readFile(t, target); got != `{"plugin": ["other"]}` {
		t.Fatalf("linked config = %q", got)
	}
}

func TestRemoveKtxEntriesReportsWhatItRemoved(t *testing.T) {
	file := filepath.Join(t.TempDir(), "opencode.json")
	writeFile(t, file, `{"plugin": ["@jgabor/ktx@0.1.0", "other", "@jgabor/ktx"]}`)
	removed, err := opencodeplugin.RemoveKtxEntries(file, "")
	if err != nil || !reflect.DeepEqual(removed, []string{"@jgabor/ktx@0.1.0", "@jgabor/ktx"}) {
		t.Fatalf("RemoveKtxEntries = %q, %v", removed, err)
	}
	if got := readFile(t, file); got != `{"plugin": ["other"]}` {
		t.Fatalf("config = %q", got)
	}
	writeFile(t, file, `{"plugin": [`)
	if _, err := opencodeplugin.RemoveKtxEntries(file, ""); err == nil {
		t.Fatal("a malformed config was edited")
	}
	if removed, err := opencodeplugin.RemoveKtxEntries(filepath.Join(t.TempDir(), "absent.json"), ""); err != nil || removed != nil {
		t.Fatalf("absent file = %q, %v", removed, err)
	}
}
