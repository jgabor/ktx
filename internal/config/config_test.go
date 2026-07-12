// Table-driven tests for the ktx.toml loader (stage-list + plugin
// support via BurntSushi/toml).
//
// Coverage includes:
//   - missing config → empty list + no error (passthrough default)
//   - user-level ktx.toml parses
//   - user-level config loads from $XDG_CONFIG_HOME/ktx/ktx.toml
//   - malformed TOML → *ParseError
//   - unknown stage names preserved verbatim (resolution at runner
//     invocation time, not config-load time)
//   - [[plugin]] blocks parse into Config.Plugins
//   - missing required plugin fields → *ParseError
//   - stages-only shape → Plugins nil, no error (regression)
//   - recover_footer: false default (absent + no-config), true/false
//     explicit, non-bool value → *ParseError naming the field
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

// writeUserConfig writes content to the user-level config path
// ($XDG_CONFIG_HOME/ktx/ktx.toml). Requires XDG_CONFIG_HOME to be set
// (tests set it via t.Setenv before calling this helper).
func writeUserConfig(t *testing.T, content string) {
	t.Helper()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		t.Fatal("XDG_CONFIG_HOME not set")
	}
	path := filepath.Join(xdg, "ktx", "ktx.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// chdirTemp changes the test's working directory to a fresh tmpdir so
// the repo's own ktx.toml (or example config) is not picked up from
// cwd during test runs inside the ktx repo. The previous cwd is
// restored on cleanup. The fresh tmpdir is returned so the test can
// write fixtures into it if needed.
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

// TestLoad_MissingConfig covers the no-config default: when no
// user-level ktx.toml exists, Load returns an empty stage list and a
// nil error.
func TestLoad_MissingConfig(t *testing.T) {
	t.Run("no user config → empty stage list, no error", func(t *testing.T) {
		// Isolate cwd so no repo ktx.toml is found.
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

// TestLoad_UserConfig covers the user-level path: a ktx.toml at
// $XDG_CONFIG_HOME/ktx/ktx.toml is parsed, the stages array
// populates PipelineDefault, and the order is preserved.
func TestLoad_UserConfig(t *testing.T) {
	t.Run("user-level ktx.toml parses with ordered stages", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `# ktx.toml — stage-list fixture
[pipeline.default]
stages = ["strip-ansi", "head:100"]
`)

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
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, "[pipeline.default]\nstages = []\n")

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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeUserConfig(t, "[ui]\ntheme = \"dark\"\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PipelineDefault) != 0 {
		t.Errorf("PipelineDefault = %v, want empty (no stages key)", cfg.PipelineDefault)
	}
}

// TestLoad_UserConfigPath covers the user-level config path: a
// ktx.toml at $XDG_CONFIG_HOME/ktx/ktx.toml is loaded by Load.
func TestLoad_UserConfigPath(t *testing.T) {
	t.Run("user-level config is loaded", func(t *testing.T) {
		chdirTemp(t)

		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["normalize"]
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.PipelineDefault) != 1 || cfg.PipelineDefault[0] != "normalize" {
			t.Errorf("PipelineDefault = %v, want [normalize]", cfg.PipelineDefault)
		}
	})
}

// TestLoad_PipelineTimeout covers the configurable pipeline
// timeout: [pipeline.default] timeout parses as a Go duration string,
// defaults to DefaultPipelineTimeout (2s) when omitted or when no
// ktx.toml exists, and a garbage/non-positive value is a *ParseError
// at config-load time.
func TestLoad_PipelineTimeout(t *testing.T) {
	t.Run("timeout='10s' parses to 10s (configurable)", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]
timeout = "10s"
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if ResolveTimeout(cfg, "default") != 10*time.Second {
			t.Errorf("PipelineTimeout = %v, want 10s", ResolveTimeout(cfg, "default"))
		}
	})

	t.Run("no timeout field → default 2s", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if ResolveTimeout(cfg, "default") != DefaultPipelineTimeout {
			t.Errorf("PipelineTimeout = %v, want %v (default 2s)", ResolveTimeout(cfg, "default"), DefaultPipelineTimeout)
		}
		if DefaultPipelineTimeout != 2*time.Second {
			t.Errorf("DefaultPipelineTimeout = %v, want 2s", DefaultPipelineTimeout)
		}
	})

	t.Run("no ktx.toml → default 2s (passthrough path)", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if ResolveTimeout(cfg, "default") != DefaultPipelineTimeout {
			t.Errorf("PipelineTimeout = %v, want %v (default on passthrough path)", ResolveTimeout(cfg, "default"), DefaultPipelineTimeout)
		}
	})

	t.Run("garbage timeout → *ParseError", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]
timeout = "garbage"
`)

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
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				writeUserConfig(t, fmt.Sprintf(`[pipeline.default]
stages = ["head:3"]
timeout = %q
`, bad))

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
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]
timeout = "2m"
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if ResolveTimeout(cfg, "default") != 2*time.Minute {
			t.Errorf("PipelineTimeout = %v, want 2m", ResolveTimeout(cfg, "default"))
		}
	})
}

// TestLoad_RecoverFooter covers the recover_footer config surface on
// [pipeline.default]. The flag defaults to false when absent or no
// ktx.toml exists. Explicit true enables the recovery footer.
// A non-bool value is a *ParseError naming the field.
func TestLoad_RecoverFooter(t *testing.T) {
	t.Run("no ktx.toml → false (default off)", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if ResolveRecoverFooter(cfg, "default") {
			t.Errorf("RecoverFooter = true, want false (no-config default)")
		}
	})

	t.Run("stages-only config (no recover_footer key) → false", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "head:100"]
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if ResolveRecoverFooter(cfg, "default") {
			t.Errorf("RecoverFooter = true, want false (absent key defaults to false)")
		}
	})

	t.Run("recover_footer = true → true", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = true
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !ResolveRecoverFooter(cfg, "default") {
			t.Errorf("RecoverFooter = false, want true")
		}
	})

	t.Run("recover_footer = false → false (explicit off)", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = false
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if ResolveRecoverFooter(cfg, "default") {
			t.Errorf("RecoverFooter = true, want false (explicit off)")
		}
	})

	t.Run("recover_footer = \"yes\" (non-bool) → *ParseError naming the field", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = "yes"
`)

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want *ParseError for non-bool recover_footer")
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("error = %T, want *ParseError", err)
		}
		if !strings.Contains(pe.Msg, "recover_footer") {
			t.Errorf("ParseError.Msg = %q, want it to name the field 'recover_footer'", pe.Msg)
		}
		if !strings.Contains(pe.Path, "ktx.toml") {
			t.Errorf("ParseError.Path = %q, want it to name ktx.toml", pe.Path)
		}
	})
}

func TestNamedBenchmarkPipelines(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "tools", "bench", "pipelines", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no named benchmark pipelines found")
	}

	expectedRecovery := map[string]bool{
		"conservative-a-recover": true,
	}
	for _, file := range files {
		file := file
		t.Run(strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := parse(data)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
			if ResolveRecoverFooter(cfg, "default") != expectedRecovery[name] {
				t.Errorf("RecoverFooter = %v, want %v", ResolveRecoverFooter(cfg, "default"), expectedRecovery[name])
			}
			if ResolveTimeout(cfg, "default") != DefaultPipelineTimeout {
				t.Errorf("PipelineTimeout = %v, want %v", ResolveTimeout(cfg, "default"), DefaultPipelineTimeout)
			}
			if cfg.PipelineDefault == nil {
				t.Error("PipelineDefault is nil; named pipelines must declare stages")
			}
		})
	}
}

// TestResolveTimeout covers per-profile timeout resolution: a selected
// profile's own timeout is used when set, otherwise the default
// profile's timeout, otherwise DefaultPipelineTimeout.
func TestResolveTimeout(t *testing.T) {
	const toml = `[pipeline.default]
stages = ["head:3"]
timeout = "2s"

[pipeline.fast]
stages = ["head:1"]
timeout = "50ms"

[pipeline.bare]
stages = ["head:2"]
`
	c, err := parse([]byte(toml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// A profile with its own timeout resolves to that value.
	if got := ResolveTimeout(c, "fast"); got != 50*time.Millisecond {
		t.Errorf("ResolveTimeout(fast) = %v, want 50ms (the profile's own timeout)", got)
	}
	// A profile without a timeout inherits the default profile's timeout.
	if got := ResolveTimeout(c, "bare"); got != 2*time.Second {
		t.Errorf("ResolveTimeout(bare) = %v, want 2s (inherited from the default profile)", got)
	}
	// The default profile resolves to its own value.
	if got := ResolveTimeout(c, "default"); got != 2*time.Second {
		t.Errorf("ResolveTimeout(default) = %v, want 2s (the default profile's own timeout)", got)
	}
	// An unknown profile falls back to the default profile's timeout.
	if got := ResolveTimeout(c, "missing"); got != 2*time.Second {
		t.Errorf("ResolveTimeout(missing) = %v, want 2s (fallback to the default profile)", got)
	}
}

// TestResolveTimeout_NeitherProfileNorDefault covers a config where both
// the selected profile and the default profile omit a timeout: the
// const DefaultPipelineTimeout is used (so the no-config passthrough and
// profiles that set nothing still get a sensible critical-path deadline).
func TestResolveTimeout_NeitherProfileNorDefault(t *testing.T) {
	const toml = `[pipeline.default]
stages = ["head:3"]

[pipeline.bare]
stages = ["head:2"]
`
	c, err := parse([]byte(toml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := ResolveTimeout(c, "bare"); got != DefaultPipelineTimeout {
		t.Errorf("ResolveTimeout(bare) = %v, want %v (const default when profile and default both omit)", got, DefaultPipelineTimeout)
	}
	if got := ResolveTimeout(c, "default"); got != DefaultPipelineTimeout {
		t.Errorf("ResolveTimeout(default) = %v, want %v (const default when default omits)", got, DefaultPipelineTimeout)
	}
}

// TestResolveTimeout_NoConfig covers the no-ktx.toml baseline: a nil
// Profiles map yields DefaultPipelineTimeout for any profile name.
func TestResolveTimeout_NoConfig(t *testing.T) {
	c := defaultConfig()
	if got := ResolveTimeout(c, "default"); got != DefaultPipelineTimeout {
		t.Errorf("ResolveTimeout(default) = %v, want %v (const default on the no-config path)", got, DefaultPipelineTimeout)
	}
	if got := ResolveTimeout(c, "anything"); got != DefaultPipelineTimeout {
		t.Errorf("ResolveTimeout(anything) = %v, want %v (const default on the no-config path)", got, DefaultPipelineTimeout)
	}
}

// TestResolveRecoverFooter covers per-profile recover_footer resolution:
// a selected profile's own value is used when set, otherwise the default
// profile's value, otherwise false.
func TestResolveRecoverFooter(t *testing.T) {
	const toml = `[pipeline.default]
stages = ["head:3"]
recover_footer = true

[pipeline.silent]
stages = ["head:1"]
recover_footer = false

[pipeline.bare]
stages = ["head:2"]
`
	c, err := parse([]byte(toml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// A profile with its own explicit value resolves to that value.
	if got := ResolveRecoverFooter(c, "silent"); got != false {
		t.Errorf("ResolveRecoverFooter(silent) = %v, want false (the profile's own explicit value)", got)
	}
	if got := ResolveRecoverFooter(c, "default"); got != true {
		t.Errorf("ResolveRecoverFooter(default) = %v, want true (the default profile's own value)", got)
	}
	// A profile without a value inherits the default profile's value.
	if got := ResolveRecoverFooter(c, "bare"); got != true {
		t.Errorf("ResolveRecoverFooter(bare) = %v, want true (inherited from the default profile)", got)
	}
	// An unknown profile falls back to the default profile's value.
	if got := ResolveRecoverFooter(c, "missing"); got != true {
		t.Errorf("ResolveRecoverFooter(missing) = %v, want true (fallback to the default profile)", got)
	}
}

// TestResolveRecoverFooter_NeitherProfileNorDefault covers a config where
// both the selected profile and the default profile omit recover_footer:
// the const false is used (the no-config passthrough suppresses the
// footer).
func TestResolveRecoverFooter_NeitherProfileNorDefault(t *testing.T) {
	const toml = `[pipeline.default]
stages = ["head:3"]

[pipeline.bare]
stages = ["head:2"]
`
	c, err := parse([]byte(toml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := ResolveRecoverFooter(c, "bare"); got != false {
		t.Errorf("ResolveRecoverFooter(bare) = %v, want false (neither profile nor default sets it)", got)
	}
	if got := ResolveRecoverFooter(c, "default"); got != false {
		t.Errorf("ResolveRecoverFooter(default) = %v, want false (default omits it)", got)
	}
}

// TestResolveRecoverFooter_NoConfig covers the no-ktx.toml baseline: a
// nil Profiles map yields false for any profile name.
func TestResolveRecoverFooter_NoConfig(t *testing.T) {
	c := defaultConfig()
	if got := ResolveRecoverFooter(c, "default"); got != false {
		t.Errorf("ResolveRecoverFooter(default) = %v, want false (const default on the no-config path)", got)
	}
	if got := ResolveRecoverFooter(c, "anything"); got != false {
		t.Errorf("ResolveRecoverFooter(anything) = %v, want false (const default on the no-config path)", got)
	}
}

// TestLoad_PerProfileTimeout_Validated covers that a timeout set on ANY
// profile (not just default) is parsed and validated: a garbage or
// non-positive value in a non-default profile is a *ParseError naming
// that profile, and a valid non-default timeout surfaces via the
// profile's own Profile.Timeout (so ResolveTimeout returns it).
func TestLoad_PerProfileTimeout_Validated(t *testing.T) {
	t.Run("non-default valid timeout parses and is set on the profile", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]

[pipeline.logs]
stages = ["head:5"]
timeout = "750ms"
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Profiles["logs"].Timeout != 750*time.Millisecond {
			t.Errorf("Profiles[logs].Timeout = %v, want 750ms (parsed per-profile)", cfg.Profiles["logs"].Timeout)
		}
		// default sets no timeout → its Profile.Timeout stays zero.
		if cfg.Profiles["default"].Timeout != 0 {
			t.Errorf("Profiles[default].Timeout = %v, want 0 (default omits timeout)", cfg.Profiles["default"].Timeout)
		}
	})

	t.Run("non-default garbage timeout → *ParseError naming the profile", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]

[pipeline.logs]
stages = ["head:5"]
timeout = "nope"
`)

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want *ParseError for a garbage timeout on a non-default profile")
		}
		pe, ok := err.(*ParseError)
		if !ok {
			t.Fatalf("error = %T, want *ParseError", err)
		}
		if !strings.Contains(pe.Msg, "pipeline.logs") {
			t.Errorf("ParseError.Msg = %q, want it to name the offending profile [pipeline.logs]", pe.Msg)
		}
		if !strings.Contains(pe.Msg, "timeout") {
			t.Errorf("ParseError.Msg = %q, want it to mention 'timeout'", pe.Msg)
		}
	})

	t.Run("non-default non-positive timeout → *ParseError", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["head:3"]

[pipeline.logs]
stages = ["head:5"]
timeout = "0s"
`)

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want *ParseError for a non-positive timeout on a non-default profile")
		}
		if _, ok := err.(*ParseError); !ok {
			t.Errorf("error = %T, want *ParseError", err)
		}
	})
}

// TestLoad_Malformed covers the syntax-error path: a malformed
// ktx.toml returns *ParseError so the CLI layer can map it to exit 1.
// BurntSushi/toml's error messages differ from the prior hand-rolled
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
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			writeUserConfig(t, tc.content)

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

// TestLoad_UnknownStageNamesPreserved covers the unknown-stage
// invariant: unknown stage names in the array are returned as-is. Resolution to the Registry
// happens at runner invocation time, not at config load — the
// unknown-stage invariant (3) handles them at runtime.
func TestLoad_UnknownStageNamesPreserved(t *testing.T) {
	chdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "frobnicate", "head:5", "bogus:name"]
`)

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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeUserConfig(t, `[pipeline.default]
stages = [
  "strip-ansi",
  "head:100",
]
`)

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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeUserConfig(t, `[pipeline.default]
stages = ['strip-ansi', 'head:100']
`)

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
// in array elements: `\"` `\\` `\n` `\t`. Stage names will not
// contain these (they are lowercase identifiers with hyphens/colons),
// but the reader must not silently corrupt a config that uses them.
func TestLoad_EscapedStringElement(t *testing.T) {
	chdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A stage name containing a literal quoted-char via escape.
	writeUserConfig(t, `[pipeline.default]
stages = ["a\"b"]
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.PipelineDefault) != 1 || cfg.PipelineDefault[0] != `a"b` {
		t.Errorf("PipelineDefault = %v, want [a\"b]", cfg.PipelineDefault)
	}
}

// === Plugin tests ===

// TestLoad_PluginBlocks parses one [[plugin]] block with all fields
// populated. Config.Plugins should contain one entry with matching
// fields in document order.
func TestLoad_PluginBlocks(t *testing.T) {
	t.Run("single plugin with all fields", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["mycat"]

[[plugin]]
name = "mycat"
command = "/bin/cat"
description = "passthrough"
`)

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
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[[plugin]]
name = "first"
command = "/bin/echo"

[[plugin]]
name = "second"
command = "/bin/cat"
description = "line two"
`)

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
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[[plugin]]
name = "nodesc"
command = "/bin/true"
`)

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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "mycat", "head:100"]

[[plugin]]
name = "mycat"
command = "/bin/cat"
`)

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

// TestLoad_NoPluginsStagesOnlyShape verifies that a stages-only ktx.toml
// (stages only, no [[plugin]] blocks) yields nil Plugins and no
// error — the regression byte-identical contract.
func TestLoad_NoPluginsStagesOnlyShape(t *testing.T) {
	chdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "head:100"]
`)

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
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[[plugin]]
command = "/bin/cat"
`)

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
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[[plugin]]
name = "nocmd"
`)

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

// TestLoad_RouteRules covers [[route]] block parsing: rules parse into
// Config.Routes in document order with all match fields populated;
// empty exit_status normalizes to "any"; the profile reference must
// name an existing [pipeline.<name>] table.
func TestLoad_RouteRules(t *testing.T) {
	t.Run("route rules parse with all fields, document order preserved", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]

[pipeline.logs]
stages = ["head:50"]

[[route]]
tool = "bash"
command_prefix = "git"
exit_status = "success"
profile = "logs"

[[route]]
tool = "bash"
exit_status = "failure"
profile = "default"
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Routes) != 2 {
			t.Fatalf("Routes = %v, want 2 rules", cfg.Routes)
		}
		r0 := cfg.Routes[0]
		if r0.Tool != "bash" {
			t.Errorf("Routes[0].Tool = %q, want bash", r0.Tool)
		}
		if r0.CommandPrefix != "git" {
			t.Errorf("Routes[0].CommandPrefix = %q, want git", r0.CommandPrefix)
		}
		if r0.ExitStatus != "success" {
			t.Errorf("Routes[0].ExitStatus = %q, want success", r0.ExitStatus)
		}
		if r0.Profile != "logs" {
			t.Errorf("Routes[0].Profile = %q, want logs", r0.Profile)
		}
		r1 := cfg.Routes[1]
		if r1.Profile != "default" {
			t.Errorf("Routes[1].Profile = %q, want default", r1.Profile)
		}
	})

	t.Run("empty exit_status normalizes to any", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]

[[route]]
tool = "bash"
profile = "default"
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Routes) != 1 {
			t.Fatalf("Routes = %v, want 1 rule", cfg.Routes)
		}
		if cfg.Routes[0].ExitStatus != "any" {
			t.Errorf("ExitStatus = %q, want any (normalized from empty)", cfg.Routes[0].ExitStatus)
		}
	})

	t.Run("empty match fields are valid (profile-only rule)", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]

[[route]]
profile = "default"
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Routes) != 1 {
			t.Fatalf("Routes = %v, want 1 rule", cfg.Routes)
		}
		if cfg.Routes[0].Tool != "" {
			t.Errorf("Tool = %q, want empty", cfg.Routes[0].Tool)
		}
		if cfg.Routes[0].CommandPrefix != "" {
			t.Errorf("CommandPrefix = %q, want empty", cfg.Routes[0].CommandPrefix)
		}
	})
}

// TestLoad_NoRouteRules covers the no-rules case: a ktx.toml without
// [[route]] blocks yields nil Routes and the existing fields
// (PipelineDefault, Profiles, Plugins) are unchanged.
func TestLoad_NoRouteRules(t *testing.T) {
	t.Run("stages-only config → nil Routes, behavior unchanged", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "head:100"]
`)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Routes != nil {
			t.Errorf("Routes = %v, want nil (no [[route]] blocks)", cfg.Routes)
		}
		if len(cfg.PipelineDefault) != 2 {
			t.Errorf("PipelineDefault = %v, want 2 entries", cfg.PipelineDefault)
		}
		if cfg.Plugins != nil {
			t.Errorf("Plugins = %v, want nil", cfg.Plugins)
		}
	})

	t.Run("no-config default → nil Routes", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Routes != nil {
			t.Errorf("Routes = %v, want nil (no config)", cfg.Routes)
		}
	})
}

// TestLoad_RouteRuleMissingProfile verifies that a [[route]] block
// referencing a profile name not present in any [pipeline.<name>]
// table returns *ParseError naming the offending rule index and the
// missing profile name.
func TestLoad_RouteRuleMissingProfile(t *testing.T) {
	t.Run("unknown profile → ParseError names rule and missing profile", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]

[[route]]
tool = "bash"
profile = "nonexistent"
`)

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want ParseError")
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("Load returned %T, want *ParseError", err)
		}
		if !strings.Contains(pe.Msg, "nonexistent") {
			t.Errorf("ParseError.Msg = %q, want it to name the missing profile 'nonexistent'", pe.Msg)
		}
		if !strings.Contains(pe.Msg, "profile") {
			t.Errorf("ParseError.Msg = %q, want it to mention 'profile'", pe.Msg)
		}
		if !strings.Contains(pe.Path, "ktx.toml") {
			t.Errorf("ParseError.Path = %q, want 'ktx.toml'", pe.Path)
		}
	})

	t.Run("missing profile field → ParseError", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]

[[route]]
tool = "bash"
`)

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want ParseError")
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("Load returned %T, want *ParseError", err)
		}
		if !strings.Contains(pe.Msg, "profile") {
			t.Errorf("ParseError.Msg = %q, want it to mention 'profile'", pe.Msg)
		}
		if !strings.Contains(pe.Path, "ktx.toml") {
			t.Errorf("ParseError.Path = %q, want 'ktx.toml'", pe.Path)
		}
	})

	t.Run("second rule's unknown profile named with correct index", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		writeUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]

[pipeline.logs]
stages = ["head:50"]

[[route]]
tool = "bash"
profile = "logs"

[[route]]
tool = "bash"
profile = "missing-prof"
`)

		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded; want ParseError")
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("Load returned %T, want *ParseError", err)
		}
		if !strings.Contains(pe.Msg, "missing-prof") {
			t.Errorf("ParseError.Msg = %q, want it to name 'missing-prof'", pe.Msg)
		}
		if !strings.Contains(pe.Msg, "index 1") {
			t.Errorf("ParseError.Msg = %q, want it to name index 1", pe.Msg)
		}
	})
}
