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
	"path/filepath"
	"reflect"
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

func TestDoctorReadOnlySchemaMatrix(t *testing.T) {
	tests := []struct {
		name        string
		seed        func(*testing.T, string)
		wantStatus  string
		wantCode    int
		checkName   string
		checkStatus string
		detail      string
	}{
		{
			name: "supported older schema is prepared",
			seed: func(t *testing.T, path string) {
				seedDoctorDatabase(t, path, func(d *dbpkg.DB) {
					if _, err := d.DB().Exec(`PRAGMA user_version = 7`); err != nil {
						t.Fatalf("set older user_version: %v", err)
					}
				})
			},
			wantStatus:  doctorStatusOK,
			wantCode:    ExitOK,
			checkName:   "schema_version",
			checkStatus: doctorStatusOK,
			detail:      "prepared to supported schema 16",
		},
		{
			name:        "current schema remains healthy",
			seed:        func(t *testing.T, path string) { seedDoctorDatabase(t, path, nil) },
			wantStatus:  doctorStatusOK,
			wantCode:    ExitOK,
			checkName:   "schema_version",
			checkStatus: doctorStatusOK,
			detail:      "supported schema 16",
		},
		{
			name: "current schema missing compressed field fails closed",
			seed: func(t *testing.T, path string) {
				seedDoctorDatabase(t, path, func(d *dbpkg.DB) {
					if _, err := d.DB().Exec(`ALTER TABLE compressed DROP COLUMN timestamp_unix_nano`); err != nil {
						t.Fatalf("drop compressed timestamp: %v", err)
					}
				})
			},
			wantStatus:  doctorStatusError,
			wantCode:    ExitInternal,
			checkName:   "schema_valid",
			checkStatus: doctorStatusError,
			detail:      "timestamp_unix_nano",
		},
		{
			name: "newer schema is incompatible",
			seed: func(t *testing.T, path string) {
				seedDoctorDatabase(t, path, func(d *dbpkg.DB) {
					if _, err := d.DB().Exec(`PRAGMA user_version = 17`); err != nil {
						t.Fatalf("set newer user_version: %v", err)
					}
				})
			},
			wantStatus:  doctorStatusError,
			wantCode:    ExitInternal,
			checkName:   "schema_version",
			checkStatus: doctorStatusError,
			detail:      "do not lower user_version",
		},
		{
			name: "older malformed schema is not partially repaired",
			seed: func(t *testing.T, path string) {
				seedDoctorDatabase(t, path, func(d *dbpkg.DB) {
					if _, err := d.DB().Exec(`ALTER TABLE tool_calls DROP COLUMN tool; PRAGMA user_version = 7`); err != nil {
						t.Fatalf("malform older database: %v", err)
					}
				})
			},
			wantStatus:  doctorStatusError,
			wantCode:    ExitInternal,
			checkName:   "schema_valid",
			checkStatus: doctorStatusError,
			detail:      "tool",
		},
		{
			name: "corrupt input remains unchanged",
			seed: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
					t.Fatalf("write corrupt database: %v", err)
				}
			},
			wantStatus:  doctorStatusError,
			wantCode:    ExitInternal,
			checkName:   "database_openable",
			checkStatus: doctorStatusError,
			detail:      "open",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "seeded.db")
			t.Setenv("KTX_DB", path)
			t.Setenv("HOME", filepath.Join(root, "home"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

			tt.seed(t, path)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read database before doctor: %v", err)
			}

			var stdout, stderr bytes.Buffer
			code := runDoctorWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (stdout=%q stderr=%q)", code, tt.wantCode, stdout.String(), stderr.String())
			}
			report := decodeDoctorReport(t, stdout.Bytes())
			if report.Status != tt.wantStatus {
				t.Errorf("report status = %q, want %q", report.Status, tt.wantStatus)
			}
			assertDoctorCheck(t, report, tt.checkName, tt.checkStatus, tt.detail)

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read database after doctor: %v", err)
			}
			if tt.name != "supported older schema is prepared" && !bytes.Equal(before, after) {
				t.Fatal("doctor changed the database bytes")
			}
		})
	}
}

func TestDoctorMissingDisposableDefaultIsNotCreated(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KTX_DB", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	wantPath := filepath.Join(root, "data", "ktx", "ktx.db")

	var stdout, stderr bytes.Buffer
	code := runDoctorWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitInternal {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitInternal, stderr.String())
	}
	report := decodeDoctorReport(t, stdout.Bytes())
	if report.Path != wantPath {
		t.Fatalf("report path = %q, want disposable default %q", report.Path, wantPath)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("doctor created missing default database: stat error = %v", err)
	}
}

func TestDoctorAuditsInspectionBeforeReadOnlyOpen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "seeded.db")
	seedDoctorDatabase(t, path, nil)
	audit := filepath.Join(root, "audit.jsonl")
	t.Setenv("KTX_DB", path)
	t.Setenv("KTX_VERIFY_DB_AUDIT", audit)
	t.Setenv("KTX_VERIFY_FORBIDDEN_DB_PATHS", "[]")

	report := buildDoctorReport()
	if report.Status != doctorStatusOK {
		t.Fatalf("doctor status = %q", report.Status)
	}
	records := readDoctorAudit(t, audit)
	if got := doctorAuditOperations(records); !reflect.DeepEqual(got, []string{"resolve", "inspect", "open_read_only"}) {
		t.Fatalf("audit operations = %v", got)
	}
}

func TestDoctorAuditRejectsBeforeInspectionSideEffects(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "inert", "ktx.db")
	audit := filepath.Join(root, "audit.jsonl")
	forbidden, err := json.Marshal([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_DB", path)
	t.Setenv("KTX_VERIFY_DB_AUDIT", audit)
	t.Setenv("KTX_VERIFY_FORBIDDEN_DB_PATHS", string(forbidden))

	report := buildDoctorReport()
	if report.Status != doctorStatusError {
		t.Fatalf("doctor status = %q, want error", report.Status)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("doctor touched forbidden inert path: %v", err)
	}
	records := readDoctorAudit(t, audit)
	if len(records) != 1 || records[0].Operation != "resolve" || !records[0].Forbidden {
		t.Fatalf("forbidden audit records = %#v", records)
	}
}

func TestDoctorAuditFailuresAreFailClosed(t *testing.T) {
	for _, test := range []struct {
		name      string
		auditPath func(string) string
		forbidden string
	}{
		{name: "invalid payload", auditPath: func(root string) string { return filepath.Join(root, "audit.jsonl") }, forbidden: "{"},
		{name: "unwritable audit path", auditPath: func(root string) string { return filepath.Join(root, "missing", "audit.jsonl") }, forbidden: "[]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "inert", "ktx.db")
			t.Setenv("KTX_DB", path)
			t.Setenv("KTX_VERIFY_DB_AUDIT", test.auditPath(root))
			t.Setenv("KTX_VERIFY_FORBIDDEN_DB_PATHS", test.forbidden)
			report := buildDoctorReport()
			if report.Status != doctorStatusError || len(report.Checks) != 1 || report.Checks[0].Name != "database_path" {
				t.Fatalf("doctor report = %#v", report)
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("doctor touched inert path after audit failure: %v", err)
			}
		})
	}
}

type doctorAuditRecord struct {
	Operation string `json:"operation"`
	Forbidden bool   `json:"forbidden"`
}

func readDoctorAudit(t *testing.T, path string) []doctorAuditRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []doctorAuditRecord
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var record doctorAuditRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func doctorAuditOperations(records []doctorAuditRecord) []string {
	operations := make([]string, len(records))
	for index, record := range records {
		operations[index] = record.Operation
	}
	return operations
}

func TestRunWithDoctorAcceptsSubcommandFormat(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "seeded.db")
	t.Setenv("KTX_DB", path)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	seedDoctorDatabase(t, path, nil)

	var stdout, stderr bytes.Buffer
	code := runWith([]string{"doctor", "--format", "json"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	report := decodeDoctorReport(t, stdout.Bytes())
	if report.Status != doctorStatusOK {
		t.Fatalf("report status = %q, want %q", report.Status, doctorStatusOK)
	}
}

func seedDoctorDatabase(t *testing.T, path string, mutate func(*dbpkg.DB)) {
	t.Helper()
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("seed database: %v", err)
	}
	if mutate != nil {
		mutate(d)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}
}

func decodeDoctorReport(t *testing.T, data []byte) *doctorReport {
	t.Helper()
	var report doctorReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode doctor report: %v\nstdout=%q", err, data)
	}
	return &report
}

func assertDoctorCheck(t *testing.T, report *doctorReport, name, status, detail string) {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			if check.Status != status {
				t.Errorf("check %q status = %q, want %q", name, check.Status, status)
			}
			if !strings.Contains(check.Detail, detail) {
				t.Errorf("check %q detail = %q, want substring %q", name, check.Detail, detail)
			}
			return
		}
	}
	t.Errorf("doctor report missing check %q: %#v", name, report.Checks)
}
