// Table-driven tests for the ktx.toml loader (M4 plan Task 5).
//
// Coverage per the plan's acceptance:
//   - missing config → empty list + no error (identity default)
//   - project-root ktx.toml parses
//   - user-level fallback used when project-root absent
//   - precedence: project-root wins when both exist
//   - malformed TOML → *ParseError
//   - unknown stage names preserved verbatim (resolution at runner
//     invocation time, not config-load time)
//
// All subtests use t.TempDir()-based fixtures and t.Setenv for
// XDG_CONFIG_HOME so no test mutates the user's real config dir.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
// stage list (no error). A config with only future-M5 keys is
// forward-compatible with M4's minimal loader contract.
func TestLoad_ProjectRootStagesAbsentQuoted(t *testing.T) {
	chdirTemp(t)
	writeFile(t, "ktx.toml", "[plugin]\nname = \"demo\"\n")
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

// TestLoad_Malformed covers the syntax-error path: a malformed
// ktx.toml returns *ParseError so the CLI layer can map it to exit 1.
func TestLoad_Malformed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		// wantSubstr is a substring of the error message; we do
		// not pin the exact wording (the parser's error wording
		// is an implementation detail) but every ParseError
		// must name the file path and the line.
		wantSubstr string
	}{
		{
			name:       "unclosed section header",
			content:    "[pipeline.default\n",
			wantSubstr: "unclosed section header",
		},
		{
			name:       "key without =",
			content:    "[pipeline.default]\njust-a-key\n",
			wantSubstr: "key = value",
		},
		{
			name:       "unterminated string",
			content:    `[pipeline.default]` + "\n" + `stages = ["no-end]` + "\n",
			wantSubstr: "unterminated",
		},
		{
			name:       "unterminated array",
			content:    `[pipeline.default]` + "\n" + `stages = ["a"` + "\n",
			wantSubstr: "unterminated",
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
			if !strings.Contains(pe.Msg, tc.wantSubstr) {
				t.Errorf("ParseError.Msg = %q, want substring %q", pe.Msg, tc.wantSubstr)
			}
			if !strings.Contains(pe.Path, "ktx.toml") {
				t.Errorf("ParseError.Path = %q, want it to name ktx.toml", pe.Path)
			}
			if pe.Line < 1 {
				t.Errorf("ParseError.Line = %d, want >= 1", pe.Line)
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
