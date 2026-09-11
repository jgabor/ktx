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
		command := commandMetadata{input: tc.Input}
		for _, r := range routes {
			if ruleMatchesCommand(tc, r, &command) {
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

// commandMetadata is private to one resolution and only interprets fields
// when a surviving rule needs them. It never retains input across calls.
type commandMetadata struct {
	input                          string
	command, subcommand            string
	parsed, valid, split, subValid bool
}

func (c *commandMetadata) getCommand() (string, bool) {
	if !c.parsed {
		c.command, c.valid = commandFromInput(c.input)
		c.parsed = true
	}
	return c.command, c.valid
}

func (c *commandMetadata) getSubcommand() (string, bool) {
	if !c.split {
		if cmd, ok := c.getCommand(); ok {
			c.subcommand, c.subValid = subcommandFromCommand(cmd)
		}
		c.split = true
	}
	return c.subcommand, c.subValid
}

// ruleMatchesCommand reports whether all non-empty conditions hold. ExitCode
// remains diagnostic; only explicit IsError values match success or failure.
func ruleMatchesCommand(tc *db.ToolCall, r config.RouteRule, command *commandMetadata) bool {
	if r.Tool != "" && tc.Tool != r.Tool {
		return false
	}
	if r.CommandPrefix != "" {
		cmd, ok := command.getCommand()
		if !ok || !strings.HasPrefix(cmd, r.CommandPrefix) {
			return false
		}
	}
	if r.Subcommand != "" {
		sub, ok := command.getSubcommand()
		if !ok || sub != r.Subcommand {
			return false
		}
	}
	switch r.ExitStatus {
	case "success":
		if tc.IsError == nil || *tc.IsError {
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

// subcommandFromCommand returns the first non-flag token after the binary.
func subcommandFromCommand(cmd string) (string, bool) {
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
