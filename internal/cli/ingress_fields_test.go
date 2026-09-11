package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/parser"
)

func ingressEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"KTX_DB", "KTX_SESSION_ID", "KTX_COLLECTION_ID"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"} {
		t.Setenv(key, t.TempDir())
	}
}

func ingressLocalCommand(t *testing.T, binary, path string, args ...string) *exec.Cmd {
	t.Helper()
	ingressEnvironment(t)
	// Hook and run have no database flag. Fresh application roots point only
	// at this test's explicitly owned database; KTX_DB remains unset.
	dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "ktx")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(dir, "ktx.db")); err != nil {
		t.Fatal(err)
	}
	return exec.Command(binary, args...)
}

type ingressOutputCase struct{ name, raw, want string }

func ingressOutputCases(codex bool) []ingressOutputCase {
	cases := []ingressOutputCase{
		{"absent", "", ""},
		{"null", "null", ""},
		{"bare-empty", `""`, ""},
		{"bare-string", `"native"`, "native"},
		{"absent-field", `{"secondary":"other"}`, `{"secondary":"other"}`},
		{"empty-field", `{"output":"","text":"other","metadata":{}}`, ""},
		{"nonempty-field", `{"output":"native","text":"other"}`, "native"},
		{"number", `42`, `42`},
		{"nonstring-field", `{"output":42,"text":"other"}`, `{"output":42,"text":"other"}`},
		{"null-field", `{"output":null}`, `{"output":null}`},
	}
	if codex {
		cases = append(cases,
			ingressOutputCase{"empty-primary", `{"aggregated_output":"","output":"other","text":"third"}`, ""},
			ingressOutputCase{"null-primary", `{"aggregated_output":null,"output":"other"}`, "other"},
			ingressOutputCase{"malformed-primary", `{"aggregated_output":42,"output":"other"}`, `{"aggregated_output":42,"output":"other"}`},
			ingressOutputCase{"malformed-secondary", `{"aggregated_output":"native","output":42}`, `{"aggregated_output":"native","output":42}`},
			ingressOutputCase{"empty-text", `{"text":""}`, ""},
		)
	}
	return cases
}

func ingressHookPayload(codex bool, raw string) string {
	if codex {
		body := `{"hook_event_name":"PostToolUse","session_id":"native-session","tool_use_id":"native-call","tool_name":"Bash","tool_input":{"command":"test"},"cwd":"/offline","transcript_path":"/offline/transcript","permission_mode":"default"`
		if raw != "" {
			body += `,"tool_response":` + raw
		}
		return body + `}`
	}
	body := `{"runtime":"opencode","input":{"tool":"bash","sessionID":"native-session","callID":"native-call","args":{"command":"test"}}`
	if raw != "" {
		body += `,"output":` + raw
	}
	return body + `}`
}

func assertIngressOutput(t *testing.T, d *dbpkg.DB, codex bool, response []byte, want string) {
	t.Helper()
	if codex {
		var envelope codexHookOutput
		if err := json.Unmarshal(response, &envelope); err != nil || envelope.Decision != "" || envelope.Reason != "" {
			t.Fatalf("Codex passthrough changed: %s (%v)", response, err)
		}
	} else {
		var envelope hookEnvelope
		if err := json.Unmarshal(response, &envelope); err != nil || envelope.Output != want {
			t.Fatalf("OpenCode visible output: %s, want %q (%v)", response, want, err)
		}
	}
	var count int
	var output string
	if err := d.DB().QueryRow(`SELECT count(*), output FROM tool_calls`).Scan(&count, &output); err != nil || count != 1 || output != want {
		t.Fatalf("persisted count=%d output=%q, want 1 %q (%v)", count, output, want, err)
	}
}

func TestIngressHookOutputPresence(t *testing.T) {
	for _, codex := range []bool{false, true} {
		for _, tc := range ingressOutputCases(codex) {
			t.Run(fmt.Sprintf("codex=%t/%s", codex, tc.name), func(t *testing.T) {
				ingressEnvironment(t)
				hookChdirTemp(t)
				d := hookTestDB(t)
				var out, diag bytes.Buffer
				if _, err := hookLogic(strings.NewReader(ingressHookPayload(codex, tc.raw)), d, &out, &diag); err != nil {
					t.Fatal(err)
				}
				assertIngressOutput(t, d, codex, out.Bytes(), tc.want)
			})
		}
	}
}

func TestIngressWrapperIncompleteNativeID(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, field := range []string{"", `,"callID":""`, `,"callID":null`, `,"callID":42`, `,"callID":{}`, `,"callID":" \t "`} {
			t.Run(fmt.Sprintf("local=%t/%s", local, field), func(t *testing.T) {
				binary := os.Getenv("KTX_INGRESS_CLI")
				if local && binary == "" {
					t.Skip("set KTX_INGRESS_CLI to the locally built binary")
				}
				ingressEnvironment(t)
				root := t.TempDir()
				path := filepath.Join(root, "evidence.db")
				d, err := dbpkg.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				if !local {
					mustWrapSession(t, d, "wrapper-owner")
				}
				line := `{"type":"tool_use","sessionID":"native-session","part":{"type":"tool","tool":"bash","id":"display-part"` + field + `,"state":{"status":"completed","input":{},"output":""}}}`
				_, native, ok := parser.ParseEventWithCorrelation([]byte(line))
				if !ok || native.CallID == "display-part" {
					t.Errorf("part ID promoted to native identity: %+v", native)
				}
				var out, diag bytes.Buffer
				stream := line + "\n" + line + "\n"
				if local {
					fixture := filepath.Join(root, "stream.jsonl")
					if err := os.WriteFile(fixture, []byte(stream), 0o600); err != nil {
						t.Fatal(err)
					}
					cmd := ingressLocalCommand(t, binary, path, "run", "--", "/bin/cat", fixture)
					cmd.Dir, cmd.Stdout, cmd.Stderr = root, &out, &diag
					if err := cmd.Run(); err != nil {
						t.Fatalf("wrapper: %v %s", err, &diag)
					}
				} else if n := teeAndParse(strings.NewReader(stream), &out, d, "wrapper-owner", &diag); n != 2 || diag.Len() != 0 {
					t.Fatalf("wrapper n=%d diagnostics=%s", n, &diag)
				}
				if out.String() != stream {
					t.Fatal("wrapper changed visible stream")
				}
				var count int
				if err := d.DB().QueryRow(`SELECT count(DISTINCT id) FROM tool_calls`).Scan(&count); err != nil || count != 2 {
					t.Fatalf("incomplete retries count=%d, want 2 (%v)", count, err)
				}
			})
		}
	}
}

// Run with the locally built binary to exercise dispatch and child forwarding,
// not only the in-process ingress helpers. Every invocation gets fresh roots.
func TestIngressLocalCLI(t *testing.T) {
	binary := os.Getenv("KTX_INGRESS_CLI")
	if binary == "" {
		t.Skip("set KTX_INGRESS_CLI to the locally built binary")
	}
	for _, codex := range []bool{false, true} {
		for _, tc := range ingressOutputCases(codex) {
			t.Run(fmt.Sprintf("codex=%t/%s", codex, tc.name), func(t *testing.T) {
				ingressEnvironment(t)
				root := t.TempDir()
				path := filepath.Join(root, "evidence.db")
				cmd := ingressLocalCommand(t, binary, path, "hook")
				cmd.Dir = root
				cmd.Stdin = strings.NewReader(ingressHookPayload(codex, tc.raw))
				response, err := cmd.Output()
				if err != nil {
					t.Fatalf("hook: %v", err)
				}
				d, err := dbpkg.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				assertIngressOutput(t, d, codex, response, tc.want)
			})
		}
	}
}

func TestIngressEmptyCrossIngress(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, hookFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("local=%t/hook-first=%t", local, hookFirst), func(t *testing.T) {
				binary := os.Getenv("KTX_INGRESS_CLI")
				if local && binary == "" {
					t.Skip("set KTX_INGRESS_CLI to the locally built binary")
				}
				ingressEnvironment(t)
				hookChdirTemp(t)
				root := t.TempDir()
				path := filepath.Join(root, "evidence.db")
				d, err := dbpkg.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				if !local {
					mustWrapSession(t, d, "wrapper-owner")
				}
				payload := ingressHookPayload(false, `{"output":"","text":"not tool output"}`)
				line := openCodeWrapperEvent(t, "native-session", "native-call", "bash", `{"command":"test"}`, "") + "\n"
				fixture := filepath.Join(root, "stream.jsonl")
				if err := os.WriteFile(fixture, []byte(line), 0o600); err != nil {
					t.Fatal(err)
				}
				deliver := func(hook bool) {
					t.Helper()
					var out, diag bytes.Buffer
					if local {
						args := []string{"hook"}
						if !hook {
							args = []string{"run", "--", "/bin/cat", fixture}
						}
						cmd := ingressLocalCommand(t, binary, path, args...)
						cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = root, strings.NewReader(payload), &out, &diag
						if err := cmd.Run(); err != nil {
							t.Fatalf("local ingress: %v %s", err, &diag)
						}
					} else if hook {
						if _, err := hookLogic(strings.NewReader(payload), d, &out, &diag); err != nil {
							t.Fatal(err)
						}
					} else if n := teeAndParse(strings.NewReader(line), &out, d, "wrapper-owner", &diag); n != 1 {
						t.Fatalf("wrapper count=%d: %s", n, &diag)
					}
					if strings.Contains(diag.String(), "conflict") {
						t.Fatalf("equivalent empty output conflicted: %s", &diag)
					}
					if hook {
						assertIngressOutput(t, d, false, out.Bytes(), "")
					} else if out.String() != line {
						t.Fatal("wrapper changed runtime-visible stream")
					}
				}
				deliver(hookFirst)
				id := mustOpenCodeID(t, "native-session", "native-call")
				first, err := d.GetToolCall(id)
				if err != nil {
					t.Fatal(err)
				}
				for _, hook := range []bool{!hookFirst, hookFirst, !hookFirst} {
					deliver(hook)
				}
				last, err := d.GetToolCall(id)
				if err != nil || !reflect.DeepEqual(first, last) {
					t.Fatalf("retry replaced first evidence: first=%+v last=%+v (%v)", first, last, err)
				}
				wantProducer := dbpkg.ProducerRun
				if hookFirst {
					wantProducer = dbpkg.ProducerHook
				}
				if first.Producer == nil || *first.Producer != wantProducer {
					t.Fatalf("first producer=%v want %s", first.Producer, wantProducer)
				}
				var count, total, owned int
				if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if err := d.DB().QueryRow(`SELECT sum(tool_call_count) FROM sessions`).Scan(&total); err != nil {
					t.Fatal(err)
				}
				if err := d.DB().QueryRow(`SELECT tool_call_count FROM sessions WHERE id=?`, first.SessionID).Scan(&owned); err != nil {
					t.Fatal(err)
				}
				if count != 1 || total != 1 || owned != 1 {
					t.Fatalf("source/total/owner rollups=%d/%d/%d, want 1/1/1", count, total, owned)
				}
			})
		}
	}
}
