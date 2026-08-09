// `ktx bench` — the benchmark namespace.
//
// bench owns the headline number that proves or disputes the cost
// contract. The public surface includes run, report, export, compare, rank,
// validate, agent, evidence, and corpus command families. The original
// database-backed commands are:
//
//	ktx bench run <agent> <model> <task>
//	    Consumes the methodology, runs the agent via the existing
//	    wrapper path (ktx run <agent> ...), scores per the methodology's
//	    scoring function (v1 = exact_match), persists a bench_runs row
//	    with cost derived at write time, and prints the headline
//	    number to stdout plus a one-line summary to stderr (wrapper
//	    convention).
//
//	ktx bench report [<id>] [--methodology <ver>]
//	    Reads the bench_runs table and emits the headline number with
//	    the methodology reference visible (JSON default + --format text).
//	    Three invocation modes: list (no <id>, all rows in insertion order);
//	    single-row (<id>, emits the row directly, not-found → exit 2);
//	    filtered (--methodology <ver>, recomputes headline on the
//	    filtered set).
//
// Database path resolution: KTX_DB env > default. The dispatch is the
// same shape as `ktx prices` and `ktx doctor` — a namespace with
// subcommands, no flags of its own (v1).
//
// Supported native methodology source files are explicitly embedded from
// `tools/bench`, so the installed binary does not depend on a source checkout.
// Each source definition is the reproducibility contract for its benchmark.
//
// Output contract: JSON default with --format text for humans.
// The headline number is `cost_per_correct = cost_USD / max(score,
// 1e-9)` USD, six decimal places. The JSON envelope carries the
// headline, the rows array (one element for the just-inserted row in
// `bench run`, all rows for `bench report`), and the methodology
// version string for offline reproduction.
package cli

import (
	"flag"
	"fmt"
	"io"
)

// benchEpsilon is the division-by-zero guard from the methodology
// formula (`cost_per_correct = cost_USD / max(score, 1.0e-9)`).
// score=0 produces a very large cost-per-correct value.
const benchEpsilon = 1.0e-9

// runBenchWith is the testable core of `ktx bench`.
// stdout/stderr are injected so the namespace dispatch flow can be
// exercised without touching os.Stdout/Stderr. The body parses the
// top-level bench flag set and dispatches to runBenchReport or
// runBenchRun, returning the sub-handler's exit code directly.
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
	case "export":
		return runBenchExport(rest, opts, stdout, stderr)
	case "compare":
		return runBenchCompare(rest, opts, stdout, stderr)
	case "rank":
		return runBenchRank(rest, opts, stdout, stderr)
	case "validate":
		return runBenchValidate(rest, opts, stdout, stderr)
	case "corpus":
		return runBenchCorpus(rest, opts, stdout, stderr)
	case "agent":
		return runBenchAgent(rest, opts, stdout, stderr)
	case "evidence":
		return runBenchEvidence(rest, opts, stdout, stderr)
	default:
		printError(stderr, usageError("ktx bench: unknown subcommand %q; run 'ktx bench --help'", sub), opts.Format)
		return ExitUsage
	}
}

// benchCommandFunc routes the bench namespace to its subcommands.
// Subcommands render their own diagnostics and return their status through
// benchSubToExit.
func benchCommandFunc(ctx *DispatchContext) (any, error) {
	// Bench subcommands accept help after the namespace token, so scan the
	// remaining arguments before routing.
	showHelp, rest := scanBenchHelp(ctx.Args)
	if showHelp {
		printBenchHelp(ctx.Stdout)
		return nil, nil
	}

	if len(rest) == 0 {
		printBenchHelp(ctx.Stdout)
		return nil, nil
	}

	sub := rest[0]
	tail := rest[1:]
	opts := Options{Format: ctx.Format}

	switch sub {
	case "run":
		return benchSubToExit(runBenchRun(tail, opts, ctx.Stdout, ctx.Stderr))
	case "report":
		return benchSubToExit(runBenchReport(tail, opts, ctx.Stdout, ctx.Stderr))
	case "export":
		return benchSubToExit(runBenchExport(tail, opts, ctx.Stdout, ctx.Stderr))
	case "compare":
		return benchSubToExit(runBenchCompare(tail, opts, ctx.Stdout, ctx.Stderr))
	case "rank":
		return benchSubToExit(runBenchRank(tail, opts, ctx.Stdout, ctx.Stderr))
	case "validate":
		return benchSubToExit(runBenchValidate(tail, opts, ctx.Stdout, ctx.Stderr))
	case "corpus":
		return benchSubToExit(runBenchCorpus(tail, opts, ctx.Stdout, ctx.Stderr))
	case "agent":
		return benchSubToExit(runBenchAgent(tail, opts, ctx.Stdout, ctx.Stderr))
	case "evidence":
		return benchSubToExit(runBenchEvidence(tail, opts, ctx.Stdout, ctx.Stderr))
	default:
		return nil, usageError("ktx bench: unknown subcommand %q; run 'ktx bench --help'", sub)
	}
}

// scanBenchHelp strips --help/-h from args. The scan
// matches the stdlib flag package's stop-at-first-non-flag behavior
// for the top-level bench dispatch (so `ktx bench report --help`
// routes --help to bench report, not to the bench namespace).
func scanBenchHelp(args []string) (showHelp bool, rest []string) {
	for _, a := range args {
		switch a {
		case "--help", "-h":
			showHelp = true
			continue
		}
		rest = append(rest, a)
	}
	return showHelp, rest
}

// benchSubToExit forwards a subcommand status without rendering a second error
// envelope; benchmark subcommands own their diagnostics.
func benchSubToExit(code int) (any, error) {
	if code == ExitOK {
		return nil, nil
	}
	return nil, exitWith(code)
}

// init registers `ktx bench`. Its subcommands open the database when needed.
func init() {
	Register("bench", RegisterOptions{
		Description:   "benchmark runs, experiments, and reports",
		FormatDefault: FormatJSON,
		Help:          printBenchHelp,
	}, benchCommandFunc)
}

// printBenchHelp writes the `ktx bench --help` text. Mirrors the
// help style of `ktx prices --help` and `ktx doctor --help`.
func printBenchHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx bench — benchmark namespace")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx bench <subcommand> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  run <agent> <model> <task>")
	fmt.Fprintln(w, "      Execute the methodology-pinned (agent, model, task) triple,")
	fmt.Fprintln(w, "      persist a bench_runs row with the headline number, and print")
	fmt.Fprintln(w, "      the headline to stdout (JSON default) plus a one-line")
	fmt.Fprintln(w, "      summary to stderr.")
	fmt.Fprintln(w, "  run --replay <session-id>")
	fmt.Fprintln(w, "      Replay a database-recorded session: re-parse the JSONL stream,")
	fmt.Fprintln(w, "      re-aggregate usage/cost/cache, and emit a")
	fmt.Fprintln(w, "      consistency headline comparing replay-derived metrics to the")
	fmt.Fprintln(w, "      original bench_runs row when present. Parentless streams remain")
	fmt.Fprintln(w, "      supported with comparison unavailable. PASS and informational")
	fmt.Fprintln(w, "      usage/cost/cache DRIFT exit 0; integrity or parser FAIL exits 4.")
	fmt.Fprintln(w, "      Zero agent calls. A path-like value remains a session-id.")
	fmt.Fprintln(w, "  run --replay-file <path>")
	fmt.Fprintln(w, "      Strictly verify a portable fixture offline by explicit path. Version 1")
	fmt.Fprintln(w, "      requires a benchmark parent; stream content/format/SHA-256; parser count;")
	fmt.Fprintln(w, "      nullable usage, cost, and cache fields (absent differs from zero); and")
	fmt.Fprintln(w, "      controlled built-in compression expectations. Malformed contracts exit 1")
	fmt.Fprintln(w, "      with failure_class=invalid-replay-fixture and a correction. Expectation")
	fmt.Fprintln(w, "      drift emits evidence and exits 4 with")
	fmt.Fprintln(w, "      failure_class=replay-fixture-verification-failed. File replay never reads")
	fmt.Fprintln(w, "      the database, user configuration, or plugins.")
	fmt.Fprintln(w, "      The SHA-256 detects accidental content drift; it is not a defense")
	fmt.Fprintln(w, "      against a committer changing both content and expectations.")
	fmt.Fprintln(w, "  report [<id>] [--methodology <version>] [--format text|json]")
	fmt.Fprintln(w, "      Emit the headline from persisted bench_runs rows.")
	fmt.Fprintln(w, "      Without <id>: list all rows in insertion order (oldest first);")
	fmt.Fprintln(w, "        headline is the methodology-defined aggregate across rows.")
	fmt.Fprintln(w, "      With <id>: emit a single row by nanoid; not-found → exit 2.")
	fmt.Fprintln(w, "      --methodology filters rows by methodology_version; the")
	fmt.Fprintln(w, "        headline is recomputed from the filtered set.")
	fmt.Fprintln(w, "  export [--output <path|->] <session-id>")
	fmt.Fprintln(w, "      Export one consistent benchmark-parented opencode-jsonl-1 stream as a")
	fmt.Fprintln(w, "      canonical version 1 fixture. Defaults to stdout; --output - is explicit")
	fmt.Fprintln(w, "      stdout delivery and file publication is atomic. The source database is")
	fmt.Fprintln(w, "      read-only. Unsupported source evidence exits 1 with")
	fmt.Fprintln(w, "      failure_class=invalid-replay-fixture-source; missing sessions exit 2.")
	fmt.Fprintln(w, "      Export validates consistency, not privacy or redaction; review raw stream")
	fmt.Fprintln(w, "      content before sharing. See docs/pipelines.md for the refresh procedure.")
	fmt.Fprintln(w, "  compare --model <model> [--variant <name>] --task-file <path>")
	fmt.Fprintln(w, "      Run a retained live baseline/candidate experiment.")
	fmt.Fprintln(w, "      --preflight attests source/image collection capability without auth,")
	fmt.Fprintln(w, "      model calls, or experiment artifacts; normal comparisons run it too.")
	fmt.Fprintln(w, "      The generic preflight returns before task loading; --build adds image creation.")
	fmt.Fprintln(w, "      Cache-aware opt-in: --methodology tools/bench/methodology-v6-prompt-cache-token-hit-rate.yaml")
	fmt.Fprintln(w, "      with --cache-control auto|isolated|observed, --warmup N, --rounds N.")
	fmt.Fprintln(w, "  rank --model <model> [--variant <name>] --task-file <path>")
	fmt.Fprintln(w, "      Compare and rank named pipeline configurations.")
	fmt.Fprintln(w, "  validate --experiment <directory>")
	fmt.Fprintln(w, "      Verify retained artifacts, hashes, links, and summary bytes.")
	fmt.Fprintln(w, "  agent --capabilities")
	fmt.Fprintln(w, "      Emit the offline image collector contract as structured JSON.")
	fmt.Fprintln(w, "  evidence preflight --spec <path> [--output <path>]")
	fmt.Fprintln(w, "      Run the no-network gate for bounded OpenAI/OpenCode cache evidence.")
	fmt.Fprintln(w, "  evidence gemini-preflight --spec <path> [--output <path>]")
	fmt.Fprintln(w, "      Run the no-network gate for bounded Gemini/OpenCode cache evidence.")
	fmt.Fprintln(w, "  corpus generate [--output <directory>]")
	fmt.Fprintln(w, "      Regenerate the deterministic planted-fact corpus.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Methodology source: tools/bench/methodology-v1.yaml (embedded). The")
	fmt.Fprintln(w, "CLI inputs (agent, model, task) must match the methodology's")
	fmt.Fprintln(w, "pinned triple; a drift is a usage error so the headline matches")
	fmt.Fprintln(w, "the published methodology (reproducibility contract).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  {headline, methodology_version, rows} envelope")
	fmt.Fprintln(w, "                  (single-row <id> emits the row directly)")
	fmt.Fprintln(w, "  text            one-line headline + tabwriter-aligned rows")
	fmt.Fprintln(w, "                  (single-row <id> emits a key-value summary)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  success; database replay DRIFT is informational")
	fmt.Fprintln(w, "  1  usage/contract error (wrong args, invalid format or fixture/source)")
	fmt.Fprintln(w, "  2  not found (missing methodology, report ID, or recorded session)")
	fmt.Fprintln(w, "  4  internal/verification failure (database, output, replay FAIL or fixture drift)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
