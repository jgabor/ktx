package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

// hookPipelineTimeout is the legacy default critical-path deadline for the
// pipeline run inside `ktx hook`. It is retained as the backward-
// compatible default; the timeout is configurable via the
// [pipeline.default] timeout field in ktx.toml (config.DefaultPipelineTimeout
// is the value when the field is omitted). hookLogic now reads
// cfg.PipelineTimeout and passes it to compressPipeline, so this constant
// is the fallback only. `ktx compress` (off-line) passes timeout=0 (no
// timeout) because the developer can Ctrl-C.
const hookPipelineTimeout = 30 * time.Second

// hookEnvelope is the JSON response written by `ktx hook` to stdout.
// The plugin parses this and sets output.output = Output (the
// compressed bytes the agent sees). CompressedID and SourceID are
// developer-facing (for reversibility via `ktx decompress`) and are
// never injected into the agent's context (invisibility).
type hookEnvelope struct {
	Output       string `json:"output"`
	CompressedID *string `json:"compressed_id"`
	SourceID     string `json:"source_id"`
}

// hookCommandFunc is the dispatch entry point for `ktx hook`. The
// dispatcher opens the DB and exposes stdin (NeedsDB=true,
// NeedsStdin=true, PassthroughStdout=true).
func hookCommandFunc(ctx *DispatchContext) (any, error) {
	return hookLogic(ctx.Stdin, ctx.DB, ctx.Stdout, ctx.Stderr)
}

// hookLogic reads a flat JSON tool_call from stdin, persists the
// original via the shared PersistToolCall code path (DRY, same code
// ktx write calls), then delegates to compressPipeline for the failure
// gate + pipeline (DRY, same code ktx compress calls). A
// compressed row is created ONLY when the pipeline actually transforms
// the output; passthrough (no ktx.toml) and failure-gate-skip (is_error=true)
// return the source output unchanged with compressed_id=null.
//
// The JSON envelope is written to stdout as a single line. On any
// error, no envelope is written; the exit code and stderr error
// envelope are the dispatcher's responsibility. The plugin level
// (tool.execute.after) catches non-zero exit and leaves output.output
// unchanged (conservative failure at the plugin level).
func hookLogic(stdin io.Reader, db *dbpkg.DB, stdout, stderr io.Writer) (any, error) {
	body, err := io.ReadAll(stdin)
	if err != nil {
		return nil, internalError("ktx hook: read stdin: %v", err)
	}
	if len(body) == 0 {
		return nil, usageError("ktx hook: no input on stdin")
	}

	var tc dbpkg.ToolCall
	if err := json.Unmarshal(body, &tc); err != nil {
		return nil, usageError("ktx hook: parse JSON: %v", err)
	}

	if tc.ID == "" {
		return nil, usageError("ktx hook: missing required field: id")
	}
	if tc.Tool == "" {
		return nil, usageError("ktx hook: missing required field: tool")
	}
	if tc.Timestamp == "" {
		return nil, usageError("ktx hook: missing required field: timestamp")
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
	var footer string
	envelopeOutput := compressedOutput
	if cfg.RecoverFooter && transformed {
		footer = buildRecoveryFooter(sourceID, len(tc.Output), len(compressedOutput))
		if len(compressedOutput)+len(footer) >= len(tc.Output) {
			transformed = false
			footer = ""
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

	envelope := hookEnvelope{
		Output:       envelopeOutput,
		CompressedID: compressedID,
		SourceID:     sourceID,
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(envelope); err != nil {
		return nil, internalError("ktx hook: write envelope: %v", err)
	}
	return nil, nil
}

// buildRecoveryFooter produces a compact, agent-parseable recovery
// footer appended to the compressed output in the hook envelope. The
// footer is NOT persisted in the compressed table row — it lives only
// in the agent-visible envelope.Output so the agent can recover the
// original via `ktx read <source_id>`.
//
// The footer contains: the source_id verbatim, original size, compressed
// size, the reduction ratio (percent), and the recovery command
// `ktx read <source_id>`.
func buildRecoveryFooter(sourceID string, originalBytes, compressedBytes int) string {
	reduction := 0
	if originalBytes > 0 && compressedBytes < originalBytes {
		reduction = (originalBytes - compressedBytes) * 100 / originalBytes
	}
	return fmt.Sprintf(
		"\n--- ktx recovery ---\nsource_id: %s\noriginal: %d bytes -> compressed: %d bytes (%d%% reduction)\nrecover: ktx read %s\n",
		sourceID, originalBytes, compressedBytes, reduction, sourceID,
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
	fmt.Fprintln(w, "The agent sees the compressed output.output; the original is retrievable via")
	fmt.Fprintln(w, "`ktx decompress <compressed_id>`. When no ktx.toml exists (passthrough default),")
	fmt.Fprintln(w, "the original is returned unchanged with compressed_id=null.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A pipeline timeout is enforced (critical-path contract); the default")
	fmt.Fprintln(w, "is 30s, configurable via [pipeline.default] timeout in ktx.toml (e.g.")
	fmt.Fprintln(w, "`timeout = \"10s\"`). On timeout the original is returned unchanged with a")
	fmt.Fprintln(w, "stderr warning.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Set [pipeline.default] recover_footer = true in ktx.toml (default false) to")
	fmt.Fprintln(w, "append a recovery footer to the compressed output the agent sees. The footer")
	fmt.Fprintln(w, "carries the source_id, original and compressed sizes, the reduction ratio, and")
	fmt.Fprintln(w, "the recovery command `ktx read <source_id>`. A footer-aware anti-worsening")
	fmt.Fprintln(w, "gate suppresses the transform (and the footer) when compressed+footer would")
	fmt.Fprintln(w, "not be smaller than the original; the stored compressed row stays content-only.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "This verb is called by the OpenCode plugin's tool.execute.after hook (installed")
	fmt.Fprintln(w, "via `ktx init opencode`). It is not typically invoked directly by users.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx compress    explicit-verb compression (returns compressed id)")
	fmt.Fprintln(w, "  ktx decompress  reverse a compression (return source output verbatim)")
	fmt.Fprintln(w, "  ktx init        install the OpenCode adapter plugin")
}
