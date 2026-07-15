// `ktx bench` — the benchmark namespace.
//
// bench owns the headline number that proves or disputes the cost
// contract. The v1 surface ships two subcommands:
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
// Published methodologies are embedded from `tools/bench/methodology-*.yaml`,
// so the installed binary does not depend on a source checkout. Each source
// definition is the reproducibility contract for its benchmark.
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
		NeedsDB:       false,
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
	fmt.Fprintln(w, "      Replay a recorded session: re-parse the")
	fmt.Fprintln(w, "      recorded JSONL stream, re-aggregate tokens/cost, and emit a")
	fmt.Fprintln(w, "      consistency headline comparing replay-derived metrics to the")
	fmt.Fprintln(w, "      original bench_runs row. Zero agent calls (fast, free,")
	fmt.Fprintln(w, "      deterministic). The session-id is queryable via")
	fmt.Fprintln(w, "      `ktx list` or `ktx get <bench_run_id>`.")
	fmt.Fprintln(w, "  report [<id>] [--methodology <version>] [--format text|json]")
	fmt.Fprintln(w, "      Emit the headline from persisted bench_runs rows.")
	fmt.Fprintln(w, "      Without <id>: list all rows in insertion order (oldest first);")
	fmt.Fprintln(w, "        headline is the methodology-defined aggregate across rows.")
	fmt.Fprintln(w, "      With <id>: emit a single row by nanoid; not-found → exit 2.")
	fmt.Fprintln(w, "      --methodology filters rows by methodology_version; the")
	fmt.Fprintln(w, "        headline is recomputed from the filtered set.")
	fmt.Fprintln(w, "  compare --model <model> [--variant <name>] --task-file <path>")
	fmt.Fprintln(w, "      Run a retained live baseline/candidate experiment.")
	fmt.Fprintln(w, "      Cache-aware opt-in: --methodology tools/bench/methodology-v5-cache-aware.yaml")
	fmt.Fprintln(w, "      with --cache-control auto|isolated|observed, --warmup N, --rounds N.")
	fmt.Fprintln(w, "  rank --model <model> [--variant <name>] --task-file <path>")
	fmt.Fprintln(w, "      Compare and rank named pipeline configurations.")
	fmt.Fprintln(w, "  validate --experiment <directory>")
	fmt.Fprintln(w, "      Verify retained artifacts, hashes, links, and summary bytes.")
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
	fmt.Fprintln(w, "  0  report printed (may be empty)")
	fmt.Fprintln(w, "  1  usage error (wrong args, drift from methodology, invalid format)")
	fmt.Fprintln(w, "  2  not found (missing methodology, <id> not in DB)")
	fmt.Fprintln(w, "  4  internal (database open / query failure)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
