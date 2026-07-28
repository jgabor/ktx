// Package router matches tool-call metadata to named pipeline
// profiles using the ordered route rules parsed from ktx.toml.
//
// Matching is a pure function over a tool call and the route-rule
// slice from [config.Config]. The router selects the first rule
// whose match conditions all hold and reports its Profile name with
// matched-route provenance. When no rule matches — or the tool call is
// malformed/empty — the router reports the caller-supplied default profile
// with default-fallback provenance. The router never returns an error and
// performs no I/O.
//
// Profile names and exit-status values are validated at config-load time, so
// the router trusts that each rule carries a real [pipeline.<name>] target and
// one of the documented exit-status values.
package router

import (
	"encoding/json"
	"strings"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/db"
)

const (
	RouteMatched         = "matched"
	RouteDefaultFallback = "default_fallback"
)

// Resolution identifies the selected profile and whether an ordered route
// matched or the default profile was used as fallback.
type Resolution struct {
	Profile string
	Route   string
}

// Resolve returns the first matching route's profile and matched provenance.
// When no rule matches, or tc/routes are nil, it returns defaultProfile with
// default-fallback provenance.
func Resolve(tc *db.ToolCall, routes []config.RouteRule, defaultProfile string) Resolution {
	if tc != nil {
		for _, r := range routes {
			if ruleMatches(tc, r) {
				return Resolution{Profile: r.Profile, Route: RouteMatched}
			}
		}
	}
	return Resolution{Profile: defaultProfile, Route: RouteDefaultFallback}
}

// Match returns the profile name of the first route rule whose
// conditions all match tc. Conditions with empty fields are wildcards:
// an empty Tool matches any tool, an empty CommandPrefix matches any
// command, an empty Subcommand matches any subcommand, and an empty
// or "any" ExitStatus matches any exit state. When no rule matches,
// or tc/routes are nil, Match returns defaultProfile.
//
// Malformed or empty tool-call input JSON causes command-prefix and
// subcommand matching to fail for that rule (their conditions cannot
// be satisfied); other conditions on the same rule still apply. Match
// never panics on malformed input.
func Match(tc *db.ToolCall, routes []config.RouteRule, defaultProfile string) string {
	return Resolve(tc, routes, defaultProfile).Profile
}

// ruleMatches reports whether every non-empty condition on r holds
// against tc. Empty fields are wildcards. CommandPrefix requires the
// command string to start with the prefix; Subcommand requires the
// first non-flag token after the binary name to equal the value.
// ExitStatus "success" requires no error; "failure" requires an error; "any"
// matches either state. The config loader rejects every other non-empty value.
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
	if r.Subcommand != "" {
		sub, ok := subcommandFromInput(tc.Input)
		if !ok || sub != r.Subcommand {
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

// CommandFromInput is the exported wrapper for commandFromInput.
// It extracts the "command" field from a tool-call's input JSON.
// Returns "" and false when the input is empty, not valid JSON,
// or the command field is absent or empty.
func CommandFromInput(input string) (string, bool) {
	return commandFromInput(input)
}

// BinaryFromInput extracts the binary name (first token) from a
// tool-call's input JSON command field. Returns "" when the input
// is malformed, the command is absent, or the command has no
// non-whitespace tokens.
func BinaryFromInput(input string) string {
	cmd, ok := commandFromInput(input)
	if !ok {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// subcommandFromInput parses the tool-call input JSON, extracts the
// "command" field, and returns the first non-flag token after the
// binary name. Tokens starting with "-" are treated as flags and
// skipped. Returns ok=false when the input is malformed, the command
// has fewer than two tokens, or no non-flag token follows the binary.
func subcommandFromInput(input string) (string, bool) {
	cmd, ok := commandFromInput(input)
	if !ok {
		return "", false
	}
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return "", false
	}
	for _, f := range fields[1:] {
		if !strings.HasPrefix(f, "-") {
			return f, true
		}
	}
	return "", false
}
