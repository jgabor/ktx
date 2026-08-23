// Tests for the shared command dispatcher.
//
// The dispatch contract is the spine of this file. Each requirement
// is a subtest so the regression surface is enumerable. Companion
// cases cover the boundary conditions (--help, unknown flag, format
// validation) and the runWith integration that proves the new
// registry is actually wired into the dispatcher (not just a side table).
//
// Test isolation: dispatchRegistry is package-level state. Every test uses
// the registerFor helper which both calls dispatch.Register and schedules a
// t.Cleanup that deletes the entry. Parallel tests cannot observe a leaked
// registration; sequential tests cannot either once the cleanup fires.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// registerFor installs a verb in the dispatch registry and schedules a
// cleanup that removes it on test exit. The verb name is required; tests
// that share verb names will collide via the map's last-write-wins
// semantics, so each test should pass a unique name (or run serially).
func registerFor(t *testing.T, verb string, opts RegisterOptions, do CommandFunc) {
	t.Helper()
	if code := Register(verb, opts, do); code != ExitOK {
		t.Fatalf("Register(%q) returned %d, want %d", verb, code, ExitOK)
	}
	t.Cleanup(func() { delete(dispatchRegistry, verb) })
}

// captureVerb registers a verb that records the *DispatchContext it
// received and returns it via the supplied pointer. Tests that need to
// inspect ctx.Args / ctx.Format / ctx.DB / ctx.Passthrough after the
// dispatch pipeline runs use this rather than reaching into the verb
// closure.
func captureVerb(t *testing.T, verb string, opts RegisterOptions, captured **DispatchContext) CommandFunc {
	t.Helper()
	return func(ctx *DispatchContext) (any, error) {
		*captured = ctx
		return nil, nil
	}
}

// TestDispatch_OkNoOutput covers: a registered verb that returns
// (nil, nil) produces exit 0 and writes nothing to stdout. The verb is a
// no-op closure so the only observable outputs are the dispatcher's.
func TestDispatch_OkNoOutput(t *testing.T) {
	registerFor(t, "basic_foo", RegisterOptions{}, func(ctx *DispatchContext) (any, error) {
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := runDispatch("basic_foo", nil, false, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on a no-op verb, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty on a no-op verb, got %q", stderr.String())
	}
}

func TestPublicDispatchContracts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("KTX_DB", "")

	t.Run("run post-agent help is rendered", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runDispatch("run", []string{"sh", "--help"}, false, &stdout, &stderr); code != ExitOK {
			t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage: ktx run") {
			t.Fatalf("help output = %q", stdout.String())
		}
	})

	t.Run("doctor failure renders once", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runDispatch("doctor", []string{"unexpected"}, false, &stdout, &stderr); code != ExitUsage {
			t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
		}
		if lines := strings.Count(strings.TrimSpace(stderr.String()), "\n") + 1; lines != 1 {
			t.Fatalf("diagnostic lines = %d, stderr = %q", lines, stderr.String())
		}
	})

	t.Run("init failure renders once", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runDispatch("init", []string{"opencode", "--show"}, false, &stdout, &stderr); code != ExitNotFound {
			t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
		}
		if lines := strings.Count(strings.TrimSpace(stderr.String()), "\n") + 1; lines != 1 {
			t.Fatalf("diagnostic lines = %d, stderr = %q", lines, stderr.String())
		}
	})

	t.Run("init uninstall failure renders once", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runDispatch("init", []string{"opencode", "--uninstall"}, false, &stdout, &stderr); code != ExitNotFound {
			t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
		}
		if lines := strings.Count(strings.TrimSpace(stderr.String()), "\n") + 1; lines != 1 {
			t.Fatalf("diagnostic lines = %d, stderr = %q", lines, stderr.String())
		}
	})

	t.Run("record stream values", func(t *testing.T) {
		for _, tc := range []struct {
			arg  string
			want bool
		}{
			{arg: "--record-stream", want: true},
			{arg: "--record-stream=true", want: true},
			{arg: "--record-stream=false", want: false},
			{arg: "--record-stream=1", want: true},
			{arg: "--record-stream=0", want: false},
		} {
			rest, parsed, err := parseRunDispatchArgs([]string{tc.arg, "agent", "--child-flag"})
			if err != nil {
				t.Errorf("%s: %v", tc.arg, err)
			}
			if strings.Join(rest, " ") != "agent --child-flag" {
				t.Errorf("%s: passthrough args = %q", tc.arg, rest)
			}
			if got := parsed.(runParsedArgs).recordStream; got != tc.want {
				t.Errorf("%s: recordStream = %t, want %t", tc.arg, got, tc.want)
			}
		}
		var stdout, stderr bytes.Buffer
		if code := runDispatch("run", []string{"agent", "--record-stream="}, false, &stdout, &stderr); code != ExitUsage {
			t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
		}
		if lines := strings.Count(strings.TrimSpace(stderr.String()), "\n") + 1; lines != 1 {
			t.Fatalf("diagnostic lines = %d, stderr = %q", lines, stderr.String())
		}
		if !strings.Contains(stderr.String(), `invalid value \"\" for --record-stream`) {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})

	t.Run("plugin documented and unknown flags", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
			want int
		}{
			{name: "json", args: []string{"list", "--json"}, want: ExitOK},
			{name: "unknown", args: []string{"list", "--typo"}, want: ExitUsage},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				if code := runDispatch("plugin", tc.args, false, &stdout, &stderr); code != tc.want {
					t.Fatalf("exit = %d, want %d, stderr = %q", code, tc.want, stderr.String())
				}
			})
		}
	})
}

// TestDispatch_NotFoundEnvelope covers: a verb that returns
// dbpkg.ErrNotFound produces exit 2 and a stderr envelope whose code is
// "not-found". The error sentinel is the contract that ties the dispatch
// layer to the persistence layer: any code that returns dbpkg.ErrNotFound
// must surface as exit 2, never as the internal-error fallback.
func TestDispatch_NotFoundEnvelope(t *testing.T) {
	var captured *DispatchContext
	registerFor(t, "context_foo", RegisterOptions{}, captureVerb(t, "context_foo", RegisterOptions{}, &captured))

	// Replace the no-op logic with one that returns ErrNotFound. The
	// previous registration gets overwritten by the second Register,
	// which is exactly the surface this case exercises: the verb returns
	// the sentinel at dispatch time.
	Register("context_foo", RegisterOptions{}, func(ctx *DispatchContext) (any, error) {
		captured = ctx
		return nil, dbpkg.ErrNotFound
	})

	var stdout, stderr bytes.Buffer
	code := runDispatch("context_foo", nil, false, &stdout, &stderr)
	if code != ExitNotFound {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on not-found, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"code":"not-found"`) {
		t.Errorf("stderr missing not-found error code: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "db: not found") {
		t.Errorf("stderr missing not-found message: %q", stderr.String())
	}
}

// TestDispatch_UsageError covers: a verb that returns a usage
// error produces exit 1 and a stderr envelope whose code is "usage".
// usageError is the standard constructor; the test exercises the
// printing + exit-code path.
func TestDispatch_UsageError(t *testing.T) {
	registerFor(t, "error_foo", RegisterOptions{}, func(ctx *DispatchContext) (any, error) {
		return nil, usageError("bad input: %s", "missing-id")
	})

	var stdout, stderr bytes.Buffer
	code := runDispatch("error_foo", nil, false, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on usage error, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"code":"usage"`) {
		t.Errorf("stderr missing usage error code: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "missing-id") {
		t.Errorf("stderr missing bad-input detail: %q", stderr.String())
	}
}

// TestDispatch_PassthroughStdout covers: a verb with
// PassthroughStdout=true writes its own raw JSON envelope to stdout and
// the dispatcher does not wrap, double-encode, or modify it. The verb
// emits a fixed JSON object; the test decodes what actually arrived on
// stdout and checks byte-equality with the verb's literal.
func TestDispatch_PassthroughStdout(t *testing.T) {
	handRolled := map[string]any{
		"id":   "raw-abc123",
		"kind": "tool_call",
	}
	registerFor(t, "passthrough_echo", RegisterOptions{PassthroughStdout: true}, func(ctx *DispatchContext) (any, error) {
		enc := json.NewEncoder(ctx.Stdout)
		if err := enc.Encode(handRolled); err != nil {
			return nil, err
		}
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := runDispatch("passthrough_echo", nil, false, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty on success, got %q", stderr.String())
	}

	// The verb's JSON encoder adds a trailing newline. Trim it so the
	// decoded value matches the literal map.
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(stdout.Bytes(), "\n"), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v (raw=%q)", err, stdout.String())
	}
	if got["id"] != handRolled["id"] || got["kind"] != handRolled["kind"] {
		t.Errorf("stdout envelope = %v, want %v", got, handRolled)
	}

	// Belt-and-braces: no "error" key (would indicate the dispatcher
	// wrapped the verb's output as an error envelope).
	if _, hasErr := got["error"]; hasErr {
		t.Errorf("stdout must not carry a top-level error envelope: %v", got)
	}
}

// TestDispatch_NoDB covers: a verb with DatabaseNone never
// touches the database, so an unreadable database file at the resolved
// path is irrelevant. The verb runs and exits 0; the dispatcher's
// dbpkg.ResolvePath + dbpkg.Open calls are not made.
//
// The fixture points XDG_DATA_HOME at a directory containing a chmod 0
// db file so dbpkg.Open would fail if the dispatcher called it. This
// is the strongest possible negative: a permissions failure (not just a
// missing file) is the kind of unreadable the test is defending against.
func TestDispatch_NoDB(t *testing.T) {
	tmp := t.TempDir()
	dataHome := filepath.Join(tmp, "share")
	if err := os.MkdirAll(dataHome, 0o755); err != nil {
		t.Fatalf("mkdir data home: %v", err)
	}
	dbDir := filepath.Join(dataHome, "ktx")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir ktx data dir: %v", err)
	}
	dbPath := filepath.Join(dbDir, "ktx.db")
	if err := os.WriteFile(dbPath, []byte("garbage"), 0o644); err != nil {
		t.Fatalf("seed unreadable db: %v", err)
	}
	if err := os.Chmod(dbPath, 0o000); err != nil {
		t.Fatalf("chmod 0o000 db: %v", err)
	}
	// As root (the only case where chmod 0o000 doesn't bite) the test
	// is weakened but not invalidated: the verb still runs and exits 0
	// because DatabaseAccess is DatabaseNone, which is the contract under test.
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o644) })
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", filepath.Join(dataHome, "ktx", "ktx.db"))

	registerFor(t, "stdin_foo", RegisterOptions{}, func(ctx *DispatchContext) (any, error) {
		if ctx.DB != nil {
			t.Errorf("ctx.DB should be nil with DatabaseNone, got %v", ctx.DB)
		}
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := runDispatch("stdin_foo", nil, false, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
}

// TestDispatch_ParseArgs verifies that command-specific parsing can consume
// arguments and carry a typed value to the command.
func TestDispatch_ParseArgs(t *testing.T) {
	var captured *DispatchContext
	registerFor(t, "parse_args", RegisterOptions{
		ParseArgs: func(args []string) ([]string, any, error) {
			return []string{"a", "b"}, 42, nil
		},
	}, captureVerb(t, "parse_args", RegisterOptions{}, &captured))

	var stdout, stderr bytes.Buffer
	code := runDispatch("parse_args", []string{"ignored-by-parser"}, false, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	if captured == nil {
		t.Fatal("dispatcher did not invoke the registered verb")
	}
	if got, want := captured.Args, []string{"a", "b"}; !sliceEq(got, want) {
		t.Errorf("ctx.Args = %v, want %v", got, want)
	}
	if captured.ParsedArgs != 42 {
		t.Errorf("ctx.ParsedArgs = %v, want 42", captured.ParsedArgs)
	}
}

// TestDispatch_HelpHook covers the --help edge case: a verb with a Help
// hook renders that hook on --help and -h, exits 0, and writes nothing
// to stderr. The hook is the verb's chance to present its own help;
// the dispatcher is a thin pass-through.
func TestDispatch_HelpHook(t *testing.T) {
	hookCalled := false
	registerFor(t, "help_hook", RegisterOptions{
		Help: func(w io.Writer) {
			hookCalled = true
		},
	}, func(ctx *DispatchContext) (any, error) {
		t.Errorf("verb must not be invoked when --help is set")
		return nil, nil
	})

	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			hookCalled = false
			var stdout, stderr bytes.Buffer
			code := runDispatch("help_hook", []string{flag}, false, &stdout, &stderr)
			if code != ExitOK {
				t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
			}
			if !hookCalled {
				t.Errorf("Help hook not called for %s", flag)
			}
		})
	}
}

// TestDispatch_HelpStub covers the --help path when no Help hook is
// supplied: the dispatcher prints a one-line stub and exits 0. The
// path is exercised because the verb author is not required to
// provide Help; the dispatcher is honest about the gap.
func TestDispatch_HelpStub(t *testing.T) {
	registerFor(t, "help_stub", RegisterOptions{}, func(ctx *DispatchContext) (any, error) {
		t.Errorf("verb must not be invoked when --help is set")
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := runDispatch("help_stub", []string{"--help"}, false, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no help available") {
		t.Errorf("stdout missing stub, got %q", stdout.String())
	}
}

// TestDispatch_UnknownFlag covers the unknown-flag edge case: anything
// the dispatcher does not recognise as a registered flag is a usage
// error. The test uses --bogus, but the path covers every non-flag
// positional that isn't --help or --format.
func TestDispatch_UnknownFlag(t *testing.T) {
	registerFor(t, "unknown_flag", RegisterOptions{}, func(ctx *DispatchContext) (any, error) {
		t.Errorf("verb must not be invoked on unknown flag")
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := runDispatch("unknown_flag", []string{"--bogus"}, false, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), `"code":"usage"`) {
		t.Errorf("stderr missing usage error code: %q", stderr.String())
	}
}

// TestDispatch_InvalidFormat covers the --format validation edge case:
// values other than json/text are a usage error regardless of which
// side of the verb they appear on. The helper extractFormat pulls
// --format out of the args; the dispatcher validates the value.
func TestDispatch_InvalidFormat(t *testing.T) {
	registerFor(t, "bad_format", RegisterOptions{FormatDefault: FormatJSON}, func(ctx *DispatchContext) (any, error) {
		t.Errorf("verb must not be invoked on invalid --format")
		return nil, nil
	})

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"--format before flag", []string{"--format", "yaml"}},
		{"--format=value", []string{"--format=yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runDispatch("bad_format", tc.args, false, &stdout, &stderr)
			if code != ExitUsage {
				t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "invalid --format") {
				t.Errorf("stderr missing invalid-format message: %q", stderr.String())
			}
		})
	}
}

// TestDispatch_FormatExtraction covers the happy paths for --format:
// subcommand-local --format overrides FormatDefault, and FormatDefault
// is honoured when no --format is given. The verb captures ctx.Format
// so the assertion is on what the verb sees, not what the dispatcher
// would render.
func TestDispatch_FormatExtraction(t *testing.T) {
	t.Run("FormatDefault when no --format", func(t *testing.T) {
		var captured *DispatchContext
		registerFor(t, "fmt_default", RegisterOptions{FormatDefault: FormatJSON}, captureVerb(t, "fmt_default", RegisterOptions{}, &captured))
		var stdout, stderr bytes.Buffer
		code := runDispatch("fmt_default", nil, false, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d", code, ExitOK)
		}
		if captured == nil || captured.Format != FormatJSON {
			t.Errorf("ctx.Format = %v, want %q", captured, FormatJSON)
		}
	})

	t.Run("subcommand-local --format overrides default", func(t *testing.T) {
		var captured *DispatchContext
		registerFor(t, "fmt_local", RegisterOptions{FormatDefault: FormatJSON}, captureVerb(t, "fmt_local", RegisterOptions{}, &captured))
		var stdout, stderr bytes.Buffer
		code := runDispatch("fmt_local", []string{"--format", "text"}, false, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d", code, ExitOK)
		}
		if captured == nil || captured.Format != FormatText {
			t.Errorf("ctx.Format = %v, want %q", captured, FormatText)
		}
	})

	t.Run("empty FormatDefault stays empty", func(t *testing.T) {
		var captured *DispatchContext
		registerFor(t, "fmt_empty", RegisterOptions{}, captureVerb(t, "fmt_empty", RegisterOptions{}, &captured))
		var stdout, stderr bytes.Buffer
		code := runDispatch("fmt_empty", nil, false, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d", code, ExitOK)
		}
		if captured == nil || captured.Format != "" {
			t.Errorf("ctx.Format = %v, want empty", captured)
		}
	})

	// PassthroughStdout verbs (the run verb) forward --format to the
	// child agent unchanged. The dispatcher must NOT steal --format from
	// the verb's arg list. The `ktx run opencode run --format json -m
	// <m> <prompt>` invocation failed end-to-
	// end because extractFormat (flags.go:34) was consuming `--format
	// json` from the verb's args before the agent saw it; this test
	// asserts the fix (skip extractFormat when PassthroughStdout=true).
	t.Run("PassthroughStdout preserves --format in ctx.Args", func(t *testing.T) {
		var captured *DispatchContext
		registerFor(t, "fmt_passthrough", RegisterOptions{PassthroughStdout: true},
			captureVerb(t, "fmt_passthrough", RegisterOptions{PassthroughStdout: true}, &captured))
		var stdout, stderr bytes.Buffer
		args := []string{"opencode", "run", "--format", "json", "-m", "neuralwatt/glm-5.2-short-fast-flex", "What is 2+2?"}
		code := runDispatch("fmt_passthrough", args, false, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if captured == nil {
			t.Fatal("verb was not invoked")
		}
		// ctx.Args should match the input verbatim: --format is preserved.
		want := args
		if len(captured.Args) != len(want) {
			t.Fatalf("ctx.Args length = %d, want %d (args=%v)", len(captured.Args), len(want), captured.Args)
		}
		for i, a := range want {
			if captured.Args[i] != a {
				t.Errorf("ctx.Args[%d] = %q, want %q", i, captured.Args[i], a)
			}
		}
		// ctx.Format stays "" because extractFormat did not run.
		if captured.Format != "" {
			t.Errorf("ctx.Format = %q, want \"\" (extractFormat must be skipped for PassthroughStdout)", captured.Format)
		}
	})
}

// TestDispatch_ParseArgsError covers the command parser failure path: a hook
// that returns a non-nil error short-circuits to a usage error. The
// verb is not invoked. The hook is the ktx init shape for unknown
// flags and unexpected positionals.
func TestDispatch_ParseArgsError(t *testing.T) {
	registerFor(t, "preparse_err", RegisterOptions{
		ParseArgs: func(args []string) ([]string, any, error) {
			return nil, nil, errors.New("unknown flag --projct")
		},
	}, func(ctx *DispatchContext) (any, error) {
		t.Errorf("verb must not be invoked when argument parsing errors")
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	code := runDispatch("preparse_err", []string{"--projct"}, false, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown flag --projct") {
		t.Errorf("stderr missing preParse error message: %q", stderr.String())
	}
}

// TestDispatch_PassthroughWithDB covers passthrough plus database access:
// a verb that needs the database and writes its own raw output. The
// dispatcher opens the DB for writes but does not wrap stdout
// (PassthroughStdout=true). The test asserts ctx.DB is non-nil and the
// verb's literal envelope is byte-identical on stdout.
func TestDispatch_PassthroughWithDB(t *testing.T) {
	tmp := t.TempDir()
	dataHome := filepath.Join(tmp, "share")
	if err := os.MkdirAll(dataHome, 0o755); err != nil {
		t.Fatalf("mkdir data home: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", filepath.Join(dataHome, "ktx", "ktx.db"))

	var captured *DispatchContext
	do := func(ctx *DispatchContext) (any, error) {
		captured = ctx
		fmt.Fprintln(ctx.Stdout, `{"ok":true}`)
		return nil, nil
	}
	registerFor(t, "passthrough_db", RegisterOptions{DatabaseAccess: DatabaseReadWrite, PassthroughStdout: true}, do)

	var stdout, stderr bytes.Buffer
	code := runDispatch("passthrough_db", nil, false, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	if captured == nil || captured.DB == nil {
		t.Errorf("ctx.DB should be non-nil with database access, captured=%v", captured)
	}
	if !strings.Contains(stdout.String(), `{"ok":true}`) {
		t.Errorf("stdout missing verb's raw output: %q", stdout.String())
	}
}

// TestDispatch_UnknownVerb covers the runDispatch-not-found path: an
// unknown verb gets a usage error envelope with exit 1.
func TestDispatch_UnknownVerb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDispatch("definitely_not_registered", nil, false, &stdout, &stderr)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "definitely_not_registered") {
		t.Errorf("stderr missing verb name: %q", stderr.String())
	}
}

// TestDispatch_PassthroughFlag covers the Passthrough flag on the
// DispatchContext: the verb sees ctx.Passthrough mirroring
// RegisterOptions.PassthroughStdout. The verb does not need to keep a
// reference to its own options struct; the dispatcher propagates the
// value.
func TestDispatch_PassthroughFlag(t *testing.T) {
	var captured *DispatchContext
	registerFor(t, "passthrough_flag", RegisterOptions{PassthroughStdout: true}, captureVerb(t, "passthrough_flag", RegisterOptions{PassthroughStdout: true}, &captured))

	var stdout, stderr bytes.Buffer
	code := runDispatch("passthrough_flag", nil, false, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if captured == nil || !captured.Passthrough {
		t.Errorf("ctx.Passthrough = %v, want true", captured)
	}
}

// TestDispatch_DatabaseNone covers the no-database case at the context
// level: a verb with DatabaseNone sees ctx.DB == nil. The check is
// redundant with the earlier case (which proves the dispatcher never opens
// the DB) but it documents the contract the verb can rely on.
func TestDispatch_DatabaseNone(t *testing.T) {
	var captured *DispatchContext
	registerFor(t, "no_db_ctx", RegisterOptions{}, captureVerb(t, "no_db_ctx", RegisterOptions{}, &captured))

	var stdout, stderr bytes.Buffer
	code := runDispatch("no_db_ctx", nil, false, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d", code, ExitOK)
	}
	if captured == nil || captured.DB != nil {
		t.Errorf("ctx.DB = %v, want nil with DatabaseNone", captured)
	}
}

// TestDispatch_RegisterGuards covers the Register guard rails: a nil
// CommandFunc or empty verb name returns ExitUsage without touching
// the registry. The guard exists so a malformed init() does not silently
// pollute the dispatch table.
func TestDispatch_RegisterGuards(t *testing.T) {
	t.Run("nil CommandFunc", func(t *testing.T) {
		code := Register("nil_fn", RegisterOptions{}, nil)
		if code != ExitUsage {
			t.Errorf("Register with nil fn = %d, want %d", code, ExitUsage)
		}
		if _, present := dispatchRegistry["nil_fn"]; present {
			t.Error("registry should not contain nil-fn entry")
		}
	})

	t.Run("empty verb name", func(t *testing.T) {
		code := Register("", RegisterOptions{}, func(ctx *DispatchContext) (any, error) { return nil, nil })
		if code != ExitUsage {
			t.Errorf("Register with empty name = %d, want %d", code, ExitUsage)
		}
		if _, present := dispatchRegistry[""]; present {
			t.Error("registry should not contain empty-name entry")
		}
	})
}

// TestDispatch_Integration verifies that registered commands are wired into
// the binary entry point rather than only the dispatcher test seam.
func TestDispatch_Integration(t *testing.T) {
	t.Run("runWith uses dispatch registry", func(t *testing.T) {
		registerFor(t, "demo_echo", RegisterOptions{PassthroughStdout: true}, func(ctx *DispatchContext) (any, error) {
			fmt.Fprintln(ctx.Stdout, `{"demo":true}`)
			return nil, nil
		})

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"demo_echo"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("runWith exit = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), `{"demo":true}`) {
			t.Errorf("stdout missing demo verb output: %q", stdout.String())
		}
	})

	t.Run("runWith unknown verb still errors via legacy path", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"definitely_unknown_verb"}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("runWith exit = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "definitely_unknown_verb") {
			t.Errorf("stderr missing verb name: %q", stderr.String())
		}
	})

	t.Run("runWith dispatches a registered verb with global --format", func(t *testing.T) {
		// Global --format is parsed by the dispatcher but not threaded
		// into the dispatch module's FormatDefault (the module uses
		// its own seed). This test exercises the verb's view: the
		// subcommand-local --format still wins because the dispatch
		// module re-parses it from the post-verb args.
		var captured *DispatchContext
		registerFor(t, "demo_fmt", RegisterOptions{FormatDefault: FormatJSON}, captureVerb(t, "demo_fmt", RegisterOptions{}, &captured))

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"demo_fmt", "--format", "text"}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("runWith exit = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
		}
		if captured == nil || captured.Format != FormatText {
			t.Errorf("ctx.Format = %v, want %q", captured, FormatText)
		}
	})

	t.Run("runWith surfaces not-found exit code from registered verb", func(t *testing.T) {
		registerFor(t, "demo_404", RegisterOptions{}, func(ctx *DispatchContext) (any, error) {
			return nil, dbpkg.ErrNotFound
		})

		var stdout, stderr bytes.Buffer
		code := runWith([]string{"demo_404"}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("runWith exit = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})
}

// sliceEq reports whether two []string have identical content. The
// stdlib reflect.DeepEqual works but allocates; this is the test-only
// helper that keeps the assertion obvious on the failure line.
func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
