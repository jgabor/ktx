// Package pipeline executes ktx's compression pipeline (D14).
//
// The pipeline is a linear ordered slice of bytes-in/bytes-out stages.
// Each stage implements the Stage interface; the Runner applies stages
// in order and enforces Decision 14's three conservative-on-failure
// invariants at runtime (NOT as a stage-author convention — the lowfat
// failure mode is explicitly rejected):
//
//  1. Empty-output guard: a stage producing empty output from
//     non-empty input has its output discarded and the input passes
//     through to the next stage unchanged. (Edge: empty input →
//     empty output preserves the empty output — the guard only fires
//     when input was non-empty.)
//  2. Stage-execution-error passthrough: a stage returning a
//     non-nil error (or a panic recovered by the runner) passes its
//     input through unchanged to the next stage; the error is not
//     fatal to the pipeline.
//  3. Unknown-stage skip: a stage name the Registry cannot resolve
//     is skipped with a stderr warning ("ktx: warning: unknown
//     pipeline stage %q, skipped") and the pipeline continues.
//
//  4. Anti-worsening gate (D22 Phase 2): after all stages run, if the
//     pipeline result is NOT smaller than the input (len(out) >=
//     len(in)) AND the input was non-empty, the entire pipeline result
//     is discarded and the input is returned unchanged (with Ran
//     cleared). A pipeline that grows output — or merely rewrites it
//     at equal length — did not reduce the agent's context cost, so it
//     is treated as passthrough (no compressed row). This fires on the
//     pipeline RESULT, not per-stage; invariants 1–3 still fire
//     per-stage during the loop. (rtk guard.rs never_worse; lowfat
//     lib.rs 10% gate.)
//
// Reversibility is captured at the pipeline BOUNDARY, not per stage
// (D14): the database holds the original (tool_calls) and a
// compressed artifact references it by id (`ktx decompress <id>`
// resolves source_id and returns the source's original output
// verbatim — no stage-inverse runs). The runner records the names of
// stages that successfully ran so the compressed row's `pipeline`
// JSON array reflects what actually executed (the D14 "a record of
// what stages ran" contract).
//
// The Stage interface is deliberately narrow (bytes-in, bytes-out,
// error-or-nil) so M5 shell-macro stages slot in unchanged: a
// process-backed Stage is just another Apply. The runner owns panic
// recovery so a buggy built-in stage cannot crash the compress verb.
package pipeline

import (
	"bytes"
	"fmt"
	"io"
)

// Stage is the per-stage contract (D14). Each stage receives the
// previous stage's output (or the source bytes for the first stage)
// and returns the transformed bytes plus an error-or-nil.
//
// The interface is narrow by design: bytes-in, bytes-out, error-or-nil.
// Stage metadata, ordering guarantees, and terminal-vs-continuable
// semantics are M4 implementation details decided at the runner level,
// not encoded in the interface (the ARCHITECTURE.md open deliberation
// "Pipeline stage contract" deliberately defers the full interface to
// implementation). M5 shell-macro stages implement the same interface
// against an external process; the runner does not distinguish them
// from built-in Go stages.
//
// A Stage MUST be safe to invoke from a single goroutine (the runner
// applies stages sequentially; no concurrency in M4). Stages that
// carry mutable state should document their concurrency contract; the
// built-in Go stages in internal/pipeline/stages are pure functions
// over bytes and trivially satisfy single-goroutine safety.
type Stage interface {
	// Apply transforms input and returns the result, or input
	// unchanged with a non-nil error when the stage cannot run
	// safely. The runner enforces the conservative-on-failure
	// invariants around Apply — a panicked Apply is recovered and
	// treated as a stage-execution-error (invariant 2), so a buggy
	// stage author cannot crash the compress verb.
	Apply(input []byte) ([]byte, error)
}

// StageFunc lets a plain function satisfy Stage. Useful for
// registering built-in stages as closures and for test-only fake
// stages (the runner_test.go uses StageFunc extensively).
type StageFunc func(input []byte) ([]byte, error)

// Apply delegates to the underlying function.
func (f StageFunc) Apply(input []byte) ([]byte, error) { return f(input) }

// Registry maps stage names to Stage implementations. The M4
// resolution model is name → Stage: a ktx.toml's
// `[pipeline.default].stages` array is a list of stage-name tokens,
// and the runner resolves each token against a Registry at invocation
// time. An unknown name fires invariant (3) — the stage is skipped
// with a stderr warning, NOT a fatal error (D14).
//
// Two registration shapes coexist:
//
//   - Register(name, Stage) for simple stages (strip-ansi, dedup-blank,
//     normalize). The token form is just the bare name; there is no arg.
//   - RegisterBuilder(name, Builder) for parameterised stages (head,
//     truncate). The token form is `name:arg` (e.g. `head:100`,
//     `truncate:4096`); the Builder receives the parsed arg and returns
//     a Stage configured by it. A bare-name token (no colon) calls the
//     Builder with arg="" — the stage author supplies a sensible default
//     in that branch.
//
// Resolution happens at runner invocation time, NOT at config-load time
// (config.Load returns stage tokens; the runner resolves them) so an
// unknown name in a freshly-shipped ktx.toml degrades gracefully at
// runtime rather than failing config load. Lookup(token) splits the
// token at the FIRST colon before resolving so a parameterised stage's
// arg may itself contain colons (no built-in accepts such an arg, but
// the split is single-pass so future stages are not constrained).
//
// The Registry is populated at package init by the built-in Go stages
// (internal/pipeline/stages) and will be extended in M5 by the
// `ktx plugin` registry (TODO:30); for M4 the single default profile
// is the only consumer.
type Registry struct {
	builders map[string]Builder
}

// Builder configures a Stage from a `name:arg` token's arg portion.
// An empty arg means the bare-name form was registered (`head` not
// `head:100`); the Builder returns a Stage using a documented default
// (head defaults to 10 lines; truncate defaults to 4096 bytes). A
// malformed arg (e.g. `head:abc`) returns a non-nil error — the
// runner treats this as a stage-execution-error (invariant 2): the
// input passes through unchanged to the next stage.
type Builder func(arg string) (Stage, error)

// NewRegistry returns an empty Registry. Built-in stages are
// registered via Register / RegisterBuilder; M5 will add a
// `ktx plugin` path that shells out to an external process and
// registers the result as a stage under a user-chosen name.
func NewRegistry() *Registry {
	return &Registry{builders: make(map[string]Builder)}
}

// Register adds a simple Stage under name (a Stage that ignores any
// `:arg` token portion). Returns the Registry so callers can chain
// registrations. A duplicate registration overwrites the prior entry
// — the last writer wins, matching the "user config over built-in"
// precedence a future plugin registry will need.
func (r *Registry) Register(name string, s Stage) *Registry {
	if r == nil || s == nil {
		return r
	}
	r.builders[name] = func(string) (Stage, error) { return s, nil }
	return r
}

// RegisterBuilder adds a parameterised stage under name (a stage
// whose Behavior depends on a `name:arg` token's arg). Returns the
// Registry for chaining. Same overwrite semantics as Register.
func (r *Registry) RegisterBuilder(name string, b Builder) *Registry {
	if r == nil || b == nil {
		return r
	}
	r.builders[name] = b
	return r
}

// Lookup resolves a stage token to a Stage. Tokens are split at the
// FIRST colon; the portion before is the bare name, the portion after
// (which may be empty) is the arg passed to the stage's Builder. A
// token with no colon resolves as (token, ""). Returns (nil, false)
// when the bare name is not registered — the runner applies invariant
// (3): skip with a warning, NOT a fatal error.
//
// A Builder returning a non-nil error (malformed arg) propagates that
// error here so the caller (the runner) treats it as a
// stage-execution-error (invariant 2) rather than an unknown stage.
// The (Stage, true, err) shape distinguishes "recognised name, bad
// arg" from "unrecognised name".
func (r *Registry) Lookup(token string) (Stage, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	name, arg := splitToken(token)
	b, ok := r.builders[name]
	if !ok {
		return nil, false, nil
	}
	s, err := b(arg)
	if err != nil {
		return nil, true, err
	}
	return s, true, nil
}

// splitToken separates a `name[:arg]` token at the first colon. A
// token with no colon returns (token, ""). Exported via the package
// doc but unexported here because callers use Lookup directly.
func splitToken(token string) (name, arg string) {
	for i := 0; i < len(token); i++ {
		if token[i] == ':' {
			return token[:i], token[i+1:]
		}
	}
	return token, ""
}

// Runner executes a linear ordered slice of stages against an input.
// The runner owns the conservative-on-failure enforcement so a stage
// author cannot accidentally violate the contract — even a panicking
// or error-returning built-in stage keeps the pipeline running with
// pass-through semantics.
//
// The runner's Pipeline field is a slice of stage-name strings (NOT
// Stage implementations); the names are resolved against a Registry
// at Run time. This indirection means an unknown name in a ktx.toml
// does not break config-load — it degrades at runtime via invariant
// (3).
type Runner struct {
	// Registry resolves stage names to Stage implementations. A nil
	// Registry is treated as an empty registry: every stage name is
	// unknown, and the pipeline is passthrough (every stage skipped
	// with a warning). This matches the D14 no-ktx.toml default for
	// a degenerate case and keeps the runner safe against a
	// misconfigured caller.
	Registry *Registry

	// Pipeline is the ordered list of stage names to apply. An empty
	// slice is passthrough (D14: no-ktx.toml → empty pipeline → input
	// returned unchanged; the compressed.output equals source.output
	// and the pipeline JSON is "[]").
	Pipeline []string

	// Warn is where invariant (3) — the unknown-stage skip
	// warning — is written. Defaults to io.Discard when nil so a
	// caller that does not care about warnings gets silent
	// pass-through (the warnings are diagnostics, not data). The
	// CLI layer sets this to os.Stderr; tests inject a bytes.Buffer
	// to assert the warning shape.
	Warn io.Writer
}

// warnf writes a formatted warning to r.Warn, or to io.Discard when
// r.Warn is nil. Errors from the writer are ignored — warnings are
// best-effort diagnostics and a broken stderr must not crash the
// compress verb.
func (r *Runner) warnf(format string, args ...any) {
	w := r.Warn
	if w == nil {
		w = io.Discard
	}
	fmt.Fprintf(w, format, args...)
}

// Result is the runner's output: the transformed bytes plus the list
// of stage names that successfully ran. The Ran slice is what the
// caller persists as the compressed row's `pipeline` JSON array
// (D14: "a record of what stages ran"). Stages that errored, were
// skipped (unknown name), or panicked are NOT in Ran — only stages
// whose Apply returned a non-nil error are absent; stage names that
// were skipped due to invariant (1) (empty-output guard) ARE in Ran
// because the stage ran (it just produced empty output and the guard
// discarded it; the stage did execute).
//
// The Ran slice's ordering matches the Pipeline's ordering (the
// stages that ran appear in the order they ran).
type Result struct {
	Out []byte
	// Ran is the list of stage names that successfully applied. A
	// stage that returned an error or panicked is absent from Ran
	// (the runner passed its input through but the stage did not
	// "run" successfully). A stage skipped because its name was not
	// in the Registry is absent from Ran (the warn logged it, but
	// nothing ran). A stage that hit invariant (1)'s empty-output
	// guard IS in Ran — the stage ran, it just produced empty
	// output and the guard discarded it.
	Ran []string
}

// Run applies the pipeline to input and returns the Result. The
// function enforces all three conservative-on-failure invariants
// (empty-output guard, stage-error passthrough, unknown-stage skip)
// at runtime; see the package doc for the full contract.
//
// The runner does NOT mutate the input slice; each stage receives a
// copy of the previous stage's output. (In practice stages often
// return slices that alias their input buffer; the runner does not
// rely on aliasing either way — it passes whatever the previous stage
// returned to the next stage.)
//
// An empty Pipeline (zero stages) is passthrough: the input is returned
// unchanged and Ran is empty (the D14 no-ktx.toml default).
//
// Stage tokens may carry an arg via `name:arg` (e.g. `head:100`).
// The registry splits the token and resolves the bare name; a
// parameterised stage's Builder receives the parsed arg. A malformed
// arg (e.g. `head:abc`) returns a Builder error which the runner
// treats as a stage-execution-error (invariant 2): the input passes
// through unchanged and the stage name is absent from Ran. A bare
// name with no colon uses the stage's documented default (head=10,
// truncate=4096).
func (r *Runner) Run(input []byte) Result {
	res := Result{Out: input, Ran: nil}
	if r == nil {
		return res
	}

	current := input
	for _, token := range r.Pipeline {
		stage, recognised, err := r.Registry.Lookup(token)
		if err != nil {
			// Recognised name but malformed arg (e.g. head:abc).
			// Invariant (2): stage-execution-error passthrough.
			// The token is NOT recorded in Ran (the stage did not
			// successfully run).
			r.warnf("ktx: warning: pipeline stage %q errored: %v\n", token, err)
			continue
		}
		if !recognised {
			// Invariant (3): unknown-stage skip. Warning to
			// stderr, pipeline continues, the token is NOT
			// recorded in Ran (nothing ran).
			r.warnf("ktx: warning: unknown pipeline stage %q, skipped\n", token)
			continue
		}

		out, ran := applyStageWithWarn(stage, token, current, r)
		if ran {
			res.Ran = append(res.Ran, token)
		}
		current = out
	}
	res.Out = current

	// Anti-worsening gate — the fourth conservative-on-failure
	// invariant (D14-adjacent, fires on the pipeline RESULT, not
	// per-stage). When the input was non-empty and the pipeline's
	// net output is NOT smaller than the input (len(out) >=
	// len(in)), the pipeline did not help: a stage that grows output
	// is worse than no stage (rtk guard.rs:6-12 never_worse; lowfat
	// lib.rs:46-55 gate). The gate discards the pipeline's work and
	// returns the input unchanged. Equal length is treated as "not
	// better" — a stage that only rewrites bytes at the same length
	// has not reduced the agent's context cost.
	//
	// Clearing Ran is intentional: the pipeline's net effect was
	// nullified, so the compressed-row's `pipeline` JSON array (the
	// D14 "record of what stages ran") is empty — this makes
	// compressPipeline's `transformed` computation yield false
	// (output == input AND pipeline JSON == "[]"), so NO compressed
	// row is created for a result that did not shrink the input
	// (D22: a transform that does not help returns the original
	// with compressed_id=null). The three per-stage D14 invariants
	// (empty-output guard, stage-error passthrough, unknown-stage
	// skip) still fired during the loop above; the gate is a
	// post-hoc result-level discard, not a replacement.
	if len(input) > 0 && len(res.Out) >= len(input) {
		res.Out = input
		res.Ran = nil
	}
	return res
}

// applyStageWithWarn invokes a single Stage with the
// conservative-on-failure contract enforced. Returns (output, ran)
// where ran is true when the stage successfully executed (its Apply
// returned a non-nil error OR panicked → ran=false; empty-output
// guard fired → ran=true because the stage DID run, its output was
// just discarded). The warn writer receives the panic-recovery
// notice so the user sees that a buggy stage was caught.
//
// The three invariants:
//
//  1. Empty-output guard: non-empty input → empty output (len 0) with
//     nil error → discard empty output, pass input through. Edge:
//     empty input → empty output preserves the empty output (the
//     guard only fires when input was non-empty).
//  2. Stage-execution-error passthrough: non-nil error (or recovered
//     panic) → pass input through unchanged, continue.
//  3. Unknown-stage skip is handled by Run before reaching here.
func applyStageWithWarn(s Stage, token string, input []byte, r *Runner) (output []byte, ran bool) {
	// Recover panics so a buggy stage cannot crash the compress
	// verb. A panic is treated as a stage-execution error: the
	// input passes through unchanged, the stage name is NOT
	// recorded in Ran, and the pipeline continues.
	defer func() {
		if rec := recover(); rec != nil {
			if r != nil {
				r.warnf("ktx: warning: pipeline stage %q panicked: %v\n", token, rec)
			}
			output = input
			ran = false
		}
	}()

	out, err := s.Apply(input)
	if err != nil {
		// Invariant (2): stage-execution-error passthrough.
		// The input passes through unchanged; the stage did not
		// successfully run (ran=false) so the pipeline JSON
		// omits it. We log the warning here so the user sees that
		// the stage errored (the err return path is distinct
		// from the Build-error path in Run, which also warns).
		if r != nil {
			r.warnf("ktx: warning: pipeline stage %q errored: %v\n", token, err)
		}
		return input, false
	}

	// Invariant (1): empty-output guard. A stage producing empty
	// output from NON-EMPTY input has its output discarded and the
	// input passes through. The guard only fires when input was
	// non-empty — an empty input → empty output preserves the
	// empty output (a legitimate "stage ran on empty input and
	// produced empty output" case).
	if len(out) == 0 && len(input) > 0 {
		// The stage ran (it returned nil error), so it IS
		// recorded in Ran — the user should see that the stage
		// executed even though its output was discarded. The
		// pass-through is the input.
		return input, true
	}

	return out, true
}

// EncodePipelineJSON renders the stage-names slice as a JSON array
// string, e.g. `["strip-ansi","head:5"]` or `[]` for an empty slice.
// The compressed row's `pipeline` column stores this string so the
// D14 "a record of what stages ran" contract has an inspectable,
// deterministically-encoded representation (a profile-hash would
// require readers to resolve the hash against a registry ktx does
// not yet have).
//
// Encoding lives in package pipeline (not in db) because the pipeline
// owns the JSON shape contract; the db layer persists it verbatim as
// TEXT. We hand-roll the marshal to avoid encoding/json pulling in
// reflection for a slice of strings — the format is trivial.
func EncodePipelineJSON(names []string) string {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		// Escape the two characters JSON requires inside a string:
		// the quote and the backslash. Stage names are
		// author-chosen (e.g. "head:5", "strip-ansi") and unlikely
		// to need full JSON-string escaping, but correctness is
		// cheap.
		for _, c := range []byte(n) {
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			default:
				b.WriteByte(c)
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}
