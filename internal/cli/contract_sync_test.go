package cli

import (
	"bytes"
	"path/filepath"
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

func TestBenchmarkNamespaceCommentListsPublicFamilies(t *testing.T) {
	contents := readDocumentationFile(t, filepath.Join(repositoryRoot(t), "internal", "cli", "bench.go"))
	assertContractText(t, contents,
		[]string{"run, report, export, compare, rank", "validate, agent, evidence, and corpus"},
		[]string{"surface ships two subcommands"},
	)
}

func assertContractText(t *testing.T, contents string, required, forbidden []string) {
	t.Helper()
	for _, text := range required {
		if !strings.Contains(contents, text) {
			t.Errorf("contract is missing %q", text)
		}
	}
	for _, text := range forbidden {
		if strings.Contains(contents, text) {
			t.Errorf("contract retains stale wording %q", text)
		}
	}
}
