// Command runtime_identity_probe exercises retry-class behavior offline against
// the current checkout. It writes only to temporary directories and makes no
// model or network calls.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/identity"
	"github.com/jgabor/ktx/internal/parser"
)

const openCodeStream = `{"type":"tool_use","timestamp":"2026-07-15T12:00:00Z","sessionID":"ses-hook-fixture","part":{"type":"tool","tool":"bash","callID":"call-hook-fixture","state":{"status":"completed","input":{"command":"echo hi"},"output":"hi\n","metadata":{"exit":0},"time":{"start":1000,"end":1001}}}}`

const openCodeCallback = `{"runtime":"opencode","input":{"tool":"bash","sessionID":"ses-hook-fixture","callID":"call-hook-fixture","args":{"command":"echo hi"}},"output":{"title":"echo","output":"hi\n","metadata":{"exit":0}}}`

const codexCallback = `{"hook_event_name":"PostToolUse","session_id":"codex-session-fixture","turn_id":"turn-fixture","tool_use_id":"codex-call-fixture","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_response":{"aggregated_output":"hi\n","exit_code":0}}`

type nativeTuple struct {
	Runtime string
	Session string
	Call    string
}

func main() {
	root, err := os.Getwd()
	must(err)
	tmp, err := os.MkdirTemp("", "ktx-runtime-identity-probe-")
	must(err)
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "ktx")
	must(run(root, nil, nil, "go", "build", "-o", bin, "./cmd/ktx"))

	hookID := probeDuplicateOpenCodeCallback(root, tmp, bin)
	probeOpenCodeWrapperReplay(hookID)
	probeModelReexecution()
	probeProcessAndSessionRestart(root, tmp, bin)
	probeHookFailureRedelivery(root, tmp, bin)
	fmt.Println("PASS all offline runtime identity retry-class probes")
}

func probeDuplicateOpenCodeCallback(root, tmp, bin string) string {
	env := probeEnv(filepath.Join(tmp, "duplicate-opencode"))
	must(run(root, env, []byte(openCodeCallback), bin, "hook"))
	must(run(root, env, []byte(openCodeCallback), bin, "hook"))
	count := scalar(env["KTX_DB"], `SELECT count(*) FROM tool_calls WHERE runtime = 'opencode'`)
	assert(count == 1, "duplicate OpenCode callback: got %d rows, want one deterministic source", count)
	rollups := scalar(env["KTX_DB"], `SELECT coalesce(sum(tool_call_count), 0) FROM sessions`)
	assert(rollups == 1, "duplicate OpenCode callback: roll-up sum %d, want 1", rollups)
	want, err := identity.Derive("opencode", "ses-hook-fixture", "call-hook-fixture")
	must(err)
	var got string
	must(queryString(env["KTX_DB"], `SELECT id FROM tool_calls WHERE runtime = 'opencode'`, &got))
	assert(got == want, "OpenCode hook source ID %q, want derived ID %q", got, want)
	fmt.Println("PASS duplicate OpenCode callback: exact trusted delivery converged to one deterministic source and roll-up")
	return got
}

func probeOpenCodeWrapperReplay(hookID string) {
	firstTuple := parseOpenCodeTuple([]byte(openCodeStream))
	secondTuple := parseOpenCodeTuple([]byte(openCodeStream))
	assert(firstTuple == secondTuple, "wrapper replay changed native tuple: %#v != %#v", firstTuple, secondTuple)
	_, firstNative, ok := parser.ParseEventWithCorrelation([]byte(openCodeStream))
	assert(ok, "wrapper replay first parse rejected fixture")
	_, secondNative, ok := parser.ParseEventWithCorrelation([]byte(openCodeStream))
	assert(ok, "wrapper replay second parse rejected fixture")
	assert(firstNative.Trusted && secondNative.Trusted, "wrapper replay did not retain trusted native correlation")
	first, err := correlation.Resolve(
		correlation.NativeTuple{Runtime: firstNative.Runtime, SessionID: firstNative.SessionID, CallID: firstNative.CallID},
		correlation.Trusted(correlation.ProvenanceOpenCodeWrapper),
	)
	must(err)
	second, err := correlation.Resolve(
		correlation.NativeTuple{Runtime: secondNative.Runtime, SessionID: secondNative.SessionID, CallID: secondNative.CallID},
		correlation.Trusted(correlation.ProvenanceOpenCodeWrapper),
	)
	must(err)
	assert(first.Eligible && second.Eligible, "wrapper replay was not eligible for deterministic identity")
	assert(first.ID == second.ID, "wrapper replay source IDs differ: %q != %q", first.ID, second.ID)
	assert(first.ID == hookID, "equivalent hook and wrapper IDs differ: %q != %q", hookID, first.ID)
	fmt.Println("PASS OpenCode wrapper replay: retained tuple converged across replay and matched the equivalent hook source")
}

func probeModelReexecution() {
	first := nativeTuple{Runtime: "opencode", Session: "ses-model-fixture", Call: "call-model-1"}
	second := nativeTuple{Runtime: "opencode", Session: "ses-model-fixture", Call: "call-model-2"}
	firstID, err := identity.Derive(first.Runtime, first.Session, first.Call)
	must(err)
	secondID, err := identity.Derive(second.Runtime, second.Session, second.Call)
	must(err)
	assert(firstID != secondID, "new model call ID collapsed to existing identity %q", firstID)
	fmt.Println("PASS model-requested re-execution fixture: new native call ID produces a distinct derived identity")
}

func probeProcessAndSessionRestart(root, tmp, bin string) {
	dbPath := filepath.Join(tmp, "restart.db")
	firstEnv := probeEnv(filepath.Join(tmp, "restart-a"))
	firstEnv["KTX_DB"] = dbPath
	secondEnv := probeEnv(filepath.Join(tmp, "restart-b"))
	secondEnv["KTX_DB"] = dbPath
	must(run(root, firstEnv, []byte(codexCallback), bin, "hook"))
	must(run(root, secondEnv, []byte(codexCallback), bin, "hook"))
	rows := scalar(dbPath, `SELECT count(*) FROM tool_calls WHERE runtime = 'codex'`)
	sessions := scalar(dbPath, `SELECT count(*) FROM sessions`)
	rollups := scalar(dbPath, `SELECT coalesce(sum(tool_call_count), 0) FROM sessions`)
	assert(rows == 1, "process/session restart: got %d source rows, want 1", rows)
	assert(sessions == 2, "process/session restart: got %d KTX sessions, want 2", sessions)
	assert(rollups == 1, "process/session restart: roll-up sum %d, want 1", rollups)
	fmt.Println("PASS KTX process/session restart simulation: same Codex tuple, 2 KTX sessions, 1 source row and roll-up")
}

func probeHookFailureRedelivery(root, tmp, bin string) {
	probeOneHookFailure(root, filepath.Join(tmp, "failure-codex"), bin, []byte(codexCallback), "codex", 1)
	probeOneHookFailure(root, filepath.Join(tmp, "failure-opencode"), bin, []byte(openCodeCallback), "opencode", 1)
	fmt.Println("PASS hook-failure redelivery: failure after persistence converged for Codex and OpenCode")
}

func probeOneHookFailure(root, base, bin string, payload []byte, runtime string, wantRows int) {
	env := probeEnv(base)
	configPath := filepath.Join(env["XDG_CONFIG_HOME"], "ktx", "ktx.toml")
	must(os.MkdirAll(filepath.Dir(configPath), 0o755))
	must(os.WriteFile(configPath, []byte("[invalid\n"), 0o600))
	err := run(root, env, payload, bin, "hook")
	assert(err != nil, "%s hook failure fixture unexpectedly succeeded", runtime)
	assert(scalar(env["KTX_DB"], `SELECT count(*) FROM tool_calls`) == 1, "%s hook failure did not persist its source before failing", runtime)
	must(os.Remove(configPath))
	must(run(root, env, payload, bin, "hook"))
	count := scalar(env["KTX_DB"], `SELECT count(*) FROM tool_calls`)
	assert(count == wantRows, "%s hook redelivery retained %d rows, want %d", runtime, count, wantRows)
}

func parseOpenCodeTuple(payload []byte) nativeTuple {
	var event struct {
		SessionID string `json:"sessionID"`
		Part      struct {
			CallID string `json:"callID"`
		} `json:"part"`
	}
	must(json.Unmarshal(payload, &event))
	assert(event.SessionID != "" && event.Part.CallID != "", "OpenCode fixture lacks native tuple")
	return nativeTuple{Runtime: "opencode", Session: event.SessionID, Call: event.Part.CallID}
}

func probeEnv(base string) map[string]string {
	must(os.MkdirAll(base, 0o755))
	return map[string]string{
		"HOME":            base,
		"KTX_DB":          filepath.Join(base, "ktx.db"),
		"XDG_CONFIG_HOME": filepath.Join(base, "config"),
		"XDG_DATA_HOME":   filepath.Join(base, "data"),
		"XDG_RUNTIME_DIR": filepath.Join(base, "runtime"),
		"KTX_SESSION_ID":  "",
	}
}

func run(dir string, overrides map[string]string, stdin []byte, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = mergedEnv(overrides)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w; stdout=%q stderr=%q", name, args, err, stdout.String(), stderr.String())
	}
	return nil
}

func mergedEnv(overrides map[string]string) []string {
	values := map[string]string{}
	for _, item := range os.Environ() {
		for i := 0; i < len(item); i++ {
			if item[i] == '=' {
				values[item[:i]] = item[i+1:]
				break
			}
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func scalar(path, query string) int {
	d, err := dbpkg.Open(path)
	must(err)
	defer d.Close()
	var value int
	must(d.DB().QueryRow(query).Scan(&value))
	return value
}

func queryString(path, query string, dest *string) error {
	d, err := dbpkg.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.DB().QueryRow(query).Scan(dest)
}

func assert(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
