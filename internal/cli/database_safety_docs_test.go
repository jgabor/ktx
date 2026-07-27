package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestDatabaseSafetyGuidanceContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	contract := readGuidanceFile(t, filepath.Join(root, "docs", "database-safety.md"))

	for _, required := range []string{
		"DB-free",
		"Genuinely read-only",
		"Creation and existing-upgrade capable",
		"Forbidden ambient-live development access",
		"removes `KTX_DB`",
		"HOME=",
		"XDG_DATA_HOME=",
		"XDG_CONFIG_HOME=",
		"XDG_CACHE_HOME=",
		"XDG_STATE_HOME=",
		"GOCACHE",
		"GOMODCACHE",
		"BUN_INSTALL_CACHE_DIR",
		"GOLANGCI_LINT_CACHE",
		"verification-only database access audit",
		"forbidden=0",
		"seeded temporary database",
		"Do not install yet",
		"effective installed binary",
		"Do not write the database",
		"Do not lower `PRAGMA user_version`",
		"doctor",
		"never creates or migrates",
	} {
		if !strings.Contains(contract, required) {
			t.Errorf("database safety contract missing %q", required)
		}
	}
	for _, target := range []string{"`mage test`", "`mage check`", "`mage verify`"} {
		if !strings.Contains(contract, target) {
			t.Errorf("database safety contract missing self-hermetic target %q", target)
		}
	}

	for _, path := range []string{"AGENTS.md", "README.md", filepath.Join(".github", "workflows", "ci.yml")} {
		content := readGuidanceFile(t, filepath.Join(root, path))
		if !strings.Contains(content, "docs/database-safety.md") {
			t.Errorf("%s does not point to the canonical database safety contract", path)
		}
	}

	routing := readGuidanceFile(t, filepath.Join(root, "docs", "routing-candidate-characterization.md"))
	for _, forbidden := range []string{
		`build/ktx db path`,
		"sqlite3 -readonly \"$(",
		"\ngo test ./internal/router",
		"\nmage check",
	} {
		if strings.Contains(routing, forbidden) {
			t.Errorf("routing characterization retains unsafe active procedure %q", forbidden)
		}
	}
	for _, required := range []string{
		"non-live",
		"mktemp -d",
		"sqlite3 -readonly \"$root/routing-characterization.db\"",
		"ktx_verify go test ./internal/router",
		"ktx_verify mage check",
	} {
		if !strings.Contains(routing, required) {
			t.Errorf("routing characterization missing safe procedure %q", required)
		}
	}
	if !regexp.MustCompile(`sanitized\s+(?:non-live\s+)?snapshot`).MatchString(routing) {
		t.Error("routing characterization does not require a sanitized snapshot")
	}

	ambientSQLite := regexp.MustCompile(`(?s)sqlite3\s+-readonly\s+"\$\([^\n]*\bdb\s+path\b`)
	docs, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatalf("list active documentation: %v", err)
	}
	for _, path := range docs {
		if ambientSQLite.MatchString(readGuidanceFile(t, path)) {
			t.Errorf("%s derives a read-only SQLite target from ambient db path resolution", path)
		}
	}
}

func TestDBDispatchCommentClassifiesOwnedOpen(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	dbSource := readGuidanceFile(t, filepath.Join(filepath.Dir(source), "db.go"))
	match := regexp.MustCompile(`(?s)(// dbCommandFunc.*?)\nfunc dbCommandFunc`).FindStringSubmatch(dbSource)
	if match == nil {
		t.Fatal("dbCommandFunc documentation comment not found")
	}
	doc := regexp.MustCompile(`(?m)^// ?`).ReplaceAllString(match[1], "")
	doc = strings.Join(strings.Fields(doc), " ")

	for _, required := range []string{
		"NeedsDB=false skips only the dispatcher-managed database open",
		"`db path` and unavailable `db prune` are DB-free",
		"`db report` owns its genuinely read-only open",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("dbCommandFunc documentation missing classification %q", required)
		}
	}

	blanketDBFree := regexp.MustCompile(`(?i)\b(?:ktx db|(?:the|this|database|db) namespace)\b[^.]*\b(?:(?:is|remains)\s+(?:entirely\s+)?DB-free|(?:never|does not|doesn't|cannot|can't)[^.]*\b(?:database|DB))\b`)
	if contradiction := blanketDBFree.FindString(doc); contradiction != "" {
		t.Errorf("dbCommandFunc documentation misclassifies the namespace as DB-free: %q", contradiction)
	}
}

func readGuidanceFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
