// readleasecompare compares retained developer engineering evidence for the
// bounded ktx read ownership experiment. It is not a ktx command.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	comparisonSchemaVersion      = 2
	comparisonPolicyVersion      = "read-lease-v2"
	reportSchemaVersion          = 2
	componentReportSchemaVersion = 3
	manifestSchemaVersion        = 2
	focusedReadSchemaVersion     = 1
)

var requiredOrder = []string{"B", "C", "C", "B", "B", "C", "C", "B", "B", "C"}

var requiredExternalGates = []string{
	"read_only",
	"absent_database",
	"stale_schema",
	"newer_schema",
	"output_failure_recovery",
	"termination_recovery",
	"mage_engineering_verify",
	"mage_check",
}

type values []string

func (items *values) String() string { return strings.Join(*items, ",") }

func (items *values) Set(value string) error {
	*items = append(*items, value)
	return nil
}

type manifest struct {
	SchemaVersion       int              `json:"schema_version"`
	StartedAt           string           `json:"started_at"`
	FinishedAt          string           `json:"finished_at"`
	DurationNanoseconds int64            `json:"duration_nanoseconds"`
	Reports             []manifestReport `json:"reports"`
}

type manifestReport struct {
	Name          string `json:"name"`
	MageTarget    string `json:"mage_target"`
	File          string `json:"file"`
	SchemaVersion int    `json:"report_schema_version"`
	SHA256        string `json:"sha256"`
}

type componentReport struct {
	SchemaVersion       int                   `json:"schema_version"`
	StartedAt           string                `json:"started_at"`
	FinishedAt          string                `json:"finished_at"`
	DurationNanoseconds int64                 `json:"duration_nanoseconds"`
	FocusedReads        []focusedReadEvidence `json:"focused_reads"`
}

type focusedReadEvidence struct {
	SchemaVersion int              `json:"schema_version"`
	Name          string           `json:"name"`
	Workload      workloadIdentity `json:"workload"`
	Distribution  readDistribution `json:"distribution"`
}

type workloadIdentity struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Bytes         int    `json:"bytes"`
	GeneratorSeed uint64 `json:"generator_seed"`
	SHA256        string `json:"sha256"`
}

type readDistribution struct {
	LatencyNanosecondsPerOperation numericDistribution `json:"latency_nanoseconds_per_operation"`
	AllocatedBytesPerOperation     numericDistribution `json:"allocated_bytes_per_operation"`
	AllocationsPerOperation        numericDistribution `json:"allocations_per_operation"`
}

type numericDistribution struct {
	Median float64 `json:"median"`
}

type loadReport struct {
	SchemaVersion       int                 `json:"schema_version"`
	StartedAt           string              `json:"started_at"`
	FinishedAt          string              `json:"finished_at"`
	DurationNanoseconds int64               `json:"duration_nanoseconds"`
	Environment         loadEnvironment     `json:"environment"`
	Measurements        []loadMeasurement   `json:"measurements"`
	Scenarios           []loadScenario      `json:"scenarios"`
	BlockedReaderTarget blockedReaderTarget `json:"blocked_reader_target"`
}

type loadEnvironment struct {
	KTXSHA256 string `json:"ktx_sha256"`
}

type loadMeasurement struct {
	Surface     string      `json:"surface"`
	Concurrency int         `json:"concurrency"`
	Warmup      loadRound   `json:"warmup"`
	Rounds      []loadRound `json:"rounds"`
}

type loadRound struct {
	Metrics        loadMetrics    `json:"metrics"`
	Reconciliation reconciliation `json:"reconciliation"`
	Outcome        string         `json:"outcome"`
	Participants   []participant  `json:"participants"`
}

type loadMetrics struct {
	P50LatencyNanoseconds         float64        `json:"p50_latency_nanoseconds"`
	P99LatencyNanoseconds         float64        `json:"p99_latency_nanoseconds"`
	ThroughputOperationsPerSecond float64        `json:"throughput_operations_per_second"`
	FailureCodes                  map[string]int `json:"failure_codes"`
}

type reconciliation struct {
	Matches bool `json:"matches"`
}

type participant struct {
	Class       string `json:"class"`
	Role        string `json:"role"`
	Outcome     string `json:"outcome"`
	ExitCode    int    `json:"exit_code"`
	FailureCode string `json:"failure_code"`
}

type loadScenario struct {
	Name           string         `json:"name"`
	Metrics        loadMetrics    `json:"metrics"`
	Reconciliation reconciliation `json:"reconciliation"`
	Outcome        string         `json:"outcome"`
	Participants   []participant  `json:"participants"`
}

type blockedReaderTarget struct {
	SchemaVersion          int              `json:"schema_version"`
	Workload               workloadIdentity `json:"workload"`
	ReaderOutputStarted    bool             `json:"reader_output_started"`
	ReaderBackpressured    bool             `json:"reader_backpressured"`
	HookOutcome            string           `json:"hook_outcome"`
	HookExitCode           int              `json:"hook_exit_code"`
	HookLatencyNanoseconds int64            `json:"hook_latency_nanoseconds"`
	HookFailureCode        string           `json:"hook_failure_code"`
	Reconciliation         reconciliation   `json:"reconciliation"`
}

type observedRun struct {
	Position          int
	Arm               string
	EvidenceDirectory string
	BinarySHA256      string
	FocusedReads      map[string]focusedReadEvidence
	HookC1P50         float64
	HookC32P99        float64
	HookC32Throughput float64
	ExactReconciled   bool
	SupportedFailures bool
	BlockedTargetPass bool
	BlockedTarget     blockedReaderTarget
}

type runReport struct {
	Position            int                 `json:"position"`
	Arm                 string              `json:"arm"`
	EvidenceDirectory   string              `json:"evidence_directory"`
	BinarySHA256        string              `json:"binary_sha256"`
	BlockedReaderTarget blockedReaderTarget `json:"blocked_reader_target"`
}

type metricResult struct {
	Name               string    `json:"name"`
	Selector           string    `json:"selector"`
	Direction          string    `json:"direction"`
	BaselineValues     []float64 `json:"baseline_values"`
	CandidateValues    []float64 `json:"candidate_values"`
	BaselineCenter     float64   `json:"baseline_center"`
	BaselineMAD        float64   `json:"baseline_mad"`
	BaselineNoise      float64   `json:"baseline_normalized_noise"`
	NoiseLimit         float64   `json:"noise_limit"`
	CandidateCenter    float64   `json:"candidate_center"`
	Delta              float64   `json:"delta"`
	PracticalAllowance float64   `json:"practical_allowance"`
	NoiseAllowance     float64   `json:"noise_allowance"`
	TotalAllowance     float64   `json:"total_allowance"`
	AllowedMinimum     *float64  `json:"allowed_minimum,omitempty"`
	AllowedMaximum     *float64  `json:"allowed_maximum,omitempty"`
	Status             string    `json:"status"`
}

type gateResult struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Status string `json:"status"`
}

type comparisonReport struct {
	SchemaVersion      int            `json:"schema_version"`
	PolicyVersion      string         `json:"policy_version"`
	Sequence           int            `json:"sequence"`
	RequiredOrder      []string       `json:"required_order"`
	ObservedOrder      []string       `json:"observed_order"`
	Runs               []runReport    `json:"runs"`
	Metrics            []metricResult `json:"metrics"`
	HardGates          []gateResult   `json:"hard_gates"`
	MeasurementVerdict string         `json:"measurement_verdict"`
	Decision           string         `json:"decision"`
	RerunAllowed       bool           `json:"rerun_allowed"`
}

type metricSpec struct {
	name               string
	selector           string
	direction          string
	timing             bool
	practicalAllowance float64
	noiseLimit         float64
	value              func(observedRun) (float64, error)
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "readleasecompare: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("readleasecompare", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var runValues, gateValues values
	sequence := fs.Int("sequence", 1, "comparison sequence, 1 or 2")
	output := fs.String("output", "-", "JSON report path or - for stdout")
	fs.Var(&runValues, "run", "ordered B=<evidence-dir> or C=<evidence-dir>; repeat ten times")
	fs.Var(&gateValues, "gate", "external hard gate name=true|false; repeat for final decision")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *sequence != 1 && *sequence != 2 {
		return fmt.Errorf("sequence = %d, want 1 or 2", *sequence)
	}
	externalGates, err := parseExternalGates(gateValues)
	if err != nil {
		return err
	}
	runs, observedOrder, err := readRuns(runValues)
	if err != nil {
		return err
	}
	report, err := compare(runs, observedOrder, *sequence, externalGates)
	if err != nil {
		return err
	}
	return writeReport(*output, stdout, report)
}

func parseExternalGates(raw []string) (map[string]bool, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	parsed := make(map[string]bool, len(raw))
	for _, value := range raw {
		name, rawValue, found := strings.Cut(value, "=")
		if !found || name == "" || (rawValue != "true" && rawValue != "false") {
			return nil, fmt.Errorf("gate %q must be name=true or name=false", value)
		}
		if _, exists := parsed[name]; exists {
			return nil, fmt.Errorf("gate %q is repeated", name)
		}
		parsed[name] = rawValue == "true"
	}
	for _, name := range requiredExternalGates {
		if _, exists := parsed[name]; !exists {
			return nil, fmt.Errorf("missing required external gate %q", name)
		}
	}
	if len(parsed) != len(requiredExternalGates) {
		return nil, fmt.Errorf("external gates include an unknown name")
	}
	return parsed, nil
}

func readRuns(raw []string) ([]observedRun, []string, error) {
	if len(raw) != len(requiredOrder) {
		return nil, nil, fmt.Errorf("run count = %d, want %d", len(raw), len(requiredOrder))
	}
	seenDirectories := make(map[string]bool, len(raw))
	runs := make([]observedRun, 0, len(raw))
	order := make([]string, 0, len(raw))
	for index, value := range raw {
		arm, directory, found := strings.Cut(value, "=")
		if !found || (arm != "B" && arm != "C") || directory == "" {
			return nil, nil, fmt.Errorf("run %q must be B=<evidence-dir> or C=<evidence-dir>", value)
		}
		if arm != requiredOrder[index] {
			return nil, nil, fmt.Errorf("run %d arm = %s, want %s", index+1, arm, requiredOrder[index])
		}
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return nil, nil, err
		}
		absolute = filepath.Clean(absolute)
		if seenDirectories[absolute] {
			return nil, nil, fmt.Errorf("evidence directory is reused: %s", absolute)
		}
		seenDirectories[absolute] = true
		observed, err := readRun(index+1, arm, absolute)
		if err != nil {
			return nil, nil, err
		}
		runs = append(runs, observed)
		order = append(order, arm)
	}
	return runs, order, nil
}

func readRun(position int, arm, directory string) (observedRun, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d manifest: %w", position, err)
	}
	var manifestData manifest
	if err := json.Unmarshal(manifestBytes, &manifestData); err != nil {
		return observedRun{}, fmt.Errorf("run %d decode manifest: %w", position, err)
	}
	if err := validateTemporal(manifestData.SchemaVersion, manifestData.StartedAt, manifestData.FinishedAt, manifestData.DurationNanoseconds, manifestSchemaVersion); err != nil {
		return observedRun{}, fmt.Errorf("run %d manifest: %w", position, err)
	}
	componentInfo, err := manifestNamedReport(manifestData, "component")
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d manifest: %w", position, err)
	}
	loadInfo, err := manifestNamedReport(manifestData, "load")
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d manifest: %w", position, err)
	}
	componentBytes, err := checkedReport(directory, componentInfo)
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d component report: %w", position, err)
	}
	loadBytes, err := checkedReport(directory, loadInfo)
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d load report: %w", position, err)
	}
	var component componentReport
	if err := json.Unmarshal(componentBytes, &component); err != nil {
		return observedRun{}, fmt.Errorf("run %d decode component report: %w", position, err)
	}
	if (component.SchemaVersion != reportSchemaVersion && component.SchemaVersion != componentReportSchemaVersion) || componentInfo.SchemaVersion != component.SchemaVersion {
		return observedRun{}, fmt.Errorf("run %d component report schema %d lacks focused read policy evidence", position, component.SchemaVersion)
	}
	if err := validateTemporal(component.SchemaVersion, component.StartedAt, component.FinishedAt, component.DurationNanoseconds, component.SchemaVersion); err != nil {
		return observedRun{}, fmt.Errorf("run %d component report: %w", position, err)
	}
	focusedReads, err := indexedFocusedReads(component.FocusedReads)
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d component report: %w", position, err)
	}
	var load loadReport
	if err := json.Unmarshal(loadBytes, &load); err != nil {
		return observedRun{}, fmt.Errorf("run %d decode load report: %w", position, err)
	}
	if load.SchemaVersion != reportSchemaVersion || loadInfo.SchemaVersion != load.SchemaVersion {
		return observedRun{}, fmt.Errorf("run %d load report schema %d lacks focused read policy evidence", position, load.SchemaVersion)
	}
	if err := validateTemporal(load.SchemaVersion, load.StartedAt, load.FinishedAt, load.DurationNanoseconds, reportSchemaVersion); err != nil {
		return observedRun{}, fmt.Errorf("run %d load report: %w", position, err)
	}
	if !validSHA256(load.Environment.KTXSHA256) {
		return observedRun{}, fmt.Errorf("run %d load report has invalid binary hash", position)
	}
	hookC1P50, err := runLevelMetric(load.Measurements, 1, func(metrics loadMetrics) float64 { return metrics.P50LatencyNanoseconds })
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d concurrency-1 p50: %w", position, err)
	}
	hookC32P99, err := runLevelMetric(load.Measurements, 32, func(metrics loadMetrics) float64 { return metrics.P99LatencyNanoseconds })
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d concurrency-32 p99: %w", position, err)
	}
	hookC32Throughput, err := runLevelMetric(load.Measurements, 32, func(metrics loadMetrics) float64 { return metrics.ThroughputOperationsPerSecond })
	if err != nil {
		return observedRun{}, fmt.Errorf("run %d concurrency-32 throughput: %w", position, err)
	}
	exact, supported := validateLoadContract(load)
	targetPass := load.BlockedReaderTarget.ReaderOutputStarted && load.BlockedReaderTarget.ReaderBackpressured && load.BlockedReaderTarget.HookOutcome == "ok" && load.BlockedReaderTarget.HookExitCode == 0 && load.BlockedReaderTarget.HookFailureCode == "" && load.BlockedReaderTarget.Reconciliation.Matches
	return observedRun{
		Position:          position,
		Arm:               arm,
		EvidenceDirectory: directory,
		BinarySHA256:      load.Environment.KTXSHA256,
		FocusedReads:      focusedReads,
		HookC1P50:         hookC1P50,
		HookC32P99:        hookC32P99,
		HookC32Throughput: hookC32Throughput,
		ExactReconciled:   exact,
		SupportedFailures: supported,
		BlockedTargetPass: targetPass,
		BlockedTarget:     load.BlockedReaderTarget,
	}, nil
}

func manifestNamedReport(data manifest, name string) (manifestReport, error) {
	var found *manifestReport
	for index := range data.Reports {
		if data.Reports[index].Name != name {
			continue
		}
		if found != nil {
			return manifestReport{}, fmt.Errorf("report %q is repeated", name)
		}
		found = &data.Reports[index]
	}
	if found == nil {
		return manifestReport{}, fmt.Errorf("report %q is missing", name)
	}
	return *found, nil
}

func checkedReport(directory string, report manifestReport) ([]byte, error) {
	if report.File == "" || filepath.Base(report.File) != report.File || !validSHA256(report.SHA256) {
		return nil, fmt.Errorf("invalid manifest report reference")
	}
	contents, err := os.ReadFile(filepath.Join(directory, report.File))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(contents)
	if hex.EncodeToString(sum[:]) != report.SHA256 {
		return nil, fmt.Errorf("SHA-256 does not match manifest")
	}
	return contents, nil
}

func validateTemporal(schemaVersion int, startedAt, finishedAt string, durationNanoseconds int64, currentSchema int) error {
	if schemaVersion == 1 {
		return nil
	}
	if schemaVersion != currentSchema {
		return fmt.Errorf("unsupported schema %d", schemaVersion)
	}
	if durationNanoseconds < 0 {
		return fmt.Errorf("duration_nanoseconds = %d", durationNanoseconds)
	}
	started, err := parseUTCTimestamp(startedAt)
	if err != nil {
		return fmt.Errorf("started_at: %w", err)
	}
	finished, err := parseUTCTimestamp(finishedAt)
	if err != nil {
		return fmt.Errorf("finished_at: %w", err)
	}
	if finished.Before(started) {
		return fmt.Errorf("finished_at precedes started_at")
	}
	if got := finished.Sub(started).Nanoseconds(); got != durationNanoseconds {
		return fmt.Errorf("duration_nanoseconds = %d, want %d", durationNanoseconds, got)
	}
	return nil
}

func parseUTCTimestamp(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("must be UTC RFC3339Nano")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func indexedFocusedReads(reads []focusedReadEvidence) (map[string]focusedReadEvidence, error) {
	want := []struct {
		name  string
		bytes int
	}{
		{name: "ktx-read-output-1024", bytes: 1024},
		{name: "ktx-read-output-65536", bytes: 65536},
		{name: "ktx-read-output-1048576", bytes: 1048576},
		{name: "ktx-read-output-2097152", bytes: 2097152},
	}
	if len(reads) != len(want) {
		return nil, fmt.Errorf("focused read count = %d, want %d", len(reads), len(want))
	}
	indexed := make(map[string]focusedReadEvidence, len(reads))
	for _, read := range reads {
		if read.SchemaVersion != focusedReadSchemaVersion || read.Name != "ktx-read" || read.Workload.Kind != "ktx-read-output" || read.Workload.GeneratorSeed == 0 || !validSHA256(read.Workload.SHA256) {
			return nil, fmt.Errorf("focused read has invalid identity")
		}
		if _, exists := indexed[read.Workload.Name]; exists {
			return nil, fmt.Errorf("focused read %q is repeated", read.Workload.Name)
		}
		if !positive(read.Distribution.LatencyNanosecondsPerOperation.Median) || !nonnegative(read.Distribution.AllocatedBytesPerOperation.Median) || !nonnegative(read.Distribution.AllocationsPerOperation.Median) {
			return nil, fmt.Errorf("focused read %q has invalid metrics", read.Workload.Name)
		}
		indexed[read.Workload.Name] = read
	}
	for _, expected := range want {
		read, found := indexed[expected.name]
		if !found || read.Workload.Bytes != expected.bytes {
			return nil, fmt.Errorf("focused read %q is missing or has the wrong size", expected.name)
		}
	}
	return indexed, nil
}

func runLevelMetric(measurements []loadMeasurement, concurrency int, value func(loadMetrics) float64) (float64, error) {
	var target *loadMeasurement
	for index := range measurements {
		measurement := &measurements[index]
		if measurement.Surface != "one-shot-hook" || measurement.Concurrency != concurrency {
			continue
		}
		if target != nil {
			return 0, fmt.Errorf("measurement is repeated")
		}
		target = measurement
	}
	if target == nil || len(target.Rounds) != 5 {
		return 0, fmt.Errorf("measurement is missing five measured rounds")
	}
	values := make([]float64, 0, len(target.Rounds))
	for _, round := range target.Rounds {
		metric := value(round.Metrics)
		if !positive(metric) {
			return 0, fmt.Errorf("round metric is not positive")
		}
		values = append(values, metric)
	}
	return median(values), nil
}

func validateLoadContract(load loadReport) (exactReconciled, supportedFailures bool) {
	exactReconciled = load.BlockedReaderTarget.Reconciliation.Matches
	supportedFailures = true
	for _, measurement := range load.Measurements {
		for _, round := range append([]loadRound{measurement.Warmup}, measurement.Rounds...) {
			exactReconciled = exactReconciled && round.Reconciliation.Matches
			supportedFailures = supportedFailures && ordinaryRoundPasses(round)
		}
	}
	byName := make(map[string]loadScenario, len(load.Scenarios))
	for _, scenario := range load.Scenarios {
		if _, exists := byName[scenario.Name]; exists {
			supportedFailures = false
		}
		byName[scenario.Name] = scenario
		exactReconciled = exactReconciled && scenario.Reconciliation.Matches
	}
	for _, name := range []string{"supported-writer-path-alias", "foreign-held-reader", "foreign-held-writer", "supported-compatible-migration-race", "supported-duplicate-delivery"} {
		scenario, found := byName[name]
		supportedFailures = supportedFailures && found && scenario.Outcome == "completed" && len(scenario.Metrics.FailureCodes) == 0
	}
	foreign, found := byName["foreign-writer-reader-exhaustion"]
	supportedFailures = supportedFailures && found && foreign.Outcome == "completed-with-classified-failures" && onlyFailure(foreign.Metrics.FailureCodes, "database-unavailable") && hasParticipant(foreign.Participants, "supported-ktx", "read-only-db-report", "database-unavailable", "database-unavailable")
	termination, found := byName["supported-process-termination-ownership-recovery"]
	supportedFailures = supportedFailures && found && termination.Outcome == "completed-with-classified-failures" && onlyFailure(termination.Metrics.FailureCodes, "terminated") && hasParticipant(termination.Participants, "supported-ktx", "recovery-one-shot-hook", "ok", "")
	return exactReconciled, supportedFailures
}

func ordinaryRoundPasses(round loadRound) bool {
	if round.Outcome != "completed" || len(round.Metrics.FailureCodes) != 0 {
		return false
	}
	for _, participant := range round.Participants {
		if participant.Class == "supported-ktx" && participant.FailureCode != "" {
			return false
		}
	}
	return true
}

func onlyFailure(failures map[string]int, name string) bool {
	return len(failures) == 1 && failures[name] == 1
}

func hasParticipant(participants []participant, class, role, outcome, failureCode string) bool {
	for _, participant := range participants {
		if participant.Class == class && participant.Role == role && participant.Outcome == outcome && participant.FailureCode == failureCode {
			return true
		}
	}
	return false
}

func compare(runs []observedRun, observedOrder []string, sequence int, externalGates map[string]bool) (comparisonReport, error) {
	if len(runs) != len(requiredOrder) {
		return comparisonReport{}, fmt.Errorf("run count = %d, want %d", len(runs), len(requiredOrder))
	}
	metrics, err := compareMetrics(runs, sequence)
	if err != nil {
		return comparisonReport{}, err
	}
	gates := internalGates(runs, externalGates)
	measurementVerdict := measurementVerdict(metrics, gates)
	decision := measurementVerdict
	switch externalGateStatus(gates) {
	case "fail":
		decision = "reject"
	case "not_evaluated":
		if measurementVerdict == "retain" {
			decision = "pending-hard-gates"
		}
	}
	reportRuns := make([]runReport, 0, len(runs))
	for _, run := range runs {
		arm := "baseline"
		if run.Arm == "C" {
			arm = "candidate"
		}
		reportRuns = append(reportRuns, runReport{
			Position: run.Position, Arm: arm, EvidenceDirectory: run.EvidenceDirectory,
			BinarySHA256: run.BinarySHA256, BlockedReaderTarget: run.BlockedTarget,
		})
	}
	return comparisonReport{
		SchemaVersion:      comparisonSchemaVersion,
		PolicyVersion:      comparisonPolicyVersion,
		Sequence:           sequence,
		RequiredOrder:      append([]string(nil), requiredOrder...),
		ObservedOrder:      observedOrder,
		Runs:               reportRuns,
		Metrics:            metrics,
		HardGates:          gates,
		MeasurementVerdict: measurementVerdict,
		Decision:           decision,
		RerunAllowed:       decision == "inconclusive" && sequence == 1,
	}, nil
}

func internalGates(runs []observedRun, external map[string]bool) []gateResult {
	exact := true
	supported := true
	target := true
	identities := make(map[string]workloadIdentity)
	byteIdentity := true
	baselineHash := ""
	candidateHash := ""
	binaryIdentity := true
	for _, run := range runs {
		exact = exact && run.ExactReconciled
		supported = supported && run.SupportedFailures
		if run.Arm == "C" {
			target = target && run.BlockedTargetPass
		}
		for name, read := range run.FocusedReads {
			if prior, exists := identities[name]; exists && prior != read.Workload {
				byteIdentity = false
			} else {
				identities[name] = read.Workload
			}
		}
		if run.Arm == "B" {
			if baselineHash == "" {
				baselineHash = run.BinarySHA256
			} else if baselineHash != run.BinarySHA256 {
				binaryIdentity = false
			}
		} else if candidateHash == "" {
			candidateHash = run.BinarySHA256
		} else if candidateHash != run.BinarySHA256 {
			binaryIdentity = false
		}
	}
	if baselineHash == "" || candidateHash == "" || baselineHash == candidateHash {
		binaryIdentity = false
	}
	gates := []gateResult{
		{Name: "exact_reconciliation", Source: "retained load reports", Status: passFail(exact)},
		{Name: "supported_failure", Source: "retained load reports", Status: passFail(supported)},
		{Name: "byte_identity", Source: "focused read workload identities", Status: passFail(byteIdentity)},
		{Name: "binary_identity", Source: "retained load report hashes", Status: passFail(binaryIdentity)},
		{Name: "blocked_reader_target", Source: "retained blocked-reader targets", Status: passFail(target)},
	}
	for _, name := range requiredExternalGates {
		if external == nil {
			gates = append(gates, gateResult{Name: name, Source: "external verification", Status: "not_evaluated"})
			continue
		}
		gates = append(gates, gateResult{Name: name, Source: "external verification", Status: passFail(external[name])})
	}
	return gates
}

func compareMetrics(runs []observedRun, sequence int) ([]metricResult, error) {
	definitions := metricDefinitions()
	results := make([]metricResult, 0, len(definitions))
	for _, definition := range definitions {
		baseline := make([]float64, 0, 5)
		candidate := make([]float64, 0, 5)
		for _, run := range runs {
			value, err := definition.value(run)
			if err != nil {
				return nil, fmt.Errorf("%s run %d: %w", definition.name, run.Position, err)
			}
			if !positive(value) {
				return nil, fmt.Errorf("%s run %d value is not positive", definition.name, run.Position)
			}
			if run.Arm == "B" {
				baseline = append(baseline, value)
			} else {
				candidate = append(candidate, value)
			}
		}
		if len(baseline) != 5 || len(candidate) != 5 {
			return nil, fmt.Errorf("%s arm counts are baseline=%d candidate=%d", definition.name, len(baseline), len(candidate))
		}
		results = append(results, evaluateMetric(definition, baseline, candidate, sequence))
	}
	return results, nil
}

func metricDefinitions() []metricSpec {
	definitions := make([]metricSpec, 0, 15)
	for _, workload := range []string{"ktx-read-output-1024", "ktx-read-output-65536", "ktx-read-output-1048576", "ktx-read-output-2097152"} {
		workload := workload
		prefix := "component.focused_reads[workload.name=" + workload + "].distribution"
		definitions = append(definitions,
			metricSpec{
				name: "focused_read." + workload + ".latency_median_nanoseconds", selector: prefix + ".latency_nanoseconds_per_operation.median", direction: "lower", timing: true, practicalAllowance: 0.05, noiseLimit: 0.05,
				value: func(run observedRun) (float64, error) {
					read, ok := run.FocusedReads[workload]
					if !ok {
						return 0, fmt.Errorf("focused workload is missing")
					}
					return read.Distribution.LatencyNanosecondsPerOperation.Median, nil
				},
			},
			metricSpec{
				name: "focused_read." + workload + ".allocated_bytes_median", selector: prefix + ".allocated_bytes_per_operation.median", direction: "lower", practicalAllowance: 0.10, noiseLimit: 0.05,
				value: func(run observedRun) (float64, error) {
					read, ok := run.FocusedReads[workload]
					if !ok {
						return 0, fmt.Errorf("focused workload is missing")
					}
					return read.Distribution.AllocatedBytesPerOperation.Median, nil
				},
			},
			metricSpec{
				name: "focused_read." + workload + ".allocation_count_median", selector: prefix + ".allocations_per_operation.median", direction: "lower", practicalAllowance: 0.10, noiseLimit: 0.05,
				value: func(run observedRun) (float64, error) {
					read, ok := run.FocusedReads[workload]
					if !ok {
						return 0, fmt.Errorf("focused workload is missing")
					}
					return read.Distribution.AllocationsPerOperation.Median, nil
				},
			},
		)
	}
	return append(definitions,
		metricSpec{
			name: "one_shot_hook.concurrency_1.p50_latency_nanoseconds", selector: "load.measurements[surface=one-shot-hook,concurrency=1].rounds[*].metrics.p50_latency_nanoseconds |> median", direction: "lower", timing: true, practicalAllowance: 0.05, noiseLimit: 0.05,
			value: func(run observedRun) (float64, error) { return run.HookC1P50, nil },
		},
		metricSpec{
			name: "one_shot_hook.concurrency_32.p99_latency_nanoseconds", selector: "load.measurements[surface=one-shot-hook,concurrency=32].rounds[*].metrics.p99_latency_nanoseconds |> median", direction: "lower", timing: true, practicalAllowance: 0.10, noiseLimit: 0.10,
			value: func(run observedRun) (float64, error) { return run.HookC32P99, nil },
		},
		metricSpec{
			name: "one_shot_hook.concurrency_32.throughput_operations_per_second", selector: "load.measurements[surface=one-shot-hook,concurrency=32].rounds[*].metrics.throughput_operations_per_second |> median", direction: "higher", timing: true, practicalAllowance: 0.05, noiseLimit: 0.05,
			value: func(run observedRun) (float64, error) { return run.HookC32Throughput, nil },
		},
	)
}

func evaluateMetric(spec metricSpec, baseline, candidate []float64, sequence int) metricResult {
	baselineCenter := median(baseline)
	deviations := make([]float64, 0, len(baseline))
	for _, value := range baseline {
		deviations = append(deviations, math.Abs(value-baselineCenter))
	}
	mad := median(deviations)
	noise := 1.4826 * mad / baselineCenter
	candidateCenter := median(candidate)
	allowance := spec.practicalAllowance + noise
	result := metricResult{
		Name:               spec.name,
		Selector:           spec.selector,
		Direction:          spec.direction,
		BaselineValues:     baseline,
		CandidateValues:    candidate,
		BaselineCenter:     baselineCenter,
		BaselineMAD:        mad,
		BaselineNoise:      noise,
		NoiseLimit:         spec.noiseLimit,
		CandidateCenter:    candidateCenter,
		Delta:              (candidateCenter - baselineCenter) / baselineCenter,
		PracticalAllowance: spec.practicalAllowance,
		NoiseAllowance:     noise,
		TotalAllowance:     allowance,
	}
	regression := false
	if spec.direction == "lower" {
		maximum := baselineCenter * (1 + allowance)
		result.AllowedMaximum = &maximum
		regression = candidateCenter > maximum
	} else {
		minimum := baselineCenter * (1 - allowance)
		result.AllowedMinimum = &minimum
		regression = candidateCenter < minimum
	}
	if spec.timing && noise > spec.noiseLimit {
		if sequence == 1 {
			result.Status = "inconclusive"
		} else if regression {
			result.Status = "regression"
		} else {
			result.Status = "pass_non_authorizing_noisy"
		}
		return result
	}
	if regression {
		result.Status = "regression"
	} else {
		result.Status = "pass"
	}
	return result
}

func measurementVerdict(metrics []metricResult, gates []gateResult) string {
	for _, gate := range gates {
		if gate.Status == "fail" && gate.Name != "read_only" && gate.Name != "absent_database" && gate.Name != "stale_schema" && gate.Name != "newer_schema" && gate.Name != "output_failure_recovery" && gate.Name != "termination_recovery" && gate.Name != "mage_engineering_verify" && gate.Name != "mage_check" {
			return "reject"
		}
	}
	for _, metric := range metrics {
		if metric.Status == "inconclusive" {
			return "inconclusive"
		}
	}
	for _, metric := range metrics {
		if metric.Status == "regression" {
			return "reject"
		}
	}
	return "retain"
}

func externalGateStatus(gates []gateResult) string {
	for _, gate := range gates {
		if gate.Source != "external verification" {
			continue
		}
		if gate.Status == "fail" {
			return "fail"
		}
		if gate.Status != "pass" {
			return "not_evaluated"
		}
	}
	return "pass"
}

func writeReport(path string, stdout io.Writer, report comparisonReport) error {
	contents, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if path == "-" {
		_, err := stdout.Write(contents)
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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

func median(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	return ordered[len(ordered)/2]
}

func positive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func nonnegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func passFail(passed bool) string {
	if passed {
		return "pass"
	}
	return "fail"
}
