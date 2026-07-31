package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var expectedSkillFiles = []string{
	"SKILL.md",
	filepath.Join("references", "benchmarks-development.md"),
	filepath.Join("references", "cli.md"),
	filepath.Join("references", "database-telemetry.md"),
	filepath.Join("references", "pipelines.md"),
}

func TestKTXSkillLayout(t *testing.T) {
	root := ktxSkillRoot(t)
	if issues := validateSkillLayout(root); len(issues) != 0 {
		t.Fatalf("valid skill layout rejected: %s", strings.Join(issues, "; "))
	}

	t.Run("rejects invalid metadata", func(t *testing.T) {
		fixture := copySkillFixture(t, root)
		path := filepath.Join(fixture, "SKILL.md")
		contents := readDocumentationFile(t, path)
		writeTestFile(t, path, strings.Replace(contents, "name: ktx", "name: wrong", 1))
		if issues := validateSkillLayout(fixture); len(issues) == 0 {
			t.Fatal("invalid skill name was accepted")
		}
	})

	t.Run("rejects malformed YAML with expected strings", func(t *testing.T) {
		fixture := copySkillFixture(t, root)
		path := filepath.Join(fixture, "SKILL.md")
		contents := readDocumentationFile(t, path)
		malformed := strings.Replace(contents, "\n---\n\n# ktx", "\ninvalid: [\n---\n\n# ktx", 1)
		if malformed == contents {
			t.Fatal("test fixture did not alter frontmatter")
		}
		for _, legacyRequired := range []string{
			"\nname: ktx\n", "description: >-", "github.com/jgabor/ktx",
			"Load before searching implementation source.", "Do not use for Android/Kotlin", "Khronos KTX texture files.",
		} {
			if !strings.Contains(malformed, legacyRequired) {
				t.Fatalf("malformed fixture lost legacy string check %q", legacyRequired)
			}
		}
		writeTestFile(t, path, malformed)
		if issues := validateSkillLayout(fixture); len(issues) == 0 {
			t.Fatal("syntactically malformed frontmatter was accepted")
		}
	})

	t.Run("rejects a missing reference", func(t *testing.T) {
		fixture := copySkillFixture(t, root)
		if err := os.Remove(filepath.Join(fixture, "references", "cli.md")); err != nil {
			t.Fatal(err)
		}
		if issues := validateSkillLayout(fixture); len(issues) == 0 {
			t.Fatal("missing reference was accepted")
		}
	})
}

func TestKTXSkillLocalLinks(t *testing.T) {
	root := ktxSkillRoot(t)
	if issues := validateLocalMarkdownLinks(root); len(issues) != 0 {
		t.Fatalf("valid local links rejected: %s", strings.Join(issues, "; "))
	}

	fixture := copySkillFixture(t, root)
	path := filepath.Join(fixture, "SKILL.md")
	writeTestFile(t, path, readDocumentationFile(t, path)+"\n[missing](references/missing.md)\n")
	if issues := validateLocalMarkdownLinks(fixture); len(issues) == 0 {
		t.Fatal("broken local Markdown link was accepted")
	}
}

func TestKTXSkillFactualContracts(t *testing.T) {
	files := readSkillFiles(t, ktxSkillRoot(t))
	if failures := validateSkillFactualContracts(files); len(failures) != 0 {
		t.Fatalf("valid skill contracts rejected: %v", failures)
	}
	boundaryFiles := cloneStrings(files)
	validatorPath := filepath.Join("internal", "cli", "skill_contract_test.go")
	boundaryFiles[validatorPath] = readDocumentationFile(t, filepath.Join(repositoryRoot(t), validatorPath))
	if issues := validateSkillProjectMemoryBoundary(boundaryFiles); len(issues) != 0 {
		t.Fatalf("valid boundary sources rejected: %v", issues)
	}

	tests := []struct {
		name     string
		contract string
		claim    string
	}{
		{name: "authority", contract: "authority", claim: "The focused references are canonical."},
		{name: "benchmark effects", contract: "benchmark effects", claim: "`ktx bench run <task>` is the complete live benchmark interface."},
		{name: "evidence limits", contract: "evidence limits", claim: "Direct SQL proves collector lifecycle or requested-window coverage."},
		{name: "runtime boundaries", contract: "runtime boundaries", claim: "Child stdout passes through unchanged."},
		{name: "database ownership", contract: "database ownership", claim: "Use `sqlite3 -readonly \"$(ktx db path)\"` during development."},
	}
	for _, test := range tests {
		t.Run("rejects "+test.name+" drift", func(t *testing.T) {
			mutated := cloneStrings(files)
			mutated["SKILL.md"] += "\n" + test.claim + "\n"
			failures := validateSkillFactualContracts(mutated)
			if _, ok := failures[test.contract]; !ok {
				t.Fatalf("drift did not fail %q contract: %v", test.contract, failures)
			}
		})
	}

	t.Run("rejects project-memory path markers", func(t *testing.T) {
		mutated := cloneStrings(files)
		mutated["SKILL.md"] += "\nConsult `" + forbiddenPathExample() + "`.\n"
		failures := validateSkillFactualContracts(mutated)
		if _, ok := failures["project-memory boundary"]; !ok {
			t.Fatalf("project-memory path marker was accepted: %v", failures)
		}
	})

	t.Run("rejects project-tracking markers", func(t *testing.T) {
		mutated := cloneStrings(files)
		mutated["SKILL.md"] += "\nFollow " + forbiddenTrackingExample() + ".\n"
		failures := validateSkillFactualContracts(mutated)
		if _, ok := failures["project-memory boundary"]; !ok {
			t.Fatalf("project-tracking marker was accepted: %v", failures)
		}
	})

	t.Run("allows public authority references", func(t *testing.T) {
		mutated := cloneStrings(files)
		mutated["SKILL.md"] += "\nUse `AGENTS.md`, `ARCHITECTURE.md`, `TODO.md`, and `README.md`.\n"
		if issues := validateSkillProjectMemoryBoundary(mutated); len(issues) != 0 {
			t.Fatalf("public authority references were rejected: %v", issues)
		}
	})

	t.Run("rejects generated marker in validator source", func(t *testing.T) {
		mutated := cloneStrings(boundaryFiles)
		mutated[validatorPath] += "\n" + forbiddenTrackingExample() + "\n"
		if issues := validateSkillProjectMemoryBoundary(mutated); len(issues) == 0 {
			t.Fatal("generated validator-source marker was accepted")
		}
	})
}

func TestKTXSkillExamplesMatchCLIHelp(t *testing.T) {
	files := readSkillFiles(t, ktxSkillRoot(t))
	if issues := validateDocumentedCLIExamples(files); len(issues) != 0 {
		t.Fatalf("documented CLI examples rejected: %s", strings.Join(issues, "; "))
	}

	mutated := cloneStrings(files)
	path := filepath.Join("references", "benchmarks-development.md")
	mutated[path] = strings.Replace(mutated[path],
		"ktx bench run <agent> <model> <task>",
		"ktx bench run <task>", 1)
	if issues := validateDocumentedCLIExamples(mutated); len(issues) == 0 {
		t.Fatal("benchmark required-argument drift was accepted")
	}
}

func validateSkillLayout(root string) []string {
	want := append([]string(nil), expectedSkillFiles...)
	sort.Strings(want)
	var got, issues []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			got = append(got, relative)
		}
		return nil
	})
	if err != nil {
		return []string{err.Error()}
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		issues = append(issues, fmt.Sprintf("skill Markdown files = %v, want %v", got, want))
	}

	entryPath := filepath.Join(root, "SKILL.md")
	contents, err := os.ReadFile(entryPath)
	if err != nil {
		return append(issues, fmt.Sprintf("read SKILL.md: %v", err))
	}
	frontmatter, ok := markdownFrontmatter(string(contents))
	if !ok {
		return append(issues, "SKILL.md has no valid frontmatter block")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &metadata); err != nil {
		return append(issues, fmt.Sprintf("SKILL.md frontmatter is invalid YAML: %v", err))
	}
	if metadata.Name != "ktx" {
		issues = append(issues, fmt.Sprintf("SKILL.md name = %q, want %q", metadata.Name, "ktx"))
	}
	description := strings.Join(strings.Fields(metadata.Description), " ")
	if description == "" {
		issues = append(issues, "SKILL.md description must not be empty")
	}
	for _, required := range []string{
		"github.com/jgabor/ktx",
		"Load before searching implementation source.",
		"Do not use for Android/Kotlin",
		"Khronos KTX texture files.",
	} {
		if !strings.Contains(description, required) {
			issues = append(issues, "SKILL.md description is missing "+fmt.Sprintf("%q", required))
		}
	}
	return issues
}

func validateLocalMarkdownLinks(root string) []string {
	linkPattern := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`)
	var issues []string
	for relative, contents := range readSkillFilesFromDisk(root) {
		for _, match := range linkPattern.FindAllStringSubmatch(contents, -1) {
			target := strings.SplitN(match[1], "#", 2)[0]
			if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			resolved := filepath.Clean(filepath.Join(root, filepath.Dir(relative), filepath.FromSlash(target)))
			if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
				issues = append(issues, fmt.Sprintf("%s link %q does not resolve to a file", relative, match[1]))
			}
		}
	}
	return issues
}

func validateSkillFactualContracts(files map[string]string) map[string][]string {
	contents := strings.Join(strings.Fields(strings.Join(mapValues(files), "\n")), " ")
	rules := []struct {
		name                string
		required, forbidden []string
	}{
		{
			name:      "authority",
			required:  []string{"derived task summaries", "Project memory owns intent", "Implementation and tests establish current behavior"},
			forbidden: []string{"references are canonical", "focused references are canonical"},
		},
		{
			name:      "benchmark effects",
			required:  []string{"<agent> <model> <task>", "mutates Docker image/build-cache state", "network to fetch base-image and dependency inputs", "no model/provider calls", "provider charges", "writes generated corpus files", "rewrites derived `summary.json`"},
			forbidden: []string{"`ktx bench run <task>` is the complete live benchmark interface"},
		},
		{
			name:      "evidence limits",
			required:  []string{"cannot prove collector lifecycle or requested-window coverage", "non-transactional views", "not exact reproduction", "Do not infer provider behavior", "causally attribute movement to compression"},
			forbidden: []string{"Direct SQL proves collector lifecycle or requested-window coverage"},
		},
		{
			name:      "runtime boundaries",
			required:  []string{"does not cancel or sandbox stage execution", "continue in the background", "not byte-exact passthrough", "modify the active plugin configuration"},
			forbidden: []string{"Child stdout passes through unchanged"},
		},
		{
			name:      "database ownership",
			required:  []string{"KTX_OWNED_DB", "KTX_READONLY_DB", "preconfirmed", "existing risk-bearing example", "not a generally safe starting point", "or a policy change"},
			forbidden: []string{`sqlite3 -readonly "$(ktx db path)"`, "## Recommended Starting Point"},
		},
	}
	failures := make(map[string][]string)
	for _, rule := range rules {
		if issues := contractTextIssues(contents, rule.required, rule.forbidden); len(issues) != 0 {
			failures[rule.name] = issues
		}
	}
	if issues := validateSkillProjectMemoryBoundary(files); len(issues) != 0 {
		failures["project-memory boundary"] = issues
	}
	return failures
}

func validateSkillProjectMemoryBoundary(files map[string]string) []string {
	wordBoundary := `\b`
	digits := `[0-9]+`
	patterns := []struct {
		name    string
		pattern string
	}{
		{name: "project-memory path", pattern: `(?i)\.` + "agen" + "tera/" + `(?:decisions|progress|plan|health|vision|objective|experiments|docs)\.yaml`},
		{name: "decision ID", pattern: wordBoundary + "D" + digits + wordBoundary},
		{name: "expanded decision ID", pattern: wordBoundary + "Dec" + "ision " + digits + wordBoundary},
		{name: "milestone ID", pattern: wordBoundary + "M" + `[1-7]` + wordBoundary + `|` + wordBoundary + "Mile" + "stone " + `[1-7]` + wordBoundary},
		{name: "project phase", pattern: wordBoundary + "Pha" + "se " + `[1-3]` + wordBoundary},
		{name: "acceptance-criteria ID", pattern: wordBoundary + "A" + "C" + `[1-9]` + wordBoundary + `|(?i:` + wordBoundary + "acceptance " + "criteria " + `[1-9]` + wordBoundary + `)`},
		{name: "plan task reference", pattern: `(?i:` + wordBoundary + "plan " + "Task " + digits + wordBoundary + `)|` + wordBoundary + "Ta" + "sk " + digits + wordBoundary},
		{name: "decision-mode label", pattern: `(?i:` + wordBoundary + `(?:firm|provisional|exploratory) ` + "dec" + "ision" + wordBoundary + `)`},
		{name: "migration-vintage label", pattern: `(?i:` + wordBoundary + `pre-(?:M` + digits + `|D` + digits + `) ` + "data" + "base" + wordBoundary + `)`},
		{name: "internal system name", pattern: `(?i:` + wordBoundary + "agen" + "tera" + wordBoundary + `)`},
	}

	var issues []string
	for relative, contents := range files {
		for _, candidate := range patterns {
			if regexp.MustCompile(candidate.pattern).FindString(contents) != "" {
				issues = append(issues, fmt.Sprintf("%s contains forbidden %s", relative, candidate.name))
			}
		}
	}
	sort.Strings(issues)
	return issues
}

func forbiddenPathExample() string {
	return strings.Join([]string{".", "agen", "tera", "/", "vision", ".yaml"}, "")
}

func forbiddenTrackingExample() string {
	return strings.Join([]string{"Dec", "ision", " ", "12"}, "")
}

func validateDocumentedCLIExamples(files map[string]string) []string {
	benchDoc := files[filepath.Join("references", "benchmarks-development.md")]
	cliDoc := files[filepath.Join("references", "cli.md")]
	var issues []string
	issues = append(issues, contractTextIssues(benchDoc, []string{
		"ktx bench run <agent> <model> <task>",
		"ktx bench validate --experiment <directory>",
		"ktx bench evidence preflight --spec <path> [--output <path>]",
		"ktx bench corpus generate [--output <directory>]",
	}, nil)...)
	issues = append(issues, contractTextIssues(cliDoc, []string{
		"ktx stats summary --window 30m --format json",
		"- `0`: success.",
		"- `1`: usage or input contract error.",
		"- `2`: not found where applicable.",
		"- `4`: internal, integrity, verification, or hard health failure.",
	}, nil)...)

	for _, check := range []struct {
		name     string
		args     []string
		required []string
	}{
		{name: "bench", args: []string{"bench", "--help"}, required: []string{
			"run <agent> <model> <task>",
			"validate --experiment <directory>",
			"evidence preflight --spec <path> [--output <path>]",
			"corpus generate [--output <directory>]",
			"0 success", "1 usage/contract error", "2 not found", "4 internal/verification failure",
		}},
		{name: "stats summary", args: []string{"stats", "summary", "--help"}, required: []string{
			"Usage: ktx stats summary [--window DURATION] [--format text|json]",
			"--window DURATION positive Go duration",
		}},
	} {
		var stdout, stderr bytes.Buffer
		if code := runWith(check.args, &stdout, &stderr); code != ExitOK {
			issues = append(issues, fmt.Sprintf("%s help exit = %d, want %d: %s", check.name, code, ExitOK, stderr.String()))
			continue
		}
		help := strings.Join(strings.Fields(stdout.String()), " ")
		for _, required := range check.required {
			if !strings.Contains(help, required) {
				issues = append(issues, fmt.Sprintf("%s help is missing %q", check.name, required))
			}
		}
	}
	return issues
}

func markdownFrontmatter(contents string) (string, bool) {
	if !strings.HasPrefix(contents, "---\n") {
		return "", false
	}
	end := strings.Index(contents[4:], "\n---\n")
	if end < 0 {
		return "", false
	}
	return "\n" + contents[4:4+end] + "\n", true
}

func ktxSkillRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(repositoryRoot(t), ".agents", "skills", "ktx")
}

func readSkillFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := readSkillFilesFromDisk(root)
	if len(files) != len(expectedSkillFiles) {
		t.Fatalf("read %d skill files, want %d", len(files), len(expectedSkillFiles))
	}
	return files
}

func readSkillFilesFromDisk(root string) map[string]string {
	files := make(map[string]string, len(expectedSkillFiles))
	for _, relative := range expectedSkillFiles {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if err == nil {
			files[relative] = string(contents)
		}
	}
	return files
}

func copySkillFixture(t *testing.T, source string) string {
	t.Helper()
	target := t.TempDir()
	for relative, contents := range readSkillFiles(t, source) {
		path := filepath.Join(target, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, path, contents)
	}
	return target
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cloneStrings(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func mapValues(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}
