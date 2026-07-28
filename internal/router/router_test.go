// Tests for the route-rule matcher. Each match dimension (tool name,
// command prefix, exit status) gets one pass and one fail case; the
// remaining cases cover fallback, malformed input, nil safety, and
// first-match-wins ordering.
package router

import (
	"testing"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/db"
)

const defaultProfile = "default"

// boolPtr returns a pointer to b. Used to populate ToolCall.IsError.
func boolPtr(b bool) *bool { return &b }

// intPtr returns a pointer to i. Used to populate ToolCall.ExitCode.
func intPtr(i int64) *int64 { return &i }

// TestMatch_ToolName covers the Tool dimension: one match and one
// non-match on the tool-name condition.
func TestMatch_ToolName(t *testing.T) {
	routes := []config.RouteRule{
		{Tool: "bash", Profile: "bash-profile"},
	}

	t.Run("tool name matches → rule profile", func(t *testing.T) {
		tc := &db.ToolCall{Tool: "bash"}
		if got := Match(tc, routes, defaultProfile); got != "bash-profile" {
			t.Errorf("Match = %q, want bash-profile", got)
		}
	})

	t.Run("tool name does not match → default", func(t *testing.T) {
		tc := &db.ToolCall{Tool: "edit"}
		if got := Match(tc, routes, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})
}

// TestMatch_CommandPrefix covers the CommandPrefix dimension: one
// match and one non-match on the command-prefix condition.
func TestMatch_CommandPrefix(t *testing.T) {
	routes := []config.RouteRule{
		{CommandPrefix: "git", Profile: "git-profile"},
	}

	t.Run("command prefix matches → rule profile", func(t *testing.T) {
		tc := &db.ToolCall{
			Tool:  "bash",
			Input: `{"command":"git status"}`,
		}
		if got := Match(tc, routes, defaultProfile); got != "git-profile" {
			t.Errorf("Match = %q, want git-profile", got)
		}
	})

	t.Run("command prefix does not match → default", func(t *testing.T) {
		tc := &db.ToolCall{
			Tool:  "bash",
			Input: `{"command":"ls -la"}`,
		}
		if got := Match(tc, routes, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})
}

// TestMatch_ExitStatus covers the ExitStatus dimension: "failure"
// matches an errored call, "success" matches a non-errored call, and
// each fails to match the opposite state.
func TestMatch_ExitStatus(t *testing.T) {
	failureRule := []config.RouteRule{
		{ExitStatus: "failure", Profile: "failure-profile"},
	}
	successRule := []config.RouteRule{
		{ExitStatus: "success", Profile: "success-profile"},
	}

	t.Run("failure rule matches errored call → rule profile", func(t *testing.T) {
		tc := &db.ToolCall{Tool: "bash", IsError: boolPtr(true), ExitCode: intPtr(1)}
		if got := Match(tc, failureRule, defaultProfile); got != "failure-profile" {
			t.Errorf("Match = %q, want failure-profile", got)
		}
	})

	t.Run("failure rule does not match successful call → default", func(t *testing.T) {
		tc := &db.ToolCall{Tool: "bash", IsError: boolPtr(false), ExitCode: intPtr(0)}
		if got := Match(tc, failureRule, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})

	t.Run("success rule matches successful call → rule profile", func(t *testing.T) {
		tc := &db.ToolCall{Tool: "bash", IsError: boolPtr(false), ExitCode: intPtr(0)}
		if got := Match(tc, successRule, defaultProfile); got != "success-profile" {
			t.Errorf("Match = %q, want success-profile", got)
		}
	})

	t.Run("success rule does not match errored call → default", func(t *testing.T) {
		tc := &db.ToolCall{Tool: "bash", IsError: boolPtr(true), ExitCode: intPtr(1)}
		if got := Match(tc, successRule, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})
}

// TestMatch_Fallback covers the default-fallback case: no rule
// matches and the router returns the caller-supplied default profile
// name. Also confirms an empty routes slice returns the default.
func TestMatch_Fallback(t *testing.T) {
	t.Run("no matching rule → default profile", func(t *testing.T) {
		routes := []config.RouteRule{
			{Tool: "bash", CommandPrefix: "git", Profile: "git-profile"},
		}
		tc := &db.ToolCall{Tool: "edit", Input: `{"command":"rm -rf /"}`}
		if got := Match(tc, routes, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})

	t.Run("empty routes → default profile", func(t *testing.T) {
		tc := &db.ToolCall{Tool: "bash", Input: `{"command":"git status"}`}
		if got := Match(tc, nil, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})

	t.Run("custom default profile is honored", func(t *testing.T) {
		tc := &db.ToolCall{Tool: "bash"}
		if got := Match(tc, nil, "custom-default"); got != "custom-default" {
			t.Errorf("Match = %q, want custom-default", got)
		}
	})
}

func TestResolve_RouteProvenance(t *testing.T) {
	routes := []config.RouteRule{{Tool: "bash", Profile: defaultProfile}}

	t.Run("matched route targeting default", func(t *testing.T) {
		got := Resolve(&db.ToolCall{Tool: "bash"}, routes, defaultProfile)
		if got.Profile != defaultProfile || got.Route != RouteMatched {
			t.Errorf("Resolve = %+v, want profile %q via %q", got, defaultProfile, RouteMatched)
		}
	})

	t.Run("default fallback", func(t *testing.T) {
		got := Resolve(&db.ToolCall{Tool: "read"}, routes, defaultProfile)
		if got.Profile != defaultProfile || got.Route != RouteDefaultFallback {
			t.Errorf("Resolve = %+v, want profile %q via %q", got, defaultProfile, RouteDefaultFallback)
		}
	})

	t.Run("nil tool call falls back", func(t *testing.T) {
		got := Resolve(nil, routes, defaultProfile)
		if got.Profile != defaultProfile || got.Route != RouteDefaultFallback {
			t.Errorf("Resolve = %+v, want profile %q via %q", got, defaultProfile, RouteDefaultFallback)
		}
	})
}

// TestMatch_MalformedInput covers malformed-input handling: malformed or empty input JSON
// yields the default profile without error, even when a rule requires
// a command prefix. A profile-only rule (no CommandPrefix) still
// matches because no command lookup occurs.
func TestMatch_MalformedInput(t *testing.T) {
	rulesRequringCommand := []config.RouteRule{
		{CommandPrefix: "git", Profile: "git-profile"},
	}

	cases := map[string]string{
		"empty input":         "",
		"malformed JSON":      `{"command":`,
		"missing command key": `{"path":"/tmp"}`,
		"empty command value": `{"command":""}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			tc := &db.ToolCall{Tool: "bash", Input: input}
			if got := Match(tc, rulesRequringCommand, defaultProfile); got != defaultProfile {
				t.Errorf("Match = %q, want %q for malformed input", got, defaultProfile)
			}
		})
	}

	t.Run("profile-only rule matches despite malformed input", func(t *testing.T) {
		profileOnly := []config.RouteRule{{Profile: "catch-all"}}
		tc := &db.ToolCall{Tool: "bash", Input: `{"command":`}
		if got := Match(tc, profileOnly, defaultProfile); got != "catch-all" {
			t.Errorf("Match = %q, want catch-all (profile-only rule has no conditions to fail)", got)
		}
	})
}

// TestMatch_NilToolCall covers the nil-safety guarantee: a nil
// tool call returns the default profile rather than panicking.
func TestMatch_NilToolCall(t *testing.T) {
	routes := []config.RouteRule{
		{Tool: "bash", Profile: "bash-profile"},
	}
	if got := Match(nil, routes, defaultProfile); got != defaultProfile {
		t.Errorf("Match(nil, ...) = %q, want %q", got, defaultProfile)
	}
}

// TestMatch_FirstMatchWins covers the first-match-wins ordering:
// when two rules could both match, the earlier one wins. A later
// rule's profile is not selected even when its conditions also hold.
func TestMatch_FirstMatchWins(t *testing.T) {
	routes := []config.RouteRule{
		{Tool: "bash", CommandPrefix: "git", Profile: "first"},
		{Tool: "bash", CommandPrefix: "git", Profile: "second"},
	}
	tc := &db.ToolCall{Tool: "bash", Input: `{"command":"git status"}`}
	if got := Match(tc, routes, defaultProfile); got != "first" {
		t.Errorf("Match = %q, want first (first-match-wins)", got)
	}
}

// TestMatch_NonBashTool covers tool-name routing: a route rule matching on tool
// name for a non-bash tool selects that rule's profile.
func TestMatch_NonBashTool(t *testing.T) {
	routes := []config.RouteRule{
		{Tool: "read", Profile: "read-profile"},
	}
	tc := &db.ToolCall{Tool: "read", Input: `{"path":"/etc/hosts"}`}
	if got := Match(tc, routes, defaultProfile); got != "read-profile" {
		t.Errorf("Match = %q, want read-profile", got)
	}
}

// TestMatch_ExitFailureRule covers exit-status routing: a tool call with a non-zero
// exit code (signaled via IsError=true) matching a rule that requires
// exit failure selects that rule's profile.
func TestMatch_ExitFailureRule(t *testing.T) {
	routes := []config.RouteRule{
		{Tool: "bash", ExitStatus: "failure", Profile: "failure-profile"},
	}
	tc := &db.ToolCall{
		Tool:     "bash",
		Input:    `{"command":"git commit"}`,
		ExitCode: intPtr(1),
		IsError:  boolPtr(true),
	}
	if got := Match(tc, routes, defaultProfile); got != "failure-profile" {
		t.Errorf("Match = %q, want failure-profile", got)
	}
}

// TestMatch_AllConditions covers a rule that specifies all three
// match dimensions at once: it matches only when every condition
// holds, and fails when any one is off.
func TestMatch_AllConditions(t *testing.T) {
	routes := []config.RouteRule{
		{
			Tool:          "bash",
			CommandPrefix: "git",
			ExitStatus:    "success",
			Profile:       "git-success",
		},
	}

	t.Run("all conditions match → rule profile", func(t *testing.T) {
		tc := &db.ToolCall{
			Tool:     "bash",
			Input:    `{"command":"git status"}`,
			IsError:  boolPtr(false),
			ExitCode: intPtr(0),
		}
		if got := Match(tc, routes, defaultProfile); got != "git-success" {
			t.Errorf("Match = %q, want git-success", got)
		}
	})

	t.Run("one condition off → default (tool mismatch)", func(t *testing.T) {
		tc := &db.ToolCall{
			Tool:    "edit",
			Input:   `{"command":"git status"}`,
			IsError: boolPtr(false),
		}
		if got := Match(tc, routes, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})

	t.Run("one condition off → default (exit mismatch)", func(t *testing.T) {
		tc := &db.ToolCall{
			Tool:     "bash",
			Input:    `{"command":"git status"}`,
			IsError:  boolPtr(true),
			ExitCode: intPtr(1),
		}
		if got := Match(tc, routes, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})
}

// TestMatch_EmptyConditionsAreWildcards covers rules whose match
// fields are empty: such a rule matches anything. A profile-only
// rule has no conditions and always fires.
func TestMatch_EmptyConditionsAreWildcards(t *testing.T) {
	t.Run("profile-only rule matches any tool call", func(t *testing.T) {
		profileOnly := []config.RouteRule{{Profile: "catch-all"}}
		tc := &db.ToolCall{Tool: "bash"}
		if got := Match(tc, profileOnly, defaultProfile); got != "catch-all" {
			t.Errorf("Match = %q, want catch-all", got)
		}
	})

	t.Run("exit_status any matches both success and failure", func(t *testing.T) {
		anyRule := []config.RouteRule{{ExitStatus: "any", Profile: "any-profile"}}
		success := &db.ToolCall{Tool: "bash", IsError: boolPtr(false)}
		failure := &db.ToolCall{Tool: "bash", IsError: boolPtr(true)}
		if got := Match(success, anyRule, defaultProfile); got != "any-profile" {
			t.Errorf("success Match = %q, want any-profile", got)
		}
		if got := Match(failure, anyRule, defaultProfile); got != "any-profile" {
			t.Errorf("failure Match = %q, want any-profile", got)
		}
	})

	t.Run("nil IsError treated as success for success rule", func(t *testing.T) {
		successRule := []config.RouteRule{{ExitStatus: "success", Profile: "success-profile"}}
		tc := &db.ToolCall{Tool: "bash", IsError: nil}
		if got := Match(tc, successRule, defaultProfile); got != "success-profile" {
			t.Errorf("Match = %q, want success-profile (nil IsError = no error)", got)
		}
	})
}

// TestMatch_Subcommand covers the Subcommand dimension: the router
// matches on the first non-flag token after the binary name in the
// command string. Flags before the subcommand are skipped.
func TestMatch_Subcommand(t *testing.T) {
	t.Run("subcommand matches → rule profile", func(t *testing.T) {
		routes := []config.RouteRule{
			{Subcommand: "status", Profile: "status-profile"},
		}
		tc := &db.ToolCall{Tool: "bash", Input: `{"command":"git status --short"}`}
		if got := Match(tc, routes, defaultProfile); got != "status-profile" {
			t.Errorf("Match = %q, want status-profile", got)
		}
	})

	t.Run("different subcommand does not match → default", func(t *testing.T) {
		routes := []config.RouteRule{
			{Subcommand: "status", Profile: "status-profile"},
		}
		tc := &db.ToolCall{Tool: "bash", Input: `{"command":"git diff"}`}
		if got := Match(tc, routes, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})

	t.Run("command with no subcommand does not match → default", func(t *testing.T) {
		routes := []config.RouteRule{
			{Subcommand: "status", Profile: "status-profile"},
		}
		tc := &db.ToolCall{Tool: "bash", Input: `{"command":"git"}`}
		if got := Match(tc, routes, defaultProfile); got != defaultProfile {
			t.Errorf("Match = %q, want %q", got, defaultProfile)
		}
	})

	t.Run("flags before subcommand are skipped → rule profile", func(t *testing.T) {
		routes := []config.RouteRule{
			{Subcommand: "log", Profile: "log-profile"},
		}
		tc := &db.ToolCall{Tool: "bash", Input: `{"command":"git --no-pager log"}`}
		if got := Match(tc, routes, defaultProfile); got != "log-profile" {
			t.Errorf("Match = %q, want log-profile", got)
		}
	})

	t.Run("malformed input does not match → default", func(t *testing.T) {
		routes := []config.RouteRule{
			{Subcommand: "status", Profile: "status-profile"},
		}
		cases := map[string]string{
			"empty input":         "",
			"malformed JSON":      `{"command":`,
			"missing command key": `{"path":"/tmp"}`,
			"empty command value": `{"command":""}`,
		}
		for name, input := range cases {
			t.Run(name, func(t *testing.T) {
				tc := &db.ToolCall{Tool: "bash", Input: input}
				if got := Match(tc, routes, defaultProfile); got != defaultProfile {
					t.Errorf("Match = %q, want %q", got, defaultProfile)
				}
			})
		}
	})
}
