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
//	ktx bench report [<id>] [--methodology <ver>]
//	    Reads the bench_runs table and emits the headline number with
//	    the methodology reference visible (JSON default + --format text).
//	    Three invocation modes: list (no <id>, all rows in insertion order);
//	    single-row (<id>, emits the row directly, not-found → exit 2);
//	    filtered (--methodology <ver>, recomputes headline on the
//	    filtered set). Plan Task 5 implements all three.
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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
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

// runBenchWith is the testable core of `ktx bench` (Task 3 seam).
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
	default:
		printError(stderr, usageError("ktx bench: unknown subcommand %q; run 'ktx bench --help'", sub), opts.Format)
		return ExitUsage
	}
}

// benchCommandFunc is the dispatch wrapper for the bench namespace.
// The dispatcher already parsed --help/-h; the verb's job is to look
// at the positional tail and route to runBenchReport or runBenchRun,
// converting their int exit code into the dispatcher's (any, error)
// shape.
//
// Per the plan Task 6 hard constraint, runBenchReport and
// runBenchReportImpl remain direct test seams — they open their own
// database and parse their own flags. The top-level namespace
// dispatcher is the only piece that registers via dispatch.Register;
// the inner adapters stay callable directly so existing Task 3 tests
// pass unchanged.
//
// Exit-code mapping: 0 → (nil, nil); non-zero → typed error. The
// dispatcher's exitCodeOf recognises CodeUsage (1), CodeNotFound (2),
// and CodeInternal (4) so the not-found envelope from bench report
// (which is constructed via notFoundError inside runBenchReportImpl)
// reaches the user with exit 2 instead of being collapsed to exit 4.
func benchCommandFunc(ctx *DispatchContext) (any, error) {
	// Re-parse --help/-h from ctx.Args. The dispatcher already
	// consumed --help via the Help hook (renderHelp → printBenchHelp
	// → exit 0) before invoking the verb, so this branch is
	// defensive: it covers the rare case where the dispatcher's
	// flag set ever stops consuming --help before the verb.
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
		return benchSubToErr("ktx bench run", runBenchRun(tail, opts, ctx.Stdout, ctx.Stderr))
	case "report":
		return benchSubToErr("ktx bench report", runBenchReport(tail, opts, ctx.Stdout, ctx.Stderr))
	default:
		return nil, usageError("ktx bench: unknown subcommand %q; run 'ktx bench --help'", sub)
	}
}

// scanBenchHelp strips --help/-h from args. The hand-rolled scan
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

// benchSubToErr converts an int exit code from a bench sub-handler
// into the dispatcher's (any, error) shape. The mapping mirrors
// runBenchReportImpl's typed-error contract: a usage error from
// bench report surfaces as a typed *Error{CodeUsage} so the
// dispatcher's exitCodeOf maps it to exit 1; not-found surfaces as
// *Error{CodeNotFound} (exit 2); internal surfaces as
// *Error{CodeInternal} (exit 4).
//
// Note on envelope rendering: the sub-handlers (runBenchReport,
// runBenchRun) print their own error envelopes to stderr and return
// the int exit code. The dispatcher's exitCodeOf only sees the
// typed error returned here, so the dispatcher's printError is NOT
// invoked on the dispatch path — the sub-handler's envelope is the
// one that reaches the user. The sub-handler returns int not error,
// so the dispatcher's printError path is bypassed entirely; only
// exitCodeOf is consulted (via the typed error returned here). For
// AC5 (empty bench_runs → exit 0), no envelope is printed; the
// dispatcher's success path applies.
func benchSubToErr(prefix string, code int) (any, error) {
	switch code {
	case ExitOK:
		return nil, nil
	case ExitUsage:
		return nil, usageError("%s: usage error", prefix)
	case ExitNotFound:
		return nil, notFoundError("%s: not found", prefix)
	case ExitInternal:
		return nil, internalError("%s: internal error", prefix)
	default:
		return nil, internalError("%s: exit code %d", prefix, code)
	}
}

// init registers `ktx bench` with the dispatch module. After init
// runs, the verb is reachable through runWith (the legacy
// commandIndex entry in cli.go is removed in Task 6).
//
// NeedsDB=false: the bench namespace dispatcher itself does not
// touch the database; runBenchReport and runBenchRun each open
// their own *dbpkg.DB via their existing dispatch adapter paths.
// The inner functions remain direct test seams (runBenchReportImpl
// takes a pre-opened DB; runBenchReport opens the DB on call).
//
// FormatDefault=FormatJSON: matches the plan Task 6 contract for
// `ktx bench report`'s JSON default with --format text for humans.
func init() {
	Register("bench", RegisterOptions{
		Description:   "benchmark namespace (run, report)",
		NeedsDB:       false,
		FormatDefault: FormatJSON,
		Help:          printBenchHelp,
	}, benchCommandFunc)
}

// runBenchReport is the plan Task 5 implementation. It replaces the
// Task 4 "not yet implemented" stub with the headline consumer that
// surfaces the D12 headline from persisted bench_runs rows. Three
// invocation modes are supported:
//
//	ktx bench report                      — list all rows in insertion
//	                                       order (oldest first); emit
//	                                       top-level headline + rows
//	                                       array (JSON default) or
//	                                       one-line summary + tabwriter
//	                                       table (--format text)
//	ktx bench report <id>                 — single-row lookup by D3
//	                                       nanoid; JSON default emits
//	                                       the row directly (no rows
//	                                       array, no headline envelope);
//	                                       --format text emits a
//	                                       one-line-per-field summary
//	ktx bench report --methodology <ver>  — filter rows by
//	                                       methodology_version; headline
//	                                       is recomputed from the
//	                                       filtered set
//
// The function is the dispatcher adapter: it parses --format and
// --methodology, opens the database, and converts the typed error
// envelope into an exit code. The spec-signature implementation lives
// in runBenchReportImpl (stdout, stderr, db, args, format, filter) →
// error. The split mirrors the runRun/runRunWith pattern elsewhere in
// the CLI layer (the adapter owns flag.Parse + DB open + exit code;
// the impl owns the three-mode dispatch + rendering).
//
// Exit codes (D4 contract):
//
//	0 = ok (including empty bench_runs; an empty benchmark is not an error)
//	1 = usage error (invalid --format, unknown flag, unexpected arg)
//	2 = not-found (positional <id> not in DB)
//	4 = internal (database open / query failure)
func runBenchReport(args []string, opts Options, stdout, stderr io.Writer) int {
	// Subcommand-local --format. extractFormat accepts both --format
	// value and --format=value forms; the subcommand's value takes
	// precedence over the global opts.Format when both are set.
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}
	if format != FormatJSON && format != FormatText {
		printError(stderr, usageError("ktx bench report: invalid --format %q: want %q or %q", format, FormatJSON, FormatText), format)
		return ExitUsage
	}

	// Subcommand-local --methodology. The filter is a single string
	// flag; the empty string is the "no filter" sentinel. A missing
	// value (--methodology alone, with no following token) is caught
	// by flag.Parse as a usage error.
	fs := flag.NewFlagSet("ktx bench report", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	var methodologyFilter string
	fs.StringVar(&methodologyFilter, "methodology", "", "filter rows by methodology_version (e.g., 'v1')")
	if err := fs.Parse(rest); err != nil {
		printError(stderr, usageError("ktx bench report: %v", err), format)
		return ExitUsage
	}

	// Open the database. The dbpkg.Open call applies the bench_runs
	// migration if the database is pre-M3 (Task 3 idempotent path).
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx bench report: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := dbpkg.Open(path)
	if err != nil {
		printError(stderr, internalError("ktx bench report: open database: %v", err), format)
		return ExitInternal
	}
	defer db.Close()

	if err := runBenchReportImpl(stdout, stderr, db, fs.Args(), format, methodologyFilter); err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// runBenchReportImpl is the Task 5 implementation core. Its signature
// matches the plan Task 5 spec exactly: stdout/stderr injection, a
// pre-opened *dbpkg.DB, the parsed positional args (optional <id>),
// the resolved format ("" or FormatJSON default to JSON; FormatText for
// humans), and the methodology filter ("" for no filter). Returns a
// typed *Error on failure so the dispatcher adapter can map it to an
// exit code via the D4 contract.
//
// The three invocation modes are dispatched on the presence of a
// positional arg:
//
//	[]                   → list mode: all rows (or --methodology-filtered)
//	[id]                 → single-row mode: QueryBenchRun by id
//	[id1, id2, ...]      → usage error (single-row only takes one id)
//
// Pointer discipline preserved end-to-end: BenchRun.Cost is *float64 so
// the absent-vs-zero distinction survives JSON round-trips (the
// existing Task 4 contract). The text renderer maps a nil Cost to "-"
// (matches M2's ktx list --format text convention for absent M2 fields).
func runBenchReportImpl(
	stdout, stderr io.Writer,
	db *dbpkg.DB,
	args []string,
	format string,
	methodologyFilter string,
) error {
	_ = stderr // stderr is reserved for the dispatcher adapter's error envelope; unused in the success path.

	// Single-row mode: positional <id>. The plan Task 5 AC3 contract
	// is "JSON for the row only (no rows array, headline is the row's
	// value); if no row matches, ktx exits with code 2 (not-found)".
	if len(args) >= 1 {
		if len(args) > 1 {
			return usageError("ktx bench report: unexpected argument %q; report takes a single optional <id>", args[1])
		}
		id := args[0]
		b, err := db.QueryBenchRun(id)
		if err != nil {
			if errors.Is(err, dbpkg.ErrNotFound) {
				return notFoundError("ktx bench report: no bench_run with id '%s'", id)
			}
			return internalError("ktx bench report: query bench_run: %v", err)
		}
		return renderBenchRunSingle(stdout, format, b)
	}

	// List / filtered mode: load all rows in insertion order (D12's
	// "rows array, oldest first" contract — QueryBenchRuns returns
	// ORDER BY timestamp ASC, id ASC). Filter in memory when
	// --methodology is set; the cost of an extra SELECT WHERE on a
	// ~handful of rows is not worth the extra method on the db package, and an
	// in-memory filter keeps the headline aggregation in one place.
	rows, err := db.QueryBenchRuns()
	if err != nil {
		return internalError("ktx bench report: query bench_runs: %v", err)
	}
	if methodologyFilter != "" {
		rows = filterByMethodology(rows, methodologyFilter)
	}

	if format == FormatText {
		return renderBenchReportText(stdout, rows, methodologyFilter)
	}
	return renderBenchReportJSON(stdout, rows, methodologyFilter)
}

// renderBenchRunSingle writes the single-row output. JSON default
// emits the BenchRun directly (no headline envelope per AC3); the
// existing BenchRun JSON tags govern the shape — id, agent, model,
// task, score, tokens_in, tokens_out, cost, methodology_version,
// timestamp — with pointer discipline preserved (Cost *float64,
// tokens_in/out as int64 per the db schema). Text format emits a
// one-line-per-field summary aligned with the list-mode table.
func renderBenchRunSingle(stdout io.Writer, format string, b dbpkg.BenchRun) error {
	if format == FormatText {
		return renderBenchRunSingleText(stdout, b)
	}
	return json.NewEncoder(stdout).Encode(b)
}

// renderBenchRunSingleText writes the key-value summary for a single
// row. The field set mirrors the spec's single-row text example:
// id, agent, model, task, score, cost (with "USD" suffix for human
// readability), methodology_version, timestamp (RFC3339Nano). tokens_in
// and tokens_out are omitted from the text rendering — the spec's
// single-row text example shows them absent, and the per-row cost is
// the headline-relevant number for a single run.
func renderBenchRunSingleText(w io.Writer, b dbpkg.BenchRun) error {
	fmt.Fprintf(w, "id:                  %s\n", b.ID)
	fmt.Fprintf(w, "agent:               %s\n", b.Agent)
	fmt.Fprintf(w, "model:               %s\n", b.Model)
	fmt.Fprintf(w, "task:                %s\n", b.Task)
	fmt.Fprintf(w, "score:               %.4f\n", b.Score)
	if b.Cost != nil {
		fmt.Fprintf(w, "cost:                %.6f USD\n", *b.Cost)
	} else {
		fmt.Fprintln(w, "cost:                -")
	}
	fmt.Fprintf(w, "methodology_version: %s\n", b.MethodologyVersion)
	fmt.Fprintf(w, "timestamp:           %s\n", formatBenchTimestamp(b.Timestamp))
	return nil
}

// renderBenchReportJSON writes the list-mode JSON envelope per the
// plan Task 5 AC1 contract:
//
//	{
//	  "headline": <number|null>,
//	  "methodology_version": <string|null>,
//	  "rows": [BenchRun, ...]
//	}
//
// The headline is the multi-row aggregate (SUM(cost) / max(SUM(score),
// 1e-9), per D12) or null when SUM(cost) is NULL (every row's cost is
// NULL). methodology_version prefers the first row's value (the data's
// own statement); when no rows exist, it falls back to the filter
// string so a `--methodology v1` invocation with zero matching rows
// still surfaces "we filtered for v1" in the envelope. An empty
// bench_runs table with no filter renders methodology_version as null
// (the AC6 contract).
//
// The BenchRun struct's JSON tags govern the per-row shape (10
// fields including tokens_in / tokens_out, cost as *float64 with
// nil → JSON null). The headline is *float64-aware: a nil pointer
// emits JSON null via the envelope's map[string]any indirection.
func renderBenchReportJSON(stdout io.Writer, rows []dbpkg.BenchRun, methodologyFilter string) error {
	headline, ok := computeAggregateHeadline(rows)
	var headlineVal any = nil
	if ok {
		headlineVal = headline
	}
	methodologyVersion := envelopeMethodologyVersion(rows, methodologyFilter)
	envelope := map[string]any{
		"headline":            headlineVal,
		"methodology_version": methodologyVersion,
		"rows":                rows,
	}
	return json.NewEncoder(stdout).Encode(envelope)
}

// renderBenchReportText writes the list-mode text output: a one-line
// headline summary with the methodology reference visible (per the
// spec example and AC2), a horizontal rule, and a tabwriter-aligned
// table of rows in insertion order. An empty bench_runs (or a filter
// that matches no rows) emits the headline summary only — no rule, no
// table — so the user sees "no data" cleanly without a half-rendered
// header.
func renderBenchReportText(w io.Writer, rows []dbpkg.BenchRun, methodologyFilter string) error {
	headline, ok := computeAggregateHeadline(rows)
	var headlineStr string
	if ok {
		headlineStr = fmt.Sprintf("%.6f", headline)
	} else {
		headlineStr = "<no data>"
	}
	methodologyVersionStr := envelopeMethodologyVersion(rows, methodologyFilter)
	if methodologyVersionStr == nil {
		methodologyVersionStr = "-"
	}
	fmt.Fprintf(w, "methodology %s — headline: %s USD/correct (N=%d)\n",
		methodologyVersionStr, headlineStr, len(rows))

	if len(rows) == 0 {
		return nil
	}

	fmt.Fprintln(w, "─────────────────────────────────────────────")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "id\tagent\ttask\tscore\tcost\tmethod\ttimestamp")
	for _, b := range rows {
		cost := "-"
		if b.Cost != nil {
			cost = fmt.Sprintf("%.6f", *b.Cost)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.4f\t%s\t%s\t%s\n",
			b.ID, b.Agent, b.Task, b.Score, cost, b.MethodologyVersion,
			formatBenchTimestamp(b.Timestamp))
	}
	return tw.Flush()
}

// envelopeMethodologyVersion returns the envelope's methodology_version
// field. First-row value takes precedence over the filter (the data's
// own statement is the source of truth); the filter string is the
// fallback when no rows exist so a `--methodology v1` invocation with
// zero matching rows still self-describes. Both sources nil returns
// nil — JSON renders that as null, the text renderer maps it to "-".
func envelopeMethodologyVersion(rows []dbpkg.BenchRun, methodologyFilter string) any {
	if len(rows) > 0 {
		return rows[0].MethodologyVersion
	}
	if methodologyFilter != "" {
		return methodologyFilter
	}
	return nil
}

// filterByMethodology returns a new slice containing only rows whose
// methodology_version matches version. An empty version returns rows
// unchanged (the no-filter case is handled by the caller skipping the
// filter call entirely, but the helper is defensive against accidental
// empty-string invocations).
//
// The filter is in-memory rather than SQL WHERE. Two reasons: (a) the
// bench_runs table is small (one row per bench run; v1 methodology
// pins a single (agent, model, task) triple, so the cardinality is
// bounded by the number of `ktx bench run` invocations); (b) the
// headline aggregation runs across the filtered set, so keeping the
// rows in memory lets one pass handle filter + aggregate + render.
// Adding a QueryBenchRunsByMethodology method on the db package would push the
// filter into SQL without simplifying the rest of the flow.
func filterByMethodology(rows []dbpkg.BenchRun, version string) []dbpkg.BenchRun {
	if version == "" {
		return rows
	}
	out := make([]dbpkg.BenchRun, 0, len(rows))
	for _, b := range rows {
		if b.MethodologyVersion == version {
			out = append(out, b)
		}
	}
	return out
}

// computeAggregateHeadline computes the multi-row headline per the
// D12 methodology's `computation:` block:
//
//	headline = SUM(cost) / max(SUM(score), 1.0e-9)
//
// Returns (headline, ok) where ok=false signals the headline is null
// (every row's Cost pointer is nil — there is no cost to aggregate,
// so the headline is meaningless). The 1e-9 epsilon guards against
// division by zero when SUM(score)=0; the resulting large value
// correctly signals "an incorrect answer is unboundedly expensive per
// correct" (the vision's "cost is the contract made loud" — D12
// reasoning).
//
// Per-row score is 0.0 or 1.0 (exact-match v1), so SUM(score) over
// rows is the count of correct runs; the headline is total_cost /
// correct_runs, the average cost per correct answer.
//
// computeAggregateHeadline does NOT coerce a nil totalCost to 0 —
// the caller must check the ok flag and render null when ok=false.
// This preserves the absent-vs-meaningful distinction at the
// aggregation level: an empty bench_runs table (zero rows, no cost)
// and a bench_runs table with cost=0.0 on every row (one row × $0)
// both produce a "null" headline, because the dataset carries no
// evidence of cost to aggregate.
func computeAggregateHeadline(rows []dbpkg.BenchRun) (float64, bool) {
	var totalCost *float64
	var totalScore float64
	for i := range rows {
		if rows[i].Cost != nil {
			if totalCost == nil {
				v := *rows[i].Cost
				totalCost = &v
			} else {
				*totalCost += *rows[i].Cost
			}
		}
		totalScore += rows[i].Score
	}
	if totalCost == nil {
		return 0, false
	}
	denom := totalScore
	if denom < benchEpsilon {
		denom = benchEpsilon
	}
	return *totalCost / denom, true
}

// formatBenchTimestamp converts the bench_runs INTEGER Unix epoch
// seconds column to RFC3339Nano for human-readable text output. The
// JSON path emits the integer as-is (the D12 reproducibility contract
// keeps the timestamp machine-readable; RFC3339Nano is for humans).
// A zero or negative timestamp renders as "-" (matches the cost
// pointer discipline: nil Cost → "-"; zero timestamp → "-").
func formatBenchTimestamp(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339Nano)
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
			return nil, path, dbpkg.ErrBenchMethodologyNotFound
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
// dbpkg.ErrBenchMethodologyNotFound when no such directory exists
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
			return "", dbpkg.ErrBenchMethodologyNotFound
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
//  4. Open the database (or fall through to internal db.Open),
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
// task definition, 4 (internal) on database / subprocess failures.
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

	// Open the database. The dbpkg.Open call applies the bench_runs
	// migration if the database is pre-M3 (D11 forward-fix).
	path, err := dbpkg.ResolvePath("")
	if err != nil {
		printError(stderr, internalError("ktx bench run: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := dbpkg.Open(path)
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
	modelSpec := m.ModelProvider + "/" + m.ModelID
	cmd := exec.Command(exe, "run", agentArg, "run", "--format", "json", "-m", modelSpec, m.TaskPrompt)
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
	// Snapshot the agent's stdout BEFORE any scanner reads it.
	// extractAgentOutput and extractStreamMetrics both consume the
	// bytes via bufio.Scanner; a *bytes.Buffer's read offset advances
	// to EOF on the first scan, so the second scanner would see
	// no bytes. Snapshotting once keeps both extractions faithful to
	// the captured stream.
	agentStdoutBytes := agentStdout.Bytes()
	streamMetrics := extractStreamMetrics(agentStdoutBytes)
	// Per D17: OpenCode 1.17.8 step_finish carries tokens/cost on the
	// stream's step-aggregate event, NOT on the tool_call rows the
	// wrapper persists from tool_use events. SummarizeBenchToolCalls
	// returns zeros / NULL in that gap; streamMetrics — when it has
	// seen any step_finish events — is the authoritative fallback
	// for filling the bench_runs row's tokens/cost fields.
	if streamMetrics.hasData {
		if tokensIn == 0 && streamMetrics.tokensIn > 0 {
			tokensIn = streamMetrics.tokensIn
		}
		if tokensOut == 0 && streamMetrics.tokensOut > 0 {
			tokensOut = streamMetrics.tokensOut
		}
		// streamMetrics.cost is the provider-reported cost on the
		// stream (D17 L3 branch (a)); it wins over SummarizeBenchToolCalls'
		// nil result. A non-nil provider cost does NOT override a
		// non-nil tool_calls-aggregated cost — the per-row evidence
		// is the stronger signal when present.
		if sumCost == nil && streamMetrics.cost != nil {
			v := *streamMetrics.cost
			sumCost = &v
		}
	}
	// Prefer the wrapper's tool_calls values when non-empty; fall
	// back to the methodology-pinned model and the user-supplied
	// agent name. The bench_runs row is the headline record, not a
	// re-summary of the tool_calls.
	if aggModel == "" {
		if streamMetrics.model != "" {
			aggModel = streamMetrics.model
		} else {
			aggModel = m.ModelID
		}
	}
	if aggProvider == "" {
		if streamMetrics.provider != "" {
			aggProvider = streamMetrics.provider
		} else {
			aggProvider = m.ModelProvider
		}
	}
	_ = agentName // agentName is the wrapper's session.agent; not needed for the bench row.

	// Score the agent's output. v1 = exact_match against expected.
	actualOutput := extractAgentOutput(agentStdoutBytes)
	score := scoreExactMatch(actualOutput, m.TaskExpectedOutput)

	// Build the BenchRun. The Cost pointer is populated from the
	// SUM(cost) of the underlying tool_calls when present; otherwise
	// it stays nil and deriveBenchCost fills it from the cache at
	// write time (D11 AC4).
	br := &dbpkg.BenchRun{
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

	id, err := dbpkg.NewID()
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
		"rows":               []dbpkg.BenchRun{persisted},
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
		case dbpkg.ErrBenchMethodologyNotFound:
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
func findWrapperSession(db *dbpkg.DB, agentArg string, wrapperStart time.Time) (sessionID, agentName string, err error) {
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
//     For OpenCode 1.17.8 the answer lives at part.text on the final
//     `text` event (see .agentera/verification/fixtures/opencode-tool-use.jsonl
//     line 5: {"type":"text",...,"part":{"type":"text","text":...}}).
//     The earlier top-level `content` alias is preserved as a
//     fallback for the flat-fallback shape and for tests that
//     pre-date the OpenCode fixture.
//  2. Plain text: each line is just text. The last non-empty line
//     is the agent's response.
//
// The agent's response is then compared against the methodology's
// expected_output via exact_match scoring (v1).
//
// The input is a []byte snapshot taken before any scanner reads
// the buffer; once a bufio.Scanner has advanced a *bytes.Buffer's
// read offset to EOF, a second pass over the same buffer would see
// no bytes. The caller therefore snapshots agentStdout.Bytes() once
// and passes the snapshot to both extractAgentOutput and
// extractStreamMetrics so neither call invalidates the other.
func extractAgentOutput(stdout []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
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
				// OpenCode nests the answer under part.text;
				// the flat-fallback shape uses top-level content.
				var content string
				if raw := probe["content"]; len(raw) > 0 {
					_ = json.Unmarshal(raw, &content)
				}
				if content == "" && len(probe["part"]) > 0 {
					var part map[string]json.RawMessage
					if err := json.Unmarshal(probe["part"], &part); err == nil {
						for _, k := range []string{"text", "content"} {
							if raw := part[k]; len(raw) > 0 {
								if err := json.Unmarshal(raw, &content); err == nil && content != "" {
									break
								}
							}
						}
					}
				}
				if content != "" {
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

// streamMetrics carries per-step-enrichment evidence extracted from
// the captured JSONL stream that `ktx run` teed to bench run's
// captured agent stdout. Per Decision 17: OpenCode 1.17.8 emits
// tokens_in/tokens_out/cost/model on the step_finish event (a
// step-aggregate), not on tool_use (per-tool). The wrapper persists
// only tool_use events as tool_call rows, so those rows are
// NULL-by-design against real OpenCode (see parser.go:150-158 and
// .agentera/verification/m2-acceptance.md §3 'Surprises'). This
// struct surfaces the stream-side evidence the D17-amended L2/L3
// contract produced; bench run uses it to populate bench_runs row
// fields that would otherwise stay 0/NULL via SummarizeBenchToolCalls.
type streamMetrics struct {
	tokensIn  int64
	tokensOut int64
	cost      *float64
	model     string
	provider  string
	hasData   bool
}

// extractStreamMetrics parses the captured JSONL stream looking for
// step_finish events (the OpenCode 1.17.8 step-aggregate). Returns
// a streamMetrics populated with the SUM of tokens_in / tokens_out
// over all step_finish lines; cost is the MAX provider-reported
// part.cost across step_finish lines (matches the provider-cost
// branch (a) of D17-amended L3); model is the methodology-pinned
// model id when the stream does not carry a model identifier (the
// step_finish event has no model field today, only part.cost and
// part.tokens). provider is derived per the OpenCode runtime-known
// default ("opencode") unless explicitly present on a step_finish
// event (the parser also defaults to "opencode" for the same reason;
// see parser.go:226-229).
//
// When the stream carries no step_finish events (e.g., a flat-
// fallback runtime that emits only tool calls with tokens on
// each), streamMetrics.hasData=false and the caller falls through
// to the tool_calls-based aggregation path.
//
// The input is a []byte snapshot taken before any scanner reads the
// buffer (see extractAgentOutput's doc for why — once a bufio.
// Scanner consumes a *bytes.Buffer, the read offset is at EOF).
func extractStreamMetrics(stdout []byte) streamMetrics {
	var sm streamMetrics
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(line, &top); err != nil {
			continue
		}
		var eventType string
		_ = json.Unmarshal(top["type"], &eventType)
		if eventType != "step_finish" {
			continue
		}
		// part.tokens.input / .output is the OpenCode 1.17.8 shape.
		var part map[string]json.RawMessage
		if err := json.Unmarshal(top["part"], &part); err != nil {
			continue
		}
		sm.hasData = true
		var tk struct {
			Input  json.RawMessage `json:"input"`
			Output json.RawMessage `json:"output"`
		}
		_ = json.Unmarshal(part["tokens"], &tk)
		if n, ok := jsonNumberToInt64(tk.Input); ok {
			sm.tokensIn += n
		}
		if n, ok := jsonNumberToInt64(tk.Output); ok {
			sm.tokensOut += n
		}
		// part.cost: provider-reported cost in the stream (D17 L3 branch (a)).
		// MAX across events keeps the largest reported cost (the
		// last step_finish usually carries the total). cost=0 is a
		// valid provider-reported value (e.g., cache hits) and is
		// not an absent value; absent stays nil.
		if raw := part["cost"]; len(raw) > 0 {
			var c float64
			if err := json.Unmarshal(raw, &c); err == nil {
				if sm.cost == nil || c > *sm.cost {
					v := c
					sm.cost = &v
				}
			}
		}
		if raw := part["model"]; len(raw) > 0 {
			var m string
			if err := json.Unmarshal(raw, &m); err == nil && m != "" && sm.model == "" {
				sm.model = m
			}
		}
		if raw := part["provider"]; len(raw) > 0 {
			var p string
			if err := json.Unmarshal(raw, &p); err == nil && p != "" && sm.provider == "" {
				sm.provider = p
			}
		}
	}
	return sm
}

// jsonNumberToInt64 coerces a json.RawMessage that's expected to be
// numeric into an int64. JSON numbers can decode as float64 via the
// default UnmarshalJSON; int64 values from OpenCode step_finish
// tokens round-trip cleanly when the source emits them without
// a fractional component (real OpenCode 1.17.8 emits integer
// token counts). Returns (0, false) on parsing failure or non-
// numeric input; the caller treats false as 'field absent'.
func jsonNumberToInt64(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var i int64
	if err := json.Unmarshal(raw, &i); err == nil {
		return i, true
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f), true
	}
	return 0, false
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
	fmt.Fprintln(w, "  report [<id>] [--methodology <version>] [--format text|json]")
	fmt.Fprintln(w, "      Emit the headline from persisted bench_runs rows.")
	fmt.Fprintln(w, "      Without <id>: list all rows in insertion order (oldest first);")
	fmt.Fprintln(w, "        headline is the methodology-defined aggregate across rows.")
	fmt.Fprintln(w, "      With <id>: emit a single row by D3 nanoid; not-found → exit 2.")
	fmt.Fprintln(w, "      --methodology filters rows by methodology_version; the")
	fmt.Fprintln(w, "        headline is recomputed from the filtered set.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Methodology file: .agentera/bench/methodology-v1.yaml (D12). The")
	fmt.Fprintln(w, "CLI inputs (agent, model, task) must match the methodology's")
	fmt.Fprintln(w, "pinned triple; a drift is a usage error so the headline matches")
	fmt.Fprintln(w, "the published methodology (D12 reproducibility contract).")
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
