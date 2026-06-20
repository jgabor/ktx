// `ktx bench` — the M3 benchmark namespace (Decision 2, Decision 12).
//
// bench owns the headline number that proves or disputes the cost
// contract. The v1 surface ships two subcommands:
//
//	ktx bench run <agent> <model> <task>
//	    Consumes the D12 methodology, runs the agent via the existing
//	    wrapper path (ktx run <agent> ...), scores per the methodology's
//	    scoring function (v1 = exact_match), persists a bench_runs row
//	    with cost derived per D11 at write time, and prints the headline
//	    number to stdout plus a one-line summary to stderr (D1 wrapper
//	    convention).
//
//	ktx bench report
//	    Reads the bench_runs table and emits the headline number with
//	    the methodology reference visible (JSON default + --format text).
//	    Implementation is reserved for plan Task 5; this cycle stubs
//	    runBenchReport with a "not yet implemented" error so the dispatch
//	    contract is honest about what is and is not yet built.
//
// Database path resolution (Decision 8): KTX_DB env > default. The
// dispatch is the same shape as `ktx prices` and `ktx doctor` — a
// namespace with subcommands, no flags of its own (v1).
//
// Methodology file location: `.agentera/bench/methodology-v1.yaml`,
// resolved by walking up from the current working directory looking
// for `.agentera/`. The methodology file is the reproducibility
// contract (D12) — every input that produced the headline number is
// enumerated there. Plan Task 4 AC7 (missing methodology → exit 2,
// JSON error) is enforced by runBenchRun before any wrapper
// invocation.
//
// Output contract (D12): JSON default with --format text for humans.
// The headline number is `cost_per_correct = cost_USD / max(score,
// 1e-9)` USD, six decimal places. The JSON envelope carries the
// headline, the rows array (one element for the just-inserted row in
// `bench run`, all rows for `bench report`), and the methodology
// version string for offline reproduction.
package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jgabor/ktx/internal/store"
)

// benchMethodologyRelPath is the path of the methodology file relative
// to the repo root (D12 path commitment). Bench run resolves the repo
// root by walking up from CWD looking for `.agentera/`, then joins
// this relative path.
const benchMethodologyRelPath = ".agentera/bench/methodology-v1.yaml"

// benchEpsilon is the division-by-zero guard from the methodology
// formula (D12: `cost_per_correct = cost_USD / max(score, 1.0e-9)`).
// score=0 produces a very large headline number — the vision's "cost
// is the contract made loud" — signalling that an incorrect answer is
// unboundedly expensive per correct.
const benchEpsilon = 1.0e-9

// runBench dispatches the `ktx bench` namespace.
//
//	ktx bench                -> ktx bench --help (default)
//	ktx bench run <a> <m> <t> -> run a benchmark (Task 4)
//	ktx bench report         -> emit the headline (Task 5 stub)
//
// Future subcommands (ktx bench compare) are out of scope for the M3
// plan and would land in a post-M3 cycle per the D2 subcommand-surface
// decision (bench namespace shape: run/report/compare).
func runBench(args []string, opts Options) int {
	return runBenchWith(args, opts, os.Stdout, os.Stderr)
}

func runBenchWith(args []string, opts Options, stdout, stderr io.Writer) int {
	var showHelp bool
	fs := flag.NewFlagSet("ktx bench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(args); err != nil {
		printError(stderr, usageError("ktx bench: %v", err), opts.Format)
		return ExitUsage
	}
	if showHelp {
		printBenchHelp(stdout)
		return ExitOK
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printBenchHelp(stdout)
		return ExitOK
	}

	sub := rest[0]
	rest = rest[1:]

	switch sub {
	case "run":
		return runBenchRun(rest, opts, stdout, stderr)
	case "report":
		return runBenchReport(rest, opts, stdout, stderr)
	default:
		printError(stderr, usageError("ktx bench: unknown subcommand %q; run 'ktx bench --help'", sub), opts.Format)
		return ExitUsage
	}
}

// runBenchReport is the plan Task 5 stub. It returns ExitInternal with
// a usage error so the user knows bench report is reserved for a future
// cycle. The dispatch contract is honest about what is and is not yet
// built — same pattern as the M2 cycle when `ktx compress` was a stub.
func runBenchReport(args []string, opts Options, stdout, stderr io.Writer) int {
	printError(stderr, usageError("ktx bench report: not yet implemented (reserved for plan Task 5)"), opts.Format)
	return ExitUsage
}

// methodology is the parsed subset of the methodology file the bench
// runner needs. The full YAML schema has many fields (scoring.epsilon,
// trials.non_determinism_handling, etc.) that are not consumed at
// run-time — v1 only needs the version string, the scoring function
// name, the task fields (identifier + prompt + expected_output), and
// the model fields (id + provider). Adding more fields later extends
// the parser, not the struct.
type methodology struct {
	Version            string
	ScoringFunction    string
	TaskIdentifier     string
	TaskPrompt         string
	TaskExpectedOutput string
	ModelID            string
	ModelProvider      string
}

// loadMethodology reads the v1 methodology file from the repo root and
// returns its parsed subset. The repo root is resolved by walking up
// from CWD looking for the `.agentera` directory; the methodology file
// lives at `.agentera/bench/methodology-v1.yaml` per D12.
//
// Returns ErrBenchMethodologyNotFound (mapped to ExitNotFound by the
// caller) when the file is absent, and a parse error (mapped to
// ExitInternal) when the file is malformed. The parser is a small
// line-based subset reader — the methodology file's v1 schema is
// shallow enough that a hand-rolled parser is more honest than pulling
// in a YAML dependency (the M3 plan's "Go stdlib first, no new external
// deps" rule).
func loadMethodology() (*methodology, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("bench: getwd: %w", err)
	}
	root, err := findRepoRoot()
	if err != nil {
		// The repo root is not found — surface the expected path so
		// the user knows where ktx looked.
		expected := filepath.Join(cwd, benchMethodologyRelPath)
		return nil, expected, err
	}
	path := filepath.Join(root, benchMethodologyRelPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, path, store.ErrBenchMethodologyNotFound
		}
		return nil, path, fmt.Errorf("bench: read methodology %q: %w", path, err)
	}
	m, err := parseMethodology(string(data))
	if err != nil {
		return nil, path, fmt.Errorf("bench: parse methodology %q: %w", path, err)
	}
	return m, path, nil
}

// findRepoRoot walks up from CWD looking for a directory containing
// `.agentera`. The methodology file lives there. The walk is bounded
// to the filesystem root (stop-at /) so a runaway traversal cannot
// exceed a reasonable depth; in practice ktx is invoked from a
// project checkout, so the walk is one or two levels.
//
// Returns the path of the directory containing `.agentera`, or
// store.ErrBenchMethodologyNotFound when no such directory exists
// within the walk boundary. The caller maps the typed error to
// ExitNotFound (exit 2) per plan Task 4 AC7: missing methodology is
// a hard failure, not a soft fail.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("bench: getwd: %w", err)
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, ".agentera")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", store.ErrBenchMethodologyNotFound
		}
		dir = parent
	}
}

// parseMethodology parses a v1 methodology YAML file into the subset
// struct. The parser handles only the schema's surface — top-level
// scalars, three levels of nested mappings, single-line string values,
// and `#` comments. Block scalars (the `|` literal block notation
// used for `scope`, `trials.non_determinism_handling`, and
// `computation`) are skipped: bench run does not consume them.
//
// The parser is intentionally narrow. A future methodology version
// that introduces a richer structure should land with a real YAML
// library (gopkg.in/yaml.v3) — the v1 file is hand-crafted and the
// parser matches its hand-crafted shape exactly. YAGNI for v1.
//
// Returns an error when a required field is missing (the bench run
// cannot proceed without task.expected_output at minimum). The error
// names the missing field so the user can fix the file.
func parseMethodology(data string) (*methodology, error) {
	m := &methodology{}
	lines := strings.Split(data, "\n")

	// pathStack tracks the running block context. A bare "key:" pushes
	// the key; a leaf "key: value" reads the leaf at the current path.
	// The stack is popped when the indent decreases (we are returning
	// to a shallower nesting level).
	//
	// Indentation step is 2 spaces (the v1 file uses 2-space indentation
	// throughout). Depth = indent / 2.
	const indentStep = 2
	var pathStack []string
	// depth tracks the current depth BEFORE processing the line; -1
	// means "no current depth" (start of file or just popped all).
	depth := -1

	for _, raw := range lines {
		// Strip end-of-line comments. The v1 file has no `#` inside
		// quoted strings; a more robust parser would track quote
		// depth, but the v1 schema is hand-curated enough that the
		// simple rule works.
		if idx := strings.Index(raw, "#"); idx >= 0 {
			raw = raw[:idx]
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		trimmed := strings.TrimSpace(raw)

		// Skip block-scalar continuations (lines indented further than
		// the parent block opener). We detect this by checking whether
		// the trimmed line has no colon — a YAML block scalar body line
		// is just text. Skip these lines entirely.
		if !strings.Contains(trimmed, ":") {
			continue
		}
		// Detect block-scalar opener ("key: |" or "key: >"). When we see
		// one, we record the path but also enter block-scalar mode so
		// subsequent indented lines are skipped until the indent drops.
		// For v1 simplicity, we just push the key and let the
		// "no colon" check skip the body lines.
		// Skip list items too — bench run does not consume them.
		if strings.HasPrefix(trimmed, "- ") {
			continue
		}

		indent := leadingSpaces(raw)
		lineDepth := indent / indentStep

		// Pre-determine whether this line is an opener or a leaf.
		var key, val string
		var hasValue bool
		if idx := strings.Index(trimmed, ":"); idx >= 0 {
			key = strings.TrimSpace(trimmed[:idx])
			rest := strings.TrimSpace(trimmed[idx+1:])
			if rest == "" {
				hasValue = false
			} else {
				val = unquoteYAML(rest)
				hasValue = true
			}
		} else {
			continue
		}

		// Pop path entries until the stack length matches the line's
		// depth. For an opener at depth N, the parent context is the
		// current pathStack of length N (the opener becomes the N+1th
		// entry after pushing). For a leaf at depth N, the parent
		// context is also pathStack of length N (the leaf's key
		// completes the path without pushing). So in both cases we
		// align to length == lineDepth BEFORE the (possible) push.
		for len(pathStack) > lineDepth {
			pathStack = pathStack[:len(pathStack)-1]
		}

		if hasValue {
			// Leaf: read at the current path.
			pathKey := strings.Join(pathStack, ".")
			switch {
			case len(pathStack) == 0 && key == "methodology_version":
				m.Version = val
			case pathKey == "inputs.task":
				switch key {
				case "identifier":
					m.TaskIdentifier = val
				case "prompt":
					m.TaskPrompt = val
				case "expected_output":
					m.TaskExpectedOutput = val
				}
			case pathKey == "inputs.model":
				switch key {
				case "id":
					m.ModelID = val
				case "provider":
					m.ModelProvider = val
				}
			case pathKey == "inputs.scoring":
				switch key {
				case "function":
					m.ScoringFunction = val
				}
			}
		} else {
			// Opener: push onto the stack.
			pathStack = append(pathStack, key)
		}
		_ = depth // depth is unused now; pathStack carries the context.
	}

	// Validate the required subset. Bench run cannot proceed without
	// these fields — the missing-field error names the absent field so
	// the user can fix the file in one read.
	var missing []string
	if m.Version == "" {
		missing = append(missing, "methodology_version")
	}
	if m.ScoringFunction == "" {
		missing = append(missing, "scoring.function")
	}
	if m.TaskIdentifier == "" {
		missing = append(missing, "inputs.task.identifier")
	}
	if m.TaskPrompt == "" {
		missing = append(missing, "inputs.task.prompt")
	}
	if m.TaskExpectedOutput == "" {
		missing = append(missing, "inputs.task.expected_output")
	}
	if m.ModelID == "" {
		missing = append(missing, "inputs.model.id")
	}
	if m.ModelProvider == "" {
		missing = append(missing, "inputs.model.provider")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("methodology: missing required fields: %s", strings.Join(missing, ", "))
	}
	return m, nil
}

// leadingSpaces counts leading space characters in s (tabs are
// treated as 4-space indentation for the v1 schema; the file is
// hand-curated and uses spaces only).
func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
		} else if r == '\t' {
			n += 4
		} else {
			break
		}
	}
	return n
}

// unquoteYAML strips surrounding single or double quotes from a
// string value. The v1 methodology file uses double quotes consistently
// for string values; single quotes are accepted for symmetry.
func unquoteYAML(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// runBenchRun is the M3 plan Task 4 implementation: the headline
// producer. The flow:
//
//  1. Parse positional args (agent, model, task).
//  2. Load the methodology file; bail with ExitNotFound (AC7) on
//     missing or unreadable file.
//  3. Validate that the supplied (agent, model, task) match the
//     methodology's pinned inputs. The methodology file IS the
//     reproducibility contract — the CLI rejects a run whose inputs
//     drift from the published methodology. (v1 ships single-task;
//     M3 plan Task 4 pins the (agent, model, task) triple in the
//     methodology file. A drift is treated as a usage error so the
//     user knows the headline they would produce does not match the
//     published one.)
//  4. Open the database (or fall through to internal store Open),
//     pre-flight check for the bench_runs table presence.
//  5. Invoke `ktx run <agent> <task.prompt>` as a subprocess. The
//     subprocess's stdout is captured for scoring; its stderr is
//     captured for the wrapper run summary. KTX_SESSION_ID is
//     exported so any runtime hook noops (D1/D10).
//  6. Score the agent's output (last text event's content, falling
//     back to the last non-empty line of stdout) against
//     task.expected_output.
//  7. Find the wrapper's session in the database (most recent session
//     whose agent matches and is not older than the wrapper's start
//     time), aggregate tokens/cost/model from tool_calls.
//  8. D11 derivation fills bench_runs.cost when the tool_calls'
//     SUM(cost) is NULL but tokens + model are present and the cache
//     has the price (AC4).
//  9. Insert the bench_runs row with all 10 columns populated.
//  10. Print the headline JSON envelope to stdout and a one-line
//      summary to stderr (D1 wrapper convention).
//
// Exit codes: 0 on success, 2 (not-found) on missing methodology or
// task definition, 4 (internal) on store / subprocess failures.
func runBenchRun(args []string, opts Options, stdout, stderr io.Writer) int {
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}
	if format != FormatJSON && format != FormatText {
		printError(stderr, usageError("ktx bench run: invalid --format %q: want %q or %q", format, FormatJSON, FormatText), format)
		return ExitUsage
	}

	fs := flag.NewFlagSet("ktx bench run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(rest); err != nil {
		printError(stderr, usageError("ktx bench run: %v", err), format)
		return ExitUsage
	}
	rest = fs.Args()
	if len(rest) != 3 {
		printError(stderr, usageError("ktx bench run: usage: ktx bench run <agent> <model> <task>"), format)
		return ExitUsage
	}
	agentArg, modelArg, taskArg := rest[0], rest[1], rest[2]

	_, m, exitCode := loadMethodologyOrFail(format, stderr)
	if exitCode != ExitOK {
		return exitCode
	}

	// Validate the CLI inputs against the methodology's pinned triple.
	// A drift is a usage error so the user sees the headline does not
	// match the published methodology (Task 4 AC7 / D12 reproducibility
	// contract).
	if agentArg != "opencode" { // v1: agent is the binary name; methodology pins opencode 1.17.8
		printError(stderr, usageError("ktx bench run: agent %q does not match methodology pinned agent %q", agentArg, "opencode"), format)
		return ExitUsage
	}
	if modelArg != m.ModelID {
		printError(stderr, usageError("ktx bench run: model %q does not match methodology pinned model %q", modelArg, m.ModelID), format)
		return ExitUsage
	}
	if taskArg != m.TaskIdentifier {
		printError(stderr, usageError("ktx bench run: task %q does not match methodology pinned task %q", taskArg, m.TaskIdentifier), format)
		return ExitUsage
	}

	// Open the database. The store.Open call applies the bench_runs
	// migration if the database is pre-M3 (D11 forward-fix).
	path, err := store.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx bench run: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := store.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx bench run: open database: %v", err), format)
		return ExitInternal
	}
	defer db.Close()

	// Pre-flight: verify the bench_runs table exists. Doctor would
	// surface this as a warning; bench run surfaces it as a hard error
	// because the run cannot proceed without the table. Plan Task 4
	// AC7 treats missing inputs as a hard failure.
	hasTable, err := db.HasBenchRunsTable()
	if err != nil {
		printError(stderr, internalError("ktx bench run: bench_runs check: %v", err), format)
		return ExitInternal
	}
	if !hasTable {
		printError(stderr, notFoundError("ktx bench run: bench_runs table is missing (pre-M3 database; run any ktx command to apply the migration)"), format)
		return ExitNotFound
	}

	// Invoke the wrapper subprocess. The wrapper's stdout (agent's
	// output, per D1 transparent proxy) is captured for scoring; its
	// stderr (the run summary) is captured for diagnostics. We do NOT
	// pass the wrapper's stdout through to bench run's stdout —
	// bench run's stdout is the headline JSON envelope, and the
	// user explicitly asked for the headline (not the agent's
	// response). The agent's response is queryable via `ktx get` on
	// the underlying tool_calls.
	wrapperStart := time.Now().UTC()
	exe, err := os.Executable()
	if err != nil {
		printError(stderr, internalError("ktx bench run: resolve ktx binary: %v", err), format)
		return ExitInternal
	}
	cmd := exec.Command(exe, "run", agentArg, m.TaskPrompt)
	var agentStdout, agentStderr bytes.Buffer
	cmd.Stdout = &agentStdout
	cmd.Stderr = &agentStderr
	cmd.Env = os.Environ() // KTX_SESSION_ID is exported by the wrapper itself (D10).
	if err := cmd.Run(); err != nil {
		// A non-zero exit from the wrapper is informational, not a
		// bench run failure — the agent may have failed to run but
		// the bench infrastructure still persists a row with score=0
		// and the partial aggregation. The headline is meaningful
		// either way. Surface the wrapper's stderr in the bench
		// run's stderr so the user can diagnose.
		fmt.Fprintf(stderr, "ktx: warning: wrapper exited with error: %v\n", err)
		if agentStderr.Len() > 0 {
			fmt.Fprintf(stderr, "--- wrapper stderr ---\n%s--- end wrapper stderr ---\n", agentStderr.String())
		}
	}

	// Find the wrapper's session. The wrapper creates a session for
	// the agent invocation; we find it by listing sessions and
	// matching the most-recent session whose started_at is after our
	// wrapperStart. The pre-condition (no concurrent ktx writers
	// against this database) holds for v1 because bench run is a
	// single-process invocation; a future concurrent run mode would
	// need a tighter match (e.g. agent name + started_at within a
	// tighter window).
	sessionID, agentName, err := findWrapperSession(db, agentArg, wrapperStart)
	if err != nil {
		printError(stderr, internalError("ktx bench run: find wrapper session: %v", err), format)
		return ExitInternal
	}
	if sessionID == "" {
		// Wrapper did not create a session — most likely the wrapper
		// failed before reaching OpenNamedSession (e.g. spawn error
		// for a non-existent agent). Surface a clear error so the
		// user can fix the agent path.
		printError(stderr, internalError("ktx bench run: wrapper did not create a session; check that agent %q is on PATH", agentArg), format)
		return ExitInternal
	}

	// Aggregate tokens/cost/model from the wrapper's tool_calls.
	tokensIn, tokensOut, sumCost, aggModel, aggProvider, err := db.SummarizeBenchToolCalls(sessionID)
	if err != nil {
		printError(stderr, internalError("ktx bench run: aggregate tool_calls: %v", err), format)
		return ExitInternal
	}
	// Prefer the wrapper's tool_calls values when non-empty; fall
	// back to the methodology-pinned model and the user-supplied
	// agent name. The bench_runs row is the headline record, not a
	// re-summary of the tool_calls.
	if aggModel == "" {
		aggModel = m.ModelID
	}
	if aggProvider == "" {
		aggProvider = m.ModelProvider
	}
	_ = agentName // agentName is the wrapper's session.agent; not needed for the bench row.

	// Score the agent's output. v1 = exact_match against expected.
	actualOutput := extractAgentOutput(&agentStdout)
	score := scoreExactMatch(actualOutput, m.TaskExpectedOutput)

	// Build the BenchRun. The Cost pointer is populated from the
	// SUM(cost) of the underlying tool_calls when present; otherwise
	// it stays nil and deriveBenchCost fills it from the cache at
	// write time (D11 AC4).
	br := &store.BenchRun{
		ID:                 "", // populated below
		Agent:              aggProvider + "/" + aggModel, // bench_runs stores provider/model as the agent identifier
		Model:              aggModel,
		Task:               m.TaskIdentifier,
		Score:              score,
		TokensIn:           tokensIn,
		TokensOut:          tokensOut,
		Cost:               sumCost,
		MethodologyVersion: m.Version,
		Timestamp:          time.Now().UTC().Unix(),
	}
	if br.Agent == "" || br.Agent == "/" {
		// Fallback when neither tool_calls nor the methodology
		// supplied values: the bench row records the CLI-supplied
		// agent name verbatim.
		br.Agent = agentArg
	}

	id, err := store.NewID()
	if err != nil {
		printError(stderr, internalError("ktx bench run: generate id: %v", err), format)
		return ExitInternal
	}
	br.ID = id

	if err := db.InsertBenchRun(br); err != nil {
		printError(stderr, internalError("ktx bench run: persist bench_run: %v", err), format)
		return ExitInternal
	}

	// Re-fetch the persisted row so the output reflects the
	// post-derivation cost (the in-memory br.Cost pointer may have
	// been filled by deriveBenchCost before the INSERT, but the
	// stored row is the source of truth).
	persisted, err := db.QueryBenchRun(id)
	if err != nil {
		printError(stderr, internalError("ktx bench run: refetch bench_run: %v", err), format)
		return ExitInternal
	}

	// Compute the headline (D12). cost_per_correct = cost_USD /
	// max(score, 1e-9). When score=0 the headline is a very large
	// number — the "cost is the contract made loud" signal.
	headline := computeHeadline(persisted.Cost, persisted.Score)

	// JSON envelope per D12's output block: {headline, rows,
	// methodology_version}. One row for `bench run` (the just-inserted
	// bench_run); `rows` is an array for `bench report`'s
	// multi-row view.
	envelope := map[string]any{
		"headline":           headline,
		"rows":               []store.BenchRun{persisted},
		"methodology_version": persisted.MethodologyVersion,
	}

	switch format {
	case FormatText:
		// Text rendering: one-line headline with methodology
		// reference, plus a key-value summary of the row.
		fmt.Fprintf(stdout, "headline: %.6f USD/correct (methodology %s)\n", headline, persisted.MethodologyVersion)
		fmt.Fprintf(stdout, "row id: %s\n", persisted.ID)
		fmt.Fprintf(stdout, "agent: %s\n", persisted.Agent)
		fmt.Fprintf(stdout, "model: %s\n", persisted.Model)
		fmt.Fprintf(stdout, "task: %s\n", persisted.Task)
		fmt.Fprintf(stdout, "score: %.4f\n", persisted.Score)
		if persisted.Cost != nil {
			fmt.Fprintf(stdout, "cost: %.6f\n", *persisted.Cost)
		} else {
			fmt.Fprintln(stdout, "cost: null")
		}
		fmt.Fprintf(stdout, "tokens_in: %d\n", persisted.TokensIn)
		fmt.Fprintf(stdout, "tokens_out: %d\n", persisted.TokensOut)
	default:
		// JSON default (D4). The envelope is the D12 output shape.
		if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
			printError(stderr, internalError("ktx bench run: write JSON: %v", err), format)
			return ExitInternal
		}
	}

	// D1 wrapper convention: one-line summary to stderr. The summary
	// carries the headline and methodology reference so a glance at
	// stderr surfaces the headline without parsing JSON.
	costStr := "null"
	if persisted.Cost != nil {
		costStr = fmt.Sprintf("%.6f", *persisted.Cost)
	}
	fmt.Fprintf(stderr, "{\"summary\":{\"bench_run\":%q,\"methodology\":%q,\"headline\":%.6f,\"score\":%.4f,\"cost\":%s,\"tokens_in\":%d,\"tokens_out\":%d}}\n",
		persisted.ID, persisted.MethodologyVersion, headline, persisted.Score, costStr, persisted.TokensIn, persisted.TokensOut)

	return ExitOK
}

// loadMethodologyOrFail wraps loadMethodology with the CLI error
// envelope. Returns (path, methodology, exit_code). The exit code is
// non-zero when load fails; the caller propagates it.
func loadMethodologyOrFail(format string, stderr io.Writer) (string, *methodology, int) {
	m, path, err := loadMethodology()
	if err != nil {
		switch err {
		case store.ErrBenchMethodologyNotFound:
			printError(stderr, notFoundError("ktx bench run: methodology file not found (looked for %q)", path), format)
			return path, nil, ExitNotFound
		default:
			printError(stderr, internalError("ktx bench run: %v", err), format)
			return path, nil, ExitInternal
		}
	}
	return path, m, ExitOK
}

// findWrapperSession locates the wrapper's session in the database.
// The wrapper creates a session for each invocation; bench run finds
// it by listing sessions and matching the most recent whose agent
// matches agentArg and whose started_at is on or after wrapperStart.
//
// A future concurrent-run mode would need a tighter match — the
// current heuristic assumes single-writer (the wrapper itself) on the
// bench run's database.
func findWrapperSession(db *store.DB, agentArg string, wrapperStart time.Time) (sessionID, agentName string, err error) {
	sessions, err := db.ListSessions()
	if err != nil {
		return "", "", err
	}
	// Pick the most recent session whose started_at is >= wrapperStart
	// (or close enough) and whose agent matches the binary name. The
	// started_at field is an RFC3339Nano string; we compare by string
	// ordering (RFC3339Nano is lexicographically sortable).
	startStr := wrapperStart.Format("2006-01-02T15:04:05.000000000Z07:00")
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		if s.StartedAt < startStr {
			continue
		}
		// The wrapper stores the binary name as the agent field;
		// match it to the CLI-supplied agentArg.
		if s.Agent != agentArg {
			continue
		}
		return s.ID, s.Agent, nil
	}
	// Fallback: most-recent session regardless of started_at (handles
	// clock skew where the wrapper started before bench run's
	// wrapperStart observation).
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		if s.Agent == agentArg {
			return s.ID, s.Agent, nil
		}
	}
	return "", "", nil
}

// extractAgentOutput returns the agent's final response from the
// captured stdout. The wrapper's stdout is the agent's verbatim
// output (D1 transparent proxy); bench run captures it for scoring.
//
// The extractor handles two shapes:
//
//  1. JSONL events: each line is a JSON object with a "type" field.
//     The last `text` event's "content" is the agent's response.
//  2. Plain text: each line is just text. The last non-empty line
//     is the agent's response.
//
// The agent's response is then compared against the methodology's
// expected_output via exact_match scoring (v1).
func extractAgentOutput(stdout *bytes.Buffer) string {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var lastText string
	var lastNonEmpty string
	for scanner.Scan() {
		line := scanner.Bytes()
		text := strings.TrimRight(string(line), "\r")
		if text == "" {
			continue
		}
		lastNonEmpty = text
		// Try to parse as JSON to detect a text event.
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(line, &probe); err == nil {
			var eventType string
			_ = json.Unmarshal(probe["type"], &eventType)
			if eventType == "text" {
				var content string
				if err := json.Unmarshal(probe["content"], &content); err == nil {
					lastText = content
				}
			}
		}
	}
	if lastText != "" {
		return lastText
	}
	return lastNonEmpty
}

// scoreExactMatch is the v1 scorer (D12: scoring.function = exact_match).
// Returns 1.0 when actual equals expected after whitespace trimming,
// 0.0 otherwise. The comparison is byte-exact post-trim; the v1
// methodology documents that "temperature>0 runs are not bit-
// reproducible; exact-match scoring will reject any token-level
// variance." A future methodology version may add Levenshtein-based
// fuzzy matching.
func scoreExactMatch(actual, expected string) float64 {
	if strings.TrimSpace(actual) == strings.TrimSpace(expected) {
		return 1.0
	}
	return 0.0
}

// computeHeadline computes the D12 headline number:
//
//	cost_per_correct = cost_USD / max(score, 1e-9)
//
// The epsilon prevents division by zero when score=0. The 1e-9
// guard is documented in methodology-v1.yaml and the D12 entry.
// Returns 0 when cost is nil (the bench row's NULL cost path — the
// headline reflects partial data, AC5 soft-fail).
func computeHeadline(cost *float64, score float64) float64 {
	if cost == nil {
		return 0
	}
	denom := score
	if denom < benchEpsilon {
		denom = benchEpsilon
	}
	return *cost / denom
}

// printBenchHelp writes the `ktx bench --help` text. Mirrors the
// help style of `ktx prices --help` and `ktx doctor --help`.
func printBenchHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx bench — benchmark namespace (M3)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx bench <subcommand> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  run <agent> <model> <task>")
	fmt.Fprintln(w, "      Execute the methodology-pinned (agent, model, task) triple,")
	fmt.Fprintln(w, "      persist a bench_runs row with the headline number, and print")
	fmt.Fprintln(w, "      the headline to stdout (JSON default) plus a one-line")
	fmt.Fprintln(w, "      summary to stderr.")
	fmt.Fprintln(w, "  report")
	fmt.Fprintln(w, "      Emit the headline from persisted bench_runs rows (reserved")
	fmt.Fprintln(w, "      for plan Task 5; not yet implemented).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Methodology file: .agentera/bench/methodology-v1.yaml (D12). The")
	fmt.Fprintln(w, "CLI inputs (agent, model, task) must match the methodology's")
	fmt.Fprintln(w, "pinned triple; a drift is a usage error so the headline matches")
	fmt.Fprintln(w, "the published methodology (D12 reproducibility contract).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  {headline, rows, methodology_version} envelope")
	fmt.Fprintln(w, "  text            one-line headline + key-value row summary")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  bench run completed")
	fmt.Fprintln(w, "  1  usage error (wrong number of args, drift from methodology)")
	fmt.Fprintln(w, "  2  not found (missing methodology file, missing bench_runs)")
	fmt.Fprintln(w, "  4  internal (database, subprocess, or write failure)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
