package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/experiment"
)

func runReadDescriptionTurns(args []string, followup, directory string, stdout, stderr io.Writer) error {
	version, err := exec.Command("opencode", "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "1.18.5" {
		return errors.New("read-description treatment requires OpenCode 1.18.5")
	}
	pluginPath, err := opencodeGlobalPluginPath()
	if err != nil {
		return err
	}
	plugin, err := os.ReadFile(pluginPath)
	if err != nil || string(plugin) != ktxOpencodePluginTemplate {
		return errors.New("read-description treatment requires the binary's generated collector plugin")
	}
	turns := 1
	if followup != "" {
		turns = 2
	}
	session := ""
	for turn := 1; turn <= turns; turn++ {
		path := filepath.Join(directory, fmt.Sprintf("turn-%d.jsonl", turn))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		commandArgs := args
		if turn == 2 {
			commandArgs = append(append([]string{}, args[:len(args)-1]...), "--session", session, followup)
		}
		command := exec.Command("opencode", commandArgs...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, io.MultiWriter(file, stdout), stderr
		err = command.Run()
		if err == nil {
			_, err = file.Seek(0, io.SeekStart)
			if err == nil {
				var observed string
				observed, err = experiment.RuntimeTurnSession(file)
				if err == nil && session != "" && observed != session {
					err = errors.New("follow-up switched runtime sessions")
				}
				session = observed
			}
		}
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

// Only the owned benchmark adapter enables this hook. No caller-supplied code,
// schema, or replacement text is evaluated. AST JSON is an observation of the
// hook input, not a serialization of validators or the provider's final schema.
const benchmarkReadDescriptionTemplate = `
import { appendFileSync, readFileSync } from "node:fs"
import { createHash } from "node:crypto"
const benchmarkReadDescription = (required: boolean, epoch: string) => {
  const side = process.env.` + benchcontract.ReadDescriptionEnv + `
  if (!side) return undefined
  if (!required || !["baseline", "candidate"].includes(side)) throw new Error("read-description treatment requires an owned benchmark arm")
  const directory = "` + benchcontract.ReadDescriptionDir + `"
  const digest = (text: string) => createHash("sha256").update(text).digest("hex")
  const original = readFileSync(directory + "/original.txt", "utf8")
  const candidate = readFileSync(directory + "/candidate.txt", "utf8")
  if (digest(original) !== "` + benchcontract.ReadDescriptionOriginalSHA256 + `" || digest(candidate) !== "` + benchcontract.ReadDescriptionCandidateSHA256 + `") throw new Error("read-description fixture identity mismatch")
  const tools = new Set(["invalid", "question", "bash", "read", "glob", "grep", "task", "webfetch", "todowrite", "skill", "apply_patch"])
  let ordinal = 0
  return async (input: any, output: any) => {
    if (!tools.has(input.toolID)) throw new Error("unexpected tool in read-description experiment")
    const before = output.description
    const parameters = output.parameters
    const jsonSchema = output.jsonSchema
    const ast = JSON.stringify(parameters?.ast)
    if (typeof before !== "string" || typeof ast !== "string" || before.length > 65536 || ast.length > 65536) throw new Error("unavailable or oversized public tool definition")
    if (input.toolID === "read") {
      if (before !== original || digest(ast) !== "` + benchcontract.ReadParametersASTSHA256 + `" || jsonSchema !== undefined) throw new Error("read-description source or parameter schema changed")
      if (side === "candidate") {
        let transformed = before
        for (const line of [
          "- The filePath parameter should be an absolute path.\n",
          "- By default, this tool returns up to 2000 lines from the start of the file.\n",
          "- The offset parameter is the line number to start from (1-indexed).\n",
        ]) {
          if (transformed.split(line).length !== 2) throw new Error("read-description removal mismatch")
          transformed = transformed.replace(line, "")
        }
        if (transformed !== candidate) throw new Error("read-description transform mismatch")
        output.description = transformed
      }
    }
    const record = {
      version: "` + benchcontract.ReadDescriptionTreatment + `", boundary: "tool.definition", runtime: "1.18.5",
      owner: process.env.KTX_BENCH_SESSION_ID, interval: process.env.KTX_BENCH_EVENT_INTERVAL_ID,
      epoch, ordinal: ++ordinal, side, tool: input.toolID,
      before_sha256: digest(before), after_sha256: digest(output.description),
      parameters_sha256: digest(ast), json_schema_sha256: digest(JSON.stringify(jsonSchema ?? null)),
      parameters_unchanged: parameters === output.parameters && ast === JSON.stringify(output.parameters?.ast),
      json_schema_unchanged: jsonSchema === output.jsonSchema,
      ...(input.toolID === "read" ? {before, after: output.description, parameters_ast: ast} : {}),
    }
    appendFileSync(directory + "/hooks.jsonl", JSON.stringify(record) + "\n", {mode: 0o600})
  }
}
`
