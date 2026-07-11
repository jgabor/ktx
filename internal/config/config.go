// Package config loads ktx's ktx.toml configuration.
//
// ktx.toml is the single source of truth for the compression pipeline
// and user-defined plugins. The file shape:
//
//	# ktx.toml
//	[pipeline.default]
//	stages = ["strip-ansi", "head:100"]
//
//	[[plugin]]
//	name = "mycat"
//	command = "/bin/cat"
//	description = "passthrough"
//
// Missing / empty config → empty stages, nil plugins (the no-config
// default; `ktx compress` runs the passthrough pipeline). Single
// scope: user-level $XDG_CONFIG_HOME/ktx/ktx.toml; passthrough when
// absent. Per-invocation only — no caching
// across processes (no global state; each `ktx compress` invocation
// calls Load once).
//
// Configuration is parsed with BurntSushi/toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultPipelineTimeout is the critical-path deadline for the pipeline
// run inside `ktx hook` when ktx.toml omits the timeout field.
// `ktx hook` is on the agent's critical path (the agent waits for the
// tool result), so a hanging stage must not block indefinitely.
// Built-in stages finish in well under a second on multi-MB inputs;
// 2s leaves headroom for lightweight external plugins. Slow plugins
// should set an explicit timeout in ktx.toml.
const DefaultPipelineTimeout = 2 * time.Second

// userConfigPath is the user-level config path relative to
// $XDG_CONFIG_HOME. Resolves to
// $XDG_CONFIG_HOME/ktx/ktx.toml.
const userConfigPath = "ktx/ktx.toml"

// Plugin is a user-defined external compression stage registered via
// a [[plugin]] block in ktx.toml. The name is the stage-name token
// used in [pipeline.default].stages; the command is the shell-macro
// executed by the pipeline runner (stdin=input bytes, stdout=output
// bytes). Description is optional (empty string when absent).
type Plugin struct {
	Name        string
	Command     string
	Description string
}

// Config is the parsed ktx.toml representation. It carries
// the default pipeline's stage list and any user-defined plugins. The
// Load signature is additive: new fields can be added without
// breaking callers (struct growth).
type Config struct {
	// PipelineDefault is the ordered list of stage-name tokens for
	// the default pipeline (e.g. ["strip-ansi", "head:100"]). Empty
	// when ktx.toml is absent or when [pipeline.default] has no
	// stages key — both map to the passthrough default.
	PipelineDefault []string

	// Plugins holds user-defined external stages from [[plugin]]
	// blocks, in document order. Nil when no [[plugin]] blocks exist
	// (the no-plugin case). The pipeline runner registers each as
	// an external Builder after the built-in stages so
	// last-writer-wins allows user plugins to override built-ins by
	// name.
	Plugins []Plugin

	// Profiles holds all discovered pipeline profiles from the
	// [pipeline.<name>] tables (e.g. default, json, logs). The
	// "default" key is always present (it maps to PipelineDefault).
	// ktx currently runs the default profile.
	Profiles map[string][]string

	// PipelineTimeout is the configured pipeline deadline for the
	// [pipeline.default] table's `timeout` field, parsed as a Go
	// duration string (e.g. "2s", "500ms", "30s"). Default
	// DefaultPipelineTimeout (2s) when the field is absent. `ktx hook`
	// reads this so a user can tune the critical-path deadline.
	// `ktx compress` ignores it (off-line, timeout=0, developer can
	// Ctrl-C). A non-positive or unparseable value is a *ParseError at
	// config-load time. Only the "default" profile's timeout is read.
	PipelineTimeout time.Duration

	// RecoverFooter is the [pipeline.default].recover_footer flag.
	// Defaults to false when the field is absent or no ktx.toml exists.
	// Set recover_footer = true to surface a `ktx read <source_id>` cue.
	// Only the "default" profile's value is read.
	RecoverFooter bool
}

// ParseError is returned by Load when a discovered ktx.toml contains
// a syntax error or a missing required field. The CLI layer maps it
// to exit 1 (usage). The error names the file path and the offending
// line number (when known) so the user can locate the problem.
type ParseError struct {
	Path string
	Line int    // 1-indexed; 0 when unavailable
	Msg  string // human-readable error description
}

// Error implements the error interface.
func (e *ParseError) Error() string {
	return fmt.Sprintf("ktx.toml %s:%d: %s", e.Path, e.Line, e.Msg)
}

// Load resolves and parses the ktx.toml from the user-level config
// path ($XDG_CONFIG_HOME/ktx/ktx.toml, with ~/.config/ktx/ktx.toml
// fallback per the XDG spec). Returns a Config with empty stages and
// nil plugins when no file exists (the no-config default). A discovered
// file with a syntax error or missing required field returns a
// *ParseError; the CLI layer maps it to exit 1.
//
// ConfigPath returns the resolved path to the ktx.toml that Load()
// would use, and whether it exists. Used by `ktx plugin` to know
// which file to edit. When no config file exists, returns ("", false).
func ConfigPath() (string, bool) {
	userPath, err := userConfigFile()
	if err != nil {
		return "", false
	}
	if _, ok := fileExists(userPath); ok {
		return userPath, true
	}
	return "", false
}

// UserConfigPath returns the user-level config path, creating the
// parent directory if needed. Used by `ktx plugin add` when creating
// a new config file.
func UserConfigPath() (string, error) {
	userPath, err := userConfigFile()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	return userPath, nil
}

// Load is per-invocation only: it does not cache across processes
// (no global state).
func Load() (*Config, error) {
	// User-level $XDG_CONFIG_HOME/ktx/ktx.toml.
	userPath, err := userConfigFile()
	if err != nil {
		return defaultConfig(), nil
	}
	if _, ok := fileExists(userPath); ok {
		return loadFile(userPath)
	}
	// No config found → passthrough default.
	return defaultConfig(), nil
}

// defaultConfig is the no-file / load-error-safe baseline: empty stages
// (passthrough), 2s pipeline timeout, recover_footer off.
func defaultConfig() *Config {
	return &Config{
		PipelineTimeout: DefaultPipelineTimeout,
		RecoverFooter:   false,
	}
}

// userConfigFile resolves the user-level config path:
// $XDG_CONFIG_HOME/ktx/ktx.toml, with the XDG spec's
// ~/.config/ktx/ktx.toml fallback when XDG_CONFIG_HOME is unset.
func userConfigFile() (string, error) {
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		xdgConfig = filepath.Join(home, ".config")
	}
	return filepath.Join(xdgConfig, userConfigPath), nil
}

// fileExists reports whether path exists as a regular file (not a
// directory). Returns the resolved absolute path so subsequent load
// operations can print errors with a stable path.
func fileExists(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		return "", false
	}
	if info.IsDir() {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, true
	}
	return abs, true
}

// loadFile reads and parses path. Returns a *ParseError on syntax
// errors or missing required [[plugin]] fields so the CLI's toKtxError
// path can surface a usage exit (1) with a stable message. An empty
// file is a valid Config with empty stages and nil plugins (the
// passthrough default).
func loadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ParseError{Path: path, Line: 0, Msg: fmt.Sprintf("read: %v", err)}
	}
	cfg, err := parse(data)
	if err != nil {
		if pe, ok := err.(*ParseError); ok {
			pe.Path = path
			return nil, pe
		}
		return nil, &ParseError{Path: path, Line: 0, Msg: err.Error()}
	}
	return cfg, nil
}

// tomlConfig is the intermediate decode struct mapping the TOML
// document into Go fields. BurntSushi decodes into this struct;
// parse maps it to the public Config. Pipeline is decoded as a map
// so every profile ([pipeline.json], [pipeline.logs]) is captured without
// struct changes, even though ktx does not route by content type.
type tomlConfig struct {
	Pipeline map[string]struct {
		Stages        []string `toml:"stages"`
		Timeout       string   `toml:"timeout"`
		RecoverFooter *bool    `toml:"recover_footer"` // nil = default false
	} `toml:"pipeline"`
	Plugin []tomlPlugin `toml:"plugin"`
}

// tomlPlugin maps a single [[plugin]] block. Name and Command are
// required; Description is optional.
type tomlPlugin struct {
	Name        string `toml:"name"`
	Command     string `toml:"command"`
	Description string `toml:"description"`
}

// parse decodes TOML bytes into a Config using BurntSushi/toml. A
// syntax error is wrapped in a *ParseError preserving the line number
// and message from BurntSushi's ParseError so the CLI layer's error
// envelope contract (Path/Line/Msg) is maintained. Missing required
// [[plugin]] fields (name, command) are validated after decode and
// also return *ParseError.
func parse(data []byte) (*Config, error) {
	var decoded tomlConfig
	if _, err := toml.Decode(string(data), &decoded); err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			return nil, &ParseError{
				Line: pe.Position.Line,
				Msg:  pe.Message,
			}
		}
		return nil, &ParseError{Line: 0, Msg: err.Error()}
	}

	// Build the Profiles map from all discovered pipeline tables.
	profiles := make(map[string][]string, len(decoded.Pipeline))
	for name, p := range decoded.Pipeline {
		// Defensive copy so the caller cannot mutate the parsed
		// slice via a shared backing array.
		out := make([]string, len(p.Stages))
		copy(out, p.Stages)
		profiles[name] = out
	}

	cfg := &Config{
		PipelineDefault: profiles["default"],
		Profiles:        profiles,
	}

	// Parse the configurable pipeline timeout. Stored as a raw
	// string in TOML and parsed here via time.ParseDuration so an
	// invalid duration surfaces as a *ParseError while loading config.
	// Empty → DefaultPipelineTimeout (2s). Only the "default" profile's
	// timeout is read.
	cfg.PipelineTimeout = DefaultPipelineTimeout
	if def, ok := decoded.Pipeline["default"]; ok && def.Timeout != "" {
		d, err := time.ParseDuration(def.Timeout)
		if err != nil {
			return nil, &ParseError{
				Line: 0,
				Msg:  fmt.Sprintf("[pipeline.default] timeout: invalid duration %q: %v", def.Timeout, err),
			}
		}
		if d <= 0 {
			return nil, &ParseError{
				Line: 0,
				Msg:  fmt.Sprintf("[pipeline.default] timeout: must be positive, got %q", def.Timeout),
			}
		}
		cfg.PipelineTimeout = d
	}

	// recover_footer defaults to false. A *bool decode distinguishes
	// "key absent" from an explicit value. Only the
	// "default" profile's value is read. A non-bool value surfaces as
	// a *ParseError via BurntSushi's decode error (handled above).
	cfg.RecoverFooter = false
	if def, ok := decoded.Pipeline["default"]; ok && def.RecoverFooter != nil {
		cfg.RecoverFooter = *def.RecoverFooter
	}

	for i, p := range decoded.Plugin {
		if p.Name == "" {
			return nil, &ParseError{
				Line: 0,
				Msg:  fmt.Sprintf("[[plugin]] block at index %d missing required field 'name'", i),
			}
		}
		if p.Command == "" {
			return nil, &ParseError{
				Line: 0,
				Msg:  fmt.Sprintf("[[plugin]] block %q missing required field 'command'", p.Name),
			}
		}
		cfg.Plugins = append(cfg.Plugins, Plugin(p))
	}

	return cfg, nil
}
