// Tests for `ktx init claude` (internal/cli/init_claude_code.go).
//
// Each test puts a fake claude on a PATH of its own. The fake records every
// call and keeps the marketplace and plugin listings in files, so no test
// reaches the real Claude Code configuration.
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

const fakeClaudeScript = `#!/bin/sh
PATH=/usr/bin:/bin
dir="$FAKE_CLAUDE_DIR"
printf '%s\n' "$*" >> "$dir/calls"
case "$*" in
"plugin marketplace list --json") cat "$dir/marketplaces.json"; exit 0 ;;
"plugin list --json") cat "$dir/plugins.json"; exit 0 ;;
esac
if [ -f "$dir/fail" ]; then
  echo "network unreachable" >&2
  exit 1
fi
case "$*" in
"plugin marketplace add jgabor/ktx") echo '[{"name":"ktx","source":"github","repo":"jgabor/ktx"}]' > "$dir/marketplaces.json" ;;
"plugin install ktx@ktx") echo '[{"id":"ktx@ktx","enabled":true}]' > "$dir/plugins.json" ;;
"plugin uninstall ktx@ktx") echo '[]' > "$dir/plugins.json" ;;
"plugin marketplace remove ktx") echo '[]' > "$dir/marketplaces.json" ;;
*) echo "unexpected call: $*" >&2; exit 2 ;;
esac
`

const (
	githubMarketplace = `[{"name":"other","source":"github","repo":"someone/other"},{"name":"ktx","source":"github","repo":"jgabor/ktx"}]`
	installedPlugin   = `[{"id":"other@other","enabled":true},{"id":"ktx@ktx","enabled":true}]`
)

type fakeClaude struct {
	dir string
	bin string
}

// setupFakeClaude puts a fake claude, and a ktx when withKtx is set, on a PATH
// of the test's own, with the given marketplace and plugin listings.
func setupFakeClaude(t *testing.T, marketplaces, plugins string, withKtx bool) fakeClaude {
	t.Helper()
	fake := fakeClaude{dir: t.TempDir(), bin: t.TempDir()}
	if err := os.WriteFile(filepath.Join(fake.bin, "claude"), []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if withKtx {
		if err := os.WriteFile(filepath.Join(fake.bin, "ktx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake.write(t, "marketplaces.json", marketplaces)
	fake.write(t, "plugins.json", plugins)
	t.Setenv("FAKE_CLAUDE_DIR", fake.dir)
	t.Setenv("PATH", fake.bin)
	return fake
}

func (f fakeClaude) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// calls returns the claude invocations other than the two listings.
func (f fakeClaude) calls(t *testing.T) []string {
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

func runClaudeCodeInit(t *testing.T, format string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runInitWith(append([]string{"claude"}, args...), Options{Format: format}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func decodeJSONObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	return value
}

func TestInitClaudeCodeInstallsThroughClaude(t *testing.T) {
	fake := setupFakeClaude(t, `[{"name":"other","source":"github","repo":"someone/other"}]`, `[]`, true)

	code, stdout, stderr := runClaudeCodeInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	want := []string{"plugin marketplace add jgabor/ktx", "plugin install ktx@ktx"}
	if got := fake.calls(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	report := decodeJSONObject(t, stdout)
	if report["installed"] != true || report["plugin"] != "ktx@ktx" || report["enabled"] != true ||
		report["added_marketplace"] != true || report["installed_plugin"] != true || report["note"] == nil {
		t.Fatalf("report = %v", report)
	}
	if _, ok := report["already_installed"]; ok {
		t.Fatalf("first install reported already_installed: %v", report)
	}
	if marketplace, _ := report["marketplace"].(map[string]any); marketplace["repo"] != "jgabor/ktx" {
		t.Fatalf("marketplace = %v", report["marketplace"])
	}
	if report["ktx_on_path"] != filepath.Join(fake.bin, "ktx") {
		t.Fatalf("ktx_on_path = %v", report["ktx_on_path"])
	}
}

func TestInitClaudeCodeInstallIsIdempotent(t *testing.T) {
	fake := setupFakeClaude(t, githubMarketplace, installedPlugin, true)

	code, stdout, _ := runClaudeCodeInit(t, FormatJSON)
	if code != ExitOK || decodeJSONObject(t, stdout)["already_installed"] != true {
		t.Fatalf("install = %d %s", code, stdout)
	}
	code, stdout, _ = runClaudeCodeInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "already installed: ktx@ktx (no change)") ||
		!strings.Contains(stdout, "marketplace: ktx (github jgabor/ktx)") || strings.Contains(stdout, "/reload-plugins") {
		t.Fatalf("text install = %d %q", code, stdout)
	}
	if got := fake.calls(t); len(got) != 0 {
		t.Fatalf("a re-run changed Claude Code: %v", got)
	}
}

func TestInitClaudeCodeKeepsAnExistingKtxMarketplace(t *testing.T) {
	fake := setupFakeClaude(t, `[{"name":"ktx","source":"directory","path":"/src/ktx"}]`, `[]`, false)

	code, stdout, stderr := runClaudeCodeInit(t, FormatText)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if got, want := fake.calls(t), []string{"plugin install ktx@ktx"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for _, want := range []string{"installed: ktx@ktx", "marketplace: ktx (directory /src/ktx)", "ktx on PATH: not found", "/reload-plugins"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestInitClaudeCodeReportsADisabledPlugin(t *testing.T) {
	setupFakeClaude(t, githubMarketplace, `[{"id":"ktx@ktx","enabled":false}]`, true)

	code, stdout, _ := runClaudeCodeInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "claude plugin enable ktx@ktx") {
		t.Fatalf("install = %d %q", code, stdout)
	}
}

func TestInitClaudeCodeWithoutClaudePrintsTheCommands(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	code, stdout, stderr := runClaudeCodeInit(t, FormatJSON)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	report := decodeJSONObject(t, stdout)
	steps := []any{"claude plugin marketplace add jgabor/ktx", "claude plugin install ktx@ktx"}
	if report["installed"] != false || !reflect.DeepEqual(report["next_steps"], steps) {
		t.Fatalf("report = %v", report)
	}
	code, stdout, _ = runClaudeCodeInit(t, FormatText)
	if code != ExitOK || !strings.Contains(stdout, "  claude plugin marketplace add jgabor/ktx\n  claude plugin install ktx@ktx\n") {
		t.Fatalf("text = %d %q", code, stdout)
	}
	for _, args := range [][]string{{"--show"}, {"--uninstall"}} {
		if code, _, stderr := runClaudeCodeInit(t, FormatJSON, args...); code != ExitNotFound || !strings.Contains(stderr, "claude not found") {
			t.Fatalf("%v = %d %q", args, code, stderr)
		}
	}
}

func TestInitClaudeCodeFailingClaudeIsAnInternalError(t *testing.T) {
	fake := setupFakeClaude(t, `[]`, `[]`, true)
	fake.write(t, "fail", "")

	code, stdout, stderr := runClaudeCodeInit(t, FormatJSON)
	if code != ExitInternal || stdout != "" || !strings.Contains(stderr, "network unreachable") ||
		!strings.Contains(stderr, "plugin marketplace add jgabor/ktx") {
		t.Fatalf("install = %d %q %q", code, stdout, stderr)
	}
}

func TestInitClaudeCodeShow(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		fake := setupFakeClaude(t, githubMarketplace, installedPlugin, true)
		code, stdout, _ := runClaudeCodeInit(t, FormatJSON, "--show")
		report := decodeJSONObject(t, stdout)
		if code != ExitOK || report["installed"] != true || report["enabled"] != true || report["ktx_version"] == "" ||
			report["ktx_on_path"] != filepath.Join(fake.bin, "ktx") || report["runtime_activation"] != "not_verified" {
			t.Fatalf("show = %d %v", code, report)
		}
		code, stdout, _ = runClaudeCodeInit(t, FormatText, "--show")
		if code != ExitOK || !strings.Contains(stdout, "enabled: true") || !strings.Contains(stdout, "marketplace: ktx (github jgabor/ktx)") {
			t.Fatalf("text show = %d %q", code, stdout)
		}
		if got := fake.calls(t); len(got) != 0 {
			t.Fatalf("--show changed Claude Code: %v", got)
		}
	})

	t.Run("not installed exits 2", func(t *testing.T) {
		setupFakeClaude(t, githubMarketplace, `[]`, true)
		code, stdout, stderr := runClaudeCodeInit(t, FormatJSON, "--show")
		if code != ExitNotFound || stdout != "" || !strings.Contains(stderr, "not installed") {
			t.Fatalf("show = %d %q %q", code, stdout, stderr)
		}
	})
}

func TestInitClaudeCodeUninstall(t *testing.T) {
	cases := []struct {
		name         string
		marketplaces string
		plugins      string
		want         []string
	}{
		{"plugin and marketplace", githubMarketplace, installedPlugin, []string{"plugin uninstall ktx@ktx", "plugin marketplace remove ktx"}},
		{"marketplace only", githubMarketplace, `[]`, []string{"plugin marketplace remove ktx"}},
		{"plugin only", `[]`, installedPlugin, []string{"plugin uninstall ktx@ktx"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := setupFakeClaude(t, c.marketplaces, c.plugins, true)
			code, stdout, stderr := runClaudeCodeInit(t, FormatJSON, "--uninstall")
			if code != ExitOK || decodeJSONObject(t, stdout)["uninstalled"] != true {
				t.Fatalf("uninstall = %d %q %q", code, stdout, stderr)
			}
			if got := fake.calls(t); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("calls = %v, want %v", got, c.want)
			}
		})
	}

	t.Run("nothing installed exits 2", func(t *testing.T) {
		fake := setupFakeClaude(t, `[]`, `[]`, true)
		if code, _, stderr := runClaudeCodeInit(t, FormatText, "--uninstall"); code != ExitNotFound || !strings.Contains(stderr, "neither") {
			t.Fatalf("uninstall = %d %q", code, stderr)
		}
		if got := fake.calls(t); len(got) != 0 {
			t.Fatalf("calls = %v", got)
		}
	})
}

func TestInitClaudeCodeRejectsProject(t *testing.T) {
	fake := setupFakeClaude(t, `[]`, `[]`, true)
	for _, args := range [][]string{{"--project"}, {"--project", "--show"}, {"--project", "--uninstall"}} {
		code, stdout, stderr := runClaudeCodeInit(t, FormatJSON, args...)
		if code != ExitUsage || stdout != "" || !strings.Contains(stderr, "--project") {
			t.Fatalf("%v = %d %q %q", args, code, stdout, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(fake.dir, "calls")); !os.IsNotExist(err) {
		t.Fatalf("rejected --project still ran claude: %v", err)
	}
}

func TestInitHelpListsClaudeCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runInitWith([]string{"--help"}, Options{Format: FormatText}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("help exit = %d", code)
	}
	for _, want := range []string{"ktx@ktx", "hook --runtime claude-code", "/reload-plugins", "ktx init claude --show"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help lacks %q", want)
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := runInitWith([]string{"bogus"}, Options{Format: FormatJSON}, &stdout, &stderr); code != ExitUsage || !strings.Contains(stderr.String(), "opencode, codex, claude\"") {
		t.Fatalf("unknown runtime = %d %q", code, stderr.String())
	}
}
