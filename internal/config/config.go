// Package config loads ktx's ktx.toml configuration (M4 plan Task 5 /
// D5).
//
// This is the minimal M4 surface: it discovers a ktx.toml at the
// project root (the current working directory) or at
// $XDG_CONFIG_HOME/ktx/config.toml (the user-level fallback), parses
// a single `[pipeline.default]` table's `stages` array (a list of
// stage-name strings), and returns a Config struct with the ordered
// stage list. File-shape:
//
//	# ktx.toml — M4 shape
//	[pipeline.default]
//	stages = ["strip-ansi", "head:100"]
//
// Missing / empty config → empty stage list (the D14 no-ktx.toml
// default; `ktx compress` runs the identity pipeline). Precedence:
// project root wins; when only the user-level file exists it is used;
// when neither exists, identity. Per-invocation only — no caching
// across processes (no global state; M5 may revisit if config becomes
// hot, but M4 has a single Load call per `ktx compress` invocation).
//
// === Dependency choice (plan Task 5 AC7) ===
//
// The choice is between BurntSushi/toml (D5-approved dep, already
// listed for M5) and a hand-rolled minimal reader. We choose the
// hand-rolled reader for M4 on three grounds:
//
//  1. AGENTS.md's "Go stdlib first, no new external deps" — adding
//     the dep now would import it transitively into every ktx
//     binary even when ktx.toml is absent (the D14 default no-config
//     path), which is wasteful for the most common invocation.
//  2. The M4 config shape is one table with one array-of-strings
//     key — a full TOML parser's escape/comment/inline-table
//     machinery is YAGNI for M4. The hand-rolled reader covers the
//     M4 shape, the standard `# comment` lines, and the standard
//     string-array element syntax (single- or double-quoted, with
//     trailing commas allowed).
//  3. M5 will need a richer surface (content-type profiles, shell
//     macros, plugin references) and the right answer then is to
//     adopt BurntSushi/toml across the whole file (the D5-approved
//     dep makes more sense when the TOML gets complex). Adopting it
//     for the M4 minimal shape would mean rewriting the loader
//     twice (hand-roll → BurntSushi for M5), which is more churn
//     than adopting BurntSushi from scratch when M5 lands. The M4
//     reader is small enough (~150 LOC) that the throwaway cost is
//     low and the no-new-dep gain for M4 is real.
//
// The hand-rolled reader's contract for M4:
//   - lines starting with `#` (after leading whitespace) are comments
//   - blank lines are skipped
//   - `[section.name]` headers are recognised (dotted names allowed)
//   - `key = value` lines where value is a TOML string array
//     `["a", "b"]` (single- or double-quoted elements, trailing
//     comma optional, whitespace tolerant)
//   - any other syntax emits a ParseError naming the file and line
//     so the CLI layer maps it to an exit-1 usage error
//
// Unknown stage names returned by Load are preserved verbatim —
// resolution to the pipeline.Registry happens at runner invocation
// time, NOT at config load (the plan's AC4). The runner's
// unknown-stage invariant (3) handles them at runtime.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// projectConfigName is the project-root config filename. ktx looks
// for this in the current working directory (the project root for
// ktx invocations). The cwd-as-project-root convention matches
// Cargo.toml / pyproject.toml / lowfat.toml — the file is colocated
// with the codebase under test/developed.
const projectConfigName = "ktx.toml"

// userConfigPath is the user-level config path relative to
// $XDG_CONFIG_HOME. Matches D5's
// $XDG_CONFIG_HOME/ktx/config.toml (the same place ktx looks for the
// future `[plugin]` registry and other user-scoped settings).
const userConfigPath = "ktx/config.toml"

// Config is the parsed ktx.toml representation. For M4 it carries
// only the default pipeline's stage list; M5 will extend it with
// content-type profiles, shell macros, and plugin references without
// changing the Load signature (additive struct growth).
type Config struct {
	// PipelineDefault is the ordered list of stage-name tokens for
	// the default pipeline (e.g. ["strip-ansi", "head:100"]). Empty
	// when ktx.toml is absent or when [pipeline.default] has no
	// stages key — both map to the D14 identity default.
	PipelineDefault []string
}

// ParseError is returned by Load when a discovered ktx.toml contains
// a syntax error or an unsupported M4 shape. The CLI layer maps it
// to exit 1 (usage). The error names the file path and the offending
// line number so the user can locate the problem; the wrapper keeps
// the underlying syntax description verbatim.
type ParseError struct {
	Path string
	Line int    // 1-indexed
	Msg  string // human-readable syntax description
}

// Error implements the error interface.
func (e *ParseError) Error() string {
	return fmt.Sprintf("ktx.toml %s:%d: %s", e.Path, e.Line, e.Msg)
}

// Load resolves and parses the ktx.toml per the precedence:
// project-root ktx.toml > $XDG_CONFIG_HOME/ktx/config.toml > identity.
// Returns a Config with an empty stage list when no file exists
// (the D14 no-config default). A discovered file with a syntax error
// returns a *ParseError; the CLI layer maps it to exit 1.
//
// Load is per-invocation only: it does not cache across processes
// (no global state; the M4 invocations are tiny and one-shot). M5
// may revisit if config becomes hot, but for M4 the cost is one
// stat + one small parse per `ktx compress` invocation.
func Load() (*Config, error) {
	// 1. Project-root ktx.toml (cwd).
	if projPath, ok := fileExists(projectConfigName); ok {
		return loadFile(projPath)
	}
	// 2. User-level $XDG_CONFIG_HOME/ktx/config.toml.
	userPath, err := userConfigFile()
	if err != nil {
		// An unreadable XDG_CONFIG_HOME is treated as "no user
		// config" — identity. A malformed XDG path is not a
		// fatal error in M4 (the project-root path did not
		// exist either, so the user simply has no config).
		return &Config{}, nil
	}
	if _, ok := fileExists(userPath); ok {
		return loadFile(userPath)
	}
	// 3. No config found → identity (D14 default).
	return &Config{}, nil
}

// userConfigFile resolves the user-level config path per D5:
// $XDG_CONFIG_HOME/ktx/config.toml, with the XDG spec's
// ~/.config/ktx/config.toml fallback when XDG_CONFIG_HOME is unset.
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
// operations can print errors with a stable path. A directory at the
// config path is treated as "does not exist" — the loader's contract
// is "find a readable config file" and a directory does not satisfy
// that contract.
func fileExists(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		// Any other stat error (permission, etc.) is treated as
		// "not found" for M4 — the user will see identity and
		// can investigate via `ktx doctor` (TODO future). A
		// permission error on the project-root config is rare
		// and not worth a dedicated exit code in M4.
		return "", false
	}
	if info.IsDir() {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, true // degrade to the path-as-given
	}
	return abs, true
}

// loadFile reads and parses path. Returns a *ParseError on syntax
// errors so the CLI's toKtxError path can surface a usage exit (1)
// with a stable message. An empty file is a valid Config with an
// empty stage list (the D14 identity default).
func loadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// A read failure after fileExists succeeded is a race
		// (file deleted between stat and read) or a permission
		// change. Surface as a ParseError so the user sees a
		// consistent envelope; the path/line are still named.
		return nil, &ParseError{Path: path, Line: 0, Msg: fmt.Sprintf("read: %v", err)}
	}
	cfg, err := parse(data)
	if err != nil {
		// Annotate the syntax error with the file path so the
		// user can locate it. The parser does not know the
		// path; we add it here.
		if pe, ok := err.(*ParseError); ok {
			pe.Path = path
			return nil, pe
		}
		return nil, &ParseError{Path: path, Line: 0, Msg: err.Error()}
	}
	return cfg, nil
}

// parse is the hand-rolled TOML reader (M4 minimal subset). It
// recognises comments, blank lines, [section.subsection] headers,
// and key = value where value is a TOML string array. Any other
// syntax returns a *ParseError naming the offending line number.
//
// The reader is line-oriented for the section/key parsing (TOML's
// arrays can technically span lines, but M4's stages array fits on
// one line by convention; we DO support multi-line arrays by
// accumulating until the closing bracket).
func parse(data []byte) (*Config, error) {
	cfg := &Config{}
	lines := strings.Split(string(data), "\n")

	// currentSection tracks which [section] header we are inside
	// so the key=value assignment knows where to land. M4 only
	// consumes keys under [pipeline.default].
	currentSection := ""
	// arrayAccum holds a multi-line array in progress (key, partial
	// value text). Empty when not mid-array.
	var arrayAccum struct {
		key   string
		parts []string
		raw   strings.Builder
	}
	inArray := false

	for i, raw := range lines {
		lineNum := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !inArray {
			// Section header.
			if strings.HasPrefix(line, "[") {
				if !strings.HasSuffix(line, "]") {
					return nil, &ParseError{Line: lineNum, Msg: "unclosed section header"}
				}
				currentSection = strings.TrimSpace(line[1 : len(line)-1])
				continue
			}
			// key = value
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				return nil, &ParseError{Line: lineNum, Msg: "expected `key = value`"}
			}
			key := strings.TrimSpace(line[:eq])
			val := strings.TrimSpace(line[eq+1:])
			if key == "" {
				return nil, &ParseError{Line: lineNum, Msg: "empty key"}
			}

			// M4 only cares about stages = [...] under
			// [pipeline.default]; other keys are accepted but
			// ignored (forward-compat for M5). The array-valued
			// path is the only one we need.
			if strings.HasPrefix(val, "[") {
				// Begin a (possibly multi-line) array. We
				// accumulate until the closing bracket.
				arrayAccum.key = key
				arrayAccum.parts = nil
				arrayAccum.raw.Reset()
				arrayAccum.raw.WriteString(val)
				if strings.HasSuffix(val, "]") {
					// Single-line array — close it.
					arr, err := parseStringArray(arrayAccum.raw.String(), lineNum)
					if err != nil {
						return nil, err
					}
					assignArray(cfg, currentSection, arrayAccum.key, arr)
					inArray = false
					arrayAccum.key = ""
					arrayAccum.raw.Reset()
				} else {
					inArray = true
				}
				continue
			}
			// Non-array scalar values are accepted silently for
			// M4 forward-compat (M5 may use them; M4 ignores).
			continue
		}

		// Mid-array: accumulate until closing bracket.
		arrayAccum.raw.WriteString(" ")
		arrayAccum.raw.WriteString(line)
		if strings.Contains(line, "]") {
			arr, err := parseStringArray(arrayAccum.raw.String(), lineNum)
			if err != nil {
				return nil, err
			}
			assignArray(cfg, currentSection, arrayAccum.key, arr)
			inArray = false
			arrayAccum.key = ""
			arrayAccum.raw.Reset()
		}
	}
	if inArray {
		return nil, &ParseError{Line: len(lines), Msg: "unterminated array (missing closing `]`)"}
	}
	return cfg, nil
}

// assignArray places the parsed array under the right section/key.
// For M4 the only consumed destination is
// [pipeline.default].stages. Other arrays are accepted silently.
func assignArray(cfg *Config, section, key string, arr []string) {
	if section == "pipeline.default" && key == "stages" {
		// Defensive copy so the caller cannot mutate the parsed
		// slice via a shared backing array (the parse path
		// reuses buffers).
		out := make([]string, len(arr))
		copy(out, arr)
		cfg.PipelineDefault = out
	}
}

// parseStringArray parses a TOML inline string array like
// `["a", "b",]` and returns the string elements. Single- or
// double-quoted elements are accepted (single-quote is not standard
// TOML for basic strings but several linters allow it; we accept
// both to be lenient). Trailing comma is optional. Whitespace is
// tolerated. lineNum is the line where parsing started, used for
// error messages.
func parseStringArray(s string, lineNum int) ([]string, error) {
	// Trim outer brackets.
	open := strings.IndexByte(s, '[')
	close := strings.LastIndexByte(s, ']')
	if open < 0 || close < 0 || close <= open {
		return nil, &ParseError{Line: lineNum, Msg: "malformed array (missing `]`)"}
	}
	inner := s[open+1 : close]
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return []string{}, nil
	}

	// Walk the comma-separated elements, parsing each as a quoted
	// string. We accept " or ' as the quote char for leniency.
	var out []string
	rest := inner
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		var quote byte
		if rest[0] == '"' || rest[0] == '\'' {
			quote = rest[0]
		} else {
			return nil, &ParseError{Line: lineNum, Msg: fmt.Sprintf("array element not a quoted string: %q", rest)}
		}
		// Find the matching closing quote, respecting backslash
		// escapes for double-quoted elements (single-quoted
		// elements are literal — TOML's basic vs literal
		// string distinction; we support both).
		end := -1
		for i := 1; i < len(rest); i++ {
			if quote == '"' && rest[i] == '\\' {
				i++ // skip escaped char
				continue
			}
			if rest[i] == quote {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, &ParseError{Line: lineNum, Msg: "unterminated string in array"}
		}
		// Extract the body and decode escapes (double-quoted).
		body := rest[1:end]
		var decoded string
		if quote == '"' {
			// Minimal escape handling: \" \\ \n \t. Other
			// escapes pass through verbatim (M4 stage names
			// will not contain them).
			dec := strings.Builder{}
			for i := 0; i < len(body); i++ {
				if body[i] == '\\' && i+1 < len(body) {
					switch body[i+1] {
					case '"':
						dec.WriteByte('"')
					case '\\':
						dec.WriteByte('\\')
					case 'n':
						dec.WriteByte('\n')
					case 't':
						dec.WriteByte('\t')
					default:
						dec.WriteByte(body[i])
						dec.WriteByte(body[i+1])
					}
					i++
					continue
				}
				dec.WriteByte(body[i])
			}
			decoded = dec.String()
		} else {
			// Single-quoted = literal.
			decoded = body
		}
		out = append(out, decoded)

		rest = strings.TrimSpace(rest[end+1:])
		if rest == "" {
			break
		}
		if rest[0] != ',' {
			return nil, &ParseError{Line: lineNum, Msg: "expected `,` between array elements"}
		}
		rest = strings.TrimSpace(rest[1:])
	}
	return out, nil
}
