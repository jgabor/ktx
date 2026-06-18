// `ktx doctor` — read-only health check for the ktx installation (Decision 2,
// Task 8). It verifies the three properties that block every other ktx
// command:
//
//  1. The database file exists at the resolved path.
//  2. The database is openable (sqlite3 file is valid; not a directory,
//     not a corrupt blob).
//  3. The schema matches Decision 7: `sessions` and `tool_calls` tables
//     exist with the expected columns.
//  4. The ID format is valid: sampled IDs from the database match the
//     Decision 3 shape (16-char lowercase alpha).
//
// Doctor is read-only. It does not create the database if it is missing —
// that is the user's signal to run `ktx run` / `ktx write` for the first
// time, or to investigate a config issue. It does not migrate, prune, or
// fix anything; repair commands belong to a future `ktx doctor --fix` or
// `ktx db prune` (out of M1 scope per Decision 2 revised).
//
// Output contract (Decision 2 / 4):
//
//	{
//	  "status": "ok|degraded|error",
//	  "path":   "<resolved database path>",
//	  "checks": [
//	    {"name": "...", "status": "ok|error", "detail": "..."},
//	    ...
//	  ]
//	}
//
// `degraded` is reserved for future use (e.g., warning that the system
// can still work but is not optimal). For M1 doctor, the top-level
// status is either "ok" (all checks passed) or "error" (at least one
// check failed).
//
// Exit codes (Decision 4):
//
//	0  all checks passed (status == "ok")
//	1  usage error (invalid flag, unexpected argument)
//	4  internal error (status == "error")
//
// Path resolution mirrors the other ktx commands: KTX_DB env > default
// ($XDG_DATA_HOME/ktx/ktx.db). Per-invocation override lives on
// `ktx db --path` (Decision 8); doctor does not accept its own --path.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jgabor/ktx/internal/store"
)

// doctorSampleLimit bounds how many IDs doctor inspects. The check is
// "spot check, not exhaustive scan": a 100-row sample catches
// corruption from manual edits or schema mismatches quickly without
// O(n) work over a large production database. The 101st row being
// malformed is exceedingly rare and would surface on the next doctor
// invocation once records accumulate.
const doctorSampleLimit = 100

// doctorStatus values. These are the canonical top-level status values
// exposed in the JSON output. The cli package accepts only "ok" and
// "error" in M1; "degraded" is reserved for future use.
const (
	doctorStatusOK       = "ok"
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
// shape mirrors Decision 2's diagnostic contract: status + per-check
// breakdown + the resolved path so the user can audit which database
// was actually inspected.
type doctorReport struct {
	Status string        `json:"status"`
	Path   string        `json:"path"`
	Checks []doctorCheck `json:"checks"`
}

func runDoctor(args []string, opts Options) int {
	return runDoctorWith(args, opts, os.Stdout, os.Stderr)
}

// runDoctorWith is the testable core of `ktx doctor`. stdout/stderr are
// injected so the report can be rendered without touching os.Stdout.
func runDoctorWith(args []string, opts Options, stdout, stderr io.Writer) int {
	// Subcommand-local --format (same rationale as get/list): the global
	// --format only parses from the front of args, so `ktx doctor
	// --format text` reaches us via extractFormat. Subcommand value
	// wins over the global one when both are set.
	format, rest := extractFormat(args)
	if format == "" {
		format = opts.Format
	}

	var showHelp bool
	fs := flag.NewFlagSet("ktx doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own errors and help
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help (shorthand)")

	if err := fs.Parse(rest); err != nil {
		printError(stderr, usageError("ktx doctor: %v", err), format)
		return ExitUsage
	}
	if showHelp {
		printDoctorHelp(stdout)
		return ExitOK
	}
	if format != FormatJSON && format != FormatText {
		printError(stderr, usageError("ktx doctor: invalid --format %q: want %q or %q", format, FormatJSON, FormatText), format)
		return ExitUsage
	}
	if len(fs.Args()) > 0 {
		printError(stderr, usageError("ktx doctor: unexpected argument %q", fs.Arg(0)), format)
		return ExitUsage
	}

	report := buildDoctorReport()

	switch format {
	case FormatText:
		printDoctorText(stdout, report)
	default:
		// JSON default (Decision 4). json.NewEncoder writes a trailing
		// newline so stdout stays pipe-friendly.
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			printError(stderr, internalError("ktx doctor: write JSON: %v", err), format)
			return ExitInternal
		}
	}

	if report.Status != doctorStatusOK {
		// Any failed check maps to a non-zero exit. The choice between
		// ExitInternal (4) and ExitUsage (1) is between "the system
		// is broken" and "the user typed something wrong"; a failing
		// check is the former.
		return ExitInternal
	}
	return ExitOK
}

// buildDoctorReport runs the checks in order and assembles the
// structured report. Each check is append-only — a failed earlier check
// does not short-circuit the later ones; we want the user to see the
// full picture so they can fix everything in one pass. The path
// resolution check is the only exception: if we cannot determine where
// the database should live, the rest of the checks have nothing to
// inspect, so the report returns early with a single error check.
func buildDoctorReport() *doctorReport {
	report := &doctorReport{}

	path, err := store.ResolvePath("")
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

	// Check 1: file exists. Read-only (does not call store.Open which
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

	// Check 2: openable. store.Open runs migrations on first open, but
	// the file already exists at this point so the migrations are
	// no-ops (CREATE TABLE IF NOT EXISTS is idempotent).
	db, err := store.Open(path)
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

	// Check 3: schema. Expected tables + columns match Decision 7.
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
			Detail: "sessions and tool_calls tables present with expected columns",
		})
	}

	// Check 4: ID format. Sample up to doctorSampleLimit IDs from each
	// table and validate against Decision 3's regex. An empty database
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

// checkIDFormat samples IDs from sessions and tool_calls and verifies
// each one matches Decision 3. The check returns ok with detail
// "sampled N+M IDs" when no IDs are present (vacuously true) or when
// every sampled ID matches the regex. The first malformed ID is
// surfaced in the detail so the user can grep for it.
func checkIDFormat(db *store.DB) doctorCheck {
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
		if !store.IDPattern.MatchString(id) {
			return doctorCheck{
				Name:   "id_format",
				Status: doctorStatusError,
				Detail: fmt.Sprintf("session id %q does not match Decision 3 (16-char lowercase alpha)", id),
			}
		}
	}
	for _, id := range toolIDs {
		if !store.IDPattern.MatchString(id) {
			return doctorCheck{
				Name:   "id_format",
				Status: doctorStatusError,
				Detail: fmt.Sprintf("tool_call id %q does not match Decision 3 (16-char lowercase alpha)", id),
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
		Detail: fmt.Sprintf("sampled %d session ids and %d tool_call ids; all match Decision 3", len(sessionIDs), len(toolIDs)),
	}
}

// printDoctorText renders the doctor report in a human-readable form.
// One line per check plus a status summary. Mirrors the verbose-style
// of other ktx text outputs (`ktx list --format text`, `ktx get --format
// text`) so the surface stays consistent.
func printDoctorText(w io.Writer, report *doctorReport) {
	fmt.Fprintf(w, "ktx doctor — %s\n", report.Status)
	if report.Path != "" {
		fmt.Fprintf(w, "path: %s\n", report.Path)
	}
	fmt.Fprintln(w, "checks:")
	for _, c := range report.Checks {
		marker := "[OK]"
		if c.Status != doctorStatusOK {
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
	fmt.Fprintln(w, "  - the schema matches Decision 7 (sessions and tool_calls tables)")
	fmt.Fprintln(w, "  - sampled IDs match Decision 3 (16-char lowercase alpha)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Output format:")
	fmt.Fprintln(w, "  json (default)  structured report on stdout with status + per-check detail")
	fmt.Fprintln(w, "  text            one-line-per-check summary suitable for terminals")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exit codes:")
	fmt.Fprintln(w, "  0  all checks passed (status: ok)")
	fmt.Fprintln(w, "  1  usage error (invalid flag, unexpected argument)")
	fmt.Fprintln(w, "  4  one or more checks failed (status: error)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Database path: KTX_DB env or $XDG_DATA_HOME/ktx/ktx.db (see 'ktx db --path').")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Doctor is read-only. It does not create the database if it is missing,")
	fmt.Fprintln(w, "does not migrate, and does not repair. Use 'ktx run' or 'ktx write' to")
	fmt.Fprintln(w, "create a new database; use 'ktx db prune' (when implemented) for cleanup.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'ktx --help' for the top-level command list.")
}
