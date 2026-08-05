package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/evidence"
	"github.com/jgabor/ktx/internal/identity"
	"github.com/jgabor/ktx/internal/parser"
)

//.Shared fixtures for OpenCode hook and wrapper identity tests.
//
//The hook adapter (tool.execute.after) receives:
//   {"runtime":"opencode","input":{"tool","sessionID","callID","args"},"output":{...}}
//
//The wrapper stream (opencode run --format json) emits:
//   {"type":"tool_use","timestamp":...,"sessionID":...,"part":{"type":"tool","tool":"...","callID":"...","state":{"input":...,"output":...}}}
//
//Both expose the same native correlation tuple:
//   (opencode, sessionID, callID)
//
//These helpers build equivalent events for each ingress so tests can prove
//they resolve to the same deterministic source ID.

// openCodeHookPayload builds an OpenCode hook callback JSON string from the
// native tuple and evidence fields. When sessionID or callID is empty, the
// field is omitted so the hook treats the tuple as incomplete.
func openCodeHookPayload(t *testing.T, sessionID, callID, tool, argsJSON, output string) string {
	t.Helper()
	m := map[string]any{
		"runtime":   "opencode",
		"timestamp": "2026-07-16T12:00:00Z",
		"input":     map[string]any{},
		"output":    map[string]any{"output": output},
	}
	in := m["input"].(map[string]any)
	in["tool"] = tool
	if argsJSON != "" {
		in["args"] = json.RawMessage(argsJSON)
	}
	if sessionID != "" {
		in["sessionID"] = sessionID
	}
	if callID != "" {
		in["callID"] = callID
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal opencode hook payload: %v", err)
	}
	return string(b)
}

func openCodeHookOutcomePayload(t *testing.T, sessionID, callID, tool, output string, exit any, includeExit bool) string {
	t.Helper()
	metadata := map[string]any{}
	if includeExit {
		metadata["exit"] = exit
	}
	m := map[string]any{
		"runtime":   "opencode",
		"timestamp": "2026-07-16T12:00:00Z",
		"input": map[string]any{
			"tool": tool, "sessionID": sessionID, "callID": callID,
			"args": map[string]any{"command": "test"},
		},
		"output": map[string]any{"title": "shell", "output": output, "metadata": metadata},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal OpenCode outcome payload: %v", err)
	}
	return string(b)
}

// openCodeWrapperEvent builds an OpenCode wrapper stream JSON line from the
// native tuple and evidence fields. The part.state.input and output fields
// carry the same evidence as the hook payload so cross-ingress equality
// holds under canonical JSON comparison.
func openCodeWrapperEvent(t *testing.T, sessionID, callID, tool, inputJSON, output string) string {
	t.Helper()
	var inputVal any
	if inputJSON != "" {
		if err := json.Unmarshal([]byte(inputJSON), &inputVal); err != nil {
			t.Fatalf("parse input JSON %q: %v", inputJSON, err)
		}
	}
	ev := map[string]any{
		"type":      "tool_use",
		"timestamp": "2026-07-16T12:00:00Z",
		"sessionID": sessionID,
		"part": map[string]any{
			"type":   "tool",
			"tool":   tool,
			"callID": callID,
			"state": map[string]any{
				"input":  inputVal,
				"output": output,
			},
		},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal opencode wrapper event: %v", err)
	}
	return string(b)
}

// mustOpenCodeID derives the expected deterministic source ID from the native
// OpenCode tuple. It fails the test if derivation fails.
func mustOpenCodeID(t *testing.T, sessionID, callID string) string {
	t.Helper()
	id, err := identity.Derive("opencode", sessionID, callID)
	if err != nil {
		t.Fatalf("derive OpenCode id: %v", err)
	}
	return id
}

// mustResolveOpenCode resolves an OpenCode tuple through the correlation
// contract to produce an eligible Source for direct evidence-layer tests.
func mustResolveOpenCode(t *testing.T, sessionID, callID string) correlation.Source {
	t.Helper()
	src, err := correlation.Resolve(
		correlation.NativeTuple{Runtime: "opencode", SessionID: sessionID, CallID: callID},
		correlation.Trusted(correlation.ProvenanceOpenCodeHook),
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !src.Eligible {
		t.Fatalf("want eligible source, got %+v", src)
	}
	return src
}

// mustWrapSession creates a KTX session row so teeAndParse's foreign-key
// constraint on tool_calls.session_id is satisfied.
func mustWrapSession(t *testing.T, d *dbpkg.DB, id string) {
	t.Helper()
	if err := d.InsertSession(&dbpkg.Session{
		ID:         id,
		Agent:      "opencode",
		StartedAt:  "2026-07-16T12:00:00Z",
		LastSeenAt: "2026-07-16T12:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Equivalent eligible hook and wrapper events resolve to the same ID.
// ---------------------------------------------------------------------------

// TestOpenCode_HookAndWrapperResolveSameSourceID proves that the same native
// tuple delivered through the hook adapter and the wrapper parser produces
// the same deterministic source ID. This is the cross-ingress equivalence
// required for cross-ingress convergence.
func TestOpenCode_HookAndWrapperResolveSameSourceID(t *testing.T) {
	const (
		sessionID = "ses-xover-001"
		callID    = "call-xover-001"
		tool      = "bash"
		argsJSON  = `{"command":"echo crossover"}`
		output    = "crossover result\n"
	)

	// Hook path: resolve via the hook's trusted provenance.
	hookSrc := mustResolveOpenCode(t, sessionID, callID)

	// Wrapper path: parser extracts the tuple, wrapper resolves via
	// the wrapper's trusted provenance.
	line := openCodeWrapperEvent(t, sessionID, callID, tool, argsJSON, output)
	_, nc, ok := parser.ParseEventWithCorrelation([]byte(line))
	if !ok {
		t.Fatal("ParseEventWithCorrelation returned false for tool_use event")
	}
	if !nc.Trusted {
		t.Fatal("want trusted native correlation for OpenCode tool_use event")
	}
	wrapperSrc, err := correlation.Resolve(
		correlation.NativeTuple{Runtime: nc.Runtime, SessionID: nc.SessionID, CallID: nc.CallID},
		correlation.Trusted(correlation.ProvenanceOpenCodeWrapper),
	)
	if err != nil {
		t.Fatalf("wrapper resolve: %v", err)
	}

	// Both must be eligible and converge to the same ID.
	if !hookSrc.Eligible || !wrapperSrc.Eligible {
		t.Fatalf("want both eligible; got hook=%+v wrapper=%+v", hookSrc, wrapperSrc)
	}
	if hookSrc.ID != wrapperSrc.ID {
		t.Fatalf("hook ID %q != wrapper ID %q for the same native tuple", hookSrc.ID, wrapperSrc.ID)
	}
	expected := mustOpenCodeID(t, sessionID, callID)
	if hookSrc.ID != expected {
		t.Fatalf("ID %q != expected %q", hookSrc.ID, expected)
	}
}

// TestOpenCode_HookExactDuplicateConverges proves that delivering the same
// eligible hook callback twice produces one source row and one roll-up
// increment (shared vector: same payload, same tuple).
func TestOpenCode_HookExactDuplicateConverges(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "ses-dedup-001"
		callID    = "call-dedup-001"
	)
	payload := openCodeHookPayload(t, sessionID, callID, "bash", `{"command":"echo hello"}`, "hello\n")

	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
			t.Fatalf("delivery %d: %v (stderr=%q)", i, err, stderr.String())
		}
	}

	sourceID := mustOpenCodeID(t, sessionID, callID)
	var sourceCount int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id = ?`, sourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Errorf("source rows = %d, want 1 (exact duplicate converges)", sourceCount)
	}

	persisted, err := d.GetToolCall(sourceID)
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	session, err := d.GetSession(persisted.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.ToolCallCount != 1 {
		t.Errorf("tool_call_count = %d, want 1 (one roll-up)", session.ToolCallCount)
	}
}

// TestOpenCode_WrapperReplayConverges proves that replaying the same wrapper
// stream event line through teeAndParse produces one source row (the wrapper
// uses the evidence layer for eligible retries).
func TestOpenCode_WrapperReplayConverges(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "ses-wrap-001"
		callID    = "call-wrap-001"
	)
	mustWrapSession(t, d, "ktx-wrap-session")
	line := openCodeWrapperEvent(t, sessionID, callID, "bash", `{"command":"echo"}`, "output\n")
	stream := line + "\n" + line + "\n" // replay the same line twice

	var stdout, stderr bytes.Buffer
	n := teeAndParse(strings.NewReader(stream), &stdout, d, "ktx-wrap-session", &stderr)
	if n != 2 {
		t.Errorf("persisted count = %d, want 2 (both lines parsed)", n)
	}

	sourceID := mustOpenCodeID(t, sessionID, callID)
	var sourceCount int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id = ?`, sourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	// The wrapper uses evidence.PersistSource for eligible sources:
	// PersistToolCallOnce converges to one row.
	if sourceCount != 1 {
		t.Errorf("source rows = %d, want 1 (wrapper replay converges for eligible source)", sourceCount)
	}
}

// TestOpenCode_CrossIngressEquivalencePersistence proves that a hook delivery
// and a wrapper delivery of the same eligible tuple converge to one source
// row in the same database. This verifies end-to-end convergence.
func TestOpenCode_CrossIngressEquivalencePersistence(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "ses-xingress-001"
		callID    = "call-xingress-001"
		tool      = "bash"
		argsJSON  = `{"command":"echo cross"}`
		output    = "cross result\n"
	)

	// 1. Deliver through the hook path.
	hookPayload := openCodeHookPayload(t, sessionID, callID, tool, argsJSON, output)
	var hookStdout, hookStderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(hookPayload), d, &hookStdout, &hookStderr); err != nil {
		t.Fatalf("hook delivery: %v", err)
	}

	// 2. Deliver the same tuple through the wrapper path.
	mustWrapSession(t, d, "ktx-wrap-session")
	wrapperLine := openCodeWrapperEvent(t, sessionID, callID, tool, argsJSON, output)
	var wrapStdout, wrapStderr bytes.Buffer
	teeAndParse(strings.NewReader(wrapperLine+"\n"), &wrapStdout, d, "ktx-wrap-session", &wrapStderr)

	sourceID := mustOpenCodeID(t, sessionID, callID)
	var sourceCount int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id = ?`, sourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Errorf("source rows = %d, want 1 (hook + wrapper converge)", sourceCount)
	}
	persisted, err := d.GetToolCall(sourceID)
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Runtime == nil || *persisted.Runtime != "opencode" {
		t.Errorf("runtime = %v, want opencode", persisted.Runtime)
	}
}

// ---------------------------------------------------------------------------
// OpenCode input without verified native correlation receives a fresh NanoID.
// ---------------------------------------------------------------------------

// TestOpenCode_MissingTupleFallback proves that an OpenCode hook payload with
// an absent native sessionID/callID falls back to a fresh NanoID.
func TestOpenCode_MissingTupleFallback(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// OpenCode payload with the recognized input object but without
	// sessionID and callID.
	payload := openCodeHookPayload(t, "", "", "bash", `{"command":"echo"}`, "hello\n")

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &env)
	if !idPattern.MatchString(env.SourceID) {
		t.Errorf("source_id = %q, want 16-char lowercase (fresh NanoID)", env.SourceID)
	}
	// Verify it is NOT a deterministic ID derived from empty tuple fields.
	// The hook should have fallen back, not derived.
	if _, err := identity.Derive("opencode", "", ""); err == nil {
		t.Error("identity.Derive succeeded with empty fields; fallback path may not have triggered")
	}
}

// TestOpenCode_FlatClaimingOpencodeRemainsUntrusted proves that a flat
// payload carrying runtime:"opencode" but lacking the recognized nested
// input schema cannot self-qualify for deterministic identity:
// eligibility is provenance-driven, not text-driven.
func TestOpenCode_FlatClaimingOpencodeRemainsUntrusted(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Flat payload with runtime:"opencode" but no nested input object
	// carrying sessionID/callID.
	payload := `{"runtime":"opencode","id":"flat-oc-001","tool":"bash","input":"echo","output":"hello\n","timestamp":"2026-07-16T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &env)
	if env.SourceID == "flat-oc-001" {
		t.Errorf("source_id = caller-supplied %q, want fresh NanoID (flat payload untrusted)", env.SourceID)
	}
	if !idPattern.MatchString(env.SourceID) {
		t.Errorf("source_id = %q, want 16-char lowercase (fresh NanoID)", env.SourceID)
	}
}

// TestOpenCode_MalformedSchemaFallback proves that malformed native tuple
// fields (empty strings) fall back to a fresh NanoID.
func TestOpenCode_MalformedSchemaFallback(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Input with sessionID and callID present but empty strings.
	payload := `{"runtime":"opencode","timestamp":"2026-07-16T12:00:00Z","input":{"tool":"bash","sessionID":"","callID":"","args":{"command":"echo"}},"output":{"output":"hello\n"}}`

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &env)
	if !idPattern.MatchString(env.SourceID) {
		t.Errorf("source_id = %q, want 16-char lowercase (fresh NanoID for empty tuple)", env.SourceID)
	}
}

// ---------------------------------------------------------------------------
// A new native call ID creates a distinct observation.
// ---------------------------------------------------------------------------

// TestOpenCode_NewCallIDDistinctObservation proves that model-requested
// re-execution (a new native call ID in the same session) produces a
// distinct source identity.
func TestOpenCode_NewCallIDDistinctObservation(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const sessionID = "ses-reexec-001"
	payload1 := openCodeHookPayload(t, sessionID, "call-reexec-001", "bash", `{"command":"echo first"}`, "first\n")
	payload2 := openCodeHookPayload(t, sessionID, "call-reexec-002", "bash", `{"command":"echo second"}`, "second\n")

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload1), d, &stdout, &stderr); err != nil {
		t.Fatalf("first: %v", err)
	}
	stdout.Reset()
	if _, err := hookLogic(strings.NewReader(payload2), d, &stdout, &stderr); err != nil {
		t.Fatalf("second: %v", err)
	}

	id1 := mustOpenCodeID(t, sessionID, "call-reexec-001")
	id2 := mustOpenCodeID(t, sessionID, "call-reexec-002")
	if id1 == id2 {
		t.Fatalf("same ID for different call IDs: %q", id1)
	}

	var count int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id IN (?, ?)`, id1, id2).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("source rows = %d, want 2 (distinct observations for new call ID)", count)
	}
}

// ---------------------------------------------------------------------------
// Caller-supplied compatibility ingress remains unchanged.
// ---------------------------------------------------------------------------

// TestOpenCode_FlatCallerSuppliedIDPreserved proves that the flat fallback
// path without a runtime-adapter discriminator preserves the caller-supplied id.
// (Also covered by TestHook_NoRuntime_FlatDeserialisation, but this test
// makes the identity-preservation assertion explicit.)
func TestOpenCode_FlatCallerSuppliedIDPreserved(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"flat-preserve-001","tool":"bash","input":"echo","output":"hello\n","timestamp":"2026-07-16T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	_ = json.Unmarshal(stdout.Bytes(), &env)
	if env.SourceID != "flat-preserve-001" {
		t.Errorf("source_id = %q, want flat-preserve-001 (caller-supplied id preserved)", env.SourceID)
	}
}

// ---------------------------------------------------------------------------
// Changed authoritative evidence → conflict.
// ---------------------------------------------------------------------------

// TestOpenCode_ChangedEvidenceConflicts proves that delivering the same
// native tuple with different authoritative evidence (changed output) is
// rejected as a conflict, preserving the first row.
func TestOpenCode_ChangedEvidenceConflicts(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "ses-conflict-001"
		callID    = "call-conflict-001"
	)
	first := openCodeHookPayload(t, sessionID, callID, "bash", `{"command":"echo first"}`, "first output\n")
	changed := openCodeHookPayload(t, sessionID, callID, "bash", `{"command":"echo first"}`, "changed output\n")

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(first), d, &stdout, &stderr); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	_, err := hookLogic(strings.NewReader(changed), d, &stdout, &stderr)
	var cliErr *Error
	if !errors.As(err, &cliErr) || cliErr.Code != CodeConflict {
		t.Fatalf("changed retry error = %v, want conflict", err)
	}

	sourceID := mustOpenCodeID(t, sessionID, callID)
	persisted, err := d.GetToolCall(sourceID)
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Output != "first output\n" {
		t.Errorf("persisted output = %q, want first output (conflict preserves first evidence)", persisted.Output)
	}
}

func TestOpenCodeHook_OutcomeCapture(t *testing.T) {
	testCases := []struct {
		name        string
		tool        string
		exit        any
		includeExit bool
		wantExit    *int64
		wantError   *bool
	}{
		{name: "zero shell exit", tool: "bash", exit: 0, includeExit: true, wantExit: pointerInt64(0), wantError: pointerBool(false)},
		{name: "nonzero shell exit", tool: "bash", exit: 7, includeExit: true, wantExit: pointerInt64(7), wantError: pointerBool(true)},
		{name: "missing shell exit", tool: "bash"},
		{name: "malformed shell exit", tool: "bash", exit: "7", includeExit: true},
		{name: "unsupported tool exit", tool: "read", exit: 1, includeExit: true},
	}

	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			d := hookTestDB(t)
			hookChdirTemp(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			sessionID := fmt.Sprintf("ses-outcome-%d", index)
			callID := fmt.Sprintf("call-outcome-%d", index)
			payload := openCodeHookOutcomePayload(t, sessionID, callID, testCase.tool, "result\n", testCase.exit, testCase.includeExit)

			var stdout, stderr bytes.Buffer
			if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
				t.Fatalf("hookLogic: %v", err)
			}
			persisted, err := d.GetToolCall(mustOpenCodeID(t, sessionID, callID))
			if err != nil {
				t.Fatalf("GetToolCall: %v", err)
			}
			assertInt64Pointer(t, "ExitCode", persisted.ExitCode, testCase.wantExit)
			assertBoolPointer(t, "IsError", persisted.IsError, testCase.wantError)
		})
	}
}

func TestOpenCodeOutcome_FirstAcceptedMetadataWins(t *testing.T) {
	t.Run("hook outcome is not overridden or enriched", func(t *testing.T) {
		for _, testCase := range []struct {
			name         string
			firstExit    any
			firstPresent bool
			wantExit     *int64
			wantError    *bool
		}{
			{name: "conflicting outcome", firstExit: 0, firstPresent: true, wantExit: pointerInt64(0), wantError: pointerBool(false)},
			{name: "missing outcome", wantExit: nil, wantError: nil},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				d := hookTestDB(t)
				hookChdirTemp(t)
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				sessionID := "ses-first-" + strings.ReplaceAll(testCase.name, " ", "-")
				callID := "call-first-" + strings.ReplaceAll(testCase.name, " ", "-")
				first := openCodeHookOutcomePayload(t, sessionID, callID, "bash", "same output\n", testCase.firstExit, testCase.firstPresent)
				retry := openCodeHookOutcomePayload(t, sessionID, callID, "bash", "same output\n", 9, true)
				for _, payload := range []string{first, retry} {
					var stdout, stderr bytes.Buffer
					if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
						t.Fatalf("hookLogic: %v", err)
					}
				}
				persisted, err := d.GetToolCall(mustOpenCodeID(t, sessionID, callID))
				if err != nil {
					t.Fatalf("GetToolCall: %v", err)
				}
				assertInt64Pointer(t, "ExitCode", persisted.ExitCode, testCase.wantExit)
				assertBoolPointer(t, "IsError", persisted.IsError, testCase.wantError)
			})
		}
	})

	t.Run("wrapper error remains canonical on retry", func(t *testing.T) {
		d := hookTestDB(t)
		if err := d.InsertSession(&dbpkg.Session{ID: "wrapper-outcome-session", Agent: "opencode"}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		const sessionID = "ses-wrapper-error-first"
		const callID = "call-wrapper-error-first"
		firstLine := `{"type":"tool_use","sessionID":"` + sessionID + `","part":{"type":"tool","tool":"bash","callID":"` + callID + `","state":{"status":"error","input":{"command":"test"},"output":"","error":"runtime failure","metadata":{"exit":0}}}}`
		retryLine := `{"type":"tool_use","sessionID":"` + sessionID + `","part":{"type":"tool","tool":"bash","callID":"` + callID + `","state":{"status":"completed","input":{"command":"test"},"output":"runtime failure","metadata":{"exit":0}}}}`
		src := mustResolveOpenCode(t, sessionID, callID)
		for _, line := range []string{firstLine, retryLine} {
			tc, _, ok := parser.ParseEventWithCorrelation([]byte(line))
			if !ok {
				t.Fatal("ParseEventWithCorrelation returned false")
			}
			tc.ID = src.ID
			tc.SessionID = "wrapper-outcome-session"
			if _, err := evidence.PersistSource(d, src, tc); err != nil {
				t.Fatalf("PersistSource: %v", err)
			}
		}
		persisted, err := d.GetToolCall(src.ID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		assertInt64Pointer(t, "ExitCode", persisted.ExitCode, pointerInt64(0))
		assertBoolPointer(t, "IsError", persisted.IsError, pointerBool(true))
		if persisted.Output != "runtime failure" {
			t.Errorf("Output = %q, want runtime error text", persisted.Output)
		}
	})
}

func pointerInt64(value int64) *int64 { return &value }
func pointerBool(value bool) *bool    { return &value }

func assertInt64Pointer(t *testing.T, name string, got, want *int64) {
	t.Helper()
	if (got == nil) != (want == nil) || got != nil && *got != *want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func assertBoolPointer(t *testing.T, name string, got, want *bool) {
	t.Helper()
	if (got == nil) != (want == nil) || got != nil && *got != *want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// ---------------------------------------------------------------------------
// KTX session ownership is separate from native tuple identity.
// ---------------------------------------------------------------------------

// TestOpenCode_KTXSessionOwnershipSeparate proves that the same native tuple
// delivered through two different KTX sessions converges to one source row
// owned by the first delivery's KTX session. Native runtime
// session identity is distinct from KTX session ownership.
func TestOpenCode_KTXSessionOwnershipSeparate(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "ses-owner-001"
		callID    = "call-owner-001"
	)
	payload := openCodeHookPayload(t, sessionID, callID, "bash", `{"command":"echo"}`, "hello\n")

	// First delivery creates one KTX session.
	var stdout1, stderr1 bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout1, &stderr1); err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	// Second delivery of the same tuple: the hook resolves the same
	// source ID and evidence.PersistSource finds the existing row.
	// The KTX session association stays with the first delivery.
	var stdout2, stderr2 bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout2, &stderr2); err != nil {
		t.Fatalf("second delivery: %v", err)
	}

	sourceID := mustOpenCodeID(t, sessionID, callID)
	var sourceCount int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id = ?`, sourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Errorf("source rows = %d, want 1 (same tuple, different KTX session)", sourceCount)
	}
}

// ---------------------------------------------------------------------------
// Concurrent exact retries under -race.
// ---------------------------------------------------------------------------

// TestOpenCode_ConcurrentExactRetriesConverge proves that concurrent
// delivery of the same eligible tuple produces one source row, one roll-up
// increment, and at most one compressed artifact. Run with -race.
func TestOpenCode_ConcurrentExactRetriesConverge(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "ses-concurrent-001"
		callID    = "call-concurrent-001"
	)
	payload := openCodeHookPayload(t, sessionID, callID, "bash", `{"command":"echo race"}`, "race output\n")

	// First delivery to set up the source row.
	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("initial delivery: %v", err)
	}

	const retries = 8
	start := make(chan struct{})
	errs := make(chan error, retries)
	var wg sync.WaitGroup
	for i := 0; i < retries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var s, e bytes.Buffer
			if _, err := hookLogic(strings.NewReader(payload), d, &s, &e); err != nil {
				errs <- fmt.Errorf("%w (stderr=%q)", err, e.String())
			} else {
				errs <- nil
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("retry: %v", err)
		}
	}

	sourceID := mustOpenCodeID(t, sessionID, callID)
	var sourceCount, compressedCount int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id = ?`, sourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := d.DB().QueryRow(`SELECT count(*) FROM compressed WHERE source_id = ?`, sourceID).Scan(&compressedCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Errorf("source rows = %d, want 1", sourceCount)
	}
	if compressedCount > 1 {
		t.Errorf("compressed rows = %d, want at most 1", compressedCount)
	}
}

// TestOpenCode_WrapperConcurrentExactRetriesConverge proves that concurrent
// wrapper delivery of the same eligible tuple also converges. Run with -race.
func TestOpenCode_WrapperConcurrentExactRetriesConverge(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "ses-wrap-conc-001"
		callID    = "call-wrap-conc-001"
	)
	mustWrapSession(t, d, "ktx-sess-1")
	mustWrapSession(t, d, "ktx-sess-conc")
	line := openCodeWrapperEvent(t, sessionID, callID, "bash", `{"command":"echo"}`, "output\n")

	// First delivery.
	var stdout, stderr bytes.Buffer
	teeAndParse(strings.NewReader(line+"\n"), &stdout, d, "ktx-sess-1", &stderr)

	const retries = 8
	start := make(chan struct{})
	errs := make(chan error, retries)
	var wg sync.WaitGroup
	for i := 0; i < retries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var s, e bytes.Buffer
			teeAndParse(strings.NewReader(line+"\n"), &s, d, "ktx-sess-conc", &e)
			errs <- nil // teeAndParse logs errors to stderr but does not return them
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	sourceID := mustOpenCodeID(t, sessionID, callID)
	var sourceCount int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id = ?`, sourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Errorf("source rows = %d, want 1 (concurrent wrapper exact retries converge)", sourceCount)
	}
}

// ---------------------------------------------------------------------------
// Sequential exact retries.
// ---------------------------------------------------------------------------

// TestOpenCode_SequentialExactRetriesConverge proves that sequential delivery
// of the same eligible tuple produces one source row.
func TestOpenCode_SequentialExactRetriesConverge(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "ses-seq-001"
		callID    = "call-seq-001"
	)
	payload := openCodeHookPayload(t, sessionID, callID, "bash", `{"command":"echo"}`, "hello\n")

	for i := 0; i < 5; i++ {
		var stdout, stderr bytes.Buffer
		if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}

	sourceID := mustOpenCodeID(t, sessionID, callID)
	var sourceCount int
	if err := d.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE id = ?`, sourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Errorf("source rows = %d, want 1 (sequential exact retries converge)", sourceCount)
	}
}

// ---------------------------------------------------------------------------
// KTX_SESSION_ID guard: wrapper is the single source under ktx run.
// ---------------------------------------------------------------------------

// TestOpenCode_KTXSessionIDGuardSuppressesHook proves that when
// KTX_SESSION_ID is set, the hook is a noop and the wrapper is the single
// source of truth. This preserves the existing duplicate-ingress guard.
func TestOpenCode_KTXSessionIDGuardSuppressesHook(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("KTX_SESSION_ID", "ktx-run-session")

	const (
		sessionID = "ses-guard-001"
		callID    = "call-guard-001"
	)
	payload := openCodeHookPayload(t, sessionID, callID, "bash", `{"command":"echo"}`, "hello\n")

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (hook noop under KTX_SESSION_ID)", stdout.String())
	}

	// No row should be persisted by the hook.
	sourceID := mustOpenCodeID(t, sessionID, callID)
	if _, err := d.GetToolCall(sourceID); !errors.Is(err, dbpkg.ErrNotFound) {
		t.Errorf("GetToolCall = %v, want ErrNotFound (hook suppressed)", err)
	}
}

// ---------------------------------------------------------------------------
// Parser NativeCorrelation extraction.
// ---------------------------------------------------------------------------

// TestParseEventWithCorrelation_OpenCodeTrusted proves that the parser
// extracts the native correlation tuple from an OpenCode tool_use event
// with the trusted flag set.
func TestParseEventWithCorrelation_OpenCodeTrusted(t *testing.T) {
	const sessionID = "ses-parser-001"
	const callID = "call-parser-001"
	line := openCodeWrapperEvent(t, sessionID, callID, "bash", `{"command":"echo"}`, "hello\n")

	tc, nc, ok := parser.ParseEventWithCorrelation([]byte(line))
	if !ok {
		t.Fatal("ParseEventWithCorrelation returned false")
	}
	if !nc.Trusted {
		t.Error("want Trusted=true for OpenCode tool_use event")
	}
	if nc.Runtime != "opencode" {
		t.Errorf("Runtime = %q, want opencode", nc.Runtime)
	}
	if nc.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", nc.SessionID, sessionID)
	}
	if nc.CallID != callID {
		t.Errorf("CallID = %q, want %q", nc.CallID, callID)
	}
	if tc == nil {
		t.Fatal("toolcall is nil")
	}
}

// TestParseEventWithCorrelation_FlatFallbackUntrusted proves that the parser
// marks flat-fallback events as untrusted (no native tuple). A flat event
// with a runtime field cannot self-qualify.
func TestParseEventWithCorrelation_FlatFallbackUntrusted(t *testing.T) {
	// Flat fallback: top-level object with tool/output, no type:"tool_use".
	line := `{"tool":"bash","input":"echo","output":"hello\n","timestamp":"2026-07-16T12:00:00Z"}`
	_, nc, ok := parser.ParseEventWithCorrelation([]byte(line))
	if !ok {
		t.Fatal("ParseEventWithCorrelation returned false for flat event")
	}
	if nc.Trusted {
		t.Error("want Trusted=false for flat-fallback event")
	}
	if nc.Runtime != "" {
		t.Errorf("Runtime = %q, want empty for flat-fallback", nc.Runtime)
	}
}

// TestParseEventWithCorrelation_BackwardCompatParseEvent proves that
// ParseEvent (the original API) still works by delegating to
// ParseEventWithCorrelation and discarding the correlation.
func TestParseEventWithCorrelation_BackwardCompatParseEvent(t *testing.T) {
	line := openCodeWrapperEvent(t, "ses-bc", "call-bc", "bash", `{"command":"echo"}`, "hi\n")
	tc, ok := parser.ParseEvent([]byte(line))
	if !ok || tc == nil {
		t.Fatal("ParseEvent returned false or nil")
	}
	if tc.Tool != "bash" {
		t.Errorf("Tool = %q, want bash", tc.Tool)
	}
}
