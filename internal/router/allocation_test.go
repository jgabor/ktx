package router

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/db"
)

// Resolve's previous per-rule, twice-per-prefix/subcommand interpretation.
func repeatedRouteOracle(tc *db.ToolCall, routes []config.RouteRule, fallback string) Resolution {
	command := func() (string, bool) {
		var p struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(tc.Input), &p) != nil {
			return "", false
		}
		return p.Command, p.Command != ""
	}
	if tc != nil {
		for _, r := range routes {
			if r.Tool != "" && r.Tool != tc.Tool {
				continue
			}
			if r.CommandPrefix != "" {
				cmd, ok := command()
				if !ok || !strings.HasPrefix(cmd, r.CommandPrefix) {
					continue
				}
			}
			if r.Subcommand != "" {
				cmd, ok := command()
				if !ok {
					continue
				}
				fields := strings.Fields(cmd)
				sub := ""
				if len(fields) > 1 {
					for _, field := range fields[1:] {
						if !strings.HasPrefix(field, "-") {
							sub = field
							break
						}
					}
				}
				if sub == "" || sub != r.Subcommand {
					continue
				}
			}
			if r.ExitStatus == "success" && (tc.IsError == nil || *tc.IsError) {
				continue
			}
			if r.ExitStatus == "failure" && (tc.IsError == nil || !*tc.IsError) {
				continue
			}
			return Resolution{Profile: r.Profile, Route: RouteMatched}
		}
	}
	return Resolution{Profile: fallback, Route: RouteDefaultFallback}
}

func TestLazyRoutesMatchRepeatedOracle(t *testing.T) {
	inputs := []string{"", "null", "[]", "{}", `{"command":null}`, `{"command":1}`, `{"command":"git status"} trailing`, `{"command":"git status","ignored":[}`, `{"COMMAND":"git\u2003status"}`, `{"command":"git diff","command":"git -C dir status"}`, `{"command":"git status","command":null}`, `{"command":2,"command":"git status"}`, `{"command":" git\t--flag\nstatus "}`, `{"command":"git --flag"}`, `{"command":"git status","ignored":"` + strings.Repeat("x", 1<<20) + `"}`}
	no, yes := false, true
	for _, input := range inputs {
		for _, status := range []*bool{nil, &no, &yes} {
			for _, tool := range []string{"", "bash", "read"} {
				tc := &db.ToolCall{Input: input, Tool: tool, IsError: status}
				routes := []config.RouteRule{
					{Tool: "read", Profile: "read"},
					{CommandPrefix: "git", Subcommand: "diff", ExitStatus: "success", Profile: "diff"},
					{CommandPrefix: "git", Subcommand: "status", ExitStatus: "failure", Profile: "failed"},
					{Subcommand: "status", Profile: "status"},
					{CommandPrefix: "git", Profile: "git"},
					{ExitStatus: "success", Profile: "ok"},
				}
				for start := range routes {
					got, want := Resolve(tc, routes[start:], "default"), repeatedRouteOracle(tc, routes[start:], "default")
					if got != want {
						t.Fatalf("tool=%q status=%v start=%d got=%v want=%v", tool, status, start, got, want)
					}
				}
			}
		}
	}
}

func TestCommandMetadataIsLazyAndLocal(t *testing.T) {
	tc := &db.ToolCall{Tool: "bash", Input: `{"command":"git status"}`}
	c := commandMetadata{input: tc.Input}
	for _, rule := range []config.RouteRule{{Tool: "read", CommandPrefix: "git"}, {ExitStatus: "any"}} {
		ruleMatchesCommand(tc, rule, &c)
		if c.parsed || c.split {
			t.Fatal("irrelevant rule interpreted command")
		}
	}
	ruleMatchesCommand(tc, config.RouteRule{CommandPrefix: "other", Subcommand: "status"}, &c)
	if !c.parsed || c.split {
		t.Fatal("prefix mismatch must not split command")
	}
	routes := []config.RouteRule{{Subcommand: "status", Profile: "status"}}
	if Resolve(tc, routes, "default").Profile != "status" {
		t.Fatal("first resolution")
	}
	tc.Input = `{"command":"git diff"}`
	if Resolve(tc, routes, "default").Profile != "default" {
		t.Fatal("resolution reused old input")
	}
}

func TestManyRoutesAllocationBudget(t *testing.T) {
	tc := &db.ToolCall{Input: `{"command":"git status","ignored":"` + strings.Repeat("x", 1<<20) + `"}`}
	routes := make([]config.RouteRule, 64)
	for i := range routes {
		routes[i] = config.RouteRule{CommandPrefix: "git", Subcommand: "diff", Profile: "diff"}
	}
	Resolve(tc, routes, "default")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := Resolve(tc, routes, "default")
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	if got.Profile != "default" || allocated > 2<<20 {
		t.Fatalf("result %v, allocated %d (budget %d)", got, allocated, 2<<20)
	}
	t.Logf("allocated %d bytes, budget %d", allocated, 2<<20)
}

func BenchmarkManyRoutes(b *testing.B) {
	tc := &db.ToolCall{Tool: "bash", Input: `{"command":"git status","ignored":"` + strings.Repeat("x", 1<<20) + `"}`}
	routes := make([]config.RouteRule, 64)
	for i := range routes {
		routes[i] = config.RouteRule{CommandPrefix: "git", Subcommand: "diff", Profile: "other"}
	}
	for _, irrelevant := range []bool{false, true} {
		name := "command"
		if irrelevant {
			name = "irrelevant"
			for i := range routes {
				routes[i].Tool = "read"
			}
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				Resolve(tc, routes, "default")
			}
		})
	}
}
