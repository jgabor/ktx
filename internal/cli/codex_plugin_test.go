package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// The repository serves two plugins named ktx@ktx: the Claude Code plugin in
// plugins/ktx from .claude-plugin/marketplace.json, and the Codex plugin in
// plugins/codex from .agents/plugins/marketplace.json. Codex also reads the
// Claude Code marketplace layout, so these checks keep each runtime on its own
// folder and its own hook contract.

const (
	codexMarketplacePath  = ".agents/plugins/marketplace.json"
	claudeMarketplacePath = ".claude-plugin/marketplace.json"
	codexPluginSource     = "./plugins/codex"
	claudePluginSource    = "./plugins/ktx"
	codexPluginManifest   = "plugins/codex/.codex-plugin/plugin.json"
	codexPluginHooks      = "plugins/codex/hooks/hooks.json"
	codexPluginStatsSkill = "plugins/codex/skills/stats/SKILL.md"
)

var codexPluginRepositoryFiles = []string{
	codexMarketplacePath,
	claudeMarketplacePath,
	codexPluginManifest,
	codexPluginHooks,
	codexPluginStatsSkill,
	"plugins/ktx/hooks/hooks.json",
}

func readJSONObject(repo fs.FS, path string) (map[string]any, error) {
	data, err := fs.ReadFile(repo, path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return value, nil
}

// marketplacePluginSources returns the source path of each plugin in a
// marketplace, keyed by plugin name. Claude Code writes a source as a string;
// Codex writes it as an object with a path.
func marketplacePluginSources(marketplace map[string]any) map[string][]string {
	sources := map[string][]string{}
	plugins, _ := marketplace["plugins"].([]any)
	for _, raw := range plugins {
		plugin, _ := raw.(map[string]any)
		name, _ := plugin["name"].(string)
		switch source := plugin["source"].(type) {
		case string:
			sources[name] = append(sources[name], source)
		case map[string]any:
			path, _ := source["path"].(string)
			sources[name] = append(sources[name], path)
		default:
			sources[name] = append(sources[name], "")
		}
	}
	return sources
}

// checkCodexPlugin reports every way the repository layout lets Codex load
// something other than its own plugin, or lets Claude Code load the Codex one.
func checkCodexPlugin(repo fs.FS) error {
	var problems []error
	fail := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	codexMarketplace, err := readJSONObject(repo, codexMarketplacePath)
	if err != nil {
		return err
	}
	if codexMarketplace["name"] != "ktx" {
		fail("%s name = %v, want ktx", codexMarketplacePath, codexMarketplace["name"])
	}
	if sources := marketplacePluginSources(codexMarketplace); len(sources) != 1 || !reflect.DeepEqual(sources["ktx"], []string{codexPluginSource}) {
		fail("%s plugins = %v, want only ktx from %s", codexMarketplacePath, sources, codexPluginSource)
	}
	plugins, _ := codexMarketplace["plugins"].([]any)
	for _, raw := range plugins {
		plugin, _ := raw.(map[string]any)
		source, _ := plugin["source"].(map[string]any)
		policy, _ := plugin["policy"].(map[string]any)
		if source["source"] != "local" || policy["installation"] != "AVAILABLE" || plugin["category"] == nil {
			fail("%s plugin %v needs a local source, installation AVAILABLE and a category", codexMarketplacePath, plugin["name"])
		}
	}

	claudeMarketplace, err := readJSONObject(repo, claudeMarketplacePath)
	if err != nil {
		return err
	}
	if sources := marketplacePluginSources(claudeMarketplace); !reflect.DeepEqual(sources["ktx"], []string{claudePluginSource}) {
		fail("%s ktx sources = %v, want only %s", claudeMarketplacePath, sources["ktx"], claudePluginSource)
	}

	manifest, err := readJSONObject(repo, codexPluginManifest)
	if err != nil {
		return err
	}
	if manifest["name"] != "ktx" {
		fail("%s name = %v, want ktx", codexPluginManifest, manifest["name"])
	}
	if _, ok := manifest["version"]; ok {
		fail("%s declares a version; the marketplace revision identifies the plugin", codexPluginManifest)
	}
	if manifest["hooks"] != "./hooks/hooks.json" {
		fail("%s hooks = %v, want ./hooks/hooks.json", codexPluginManifest, manifest["hooks"])
	}
	if manifest["skills"] != "./skills/" {
		fail("%s skills = %v, want ./skills/", codexPluginManifest, manifest["skills"])
	}

	hooks, err := readJSONObject(repo, codexPluginHooks)
	if err != nil {
		return err
	}
	want := map[string]any{"hooks": map[string]any{codexPostToolUseKey: []any{ktxCodexPostToolUseEntry(ktxHookCommand)}}}
	if got, _ := json.Marshal(hooks); string(got) != string(mustMarshalJSON(want)) {
		fail("%s = %s, want %s", codexPluginHooks, got, mustMarshalJSON(want))
	}

	skill, err := fs.ReadFile(repo, codexPluginStatsSkill)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(skill), "---\nname: stats\ndescription: ") || !strings.Contains(string(skill), "\nktx stats summary\n") {
		fail("%s needs name stats, a description, and the command ktx stats summary", codexPluginStatsSkill)
	}

	// Each plugin folder carries only its own runtime's manifest.
	for _, path := range []string{"plugins/codex/.claude-plugin", "plugins/ktx/.codex-plugin"} {
		if _, err := fs.Stat(repo, path); err == nil {
			fail("%s exists; each runtime must load only its own plugin folder", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return errors.Join(problems...)
}

func mustMarshalJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func TestCodexPluginRepositoryLayout(t *testing.T) {
	if err := checkCodexPlugin(os.DirFS("../..")); err != nil {
		t.Fatal(err)
	}
}

func TestCodexPluginLayoutRejectsCrossedRuntimes(t *testing.T) {
	repo := fstest.MapFS{}
	for _, path := range codexPluginRepositoryFiles {
		data, err := os.ReadFile("../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		repo[path] = &fstest.MapFile{Data: data}
	}
	if err := checkCodexPlugin(repo); err != nil {
		t.Fatalf("unchanged copy: %v", err)
	}
	replace := func(path, old, new string) func(fstest.MapFS) {
		return func(copy fstest.MapFS) {
			data := string(copy[path].Data)
			if !strings.Contains(data, old) {
				t.Fatalf("%s does not contain %q", path, old)
			}
			copy[path] = &fstest.MapFile{Data: []byte(strings.Replace(data, old, new, 1))}
		}
	}
	cases := map[string]struct {
		mutate func(fstest.MapFS)
		want   string
	}{
		"Claude Code hook in the Codex hooks file": {
			mutate: replace(codexPluginHooks, `"command": "ktx hook"`, `"command": "ktx hook --runtime claude-code"`),
			want:   codexPluginHooks,
		},
		"Codex marketplace serves the Claude Code folder": {
			mutate: replace(codexMarketplacePath, `"./plugins/codex"`, `"./plugins/ktx"`),
			want:   codexMarketplacePath,
		},
		"Claude Code marketplace serves the Codex folder": {
			mutate: replace(claudeMarketplacePath, `"./plugins/ktx"`, `"./plugins/codex"`),
			want:   claudeMarketplacePath,
		},
		"manifest without its own hooks file falls back to hooks/hooks.json": {
			mutate: replace(codexPluginManifest, `"hooks": "./hooks/hooks.json",`, ``),
			want:   "hooks = <nil>",
		},
		"manifest pins a version": {
			mutate: replace(codexPluginManifest, `"name": "ktx",`, `"name": "ktx", "version": "1.0.0",`),
			want:   "declares a version",
		},
		"skill without the stats command": {
			mutate: replace(codexPluginStatsSkill, "\nktx stats summary\n", "\nktx stats\n"),
			want:   codexPluginStatsSkill,
		},
		"Codex manifest in the Claude Code folder": {
			mutate: func(copy fstest.MapFS) {
				copy["plugins/ktx/.codex-plugin/plugin.json"] = &fstest.MapFile{Data: copy[codexPluginManifest].Data}
			},
			want: "plugins/ktx/.codex-plugin exists",
		},
		"Claude Code manifest in the Codex folder": {
			mutate: func(copy fstest.MapFS) {
				copy["plugins/codex/.claude-plugin/plugin.json"] = &fstest.MapFile{Data: []byte(`{"name":"ktx"}`)}
			},
			want: "plugins/codex/.claude-plugin exists",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			copy := fstest.MapFS{}
			for path, file := range repo {
				copy[path] = &fstest.MapFile{Data: file.Data}
			}
			tc.mutate(copy)
			err := checkCodexPlugin(copy)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkCodexPlugin error = %v, want one naming %q", err, tc.want)
			}
		})
	}
}
