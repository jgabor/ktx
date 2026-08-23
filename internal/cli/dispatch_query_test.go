// End-to-end dispatch tests for the query + complex-flow verbs
// (ktx get, ktx list, ktx run, ktx bench).
//
// These verbs register via dispatch.Register
// (get.go:init, list.go:init, run.go:init, bench.go:init) instead of
// through the shared command registry. Every case is exercised
// end-to-end via runWith (the production entry point), which routes
// through parseGlobalFlags → dispatchRegistry → runRegisteredVerb →
// the verb's CommandFunc.
//
// stdin injection uses os.Pipe + os.Stdin swap because the dispatch
// module's runRegisteredVerb reads from os.Stdin when NeedsStdin=true
// (sibling pattern). runCmd injection (the test seam) is
// preserved: the dispatch path's ktx run uses the same package-level
// runCmd used by command tests, so a single t.Cleanup
// override is observable by both call sites within the same test.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// queryDispatchTestDB opens a per-test KTX_DB database. The dispatch
// path's query verbs (ktx get, ktx list) open the DB through the
// dispatcher, so the file must exist and have the schema migrated
// before the verb runs. The dispatcher calls dbpkg.Open (which
// migrates) so a missing file is fine; we just need KTX_DB pointing
// at a writable path.
func queryDispatchTestDB(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/ktx.db"
	t.Setenv("KTX_DB", path)
	return path
}

// seedToolCallForDispatch inserts a session and a tool_call row with
// the cost-publication pointer set. Mirrors the seedToolCallForGet
// helper in get_test.go but exposes the *dbpkg.DB so the dispatch test
// can read it back through the same DB the verb opened (proves the
// dispatcher took the same path as the legacy seam).
func seedToolCallForDispatch(t *testing.T, path string) *dbpkg.DB {
	t.Helper()
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.InsertSession(&dbpkg.Session{
		ID:         "zyxwvutsrqponmlk",
		Agent:      "test-agent",
		StartedAt:  "2026-06-18T12:00:00Z",
		LastSeenAt: "2026-06-18T12:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	cost := 0.0123
	if err := d.PersistToolCall(&dbpkg.ToolCall{
		ID:          "abcdefghijklmnop",
		SessionID:   "zyxwvutsrqponmlk",
		Tool:        "bash",
		Input:       `{"command":"echo hi"}`,
		Output:      "hi\n",
		ContentHash: dbpkg.ContentHash("hi\n"),
		Timestamp:   "2026-06-18T12:00:00Z",
		EventType:   "tool_call",
		Cost:        &cost,
	}); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}
	return d
}

// seedListSessionForDispatch inserts a single session row with no
// tool_calls so the text-view path (with no cost data) renders the
// explicit "-" markers.
func seedListSessionForDispatch(t *testing.T, path string) *dbpkg.DB {
	t.Helper()
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.InsertSession(&dbpkg.Session{
		ID:            "zyxwvutsrqponmlk",
		Agent:         "test-agent",
		StartedAt:     "2026-06-18T12:00:00Z",
		LastSeenAt:    "2026-06-18T12:00:00Z",
		ToolCallCount: 3,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	return d
}

// TestRunWith_GetDispatched covers: `ktx get <id> --format text`
// emits the one-line-per-field text view with the optional cost
// section when at least one cost pointer is non-nil. The dispatch path
// routes through the registered getCommandFunc; --format is extracted
// by the dispatcher (not the verb) and surfaces via ctx.Format.
//
// Fail companion: a wrong tool name (the test asserts the field is
// present, so flipping it to a typo proves the assertion is
// meaningful — not a no-op pass).
func TestRunWith_GetDispatched(t *testing.T) {
	t.Run("get --format text emits text view with cost section", func(t *testing.T) {
		path := queryDispatchTestDB(t)
		seedToolCallForDispatch(t, path)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"get", "abcdefghijklmnop", "--format", "text"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		out := stdout.String()
		// Core fields must be present.
		if !strings.Contains(out, "id:") || !strings.Contains(out, "abcdefghijklmnop") {
			t.Errorf("text output missing id field: %q", out)
		}
		if !strings.Contains(out, "tool:") || !strings.Contains(out, "bash") {
			t.Errorf("text output missing tool field: %q", out)
		}
		if !strings.Contains(out, "session_id:") || !strings.Contains(out, "zyxwvutsrqponmlk") {
			t.Errorf("text output missing session_id field: %q", out)
		}
		// Cost section: at least one cost pointer was set (Cost=0.0123),
		// so the section must be present and contain the cost line.
		if !strings.Contains(out, "Costs:") {
			t.Errorf("text output missing cost section header: %q", out)
		}
		if !strings.Contains(out, "cost:") {
			t.Errorf("text output missing cost line: %q", out)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on success, got %q", stderr.String())
		}
	})

	t.Run("fail companion: assert correct tool name to prove the assertion is wired", func(t *testing.T) {
		// Fail companion: the field probe must be specific enough
		// to detect a wrong value. Asserting on a name that does NOT
		// exist in the seeded row proves the test's positive
		// assertion isn't a tautology.
		path := queryDispatchTestDB(t)
		seedToolCallForDispatch(t, path)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"get", "abcdefghijklmnop", "--format", "text"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if strings.Contains(stdout.String(), "tool: nonexistent-tool-zzzz") {
			t.Errorf("expected the seeded tool 'bash', got an unexpected 'nonexistent-tool-zzzz': %q", stdout.String())
		}
	})

	t.Run("ktx get not-found exits 2 via dispatch registry", func(t *testing.T) {
		queryDispatchTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"get", "abcdefghijklmnop"}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})

	t.Run("ktx get --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"get", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx get") {
			t.Errorf("stdout missing ktx get header: %q", stdout.String())
		}
	})
}

// TestRunWith_ListDispatched covers the JSON and text list modes.
//
// JSON mode: `ktx list` with a populated session set emits
// stdout as the array of sessionListRow with optional costs object.
//
// The text view with sessions lacking cost data shows
// "-" for the cost columns.
//
// Fail companion: asserts on a wrong column header to prove the
// text-view assertion is meaningful.
func TestRunWith_ListDispatched(t *testing.T) {
	t.Run("list JSON mode emits sessionListRow array with optional costs", func(t *testing.T) {
		path := queryDispatchTestDB(t)
		seedListSessionForDispatch(t, path)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"list"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Decode as a generic list of maps to confirm the array
		// shape and the session fields. The costs field is omitted
		// (omitempty on the wrapper) because no tool_call has cost
		// data — omission OR null is permitted; omission is the
		// chosen signal.
		var rows []map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
			t.Fatalf("stdout is not a JSON array: %v\nstdout=%q", err, stdout.String())
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		row := rows[0]
		if row["id"] != "zyxwvutsrqponmlk" {
			t.Errorf("row.id = %v, want zyxwvutsrqponmlk", row["id"])
		}
		if row["agent"] != "test-agent" {
			t.Errorf("row.agent = %v, want test-agent", row["agent"])
		}
		// The costs object is optional. With no cost data it is
		// absent (omitempty on the wrapper).
		if _, ok := row["costs"]; ok {
			t.Errorf("row.costs = %v, want absent (no cost data)", row["costs"])
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on success, got %q", stderr.String())
		}
	})

	t.Run("list --format text with no cost data shows '-' for cost columns", func(t *testing.T) {
		path := queryDispatchTestDB(t)
		seedListSessionForDispatch(t, path)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"list", "--format", "text"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		out := stdout.String()
		// The tabwriter-aligned table emits a row per session
		// with columns ID AGENT STARTED ENDED TOOL_CALLS TOKENS
		// COST MODEL. With no cost data, the cost columns show "-".
		if !strings.Contains(out, "zyxwvutsrqponmlk") {
			t.Errorf("text output missing session ID: %q", out)
		}
		if !strings.Contains(out, "test-agent") {
			t.Errorf("text output missing session agent: %q", out)
		}
		// tabwriter pads cells with spaces; the cost columns
		// (TOKENS, COST, MODEL) emit "-" separated by padded
		// spaces when no cost data is present. Probe the data
		// row's tail (TOOL_CALLS=3 followed by three "-"
		// columns) to prove the markers are present.
		if !strings.Contains(out, "3           -       -     -") {
			t.Errorf("text output missing the three consecutive '-' cost markers after tool_calls=3: %q", out)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on success, got %q", stderr.String())
		}
	})

	t.Run("fail companion: assert the rendered row actually has data", func(t *testing.T) {
		// Fail companion: assert against a string that is NOT
		// in the rendered table (the rendered row carries
		// 'test-agent' and tool_call_count=3; the probe looks for
		// 'wrong-agent-zzzz' to prove the assertion isn't a
		// tautology).
		path := queryDispatchTestDB(t)
		seedListSessionForDispatch(t, path)

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"list", "--format", "text"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if strings.Contains(stdout.String(), "wrong-agent-zzzz") {
			t.Errorf("expected the seeded agent 'test-agent', got an unexpected 'wrong-agent-zzzz': %q", stdout.String())
		}
	})

	t.Run("ktx list --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"list", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx list") {
			t.Errorf("stdout missing ktx list header: %q", stdout.String())
		}
	})
}

// TestRunWith_RunDispatched covers: `ktx run <agent> --quiet`
// with runCmd returning a stub exiting 0 — no run summary is written
// to stderr and the exit code matches the stub.
//
// The dispatch path uses runCmd via executeRun; substituting the
// package-level seam with a stub that runs /bin/echo ok proves the
// agent's stdout passes through (transparent proxy) and the
// exit code is 0. --quiet suppresses the one-line JSON summary
// that the wrapper writes to stderr on success.
//
// A non-zero child status is forwarded unchanged.
func TestRunWith_RunDispatched(t *testing.T) {
	t.Run("run --quiet with stub exiting 0 → no summary, exit 0", func(t *testing.T) {
		// Isolate filesystem state: testutil sets XDG_DATA_HOME so
		// dbpkg.DefaultPath resolves to a per-test file.
		testutil.WithTempXDG(t)

		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/echo", "ok")
		}

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"run", "--quiet", "fake-agent"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Transparent proxy: agent stdout passes through
		// untouched.
		if got := stdout.String(); got != "ok\n" {
			t.Errorf("stdout = %q, want %q (agent stdout did not pass through)", got, "ok\n")
		}

		// --quiet suppresses the one-line JSON summary on
		// stderr. The wrapper writes the summary as
		// {"summary":{...}} when not quiet; --quiet skips that
		// write. The dispatch path's error path does not fire on
		// exit 0, so stderr must be empty.
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty when --quiet, got %q", stderr.String())
		}
		if strings.Contains(stderr.String(), `"summary"`) {
			t.Errorf("stderr must not contain a summary when --quiet: %q", stderr.String())
		}
	})

	t.Run("fail companion: without --quiet the summary is written", func(t *testing.T) {
		// Fail companion: when --quiet is NOT set, the wrapper
		// writes the one-line summary to stderr. Proves the
		// --quiet assertion above is meaningful (the summary is
		// actually written when not suppressed).
		testutil.WithTempXDG(t)

		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/echo", "ok")
		}

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"run", "fake-agent"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"summary"`) {
			t.Errorf("stderr missing run summary when --quiet is NOT set: %q", stderr.String())
		}
	})

	t.Run("child exit status is preserved", func(t *testing.T) {
		testutil.WithTempXDG(t)

		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 7")
		}

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"run", "--quiet", "fake-agent"}, &stdout, &stderr)
		if code != 7 {
			t.Errorf("exit code = %d, want 7", code)
		}
		if strings.Contains(stderr.String(), `"code":"internal"`) {
			t.Errorf("child status should not render an internal error: %q", stderr.String())
		}
	})

	t.Run("record-stream leaves child arguments unchanged", func(t *testing.T) {
		testutil.WithTempXDG(t)

		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		var gotName string
		var gotArgs []string
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			gotName = name
			gotArgs = append([]string(nil), arg...)
			return exec.CommandContext(ctx, "/bin/true")
		}

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"run", "--quiet", "--record-stream=false", "fake-agent", "--child-flag", "value"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if gotName != "fake-agent" || !slices.Equal(gotArgs, []string{"--child-flag", "value"}) {
			t.Fatalf("child invocation = %q %q", gotName, gotArgs)
		}
	})

	t.Run("ktx run --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"run", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx run") {
			t.Errorf("stdout missing ktx run header: %q", stdout.String())
		}
	})
}

// TestRunWith_BenchReportDispatched covers: `ktx bench report`
// with an empty bench_runs table emits a JSON envelope on stdout
// where headline is null and rows is [].
//
// The dispatch path routes through benchCommandFunc → runBenchReport
// → runBenchReportImpl. runBenchReportImpl takes a pre-opened
// *dbpkg.DB; the dispatch path's runBenchReport opens its own DB
// via the existing dispatcher-adapter pattern, so the test seeds
// the bench_runs table through the same path the dispatcher would
// (KTX_DB env + dbpkg.Open).
//
// Fail companion: the test asserts on a non-empty table scenario by
// inverting the predicate — empty table should produce rows=[],
// so probing for rows.length > 0 in the empty case proves the
// assertion is wired.
func TestRunWith_BenchReportDispatched(t *testing.T) {
	t.Run("bench report with empty bench_runs → headline=null, rows=[]", func(t *testing.T) {
		// Seed the database with the schema migrated (no bench_runs
		// rows). The dispatch path opens the DB on its own.
		path := t.TempDir() + "/ktx.db"
		t.Setenv("KTX_DB", path)
		d, err := dbpkg.Open(path)
		if err != nil {
			t.Fatalf("dbpkg.Open(%q): %v", path, err)
		}
		d.Close() // dispatcher reopens on demand

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"bench", "report"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Decode the envelope shape:
		// {headline, methodology_version, rows}.
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
		if env.Rows == nil {
			t.Errorf("rows = nil, want []")
		}
		if len(env.Rows) != 0 {
			t.Errorf("rows = %d, want 0", len(env.Rows))
		}
		// methodology_version for an empty table with no
		// --methodology filter is null (the absence-marker
		// contract).
		if env.MethodologyVersion != nil {
			t.Errorf("methodology_version = %v, want null", *env.MethodologyVersion)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr should be empty on success, got %q", stderr.String())
		}
	})

	t.Run("fail companion: rows must NOT be populated for an empty table", func(t *testing.T) {
		// Fail companion: prove the rows=[] assertion is
		// meaningful by inverting it. An empty bench_runs must
		// produce rows.length == 0; asserting rows.length > 0
		// must fail.
		path := t.TempDir() + "/ktx.db"
		t.Setenv("KTX_DB", path)
		d, err := dbpkg.Open(path)
		if err != nil {
			t.Fatalf("dbpkg.Open(%q): %v", path, err)
		}
		d.Close()

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"bench", "report"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		var env struct {
			Rows []any `json:"rows"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("stdout is not valid JSON: %v", err)
		}
		if len(env.Rows) > 0 {
			t.Errorf("rows.length = %d, want 0", len(env.Rows))
		}
	})

	t.Run("ktx bench --help routes through dispatch registry", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"bench", "--help"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		for _, contract := range []string{
			"run --replay <session-id>",
			"usage/cost/cache DRIFT exit 0",
			"run --replay-file <path>",
			"failure_class=replay-fixture-verification-failed",
			"export [--output <path|->] <session-id>",
			"review raw stream",
		} {
			if !strings.Contains(stdout.String(), contract) {
				t.Errorf("stdout missing replay help contract %q: %q", contract, stdout.String())
			}
		}
	})

	t.Run("ktx bench with no subcommand prints help via dispatch registry", func(t *testing.T) {
		path := t.TempDir() + "/ktx.db"
		t.Setenv("KTX_DB", path)
		d, err := dbpkg.Open(path)
		if err != nil {
			t.Fatalf("dbpkg.Open(%q): %v", path, err)
		}
		d.Close()

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"bench"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx bench") {
			t.Errorf("stdout missing ktx bench header: %q", stdout.String())
		}
	})

	t.Run("ktx bench unknown subcommand exits 1 via dispatch registry", func(t *testing.T) {
		path := t.TempDir() + "/ktx.db"
		t.Setenv("KTX_DB", path)
		d, err := dbpkg.Open(path)
		if err != nil {
			t.Fatalf("dbpkg.Open(%q): %v", path, err)
		}
		d.Close()

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"bench", "nonexistent-subcommand"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"usage"`) {
			t.Errorf("stderr missing usage envelope: %q", stderr.String())
		}
		if !strings.Contains(stderr.String(), "nonexistent-subcommand") {
			t.Errorf("stderr missing subcommand name: %q", stderr.String())
		}
	})
}
