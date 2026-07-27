// Table-driven tests for `ktx db` (internal/cli/db.go).
//
// Coverage:
//
//   - KTX_DB=” and no --path → stdout is the resolved
//     path under the test XDG_DATA_HOME. The default resolution is
//     $XDG_DATA_HOME/ktx/ktx.db.
//   - A missing database file at
//     t.TempDir()/ktx.db is created (parent dir + migration) by
//     runDB path resolution + a subsequent dbpkg.Open; the second
//     open is a no-op. This is exercised through the integration with
//     the db path (the full flow is covered end-to-end via the
//     write_test.go and list_test.go flows that also call dbpkg.Open
//     against a fresh KTX_DB).
//
// Companion cases: --path override (per-invocation, not persisted),
// unknown subcommand, --help, and the prun stub.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// TestRunDBWith_PathResolution covers `ktx db path`
// with no --path override and no KTX_DB env → the path printed on
// stdout is the resolved default under the test XDG_DATA_HOME. The
// default is $XDG_DATA_HOME/ktx/ktx.db.
//
// Companion cases assert:
//   - --path override prints the override verbatim (per-invocation, not
//     persisted; the second call without --path still prints the
//     default).
//   - `ktx db` (no subcommand) defaults to `ktx db path`.
//   - unknown subcommand returns usage exit 1.
//   - --help exits 0 with help on stdout.
func TestRunDBWith_PathResolution(t *testing.T) {
	t.Run("KTX_DB='' and no --path prints XDG_DATA_HOME default", func(t *testing.T) {
		// Isolate the XDG path so the test does not observe or
		// clobber the user's real XDG_DATA_HOME.
		tmp := testutil.WithTempXDG(t)
		// The default path is $XDG_DATA_HOME/ktx/ktx.db.
		want := filepath.Join(tmp, "share", "ktx", "ktx.db")

		// Ensure KTX_DB is unset for this subtest.
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		code := runDBWith([]string{"path"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		got := strings.TrimSpace(stdout.String())
		if got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
	})

	t.Run("--path override prints override verbatim", func(t *testing.T) {
		// The --path flag is per-invocation only:
		// nothing is persisted. A subsequent invocation without
		// --path must still print the default, not the override.
		tmp := testutil.WithTempXDG(t)
		wantDefault := filepath.Join(tmp, "share", "ktx", "ktx.db")
		override := filepath.Join(tmp, "share", "custom-ktx.db")
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		code := runDBWith([]string{"--path", override, "path"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != override {
			t.Errorf("stdout = %q, want %q", got, override)
		}

		// Verify the override is NOT persisted: a second
		// invocation without --path falls back to the default.
		stdout.Reset()
		stderr.Reset()
		code = runDBWith([]string{"path"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("second call exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != wantDefault {
			t.Errorf("second stdout = %q, want %q (override leaked)", got, wantDefault)
		}
	})

	t.Run("no subcommand defaults to path", func(t *testing.T) {
		tmp := testutil.WithTempXDG(t)
		want := filepath.Join(tmp, "share", "ktx", "ktx.db")
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		code := runDBWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
	})

	t.Run("unknown subcommand exits 1 (usage)", func(t *testing.T) {
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		code := runDBWith([]string{"nonexistent-subcommand"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "nonexistent-subcommand") {
			t.Errorf("stderr missing subcommand name: %q", stderr.String())
		}
	})

	t.Run("prune subcommand is stubbed exits 1 (usage)", func(t *testing.T) {
		// `ktx db prune` is reserved for future
		// orphaned-session cleanup. The stub is intentionally
		// unimplemented (exits 1 with a usage error) so the
		// CLI surface reserves the verb without claiming
		// behavior that does not exist yet.
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		code := runDBWith([]string{"prune"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "not implemented") {
			t.Errorf("stderr missing not-implemented message: %q", stderr.String())
		}
	})

	t.Run("--help exits 0 with help on stdout", func(t *testing.T) {
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", "")

		var stdout, stderr bytes.Buffer
		code := runDBWith([]string{"--help"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx db") {
			t.Errorf("stdout missing ktx db header: %q", stdout.String())
		}
	})
}

func TestDBReportRejectsSourceDatabaseDestinationAliases(t *testing.T) {
	tests := []struct {
		name        string
		destination func(*testing.T, string) string
	}{
		{name: "exact path", destination: func(_ *testing.T, path string) string { return path }},
		{name: "relative absolute equivalent", destination: func(t *testing.T, path string) string {
			workingDirectory, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(workingDirectory, path)
			if err != nil {
				t.Fatal(err)
			}
			return relative
		}},
		{name: "symlink", destination: func(t *testing.T, path string) string {
			alias := filepath.Join(filepath.Dir(path), "source-link.db")
			if err := os.Symlink(path, alias); err != nil {
				t.Fatal(err)
			}
			return alias
		}},
		{name: "hardlink", destination: func(t *testing.T, path string) string {
			alias := filepath.Join(filepath.Dir(path), "source-hardlink.db")
			if err := os.Link(path, alias); err != nil {
				t.Fatal(err)
			}
			return alias
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "ktx.db")
			database, err := dbpkg.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			destination := test.destination(t, path)
			for _, sidecar := range []string{path + "-wal", path + "-shm"} {
				if err := os.WriteFile(sidecar, []byte(filepath.Base(sidecar)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := directorySnapshot(t, directory)
			audit := filepath.Join(t.TempDir(), "database-access.jsonl")
			t.Setenv("KTX_DB", path)
			t.Setenv("KTX_VERIFY_DB_AUDIT", audit)
			t.Setenv("KTX_VERIFY_FORBIDDEN_DB_PATHS", "[]")

			var stdout, stderr bytes.Buffer
			code := runWith([]string{"db", "report", "--output", destination}, &stdout, &stderr)
			if code != ExitUsage || stdout.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			for _, expected := range []string{
				`"failure_class":"invalid-database-report-destination"`,
				"source database",
				"--output -",
			} {
				if !strings.Contains(stderr.String(), expected) {
					t.Errorf("stderr missing %q: %s", expected, stderr.String())
				}
			}
			if after := directorySnapshot(t, directory); !reflect.DeepEqual(after, before) {
				t.Fatalf("source storage changed:\nbefore=%#v\nafter=%#v", before, after)
			}
			auditBytes, err := os.ReadFile(audit)
			if err != nil {
				t.Fatal(err)
			}
			auditText := string(auditBytes)
			if strings.Count(auditText, `"operation":"resolve"`) != 1 || strings.Count(auditText, `"operation":"inspect"`) != 1 || strings.Contains(auditText, `"operation":"open_read_only"`) {
				t.Fatalf("unexpected access audit: %s", auditText)
			}
		})
	}
}

func TestDBReportPreservesStdoutAndDistinctFileOutput(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "ktx.db")
	database, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_DB", path)
	before := directorySnapshot(t, directory)

	t.Run("stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runWith([]string{"db", "report", "--output", "-"}, &stdout, &stderr); code != ExitOK {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if !json.Valid(stdout.Bytes()) {
			t.Fatalf("stdout is not JSON: %s", stdout.String())
		}
	})

	t.Run("distinct file", func(t *testing.T) {
		output := filepath.Join(t.TempDir(), "report.json")
		var stdout, stderr bytes.Buffer
		if code := runWith([]string{"db", "report", "--output", output}, &stdout, &stderr); code != ExitOK {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		if stdout.Len() != 0 || !json.Valid(data) {
			t.Fatalf("stdout=%q output=%s", stdout.String(), data)
		}
	})

	if after := directorySnapshot(t, directory); !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only report changed source storage:\nbefore=%#v\nafter=%#v", before, after)
	}
}

// TestRunDBWith_AcceptanceCompanion_DBOpen covers the dbpkg
// seam path: a missing database file at t.TempDir()/ktx.db is
// created (parent directory + schema migration) by the first
// dbpkg.Open; the second open is a no-op (no error, no schema drift).
//
// This is the integration surface: the `ktx db path` resolver prints
// the path, and any subsequent subcommand that calls dbpkg.Open
// (write, read, list, get, doctor, bench) creates the file on first
// invocation. The test confirms the contract directly via dbpkg.Open
// because the subcommand-level test would also be valid but the
// dbpkg seam is the single source of truth.
func TestRunDBWith_AcceptanceCompanion_DBOpen(t *testing.T) {
	t.Run("missing DB → parent created, schema migrated, re-open no-op", func(t *testing.T) {
		// Stage: t.TempDir()/<parent>/ktx.db where <parent> does
		// NOT exist yet. The path resolver (dbpkg.Open) is
		// responsible for creating the parent.
		base := t.TempDir()
		path := filepath.Join(base, "nested", "ktx.db")
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatalf("pre-state: parent %q should not exist", filepath.Dir(path))
		}

		// First open: creates the parent directory, runs
		// migration, returns a usable *DB.
		d1, err := dbpkg.Open(path)
		if err != nil {
			t.Fatalf("first Open(%q): %v", path, err)
		}
		t.Cleanup(func() { d1.Close() })
		if _, err := os.Stat(filepath.Dir(path)); err != nil {
			t.Errorf("parent %q not created: %v", filepath.Dir(path), err)
		}

		// Insert a row to confirm the schema is real (not
		// just a file with no tables).
		if err := d1.InsertSession(&dbpkg.Session{
			ID:         "zyxwvutsrqponmlk",
			Agent:      "test",
			StartedAt:  "2026-06-18T00:00:00Z",
			LastSeenAt: "2026-06-18T00:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		d1.Close()

		// Second open: re-opens the existing file. The
		// migration is idempotent so the second open must
		// not fail and the row must persist.
		d2, err := dbpkg.Open(path)
		if err != nil {
			t.Fatalf("second Open(%q): %v", path, err)
		}
		t.Cleanup(func() { d2.Close() })
		s, err := d2.GetSession("zyxwvutsrqponmlk")
		if err != nil {
			t.Fatalf("GetSession after re-open: %v", err)
		}
		if s.Agent != "test" {
			t.Errorf("Agent = %q, want test", s.Agent)
		}
	})
}
