// Package stages holds ktx's built-in compression pipeline stages.
//
// Each stage is a pure function over bytes ([]byte → []byte, error-or-nil)
// implementing the pipeline.Stage interface. Stages carry NO database
// access, NO filesystem access, and NO global state — the runner
// applies them in sequence and the conservative-on-failure invariants
// (empty-output guard, stage-error passthrough, unknown-stage skip)
// live in the runner, not in stage-author convention.
//
// Built-ins cover strict structured-result projection (unwrap-result),
// lexical JSON compaction (json-compact, json-block), cleanup (strip-ansi, dedup-blank,
// normalize), bounded rendering (head,
// truncate, keep-ends, token-budget), and deterministic filtering (grep,
// grep-v, replace). BuiltinStageNames is the authoritative enumeration.
//
// All built-in stages are off by default: only stages named in the
// selected ktx.toml pipeline profile run. RegisterBuiltins
// populates a registry at package init so the CLI's compress verb
// can resolve names without a separate plugin loader; the plugin
// registry adds external stages through `ktx plugin add` and
// registers additional names via external processes.
//
// Parameterized stages use a `name:arg` token form. The Registry's
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
	"unwrap-result",
	"json-compact",
	"json-block",
	"strip-ansi",
	"dedup-blank",
	"normalize",
	"head",
	"truncate",
	// Deterministic compression stages.
	"keep-ends",
	"grep",
	"grep-v",
	"token-budget",
	"replace",
}

// BuiltinStageNames returns the names of all built-in pipeline stages
// in registration order. The caller may use this to present a unified
// view of valid stage names (builtins + user plugins) to agents.
func BuiltinStageNames() []string {
	return builtinStageNames
}

// RegisterBuiltins registers every built-in stage on reg. A nil
// reg is a no-op (defensive; the caller passes a fresh
// pipeline.NewRegistry() in normal use).
//
// The simple stages (unwrap-result, json-compact, json-block, strip-ansi, dedup-blank,
// normalize) are
// registered via Register — their token form is the bare name and
// they ignore any `:arg` portion. The two parameterised stages (head,
// truncate) are registered via RegisterBuilder — they parse the arg
// via strconv.Atoi to configure how many lines/bytes to keep.
//
// Stages registered here are the ONLY built-in stages ktx ships with;
// the ktx.toml `[pipeline.default].stages` array names which
// of them run (a stage registered here but not named in the config
// does NOT run — ktx's "OFF by default" contract).
func RegisterBuiltins(reg *pipeline.Registry) {
	if reg == nil {
		return
	}
	// Simple stages — bytes-in, bytes-out, no configuration token.
	reg.Register("unwrap-result", pipeline.StageFunc(UnwrapResult))
	reg.Register("json-compact", pipeline.StageFunc(JSONCompact))
	reg.Register("json-block", pipeline.StageFunc(JSONBlock))
	reg.Register("strip-ansi", pipeline.StageFunc(StripANSI))
	reg.Register("dedup-blank", pipeline.StageFunc(DedupBlank))
	reg.Register("normalize", pipeline.StageFunc(Normalize))

	// Parameterised stages — RegisterBuilder receives the parsed
	// arg (or "" for the bare-name form) and returns a Stage whose
	// Behavior depends on the arg.
	reg.RegisterBuilder("head", headBuilder)
	reg.RegisterBuilder("truncate", truncateBuilder)

	// Deterministic compression stages. Each is a pure
	// function over bytes (no ML, no new deps). keep-ends:N:M,
	// grep:pattern, grep-v:pattern, and token-budget:N all reuse the
	// existing StageFunc / Builder contract; the anti-worsening gate
	// and the three conservative-on-failure invariants apply to them unchanged.
	reg.RegisterBuilder("keep-ends", keepEndsBuilder)
	reg.RegisterBuilder("grep", func(arg string) (pipeline.Stage, error) { return grepBuilder(arg, true) })
	reg.RegisterBuilder("grep-v", func(arg string) (pipeline.Stage, error) { return grepBuilder(arg, false) })
	reg.RegisterBuilder("token-budget", tokenBudgetBuilder)
	reg.RegisterBuilder("replace", replaceBuilder)
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
// lowercase ASCII plus hyphen — matching the built-in stage
// vocabulary.
func ParseNameArg(token string) (name, arg string) {
	for i := 0; i < len(token); i++ {
		if token[i] == ':' {
			return token[:i], token[i+1:]
		}
	}
	return token, ""
}
