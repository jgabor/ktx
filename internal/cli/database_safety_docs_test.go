package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseSafetyDocumentationMatchesCommandContract(t *testing.T) {
	root := repositoryRoot(t)
	canonical := readDocumentationFile(t, filepath.Join(root, "docs", "database-safety.md"))
	lower := strings.ToLower(canonical)
	for _, required := range []string{
		"genuinely read-only",
		"`get`, `read`, `list`, `stats`, `decompress`",
		"`bench report`",
		"`telemetry list`",
		"`ktx db migrate`",
		"mode=ro",
		"query_only",
		"transaction",
		"ambient live database",
		"never lower",
		"`pragma user_version`",
		"`mage test`",
		"`mage check`",
		"`mage verify`",
		"gocache",
		"gomodcache",
		"bun_install_cache_dir",
		"golangci_lint_cache",
		"forbidden=0",
		"seeded temporary database",
		"tmpdir",
		"ktx_verify",
		"database paths outside the disposable root",
	} {
		if !strings.Contains(lower, strings.ToLower(required)) {
			t.Errorf("database safety documentation is missing %q", required)
		}
	}

	for _, relative := range []string{
		"README.md",
		"AGENTS.md",
		filepath.Join("docs", "database-safety.md"),
		filepath.Join("docs", "binary-capabilities.md"),
		filepath.Join("docs", "opencode-telemetry.md"),
		filepath.Join("docs", "pipelines.md"),
	} {
		contents := readDocumentationFile(t, filepath.Join(root, relative))
		for _, obsolete := range []string{"--expect-runtime-sha256", "runtime-proof-required", "runtime-proof-mismatch"} {
			if strings.Contains(contents, obsolete) {
				t.Errorf("%s still publishes obsolete runtime proof %q", relative, obsolete)
			}
		}
	}
	for _, relative := range []string{"README.md", "AGENTS.md", filepath.Join(".github", "workflows", "ci.yml")} {
		contents := readDocumentationFile(t, filepath.Join(root, relative))
		if !strings.Contains(contents, "docs/database-safety.md") {
			t.Errorf("%s does not link to the canonical database safety contract", relative)
		}
	}

	routing := readDocumentationFile(t, filepath.Join(root, "docs", "routing-candidate-characterization.md"))
	for _, forbidden := range []string{"build/ktx db path", `sqlite3 -readonly "$("`} {
		if strings.Contains(routing, forbidden) {
			t.Errorf("routing characterization retains unsafe active procedure %q", forbidden)
		}
	}
	for _, required := range []string{"non-live", "mktemp -d", "ktx_verify go test ./internal/router", "ktx_verify mage check"} {
		if !strings.Contains(routing, required) {
			t.Errorf("routing characterization is missing safe procedure %q", required)
		}
	}
}

func TestCriticalPathPerformanceContractDefinesDistinctBoundaries(t *testing.T) {
	contents := strings.ToLower(strings.ReplaceAll(readDocumentationFile(t, filepath.Join(repositoryRoot(t), "docs", "critical-path-performance.md")), "\n", " "))
	for _, required := range []string{
		"component ceilings",
		"real-process latency",
		"product cost and quality",
		"concurrency reliability",
		"durable engineering verification",
		"mage engineeringverify",
		"ktx_engineering_evidence_dir",
		"manifest.json",
		"not a timing gate",
		"not part of `mage verify`",
		"supported writers",
		"supported read-only commands",
		"compatible migrations",
		"mixed-version binaries",
		"foreign sqlite clients",
		"source acceptance",
		"artifact acceptance",
		"disposition acceptance",
		"pre-source failure is unaccepted",
		"no durable spool",
		"no zero-loss claim",
		"no progress guarantee while a foreign sqlite client holds a lock",
		"indefinitely",
		"durable",
		"retry-safe",
		"database-unavailable",
		"process exit `4`",
		"database_path",
		"correction",
		"ktx_verify_root",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("critical-path contract is missing %q", required)
		}
	}
}

func TestBinaryCapabilitiesDocumentedAsDiagnosticOnly(t *testing.T) {
	contents := readDocumentationFile(t, filepath.Join(repositoryRoot(t), "docs", "binary-capabilities.md"))
	for _, required := range []string{"diagnostic", "does not authorize database access"} {
		if !strings.Contains(contents, required) {
			t.Errorf("binary capability documentation is missing %q", required)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
}

func readDocumentationFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
