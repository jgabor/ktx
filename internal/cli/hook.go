package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// hookEnvelope is the JSON response written by `ktx hook` to stdout.
// The plugin parses this and sets output.output = Output (the
// compressed bytes the agent sees). CompressedID and SourceID are
// developer-facing (for reversibility via `ktx decompress`) and are
// never injected into the agent's context (invisibility).
type hookEnvelope struct {
	Output       string  `json:"output"`
	CompressedID *string `json:"compressed_id"`
	SourceID     string  `json:"source_id"`
}

// codexHookOutput is the Codex PostToolUse hook response written by
// `ktx hook` to stdout when the input payload carries a hook_event_name.
// When the pipeline transforms the output, the hook blocks the original
// tool result and replaces it with the compressed text in reason. When
// passthrough (no transform), an empty JSON object is emitted and Codex
// leaves the tool result unchanged.
type codexHookOutput struct {
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// hookCommandFunc is the dispatch entry point for `ktx hook`. The
// dispatcher opens the DB and exposes stdin (NeedsDB=true,
// NeedsStdin=true, PassthroughStdout=true).
func hookCommandFunc(ctx *DispatchContext) (any, error) {
	return hookLogic(ctx.Stdin, ctx.DB, ctx.Stdout, ctx.Stderr)
}

// openCodeInput is the OpenCode callback input shape: an object with a
// tool name (string) and an args object. The hook extracts these into
// the flat ToolCall fields when runtime="opencode".
type openCodeInput struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// openCodeOutputObject is the OpenCode callback output shape when the
// output is an object carrying an output field (string).
type openCodeOutputObject struct {
	Output string `json:"output"`
}

// extractOpenCodeFields extracts the flat tool, input, and output
// strings from raw OpenCode callback input and output JSON values. The
// input is an object with a tool (string) and args (object); the output
// is either a bare string or an object with an output field. An empty
// tool name falls back to "unknown". An output object without an
// output field falls back to the stringified object.
func extractOpenCodeFields(inputRaw, outputRaw json.RawMessage) (tool, input, output string) {
	tool = "unknown"

	if len(inputRaw) > 0 {
		var in openCodeInput
		if err := json.Unmarshal(inputRaw, &in); err == nil && in.Tool != "" {
			tool = in.Tool
		}
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

	return tool, input, output
}

// codexToolResponseObject covers the known Codex tool_response shapes:
// an object carrying aggregated_output, output, or text (checked in
// that order before falling back to the stringified object).
type codexToolResponseObject struct {
	AggregatedOutput string `json:"aggregated_output"`
	Output           string `json:"output"`
	Text             string `json:"text"`
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
// When hook_event_name is present (Codex PostToolUse payload), the
// hook extracts tool name, input, and output from the top-level
// tool_name, tool_input, and tool_response fields. When
// runtime="opencode" is present, the hook extracts tool name, input
// string, and output string from raw OpenCode callback objects
// (input.tool + input.args, output as string or {output: string})
// instead of requiring a flat tool_call shape. Any other payload
// (absent hook_event_name and absent/unrecognised runtime) falls back
// to flat tool_call deserialisation (backward compatible).
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
	var peek struct {
		Runtime       *string `json:"runtime"`
		HookEventName *string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		return nil, usageError("ktx hook: parse JSON: %v", err)
	}

	var isCodex bool
	if peek.HookEventName != nil && *peek.HookEventName != "" {
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
		tool, input, output := extractCodexFields(toolName, toolInputRaw, toolResponseRaw)
		tc.Tool = tool
		tc.Input = input
		tc.Output = output
		if toolUseID != "" {
			tc.ID = toolUseID
		}
		rt := "codex"
		tc.Runtime = &rt
	} else if peek.Runtime != nil && *peek.Runtime == "opencode" {
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
		tool, input, output := extractOpenCodeFields(inputRaw, outputRaw)
		tc.Tool = tool
		tc.Input = input
		tc.Output = output
		rt := *peek.Runtime
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

	sessionID, err := db.ResolveSessionID(tc.SessionID, tc.Timestamp)
	if err != nil {
		return nil, internalError("ktx hook: resolve session: %v", err)
	}
	tc.SessionID = sessionID

	if tc.ContentHash == "" && tc.Output != "" {
		tc.ContentHash = dbpkg.ContentHash(tc.Output)
	}

	if err := db.PersistToolCall(&tc); err != nil {
		return nil, internalError("ktx hook: persist tool_call: %v", err)
	}

	sourceID := tc.ID

	cfg, err := config.Load()
	if err != nil {
		return nil, usageError("%v", err)
	}
	timeout := config.DefaultPipelineTimeout
	if cfg.PipelineTimeout > 0 {
		timeout = cfg.PipelineTimeout
	}

	compressedOutput, pipeJSON, transformed, err := compressPipeline(&tc, nil, stderr, timeout)
	if err != nil {
		return nil, err
	}

	// Footer-aware second gate. Fires only when recover_footer is
	// enabled and the pipeline actually transformed the output. The
	// pipeline Runner's byte-gate already fired inside compressPipeline
	// (it discards results not smaller than the input). This second
	// gate accounts for the footer's own byte overhead: when
	// len(compressed)+len(footer) >= len(original), the transform is
	// suppressed — source returned unchanged, no footer, no compressed
	// row. The footer lives only in the agent-visible envelope.Output;
	// the persisted Compressed.Output stays content-only.
	envelopeOutput := compressedOutput
	if cfg.RecoverFooter && transformed {
		footer := buildRecoveryFooter(sourceID, len(tc.Output), len(compressedOutput))
		if len(compressedOutput)+len(footer) >= len(tc.Output) {
			transformed = false
			envelopeOutput = tc.Output
			compressedOutput = tc.Output
		} else {
			envelopeOutput = compressedOutput + footer
		}
	}

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

	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if isCodex {
		var out codexHookOutput
		if transformed {
			out = codexHookOutput{Decision: "block", Reason: envelopeOutput}
		}
		if err := enc.Encode(out); err != nil {
			return nil, internalError("ktx hook: write codex output: %v", err)
		}
		return nil, nil
	}
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
		NeedsDB:           true,
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
	fmt.Fprintln(w, "Reads a flat JSON tool_call from stdin, persists the original to the")
	fmt.Fprintln(w, "database, runs the configured compression pipeline, and writes a JSON")
	fmt.Fprintln(w, "envelope to stdout:")
	fmt.Fprintln(w, `  {"output": "<compressed>", "compressed_id": "<id>|null", "source_id": "<id>"}`)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Input fields:")
	fmt.Fprintln(w, "  tool       required (the binary cannot infer the tool name)")
	fmt.Fprintln(w, "  id         optional; generated as a 16-char lowercase id when empty")
	fmt.Fprintln(w, "  timestamp  optional; generated as an RFC3339Nano timestamp when empty")
	fmt.Fprintln(w, "  output     tool output to persist (and compress when a pipeline is configured)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The agent sees the compressed output.output; the original is retrievable via")
	fmt.Fprintln(w, "`ktx decompress <compressed_id>`. When no ktx.toml exists (passthrough default),")
	fmt.Fprintln(w, "the original is returned unchanged with compressed_id=null.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A pipeline timeout is enforced (critical-path contract); the default")
	fmt.Fprintln(w, "is 2s, configurable via [pipeline.default] timeout in ktx.toml (e.g.")
	fmt.Fprintln(w, "`timeout = \"500ms\"` or `timeout = \"10s\"`). On timeout the original is")
	fmt.Fprintln(w, "returned unchanged with a stderr warning.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "By default, lossy compression appends a recovery footer to the compressed")
	fmt.Fprintln(w, "output the agent sees (recover_footer = true). The footer carries the")
	fmt.Fprintln(w, "source_id, the compressed size, the reduction ratio, and the recovery")
	fmt.Fprintln(w, "command `ktx read <source_id>` prefixed by an instructional cue. Set")
	fmt.Fprintln(w, "recover_footer = false to suppress it. A footer-aware anti-worsening gate")
	fmt.Fprintln(w, "suppresses the transform (and the footer) when compressed+footer would not")
	fmt.Fprintln(w, "be smaller than the original; the stored compressed row stays content-only.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Codex mode: when the input payload carries a top-level `hook_event_name`")
	fmt.Fprintln(w, "field (Codex PostToolUse), the hook auto-detects it and extracts tool")
	fmt.Fprintln(w, "name, input, and output from `tool_name`, `tool_input`, and")
	fmt.Fprintln(w, "`tool_response` instead of the flat tool_call shape. The response uses")
	fmt.Fprintln(w, "the Codex PostToolUse output contract:")
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
