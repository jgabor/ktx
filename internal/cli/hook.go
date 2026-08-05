package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/correlation"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/evidence"
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

func writeTrustedHookOutput(stdout io.Writer, isCodex bool, result evidence.Result, output, runtimeSessionID, runtimeCallID string) error {
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
	if benchmarkEnv, present, _ := benchcontract.ReadEnvironment(); present {
		envelope.KTXSessionID = benchmarkEnv.SessionID
		envelope.RuntimeSessionID = runtimeSessionID
		envelope.CallID = runtimeCallID
	}
	if err := enc.Encode(envelope); err != nil {
		return internalError("ktx hook: write envelope: %v", err)
	}
	return nil
}

// hookCommandFunc is the dispatch entry point for `ktx hook`. The
// dispatcher opens the DB for writes and exposes stdin
// (NeedsStdin=true, PassthroughStdout=true).
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
func hookLogic(stdin io.Reader, db *dbpkg.DB, stdout, stderr io.Writer) (any, error) {
	if os.Getenv("KTX_SESSION_ID") != "" {
		return nil, nil
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

	sessionHint := tc.SessionID
	if benchmarkEnv, present, envErr := benchcontract.ReadEnvironment(); present {
		if envErr != nil {
			return nil, usageError("ktx hook: %v", envErr)
		}
		if sessionHint != "" && sessionHint != benchmarkEnv.SessionID {
			return nil, usageError("ktx hook: payload session conflicts with benchmark attempt owner")
		}
		sessionHint = benchmarkEnv.SessionID
	}
	sessionID, err := db.ResolveSessionID(sessionHint, tc.Timestamp)
	if err != nil {
		return nil, internalError("ktx hook: resolve session: %v", err)
	}
	tc.SessionID = sessionID

	if tc.ContentHash == "" && tc.Output != "" {
		tc.ContentHash = dbpkg.ContentHash(tc.Output)
	}

	sourceInserted := false
	if isCodex || isOpenCode {
		inserted, err := evidence.PersistSource(db, src, &tc)
		if err != nil {
			if errors.Is(err, dbpkg.ErrToolCallConflict) {
				label := "Codex"
				if isOpenCode {
					label = "OpenCode"
				}
				return nil, conflictError("ktx hook: %s retry conflicts with the evidence already stored for source %q", label, tc.ID)
			}
			return nil, internalError("ktx hook: persist tool_call: %v", err)
		}
		sourceInserted = inserted
	} else if err := db.PersistToolCall(&tc); err != nil {
		return nil, internalError("ktx hook: persist tool_call: %v", err)
	}

	sourceID := tc.ID

	cfg, err := config.Load()
	if err != nil {
		// A malformed current config cannot supersede a first artifact already
		// retained for an exact trusted retry. Return that artifact without a
		// footer because footer policy is unavailable from the broken snapshot.
		if (isCodex || isOpenCode) && !sourceInserted {
			result, artifactErr := evidence.PersistArtifact(db, src, &tc, false, nil)
			if artifactErr != nil {
				return nil, internalError("ktx hook: load existing compressed artifact: %v", artifactErr)
			}
			if result.Transformed {
				if dispositionErr := recordHookCompressionDisposition(db, result.SourceID, result.CompressedID, pipelineExecution{}); dispositionErr != nil {
					return nil, internalError("ktx hook: persist compression disposition: %v", dispositionErr)
				}
				return nil, writeTrustedHookOutput(stdout, isCodex, result, result.Output, runtimeSessionID, runtimeCallID)
			}
		}
		if dispositionErr := recordHookCompressionDisposition(db, sourceID, "", pipelineExecution{}); dispositionErr != nil {
			return nil, internalError("ktx hook: persist compression disposition: %v", dispositionErr)
		}
		return nil, usageError("%v", err)
	}

	// Resolve route, stages, timeout, and footer from the same loaded snapshot.
	// enforceTimeout=true puts the pipeline on the agent's critical path.
	execution, err := compressPipelineWithConfig(&tc, cfg, nil, stderr, true)
	if err != nil {
		return nil, err
	}
	compressedOutput := execution.output
	pipeJSON := execution.pipelineRecord()
	transformed := execution.transformed

	// Footer-aware second gate. Fires only when recover_footer is
	// enabled for the selected profile and the pipeline actually
	// transformed the output. The pipeline Runner's byte-gate already
	// fired inside compressPipeline (it discards results not smaller
	// than the input). This second gate accounts for the footer's own
	// byte overhead: when len(compressed)+len(footer) >= len(original),
	// the transform is suppressed — source returned unchanged, no
	// footer, no compressed row. The footer lives only in the
	// agent-visible envelope.Output; the persisted Compressed.Output
	// stays content-only.
	envelopeOutput := compressedOutput
	if execution.recoverFooter && transformed {
		footer := buildRecoveryFooter(sourceID, len(tc.Output), len(compressedOutput))
		if len(compressedOutput)+len(footer) >= len(tc.Output) {
			transformed = false
			execution.antiWorseningRejected = true
			envelopeOutput = tc.Output
			compressedOutput = tc.Output
		} else {
			envelopeOutput = compressedOutput + footer
		}
	}

	// Evidence-safe artifact persistence for Codex and OpenCode trusted
	// paths. Once an artifact exists, exact retries return the first persisted
	// artifact even when the current pipeline configuration differs. A
	// concurrent no-transform retry may return source output if it checks before
	// the first artifact is persisted; later retries return that artifact.
	if isCodex || isOpenCode {
		var candidate *dbpkg.Compressed
		if transformed {
			candidate = &dbpkg.Compressed{
				Output:      compressedOutput,
				ContentHash: dbpkg.ContentHash(compressedOutput),
				Pipeline:    pipeJSON,
				Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
			}
		}
		result, err := evidence.PersistArtifact(db, src, &tc, sourceInserted, candidate)
		if err != nil {
			return nil, internalError("ktx hook: persist compressed: %v", err)
		}
		if err := recordHookCompressionDisposition(db, result.SourceID, result.CompressedID, execution); err != nil {
			return nil, internalError("ktx hook: persist compression disposition: %v", err)
		}

		// The footer is agent-visible decoration on the authoritative
		// (possibly stored) artifact; it is never persisted.
		finalOutput := result.Output
		if result.Transformed && execution.recoverFooter {
			footer := buildRecoveryFooter(result.SourceID, len(tc.Output), len(result.Output))
			if len(result.Output)+len(footer) < len(tc.Output) {
				finalOutput = result.Output + footer
			}
		}

		return nil, writeTrustedHookOutput(stdout, isCodex, result, finalOutput, runtimeSessionID, runtimeCallID)
	}

	// Flat/non-runtime path: ordinary compressed persistence + envelope
	// encoding.
	var compressedID *string
	if transformed {
		hash := dbpkg.ContentHash(compressedOutput)
		cid, err := dbpkg.NewID()
		if err != nil {
			return nil, internalError("ktx hook: generate compressed id: %v", err)
		}
		c := &dbpkg.Compressed{
			ID:          cid,
			SourceID:    sourceID,
			Output:      compressedOutput,
			ContentHash: hash,
			Pipeline:    pipeJSON,
			Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := db.InsertCompressed(c); err != nil {
			return nil, internalError("ktx hook: persist compressed: %v", err)
		}
		compressedID = &cid
	}
	artifactID := ""
	if compressedID != nil {
		artifactID = *compressedID
	}
	if err := recordHookCompressionDisposition(db, sourceID, artifactID, execution); err != nil {
		return nil, internalError("ktx hook: persist compression disposition: %v", err)
	}

	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	envelope := hookEnvelope{
		Output:       envelopeOutput,
		CompressedID: compressedID,
		SourceID:     sourceID,
	}
	if err := enc.Encode(envelope); err != nil {
		return nil, internalError("ktx hook: write envelope: %v", err)
	}
	return nil, nil
}

func recordHookCompressionDisposition(database *dbpkg.DB, sourceID, artifactID string, execution pipelineExecution) error {
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
	return database.RecordCompressionDisposition(&dbpkg.CompressionDisposition{
		SourceID: sourceID, Profile: profile, Route: route,
		Disposition: disposition, ArtifactID: artifactID,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
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
	reduction := 0
	if originalBytes > 0 && compressedBytes < originalBytes {
		reduction = (originalBytes - compressedBytes) * 100 / originalBytes
	}
	return fmt.Sprintf(
		"\n[ktx] output compressed: %d bytes (%d%% reduction) • get full output with `ktx read %s`\n",
		compressedBytes, reduction, sourceID,
	)
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
	fmt.Fprintln(w, "When KTX_SESSION_ID is set in the environment, the hook exits 0 with no")
	fmt.Fprintln(w, "output. This noop guard keeps the `ktx run` wrapper the single source")
	fmt.Fprintln(w, "of truth when both the wrapper and an adapter are active in one session.")
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
