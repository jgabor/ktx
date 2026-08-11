//go:build mage

package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

var Default = Check

const (
	hermeticMarkerEnv         = "KTX_VERIFY_HERMETIC"
	hermeticRootEnv           = "KTX_VERIFY_ROOT"
	hermeticBoundaryIDEnv     = "KTX_VERIFY_BOUNDARY_ID"
	hermeticCapabilityEnv     = "KTX_VERIFY_CAPABILITY"
	hermeticNonceEnv          = "KTX_VERIFY_NONCE"
	databaseAuditEnv          = "KTX_VERIFY_DB_AUDIT"
	forbiddenDBPathsEnv       = "KTX_VERIFY_FORBIDDEN_DB_PATHS"
	temporaryDirectoryEnv     = "TMPDIR"
	boundaryNonceHexLength    = 32
	maxForbiddenPayloadBytes  = 8192
	maxForbiddenDatabasePaths = 16
	engineeringEvidenceDirEnv = "KTX_ENGINEERING_EVIDENCE_DIR"
	engineeringManifestFile   = "manifest.json"
	engineeringManifestSchema = 2
)

var applicationRootVariables = []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"}
var toolCacheVariables = []string{"GOCACHE", "GOMODCACHE", "BUN_INSTALL_CACHE_DIR", "GOLANGCI_LINT_CACHE"}

type engineeringReportSpec struct {
	name         string
	target       string
	file         string
	identityKeys []string
}

var engineeringReportSpecs = []engineeringReportSpec{
	{name: "component", target: "componentBench", file: "component-bench.json", identityKeys: []string{"machine", "workloads", "benchmarks"}},
	{name: "load", target: "loadBench", file: "real-process-load.json", identityKeys: []string{"environment", "payloads", "measurements", "scenarios"}},
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

func Install() error {
	return run("go", "install", "-buildvcs=false", "-trimpath", "-ldflags", ldflags(), "./cmd/ktx")
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
	return run("go", "test", "-race", "-tags=mage", "./...")
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
	if schemaVersion != engineeringManifestSchema {
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
	if _, err := exec.LookPath("bun"); err != nil {
		return errors.New("bun not found; it is required to execute the generated OpenCode plugin fixture")
	}
	pattern := "^Test(ValidateObservationRequiresRuntimeIdentity|ValidateObservationPreservesMeasurementStates|ContractFixtures|CollectorCorrelatesDeduplicatesAndOrders|CollectorKeepsFirstObservationAuthoritative|HookAndWrapperModelStepsNormalizeWithParity|SessionTelemetryPersistsSeparatelyAndListsMeasurements|SessionTelemetryPersistsUnknownAndMalformedFields|SessionTelemetryDuplicateConcurrentAndAtomicAssociation|SessionTelemetryRuntimeQualifiedIdentityAndFirstEvidence|SessionTelemetryConcurrentConflictHasOneCompleteWinner|SessionTelemetrySchemaV6FreshAndHistorical|SessionTelemetryV4MigrationPreservesAllEvidence|SessionTelemetryV6MigrationRollsBack|TelemetryRecordAndBoundedJSONInspection|TelemetryInspectionOrdersRuntimeNamespacesAndShowsProvenance|TelemetryRecordDistinguishesUnavailableFieldsAndPreservesZeros|TelemetryExplicitStatesPreserveCrossIngressFirstEvidence|TelemetryCommandDatabaseFailuresFailOpenOnlyForRecord|TelemetryRejectsMalformedAndFailsOpenOnPersistenceError|WrapperRetainsModelStepsSeparatelyWithFirstEvidenceAuthority|OpencodeGeneratedAdapterEventAndToolIntegration)$"
	return run("go", "test", "-race", "./internal/telemetry/...", "./internal/db", "./internal/cli", "-run", pattern, "-count=1", "-timeout=30s")
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
		{name: "test", run: test},
		{name: "vet", run: Vet},
		{name: "lint", run: Lint},
		{name: "build", run: Build},
	}

	for _, step := range steps {
		if err := step.run(); err != nil {
			return fmt.Errorf("%s failed: %w", step.name, err)
		}
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
