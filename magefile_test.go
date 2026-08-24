//go:build mage

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHermeticVerificationEnvironment(t *testing.T) {
	if os.Getenv(hermeticMarkerEnv) != "1" {
		t.Fatal("standard test target did not enter the hermetic boundary")
	}
	if _, present := os.LookupEnv("KTX_DB"); present {
		t.Fatal("KTX_DB is present inside the hermetic boundary")
	}
	root := os.Getenv(hermeticRootEnv)
	if root == "" {
		t.Fatal("disposable root is not declared")
	}
	for _, key := range applicationRootVariables {
		value := os.Getenv(key)
		if value == "" || filepath.Dir(value) != root {
			t.Errorf("%s=%q is not a direct child of disposable root %q", key, value, root)
		}
	}
	if value := os.Getenv(temporaryDirectoryEnv); value == "" || filepath.Dir(value) != root {
		t.Errorf("%s=%q is not a direct child of disposable root %q", temporaryDirectoryEnv, value, root)
	}
	for _, key := range toolCacheVariables {
		value := os.Getenv(key)
		if value == "" {
			t.Errorf("%s is empty", key)
			continue
		}
		if pathWithin(root, value) {
			t.Errorf("%s=%q is inside disposable root %q", key, value, root)
		}
	}
	if audit := os.Getenv(databaseAuditEnv); audit == "" || filepath.Dir(audit) != root {
		t.Errorf("database audit path %q is not owned by disposable root %q", audit, root)
	}
	if capability := os.Getenv(hermeticCapabilityEnv); capability == "" {
		t.Error("reentry capability path is not declared")
	} else if _, err := os.Stat(capability); !os.IsNotExist(err) {
		t.Errorf("single-use reentry capability was not consumed: %v", err)
	}
}

func TestHermeticTargetsUseSingleBoundary(t *testing.T) {
	if got := os.Getenv(hermeticBoundaryIDEnv); got == "" {
		t.Fatal("hermetic boundary has no invocation identifier")
	}
}

func TestGeneratedAdapterGateRequiresBinaryAndBun(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "ktx")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := generatedAdapterGateRequirements(binary); err == nil || !strings.Contains(err.Error(), "bun not found") {
		t.Fatalf("missing Bun result = %v", err)
	}

	bin := t.TempDir()
	bun := "bun"
	if runtime.GOOS == "windows" {
		bun += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, bun), []byte("bun"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	missingBinary := filepath.Join(directory, "missing-ktx")
	if _, err := generatedAdapterGateRequirements(missingBinary); err == nil || !strings.Contains(err.Error(), "built KTX binary is required") {
		t.Fatalf("missing binary result = %v", err)
	}
	if got, err := generatedAdapterGateRequirements(binary); err != nil || got != binary {
		t.Fatalf("valid requirements = %q, %v; want %q", got, err, binary)
	}
}

func TestEngineeringEvidenceDirectoryRequiresNewExplicitPath(t *testing.T) {
	t.Setenv(engineeringEvidenceDirEnv, "")
	if _, err := engineeringEvidenceDirectory(); err == nil {
		t.Fatal("missing evidence directory succeeded")
	}

	directory := filepath.Join(t.TempDir(), "evidence")
	t.Setenv(engineeringEvidenceDirEnv, directory)
	got, err := engineeringEvidenceDirectory()
	if err != nil || got != directory {
		t.Fatalf("engineering evidence directory = %q, %v", got, err)
	}
	if err := createEngineeringEvidenceDirectory(got); err != nil {
		t.Fatal(err)
	}
	if err := createEngineeringEvidenceDirectory(got); err == nil {
		t.Fatal("existing evidence directory accepted")
	}
}

func TestEngineeringReportSummaryRequiresIdentity(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "component.json")
	if err := os.WriteFile(valid, []byte(`{"schema_version":1,"machine":{"go_version":"go"},"workloads":[{"name":"fixed"}],"benchmarks":[{"name":"parser"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	schemaVersion, digest, err := summarizeEngineeringReport(valid, engineeringReportSpecs[0].identityKeys)
	if err != nil || schemaVersion != 1 || len(digest) != 64 {
		t.Fatalf("valid report summary = %d, %q, %v", schemaVersion, digest, err)
	}

	invalid := filepath.Join(directory, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"schema_version":1,"machine":{"go_version":"go"},"workloads":[{"name":"fixed"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := summarizeEngineeringReport(invalid, engineeringReportSpecs[0].identityKeys); err == nil || !strings.Contains(err.Error(), "benchmarks") {
		t.Fatalf("missing identity result = %v", err)
	}

	invalidTiming := filepath.Join(directory, "invalid-timing.json")
	if err := os.WriteFile(invalidTiming, []byte(`{"schema_version":2,"started_at":"2026-08-10T00:00:00.000000002Z","finished_at":"2026-08-10T00:00:00.000000001Z","duration_nanoseconds":0,"machine":{"go_version":"go"},"workloads":[{"name":"fixed"}],"benchmarks":[{"name":"parser"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := summarizeEngineeringReport(invalidTiming, engineeringReportSpecs[0].identityKeys); err == nil || !strings.Contains(err.Error(), "timing") {
		t.Fatalf("invalid timing result = %v", err)
	}

	current := filepath.Join(directory, "current.json")
	if err := os.WriteFile(current, []byte(`{"schema_version":3,"started_at":"2026-08-10T00:00:00.000000001Z","finished_at":"2026-08-10T00:00:00.000000002Z","duration_nanoseconds":1,"machine":{"go_version":"go"},"workloads":[{"name":"fixed"}],"benchmarks":[{"name":"parser"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if schemaVersion, _, err := summarizeEngineeringReport(current, engineeringReportSpecs[0].identityKeys); err != nil || schemaVersion != 3 {
		t.Fatalf("current report summary = %d, %v", schemaVersion, err)
	}
}

func TestEngineeringManifestRetainsReportReferences(t *testing.T) {
	directory := t.TempDir()
	reports := []engineeringEvidenceReport{{Name: "component", MageTarget: "componentBench", File: "component-bench.json", SchemaVersion: 1, SHA256: strings.Repeat("a", 64)}}
	started := mustEngineeringTime(t, "2026-08-10T00:00:00.000000001Z")
	finished := mustEngineeringTime(t, "2026-08-10T00:00:00.000000002Z")
	if err := writeEngineeringEvidenceManifest(directory, reports, started, finished); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, engineeringManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var manifest engineeringEvidenceManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != engineeringManifestSchema || manifest.StartedAt != "2026-08-10T00:00:00.000000001Z" || manifest.FinishedAt != "2026-08-10T00:00:00.000000002Z" || manifest.DurationNanoseconds != 1 || len(manifest.Reports) != 1 || manifest.Reports[0] != reports[0] {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestEngineeringManifestSchemaOneIsPreservedWithoutTiming(t *testing.T) {
	manifest := engineeringEvidenceManifest{SchemaVersion: 1, Reports: []engineeringEvidenceReport{{Name: "component"}}}
	if err := validateEngineeringEvidenceManifest(manifest); err != nil {
		t.Fatal(err)
	}
}

func mustEngineeringTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestHermeticReentryRejectsForgedMarkerBeforeBody(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "body-ran")
	t.Setenv(hermeticMarkerEnv, "1")
	t.Setenv("KTX_DB", filepath.Join(t.TempDir(), "inert.db"))
	err := hermeticTarget("test", func() error { return os.WriteFile(sentinel, []byte("ran"), 0o600) })
	if err == nil || !strings.Contains(err.Error(), "KTX_DB") {
		t.Fatalf("forged reentry error = %v", err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("target body ran before reentry rejection: %v", statErr)
	}
}

func TestHermeticReentryValidationFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, map[string]string)
	}{
		{name: "missing boundary id", mutate: func(t *testing.T, env map[string]string) { env[hermeticBoundaryIDEnv] = "" }},
		{name: "malformed boundary id", mutate: func(t *testing.T, env map[string]string) { env[hermeticBoundaryIDEnv] = "not-hex" }},
		{name: "missing root", mutate: func(t *testing.T, env map[string]string) {
			env[hermeticRootEnv] = filepath.Join(t.TempDir(), "missing")
		}},
		{name: "duplicate app directories", mutate: func(t *testing.T, env map[string]string) { env["XDG_STATE_HOME"] = env["XDG_CACHE_HOME"] }},
		{name: "app directory outside root", mutate: func(t *testing.T, env map[string]string) { env["XDG_DATA_HOME"] = t.TempDir() }},
		{name: "temporary directory outside root", mutate: func(t *testing.T, env map[string]string) { env[temporaryDirectoryEnv] = t.TempDir() }},
		{name: "audit outside root", mutate: func(t *testing.T, env map[string]string) {
			env[databaseAuditEnv] = filepath.Join(t.TempDir(), "audit.jsonl")
		}},
		{name: "malformed forbidden paths", mutate: func(t *testing.T, env map[string]string) { env[forbiddenDBPathsEnv] = "{" }},
		{name: "oversized forbidden paths", mutate: func(t *testing.T, env map[string]string) {
			env[forbiddenDBPathsEnv] = `[` + strings.Repeat(`"x",`, maxForbiddenDatabasePaths) + `"x"]`
		}},
		{name: "empty cache", mutate: func(t *testing.T, env map[string]string) { env["GOCACHE"] = "" }},
		{name: "cache inside root", mutate: func(t *testing.T, env map[string]string) {
			env["GOCACHE"] = filepath.Join(env[hermeticRootEnv], "tool-cache")
		}},
		{name: "missing capability", mutate: func(t *testing.T, env map[string]string) {
			env[hermeticCapabilityEnv] = filepath.Join(env[hermeticRootEnv], "missing-capability")
		}},
		{name: "wrong nonce", mutate: func(t *testing.T, env map[string]string) {
			env[hermeticNonceEnv] = strings.Repeat("0", boundaryNonceHexLength)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validReentryEnvironment(t)
			test.mutate(t, env)
			setTestEnvironment(t, env)
			ran := false
			if err := hermeticTarget("test", func() error { ran = true; return nil }); err == nil {
				t.Fatal("invalid reentry succeeded")
			}
			if ran {
				t.Fatal("target body ran for invalid reentry")
			}
		})
	}
}

func TestHermeticValidParentReentrySucceedsOnce(t *testing.T) {
	env := validReentryEnvironment(t)
	setTestEnvironment(t, env)
	runs := 0
	if err := hermeticTarget("test", func() error { runs++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := hermeticTarget("test", func() error { runs++; return nil }); err == nil {
		t.Fatal("consumed parent capability was accepted twice")
	}
	if runs != 1 {
		t.Fatalf("target body runs = %d, want 1", runs)
	}
}

func TestOuterVerificationEnvironmentStripsIncomingDatabase(t *testing.T) {
	environment := verificationEnvironment([]string{"KTX_DB=" + filepath.Join(t.TempDir(), "inert.db"), "KEEP=value"}, map[string]string{"HOME": t.TempDir()})
	for _, entry := range environment {
		if strings.HasPrefix(entry, "KTX_DB=") {
			t.Fatalf("outer environment retained %q", entry)
		}
	}
}

func validReentryEnvironment(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	env := map[string]string{
		hermeticMarkerEnv: "1", hermeticRootEnv: root, hermeticBoundaryIDEnv: "0123456789abcdef",
		databaseAuditEnv: filepath.Join(root, "database-access.jsonl"), forbiddenDBPathsEnv: "[]",
	}
	for index, name := range applicationRootVariables {
		path := filepath.Join(root, strings.ToLower(strings.TrimPrefix(name, "XDG_"))+string(rune('a'+index)))
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		env[name] = path
	}
	temporaryDirectory := filepath.Join(root, "tmp")
	if err := os.Mkdir(temporaryDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	env[temporaryDirectoryEnv] = temporaryDirectory
	for _, name := range toolCacheVariables {
		env[name] = filepath.Join(t.TempDir(), strings.ToLower(name))
	}
	capabilityPath, nonce, err := createReentryCapability(root, env[hermeticBoundaryIDEnv])
	if err != nil {
		t.Fatal(err)
	}
	env[hermeticCapabilityEnv], env[hermeticNonceEnv] = capabilityPath, nonce
	return env
}

func setTestEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	t.Setenv("KTX_DB", "")
	if err := os.Unsetenv("KTX_DB"); err != nil {
		t.Fatal(err)
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	for _, name := range []string{hermeticMarkerEnv, hermeticRootEnv, hermeticBoundaryIDEnv, hermeticCapabilityEnv, hermeticNonceEnv, databaseAuditEnv, forbiddenDBPathsEnv, temporaryDirectoryEnv} {
		if _, exists := values[name]; !exists {
			t.Setenv(name, "")
		}
	}
}
