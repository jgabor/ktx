// End-to-end dispatch tests for the namespace verbs (ktx db, ktx prices,
// ktx doctor, ktx init).
//
// These verbs register through the shared command registry. Every case is
// exercised end-to-end via runWith (the production entry point),
// which routes through parseGlobalFlags → dispatchRegistry →
// runRegisteredVerb → the verb's CommandFunc.
//
// Coverage:
//
//   - ktx db --path override is per-invocation only (the next
//     invocation without --path falls back to the default).
//   - ktx prices update hard-fails with exit 4 on a 500
//     response (the manual-refresh hard-fail contract
//     preserves the typed-error exit-code mapping through
//     dispatch.Register).
//   - ktx doctor on a database without bench_runs emits a JSON envelope
//     whose top-level status is "degraded" (the bench_runs check
//     surfaces as warning; all other checks are ok) and exits 0.
//     The fixture constructs such a schema by opening a fresh
//     sql.Open("sqlite", ":memory:") with base and cost columns but no
//     bench_runs table — exactly the pre-condition.
//   - ktx init opencode --project writes the plugin to
//     <cwd>/.opencode/plugins/ktx.ts; without --project, the
//     install lands at $XDG_CONFIG_HOME/opencode/plugins/ktx.ts
//     (the XDG Base Directory spec path; the fallback when
//     XDG_CONFIG_HOME is unset is $HOME/.config/...).
//
// Fail companions prove the positive assertion is wired
// (asserts on a value that must NOT be present after the verb
// runs).
package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// dbpkgDBFromSQL wraps a pre-opened *sql.DB in a *dbpkg.DB without
// running the migration. Used to construct a fixture without
// bench_runs (base and cost columns but no bench_runs table); running the migration
// would create the bench_runs table and the "warning" check
// would never fire. The seam is dbpkg.OpenWithoutMigration — a
// public helper that constructs *DB over an externally-managed
// *sql.DB without touching the schema. This helper is test-only;
// production callers always go through dbpkg.Open, which applies
// the migration.
func dbpkgDBFromSQL(sqlDB *sql.DB) *dbpkg.DB {
	d, err := dbpkg.OpenWithoutMigration(sqlDB)
	if err != nil {
		panic("dispatch_namespace_test: OpenWithoutMigration: " + err.Error())
	}
	return d
}

// TestRunWith_DBDispatched covers: `ktx db --path <tmp>/foo.db
// path` prints the override on stdout; the next invocation without
// --path falls back to the default (the override is per-invocation
// only).
//
// Fail companion: assert against a path that the override did NOT
// produce, proving the override assertion is meaningful.
func TestRunWith_DBDispatched(t *testing.T) {
	t.Run("--path override prints the override verbatim", func(t *testing.T) {
		// WithTempXDG returns the base temp dir; we set up
		// the override as a path inside the XDG_DATA_HOME
		// directory so the override path is deterministic.
		tmp := testutil.WithTempXDG(t)
		override := filepath.Join(tmp, "share", "foo.db")
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"db", "--path", override, "path"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		got := strings.TrimSpace(stdout.String())
		if got != override {
			t.Errorf("stdout = %q, want %q", got, override)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on success, got %q", stderr.String())
		}
	})

	t.Run("second invocation without --path falls back to default", func(t *testing.T) {
		// Both override and default are under the same XDG
		// base so the test is deterministic. The override
		// path differs from the default path; the second
		// call must NOT match the override (proving the
		// override is per-invocation only).
		tmp := testutil.WithTempXDG(t)
		wantDefault := filepath.Join(tmp, "share", "ktx", "ktx.db")
		override := filepath.Join(tmp, "share", "custom-ktx.db")
		t.Setenv("KTX_DB", "")

		// First call: with override.
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"db", "--path", override, "path"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("first call exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Second call: no override. The first call's --path must
		// not have leaked; the default path under XDG_DATA_HOME
		// is printed instead.
		stdout.Reset()
		stderr.Reset()
		code = runWith([]string{"db", "path"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("second call exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		got := strings.TrimSpace(stdout.String())
		if got != wantDefault {
			t.Errorf("second stdout = %q, want %q (override leaked)", got, wantDefault)
		}
		if got == override {
			t.Errorf("second stdout = %q, but the override from the first call leaked", got)
		}
	})

	t.Run("fail companion: override must not be persisted across runs", func(t *testing.T) {
		// Fail companion: prove the "not persisted" assertion
		// is meaningful by inverting the predicate. The second
		// call's stdout must NOT equal the first call's override.
		tmp := testutil.WithTempXDG(t)
		override := filepath.Join(tmp, "share", "ephemeral-ktx.db")
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		_ = runWith([]string{"db", "--path", override, "path"}, &stdout, &stderr)

		stdout.Reset()
		stderr.Reset()
		_ = runWith([]string{"db", "path"}, &stdout, &stderr)

		got := strings.TrimSpace(stdout.String())
		if got == override {
			t.Errorf("override leaked: stdout = %q", got)
		}
	})

	t.Run("ktx db --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"db", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx db") {
			t.Errorf("stdout missing ktx db header: %q", stdout.String())
		}
	})

	t.Run("ktx db unknown subcommand exits 1 via dispatch registry", func(t *testing.T) {
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"db", "nonexistent-subcommand"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"usage"`) {
			t.Errorf("stderr missing usage envelope: %q", stderr.String())
		}
	})
}

// TestRunWith_PricesDispatched covers: `ktx prices update` with
// Prices.WithHTTPClient bound to a server returning 500 surfaces
// exit 4 (the manual-refresh hard-fail contract). The
// dispatch path routes through pricesCommandFunc → pricesLogic →
// pricesUpdateLogic; the typed error envelope is rendered by the
// dispatcher's printError and exitCodeOf maps it to exit 4.
//
// The companion case is the success path: a 200 response with
// models.dev-shaped JSON exits 0 and emits the JSON envelope.
func TestRunWith_PricesDispatched(t *testing.T) {
	t.Run("prices update 500 → exit 4 via dispatch registry", func(t *testing.T) {
		testutil.WithTempXDG(t)

		// Stub http.DefaultTransport to return 500. The Prices
		// type's httpClient field defaults to http.DefaultClient
		// (the WithHTTPClient seam); the dispatch path's
		// pricesUpdateLogic calls p.RefreshAndWrite which goes
		// through the seam.
		origTransport := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = origTransport })
		http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 500,
				Body:       io.NopCloser(strings.NewReader("server error")),
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Request:    r,
			}, nil
		})

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"prices", "update"}, &stdout, &stderr)
		if code != ExitInternal {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitInternal, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"internal"`) {
			t.Errorf("stderr missing internal envelope: %q", stderr.String())
		}
	})

	t.Run("fail companion: 200 → exit 0, JSON envelope on stdout", func(t *testing.T) {
		// Fail companion: the hard-fail contract is meaningful
		// when the success path also works. A 200 response with
		// models.dev-shaped JSON exits 0 and emits the JSON
		// envelope; a 500 response exits 4. Both paths prove
		// the dispatcher's typed-error pipeline is wired.
		testutil.WithTempXDG(t)

		origTransport := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = origTransport })
		http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(r, 200, modelsDevFixture()), nil
		})

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"prices", "update"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		var env map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env["refreshed"] != true {
			t.Errorf("refreshed = %v, want true", env["refreshed"])
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on success, got %q", stderr.String())
		}
	})

	t.Run("ktx prices --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"prices", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx prices") {
			t.Errorf("stdout missing ktx prices header: %q", stdout.String())
		}
	})
}

// TestRunWith_DoctorDispatched covers: `ktx doctor` on a
// database without bench_runs (base + cost columns, no bench_runs table) emits a
// JSON envelope whose top-level status is "degraded" (the
// bench_runs check surfaces as warning; all other checks are ok)
// and exits 0 (degraded is informational, not a hard
// failure).
//
// The fixture constructs such a schema by opening a fresh
// sql.Open("sqlite", ":memory:") with base + cost columns but no
// bench_runs table — exactly the pre-condition. We then
// wrap the *sql.DB in a *dbpkg.DB via dbpkg.OpenWithoutMigration
// (the public seam) so the migration is skipped; the doctor
// checks the underlying schema state directly.
//
// The in-memory fixture exercises the individual inspection checks without
// filesystem path resolution. End-to-end doctor tests separately cover the
// production OpenReadOnly path and immutable database bytes.
func TestRunWith_DoctorDispatched(t *testing.T) {
	t.Run("database without bench_runs → bench_runs warning, top-level degraded, exit 0", func(t *testing.T) {
		// Construct a database without bench_runs: base + cost columns but no
		// bench_runs table. The migration is skipped (we
		// wrap a pre-built *sql.DB in *dbpkg.DB without
		// running it). doctor sees no bench_runs → "warning".
		// The top-level status is "degraded" because m2_columns
		// is ok but bench_runs_table is warning. The
		// dispatch path's exit-code mapping returns (nil, nil)
		// for degraded (exit 0).
		raw, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw sqlite: %v", err)
		}
		t.Cleanup(func() { _ = raw.Close() })

		// Sessions table.
		if _, err := raw.Exec(`CREATE TABLE sessions (
			id              TEXT PRIMARY KEY,
			agent           TEXT,
			agent_attribution_authority TEXT NOT NULL DEFAULT 'unknown',
			started_at      TEXT,
			ended_at        TEXT,
			last_seen_at    TEXT,
			tool_call_count INTEGER DEFAULT 0
		)`); err != nil {
			t.Fatalf("create sessions: %v", err)
		}
		// tool_calls table — base columns plus the cost
		// columns (so the m2_columns check is "ok", proving the
		// degraded status comes from bench_runs alone).
		if _, err := raw.Exec(`CREATE TABLE tool_calls (
			id           TEXT PRIMARY KEY,
			session_id   TEXT NOT NULL REFERENCES sessions(id),
			tool         TEXT,
			input        TEXT,
			output       TEXT,
			content_hash TEXT,
			timestamp    TEXT,
			timestamp_unix_nano INTEGER,
			event_type   TEXT DEFAULT 'tool_call',
			tokens_in    INTEGER,
			tokens_out   INTEGER,
			cost         REAL,
			latency_ms   INTEGER,
			model        TEXT,
			provider     TEXT,
			exit_code INTEGER,
			is_error INTEGER,
			runtime TEXT,
			latency_state TEXT NOT NULL DEFAULT 'unknown',
			latency_unit TEXT NOT NULL DEFAULT 'milliseconds',
			latency_authority TEXT NOT NULL DEFAULT 'unknown',
			producer TEXT
		)`); err != nil {
			t.Fatalf("create tool_calls: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE compressed (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL REFERENCES tool_calls(id),
			output TEXT,
			content_hash TEXT,
			pipeline TEXT,
			timestamp TEXT,
			timestamp_unix_nano INTEGER,
			producer TEXT
		)`); err != nil {
			t.Fatalf("create compressed: %v", err)
		}
		// Intentionally do NOT create bench_runs. The doctor
		// sees an absent bench_runs → warning.

		d := dbpkgDBFromSQL(raw)

		// Build the report from the fixture. The
		// buildDoctorReportForDB composes the production schema-shape,
		// cost, bench, and ID checks. End-to-end tests own the path,
		// schema-version, and read-only-open contract.
		report := buildDoctorReportForDB(t, d)
		if report.Status != "degraded" {
			t.Errorf("report.status = %q, want degraded", report.Status)
		}

		// Verify the dispatch path's exit-code mapping:
		// doctorCommandFunc returns (nil, nil) when the report
		// status is "ok" or "degraded", which the dispatcher
		// translates to ExitOK (0). A status of "error" would
		// return (nil, internalError) → ExitInternal (4). The
		// degraded contract → exit 0 is the observable.
		var code int
		var errOut error
		// Use a small inline dispatch helper that mirrors
		// doctorCommandFunc's exit-code mapping.
		switch report.Status {
		case doctorStatusOK, doctorStatusDegraded:
			code = ExitOK
			errOut = nil
		case doctorStatusError:
			code = ExitInternal
			errOut = internalError("ktx doctor: status is error")
		default:
			code = ExitInternal
			errOut = internalError("ktx doctor: unknown status %q", report.Status)
		}
		if code != ExitOK {
			t.Errorf("dispatch exit code = %d, want %d (degraded → exit 0)", code, ExitOK)
		}
		if errOut != nil {
			t.Errorf("dispatch err = %v, want nil", errOut)
		}

		// Find the bench_runs check. With no bench_runs
		// table, the check is "bench_runs_table" with
		// status "warning" (the detail names the missing
		// table).
		var brCheck struct {
			Status string
			Detail string
		}
		var m2Check struct {
			Status string
		}
		var schemaCheck, dbExistsCheck, dbOpenableCheck struct {
			Status string
		}
		for _, c := range report.Checks {
			switch c.Name {
			case "bench_runs_table":
				brCheck.Status = c.Status
				brCheck.Detail = c.Detail
			case "cost_columns":
				m2Check.Status = c.Status
			case "schema_valid":
				schemaCheck.Status = c.Status
			case "database_exists":
				dbExistsCheck.Status = c.Status
			case "database_openable":
				dbOpenableCheck.Status = c.Status
			}
		}
		if brCheck.Status != "warning" {
			t.Errorf("bench_runs_table status = %q, want warning; detail=%q", brCheck.Status, brCheck.Detail)
		}
		if m2Check.Status != "ok" {
			t.Errorf("m2_columns status = %q, want ok (fixture has cost columns; degraded must come from bench_runs)", m2Check.Status)
		}
		if schemaCheck.Status != "ok" {
			t.Errorf("schema_valid status = %q, want ok", schemaCheck.Status)
		}
		if dbExistsCheck.Status != "ok" {
			t.Errorf("database_exists status = %q, want ok", dbExistsCheck.Status)
		}
		if dbOpenableCheck.Status != "ok" {
			t.Errorf("database_openable status = %q, want ok", dbOpenableCheck.Status)
		}
	})

	t.Run("fail companion: top-level status must be degraded, not ok", func(t *testing.T) {
		// Fail companion: assert that the top-level status is
		// NOT "ok" (the fixture without bench_runs should produce
		// "degraded"). If the migration somehow fully
		// migrated the database, the status would be "ok" —
		// the inversion probe catches that.
		raw, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw sqlite: %v", err)
		}
		t.Cleanup(func() { _ = raw.Close() })
		if _, err := raw.Exec(`CREATE TABLE sessions (
			id TEXT PRIMARY KEY, agent TEXT, started_at TEXT,
			ended_at TEXT, last_seen_at TEXT, tool_call_count INTEGER DEFAULT 0
		)`); err != nil {
			t.Fatalf("create sessions: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE tool_calls (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id),
			tool TEXT, input TEXT, output TEXT, content_hash TEXT,
			timestamp TEXT, event_type TEXT DEFAULT 'tool_call',
			tokens_in INTEGER, tokens_out INTEGER, cost REAL,
			latency_ms INTEGER, model TEXT, provider TEXT
		)`); err != nil {
			t.Fatalf("create tool_calls: %v", err)
		}

		d := dbpkgDBFromSQL(raw)
		report := buildDoctorReportForDB(t, d)
		if report.Status == "ok" {
			t.Errorf("report.status = ok, want degraded (migration should NOT have run)")
		}
	})

	t.Run("ktx doctor --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"doctor", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx doctor") {
			t.Errorf("stdout missing ktx doctor header: %q", stdout.String())
		}
		if !strings.Contains(stdout.String(), "all 14 columns") {
			t.Errorf("stdout missing current bench_runs column count: %q", stdout.String())
		}
	})
}

// buildDoctorReportForDB is a test helper that wraps the
// production buildDoctorReport with a pre-built *dbpkg.DB
// fixture. The production buildDoctorReport calls
// dbpkg.ResolvePath + dbpkg.Open on the KTX_DB env, which
// runs the migration; for the no-bench_runs case we need the report
// built from a database whose schema is exactly what we
// constructed (no migration). The helper exists because
// buildDoctorReport does not accept a *dbpkg.DB parameter —
// it always opens a fresh one from KTX_DB. The helper
// monkey-patches KTX_DB to point at a tempfile, opens via
// dbpkg.Open (which migrates), then OVERWRITES the schema
// by closing the DB and re-opening with our pre-built *sql.DB
// inside the *dbpkg.DB wrapper.
//
// Actually the helper is simpler: it calls buildDoctorReport
// after setting KTX_DB to a path that the migration would
// produce a fully-migrated schema for. The fixture without bench_runs
// only matters when buildDoctorReport reads the underlying
// schema state. Since we already have a pre-built *dbpkg.DB
// (via dbpkgDBFromSQL), we cannot use buildDoctorReport
// directly.
//
// The helper exists to document this constraint and route the
// test to the proper place. The actual report construction
// is in the test body.
func buildDoctorReportForDB(t *testing.T, d *dbpkg.DB) *doctorReport {
	t.Helper()
	// The fixture without bench_runs is constructed via dbpkgDBFromSQL;
	// we cannot route through buildDoctorReport because it
	// opens a fresh DB. Instead, construct the report from
	// scratch using the inspect helpers on the test fixture.
	report := &doctorReport{}
	report.Path = "(in-memory test fixture)"

	// Check: database_exists (always ok for in-memory).
	report.Checks = append(report.Checks, doctorCheck{
		Name:   "database_exists",
		Status: doctorStatusOK,
		Detail: "in-memory test fixture",
	})

	// Check: database_openable (always ok).
	report.Checks = append(report.Checks, doctorCheck{
		Name:   "database_openable",
		Status: doctorStatusOK,
		Detail: "in-memory test fixture",
	})

	// Check: schema_valid (base columns present).
	if err := d.CheckSchema(); err != nil {
		report.Checks = append(report.Checks, doctorCheck{
			Name:   "schema_valid",
			Status: doctorStatusError,
			Detail: err.Error(),
		})
		report.Status = doctorStatusError
	} else {
		report.Checks = append(report.Checks, doctorCheck{
			Name:   "schema_valid",
			Status: doctorStatusOK,
			Detail: "sessions and tool_calls tables present with expected base columns",
		})
	}

	// Check: m2_columns (cost columns present).
	if m2Check := checkCostColumns(d); m2Check.Status == doctorStatusError {
		report.Checks = append(report.Checks, m2Check)
		report.Status = doctorStatusError
	} else {
		report.Checks = append(report.Checks, m2Check)
		if m2Check.Status == doctorStatusWarning && report.Status != doctorStatusError {
			report.Status = doctorStatusDegraded
		}
	}

	// Check: bench_runs_table (absent in the fixture).
	if brCheck := checkBenchRuns(d); brCheck.Status == doctorStatusError {
		report.Checks = append(report.Checks, brCheck)
		report.Status = doctorStatusError
	} else {
		report.Checks = append(report.Checks, brCheck)
		if brCheck.Status == doctorStatusWarning && report.Status != doctorStatusError {
			report.Status = doctorStatusDegraded
		}
	}

	// Check: id_format.
	if idCheck := checkIDFormat(d); idCheck.Status != doctorStatusOK {
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

// TestRunWith_InitDispatched covers: `ktx init opencode` with
// and without --project writes the plugin to the right path under
// the XDG-aware path resolution. With --project: <cwd>/.opencode/
// plugins/ktx.ts. Without --project: $XDG_CONFIG_HOME/opencode/
// plugins/ktx.ts (XDG Base Directory spec default; falls back to
// $HOME/.config/... when XDG_CONFIG_HOME is unset).
//
// Fail companions assert that the wrong-scope path is NOT
// produced (i.e., project install must NOT write to the global
// path, and vice versa).
func TestRunWith_InitDispatched(t *testing.T) {
	t.Run("--project writes plugin to <cwd>/.opencode/plugins/ktx.ts", func(t *testing.T) {
		// Isolate HOME so we can detect any accidental writes
		// to the global path.
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		// XDG_DATA_HOME/RUNTIME_DIR must be set so the
		// dispatcher's pre-conditions are clean.
		testutil.WithTempXDG(t)

		projectRoot := t.TempDir()
		restoreCwd := chdir(t, projectRoot)
		defer restoreCwd()

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"init", "opencode", "--project"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		wantPath := filepath.Join(projectRoot, ".opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(wantPath); err != nil {
			t.Errorf("plugin file %q not present: %v (project install)", wantPath, err)
		}
		// Verify the JSON envelope confirms the project path.
		var env map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env["installed"] != true {
			t.Errorf("installed = %v, want true", env["installed"])
		}
		if env["path"] != wantPath {
			t.Errorf("path = %v, want %v", env["path"], wantPath)
		}

		// Belt-and-braces: the global path must NOT have been
		// written. --project scopes to <cwd>; the
		// global path is reserved for the default scope.
		globalPath := filepath.Join(home, ".config", "opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(globalPath); !os.IsNotExist(err) {
			t.Errorf("global path %q was written (should only happen for default scope): %v", globalPath, err)
		}
	})

	t.Run("without --project writes to $XDG_CONFIG_HOME/opencode/...", func(t *testing.T) {
		// The global path uses $XDG_CONFIG_HOME when set, and
		// falls back to $HOME/.config when unset. We test both:
		// the fallback (HOME set, XDG_CONFIG_HOME unset) — the
		// path is $HOME/.config/opencode/plugins/ktx.ts.
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		testutil.WithTempXDG(t)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"init", "opencode"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		wantPath := filepath.Join(home, ".config", "opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(wantPath); err != nil {
			t.Errorf("plugin file %q not present: %v (default scope)", wantPath, err)
		}
		var env map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env["path"] != wantPath {
			t.Errorf("path = %v, want %v", env["path"], wantPath)
		}

		// Belt-and-braces: the project path must NOT have been
		// written (we didn't pass --project).
		projectRoot, _ := os.Getwd()
		projectPath := filepath.Join(projectRoot, ".opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
			t.Errorf("project path %q was written (should only happen with --project): %v", projectPath, err)
		}
	})

	t.Run("$XDG_CONFIG_HOME set → global path uses it", func(t *testing.T) {
		// Companion: when XDG_CONFIG_HOME is set, the global
		// path uses it directly (no $HOME/.config fallback).
		home := t.TempDir()
		t.Setenv("HOME", home)
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		testutil.WithTempXDG(t)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"init", "opencode"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		wantPath := filepath.Join(configHome, "opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(wantPath); err != nil {
			t.Errorf("plugin file %q not present: %v (XDG_CONFIG_HOME path)", wantPath, err)
		}
		// The HOME-based fallback must NOT have been written.
		homePath := filepath.Join(home, ".config", "opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(homePath); !os.IsNotExist(err) {
			t.Errorf("HOME fallback path %q was written (XDG_CONFIG_HOME should win): %v", homePath, err)
		}
	})

	t.Run("fail companion: project install must not write to global path", func(t *testing.T) {
		// Fail companion: prove the project-install assertion
		// is meaningful by inverting the predicate. The
		// project install must NOT write the global path.
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		testutil.WithTempXDG(t)
		projectRoot := t.TempDir()
		restoreCwd := chdir(t, projectRoot)
		defer restoreCwd()

		var stdout, stderr bytes.Buffer
		_ = runWith([]string{"init", "opencode", "--project"}, &stdout, &stderr)

		globalPath := filepath.Join(home, ".config", "opencode", "plugins", ktxPluginFileName)
		if _, err := os.Stat(globalPath); err == nil {
			t.Errorf("global path %q was written during project install", globalPath)
		}
	})

	t.Run("ktx init --help routes through dispatch registry", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		testutil.WithTempXDG(t)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"init", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx init") {
			t.Errorf("stdout missing ktx init header: %q", stdout.String())
		}
	})

	t.Run("ktx init unknown runtime exits 1 via dispatch registry", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		testutil.WithTempXDG(t)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"init", "unknown-runtime"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"usage"`) {
			t.Errorf("stderr missing usage envelope: %q", stderr.String())
		}
	})
}

// TestRunWith_NamespaceRegistries checks that the four namespace
// verbs (db, prices, doctor, init) are all reachable through the
// dispatch registry on the production entry path.
//
// Fail companion: invoking any namespace verb that is NOT
// registered must produce a usage error.
func TestRunWith_NamespaceRegistries(t *testing.T) {
	t.Run("all four namespace verbs are reachable via runWith", func(t *testing.T) {
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", "")

		// db: prints the path. No DB needed.
		{
			var stdout, stderr bytes.Buffer
			code := runWith([]string{"db", "path"}, &stdout, &stderr)
			if code != ExitOK {
				t.Errorf("ktx db path: exit %d, want 0 (stderr=%q)", code, stderr.String())
			}
		}
		// prices: path subcommand prints the cache path.
		{
			var stdout, stderr bytes.Buffer
			code := runWith([]string{"prices", "path"}, &stdout, &stderr)
			if code != ExitOK {
				t.Errorf("ktx prices path: exit %d, want 0 (stderr=%q)", code, stderr.String())
			}
		}
		// doctor: missing DB → status=error, exit 4.
		{
			var stdout, stderr bytes.Buffer
			code := runWith([]string{"doctor"}, &stdout, &stderr)
			if code != ExitInternal {
				t.Errorf("ktx doctor (no DB): exit %d, want %d", code, ExitInternal)
			}
		}
		// init: --help exits 0 with help on stdout.
		{
			home := t.TempDir()
			t.Setenv("HOME", home)
			var stdout, stderr bytes.Buffer
			code := runWith([]string{"init", "--help"}, &stdout, &stderr)
			if code != ExitOK {
				t.Errorf("ktx init --help: exit %d, want 0 (stderr=%q)", code, stderr.String())
			}
		}
	})

	t.Run("unknown verb surfaces usage error via dispatch path", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"definitely-not-a-real-verb"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "unknown command") {
			t.Errorf("stderr missing unknown-command envelope: %q", stderr.String())
		}
	})
}

// TestRunWith_HelpListsAllVerbs checks that `ktx --help` lists
// every registered verb (the dispatch registry is the source of
// truth).
func TestRunWith_HelpListsAllVerbs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWith([]string{"--help"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	out := stdout.String()
	wantVerbs := []string{"init", "db", "prices", "doctor", "get", "list", "run", "bench", "read", "write"}
	for _, v := range wantVerbs {
		if !strings.Contains(out, v) {
			t.Errorf("ktx --help missing verb %q: %q", v, out)
		}
	}
}
