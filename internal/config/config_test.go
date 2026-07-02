// Table-driven tests for the ktx.toml loader (M4 plan Task 5 + M5
// plugin support via BurntSushi/toml).
//
// Coverage per the plan's acceptance:
//   - missing config → empty list + no error (passthrough default)
//   - project-root ktx.toml parses
//   - user-level fallback used when project-root absent
//   - precedence: project-root wins when both exist
//   - malformed TOML → *ParseError
//   - unknown stage names preserved verbatim (resolution at runner
//     invocation time, not config-load time)
//   - [[plugin]] blocks parse into Config.Plugins (M5)
//   - missing required plugin fields → *ParseError (M5)
//   - M4 stages-only shape → Plugins nil, no error (M5 regression)
//
// All subtests use t.TempDir()-based fixtures and t.Setenv for
// XDG_CONFIG_HOME so no test mutates the user's real config dir.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFile writes content to path, creating parent dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// chdirTemp changes the test's working directory to a fresh tmpdir
// so the project-root ktx.toml lookup runs against an isolated cwd.
// The previous cwd is restored on cleanup. The fresh tmpdir is
// returned so the test can write its own ktx.toml into it.
func chdirTemp(t *testing.T) string {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})
	return dir
}

// TestLoad_MissingConfig covers the D14 no-config default: when
// neither a project-root ktx.toml nor a user-level config exists,
// Load returns an empty stage list and a nil error.
func TestLoad_MissingConfig(t *testing.T) {
	t.Run("no project or user config → empty stage list, no error", func(t *testing.T) {
		// Isolate cwd so no parent ktx.toml is found.
		chdirTemp(t)
		// Isolate XDG_CONFIG_HOME so no user config is found.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg == nil {
			t.Fatal("Load returned nil Config")
		}
		if len(cfg.PipelineDefault) != 0 {
			t.Errorf("PipelineDefault = %v, want empty", cfg.PipelineDefault)
		}
	})
}

// TestLoad_ProjectRoot covers the project-root path: a ktx.toml in
// the cwd is parsed, the stages array populates PipelineDefault,
// and the order is preserved.
func TestLoad_ProjectRoot(t *testing.T) {
	t.Run("project-root ktx.toml parses with ordered stages", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `# ktx.toml — M4 fixture
[pipeline.default]
stages = ["strip-ansi", "head:100"]
`)
		// Isolate XDG so the user-level fallback does not
		// interfere with the precedence test.
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := []string{"strip-ansi", "head:100"}
		if len(cfg.PipelineDefault) != 2 {
			t.Fatalf("PipelineDefault = %v, want %v", cfg.PipelineDefault, want)
		}
		for i, w := range want {
			if cfg.PipelineDefault[i] != w {
				t.Errorf("PipelineDefault[%d] = %q, want %q", i, cfg.PipelineDefault[i], w)
			}
		}
	})

	t.Run("empty stages array → empty list, no error", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", "[pipeline.default]\nstages = []\n")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.PipelineDefault) != 0 {
			t.Errorf("PipelineDefault = %v, want empty", cfg.PipelineDefault)
		}
	})
}

// TestLoad_ProjectRootStagesAbsentQuoted covers the forward-compat
// case: a ktx.toml with only unrelated sections yields an empty
// stage list (no error). A config with only future keys is
// forward-compatible with the loader contract.
func TestLoad_ProjectRootStagesAbsentQuoted(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "ktx.toml", "[ui]\ntheme = \"dark\"\n")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PipelineDefault) != 0 {
		t.Errorf("PipelineDefault = %v, want empty (no stages key)", cfg.PipelineDefault)
	}
}

// TestLoad_UserLevelFallback covers the user-level path: when no
// project-root ktx.toml exists but $XDG_CONFIG_HOME/ktx/config.toml
// does, Load uses it.
func TestLoad_UserLevelFallback(t *testing.T) {
	t.Run("user-level config used when project-root absent", func(t *testing.T) {
		chdirTemp(t) // no project-root ktx.toml

		xdg := t.TempDir()
		writeFile(t, filepath.Join(xdg, "ktx", "config.toml"), `[pipeline.default]
stages = ["normalize"]
`)
		t.Setenv("XDG_CONFIG_HOME", xdg)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.PipelineDefault) != 1 || cfg.PipelineDefault[0] != "normalize" {
			t.Errorf("PipelineDefault = %v, want [normalize]", cfg.PipelineDefault)
		}
	})
}

// TestLoad_Precedence covers the precedence rule: when both a
// project-root ktx.toml and a user-level config exist, the
// project-root wins.
func TestLoad_Precedence(t *testing.T) {
	t.Run("project-root wins when both exist", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:5"]
`)

		xdg := t.TempDir()
		writeFile(t, filepath.Join(xdg, "ktx", "config.toml"), `[pipeline.default]
stages = ["normalize"]
`)
		t.Setenv("XDG_CONFIG_HOME", xdg)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		// The project-root "head:5" must win; the user-level
		// "normalize" must NOT appear.
		if len(cfg.PipelineDefault) != 1 || cfg.PipelineDefault[0] != "head:5" {
			t.Errorf("PipelineDefault = %v, want [head:5] (project-root wins)",
				cfg.PipelineDefault)
		}
	})
}

// TestLoad_PipelineTimeout covers the D22 Phase 2 configurable pipeline
// timeout: [pipeline.default] timeout parses as a Go duration string,
// defaults to 30s when omitted or when no ktx.toml exists, and a
// garbage/non-positive value is a *ParseError at config-load time.
func TestLoad_PipelineTimeout(t *testing.T) {
	t.Run("timeout='10s' parses to 10s (configurable)", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:3"]
timeout = "10s"
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.PipelineTimeout != 10*time.Second {
			t.Errorf("PipelineTimeout = %v, want 10s", cfg.PipelineTimeout)
		}
	})

	t.Run("no timeout field → default 30s (backward compatible)", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:3"]
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.PipelineTimeout != DefaultPipelineTimeout {
			t.Errorf("PipelineTimeout = %v, want %v (default 30s)", cfg.PipelineTimeout, DefaultPipelineTimeout)
		}
	})

	t.Run("no ktx.toml → default 30s (passthrough path)", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.PipelineTimeout != DefaultPipelineTimeout {
			t.Errorf("PipelineTimeout = %v, want %v (default on passthrough path)", cfg.PipelineTimeout, DefaultPipelineTimeout)
		}
	})

	t.Run("garbage timeout → *ParseError", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:3"]
timeout = "garbage"
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want *ParseError for garbage timeout")
		}
		pe, ok := err.(*ParseError)
		if !ok {
			t.Fatalf("error = %T, want *ParseError", err)
		}
		if !strings.Contains(pe.Msg, "timeout") {
			t.Errorf("ParseError.Msg = %q, want it to mention 'timeout'", pe.Msg)
		}
	})

	t.Run("non-positive timeout → *ParseError", func(t *testing.T) {
		for _, bad := range []string{"0s", "-5s", "0"} {
			t.Run(bad, func(t *testing.T) {
				chdirTemp(t)
				writeFile(t, "ktx.toml", fmt.Sprintf(`[pipeline.default]
stages = ["head:3"]
timeout = %q
`, bad))
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())

				_, err := Load()
				if err == nil {
					t.Fatalf("Load(%q) succeeded; want *ParseError", bad)
				}
				if _, ok := err.(*ParseError); !ok {
					t.Errorf("error = %T, want *ParseError for %q", err, bad)
				}
			})
		}
	})

	t.Run("duration with minutes parses (2m)", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["head:3"]
timeout = "2m"
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.PipelineTimeout != 2*time.Minute {
			t.Errorf("PipelineTimeout = %v, want 2m", cfg.PipelineTimeout)
		}
	})
}

// TestLoad_Malformed covers the syntax-error path: a malformed
// ktx.toml returns *ParseError so the CLI layer can map it to exit 1.
// BurntSushi/toml's error messages differ from the M4 hand-rolled
// reader's wording; we assert ParseError existence, a non-empty
// message, and the file path — not specific substrings.
func TestLoad_Malformed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{
			name:    "unclosed section header",
			content: "[pipeline.default\n",
		},
		{
			name:    "key without equals",
			content: "[pipeline.default]\njust-a-key\n",
		},
		{
			name:    "unterminated string in array",
			content: `[pipeline.default]` + "\n" + `stages = ["no-end]` + "\n",
		},
		{
			name:    "unterminated array",
			content: `[pipeline.default]` + "\n" + `stages = ["a"` + "\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chdirTemp(t)
			writeFile(t, "ktx.toml", tc.content)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			_, err := Load()
			if err == nil {
				t.Fatal("Load succeeded; want ParseError")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Load returned %T, want *ParseError", err)
			}
			if pe.Msg == "" {
				t.Errorf("ParseError.Msg is empty; want non-empty error message")
			}
			if !strings.Contains(pe.Path, "ktx.toml") {
				t.Errorf("ParseError.Path = %q, want it to name ktx.toml", pe.Path)
			}
		})
	}
}

// TestLoad_UnknownStageNamesPreserved covers AC4: unknown stage
// names in the array are returned as-is. Resolution to the Registry
// happens at runner invocation time, not at config load — the
// unknown-stage invariant (3) handles them at runtime.
func TestLoad_UnknownStageNamesPreserved(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["strip-ansi", "frobnicate", "head:5", "bogus:name"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// All four names must survive — the loader does not validate
	// stage existence.
	want := []string{"strip-ansi", "frobnicate", "head:5", "bogus:name"}
	if len(cfg.PipelineDefault) != len(want) {
		t.Fatalf("PipelineDefault = %v, want %v", cfg.PipelineDefault, want)
	}
	for i, w := range want {
		if cfg.PipelineDefault[i] != w {
			t.Errorf("PipelineDefault[%d] = %q, want %q", i, cfg.PipelineDefault[i], w)
		}
	}
}

// TestLoad_MultiLineArray covers the multi-line array form: a stages
// array split across multiple lines parses identically to the
// inline form. TOML allows arrays to span lines, and a hand-rolled
// reader that only handled single-line arrays would reject a real
// ktx.toml that a user formatted for readability.
func TestLoad_MultiLineArray(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "ktx.toml", `[pipeline.default]
stages = [
  "strip-ansi",
  "head:100",
]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"strip-ansi", "head:100"}
	if len(cfg.PipelineDefault) != 2 {
		t.Fatalf("PipelineDefault = %v, want %v", cfg.PipelineDefault, want)
	}
	for i, w := range want {
		if cfg.PipelineDefault[i] != w {
			t.Errorf("PipelineDefault[%d] = %q, want %q", i, cfg.PipelineDefault[i], w)
		}
	}
}

// TestLoad_SingleQuotedElements covers the lenient single-quote
// acceptance: a TOML literal-string array element (single-quoted)
// parses identically to a basic (double-quoted) element. Several
// linters accept single-quote in inline arrays; we tolerate it so a
// stray single-quote does not break Load.
func TestLoad_SingleQuotedElements(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "ktx.toml", `[pipeline.default]
stages = ['strip-ansi', 'head:100']
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"strip-ansi", "head:100"}
	if len(cfg.PipelineDefault) != 2 {
		t.Fatalf("PipelineDefault = %v, want %v", cfg.PipelineDefault, want)
	}
}

// TestLoad_EscapedStringElement covers basic-string escape decoding
// in array elements: `\"` `\\` `\n` `\t`. M4 stage names will not
// contain these (they are lowercase identifiers with hyphens/colons),
// but the reader must not silently corrupt a config that uses them.
func TestLoad_EscapedStringElement(t *testing.T) {
	chdirTemp(t)
	// A stage name containing a literal quoted-char via escape.
	writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["a\"b"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PipelineDefault) != 1 || cfg.PipelineDefault[0] != `a"b` {
		t.Errorf("PipelineDefault = %v, want [a\"b]", cfg.PipelineDefault)
	}
}

// === M5 plugin tests ===

// TestLoad_PluginBlocks parses one [[plugin]] block with all fields
// populated. Config.Plugins should contain one entry with matching
// fields in document order.
func TestLoad_PluginBlocks(t *testing.T) {
	t.Run("single plugin with all fields", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["mycat"]

[[plugin]]
name = "mycat"
command = "/bin/cat"
description = "passthrough"
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Plugins) != 1 {
			t.Fatalf("Plugins = %v, want 1 entry", cfg.Plugins)
		}
		p := cfg.Plugins[0]
		if p.Name != "mycat" {
			t.Errorf("Plugins[0].Name = %q, want %q", p.Name, "mycat")
		}
		if p.Command != "/bin/cat" {
			t.Errorf("Plugins[0].Command = %q, want %q", p.Command, "/bin/cat")
		}
		if p.Description != "passthrough" {
			t.Errorf("Plugins[0].Description = %q, want %q", p.Description, "passthrough")
		}
	})

	t.Run("multiple plugins in document order", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[[plugin]]
name = "first"
command = "/bin/echo"

[[plugin]]
name = "second"
command = "/bin/cat"
description = "line two"
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Plugins) != 2 {
			t.Fatalf("Plugins = %v, want 2 entries", cfg.Plugins)
		}
		if cfg.Plugins[0].Name != "first" {
			t.Errorf("Plugins[0].Name = %q, want %q", cfg.Plugins[0].Name, "first")
		}
		if cfg.Plugins[1].Name != "second" {
			t.Errorf("Plugins[1].Name = %q, want %q", cfg.Plugins[1].Name, "second")
		}
	})

	t.Run("description absent → empty string", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[[plugin]]
name = "nodesc"
command = "/bin/true"
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Plugins) != 1 {
			t.Fatalf("Plugins = %v, want 1 entry", cfg.Plugins)
		}
		if cfg.Plugins[0].Description != "" {
			t.Errorf("Plugins[0].Description = %q, want empty", cfg.Plugins[0].Description)
		}
	})
}

// TestLoad_PluginsAndStagesCoexist verifies that a ktx.toml with both
// [pipeline.default].stages AND [[plugin]] blocks populates both
// Config.Stages and Config.Plugins.
func TestLoad_PluginsAndStagesCoexist(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["strip-ansi", "mycat", "head:100"]

[[plugin]]
name = "mycat"
command = "/bin/cat"
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PipelineDefault) != 3 {
		t.Fatalf("PipelineDefault = %v, want 3 entries", cfg.PipelineDefault)
	}
	if cfg.PipelineDefault[1] != "mycat" {
		t.Errorf("PipelineDefault[1] = %q, want %q", cfg.PipelineDefault[1], "mycat")
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("Plugins = %v, want 1 entry", cfg.Plugins)
	}
	if cfg.Plugins[0].Name != "mycat" {
		t.Errorf("Plugins[0].Name = %q, want %q", cfg.Plugins[0].Name, "mycat")
	}
}

// TestLoad_NoPluginsM4Shape verifies that an M4-shape ktx.toml
// (stages only, no [[plugin]] blocks) yields nil Plugins and no
// error — the M4 regression byte-identical contract.
func TestLoad_NoPluginsM4Shape(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "ktx.toml", `[pipeline.default]
stages = ["strip-ansi", "head:100"]
`)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PipelineDefault) != 2 {
		t.Fatalf("PipelineDefault = %v, want 2 entries", cfg.PipelineDefault)
	}
	if cfg.Plugins != nil {
		t.Errorf("Plugins = %v, want nil (no [[plugin]] blocks)", cfg.Plugins)
	}
}

// TestLoad_PluginMissingRequiredField verifies that a malformed
// [[plugin]] block (missing name or command) returns *ParseError.
func TestLoad_PluginMissingRequiredField(t *testing.T) {
	t.Run("missing name", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[[plugin]]
command = "/bin/cat"
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want ParseError")
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("Load returned %T, want *ParseError", err)
		}
		if !strings.Contains(pe.Msg, "name") {
			t.Errorf("ParseError.Msg = %q, want 'name'", pe.Msg)
		}
		if !strings.Contains(pe.Path, "ktx.toml") {
			t.Errorf("ParseError.Path = %q, want 'ktx.toml'", pe.Path)
		}
	})

	t.Run("missing command", func(t *testing.T) {
		chdirTemp(t)
		writeFile(t, "ktx.toml", `[[plugin]]
name = "nocmd"
`)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want ParseError")
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("Load returned %T, want *ParseError", err)
		}
		if !strings.Contains(pe.Msg, "command") {
			t.Errorf("ParseError.Msg = %q, want 'command'", pe.Msg)
		}
	})
}
