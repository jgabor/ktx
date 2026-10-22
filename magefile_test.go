//go:build mage

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type testExitError int

func (e testExitError) Error() string { return fmt.Sprintf("exit %d", e) }
func (e testExitError) ExitCode() int { return int(e) }

func TestDiscoverInstalledAdapters(t *testing.T) {
	runner := func(_ string, args ...string) (string, string, error) {
		runtimeName := args[3]
		switch runtimeName {
		case "opencode":
			return `{"installed":true,"path":"/project/ktx.ts","candidate_locations":{"global":"/global/ktx.ts","project":"/project/ktx.ts"}}`, "", nil
		}
		return `{"installed":true,"scopes":["global","project"]}`, "", nil
	}
	got, err := discoverInstalledAdapters("./build/ktx", runner)
	if err != nil {
		t.Fatal(err)
	}
	if got != (adapterScopes{opencodeProject: true, codexGlobal: true, codexProject: true}) {
		t.Fatalf("scopes = %+v", got)
	}

	var inspected []string
	got, err = discoverInstalledAdapters("./build/ktx", func(_ string, args ...string) (string, string, error) {
		inspected = append(inspected, strings.Join(args, " "))
		if args[3] == "codex" {
			return `{"installed":true,"scopes":["global"]}`, "", nil
		}
		return "", "not installed", testExitError(2)
	})
	if err != nil || got != (adapterScopes{codexGlobal: true}) {
		t.Fatalf("codex only scopes = %+v, error = %v", got, err)
	}
	if want := []string{"--format json init opencode --show", "--format json init codex --show"}; !reflect.DeepEqual(inspected, want) {
		t.Fatalf("inspected = %q, want %q", inspected, want)
	}

	_, err = discoverInstalledAdapters("./build/ktx", func(_ string, _ ...string) (string, string, error) {
		return "", "broken status", errors.New("failed")
	})
	if err == nil || !strings.Contains(err.Error(), "before install") {
		t.Fatalf("discovery failure = %v", err)
	}
}

func TestDiscoverInstalledAdaptersTreatsNotFoundAsAbsent(t *testing.T) {
	got, err := discoverInstalledAdapters("./build/ktx", func(_ string, _ ...string) (string, string, error) {
		return "", "not installed", testExitError(2)
	})
	if err != nil || got != (adapterScopes{}) {
		t.Fatalf("scopes = %+v, error = %v", got, err)
	}
}

func TestRefreshInstalledAdapters(t *testing.T) {
	var calls []string
	runner := func(name string, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return "", "", nil
	}
	var output strings.Builder
	err := refreshInstalledAdapters("/bin/ktx", adapterScopes{opencodeGlobal: true, codexProject: true}, runner, &output)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(calls, "\n"), "/bin/ktx init opencode\n/bin/ktx init codex --project"; got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	for _, required := range []string{"Restart OpenCode", "Restart Codex", "/hooks"} {
		if !strings.Contains(output.String(), required) {
			t.Errorf("success output lacks %q: %s", required, output.String())
		}
	}
}

// fakeClaudeCLI answers the claude plugin commands useClaudeMarketplace runs
// from listings in memory and records every call that changes them.
type fakeClaudeCLI struct {
	marketplace *claudeMarketplace
	installed   bool
	failOn      string
	changes     []string
}

func (f *fakeClaudeCLI) run(name string, args ...string) (string, string, error) {
	if name != "/bin/claude" {
		return "", "", fmt.Errorf("ran %s, not claude", name)
	}
	call := strings.Join(args, " ")
	switch call {
	case "plugin marketplace list --json":
		data, _ := json.Marshal(append([]claudeMarketplace{{Name: "other", Source: "github", Repo: "someone/other"}}, f.marketplaceList()...))
		return string(data), "", nil
	case "plugin list --json":
		if f.installed {
			return `[{"id":"other@other"},{"id":"ktx@ktx"}]`, "", nil
		}
		return `[{"id":"other@other"}]`, "", nil
	}
	f.changes = append(f.changes, call)
	if call == f.failOn {
		return "", "network unreachable", errors.New("exit 1")
	}
	switch {
	case call == "plugin uninstall ktx@ktx":
		f.installed = false
	case call == "plugin marketplace remove ktx":
		f.marketplace = nil
	case call == "plugin marketplace add jgabor/ktx":
		f.marketplace = &claudeMarketplace{Name: "ktx", Source: "github", Repo: "jgabor/ktx"}
	case strings.HasPrefix(call, "plugin marketplace add /"):
		f.marketplace = &claudeMarketplace{Name: "ktx", Source: "directory", Path: strings.TrimPrefix(call, "plugin marketplace add ")}
	case call == "plugin install ktx@ktx":
		f.installed = true
	default:
		return "", "unexpected call", errors.New("exit 2")
	}
	return "", "", nil
}

func (f *fakeClaudeCLI) marketplaceList() []claudeMarketplace {
	if f.marketplace == nil {
		return nil
	}
	return []claudeMarketplace{*f.marketplace}
}

func TestUseClaudeMarketplaceSwitchesSources(t *testing.T) {
	const checkout = "/home/dev/ktx"
	dev := func(m claudeMarketplace) bool { return m.Source == "directory" && m.Path == checkout }
	release := func(m claudeMarketplace) bool { return m.Source == "github" && m.Repo == "jgabor/ktx" }
	devMarketplace := &claudeMarketplace{Name: "ktx", Source: "directory", Path: checkout}
	releaseMarketplace := &claudeMarketplace{Name: "ktx", Source: "github", Repo: "jgabor/ktx"}
	cases := []struct {
		name    string
		fake    fakeClaudeCLI
		source  string
		matches func(claudeMarketplace) bool
		want    []string
		output  string
	}{
		{"no ktx marketplace yet", fakeClaudeCLI{}, checkout, dev,
			[]string{"plugin marketplace add " + checkout, "plugin install ktx@ktx"}, "now reads from " + checkout},
		{"dev to release", fakeClaudeCLI{marketplace: devMarketplace, installed: true}, "jgabor/ktx", release,
			[]string{"plugin uninstall ktx@ktx", "plugin marketplace remove ktx", "plugin marketplace add jgabor/ktx", "plugin install ktx@ktx"}, "now reads from jgabor/ktx"},
		{"release to dev", fakeClaudeCLI{marketplace: releaseMarketplace, installed: true}, checkout, dev,
			[]string{"plugin uninstall ktx@ktx", "plugin marketplace remove ktx", "plugin marketplace add " + checkout, "plugin install ktx@ktx"}, "now reads from " + checkout},
		{"already on the source", fakeClaudeCLI{marketplace: releaseMarketplace, installed: true}, "jgabor/ktx", release,
			nil, "already reads from jgabor/ktx"},
		{"marketplace matches, plugin missing", fakeClaudeCLI{marketplace: devMarketplace}, checkout, dev,
			[]string{"plugin install ktx@ktx"}, "now reads from " + checkout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := c.fake
			var output strings.Builder
			if err := useClaudeMarketplace("/bin/claude", c.source, c.matches, fake.run, &output); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fake.changes, c.want) {
				t.Fatalf("changes = %q, want %q", fake.changes, c.want)
			}
			if !strings.Contains(output.String(), c.output) {
				t.Fatalf("output = %q, want %q", output.String(), c.output)
			}
			if fake.marketplace == nil || !c.matches(*fake.marketplace) || !fake.installed {
				t.Fatalf("end state = %+v installed=%t", fake.marketplace, fake.installed)
			}
		})
	}
}

func TestUseClaudeMarketplaceFailureNamesRecovery(t *testing.T) {
	fake := fakeClaudeCLI{failOn: "plugin marketplace add jgabor/ktx"}
	var output strings.Builder
	err := useClaudeMarketplace("/bin/claude", "jgabor/ktx", func(m claudeMarketplace) bool { return m.Repo == "jgabor/ktx" }, fake.run, &output)
	if err == nil {
		t.Fatal("switch succeeded")
	}
	for _, required := range []string{
		"network unreachable",
		"Recovery: run `claude plugin marketplace remove ktx`, `claude plugin marketplace add jgabor/ktx`, then `claude plugin install ktx@ktx`",
	} {
		if !strings.Contains(err.Error(), required) {
			t.Errorf("failure lacks %q: %v", required, err)
		}
	}
	if output.Len() != 0 {
		t.Fatalf("failure claimed success: %q", output.String())
	}
}

func TestRefreshInstalledAdaptersAttemptsAllFailuresWithSafeRecovery(t *testing.T) {
	var calls []string
	var output strings.Builder
	binary := filepath.Join(t.TempDir(), "bin with spaces", "ktx")
	err := refreshInstalledAdapters(binary, adapterScopes{opencodeGlobal: true, opencodeProject: true, codexGlobal: true, codexProject: true}, func(_ string, args ...string) (string, string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if call == "init opencode" || call == "init codex --project" {
			return "", "permission denied", errors.New("exit 1")
		}
		return "", "", nil
	}, &output)
	if err == nil {
		t.Fatal("refresh succeeded")
	}
	if got, want := strings.Join(calls, "\n"), "init opencode\ninit opencode --project\ninit codex\ninit codex --project"; got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	quotedBinary := shellArgument(binary)
	for _, required := range []string{
		"opencode global", "codex project",
		"Recovery: run `" + quotedBinary + " init opencode`",
		"Recovery: run `" + quotedBinary + " init codex --project`",
	} {
		if !strings.Contains(err.Error(), required) {
			t.Errorf("failure lacks %q: %v", required, err)
		}
	}
	if output.Len() != 0 || strings.Contains(err.Error(), "installed and enabled adapters refreshed") {
		t.Fatalf("failure claimed success: stdout=%q error=%v", output.String(), err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := recoveryCommand(binary, "argument with spaces")
	got, runErr := exec.Command("sh", "-c", command).Output()
	if runErr != nil || string(got) != "argument with spaces\n" {
		t.Fatalf("recovery command %q is not shell-safe: output=%q error=%v", command, got, runErr)
	}
}

func TestRefreshInstalledAdaptersCreatesNoneWhenAbsent(t *testing.T) {
	called := false
	var output strings.Builder
	err := refreshInstalledAdapters("/bin/ktx", adapterScopes{}, func(_ string, _ ...string) (string, string, error) {
		called = true
		return "", "", nil
	}, &output)
	if err != nil || called || !strings.Contains(output.String(), "no managed adapters") {
		t.Fatalf("err=%v called=%t output=%q", err, called, output.String())
	}
}

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
	for _, key := range jsRuntimeVariables {
		if value := os.Getenv(key); value != "" && (!filepath.IsAbs(value) || pathWithin(root, value)) {
			t.Errorf("%s=%q is not an absolute path outside disposable root %q", key, value, root)
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

func TestOpencodeTelemetryFixtureRejectsInvalidBoundaryBeforeTools(t *testing.T) {
	t.Setenv(hermeticMarkerEnv, "1")
	t.Setenv(hermeticBoundaryIDEnv, "")
	t.Setenv("PATH", t.TempDir())
	if err := OpencodeTelemetryFixture(); err == nil || !strings.Contains(err.Error(), "invalid hermetic reentry") {
		t.Fatalf("fixture must validate isolation before looking up Node or running tests: %v", err)
	}
}

func TestMaintainedVerificationEntrypointWiring(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	t.Run("pre-push", func(t *testing.T) {
		_, hook, ok := strings.Cut(read(".lefthook.yml"), "pre-push:")
		if !ok || !strings.Contains(hook, "run: mage test") {
			t.Error("pre-push must use the audited race-enabled Mage test target")
		}
		if strings.Contains(hook, "--fix") || strings.Contains(hook, "stage_fixed:") {
			t.Error("parallel pre-push verification must not fix or stage its inputs")
		}
	})
	t.Run("CI prerequisites", func(t *testing.T) {
		workflow := read(".github/workflows/ci.yml")
		for _, required := range []string{"oven-sh/setup-bun@", "actions/setup-node@", "actions/setup-python@", "run: mage verify"} {
			if !strings.Contains(workflow, required) {
				t.Errorf("CI lacks %q", required)
			}
		}
	})
	t.Run("Python verifier in check", func(t *testing.T) {
		source := read("magefile.go")
		_, body, _ := strings.Cut(source, "func check() error {")
		body, _, _ = strings.Cut(body, "\n}\n")
		if !strings.Contains(body, "run: schemaReadinessVerifierTest") {
			t.Error("check must execute Python verifier tests (verify inherits check)")
		}
	})
}

func TestGeneratedAdapterGateRequiresBinaryAndBun(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "ktx")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_TEST_BUN", "")
	t.Setenv("PATH", t.TempDir())
	if _, err := generatedAdapterGateRequirements(binary); err == nil || !strings.Contains(err.Error(), "bun not found") {
		t.Fatalf("missing Bun result = %v", err)
	}
	t.Setenv("KTX_TEST_BUN", filepath.Join(directory, "missing-bun"))
	if _, err := generatedAdapterGateRequirements(binary); err == nil || !strings.Contains(err.Error(), "KTX_TEST_BUN") {
		t.Fatalf("missing captured Bun result = %v", err)
	}
	t.Setenv("KTX_TEST_BUN", "")

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
		{name: "relative JavaScript runtime", mutate: func(t *testing.T, env map[string]string) { env["KTX_TEST_NODE"] = "node" }},
		{name: "JavaScript runtime inside root", mutate: func(t *testing.T, env map[string]string) {
			env["KTX_TEST_BUN"] = filepath.Join(env[hermeticRootEnv], "bun")
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

func TestInstallClaudePluginSkipsWithoutClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	matched := false
	if err := installClaudePlugin("jgabor/ktx", func(claudeMarketplace) bool { matched = true; return true }); err != nil {
		t.Fatalf("skip failed: %v", err)
	}
	if matched {
		t.Fatal("read Claude Code state without claude on PATH")
	}
}
