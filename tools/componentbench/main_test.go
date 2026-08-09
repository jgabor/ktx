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

	"github.com/jgabor/ktx/internal/pipeline/stages"
)

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
	if benchmark.SchemaVersion != resultSchemaVersion || len(benchmark.Samples) != measuredSamples || benchmark.Warmup.Operations != 1 {
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
	if _, err := json.Marshal(measured); err != nil {
		t.Fatalf("marshal result: %v", err)
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
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("documentation is missing %q", required)
		}
	}
}
