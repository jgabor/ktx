package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// TestExecuteRun_RunCmdInjection covers the runCmd seam acceptance
// contract: a stub runCmd that returns a fake *exec.Cmd (writing "ok"
// to stdout and exiting 0) is honoured by executeRun, the agent's
// stdout passes through to ktx's stdout, and the exit code is 0.
//
// The stub replaces package-level runCmd via t.Cleanup so production
// behavior is restored after the test, and parallel tests cannot
// observe a substituted seam.
func TestExecuteRun_RunCmdInjection(t *testing.T) {
	t.Run("stub runCmd is invoked and stdout passes through", func(t *testing.T) {
		// Isolate filesystem state: testutil sets XDG_DATA_HOME so
		// dbpkg.DefaultPath resolves to a per-test file. Without
		// this, executeRun would open the user's real database.
		testutil.WithTempXDG(t)

		// Swap runCmd for a stub that ignores the agent name and
		// arguments and instead invokes /bin/echo ok. This proves
		// executeRun went through the seam (and not via a hardcoded
		// exec.CommandContext reference): the substituted fake ran,
		// and its output reached ktx's stdout. /bin/echo is
		// available on every unix CI host and exits 0 with "ok\n"
		// on stdout — the canonical stand-in for a "fake *exec.Cmd
		// writing 'ok' to stdout and exiting 0" without forking a
		// bespoke test binary.
		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })

		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "/bin/echo", "ok")
			return cmd
		}

		var stdout, stderr bytes.Buffer
		code := executeRun("fake-agent", []string{"ignored"}, Options{Format: FormatJSON}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr.String())
		}
		if got := stdout.String(); got != "ok\n" {
			t.Fatalf("stdout = %q, want %q (agent stdout did not pass through)", got, "ok\n")
		}
	})

	t.Run("production runCmd is exec.CommandContext by default", func(t *testing.T) {
		// Without explicit substitution, runCmd must equal the
		// exec.CommandContext function so production code paths
		// spawn real subprocesses. This is the contract that makes
		// the seam safe to override in tests: a fresh package
		// state has the canonical implementation, and only tests
		// that explicitly assign runCmd see the substitute.
		//
		// Comparing func values directly is fragile across package
		// boundaries because Go forbids == on funcs from different
		// packages. We compare function names via runtime.FuncForPC
		// instead — both refs resolve to the same code pointer for
		// the same stdlib function.
		if got := funcName(runCmd); got != "os/exec.CommandContext" {
			t.Fatalf("default runCmd = %s, want os/exec.CommandContext", got)
		}
	})
}

// funcName returns the qualified name of fn (e.g.
// "exec.CommandContext") so tests can assert against the canonical
// production binding without depending on Go's unstable func-value
// equality semantics across package boundaries. Returns "<nil>" when
// fn is nil and "<unknown>" when the runtime cannot resolve the
// function (which only happens for synthesized closures).
func funcName(fn any) string {
	if fn == nil {
		return "<nil>"
	}
	v := reflect.ValueOf(fn)
	pc := v.Pointer()
	if pc == 0 {
		return "<unknown>"
	}
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "<unknown>"
	}
	return f.Name()
}

// TestRunRunWith_Dispatch covers the run subcommand's pass/fail
// surface. The wrapper path drives agent subprocesses via runCmd;
// the subcommand itself is a thin dispatcher that wires the global
// and post-agent --quiet, --help, and the agent positional argument.
func TestRunRunWith_Dispatch(t *testing.T) {
	t.Run("missing agent argument exits 1 (usage)", func(t *testing.T) {
		// KTX_DB is set so the run flow can open the database
		// at the very first step (resolve path). Without an
		// agent argument, the wrapper short-circuits before
		// any DB work, so the database is irrelevant; we set
		// it to avoid an unrelated error.
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")

		var stdout, stderr bytes.Buffer
		code := runRunWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "missing agent command") {
			t.Errorf("stderr missing missing-agent message: %q", stderr.String())
		}
	})

	t.Run("--help prints help and exits 0", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")

		var stdout, stderr bytes.Buffer
		code := runRunWith([]string{"--help"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx run") {
			t.Errorf("stdout missing ktx run header: %q", stdout.String())
		}
	})

	t.Run("quiet flag suppresses run summary on stderr", func(t *testing.T) {
		// /bin/echo ok → agent exits 0, stdout = "ok\n", no
		// tool events. The summary is the one-line JSON
		// envelope to stderr; --quiet suppresses it.
		testutil.WithTempXDG(t)

		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/echo", "ok")
		}

		var stdout, stderr bytes.Buffer
		code := runRunWith([]string{"--quiet", "fake-agent"}, Options{Format: FormatJSON, Quiet: false}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if strings.Contains(stderr.String(), "summary") {
			t.Errorf("stderr should not contain summary when --quiet, got %q", stderr.String())
		}
	})

	t.Run("non-zero agent exit propagates as ktx run exit code", func(t *testing.T) {
		// /bin/false exits 1; the wrapper's exit code matches
		// the child's ("exit code matches the child's exit
		// code"). The run summary still prints (wrapper
		// convention) because --quiet is not set.
		testutil.WithTempXDG(t)

		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/false")
		}

		var stdout, stderr bytes.Buffer
		code := runRunWith([]string{"fake-agent"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1 (agent failed; stderr=%q)", code, stderr.String())
		}
	})

	t.Run("tool_use event persisted to tool_calls table", func(t *testing.T) {
		// runCmd seam acceptance plus a higher-level
		// integration check: an agent emitting one tool_use
		// JSONL event causes a tool_call row to be persisted
		// in the database, queryable via db.GetRecord.
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", os.Getenv("XDG_DATA_HOME")+"/ktx/ktx.db")

		// /bin/sh -c "echo ..." emits one JSONL line
		// carrying a tool_use event. The wrapper forwards
		// the line to parser.ParseEvent, fills in the
		// session ID, and persists the tool_call.
		const payload = `{"type":"tool_use","timestamp":"2026-06-18T12:00:00Z","part":{"type":"tool","tool":"bash","callID":"call_001","state":{"input":{"command":"echo hello world"},"output":"hello world\n"}}}`
		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf '%s\\n' '"+payload+"'")
		}

		var stdout, stderr bytes.Buffer
		code := runRunWith([]string{"fake-agent"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// The tool_use line should be passed through
		// verbatim (transparent proxy) and a tool_call
		// row should be persisted in the database.
		if !strings.Contains(stdout.String(), `"tool_use"`) {
			t.Errorf("stdout missing tool_use passthrough: %q", stdout.String())
		}

		// Open the database and look for any tool_call
		// row. The wrapper generates a fresh nanoid so
		// we cannot hardcode the ID; instead, list and
		// count.
		d, err := dbpkg.Open(os.Getenv("KTX_DB"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Sample tool_call IDs from the database.
		_, toolIDs, err := d.SampleIDs(100)
		if err != nil {
			t.Fatalf("SampleIDs: %v", err)
		}
		if len(toolIDs) == 0 {
			t.Fatalf("no tool_call rows persisted; expected at least one")
		}
		// Confirm at least one row has the expected tool name.
		found := false
		for _, id := range toolIDs {
			rec, err := d.GetToolCall(id)
			if err != nil {
				continue
			}
			if rec.Tool == "bash" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no persisted tool_call has tool=bash; toolIDs=%v", toolIDs)
		}
	})

	t.Run("--record-stream persists recorded_streams row with NULL bench_run_id", func(t *testing.T) {
		// ktx run --record-stream captures the agent's JSONL stdout
		// into the recorded_streams table alongside the transparent
		// passthrough. The row's bench_run_id is NULL (ktx run has
		// no bench_runs parent). The captured stream is re-readable
		// via GetRecordedStreamBySession so --replay can exercise it.
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", os.Getenv("XDG_DATA_HOME")+"/ktx/ktx.db")

		// The fake agent emits one tool_use event and one text event
		// — the same shape the existing tool_use test uses. The
		// stream must round-trip with the exact bytes the agent
		// emitted (teeAndParse writes each line + \n).
		const payload = `{"type":"tool_use","timestamp":"2026-06-18T12:00:00Z","part":{"type":"tool","tool":"bash","callID":"call_001","state":{"input":{"command":"echo hi"},"output":"hi\n"}}}` + "\n" +
			`{"type":"text","part":{"text":"done"}}`
		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf '%s\n' '"+payload+"'")
		}

		var stdout, stderr bytes.Buffer
		code := runRunWith([]string{"--record-stream", "fake-agent"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// The agent's stdout must still pass through
		// (transparent proxy) — --record-stream captures an extra
		// copy but does not suppress passthrough.
		if !strings.Contains(stdout.String(), `"tool_use"`) {
			t.Errorf("stdout missing tool_use passthrough: %q", stdout.String())
		}

		// Open the DB and verify a recorded_streams row exists for
		// the session.
		d, err := dbpkg.Open(os.Getenv("KTX_DB"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Find the session created by executeRun (it uses
		// OpenNamedSession with agent="fake-agent").
		sessions, err := d.ListSessions()
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		var sessionID string
		for _, s := range sessions {
			if s.Agent == "fake-agent" {
				sessionID = s.ID
				break
			}
		}
		if sessionID == "" {
			t.Fatalf("no session with agent=fake-agent found; sessions=%v", sessions)
		}

		// The recorded_stream must exist and have a NULL bench_run_id.
		rs, err := d.GetRecordedStreamBySession(sessionID)
		if err != nil {
			t.Fatalf("GetRecordedStreamBySession: %v", err)
		}
		if rs.BenchRunID != "" {
			t.Errorf("BenchRunID = %q, want empty (NULL — ktx run has no bench_runs parent)", rs.BenchRunID)
		}
		if rs.StreamFormat != "opencode-jsonl-1" {
			t.Errorf("StreamFormat = %q, want opencode-jsonl-1", rs.StreamFormat)
		}
		if rs.CapturedAt == "" {
			t.Error("CapturedAt is empty; want RFC3339Nano timestamp")
		}

		// content_hash must match SHA-256 of the captured stream.
		recomputed := dbpkg.ContentHash(rs.StreamText)
		if recomputed != rs.ContentHash {
			t.Errorf("content_hash mismatch: persisted %q, recomputed %q", rs.ContentHash, recomputed)
		}

		// The captured stream must contain both events (the tool_use
		// and the text).
		if !strings.Contains(rs.StreamText, `"tool_use"`) {
			t.Errorf("StreamText missing tool_use event: %q", rs.StreamText)
		}
		if !strings.Contains(rs.StreamText, `"text"`) {
			t.Errorf("StreamText missing text event: %q", rs.StreamText)
		}
	})

	t.Run("--record-stream after agent command also captures", func(t *testing.T) {
		// The post-agent --record-stream form (ktx run <agent>
		// --record-stream) must work just like the pre-agent form.
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", os.Getenv("XDG_DATA_HOME")+"/ktx/ktx.db")

		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/echo", "ok")
		}

		var stdout, stderr bytes.Buffer
		code := runRunWith([]string{"fake-agent", "--record-stream"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Verify a recorded_streams row exists.
		d, err := dbpkg.Open(os.Getenv("KTX_DB"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessions, err := d.ListSessions()
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		for _, s := range sessions {
			if s.Agent == "fake-agent" {
				rs, err := d.GetRecordedStreamBySession(s.ID)
				if err != nil {
					t.Fatalf("GetRecordedStreamBySession: %v", err)
				}
				if rs.BenchRunID != "" {
					t.Errorf("BenchRunID = %q, want empty", rs.BenchRunID)
				}
				return
			}
		}
		t.Fatal("no recorded_streams row found for fake-agent session")
	})

	t.Run("--record-stream=false does not capture", func(t *testing.T) {
		// The --record-stream=false form must explicitly disable
		// recording, so no recorded_streams row is persisted.
		testutil.WithTempXDG(t)
		t.Setenv("KTX_DB", os.Getenv("XDG_DATA_HOME")+"/ktx/ktx.db")

		origRunCmd := runCmd
		t.Cleanup(func() { runCmd = origRunCmd })
		runCmd = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/echo", "ok")
		}

		var stdout, stderr bytes.Buffer
		code := runRunWith([]string{"--record-stream=false", "fake-agent"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}

		// Verify no recorded_streams row exists.
		d, err := dbpkg.Open(os.Getenv("KTX_DB"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessions, err := d.ListSessions()
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		for _, s := range sessions {
			if s.Agent == "fake-agent" {
				_, err := d.GetRecordedStreamBySession(s.ID)
				if err == nil {
					t.Fatal("GetRecordedStreamBySession succeeded; want ErrNotFound (--record-stream=false should not capture)")
				}
				if !errors.Is(err, dbpkg.ErrNotFound) {
					t.Fatalf("GetRecordedStreamBySession: %v, want ErrNotFound", err)
				}
				return
			}
		}
	})
}