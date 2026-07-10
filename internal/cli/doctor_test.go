// Table-driven tests for `ktx doctor` (internal/cli/doctor.go).
//
// Doctor is a read-only health check. It does
// NOT create the database — a missing file is a configuration signal,
// not a fault. The doctor flow exercises the full schema
// surface: database_exists, database_openable, schema_valid,
// m2_columns, bench_runs_table, id_format.
//
// Coverage:
//   - missing DB → status=error, exit 4
//   - healthy DB (fully migrated) → status=ok, exit 0
//   - text mode → tabwriter-style summary, exit 0
//   - unexpected positional arg → exit 1
//   - invalid --format → exit 1
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// doctorTestDB opens a per-test KTX_DB database. Doctor reads it
// via dbpkg.Open in buildDoctorReport.
func doctorTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	path := os.Getenv("KTX_DB")
	if path == "" {
		t.Fatal("doctorTestDB requires KTX_DB to be set")
	}
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestRunDoctorWith_HealthCheck covers the doctor subcommand's
// pass/fail surface: a healthy migrated database emits a
// status=ok JSON envelope on stdout and exits 0; a missing database
// file produces a status=error envelope and exits 4 (internal error).
func TestRunDoctorWith_HealthCheck(t *testing.T) {
	t.Run("healthy DB → status=ok, exit 0", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		doctorTestDB(t) // Open creates the file with full schema

		var stdout, stderr bytes.Buffer
		code := runDoctorWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Decode the JSON envelope and assert the per-check
		// status values. The exact per-check wording is
		// documented in doctor.go; we assert on the
		// structural fields (top-level status + per-check
		// name + status).
		var report struct {
			Status string `json:"status"`
			Path   string `json:"path"`
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checks"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if report.Status != "ok" {
			t.Errorf("report.status = %q, want ok", report.Status)
		}
		if report.Path == "" {
			t.Errorf("report.path is empty")
		}
		// Every check should be "ok" on a fresh database.
		// The expected check set is the union of all
		// probes.
		wantChecks := map[string]bool{
			"database_exists":   false,
			"database_openable": false,
			"schema_valid":      false,
			"cost_columns":      false,
			"bench_runs_table":  false,
			"id_format":         false,
		}
		for _, c := range report.Checks {
			if _, ok := wantChecks[c.Name]; ok {
				wantChecks[c.Name] = true
			}
			if c.Status != "ok" {
				t.Errorf("check %q status = %q, want ok", c.Name, c.Status)
			}
		}
		for name, seen := range wantChecks {
			if !seen {
				t.Errorf("missing check %q in report", name)
			}
		}
	})

	t.Run("missing DB → status=error, exit 4", func(t *testing.T) {
		// KTX_DB points at a path that does not exist; the
		// parent directory is created lazily only when
		// dbpkg.Open runs (which doctor does NOT do — the
		// database_exists check is a stat-only check that
		// short-circuits before the Open).
		t.Setenv("KTX_DB", t.TempDir()+"/missing-ktx.db")

		var stdout, stderr bytes.Buffer
		code := runDoctorWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitInternal {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitInternal, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"status":"error"`) {
			t.Errorf("stdout missing status=error: %q", stdout.String())
		}
		if !strings.Contains(stdout.String(), "database_exists") {
			t.Errorf("stdout missing database_exists check: %q", stdout.String())
		}
	})

	t.Run("text mode → status=ok, summary on stdout", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		doctorTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runDoctorWith([]string{"--format", "text"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "ktx doctor —") {
			t.Errorf("text output missing status header: %q", out)
		}
		if !strings.Contains(out, "[OK]") {
			t.Errorf("text output missing [OK] marker: %q", out)
		}
		if !strings.Contains(out, "database_exists") {
			t.Errorf("text output missing database_exists line: %q", out)
		}
	})

	t.Run("unexpected positional arg exits 1 (usage)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		doctorTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runDoctorWith([]string{"unexpected"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
	})

	t.Run("invalid --format exits 1 (usage)", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		doctorTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runDoctorWith([]string{"--format", "yaml"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
	})
}
