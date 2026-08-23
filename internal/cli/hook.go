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

func writeTrustedHookOutput(stdout io.Writer, isCodex bool, result evidence.Result, output, runtimeSessionID, runtimeCallID string, recoveryCue bool) error {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if isCodex {
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

// hookCommandFunc is the dispatch entry point for `ktx hook`. The
// dispatcher opens a current schema without exclusive coordination and exposes
// stdin (NeedsStdin=true, PassthroughStdout=true). Creation and migration keep
// the normal coordinated fallback.
func hookCommandFunc(ctx *DispatchContext) (any, error) {
	return hookLogic(ctx.Stdin, ctx.DB, ctx.Stdout, ctx.Stderr)
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
	Output   string                     `json:"output"`
	Metadata map[string]json.RawMessage `json:"metadata"`
}

// extractOpenCodeFields extracts the flat tool, input, output strings, and
// the native correlation identifiers (sessionID, callID) from raw OpenCode
// callback input and output JSON values. The input is an object with a tool
// (string), sessionID, callID, and args (object); the output is either a bare
// string or an object with an output field. An empty tool name falls back to
// "unknown". An output object without an output field falls back to the
// stringified object. The native sessionID and callID are empty when absent
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
			if err := json.Unmarshal(outputRaw, &out); err == nil && out.Output != "" {
				output = out.Output
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
	AggregatedOutput string `json:"aggregated_output"`
	Output           string `json:"output"`
	Text             string `json:"text"`
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
				case resp.AggregatedOutput != "":
					output = resp.AggregatedOutput
				case resp.Output != "":
					output = resp.Output
				case resp.Text != "":
					output = resp.Text
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
	return hookLogicObserved(stdin, db, stdout, stderr, nil)
}

// hookLogicObserved exposes successful hook write phases to package tests.
// It does not emit production output or change phase ownership.
func hookLogicObserved(stdin io.Reader, db *dbpkg.DB, stdout, stderr io.Writer, observe hookPhaseObserver) (any, error) {
	collectionID := os.Getenv("KTX_COLLECTION_ID")
	if os.Getenv("KTX_SESSION_ID") != "" && collectionID == "" {
		return nil, nil
	}
	if collectionID != "" {
		if !dbpkg.IDPattern.MatchString(collectionID) || os.Getenv("KTX_SESSION_ID") == "" {
			return nil, usageError("ktx hook: invalid explicit collection ownership")
		}
		if err := db.ValidateCollectionInterval(collectionID, []string{"run"}); err != nil {
			return nil, usageError("ktx hook: collection ownership mismatch: %v", err)
		}
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
	var peek struct {
		Runtime       *string `json:"runtime"`
		HookEventName *string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		return nil, usageError("ktx hook: parse JSON: %v", err)
	}

	var isCodex, isOpenCode bool
	var runtimeSessionID, runtimeCallID string
	var src correlation.Source
	if peek.HookEventName != nil && *peek.HookEventName != "" {
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
		isCodex = true
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
		isOpenCode = true
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
	if collectionID != "" {
		sessionHint = os.Getenv("KTX_SESSION_ID")
	}
	if benchmarkEnv, present, envErr := benchcontract.ReadEnvironment(); present {
		if envErr != nil {
			return nil, usageError("ktx hook: %v", envErr)
		}
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
	trusted := isCodex || isOpenCode
	preflight, err := preflightHookRetry(db, trusted, &tc)
	if err != nil {
		return nil, hookSourceError(err, isOpenCode, sourceID)
	}
	if preflight.sourceExists {
		tc.SessionID = preflight.sessionID
	}

	cfg, configErr := config.Load()
	var execution pipelineExecution
	var pipelineErr error
	if configErr == nil {
		// Route selection and transformation stay outside exclusive database
		// coordination.
		execution, pipelineErr = compressPipelineWithConfig(&tc, cfg, nil, stderr, true)
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
		if err := retainRecoveryAttempt(db, collectionID, isOpenCode && src.Eligible, &tc); err != nil {
			return nil, conflictError("ktx hook: retain recovery attempt: %v", err)
		}
		return nil, writeHookResult(stdout, isCodex, trusted, preflight.result, tc.Output, execution, runtimeSessionID, runtimeCallID)
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
			runtime := "opencode"
			if isCodex {
				runtime = "codex"
			}
			if err := writer.IdentifySession(tc.SessionID, runtime); err != nil {
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
			return nil, hookSourceError(err, isOpenCode, sourceID)
		}
		if pipelineErr != nil && errors.Is(err, pipelineErr) {
			return nil, pipelineErr
		}
		return nil, internalError("ktx hook: persist evidence: %v", err)
	}
	if configErr != nil {
		if result.Transformed {
			if err := retainRecoveryAttempt(db, collectionID, isOpenCode && src.Eligible, &tc); err != nil {
				return nil, conflictError("ktx hook: retain recovery attempt: %v", err)
			}
			return nil, writeHookResult(stdout, isCodex, trusted, result, tc.Output, pipelineExecution{}, runtimeSessionID, runtimeCallID)
		}
		return nil, usageError("%v", configErr)
	}
	if err := retainRecoveryAttempt(db, collectionID, isOpenCode && src.Eligible, &tc); err != nil {
		return nil, conflictError("ktx hook: retain recovery attempt: %v", err)
	}
	return nil, writeHookResult(stdout, isCodex, trusted, result, tc.Output, execution, runtimeSessionID, runtimeCallID)
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
	disposition, dispositionErr := database.GetCompressionDisposition(tc.ID)
	if dispositionErr != nil {
		if errors.Is(dispositionErr, dbpkg.ErrNotFound) {
			return state, nil
		}
		return state, dispositionErr
	}
	if artifactErr == nil && disposition.Disposition == dbpkg.CompressionDispositionArtifactPersisted && disposition.ArtifactID == artifact.ID {
		state.complete = true
		state.result = evidence.Result{SourceID: tc.ID, Output: artifact.Output, CompressedID: artifact.ID, ArtifactPipeline: artifact.Pipeline, Transformed: true}
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

func hookSourceError(err error, isOpenCode bool, sourceID string) error {
	if !errors.Is(err, dbpkg.ErrToolCallConflict) {
		return internalError("ktx hook: inspect tool_call: %v", err)
	}
	label := "Codex"
	if isOpenCode {
		label = "OpenCode"
	}
	return conflictError("ktx hook: %s retry conflicts with the evidence already stored for source %q", label, sourceID)
}

func writeHookResult(stdout io.Writer, isCodex, trusted bool, result evidence.Result, original string, execution pipelineExecution, runtimeSessionID, runtimeCallID string) error {
	structuredFallback, _ := pipeline.StructuredFallbackFromRecord(result.ArtifactPipeline)
	output := result.Output
	if result.Transformed && execution.recoverFooter && !structuredFallback {
		output, _ = recoveryCueOutput(result.SourceID, original, result.Output)
	}
	if trusted {
		return writeTrustedHookOutput(stdout, isCodex, result, output, runtimeSessionID, runtimeCallID, result.Transformed && execution.recoverFooter && strings.Contains(output, "`ktx read "+result.SourceID+"`"))
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
		Help:              printHookHelp,
	}, hookCommandFunc)
}

func printHookHelp(w io.Writer) {
	fmt.Fprintln(w, "ktx hook — in-flight tool_call compression")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx hook < tool_call.json")
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
	fmt.Fprintln(w, "  Flat sidecar is_error is a caller assertion whose authority the caller")
	fmt.Fprintln(w, "  establishes. Runtime, model, and provider labels do not strengthen it.")
	fmt.Fprintln(w, "  Historical unsupported evidence remains unknown.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Source identity:")
	fmt.Fprintln(w, "  Recognized OpenCode hook events and Codex PostToolUse events are eligible")
	fmt.Fprintln(w, "  for deterministic source IDs when their verified native schema supplies a")
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
	fmt.Fprintln(w, "When only KTX_SESSION_ID is set, the hook exits 0 with no output so")
	fmt.Fprintln(w, "ordinary `ktx run` remains the single evidence owner. Explicit collection")
	fmt.Fprintln(w, "also sets KTX_COLLECTION_ID and keeps the hook active with strict ownership.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "This verb is called by `ktx init` adapters: the OpenCode plugin's")
	fmt.Fprintln(w, "tool.execute.after hook and the Codex PostToolUse command hook. It is not")
	fmt.Fprintln(w, "typically invoked directly by users.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx compress    explicit-verb compression (returns compressed id)")
	fmt.Fprintln(w, "  ktx decompress  reverse a compression (return source output verbatim)")
	fmt.Fprintln(w, "  ktx init        install an OpenCode or Codex adapter")
}
