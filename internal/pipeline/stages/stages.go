// Package stages holds ktx's built-in compression pipeline stages
// (M4 plan Task 4 / D14).
//
// Each stage is a pure function over bytes ([]byte → []byte, error-or-nil)
// implementing the pipeline.Stage interface. Stages carry NO database
// access, NO filesystem access, and NO global state — the runner
// applies them in sequence and the conservative-on-failure invariants
// (empty-output guard, stage-error passthrough, unknown-stage skip)
// live in the runner, NOT in the stage author's convention (D14
// explicitly rejects the lowfat failure mode).
//
// The five built-in stages for M4:
//
//   - strip-ansi  : removes ANSI escape sequences (CSI … m, cursor
//                   moves, etc.) so colored terminal transcripts
//                   become plain text.
//   - dedup-blank : collapses runs of 2+ consecutive blank lines to
//                   a single blank line; single blank lines and
//                   non-blank content are byte-identical.
//   - head        : keeps the first N lines (default N=10; configurable
//                   via `head:100`). Input shorter than N is returned
//                   unchanged; trailing newline preserved.
//   - truncate    : keeps the first N bytes (default N=4096;
//                   configurable via `truncate:4096`) and appends a
//                   marker line `[ktx: truncated N of M bytes]` when
//                   truncation occurred. Input shorter than N is
//                   returned unchanged.
//   - normalize   : normalizes mixed line endings (\r\n, \r) to \n
//                   and trims trailing whitespace from each line;
//                   leading whitespace is preserved.
//
// All five stages are OFF by default (D14): only stages named in a
// ktx.toml's `[pipeline.default].stages` array run. RegisterBuiltins
// populates a registry at package init so the CLI's compress verb
// can resolve names without a separate plugin loader; the plugin
// registry (TODO:30, M5) will add a `ktx plugin add` path that
// registers additional names via external processes.
//
// Stage-name tokens: `head`, `truncate`, and (future) any
// parameterised stage use a `name:arg` token form. The Registry's
// Lookup splits the token at the first colon; the Builder receives
// the arg (or "" for the bare-name form) and returns a Stage whose
// configured Behavior depends on the arg.
package stages

import (
	"github.com/jgabor/ktx/internal/pipeline"
)

// builtinStageNames is the ordered list of built-in stage names,
// matching the registration order in RegisterBuiltins. Used by
// `ktx plugin list` to enumerate built-in stages alongside
// user-defined plugins.
var builtinStageNames = []string{
	"strip-ansi",
	"dedup-blank",
	"normalize",
	"head",
	"truncate",
}

// BuiltinStageNames returns the names of all built-in pipeline stages
// in registration order. The caller may use this to present a unified
// view of valid stage names (builtins + user plugins) to agents.
func BuiltinStageNames() []string {
	return builtinStageNames
}

// RegisterBuiltins registers all five built-in stages on reg. A nil
// reg is a no-op (defensive; the caller passes a fresh
// pipeline.NewRegistry() in normal use).
//
// The three simple stages (strip-ansi, dedup-blank, normalize) are
// registered via Register — their token form is the bare name and
// they ignore any `:arg` portion. The two parameterised stages (head,
// truncate) are registered via RegisterBuilder — they parse the arg
// via strconv.Atoi to configure how many lines/bytes to keep.
//
// Stages registered here are the ONLY built-in stages ktx ships with
// in M4; the ktx.toml `[pipeline.default].stages` array names which
// of them run (a stage registered here but not named in the config
// does NOT run — D14's "OFF by default" contract).
func RegisterBuiltins(reg *pipeline.Registry) {
	if reg == nil {
		return
	}
	// Simple stages — bytes-in, bytes-out, no configuration token.
	reg.Register("strip-ansi", pipeline.StageFunc(StripANSI))
	reg.Register("dedup-blank", pipeline.StageFunc(DedupBlank))
	reg.Register("normalize", pipeline.StageFunc(Normalize))

	// Parameterised stages — RegisterBuilder receives the parsed
	// arg (or "" for the bare-name form) and returns a Stage whose
	// Behavior depends on the arg.
	reg.RegisterBuilder("head", headBuilder)
	reg.RegisterBuilder("truncate", truncateBuilder)
}

// ParseNameArg splits a `name[:arg]` stage-name token into its name
// and arg parts. The split happens at the FIRST colon so a name
// containing colons is impossible (stage names are identifiers; the
// arg may contain colons in pathological inputs, though neither
// built-in parameterised stage accepts a colon-bearing arg). A token
// with no colon returns (token, ""). The arg is returned verbatim;
// the caller's configurator parses it (int parsing for head/truncate).
//
// Exported for tests that want to exercise the split directly
// without going through the Registry's Lookup. Stage names are
// lowercase ASCII plus hyphen — matching the D5 built-in stage
// vocabulary.
func ParseNameArg(token string) (name, arg string) {
	for i := 0; i < len(token); i++ {
		if token[i] == ':' {
			return token[:i], token[i+1:]
		}
	}
	return token, ""
}
