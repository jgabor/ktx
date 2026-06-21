// Table-driven tests for `ktx bench` (internal/cli/bench.go).
//
// Acceptance coverage:
//
//   - AC 5 (Task 3): an empty bench_runs table queried via
//     runBenchReportImpl emits a JSON envelope on stdout where
//     `headline` is null and `rows` is []. The test calls the impl
//     directly (not the runBenchReport wrapper) per the spec: the
//     wrapper adds the database-open + format-arg parsing, but the
//     envelope shape is the impl's responsibility.
//
// Companion cases assert: list mode with a single row carries a
// non-null headline and a one-element rows array; single-row mode
// emits the BenchRun directly; not-found id returns a not-found
// error envelope (mapped to exit 2 by the wrapper).
package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// benchTestDB returns an in-memory *dbpkg.DB with the full schema
// migrated. The bench impl does not touch the filesystem; an
// in-memory database is the cleanest fixture.
func benchTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestRunBenchReportImpl_EmptyEnvelope covers AC 5: an empty
// bench_runs table produces a JSON envelope with `headline: null`
// and `rows: []`. The envelope is the only output on stdout; the
// implementation does not write to stderr on success.
//
// The test calls runBenchReportImpl directly (per the spec) so the
// envelope shape and headline/rows computation are exercised
// without the runBenchReport wrapper's database-open + flag-parse
// surface.
func TestRunBenchReportImpl_EmptyEnvelope(t *testing.T) {
	t.Run("AC5: empty bench_runs → headline=null, rows=[]", func(t *testing.T) {
		d := benchTestDB(t)
		var stdout, stderr bytes.Buffer

		err := runBenchReportImpl(&stdout, &stderr, d, nil, FormatJSON, "")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		// Decode the JSON envelope. The shape is the
		// plan-spec contract: {headline, methodology_version,
		// rows}. The headline for an empty table with no
		// filter is null (the AC6 contract).
		var env struct {
			Headline           *float64  `json:"headline"`
			MethodologyVersion *string   `json:"methodology_version"`
			Rows               []any     `json:"rows"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env.Headline != nil {
			t.Errorf("headline = %v, want null", *env.Headline)
		}
		if env.Rows == nil {
			t.Errorf("rows = nil, want []")
		}
		if len(env.Rows) != 0 {
			t.Errorf("rows = %d, want 0", len(env.Rows))
		}
		// methodology_version for an empty table with no
		// filter is null (the AC6 contract: "the absence
		// marker").
		if env.MethodologyVersion != nil {
			t.Errorf("methodology_version = %v, want null", *env.MethodologyVersion)
		}
		// stderr should be empty on success.
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty, got %q", stderr.String())
		}
	})

	t.Run("AC5 companion: filter on empty table → rows=[], filter surfaces in envelope", func(t *testing.T) {
		// When --methodology is set against an empty table,
		// methodology_version falls back to the filter
		// string (per the spec).
		d := benchTestDB(t)
		var stdout, stderr bytes.Buffer

		err := runBenchReportImpl(&stdout, &stderr, d, nil, FormatJSON, "v1")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		var env struct {
			Headline           *float64 `json:"headline"`
			MethodologyVersion *string  `json:"methodology_version"`
			Rows               []any    `json:"rows"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env.Headline != nil {
			t.Errorf("headline = %v, want null", *env.Headline)
		}
		if env.Rows == nil || len(env.Rows) != 0 {
			t.Errorf("rows = %v, want []", env.Rows)
		}
		if env.MethodologyVersion == nil || *env.MethodologyVersion != "v1" {
			t.Errorf("methodology_version = %v, want v1 (filter fallback)", env.MethodologyVersion)
		}
	})
}

// TestRunBenchReportImpl_NonEmpty covers the list-mode shape with
// data. Companion to AC 5: a single row's headline is the
// row's cost / max(score, 1e-9), the rows array carries the row
// directly, methodology_version is the row's value.
func TestRunBenchReportImpl_NonEmpty(t *testing.T) {
	t.Run("list mode with one row → headline=row's value, rows=[B]", func(t *testing.T) {
		d := benchTestDB(t)
		cost := 0.000825
		if err := d.InsertBenchRun(&dbpkg.BenchRun{
			ID:                 "ailjmiiapmzoetub",
			Agent:              "anthropic/claude-sonnet-4-20250514",
			Model:              "claude-sonnet-4-20250514",
			Task:               "addition_v1",
			Score:              1.0,
			TokensIn:           150,
			TokensOut:          25,
			Cost:               &cost,
			MethodologyVersion: "v1",
			Timestamp:          1718928000,
		}); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}

		var stdout, stderr bytes.Buffer
		err := runBenchReportImpl(&stdout, &stderr, d, nil, FormatJSON, "")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		var env struct {
			Headline           *float64  `json:"headline"`
			MethodologyVersion *string   `json:"methodology_version"`
			Rows               []map[string]any `json:"rows"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if env.Headline == nil {
			t.Fatalf("headline = nil, want 0.000825")
		}
		if *env.Headline != 0.000825 {
			t.Errorf("headline = %v, want 0.000825", *env.Headline)
		}
		if env.MethodologyVersion == nil || *env.MethodologyVersion != "v1" {
			t.Errorf("methodology_version = %v, want v1", env.MethodologyVersion)
		}
		if len(env.Rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(env.Rows))
		}
		row := env.Rows[0]
		if row["id"] != "ailjmiiapmzoetub" {
			t.Errorf("row.id = %v, want ailjmiiapmzoetub", row["id"])
		}
	})

	t.Run("single-row mode emits the BenchRun directly", func(t *testing.T) {
		d := benchTestDB(t)
		cost := 0.000825
		if err := d.InsertBenchRun(&dbpkg.BenchRun{
			ID:                 "ailjmiiapmzoetub",
			Agent:              "anthropic/claude-sonnet-4-20250514",
			Model:              "claude-sonnet-4-20250514",
			Task:               "addition_v1",
			Score:              1.0,
			TokensIn:           150,
			TokensOut:          25,
			Cost:               &cost,
			MethodologyVersion: "v1",
			Timestamp:          1718928000,
		}); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}

		var stdout, stderr bytes.Buffer
		err := runBenchReportImpl(&stdout, &stderr, d, []string{"ailjmiiapmzoetub"}, FormatJSON, "")
		if err != nil {
			t.Fatalf("runBenchReportImpl: %v", err)
		}

		// Single-row mode emits the BenchRun directly
		// (no rows array, no headline envelope).
		var row struct {
			ID                string  `json:"id"`
			Cost              *float64 `json:"cost"`
			MethodologyVersion string  `json:"methodology_version"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &row); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout.String())
		}
		if row.ID != "ailjmiiapmzoetub" {
			t.Errorf("id = %q, want ailjmiiapmzoetub", row.ID)
		}
		if row.Cost == nil || *row.Cost != 0.000825 {
			t.Errorf("cost = %v, want 0.000825", row.Cost)
		}
	})

	t.Run("single-row mode not-found returns a *Error{CodeNotFound}", func(t *testing.T) {
		d := benchTestDB(t)
		var stdout, stderr bytes.Buffer

		err := runBenchReportImpl(&stdout, &stderr, d, []string{"nonexistentrowid"}, FormatJSON, "")
		if err == nil {
			t.Fatalf("runBenchReportImpl: nil error; want not-found")
		}
		// The error must be a *Error{CodeNotFound} so the
		// dispatcher adapter maps it to ExitNotFound (exit 2).
		// We assert on the public exit-code mapping via
		// exitCodeOf (defined in errors.go) and the message
		// content. The notFoundError constructor in this
		// package creates a fresh *Error{CodeNotFound}
		// without wrapping dbpkg.ErrNotFound; that is the
		// public contract — the wrapper adapter inspects
		// the *Error's Code field, not the chain.
		if got := exitCodeOf(err); got != ExitNotFound {
			t.Errorf("exitCodeOf(err) = %d, want %d (ExitNotFound)", got, ExitNotFound)
		}
		// The error message includes the missing id so the
		// user can grep for the offending record.
		if !strings.Contains(err.Error(), "nonexistentrowid") {
			t.Errorf("err.Error() = %q, want to mention the missing id", err.Error())
		}
		_ = stderr
	})
}

// TestRunBenchWith_Dispatch covers the bench subcommand dispatcher
// surface: --help, unknown subcommand, no subcommand. The bench
// run/report specific paths are covered by runBenchReportImpl
// (above) and the runBenchRun flow (not in scope for Task 3 —
// bench run requires a methodology file and an agent binary).
func TestRunBenchWith_Dispatch(t *testing.T) {
	t.Run("no args prints help and exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx bench") {
			t.Errorf("stdout missing ktx bench header: %q", stdout.String())
		}
	})

	t.Run("--help prints help and exits 0", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchWith([]string{"--help"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx bench") {
			t.Errorf("stdout missing ktx bench header: %q", stdout.String())
		}
	})

	t.Run("unknown subcommand exits 1 (usage)", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchWith([]string{"nonexistent-subcommand"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "nonexistent-subcommand") {
			t.Errorf("stderr missing subcommand name: %q", stderr.String())
		}
	})
}
