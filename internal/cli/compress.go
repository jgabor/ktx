// `ktx compress` — the explicit-verb compression entry point. Reads a
// source tool_call by id, runs the configured pipeline (passthrough
// when no ktx.toml is present — the no-config default), and persists
// a new row in the `compressed` reversibility-layer table whose
// `source_id` references the original tool_calls.id. Prints the new
// compressed id to stdout as raw JSON (the I/O-primitive contract: no
// --format flag, raw JSON, matching `ktx write` / `ktx read`).
//
// Flow:
//
//  1. Load ktx.toml (config.Load — user-level $XDG_CONFIG_HOME/ktx/ktx.toml;
//     passthrough; the no-config default yields an empty pipeline).
//  2. Fetch the source tool_call by id (db.GetToolCall). Source-not-found
//     maps to exit 2 (ErrNotFound → CodeNotFound).
//  3. Build the pipeline Runner: RegisterBuiltins on a fresh Registry
//     + the config's stage list + Warn=stderr (so unknown-stage
//     skips surface to the user).
//  4. runner.Run([]byte(source.Output)) — applies the linear pipeline
//     with the three conservative-on-failure invariants enforced
//     at runtime (empty-output guard, stage-error passthrough,
//     unknown-stage skip). Identity pipeline → output == input.
//  5. Compute content_hash = db.ContentHash(compressedOutput) — hash
//     the artifact that was stored, NOT the source output, so a
//     downstream integrity check compares the hash of the persisted
//     output against the persisted hash.
//  6. Build pipeline JSON = pipeline.EncodePipelineRecord(profile, route,
//     res.Ran) — a record of profile selection and retained stages.
//  7. Generate a fresh 16-char nanoid for the compressed row.
//  8. db.InsertCompressed — the source_id FK is enforced
//     (foreign_keys=1); a miss (should not happen here since step 2
//     fetched the source) maps to ErrSourceNotFound → exit 2.
//  9. Print the compressed id as raw JSON to stdout.
//
// Reversibility is captured at the pipeline BOUNDARY, not per stage:
// `ktx decompress <compressed-id>` resolves source_id and returns the
// source's original output bytes verbatim regardless of how many lossy
// stages ran. So compress(source) != source is possible (a lossy stage
// changed the bytes) AND decompress(compress(source)) == source holds
// (the original is retained in tool_calls).
//
// The command writes the compressed record ID as raw JSON.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/pipeline/stages"
	"github.com/jgabor/ktx/internal/router"
)

// sourceFailureWarning is emitted when the pre-pipeline failure gate
// skips compression because is_error is true on the source tool_call.
const sourceFailureWarning = "ktx: warning: source tool_call indicates failure, compression skipped\n"

// pipelineExecution is the shared operation result for explicit compression,
// hooks, and database replay. Every field derives from one loaded Config.
type pipelineExecution struct {
	output                string
	profile               string
	route                 string
	stages                []string
	recoverFooter         bool
	transformed           bool
	structuredFallback    bool
	antiWorseningRejected bool
}

func (e pipelineExecution) pipelineRecord() string {
	return pipeline.EncodePipelineRecord(e.profile, e.route, e.stages)
}

func (e pipelineExecution) artifactPipelineRecord() string {
	return pipeline.EncodeArtifactPipelineRecord(e.profile, e.route, e.stages, e.structuredFallback)
}

// runCompressWith is the testable core of `ktx compress`. stdout/stderr
// are injected so the compress flow can be exercised without touching
// os.Stdout or os.Stderr. The body opens the database, delegates to
// compressLogic with a nil Registry (use the default built-in
// registry), and translates the returned error into an exit-code +
// stderr envelope.
func runCompressWith(args []string, opts Options, stdout, stderr io.Writer) int {
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx compress: resolve database path: %v", err), opts.Format)
		return ExitInternal
	}
	db, err := openDatabase(path, "compress")
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	defer db.Close()

	_, err = compressLogic(args, nil, db, stdout, stderr)
	if err != nil {
		printError(stderr, err, opts.Format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// compressLogic compresses and persists one record using an open database.
//
// reg is the pipeline.Registry to resolve stage names against. A
// nil reg selects a fresh registry containing the built-in stages. A caller
// may provide a registry to control stage resolution.
func compressLogic(args []string, reg *pipeline.Registry, db *dbpkg.DB, stdout, stderr io.Writer) (any, error) {
	var showHelp bool
	fs := flag.NewFlagSet("ktx compress", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		return nil, usageError("ktx compress: %v", err)
	}
	if showHelp {
		printCompressHelp(stdout)
		return nil, nil
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return nil, usageError("ktx compress: missing required argument: <id>")
	}
	if len(rest) > 1 {
		return nil, usageError("ktx compress: unexpected argument %q; ktx compress takes a single source id", rest[1])
	}
	sourceID := rest[0]

	cid, _, _, err := compressCore(sourceID, reg, db, stderr)
	if err != nil {
		return nil, err
	}
	printID(stdout, cid)
	return nil, nil
}

// compressPipeline loads configuration for callers that do not already own an
// operation snapshot, then delegates to compressPipelineWithConfig.
func compressPipeline(source *dbpkg.ToolCall, reg *pipeline.Registry, stderr io.Writer, enforceTimeout bool) (compressedOutput, pipelineJSON, profileName string, transformed bool, err error) {
	cfg, err := config.Load()
	if err != nil {
		return "", "", "", false, usageError("%v", err)
	}
	execution, err := compressPipelineWithConfig(source, cfg, reg, stderr, enforceTimeout)
	if err != nil {
		return "", "", "", false, err
	}
	return execution.output, execution.artifactPipelineRecord(), execution.profile, execution.transformed, nil
}

// compressPipelineWithConfig resolves selection, stages, timeout, footer, and
// execution from one Config snapshot. The caller still owns artifact
// persistence and whether critical-path timeout enforcement is enabled.
func compressPipelineWithConfig(source *dbpkg.ToolCall, cfg *config.Config, reg *pipeline.Registry, stderr io.Writer, enforceTimeout bool) (pipelineExecution, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	selection := router.Resolve(source, cfg.Routes, "default")
	execution := pipelineExecution{
		output:        source.Output,
		profile:       selection.Profile,
		route:         selection.Route,
		recoverFooter: config.ResolveRecoverFooter(cfg, selection.Profile),
	}
	pipelineStages := stagesForProfile(cfg, selection.Profile)

	runner := buildCompressRunner(reg, cfg, pipelineStages, stderr)

	// Pre-pipeline failure gate: skip compression for failed tool calls.
	if source.IsError != nil && *source.IsError {
		fmt.Fprint(stderr, sourceFailureWarning)
		return execution, nil
	}

	// Identity: no stages configured → source returned unchanged.
	if len(runner.Pipeline) == 0 {
		return execution, nil
	}

	// Resolve the critical-path deadline per-profile (the selected
	// profile's own timeout, falling back to the default profile's,
	// then DefaultPipelineTimeout). Callers off the critical path pass
	// enforceTimeout=false to skip the deadline entirely.
	var timeout time.Duration
	if enforceTimeout {
		timeout = config.ResolveTimeout(cfg, selection.Profile)
	}

	// Run the pipeline. When a timeout is set, race the pipeline against
	// a timer; on timeout, return source.output unchanged.
	type runResult struct{ res pipeline.Result }
	ch := make(chan runResult, 1)
	go func() {
		ch <- runResult{res: runner.Run([]byte(source.Output))}
	}()

	var res pipeline.Result
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case r := <-ch:
			res = r.res
		case <-timer.C:
			fmt.Fprintf(stderr, "ktx: warning: pipeline timeout after %s, returning original\n", timeout)
			return execution, nil
		}
	} else {
		res = (<-ch).res
	}

	execution.output = string(res.Out)
	execution.stages = res.Ran
	execution.transformed = execution.output != source.Output || len(res.Ran) > 0
	execution.structuredFallback = res.StructuredFallback
	execution.antiWorseningRejected = res.AntiWorseningRejected
	return execution, nil
}

// compressCore is the shared pipeline+persist core used by `ktx compress`
// (via compressLogic). It fetches the source tool_call by id, delegates
// to compressPipeline for the failure gate + pipeline, ALWAYS persists a
// compressed row (the user explicitly invoked compress), and returns the
// compressed id, output bytes, pipeline JSON, and any error.
//
// `ktx hook` does NOT call compressCore — it calls compressPipeline
// directly because hook owns the insert-when-transformed policy (hook
// creates a row ONLY when the pipeline transforms; compress always
// creates one).
//
// reg is the pipeline.Registry to resolve stage names against. A
// nil reg means "build the default built-in registry" (RegisterBuiltins
// on a fresh Registry + external plugins from config).
func compressCore(sourceID string, reg *pipeline.Registry, db *dbpkg.DB, stderr io.Writer) (compressedID, compressedOutput, pipelineJSON string, err error) {
	source, err := db.GetToolCall(sourceID)
	if err != nil {
		if errors.Is(err, dbpkg.ErrNotFound) {
			return "", "", "", notFoundError("source tool_call %q not found", sourceID)
		}
		return "", "", "", internalError("get source: %v", err)
	}

	compressedOutput, pipelineJSON, _, _, err = compressPipeline(source, reg, stderr, false)
	if err != nil {
		return "", "", "", err
	}

	hash := dbpkg.ContentHash(compressedOutput)

	cid, err := dbpkg.NewID()
	if err != nil {
		return "", "", "", internalError("generate id: %v", err)
	}

	c := &dbpkg.Compressed{
		ID:          cid,
		SourceID:    sourceID,
		Output:      compressedOutput,
		ContentHash: hash,
		Pipeline:    pipelineJSON,
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := db.InsertCompressed(c); err != nil {
		if errors.Is(err, dbpkg.ErrSourceNotFound) {
			return "", "", "", notFoundError("source tool_call %q not found", sourceID)
		}
		return "", "", "", internalError("persist compressed: %v", err)
	}

	return cid, compressedOutput, pipelineJSON, nil
}

// stagesForProfile resolves the stage list for a given profile name from
// the loaded config. When the profile name exists in the Profiles map,
// its stages are returned. When the name is absent — which happens when
// no ktx.toml is present (Profiles is nil and the router returned
// "default") — the function falls back to PipelineDefault so compression
// still runs with the default stages (or passthrough when that is empty).
func stagesForProfile(cfg *config.Config, profileName string) []string {
	if cfg == nil {
		return nil
	}
	if p, ok := cfg.Profiles[profileName]; ok {
		return p.Stages
	}
	return cfg.PipelineDefault
}

// buildCompressRunner constructs the pipeline.Runner for `ktx compress`.
// A nil reg builds a fresh Registry with the built-in stages registered
// (the "OFF by default" contract: the stages exist but do not run
// unless named in the config), including unwrap-result, cleanup,
// bounded-rendering, and deterministic-filter stages. A non-nil reg
// (test injection) is used as-is so
// tests can substitute fake stages without re-wiring the built-in
// registration. The runner's Warn writer is set to stderr so
// unknown-stage skips and stage-error pass-throughs surface to the
// user (the runner owns the warning text; the verb just provides the
// sink).
//
// When cfg has Plugins (from [[plugin]] blocks), each is registered
// via RegisterExternalBuilder AFTER RegisterBuiltins so
// last-writer-wins lets a user plugin shadow a built-in of the same
// name. The registry is rebuilt per-invocation — removing the
// [[plugin]] block from ktx.toml and re-running restores the built-in.
func buildCompressRunner(reg *pipeline.Registry, cfg *config.Config, pipelineStages []string, stderr io.Writer) *pipeline.Runner {
	if reg == nil {
		reg = pipeline.NewRegistry()
		stages.RegisterBuiltins(reg)
		if cfg != nil {
			for _, p := range cfg.Plugins {
				reg.RegisterExternalBuilder(p.Name, p.Command)
			}
		}
	}
	return &pipeline.Runner{
		Registry: reg,
		Pipeline: pipelineStages,
		Warn:     stderr,
	}
}

// compressCommandFunc is the dispatch wrapper. The dispatcher already
// opened the DB for writes, parsed --help/--format, and exposed a
// clean positional Args tail. The verb's only responsibility is to
// apply compressLogic (with a nil Registry — the default built-in
// stages) and surface the typed error to the dispatcher's printError
// + exitCodeOf pipeline.
func compressCommandFunc(ctx *DispatchContext) (any, error) {
	return compressLogic(ctx.Args, nil, ctx.DB, ctx.Stdout, ctx.Stderr)
}

// init registers `ktx compress` with the dispatch module.
//
// DatabaseReadWrite: compress reads a source tool_call and writes a
// compressed row in one invocation.
// PassthroughStdout=true: the verb writes the compressed id as raw
// JSON (I/O primitive). The dispatcher's printError still
// goes to stderr on failure.
func init() {
	Register("compress", RegisterOptions{
		Description:       "compress a tool_call's output (reversible via ktx decompress)",
		DatabaseAccess:    DatabaseReadWrite,
		PassthroughStdout: true,
		Help:              printCompressHelp,
	}, compressCommandFunc)
}

// printCompressHelp writes the `ktx compress` help. Mirrors
// `ktx write --help` / `ktx read --help` style so the I/O
// primitives present consistently. Cross-references `ktx decompress`
// (the reversal primitive) so users can discover the pair without
// reading the design rationale (the --help cross-reference rule).
func printCompressHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx compress — compress a tool_call's output (reversible)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx compress <id>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Reads the source tool_call by id, runs the configured compression")
	fmt.Fprintln(w, "pipeline (passthrough when no ktx.toml is present — the no-config default), and")
	fmt.Fprintln(w, "persists a new row in the `compressed` table whose source_id references")
	fmt.Fprintln(w, "the original. Prints the new compressed id to stdout as raw JSON.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Reversibility: ktx decompress <compressed-id> resolves source_id and")
	fmt.Fprintln(w, "returns the source's original output bytes verbatim. Reversal is")
	fmt.Fprintln(w, "out-of-band by id, not per-stage; the original is retained in")
	fmt.Fprintln(w, "tool_calls (append-only originals with content_hash for integrity and")
	fmt.Fprintln(w, "nanoid addressability). compress(source) may differ from")
	fmt.Fprintln(w, "source when a lossy stage runs, but decompress(compress(x)) == x holds.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Pipeline configuration: $XDG_CONFIG_HOME/ktx/ktx.toml. Minimal shape:")
	fmt.Fprintln(w, "  [pipeline.default]")
	fmt.Fprintln(w, "  stages = [\"strip-ansi\", \"head:100\"]")
	fmt.Fprintln(w, "Empty/missing config → passthrough pipeline (output == source.output).")
	fmt.Fprintln(w, "Built-in stages: unwrap-result, strip-ansi, dedup-blank, normalize,")
	fmt.Fprintln(w, "head (head:N), truncate (truncate:N), keep-ends (keep-ends:N:M),")
	fmt.Fprintln(w, "grep (grep:pattern), grep-v (grep-v:pattern), token-budget (token-budget:N),")
	fmt.Fprintln(w, "replace (replace:pattern::replacement).")
	fmt.Fprintln(w, "Each OFF unless named in [pipeline.default].stages.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Conservative-on-failure (runtime-enforced by the runner):")
	fmt.Fprintln(w, "  - empty-output guard: a stage emptying non-empty output is discarded")
	fmt.Fprintln(w, "  - stage-error passthrough: an errored or panicking stage passes input through")
	fmt.Fprintln(w, "  - unknown-stage skip: an unresolved stage name is skipped with a warning")
	fmt.Fprintln(w, "  - anti-worsening gate: a pipeline result not smaller than input is discarded")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  compressed row persisted; id printed")
	fmt.Fprintln(w, "  1  usage error (missing/invalid id, malformed ktx.toml)")
	fmt.Fprintln(w, "  2  source tool_call not found")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output: raw JSON (the new compressed id). No --format flag.")
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx decompress  reverse a compression (return source output verbatim)")
	fmt.Fprintln(w, "  ktx write        persist a tool_call record from sidecar input")
	fmt.Fprintln(w, "  ktx read         fetch a persisted record by ID (raw JSON)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
