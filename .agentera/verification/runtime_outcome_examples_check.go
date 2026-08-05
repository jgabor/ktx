// Command runtime_outcome_examples_check validates the retained outcome
// examples without invoking a runtime, model, product binary, or database.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type fixture struct {
	ContractVersion string            `json:"contract_version"`
	RuntimePins     map[string]string `json:"runtime_pins"`
	Cases           []example         `json:"cases"`
}

type example struct {
	ID              string         `json:"id"`
	Ingress         string         `json:"ingress"`
	Producer        string         `json:"producer"`
	CallerAuthority bool           `json:"caller_authority"`
	Payload         map[string]any `json:"payload"`
	Expected        map[string]any `json:"expected"`
}

func main() {
	if len(os.Args) != 2 {
		fail("usage: go run .agentera/verification/runtime_outcome_examples_check.go .agentera/verification/runtime-outcome-examples.json")
	}
	b, err := os.ReadFile(os.Args[1])
	must(err)
	var f fixture
	must(json.Unmarshal(b, &f))
	require(f.ContractVersion == "runtime-outcome-examples-v1", "unexpected contract version")
	require(f.RuntimePins["opencode"] == "1.18.5@e5cc278dec9294a627a7b05f47ce6a564408c1a2", "unexpected OpenCode pin")
	require(f.RuntimePins["codex"] == "0.144.4@8c68d4c87dc54d38861f5114e920c3de2efa5876", "unexpected Codex pin")
	require(len(f.Cases) == 10, "got %d cases, want 10", len(f.Cases))

	seen := make(map[string]bool, len(f.Cases))
	for _, c := range f.Cases {
		require(c.ID != "" && !seen[c.ID], "missing or duplicate case id %q", c.ID)
		seen[c.ID] = true
		got := classify(c)
		require(got["outcome"] == c.Expected["outcome"], "%s outcome=%v, want %v", c.ID, got["outcome"], c.Expected["outcome"])
		for field, want := range c.Expected {
			if field == "outcome" {
				continue
			}
			require(got[field] == want, "%s %s=%v, want %v", c.ID, field, got[field], want)
		}
	}

	for _, id := range []string{
		"opencode-wrapper-error-absent-exit",
		"opencode-wrapper-completed-absent-exit",
		"opencode-wrapper-absent-status-valid-exit",
		"opencode-hook-valid-shell-exit",
		"opencode-hook-malformed-shell-exit",
		"codex-post-tool-use-success-misleading-nested-fields",
		"codex-post-tool-use-success-absent-nested-outcome",
		"codex-malformed-post-tool-use-missing-tool-use-id",
		"sidecar-explicit-success-conflicting-exit",
		"sidecar-malformed-outcome",
	} {
		require(seen[id], "missing required case %s", id)
	}
	fmt.Printf("PASS runtime outcome examples: %d cases, OpenCode and Codex pins verified\n", len(f.Cases))
}

func classify(c example) map[string]any {
	switch c.Ingress {
	case "opencode_wrapper":
		state := object(object(c.Payload["part"])["state"])
		status, statusOK := state["status"].(string)
		if status == "completed" || status == "error" {
			require(validOpenCodeTerminal(c.Payload), "%s is not a contract-valid OpenCode terminal event", c.ID)
		}
		exit, exitOK := integer(object(state["metadata"])["exit"])
		statusClass := "unknown"
		if status == "completed" || status == "error" {
			statusClass = "authoritative"
		}
		exitClass := "unknown"
		if c.Producer == "opencode_shell" && exitOK {
			exitClass = "authoritative"
		}
		outcome := "unknown"
		if statusOK && status == "error" {
			outcome = "failure"
		} else if exitClass == "authoritative" && exit != 0 {
			outcome = "failure"
		} else if exitClass == "authoritative" || status == "completed" {
			outcome = "success"
		}
		return map[string]any{"outcome": outcome, "status": statusClass, "exit": exitClass}

	case "opencode_hook":
		require(validOpenCodeHook(c.Payload), "%s is not a contract-valid OpenCode after-hook callback", c.ID)
		exit, ok := integer(object(object(c.Payload["output"])["metadata"])["exit"])
		exitClass, outcome := "unknown", "unknown"
		if c.Producer == "opencode_shell" && ok {
			exitClass = "authoritative"
			outcome = "success"
			if exit != 0 {
				outcome = "failure"
			}
		}
		return map[string]any{"outcome": outcome, "status": "unsupported", "exit": exitClass}

	case "codex_post_tool_use":
		if c.Producer == "codex_runtime" && validCodexPostToolUse(c.Payload) {
			return map[string]any{"outcome": "success", "event": "authoritative", "nested": "unknown"}
		}
		return map[string]any{"outcome": "unknown", "event": "unknown", "nested": "unknown"}

	case "sidecar":
		isError, ok := c.Payload["is_error"].(bool)
		if c.CallerAuthority && ok {
			outcome := "success"
			if isError {
				outcome = "failure"
			}
			return map[string]any{"outcome": outcome, "is_error": "authoritative", "exit": "diagnostic"}
		}
		return map[string]any{"outcome": "unknown", "is_error": "unknown", "exit": "unknown"}
	default:
		fail("%s has unknown ingress %q", c.ID, c.Ingress)
	}
	return nil
}

func validOpenCodeTerminal(p map[string]any) bool {
	part := object(p["part"])
	for _, key := range []string{"id", "sessionID", "messageID", "callID", "tool"} {
		if value, ok := part[key].(string); !ok || value == "" {
			return false
		}
	}
	if p["type"] != "tool_use" || part["type"] != "tool" {
		return false
	}
	state := object(part["state"])
	if object(state["input"]) == nil || !validTime(object(state["time"])) {
		return false
	}
	switch state["status"] {
	case "error":
		_, ok := state["error"].(string)
		return ok
	case "completed":
		_, outputOK := state["output"].(string)
		_, titleOK := state["title"].(string)
		return outputOK && titleOK && object(state["metadata"]) != nil
	default:
		return false
	}
}

func validTime(v map[string]any) bool {
	_, startOK := v["start"].(float64)
	_, endOK := v["end"].(float64)
	return startOK && endOK
}

func validOpenCodeHook(p map[string]any) bool {
	input := object(p["input"])
	for _, key := range []string{"tool", "sessionID", "callID"} {
		if value, ok := input[key].(string); !ok || value == "" {
			return false
		}
	}
	if _, ok := input["args"]; !ok {
		return false
	}
	output := object(p["output"])
	for _, key := range []string{"title", "output"} {
		if _, ok := output[key].(string); !ok {
			return false
		}
	}
	return object(output["metadata"]) != nil
}

func validCodexPostToolUse(p map[string]any) bool {
	if p["hook_event_name"] != "PostToolUse" {
		return false
	}
	for _, key := range []string{"cwd", "model", "permission_mode", "session_id", "tool_name", "tool_use_id", "turn_id"} {
		if _, ok := p[key].(string); !ok {
			return false
		}
	}
	permissions := map[string]bool{"default": true, "acceptEdits": true, "plan": true, "dontAsk": true, "bypassPermissions": true}
	if !permissions[p["permission_mode"].(string)] {
		return false
	}
	allowed := map[string]bool{
		"agent_id": true, "agent_type": true, "cwd": true, "hook_event_name": true,
		"model": true, "permission_mode": true, "session_id": true, "tool_input": true,
		"tool_name": true, "tool_response": true, "tool_use_id": true,
		"transcript_path": true, "turn_id": true,
	}
	for key := range p {
		if !allowed[key] {
			return false
		}
	}
	for _, key := range []string{"agent_id", "agent_type"} {
		if value, present := p[key]; present && !isString(value) {
			return false
		}
	}
	_, hasInput := p["tool_input"]
	_, hasResponse := p["tool_response"]
	transcript, hasTranscript := p["transcript_path"]
	return hasInput && hasResponse && hasTranscript && (transcript == nil || isString(transcript))
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func integer(v any) (int64, bool) {
	n, ok := v.(float64)
	return int64(n), ok && n == float64(int64(n))
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func require(ok bool, format string, args ...any) {
	if !ok {
		fail(format, args...)
	}
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL runtime outcome examples: "+format+"\n", args...)
	os.Exit(1)
}
