// componentbench emits reproducible, benchmark-only component measurements.
// It never resolves KTX_DB or invokes the public ktx CLI.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jgabor/ktx/internal/cli"
	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/parser"
	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/pipeline/stages"
	"github.com/jgabor/ktx/internal/router"
)

const (
	resultSchemaVersion      = 2
	benchmarkSchemaVersion   = 1
	focusedReadSchemaVersion = 1
	warmupSamples            = 1
	measuredSamples          = 5
	maximumSampleBytes       = 1 << 20
	maximumOperations        = 64
	focusedReadSessionID     = "readbenchsession"
	focusedReadSourceID      = "readbenchsourcea"
)

type result struct {
	SchemaVersion       int                 `json:"schema_version"`
	StartedAt           string              `json:"started_at"`
	FinishedAt          string              `json:"finished_at"`
	DurationNanoseconds int64               `json:"duration_nanoseconds"`
	WarmupSamples       int                 `json:"warmup_samples"`
	MeasuredSamples     int                 `json:"measured_samples"`
	Machine             machineIdentity     `json:"machine"`
	Workloads           []workloadID        `json:"workloads"`
	Benchmarks          []benchmarkResult   `json:"benchmarks"`
	FocusedReads        []focusedReadResult `json:"focused_reads"`
}

type machineIdentity struct {
	GoVersion       string             `json:"go_version"`
	OperatingSystem string             `json:"operating_system"`
	Architecture    string             `json:"architecture"`
	Processor       string             `json:"processor"`
	Filesystem      filesystemIdentity `json:"filesystem"`
}

type filesystemIdentity struct {
	Type      string `json:"type"`
	BlockSize int64  `json:"block_size,omitempty"`
}

type workloadID struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Bytes         int    `json:"bytes"`
	GeneratorSeed uint64 `json:"generator_seed"`
	SHA256        string `json:"sha256"`
}

type workload struct {
	workloadID
	data []byte
}

type benchmarkResult struct {
	SchemaVersion              int          `json:"schema_version"`
	Name                       string       `json:"name"`
	Component                  string       `json:"component"`
	Case                       string       `json:"case"`
	Workload                   string       `json:"workload"`
	ProcessedBytesPerOperation int64        `json:"processed_bytes_per_operation"`
	Warmup                     sample       `json:"warmup"`
	Samples                    []sample     `json:"samples"`
	Distribution               distribution `json:"distribution"`
}

type focusedReadResult struct {
	SchemaVersion int          `json:"schema_version"`
	Name          string       `json:"name"`
	Workload      workloadID   `json:"workload"`
	Warmup        sample       `json:"warmup"`
	Samples       []sample     `json:"samples"`
	Distribution  distribution `json:"distribution"`
}

type sample struct {
	Operations                     int     `json:"operations"`
	ProcessedBytes                 int64   `json:"processed_bytes"`
	LatencyNanosecondsPerOperation float64 `json:"latency_nanoseconds_per_operation"`
	ThroughputBytesPerSecond       float64 `json:"throughput_bytes_per_second"`
	AllocatedBytesPerOperation     uint64  `json:"allocated_bytes_per_operation"`
	AllocationsPerOperation        uint64  `json:"allocations_per_operation"`
}

type distribution struct {
	LatencyNanosecondsPerOperation numericDistribution `json:"latency_nanoseconds_per_operation"`
	ThroughputBytesPerSecond       numericDistribution `json:"throughput_bytes_per_second"`
	AllocatedBytesPerOperation     numericDistribution `json:"allocated_bytes_per_operation"`
	AllocationsPerOperation        numericDistribution `json:"allocations_per_operation"`
}

type numericDistribution struct {
	Minimum float64 `json:"minimum"`
	Median  float64 `json:"median"`
	Maximum float64 `json:"maximum"`
	Mean    float64 `json:"mean"`
}

type benchmarkOperation struct {
	run   func() error
	close func() error
}

type benchmarkSpec struct {
	Name                       string
	Component                  string
	Case                       string
	Workload                   workload
	ProcessedBytesPerOperation int64
	newOperation               func(*benchmarkContext) (benchmarkOperation, error)
}

type benchmarkContext struct {
	root       string
	configHome string
	sequence   int
}

func main() {
	measured, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "componentbench: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(measured); err != nil {
		fmt.Fprintf(os.Stderr, "componentbench: encode result: %v\n", err)
		os.Exit(1)
	}
}

func run() (result, error) {
	root, err := os.MkdirTemp("", "ktx-component-bench-")
	if err != nil {
		return result{}, fmt.Errorf("create benchmark root: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()

	configHome := filepath.Join(root, "config")
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		return result{}, fmt.Errorf("create benchmark config root: %w", err)
	}
	var measured result
	if err := withEnvironment("XDG_CONFIG_HOME", configHome, func() error {
		var err error
		measured, err = runWithRoot(root, configHome)
		return err
	}); err != nil {
		return result{}, err
	}
	return measured, nil
}

func runWithRoot(root, configHome string) (result, error) {
	started := time.Now().UTC()
	workloads := fixedWorkloads()
	measured, err := runSpecs(root, configHome, workloads, benchmarkSpecs(workloads))
	if err != nil {
		return result{}, err
	}
	finished := time.Now().UTC()
	measured.StartedAt = started.Format(time.RFC3339Nano)
	measured.FinishedAt = finished.Format(time.RFC3339Nano)
	measured.DurationNanoseconds = finished.Sub(started).Nanoseconds()
	if err := validateComponentReport(measured); err != nil {
		return result{}, err
	}
	return measured, nil
}

func runSpecs(root, configHome string, workloads []workload, specs []benchmarkSpec) (result, error) {
	ctx := &benchmarkContext{root: root, configHome: configHome}
	measured := result{
		SchemaVersion:   resultSchemaVersion,
		WarmupSamples:   warmupSamples,
		MeasuredSamples: measuredSamples,
		Machine: machineIdentity{
			GoVersion:       runtime.Version(),
			OperatingSystem: runtime.GOOS,
			Architecture:    runtime.GOARCH,
			Processor:       processorIdentity(),
			Filesystem:      filesystemIdentityFor(root),
		},
		Workloads: make([]workloadID, len(workloads)),
	}
	for index, workload := range workloads {
		measured.Workloads[index] = workload.workloadID
	}

	for _, spec := range specs {
		measurement, err := measure(ctx, spec)
		if err != nil {
			return result{}, err
		}
		measured.Benchmarks = append(measured.Benchmarks, measurement)
	}
	focusedReads, err := measureFocusedReads(ctx, fixedFocusedReadWorkloads())
	if err != nil {
		return result{}, err
	}
	measured.FocusedReads = focusedReads
	return measured, nil
}

func validateComponentReport(measured result) error {
	if measured.SchemaVersion != resultSchemaVersion {
		return fmt.Errorf("component report schema = %d, want %d", measured.SchemaVersion, resultSchemaVersion)
	}
	if err := validateReportTiming(measured.StartedAt, measured.FinishedAt, measured.DurationNanoseconds); err != nil {
		return fmt.Errorf("component report timing: %w", err)
	}
	expected := fixedFocusedReadWorkloads()
	if len(measured.FocusedReads) != len(expected) {
		return fmt.Errorf("focused read measurements = %d, want %d", len(measured.FocusedReads), len(expected))
	}
	for index, measurement := range measured.FocusedReads {
		if measurement.SchemaVersion != focusedReadSchemaVersion || measurement.Name != "ktx-read" || measurement.Workload != expected[index].workloadID {
			return fmt.Errorf("focused read measurement %d has invalid identity", index+1)
		}
		if err := validateSample(measurement.Warmup); err != nil {
			return fmt.Errorf("focused read %s warmup: %w", measurement.Workload.Name, err)
		}
		if len(measurement.Samples) != measuredSamples {
			return fmt.Errorf("focused read %s samples = %d, want %d", measurement.Workload.Name, len(measurement.Samples), measuredSamples)
		}
		for sampleIndex, sample := range measurement.Samples {
			if err := validateSample(sample); err != nil {
				return fmt.Errorf("focused read %s sample %d: %w", measurement.Workload.Name, sampleIndex+1, err)
			}
		}
	}
	return nil
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

func validateSample(measured sample) error {
	if measured.Operations <= 0 || measured.ProcessedBytes < 0 || measured.LatencyNanosecondsPerOperation < 0 || measured.ThroughputBytesPerSecond < 0 {
		return fmt.Errorf("invalid metric values")
	}
	return nil
}

func withEnvironment(name, value string, fn func() error) error {
	previous, present := os.LookupEnv(name)
	if err := os.Setenv(name, value); err != nil {
		return err
	}
	defer func() {
		if present {
			_ = os.Setenv(name, previous)
		} else {
			_ = os.Unsetenv(name)
		}
	}()
	return fn()
}

func fixedWorkloads() []workload {
	sizes := []int{1024, 65536, 1048576}
	kinds := []string{"plain-text", "json", "ndjson"}
	workloads := make([]workload, 0, len(sizes)*len(kinds))
	for sizeIndex, size := range sizes {
		for kindIndex, kind := range kinds {
			seed := uint64(1000 + sizeIndex*len(kinds) + kindIndex)
			data := generateWorkload(kind, size, seed)
			sum := sha256.Sum256(data)
			workloads = append(workloads, workload{
				workloadID: workloadID{
					Name:          fmt.Sprintf("%s-%d", kind, size),
					Kind:          kind,
					Bytes:         len(data),
					GeneratorSeed: seed,
					SHA256:        hex.EncodeToString(sum[:]),
				},
				data: data,
			})
		}
	}
	return workloads
}

func fixedFocusedReadWorkloads() []workload {
	sizes := []int{1024, 65536, 1048576, 2097152}
	workloads := make([]workload, 0, len(sizes))
	for index, size := range sizes {
		seed := uint64(2000 + index)
		data := generateWorkload("plain-text", size, seed)
		sum := sha256.Sum256(data)
		workloads = append(workloads, workload{
			workloadID: workloadID{
				Name:          fmt.Sprintf("ktx-read-output-%d", size),
				Kind:          "ktx-read-output",
				Bytes:         len(data),
				GeneratorSeed: seed,
				SHA256:        hex.EncodeToString(sum[:]),
			},
			data: data,
		})
	}
	return workloads
}

func generateWorkload(kind string, size int, seed uint64) []byte {
	switch kind {
	case "plain-text":
		data := make([]byte, size)
		for index := range data {
			if index%79 == 78 {
				data[index] = '\n'
				continue
			}
			data[index] = generatedLetter(&seed)
		}
		return data
	case "json":
		const prefix = `{"payload":"`
		const suffix = `"}`
		data := make([]byte, size)
		copy(data, prefix)
		for index := len(prefix); index < size-len(suffix); index++ {
			data[index] = generatedLetter(&seed)
		}
		copy(data[size-len(suffix):], suffix)
		return data
	case "ndjson":
		const prefix = `{"payload":"`
		const suffix = `"}` + "\n"
		data := make([]byte, 0, size)
		for len(data) < size {
			remaining := size - len(data)
			lineLength := min(256, remaining)
			minimumLineLength := len(prefix) + len(suffix)
			if remaining > lineLength && remaining-lineLength < minimumLineLength {
				lineLength = remaining - minimumLineLength
			}
			payloadLength := lineLength - minimumLineLength
			data = append(data, prefix...)
			for range payloadLength {
				data = append(data, generatedLetter(&seed))
			}
			data = append(data, suffix...)
		}
		return data
	default:
		panic("unknown workload kind " + kind)
	}
}

func generatedLetter(seed *uint64) byte {
	*seed = *seed*6364136223846793005 + 1442695040888963407
	return 'a' + byte((*seed>>32)%26)
}

func benchmarkSpecs(workloads []workload) []benchmarkSpec {
	var specs []benchmarkSpec
	for _, workload := range workloads {
		for _, malformed := range []bool{false, true} {
			caseName := "success"
			if malformed {
				caseName = "malformed"
			}
			specs = append(specs,
				parserBenchmarkSpec(workload, caseName),
				configBenchmarkSpec(workload, caseName),
				routerBenchmarkSpec(workload, caseName),
			)
			for _, stageName := range stages.BuiltinStageNames() {
				specs = append(specs, stageBenchmarkSpec(stageName, workload, caseName))
			}
		}
		specs = append(specs,
			pipelineBenchmarkSpec(workload, "success"),
			pipelineBenchmarkSpec(workload, "passthrough"),
			pipelineBenchmarkSpec(workload, "failure"),
			persistenceBenchmarkSpec(workload, "success"),
			persistenceBenchmarkSpec(workload, "failure"),
		)
	}
	return specs
}

func parserBenchmarkSpec(workload workload, caseName string) benchmarkSpec {
	input := parserInput(workload.data, caseName == "malformed")
	return benchmarkSpec{
		Name:                       benchmarkName("parser", caseName, workload.Name),
		Component:                  "parser",
		Case:                       caseName,
		Workload:                   workload,
		ProcessedBytesPerOperation: int64(len(input)),
		newOperation: func(*benchmarkContext) (benchmarkOperation, error) {
			return benchmarkOperation{run: func() error {
				_, accepted := parser.ParseEvent(input)
				if caseName == "malformed" && accepted {
					return fmt.Errorf("parser accepted malformed workload")
				}
				if caseName == "success" && !accepted {
					return fmt.Errorf("parser rejected valid workload")
				}
				return nil
			}}, nil
		},
	}
}

func parserInput(data []byte, malformed bool) []byte {
	payload, err := json.Marshal(struct {
		Tool      string `json:"tool"`
		Input     string `json:"input"`
		Output    string `json:"output"`
		Timestamp string `json:"timestamp"`
	}{
		Tool:      "bash",
		Input:     `{"command":"component-benchmark"}`,
		Output:    string(data),
		Timestamp: "2026-08-09T00:00:00Z",
	})
	if err != nil {
		panic(err)
	}
	if malformed {
		return payload[:len(payload)-1]
	}
	return payload
}

func configBenchmarkSpec(workload workload, caseName string) benchmarkSpec {
	input := configInput(workload.data, caseName == "malformed")
	return benchmarkSpec{
		Name:                       benchmarkName("config", caseName, workload.Name),
		Component:                  "config",
		Case:                       caseName,
		Workload:                   workload,
		ProcessedBytesPerOperation: int64(len(input)),
		newOperation: func(ctx *benchmarkContext) (benchmarkOperation, error) {
			path := filepath.Join(ctx.configHome, "ktx", "ktx.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return benchmarkOperation{}, err
			}
			if err := os.WriteFile(path, input, 0o600); err != nil {
				return benchmarkOperation{}, err
			}
			return benchmarkOperation{run: func() error {
				loaded, err := config.Load()
				if caseName == "malformed" {
					if err == nil {
						return fmt.Errorf("config accepted malformed workload")
					}
					return nil
				}
				if err != nil {
					return err
				}
				if len(loaded.PipelineDefault) == 0 {
					return fmt.Errorf("config did not load benchmark pipeline")
				}
				return nil
			}}, nil
		},
	}
}

func configInput(data []byte, malformed bool) []byte {
	input := []byte("[pipeline.default]\nstages = [\"head:10\"]\n[[plugin]]\nname = \"fixture\"\ncommand = \"true\"\ndescription = " + strconv.Quote(string(data)) + "\n")
	if malformed {
		return append(input, "broken = ["...)
	}
	return input
}

func routerBenchmarkSpec(workload workload, caseName string) benchmarkSpec {
	input := commandInput(workload.data)
	if caseName == "malformed" {
		input = append([]byte(nil), workload.data...)
	}
	toolCall := &dbpkg.ToolCall{Tool: "bash", Input: string(input)}
	routes := []config.RouteRule{{Tool: "bash", CommandPrefix: "component-benchmark", Profile: "selected"}}
	return benchmarkSpec{
		Name:                       benchmarkName("router", caseName, workload.Name),
		Component:                  "router",
		Case:                       caseName,
		Workload:                   workload,
		ProcessedBytesPerOperation: int64(len(input)),
		newOperation: func(*benchmarkContext) (benchmarkOperation, error) {
			return benchmarkOperation{run: func() error {
				resolution := router.Resolve(toolCall, routes, "default")
				if caseName == "success" && resolution.Profile != "selected" {
					return fmt.Errorf("router profile = %q, want selected", resolution.Profile)
				}
				if caseName == "malformed" && resolution.Profile != "default" {
					return fmt.Errorf("router profile = %q, want default", resolution.Profile)
				}
				return nil
			}}, nil
		},
	}
}

func commandInput(data []byte) []byte {
	input, err := json.Marshal(map[string]string{"command": "component-benchmark " + string(data)})
	if err != nil {
		panic(err)
	}
	return input
}

func stageBenchmarkSpec(stageName string, workload workload, caseName string) benchmarkSpec {
	input := stageInput(stageName, workload.data, caseName == "malformed")
	token := stageToken(stageName)
	return benchmarkSpec{
		Name:                       benchmarkName("stage/"+stageName, caseName, workload.Name),
		Component:                  "stage/" + stageName,
		Case:                       caseName,
		Workload:                   workload,
		ProcessedBytesPerOperation: int64(len(input)),
		newOperation: func(*benchmarkContext) (benchmarkOperation, error) {
			registry := pipeline.NewRegistry()
			stages.RegisterBuiltins(registry)
			return benchmarkOperation{run: func() error {
				stage, found, err := registry.Lookup(token)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("built-in stage %q was not registered", stageName)
				}
				_, err = stage.Apply(input)
				return err
			}}, nil
		},
	}
}

func stageInput(stageName string, data []byte, malformed bool) []byte {
	input := append([]byte(nil), data...)
	if malformed {
		switch stageName {
		case "strip-ansi":
			return append([]byte("\x1b["), input...)
		case "json-compact", "unwrap-result":
			return append([]byte(`{"payload":`), input...)
		default:
			return append([]byte{0xff}, input...)
		}
	}
	switch stageName {
	case "strip-ansi":
		return append(append([]byte("\x1b[31m"), input...), []byte("\x1b[0m")...)
	case "dedup-blank":
		return bytes.ReplaceAll(input, []byte("\n"), []byte("\n\n\n"))
	case "normalize":
		return bytes.ReplaceAll(input, []byte("\n"), []byte("  \r\n"))
	default:
		return input
	}
}

func stageToken(stageName string) string {
	switch stageName {
	case "head":
		return "head:10"
	case "truncate":
		return "truncate:256"
	case "keep-ends":
		return "keep-ends:10:10"
	case "grep":
		return "grep:."
	case "grep-v":
		return "grep-v:^$"
	case "token-budget":
		return "token-budget:256"
	case "replace":
		return "replace:zzzz::z"
	default:
		return stageName
	}
}

func pipelineBenchmarkSpec(workload workload, caseName string) benchmarkSpec {
	input := append([]byte(nil), workload.data...)
	stagesForCase := []string{"strip-ansi", "normalize", "dedup-blank", "head:20", "token-budget:256"}
	switch caseName {
	case "passthrough":
		stagesForCase = nil
	case "failure":
		stagesForCase = []string{"head:not-a-number"}
	}
	return benchmarkSpec{
		Name:                       benchmarkName("pipeline", caseName, workload.Name),
		Component:                  "pipeline",
		Case:                       caseName,
		Workload:                   workload,
		ProcessedBytesPerOperation: int64(len(input)),
		newOperation: func(*benchmarkContext) (benchmarkOperation, error) {
			registry := pipeline.NewRegistry()
			stages.RegisterBuiltins(registry)
			runner := &pipeline.Runner{Registry: registry, Pipeline: stagesForCase}
			return benchmarkOperation{run: func() error {
				result := runner.Run(input)
				if caseName == "passthrough" && !bytes.Equal(result.Out, input) {
					return fmt.Errorf("passthrough pipeline changed input")
				}
				if caseName == "failure" && !bytes.Equal(result.Out, input) {
					return fmt.Errorf("failure pipeline did not preserve input")
				}
				return nil
			}}, nil
		},
	}
}

func persistenceBenchmarkSpec(workload workload, caseName string) benchmarkSpec {
	return benchmarkSpec{
		Name:                       benchmarkName("persistence", caseName, workload.Name),
		Component:                  "persistence",
		Case:                       caseName,
		Workload:                   workload,
		ProcessedBytesPerOperation: int64(len(workload.data) + len(`{"command":"component-benchmark"}`)),
		newOperation: func(ctx *benchmarkContext) (benchmarkOperation, error) {
			ctx.sequence++
			path := filepath.Join(ctx.root, "database", fmt.Sprintf("persistence-%d.db", ctx.sequence))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return benchmarkOperation{}, err
			}
			database, err := dbpkg.Open(path)
			if err != nil {
				return benchmarkOperation{}, err
			}
			const sessionID = "ssssssssssssssss"
			if err := database.InsertSession(&dbpkg.Session{ID: sessionID, Agent: "componentbench", StartedAt: "2026-08-09T00:00:00Z", LastSeenAt: "2026-08-09T00:00:00Z"}); err != nil {
				database.Close()
				return benchmarkOperation{}, err
			}
			output := string(workload.data)
			hash := dbpkg.ContentHash(output)
			index := 0
			return benchmarkOperation{
				run: func() error {
					index++
					callSessionID := sessionID
					if caseName == "failure" {
						callSessionID = "mmmmmmmmmmmmmmmm"
					}
					err := database.PersistToolCall(&dbpkg.ToolCall{
						ID:          benchmarkID(index),
						SessionID:   callSessionID,
						Tool:        "bash",
						Input:       `{"command":"component-benchmark"}`,
						Output:      output,
						ContentHash: hash,
						Timestamp:   "2026-08-09T00:00:00Z",
					})
					if caseName == "failure" {
						if err == nil {
							return fmt.Errorf("persistence accepted missing session")
						}
						return nil
					}
					return err
				},
				close: database.Close,
			}, nil
		},
	}
}

func measureFocusedReads(ctx *benchmarkContext, workloads []workload) ([]focusedReadResult, error) {
	results := make([]focusedReadResult, 0, len(workloads))
	for _, workload := range workloads {
		measurement, err := measureFocusedRead(ctx, workload)
		if err != nil {
			return nil, err
		}
		results = append(results, measurement)
	}
	return results, nil
}

func measureFocusedRead(ctx *benchmarkContext, workload workload) (focusedReadResult, error) {
	ctx.sequence++
	path := filepath.Join(ctx.root, "database", fmt.Sprintf("focused-read-%d.db", ctx.sequence))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return focusedReadResult{}, err
	}
	database, err := dbpkg.Open(path)
	if err != nil {
		return focusedReadResult{}, err
	}
	if err := database.InsertSession(&dbpkg.Session{
		ID: focusedReadSessionID, Agent: "componentbench", StartedAt: "2026-08-09T00:00:00Z", LastSeenAt: "2026-08-09T00:00:00Z",
	}); err != nil {
		_ = database.Close()
		return focusedReadResult{}, err
	}
	output := string(workload.data)
	if err := database.PersistToolCall(&dbpkg.ToolCall{
		ID: focusedReadSourceID, SessionID: focusedReadSessionID, Tool: "read", Input: "component-benchmark",
		Output: output, ContentHash: dbpkg.ContentHash(output), Timestamp: "2026-08-09T00:00:00Z",
	}); err != nil {
		_ = database.Close()
		return focusedReadResult{}, err
	}
	if err := database.Close(); err != nil {
		return focusedReadResult{}, err
	}

	operation := benchmarkOperation{run: func() error {
		var stderr bytes.Buffer
		if code := cli.RunForComponentBench([]string{"read", focusedReadSourceID}, io.Discard, &stderr); code != 0 {
			return fmt.Errorf("ktx read exit %d: %s", code, strings.TrimSpace(stderr.String()))
		}
		return nil
	}}
	var warmup sample
	var samples []sample
	err = withEnvironment("KTX_DB", path, func() error {
		var err error
		warmup, err = measureOperation(operation, int64(workload.Bytes), 1)
		if err != nil {
			return err
		}
		samples = make([]sample, 0, measuredSamples)
		for sampleIndex := 0; sampleIndex < measuredSamples; sampleIndex++ {
			measured, err := measureOperation(operation, int64(workload.Bytes), operationsFor(int64(workload.Bytes)))
			if err != nil {
				return err
			}
			samples = append(samples, measured)
		}
		return nil
	})
	if err != nil {
		return focusedReadResult{}, fmt.Errorf("measure focused read %s: %w", workload.Name, err)
	}
	return focusedReadResult{
		SchemaVersion: focusedReadSchemaVersion,
		Name:          "ktx-read",
		Workload:      workload.workloadID,
		Warmup:        warmup,
		Samples:       samples,
		Distribution:  distributionFor(samples),
	}, nil
}

func benchmarkID(index int) string {
	id := make([]byte, 16)
	for position := len(id) - 1; position >= 0; position-- {
		id[position] = byte('a' + index%26)
		index /= 26
	}
	return string(id)
}

func benchmarkName(component, caseName, workloadName string) string {
	return component + "/" + caseName + "/" + workloadName
}

func measure(ctx *benchmarkContext, spec benchmarkSpec) (benchmarkResult, error) {
	operations := operationsFor(spec.ProcessedBytesPerOperation)
	warmupOperation, err := spec.newOperation(ctx)
	if err != nil {
		return benchmarkResult{}, fmt.Errorf("prepare %s warmup: %w", spec.Name, err)
	}
	warmup, measureErr := measureOperation(warmupOperation, spec.ProcessedBytesPerOperation, 1)
	closeErr := closeOperation(warmupOperation)
	if measureErr != nil {
		return benchmarkResult{}, fmt.Errorf("run %s warmup: %w", spec.Name, measureErr)
	}
	if closeErr != nil {
		return benchmarkResult{}, fmt.Errorf("close %s warmup: %w", spec.Name, closeErr)
	}

	samples := make([]sample, 0, measuredSamples)
	for sampleIndex := 0; sampleIndex < measuredSamples; sampleIndex++ {
		operation, err := spec.newOperation(ctx)
		if err != nil {
			return benchmarkResult{}, fmt.Errorf("prepare %s sample %d: %w", spec.Name, sampleIndex+1, err)
		}
		measured, measureErr := measureOperation(operation, spec.ProcessedBytesPerOperation, operations)
		closeErr := closeOperation(operation)
		if measureErr != nil {
			return benchmarkResult{}, fmt.Errorf("run %s sample %d: %w", spec.Name, sampleIndex+1, measureErr)
		}
		if closeErr != nil {
			return benchmarkResult{}, fmt.Errorf("close %s sample %d: %w", spec.Name, sampleIndex+1, closeErr)
		}
		samples = append(samples, measured)
	}

	return benchmarkResult{
		SchemaVersion:              benchmarkSchemaVersion,
		Name:                       spec.Name,
		Component:                  spec.Component,
		Case:                       spec.Case,
		Workload:                   spec.Workload.Name,
		ProcessedBytesPerOperation: spec.ProcessedBytesPerOperation,
		Warmup:                     warmup,
		Samples:                    samples,
		Distribution:               distributionFor(samples),
	}, nil
}

func closeOperation(operation benchmarkOperation) error {
	if operation.close == nil {
		return nil
	}
	return operation.close()
}

func operationsFor(processedBytes int64) int {
	if processedBytes <= 0 {
		return 1
	}
	operations := int64(maximumSampleBytes) / processedBytes
	if operations < 1 {
		return 1
	}
	if operations > maximumOperations {
		return maximumOperations
	}
	return int(operations)
}

func measureOperation(operation benchmarkOperation, processedBytes int64, operations int) (sample, error) {
	if operation.run == nil {
		return sample{}, fmt.Errorf("benchmark operation is nil")
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	for operationIndex := 0; operationIndex < operations; operationIndex++ {
		if err := operation.run(); err != nil {
			return sample{}, err
		}
	}
	elapsed := time.Since(started)
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}
	runtime.ReadMemStats(&after)
	processed := processedBytes * int64(operations)
	return sample{
		Operations:                     operations,
		ProcessedBytes:                 processed,
		LatencyNanosecondsPerOperation: float64(elapsed.Nanoseconds()) / float64(operations),
		ThroughputBytesPerSecond:       float64(processed) / elapsed.Seconds(),
		AllocatedBytesPerOperation:     (after.TotalAlloc - before.TotalAlloc) / uint64(operations),
		AllocationsPerOperation:        (after.Mallocs - before.Mallocs) / uint64(operations),
	}, nil
}

func distributionFor(samples []sample) distribution {
	return distribution{
		LatencyNanosecondsPerOperation: distributionOf(samples, func(sample sample) float64 { return sample.LatencyNanosecondsPerOperation }),
		ThroughputBytesPerSecond:       distributionOf(samples, func(sample sample) float64 { return sample.ThroughputBytesPerSecond }),
		AllocatedBytesPerOperation:     distributionOf(samples, func(sample sample) float64 { return float64(sample.AllocatedBytesPerOperation) }),
		AllocationsPerOperation:        distributionOf(samples, func(sample sample) float64 { return float64(sample.AllocationsPerOperation) }),
	}
}

func distributionOf(samples []sample, value func(sample) float64) numericDistribution {
	values := make([]float64, len(samples))
	var total float64
	for index, sample := range samples {
		values[index] = value(sample)
		total += values[index]
	}
	sort.Float64s(values)
	return numericDistribution{
		Minimum: values[0],
		Median:  values[len(values)/2],
		Maximum: values[len(values)-1],
		Mean:    total / float64(len(values)),
	}
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
