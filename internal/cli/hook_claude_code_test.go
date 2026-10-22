package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/identity"
)

const claudeCodeTestSession = "0f4c2a8e-claude-session"

// claudeCodeEvent returns a Claude Code PostToolUse Bash event with the
// metadata fields Claude Code sends alongside the required ones. An empty
// sessionID or toolUseID omits that field.
func claudeCodeEvent(sessionID, toolUseID string, response map[string]any) map[string]any {
	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"transcript_path": "/home/user/.claude/projects/project/" + sessionID + ".jsonl",
		"cwd":             "/tmp/project",
		"permission_mode": "default",
		"prompt_id":       "prompt-1",
		"effort":          "high",
		"agent_id":        "agent-1",
		"agent_type":      "general-purpose",
		"scratchpad_dir":  "/tmp/scratchpad",
		"duration_ms":     1234,
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "seq 1 100", "description": "Print numbers"},
		"tool_response":   response,
	}
	if sessionID != "" {
		event["session_id"] = sessionID
	}
	if toolUseID != "" {
		event["tool_use_id"] = toolUseID
	}
	return event
}

// claudeCodeBashResponse returns a Bash tool_response with the given stdout
// and the other keys Claude Code reports for a completed command.
func claudeCodeBashResponse(stdout string) map[string]any {
	return map[string]any{
		"stdout":           stdout,
		"stderr":           "warning: <tag> & done",
		"interrupted":      false,
		"isImage":          false,
		"noOutputExpected": false,
	}
}

// marshalClaudeCode encodes a payload the way Claude Code does: compact and
// without HTML escaping.
func marshalClaudeCode(t *testing.T, value any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		t.Fatalf("marshal Claude Code payload: %v", err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func claudeCodePayload(t *testing.T, toolUseID, stdout string) string {
	t.Helper()
	return marshalClaudeCode(t, claudeCodeEvent(claudeCodeTestSession, toolUseID, claudeCodeBashResponse(stdout)))
}

func mustClaudeCodeID(t *testing.T, sessionID, toolUseID string) string {
	t.Helper()
	id, err := identity.Derive("claude-code", sessionID, toolUseID)
	if err != nil {
		t.Fatalf("derive Claude Code id: %v", err)
	}
	return id
}

func runClaudeCodeHook(t *testing.T, d *dbpkg.DB, payload string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	_, err := hookLogicFor(strings.NewReader(payload), d, &stdout, &stderr, "claude-code")
	return stdout.String(), err
}

func mustRunClaudeCodeHook(t *testing.T, d *dbpkg.DB, payload string) string {
	t.Helper()
	out, err := runClaudeCodeHook(t, d, payload)
	if err != nil {
		t.Fatalf("hookLogicFor: %v", err)
	}
	return out
}

func claudeCodeLines(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "line %d <ok> & padding content here\n", i)
	}
	return sb.String()
}

// claudeCodeUpdatedOutput decodes a transforming hook response and returns
// its updatedToolOutput keys with their raw bytes.
func claudeCodeUpdatedOutput(t *testing.T, out string) map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		t.Fatalf("parse hook output: %v\nstdout=%s", err, out)
	}
	if len(top) != 1 {
		t.Fatalf("hook output keys = %v, want only hookSpecificOutput", top)
	}
	var specific struct {
		HookEventName     string                     `json:"hookEventName"`
		UpdatedToolOutput map[string]json.RawMessage `json:"updatedToolOutput"`
	}
	if err := json.Unmarshal(top["hookSpecificOutput"], &specific); err != nil {
		t.Fatalf("parse hookSpecificOutput: %v\nstdout=%s", err, out)
	}
	if specific.HookEventName != "PostToolUse" {
		t.Errorf("hookEventName = %q, want PostToolUse", specific.HookEventName)
	}
	if specific.UpdatedToolOutput == nil {
		t.Fatalf("updatedToolOutput missing: %s", out)
	}
	return specific.UpdatedToolOutput
}

func assertClaudeCodePassthroughOutput(t *testing.T, out string) {
	t.Helper()
	if got := strings.TrimSpace(out); got != "{}" {
		t.Errorf("stdout = %q, want {}", got)
	}
}

func setupClaudeCodeHookTest(t *testing.T, config string) *dbpkg.DB {
	t.Helper()
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if config != "" {
		hookWriteUserConfig(t, config)
	}
	return d
}

const claudeCodeHeadConfig = `[pipeline.default]
stages = ["head:3"]
`

func TestHookClaudeCodeTransformReplacesOnlyStdout(t *testing.T) {
	d := setupClaudeCodeHookTest(t, claudeCodeHeadConfig)
	original := claudeCodeLines(100)
	response := claudeCodeBashResponse(original)
	response["returnCodeInterpretation"] = "success"
	response["futureField"] = map[string]any{"nested": []any{1, "two", nil}}
	payload := marshalClaudeCode(t, claudeCodeEvent(claudeCodeTestSession, "toolu_transform", response))

	out := mustRunClaudeCodeHook(t, d, payload)
	escape := `\` + "u00"
	if strings.Contains(out, escape+"3c") || strings.Contains(out, escape+"26") || !strings.Contains(out, "<ok> &") {
		t.Errorf("hook output HTML-escaped its text: %s", out)
	}
	updated := claudeCodeUpdatedOutput(t, out)

	var originalFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(marshalClaudeCode(t, response)), &originalFields); err != nil {
		t.Fatal(err)
	}
	if len(updated) != len(originalFields) {
		t.Errorf("updatedToolOutput keys = %d, want %d (same keys as tool_response)", len(updated), len(originalFields))
	}
	for key, raw := range originalFields {
		if key == "stdout" {
			continue
		}
		if got, ok := updated[key]; !ok || !bytes.Equal(got, raw) {
			t.Errorf("updatedToolOutput[%q] = %s, want byte-identical %s", key, got, raw)
		}
	}
	var stdout string
	if err := json.Unmarshal(updated["stdout"], &stdout); err != nil {
		t.Fatalf("updated stdout is not a string: %s", updated["stdout"])
	}
	if !strings.HasPrefix(stdout, "line 0 <ok> & padding") || strings.Contains(stdout, "line 50") {
		t.Errorf("updated stdout = %q, want the first three lines", stdout)
	}
	if strings.Contains(stdout, "[ktx]") {
		t.Errorf("updated stdout carries a footer while footers are off: %q", stdout)
	}

	sourceID := mustClaudeCodeID(t, claudeCodeTestSession, "toolu_transform")
	persisted, err := d.GetToolCall(sourceID)
	if err != nil {
		t.Fatalf("GetToolCall(%s): %v", sourceID, err)
	}
	if persisted.Output != original {
		t.Errorf("persisted output differs from the original stdout")
	}
	if persisted.Tool != "Bash" {
		t.Errorf("persisted tool = %q, want Bash", persisted.Tool)
	}
	if persisted.Input != `{"command":"seq 1 100","description":"Print numbers"}` {
		t.Errorf("persisted input = %q, want the raw tool_input", persisted.Input)
	}
	if persisted.Runtime == nil || *persisted.Runtime != "claude-code" {
		t.Errorf("persisted runtime = %v, want claude-code", persisted.Runtime)
	}
	if persisted.IsError == nil || *persisted.IsError {
		t.Errorf("persisted is_error = %v, want explicit success", persisted.IsError)
	}
	var compressed string
	if err := d.DB().QueryRow(`SELECT output FROM compressed WHERE source_id = ?`, sourceID).Scan(&compressed); err != nil {
		t.Fatalf("compressed row: %v", err)
	}
	if compressed != stdout {
		t.Errorf("delivered stdout %q differs from the stored compressed row %q", stdout, compressed)
	}

	sessions, err := d.ListSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %#v, err = %v", sessions, err)
	}
	if sessions[0].Agent != "claude-code" || sessions[0].AgentAttributionAuthority != dbpkg.AgentAttributionIdentified {
		t.Errorf("session attribution = (%q, %q), want identified claude-code", sessions[0].Agent, sessions[0].AgentAttributionAuthority)
	}
}

func TestHookClaudeCodePassthroughWithoutConfig(t *testing.T) {
	d := setupClaudeCodeHookTest(t, "")
	original := "some tool result text\n"
	out := mustRunClaudeCodeHook(t, d, claudeCodePayload(t, "toolu_passthrough", original))
	assertClaudeCodePassthroughOutput(t, out)

	sourceID := mustClaudeCodeID(t, claudeCodeTestSession, "toolu_passthrough")
	persisted, err := d.GetToolCall(sourceID)
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Output != original {
		t.Errorf("persisted output = %q, want %q", persisted.Output, original)
	}
	if n := countRows(t, d, "compressed"); n != 0 {
		t.Errorf("compressed rows = %d, want 0", n)
	}
	disposition, err := d.GetCompressionDisposition(sourceID)
	if err != nil {
		t.Fatalf("GetCompressionDisposition: %v", err)
	}
	if disposition.Disposition != dbpkg.CompressionDispositionIntentionalPassthrough {
		t.Errorf("disposition = %+v, want intentional passthrough", disposition)
	}
}

func TestHookClaudeCodeUnknownFieldsStayTrusted(t *testing.T) {
	d := setupClaudeCodeHookTest(t, "")
	event := claudeCodeEvent(claudeCodeTestSession, "toolu_future", claudeCodeBashResponse("ok\n"))
	event["future_object"] = map[string]any{"a": 1}
	event["future_null"] = nil
	event["future_list"] = []any{"x", 2}
	mustRunClaudeCodeHook(t, d, marshalClaudeCode(t, event))

	persisted, err := d.GetToolCall(mustClaudeCodeID(t, claudeCodeTestSession, "toolu_future"))
	if err != nil {
		t.Fatalf("trusted identity not used: %v", err)
	}
	if persisted.IsError == nil || *persisted.IsError {
		t.Errorf("is_error = %v, want explicit success", persisted.IsError)
	}
}

func TestHookClaudeCodeUntrustedPayloadsGetFreshIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing session_id", func(e map[string]any) { delete(e, "session_id") }},
		{"missing tool_use_id", func(e map[string]any) { delete(e, "tool_use_id") }},
		{"empty session_id", func(e map[string]any) { e["session_id"] = "" }},
		{"empty tool_use_id", func(e map[string]any) { e["tool_use_id"] = "" }},
		{"numeric tool_use_id", func(e map[string]any) { e["tool_use_id"] = 7 }},
		{"missing tool_name", func(e map[string]any) { delete(e, "tool_name") }},
		{"null tool_input", func(e map[string]any) { e["tool_input"] = nil }},
		{"string tool_input", func(e map[string]any) { e["tool_input"] = "seq 1 100" }},
		{"null tool_response", func(e map[string]any) { e["tool_response"] = nil }},
		{"string tool_response", func(e map[string]any) { e["tool_response"] = "ok" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := setupClaudeCodeHookTest(t, "")
			event := claudeCodeEvent(claudeCodeTestSession, "toolu_untrusted", claudeCodeBashResponse("ok\n"))
			tc.mutate(event)
			payload := marshalClaudeCode(t, event)
			for i := 0; i < 2; i++ {
				out := mustRunClaudeCodeHook(t, d, payload)
				assertClaudeCodePassthroughOutput(t, out)
			}
			_, ids, err := d.SampleIDs(2)
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) != 2 || ids[0] == ids[1] {
				t.Fatalf("source ids = %v, want two fresh identities", ids)
			}
			trustedID := mustClaudeCodeID(t, claudeCodeTestSession, "toolu_untrusted")
			for _, id := range ids {
				if id == trustedID {
					t.Errorf("untrusted payload used the trusted identity %q", id)
				}
				persisted, err := d.GetToolCall(id)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.IsError != nil {
					t.Errorf("is_error = %v, want unknown", *persisted.IsError)
				}
				if persisted.Runtime == nil || *persisted.Runtime != "claude-code" {
					t.Errorf("runtime = %v, want claude-code", persisted.Runtime)
				}
			}
			sessions, err := d.ListSessions()
			if err != nil || len(sessions) != 1 {
				t.Fatalf("sessions = %#v, err = %v", sessions, err)
			}
			if sessions[0].AgentAttributionAuthority != dbpkg.AgentAttributionUnknown {
				t.Errorf("untrusted attribution = %#v", sessions[0])
			}
		})
	}
}

// An untrusted payload keeps the Claude Code response contract: a
// replaceable result is still compressed, but with fresh identity and an
// unknown outcome.
func TestHookClaudeCodeUntrustedReplaceablePayloadStillCompresses(t *testing.T) {
	d := setupClaudeCodeHookTest(t, claudeCodeHeadConfig)
	event := claudeCodeEvent(claudeCodeTestSession, "", claudeCodeBashResponse(claudeCodeLines(100)))
	out := mustRunClaudeCodeHook(t, d, marshalClaudeCode(t, event))
	updated := claudeCodeUpdatedOutput(t, out)
	var stdout string
	if err := json.Unmarshal(updated["stdout"], &stdout); err != nil || !strings.HasPrefix(stdout, "line 0") || strings.Contains(stdout, "line 50") {
		t.Errorf("updated stdout = %q (err %v), want the first three lines", stdout, err)
	}
	_, ids, err := d.SampleIDs(1)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
	persisted, err := d.GetToolCall(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if persisted.IsError != nil {
		t.Errorf("is_error = %v, want unknown", *persisted.IsError)
	}
	if n := countRows(t, d, "compressed"); n != 1 {
		t.Errorf("compressed rows = %d, want 1", n)
	}
}

func TestHookClaudeCodeRejectsOtherEventsBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		drop  bool
	}{
		{name: "PreToolUse", value: "PreToolUse"},
		{name: "PostToolUseFailure", value: "PostToolUseFailure"},
		{name: "lowercase", value: "posttooluse"},
		{name: "empty", value: ""},
		{name: "number", value: 42},
		{name: "null", value: nil},
		{name: "absent", drop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := setupClaudeCodeHookTest(t, claudeCodeHeadConfig)
			event := claudeCodeEvent(claudeCodeTestSession, "toolu_event", claudeCodeBashResponse(claudeCodeLines(50)))
			if tc.drop {
				delete(event, "hook_event_name")
			} else {
				event["hook_event_name"] = tc.value
			}
			out, err := runClaudeCodeHook(t, d, marshalClaudeCode(t, event))
			var cliErr *Error
			if !errors.As(err, &cliErr) || cliErr.Code != CodeUsage {
				t.Fatalf("error = %v, want usage error", err)
			}
			if !strings.Contains(cliErr.Message, `"PostToolUse"`) {
				t.Errorf("message = %q, want it to name PostToolUse", cliErr.Message)
			}
			if exitCodeOf(err) != ExitUsage {
				t.Errorf("exit = %d, want %d", exitCodeOf(err), ExitUsage)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			assertNoEvidenceWritten(t, d)
		})
	}
}

func TestHookClaudeCodeNonReplaceableResultsSkipThePipeline(t *testing.T) {
	long := claudeCodeLines(100)
	for _, tc := range []struct {
		name        string
		mutate      func(map[string]any)
		wantUnknown bool
	}{
		{name: "background task", mutate: func(r map[string]any) { r["backgroundTaskId"] = "bg-1" }},
		{name: "null background task", mutate: func(r map[string]any) { r["backgroundTaskId"] = nil }},
		{name: "image", mutate: func(r map[string]any) { r["isImage"] = true }},
		{name: "interrupted", mutate: func(r map[string]any) { r["interrupted"] = true }, wantUnknown: true},
		{name: "numeric stdout", mutate: func(r map[string]any) { r["stdout"] = 42 }},
		{name: "null stdout", mutate: func(r map[string]any) { r["stdout"] = nil }},
		{name: "missing stdout", mutate: func(r map[string]any) { delete(r, "stdout"); r["output"] = long }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := setupClaudeCodeHookTest(t, claudeCodeHeadConfig)
			response := claudeCodeBashResponse(long)
			tc.mutate(response)
			toolUseID := "toolu_" + strings.ReplaceAll(tc.name, " ", "_")
			out := mustRunClaudeCodeHook(t, d, marshalClaudeCode(t, claudeCodeEvent(claudeCodeTestSession, toolUseID, response)))
			assertClaudeCodePassthroughOutput(t, out)

			sourceID := mustClaudeCodeID(t, claudeCodeTestSession, toolUseID)
			persisted, err := d.GetToolCall(sourceID)
			if err != nil {
				t.Fatalf("GetToolCall: %v", err)
			}
			wantOutput := long
			if stdout, ok := response["stdout"].(string); !ok || stdout != long {
				wantOutput = marshalClaudeCode(t, response)
			}
			if persisted.Output != wantOutput {
				t.Errorf("persisted output = %q, want %q", persisted.Output, wantOutput)
			}
			if tc.wantUnknown {
				if persisted.IsError != nil {
					t.Errorf("is_error = %v, want unknown", *persisted.IsError)
				}
			} else if persisted.IsError == nil || *persisted.IsError {
				t.Errorf("is_error = %v, want explicit success", persisted.IsError)
			}
			if n := countRows(t, d, "compressed"); n != 0 {
				t.Errorf("compressed rows = %d, want 0", n)
			}
			disposition, err := d.GetCompressionDisposition(sourceID)
			if err != nil {
				t.Fatalf("GetCompressionDisposition: %v", err)
			}
			if disposition.Disposition != dbpkg.CompressionDispositionIntentionalPassthrough || disposition.Profile != "" || disposition.Route != "" {
				t.Errorf("disposition = %+v, want intentional passthrough with no selected profile", disposition)
			}
		})
	}
}

func TestHookClaudeCodeExactRetryConvergesToFirstArtifact(t *testing.T) {
	d := setupClaudeCodeHookTest(t, claudeCodeHeadConfig)
	payload := claudeCodePayload(t, "toolu_retry", claudeCodeLines(100))
	first := mustRunClaudeCodeHook(t, d, payload)
	claudeCodeUpdatedOutput(t, first)

	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:10"]
`)
	second := mustRunClaudeCodeHook(t, d, payload)
	if first != second {
		t.Errorf("retry output differs from the first artifact:\nfirst=%s\nsecond=%s", first, second)
	}
	if n := countRows(t, d, "tool_calls"); n != 1 {
		t.Errorf("tool_calls = %d, want 1", n)
	}
	if n := countRows(t, d, "compressed"); n != 1 {
		t.Errorf("compressed = %d, want 1", n)
	}

	// A retry whose result is no longer replaceable keeps the original
	// result even though an artifact exists.
	response := claudeCodeBashResponse(claudeCodeLines(100))
	response["backgroundTaskId"] = "bg-2"
	background := mustRunClaudeCodeHook(t, d, marshalClaudeCode(t, claudeCodeEvent(claudeCodeTestSession, "toolu_retry", response)))
	assertClaudeCodePassthroughOutput(t, background)
}

func TestHookClaudeCodeChangedRetryConflicts(t *testing.T) {
	d := setupClaudeCodeHookTest(t, "")
	mustRunClaudeCodeHook(t, d, claudeCodePayload(t, "toolu_conflict", "first output\n"))
	out, err := runClaudeCodeHook(t, d, claudeCodePayload(t, "toolu_conflict", "changed output\n"))
	var cliErr *Error
	if !errors.As(err, &cliErr) || cliErr.Code != CodeConflict {
		t.Fatalf("changed retry error = %v, want conflict", err)
	}
	if !strings.Contains(cliErr.Message, "Claude Code retry conflicts") {
		t.Errorf("conflict message = %q, want the Claude Code label", cliErr.Message)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	persisted, err := d.GetToolCall(mustClaudeCodeID(t, claudeCodeTestSession, "toolu_conflict"))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Output != "first output\n" {
		t.Errorf("persisted output = %q, want the first delivery", persisted.Output)
	}
}

func TestHookClaudeCodeRecoveryFooterAppendsToStdout(t *testing.T) {
	d := setupClaudeCodeHookTest(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = true
`)
	out := mustRunClaudeCodeHook(t, d, claudeCodePayload(t, "toolu_footer", claudeCodeLines(100)))
	updated := claudeCodeUpdatedOutput(t, out)
	var stdout string
	if err := json.Unmarshal(updated["stdout"], &stdout); err != nil {
		t.Fatal(err)
	}
	sourceID := mustClaudeCodeID(t, claudeCodeTestSession, "toolu_footer")
	footer := strings.Index(stdout, "\n[ktx]")
	if !strings.HasPrefix(stdout, "line 0") || footer <= 0 {
		t.Errorf("stdout = %q, want compressed lines followed by the footer", stdout)
	}
	if !strings.Contains(stdout, "`ktx read "+sourceID+"`") {
		t.Errorf("stdout = %q, want the recovery command for %s", stdout, sourceID)
	}
	var compressed string
	if err := d.DB().QueryRow(`SELECT output FROM compressed WHERE source_id = ?`, sourceID).Scan(&compressed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compressed, "[ktx]") {
		t.Errorf("stored compressed row carries the footer: %q", compressed)
	}
}

func TestHookClaudeCodeRecoveryFooterSizeCheckKeepsOriginal(t *testing.T) {
	d := setupClaudeCodeHookTest(t, `[pipeline.default]
stages = ["strip-ansi"]
recover_footer = true
`)
	original := "\x1b[31mError\x1b[0m: build started\nsee log for details\n"
	out := mustRunClaudeCodeHook(t, d, claudeCodePayload(t, "toolu_small", original))
	assertClaudeCodePassthroughOutput(t, out)
	disposition, err := d.GetCompressionDisposition(mustClaudeCodeID(t, claudeCodeTestSession, "toolu_small"))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.Disposition != dbpkg.CompressionDispositionAntiWorseningRejected {
		t.Errorf("disposition = %+v, want anti-worsening rejection", disposition)
	}
}

// Without --runtime, a Claude Code payload takes the Codex PostToolUse path:
// the Codex schema check fails, so the call is untrusted, and the response
// follows the Codex contract. The Codex path stores the whole tool_response
// object as text, so the compact payload passes through and an indented one
// lets json-compact show the Codex block response.
func TestHookClaudeCodePayloadWithoutRuntimeFlagKeepsCodexFallback(t *testing.T) {
	event := claudeCodeEvent(claudeCodeTestSession, "toolu_noflag", claudeCodeBashResponse(claudeCodeLines(100)))
	indented, err := json.MarshalIndent(event, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		payload      string
		wantDecision string
	}{
		{name: "compact payload passes through", payload: marshalClaudeCode(t, event)},
		{name: "indented payload blocks", payload: string(indented), wantDecision: "block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := setupClaudeCodeHookTest(t, `[pipeline.default]
stages = ["json-compact"]
`)
			var stdout, stderr bytes.Buffer
			if _, err := hookLogic(strings.NewReader(tc.payload), d, &stdout, &stderr); err != nil {
				t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
			}
			var out codexHookOutput
			if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
				t.Fatalf("parse Codex output: %v\nstdout=%s", err, stdout.String())
			}
			if out.Decision != tc.wantDecision || (tc.wantDecision != "") != (out.Reason != "") {
				t.Errorf("output = %s, want Codex decision %q", stdout.String(), tc.wantDecision)
			}
			if strings.Contains(stdout.String(), "hookSpecificOutput") {
				t.Errorf("stdout = %s, want no Claude Code response without the flag", stdout.String())
			}
			_, ids, err := d.SampleIDs(1)
			if err != nil || len(ids) != 1 {
				t.Fatalf("ids = %v, err = %v", ids, err)
			}
			for _, trusted := range []string{mustClaudeCodeID(t, claudeCodeTestSession, "toolu_noflag"), mustCodexID(t, claudeCodeTestSession, "toolu_noflag")} {
				if ids[0] == trusted {
					t.Errorf("source id %q is a trusted identity; want a fresh one", ids[0])
				}
			}
			persisted, err := d.GetToolCall(ids[0])
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Runtime == nil || *persisted.Runtime != "codex" {
				t.Errorf("runtime = %v, want codex", persisted.Runtime)
			}
			if persisted.IsError != nil {
				t.Errorf("is_error = %v, want unknown", *persisted.IsError)
			}
		})
	}
}

func TestHookClaudeCodeWrapperSessionIsNoop(t *testing.T) {
	t.Setenv("KTX_SESSION_ID", "wrapper-session")
	d := setupClaudeCodeHookTest(t, claudeCodeHeadConfig)
	out := mustRunClaudeCodeHook(t, d, claudeCodePayload(t, "toolu_wrapper", claudeCodeLines(100)))
	if out != "" {
		t.Errorf("stdout = %q, want no output", out)
	}
	assertNoEvidenceWritten(t, d)
}

func TestParseHookDispatchArgs(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		runtime string
		rest    []string
	}{
		{args: nil, rest: []string{}},
		{args: []string{"--runtime", "claude-code"}, runtime: "claude-code", rest: []string{}},
		{args: []string{"--runtime=claude-code"}, runtime: "claude-code", rest: []string{}},
		{args: []string{"--quiet", "--runtime", "claude-code", "--help"}, runtime: "claude-code", rest: []string{"--quiet", "--help"}},
		{args: []string{"--", "--runtime", "bogus"}, rest: []string{"--", "--runtime", "bogus"}},
	} {
		rest, parsed, err := parseHookDispatchArgs(tc.args)
		if err != nil {
			t.Errorf("parse %q: %v", tc.args, err)
			continue
		}
		if got := parsed.(hookParsedArgs).runtime; got != tc.runtime {
			t.Errorf("parse %q runtime = %q, want %q", tc.args, got, tc.runtime)
		}
		if fmt.Sprint(rest) != fmt.Sprint(tc.rest) {
			t.Errorf("parse %q rest = %q, want %q", tc.args, rest, tc.rest)
		}
	}
	for _, args := range [][]string{
		{"--runtime"},
		{"--runtime="},
		{"--runtime", ""},
		{"--runtime", "bogus"},
		{"--runtime", "codex"},
		{"--runtime=Claude-Code"},
		{"--runtime", "claude-code", "--runtime", "claude-code"},
		{"--runtime=claude-code", "--runtime=claude-code"},
	} {
		if _, _, err := parseHookDispatchArgs(args); err == nil || !strings.Contains(err.Error(), "--runtime") {
			t.Errorf("parse %q error = %v, want a --runtime usage error", args, err)
		}
	}
}

func TestHookRuntimeFlagRejectedBeforeDatabaseOpens(t *testing.T) {
	for _, args := range [][]string{
		{"hook", "--runtime", "bogus"},
		{"hook", "--runtime"},
		{"hook", "--runtime="},
		{"hook", "--runtime", "claude-code", "--runtime=claude-code"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "never.db")
			t.Setenv("KTX_DB", dbPath)
			t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))
			var stdout, stderr bytes.Buffer
			if code := runWith(args, &stdout, &stderr); code != ExitUsage {
				t.Fatalf("exit = %d, want %d; stderr=%q", code, ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "--runtime") {
				t.Errorf("stderr = %q, want a --runtime correction", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("database stat = %v, want not created", err)
			}
		})
	}
}

func TestHookClaudeCodeNotFoundNeverExitsTwo(t *testing.T) {
	reg := registeredVerb{
		opts: RegisterOptions{ParseArgs: parseHookDispatchArgs, PassthroughStdout: true},
		do: func(*DispatchContext) (any, error) {
			return nil, notFoundError("ktx hook: source not found")
		},
	}
	for _, tc := range []struct {
		args []string
		want int
	}{
		{args: []string{"--runtime", "claude-code"}, want: ExitUsage},
		{args: []string{"--runtime=claude-code"}, want: ExitUsage},
		{args: nil, want: ExitNotFound},
	} {
		var stdout, stderr bytes.Buffer
		if code := runRegisteredVerb("hook", reg, tc.args, Options{}, &stdout, &stderr); code != tc.want {
			t.Errorf("args %q exit = %d, want %d; stderr=%q", tc.args, code, tc.want, stderr.String())
		}
	}
	if got := hookExitCode(hookParsedArgs{runtime: "claude-code"}, ExitConflict); got != ExitConflict {
		t.Errorf("conflict exit = %d, want %d", got, ExitConflict)
	}
	if got := hookExitCode(nil, ExitNotFound); got != ExitNotFound {
		t.Errorf("other command exit = %d, want %d", got, ExitNotFound)
	}
}
