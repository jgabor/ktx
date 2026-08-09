// loadbench measures real ktx processes against disposable databases.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

const (
	reportSchemaVersion = 1
	warmupRounds        = 1
	measuredRounds      = 5
	streamEvents        = 8
	processTimeout      = 12 * time.Second
	foreignHoldDuration = 250 * time.Millisecond
	terminationDelay    = 100 * time.Millisecond
	aliasWriterCount    = 8
	streamWorkerEnv     = "KTX_LOADBENCH_STREAM_WORKER"
	streamPathEnv       = "KTX_LOADBENCH_STREAM_PATH"
)

var concurrencyLevels = []int{1, 2, 4, 8, 16, 32}

type report struct {
	SchemaVersion  int                 `json:"schema_version"`
	WarmupRounds   int                 `json:"warmup_rounds"`
	MeasuredRounds int                 `json:"measured_rounds"`
	Environment    environmentIdentity `json:"environment"`
	Payloads       []payloadIdentity   `json:"payloads"`
	Measurements   []measurement       `json:"measurements"`
	Scenarios      []scenario          `json:"scenarios"`
	Preflight      preflight           `json:"preflight"`
}

type environmentIdentity struct {
	KTXPath               string   `json:"ktx_path"`
	KTXSHA256             string   `json:"ktx_sha256"`
	GoVersion             string   `json:"go_version"`
	OperatingSystem       string   `json:"operating_system"`
	Architecture          string   `json:"architecture"`
	Processor             string   `json:"processor"`
	DatabaseSelection     string   `json:"database_selection"`
	DisposableEnvironment []string `json:"disposable_environment"`
}

type preflight struct {
	AmbientKTXDB string `json:"ambient_ktx_db"`
	Fixtures     string `json:"fixtures"`
}

type payloadIdentity struct {
	Name          string `json:"name"`
	Bytes         int    `json:"bytes"`
	GeneratorSeed uint64 `json:"generator_seed"`
	SHA256        string `json:"sha256"`
}

type fixture struct {
	payloadIdentity
	data []byte
}

type measurement struct {
	Surface      string  `json:"surface"`
	Concurrency  int     `json:"concurrency"`
	StreamEvents int     `json:"stream_events,omitempty"`
	Warmup       round   `json:"warmup"`
	Rounds       []round `json:"rounds"`
}

type round struct {
	FreshOwnedDatabase bool           `json:"fresh_owned_database"`
	TimeoutNanoseconds int64          `json:"timeout_nanoseconds"`
	BootstrapToolCalls int            `json:"bootstrap_tool_calls"`
	WriterAttempts     int            `json:"writer_attempts"`
	WriterCompletions  int            `json:"writer_completions"`
	Participants       []participant  `json:"participants"`
	Metrics            metrics        `json:"metrics"`
	Reconciliation     reconciliation `json:"reconciliation"`
	Outcome            string         `json:"outcome"`
}

type scenario struct {
	Name               string         `json:"name"`
	FreshOwnedDatabase bool           `json:"fresh_owned_database"`
	TimeoutNanoseconds int64          `json:"timeout_nanoseconds"`
	BootstrapToolCalls int            `json:"bootstrap_tool_calls"`
	Participants       []participant  `json:"participants"`
	Metrics            metrics        `json:"metrics"`
	Reconciliation     reconciliation `json:"reconciliation"`
	Outcome            string         `json:"outcome"`
}

type participant struct {
	Class       string `json:"class"`
	Role        string `json:"role"`
	Outcome     string `json:"outcome"`
	Attempts    int    `json:"attempts,omitempty"`
	Completions int    `json:"completions,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
	FailureCode string `json:"failure_code,omitempty"`
	LatencyNS   int64  `json:"latency_nanoseconds"`
}

type metrics struct {
	ThroughputOperationsPerSecond float64        `json:"throughput_operations_per_second"`
	P50LatencyNanoseconds         float64        `json:"p50_latency_nanoseconds"`
	P95LatencyNanoseconds         float64        `json:"p95_latency_nanoseconds"`
	P99LatencyNanoseconds         float64        `json:"p99_latency_nanoseconds"`
	MaximumLatencyNanoseconds     int64          `json:"maximum_latency_nanoseconds"`
	Retries                       int            `json:"retries"`
	RetryScope                    string         `json:"retry_scope"`
	CumulativeLockWaitNanoseconds int64          `json:"cumulative_lock_wait_nanoseconds"`
	LockWaitScope                 string         `json:"lock_wait_scope"`
	FailureCodes                  map[string]int `json:"failure_codes"`
}

type reconciliation struct {
	ExpectedMinimum int  `json:"expected_minimum"`
	ExpectedMaximum int  `json:"expected_maximum"`
	Persisted       int  `json:"persisted"`
	Matches         bool `json:"matches"`
}

type processRunner struct {
	binary string
}

type processResult struct {
	ExitCode  int
	Code      string
	LatencyNS int64
	TimedOut  bool
	stdout    []byte
	stderr    []byte
}

type roundEnvironment struct {
	root string
	db   string
	env  []string
}

type foreignHolder struct {
	release chan struct{}
	done    chan error
	once    sync.Once
}

func main() {
	if os.Getenv(streamWorkerEnv) == "1" {
		if err := streamWorker(); err != nil {
			fmt.Fprintf(os.Stderr, "loadbench worker: %v\n", err)
			os.Exit(1)
		}
		return
	}

	measured, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loadbench: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(measured); err != nil {
		fmt.Fprintf(os.Stderr, "loadbench: encode report: %v\n", err)
		os.Exit(1)
	}
}

func streamWorker() error {
	path := os.Getenv(streamPathEnv)
	if path == "" {
		return errors.New("missing stream path")
	}
	stream, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	_, err = os.Stdout.Write(stream)
	return err
}

func run() (report, error) {
	fixtures := fixedFixtures()
	if err := validateStartup(os.Getenv("KTX_DB"), fixtures); err != nil {
		return report{}, err
	}

	root, err := os.MkdirTemp("", "ktx-load-bench-")
	if err != nil {
		return report{}, fmt.Errorf("create benchmark root: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()

	return withOwnedEnvironment(root, func() (report, error) {
		binary, hash, err := localBinary()
		if err != nil {
			return report{}, err
		}
		runner := processRunner{binary: binary}
		measured := report{
			SchemaVersion:  reportSchemaVersion,
			WarmupRounds:   warmupRounds,
			MeasuredRounds: measuredRounds,
			Environment: environmentIdentity{
				KTXPath:               binary,
				KTXSHA256:             hash,
				GoVersion:             runtime.Version(),
				OperatingSystem:       runtime.GOOS,
				Architecture:          runtime.GOARCH,
				Processor:             processorIdentity(),
				DatabaseSelection:     "explicit KTX_DB beneath a fresh owned round root",
				DisposableEnvironment: []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"},
			},
			Preflight: preflight{AmbientKTXDB: "rejected when set", Fixtures: "validated before workers start"},
		}
		measured.Payloads = make([]payloadIdentity, len(fixtures))
		for index, fixture := range fixtures {
			measured.Payloads[index] = fixture.payloadIdentity
		}

		hooks, err := runHookMeasurements(root, runner, fixtures[0])
		if err != nil {
			return report{}, err
		}
		measured.Measurements = append(measured.Measurements, hooks...)
		wrappers, err := runWrapperMeasurement(root, runner, fixtures[1])
		if err != nil {
			return report{}, err
		}
		measured.Measurements = append(measured.Measurements, wrappers)

		scenarios, err := runScenarios(root, runner, fixtures[0])
		if err != nil {
			return report{}, err
		}
		measured.Scenarios = scenarios
		if err := validateMandatoryLoadReport(measured); err != nil {
			return report{}, err
		}
		return measured, nil
	})
}

func validateStartup(ambient string, fixtures []fixture) error {
	if ambient != "" {
		return fmt.Errorf("refuse ambient KTX_DB %q; unset it before starting the load harness", ambient)
	}
	return validateFixtures(fixtures)
}

func validateFixtures(fixtures []fixture) error {
	if len(fixtures) != 2 {
		return fmt.Errorf("fixture count = %d, want 2", len(fixtures))
	}
	seen := make(map[string]bool, len(fixtures))
	for _, fixture := range fixtures {
		if fixture.Name == "" || fixture.GeneratorSeed == 0 || fixture.Bytes != len(fixture.data) || seen[fixture.Name] {
			return fmt.Errorf("invalid fixture %q", fixture.Name)
		}
		seen[fixture.Name] = true
		sum := sha256.Sum256(fixture.data)
		if fixture.SHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("fixture %q hash does not match data", fixture.Name)
		}
	}
	return nil
}

func validateMandatoryLoadReport(measured report) error {
	if measured.SchemaVersion != reportSchemaVersion || measured.WarmupRounds != warmupRounds || measured.MeasuredRounds != measuredRounds {
		return fmt.Errorf("report contract schema=%d warmup=%d measured=%d", measured.SchemaVersion, measured.WarmupRounds, measured.MeasuredRounds)
	}

	seenConcurrency := make(map[int]bool, len(concurrencyLevels))
	seenStream := false
	for _, measurement := range measured.Measurements {
		if err := validateMandatoryRound(measurement.Warmup); err != nil {
			return fmt.Errorf("%s warmup: %w", measurement.Surface, err)
		}
		if len(measurement.Rounds) != measuredRounds {
			return fmt.Errorf("%s measured rounds=%d, want %d", measurement.Surface, len(measurement.Rounds), measuredRounds)
		}
		for index, round := range measurement.Rounds {
			if err := validateMandatoryRound(round); err != nil {
				return fmt.Errorf("%s round %d: %w", measurement.Surface, index+1, err)
			}
		}

		switch measurement.Surface {
		case "one-shot-hook":
			if !containsConcurrency(concurrencyLevels, measurement.Concurrency) || seenConcurrency[measurement.Concurrency] {
				return fmt.Errorf("unexpected one-shot-hook concurrency %d", measurement.Concurrency)
			}
			seenConcurrency[measurement.Concurrency] = true
			if err := validateMixedReadRounds(measurement); err != nil {
				return err
			}
		case "sustained-wrapper-stream":
			if seenStream || measurement.StreamEvents != streamEvents {
				return fmt.Errorf("invalid sustained wrapper measurement")
			}
			seenStream = true
		default:
			return fmt.Errorf("unexpected measurement surface %q", measurement.Surface)
		}
	}
	for _, concurrency := range concurrencyLevels {
		if !seenConcurrency[concurrency] {
			return fmt.Errorf("missing one-shot-hook concurrency %d", concurrency)
		}
	}
	if !seenStream {
		return fmt.Errorf("missing sustained-wrapper-stream measurement")
	}
	return validateMandatoryScenarios(measured.Scenarios)
}

func validateMandatoryRound(round round) error {
	if !round.FreshOwnedDatabase || round.WriterAttempts <= 0 || round.WriterCompletions != round.WriterAttempts {
		return fmt.Errorf("owned database or writer completion invariant failed")
	}
	if !round.Reconciliation.Matches {
		return fmt.Errorf("reconciliation mismatch")
	}
	if len(round.Metrics.FailureCodes) != 0 || round.Outcome != "completed" {
		return fmt.Errorf("supported operation reported failures")
	}
	for _, participant := range round.Participants {
		if participant.Class == "supported-ktx" && participant.FailureCode != "" {
			return fmt.Errorf("supported participant %q reported %q", participant.Role, participant.FailureCode)
		}
	}
	return nil
}

func validateMixedReadRounds(measurement measurement) error {
	rounds := append([]round{measurement.Warmup}, measurement.Rounds...)
	for index, round := range rounds {
		if !hasParticipant(round.Participants, "supported-ktx", "one-shot-hook-writers", "completed", "") ||
			!hasParticipant(round.Participants, "supported-ktx", "read-only-query", "ok", "") {
			return fmt.Errorf("one-shot-hook concurrency %d round %d lacks successful writer/read pair", measurement.Concurrency, index)
		}
	}
	return nil
}

func validateMandatoryScenarios(scenarios []scenario) error {
	if len(scenarios) != 7 {
		return fmt.Errorf("scenario count=%d, want 7", len(scenarios))
	}
	byName := make(map[string]scenario, len(scenarios))
	for _, scenario := range scenarios {
		if _, exists := byName[scenario.Name]; exists {
			return fmt.Errorf("duplicate scenario %q", scenario.Name)
		}
		if !scenario.FreshOwnedDatabase || !scenario.Reconciliation.Matches {
			return fmt.Errorf("scenario %q lost ownership or reconciliation", scenario.Name)
		}
		byName[scenario.Name] = scenario
	}
	for _, name := range []string{
		"supported-writer-path-alias", "foreign-held-reader", "foreign-held-writer", "supported-compatible-migration-race",
	} {
		scenario, ok := byName[name]
		if !ok {
			return fmt.Errorf("missing scenario %q", name)
		}
		if len(scenario.Metrics.FailureCodes) != 0 || scenario.Outcome != "completed" {
			return fmt.Errorf("scenario %q reported an unexpected failure", name)
		}
	}

	foreign, ok := byName["foreign-writer-reader-exhaustion"]
	if !ok || !onlyFailure(foreign.Metrics, "database-unavailable") || foreign.Outcome != "completed-with-classified-failures" ||
		!hasParticipant(foreign.Participants, "supported-ktx", "read-only-db-report", "database-unavailable", "database-unavailable") {
		return fmt.Errorf("foreign writer/read-only exhaustion did not retain the typed availability result")
	}
	for _, participant := range foreign.Participants {
		if participant.Role == "read-only-db-report" && participant.ExitCode != 4 {
			return fmt.Errorf("foreign writer/read-only exhaustion exit=%d, want 4", participant.ExitCode)
		}
	}

	duplicate, ok := byName["supported-duplicate-delivery"]
	if !ok || duplicate.Metrics.Retries <= 0 || len(duplicate.Metrics.FailureCodes) != 0 || duplicate.Outcome != "completed" {
		return fmt.Errorf("duplicate delivery did not converge through a retry")
	}

	termination, ok := byName["supported-process-termination-ownership-recovery"]
	if !ok || !onlyFailure(termination.Metrics, "terminated") || termination.Outcome != "completed-with-classified-failures" ||
		!hasParticipant(termination.Participants, "supported-ktx", "terminated-one-shot-hook", "terminated", "terminated") ||
		!hasParticipant(termination.Participants, "supported-ktx", "recovery-one-shot-hook", "ok", "") {
		return fmt.Errorf("process termination did not recover supported ownership")
	}
	return nil
}

func containsConcurrency(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func onlyFailure(measured metrics, code string) bool {
	return len(measured.FailureCodes) == 1 && measured.FailureCodes[code] == 1
}

func hasParticipant(participants []participant, class, role, outcome, failureCode string) bool {
	for _, participant := range participants {
		if participant.Class == class && participant.Role == role && participant.Outcome == outcome && participant.FailureCode == failureCode {
			return true
		}
	}
	return false
}

func fixedFixtures() []fixture {
	definitions := []struct {
		name string
		seed uint64
		size int
	}{
		{name: "hook-output-v1", seed: 6001, size: 4096},
		{name: "wrapper-stream-output-v1", seed: 6002, size: 1024},
	}
	fixtures := make([]fixture, 0, len(definitions))
	for _, definition := range definitions {
		data := generatedPayload(definition.size, definition.seed)
		sum := sha256.Sum256(data)
		fixtures = append(fixtures, fixture{
			payloadIdentity: payloadIdentity{
				Name:          definition.name,
				Bytes:         len(data),
				GeneratorSeed: definition.seed,
				SHA256:        hex.EncodeToString(sum[:]),
			},
			data: data,
		})
	}
	return fixtures
}

func generatedPayload(size int, seed uint64) []byte {
	data := make([]byte, size)
	for index := range data {
		seed = seed*6364136223846793005 + 1442695040888963407
		if index%79 == 78 {
			data[index] = '\n'
			continue
		}
		data[index] = 'a' + byte((seed>>32)%26)
	}
	return data
}

func withOwnedEnvironment(root string, fn func() (report, error)) (report, error) {
	directories := map[string]string{
		"HOME":            filepath.Join(root, "home"),
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_CACHE_HOME":  filepath.Join(root, "cache"),
		"XDG_STATE_HOME":  filepath.Join(root, "state"),
		"TMPDIR":          filepath.Join(root, "tmp"),
		"KTX_VERIFY_ROOT": root,
	}
	for _, path := range directories {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return report{}, fmt.Errorf("create disposable environment: %w", err)
		}
	}

	previous := make(map[string]*string, len(directories)+2)
	for name, value := range directories {
		previous[name] = environmentValue(name)
		if err := os.Setenv(name, value); err != nil {
			return report{}, err
		}
	}
	for _, name := range []string{"KTX_DB", "KTX_SESSION_ID"} {
		previous[name] = environmentValue(name)
		if err := os.Unsetenv(name); err != nil {
			return report{}, err
		}
	}
	defer restoreEnvironment(previous)
	return fn()
}

func environmentValue(name string) *string {
	value, present := os.LookupEnv(name)
	if !present {
		return nil
	}
	return &value
}

func restoreEnvironment(values map[string]*string) {
	for name, value := range values {
		if value == nil {
			_ = os.Unsetenv(name)
			continue
		}
		_ = os.Setenv(name, *value)
	}
}

func localBinary() (string, string, error) {
	path, err := filepath.Abs(filepath.Join("build", binaryName()))
	if err != nil {
		return "", "", fmt.Errorf("resolve local ktx binary: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("local ktx binary %q is unavailable; run mage build first: %w", path, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("local ktx binary %q is a directory", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("hash local ktx binary: %w", err)
	}
	sum := sha256.Sum256(body)
	return path, hex.EncodeToString(sum[:]), nil
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "ktx.exe"
	}
	return "ktx"
}

func runHookMeasurements(root string, runner processRunner, fixture fixture) ([]measurement, error) {
	measurements := make([]measurement, 0, len(concurrencyLevels))
	for _, concurrency := range concurrencyLevels {
		measurement := measurement{Surface: "one-shot-hook", Concurrency: concurrency}
		for roundIndex := 0; roundIndex < warmupRounds+measuredRounds; roundIndex++ {
			result, err := runHookRound(root, runner, fixture, concurrency, roundIndex)
			if err != nil {
				return nil, err
			}
			if roundIndex < warmupRounds {
				measurement.Warmup = result
			} else {
				measurement.Rounds = append(measurement.Rounds, result)
			}
		}
		measurements = append(measurements, measurement)
	}
	return measurements, nil
}

func runHookRound(root string, runner processRunner, fixture fixture, concurrency, roundIndex int) (round, error) {
	environment, err := newRoundEnvironment(root, fmt.Sprintf("hook-%02d-%02d", concurrency, roundIndex))
	if err != nil {
		return round{}, err
	}
	setup := runner.hook(environment.env, hookPayload(stableID(100000+concurrency*100+roundIndex), fixture))
	if setup.Code != "ok" {
		return round{}, fmt.Errorf("initialize hook round %d/%d: %s", concurrency, roundIndex, setup.Code)
	}

	start := time.Now()
	results, reader := concurrentHooks(runner, environment, fixture, concurrency, 200000+concurrency*1000+roundIndex*100)
	elapsed := time.Since(start)
	persisted, finalQuery, err := runner.persistedCount(environment.env)
	if err != nil {
		return round{}, fmt.Errorf("reconcile hook round %d/%d: %w", concurrency, roundIndex, err)
	}

	completed := completed(results)
	metrics := summarize(results, completed, elapsed, 0)
	addFailure(metrics.FailureCodes, reader)
	addFailure(metrics.FailureCodes, finalQuery)
	reconciliation := exactReconciliation(1+concurrency, persisted)
	if !reconciliation.Matches {
		metrics.FailureCodes["reconciliation-mismatch"]++
	}
	return round{
		FreshOwnedDatabase: true,
		TimeoutNanoseconds: int64(processTimeout),
		BootstrapToolCalls: 1,
		WriterAttempts:     concurrency,
		WriterCompletions:  completed,
		Participants: []participant{
			aggregateParticipant("supported-ktx", "one-shot-hook-writers", results),
			participantFromResult("supported-ktx", "read-only-query", reader),
			participantFromResult("supported-ktx", "reconciliation-query", finalQuery),
		},
		Metrics:        metrics,
		Reconciliation: reconciliation,
		Outcome:        measurementOutcome(metrics, reconciliation),
	}, nil
}

func runWrapperMeasurement(root string, runner processRunner, fixture fixture) (measurement, error) {
	measurement := measurement{Surface: "sustained-wrapper-stream", Concurrency: 1, StreamEvents: streamEvents}
	for roundIndex := 0; roundIndex < warmupRounds+measuredRounds; roundIndex++ {
		result, err := runWrapperRound(root, runner, fixture, roundIndex)
		if err != nil {
			return measurement, err
		}
		if roundIndex < warmupRounds {
			measurement.Warmup = result
		} else {
			measurement.Rounds = append(measurement.Rounds, result)
		}
	}
	return measurement, nil
}

func runWrapperRound(root string, runner processRunner, fixture fixture, roundIndex int) (round, error) {
	environment, err := newRoundEnvironment(root, fmt.Sprintf("wrapper-%02d", roundIndex))
	if err != nil {
		return round{}, err
	}
	setup := runner.hook(environment.env, hookPayload(stableID(300000+roundIndex), fixture))
	if setup.Code != "ok" {
		return round{}, fmt.Errorf("initialize wrapper round %d: %s", roundIndex, setup.Code)
	}
	stream, err := wrapperStream(fixture, streamEvents)
	if err != nil {
		return round{}, err
	}
	streamPath := filepath.Join(environment.root, "events.jsonl")
	if err := os.WriteFile(streamPath, stream, 0o600); err != nil {
		return round{}, fmt.Errorf("write wrapper stream: %w", err)
	}

	start := time.Now()
	results, reader := concurrentWrapperAndQuery(runner, environment, streamPath)
	elapsed := time.Since(start)
	persisted, finalQuery, err := runner.persistedCount(environment.env)
	if err != nil {
		return round{}, fmt.Errorf("reconcile wrapper round %d: %w", roundIndex, err)
	}

	completedOperations := 0
	if results[0].Code == "ok" {
		completedOperations = streamEvents
	}
	metrics := summarize(results, completedOperations, elapsed, 0)
	addFailure(metrics.FailureCodes, reader)
	addFailure(metrics.FailureCodes, finalQuery)
	reconciliation := exactReconciliation(1+streamEvents, persisted)
	if !reconciliation.Matches {
		metrics.FailureCodes["reconciliation-mismatch"]++
	}
	return round{
		FreshOwnedDatabase: true,
		TimeoutNanoseconds: int64(processTimeout),
		BootstrapToolCalls: 1,
		WriterAttempts:     streamEvents,
		WriterCompletions:  completedOperations,
		Participants: []participant{
			aggregateParticipant("supported-ktx", "sustained-wrapper-stream", results),
			participantFromResult("supported-ktx", "read-only-query", reader),
			participantFromResult("supported-ktx", "reconciliation-query", finalQuery),
		},
		Metrics:        metrics,
		Reconciliation: reconciliation,
		Outcome:        measurementOutcome(metrics, reconciliation),
	}, nil
}

func runScenarios(root string, runner processRunner, payload fixture) ([]scenario, error) {
	results := make([]scenario, 0, 7)
	for _, operation := range []struct {
		name string
		run  func(string, processRunner, fixture) (scenario, error)
	}{
		{name: "writer-path-alias", run: runWriterPathAliasScenario},
		{name: "held-reader", run: runHeldReaderScenario},
		{name: "held-writer", run: runHeldWriterScenario},
		{name: "foreign-writer-reader-exhaustion", run: runForeignWriterReaderExhaustionScenario},
		{name: "compatible-migration-race", run: runMigrationRaceScenario},
		{name: "duplicate-delivery", run: runDuplicateDeliveryScenario},
		{name: "process-termination", run: runProcessTerminationScenario},
	} {
		result, err := operation.run(root, runner, payload)
		if err != nil {
			return nil, fmt.Errorf("run %s scenario: %w", operation.name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func runForeignWriterReaderExhaustionScenario(root string, runner processRunner, fixture fixture) (scenario, error) {
	environment, err := newRoundEnvironment(root, "foreign-writer-reader-exhaustion")
	if err != nil {
		return scenario{}, err
	}
	setup := runner.hook(environment.env, hookPayload(stableID(450000), fixture))
	if setup.Code != "ok" {
		return scenario{}, fmt.Errorf("initialize foreign writer reader exhaustion scenario: %s", setup.Code)
	}
	holder, err := startForeignHolder(environment.db, "writer")
	if err != nil {
		return scenario{}, err
	}
	start := time.Now()
	reader := runner.query(environment.env)
	elapsed := time.Since(start)
	holder.Release()
	if err := holder.Wait(); err != nil {
		return scenario{}, err
	}
	if reader.Code != "database-unavailable" {
		return scenario{}, fmt.Errorf("foreign writer read-only result = %s, want database-unavailable", reader.Code)
	}
	persisted, query, err := runner.persistedCount(environment.env)
	if err != nil {
		return scenario{}, err
	}
	metrics := summarize([]processResult{reader}, 0, elapsed, 0)
	addFailure(metrics.FailureCodes, query)
	reconciliation := exactReconciliation(1, persisted)
	if !reconciliation.Matches {
		metrics.FailureCodes["reconciliation-mismatch"]++
	}
	return scenario{
		Name:               "foreign-writer-reader-exhaustion",
		FreshOwnedDatabase: true,
		TimeoutNanoseconds: int64(processTimeout),
		BootstrapToolCalls: 1,
		Participants: []participant{
			{Class: "foreign-sqlite", Role: "held-writer-beyond-supported-wait", Outcome: "released", LatencyNS: elapsed.Nanoseconds()},
			participantFromResult("supported-ktx", "read-only-db-report", reader),
			participantFromResult("supported-ktx", "reconciliation-query", query),
		},
		Metrics:        metrics,
		Reconciliation: reconciliation,
		Outcome:        measurementOutcome(metrics, reconciliation),
	}, nil
}

func runWriterPathAliasScenario(root string, runner processRunner, fixture fixture) (scenario, error) {
	environment, err := newRoundEnvironment(root, "writer-path-alias")
	if err != nil {
		return scenario{}, err
	}
	setup := runner.hook(environment.env, hookPayload(stableID(350000), fixture))
	if setup.Code != "ok" {
		return scenario{}, fmt.Errorf("initialize writer path alias scenario: %s", setup.Code)
	}
	alias := filepath.Join(environment.root, "db", "ktx-alias.db")
	if err := os.Link(environment.db, alias); err != nil {
		return scenario{}, fmt.Errorf("create database path alias: %w", err)
	}

	start := time.Now()
	workers := concurrentHooksAcrossPaths(runner, environment.env, workerEnvironment(environment.root, alias), fixture, aliasWriterCount, 350100)
	elapsed := time.Since(start)
	persisted, query, err := runner.persistedCount(environment.env)
	if err != nil {
		return scenario{}, err
	}
	metrics := summarize(workers, completed(workers), elapsed, 0)
	addFailure(metrics.FailureCodes, query)
	reconciliation := exactReconciliation(1+len(workers), persisted)
	if !reconciliation.Matches {
		metrics.FailureCodes["reconciliation-mismatch"]++
	}
	return scenario{
		Name:               "supported-writer-path-alias",
		FreshOwnedDatabase: true,
		TimeoutNanoseconds: int64(processTimeout),
		BootstrapToolCalls: 1,
		Participants: []participant{
			aggregateParticipant("supported-ktx", "canonical-and-alias-one-shot-hook-writers", workers),
			participantFromResult("supported-ktx", "reconciliation-query", query),
		},
		Metrics:        metrics,
		Reconciliation: reconciliation,
		Outcome:        measurementOutcome(metrics, reconciliation),
	}, nil
}

func runHeldReaderScenario(root string, runner processRunner, fixture fixture) (scenario, error) {
	return runHeldScenario(root, runner, fixture, "reader")
}

func runHeldWriterScenario(root string, runner processRunner, fixture fixture) (scenario, error) {
	return runHeldScenario(root, runner, fixture, "writer")
}

func runHeldScenario(root string, runner processRunner, fixture fixture, holderRole string) (scenario, error) {
	environment, err := newRoundEnvironment(root, "held-"+holderRole)
	if err != nil {
		return scenario{}, err
	}
	setup := runner.hook(environment.env, hookPayload(stableID(400000), fixture))
	if setup.Code != "ok" {
		return scenario{}, fmt.Errorf("initialize held %s scenario: %s", holderRole, setup.Code)
	}
	holder, err := startForeignHolder(environment.db, holderRole)
	if err != nil {
		return scenario{}, err
	}
	timer := time.AfterFunc(foreignHoldDuration, holder.Release)
	start := time.Now()
	worker := runner.hook(environment.env, hookPayload(stableID(400100), fixture))
	elapsed := time.Since(start)
	timer.Stop()
	holder.Release()
	if err := holder.Wait(); err != nil {
		return scenario{}, err
	}
	persisted, query, err := runner.persistedCount(environment.env)
	if err != nil {
		return scenario{}, err
	}

	metrics := summarize([]processResult{worker}, completed([]processResult{worker}), elapsed, 0)
	addObservedLockWait(&metrics, foreignHoldDuration)
	addFailure(metrics.FailureCodes, query)
	reconciliation := exactReconciliation(2, persisted)
	if !reconciliation.Matches {
		metrics.FailureCodes["reconciliation-mismatch"]++
	}
	return scenario{
		Name:               "foreign-held-" + holderRole,
		FreshOwnedDatabase: true,
		TimeoutNanoseconds: int64(processTimeout),
		BootstrapToolCalls: 1,
		Participants: []participant{
			{Class: "foreign-sqlite", Role: "held-" + holderRole, Outcome: "released", LatencyNS: int64(foreignHoldDuration)},
			participantFromResult("supported-ktx", "one-shot-hook-writer", worker),
			participantFromResult("supported-ktx", "reconciliation-query", query),
		},
		Metrics:        metrics,
		Reconciliation: reconciliation,
		Outcome:        measurementOutcome(metrics, reconciliation),
	}, nil
}

func runMigrationRaceScenario(root string, runner processRunner, fixture fixture) (scenario, error) {
	environment, err := newRoundEnvironment(root, "migration-race")
	if err != nil {
		return scenario{}, err
	}
	setup := runner.hook(environment.env, hookPayload(stableID(499900), fixture))
	if setup.Code != "ok" {
		return scenario{}, fmt.Errorf("initialize migration race scenario: %s", setup.Code)
	}
	if err := seedStaleCompatibleDatabase(environment.db); err != nil {
		return scenario{}, err
	}
	start := time.Now()
	workers := concurrentHookOnly(runner, environment, fixture, 2, 500000)
	elapsed := time.Since(start)
	persisted, query, err := runner.persistedCount(environment.env)
	if err != nil {
		return scenario{}, err
	}
	if err := verifyMigratedDatabase(environment.db); err != nil {
		return scenario{}, err
	}
	metrics := summarize(workers, completed(workers), elapsed, 0)
	addFailure(metrics.FailureCodes, query)
	reconciliation := exactReconciliation(1+len(workers), persisted)
	if !reconciliation.Matches {
		metrics.FailureCodes["reconciliation-mismatch"]++
	}
	return scenario{
		Name:               "supported-compatible-migration-race",
		FreshOwnedDatabase: true,
		TimeoutNanoseconds: int64(processTimeout),
		BootstrapToolCalls: 1,
		Participants: []participant{
			aggregateParticipant("supported-ktx", "one-shot-hook-writers", workers),
			participantFromResult("supported-ktx", "reconciliation-query", query),
		},
		Metrics:        metrics,
		Reconciliation: reconciliation,
		Outcome:        measurementOutcome(metrics, reconciliation),
	}, nil
}

func seedStaleCompatibleDatabase(path string) error {
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(1000)")
	if err != nil {
		return err
	}
	defer database.Close()
	_, err = database.Exec(`
		DROP TABLE compression_dispositions;
		CREATE TABLE loadbench_migration_marker (value TEXT NOT NULL);
		INSERT INTO loadbench_migration_marker VALUES ('preserved');
		PRAGMA user_version = ` + strconv.Itoa(dbpkg.MaxSupportedSchema()-1))
	return err
}

func verifyMigratedDatabase(path string) error {
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(1000)")
	if err != nil {
		return err
	}
	defer database.Close()
	var version, table int
	var marker, integrity string
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if err := database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'compression_dispositions'`).Scan(&table); err != nil {
		return err
	}
	if err := database.QueryRow(`SELECT value FROM loadbench_migration_marker`).Scan(&marker); err != nil {
		return err
	}
	if err := database.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if version != dbpkg.MaxSupportedSchema() || table != 1 || marker != "preserved" || integrity != "ok" {
		return fmt.Errorf("migration post-state version=%d table=%d marker=%q integrity=%q", version, table, marker, integrity)
	}
	return nil
}

func runDuplicateDeliveryScenario(root string, runner processRunner, fixture fixture) (scenario, error) {
	environment, err := newRoundEnvironment(root, "duplicate-delivery")
	if err != nil {
		return scenario{}, err
	}
	setup := runner.hook(environment.env, hookPayload(stableID(600000), fixture))
	if setup.Code != "ok" {
		return scenario{}, fmt.Errorf("initialize duplicate delivery scenario: %s", setup.Code)
	}
	payload, err := openCodePayload(fixture)
	if err != nil {
		return scenario{}, err
	}
	start := time.Now()
	workers := concurrentPayloads(runner, environment.env, payload, 4)
	elapsed := time.Since(start)
	persisted, query, err := runner.persistedCount(environment.env)
	if err != nil {
		return scenario{}, err
	}
	metrics := summarize(workers, completed(workers), elapsed, len(workers)-1)
	addFailure(metrics.FailureCodes, query)
	reconciliation := exactReconciliation(2, persisted)
	if !reconciliation.Matches {
		metrics.FailureCodes["reconciliation-mismatch"]++
	}
	return scenario{
		Name:               "supported-duplicate-delivery",
		FreshOwnedDatabase: true,
		TimeoutNanoseconds: int64(processTimeout),
		BootstrapToolCalls: 1,
		Participants: []participant{
			aggregateParticipant("supported-ktx", "duplicate-hook-deliveries", workers),
			participantFromResult("supported-ktx", "reconciliation-query", query),
		},
		Metrics:        metrics,
		Reconciliation: reconciliation,
		Outcome:        measurementOutcome(metrics, reconciliation),
	}, nil
}

func runProcessTerminationScenario(root string, runner processRunner, fixture fixture) (scenario, error) {
	environment, err := newRoundEnvironment(root, "process-termination")
	if err != nil {
		return scenario{}, err
	}
	setup := runner.hook(environment.env, hookPayload(stableID(700000), fixture))
	if setup.Code != "ok" {
		return scenario{}, fmt.Errorf("initialize process termination scenario: %s", setup.Code)
	}
	holder, err := startForeignHolder(environment.db, "writer")
	if err != nil {
		return scenario{}, err
	}
	worker, err := runner.terminateHook(environment.env, hookPayload(stableID(700100), fixture))
	if err != nil {
		holder.Release()
		_ = holder.Wait()
		return scenario{}, err
	}
	holder.Release()
	if err := holder.Wait(); err != nil {
		return scenario{}, err
	}
	recovery := runner.hook(environment.env, hookPayload(stableID(700200), fixture))
	persisted, query, err := runner.persistedCount(environment.env)
	if err != nil {
		return scenario{}, err
	}
	results := []processResult{worker, recovery}
	metrics := summarize(results, completed(results), time.Duration(worker.LatencyNS+recovery.LatencyNS), 0)
	addObservedLockWait(&metrics, terminationDelay)
	addFailure(metrics.FailureCodes, query)
	reconciliation := exactReconciliation(2, persisted)
	if !reconciliation.Matches {
		metrics.FailureCodes["reconciliation-mismatch"]++
	}
	return scenario{
		Name:               "supported-process-termination-ownership-recovery",
		FreshOwnedDatabase: true,
		TimeoutNanoseconds: int64(processTimeout),
		BootstrapToolCalls: 1,
		Participants: []participant{
			{Class: "foreign-sqlite", Role: "held-writer", Outcome: "released", LatencyNS: int64(terminationDelay)},
			participantFromResult("supported-ktx", "terminated-one-shot-hook", worker),
			participantFromResult("supported-ktx", "recovery-one-shot-hook", recovery),
			participantFromResult("supported-ktx", "reconciliation-query", query),
		},
		Metrics:        metrics,
		Reconciliation: reconciliation,
		Outcome:        measurementOutcome(metrics, reconciliation),
	}, nil
}

func newRoundEnvironment(root, name string) (roundEnvironment, error) {
	roundRoot := filepath.Join(root, "rounds", name)
	for _, directory := range []string{"home", "data", "config", "cache", "state", "tmp", "db"} {
		if err := os.MkdirAll(filepath.Join(roundRoot, directory), 0o700); err != nil {
			return roundEnvironment{}, fmt.Errorf("create round root: %w", err)
		}
	}
	database := filepath.Join(roundRoot, "db", "ktx.db")
	return roundEnvironment{root: roundRoot, db: database, env: workerEnvironment(roundRoot, database)}, nil
}

func workerEnvironment(root, database string) []string {
	values := make([]string, 0, len(os.Environ())+8)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		switch name {
		case "HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR", "KTX_DB", "KTX_VERIFY_ROOT", "KTX_SESSION_ID", streamWorkerEnv, streamPathEnv:
			continue
		}
		values = append(values, item)
	}
	return append(values,
		"HOME="+filepath.Join(root, "home"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"TMPDIR="+filepath.Join(root, "tmp"),
		"KTX_VERIFY_ROOT="+root,
		"KTX_DB="+database,
	)
}

func hookPayload(id string, fixture fixture) []byte {
	payload, err := json.Marshal(struct {
		ID        string `json:"id"`
		Tool      string `json:"tool"`
		Input     string `json:"input"`
		Output    string `json:"output"`
		Timestamp string `json:"timestamp"`
		IsError   bool   `json:"is_error"`
	}{
		ID:        id,
		Tool:      "bash",
		Input:     `{"command":"loadbench"}`,
		Output:    string(fixture.data),
		Timestamp: "2026-08-09T00:00:00Z",
		IsError:   false,
	})
	if err != nil {
		panic(err)
	}
	return payload
}

func openCodePayload(fixture fixture) ([]byte, error) {
	return json.Marshal(map[string]any{
		"runtime": "opencode",
		"input": map[string]any{
			"tool":      "bash",
			"sessionID": "loadbench-session",
			"callID":    "loadbench-duplicate",
			"args":      map[string]string{"command": "loadbench duplicate"},
		},
		"output": map[string]any{
			"output":   string(fixture.data),
			"metadata": map[string]int{"exit": 0},
		},
	})
}

func wrapperStream(fixture fixture, count int) ([]byte, error) {
	var stream bytes.Buffer
	for index := 0; index < count; index++ {
		line, err := json.Marshal(map[string]any{
			"tool":      "bash",
			"input":     map[string]string{"command": fmt.Sprintf("loadbench stream %d", index)},
			"output":    string(fixture.data),
			"timestamp": "2026-08-09T00:00:00Z",
			"is_error":  false,
		})
		if err != nil {
			return nil, err
		}
		stream.Write(line)
		stream.WriteByte('\n')
	}
	return stream.Bytes(), nil
}

func concurrentHooks(runner processRunner, environment roundEnvironment, fixture fixture, count, baseID int) ([]processResult, processResult) {
	start := make(chan struct{})
	results := make(chan processResult, count)
	for index := 0; index < count; index++ {
		index := index
		go func() {
			<-start
			results <- runner.hook(environment.env, hookPayload(stableID(baseID+index), fixture))
		}()
	}
	reader := make(chan processResult, 1)
	go func() {
		<-start
		reader <- runner.query(environment.env)
	}()
	close(start)
	collected := make([]processResult, 0, count)
	for range count {
		collected = append(collected, <-results)
	}
	return collected, <-reader
}

func concurrentHookOnly(runner processRunner, environment roundEnvironment, fixture fixture, count, baseID int) []processResult {
	start := make(chan struct{})
	results := make(chan processResult, count)
	for index := 0; index < count; index++ {
		index := index
		go func() {
			<-start
			results <- runner.hook(environment.env, hookPayload(stableID(baseID+index), fixture))
		}()
	}
	close(start)
	collected := make([]processResult, 0, count)
	for range count {
		collected = append(collected, <-results)
	}
	return collected
}

func concurrentHooksAcrossPaths(runner processRunner, canonical, alias []string, fixture fixture, count, baseID int) []processResult {
	start := make(chan struct{})
	results := make(chan processResult, count)
	for index := 0; index < count; index++ {
		index := index
		go func() {
			<-start
			environment := canonical
			if index%2 != 0 {
				environment = alias
			}
			results <- runner.hook(environment, hookPayload(stableID(baseID+index), fixture))
		}()
	}
	close(start)
	collected := make([]processResult, 0, count)
	for range count {
		collected = append(collected, <-results)
	}
	return collected
}

func concurrentPayloads(runner processRunner, environment []string, payload []byte, count int) []processResult {
	start := make(chan struct{})
	results := make(chan processResult, count)
	for range count {
		go func() {
			<-start
			results <- runner.hook(environment, payload)
		}()
	}
	close(start)
	collected := make([]processResult, 0, count)
	for range count {
		collected = append(collected, <-results)
	}
	return collected
}

func concurrentWrapperAndQuery(runner processRunner, environment roundEnvironment, streamPath string) ([]processResult, processResult) {
	start := make(chan struct{})
	result := make(chan processResult, 1)
	reader := make(chan processResult, 1)
	go func() {
		<-start
		env := append(append([]string(nil), environment.env...), streamWorkerEnv+"=1", streamPathEnv+"="+streamPath)
		result <- runner.wrapper(env)
	}()
	go func() {
		<-start
		reader <- runner.query(environment.env)
	}()
	close(start)
	return []processResult{<-result}, <-reader
}

func (runner processRunner) hook(environment []string, payload []byte) processResult {
	return runner.run(environment, payload, "hook")
}

func (runner processRunner) wrapper(environment []string) processResult {
	executable, err := os.Executable()
	if err != nil {
		return processResult{Code: "harness-error", ExitCode: -1}
	}
	return runner.run(environment, nil, "run", "--quiet", executable)
}

func (runner processRunner) query(environment []string) processResult {
	return runner.run(environment, nil, "db", "report", "--output", "-")
}

func (runner processRunner) persistedCount(environment []string) (int, processResult, error) {
	result := runner.query(environment)
	if result.Code != "ok" {
		return -1, result, nil
	}
	var parsed struct {
		Overview struct {
			TotalToolCalls int `json:"total_tool_calls"`
		} `json:"overview"`
	}
	if err := json.Unmarshal(result.stdout, &parsed); err != nil {
		return -1, result, fmt.Errorf("parse read-only report: %w", err)
	}
	return parsed.Overview.TotalToolCalls, result, nil
}

func (runner processRunner) run(environment []string, stdin []byte, args ...string) processResult {
	context, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	command := exec.CommandContext(context, runner.binary, args...)
	command.Env = environment
	command.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	started := time.Now()
	err := command.Run()
	return classifyProcess(err, context.Err(), time.Since(started), stdout.Bytes(), stderr.Bytes())
}

func (runner processRunner) terminateHook(environment []string, payload []byte) (processResult, error) {
	command := exec.Command(runner.binary, "hook")
	command.Env = environment
	command.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	started := time.Now()
	if err := command.Start(); err != nil {
		return processResult{}, fmt.Errorf("start hook for termination: %w", err)
	}
	time.Sleep(terminationDelay)
	killErr := command.Process.Kill()
	err := command.Wait()
	result := classifyProcess(err, nil, time.Since(started), stdout.Bytes(), stderr.Bytes())
	if killErr == nil {
		result.Code = "terminated"
		result.ExitCode = -1
	}
	return result, nil
}

func classifyProcess(err, contextErr error, elapsed time.Duration, stdout, stderr []byte) processResult {
	result := processResult{Code: "ok", LatencyNS: elapsed.Nanoseconds(), stdout: stdout, stderr: stderr}
	if contextErr != nil {
		result.Code = "timed-out"
		result.ExitCode = -1
		result.TimedOut = true
		return result
	}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = -1
	}
	if code := errorCode(stderr); code != "" {
		result.Code = code
	} else {
		result.Code = fmt.Sprintf("exit-%d", result.ExitCode)
	}
	return result
}

func errorCode(stderr []byte) string {
	for _, line := range bytes.Split(stderr, []byte("\n")) {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &envelope) == nil && envelope.Error.Code != "" {
			return envelope.Error.Code
		}
	}
	return ""
}

func startForeignHolder(path, role string) (*foreignHolder, error) {
	holder := &foreignHolder{release: make(chan struct{}), done: make(chan error, 1)}
	ready := make(chan error, 1)
	go func() {
		database, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(1)")
		if err != nil {
			ready <- err
			holder.done <- err
			return
		}
		defer func() { _ = database.Close() }()
		database.SetMaxOpenConns(1)
		switch role {
		case "reader":
			tx, err := database.Begin()
			if err == nil {
				var count int
				err = tx.QueryRow(`SELECT count(*) FROM tool_calls`).Scan(&count)
			}
			ready <- err
			if err != nil {
				holder.done <- err
				return
			}
			<-holder.release
			holder.done <- tx.Commit()
		case "writer":
			_, err := database.Exec(`BEGIN EXCLUSIVE`)
			ready <- err
			if err != nil {
				holder.done <- err
				return
			}
			<-holder.release
			_, err = database.Exec(`COMMIT`)
			holder.done <- err
		default:
			err := fmt.Errorf("unknown foreign holder role %q", role)
			ready <- err
			holder.done <- err
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			return nil, fmt.Errorf("start foreign %s holder: %w", role, err)
		}
		return holder, nil
	case <-time.After(processTimeout):
		return nil, fmt.Errorf("start foreign %s holder timed out", role)
	}
}

func (holder *foreignHolder) Release() {
	holder.once.Do(func() { close(holder.release) })
}

func (holder *foreignHolder) Wait() error {
	select {
	case err := <-holder.done:
		return err
	case <-time.After(processTimeout):
		return errors.New("foreign holder did not stop")
	}
}

func stableID(value int) string {
	id := make([]byte, 16)
	for index := len(id) - 1; index >= 0; index-- {
		id[index] = byte('a' + value%26)
		value /= 26
	}
	return string(id)
}

func completed(results []processResult) int {
	count := 0
	for _, result := range results {
		if result.Code == "ok" {
			count++
		}
	}
	return count
}

func summarize(results []processResult, completedOperations int, elapsed time.Duration, retries int) metrics {
	latencies := make([]int64, 0, len(results))
	failures := make(map[string]int)
	var lockWait int64
	for _, result := range results {
		latencies = append(latencies, result.LatencyNS)
		addFailure(failures, result)
		if result.Code == "database-unavailable" {
			lockWait += result.LatencyNS
		}
	}
	return metrics{
		ThroughputOperationsPerSecond: throughput(completedOperations, elapsed),
		P50LatencyNanoseconds:         percentile(latencies, 0.50),
		P95LatencyNanoseconds:         percentile(latencies, 0.95),
		P99LatencyNanoseconds:         percentile(latencies, 0.99),
		MaximumLatencyNanoseconds:     maximum(latencies),
		Retries:                       retries,
		RetryScope:                    "harness duplicate deliveries after the initial submission; product-internal retry counts are not exposed",
		CumulativeLockWaitNanoseconds: lockWait,
		LockWaitScope:                 "elapsed worker time for attempts classified database-unavailable",
		FailureCodes:                  failures,
	}
}

func addObservedLockWait(measured *metrics, duration time.Duration) {
	measured.CumulativeLockWaitNanoseconds += duration.Nanoseconds()
	measured.LockWaitScope = "duration of deliberately held foreign locks plus elapsed worker time for attempts classified database-unavailable"
}

func throughput(operations int, elapsed time.Duration) float64 {
	if operations == 0 || elapsed <= 0 {
		return 0
	}
	return float64(operations) / elapsed.Seconds()
}

func percentile(values []int64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	index := int(float64(len(ordered)-1)*fraction + 0.999999999)
	return float64(ordered[index])
}

func maximum(values []int64) int64 {
	var result int64
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}

func addFailure(failures map[string]int, result processResult) {
	if result.Code != "ok" {
		failures[result.Code]++
	}
}

func exactReconciliation(expected, persisted int) reconciliation {
	return reconciliation{ExpectedMinimum: expected, ExpectedMaximum: expected, Persisted: persisted, Matches: persisted == expected}
}

func measurementOutcome(metrics metrics, reconciliation reconciliation) string {
	if reconciliation.Persisted < 0 {
		return "reconciliation-unavailable"
	}
	if !reconciliation.Matches {
		return "reconciliation-mismatch"
	}
	if len(metrics.FailureCodes) != 0 {
		return "completed-with-classified-failures"
	}
	return "completed"
}

func participantFromResult(class, role string, result processResult) participant {
	return participant{
		Class:       class,
		Role:        role,
		Outcome:     result.Code,
		ExitCode:    result.ExitCode,
		FailureCode: failureCode(result),
		LatencyNS:   result.LatencyNS,
	}
}

func aggregateParticipant(class, role string, results []processResult) participant {
	participant := participant{Class: class, Role: role, Attempts: len(results), Completions: completed(results), Outcome: "completed"}
	for _, result := range results {
		participant.LatencyNS += result.LatencyNS
		if result.Code != "ok" {
			participant.Outcome = "completed-with-classified-failures"
			participant.FailureCode = result.Code
		}
	}
	return participant
}

func failureCode(result processResult) string {
	if result.Code == "ok" {
		return ""
	}
	return result.Code
}

func processorIdentity() string {
	contents, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return runtime.GOARCH
	}
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model name", "Hardware", "Processor", "machine":
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return runtime.GOARCH
}
