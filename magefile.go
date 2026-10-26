//go:build mage

package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jgabor/ktx/internal/opencodeplugin"
)

var Default = Check

const (
	hermeticMarkerEnv          = "KTX_VERIFY_HERMETIC"
	hermeticRootEnv            = "KTX_VERIFY_ROOT"
	hermeticBoundaryIDEnv      = "KTX_VERIFY_BOUNDARY_ID"
	hermeticCapabilityEnv      = "KTX_VERIFY_CAPABILITY"
	hermeticNonceEnv           = "KTX_VERIFY_NONCE"
	databaseAuditEnv           = "KTX_VERIFY_DB_AUDIT"
	forbiddenDBPathsEnv        = "KTX_VERIFY_FORBIDDEN_DB_PATHS"
	temporaryDirectoryEnv      = "TMPDIR"
	boundaryNonceHexLength     = 32
	maxForbiddenPayloadBytes   = 8192
	maxForbiddenDatabasePaths  = 16
	engineeringEvidenceDirEnv  = "KTX_ENGINEERING_EVIDENCE_DIR"
	engineeringManifestFile    = "manifest.json"
	engineeringManifestSchema  = 2
	engineeringReportMaxSchema = 3
)

var applicationRootVariables = []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}
var toolCacheVariables = []string{"GOCACHE", "GOMODCACHE", "BUN_INSTALL_CACHE_DIR", "GOLANGCI_LINT_CACHE"}

// jsRuntimeVariables name the executables that tests use to run generated
// TypeScript. Each is optional: tests skip, and gates fail, when it is absent.
var jsRuntimeVariables = map[string]string{"node": "KTX_TEST_NODE", "bun": "KTX_TEST_BUN"}

type engineeringReportSpec struct {
	name         string
	target       string
	file         string
	identityKeys []string
}

var engineeringReportSpecs = []engineeringReportSpec{
	{name: "component", target: "componentBench", file: "component-bench.json", identityKeys: []string{"machine", "workloads", "benchmarks"}},
	{name: "load", target: "loadBench", file: "real-process-load.json", identityKeys: []string{"environment", "payloads", "measurements", "agent_loads", "contention_penalties", "scenarios"}},
	{name: "blocked-decompression", target: "decompressProcessBench", file: "blocked-decompression.json", identityKeys: []string{"environment", "target"}},
}

type engineeringEvidenceManifest struct {
	SchemaVersion       int                         `json:"schema_version"`
	StartedAt           string                      `json:"started_at"`
	FinishedAt          string                      `json:"finished_at"`
	DurationNanoseconds int64                       `json:"duration_nanoseconds"`
	Reports             []engineeringEvidenceReport `json:"reports"`
}

type engineeringEvidenceReport struct {
	Name          string `json:"name"`
	MageTarget    string `json:"mage_target"`
	File          string `json:"file"`
	SchemaVersion int    `json:"report_schema_version"`
	SHA256        string `json:"sha256"`
}

func Build() error {
	if err := os.MkdirAll("build", 0o755); err != nil {
		return err
	}

	return run("go", "build", "-buildvcs=false", "-trimpath", "-ldflags", ldflags(), "-o", binaryPath(), "./cmd/ktx")
}

// Install builds ktx and installs it with go install. When OpenCode is set up
// for ktx, it builds the OpenCode package first and points OpenCode at this
// checkout's package folder. When Codex is set up for ktx, it points the ktx
// Codex marketplace at this checkout. It points the ktx Claude Code
// marketplace at this checkout with ktx@ktx installed. InstallRelease undoes
// the pairing.
func Install() error {
	if err := Build(); err != nil {
		return err
	}
	checkout, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	opencodePackage := filepath.Join(checkout, opencodePackageDir)
	scopes, err := opencodeplugin.Scopes()
	if err != nil {
		return err
	}
	opencode, err := readOpencodeSetup(lookPath("opencode"), scopes, opencodePackage, os.Stdout)
	if err != nil {
		return err
	}
	if opencode != nil {
		if err := OpencodePackage(); err != nil {
			return err
		}
	}
	hookFiles, err := codexLegacyHookFiles()
	if err != nil {
		return err
	}
	codex, err := readCodexSetup(lookPath("codex"), hookFiles, installExec, os.Stdout)
	if err != nil {
		return err
	}
	installedBinary, err := goInstalledBinary(installExec)
	if err != nil {
		return err
	}
	if _, stderr, err := installExec("go", "install", "-buildvcs=false", "-trimpath", "-ldflags", ldflags(), "./cmd/ktx"); err != nil {
		return fmt.Errorf("install development binary: %w: %s", err, strings.TrimSpace(stderr))
	}
	fmt.Printf("ktx installed at %s\n", installedBinary)
	if opencode != nil {
		if err := useOpencodePlugin(*opencode, opencodePackage, installExec, os.Stdout); err != nil {
			return err
		}
	}
	if codex != nil {
		if err := useCodexMarketplace(*codex, installedBinary, checkout, func(m codexMarketplace) bool {
			return m.Source.SourceType == "local" && filepath.Clean(m.Source.Source) == checkout
		}, installExec, os.Stdout); err != nil {
			return err
		}
	}
	return installClaudePlugin(checkout, func(m claudeMarketplace) bool {
		return m.Source == "directory" && filepath.Clean(m.Path) == checkout
	})
}

// InstallRelease installs the latest ktx release with go install. When
// OpenCode is set up for ktx, it points OpenCode at the npm package pinned to
// that release. When Codex is set up for ktx, it points the ktx Codex
// marketplace at jgabor/ktx on GitHub. It points the ktx Claude Code
// marketplace there with ktx@ktx installed. It undoes the development pairing
// that Install makes. When OpenCode or Codex is set up and the latest release
// has no plugin for it, it stops before installing anything.
func InstallRelease() error {
	hookFiles, err := codexLegacyHookFiles()
	if err != nil {
		return err
	}
	scopes, err := opencodeplugin.Scopes()
	if err != nil {
		return err
	}
	checkout, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	return installRelease(installExec, releaseRuntimes{
		codex: lookPath("codex"), codexHookFiles: hookFiles,
		opencode: lookPath("opencode"), opencodeScopes: scopes, opencodePackage: filepath.Join(checkout, opencodePackageDir),
	}, os.Stdout)
}

// releaseRuntimes is where installRelease finds the runtime CLIs and the
// files that tell whether each runtime is set up for ktx.
type releaseRuntimes struct {
	codex           string
	codexHookFiles  []string
	opencode        string
	opencodeScopes  []opencodeplugin.Scope
	opencodePackage string
}

func installRelease(runner installRunner, runtimes releaseRuntimes, stdout io.Writer) error {
	codex, err := readCodexSetup(runtimes.codex, runtimes.codexHookFiles, runner, stdout)
	if err != nil {
		return err
	}
	opencode, err := readOpencodeSetup(runtimes.opencode, runtimes.opencodeScopes, runtimes.opencodePackage, stdout)
	if err != nil {
		return err
	}
	version, opencodeSpec := "latest", ""
	if codex != nil || opencode != nil {
		release, err := latestRelease(runner)
		if err != nil {
			return err
		}
		if codex != nil {
			if err := requireReleasePlugin(release, "Codex", ".agents/plugins/marketplace.json", "plugins/codex/.codex-plugin/plugin.json"); err != nil {
				return err
			}
		}
		if opencode != nil {
			if err := requireReleasePlugin(release, "OpenCode", "plugins/opencode/package.json"); err != nil {
				return err
			}
			if opencodeSpec, err = requireOpencodePackageOnNPM(runner, release.Version); err != nil {
				return err
			}
		}
		version = release.Version
	}
	installedBinary, err := goInstalledBinary(runner)
	if err != nil {
		return err
	}
	if _, stderr, err := runner("go", "install", releaseModule+"/cmd/ktx@"+version); err != nil {
		return fmt.Errorf("install released binary: %w: %s", err, strings.TrimSpace(stderr))
	}
	fmt.Fprintf(stdout, "ktx %s installed at %s\n", version, installedBinary)
	if opencode != nil {
		if err := useOpencodePlugin(*opencode, opencodeSpec, runner, stdout); err != nil {
			return err
		}
	}
	if codex != nil {
		if err := useCodexMarketplace(*codex, installedBinary, codexReleaseSource, func(m codexMarketplace) bool {
			return m.Source.SourceType == "git" && m.Source.Source == codexReleaseURL
		}, runner, stdout); err != nil {
			return err
		}
	}
	return installClaudePlugin(claudeReleaseSource, func(m claudeMarketplace) bool {
		return m.Source == "github" && m.Repo == claudeReleaseSource
	})
}

const (
	releaseModule       = "github.com/jgabor/ktx"
	claudeReleaseSource = "jgabor/ktx"
	claudeMarketplaceID = "ktx"
	claudePluginID      = "ktx@ktx"
	codexReleaseSource  = "jgabor/ktx"
	codexReleaseURL     = "https://github.com/jgabor/ktx.git"
	codexMarketplaceID  = "ktx"
	codexPluginID       = "ktx@ktx"
	codexHookStatus     = "ktx: recording tool call"
)

// claudeMarketplace is one entry of `claude plugin marketplace list --json`.
type claudeMarketplace struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Repo   string `json:"repo"`
	Path   string `json:"path"`
}

// installClaudePlugin runs useClaudeMarketplace against the real claude CLI,
// or skips with a note when claude is not on PATH.
func installClaudePlugin(source string, matches func(claudeMarketplace) bool) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("claude not found on PATH; skipping the Claude Code plugin")
		return nil
	}
	return useClaudeMarketplace(claude, source, matches, installExec, os.Stdout)
}

// useClaudeMarketplace points the ktx marketplace at source and makes sure
// ktx@ktx is installed. matches reports whether an existing ktx marketplace
// already reads from source; one that doesn't is removed, after its plugin is
// uninstalled.
func useClaudeMarketplace(claude, source string, matches func(claudeMarketplace) bool, runner installRunner, stdout io.Writer) error {
	call := func(args ...string) (string, error) {
		out, stderr, err := runner(claude, args...)
		if err != nil {
			detail := fmt.Sprintf("claude %s: %v", strings.Join(args, " "), err)
			if text := strings.TrimSpace(stderr); text != "" {
				detail += ": " + text
			}
			return "", errors.New(detail)
		}
		return out, nil
	}
	recovery := func(err error) error {
		return fmt.Errorf("point the ktx Claude Code marketplace at %s: %w\n  Recovery: run `%s`, `%s`, then `%s`", source, err,
			recoveryCommand("claude", "plugin", "marketplace", "remove", claudeMarketplaceID),
			recoveryCommand("claude", "plugin", "marketplace", "add", source),
			recoveryCommand("claude", "plugin", "install", claudePluginID))
	}

	raw, err := call("plugin", "marketplace", "list", "--json")
	if err != nil {
		return recovery(err)
	}
	var marketplaces []claudeMarketplace
	if err := json.Unmarshal([]byte(raw), &marketplaces); err != nil {
		return recovery(fmt.Errorf("parse claude plugin marketplace list: %w", err))
	}
	raw, err = call("plugin", "list", "--json")
	if err != nil {
		return recovery(err)
	}
	var plugins []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &plugins); err != nil {
		return recovery(fmt.Errorf("parse claude plugin list: %w", err))
	}
	installed := false
	for _, plugin := range plugins {
		installed = installed || plugin.ID == claudePluginID
	}
	var current *claudeMarketplace
	for index := range marketplaces {
		if marketplaces[index].Name == claudeMarketplaceID {
			current = &marketplaces[index]
		}
	}

	changed := false
	if current != nil && !matches(*current) {
		if installed {
			if _, err := call("plugin", "uninstall", claudePluginID); err != nil {
				return recovery(err)
			}
			installed = false
		}
		if _, err := call("plugin", "marketplace", "remove", claudeMarketplaceID); err != nil {
			return recovery(err)
		}
		current = nil
		changed = true
	}
	if current == nil {
		if _, err := call("plugin", "marketplace", "add", source); err != nil {
			return recovery(err)
		}
		changed = true
	}
	if !installed {
		if _, err := call("plugin", "install", claudePluginID); err != nil {
			return recovery(err)
		}
		changed = true
	}
	if changed {
		fmt.Fprintf(stdout, "Claude Code: %s now reads from %s; run /reload-plugins\n", claudePluginID, source)
	} else {
		fmt.Fprintf(stdout, "Claude Code: %s already reads from %s\n", claudePluginID, source)
	}
	return nil
}

// codexMarketplace is one entry of `codex plugin marketplace list --json`.
type codexMarketplace struct {
	Name   string `json:"name"`
	Source struct {
		SourceType string `json:"sourceType"`
		Source     string `json:"source"`
	} `json:"marketplaceSource"`
}

// codexSetup is what Codex has of ktx: the ktx marketplace, whether ktx@ktx
// is installed, and the hooks files an earlier ktx merged a hook into.
type codexSetup struct {
	codex       string
	marketplace *codexMarketplace
	installed   bool
	legacy      []string
}

// lookPath returns the named program on PATH, or "" when there is none.
func lookPath(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

// codexLegacyHookFiles are the hooks files earlier ktx versions merged their
// Codex hook into: the global one and this checkout's.
func codexLegacyHookFiles() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	checkout, err := filepath.Abs(".")
	if err != nil {
		return nil, err
	}
	return []string{filepath.Join(home, ".codex", "hooks.json"), filepath.Join(checkout, ".codex", "hooks.json")}, nil
}

// readCodexSetup reads what Codex has of ktx. It returns nil, after a note,
// when codex is not on PATH or Codex has neither the ktx marketplace, nor
// ktx@ktx, nor an earlier merged ktx hook: the targets switch Codex only where
// ktx is already set up. It changes nothing.
func readCodexSetup(codex string, hookFiles []string, runner installRunner, stdout io.Writer) (*codexSetup, error) {
	if codex == "" {
		fmt.Fprintln(stdout, "codex not found on PATH; skipping the Codex plugin")
		return nil, nil
	}
	setup := codexSetup{codex: codex}
	read := func(target any, args ...string) error {
		out, stderr, err := runner(codex, args...)
		if err != nil {
			return fmt.Errorf("read Codex plugins: codex %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr))
		}
		if err := json.Unmarshal([]byte(out), target); err != nil {
			return fmt.Errorf("read Codex plugins: parse codex %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	var marketplaces struct {
		Marketplaces []codexMarketplace `json:"marketplaces"`
	}
	if err := read(&marketplaces, "plugin", "marketplace", "list", "--json"); err != nil {
		return nil, err
	}
	for index := range marketplaces.Marketplaces {
		if marketplaces.Marketplaces[index].Name == codexMarketplaceID {
			setup.marketplace = &marketplaces.Marketplaces[index]
		}
	}
	var plugins struct {
		Installed []struct {
			PluginID string `json:"pluginId"`
		} `json:"installed"`
	}
	if err := read(&plugins, "plugin", "list", "--json"); err != nil {
		return nil, err
	}
	for _, plugin := range plugins.Installed {
		setup.installed = setup.installed || plugin.PluginID == codexPluginID
	}
	for _, path := range hookFiles {
		merged, err := hasMergedKtxHook(path)
		if err != nil {
			return nil, err
		}
		if merged {
			setup.legacy = append(setup.legacy, path)
		}
	}
	if setup.marketplace == nil && !setup.installed && len(setup.legacy) == 0 {
		fmt.Fprintln(stdout, "Codex: ktx is not set up; skipping the Codex plugin (run `ktx init codex` to install it)")
		return nil, nil
	}
	return &setup, nil
}

// hasMergedKtxHook reports whether a hooks file holds a PostToolUse hook that
// looks like one an earlier ktx merged in. The check is loose on purpose: it
// only decides whether Codex is set up for ktx, and `ktx init codex` removes
// exactly the hooks ktx generated.
func hasMergedKtxHook(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Codex hooks %s: %w", path, err)
	}
	var doc struct {
		Hooks struct {
			PostToolUse []struct {
				Hooks []struct {
					Command       string `json:"command"`
					StatusMessage string `json:"statusMessage"`
				} `json:"hooks"`
			} `json:"PostToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("read Codex hooks %s: %w", path, err)
	}
	for _, entry := range doc.Hooks.PostToolUse {
		for _, hook := range entry.Hooks {
			if hook.StatusMessage == codexHookStatus || hook.Command == "ktx hook" {
				return true, nil
			}
		}
	}
	return false, nil
}

// ktxRelease is the latest ktx release as the Go module proxy reports it:
// its version and the module zip that `go install` builds from.
type ktxRelease struct {
	Version string
	Zip     string
}

// latestRelease resolves the latest ktx release through the Go module proxy,
// so the release the plugin checks inspect is the one installed. It runs
// outside this module, so go.mod and go.sum stay as they are.
func latestRelease(runner installRunner) (ktxRelease, error) {
	out, stderr, err := runner("go", "-C", os.TempDir(), "mod", "download", "-json", releaseModule+"@latest")
	var module struct {
		ktxRelease
		Error string
	}
	jsonErr := json.Unmarshal([]byte(out), &module)
	if err != nil || jsonErr != nil || module.Error != "" {
		detail := module.Error
		if detail == "" {
			detail = strings.TrimSpace(stderr)
		}
		if detail == "" {
			detail = errors.Join(err, jsonErr).Error()
		}
		return ktxRelease{}, fmt.Errorf("resolve the latest ktx release: %s; nothing was installed", detail)
	}
	return module.ktxRelease, nil
}

// requireReleasePlugin checks that the release's module zip holds a
// runtime's plugin files.
func requireReleasePlugin(release ktxRelease, runtimeName string, files ...string) error {
	missing, err := missingZipFiles(release.Zip, releaseModule+"@"+release.Version+"/", files...)
	if err != nil {
		return fmt.Errorf("inspect ktx %s: %w; nothing was installed", release.Version, err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("the latest ktx release, %s, has no %s plugin (missing %s), so mage installRelease changed nothing; keep %s on this checkout with `mage install` until a release includes the plugin", release.Version, runtimeName, strings.Join(missing, ", "), runtimeName)
	}
	return nil
}

// missingZipFiles returns the names that the zip archive at path lacks under
// prefix.
func missingZipFiles(path, prefix string, names ...string) ([]string, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	present := map[string]bool{}
	for _, file := range archive.File {
		present[file.Name] = true
	}
	missing := []string{}
	for _, name := range names {
		if !present[prefix+name] {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// useCodexMarketplace points the ktx Codex marketplace at source and installs
// ktx@ktx again, which refreshes Codex's copy in place. matches reports
// whether the existing ktx marketplace already reads from source; one that
// doesn't is removed, after its plugin. A Git marketplace that already matches
// is upgraded first. When an earlier ktx merged a hook into a hooks file,
// binary's `ktx init codex` then removes it, after the switch, so it never
// adds a marketplace of its own.
func useCodexMarketplace(setup codexSetup, binary, source string, matches func(codexMarketplace) bool, runner installRunner, stdout io.Writer) error {
	call := func(args ...string) error {
		_, stderr, err := runner(setup.codex, args...)
		if err != nil {
			detail := fmt.Sprintf("codex %s: %v", strings.Join(args, " "), err)
			if text := strings.TrimSpace(stderr); text != "" {
				detail += ": " + text
			}
			return errors.New(detail)
		}
		return nil
	}
	recovery := func(err error) error {
		return fmt.Errorf("point the ktx Codex marketplace at %s: %w\n  Recovery: run `%s`, `%s`, then `%s`", source, err,
			recoveryCommand("codex", "plugin", "marketplace", "remove", codexMarketplaceID),
			recoveryCommand("codex", "plugin", "marketplace", "add", source),
			recoveryCommand("codex", "plugin", "add", codexPluginID))
	}

	current := setup.marketplace
	if current != nil && !matches(*current) {
		if setup.installed {
			if err := call("plugin", "remove", codexPluginID); err != nil {
				return recovery(err)
			}
		}
		if err := call("plugin", "marketplace", "remove", codexMarketplaceID); err != nil {
			return recovery(err)
		}
		current = nil
	}
	if current == nil {
		if err := call("plugin", "marketplace", "add", source); err != nil {
			return recovery(err)
		}
	} else if current.Source.SourceType == "git" {
		if err := call("plugin", "marketplace", "upgrade", codexMarketplaceID); err != nil {
			return recovery(err)
		}
	}
	if err := call("plugin", "add", codexPluginID); err != nil {
		return recovery(err)
	}
	if len(setup.legacy) > 0 {
		out, stderr, err := runner(binary, "--format", "text", "init", "codex")
		if err != nil {
			return fmt.Errorf("remove the earlier ktx hook from %s: %w: %s\n  Recovery: run `%s`", strings.Join(setup.legacy, " and "), err, strings.TrimSpace(stderr), recoveryCommand(binary, "init", "codex"))
		}
		fmt.Fprint(stdout, out)
	}
	fmt.Fprintf(stdout, "Codex: %s refreshed from %s; start a new Codex session, and trust the ktx hook in /hooks if it is new or changed\n", codexPluginID, source)
	return nil
}

type installRunner func(string, ...string) (string, string, error)

func goInstalledBinary(runner installRunner) (string, error) {
	gobin, _, err := runner("go", "env", "GOBIN")
	if err != nil {
		return "", fmt.Errorf("resolve GOBIN: %w", err)
	}
	directory := strings.TrimSpace(gobin)
	if directory == "" {
		gopath, _, err := runner("go", "env", "GOPATH")
		if err != nil {
			return "", fmt.Errorf("resolve GOPATH: %w", err)
		}
		directory = filepath.Join(strings.Split(strings.TrimSpace(gopath), string(os.PathListSeparator))[0], "bin")
	}
	name := "ktx"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(directory, name), nil
}

func recoveryCommand(name string, args ...string) string {
	parts := append([]string{name}, args...)
	for index, part := range parts {
		parts[index] = shellArgument(part)
	}
	return strings.Join(parts, " ")
}

func shellArgument(argument string) string {
	if runtime.GOOS == "windows" {
		if argument != "" && !strings.ContainsAny(argument, " \t\"") {
			return argument
		}
		return `"` + strings.ReplaceAll(argument, `"`, `\"`) + `"`
	}
	if argument != "" {
		safe := true
		for _, character := range argument {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-", character) {
				safe = false
				break
			}
		}
		if safe {
			return argument
		}
	}
	return `'` + strings.ReplaceAll(argument, `'`, `'"'"'`) + `'`
}

func installExec(name string, args ...string) (string, string, error) {
	command := exec.Command(name, args...)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Stdin = os.Stdin
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

func ldflags() string {
	version := readEnv("VERSION", "dev")
	return strings.Join([]string{
		"-s",
		"-w",
		"-X github.com/jgabor/ktx/internal/version.Version=" + version,
	}, " ")
}

func Clean() error {
	return os.RemoveAll("build")
}

func Test() error {
	return hermeticTarget("test", test)
}

func test() error {
	return run("go", "test", "-race", "-tags=mage", "-timeout=20m", "./...")
}

// ComponentBench emits benchmark-only component measurements as JSON.
func ComponentBench() error {
	return hermeticTarget("componentBench", componentBench)
}

func componentBench() error {
	return run("go", "run", "./tools/componentbench")
}

// LoadBench measures real ktx processes against disposable databases as JSON.
func LoadBench() error {
	return hermeticTarget("loadBench", loadBench)
}

func loadBench() error {
	if err := Build(); err != nil {
		return err
	}
	return run("go", "run", "./tools/loadbench")
}

// HookContention runs the pinned hook-only contention workload.
func HookContention() error {
	return hermeticTarget("hookContention", hookContention)
}

func hookContention() error {
	if err := Build(); err != nil {
		return err
	}
	return run("go", "run", "./tools/loadbench", "--hook-contention")
}

// DecompressProcessBench emits the blocked-decompression process report.
func DecompressProcessBench() error {
	return hermeticTarget("decompressProcessBench", decompressProcessBench)
}

func decompressProcessBench() error {
	if err := Build(); err != nil {
		return err
	}
	return run("go", "run", "./tools/loadbench", "--blocked-decompression")
}

// EngineeringVerify retains component and real-process reports in an explicit
// caller-owned directory. Each nested Mage target creates and audits its own
// disposable application root.
func EngineeringVerify() error {
	directory, err := engineeringEvidenceDirectory()
	if err != nil {
		return err
	}
	return engineeringVerify(directory)
}

func engineeringEvidenceDirectory() (string, error) {
	directory := strings.TrimSpace(os.Getenv(engineeringEvidenceDirEnv))
	if directory == "" {
		return "", fmt.Errorf("%s must name a new evidence directory", engineeringEvidenceDirEnv)
	}
	return filepath.Clean(directory), nil
}

func engineeringVerify(directory string) error {
	if err := createEngineeringEvidenceDirectory(directory); err != nil {
		return err
	}

	started := time.Now().UTC()
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Mage executable: %w", err)
	}
	reports := make([]engineeringEvidenceReport, 0, len(engineeringReportSpecs))
	for _, spec := range engineeringReportSpecs {
		path := filepath.Join(directory, spec.file)
		if err := runMageTargetToFile(executable, spec.target, path); err != nil {
			return fmt.Errorf("run %s: %w", spec.target, err)
		}
		schemaVersion, digest, err := summarizeEngineeringReport(path, spec.identityKeys)
		if err != nil {
			return fmt.Errorf("validate %s: %w", spec.file, err)
		}
		reports = append(reports, engineeringEvidenceReport{
			Name: spec.name, MageTarget: spec.target, File: spec.file,
			SchemaVersion: schemaVersion, SHA256: digest,
		})
	}
	finished := time.Now().UTC()
	return writeEngineeringEvidenceManifest(directory, reports, started, finished)
}

func createEngineeringEvidenceDirectory(directory string) error {
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return fmt.Errorf("create engineering evidence parent: %w", err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return fmt.Errorf("create new engineering evidence directory %q: %w", directory, err)
	}
	return nil
}

func runMageTargetToFile(executable, target, path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(executable, target)
	command.Env = os.Environ()
	command.Stdout = file
	command.Stderr = os.Stderr
	runErr := command.Run()
	closeErr := file.Close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}

func summarizeEngineeringReport(path string, identityKeys []string) (int, string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return 0, "", err
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal(contents, &report); err != nil {
		return 0, "", err
	}
	var schemaVersion int
	if err := json.Unmarshal(report["schema_version"], &schemaVersion); err != nil || schemaVersion <= 0 {
		return 0, "", fmt.Errorf("schema_version must be a positive integer")
	}
	if err := validateEngineeringReportTiming(schemaVersion, report); err != nil {
		return 0, "", err
	}
	for _, key := range identityKeys {
		value := strings.TrimSpace(string(report[key]))
		if value == "" || value == "null" || value == "[]" || value == "{}" {
			return 0, "", fmt.Errorf("%s is missing", key)
		}
	}
	sum := sha256.Sum256(contents)
	return schemaVersion, hex.EncodeToString(sum[:]), nil
}

func writeEngineeringEvidenceManifest(directory string, reports []engineeringEvidenceReport, started, finished time.Time) error {
	manifest := engineeringEvidenceManifest{
		SchemaVersion:       engineeringManifestSchema,
		StartedAt:           started.UTC().Format(time.RFC3339Nano),
		FinishedAt:          finished.UTC().Format(time.RFC3339Nano),
		DurationNanoseconds: finished.Sub(started).Nanoseconds(),
		Reports:             reports,
	}
	if err := validateEngineeringEvidenceManifest(manifest); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	file, err := os.OpenFile(filepath.Join(directory, engineeringManifestFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(contents)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func validateEngineeringReportTiming(schemaVersion int, report map[string]json.RawMessage) error {
	if schemaVersion == 1 {
		return nil
	}
	if schemaVersion < engineeringManifestSchema || schemaVersion > engineeringReportMaxSchema {
		return fmt.Errorf("unsupported engineering report schema %d", schemaVersion)
	}
	return validateEngineeringTimingFields(report["started_at"], report["finished_at"], report["duration_nanoseconds"])
}

func validateEngineeringEvidenceManifest(manifest engineeringEvidenceManifest) error {
	if manifest.SchemaVersion == 1 {
		return nil
	}
	if manifest.SchemaVersion != engineeringManifestSchema {
		return fmt.Errorf("unsupported engineering manifest schema %d", manifest.SchemaVersion)
	}
	started, err := parseEngineeringTimestamp(manifest.StartedAt)
	if err != nil {
		return fmt.Errorf("manifest started_at: %w", err)
	}
	finished, err := parseEngineeringTimestamp(manifest.FinishedAt)
	if err != nil {
		return fmt.Errorf("manifest finished_at: %w", err)
	}
	if manifest.DurationNanoseconds < 0 || finished.Before(started) {
		return fmt.Errorf("manifest timing order is invalid")
	}
	if got := finished.Sub(started).Nanoseconds(); got != manifest.DurationNanoseconds {
		return fmt.Errorf("manifest duration_nanoseconds = %d, want %d", manifest.DurationNanoseconds, got)
	}
	return nil
}

func validateEngineeringTimingFields(startedRaw, finishedRaw, durationRaw json.RawMessage) error {
	var startedAt, finishedAt string
	var durationNanoseconds int64
	if err := json.Unmarshal(startedRaw, &startedAt); err != nil {
		return fmt.Errorf("started_at must be a UTC RFC3339Nano string")
	}
	if err := json.Unmarshal(finishedRaw, &finishedAt); err != nil {
		return fmt.Errorf("finished_at must be a UTC RFC3339Nano string")
	}
	if err := json.Unmarshal(durationRaw, &durationNanoseconds); err != nil {
		return fmt.Errorf("duration_nanoseconds must be an integer")
	}
	started, err := parseEngineeringTimestamp(startedAt)
	if err != nil {
		return fmt.Errorf("started_at: %w", err)
	}
	finished, err := parseEngineeringTimestamp(finishedAt)
	if err != nil {
		return fmt.Errorf("finished_at: %w", err)
	}
	if durationNanoseconds < 0 || finished.Before(started) {
		return fmt.Errorf("timing order is invalid")
	}
	if got := finished.Sub(started).Nanoseconds(); got != durationNanoseconds {
		return fmt.Errorf("duration_nanoseconds = %d, want %d", durationNanoseconds, got)
	}
	return nil
}

func parseEngineeringTimestamp(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("must be UTC RFC3339Nano")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

// ReplayFixture runs the bounded, offline repository gate for the retained fixture.
func ReplayFixture() error {
	return hermeticTarget("replayFixture", replayFixture)
}

func replayFixture() error {
	return run("go", "test", "-race", "./internal/cli", "-run", "^TestRetainedReplayFixtureGate$", "-count=1", "-timeout=30s")
}

// OpencodeTelemetryFixture runs the offline OpenCode telemetry matrix.
func OpencodeTelemetryFixture() error {
	return hermeticTarget("opencodeTelemetryFixture", opencodeTelemetryFixture)
}

func opencodeTelemetryFixture() error {
	if err := requireJSRuntime("node", "to execute the OpenCode package server fixture"); err != nil {
		return err
	}
	pattern := "^Test(ValidateObservationRequiresRuntimeIdentity|ValidateObservationPreservesMeasurementStates|ContractFixtures|CollectorCorrelatesDeduplicatesAndOrders|CollectorKeepsFirstObservationAuthoritative|HookAndWrapperModelStepsNormalizeWithParity|SessionTelemetryPersistsSeparatelyAndListsMeasurements|SessionTelemetryPersistsUnknownAndMalformedFields|SessionTelemetryDuplicateConcurrentAndAtomicAssociation|SessionTelemetryRuntimeQualifiedIdentityAndFirstEvidence|SessionTelemetryConcurrentConflictHasOneCompleteWinner|SessionTelemetrySchemaV6FreshAndHistorical|SessionTelemetryV4MigrationPreservesAllEvidence|SessionTelemetryV6MigrationRollsBack|TelemetryRecordAndBoundedJSONInspection|TelemetryInspectionOrdersRuntimeNamespacesAndShowsProvenance|TelemetryRecordDistinguishesUnavailableFieldsAndPreservesZeros|TelemetryExplicitStatesPreserveCrossIngressFirstEvidence|TelemetryCommandDatabaseFailuresFailOpenOnlyForRecord|TelemetryRejectsMalformedAndFailsOpenOnPersistenceError|WrapperRetainsModelStepsSeparatelyWithFirstEvidenceAuthority|OpencodeGeneratedAdapterEventAndToolIntegration)$"
	return run("go", "test", "-race", "./internal/telemetry/...", "./internal/db", "./internal/cli", "-run", pattern, "-count=1", "-timeout=30s")
}

// ClaudePluginCheck runs `claude plugin validate` on the ktx marketplace at
// the repository root and on its plugin, and runs
// `claude plugin test` on the plugin, with application folders pointed at a
// disposable root. It is opt-in, never part of check or test, and skips when
// claude is not on PATH. It never adds or installs a marketplace.
func ClaudePluginCheck() error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Println("claude not found on PATH; skipping Claude Code plugin checks")
		return nil
	}
	root, err := os.MkdirTemp("", "ktx-claude-plugin-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	values := map[string]string{}
	for _, name := range applicationRootVariables {
		values[name] = filepath.Join(root, strings.ToLower(name))
		if err := os.MkdirAll(values[name], 0o700); err != nil {
			return err
		}
	}
	environment := verificationEnvironment(os.Environ(), values)
	isolated := func(name string, args ...string) error {
		command := exec.Command(name, args...)
		command.Env = environment
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		return command.Run()
	}

	// Not --strict: the plugin deliberately has no version, so Claude Code
	// versions it by commit, and the validator warns about that.
	plugin := filepath.Join("plugins", "ktx")
	for _, folder := range []string{".", plugin} {
		if err := isolated(claude, "plugin", "validate", folder); err != nil {
			return fmt.Errorf("claude plugin validate %s: %w", folder, err)
		}
	}
	if err := isolated(claude, "plugin", "test", plugin); err != nil {
		return fmt.Errorf("claude plugin test %s: %w", plugin, err)
	}
	return nil
}

func Vet() error {
	return run("go", "vet", "./...")
}

func Fmt() error {
	return run("gofmt", "-w", "cmd", "internal", "tools/bench/data.go", "tools/componentbench", "tools/loadbench", "tools/readleasecompare")
}

func Lint() error {
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		return errors.New("golangci-lint not found; install it: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest")
	}
	return run("golangci-lint", "run", "./...")
}

func Vuln() error {
	if _, err := exec.LookPath("govulncheck"); err != nil {
		return errors.New("govulncheck not found; install it: go install golang.org/x/vuln/cmd/govulncheck@latest")
	}
	return run("govulncheck", "./...")
}

func Check() error {
	return hermeticTarget("check", check)
}

func check() error {
	steps := []struct {
		name string
		run  func() error
	}{
		{name: "build", run: Build},
		{name: "test", run: test},
		{name: "schema-readiness-verifier", run: schemaReadinessVerifierTest},
		{name: "generated-adapter", run: generatedAdapterGate},
		{name: "opencode-package", run: opencodePackageTest},
		{name: "vet", run: Vet},
		{name: "lint", run: Lint},
	}

	for _, step := range steps {
		if err := step.run(); err != nil {
			return fmt.Errorf("%s failed: %w", step.name, err)
		}
	}
	return nil
}

// opencodePackageDir is the OpenCode package folder: its server and TUI
// targets, and the folder a checkout install points OpenCode at.
var opencodePackageDir = filepath.Join("plugins", "opencode")

// OpencodePackage installs the OpenCode package's locked dependencies and
// builds its TUI target, so plugins/opencode holds both targets ready for a
// folder install.
func OpencodePackage() error {
	if err := requireJSRuntime("bun", "to build the OpenCode package"); err != nil {
		return err
	}
	bun := jsRuntimeExecutable("bun")
	if err := runIn(opencodePackageDir, bun, "install", "--frozen-lockfile"); err != nil {
		return fmt.Errorf("install OpenCode package dependencies: %w", err)
	}
	if err := runIn(opencodePackageDir, bun, filepath.Join("scripts", "build-tui.mjs")); err != nil {
		return fmt.Errorf("build OpenCode package TUI target: %w", err)
	}
	return opencodePackageReady(opencodePackageDir)
}

// opencodePackageReady reports whether every target the package exports
// exists in dir as a non-empty file.
func opencodePackageReady(dir string) error {
	targets, problems := opencodePackageTargets(dir)
	for _, target := range targets {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(target.file)))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			problems = append(problems, fmt.Errorf("OpenCode package target %s (%s) is missing or empty; run mage opencodePackage", target.export, target.file))
		}
	}
	return errors.Join(problems...)
}

// opencodePackageTarget is a file the OpenCode package exports.
type opencodePackageTarget struct {
	export string
	// file is the target's path inside the package, without a leading ./.
	file string
}

// opencodePackageTargets reads the ./server and ./tui targets from the
// package manifest in dir. It returns a problem for each target the manifest
// does not name.
func opencodePackageTargets(dir string) ([]opencodePackageTarget, []error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, []error{fmt.Errorf("read OpenCode package manifest: %w", err)}
	}
	var manifest struct {
		Exports map[string]json.RawMessage `json:"exports"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, []error{fmt.Errorf("decode OpenCode package manifest: %w", err)}
	}
	var targets []opencodePackageTarget
	var problems []error
	for _, name := range []string{"./server", "./tui"} {
		raw, ok := manifest.Exports[name]
		if !ok {
			problems = append(problems, fmt.Errorf("OpenCode package does not export %s", name))
			continue
		}
		var target string
		if err := json.Unmarshal(raw, &target); err != nil {
			var conditions struct {
				Import string `json:"import"`
			}
			if err := json.Unmarshal(raw, &conditions); err != nil || conditions.Import == "" {
				problems = append(problems, fmt.Errorf("OpenCode package export %s names no file", name))
				continue
			}
			target = conditions.Import
		}
		targets = append(targets, opencodePackageTarget{export: name, file: filepath.ToSlash(filepath.Clean(filepath.FromSlash(target)))})
	}
	return targets, problems
}

// opencodePackageTest builds the OpenCode package, then runs its tests with
// the node and bun executables captured before the disposable HOME took
// effect: data and server tests on Node, rendering tests on Bun, the runtime
// OpenCode uses.
func opencodePackageTest() error {
	if err := requireJSRuntime("node", "to run the OpenCode package tests"); err != nil {
		return err
	}
	if err := OpencodePackage(); err != nil {
		return err
	}
	if err := opencodePackageContents(opencodePackageDir); err != nil {
		return err
	}
	if err := runIn(opencodePackageDir, jsRuntimeExecutable("node"), "--disable-warning=ExperimentalWarning", "--test",
		filepath.Join("test", "server.test.ts"), filepath.Join("test", "ktx-stats.test.ts")); err != nil {
		return err
	}
	return runIn(opencodePackageDir, jsRuntimeExecutable("bun"), "test", "--conditions=browser",
		filepath.Join("test", "mouse-interaction.test.ts"), filepath.Join("test", "sidebar-render.test.tsx"),
		filepath.Join("test", "plugin-compat.test.tsx"), filepath.Join("test", "built-tui.test.mjs"))
}

// jsRuntimeExecutable returns the captured executable for a JavaScript
// runtime, or its name for a PATH lookup.
func jsRuntimeExecutable(name string) string {
	if path := os.Getenv(jsRuntimeVariables[name]); path != "" {
		return path
	}
	return name
}

func schemaReadinessVerifierTest() error {
	return run("python3", "-m", "unittest", "-v", "tools.test_schema_readiness_evidence")
}

// GeneratedAdapterGate builds KTX and runs the required generated-adapter
// readiness check used by the authoritative repository gate: the OpenCode
// package's server target, run under Bun against the built binary.
func GeneratedAdapterGate() error {
	return hermeticTarget("generatedAdapterGate", func() error {
		if err := Build(); err != nil {
			return err
		}
		return generatedAdapterGate()
	})
}

// SchemaReleaseGate verifies a prepared production-scale schema fixture.
// Set KTX_SCHEMA_RELEASE_WORK_ROOT and KTX_SCHEMA_RELEASE_REPORT explicitly.
func SchemaReleaseGate() error {
	workRoot := strings.TrimSpace(os.Getenv("KTX_SCHEMA_RELEASE_WORK_ROOT"))
	report := strings.TrimSpace(os.Getenv("KTX_SCHEMA_RELEASE_REPORT"))
	if workRoot == "" || report == "" {
		return errors.New("schema release gate requires KTX_SCHEMA_RELEASE_WORK_ROOT and KTX_SCHEMA_RELEASE_REPORT")
	}
	if err := Build(); err != nil {
		return err
	}
	return run("python3", "tools/schema_readiness_evidence.py", "release",
		"--binary", binaryPath(), "--work-root", workRoot, "--output", report, "--repository", ".")
}

func generatedAdapterGate() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("generated-adapter gate requires Linux process and lock-owner evidence; got %s", runtime.GOOS)
	}
	binary, err := generatedAdapterGateRequirements(binaryPath())
	if err != nil {
		return err
	}
	command := exec.Command("go", "test", "-race", "./internal/db", "-run", "^TestGeneratedOpenCodeAdapterReadinessOverlap$", "-count=1", "-timeout=30s")
	command.Env = append(os.Environ(), "KTX_TEST_REQUIRE_GENERATED_ADAPTER=1", "KTX_TEST_BUILT_BINARY="+binary)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = nil
	if err := command.Run(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "ktx generated-adapter gate: required overlap check passed")
	return nil
}

func generatedAdapterGateRequirements(binary string) (string, error) {
	absolute, err := filepath.Abs(binary)
	if err != nil {
		return "", fmt.Errorf("resolve built KTX binary: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("built KTX binary is required at %s: %w", absolute, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("built KTX binary is not a regular file: %s", absolute)
	}
	if err := requireJSRuntime("bun", "for the generated-adapter gate"); err != nil {
		return "", err
	}
	return absolute, nil
}

// requireJSRuntime checks that tests can start the named runtime: the captured
// executable when one was passed in, otherwise a PATH entry.
func requireJSRuntime(name, purpose string) error {
	variable := jsRuntimeVariables[name]
	if path := os.Getenv(variable); path != "" {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%s=%s is not a %s executable; it is required %s", variable, path, name, purpose)
		}
		return nil
	}
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s not found; it is required %s", name, purpose)
	}
	return nil
}

// Verify runs the complete repository verification used locally and in CI.
func Verify() error {
	return hermeticTarget("verify", verify)
}

func verify() error {
	if err := check(); err != nil {
		return err
	}
	if err := Vuln(); err != nil {
		return fmt.Errorf("vuln failed: %w", err)
	}
	return replayFixture()
}

type accessAuditRecord struct {
	Operation string `json:"operation"`
	Forbidden bool   `json:"forbidden"`
}

func hermeticTarget(target string, body func() error) error {
	if os.Getenv(hermeticMarkerEnv) == "1" {
		if err := validateHermeticReentry(); err != nil {
			return fmt.Errorf("invalid hermetic reentry: %w", err)
		}
		return body()
	}
	caches, err := captureToolCaches()
	if err != nil {
		return err
	}
	runtimes := captureJSRuntimes()
	root, err := os.MkdirTemp("", "ktx-verify-")
	if err != nil {
		return fmt.Errorf("create disposable verification root: %w", err)
	}
	defer os.RemoveAll(root)
	directories := map[string]string{
		"HOME": filepath.Join(root, "home"), "XDG_DATA_HOME": filepath.Join(root, "data"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_CACHE_HOME": filepath.Join(root, "cache"),
		"XDG_STATE_HOME":      filepath.Join(root, "state"),
		temporaryDirectoryEnv: filepath.Join(root, "tmp"),
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create disposable verification directory: %w", err)
		}
	}
	for name, path := range caches {
		if pathWithin(root, path) {
			return fmt.Errorf("%s must remain outside disposable application root", name)
		}
	}
	for name, path := range runtimes {
		if !filepath.IsAbs(path) || pathWithin(root, path) {
			return fmt.Errorf("%s must be an absolute path outside disposable application root", name)
		}
	}
	forbidden, err := ambientDatabasePaths()
	if err != nil {
		return err
	}
	forbiddenJSON, err := json.Marshal(forbidden)
	if err != nil {
		return err
	}
	boundaryID, err := newBoundaryID()
	if err != nil {
		return err
	}
	auditPath := filepath.Join(root, "database-access.jsonl")
	capabilityPath, nonce, err := createReentryCapability(root, boundaryID)
	if err != nil {
		return err
	}
	values := map[string]string{
		hermeticMarkerEnv: "1", hermeticRootEnv: root, hermeticBoundaryIDEnv: boundaryID,
		hermeticCapabilityEnv: capabilityPath, hermeticNonceEnv: nonce,
		databaseAuditEnv: auditPath, forbiddenDBPathsEnv: string(forbiddenJSON),
	}
	for name, value := range directories {
		values[name] = value
	}
	for name, value := range caches {
		values[name] = value
	}
	for name, value := range runtimes {
		values[name] = value
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current Mage executable: %w", err)
	}
	command := exec.Command(executable, target)
	command.Env = verificationEnvironment(os.Environ(), values)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = nil
	commandErr := command.Run()
	attempts, operations, forbiddenCount, auditErr := summarizeAccessAudit(auditPath)
	fmt.Fprintf(os.Stderr, "ktx verification audit: boundary=%s target=%s attempts=%d operations=%s forbidden=%d caches=external ktx_db=unset\n",
		boundaryID, target, attempts, strings.Join(operations, ","), forbiddenCount)
	if commandErr == nil && attempts == 0 {
		auditErr = errors.New("database access audit recorded no production database path surfaces")
	}
	if forbiddenCount != 0 {
		auditErr = fmt.Errorf("database access audit recorded %d forbidden attempt(s)", forbiddenCount)
	}
	if commandErr != nil {
		if auditErr != nil {
			return fmt.Errorf("%w; access audit: %v", commandErr, auditErr)
		}
		return commandErr
	}
	return auditErr
}

type reentryCapability struct {
	BoundaryID string `json:"boundary_id"`
	Nonce      string `json:"nonce"`
}

func createReentryCapability(root, boundaryID string) (string, string, error) {
	nonce, err := newHexToken(boundaryNonceHexLength / 2)
	if err != nil {
		return "", "", fmt.Errorf("create verification nonce: %w", err)
	}
	data, err := json.Marshal(reentryCapability{BoundaryID: boundaryID, Nonce: nonce})
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(root, "reentry-capability.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", fmt.Errorf("create verification reentry capability: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return "", "", fmt.Errorf("write verification reentry capability: %w", writeErr)
	}
	if closeErr != nil {
		return "", "", fmt.Errorf("close verification reentry capability: %w", closeErr)
	}
	return path, nonce, nil
}

func validateHermeticReentry() error {
	if _, present := os.LookupEnv("KTX_DB"); present {
		return errors.New("KTX_DB must be absent")
	}
	boundaryID := os.Getenv(hermeticBoundaryIDEnv)
	if !validHexToken(boundaryID, 16) {
		return errors.New("boundary identifier must be 16 lowercase hexadecimal characters")
	}
	root := os.Getenv(hermeticRootEnv)
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("disposable root must be absolute")
	}
	if err := validateDirectory(root, "disposable root"); err != nil {
		return err
	}
	directories := map[string]bool{}
	for _, name := range applicationRootVariables {
		path := os.Getenv(name)
		if path == "" || !filepath.IsAbs(path) || !strictPathWithin(root, path) {
			return fmt.Errorf("%s must be contained under disposable root", name)
		}
		if err := validateDirectory(path, name); err != nil {
			return err
		}
		clean := filepath.Clean(path)
		if directories[clean] {
			return fmt.Errorf("application directories must be distinct: %s", name)
		}
		directories[clean] = true
	}
	temporaryDirectory := os.Getenv(temporaryDirectoryEnv)
	if temporaryDirectory == "" || !filepath.IsAbs(temporaryDirectory) || !strictPathWithin(root, temporaryDirectory) {
		return fmt.Errorf("%s must be contained under disposable root", temporaryDirectoryEnv)
	}
	if err := validateDirectory(temporaryDirectory, temporaryDirectoryEnv); err != nil {
		return err
	}
	auditPath := os.Getenv(databaseAuditEnv)
	if auditPath == "" || !filepath.IsAbs(auditPath) || !strictPathWithin(root, auditPath) {
		return errors.New("database audit path must be contained under disposable root")
	}
	if info, err := os.Lstat(auditPath); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("database audit path exists but is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect database audit path: %w", err)
	}
	if err := validateForbiddenPayload(os.Getenv(forbiddenDBPathsEnv)); err != nil {
		return err
	}
	for _, name := range toolCacheVariables {
		path := strings.TrimSpace(os.Getenv(name))
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be a nonempty absolute path", name)
		}
		if pathWithin(root, path) {
			return fmt.Errorf("%s must remain outside disposable application root", name)
		}
	}
	for _, name := range jsRuntimeVariables {
		path := os.Getenv(name)
		if path != "" && (!filepath.IsAbs(path) || pathWithin(root, path)) {
			return fmt.Errorf("%s must be an absolute path outside disposable application root", name)
		}
	}
	capabilityPath := os.Getenv(hermeticCapabilityEnv)
	nonce := os.Getenv(hermeticNonceEnv)
	if !validHexToken(nonce, boundaryNonceHexLength) {
		return errors.New("verification nonce is malformed")
	}
	if capabilityPath == "" || !filepath.IsAbs(capabilityPath) || !strictPathWithin(root, capabilityPath) {
		return errors.New("reentry capability must be contained under disposable root")
	}
	info, err := os.Lstat(capabilityPath)
	if err != nil {
		return fmt.Errorf("inspect reentry capability: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() > 512 {
		return errors.New("reentry capability has invalid type, mode, or size")
	}
	data, err := os.ReadFile(capabilityPath)
	if err != nil {
		return fmt.Errorf("read reentry capability: %w", err)
	}
	var capability reentryCapability
	if err := json.Unmarshal(data, &capability); err != nil {
		return fmt.Errorf("decode reentry capability: %w", err)
	}
	if capability.BoundaryID != boundaryID || capability.Nonce != nonce {
		return errors.New("reentry capability does not match declared boundary")
	}
	if err := os.Remove(capabilityPath); err != nil {
		return fmt.Errorf("consume reentry capability: %w", err)
	}
	return nil
}

func validateDirectory(path, name string) error {
	if path == "" {
		return fmt.Errorf("%s is empty", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be an existing non-symlink directory", name)
	}
	return nil
}

func validateForbiddenPayload(raw string) error {
	if raw == "" || len(raw) > maxForbiddenPayloadBytes {
		return errors.New("forbidden database path payload is empty or oversized")
	}
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		return fmt.Errorf("decode forbidden database paths: %w", err)
	}
	if len(paths) > maxForbiddenDatabasePaths {
		return fmt.Errorf("forbidden database path count exceeds %d", maxForbiddenDatabasePaths)
	}
	for _, path := range paths {
		if path == "" || len(path) > 2048 {
			return errors.New("forbidden database path is empty or oversized")
		}
	}
	return nil
}

func validHexToken(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func newHexToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func captureToolCaches() (map[string]string, error) {
	gocache := os.Getenv("GOCACHE")
	if gocache == "" {
		var err error
		gocache, err = output("go", "env", "GOCACHE")
		if err != nil {
			return nil, fmt.Errorf("resolve Go build cache: %w", err)
		}
	}
	gomodcache := os.Getenv("GOMODCACHE")
	if gomodcache == "" {
		var err error
		gomodcache, err = output("go", "env", "GOMODCACHE")
		if err != nil {
			return nil, fmt.Errorf("resolve Go module cache: %w", err)
		}
	}
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		cacheHome = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	bunCache := readEnv("BUN_INSTALL_CACHE_DIR", filepath.Join(cacheHome, ".bun", "install", "cache"))
	lintCache := readEnv("GOLANGCI_LINT_CACHE", filepath.Join(cacheHome, "golangci-lint"))
	return map[string]string{"GOCACHE": gocache, "GOMODCACHE": gomodcache, "BUN_INSTALL_CACHE_DIR": bunCache, "GOLANGCI_LINT_CACHE": lintCache}, nil
}

// captureJSRuntimes resolves the real node and bun executables while the
// ambient HOME is still in place. Version-manager shims can reinstall
// themselves, print setup output, or fail under the disposable HOME, so tests
// receive the executable a shim would launch. An explicit KTX_TEST_NODE or
// KTX_TEST_BUN passes through unchanged; an unavailable runtime stays unset.
func captureJSRuntimes() map[string]string {
	runtimes := map[string]string{}
	for name, variable := range jsRuntimeVariables {
		if explicit := os.Getenv(variable); explicit != "" {
			runtimes[variable] = explicit
			continue
		}
		candidate, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		text, err := exec.CommandContext(ctx, candidate, "-p", "process.execPath").Output()
		cancel()
		lines := strings.Split(strings.TrimSpace(string(text)), "\n")
		if path := lines[len(lines)-1]; err == nil && filepath.IsAbs(path) {
			runtimes[variable] = path
		}
	}
	return runtimes
}

func ambientDatabasePaths() ([]string, error) {
	paths := []string{}
	if explicit := os.Getenv("KTX_DB"); explicit != "" {
		paths = append(paths, explicit)
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home := os.Getenv("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("resolve ambient home: %w", err)
			}
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	defaultPath := filepath.Join(dataHome, "ktx", "ktx.db")
	for _, path := range paths {
		if filepath.Clean(path) == filepath.Clean(defaultPath) {
			return paths, nil
		}
	}
	return append(paths, defaultPath), nil
}

func verificationEnvironment(base []string, values map[string]string) []string {
	remove := map[string]bool{"KTX_DB": true}
	for name := range values {
		remove[name] = true
	}
	result := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if !remove[name] {
			result = append(result, entry)
		}
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		result = append(result, name+"="+values[name])
	}
	return result
}

func summarizeAccessAudit(path string) (int, []string, int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil, 0, nil
	}
	if err != nil {
		return 0, nil, 0, err
	}
	defer file.Close()
	counts := map[string]int{}
	forbidden := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record accessAuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return 0, nil, 0, fmt.Errorf("decode database access audit: %w", err)
		}
		counts[record.Operation]++
		if record.Forbidden {
			forbidden++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, nil, 0, err
	}
	operations := make([]string, 0, len(counts))
	total := 0
	for operation, count := range counts {
		operations = append(operations, fmt.Sprintf("%s:%d", operation, count))
		total += count
	}
	sort.Strings(operations)
	return total, operations, forbidden, nil
}

func newBoundaryID() (string, error) {
	identifier, err := newHexToken(8)
	if err != nil {
		return "", fmt.Errorf("create verification boundary identifier: %w", err)
	}
	return identifier, nil
}

func pathWithin(root, path string) bool {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbsolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbsolute, pathAbsolute)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func strictPathWithin(root, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbsolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbsolute, pathAbsolute)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func run(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	return command.Run()
}

// runIn is run with dir as the working directory.
func runIn(dir, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir = dir
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	return command.Run()
}

func output(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.Stderr = os.Stderr
	text, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(text)), nil
}

func readEnv(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func binaryPath() string {
	name := "ktx"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join("build", name)
}
