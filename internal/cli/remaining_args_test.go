package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
)

func TestRemainingOwnedArguments(t *testing.T) {
	for _, mode := range []string{"dispatch", "binary"} {
		t.Run(mode, func(t *testing.T) {
			binary := os.Getenv("KTX_TEST_RUN_BINARY")
			if mode == "binary" && binary == "" {
				t.Skip("set KTX_TEST_RUN_BINARY to the mage-built local CLI")
			}
			for _, tc := range []struct {
				name            string
				args            []string
				code            int
				out, diagnostic string
			}{
				{"init false", []string{"init", "--project=false", "opencode", "--show", "--help=false"}, 2, "", "not installed"},
				{"init format", []string{"init", "--format", "json", "opencode", "--show"}, 2, "", "not installed"},
				{"init unknown", []string{"init", "opencode", "--wat"}, 1, "", "unknown flag"},
				{"init escaped", []string{"init", "--show", "--", "opencode"}, 2, "", "not installed"},
				{"init consumed help", []string{"init", "opencode", "--format", "--help"}, 1, "", "invalid --format"},
				{"init consumed format", []string{"init", "opencode", "--format", "--format"}, 1, "", "invalid --format"},
				{"init missing format", []string{"init", "opencode", "--format"}, 1, "", "requires a value"},
				{"db consumed help", []string{"db", "path", "--path", "--help"}, 0, "--help", ""},
				{"db consumed format", []string{"db", "path", "--path", "--format"}, 0, "--format", ""},
				{"db format consumes path", []string{"db", "--format", "--path", "path"}, 1, "", "invalid --format"},
				{"db trailing unknown", []string{"db", "path", "--wat"}, 1, "", "unexpected argument"},
				{"db missing path", []string{"db", "path", "--path"}, 1, "", "requires a value"},
				{"db escaped path", []string{"db", "path", "--", "--path=ignored"}, 1, "", "unexpected argument"},
				{"db report consumed help", []string{"db", "--output", "--help", "report"}, 4, "", "no such file"},
				{"db report consumed format", []string{"db", "report", "--output", "--format", "--include-identifiers=false"}, 4, "", "no such file"},
				{"db false help", []string{"db", "path", "--help=false"}, 0, "ktx.db", ""},
				{"stats normal", []string{"stats", "--window", "30m", "summary", "--help=false"}, 0, "schema_version", ""},
				{"stats consumed help", []string{"stats", "live-validation", "--collection", "--help"}, 1, "", "--from"},
				{"stats consumed format", []string{"stats", "live-validation", "--collection", "--format"}, 1, "", "--from"},
				{"stats format consumes limit", []string{"stats", "--format", "--limit", "1"}, 1, "", "invalid --format"},
				{"stats missing limit", []string{"stats", "--limit"}, 1, "", "requires a value"},
				{"stats unknown", []string{"stats", "summary", "--wat"}, 1, "", "wat"},
				{"stats empty escaped tail", []string{"stats", "summary", "--"}, 0, "schema_version", ""},
				{"stats escaped", []string{"stats", "--", "--format=text"}, 1, "", "unexpected argument"},
				{"stats numeric consumed help", []string{"stats", "--limit", "--help"}, 1, "", "invalid --limit"},
				{"bench around subcommand", []string{"bench", "--spec", "not-a-spec", "report"}, 1, "", "spec"},
				{"bench normal spec around subcommand", []string{"bench", "--spec", "exact-match", "report"}, 0, "", ""},
				{"bench consumed help", []string{"bench", "report", "--spec", "--help"}, 1, "", "spec"},
				{"bench consumed format", []string{"bench", "report", "--spec", "--format"}, 1, "", "spec"},
				{"bench false help", []string{"bench", "report", "--help=false"}, 0, "", ""},
				{"bench missing spec", []string{"bench", "report", "--spec"}, 1, "", "requires a value"},
				{"bench unknown", []string{"bench", "report", "--wat"}, 1, "", "flag provided but not defined"},
				{"bench booleans false", []string{"bench", "--preflight=false", "compare", "--build=false"}, 1, "", "--spec"},
				{"bench escaped", []string{"bench", "report", "--", "--format"}, 2, "", "no bench_run"},
				{"bench help", []string{"bench", "--help", "compare"}, 0, "Active semantic IDs", ""},
				{"bench nested corpus", []string{"bench", "corpus", "generate", "--wat"}, 1, "", "flag provided but not defined"},
				{"plugin literal description", []string{"plugin", "add", "owned-test", "cat", "--description", "--format"}, 0, "owned-test", ""},
				{"plugin consumed help", []string{"plugin", "--description", "--help", "add", "owned-test", "cat"}, 0, "owned-test", ""},
				{"plugin missing description", []string{"plugin", "add", "owned-test", "cat", "--description"}, 1, "", "requires a value"},
				{"plugin description then format", []string{"plugin", "add", "owned-test", "cat", "--description", "--format", "--format", "json"}, 0, "owned-test", ""},
				{"plugin escaped", []string{"plugin", "add", "--", "owned-test", "--help"}, 0, "owned-test", ""},
				{"plugin false help", []string{"plugin", "list", "--help=false"}, 0, "builtin", ""},
				{"plugin unknown", []string{"plugin", "list", "--wat"}, 1, "", "unknown argument"},
				{"format empty", []string{"stats", "--format="}, 1, "", "invalid --format"},
				{"global format precedence", []string{"--format", "text", "stats"}, 0, "", ""},
				{"local format precedence", []string{"--format", "text", "stats", "--format", "json"}, 0, "[]", ""},
				{"last format wins", []string{"stats", "--format=", "--format=json"}, 0, "[]", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := testutil.WithTempXDG(t)
					t.Setenv("HOME", root)
					t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
					t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
					t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
					t.Setenv("KTX_DB", "")
					t.Chdir(root)
					var out, diagnostic bytes.Buffer
					var code int
					if mode == "binary" {
						cmd := exec.Command(binary, tc.args...)
						cmd.Stdout, cmd.Stderr = &out, &diagnostic
						if err := cmd.Run(); err != nil {
							var exit *exec.ExitError
							if !errors.As(err, &exit) {
								t.Fatal(err)
							}
							code = exit.ExitCode()
						}
					} else {
						code = runWith(tc.args, &out, &diagnostic)
					}
					if code != tc.code || !strings.Contains(out.String(), tc.out) || !strings.Contains(diagnostic.String(), tc.diagnostic) {
						t.Fatalf("code=%d stdout=%s stderr=%s", code, &out, &diagnostic)
					}
					if tc.code != 0 && out.Len() != 0 {
						t.Errorf("failure produced success output: %s", &out)
					}
					if tc.name == "global format precedence" && strings.HasPrefix(out.String(), "[") {
						t.Error("global text format ignored")
					}
					if want := map[string]string{"plugin literal description": "--format", "plugin consumed help": "--help", "plugin description then format": "--format"}[tc.name]; want != "" {
						content, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "ktx", "ktx.toml"))
						if err != nil || !strings.Contains(string(content), `description = "`+want+`"`) {
							t.Fatalf("description not preserved: %s (%v)", content, err)
						}
					}
					if _, err := os.Stat(filepath.Join(os.Getenv("XDG_DATA_HOME"), "ktx", "ktx.db")); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("created database: %v", err)
					}
				})
			}
		})
	}
}

type ackFailureSink struct{ short bool }

func (w ackFailureSink) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestWriteAcknowledgementFailureKeepsCommit(t *testing.T) {
	for _, closeSession := range []bool{false, true} {
		for _, short := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "call", true: "close"}[closeSession], map[bool]string{false: "error", true: "short"}[short]}, "/"), func(t *testing.T) {
				t.Setenv("KTX_DB", filepath.Join(t.TempDir(), "owned.db"))
				d, _ := writeTestDB(t)
				preSeedSession(t, d, "zyxwvutsrqponmlk")
				payload := validToolCallPayload(t)
				if closeSession {
					payload = `{"event_type":"session_close","session_id":"zyxwvutsrqponmlk","timestamp":"2026-06-18T12:01:00Z"}`
				}
				var diagnostic bytes.Buffer
				if code := runWriteWith(nil, Options{Format: FormatJSON}, strings.NewReader(payload), ackFailureSink{short}, &diagnostic); code != ExitInternal || !strings.Contains(diagnostic.String(), "committed") {
					t.Fatalf("code=%d stderr=%s", code, &diagnostic)
				}
				var count int
				query := "SELECT count(*) FROM tool_calls"
				if closeSession {
					query = "SELECT count(*) FROM sessions WHERE ended_at = '2026-06-18T12:01:00Z'"
				}
				if err := d.DB().QueryRow(query).Scan(&count); err != nil || count != 1 {
					t.Fatalf("committed count=%d err=%v", count, err)
				}
				// Retry the same identity; acknowledgement loss must not duplicate evidence.
				wantRetry := ExitInternal // duplicate tool-call identity is rejected, not inserted twice
				if closeSession {
					wantRetry = ExitOK
				}
				if code := runWriteWith(nil, Options{Format: FormatJSON}, strings.NewReader(payload), io.Discard, &diagnostic); code != wantRetry {
					t.Fatalf("retry=%d %s", code, &diagnostic)
				}
				if err := d.DB().QueryRow(query).Scan(&count); err != nil || count != 1 {
					t.Fatalf("retry count=%d err=%v", count, err)
				}
			})
		}
	}
}

func TestRemainingValidationBeforeReadiness(t *testing.T) {
	for _, args := range [][]string{
		{"stats", "live-validation", "--collection", "--help"},
		{"stats", "live-validation", "--collection", "--format"},
		{"stats", "summary", "--wat"},
		{"stats", "--limit=0"},
		{"db", "report", "--output"},
		{"bench", "report", "--spec", "--format"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
				t.Setenv(key, filepath.Join(root, key))
			}
			path := filepath.Join(root, "explicit-corrupt.db")
			before := []byte("not a database; malformed arguments must win")
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KTX_DB", path)
			var out, diagnostic bytes.Buffer
			if code := runWith(args, &out, &diagnostic); code != ExitUsage {
				t.Fatalf("code=%d %s", code, &diagnostic)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("database changed: %v", err)
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("database side effect %s: %v", suffix, err)
				}
			}
		})
	}
}

func TestRemainingArgumentHelp(t *testing.T) {
	for _, verb := range []string{"init", "db", "stats", "bench", "plugin"} {
		t.Run(verb, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			if code := runWith([]string{verb, "--help"}, &out, &diagnostic); code != 0 {
				t.Fatalf("code=%d %s", code, &diagnostic)
			}
			for _, text := range []string{"Value flags consume the next token", "-- ends option handling", "--help=false", "Local --format overrides global --format"} {
				if !strings.Contains(out.String(), text) {
					t.Errorf("help missing %q", text)
				}
			}
		})
	}
}

func TestRemainingOwnedDatabaseCLI(t *testing.T) {
	binary := os.Getenv("KTX_TEST_RUN_BINARY")
	if binary == "" {
		t.Skip("set KTX_TEST_RUN_BINARY to the mage-built local CLI")
	}
	for _, args := range [][]string{
		{"stats", "--window", "30m", "summary", "--format", "json"},
		{"db", "--include-identifiers=false", "report", "--output", "-"},
		{"bench", "report", "--help=false", "--format", "json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
				t.Setenv(key, filepath.Join(root, key))
			}
			path := filepath.Join(root, "owned.db")
			t.Setenv("KTX_DB", path)
			d, _ := writeTestDB(t)
			preSeedSession(t, d, "zyxwvutsrqponmlk")
			if _, err := writeLogic(nil, strings.NewReader(validToolCallPayload(t)), d, io.Discard); err != nil {
				t.Fatal(err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, args...)
			var out, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diagnostic
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v: %s", err, &diagnostic)
			}
			if out.Len() == 0 || diagnostic.Len() != 0 {
				t.Fatalf("stdout=%s stderr=%s", &out, &diagnostic)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("read-only query changed source: %v", err)
			}
		})
	}
}
