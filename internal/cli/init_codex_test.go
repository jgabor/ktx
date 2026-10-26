// Tests for `ktx init codex` (internal/cli/init_codex.go).
//
// Each test puts a fake codex on a PATH of its own, points HOME at a temporary
// folder and works in a temporary project folder. The fake records every call
// and keeps the marketplace and plugin listings in files, so no test reaches
// the real Codex configuration.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fakeCodexScript = `#!/bin/sh
PATH=/usr/bin:/bin
dir="$FAKE_CODEX_DIR"
echo "WARNING: proceeding, even though we could not create PATH aliases" >&2
printf '%s\n' "$*" >> "$dir/calls"
case "$*" in
"plugin marketplace list --json") cat "$dir/marketplaces.json"; exit 0 ;;
"plugin list --json") cat "$dir/plugins.json"; exit 0 ;;
esac
if [ -f "$dir/fail" ]; then
  echo "network unreachable" >&2
  exit 1
fi
if [ -f "$dir/unlisted" ] && [ "$*" = "plugin add ktx@ktx" ]; then exit 0; fi
case "$*" in
"plugin marketplace add jgabor/ktx") echo '{"marketplaces":[{"name":"ktx","marketplaceSource":{"sourceType":"git","source":"https://github.com/jgabor/ktx.git"}}]}' > "$dir/marketplaces.json" ;;
"plugin add ktx@ktx") echo '{"installed":[{"pluginId":"ktx@ktx","enabled":true,"source":{"source":"local","path":"/cache/ktx/plugins/codex"}}],"available":[]}' > "$dir/plugins.json" ;;
"plugin remove ktx@ktx") echo '{"installed":[],"available":[]}' > "$dir/plugins.json" ;;
"plugin marketplace remove ktx") echo '{"marketplaces":[]}' > "$dir/marketplaces.json" ;;
*) echo "unexpected call: $*" >&2; exit 2 ;;
esac
`

const (
	codexNoMarketplaces = `{"marketplaces":[{"name":"other","marketplaceSource":{"sourceType":"git","source":"https://github.com/someone/other.git"}}]}`
	codexGitMarketplace = `{"marketplaces":[{"name":"other","marketplaceSource":{"sourceType":"git","source":"https://github.com/someone/other.git"}},{"name":"ktx","marketplaceSource":{"sourceType":"git","source":"https://github.com/jgabor/ktx.git"}}]}`
	codexNoPlugins      = `{"installed":[{"pluginId":"other@other","enabled":true,"source":{"path":"/cache/other"}}],"available":[]}`
	codexKtxInstalled   = `{"installed":[{"pluginId":"other@other","enabled":true,"source":{"path":"/cache/other"}},{"pluginId":"ktx@ktx","enabled":true,"source":{"source":"local","path":"/cache/ktx/plugins/codex"}}],"available":[]}`
)

// ktxCodexPostToolUseEntry builds a PostToolUse entry in the shape earlier ktx
// versions merged into hooks.json, and the plugin's hooks file still uses.
func ktxCodexPostToolUseEntry(command string) map[string]any {
	return map[string]any{
		"matcher": ".*",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       command,
				"statusMessage": ktxHookStatusMessage,
			},
		},
	}
}

type fakeCodex struct {
	dir     string
	bin     string
	home    string
	project string
}

// setupFakeCodex puts a fake codex, and a ktx when withKtx is set, on a PATH
// of the test's own, with the given listings, a temporary HOME and a
// temporary working directory.
func setupFakeCodex(t *testing.T, marketplaces, plugins string, withKtx bool) fakeCodex {
	t.Helper()
	fake := fakeCodex{dir: t.TempDir(), bin: t.TempDir(), home: t.TempDir(), project: t.TempDir()}
	if err := os.WriteFile(filepath.Join(fake.bin, "codex"), []byte(fakeCodexScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if withKtx {
		if err := os.WriteFile(filepath.Join(fake.bin, "ktx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake.write(t, "marketplaces.json", marketplaces)
	fake.write(t, "plugins.json", plugins)
	t.Setenv("FAKE_CODEX_DIR", fake.dir)
	t.Setenv("PATH", fake.bin)
	t.Setenv("HOME", fake.home)
	t.Chdir(fake.project)
	return fake
}

func (f fakeCodex) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// calls returns the codex invocations other than the two listings.
func (f fakeCodex) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	if os.IsNotExist(err) {
		return []string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	changes := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasSuffix(line, "list --json") {
			changes = append(changes, line)
		}
	}
	return changes
}

func (f fakeCodex) globalHooks() string { return filepath.Join(f.home, ".codex", "hooks.json") }

func (f fakeCodex) projectHooks() string { return filepath.Join(f.project, ".codex", "hooks.json") }

func writeHooksFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCodexInit(t *testing.T, format string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runInitWith(append([]string{"codex"}, args...), Options{Format: format}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// legacyGlobalHooks holds an unrelated hook in every category, a pinned ktx
// hook beside an unrelated PostToolUse hook, and a pinned ktx hook sharing a
// matcher entry with another program.
const legacyGlobalHooks = `{"hooks":{
  "SessionStart":[{"hooks":[{"type":"command","command":"orca session","timeout":10}]}],
  "PostToolUse":[
    {"hooks":[{"type":"command","command":"orca post && true 2>&1 >/dev/null","timeout":10}]},
    {"hooks":[{"command":"'/home/u/go/bin/ktx' hook","statusMessage":"ktx: recording tool call","type":"command"}],"matcher":".*"},
    {"matcher":"Bash","hooks":[{"type":"command","command":"'/old/ktx' --expect-runtime-sha256 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef hook","statusMessage":"ktx: recording tool call"},{"type":"command","command":"other-tool"}]}
  ]},
  "future":9007199254740993}`

// legacyProjectHooks holds only a bare ktx hook from the first versions.
const legacyProjectHooks = `{"hooks":{"PostToolUse":[{"matcher":".*","hooks":[{"type":"command","command":"ktx hook","timeout":10,"statusMessage":"ktx: recording tool call"}]}]}}`

func TestInitCodexInstallsThroughCodex(t *testing.T) {
	fake := setupFakeCodex(t, codexNoMarketplaces, codexNoPlugins, true)

	code, stdout, stderr := runCodexInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	want := []string{"plugin marketplace add jgabor/ktx", "plugin add ktx@ktx"}
	if got := fake.calls(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	report := decodeJSONObject(t, stdout)
	if report["installed"] != true || report["plugin"] != "ktx@ktx" || report["enabled"] != true ||
		report["added_marketplace"] != true || report["installed_plugin"] != true ||
		!strings.Contains(report["note"].(string), "/hooks") || report["warning"] != nil {
		t.Fatalf("report = %v", report)
	}
	if _, ok := report["already_installed"]; ok {
		t.Fatalf("first install reported already_installed: %v", report)
	}
	if marketplace, _ := report["marketplace"].(map[string]any); marketplace["source"] != "https://github.com/jgabor/ktx.git" || marketplace["source_type"] != "git" {
		t.Fatalf("marketplace = %v", report["marketplace"])
	}
	if removed, _ := report["removed_legacy_hooks"].([]any); len(removed) != 0 {
		t.Fatalf("removed_legacy_hooks = %v", report["removed_legacy_hooks"])
	}
	if report["ktx_on_path"] != filepath.Join(fake.bin, "ktx") {
		t.Fatalf("ktx_on_path = %v", report["ktx_on_path"])
	}
}

func TestInitCodexInstallIsIdempotent(t *testing.T) {
	fake := setupFakeCodex(t, codexGitMarketplace, codexKtxInstalled, true)

	code, stdout, _ := runCodexInit(t, FormatJSON)
	if code != ExitOK || decodeJSONObject(t, stdout)["already_installed"] != true {
		t.Fatalf("install = %d %s", code, stdout)
	}
	code, stdout, _ = runCodexInit(t, FormatText)
	for _, want := range []string{"already installed: ktx@ktx (no change)", "marketplace: ktx (git https://github.com/jgabor/ktx.git)", "/hooks", "trust the ktx hook"} {
		if code != ExitOK || !strings.Contains(stdout, want) {
			t.Errorf("text install = %d, lacks %q:\n%s", code, want, stdout)
		}
	}
	if got := fake.calls(t); len(got) != 0 {
		t.Fatalf("a re-run changed Codex: %v", got)
	}
}

func TestInitCodexKeepsAnExistingKtxMarketplace(t *testing.T) {
	fake := setupFakeCodex(t, `{"marketplaces":[{"name":"ktx","marketplaceSource":{"sourceType":"local","source":"/src/ktx"}}]}`, `{"installed":[],"available":[]}`, false)

	code, stdout, stderr := runCodexInit(t, FormatText)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if got, want := fake.calls(t), []string{"plugin add ktx@ktx"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for _, want := range []string{"installed: ktx@ktx", "marketplace: ktx (local /src/ktx)", "ktx on PATH: not found", "/hooks"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestInitCodexFailsWhenCodexInstalledAnotherLayout(t *testing.T) {
	fake := setupFakeCodex(t, codexGitMarketplace, `{"installed":[{"pluginId":"ktx@ktx","enabled":false,"source":{"path":"/cache/marketplaces/ktx/plugins/ktx"}}]}`, true)
	writeHooksFile(t, fake.globalHooks(), legacyGlobalHooks)

	code, stdout, stderr := runCodexInit(t, FormatText)
	if code != ExitInternal || stdout != "" || !strings.Contains(stderr, "Codex installed ktx@ktx from /cache/marketplaces/ktx/plugins/ktx") {
		t.Fatalf("install = %d %q %q", code, stdout, stderr)
	}
	// The earlier hook stays, since the installed plugin cannot replace it.
	if got, err := os.ReadFile(fake.globalHooks()); err != nil || string(got) != legacyGlobalHooks {
		t.Fatalf("earlier hook removed: %v", err)
	}
	code, stdout, _ = runCodexInit(t, FormatJSON, "--show")
	if code != ExitOK || decodeJSONObject(t, stdout)["warning"] == nil {
		t.Fatalf("show = %d %q", code, stdout)
	}
}

func TestInitCodexKeepsEarlierHooksWhenThePluginIsNotListed(t *testing.T) {
	fake := setupFakeCodex(t, codexGitMarketplace, codexNoPlugins, true)
	fake.write(t, "unlisted", "")
	writeHooksFile(t, fake.globalHooks(), legacyGlobalHooks)

	code, stdout, stderr := runCodexInit(t, FormatText)
	if code != ExitInternal || stdout != "" || !strings.Contains(stderr, "does not show ktx@ktx after install; earlier ktx hooks are kept") {
		t.Fatalf("install = %d %q %q", code, stdout, stderr)
	}
	if got, err := os.ReadFile(fake.globalHooks()); err != nil || string(got) != legacyGlobalHooks {
		t.Fatalf("earlier hook removed: %v", err)
	}
}

func TestInitCodexRemovesEarlierMergedHooks(t *testing.T) {
	fake := setupFakeCodex(t, codexNoMarketplaces, codexNoPlugins, true)
	writeHooksFile(t, fake.globalHooks(), legacyGlobalHooks)
	writeHooksFile(t, fake.projectHooks(), legacyProjectHooks)

	code, stdout, stderr := runCodexInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	report := decodeJSONObject(t, stdout)
	wantRemoved := []any{
		map[string]any{"path": fake.globalHooks(), "entries": float64(2)},
		map[string]any{"path": fake.projectHooks(), "entries": float64(1)},
	}
	if !reflect.DeepEqual(report["removed_legacy_hooks"], wantRemoved) {
		t.Fatalf("removed_legacy_hooks = %v, want %v", report["removed_legacy_hooks"], wantRemoved)
	}
	if got := fake.calls(t); len(got) != 2 {
		t.Fatalf("calls = %v", got)
	}

	doc, _, err := loadCodexHooks(fake.globalHooks())
	if err != nil {
		t.Fatal(err)
	}
	if _, count := filterKtxEntries(codexPostToolUseArray(codexHooksObject(doc))); count != 0 {
		t.Fatalf("ktx hooks remain: %v", doc)
	}
	raw, _ := json.Marshal(doc)
	for _, want := range []string{`"orca session"`, `"orca post`, `"other-tool"`, `"matcher":"Bash"`, `9007199254740993`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("unrelated content %s lost: %s", want, raw)
		}
	}
	if written, err := os.ReadFile(fake.globalHooks()); err != nil || !bytes.Contains(written, []byte("orca post && true 2>&1 >/dev/null")) {
		t.Errorf("other commands not kept as written: %s %v", written, err)
	}
	if info, err := os.Stat(fake.globalHooks()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions changed: %v %v", info, err)
	}
	if _, err := os.Stat(fake.projectHooks()); !os.IsNotExist(err) {
		t.Fatalf("project file holding only the ktx hook remains: %v", err)
	}

	// A second run finds nothing left to remove.
	code, stdout, _ = runCodexInit(t, FormatText)
	if code != ExitOK || strings.Contains(stdout, "removed the earlier ktx hook") {
		t.Fatalf("re-run = %d %q", code, stdout)
	}
}

func TestInitCodexTextReportsRemovedHooks(t *testing.T) {
	fake := setupFakeCodex(t, codexGitMarketplace, codexKtxInstalled, true)
	writeHooksFile(t, fake.globalHooks(), legacyGlobalHooks)

	code, stdout, _ := runCodexInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "removed the earlier ktx hook from "+fake.globalHooks()+" (2 entries)") {
		t.Fatalf("install = %d %q", code, stdout)
	}
}

func TestInitCodexMalformedHooksFileStopsBeforeChanges(t *testing.T) {
	fake := setupFakeCodex(t, codexNoMarketplaces, codexNoPlugins, true)
	writeHooksFile(t, fake.globalHooks(), `{"hooks":false}`)

	code, stdout, stderr := runCodexInit(t, FormatJSON)
	if code != ExitInternal || stdout != "" || !strings.Contains(stderr, "hooks must be an object") {
		t.Fatalf("install = %d %q %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(fake.dir, "calls")); !os.IsNotExist(err) {
		t.Fatalf("install ran codex before inspecting hooks files: %v", fake.calls(t))
	}
	if got, err := os.ReadFile(fake.globalHooks()); err != nil || string(got) != `{"hooks":false}` {
		t.Fatalf("malformed file changed: %s %v", got, err)
	}
}

func TestInitCodexFailedHookRemovalSaysWhatToRemove(t *testing.T) {
	fake := setupFakeCodex(t, codexNoMarketplaces, codexNoPlugins, true)
	real := filepath.Join(fake.home, "real-hooks.json")
	writeHooksFile(t, real, legacyProjectHooks)
	if err := os.MkdirAll(filepath.Dir(fake.globalHooks()), 0o755); err != nil {
		t.Fatal(err)
	}
	// Removal refuses a symlinked hooks file, after the plugin is installed.
	if err := os.Symlink(real, fake.globalHooks()); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCodexInit(t, FormatJSON)
	if code != ExitInternal || stdout != "" || !strings.Contains(stderr, "ktx@ktx is installed") ||
		!strings.Contains(stderr, fake.globalHooks()) || !strings.Contains(stderr, "does not run twice") {
		t.Fatalf("install = %d %q %q", code, stdout, stderr)
	}
	if got, err := os.ReadFile(real); err != nil || string(got) != legacyProjectHooks {
		t.Fatalf("symlink target changed: %s %v", got, err)
	}
}

func TestInitCodexWithoutCodexPrintsTheCommands(t *testing.T) {
	fake := setupFakeCodex(t, codexNoMarketplaces, codexNoPlugins, false)
	t.Setenv("PATH", t.TempDir())

	code, stdout, stderr := runCodexInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	report := decodeJSONObject(t, stdout)
	steps := []any{"codex plugin marketplace add jgabor/ktx", "codex plugin add ktx@ktx"}
	if report["installed"] != false || !reflect.DeepEqual(report["next_steps"], steps) || !strings.Contains(report["note"].(string), "/hooks") {
		t.Fatalf("report = %v", report)
	}
	code, stdout, _ = runCodexInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "  codex plugin marketplace add jgabor/ktx\n  codex plugin add ktx@ktx\n") {
		t.Fatalf("text = %d %q", code, stdout)
	}
	for _, args := range [][]string{{"--show"}, {"--uninstall"}} {
		if code, _, stderr := runCodexInit(t, FormatJSON, args...); code != ExitNotFound || !strings.Contains(stderr, "codex not found") {
			t.Fatalf("%v = %d %q", args, code, stderr)
		}
	}

	// The earlier merged hook is removed too, so the plugin hook is the only
	// ktx hook once the printed commands run.
	writeHooksFile(t, fake.projectHooks(), legacyProjectHooks)
	code, stdout, _ = runCodexInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "removed the earlier ktx hook from "+fake.projectHooks()+" (1 entry)") {
		t.Fatalf("text = %d %q", code, stdout)
	}
}

func TestInitCodexFailingCodexIsAnInternalError(t *testing.T) {
	fake := setupFakeCodex(t, `{"marketplaces":[]}`, `{"installed":[]}`, true)
	fake.write(t, "fail", "")

	code, stdout, stderr := runCodexInit(t, FormatJSON)
	if code != ExitInternal || stdout != "" || !strings.Contains(stderr, "network unreachable") ||
		!strings.Contains(stderr, "plugin marketplace add jgabor/ktx") {
		t.Fatalf("install = %d %q %q", code, stdout, stderr)
	}
}

func TestInitCodexShow(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		fake := setupFakeCodex(t, codexGitMarketplace, codexKtxInstalled, true)
		code, stdout, _ := runCodexInit(t, FormatJSON, "--show")
		report := decodeJSONObject(t, stdout)
		if code != ExitOK || report["installed"] != true || report["enabled"] != true || report["ktx_version"] == "" ||
			report["ktx_on_path"] != filepath.Join(fake.bin, "ktx") || report["runtime_activation"] != "not_verified" {
			t.Fatalf("show = %d %v", code, report)
		}
		code, stdout, _ = runCodexInit(t, FormatText, "--show")
		for _, want := range []string{"ktx Codex plugin: ktx@ktx", "enabled: true", "marketplace: ktx (git https://github.com/jgabor/ktx.git)", "ktx on PATH: " + filepath.Join(fake.bin, "ktx")} {
			if code != ExitOK || !strings.Contains(stdout, want) {
				t.Errorf("text show = %d, lacks %q:\n%s", code, want, stdout)
			}
		}
		if got := fake.calls(t); len(got) != 0 {
			t.Fatalf("--show changed Codex: %v", got)
		}
	})

	t.Run("reports an earlier hook left behind", func(t *testing.T) {
		fake := setupFakeCodex(t, codexGitMarketplace, codexKtxInstalled, true)
		writeHooksFile(t, fake.globalHooks(), legacyGlobalHooks)
		code, stdout, _ := runCodexInit(t, FormatText, "--show")
		if code != ExitOK || !strings.Contains(stdout, "earlier ktx hook: "+fake.globalHooks()+" (2 entries)") {
			t.Fatalf("show = %d %q", code, stdout)
		}
		if got, err := os.ReadFile(fake.globalHooks()); err != nil || string(got) != legacyGlobalHooks {
			t.Fatalf("--show changed the hooks file: %v", err)
		}
	})

	t.Run("not installed exits 2", func(t *testing.T) {
		fake := setupFakeCodex(t, codexGitMarketplace, codexNoPlugins, true)
		writeHooksFile(t, fake.projectHooks(), legacyProjectHooks)
		code, stdout, stderr := runCodexInit(t, FormatJSON, "--show")
		if code != ExitNotFound || stdout != "" || !strings.Contains(stderr, "not installed") || !strings.Contains(stderr, "still merged into "+fake.projectHooks()) {
			t.Fatalf("show = %d %q %q", code, stdout, stderr)
		}
	})
}

func TestInitCodexUninstall(t *testing.T) {
	cases := []struct {
		name         string
		marketplaces string
		plugins      string
		want         []string
	}{
		{"plugin and marketplace", codexGitMarketplace, codexKtxInstalled, []string{"plugin remove ktx@ktx", "plugin marketplace remove ktx"}},
		{"marketplace only", codexGitMarketplace, codexNoPlugins, []string{"plugin marketplace remove ktx"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := setupFakeCodex(t, c.marketplaces, c.plugins, true)
			code, stdout, stderr := runCodexInit(t, FormatJSON, "--uninstall")
			if code != ExitOK || decodeJSONObject(t, stdout)["uninstalled"] != true {
				t.Fatalf("uninstall = %d %q %q", code, stdout, stderr)
			}
			if got := fake.calls(t); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("calls = %v, want %v", got, c.want)
			}
		})
	}

	t.Run("earlier hook only", func(t *testing.T) {
		fake := setupFakeCodex(t, codexNoMarketplaces, codexNoPlugins, true)
		writeHooksFile(t, fake.globalHooks(), legacyGlobalHooks)
		code, stdout, stderr := runCodexInit(t, FormatText, "--uninstall")
		if code != ExitOK || !strings.Contains(stdout, "removed the earlier ktx hook from "+fake.globalHooks()) {
			t.Fatalf("uninstall = %d %q %q", code, stdout, stderr)
		}
		if got := fake.calls(t); len(got) != 0 {
			t.Fatalf("calls = %v", got)
		}
	})

	t.Run("nothing installed exits 2", func(t *testing.T) {
		fake := setupFakeCodex(t, codexNoMarketplaces, codexNoPlugins, true)
		if code, _, stderr := runCodexInit(t, FormatText, "--uninstall"); code != ExitNotFound || !strings.Contains(stderr, "neither") {
			t.Fatalf("uninstall = %d %q", code, stderr)
		}
		if got := fake.calls(t); len(got) != 0 {
			t.Fatalf("calls = %v", got)
		}
	})
}

func TestInitCodexRejectsProject(t *testing.T) {
	fake := setupFakeCodex(t, codexNoMarketplaces, codexNoPlugins, true)
	writeHooksFile(t, fake.projectHooks(), legacyProjectHooks)
	for _, args := range [][]string{{"--project"}, {"--project", "--show"}, {"--project", "--uninstall"}} {
		code, stdout, stderr := runCodexInit(t, FormatJSON, args...)
		if code != ExitUsage || stdout != "" || !strings.Contains(stderr, "--project") {
			t.Fatalf("%v = %d %q %q", args, code, stdout, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(fake.dir, "calls")); !os.IsNotExist(err) {
		t.Fatalf("rejected --project still ran codex: %v", err)
	}
	if got, err := os.ReadFile(fake.projectHooks()); err != nil || string(got) != legacyProjectHooks {
		t.Fatalf("rejected --project changed the hooks file: %v", err)
	}
}

func TestInitHelpListsCodex(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runInitWith([]string{"--help"}, Options{Format: FormatText}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("help exit = %d", code)
	}
	for _, want := range []string{"ktx init codex --show", "codex plugin add ktx@ktx", "/hooks", "ktx:stats"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help lacks %q", want)
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := runInitWith(nil, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitUsage || !strings.Contains(stderr.String(), "codex") {
		t.Fatalf("missing runtime = %d %q", code, stderr.String())
	}
}
