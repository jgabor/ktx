// Package router matches tool-call metadata to named pipeline
// profiles using the ordered route rules parsed from ktx.toml.
//
// Matching is a pure function over a tool call and the route-rule
// slice from [config.Config]. The router selects the first rule
// whose match conditions all hold and returns its Profile name. When
// no rule matches — or the tool call is malformed/empty — the router
// returns the caller-supplied default profile name. The router never
// returns an error and performs no I/O.
//
// Profile-name existence is validated at config-load time, so the
// router trusts that each rule's Profile resolves to a real
// [pipeline.<name>] table and does not re-check.
package router

import (
	"encoding/json"
	"strings"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/db"
)

// Match returns the profile name of the first route rule whose
// conditions all match tc. Conditions with empty fields are wildcards:
// an empty Tool matches any tool, an empty CommandPrefix matches any
// command, and an empty or "any" ExitStatus matches any exit state.
// When no rule matches, or tc/routes are nil, Match returns
// defaultProfile.
//
// Malformed or empty tool-call input JSON causes command-prefix
// matching to fail for that rule (its command-prefix condition
// cannot be satisfied); other conditions on the same rule still
// apply. Match never panics on malformed input.
func Match(tc *db.ToolCall, routes []config.RouteRule, defaultProfile string) string {
	if tc == nil {
		return defaultProfile
	}
	for _, r := range routes {
		if ruleMatches(tc, r) {
			return r.Profile
		}
	}
	return defaultProfile
}

// ruleMatches reports whether every non-empty condition on r holds
// against tc. Empty fields are wildcards. ExitStatus "success"
// requires no error; "failure" requires an error; "any" (or any
// unrecognized value) matches either state.
func ruleMatches(tc *db.ToolCall, r config.RouteRule) bool {
	if r.Tool != "" && tc.Tool != r.Tool {
		return false
	}
	if r.CommandPrefix != "" {
		cmd, ok := commandFromInput(tc.Input)
		if !ok || !strings.HasPrefix(cmd, r.CommandPrefix) {
			return false
		}
	}
	switch r.ExitStatus {
	case "success":
		if tc.IsError != nil && *tc.IsError {
			return false
		}
	case "failure":
		if tc.IsError == nil || !*tc.IsError {
			return false
		}
	}
	return true
}

// commandFromInput parses the tool-call input JSON and returns the
// "command" field. Returns ok=false when the input is empty, not
// valid JSON, or the command field is absent or empty.
func commandFromInput(input string) (string, bool) {
	if input == "" {
		return "", false
	}
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &p); err != nil {
		return "", false
	}
	if p.Command == "" {
		return "", false
	}
	return p.Command, true
}
