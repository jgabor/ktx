package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/pipeline"
)

// hookEnvelope is the JSON response written by `ktx hook` to stdout.
// The plugin parses this and sets output.output = Output (the
// compressed bytes the agent sees). CompressedID and SourceID are
// developer-facing (for reversibility via `ktx decompress`) and are
// never injected into the agent's context (invisibility — D22).
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
// hookLogic reads a D9 flat JSON tool_call from stdin, persists the
// original via the shared PersistToolCall code path (DRY, same code
// ktx write calls), then runs the D14 compression pipeline (DRY,
// same code ktx compress calls). A compressed row is created ONLY
// when the pipeline actually transforms the output; identity (no
// ktx.toml) and D15-skip (is_error=true) return the source output
// unchanged with compressed_id=null.
//
// The JSON envelope is written to stdout as a single line. On any
// error, no envelope is written; the exit code and stderr error
// envelope are the dispatcher's responsibility. The plugin level
// (tool.execute.after) catches non-zero exit and leaves output.output
// unchanged (D14 conservative failure at the plugin level).
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
		return nil, usageError("ktx hook: %v", err)
	}

	runner := buildCompressRunner(nil, cfg, stderr)

	var compressedOutput string
	var pipeJSON string
	var compressedID *string

	if tc.IsError != nil && *tc.IsError {
		fmt.Fprint(stderr, sourceFailureWarning)
		compressedOutput = tc.Output
		pipeJSON = "[]"
	} else if len(runner.Pipeline) == 0 {
		compressedOutput = tc.Output
		pipeJSON = "[]"
	} else {
		res := runner.Run([]byte(tc.Output))
		compressedOutput = string(res.Out)
		pipeJSON = pipeline.EncodePipelineJSON(res.Ran)

		if compressedOutput != tc.Output || pipeJSON != "[]" {
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
	}

	envelope := hookEnvelope{
		Output:       compressedOutput,
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
	fmt.Fprintln(w, "ktx hook — in-flight tool_call compression (D22)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: ktx hook < tool_call.json")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Reads a D9 flat JSON tool_call from stdin, persists the original to the")
	fmt.Fprintln(w, "database, runs the configured compression pipeline, and writes a JSON")
	fmt.Fprintln(w, "envelope to stdout:")
	fmt.Fprintln(w, `  {"output": "<compressed>", "compressed_id": "<id>|null", "source_id": "<id>"}`)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The agent sees the compressed output.output; the original is retrievable via")
	fmt.Fprintln(w, "`ktx decompress <compressed_id>`. When no ktx.toml exists (identity default),")
	fmt.Fprintln(w, "the original is returned unchanged with compressed_id=null.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "This verb is called by the OpenCode plugin's tool.execute.after hook (installed")
	fmt.Fprintln(w, "via `ktx init opencode`). It is not typically invoked directly by users.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Related commands:")
	fmt.Fprintln(w, "  ktx compress    explicit-verb compression (returns compressed id)")
	fmt.Fprintln(w, "  ktx decompress  reverse a compression (return source output verbatim)")
	fmt.Fprintln(w, "  ktx init        install the OpenCode adapter plugin")
}
