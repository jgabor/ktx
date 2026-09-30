package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/experiment"
	methodologypkg "github.com/jgabor/ktx/internal/methodology"
)

// runBenchReport renders persisted benchmark results. Three invocation
// modes are supported:
//
//	ktx bench report                      — list all rows in insertion
//	                                       order (oldest first); emit
//	                                       top-level headline + rows
//	                                       array (JSON default) or
//	                                       one-line summary + tabwriter
//	                                       table (--format text)
//	ktx bench report <id>                 — single-row lookup by
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
// It parses report flags, opens the database, and maps errors to CLI exit
// codes. runBenchReportImpl owns query selection and rendering.
//
// Exit codes:
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
	var specFilter string
	fs.StringVar(&specFilter, "spec", "", "filter rows by registered benchmark spec ID")
	currentCost := fs.Bool("current-cost", false, "use a current cost envelope for a single row")
	if err := fs.Parse(rest); err != nil {
		printError(stderr, usageError("ktx bench report: %v", err), format)
		return ExitUsage
	}

	if fs.NArg() > 1 {
		printError(stderr, usageError("ktx bench report: unexpected argument %q; report takes a single optional <id>", fs.Arg(1)), format)
		return ExitUsage
	}
	methodologyFilter := ""
	if specFilter != "" {
		spec, _, err := methodologypkg.Load(specFilter)
		if err != nil {
			printError(stderr, usageError("ktx bench report: unknown spec ID %q; use a registered ID such as exact-match", specFilter), format)
			return ExitUsage
		}
		methodologyFilter = spec.Version
	}

	// Prepare the selected database only after validating the request, then
	// open a read-only query handle.
	target, err := dbpkg.ResolveTarget("")
	if err != nil {
		printError(stderr, internalError("ktx bench report: resolve database path: %v", err), format)
		return ExitInternal
	}
	db, err := openQueryDatabase(target, "bench")
	if err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
	}
	defer db.Close()

	var reportErr error
	if *currentCost && fs.NArg() == 1 {
		row, err := db.QueryBenchRun(fs.Arg(0))
		if errors.Is(err, dbpkg.ErrNotFound) {
			reportErr = notFoundError("ktx bench report: no bench_run with id '%s'", fs.Arg(0))
		} else if err != nil {
			reportErr = internalError("ktx bench report: query bench_run: %v", err)
		} else {
			selection, selected := benchRowCostView([]dbpkg.BenchRun{row})
			if format == FormatText {
				reportErr = renderBenchRunSingleText(stdout, row)
				renderBenchCostViewText(stdout, []dbpkg.BenchRun{row}, selection, selected)
			} else {
				reportErr = json.NewEncoder(stdout).Encode(map[string]any{"row": row, "cost_selection": selection, "selected_cost_per_correct": selected})
			}
		}
	} else {
		reportErr = runBenchReportImpl(stdout, stderr, db, fs.Args(), format, methodologyFilter)
	}
	if err := reportErr; err != nil {
		printError(stderr, err, format)
		return exitCodeOf(err)
	}
	return ExitOK
}

// runBenchReportImpl queries and renders benchmark results using an open
// database. An empty methodology filter includes every methodology.
//
// The three invocation modes are dispatched on the presence of a
// positional arg:
//
//	[]                   → list mode: all rows (or --methodology-filtered)
//	[id]                 → single-row mode: QueryBenchRun by id
//	[id1, id2, ...]      → usage error (single-row only takes one id)
//
// Pointer discipline preserved end-to-end: BenchRun.Cost is *float64 so
// the absent-vs-zero distinction survives JSON round-trips. The text
// renderer maps a nil Cost to "-" (matches the ktx list --format text
// convention for absent fields).
func runBenchReportImpl(
	stdout, stderr io.Writer,
	db *dbpkg.DB,
	args []string,
	format string,
	methodologyFilter string,
) error {
	_ = stderr // stderr is reserved for the dispatcher adapter's error envelope; unused in the success path.

	// Single-row mode: positional <id>. The contract
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

	// List / filtered mode: load all rows in insertion order (the
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
// emits the BenchRun directly (no headline envelope); the
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
// contract:
//
//	{
//	  "headline": <number|null>,
//	  "methodology_version": <string|null>,
//	  "rows": [BenchRun, ...]
//	}
//
// The headline is the multi-row aggregate (SUM(cost) / max(SUM(score),
// 1e-9)) or null when SUM(cost) is NULL (every row's cost is
// NULL). methodology_version prefers the first row's value (the data's
// own statement); when no rows exist, it falls back to the filter
// string so a `--methodology v1` invocation with zero matching rows
// still surfaces "we filtered for v1" in the envelope. An empty
// bench_runs table with no filter renders methodology_version as null
// (the absence-marker contract).
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
	selection, selected := benchRowCostView(rows)
	envelope["cost_selection"], envelope["selected_cost_per_correct"] = selection, selected
	return json.NewEncoder(stdout).Encode(envelope)
}

// renderBenchReportText writes the list-mode text output: a one-line
// headline summary with the methodology reference visible (per the
// spec example), a horizontal rule, and a tabwriter-aligned
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
	selection, selected := benchRowCostView(rows)
	renderBenchCostViewText(w, rows, selection, selected)

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

func benchRowCostView(rows []dbpkg.BenchRun) (experiment.CostSelection, any) {
	facts := make([]experiment.CostObservation, 0, len(rows))
	for _, row := range rows {
		facts = append(facts, experiment.ReportedCostObservation(row.ID, row.Cost, "bench_runs.cost"))
	}
	selection := experiment.SelectCostCohort(facts)
	var selected any
	if selection.Basis == "reported" {
		value, _ := computeAggregateHeadline(rows)
		selected = value
	}
	return selection, selected
}

func renderBenchCostViewText(w io.Writer, rows []dbpkg.BenchRun, selection experiment.CostSelection, selected any) {
	fmt.Fprintln(w, experiment.RenderCostSelection(selection))
	value := "-"
	if selected != nil {
		value = fmt.Sprintf("%.6f", selected)
	}
	fmt.Fprintf(w, "selected_cost_per_correct: %s USD/correct\n", value)
	for _, row := range rows {
		fmt.Fprintf(w, "source_cost id=%s cost=%s estimated_api_cost=%s (stored, not retained-pricing-qualified)\n", row.ID, storedCostText(row.Cost), storedCostText(row.EstimatedAPICost))
	}
}

func storedCostText(value *float64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%.6f", *value)
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
// methodology's `computation:` block:
//
//	headline = SUM(cost) / max(SUM(score), 1.0e-9)
//
// Returns (headline, ok) where ok=false signals the headline is null
// (every row's Cost pointer is nil — there is no cost to aggregate,
// so the headline is meaningless). The 1e-9 epsilon guards against
// division by zero when SUM(score)=0.
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
// JSON path emits the integer as-is (the reproducibility contract
// keeps the timestamp machine-readable; RFC3339Nano is for humans).
// A zero or negative timestamp renders as "-" (matches the cost
// pointer discipline: nil Cost → "-"; zero timestamp → "-").
func formatBenchTimestamp(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339Nano)
}
