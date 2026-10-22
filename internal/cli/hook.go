package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/evidence"
	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/router"
)

// hookEnvelope is the JSON response written by `ktx hook` to stdout.
// The plugin parses this and sets output.output = Output (the
// compressed bytes the agent sees). CompressedID and SourceID are
// developer-facing (for reversibility via `ktx decompress`) and are
// never injected into the agent's context (invisibility).
type hookEnvelope struct {
	Output           string  `json:"output"`
	CompressedID     *string `json:"compressed_id"`
	SourceID         string  `json:"source_id"`
	KTXSessionID     string  `json:"ktx_session_id,omitempty"`
	RuntimeSessionID string  `json:"runtime_session_id,omitempty"`
	CallID           string  `json:"call_id,omitempty"`
	RecoveryCue      bool    `json:"recovery_cue,omitempty"`
}

// codexPostToolUseEvent is the sole hook_event_name value accepted for Codex
// extraction. Trusted provenance additionally requires the complete native
// input contract. Any other non-empty hook_event_name is rejected before
// persistence. An absent or empty hook_event_name falls back to OpenCode/flat
// deserialisation (backward compatible).
const codexPostToolUseEvent = "PostToolUse"

// codexHookOutput is the Codex PostToolUse hook response written by
// `ktx hook` to stdout when the input payload carries hook_event_name
// exactly equal to "PostToolUse". When the pipeline transforms the
// output, the hook blocks the original tool result and replaces it
// with the compressed text in reason. When passthrough (no transform),
// an empty JSON object is emitted and Codex leaves the tool result
// unchanged.
type codexHookOutput struct {
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// hookIngress names the input contract a hook payload arrived through. It
// selects extraction, the response shape, and diagnostic labels; it does not
// imply trusted provenance, which the native schema checks decide.
type hookIngress uint8

const (
	ingressFlat hookIngress = iota
	ingressOpenCode
	ingressCodex
	ingressClaudeCode
)

// adapter reports whether the payload arrived through a recognized runtime
// adapter contract rather than flat sidecar input.
func (i hookIngress) adapter() bool { return i != ingressFlat }

// agent is the session agent label recorded for an eligible adapter source.
func (i hookIngress) agent() string {
	switch i {
	case ingressCodex:
		return "codex"
	case ingressClaudeCode:
		return "claude-code"
	default:
		return "opencode"
	}
}

func writeTrustedHookOutput(stdout io.Writer, ingress hookIngress, result evidence.Result, output string, response claudeCodeToolResponse, runtimeSessionID, runtimeCallID string, recoveryCue bool) error {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if ingress == ingressClaudeCode {
		return writeClaudeCodeHookOutput(enc, result, output, response)
	}
	if ingress == ingressCodex {
		var response codexHookOutput
		if result.Transformed {
			response = codexHookOutput{Decision: "block", Reason: output}
		}
		if err := enc.Encode(response); err != nil {
			return internalError("ktx hook: write codex output: %v", err)
		}
		return nil
	}

	var compressedID *string
	if result.CompressedID != "" {
		compressedID = &result.CompressedID
	}
	envelope := hookEnvelope{Output: output, CompressedID: compressedID, SourceID: result.SourceID}
	envelope.RecoveryCue = recoveryCue
	if benchmarkEnv, present, _ := benchcontract.ReadEnvironment(); present {
		envelope.KTXSessionID = benchmarkEnv.SessionID
		envelope.RuntimeSessionID = runtimeSessionID
		envelope.CallID = runtimeCallID
	} else if os.Getenv("KTX_COLLECTION_ID") != "" {
		envelope.KTXSessionID = os.Getenv("KTX_SESSION_ID")
		envelope.RuntimeSessionID = runtimeSessionID
		envelope.CallID = runtimeCallID
	}
	if err := enc.Encode(envelope); err != nil {
		return internalError("ktx hook: write envelope: %v", err)
	}
	return nil
}

// claudeCodeHookOutput is the Claude Code PostToolUse hook response. When
// the pipeline transforms a replaceable result, updatedToolOutput carries the
// original tool_response with only stdout replaced by the delivered text.
// Otherwise the response is an empty object and Claude Code keeps the
// original result.
type claudeCodeHookOutput struct {
	HookSpecificOutput *claudeCodeHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type claudeCodeHookSpecificOutput struct {
	HookEventName     string         `json:"hookEventName"`
	UpdatedToolOutput map[string]any `json:"updatedToolOutput"`
}

func writeClaudeCodeHookOutput(enc *json.Encoder, result evidence.Result, output string, response claudeCodeToolResponse) error {
	var out claudeCodeHookOutput
	if result.Transformed && response.replaceable {
		updated := make(map[string]any, len(response.fields))
		for key, value := range response.fields {
			updated[key] = value
		}
		updated["stdout"] = output
		out.HookSpecificOutput = &claudeCodeHookSpecificOutput{HookEventName: claudeCodePostToolUseEvent, UpdatedToolOutput: updated}
	}
	if err := enc.Encode(out); err != nil {
		return internalError("ktx hook: write Claude Code output: %v", err)
	}
	return nil
}

// hookRuntimeClaudeCode is the only value `ktx hook --runtime` accepts. It
// selects the Claude Code PostToolUse input and output contract.
const hookRuntimeClaudeCode = "claude-code"

// claudeCodePostToolUseEvent is the only hook_event_name accepted when the
// Claude Code runtime is selected.
const claudeCodePostToolUseEvent = "PostToolUse"

// hookParsedArgs carries `ktx hook` flags through the dispatcher. runtime is
// empty unless --runtime selected an adapter contract.
type hookParsedArgs struct {
	runtime string
}

// parseHookDispatchArgs consumes `--runtime <value>` and `--runtime=<value>`.
// The value must be "claude-code"; a missing, different, or repeated value is
// a usage error reported before the database opens.
func parseHookDispatchArgs(args []string) ([]string, any, error) {
	var parsed hookParsedArgs
	seen := false
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		var value string
		switch {
		case arg == "--runtime":
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("ktx hook: --runtime requires a value; the only accepted value is %q", hookRuntimeClaudeCode)
			}
			i++
			value = args[i]
		case strings.HasPrefix(arg, "--runtime="):
			value = strings.TrimPrefix(arg, "--runtime=")
		default:
			rest = append(rest, arg)
			continue
		}
		if seen {
			return nil, nil, errors.New("ktx hook: --runtime may be given only once")
		}
		seen = true
		if value != hookRuntimeClaudeCode {
			return nil, nil, fmt.Errorf("ktx hook: unsupported --runtime %q; the only accepted value is %q", value, hookRuntimeClaudeCode)
		}
		parsed.runtime = value
	}
	return rest, parsed, nil
}

// hookExitCode keeps a Claude Code hook from exiting with the not-found
// status. Claude Code shows a PostToolUse hook's stderr to the model when the
// hook exits 2, so that failure exits as a usage error instead.
func hookExitCode(parsedArgs any, code int) int {
	if parsed, ok := parsedArgs.(hookParsedArgs); ok && parsed.runtime == hookRuntimeClaudeCode && code == ExitNotFound {
		return ExitUsage
	}
	return code
}

// hookCommandFunc is the dispatch entry point for `ktx hook`. The
// dispatcher opens a current schema without exclusive coordination and exposes
// stdin (NeedsStdin=true, PassthroughStdout=true). Creation and migration keep
// the normal coordinated fallback.
func hookCommandFunc(ctx *DispatchContext) (any, error) {
	parsed, _ := ctx.ParsedArgs.(hookParsedArgs)
	return hookLogicFor(ctx.Stdin, ctx.DB, ctx.Stdout, ctx.Stderr, parsed.runtime)
}

// openCodeInput is the OpenCode callback input shape: an object with a
// tool name (string), the native session and call identifiers, and an args
// object. The hook extracts these into the flat ToolCall fields when
// runtime="opencode". The native sessionID and callID form the trusted
// correlation tuple the hook uses for deterministic identity.
type openCodeInput struct {
	Tool      string          `json:"tool"`
	SessionID string          `json:"sessionID"`
	CallID    string          `json:"callID"`
	Args      json.RawMessage `json:"args"`
}

// openCodeOutputObject is the OpenCode callback output shape when the
// output is an object carrying an output field (string).
type openCodeOutputObject struct {
	Output   *string                    `json:"output"`
	Metadata map[string]json.RawMessage `json:"metadata"`
}

// extractOpenCodeFields extracts the flat tool, input, output strings, and
// the native correlation identifiers (sessionID, callID) from raw OpenCode
// callback input and output JSON values. The input is an object with a tool
// (string), sessionID, callID, and args (object); the output is either a bare
// string or an object with an output field. An empty tool name falls back to
// "unknown". A present string output, including empty, is retained; an object
// without a usable string output falls back to the stringified object.
// The native sessionID and callID are empty when absent
// from the input; the caller uses them to resolve deterministic identity.
func extractOpenCodeFields(inputRaw, outputRaw json.RawMessage) (tool, input, output, sessionID, callID string) {
	tool = "unknown"

	if len(inputRaw) > 0 {
		var in openCodeInput
		if err := json.Unmarshal(inputRaw, &in); err == nil && in.Tool != "" {
			tool = in.Tool
		}
		sessionID = in.SessionID
		callID = in.CallID
		if len(in.Args) > 0 {
			input = string(in.Args)
		}
	}

	if len(outputRaw) > 0 {
		var s string
		if err := json.Unmarshal(outputRaw, &s); err == nil {
			output = s
		} else {
			var out openCodeOutputObject
			if err := json.Unmarshal(outputRaw, &out); err == nil && out.Output != nil {
				output = *out.Output
			} else {
				output = string(outputRaw)
			}
		}
	}

	return tool, input, output, sessionID, callID
}

// extractOpenCodeOutcome retains process-exit evidence only for the pinned
// built-in shell producer. The after-hook has no terminal state, so all other
// tools and missing or malformed exits remain unknown.
func extractOpenCodeOutcome(tool string, outputRaw json.RawMessage) (exitCode *int64, isError *bool) {
	if tool != "bash" || len(outputRaw) == 0 {
		return nil, nil
	}
	var out openCodeOutputObject
	if err := json.Unmarshal(outputRaw, &out); err != nil {
		return nil, nil
	}
	raw, ok := out.Metadata["exit"]
	if !ok {
		return nil, nil
	}
	var exit *int64
	if err := json.Unmarshal(raw, &exit); err != nil || exit == nil {
		return nil, nil
	}
	failed := *exit != 0
	return exit, &failed
}

// codexToolResponseObject covers the known Codex tool_response shapes:
// an object carrying aggregated_output, output, or text (checked in
// that order before falling back to the stringified object).
type codexToolResponseObject struct {
	AggregatedOutput *string `json:"aggregated_output"`
	Output           *string `json:"output"`
	Text             *string `json:"text"`
}

var codexPostToolUseFields = map[string]bool{
	"agent_id":        true,
	"agent_type":      true,
	"cwd":             true,
	"hook_event_name": true,
	"model":           true,
	"permission_mode": true,
	"session_id":      true,
	"tool_input":      true,
	"tool_name":       true,
	"tool_response":   true,
	"tool_use_id":     true,
	"transcript_path": true,
	"turn_id":         true,
}

// validCodexPostToolUse reports whether a payload matches the pinned native
// PostToolUse input contract. tool_input and tool_response are intentionally
// left unconstrained; their nested content does not carry outcome meaning.
func validCodexPostToolUse(raw map[string]json.RawMessage) bool {
	for key := range raw {
		if !codexPostToolUseFields[key] {
			return false
		}
	}
	for _, key := range []string{"cwd", "model", "permission_mode", "session_id", "tool_name", "tool_use_id", "turn_id"} {
		if !jsonString(raw[key]) {
			return false
		}
	}
	var eventName, permissionMode string
	if json.Unmarshal(raw["hook_event_name"], &eventName) != nil || eventName != codexPostToolUseEvent {
		return false
	}
	if json.Unmarshal(raw["permission_mode"], &permissionMode) != nil {
		return false
	}
	switch permissionMode {
	case "default", "acceptEdits", "plan", "dontAsk", "bypassPermissions":
	default:
		return false
	}
	if _, ok := raw["tool_input"]; !ok {
		return false
	}
	if _, ok := raw["tool_response"]; !ok {
		return false
	}
	transcript, ok := raw["transcript_path"]
	if !ok || !nullOrJSONString(transcript) {
		return false
	}
	for _, key := range []string{"agent_id", "agent_type"} {
		if value, present := raw[key]; present && !jsonString(value) {
			return false
		}
	}
	return true
}

func jsonString(raw json.RawMessage) bool {
	var value *string
	return json.Unmarshal(raw, &value) == nil && value != nil
}

func nullOrJSONString(raw json.RawMessage) bool {
	var value *string
	return json.Unmarshal(raw, &value) == nil
}

func nonEmptyJSONString(raw json.RawMessage) bool {
	var value *string
	return json.Unmarshal(raw, &value) == nil && value != nil && *value != ""
}

// jsonObject reports whether a raw JSON value is an object. A JSON null
// decodes into a nil map without error, so the leading byte decides.
func jsonObject(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "{")
}

func jsonTrue(raw json.RawMessage) bool {
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

// validClaudeCodePostToolUse reports whether a Claude Code PostToolUse
// payload carries the fields trusted provenance needs: non-empty session_id
// and tool_use_id strings, a tool_name string, and tool_input and
// tool_response objects. Other fields are allowed because Claude Code adds
// fields across releases. The caller has already checked hook_event_name.
func validClaudeCodePostToolUse(raw map[string]json.RawMessage) bool {
	return nonEmptyJSONString(raw["session_id"]) &&
		nonEmptyJSONString(raw["tool_use_id"]) &&
		jsonString(raw["tool_name"]) &&
		jsonObject(raw["tool_input"]) &&
		jsonObject(raw["tool_response"])
}

// claudeCodeToolResponse is a decoded Claude Code tool_response. fields keeps
// every key's raw value so a replacement can return the response with only
// stdout changed.
type claudeCodeToolResponse struct {
	fields      map[string]json.RawMessage
	output      string
	interrupted bool
	replaceable bool
}

// decodeClaudeCodeToolResponse extracts the recorded output and decides,
// before any pipeline runs, whether the delivered output may replace stdout.
// The recorded output is the stdout string. When tool_response is not an
// object or its stdout is not a string, the recorded output is the raw
// tool_response JSON, so the stored source still holds what the runtime
// returned. A result is replaceable only when stdout is a string, isImage
// and interrupted are not true, and backgroundTaskId is absent.
func decodeClaudeCodeToolResponse(raw json.RawMessage) claudeCodeToolResponse {
	var response claudeCodeToolResponse
	if len(raw) > 0 {
		response.output = string(raw)
	}
	if !jsonObject(raw) || json.Unmarshal(raw, &response.fields) != nil {
		response.fields = nil
		return response
	}
	response.interrupted = jsonTrue(response.fields["interrupted"])
	var stdout *string
	stdoutString := json.Unmarshal(response.fields["stdout"], &stdout) == nil && stdout != nil
	if stdoutString {
		response.output = *stdout
	}
	_, background := response.fields["backgroundTaskId"]
	response.replaceable = stdoutString && !jsonTrue(response.fields["isImage"]) && !response.interrupted && !background
	return response
}

// extractCodexFields extracts the flat tool, input, and output strings
// from a Codex PostToolUse payload's top-level tool_name, tool_input,
// and tool_response fields. The tool_input is stringified JSON. The
// tool_response is either a bare string or an object carrying
// aggregated_output, output, or text; an object without a recognised
// field falls back to the stringified object. An empty tool name
// falls back to "unknown".
func extractCodexFields(toolName string, toolInputRaw, toolResponseRaw json.RawMessage) (tool, input, output string) {
	tool = "unknown"
	if toolName != "" {
		tool = toolName
	}

	if len(toolInputRaw) > 0 {
		input = string(toolInputRaw)
	}

	if len(toolResponseRaw) > 0 {
		var s string
		if err := json.Unmarshal(toolResponseRaw, &s); err == nil {
			output = s
		} else {
			var resp codexToolResponseObject
			if err := json.Unmarshal(toolResponseRaw, &resp); err == nil {
				switch {
				case resp.AggregatedOutput != nil:
					output = *resp.AggregatedOutput
				case resp.Output != nil:
					output = *resp.Output
				case resp.Text != nil:
					output = *resp.Text
				default:
					output = string(toolResponseRaw)
				}
			} else {
				output = string(toolResponseRaw)
			}
		}
	}

	return tool, input, output
}

// hookLogic reads a JSON tool_call from stdin, persists the original
// via the shared PersistToolCall code path (DRY, same code ktx write
// calls), then delegates to compressPipeline for the failure gate +
// pipeline (DRY, same code ktx compress calls). A compressed row is
// created ONLY when the pipeline actually transforms the output;
// passthrough (no ktx.toml) and failure-gate-skip (is_error=true)
// return the source output unchanged with compressed_id=null.
//
// When hook_event_name is exactly "PostToolUse", the hook extracts tool name,
// input, and output from the top-level tool_name, tool_input, and tool_response
// fields. A schema-conforming payload is explicit success and receives trusted
// provenance; nested tool_response content is never read as outcome evidence.
// Nonconforming payloads retain extraction compatibility but receive fresh
// identity and unknown outcome. Exact trusted retries reuse the first row,
// while changed authoritative evidence for the same tuple is rejected as a
// conflict. Any other non-empty hook_event_name is rejected before persistence
// (_fail before side effects_).
//
// When runtime="opencode" is present, the hook extracts tool name, input
// string, and output string from raw OpenCode callback objects
// (input.tool + input.args, output as string or {output: string})
// instead of requiring a flat tool_call shape. The recognized adapter
// input also carries the native sessionID and callID that form the
// trusted correlation tuple; when both are present and valid, the
// source id is derived deterministically and exact retries converge
// to one row. When the native tuple is incomplete or absent the hook
// assigns a fresh NanoID with no deduplication guarantee. A flat
// payload that carries runtime:"opencode" but lacks the recognized
// nested input schema cannot self-qualify for deterministic identity.
// Any other payload (absent or empty hook_event_name and
// absent/unrecognised runtime) falls back to flat tool_call
// deserialisation (backward compatible).
//
// The JSON envelope is written to stdout as a single line. On any
// error, no envelope is written; the exit code and stderr error
// envelope are the dispatcher's responsibility. The plugin level
// (tool.execute.after) catches non-zero exit and leaves output.output
// unchanged (conservative failure at the plugin level).
type hookPhaseObserver func(kind, phase string)

const (
	hookExclusiveAcquisition = "exclusive_acquisition"
	hookAcceptanceCommit     = "acceptance_commit"
)

func hookLogic(stdin io.Reader, db *dbpkg.DB, stdout, stderr io.Writer) (any, error) {
	return runHook(stdin, db, stdout, stderr, "", nil)
}

// hookLogicFor runs the hook with an explicit runtime contract. An empty
// runtime keeps payload-based dispatch; "claude-code" selects the Claude Code
// PostToolUse contract.
func hookLogicFor(stdin io.Reader, db *dbpkg.DB, stdout, stderr io.Writer, runtime string) (any, error) {
	return runHook(stdin, db, stdout, stderr, runtime, nil)
}

// hookLogicObserved exposes successful hook write phases to package tests.
// It does not emit production output or change phase ownership.
func hookLogicObserved(stdin io.Reader, db *dbpkg.DB, stdout, stderr io.Writer, observe hookPhaseObserver) (any, error) {
	return runHook(stdin, db, stdout, stderr, "", observe)
}

func runHook(stdin io.Reader, db *dbpkg.DB, stdout, stderr io.Writer, runtime string, observe hookPhaseObserver) (any, error) {
	explicitCollectionID := os.Getenv("KTX_COLLECTION_ID")
	benchmarkEnv, benchmarkPresent, benchmarkErr := benchcontract.ReadEnvironment()
	if os.Getenv("KTX_SESSION_ID") != "" && explicitCollectionID == "" && !benchmarkPresent {
		return nil, nil
	}
	if benchmarkErr != nil {
		return nil, usageError("ktx hook: %v", benchmarkErr)
	}
	collectionID, ownerSessionID, ownerPresent, ownerErr := recoveryEvidenceOwner(db)
	if ownerErr != nil {
		return nil, usageError("ktx hook: collection ownership mismatch: %v", ownerErr)
	}

	body, err := io.ReadAll(stdin)
	if err != nil {
		return nil, internalError("ktx hook: read stdin: %v", err)
	}
	if len(body) == 0 {
		return nil, usageError("ktx hook: no input on stdin")
	}

	var tc dbpkg.ToolCall

	// Peek at hook_event_name and runtime to decide between Codex,
	// OpenCode callback extraction, and flat tool_call deserialisation.
	// Only the exact Codex PostToolUse event enters Codex extraction; the
	// complete native contract below controls trusted provenance and success.
	// Any other non-empty hook_event_name is rejected before persistence.
	// An absent or empty hook_event_name falls back to OpenCode/flat.
	// The Claude Code runtime flag bypasses the peek; its branch checks the
	// event name itself.
	var peek struct {
		Runtime       *string `json:"runtime"`
		HookEventName *string `json:"hook_event_name"`
	}
	if runtime != hookRuntimeClaudeCode {
		if err := json.Unmarshal(body, &peek); err != nil {
			return nil, usageError("ktx hook: parse JSON: %v", err)
		}
	}

	ingress := ingressFlat
	var runtimeSessionID, runtimeCallID string
	var src correlation.Source
	var claudeResponse claudeCodeToolResponse
	if runtime == hookRuntimeClaudeCode {
		ingress = ingressClaudeCode
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, usageError("ktx hook: parse JSON: %v", err)
		}
		var eventName *string
		if json.Unmarshal(raw["hook_event_name"], &eventName) != nil || eventName == nil || *eventName != claudeCodePostToolUseEvent {
			// Fail before side effects: the Claude Code hook is registered
			// only for PostToolUse, so any other event is a wiring error.
			got := "absent"
			if value, ok := raw["hook_event_name"]; ok {
				got = string(value)
			}
			return nil, &Error{
				Code: CodeUsage,
				Message: fmt.Sprintf(
					`ktx hook: --runtime %s requires hook_event_name %q; got %s. Register the hook for the PostToolUse event only.`,
					hookRuntimeClaudeCode, claudeCodePostToolUseEvent, got,
				),
				Extra: map[string]any{"valid_values": []string{claudeCodePostToolUseEvent}},
			}
		}
		var toolName, sessionID, toolUseID string
		if v := raw["tool_name"]; len(v) > 0 {
			_ = json.Unmarshal(v, &toolName)
		}
		if v := raw["session_id"]; len(v) > 0 {
			_ = json.Unmarshal(v, &sessionID)
		}
		if v := raw["tool_use_id"]; len(v) > 0 {
			_ = json.Unmarshal(v, &toolUseID)
		}
		tc.Tool = "unknown"
		if toolName != "" {
			tc.Tool = toolName
		}
		if v := raw["tool_input"]; len(v) > 0 {
			tc.Input = string(v)
		}
		claudeResponse = decodeClaudeCodeToolResponse(raw["tool_response"])
		tc.Output = claudeResponse.output
		provenance := correlation.Untrusted()
		if validClaudeCodePostToolUse(raw) {
			provenance = correlation.Trusted(correlation.ProvenanceClaudeCodePostToolUse)
			// An interrupted call has no completion evidence, so its outcome
			// stays unknown.
			if !claudeResponse.interrupted {
				succeeded := false
				tc.IsError = &succeeded
			}
		}
		resolved, err := correlation.Resolve(
			correlation.NativeTuple{Runtime: hookRuntimeClaudeCode, SessionID: sessionID, CallID: toolUseID},
			provenance)
		if err != nil {
			return nil, internalError("ktx hook: resolve identity: %v", err)
		}
		src = resolved
		tc.ID = src.ID
		rt := hookRuntimeClaudeCode
		tc.Runtime = &rt
	} else if peek.HookEventName != nil && *peek.HookEventName != "" {
		if *peek.HookEventName != codexPostToolUseEvent {
			// Fail before side effects: an unknown Codex-shaped event
			// must not enter trusted provenance or roll up into
			// compressed evidence. The error names the unsupported
			// value, states the sole accepted value, and shows the two
			// working corrections (exact Codex event, or omit for
			// flat/OpenCode input).
			return nil, &Error{
				Code: CodeUsage,
				Message: fmt.Sprintf(
					`ktx hook: unsupported hook_event_name %q; only %q is accepted. Set "hook_event_name":"%s" for a Codex PostToolUse payload, or omit hook_event_name (or set it to "") for flat/OpenCode input.`,
					*peek.HookEventName, codexPostToolUseEvent, codexPostToolUseEvent,
				),
				Extra: map[string]any{
					"valid_values": []string{codexPostToolUseEvent},
					"valid_examples": []string{
						`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"aggregated_output":"..."}}`,
						`{"tool":"bash","input":"echo hi","output":"hello"}`,
					},
				},
			}
		}
		ingress = ingressCodex
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, usageError("ktx hook: parse JSON: %v", err)
		}
		var toolName string
		if v := raw["tool_name"]; len(v) > 0 {
			_ = json.Unmarshal(v, &toolName)
		}
		toolInputRaw := raw["tool_input"]
		toolResponseRaw := raw["tool_response"]
		var toolUseID string
		if v := raw["tool_use_id"]; len(v) > 0 {
			_ = json.Unmarshal(v, &toolUseID)
		}
		var runtimeSessionID string
		if v := raw["session_id"]; len(v) > 0 {
			_ = json.Unmarshal(v, &runtimeSessionID)
		}
		tool, input, output := extractCodexFields(toolName, toolInputRaw, toolResponseRaw)
		tc.Tool = tool
		tc.Input = input
		tc.Output = output
		provenance := correlation.Untrusted()
		if validCodexPostToolUse(raw) {
			provenance = correlation.Trusted(correlation.ProvenanceCodexPostToolUse)
			succeeded := false
			tc.IsError = &succeeded
		}
		resolved, err := correlation.Resolve(
			correlation.NativeTuple{Runtime: "codex", SessionID: runtimeSessionID, CallID: toolUseID},
			provenance)
		if err != nil {
			return nil, internalError("ktx hook: resolve identity: %v", err)
		}
		src = resolved
		tc.ID = src.ID
		rt := "codex"
		tc.Runtime = &rt
	} else if peek.Runtime != nil && *peek.Runtime == "opencode" {
		ingress = ingressOpenCode
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, usageError("ktx hook: parse JSON: %v", err)
		}
		inputRaw := raw["input"]
		outputRaw := raw["output"]
		// Remove the object-shaped fields so the flat ToolCall
		// unmarshal does not choke on them; the extracted values
		// override below.
		delete(raw, "input")
		delete(raw, "output")
		flatBody, err := json.Marshal(raw)
		if err != nil {
			return nil, usageError("ktx hook: parse JSON: %v", err)
		}
		if err := json.Unmarshal(flatBody, &tc); err != nil {
			return nil, usageError("ktx hook: parse JSON: %v", err)
		}
		tool, input, output, sessionID, callID := extractOpenCodeFields(inputRaw, outputRaw)
		runtimeSessionID, runtimeCallID = sessionID, callID
		tc.Tool = tool
		tc.Input = input
		tc.Output = output
		tc.ExitCode, tc.IsError = extractOpenCodeOutcome(tool, outputRaw)
		// Resolve deterministic identity from the native correlation
		// tuple. The hook is a trusted adapter that KTX authors, so
		// provenance is trusted. When the input lacks the recognized
		// sessionID/callID fields (a flat payload claiming opencode),
		// correlation.Resolve falls back to a fresh NanoID — eligibility
		// is provenance-driven, not text-driven.
		resolved, err := correlation.Resolve(
			correlation.NativeTuple{Runtime: "opencode", SessionID: sessionID, CallID: callID},
			correlation.Trusted(correlation.ProvenanceOpenCodeHook))
		if err != nil {
			return nil, internalError("ktx hook: resolve identity: %v", err)
		}
		src = resolved
		tc.ID = src.ID
		rt := "opencode"
		tc.Runtime = &rt
	} else {
		if err := json.Unmarshal(body, &tc); err != nil {
			return nil, usageError("ktx hook: parse JSON: %v", err)
		}
	}

	if tc.Tool == "" {
		return nil, usageError("ktx hook: missing required field: tool")
	}
	if tc.ID == "" {
		id, err := dbpkg.NewID()
		if err != nil {
			return nil, internalError("ktx hook: generate id: %v", err)
		}
		tc.ID = id
	}
	if tc.Timestamp == "" {
		tc.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	producer := dbpkg.ProducerHook
	tc.Producer = &producer

	sessionHint := tc.SessionID
	if ownerPresent {
		sessionHint = ownerSessionID
	}
	if benchmarkPresent {
		if sessionHint != "" && sessionHint != benchmarkEnv.SessionID {
			return nil, usageError("ktx hook: payload session conflicts with benchmark attempt owner")
		}
		sessionHint = benchmarkEnv.SessionID
	}
	if tc.ContentHash == "" && tc.Output != "" {
		tc.ContentHash = dbpkg.ContentHash(tc.Output)
	}
	db.PrepareToolCall(&tc)

	sourceID := tc.ID
	trusted := ingress.adapter()
	var preflight hookRetryPreflight
	preflightStarted := time.Now()
	err = dbpkg.AttributeDatabaseUnavailable(dbpkg.OperationSource, preflightStarted, db.WithHookPreflight(func() error {
		var err error
		preflight, err = preflightHookRetry(db, trusted, &tc)
		return err
	}))
	if err != nil {
		return nil, hookSourceError(err, ingress, sourceID)
	}
	if preflight.sourceExists {
		tc.SessionID = preflight.sessionID
	}

	cfg, configErr := config.Load()
	var execution pipelineExecution
	var pipelineErr error
	if configErr == nil {
		if preflight.result.Transformed {
			// Retained artifacts need only the current recovery-cue policy,
			// never construction or execution of a replacement pipeline.
			selection := router.Resolve(&tc, cfg.Routes, "default")
			execution.recoverFooter = config.ResolveRecoverFooter(cfg, selection.Profile)
		} else if ingress == ingressClaudeCode && !claudeResponse.replaceable {
			// Claude Code would keep the original result, so the call is
			// recorded as passthrough without running the pipeline and no
			// savings are counted that the model never received.
			execution = pipelineExecution{output: tc.Output}
		} else {
			// Route selection and transformation stay outside exclusive database
			// coordination. Source-only retries can still produce their first artifact.
			execution, pipelineErr = compressPipelineWithConfig(&tc, cfg, nil, stderr, true)
		}
	}
	execution = applyRecoveryCueGate(execution, sourceID, tc.Output)
	compressedOutput := execution.output
	pipeJSON := execution.artifactPipelineRecord()
	transformed := execution.transformed
	if preflight.complete && (preflight.result.Transformed || !transformed) {
		if configErr != nil && !preflight.result.Transformed {
			return nil, usageError("%v", configErr)
		}
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		if err := retainRecoveryAttempt(db, collectionID, ingress == ingressOpenCode && src.Eligible, &tc); err != nil {
			return nil, conflictError("ktx hook: retain recovery attempt: %v", err)
		}
		return nil, writeHookResult(stdout, ingress, preflight.result, tc.Output, execution, claudeResponse, runtimeSessionID, runtimeCallID)
	}

	var candidate *dbpkg.Compressed
	if transformed {
		candidate = &dbpkg.Compressed{Output: compressedOutput, ContentHash: dbpkg.ContentHash(compressedOutput), Pipeline: pipeJSON, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Producer: &producer}
	}
	var result evidence.Result
	coordinationStarted := time.Now()
	err = dbpkg.AttributeDatabaseUnavailable(dbpkg.OperationSource, coordinationStarted, db.WithHookWriter(func(writer *dbpkg.HookWriter) error {
		observeHookAcquisition(observe)
		if !preflight.sourceExists {
			retainedSessionID, exists, err := db.ValidateToolCallRetry(&tc)
			if err != nil {
				return err
			}
			if exists {
				tc.SessionID = retainedSessionID
			} else {
				sessionID, err := writer.ResolveSessionID(sessionHint, tc.Timestamp)
				if err != nil {
					return dbpkg.AttributeDatabaseUnavailable(dbpkg.OperationSource, time.Now(), err)
				}
				tc.SessionID = sessionID
			}
		}
		if src.Eligible {
			if err := writer.IdentifySession(tc.SessionID, ingress.agent()); err != nil {
				return err
			}
		}
		inserted, err := evidence.PersistSource(writer, src, &tc)
		if err != nil {
			return err
		}
		observeHookCommit(observe, dbpkg.OperationSource)
		if configErr != nil {
			result, err = evidence.PersistArtifact(writer, src, &tc, inserted, nil)
			if err != nil {
				return err
			}
			return recordHookCompressionDispositionObserved(writer, sourceID, result.CompressedID, pipelineExecution{}, observe)
		}
		if pipelineErr != nil {
			return pipelineErr
		}
		result, err = evidence.PersistArtifact(writer, src, &tc, inserted, candidate)
		if err != nil {
			return err
		}
		if candidate != nil {
			observeHookCommit(observe, dbpkg.OperationArtifact)
		}
		return recordHookCompressionDispositionObserved(writer, result.SourceID, result.CompressedID, execution, observe)
	}))
	if err != nil {
		if errors.Is(err, dbpkg.ErrToolCallConflict) {
			return nil, hookSourceError(err, ingress, sourceID)
		}
		if pipelineErr != nil && errors.Is(err, pipelineErr) {
			return nil, pipelineErr
		}
		return nil, internalError("ktx hook: persist evidence: %v", err)
	}
	if configErr != nil {
		if result.Transformed {
			if err := retainRecoveryAttempt(db, collectionID, ingress == ingressOpenCode && src.Eligible, &tc); err != nil {
				return nil, conflictError("ktx hook: retain recovery attempt: %v", err)
			}
			return nil, writeHookResult(stdout, ingress, result, tc.Output, pipelineExecution{}, claudeResponse, runtimeSessionID, runtimeCallID)
		}
		return nil, usageError("%v", configErr)
	}
	if err := retainRecoveryAttempt(db, collectionID, ingress == ingressOpenCode && src.Eligible, &tc); err != nil {
		return nil, conflictError("ktx hook: retain recovery attempt: %v", err)
	}
	return nil, writeHookResult(stdout, ingress, result, tc.Output, execution, claudeResponse, runtimeSessionID, runtimeCallID)
}

func retainRecoveryAttempt(db *dbpkg.DB, collectionID string, trustedOpenCode bool, tc *dbpkg.ToolCall) error {
	if collectionID == "" || !trustedOpenCode || tc.Tool != "bash" || tc.IsError == nil {
		return nil
	}
	var input map[string]json.RawMessage
	if json.Unmarshal([]byte(tc.Input), &input) != nil || len(input) != 1 {
		return nil
	}
	var command string
	if json.Unmarshal(input["command"], &command) != nil {
		return nil
	}
	parts := strings.Split(command, " ")
	if len(parts) != 3 || parts[0] != "ktx" || parts[1] != "read" || !dbpkg.IDPattern.MatchString(parts[2]) {
		return nil
	}
	cue, err := db.RecoveryCue(collectionID, parts[2])
	if errors.Is(err, dbpkg.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	attempt := dbpkg.RecoveryAttempt{AttemptSourceID: tc.ID, CollectionID: collectionID, CueSourceID: cue.SourceID, AttemptedAt: tc.Timestamp, Authority: dbpkg.RecoveryAttemptAuthority}
	if *tc.IsError {
		attempt.ResultState = "failed"
		return db.RetainRecoveryAttempt(attempt)
	}
	attempt.ResultState = "succeeded"
	var recovered dbpkg.ToolCall
	exact := false
	if json.Unmarshal([]byte(tc.Output), &recovered) == nil && recovered.ID == cue.SourceID {
		source, getErr := db.GetToolCall(cue.SourceID)
		exact = getErr == nil && recovered.Output == source.Output && recovered.ContentHash == source.ContentHash && dbpkg.ContentHash(recovered.Output) == source.ContentHash
	}
	attempt.ByteExact = &exact
	return db.RetainRecoveryAttempt(attempt)
}

type hookDispositionStore interface {
	GetCompressed(string) (*dbpkg.Compressed, error)
	RecordCompressionDisposition(*dbpkg.CompressionDisposition) error
}

func recordHookCompressionDispositionObserved(database hookDispositionStore, sourceID, artifactID string, execution pipelineExecution, observe hookPhaseObserver) (err error) {
	started := time.Now()
	defer func() { err = dbpkg.AttributeDatabaseUnavailable(dbpkg.OperationDisposition, started, err) }()
	disposition := dbpkg.CompressionDispositionIntentionalPassthrough
	if execution.antiWorseningRejected {
		disposition = dbpkg.CompressionDispositionAntiWorseningRejected
	}
	if artifactID != "" {
		disposition = dbpkg.CompressionDispositionArtifactPersisted
	}
	profile, route := execution.profile, execution.route
	if artifactID != "" {
		artifact, err := database.GetCompressed(artifactID)
		if err != nil {
			return err
		}
		profile, route = "", ""
		var selection struct {
			Profile string `json:"profile"`
			Route   string `json:"route"`
		}
		if json.Unmarshal([]byte(artifact.Pipeline), &selection) == nil {
			profile = selection.Profile
			if selection.Route == "matched" || selection.Route == "default_fallback" {
				route = selection.Route
			}
		}
	}
	err = database.RecordCompressionDisposition(&dbpkg.CompressionDisposition{
		SourceID: sourceID, Profile: profile, Route: route,
		Disposition: disposition, ArtifactID: artifactID,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err == nil {
		observeHookCommit(observe, dbpkg.OperationDisposition)
	}
	return err
}

func observeHookCommit(observe hookPhaseObserver, phase string) {
	if observe == nil {
		return
	}
	observe(hookAcceptanceCommit, phase)
}

func observeHookAcquisition(observe hookPhaseObserver) {
	if observe != nil {
		observe(hookExclusiveAcquisition, "coordination")
	}
}

type hookRetryPreflight struct {
	sourceExists bool
	sessionID    string
	complete     bool
	result       evidence.Result
}

func preflightHookRetry(database *dbpkg.DB, trusted bool, tc *dbpkg.ToolCall) (hookRetryPreflight, error) {
	if !trusted {
		return hookRetryPreflight{}, nil
	}
	sessionID, exists, err := database.ValidateToolCallRetry(tc)
	if err != nil || !exists {
		return hookRetryPreflight{sourceExists: exists, sessionID: sessionID}, err
	}
	state := hookRetryPreflight{sourceExists: true, sessionID: sessionID}
	artifact, artifactErr := database.GetCompressedBySource(tc.ID)
	if artifactErr == nil {
		state.result = evidence.Result{SourceID: tc.ID, Output: artifact.Output, CompressedID: artifact.ID, ArtifactPipeline: artifact.Pipeline, Transformed: true}
	}
	disposition, dispositionErr := database.GetCompressionDisposition(tc.ID)
	if dispositionErr != nil {
		if errors.Is(dispositionErr, dbpkg.ErrNotFound) {
			return state, nil
		}
		return state, dispositionErr
	}
	if artifactErr == nil && disposition.Disposition == dbpkg.CompressionDispositionArtifactPersisted && disposition.ArtifactID == artifact.ID {
		state.complete = true
		return state, nil
	}
	if errors.Is(artifactErr, dbpkg.ErrNotFound) && disposition.Disposition != dbpkg.CompressionDispositionArtifactPersisted {
		state.complete = true
		state.result = evidence.Result{SourceID: tc.ID, Output: tc.Output}
		return state, nil
	}
	if artifactErr != nil && !errors.Is(artifactErr, dbpkg.ErrNotFound) {
		return state, artifactErr
	}
	return state, nil
}

func hookSourceError(err error, ingress hookIngress, sourceID string) error {
	if !errors.Is(err, dbpkg.ErrToolCallConflict) {
		return internalError("ktx hook: inspect tool_call: %v", err)
	}
	// Flat input shares the Codex label for conflicts it reaches while
	// persisting a caller-supplied id.
	label := "Codex"
	switch ingress {
	case ingressOpenCode:
		label = "OpenCode"
	case ingressClaudeCode:
		label = "Claude Code"
	}
	return conflictError("ktx hook: %s retry conflicts with the evidence already stored for source %q", label, sourceID)
}

func writeHookResult(stdout io.Writer, ingress hookIngress, result evidence.Result, original string, execution pipelineExecution, response claudeCodeToolResponse, runtimeSessionID, runtimeCallID string) error {
	structuredFallback, _ := pipeline.StructuredFallbackFromRecord(result.ArtifactPipeline)
	output := result.Output
	if result.Transformed && execution.recoverFooter && !structuredFallback {
		output, _ = recoveryCueOutput(result.SourceID, original, result.Output)
	}
	if ingress.adapter() {
		return writeTrustedHookOutput(stdout, ingress, result, output, response, runtimeSessionID, runtimeCallID, result.Transformed && execution.recoverFooter && strings.Contains(output, "`ktx read "+result.SourceID+"`"))
	}
	var compressedID *string
	if result.CompressedID != "" {
		compressedID = &result.CompressedID
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(hookEnvelope{Output: output, CompressedID: compressedID, SourceID: result.SourceID}); err != nil {
		return internalError("ktx hook: write envelope: %v", err)
	}
	return nil
}

func applyRecoveryCueGate(execution pipelineExecution, sourceID, original string) pipelineExecution {
	if !execution.transformed || !execution.recoverFooter || execution.structuredFallback {
		return execution
	}
	if _, ok := recoveryCueOutput(sourceID, original, execution.output); ok {
		return execution
	}
	execution.output = original
	execution.stages = nil
	execution.transformed = false
	execution.structuredFallback = false
	execution.antiWorseningRejected = true
	return execution
}

func recoveryCueOutput(sourceID, original, compressed string) (string, bool) {
	footer := buildRecoveryFooter(sourceID, len(original), len(compressed))
	if len(compressed)+len(footer) >= len(original) {
		return original, false
	}
	return compressed + footer, true
}

// buildRecoveryFooter produces a single-line recovery footer appended to
// the compressed output in the hook envelope. The footer is NOT persisted
// in the compressed table row — it lives only in the agent-visible
// envelope.Output so the agent can recover the original via
// `ktx read <source_id>`.
//
// The footer carries the source_id verbatim (as the argument to
// `ktx read`), the compressed size in bytes, the reduction ratio
// (percent), and the recovery command prefixed by an instructional cue
// that names the action. The original byte count is omitted; the
// reduction percent alone conveys the loss magnitude at lower token
// cost than carrying both sizes.
func buildRecoveryFooter(sourceID string, originalBytes, compressedBytes int) string {
	return dbpkg.RecoveryFooter(sourceID, originalBytes, compressedBytes)
}

// RecoveryFooter returns the footer used for agent-visible compressed output.
func RecoveryFooter(sourceID string, originalBytes, compressedBytes int) string {
	return buildRecoveryFooter(sourceID, originalBytes, compressedBytes)
}

func init() {
	Register("hook", RegisterOptions{
		Description:       "in-flight tool_call compression (persist original, return compressed)",
		DatabaseAccess:    DatabaseReadWrite,
		NeedsStdin:        true,
		PassthroughStdout: true,
		ParseArgs:         parseHookDispatchArgs,
		Help:              printHookHelp,
	}, hookCommandFunc)
}

func printHookHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx hook — in-flight tool_call compression")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx hook [--runtime claude-code] < tool_call.json")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Reads a tool-call event from stdin, persists the original, runs the")
	fmt.Fprintln(w, "configured compression pipeline, and writes a JSON envelope to stdout")
	fmt.Fprintln(w, "for flat and OpenCode input:")
	fmt.Fprintln(w, `  {"output": "<compressed>", "compressed_id": "<id>|null", "source_id": "<id>"}`)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flat input fields:")
	fmt.Fprintln(w, "  tool       required (the binary cannot infer the tool name)")
	fmt.Fprintln(w, "  id         optional caller ID; a fresh NanoID is generated when empty")
	fmt.Fprintln(w, "  timestamp  optional; generated as an RFC3339Nano timestamp when empty")
	fmt.Fprintln(w, "  output     tool output to persist (and compress when a pipeline is configured)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The agent sees the compressed output.output; the original is retrievable via")
	fmt.Fprintln(w, "`ktx decompress <compressed_id>`. When no ktx.toml exists (passthrough default),")
	fmt.Fprintln(w, "the original is returned unchanged with compressed_id=null.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A pipeline timeout is enforced (critical-path contract); the default")
	fmt.Fprintln(w, "is 2s, configurable per profile via `[pipeline.<name>] timeout` in")
	fmt.Fprintln(w, "ktx.toml (e.g. `timeout = \"500ms\"` or `timeout = \"10s\"`). A profile")
	fmt.Fprintln(w, "without a timeout inherits the default profile's timeout. On timeout the")
	fmt.Fprintln(w, "original is returned unchanged with a stderr warning.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Recovery footers are off by default. Set recover_footer = true on a lossy")
	fmt.Fprintln(w, "profile to append a recovery cue to the compressed output the agent sees.")
	fmt.Fprintln(w, "The footer carries the source_id, the compressed size, the reduction")
	fmt.Fprintln(w, "ratio, and the recovery command `ktx read <source_id>` prefixed by an")
	fmt.Fprintln(w, "instructional cue. The setting is per profile, and a")
	fmt.Fprintln(w, "profile without one inherits the default profile's. A footer-aware")
	fmt.Fprintln(w, "anti-worsening gate suppresses the transform (and the footer) when")
	fmt.Fprintln(w, "compressed+footer would not be smaller than the original; the stored")
	fmt.Fprintln(w, "compressed row stays content-only.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Outcome evidence:")
	fmt.Fprintln(w, "  Persisted is_error=false is explicit success; is_error=true is explicit")
	fmt.Fprintln(w, "  failure; missing or invalid is_error is unknown. Persisted exit_code is")
	fmt.Fprintln(w, "  diagnostic only. Success routes require explicit success, failure routes")
	fmt.Fprintln(w, "  require explicit failure, and any/empty routes include all three states.")
	fmt.Fprintln(w, "  Explicit failure returns original output unchanged before the pipeline.")
	fmt.Fprintln(w, "  Unknown may use any/default behavior but is not success.")
	fmt.Fprintln(w, "  OpenCode 1.18.5 wrapper precedence is terminal error, supported shell")
	fmt.Fprintln(w, "  exit, completed result, then unknown; error text is retained. Its hook has")
	fmt.Fprintln(w, "  only supported shell exit evidence; absent, malformed, or unsupported")
	fmt.Fprintln(w, "  evidence is unknown.")
	fmt.Fprintln(w, "  A trusted schema-conforming Codex 0.144.4 PostToolUse occurrence is success.")
	fmt.Fprintln(w, "  Unconstrained nested tool_response fields cannot override it; nonconforming")
	fmt.Fprintln(w, "  or untrusted payloads are unknown.")
	fmt.Fprintln(w, "  A trusted Claude Code 2.1.304 PostToolUse occurrence is success, except an")
	fmt.Fprintln(w, "  interrupted tool_response, which is unknown. Untrusted payloads are unknown.")
	fmt.Fprintln(w, "  Flat sidecar is_error is a caller assertion whose authority the caller")
	fmt.Fprintln(w, "  establishes. Runtime, model, and provider labels do not strengthen it.")
	fmt.Fprintln(w, "  Historical unsupported evidence remains unknown.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Source identity:")
	fmt.Fprintln(w, "  Recognized OpenCode hook events, Codex PostToolUse events, and Claude Code")
	fmt.Fprintln(w, "  PostToolUse events are eligible for deterministic source IDs when their")
	fmt.Fprintln(w, "  verified native schema supplies a")
	fmt.Fprintln(w, "  complete runtime/session/call tuple. The recognized `ktx run opencode`")
	fmt.Fprintln(w, "  wrapper applies the same contract. Runtime IDs are correlation inputs, not")
	fmt.Fprintln(w, "  KTX record IDs or KTX session ownership.")
	fmt.Fprintln(w, "  Missing, malformed, or unverified native tuples receive a fresh NanoID with")
	fmt.Fprintln(w, "  no deduplication guarantee. A payload selecting the OpenCode adapter path but")
	fmt.Fprintln(w, "  lacking its complete tuple also receives a fresh NanoID; its caller id cannot")
	fmt.Fprintln(w, "  substitute for native correlation. Legacy flat input without a recognized")
	fmt.Fprintln(w, "  runtime-adapter discriminator preserves its caller id, or generates a fresh")
	fmt.Fprintln(w, "  one when absent.")
	fmt.Fprintln(w, "  Eligible exact retries reuse the first source evidence and KTX session roll-up.")
	fmt.Fprintln(w, "  The first compressed artifact is authoritative once persisted. A racing retry")
	fmt.Fprintln(w, "  may receive source output while no artifact exists; later retries return the")
	fmt.Fprintln(w, "  artifact and cannot replace it. Changed runtime, tool, canonical input, or")
	fmt.Fprintln(w, "  output conflicts before compression and leaves first evidence unchanged.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Codex mode: when the input payload carries exactly")
	fmt.Fprintln(w, "`hook_event_name` set to \"PostToolUse\", the hook extracts tool")
	fmt.Fprintln(w, "name, input, and output from `tool_name`, `tool_input`, and")
	fmt.Fprintln(w, "`tool_response` instead of the flat tool_call shape. Any other")
	fmt.Fprintln(w, "non-empty `hook_event_name` is rejected before persistence (no")
	fmt.Fprintln(w, "source, roll-up, or compressed evidence is written); an absent or")
	fmt.Fprintln(w, "empty `hook_event_name` falls back to flat/OpenCode input.")
	fmt.Fprintln(w, "The response uses the Codex PostToolUse output contract:")
	fmt.Fprintln(w, `  {"decision":"block","reason":"<compressed>"}`)
	fmt.Fprintln(w, "when the pipeline transforms the output (Codex blocks the original tool")
	fmt.Fprintln(w, "result and replaces it with the compressed reason), or `{}` when")
	fmt.Fprintln(w, "passthrough (Codex leaves the tool result unchanged). The envelope")
	fmt.Fprintln(w, "documented above does not apply to the Codex path.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Claude Code mode: `--runtime claude-code` (or `--runtime=claude-code`)")
	fmt.Fprintln(w, "selects the Claude Code 2.1.304 PostToolUse command-hook contract. It is")
	fmt.Fprintln(w, "the only accepted --runtime value; a missing, different, or repeated value")
	fmt.Fprintln(w, "is a usage error before the database opens. `hook_event_name` must be")
	fmt.Fprintln(w, "exactly \"PostToolUse\"; any other, empty, or absent value is rejected")
	fmt.Fprintln(w, "before persistence. A payload is trusted when `session_id` and")
	fmt.Fprintln(w, "`tool_use_id` are non-empty strings, `tool_name` is a string, and")
	fmt.Fprintln(w, "`tool_input` and `tool_response` are objects; other fields are allowed.")
	fmt.Fprintln(w, "Trusted payloads derive the source ID from the Claude Code session and")
	fmt.Fprintln(w, "tool-use IDs; untrusted payloads receive a fresh NanoID. The hook records")
	fmt.Fprintln(w, "`tool_response.stdout`, or the raw tool_response JSON when stdout is not")
	fmt.Fprintln(w, "a string. It compresses stdout only when stdout is a string, isImage and")
	fmt.Fprintln(w, "interrupted are not true, and backgroundTaskId is absent; other results")
	fmt.Fprintln(w, "are recorded as passthrough without running the pipeline. When the")
	fmt.Fprintln(w, "pipeline transforms the output, the response is")
	fmt.Fprintln(w, `  {"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":{...}}}`)
	fmt.Fprintln(w, "where updatedToolOutput is the original tool_response with only stdout")
	fmt.Fprintln(w, "replaced by the compressed text (and recovery footer, when enabled).")
	fmt.Fprintln(w, "Otherwise the response is `{}` and Claude Code keeps the original result.")
	fmt.Fprintln(w, "Claude Code names its shell tool \"Bash\" and route matching is")
	fmt.Fprintln(w, "case-sensitive, so routes for it use tool = \"Bash\". A not-found failure")
	fmt.Fprintln(w, "exits 1 instead of 2 in this mode, because Claude Code shows a")
	fmt.Fprintln(w, "PostToolUse hook's stderr to the model on exit 2. Without the flag, a")
	fmt.Fprintln(w, "Claude Code payload takes the Codex path above, fails its schema check,")
	fmt.Fprintln(w, "and is untrusted.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "When only KTX_SESSION_ID is set, the hook exits 0 with no output so")
	fmt.Fprintln(w, "ordinary `ktx run` remains the single evidence owner. Explicit collection")
	fmt.Fprintln(w, "also sets KTX_COLLECTION_ID and keeps the hook active with strict ownership.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "This verb is called by `ktx init` adapters: the OpenCode plugin's")
	fmt.Fprintln(w, "tool.execute.after hook, the Codex PostToolUse command hook, and the")
	fmt.Fprintln(w, "Claude Code PostToolUse command hook. It is not typically invoked")
	fmt.Fprintln(w, "directly by users.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx compress    explicit-verb compression (returns compressed id)")
	fmt.Fprintln(w, "  ktx decompress  reverse a compression (return source output verbatim)")
	fmt.Fprintln(w, "  ktx init        install an OpenCode, Codex, or Claude Code adapter")
}
