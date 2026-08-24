package cli

import (
	"bytes"
	"io"
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
		filepath.Join("docs", "database-safety.md"),
		filepath.Join("docs", "binary-capabilities.md"),
		filepath.Join("docs", "opencode-telemetry.md"),
	} {
		contents := readDocumentationFile(t, filepath.Join(root, relative))
		if !strings.Contains(contents, "--expect-runtime-sha256") {
			t.Errorf("%s does not document per-invocation ambient runtime proof", relative)
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

func TestSchemaReadinessContractSurfacesStayConsistent(t *testing.T) {
	root := repositoryRoot(t)
	canonical := strings.ToLower(readDocumentationFile(t, filepath.Join(root, "docs", "schema-readiness.md")))
	for _, required := range []string{
		"preparation is automatic for every supported older schema",
		"former rule",
		"schema-migration-required",
		"notes/schema-readiness-baseline.json",
		"tools/schema_readiness_evidence.py",
		"runtime-startup-timeout",
		"operation-timeout",
		"migration-timeout",
	} {
		if !strings.Contains(canonical, required) {
			t.Errorf("schema readiness contract is missing %q", required)
		}
	}
	for _, relative := range []string{
		filepath.Join("docs", "database-safety.md"),
		"CHANGELOG.md",
	} {
		contents := strings.ToLower(readDocumentationFile(t, filepath.Join(root, relative)))
		for _, obsolete := range []string{
			"read-only opens never create, migrate",
			"read-only evaluation does not migrate",
		} {
			if strings.Contains(contents, obsolete) {
				t.Errorf("%s retains unqualified read-only guidance %q", relative, obsolete)
			}
		}
	}

	surfaces := []string{
		"README.md",
		"AGENTS.md",
		filepath.Join(".agents", "skills", "ktx", "SKILL.md"),
		filepath.Join(".agents", "skills", "ktx", "references", "database-telemetry.md"),
		filepath.Join(".agents", "skills", "ktx", "references", "cli.md"),
		filepath.Join("docs", "opencode-telemetry.md"),
		filepath.Join("docs", "critical-path-performance.md"),
		filepath.Join("docs", "pipelines.md"),
	}
	for _, relative := range surfaces {
		contents := strings.ToLower(readDocumentationFile(t, filepath.Join(root, relative)))
		for _, obsolete := range []string{
			"schema-migration-required",
			"run `ktx db migrate` when an older",
			"query commands are genuinely read-only and never migrate",
			"doctor diagnoses but never creates, migrates",
			"commits the complete migration or rolls back without changing schema version or evidence",
		} {
			if strings.Contains(contents, obsolete) {
				t.Errorf("%s retains stale readiness guidance %q", relative, obsolete)
			}
		}
	}

	helpSurfaces := []struct {
		name     string
		render   func(io.Writer)
		required []string
		obsolete []string
	}{
		{
			name:     "doctor",
			render:   printDoctorHelp,
			required: []string{"Automatically prepares a supported older schema", "read-only after readiness"},
			obsolete: []string{"Doctor is read-only. It does not create", "does not migrate", "deliberately use an Open-backed command"},
		},
		{
			name:     "stats live-validation",
			render:   printStatsLiveValidationHelp,
			required: []string{"After automatic readiness", "read-only"},
			obsolete: []string{"read-only and does not create, migrate, or update persistent state"},
		},
	}
	for _, surface := range helpSurfaces {
		var output bytes.Buffer
		surface.render(&output)
		for _, required := range surface.required {
			if !strings.Contains(output.String(), required) {
				t.Errorf("%s help is missing %q", surface.name, required)
			}
		}
		for _, obsolete := range surface.obsolete {
			if strings.Contains(output.String(), obsolete) {
				t.Errorf("%s help retains stale readiness guidance %q", surface.name, obsolete)
			}
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
		"executable_path",
		"executable_sha256",
		"failure_class",
		"coordinator-timeout",
		"sqlite-lock-exhaustion",
		"operation_phase",
		"elapsed_wait_nanoseconds",
		"opencode-hook",
		"opencode-telemetry",
		"correction",
		"ktx_verify_root",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("critical-path contract is missing %q", required)
		}
	}
}

func TestEngineeringVerificationDocumentationSurfacesAgree(t *testing.T) {
	root := repositoryRoot(t)
	for _, surface := range []struct {
		path     string
		required []string
	}{
		{
			path: "README.md",
			required: []string{
				"mage engineeringVerify",
				"correctness, exact reconciliation, and supported-lock",
				"does not add a timing gate",
			},
		},
		{
			path: "CHANGELOG.md",
			required: []string{
				"mage engineeringVerify",
				"hash manifest",
				"supported-lock checks",
				"timing descriptive",
			},
		},
		{
			path: filepath.Join(".agents", "skills", "ktx", "references", "benchmarks-development.md"),
			required: []string{
				"KTX_ENGINEERING_EVIDENCE_DIR=<new-directory> mage engineeringVerify",
				"hash manifest",
				"supported-lock checks",
			},
		},
	} {
		contents := strings.ReplaceAll(readDocumentationFile(t, filepath.Join(root, surface.path)), "\n", " ")
		for _, required := range surface.required {
			if !strings.Contains(contents, required) {
				t.Errorf("%s is missing %q", surface.path, required)
			}
		}
	}
}

func TestSchemaReleaseGateIsDiscoverable(t *testing.T) {
	root := repositoryRoot(t)
	for _, relative := range []string{
		"README.md",
		"AGENTS.md",
		filepath.Join("docs", "schema-readiness.md"),
		filepath.Join(".agents", "skills", "ktx", "references", "benchmarks-development.md"),
	} {
		contents := readDocumentationFile(t, filepath.Join(root, relative))
		for _, required := range []string{
			"KTX_SCHEMA_RELEASE_WORK_ROOT",
			"KTX_SCHEMA_RELEASE_REPORT",
			"mage schemaReleaseGate",
			"3.2 GB",
			"attempt.failed.json",
		} {
			if !strings.Contains(contents, required) {
				t.Errorf("%s is missing schema release guidance %q", relative, required)
			}
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
