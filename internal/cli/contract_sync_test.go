package cli

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPipelineContractDocumentationMatchesBehavior(t *testing.T) {
	root := repositoryRoot(t)
	assertContractText(t,
		readDocumentationFile(t, filepath.Join(root, ".agents", "skills", "ktx", "references", "pipelines.md")),
		[]string{"preserving outer blank lines", "byte length/4", "can be cut mid-line"},
		[]string{"remove outer blank lines", "characters/4, snap to a newline"},
	)
	assertContractText(t,
		readDocumentationFile(t, filepath.Join(root, "ktx.example.toml")),
		[]string{"preserve leading/trailing blank lines", "byte-length/4 estimate", "cut mid-line if none exists"},
		[]string{"strip leading/trailing blank lines", "no mid-line cuts"},
	)
	assertContractText(t,
		readDocumentationFile(t, filepath.Join(root, "internal", "pipeline", "stages", "token_budget.go")),
		[]string{"estimates tokens from byte length", "can cut mid-line otherwise"},
		[]string{"four-characters-per-token", "no line is cut mid-way"},
	)
}

func TestVerificationDocumentationMatchesMageVerify(t *testing.T) {
	root := repositoryRoot(t)
	for _, relative := range []string{
		"README.md",
		filepath.Join(".agents", "skills", "ktx", "references", "benchmarks-development.md"),
	} {
		contents := readDocumentationFile(t, filepath.Join(root, relative))
		assertContractText(t, contents,
			[]string{"test", "vet", "lint", "build", "govulncheck", "portable replay", "golangci-lint"},
			[]string{"and then the\nportable replay gate", "requires `golangci-lint` and enforces"},
		)
	}
}

func TestStatsHelpUsesFirstArtifactAuthority(t *testing.T) {
	for name, printHelp := range map[string]func(*bytes.Buffer){
		"summary":   func(w *bytes.Buffer) { printStatsSummaryHelp(w) },
		"breakdown": func(w *bytes.Buffer) { printStatsBreakdownHelp(w) },
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			printHelp(&out)
			assertContractText(t, out.String(),
				[]string{"earliest retained compressed artifact", "lexicographically smallest artifact ID"},
				[]string{"latest retained compressed artifact", "lexicographically greatest artifact ID"},
			)
		})
	}
}

func TestToolOutcomeContractSurfacesMatchBehavior(t *testing.T) {
	root := repositoryRoot(t)
	var hookHelp bytes.Buffer
	printHookHelp(&hookHelp)
	for name, contents := range map[string]string{
		"help":        hookHelp.String(),
		"example":     readDocumentationFile(t, filepath.Join(root, "ktx.example.toml")),
		"runbook":     readDocumentationFile(t, filepath.Join(root, "docs", "pipelines.md")),
		"agent-guide": readDocumentationFile(t, filepath.Join(root, ".agents", "skills", "ktx", "references", "pipelines.md")),
	} {
		t.Run(name, func(t *testing.T) {
			assertContractText(t, contents,
				[]string{"is_error=false", "is_error=true", "explicit success", "explicit failure", "unknown", "exit_code", "OpenCode 1.18.5", "Codex 0.144.4", "PostToolUse", "tool_response", "caller assertion"},
				[]string{"success route can also match an unknown", "missing is_error matches success"},
			)
		})
	}
}

func TestLoggingContractSurfacesMatchBehavior(t *testing.T) {
	root := repositoryRoot(t)
	var help bytes.Buffer
	printHelp(&help)
	helpText := help.String()
	helpText = helpText[strings.Index(helpText, "Logging:"):strings.Index(helpText, "Run 'ktx")]
	example := readDocumentationFile(t, filepath.Join(root, "ktx.example.toml"))
	example = example[strings.Index(example, "# ── Diagnostic logging"):strings.Index(example, "[pipeline.default]")]
	for name, contents := range map[string]string{
		"help":    helpText,
		"example": example,
		"runbook": readDocumentationFile(t, filepath.Join(root, "docs", "logging.md")),
	} {
		t.Run(name, func(t *testing.T) {
			assertContractText(t, contents, []string{
				"[logs]", "error", "debug", "info", "warn", "off",
				"absolute", "best-effort", "sensitive", "stderr",
			}, []string{"rotation", "16 KiB", "5 MiB", "25 ms", "50 ms", "adapter", "runtime_surface", "config show"})
		})
	}
	assertContractText(t, helpText, []string{"XDG state", "nine typed fields", "no retry", "Unsafe targets"}, nil)
	assertContractText(t, example,
		[]string{"XDG_STATE_HOME", "timestamp", "elapsed_wait_nanoseconds", "no retry", "unsafe targets"}, nil)

	runbook := readDocumentationFile(t, filepath.Join(root, "docs", "logging.md"))
	for _, field := range []string{
		"`timestamp`", "`level`", "`event`", "`ktx_command`", "`executable_path`", "`database_path`", "`failure_class`", "`operation_phase`",
		"`elapsed_wait_nanoseconds`",
	} {
		if !strings.Contains(runbook, field) {
			t.Errorf("logging runbook is missing field %s", field)
		}
	}
	assertContractText(t, runbook,
		[]string{"payloads", "database content", "SQL", "raw driver errors", "credentials", "secrets", "arbitrary fields"}, nil)
}

func TestOutcomeReportContractsPublishCurrentSchemasAndDenominators(t *testing.T) {
	root := repositoryRoot(t)
	var summaryHelp, breakdownHelp, dbHelp bytes.Buffer
	printStatsSummaryHelp(&summaryHelp)
	printStatsBreakdownHelp(&breakdownHelp)
	printDBHelp(&dbHelp)

	assertContractText(t, summaryHelp.String(),
		[]string{"schema_version 10", "prompt_cache_token_hit_rate_pct", "trusted-ingress authority", "empty population has null coverage", "percentage units", "measured zero", "all accepted models and providers", "weighting them by tokens", "request hit rate", "no separate schema version"},
		[]string{"cache_read_share", "runtime_reported_cost_usd", "api_equivalent_estimated_cost_usd", "sum(cache_read)"})
	assertContractText(t, breakdownHelp.String(),
		[]string{"schema_version 4", "Known is success+error", "total is known+unknown", "no separate schema version", "do not report per-group outcome rates"}, nil)
	assertContractText(t, dbHelp.String(),
		[]string{"report schema 3", "population is all tool calls", "error rate is error/known", "coverage is known/total", "round to 2 decimals"}, nil)

	for _, relative := range []string{
		filepath.Join("docs", "pipelines.md"),
		filepath.Join(".agents", "skills", "ktx", "references", "database-telemetry.md"),
	} {
		contents := readDocumentationFile(t, filepath.Join(root, relative))
		assertContractText(t, contents,
			[]string{"Summary schema 10", "breakdown schema 4", "report schema 3", "prompt_cache_token_hit_rate_pct", "trusted ingress", "historical", "coverage_pct", "percentage units", "measured zero", "all accepted", "providers and models", "token-weighted", "known_calls = successful_calls + error_calls", "outcome_coverage_pct = known_calls / total_calls * 100", "no separate schema version"},
			[]string{"version 1 breakdown command"},
		)
	}
	for _, relative := range []string{
		filepath.Join(".agents", "skills", "ktx", "SKILL.md"),
		filepath.Join(".agents", "skills", "ktx", "references", "cli.md"),
	} {
		assertContractText(t, readDocumentationFile(t, filepath.Join(root, relative)),
			[]string{"prompt_cache_token_hit_rate_pct", "percentage", "provider request hit"},
			[]string{"cache_read_share", "runtime_reported_cost_usd", "api_equivalent_estimated_cost_usd"},
		)
	}
}

func TestBenchmarkNamespaceCommentListsPublicFamilies(t *testing.T) {
	contents := readDocumentationFile(t, filepath.Join(repositoryRoot(t), "internal", "cli", "bench.go"))
	assertContractText(t, contents,
		[]string{"run, report, export, compare, rank", "validate, agent, and corpus"},
		[]string{"surface ships two subcommands"},
	)
}

func TestBenchPreflightContractSurfacesMatchBehavior(t *testing.T) {
	root := repositoryRoot(t)
	var help bytes.Buffer
	printBenchHelp(&help)
	for name, contents := range map[string]string{
		"help":        help.String(),
		"readme":      readDocumentationFile(t, filepath.Join(root, "README.md")),
		"runbook":     readDocumentationFile(t, filepath.Join(root, "docs", "pipelines.md")),
		"agent-guide": readDocumentationFile(t, filepath.Join(root, ".agents", "skills", "ktx", "references", "benchmarks-development.md")),
	} {
		t.Run(name, func(t *testing.T) {
			assertContractText(t, contents,
				[]string{"returns before task loading"},
				[]string{"loads the task before preflight"},
			)
		})
	}
}

func TestCurrentCacheAwareBenchmarkEntryPointsMatchHelp(t *testing.T) {
	root := repositoryRoot(t)
	var help bytes.Buffer
	printBenchHelp(&help)
	for name, contents := range map[string]string{
		"help":            help.String(),
		"readme":          readDocumentationFile(t, filepath.Join(root, "README.md")),
		"runbook":         readDocumentationFile(t, filepath.Join(root, "docs", "pipelines.md")),
		"agent-guide":     readDocumentationFile(t, filepath.Join(root, "AGENTS.md")),
		"skill-reference": readDocumentationFile(t, filepath.Join(root, ".agents", "skills", "ktx", "references", "benchmarks-development.md")),
	} {
		t.Run(name, func(t *testing.T) {
			assertContractText(t, contents,
				[]string{"--spec prompt-cache-token-hit-rate"},
				[]string{"--methodology "})
		})
	}
	for name, contents := range map[string]string{
		"help":            help.String(),
		"readme":          readDocumentationFile(t, filepath.Join(root, "README.md")),
		"runbook":         readDocumentationFile(t, filepath.Join(root, "docs", "pipelines.md")),
		"agent-guide":     readDocumentationFile(t, filepath.Join(root, "AGENTS.md")),
		"skill-reference": readDocumentationFile(t, filepath.Join(root, ".agents", "skills", "ktx", "references", "benchmarks-development.md")),
	} {
		t.Run(name+"-contract", func(t *testing.T) {
			assertContractText(t, contents,
				[]string{"summary schema 5", "cache-reuse-v2", "provider-internal"}, nil)
		})
	}
	assertContractText(t, help.String(),
		[]string{"--warmup N (default 1), --rounds N (default 5)"}, nil)

	contract := readDocumentationFile(t, filepath.Join(root, "docs", "cache-reuse-metric-contract.md"))
	assertContractText(t, contract,
		[]string{"current machine-readable authority", "contracts/cache-reuse.json", "summary schema 5", "provider-internal claims"},
		[]string{"Future collection, derivation, and\nrendering work must implement that versioned contract"})
}

func TestCurrentPromptCacheTerminologyKeepsChangelogAndTODOAligned(t *testing.T) {
	root := repositoryRoot(t)
	assertContractText(t, readDocumentationFile(t, filepath.Join(root, "CHANGELOG.md")),
		[]string{"methodology v6", "summary schema 5", "cache-reuse-v2"}, nil)
	assertContractText(t, readDocumentationFile(t, filepath.Join(root, "TODO.md")),
		[]string{"Live latency coverage"}, []string{"Live cost and latency coverage"})
}

func TestLiveLatencyTerminologyIgnoresMarkdownDelimiters(t *testing.T) {
	for _, tc := range []struct {
		text  string
		valid bool
	}{
		{"**Live latency coverage**", true},
		{"Live latency coverage", true},
		{"**Live cost and latency coverage**", false},
		{"Live cost and latency coverage", false},
		{"Live latency coverage\n**Live cost and latency coverage**", false},
		{"**Live latency coverage**\nLive cost and latency coverage", false},
		{"unrelated wording", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			issues := contractTextIssues(tc.text, []string{"Live latency coverage"}, []string{"Live cost and latency coverage"})
			if (len(issues) == 0) != tc.valid {
				t.Fatalf("valid=%v issues=%v", tc.valid, issues)
			}
		})
	}
}

func assertContractText(t *testing.T, contents string, required, forbidden []string) {
	t.Helper()
	for _, issue := range contractTextIssues(contents, required, forbidden) {
		t.Error(issue)
	}
}

func contractTextIssues(contents string, required, forbidden []string) []string {
	var issues []string
	for _, text := range required {
		if !strings.Contains(contents, text) {
			issues = append(issues, "contract is missing "+strconv.Quote(text))
		}
	}
	for _, text := range forbidden {
		if strings.Contains(contents, text) {
			issues = append(issues, "contract retains stale wording "+strconv.Quote(text))
		}
	}
	return issues
}
