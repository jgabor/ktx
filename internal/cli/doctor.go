// `ktx doctor` — read-only health check for the ktx installation. It
// verifies the properties that block every other ktx command:
//
//  1. The database file exists at the resolved path.
//  2. The database is openable (sqlite3 file is valid; not a directory,
//     not a corrupt blob).
//  3. The schema matches the expected shape: `sessions` and `tool_calls`
//     tables exist with the expected base columns. Hard requirement — a
//     missing column is an error because record I/O cannot function.
//  4. The cost-publication columns are present on `tool_calls`.
//     Soft requirement — a missing cost column is a warning because the
//     system still functions; records load, cost fields stay NULL on
//     insert. The next ktx invocation applies the in-place migration.
//  5. The `bench_runs` table exists with all 14 expected columns.
//     Soft requirement — a missing or partial `bench_runs`
//     is a warning because the system still functions; records
//     are queryable, `ktx bench run` is the only feature that
//     requires the table. The next ktx invocation applies the in-place
//     CREATE / ALTER migration.
//  6. The ID format is valid: sampled IDs from the database match the
//     expected shape (16-char lowercase alpha).
//
// Doctor is read-only. It does not create the database if it is missing —
// that is the user's signal to run `ktx run` / `ktx write` for the first
// time, or to investigate a config issue. It does not migrate, prune, or
// repair the database.
//
// Output contract:
//
//	{
//	  "status": "ok|degraded|error",
//	  "path":   "<resolved database path>",
//	  "checks": [
//	    {"name": "...", "status": "ok|warning|error", "detail": "..."},
//	    ...
//	  ]
//	}
//
// Top-level status is "degraded" when at least one check is "warning"
// and no check is "error" (the pre-migration case); "ok" when every
// check passed; "error" when at least one hard check failed.
//
// Exit codes:
//
//	0  status: ok or degraded (records still queryable)
//	1  usage error (invalid flag, unexpected argument)
//	4  internal error (status == "error")
//
// Path resolution mirrors the other ktx commands: KTX_DB env > default
// ($XDG_DATA_HOME/ktx/ktx.db). Per-invocation override lives on
// `ktx db --path`; doctor does not accept its own --path.
//
// Doctor opens the database itself because it must inspect missing and
// unmigrated files without changing them.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// doctorSampleLimit bounds how many IDs doctor inspects. The check is
// "spot check, not exhaustive scan": a 100-row sample catches
// corruption from manual edits or schema mismatches quickly without
// O(n) work over a large production database. The 101st row being
// malformed is exceedingly rare and would surface on the next doctor
// invocation once records accumulate.
const doctorSampleLimit = 100

// doctorStatus values. These are the canonical per-check and top-level
// status values exposed in the JSON output. The schema migration
// introduces "warning" and "degraded":
//
//   - "warning" is a per-check status: the database still functions
//     (records are queryable), but a soft requirement is unmet (e.g.
//     cost columns are missing on an unmigrated database).
//   - "degraded" is a top-level status: at least one check is "warning"
//     and no check is "error". The CLI exits 0 because the system still
//     works — degraded is informational, not a hard failure.
//
// Hard failure ("error" at any check, or any check failing its hard
// requirement) maps to top-level "error" and the standard ExitInternal
// (4) exit code.
const (
	doctorStatusOK       = "ok"
	doctorStatusWarning  = "warning"
	doctorStatusError    = "error"
	doctorStatusDegraded = "degraded"
)

// doctorCheck is one entry in the report's `checks` array.
type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// doctorReport is the full structured output of `ktx doctor`. JSON
// shape mirrors the diagnostic contract: status + per-check
// breakdown + the resolved path so the user can audit which database
// was actually inspected.
type doctorReport struct {
	Status string        `json:"status"`
	Path   string        `json:"path"`
	Checks []doctorCheck `json:"checks"`
}

// runDoctorWith executes `ktx doctor` with injected output streams.
// stdout/stderr are injected so the report can be rendered without
// touching os.Stdout. The body parses --format, builds the doctor
// report, renders it, and converts the top-level status into the
// right exit code (4 for error, 0 for ok/degraded).
func runDoctorWith(args []string, opts Options, stdout, stderr io.Writer) int {
	_, code := doctorLogic(args, opts.Format, stdout, stderr)
	return code
}

// doctorLogic builds and renders the health report, returning its CLI status.
//
// The verb's own flag set parses --help/-h. The subcommand-local
// --format extraction is delegated to extractFormat (the helper also
// used by ktx get / ktx list / ktx bench) so --format works in
// either flag position. The verb's body emits the JSON envelope
// directly to stdout (PassthroughStdout=true on the dispatch
// registry).
func doctorLogic(args []string, format string, stdout, stderr io.Writer) (any, int) {
	// Subcommand-local --format. extractFormat accepts both --format
	// value and --format=value forms; the subcommand's value takes
	// precedence over the global one when both are set.
	extracted, rest := extractFormat(args)
	if extracted != "" {
		format = extracted
	}
	if format != FormatJSON && format != FormatText {
		printError(stderr, usageError("ktx doctor: invalid --format %q: want %q or %q", format, FormatJSON, FormatText), format)
		return nil, ExitUsage
	}

	var showHelp bool
	fs := flag.NewFlagSet("ktx doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(rest); err != nil {
		printError(stderr, usageError("ktx doctor: %v", err), format)
		return nil, ExitUsage
	}
	if showHelp {
		printDoctorHelp(stdout)
		return nil, ExitOK
	}
	if len(fs.Args()) > 0 {
		printError(stderr, usageError("ktx doctor: unexpected argument %q", fs.Arg(0)), format)
		return nil, ExitUsage
	}

	report := buildDoctorReport()

	switch format {
	case FormatText:
		printDoctorText(stdout, report)
	default:
		// JSON default. json.NewEncoder writes a trailing
		// newline so stdout stays pipe-friendly.
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			printError(stderr, internalError("ktx doctor: write JSON: %v", err), format)
			return nil, ExitInternal
		}
	}

	if report.Status == doctorStatusError {
		// A failing check maps to ExitInternal (4). The choice between
		// ExitInternal (4) and ExitUsage (1) is between "the system
		// is broken" and "the user typed something wrong"; a failing
		// check represents a system failure, not invalid user input.
		return nil, ExitInternal
	}
	// "ok" and "degraded" both exit 0: degraded means a soft warning
	// (e.g. cost columns missing on an unmigrated database) that does not
	// prevent the system from working. Records still query, cost
	// fields stay NULL on insert, and the next ktx invocation applies
	// the migration. Returning a non-zero exit would break that
	// "doctor says it works" signal for CI scripts and shell wrappers
	// that already invoke `ktx run` / `ktx write` to fix the issue.
	return nil, ExitOK
}

// doctorCommandFunc is the dispatch wrapper. The dispatcher already
// parsed --help/-h (via the Help hook) and extracted --format; the
// verb receives a *DispatchContext carrying a clean positional Args
// tail and the resolved format. The verb's only responsibility is to
// apply doctorLogic and surface the appropriate typed error to the
// dispatcher's exitCodeOf pipeline.
//
// PassthroughStdout=true: the verb writes its structured JSON
// envelope directly to ctx.Stdout (the dispatcher's printError path
// is for stderr; PassthroughStdout means the dispatcher does not
// impose any envelope on stdout).
//
// NeedsDB=false: the verb opens its own database via the existing
// buildDoctorReport helper. The dispatcher's dbpkg.ResolvePath +
// dbpkg.Open calls are skipped — doctor's read-only semantics don't
// fit the dispatcher's "always open the DB if NeedsDB" pattern
// because doctor must inspect a database that may not yet exist
// (the database_exists check is stat-only).
func doctorCommandFunc(ctx *DispatchContext) (any, error) {
	_, code := doctorLogic(ctx.Args, ctx.Format, ctx.Stdout, ctx.Stderr)
	switch code {
	case ExitOK:
		return nil, nil
	case ExitUsage:
		return nil, usageError("ktx doctor: usage error")
	case ExitInternal:
		return nil, internalError("ktx doctor: status is error")
	default:
		return nil, internalError("ktx doctor: exit code %d", code)
	}
}

// init registers `ktx doctor` with the dispatch module. After init
// runs, the verb is reachable through runWith.
func init() {
	Register("doctor", RegisterOptions{
		Description:       "check ktx health",
		NeedsDB:           false,
		PassthroughStdout: true,
		FormatDefault:     FormatJSON,
		Help:              printDoctorHelp,
	}, doctorCommandFunc)
}

// buildDoctorReport runs the checks in order and assembles the
// structured report. Each check is append-only — a failed earlier check
// does not short-circuit the later ones; we want the user to see the
// full picture so they can fix everything in one pass. The path
// resolution check is the only exception: if we cannot determine where
// the database should live, the rest of the checks have nothing to
// inspect, so the report returns early with a single error check.
//
// Top-level status precedence (most severe wins):
//  1. any check "error"    → top-level "error"    (exit ExitInternal)
//  2. any check "warning"  → top-level "degraded" (exit ExitOK — system still works)
//  3. all checks "ok"      → top-level "ok"       (exit ExitOK)
//
// The "degraded" branch is the schema migration's user-visible
// surface: an unmigrated database has every base column (so schema_valid is
// ok) but missing cost columns (so m2_columns is warning). The system is
// still functional — records load, cost fields stay NULL — but the
// user benefits from a clear signal that the migration has not yet
// run.
func buildDoctorReport() *doctorReport {
	report := &doctorReport{}

	path, err := dbpkg.ResolvePath("")
	if err != nil {
		report.Status = doctorStatusError
		report.Checks = append(report.Checks, doctorCheck{
			Name:   "database_path",
			Status: doctorStatusError,
			Detail: fmt.Sprintf("resolve database path: %v", err),
		})
		return report
	}
	report.Path = path

	// Check 1: file exists. Read-only (does not call dbpkg.Open which
	// would create the file as a side effect). A missing file is a
	// configuration signal — the user has not recorded anything yet,
	// or KTX_DB points at a non-existent path.
	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			report.Checks = append(report.Checks, doctorCheck{
				Name:   "database_exists",
				Status: doctorStatusError,
				Detail: fmt.Sprintf("database file %q does not exist (run `ktx run` or `ktx write` to create it)", path),
			})
		} else {
			report.Checks = append(report.Checks, doctorCheck{
				Name:   "database_exists",
				Status: doctorStatusError,
				Detail: fmt.Sprintf("stat %q: %v", path, statErr),
			})
		}
		report.Status = doctorStatusError
		return report
	}
	report.Checks = append(report.Checks, doctorCheck{
		Name:   "database_exists",
		Status: doctorStatusOK,
		Detail: fmt.Sprintf("file present at %q", path),
	})

	// Check 2: openable. dbpkg.Open runs migrations on first open, but
	// the file already exists at this point so the migrations are
	// no-ops (CREATE TABLE IF NOT EXISTS is idempotent; the
	// ALTER TABLE step is column-existence-guarded so a re-open is a
	// pure no-op on a fully migrated database).
	db, err := dbpkg.Open(path)
	if err != nil {
		report.Checks = append(report.Checks, doctorCheck{
			Name:   "database_openable",
			Status: doctorStatusError,
			Detail: fmt.Sprintf("open %q: %v", path, err),
		})
		report.Status = doctorStatusError
		return report
	}
	defer db.Close()
	report.Checks = append(report.Checks, doctorCheck{
		Name:   "database_openable",
		Status: doctorStatusOK,
		Detail: fmt.Sprintf("opened %q", path),
	})

	// Check 3: schema. Expected base tables + columns match the expected shape.
	// This is the hard requirement — a missing column is a hard
	// error because record I/O cannot function without it. The cost
	// columns are checked separately in checkCostColumns below.
	if err := db.CheckSchema(); err != nil {
		report.Checks = append(report.Checks, doctorCheck{
			Name:   "schema_valid",
			Status: doctorStatusError,
			Detail: fmt.Sprintf("%v", err),
		})
		report.Status = doctorStatusError
	} else {
		report.Checks = append(report.Checks, doctorCheck{
			Name:   "schema_valid",
			Status: doctorStatusOK,
			Detail: "sessions and tool_calls tables present with expected base columns",
		})
	}

	// Check 3b: cost columns. Soft requirement — a missing cost column
	// means the database is unmigrated, not that the system is broken.
	// Records are still queryable; cost fields stay NULL on insert.
	// Status: "ok" on migrated DB; "warning" on an unmigrated DB. The
	// detail lists the absent columns by name so the user can verify
	// the migration ran, or hand-fix a partial set.
	if m2Check := checkCostColumns(db); m2Check.Status == doctorStatusError {
		report.Checks = append(report.Checks, m2Check)
		report.Status = doctorStatusError
	} else {
		report.Checks = append(report.Checks, m2Check)
		if m2Check.Status == doctorStatusWarning && report.Status != doctorStatusError {
			report.Status = doctorStatusDegraded
		}
	}

	// Check 3c: bench_runs table + columns. Soft
	// requirement — a missing bench_runs table means the database
	// lacks the table, not that the system is broken. Records are still
	// queryable; only `ktx bench run` requires the table. Status:
	// "ok" on migrated DB or fresh DB (all 14 columns present);
	// "warning" when the table is absent or partial bench_runs
	// (some columns missing — forward-fix on next open).
	if brCheck := checkBenchRuns(db); brCheck.Status == doctorStatusError {
		report.Checks = append(report.Checks, brCheck)
		report.Status = doctorStatusError
	} else {
		report.Checks = append(report.Checks, brCheck)
		if brCheck.Status == doctorStatusWarning && report.Status != doctorStatusError {
			report.Status = doctorStatusDegraded
		}
	}

	// Check 4: ID format. Sample up to doctorSampleLimit IDs from each
	// table and validate against the expected ID regex. An empty database
	// passes vacuously (no IDs to check); a database with malformed
	// IDs fails with the first offending ID surfaced in the detail.
	if idCheck := checkIDFormat(db); idCheck.Status != doctorStatusOK {
		report.Checks = append(report.Checks, idCheck)
		if report.Status != doctorStatusError {
			report.Status = doctorStatusError
		}
	} else {
		report.Checks = append(report.Checks, idCheck)
	}

	if report.Status == "" {
		report.Status = doctorStatusOK
	}
	return report
}

// checkCostColumns probes the tool_calls cost column set via HasCostColumns.
// Returns:
//
//   - "ok" with detail "all cost columns present: tokens_in, tokens_out,
//     cost, latency_ms, model, provider" when every costSchema column
//     exists on tool_calls (post-migration or fresh DB).
//   - "warning" with detail naming the missing columns when one or more
//     are absent (unmigrated DB or partial set). The detail notes that
//     records are still queryable so the user understands the
//     warning is informational, not a hard failure.
//   - "error" only when the underlying table_info query fails (e.g.,
//     tool_calls itself is missing — that is a separate hard error
//     reported in schema_valid, but we surface it here as well so the
//     per-check trace is complete).
func checkCostColumns(db *dbpkg.DB) doctorCheck {
	ok, missing, err := db.HasCostColumns()
	if err != nil {
		return doctorCheck{
			Name:   "cost_columns",
			Status: doctorStatusError,
			Detail: fmt.Sprintf("%v", err),
		}
	}
	if ok {
		return doctorCheck{
			Name:   "cost_columns",
			Status: doctorStatusOK,
			Detail: "all cost columns present on tool_calls: tokens_in, tokens_out, cost, latency_ms, model, provider",
		}
	}
	return doctorCheck{
		Name:   "cost_columns",
		Status: doctorStatusWarning,
		Detail: fmt.Sprintf("missing cost columns on tool_calls: %s (records still queryable; cost fields will be NULL on insert — run any ktx command to apply the migration)", strings.Join(missing, ", ")),
	}
}

// checkBenchRuns probes the bench_runs table and its columns
// via HasBenchRunsTable + HasBenchRunsColumns. Returns:
//
//   - "ok" with detail "all bench_runs columns present: id, agent,
//     model, task, score, tokens_in, tokens_out, cost,
//     methodology_version, timestamp, cache_read, cache_write,
//     reasoning, estimated_api_cost" when
//     the table exists and every expected column is present
//     (post-migration or fresh DB).
//   - "warning" with detail naming the absent columns when the table
//     exists but some columns are missing (partial bench_runs — the
//     migration forward-fixes on next open).
//   - "warning" with detail noting the missing table when the table is
//     absent entirely. Records are still queryable;
//     `ktx bench run` is the only feature that requires the table, and
//     the migration creates it on next open.
//   - "error" only when the underlying sqlite_master / table_info
//     queries fail (a corrupt schema view — a separate problem from a
//     missing or partial bench_runs).
//
// The two-step probe (table then columns) lets the detail message
// distinguish "table missing entirely" from "table present
// but partial — forward-fix" — they have different remediation paths
// even though both surface as warnings.
func checkBenchRuns(db *dbpkg.DB) doctorCheck {
	hasTable, err := db.HasBenchRunsTable()
	if err != nil {
		return doctorCheck{
			Name:   "bench_runs_table",
			Status: doctorStatusError,
			Detail: fmt.Sprintf("%v", err),
		}
	}
	if !hasTable {
		return doctorCheck{
			Name:   "bench_runs_table",
			Status: doctorStatusWarning,
			Detail: "bench_runs table is missing (records still queryable — run any ktx command to apply the migration)",
		}
	}
	missing, err := db.HasBenchRunsColumns()
	if err != nil {
		return doctorCheck{
			Name:   "bench_runs_table",
			Status: doctorStatusError,
			Detail: fmt.Sprintf("%v", err),
		}
	}
	if len(missing) > 0 {
		return doctorCheck{
			Name:   "bench_runs_table",
			Status: doctorStatusWarning,
			Detail: fmt.Sprintf("partial bench_runs schema; missing columns: %s (forward-fixed on next open)", strings.Join(missing, ", ")),
		}
	}
	return doctorCheck{
		Name:   "bench_runs_table",
		Status: doctorStatusOK,
		Detail: "all bench_runs columns present: id, agent, model, task, score, tokens_in, tokens_out, cost, methodology_version, timestamp, cache_read, cache_write, reasoning, estimated_api_cost",
	}
}

// checkIDFormat samples IDs from sessions and tool_calls and verifies
// each one matches the expected ID format. The check returns ok with detail
// "sampled N+M IDs" when no IDs are present (vacuously true) or when
// every sampled ID matches the regex. The first malformed ID is
// surfaced in the detail so the user can grep for it.
func checkIDFormat(db *dbpkg.DB) doctorCheck {
	sessionIDs, toolIDs, err := db.SampleIDs(doctorSampleLimit)
	if err != nil {
		return doctorCheck{
			Name:   "id_format",
			Status: doctorStatusError,
			Detail: fmt.Sprintf("sample ids: %v", err),
		}
	}
	total := len(sessionIDs) + len(toolIDs)
	for _, id := range sessionIDs {
		if !dbpkg.IDPattern.MatchString(id) {
			return doctorCheck{
				Name:   "id_format",
				Status: doctorStatusError,
				Detail: fmt.Sprintf("session id %q does not match the expected format (16-char lowercase alpha)", id),
			}
		}
	}
	for _, id := range toolIDs {
		if !dbpkg.IDPattern.MatchString(id) {
			return doctorCheck{
				Name:   "id_format",
				Status: doctorStatusError,
				Detail: fmt.Sprintf("tool_call id %q does not match the expected format (16-char lowercase alpha)", id),
			}
		}
	}
	if total == 0 {
		return doctorCheck{
			Name:   "id_format",
			Status: doctorStatusOK,
			Detail: "no ids sampled (database is empty)",
		}
	}
	return doctorCheck{
		Name:   "id_format",
		Status: doctorStatusOK,
		Detail: fmt.Sprintf("sampled %d session ids and %d tool_call ids; all match the expected format", len(sessionIDs), len(toolIDs)),
	}
}

// printDoctorText renders the doctor report in a human-readable form.
// One line per check plus a status summary. Mirrors the verbose-style
// of other ktx text outputs (`ktx list --format text`, `ktx get --format
// text`) so the surface stays consistent.
//
// Per-check status markers:
//
//   - [OK]    — the check passed
//   - [WARN]  — a soft requirement is unmet (e.g. cost columns missing on
//     an unmigrated database); the system still works
//   - [FAIL]  — a hard requirement failed; the system cannot continue
//
// The top-level status line uses the raw status string ("ok",
// "degraded", "error") so a glance at the first line tells the user
// what shape of problem they are looking at.
func printDoctorText(w io.Writer, report *doctorReport) {
	fmt.Fprintf(w, "ktx doctor — %s\n", report.Status)
	if report.Path != "" {
		fmt.Fprintf(w, "path: %s\n", report.Path)
	}
	fmt.Fprintln(w, "checks:")
	for _, c := range report.Checks {
		marker := "[OK]"
		switch c.Status {
		case doctorStatusWarning:
			marker = "[WARN]"
		case doctorStatusError:
			marker = "[FAIL]"
		}
		fmt.Fprintf(w, "  %s %s: %s\n", marker, c.Name, c.Detail)
	}
}

// printDoctorHelp writes the `ktx doctor --help` text. Mirrors the
// help style of the other ktx commands.
func printDoctorHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx doctor — check ktx health (read-only)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx doctor [--format text|json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Runs a read-only health check on the ktx installation. Verifies:")
	fmt.Fprintln(w, "  - the database file exists at the resolved path")
	fmt.Fprintln(w, "  - the database is openable")
	fmt.Fprintln(w, "  - the schema matches the expected shape (sessions + tool_calls tables, hard requirement)")
	fmt.Fprintln(w, "  - the cost-publication columns are present on tool_calls (soft requirement: warning if missing)")
	fmt.Fprintln(w, "  - the bench_runs table is present with all 14 columns (soft requirement: warning if missing or partial)")
	fmt.Fprintln(w, "  - sampled IDs match the expected format (16-char lowercase alpha)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  structured report on stdout with status + per-check detail")
	fmt.Fprintln(w, "  text            one-line-per-check summary suitable for terminals")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Status values:")
	fmt.Fprintln(w, "  ok        all checks passed")
	fmt.Fprintln(w, "  degraded  one or more checks are warnings (e.g. cost columns missing on")
	fmt.Fprintln(w, "            an unmigrated database, or bench_runs table missing);")
	fmt.Fprintln(w, "            the system still works")
	fmt.Fprintln(w, "  error     one or more checks failed; the system cannot continue")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  status: ok or degraded (records still queryable)")
	fmt.Fprintln(w, "  1  usage error (invalid flag, unexpected argument)")
	fmt.Fprintln(w, "  4  status: error (one or more hard checks failed)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Doctor is read-only. It does not create the database if it is missing,")
	fmt.Fprintln(w, "does not migrate, and does not repair. Use 'ktx run' or 'ktx write' to")
	fmt.Fprintln(w, "create a new database; use 'ktx db prune' (when implemented) for cleanup.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
