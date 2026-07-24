//go:build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var Default = Check

func Build() error {
	if err := os.MkdirAll("build", 0o755); err != nil {
		return err
	}

	return run("go", "build", "-buildvcs=false", "-trimpath", "-ldflags", ldflags(), "-o", binaryPath(), "./cmd/ktx")
}

func Install() error {
	return run("go", "install", "-trimpath", "-ldflags", ldflags(), "./cmd/ktx")
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
	return run("go", "test", "-race", "./...")
}

// ReplayFixture runs the bounded, offline repository gate for the retained fixture.
func ReplayFixture() error {
	return run("go", "test", "-race", "./internal/cli", "-run", "^TestRetainedReplayFixtureGate$", "-count=1", "-timeout=30s")
}

// OpencodeTelemetryFixture runs the offline OpenCode telemetry matrix.
func OpencodeTelemetryFixture() error {
	if _, err := exec.LookPath("bun"); err != nil {
		return errors.New("bun not found; it is required to execute the generated OpenCode plugin fixture")
	}
	pattern := "^Test(ValidateObservationRequiresRuntimeIdentity|ContractFixtures|CollectorCorrelatesDeduplicatesAndOrders|CollectorKeepsFirstObservationAuthoritative|SessionTelemetryPersistsSeparatelyAndListsMeasurements|SessionTelemetryDuplicateConcurrentAndAtomicAssociation|SessionTelemetryRuntimeQualifiedIdentityAndFirstEvidence|SessionTelemetryConcurrentConflictHasOneCompleteWinner|SessionTelemetrySchemaV5FreshAndHistorical|SessionTelemetryV4MigrationPreservesAllEvidence|SessionTelemetryV5MigrationRollsBack|TelemetryRecordAndBoundedJSONInspection|TelemetryInspectionOrdersRuntimeNamespacesAndShowsProvenance|TelemetryRecordRequiresEveryNumericFieldAndPreservesZeros|TelemetryCommandDatabaseFailuresFailOpenOnlyForRecord|TelemetryRejectsMalformedAndFailsOpenOnPersistenceError|OpencodeGeneratedAdapterEventAndToolIntegration)$"
	return run("go", "test", "-race", "./internal/telemetry/...", "./internal/db", "./internal/cli", "-run", pattern, "-count=1", "-timeout=30s")
}

func Vet() error {
	return run("go", "vet", "./...")
}

func Fmt() error {
	return run("gofmt", "-w", "cmd", "internal", "tools/bench/data.go")
}

func Lint() error {
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		return errors.New("golangci-lint not found; install it: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest")
	}
	return run("golangci-lint", "run", "./...")
}

func Check() error {
	steps := []struct {
		name string
		run  func() error
	}{
		{name: "test", run: Test},
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
	if err := Check(); err != nil {
		return err
	}
	return ReplayFixture()
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
