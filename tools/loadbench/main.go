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
	"io"
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
	reportSchemaVersion       = 3
	hookContentionSchema      = 1
	blockedReaderTargetSchema = 1
	blockedDecompressSchema   = 2
	warmupRounds              = 1
	measuredRounds            = 5
	streamEvents              = 8
	agentWriterStreams        = 4
	agentReaderStreams        = 1
	processTimeout            = 12 * time.Second
	foreignHoldDuration       = 250 * time.Millisecond
	terminationDelay          = 100 * time.Millisecond
	aliasWriterCount          = 8
	blockedReaderDelay        = 50 * time.Millisecond
	streamWorkerEnv           = "KTX_LOADBENCH_STREAM_WORKER"
	streamPathEnv             = "KTX_LOADBENCH_STREAM_PATH"
)

var concurrencyLevels = []int{1, 2, 4, 8, 16, 32}

type report struct {
	SchemaVersion       int                 `json:"schema_version"`
	StartedAt           string              `json:"started_at"`
	FinishedAt          string              `json:"finished_at"`
	DurationNanoseconds int64               `json:"duration_nanoseconds"`
	WarmupRounds        int                 `json:"warmup_rounds"`
	MeasuredRounds      int                 `json:"measured_rounds"`
	Environment         environmentIdentity `json:"environment"`
	Payloads            []payloadIdentity   `json:"payloads"`
	Measurements        []measurement       `json:"measurements"`
	AgentLoads          []agentLoad         `json:"agent_loads"`
	ContentionPenalties contentionPenalties `json:"contention_penalties"`
	Scenarios           []scenario          `json:"scenarios"`
	BlockedReaderTarget blockedReaderTarget `json:"blocked_reader_target"`
	Preflight           preflight           `json:"preflight"`
}

type hookContentionReport struct {
	SchemaVersion               int                   `json:"schema_version"`
	Environment                 environmentIdentity   `json:"environment"`
	Workload                    payloadIdentity       `json:"workload"`
	WarmupRounds                int                   `json:"warmup_rounds"`
	MeasuredRounds              int                   `json:"measured_rounds"`
	ConcurrencyLevels           []int                 `json:"concurrency_levels"`
	ProcessTimeoutNanoseconds   int64                 `json:"process_timeout_nanoseconds"`
	PipelineTimeoutNanoseconds  int64                 `json:"pipeline_timeout_nanoseconds"`
	StartupRetryNanoseconds     int64                 `json:"startup_retry_nanoseconds"`
	StartupRetryMaximumAttempts int                   `json:"startup_retry_maximum_attempts"`
	Rounds                      []hookContentionRound `json:"rounds"`
}

type hookContentionRound struct {
	Kind           string                       `json:"kind"`
	Concurrency    int                          `json:"concurrency"`
	Round          int                          `json:"round"`
	Attempts       int                          `json:"attempts"`
	Completions    int                          `json:"completions"`
	FailureCodes   map[string]int               `json:"failure_codes"`
	Reconciliation hookContentionReconciliation `json:"reconciliation"`
}

type hookContentionReconciliation struct {
	ExpectedSources        int  `json:"expected_sources"`
	PersistedSources       int  `json:"persisted_sources"`
	ExpectedSessions       int  `json:"expected_sessions"`
	PersistedSessions      int  `json:"persisted_sessions"`
	ExpectedSessionRollup  int  `json:"expected_session_rollup"`
	PersistedSessionRollup int  `json:"persisted_session_rollup"`
	ExpectedArtifacts      int  `json:"expected_artifacts"`
	PersistedArtifacts     int  `json:"persisted_artifacts"`
	ExpectedDispositions   int  `json:"expected_dispositions"`
	PersistedDispositions  int  `json:"persisted_dispositions"`
	SourceHookProducers    int  `json:"source_hook_producers"`
	ArtifactHookProducers  int  `json:"artifact_hook_producers"`
	Matches                bool `json:"matches"`
}

type blockedDecompressionReport struct {
	SchemaVersion       int                        `json:"schema_version"`
	StartedAt           string                     `json:"started_at"`
	FinishedAt          string                     `json:"finished_at"`
	DurationNanoseconds int64                      `json:"duration_nanoseconds"`
	Environment         environmentIdentity        `json:"environment"`
	Target              blockedDecompressionTarget `json:"target"`
}

type blockedDecompressionTarget struct {
	SchemaVersion                   int                         `json:"schema_version"`
	Workload                        payloadIdentity             `json:"workload"`
	OutputStarted                   bool                        `json:"output_started"`
	Backpressured                   bool                        `json:"backpressured"`
	HookObservedBeforeOutputRelease bool                        `json:"hook_observed_before_output_release"`
	OutputBytes                     int                         `json:"output_bytes"`
	OutputSHA256                    string                      `json:"output_sha256"`
	OutputByteExact                 bool                        `json:"output_byte_exact"`
	HookOutcome                     string                      `json:"hook_outcome"`
	HookExitCode                    int                         `json:"hook_exit_code"`
	HookLatencyNanoseconds          int64                       `json:"hook_latency_nanoseconds"`
	HookFailureCode                 string                      `json:"hook_failure_code,omitempty"`
	Participants                    []participant               `json:"participants"`
	Reconciliation                  decompressionReconciliation `json:"reconciliation"`
}

type decompressionReconciliation struct {
	ExpectedSources       int  `json:"expected_sources"`
	PersistedSources      int  `json:"persisted_sources"`
	ExpectedArtifacts     int  `json:"expected_artifacts"`
	PersistedArtifacts    int  `json:"persisted_artifacts"`
	ExpectedDispositions  int  `json:"expected_dispositions"`
	PersistedDispositions int  `json:"persisted_dispositions"`
	ExpectedSessions      int  `json:"expected_sessions"`
	PersistedSessions     int  `json:"persisted_sessions"`
	Matches               bool `json:"matches"`
}

type blockedReaderTarget struct {
	SchemaVersion          int             `json:"schema_version"`
	Workload               payloadIdentity `json:"workload"`
	ReaderOutputStarted    bool            `json:"reader_output_started"`
	ReaderBackpressured    bool            `json:"reader_backpressured"`
	HookOutcome            string          `json:"hook_outcome"`
	HookExitCode           int             `json:"hook_exit_code"`
	HookLatencyNanoseconds int64           `json:"hook_latency_nanoseconds"`
	HookFailureCode        string          `json:"hook_failure_code,omitempty"`
	Reconciliation         reconciliation  `json:"reconciliation"`
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
	ReaderAttempts     int            `json:"reader_attempts,omitempty"`
	ReaderCompletions  int            `json:"reader_completions,omitempty"`
	Participants       []participant  `json:"participants"`
	Metrics            metrics        `json:"metrics"`
	ReaderMetrics      *metrics       `json:"reader_metrics,omitempty"`
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
	AgentID     string `json:"agent_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
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
	FailureRate                   float64        `json:"failure_rate"`
	Retries                       int            `json:"retries"`
	RetryScope                    string         `json:"retry_scope"`
	CumulativeLockWaitNanoseconds int64          `json:"cumulative_lock_wait_nanoseconds"`
	LockWaitScope                 string         `json:"lock_wait_scope"`
	FailureCodes                  map[string]int `json:"failure_codes"`
}

type agentLoad struct {
	Name                   string   `json:"name"`
	WriterStreams          int      `json:"writer_streams"`
	EventsPerWriterStream  int      `json:"events_per_writer_stream"`
	ReaderStreams          int      `json:"reader_streams"`
	QueriesPerReaderStream int      `json:"queries_per_reader_stream"`
	SupportedFailureCodes  []string `json:"supported_failure_codes"`
	Warmup                 round    `json:"warmup"`
	Rounds                 []round  `json:"rounds"`
	MeasuredWriterMetrics  metrics  `json:"measured_writer_metrics"`
	MeasuredReaderMetrics  *metrics `json:"measured_reader_metrics,omitempty"`
}

type contentionPenalties struct {
	P50LatencyNanoseconds         penalty `json:"p50_latency_nanoseconds"`
	P95LatencyNanoseconds         penalty `json:"p95_latency_nanoseconds"`
	P99LatencyNanoseconds         penalty `json:"p99_latency_nanoseconds"`
	MaximumLatencyNanoseconds     penalty `json:"maximum_latency_nanoseconds"`
	ThroughputOperationsPerSecond penalty `json:"throughput_operations_per_second"`
	FailureRate                   penalty `json:"failure_rate"`
}

type penalty struct {
	WritersOnly    float64  `json:"writers_only"`
	MixedAgent     float64  `json:"mixed_agent"`
	ActualPenalty  float64  `json:"actual_penalty"`
	PercentPenalty *float64 `json:"percent_penalty"`
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
	if len(os.Args) == 2 && os.Args[1] == "--blocked-decompression" {
		measured, err := runBlockedDecompressionReport()
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
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--hook-contention" {
		measured, err := runHookContentionReport()
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
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "loadbench: unexpected arguments")
		os.Exit(1)
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

func runHookContentionReport() (hookContentionReport, error) {
	fixtures := fixedFixtures()
	if err := validateStartup(os.Getenv("KTX_DB"), fixtures); err != nil {
		return hookContentionReport{}, err
	}
	root, err := os.MkdirTemp("", "ktx-hook-contention-")
	if err != nil {
		return hookContentionReport{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	binary, hash, err := localBinary()
	if err != nil {
		return hookContentionReport{}, err
	}
	report := hookContentionReport{
		SchemaVersion: hookContentionSchema, Workload: fixtures[0].payloadIdentity,
		WarmupRounds: warmupRounds, MeasuredRounds: measuredRounds,
		ConcurrencyLevels:         append([]int(nil), concurrencyLevels...),
		ProcessTimeoutNanoseconds: processTimeout.Nanoseconds(), PipelineTimeoutNanoseconds: (2 * time.Second).Nanoseconds(),
		StartupRetryNanoseconds: (2 * time.Second).Nanoseconds(), StartupRetryMaximumAttempts: 2,
		Environment: environmentIdentity{
			KTXPath: binary, KTXSHA256: hash, GoVersion: runtime.Version(), OperatingSystem: runtime.GOOS,
			Architecture: runtime.GOARCH, Processor: processorIdentity(),
			DatabaseSelection:     "explicit KTX_DB beneath a fresh owned round root",
			DisposableEnvironment: []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"},
		},
	}
	for _, concurrency := range concurrencyLevels {
		for roundIndex := 0; roundIndex < warmupRounds+measuredRounds; roundIndex++ {
			measured, err := runHookContentionRound(root, processRunner{binary: binary}, fixtures[0], concurrency, roundIndex)
			if err != nil {
				return hookContentionReport{}, err
			}
			report.Rounds = append(report.Rounds, measured)
		}
	}
	if err := validateHookContentionRoundLabels(report); err != nil {
		return hookContentionReport{}, err
	}
	return report, nil
}

func runHookContentionRound(root string, runner processRunner, fixture fixture, concurrency, roundIndex int) (hookContentionRound, error) {
	environment, err := newRoundEnvironment(root, fmt.Sprintf("hook-only-%02d-%02d", concurrency, roundIndex))
	if err != nil {
		return hookContentionRound{}, err
	}
	if err := initializeRoundDatabase(environment.db); err != nil {
		return hookContentionRound{}, err
	}
	configDir := filepath.Join(environment.root, "config", "ktx")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return hookContentionRound{}, err
	}
	if err := os.WriteFile(filepath.Join(configDir, "ktx.toml"), []byte("[pipeline.default]\nstages = [\"head:1\"]\n"), 0o600); err != nil {
		return hookContentionRound{}, err
	}
	payloads := make([][]byte, concurrency)
	base := 900000 + concurrency*1000 + roundIndex*100
	for index := range payloads {
		payloads[index] = agentHookPayload(fmt.Sprintf("hook-only-%d-%d-%d", concurrency, roundIndex, index), stableID(base+index), fixture)
	}
	results := concurrentDistinctPayloads(runner, environment.env, payloads)
	reconciliation, err := reconcileHookContention(environment.db, concurrency)
	if err != nil {
		return hookContentionRound{}, err
	}
	failures := summarize(results, completed(results), 0, 0).FailureCodes
	if len(failures) != 0 || !reconciliation.Matches {
		return hookContentionRound{}, fmt.Errorf("hook-only contention %d/%d failed: failures=%v reconciliation=%+v", concurrency, roundIndex, failures, reconciliation)
	}
	kind, number := hookContentionRoundLabel(roundIndex)
	return hookContentionRound{Kind: kind, Concurrency: concurrency, Round: number, Attempts: concurrency, Completions: completed(results), FailureCodes: failures, Reconciliation: reconciliation}, nil
}

func hookContentionRoundLabel(roundIndex int) (string, int) {
	if roundIndex < warmupRounds {
		return "warmup", roundIndex + 1
	}
	return "measured", roundIndex - warmupRounds + 1
}

func validateHookContentionRoundLabels(report hookContentionReport) error {
	var warmups, measured int
	for _, round := range report.Rounds {
		switch round.Kind {
		case "warmup":
			warmups++
		case "measured":
			measured++
		default:
			return fmt.Errorf("hook-only contention round kind %q is invalid", round.Kind)
		}
	}
	if want := len(report.ConcurrencyLevels) * report.WarmupRounds; warmups != want {
		return fmt.Errorf("hook-only contention warmup labels=%d, want %d", warmups, want)
	}
	if want := len(report.ConcurrencyLevels) * report.MeasuredRounds; measured != want {
		return fmt.Errorf("hook-only contention measured labels=%d, want %d", measured, want)
	}
	return nil
}

func reconcileHookContention(path string, expected int) (hookContentionReconciliation, error) {
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return hookContentionReconciliation{}, err
	}
	defer database.Close()
	result := hookContentionReconciliation{ExpectedSources: expected, ExpectedSessions: 1, ExpectedSessionRollup: expected, ExpectedArtifacts: expected, ExpectedDispositions: expected}
	queries := []struct {
		query string
		dest  *int
	}{
		{`SELECT count(*) FROM tool_calls`, &result.PersistedSources},
		{`SELECT count(*) FROM sessions`, &result.PersistedSessions},
		{`SELECT COALESCE(sum(tool_call_count), 0) FROM sessions`, &result.PersistedSessionRollup},
		{`SELECT count(*) FROM compressed`, &result.PersistedArtifacts},
		{`SELECT count(*) FROM compression_dispositions`, &result.PersistedDispositions},
		{`SELECT count(*) FROM tool_calls WHERE producer = 'hook'`, &result.SourceHookProducers},
		{`SELECT count(*) FROM compressed WHERE producer = 'hook'`, &result.ArtifactHookProducers},
	}
	for _, item := range queries {
		if err := database.QueryRow(item.query).Scan(item.dest); err != nil {
			return hookContentionReconciliation{}, err
		}
	}
	result.Matches = result.PersistedSources == expected && result.PersistedSessions == 1 && result.PersistedSessionRollup == expected &&
		result.PersistedArtifacts == expected && result.PersistedDispositions == expected && result.SourceHookProducers == expected && result.ArtifactHookProducers == expected
	return result, nil
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

	started := time.Now().UTC()
	measured, err := withOwnedEnvironment(root, func() (report, error) {
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
		agentLoads, penalties, err := runAgentLoads(root, runner, fixtures[0])
		if err != nil {
			return report{}, err
		}
		measured.AgentLoads = agentLoads
		measured.ContentionPenalties = penalties

		scenarios, err := runScenarios(root, runner, fixtures[0])
		if err != nil {
			return report{}, err
		}
		measured.Scenarios = scenarios
		target, err := runBlockedReaderTarget(root, runner, fixtures[0], fixtures[2])
		if err != nil {
			return report{}, err
		}
		measured.BlockedReaderTarget = target
		return measured, nil
	})
	if err != nil {
		return report{}, err
	}
	finished := time.Now().UTC()
	measured.StartedAt = started.Format(time.RFC3339Nano)
	measured.FinishedAt = finished.Format(time.RFC3339Nano)
	measured.DurationNanoseconds = finished.Sub(started).Nanoseconds()
	if err := validateMandatoryLoadReport(measured); err != nil {
		return report{}, err
	}
	return measured, nil
}

func runBlockedDecompressionReport() (blockedDecompressionReport, error) {
	fixtures := fixedFixtures()
	if err := validateStartup(os.Getenv("KTX_DB"), fixtures); err != nil {
		return blockedDecompressionReport{}, err
	}
	root, err := os.MkdirTemp("", "ktx-decompress-process-")
	if err != nil {
		return blockedDecompressionReport{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	binary, hash, err := localBinary()
	if err != nil {
		return blockedDecompressionReport{}, err
	}
	started := time.Now().UTC()
	target, err := runBlockedDecompressionTarget(root, processRunner{binary: binary}, fixtures[0], fixtures[2])
	if err != nil {
		return blockedDecompressionReport{}, err
	}
	finished := time.Now().UTC()
	measured := blockedDecompressionReport{
		SchemaVersion: blockedDecompressSchema,
		StartedAt:     started.Format(time.RFC3339Nano), FinishedAt: finished.Format(time.RFC3339Nano),
		DurationNanoseconds: finished.Sub(started).Nanoseconds(),
		Environment: environmentIdentity{
			KTXPath: binary, KTXSHA256: hash, GoVersion: runtime.Version(), OperatingSystem: runtime.GOOS,
			Architecture: runtime.GOARCH, Processor: processorIdentity(),
			DatabaseSelection:     "explicit KTX_DB beneath a fresh owned round root",
			DisposableEnvironment: []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"},
		},
		Target: target,
	}
	if err := validateBlockedDecompressionReport(measured); err != nil {
		return blockedDecompressionReport{}, err
	}
	return measured, nil
}

func validateBlockedDecompressionReport(measured blockedDecompressionReport) error {
	if measured.SchemaVersion != blockedDecompressSchema || measured.DurationNanoseconds < 0 {
		return fmt.Errorf("blocked-decompression report identity is invalid")
	}
	started, startErr := time.Parse(time.RFC3339Nano, measured.StartedAt)
	finished, finishErr := time.Parse(time.RFC3339Nano, measured.FinishedAt)
	if startErr != nil || finishErr != nil || finished.Sub(started).Nanoseconds() != measured.DurationNanoseconds {
		return fmt.Errorf("blocked-decompression report timing is invalid")
	}
	target := measured.Target
	if target.SchemaVersion != blockedDecompressSchema || target.Workload.Name != "blocked-decompression-output-v1" ||
		target.Workload.Bytes != 2*1024*1024 || target.Workload.GeneratorSeed != 6003 || len(target.Workload.SHA256) != 64 {
		return fmt.Errorf("blocked-decompression workload identity is invalid")
	}
	if !target.OutputStarted || !target.Backpressured || !target.HookObservedBeforeOutputRelease || target.HookLatencyNanoseconds < 0 {
		return fmt.Errorf("blocked-decompression observation is incomplete")
	}
	if !target.OutputByteExact || target.OutputBytes != target.Workload.Bytes || target.OutputSHA256 != target.Workload.SHA256 {
		return fmt.Errorf("blocked-decompression output is not byte-exact")
	}
	if target.HookOutcome != "ok" && target.HookOutcome != "database-unavailable" {
		return fmt.Errorf("unsupported hook failure %q", target.HookOutcome)
	}
	if (target.HookOutcome == "ok" && (target.HookExitCode != 0 || target.HookFailureCode != "")) ||
		(target.HookOutcome == "database-unavailable" && (target.HookExitCode != 4 || target.HookFailureCode != "database-unavailable")) {
		return fmt.Errorf("hook outcome identity is inconsistent")
	}
	if len(target.Participants) != 3 ||
		!hasParticipant(target.Participants, "supported-ktx", "blocked-decompression", "backpressured", "") ||
		!hasParticipant(target.Participants, "supported-ktx", "unrelated-one-shot-hook", target.HookOutcome, target.HookFailureCode) ||
		!hasParticipant(target.Participants, "engineering-harness", "exact-reconciliation", "matched", "") {
		return fmt.Errorf("blocked-decompression participant identity is invalid")
	}
	if !target.Reconciliation.Matches ||
		target.Reconciliation.ExpectedSources != target.Reconciliation.PersistedSources ||
		target.Reconciliation.ExpectedArtifacts != target.Reconciliation.PersistedArtifacts ||
		target.Reconciliation.ExpectedDispositions != target.Reconciliation.PersistedDispositions ||
		target.Reconciliation.ExpectedSessions != target.Reconciliation.PersistedSessions {
		return fmt.Errorf("blocked-decompression reconciliation mismatch")
	}
	return nil
}

func validateStartup(ambient string, fixtures []fixture) error {
	if ambient != "" {
		return fmt.Errorf("refuse ambient KTX_DB %q; unset it before starting the load harness", ambient)
	}
	return validateFixtures(fixtures)
}

func validateFixtures(fixtures []fixture) error {
	if len(fixtures) != 3 {
		return fmt.Errorf("fixture count = %d, want 3", len(fixtures))
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
	if err := validateReportTiming(measured.StartedAt, measured.FinishedAt, measured.DurationNanoseconds); err != nil {
		return fmt.Errorf("report timing: %w", err)
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
	if err := validateAgentLoads(measured.AgentLoads, measured.ContentionPenalties); err != nil {
		return err
	}
	if err := validateMandatoryScenarios(measured.Scenarios); err != nil {
		return err
	}
	return validateBlockedReaderTarget(measured.BlockedReaderTarget)
}

func validateAgentLoads(loads []agentLoad, penalties contentionPenalties) error {
	if len(loads) != 2 {
		return fmt.Errorf("agent load count=%d, want 2", len(loads))
	}
	byName := make(map[string]agentLoad, len(loads))
	for _, load := range loads {
		if _, exists := byName[load.Name]; exists {
			return fmt.Errorf("duplicate agent load %q", load.Name)
		}
		byName[load.Name] = load
	}
	for _, contract := range []struct {
		name    string
		readers int
	}{{"writers-only", 0}, {"mixed-agent", agentReaderStreams}} {
		load, ok := byName[contract.name]
		if !ok {
			return fmt.Errorf("missing required agent load %q", contract.name)
		}
		if load.WriterStreams != agentWriterStreams || load.EventsPerWriterStream != streamEvents ||
			load.ReaderStreams != contract.readers || load.QueriesPerReaderStream != contract.readers*streamEvents ||
			len(load.SupportedFailureCodes) != 0 {
			return fmt.Errorf("%s workload identity is invalid", contract.name)
		}
		if len(load.Rounds) != measuredRounds {
			return fmt.Errorf("%s measured rounds=%d, want %d", contract.name, len(load.Rounds), measuredRounds)
		}
		rounds := append([]round{load.Warmup}, load.Rounds...)
		for index, measured := range rounds {
			if err := validateAgentLoadRound(measured, contract.readers); err != nil {
				return fmt.Errorf("%s round %d: %w", contract.name, index, err)
			}
		}
		if len(load.MeasuredWriterMetrics.FailureCodes) != 0 || load.MeasuredWriterMetrics.FailureRate != 0 {
			return fmt.Errorf("%s aggregate writers reported unsupported failures", contract.name)
		}
		if contract.readers == 0 && load.MeasuredReaderMetrics != nil {
			return fmt.Errorf("writers-only aggregate unexpectedly reports readers")
		}
		if contract.readers != 0 && (load.MeasuredReaderMetrics == nil || len(load.MeasuredReaderMetrics.FailureCodes) != 0 || load.MeasuredReaderMetrics.FailureRate != 0) {
			return fmt.Errorf("mixed-agent aggregate reader failed")
		}
	}
	writers := byName["writers-only"].MeasuredWriterMetrics
	mixed := byName["mixed-agent"].MeasuredWriterMetrics
	if !penaltiesEqual(penalties, compareAgentLoads(writers, mixed)) {
		return fmt.Errorf("contention penalties do not match measured writer metrics")
	}
	return nil
}

func validateAgentLoadRound(measured round, readers int) error {
	expectedWriters := agentWriterStreams * streamEvents
	if measured.BootstrapToolCalls != 0 || measured.WriterAttempts != expectedWriters || measured.WriterCompletions != expectedWriters ||
		measured.ReaderAttempts != readers*streamEvents || measured.ReaderCompletions != measured.ReaderAttempts ||
		measured.Reconciliation.ExpectedMinimum != expectedWriters || measured.Reconciliation.ExpectedMaximum != expectedWriters || measured.Reconciliation.Persisted != expectedWriters {
		return fmt.Errorf("attempt, completion, or reconciliation count is invalid: writers=%d/%d readers=%d/%d persisted=%d/%d", measured.WriterCompletions, measured.WriterAttempts, measured.ReaderCompletions, measured.ReaderAttempts, measured.Reconciliation.Persisted, expectedWriters)
	}
	if err := validateMandatoryRound(measured); err != nil {
		return err
	}
	if measured.TimeoutNanoseconds != int64(processTimeout) {
		return fmt.Errorf("process completion bound is invalid")
	}
	seenAgents := make(map[string]bool, agentWriterStreams+readers)
	seenSessions := make(map[string]bool, agentWriterStreams+readers)
	writers, readerCount := 0, 0
	for _, participant := range measured.Participants {
		if participant.Role != "writer-stream" && participant.Role != "reader-stream" {
			continue
		}
		if participant.Class != "supported-ktx" || participant.AgentID == "" || participant.SessionID == "" ||
			seenAgents[participant.AgentID] || seenSessions[participant.SessionID] {
			return fmt.Errorf("duplicate or missing participant/session identity")
		}
		seenAgents[participant.AgentID] = true
		seenSessions[participant.SessionID] = true
		if participant.Attempts != streamEvents || participant.Completions != streamEvents || participant.Outcome != "completed" || participant.FailureCode != "" {
			return fmt.Errorf("participant %q did not complete", participant.AgentID)
		}
		if participant.Role == "writer-stream" {
			writers++
		} else {
			readerCount++
		}
	}
	if writers != agentWriterStreams || readerCount != readers ||
		!hasParticipant(measured.Participants, "engineering-harness", "empty-database-initializer", "ok", "") ||
		!hasParticipant(measured.Participants, "supported-ktx", "reconciliation-query", "ok", "") {
		return fmt.Errorf("missing required participants")
	}
	if readers == 0 && measured.ReaderMetrics != nil {
		return fmt.Errorf("writers-only round unexpectedly reports reader metrics")
	}
	if readers != 0 && (measured.ReaderMetrics == nil || len(measured.ReaderMetrics.FailureCodes) != 0 || measured.ReaderMetrics.FailureRate != 0) {
		return fmt.Errorf("reader failed")
	}
	return nil
}

func penaltiesEqual(left, right contentionPenalties) bool {
	return penaltyEqual(left.P50LatencyNanoseconds, right.P50LatencyNanoseconds) &&
		penaltyEqual(left.P95LatencyNanoseconds, right.P95LatencyNanoseconds) &&
		penaltyEqual(left.P99LatencyNanoseconds, right.P99LatencyNanoseconds) &&
		penaltyEqual(left.MaximumLatencyNanoseconds, right.MaximumLatencyNanoseconds) &&
		penaltyEqual(left.ThroughputOperationsPerSecond, right.ThroughputOperationsPerSecond) &&
		penaltyEqual(left.FailureRate, right.FailureRate)
}

func penaltyEqual(left, right penalty) bool {
	if left.WritersOnly != right.WritersOnly || left.MixedAgent != right.MixedAgent || left.ActualPenalty != right.ActualPenalty {
		return false
	}
	if left.PercentPenalty == nil || right.PercentPenalty == nil {
		return left.PercentPenalty == nil && right.PercentPenalty == nil
	}
	return *left.PercentPenalty == *right.PercentPenalty
}

func validateReportTiming(startedAt, finishedAt string, durationNanoseconds int64) error {
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

func validateBlockedReaderTarget(target blockedReaderTarget) error {
	if target.SchemaVersion != blockedReaderTargetSchema || target.Workload.Name != "blocked-reader-output-v1" || target.Workload.Bytes != 2*1024*1024 || target.Workload.GeneratorSeed == 0 || len(target.Workload.SHA256) != 64 {
		return fmt.Errorf("blocked-reader target workload is invalid")
	}
	if !target.ReaderOutputStarted || !target.ReaderBackpressured || target.HookOutcome == "" || target.HookLatencyNanoseconds < 0 {
		return fmt.Errorf("blocked-reader target outcome is incomplete")
	}
	if !target.Reconciliation.Matches {
		return fmt.Errorf("blocked-reader target reconciliation mismatch")
	}
	return nil
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
		{name: "blocked-reader-output-v1", seed: 6003, size: 2 * 1024 * 1024},
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

func runAgentLoads(root string, runner processRunner, fixture fixture) ([]agentLoad, contentionPenalties, error) {
	loads := make([]agentLoad, 0, 2)
	for _, readers := range []int{0, agentReaderStreams} {
		name := "writers-only"
		if readers != 0 {
			name = "mixed-agent"
		}
		load := agentLoad{
			Name: name, WriterStreams: agentWriterStreams, EventsPerWriterStream: streamEvents,
			ReaderStreams: readers, QueriesPerReaderStream: readers * streamEvents,
			SupportedFailureCodes: []string{},
		}
		var writerResults, readerResults []processResult
		var measuredElapsed time.Duration
		for roundIndex := 0; roundIndex < warmupRounds+measuredRounds; roundIndex++ {
			measured, writers, readers, elapsed, err := runAgentLoadRound(root, runner, fixture, name, roundIndex, load.ReaderStreams)
			if err != nil {
				return nil, contentionPenalties{}, err
			}
			if roundIndex < warmupRounds {
				load.Warmup = measured
				continue
			}
			load.Rounds = append(load.Rounds, measured)
			writerResults = append(writerResults, writers...)
			readerResults = append(readerResults, readers...)
			measuredElapsed += elapsed
		}
		load.MeasuredWriterMetrics = summarize(writerResults, completed(writerResults), measuredElapsed, 0)
		if load.ReaderStreams != 0 {
			readerMetrics := summarize(readerResults, completed(readerResults), measuredElapsed, 0)
			load.MeasuredReaderMetrics = &readerMetrics
		}
		loads = append(loads, load)
	}
	return loads, compareAgentLoads(loads[0].MeasuredWriterMetrics, loads[1].MeasuredWriterMetrics), nil
}

func runAgentLoadRound(root string, runner processRunner, fixture fixture, name string, roundIndex, readerStreams int) (round, []processResult, []processResult, time.Duration, error) {
	environment, err := newRoundEnvironment(root, fmt.Sprintf("agent-%s-%02d", name, roundIndex))
	if err != nil {
		return round{}, nil, nil, 0, err
	}
	if err := initializeRoundDatabase(environment.db); err != nil {
		return round{}, nil, nil, 0, fmt.Errorf("initialize %s round %d: %w", name, roundIndex, err)
	}

	baseID := 600000 + roundIndex*100 + readerStreams*1000
	start := time.Now()
	writers, readers := concurrentAgentStreams(runner, environment, fixture, name, roundIndex, baseID, readerStreams)
	elapsed := time.Since(start)
	allWriters := make([]processResult, 0, agentWriterStreams*streamEvents)
	participants := make([]participant, 0, agentWriterStreams+readerStreams+2)
	for streamIndex, results := range writers {
		allWriters = append(allWriters, results...)
		identity := fmt.Sprintf("writer-%d", streamIndex+1)
		participant := aggregateParticipant("supported-ktx", "writer-stream", results)
		participant.AgentID = identity
		participant.SessionID = agentSessionID(name, roundIndex, identity)
		participants = append(participants, participant)
	}
	allReaders := make([]processResult, 0, readerStreams*streamEvents)
	for streamIndex, results := range readers {
		allReaders = append(allReaders, results...)
		identity := fmt.Sprintf("reader-%d", streamIndex+1)
		participant := aggregateParticipant("supported-ktx", "reader-stream", results)
		participant.AgentID = identity
		participant.SessionID = agentSessionID(name, roundIndex, identity)
		participants = append(participants, participant)
	}
	persisted, finalQuery, err := runner.persistedCount(environment.env)
	if err != nil {
		return round{}, nil, nil, 0, fmt.Errorf("reconcile %s round %d: %w", name, roundIndex, err)
	}
	participants = append(participants,
		participant{Class: "engineering-harness", Role: "empty-database-initializer", Outcome: "ok"},
		participantFromResult("supported-ktx", "reconciliation-query", finalQuery),
	)

	writerMetrics := summarize(allWriters, completed(allWriters), elapsed, 0)
	readerMetrics := summarize(allReaders, completed(allReaders), elapsed, 0)
	addFailure(writerMetrics.FailureCodes, finalQuery)
	reconciliation := exactReconciliation(agentWriterStreams*streamEvents, persisted)
	if !reconciliation.Matches {
		writerMetrics.FailureCodes["reconciliation-mismatch"]++
	}
	if completed(allReaders) != len(allReaders) {
		writerMetrics.FailureCodes["reader-failed"]++
	}
	measured := round{
		FreshOwnedDatabase: true, TimeoutNanoseconds: int64(processTimeout),
		WriterAttempts: len(allWriters), WriterCompletions: completed(allWriters),
		ReaderAttempts: len(allReaders), ReaderCompletions: completed(allReaders),
		Participants: participants, Metrics: writerMetrics,
		Reconciliation: reconciliation, Outcome: measurementOutcome(writerMetrics, reconciliation),
	}
	if readerStreams != 0 {
		measured.ReaderMetrics = &readerMetrics
	}
	return measured, allWriters, allReaders, elapsed, nil
}

func concurrentAgentStreams(runner processRunner, environment roundEnvironment, fixture fixture, name string, roundIndex, baseID, readerStreams int) ([][]processResult, [][]processResult) {
	start := make(chan struct{})
	type streamResult struct {
		index   int
		results []processResult
	}
	writers := make(chan streamResult, agentWriterStreams)
	for streamIndex := 0; streamIndex < agentWriterStreams; streamIndex++ {
		streamIndex := streamIndex
		go func() {
			<-start
			sessionID := agentSessionID(name, roundIndex, fmt.Sprintf("writer-%d", streamIndex+1))
			results := make([]processResult, 0, streamEvents)
			for eventIndex := 0; eventIndex < streamEvents; eventIndex++ {
				results = append(results, runner.hook(environment.env, agentHookPayload(sessionID, stableID(baseID+streamIndex*streamEvents+eventIndex), fixture)))
			}
			writers <- streamResult{index: streamIndex, results: results}
		}()
	}
	readers := make(chan []processResult, readerStreams)
	for range readerStreams {
		go func() {
			<-start
			results := make([]processResult, 0, streamEvents)
			for range streamEvents {
				results = append(results, runner.query(environment.env))
			}
			readers <- results
		}()
	}
	close(start)
	writerResults := make([][]processResult, agentWriterStreams)
	for range agentWriterStreams {
		result := <-writers
		writerResults[result.index] = result.results
	}
	readerResults := make([][]processResult, 0, readerStreams)
	for range readerStreams {
		readerResults = append(readerResults, <-readers)
	}
	return writerResults, readerResults
}

func agentSessionID(load string, round int, identity string) string {
	return fmt.Sprintf("loadbench-%s-%02d-%s", load, round, identity)
}

func initializeRoundDatabase(path string) error {
	database, err := dbpkg.Open(path)
	if err != nil {
		return err
	}
	return database.Close()
}

func compareAgentLoads(writersOnly, mixed metrics) contentionPenalties {
	return contentionPenalties{
		P50LatencyNanoseconds:         latencyPenalty(writersOnly.P50LatencyNanoseconds, mixed.P50LatencyNanoseconds),
		P95LatencyNanoseconds:         latencyPenalty(writersOnly.P95LatencyNanoseconds, mixed.P95LatencyNanoseconds),
		P99LatencyNanoseconds:         latencyPenalty(writersOnly.P99LatencyNanoseconds, mixed.P99LatencyNanoseconds),
		MaximumLatencyNanoseconds:     latencyPenalty(float64(writersOnly.MaximumLatencyNanoseconds), float64(mixed.MaximumLatencyNanoseconds)),
		ThroughputOperationsPerSecond: throughputPenalty(writersOnly.ThroughputOperationsPerSecond, mixed.ThroughputOperationsPerSecond),
		FailureRate:                   latencyPenalty(writersOnly.FailureRate, mixed.FailureRate),
	}
}

func latencyPenalty(baseline, mixed float64) penalty {
	return newPenalty(baseline, mixed, mixed-baseline)
}

func throughputPenalty(baseline, mixed float64) penalty {
	return newPenalty(baseline, mixed, baseline-mixed)
}

func newPenalty(baseline, mixed, actual float64) penalty {
	result := penalty{WritersOnly: baseline, MixedAgent: mixed, ActualPenalty: actual}
	if baseline != 0 || actual == 0 {
		percentage := 0.0
		if baseline != 0 {
			percentage = actual / baseline * 100
		}
		result.PercentPenalty = &percentage
	}
	return result
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

func runBlockedReaderTarget(root string, runner processRunner, hookFixture, readerFixture fixture) (blockedReaderTarget, error) {
	environment, err := newRoundEnvironment(root, "blocked-reader-target")
	if err != nil {
		return blockedReaderTarget{}, err
	}
	readerSourceID := stableID(500000)
	setup := runner.hook(environment.env, hookPayload(readerSourceID, readerFixture))
	if setup.Code != "ok" {
		return blockedReaderTarget{}, fmt.Errorf("initialize blocked-reader target: %s", setup.Code)
	}

	context, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	reader := exec.CommandContext(context, runner.binary, "read", readerSourceID)
	reader.Env = environment.env
	readerStdout, err := reader.StdoutPipe()
	if err != nil {
		return blockedReaderTarget{}, fmt.Errorf("open blocked-reader stdout: %w", err)
	}
	var readerStderr bytes.Buffer
	reader.Stderr = &readerStderr
	if err := reader.Start(); err != nil {
		return blockedReaderTarget{}, fmt.Errorf("start blocked reader: %w", err)
	}
	readerDone := make(chan error, 1)
	go func() { readerDone <- reader.Wait() }()
	readerFinished := false
	defer func() {
		if readerFinished {
			return
		}
		_ = readerStdout.Close()
		_ = reader.Process.Kill()
		select {
		case <-readerDone: //nolint:errcheck // cleanup drains a killed reader; its wait error is not actionable
		case <-time.After(processTimeout):
		}
	}()

	firstByte := make(chan error, 1)
	go func() {
		var first [1]byte
		_, err := io.ReadFull(readerStdout, first[:])
		firstByte <- err
	}()
	select {
	case err := <-firstByte:
		if err != nil {
			return blockedReaderTarget{}, fmt.Errorf("read blocked-reader first byte: %w", err)
		}
	case <-context.Done():
		return blockedReaderTarget{}, fmt.Errorf("blocked reader did not start output: %w", context.Err())
	}
	select {
	case err := <-readerDone:
		readerFinished = true
		return blockedReaderTarget{}, fmt.Errorf("2 MiB reader did not remain backpressured: %v; stderr=%s", err, readerStderr.String())
	case <-time.After(blockedReaderDelay):
	}

	hook := runner.hook(environment.env, hookPayload(stableID(500001), hookFixture))
	if _, err := io.Copy(io.Discard, readerStdout); err != nil {
		return blockedReaderTarget{}, fmt.Errorf("release blocked reader: %w", err)
	}
	select {
	case err := <-readerDone:
		readerFinished = true
		if err != nil {
			return blockedReaderTarget{}, fmt.Errorf("blocked reader after release: %v; stderr=%s", err, readerStderr.String())
		}
	case <-context.Done():
		return blockedReaderTarget{}, fmt.Errorf("blocked reader did not finish after release: %w", context.Err())
	}

	persisted, query, err := runner.persistedCount(environment.env)
	if err != nil {
		return blockedReaderTarget{}, fmt.Errorf("reconcile blocked-reader target: %w", err)
	}
	if query.Code != "ok" {
		return blockedReaderTarget{}, fmt.Errorf("query blocked-reader target: %s", query.Code)
	}
	expected := 1
	if hook.Code == "ok" {
		expected++
	}
	return blockedReaderTarget{
		SchemaVersion:          blockedReaderTargetSchema,
		Workload:               readerFixture.payloadIdentity,
		ReaderOutputStarted:    true,
		ReaderBackpressured:    true,
		HookOutcome:            hook.Code,
		HookExitCode:           hook.ExitCode,
		HookLatencyNanoseconds: hook.LatencyNS,
		HookFailureCode:        failureCode(hook),
		Reconciliation:         exactReconciliation(expected, persisted),
	}, nil
}

func runBlockedDecompressionTarget(root string, runner processRunner, hookFixture, outputFixture fixture) (blockedDecompressionTarget, error) {
	environment, err := newRoundEnvironment(root, "blocked-decompression-target")
	if err != nil {
		return blockedDecompressionTarget{}, err
	}
	sourceID := stableID(510000)
	setup := runner.hook(environment.env, hookPayload(sourceID, outputFixture))
	if setup.Code != "ok" {
		return blockedDecompressionTarget{}, fmt.Errorf("initialize blocked decompression: %s", setup.Code)
	}
	artifactID, err := seedCompressedArtifact(environment.db)
	if err != nil {
		return blockedDecompressionTarget{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, runner.binary, "decompress", artifactID)
	command.Env = environment.env
	stdout, err := command.StdoutPipe()
	if err != nil {
		return blockedDecompressionTarget{}, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return blockedDecompressionTarget{}, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	defer func() {
		if finished {
			return
		}
		_ = stdout.Close()
		_ = command.Process.Kill()
		<-done //nolint:errcheck // cleanup drains a killed process; its wait error is not actionable
	}()
	var first [1]byte
	if _, err := io.ReadFull(stdout, first[:]); err != nil {
		return blockedDecompressionTarget{}, fmt.Errorf("observe decompression output start: %w", err)
	}
	select {
	case err := <-done:
		finished = true
		return blockedDecompressionTarget{}, fmt.Errorf("decompression did not remain backpressured: %v; stderr=%s", err, stderr.String())
	case <-time.After(blockedReaderDelay):
	}

	hook := runner.hook(environment.env, hookPayload(stableID(510001), hookFixture))
	output := bytes.NewBuffer([]byte{first[0]})
	if _, err := io.Copy(output, stdout); err != nil {
		return blockedDecompressionTarget{}, fmt.Errorf("release decompression output: %w", err)
	}
	select {
	case err := <-done:
		finished = true
		if err != nil {
			return blockedDecompressionTarget{}, fmt.Errorf("decompression after output release: %w; stderr=%s", err, stderr.String())
		}
	case <-time.After(processTimeout):
		_ = command.Process.Kill()
		<-done //nolint:errcheck // the process was killed after the timeout; its wait error only restates that
		finished = true
	}
	reconciled, err := reconcileDecompressionDatabase(environment.db, hook.Code == "ok")
	if err != nil {
		return blockedDecompressionTarget{}, err
	}
	workload := outputFixture.payloadIdentity
	workload.Name = "blocked-decompression-output-v1"
	outputSum := sha256.Sum256(output.Bytes())
	return blockedDecompressionTarget{
		SchemaVersion: blockedDecompressSchema, Workload: workload,
		OutputStarted: true, Backpressured: true, HookObservedBeforeOutputRelease: true,
		OutputBytes: output.Len(), OutputSHA256: hex.EncodeToString(outputSum[:]), OutputByteExact: bytes.Equal(output.Bytes(), outputFixture.data),
		HookOutcome: hook.Code, HookExitCode: hook.ExitCode, HookLatencyNanoseconds: hook.LatencyNS, HookFailureCode: failureCode(hook),
		Participants: []participant{
			{Class: "supported-ktx", Role: "blocked-decompression", Outcome: "backpressured"},
			participantFromResult("supported-ktx", "unrelated-one-shot-hook", hook),
			{Class: "engineering-harness", Role: "exact-reconciliation", Outcome: "matched"},
		},
		Reconciliation: reconciled,
	}, nil
}

func seedCompressedArtifact(path string) (string, error) {
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return "", err
	}
	defer database.Close()
	var sourceID string
	if err := database.QueryRow(`SELECT id FROM tool_calls ORDER BY timestamp, id LIMIT 1`).Scan(&sourceID); err != nil {
		return "", fmt.Errorf("find decompression source: %w", err)
	}
	id := stableID(510002)
	compressed := []byte("x")
	sum := sha256.Sum256(compressed)
	timestamp := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	if _, err := database.Exec(`INSERT INTO compressed (id, source_id, output, content_hash, pipeline, timestamp, timestamp_unix_nano, producer) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, sourceID, string(compressed), hex.EncodeToString(sum[:]), `{}`, timestamp.Format(time.RFC3339Nano), timestamp.UnixNano(), dbpkg.ProducerLoadFixture); err != nil {
		return "", fmt.Errorf("seed decompression artifact: %w", err)
	}
	return id, nil
}

func reconcileDecompressionDatabase(path string, hookPersisted bool) (decompressionReconciliation, error) {
	database, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return decompressionReconciliation{}, err
	}
	defer database.Close()
	counts := make([]int, 4)
	for index, query := range []string{"SELECT count(*) FROM tool_calls", "SELECT count(*) FROM compressed", "SELECT count(*) FROM compression_dispositions", "SELECT count(*) FROM sessions"} {
		if err := database.QueryRow(query).Scan(&counts[index]); err != nil {
			return decompressionReconciliation{}, err
		}
	}
	expected := 1
	if hookPersisted {
		expected = 2
	}
	result := decompressionReconciliation{
		ExpectedSources: expected, PersistedSources: counts[0], ExpectedArtifacts: 1, PersistedArtifacts: counts[1],
		ExpectedDispositions: expected, PersistedDispositions: counts[2], ExpectedSessions: 1, PersistedSessions: counts[3],
	}
	result.Matches = result.ExpectedSources == result.PersistedSources && result.ExpectedArtifacts == result.PersistedArtifacts &&
		result.ExpectedDispositions == result.PersistedDispositions && result.ExpectedSessions == result.PersistedSessions
	return result, nil
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

func agentHookPayload(sessionID, callID string, fixture fixture) []byte {
	payload, err := json.Marshal(map[string]any{
		"runtime": "opencode",
		"input": map[string]any{
			"tool": "bash", "sessionID": sessionID, "callID": callID,
			"args": map[string]string{"command": "loadbench agent stream"},
		},
		"output": map[string]any{
			"output": string(fixture.data), "metadata": map[string]int{"exit": 0},
		},
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

func concurrentDistinctPayloads(runner processRunner, environment []string, payloads [][]byte) []processResult {
	start := make(chan struct{})
	results := make(chan processResult, len(payloads))
	for _, payload := range payloads {
		payload := payload
		go func() {
			<-start
			results <- runner.hook(environment, payload)
		}()
	}
	close(start)
	collected := make([]processResult, 0, len(payloads))
	for range payloads {
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
	failureRate := 0.0
	if len(results) != 0 {
		failureRate = float64(len(results)-completed(results)) / float64(len(results))
	}
	return metrics{
		ThroughputOperationsPerSecond: throughput(completedOperations, elapsed),
		P50LatencyNanoseconds:         percentile(latencies, 0.50),
		P95LatencyNanoseconds:         percentile(latencies, 0.95),
		P99LatencyNanoseconds:         percentile(latencies, 0.99),
		MaximumLatencyNanoseconds:     maximum(latencies),
		FailureRate:                   failureRate,
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
