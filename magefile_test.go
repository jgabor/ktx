//go:build mage

package main

import (
	"archive/zip"
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

func TestRecoveryCommandIsShellSafe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting")
	}
	binary := filepath.Join(t.TempDir(), "bin with spaces", "ktx")
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
	t.Run("OpenCode package tests in check", func(t *testing.T) {
		source := read("magefile.go")
		_, body, _ := strings.Cut(source, "func check() error {")
		body, _, _ = strings.Cut(body, "\n}\n")
		if !strings.Contains(body, "run: opencodePackageTest") {
			t.Error("check must execute the OpenCode package tests (verify inherits check)")
		}
		_, test, _ := strings.Cut(source, "func opencodePackageTest() error {")
		test, _, _ = strings.Cut(test, "\n}\n")
		if !strings.Contains(test, "OpencodePackage()") {
			t.Error("the OpenCode package tests must build the package first")
		}
		if !strings.Contains(test, "opencodePackageContents(opencodePackageDir)") {
			t.Error("check must verify what npm would pack for the OpenCode package")
		}
	})
	t.Run("OpenCode package publishing", func(t *testing.T) {
		workflow := read(".github/workflows/ci.yml")
		_, jobs, ok := strings.Cut(workflow, "\n  opencode-package:\n")
		if !ok {
			t.Fatal("CI lacks the opencode-package job")
		}
		pack, publish, ok := strings.Cut(jobs, "\n  publish-opencode-package:\n")
		if !ok {
			t.Fatal("CI lacks the publish-opencode-package job")
		}
		if strings.Count(workflow, "id-token: write") != 1 || !strings.Contains(publish, "id-token: write") {
			t.Error("only the publish job may request an OIDC token")
		}
		for _, required := range []string{
			"needs: verify", "fetch-depth: 0", "git describe --tags --long", `^refs/tags/v([0-9]+\.[0-9]+\.[0-9]+)$`, "$EVENT != push",
			"run: mage opencodePackageContents", "if: steps.version.outputs.release != 'true'",
			"npm publish --dry-run --ignore-scripts --tag ci", "npm pack --ignore-scripts", "actions/upload-artifact@",
		} {
			if !strings.Contains(pack, required) {
				t.Errorf("opencode-package job lacks %q", required)
			}
		}
		for _, required := range []string{
			"if: github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')", "needs: opencode-package",
			"environment: npm-publish", "contents: read", "actions/download-artifact@", `npm publish "$tarball"`,
		} {
			if !strings.Contains(publish, required) {
				t.Errorf("publish-opencode-package job lacks %q", required)
			}
		}
		for _, forbidden := range []string{"NODE_AUTH_TOKEN", "NPM_TOKEN", "secrets."} {
			if strings.Contains(jobs, forbidden) {
				t.Errorf("the OpenCode package must publish through trusted publishing, not %q", forbidden)
			}
		}
		if strings.Count(pack, "npm publish") != 1 || strings.Count(publish, "npm publish") != 1 {
			t.Error("the pack job must only dry-run and the publish job must publish once")
		}
	})
}

// opencodePackFixture writes a package folder with the real manifest and
// stand-in files whose imports mirror the real package's.
func opencodePackFixture(t *testing.T, manifest string) string {
	t.Helper()
	if manifest == "" {
		data, err := os.ReadFile(filepath.Join(opencodePackageDir, "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		manifest = string(data)
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		"package.json": manifest,
		"README.md":    "# readme\n",
		"LICENSE":      "MIT\n",
		"server.ts":    "import type { Plugin } from \"@opencode-ai/plugin\"\nimport os from \"node:os\"\nexport default {}\n",
		"tui.js":       "import { a } from \"./ktx-stats.ts\";\nimport { createPressInteraction } from \"./mouse.ts\";\nexport default {};\n",
		"ktx-stats.ts": "export const a = 1;\n",
		"mouse.ts":     "export const createPressInteraction = () => {};\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestOpencodePackProblemsRequiresTargetsAndTheirImports(t *testing.T) {
	dir := opencodePackFixture(t, "")
	all := []string{"LICENSE", "README.md", "ktx-stats.ts", "mouse.ts", "package.json", "server.ts", "tui.js"}
	if err := opencodePackProblems(dir, all); err != nil {
		t.Fatalf("complete pack = %v", err)
	}
	without := func(name string) []string {
		var files []string
		for _, file := range all {
			if file != name {
				files = append(files, file)
			}
		}
		return files
	}
	for missing, want := range map[string]string{
		"tui.js":       "./tui target (tui.js)",
		"server.ts":    "./server target (server.ts)",
		"mouse.ts":     "mouse.ts, which the OpenCode package's tui.js imports",
		"ktx-stats.ts": "ktx-stats.ts, which the OpenCode package's tui.js imports",
		"README.md":    "README.md",
		"LICENSE":      "LICENSE",
	} {
		t.Run("without "+missing, func(t *testing.T) {
			if err := opencodePackProblems(dir, without(missing)); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("result = %v, want a problem naming %q", err, want)
			}
		})
	}
	t.Run("imports followed through imported files", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "mouse.ts"), []byte("export * from \"./lib/press.ts\";\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := opencodePackProblems(dir, all); err == nil || !strings.Contains(err.Error(), "lib/press.ts, which the OpenCode package's mouse.ts imports") {
			t.Fatalf("result = %v, want the nested import named", err)
		}
	})
}

// TestOpencodePackageContentsUsesNpmPack runs the real npm pack over a package
// whose files list leaves out the TUI target, so a manifest change that stops
// npm packing a target fails the check.
func TestOpencodePackageContentsUsesNpmPack(t *testing.T) {
	if err := requireJSRuntime("node", "for npm"); err != nil {
		t.Skip(err)
	}
	if err := opencodePackageContents(opencodePackFixture(t, "")); err != nil {
		t.Fatalf("real manifest = %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(opencodePackageDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(manifest, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["version"]; ok {
		t.Fatal("the package manifest must not carry a version; publishing sets it from the release tag")
	}
	fields["files"] = []string{"server.ts", "ktx-stats.ts", "mouse.ts"}
	broken, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	err = opencodePackageContents(opencodePackFixture(t, string(broken)))
	if err == nil || !strings.Contains(err.Error(), "./tui target (tui.js)") {
		t.Fatalf("files list without tui.js = %v, want a ./tui problem", err)
	}
}

func TestOpencodePackageCarriesTheRepositoryLicense(t *testing.T) {
	root, err := os.ReadFile("LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	packaged, err := os.ReadFile(filepath.Join(opencodePackageDir, "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	if string(packaged) != string(root) {
		t.Error("plugins/opencode/LICENSE must match the repository LICENSE")
	}
}

func TestOpencodePackageReadyRequiresBothTargets(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join(opencodePackageDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	folder := func(files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	built := folder(map[string]string{"package.json": string(manifest), "server.ts": "server", "tui.js": "tui"})
	if err := opencodePackageReady(built); err != nil {
		t.Fatalf("built package = %v", err)
	}
	for name, files := range map[string]map[string]string{
		"TUI target not built": {"package.json": string(manifest), "server.ts": "server"},
		"empty TUI target":     {"package.json": string(manifest), "server.ts": "server", "tui.js": ""},
		"no TUI export":        {"package.json": `{"exports":{"./server":"./server.ts"}}`, "server.ts": "server"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := opencodePackageReady(folder(files)); err == nil || !strings.Contains(err.Error(), "./tui") {
				t.Fatalf("result = %v, want a ./tui problem", err)
			}
		})
	}
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

// fakeCodexCLI answers the codex plugin commands readCodexSetup and
// useCodexMarketplace run from listings in memory, answers the ktx and go
// commands installRelease runs, and records every call that changes something.
type fakeCodexCLI struct {
	marketplace *codexMarketplace
	installed   bool
	failOn      string
	releaseZip  string
	changes     []string
}

func newCodexMarketplace(sourceType, source string) *codexMarketplace {
	marketplace := &codexMarketplace{Name: "ktx"}
	marketplace.Source.SourceType = sourceType
	marketplace.Source.Source = source
	return marketplace
}

func (f *fakeCodexCLI) run(name string, args ...string) (string, string, error) {
	call := strings.Join(args, " ")
	switch {
	case name == "go" && call == "env GOBIN":
		return "/gobin\n", "", nil
	case name == "go" && strings.HasSuffix(call, "mod download -json github.com/jgabor/ktx@latest"):
		return fmt.Sprintf(`{"Path":"github.com/jgabor/ktx","Version":"v0.2.0","Zip":%q}`, f.releaseZip), "", nil
	case name == "/bin/codex" && call == "plugin marketplace list --json":
		list := []codexMarketplace{{Name: "other"}}
		if f.marketplace != nil {
			list = append(list, *f.marketplace)
		}
		data, _ := json.Marshal(map[string]any{"marketplaces": list})
		return string(data), "WARNING: proceeding", nil
	case name == "/bin/codex" && call == "plugin list --json":
		if f.installed {
			return `{"installed":[{"pluginId":"other@other"},{"pluginId":"ktx@ktx"}]}`, "", nil
		}
		return `{"installed":[{"pluginId":"other@other"}]}`, "", nil
	}
	f.changes = append(f.changes, strings.TrimSpace(name+" "+call))
	if call == f.failOn {
		return "", "network unreachable", errors.New("exit 1")
	}
	switch {
	case name == "go" && strings.HasPrefix(call, "install "):
	case name != "/bin/codex":
		if call != "--format text init codex" {
			return "", "unexpected call", errors.New("exit 2")
		}
		return "removed the earlier ktx hook\n", "", nil
	case call == "plugin remove ktx@ktx":
		f.installed = false
	case call == "plugin marketplace remove ktx":
		f.marketplace = nil
	case call == "plugin marketplace add jgabor/ktx":
		f.marketplace = newCodexMarketplace("git", "https://github.com/jgabor/ktx.git")
	case strings.HasPrefix(call, "plugin marketplace add /"):
		f.marketplace = newCodexMarketplace("local", strings.TrimPrefix(call, "plugin marketplace add "))
	case call == "plugin marketplace upgrade ktx":
	case call == "plugin add ktx@ktx":
		if f.marketplace == nil {
			return "", "marketplace ktx not found", errors.New("exit 1")
		}
		f.installed = true
	default:
		return "", "unexpected call", errors.New("exit 2")
	}
	return "", "", nil
}

func writeHooks(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	pinnedKtxHooks = `{"hooks":{"PostToolUse":[{"hooks":[{"command":"orca"}]},{"matcher":".*","hooks":[{"type":"command","command":"'/home/u/go/bin/ktx' hook","statusMessage":"ktx: recording tool call"}]}]}}`
	otherHooks     = `{"hooks":{"PostToolUse":[{"hooks":[{"command":"orca"}]}],"SessionStart":[{"hooks":[{"command":"ktx hook --other"}]}]}}`
)

func TestReadCodexSetup(t *testing.T) {
	cases := []struct {
		name      string
		fake      fakeCodexCLI
		hooks     string
		setUp     bool
		legacy    bool
		installed bool
	}{
		{"marketplace and plugin", fakeCodexCLI{marketplace: newCodexMarketplace("git", "https://github.com/jgabor/ktx.git"), installed: true}, otherHooks, true, false, true},
		{"earlier merged hook only", fakeCodexCLI{}, pinnedKtxHooks, true, true, false},
		{"bare merged hook only", fakeCodexCLI{}, `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"ktx hook","timeout":10}]}]}}`, true, true, false},
		{"not set up", fakeCodexCLI{}, otherHooks, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := c.fake
			hooks := writeHooks(t, c.hooks)
			var output strings.Builder
			setup, err := readCodexSetup("/bin/codex", []string{hooks, filepath.Join(t.TempDir(), "absent.json")}, fake.run, &output)
			if err != nil {
				t.Fatal(err)
			}
			if (setup != nil) != c.setUp {
				t.Fatalf("setup = %+v, want set up %t", setup, c.setUp)
			}
			if !c.setUp {
				if !strings.Contains(output.String(), "not set up") {
					t.Fatalf("output = %q", output.String())
				}
				return
			}
			if (len(setup.legacy) > 0) != c.legacy || setup.installed != c.installed {
				t.Fatalf("setup = %+v", setup)
			}
			if len(fake.changes) != 0 {
				t.Fatalf("reading changed Codex: %v", fake.changes)
			}
		})
	}

	var output strings.Builder
	if setup, err := readCodexSetup("", nil, nil, &output); setup != nil || err != nil || !strings.Contains(output.String(), "codex not found on PATH") {
		t.Fatalf("without codex = %+v %v %q", setup, err, output.String())
	}
	fake := fakeCodexCLI{}
	if _, err := readCodexSetup("/bin/codex", []string{writeHooks(t, `{"hooks":false}`)}, fake.run, &output); err == nil {
		t.Fatal("a malformed hooks file passed")
	}
}

func TestUseCodexMarketplaceSwitchesSources(t *testing.T) {
	const checkout = "/home/dev/ktx"
	dev := func(m codexMarketplace) bool { return m.Source.SourceType == "local" && m.Source.Source == checkout }
	release := func(m codexMarketplace) bool {
		return m.Source.SourceType == "git" && m.Source.Source == "https://github.com/jgabor/ktx.git"
	}
	devMarketplace := newCodexMarketplace("local", checkout)
	releaseMarketplace := newCodexMarketplace("git", "https://github.com/jgabor/ktx.git")
	cases := []struct {
		name    string
		setup   codexSetup
		source  string
		matches func(codexMarketplace) bool
		want    []string
	}{
		{"dev to release", codexSetup{marketplace: devMarketplace, installed: true}, "jgabor/ktx", release,
			[]string{"/bin/codex plugin remove ktx@ktx", "/bin/codex plugin marketplace remove ktx", "/bin/codex plugin marketplace add jgabor/ktx", "/bin/codex plugin add ktx@ktx"}},
		{"release to dev", codexSetup{marketplace: releaseMarketplace, installed: true}, checkout, dev,
			[]string{"/bin/codex plugin remove ktx@ktx", "/bin/codex plugin marketplace remove ktx", "/bin/codex plugin marketplace add " + checkout, "/bin/codex plugin add ktx@ktx"}},
		{"dev refresh", codexSetup{marketplace: devMarketplace, installed: true}, checkout, dev,
			[]string{"/bin/codex plugin add ktx@ktx"}},
		{"release refresh", codexSetup{marketplace: releaseMarketplace, installed: true}, "jgabor/ktx", release,
			[]string{"/bin/codex plugin marketplace upgrade ktx", "/bin/codex plugin add ktx@ktx"}},
		{"earlier merged hook only", codexSetup{legacy: []string{"/home/u/.codex/hooks.json"}}, checkout, dev,
			[]string{"/bin/codex plugin marketplace add " + checkout, "/bin/codex plugin add ktx@ktx", "/gobin/ktx --format text init codex"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := fakeCodexCLI{marketplace: c.setup.marketplace, installed: c.setup.installed}
			c.setup.codex = "/bin/codex"
			var output strings.Builder
			if err := useCodexMarketplace(c.setup, "/gobin/ktx", c.source, c.matches, fake.run, &output); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fake.changes, c.want) {
				t.Fatalf("changes = %q, want %q", fake.changes, c.want)
			}
			if fake.marketplace == nil || !c.matches(*fake.marketplace) || !fake.installed {
				t.Fatalf("end state = %+v installed=%t", fake.marketplace, fake.installed)
			}
			if !strings.Contains(output.String(), "refreshed from "+c.source) || !strings.Contains(output.String(), "/hooks") {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}

func TestUseCodexMarketplaceFailureNamesRecovery(t *testing.T) {
	fake := fakeCodexCLI{failOn: "plugin marketplace add jgabor/ktx"}
	var output strings.Builder
	setup := codexSetup{codex: "/bin/codex", legacy: []string{"/home/u/.codex/hooks.json"}}
	err := useCodexMarketplace(setup, "/gobin/ktx", "jgabor/ktx", func(codexMarketplace) bool { return false }, fake.run, &output)
	if err == nil {
		t.Fatal("switch succeeded")
	}
	for _, required := range []string{
		"network unreachable",
		"Recovery: run `codex plugin marketplace remove ktx`, `codex plugin marketplace add jgabor/ktx`, then `codex plugin add ktx@ktx`",
	} {
		if !strings.Contains(err.Error(), required) {
			t.Errorf("failure lacks %q: %v", required, err)
		}
	}
	if output.Len() != 0 || strings.Contains(strings.Join(fake.changes, "\n"), "init codex") {
		t.Fatalf("failure went on: output=%q changes=%q", output.String(), fake.changes)
	}
}

// writeReleaseZip writes a module zip for ktx v0.2.0 holding the given files.
func writeReleaseZip(t *testing.T, names ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v0.2.0.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for _, name := range append([]string{"go.mod"}, names...) {
		if _, err := archive.Create("github.com/jgabor/ktx@v0.2.0/" + name); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallReleaseStopsWhenTheReleaseHasNoCodexPlugin(t *testing.T) {
	fake := fakeCodexCLI{
		marketplace: newCodexMarketplace("local", "/home/dev/ktx"),
		installed:   true,
		releaseZip:  writeReleaseZip(t, ".claude-plugin/marketplace.json", "plugins/ktx/.claude-plugin/plugin.json"),
	}
	var output strings.Builder
	err := installRelease(fake.run, releaseRuntimes{codex: "/bin/codex"}, &output)
	if err == nil || !strings.Contains(err.Error(), "v0.2.0, has no Codex plugin") || !strings.Contains(err.Error(), "changed nothing") {
		t.Fatalf("installRelease = %v", err)
	}
	if len(fake.changes) != 0 {
		t.Fatalf("installRelease changed something before stopping: %q", fake.changes)
	}
}

func TestInstallReleaseInstallsTheCheckedRelease(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	fake := fakeCodexCLI{
		marketplace: newCodexMarketplace("local", "/home/dev/ktx"),
		installed:   true,
		releaseZip:  writeReleaseZip(t, ".agents/plugins/marketplace.json", "plugins/codex/.codex-plugin/plugin.json"),
	}
	var output strings.Builder
	if err := installRelease(fake.run, releaseRuntimes{codex: "/bin/codex", codexHookFiles: []string{writeHooks(t, pinnedKtxHooks)}}, &output); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"go install github.com/jgabor/ktx/cmd/ktx@v0.2.0",
		"/bin/codex plugin remove ktx@ktx", "/bin/codex plugin marketplace remove ktx",
		"/bin/codex plugin marketplace add jgabor/ktx", "/bin/codex plugin add ktx@ktx",
		"/gobin/ktx --format text init codex",
	}
	if !reflect.DeepEqual(fake.changes, want) {
		t.Fatalf("changes = %q, want %q", fake.changes, want)
	}
	for _, required := range []string{"removed the earlier ktx hook", "refreshed from jgabor/ktx"} {
		if !strings.Contains(output.String(), required) {
			t.Errorf("output lacks %q: %q", required, output.String())
		}
	}
}

func TestInstallReleaseSkipsTheCheckWhenCodexIsNotSetUp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	fake := fakeCodexCLI{}
	var output strings.Builder
	if err := installRelease(fake.run, releaseRuntimes{codex: "/bin/codex", codexHookFiles: []string{writeHooks(t, otherHooks)}}, &output); err != nil {
		t.Fatal(err)
	}
	if want := []string{"go install github.com/jgabor/ktx/cmd/ktx@latest"}; !reflect.DeepEqual(fake.changes, want) {
		t.Fatalf("changes = %q, want %q", fake.changes, want)
	}
	if !strings.Contains(output.String(), "Codex: ktx is not set up") {
		t.Fatalf("output = %q", output.String())
	}
}
