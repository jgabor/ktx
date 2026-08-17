package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/pipeline/stages"
)

func TestDirectFixturesRecordComponentProducer(t *testing.T) {
	root := t.TempDir()
	ctx := &benchmarkContext{root: root, configHome: filepath.Join(root, "config")}
	var persistence benchmarkSpec
	for _, spec := range benchmarkSpecs(fixedWorkloads()) {
		if spec.Component == "persistence" && spec.Case == "success" {
			persistence = spec
			break
		}
	}
	operation, err := persistence.newOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.run(); err != nil {
		t.Fatal(err)
	}
	if err := operation.close(); err != nil {
		t.Fatal(err)
	}
	database, err := dbpkg.Open(filepath.Join(root, "database", "persistence-1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	row, err := database.GetToolCall(benchmarkID(1))
	if err != nil {
		t.Fatal(err)
	}
	if row.Producer == nil || *row.Producer != dbpkg.ProducerComponentBench {
		t.Fatalf("component fixture producer = %v, %v", row.Producer, err)
	}

	if _, err := measureFocusedDecompression(ctx, fixedWorkloads()[0]); err != nil {
		t.Fatal(err)
	}
	artifactDB, err := dbpkg.Open(filepath.Join(root, "database", "focused-decompress-2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer artifactDB.Close()
	artifact, err := artifactDB.GetCompressed(focusedDecompressArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Producer == nil || *artifact.Producer != dbpkg.ProducerComponentBench {
		t.Fatalf("component artifact producer = %v", artifact.Producer)
	}
}

func TestFixedWorkloadsAreReproducibleAndValid(t *testing.T) {
	first := fixedWorkloads()
	second := fixedWorkloads()
	if len(first) != 9 || len(second) != len(first) {
		t.Fatalf("workload count = %d and %d, want 9", len(first), len(second))
	}

	for index, workload := range first {
		if workload.Bytes != len(workload.data) {
			t.Errorf("%s bytes = %d, want %d", workload.Name, workload.Bytes, len(workload.data))
		}
		if workload.GeneratorSeed == 0 {
			t.Errorf("%s has no generator seed", workload.Name)
		}
		sum := sha256.Sum256(workload.data)
		if got := hex.EncodeToString(sum[:]); got != workload.SHA256 {
			t.Errorf("%s hash = %q, want %q", workload.Name, got, workload.SHA256)
		}
		if !bytes.Equal(workload.data, second[index].data) || workload.workloadID != second[index].workloadID {
			t.Errorf("%s is not reproducible", workload.Name)
		}

		switch workload.Kind {
		case "json":
			if !json.Valid(workload.data) {
				t.Errorf("%s is not valid JSON", workload.Name)
			}
		case "ndjson":
			for lineIndex, line := range bytes.Split(bytes.TrimSuffix(workload.data, []byte("\n")), []byte("\n")) {
				if !json.Valid(line) {
					t.Errorf("%s line %d is not valid JSON", workload.Name, lineIndex+1)
				}
			}
		}
	}
}

func TestCatalogCoversComponentsCasesAndBuiltins(t *testing.T) {
	workloads := fixedWorkloads()
	specs := benchmarkSpecs(workloads)
	byWorkload := make(map[string]map[string]bool)
	for _, spec := range specs {
		if spec.Name != benchmarkName(spec.Component, spec.Case, spec.Workload.Name) {
			t.Errorf("unstable name %q", spec.Name)
		}
		if spec.ProcessedBytesPerOperation <= 0 {
			t.Errorf("%s processed bytes = %d", spec.Name, spec.ProcessedBytesPerOperation)
		}
		if byWorkload[spec.Workload.Name] == nil {
			byWorkload[spec.Workload.Name] = map[string]bool{}
		}
		byWorkload[spec.Workload.Name][spec.Component+"/"+spec.Case] = true
	}

	for _, workload := range workloads {
		coverage := byWorkload[workload.Name]
		for _, required := range []string{
			"parser/success", "parser/malformed",
			"config/success", "config/malformed",
			"router/success", "router/malformed",
			"pipeline/success", "pipeline/passthrough", "pipeline/failure",
			"persistence/success", "persistence/failure",
		} {
			if !coverage[required] {
				t.Errorf("%s is missing %s", workload.Name, required)
			}
		}
		for _, stageName := range stages.BuiltinStageNames() {
			for _, caseName := range []string{"success", "malformed"} {
				required := "stage/" + stageName + "/" + caseName
				if !coverage[required] {
					t.Errorf("%s is missing %s", workload.Name, required)
				}
			}
		}
	}
}

func TestResultCarriesFiveMeasuredSamplesAndEnvironment(t *testing.T) {
	root := t.TempDir()
	fixture := fixedWorkloads()[0]
	spec := parserBenchmarkSpec(fixture, "success")
	measured, err := runSpecs(root, filepath.Join(root, "config"), []workload{fixture}, []benchmarkSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	if measured.SchemaVersion != resultSchemaVersion || measured.WarmupSamples != warmupSamples || measured.MeasuredSamples != measuredSamples {
		t.Fatalf("result version/counts = %#v", measured)
	}
	if measured.Machine.GoVersion == "" || measured.Machine.OperatingSystem == "" || measured.Machine.Processor == "" || measured.Machine.Filesystem.Type == "" {
		t.Fatalf("incomplete machine identity: %#v", measured.Machine)
	}
	if len(measured.Benchmarks) != 1 {
		t.Fatalf("benchmark count = %d, want 1", len(measured.Benchmarks))
	}
	benchmark := measured.Benchmarks[0]
	if benchmark.SchemaVersion != benchmarkSchemaVersion || len(benchmark.Samples) != measuredSamples || benchmark.Warmup.Operations != 1 {
		t.Fatalf("benchmark result = %#v", benchmark)
	}
	for _, sample := range benchmark.Samples {
		if sample.Operations < 1 || sample.ProcessedBytes < benchmark.ProcessedBytesPerOperation || sample.LatencyNanosecondsPerOperation <= 0 || sample.ThroughputBytesPerSecond <= 0 {
			t.Errorf("invalid sample: %#v", sample)
		}
	}
	if benchmark.Distribution.LatencyNanosecondsPerOperation.Minimum <= 0 || benchmark.Distribution.LatencyNanosecondsPerOperation.Maximum < benchmark.Distribution.LatencyNanosecondsPerOperation.Minimum {
		t.Errorf("invalid latency distribution: %#v", benchmark.Distribution)
	}
	if len(measured.FocusedReads) != 4 {
		t.Fatalf("focused read count = %d, want 4", len(measured.FocusedReads))
	}
	for index, focused := range measured.FocusedReads {
		if focused.SchemaVersion != focusedReadSchemaVersion || focused.Workload != fixedFocusedReadWorkloads()[index].workloadID || len(focused.Samples) != measuredSamples {
			t.Errorf("focused read %d = %#v", index, focused)
		}
		if focused.Distribution.LatencyNanosecondsPerOperation.Median <= 0 || focused.Distribution.AllocatedBytesPerOperation.Median < 0 || focused.Distribution.AllocationsPerOperation.Median < 0 {
			t.Errorf("focused read distribution = %#v", focused.Distribution)
		}
	}
	if len(measured.FocusedDecompressions) != 4 {
		t.Fatalf("focused decompression count = %d, want 4", len(measured.FocusedDecompressions))
	}
	for index, focused := range measured.FocusedDecompressions {
		if focused.SchemaVersion != focusedDecompressSchemaVersion || focused.Name != "ktx-decompress" || focused.Workload != fixedFocusedDecompressWorkloads()[index].workloadID || len(focused.Samples) != measuredSamples {
			t.Errorf("focused decompression %d = %#v", index, focused)
		}
		if focused.Distribution.LatencyNanosecondsPerOperation.Median <= 0 || focused.Distribution.AllocatedBytesPerOperation.Median < 0 || focused.Distribution.AllocationsPerOperation.Median < 0 {
			t.Errorf("focused decompression distribution = %#v", focused.Distribution)
		}
	}
	if _, err := json.Marshal(measured); err != nil {
		t.Fatalf("marshal result: %v", err)
	}
}

func TestFocusedDecompressionWorkloadsAreStable(t *testing.T) {
	first, second := fixedFocusedDecompressWorkloads(), fixedFocusedDecompressWorkloads()
	for index, size := range []int{1024, 65536, 1048576, 2097152} {
		if first[index].Bytes != size || first[index].Kind != "ktx-decompress-output" || first[index].GeneratorSeed == 0 || first[index].workloadID != second[index].workloadID || !bytes.Equal(first[index].data, second[index].data) {
			t.Fatalf("workload %d is not stable: %#v", index, first[index])
		}
	}
	first[0].data[0] ^= 1
	sum := sha256.Sum256(first[0].data)
	if hex.EncodeToString(sum[:]) == first[0].SHA256 {
		t.Fatal("changed decompression workload retained its identity hash")
	}
}

func TestFocusedDecompressionSampleValidation(t *testing.T) {
	valid := focusedReadResult{Warmup: sample{Operations: 1}, Samples: make([]sample, measuredSamples)}
	for index := range valid.Samples {
		valid.Samples[index].Operations = 1
	}
	if err := validateFocusedSamples(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Samples = invalid.Samples[:measuredSamples-1]
	if err := validateFocusedSamples(invalid); err == nil || !strings.Contains(err.Error(), "samples") {
		t.Fatalf("invalid sample count error = %v", err)
	}
}

func TestPersistenceDoesNotUseKTXDB(t *testing.T) {
	root := t.TempDir()
	ambient := filepath.Join(t.TempDir(), "ambient.db")
	t.Setenv("KTX_DB", ambient)
	workload := fixedWorkloads()[0]
	ctx := &benchmarkContext{root: root, configHome: filepath.Join(root, "config")}
	for _, caseName := range []string{"success", "failure"} {
		spec := persistenceBenchmarkSpec(workload, caseName)
		operation, err := spec.newOperation(ctx)
		if err != nil {
			t.Fatalf("prepare %s: %v", caseName, err)
		}
		if err := operation.run(); err != nil {
			_ = closeOperation(operation)
			t.Fatalf("run %s: %v", caseName, err)
		}
		if err := closeOperation(operation); err != nil {
			t.Fatalf("close %s: %v", caseName, err)
		}
	}
	if _, err := os.Stat(ambient); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("component benchmark touched KTX_DB: %v", err)
	}
}

func TestComponentReportTimingRejectsInvalidOrder(t *testing.T) {
	if err := validateReportTiming("2026-08-10T00:00:00.000000002Z", "2026-08-10T00:00:00.000000001Z", 0); err == nil || !strings.Contains(err.Error(), "precedes") {
		t.Fatalf("reversed timing error = %v", err)
	}
	if err := validateReportTiming("2026-08-10T00:00:00.000000001Z", "2026-08-10T00:00:00.000000002Z", 1); err != nil {
		t.Fatal(err)
	}
}

func TestComponentBenchmarkDocumentation(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "component-benchmarks.md"))
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(string(contents), "\n", " ")
	for _, required := range []string{
		"mage componentBench",
		"schema_version",
		"warm-up",
		"five measured samples",
		"ktx bench",
		"KTX_DB",
		"durable engineering verification",
		"started_at",
		"2097152",
		"allocated bytes",
		"ktx decompress",
		"schema_version: 3",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("documentation is missing %q", required)
		}
	}
}
