package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFixedFixturesAreReproducible(t *testing.T) {
	first := fixedFixtures()
	second := fixedFixtures()
	if err := validateFixtures(first); err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || len(second) != len(first) {
		t.Fatalf("fixture counts = %d and %d, want 3", len(first), len(second))
	}
	for index, fixture := range first {
		if fixture.payloadIdentity != second[index].payloadIdentity || !bytes.Equal(fixture.data, second[index].data) {
			t.Errorf("fixture %q is not reproducible", fixture.Name)
		}
		sum := sha256.Sum256(fixture.data)
		if fixture.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("fixture %q hash does not match", fixture.Name)
		}
	}
}

func TestStartupRejectsAmbientDatabaseAndInvalidFixture(t *testing.T) {
	fixtures := fixedFixtures()
	if err := validateStartup("/ambient/ktx.db", fixtures); err == nil || !strings.Contains(err.Error(), "ambient KTX_DB") {
		t.Fatalf("ambient database error = %v", err)
	}

	invalid := append([]fixture(nil), fixtures...)
	invalid[0].SHA256 = "not-a-hash"
	if err := validateStartup("", invalid); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("invalid fixture error = %v", err)
	}
}

func TestWorkerEnvironmentSelectsOnlyOwnedDatabase(t *testing.T) {
	t.Setenv("KTX_DB", "/ambient/ktx.db")
	t.Setenv("KTX_SESSION_ID", "ambient-session")
	environment := environmentMap(workerEnvironment("/owned/round", "/owned/round/db/ktx.db"))
	if got := environment["KTX_DB"]; got != "/owned/round/db/ktx.db" {
		t.Fatalf("KTX_DB = %q", got)
	}
	if got := environment["KTX_SESSION_ID"]; got != "" {
		t.Fatalf("KTX_SESSION_ID = %q, want cleared", got)
	}
	for name, want := range map[string]string{
		"HOME":            "/owned/round/home",
		"XDG_DATA_HOME":   "/owned/round/data",
		"XDG_CONFIG_HOME": "/owned/round/config",
		"XDG_CACHE_HOME":  "/owned/round/cache",
		"XDG_STATE_HOME":  "/owned/round/state",
		"TMPDIR":          "/owned/round/tmp",
		"KTX_VERIFY_ROOT": "/owned/round",
	} {
		if got := environment[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestMetricsAndReconciliationExposeVarianceAndFailures(t *testing.T) {
	results := []processResult{
		{Code: "ok", LatencyNS: 10},
		{Code: "database-unavailable", LatencyNS: 20},
		{Code: "ok", LatencyNS: 30},
		{Code: "ok", LatencyNS: 40},
		{Code: "ok", LatencyNS: 50},
	}
	measured := summarize(results, 4, time.Second, 2)
	if measured.ThroughputOperationsPerSecond != 4 || measured.P50LatencyNanoseconds != 30 || measured.P95LatencyNanoseconds != 50 || measured.P99LatencyNanoseconds != 50 || measured.MaximumLatencyNanoseconds != 50 {
		t.Fatalf("metrics = %#v", measured)
	}
	if measured.Retries != 2 || measured.CumulativeLockWaitNanoseconds != 20 || measured.FailureCodes["database-unavailable"] != 1 {
		t.Fatalf("contention metrics = %#v", measured)
	}
	reconciled := exactReconciliation(5, 5)
	if !reconciled.Matches || measurementOutcome(measured, reconciled) != "completed-with-classified-failures" {
		t.Fatalf("reconciliation = %#v, outcome = %q", reconciled, measurementOutcome(measured, reconciled))
	}
	mismatched := exactReconciliation(5, 4)
	if mismatched.Matches || measurementOutcome(measured, mismatched) != "reconciliation-mismatch" {
		t.Fatalf("mismatch = %#v", mismatched)
	}
}

func TestMandatoryLoadReportValidation(t *testing.T) {
	t.Run("accepts required scenarios", func(t *testing.T) {
		if err := validateMandatoryLoadReport(mandatoryLoadReport()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rejects reconciliation mismatch", func(t *testing.T) {
		measured := mandatoryLoadReport()
		measured.Measurements[0].Rounds[0].Reconciliation.Matches = false
		if err := validateMandatoryLoadReport(measured); err == nil || !strings.Contains(err.Error(), "reconciliation") {
			t.Fatalf("reconciliation mismatch = %v", err)
		}
	})

	t.Run("rejects untyped foreign exhaustion", func(t *testing.T) {
		measured := mandatoryLoadReport()
		for index := range measured.Scenarios {
			if measured.Scenarios[index].Name == "foreign-writer-reader-exhaustion" {
				measured.Scenarios[index].Metrics.FailureCodes = map[string]int{"internal": 1}
			}
		}
		if err := validateMandatoryLoadReport(measured); err == nil || !strings.Contains(err.Error(), "foreign writer") {
			t.Fatalf("untyped foreign exhaustion = %v", err)
		}
	})

	t.Run("rejects reversed timestamps", func(t *testing.T) {
		measured := mandatoryLoadReport()
		measured.StartedAt, measured.FinishedAt = measured.FinishedAt, measured.StartedAt
		if err := validateMandatoryLoadReport(measured); err == nil || !strings.Contains(err.Error(), "precedes") {
			t.Fatalf("reversed timestamps = %v", err)
		}
	})
}

func TestAgentLoadValidation(t *testing.T) {
	valid := mandatoryLoadReport()
	if err := validateMandatoryLoadReport(valid); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		edit func(*report)
		want string
	}{
		{name: "missing hooks", edit: func(report *report) { report.AgentLoads[0].Rounds[0].WriterCompletions-- }, want: "completion"},
		{name: "failed reader", edit: func(report *report) {
			round := &report.AgentLoads[1].Rounds[0]
			round.ReaderCompletions--
			round.ReaderMetrics.FailureCodes["database-unavailable"] = 1
		}, want: "completion"},
		{name: "duplicate identity", edit: func(report *report) {
			round := &report.AgentLoads[0].Rounds[0]
			round.Participants[3].AgentID = round.Participants[2].AgentID
		}, want: "identity"},
		{name: "missing participant", edit: func(report *report) {
			round := &report.AgentLoads[0].Rounds[0]
			round.Participants = round.Participants[1:]
		}, want: "participants"},
		{name: "unexpected supported failure", edit: func(report *report) {
			report.AgentLoads[0].Rounds[0].Metrics.FailureCodes["database-unavailable"] = 1
		}, want: "failures"},
		{name: "reconciliation mismatch", edit: func(report *report) {
			report.AgentLoads[1].Rounds[0].Reconciliation.Persisted--
			report.AgentLoads[1].Rounds[0].Reconciliation.Matches = false
		}, want: "reconciliation"},
		{name: "invalid penalty", edit: func(report *report) {
			report.ContentionPenalties.P95LatencyNanoseconds.ActualPenalty++
		}, want: "penalties"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			measured := mandatoryLoadReport()
			test.edit(&measured)
			if err := validateMandatoryLoadReport(measured); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestContentionPenaltiesReportActualAndPercent(t *testing.T) {
	writers := metrics{P50LatencyNanoseconds: 100, P95LatencyNanoseconds: 200, P99LatencyNanoseconds: 300, MaximumLatencyNanoseconds: 400, ThroughputOperationsPerSecond: 40}
	mixed := metrics{P50LatencyNanoseconds: 110, P95LatencyNanoseconds: 240, P99LatencyNanoseconds: 360, MaximumLatencyNanoseconds: 500, ThroughputOperationsPerSecond: 30}
	measured := compareAgentLoads(writers, mixed)
	if measured.P50LatencyNanoseconds.ActualPenalty != 10 || *measured.P50LatencyNanoseconds.PercentPenalty != 10 ||
		measured.ThroughputOperationsPerSecond.ActualPenalty != 10 || *measured.ThroughputOperationsPerSecond.PercentPenalty != 25 {
		t.Fatalf("penalties = %#v", measured)
	}
	if measured.FailureRate.PercentPenalty == nil || *measured.FailureRate.PercentPenalty != 0 {
		t.Fatalf("zero-baseline failure percentage = %v, want 0", measured.FailureRate.PercentPenalty)
	}
}

func TestBlockedDecompressionReportValidation(t *testing.T) {
	valid := validBlockedDecompressionReport()
	if err := validateBlockedDecompressionReport(valid); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		edit func(*blockedDecompressionReport)
		want string
	}{
		{name: "malformed identity", edit: func(report *blockedDecompressionReport) { report.Target.Workload.Name = "blocked-reader-output-v1" }, want: "identity"},
		{name: "output mismatch", edit: func(report *blockedDecompressionReport) { report.Target.OutputByteExact = false }, want: "byte-exact"},
		{name: "missing reconciliation", edit: func(report *blockedDecompressionReport) { report.Target.Reconciliation = decompressionReconciliation{} }, want: "reconciliation"},
		{name: "unsupported failure", edit: func(report *blockedDecompressionReport) { report.Target.HookOutcome = "timed-out" }, want: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			measured := valid
			measured.Target.Participants = append([]participant(nil), valid.Target.Participants...)
			test.edit(&measured)
			if err := validateBlockedDecompressionReport(measured); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func validBlockedDecompressionReport() blockedDecompressionReport {
	started := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	reconciliation := decompressionReconciliation{
		ExpectedSources: 1, PersistedSources: 1, ExpectedArtifacts: 1, PersistedArtifacts: 1,
		ExpectedDispositions: 1, PersistedDispositions: 1, ExpectedSessions: 1, PersistedSessions: 1, Matches: true,
	}
	return blockedDecompressionReport{
		SchemaVersion: blockedDecompressSchema, StartedAt: started.Format(time.RFC3339Nano),
		FinishedAt: started.Add(time.Nanosecond).Format(time.RFC3339Nano), DurationNanoseconds: 1,
		Target: blockedDecompressionTarget{
			SchemaVersion: blockedDecompressSchema,
			Workload:      payloadIdentity{Name: "blocked-decompression-output-v1", Bytes: 2 * 1024 * 1024, GeneratorSeed: 6003, SHA256: strings.Repeat("a", 64)},
			OutputStarted: true, Backpressured: true, HookObservedBeforeOutputRelease: true,
			OutputBytes: 2 * 1024 * 1024, OutputSHA256: strings.Repeat("a", 64), OutputByteExact: true,
			HookOutcome: "database-unavailable", HookExitCode: 4, HookLatencyNanoseconds: 1, HookFailureCode: "database-unavailable",
			Participants: []participant{
				{Class: "supported-ktx", Role: "blocked-decompression", Outcome: "backpressured"},
				{Class: "supported-ktx", Role: "unrelated-one-shot-hook", Outcome: "database-unavailable", ExitCode: 4, FailureCode: "database-unavailable"},
				{Class: "engineering-harness", Role: "exact-reconciliation", Outcome: "matched"},
			},
			Reconciliation: reconciliation,
		},
	}
}

func TestLoadBenchmarkDocumentation(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "real-process-load-benchmarks.md"))
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(string(contents), "\n", " ")
	for _, required := range []string{
		"mage loadBench",
		"schema_version",
		"one warm-up round",
		"five measured rounds",
		"1, 2, 4, 8, 16, and 32",
		"KTX_DB",
		"foreign-sqlite",
		"path-alias",
		"ownership-recovery",
		"foreign-writer-reader-exhaustion",
		"stale-compatible",
		"ktx bench",
		"durable engineering verification",
		"started_at",
		"blocked-reader",
		"decompressProcessBench",
		"backpressured 2 MiB `ktx decompress`",
		"artifact, disposition, and session reconciliation",
		"hook latency",
		"matched `writers-only` and `mixed-agent` workloads",
		"Exactly 32 hooks",
		"duplicate agent or session identities",
		"actual hook p50, p95, p99",
		"percentage",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("documentation is missing %q", required)
		}
	}
}

func mandatoryLoadReport() report {
	measured := report{
		SchemaVersion: reportSchemaVersion, StartedAt: "2026-08-10T00:00:00.000000001Z", FinishedAt: "2026-08-10T00:00:00.000000002Z", DurationNanoseconds: 1,
		WarmupRounds: warmupRounds, MeasuredRounds: measuredRounds,
	}
	for _, concurrency := range concurrencyLevels {
		measurement := measurement{Surface: "one-shot-hook", Concurrency: concurrency, Warmup: mandatoryMeasurementRound()}
		for range measuredRounds {
			measurement.Rounds = append(measurement.Rounds, mandatoryMeasurementRound())
		}
		measured.Measurements = append(measured.Measurements, measurement)
	}
	stream := measurement{Surface: "sustained-wrapper-stream", StreamEvents: streamEvents, Warmup: mandatoryRound()}
	for range measuredRounds {
		stream.Rounds = append(stream.Rounds, mandatoryRound())
	}
	measured.Measurements = append(measured.Measurements, stream)
	for _, readers := range []int{0, agentReaderStreams} {
		name := "writers-only"
		if readers != 0 {
			name = "mixed-agent"
		}
		load := agentLoad{
			Name: name, WriterStreams: agentWriterStreams, EventsPerWriterStream: streamEvents,
			ReaderStreams: readers, QueriesPerReaderStream: readers * streamEvents,
			SupportedFailureCodes: []string{}, Warmup: mandatoryAgentLoadRound(readers),
			MeasuredWriterMetrics: metrics{FailureCodes: map[string]int{}},
		}
		if readers != 0 {
			load.MeasuredReaderMetrics = &metrics{FailureCodes: map[string]int{}}
		}
		for range measuredRounds {
			load.Rounds = append(load.Rounds, mandatoryAgentLoadRound(readers))
		}
		measured.AgentLoads = append(measured.AgentLoads, load)
	}
	measured.ContentionPenalties = compareAgentLoads(measured.AgentLoads[0].MeasuredWriterMetrics, measured.AgentLoads[1].MeasuredWriterMetrics)

	for _, name := range []string{
		"supported-writer-path-alias", "foreign-held-reader", "foreign-held-writer", "supported-compatible-migration-race",
	} {
		measured.Scenarios = append(measured.Scenarios, mandatoryScenario(name))
	}
	foreign := mandatoryScenario("foreign-writer-reader-exhaustion")
	foreign.Metrics.FailureCodes = map[string]int{"database-unavailable": 1}
	foreign.Outcome = "completed-with-classified-failures"
	foreign.Participants = []participant{{Class: "supported-ktx", Role: "read-only-db-report", Outcome: "database-unavailable", ExitCode: 4, FailureCode: "database-unavailable"}}
	measured.Scenarios = append(measured.Scenarios, foreign)

	duplicate := mandatoryScenario("supported-duplicate-delivery")
	duplicate.Metrics.Retries = 1
	measured.Scenarios = append(measured.Scenarios, duplicate)

	termination := mandatoryScenario("supported-process-termination-ownership-recovery")
	termination.Metrics.FailureCodes = map[string]int{"terminated": 1}
	termination.Outcome = "completed-with-classified-failures"
	termination.Participants = []participant{
		{Class: "supported-ktx", Role: "terminated-one-shot-hook", Outcome: "terminated", FailureCode: "terminated"},
		{Class: "supported-ktx", Role: "recovery-one-shot-hook", Outcome: "ok"},
	}
	measured.Scenarios = append(measured.Scenarios, termination)
	measured.BlockedReaderTarget = blockedReaderTarget{
		SchemaVersion: blockedReaderTargetSchema, Workload: fixedFixtures()[2].payloadIdentity,
		ReaderOutputStarted: true, ReaderBackpressured: true, HookOutcome: "database-unavailable", HookExitCode: 4,
		HookLatencyNanoseconds: 1, HookFailureCode: "database-unavailable", Reconciliation: reconciliation{Matches: true},
	}
	return measured
}

func mandatoryAgentLoadRound(readers int) round {
	measured := round{
		FreshOwnedDatabase: true, TimeoutNanoseconds: int64(processTimeout),
		WriterAttempts: agentWriterStreams * streamEvents, WriterCompletions: agentWriterStreams * streamEvents,
		ReaderAttempts: readers * streamEvents, ReaderCompletions: readers * streamEvents,
		Metrics:        metrics{FailureCodes: map[string]int{}},
		Reconciliation: exactReconciliation(agentWriterStreams*streamEvents, agentWriterStreams*streamEvents), Outcome: "completed",
		Participants: []participant{
			{Class: "engineering-harness", Role: "empty-database-initializer", Outcome: "ok"},
			{Class: "supported-ktx", Role: "reconciliation-query", Outcome: "ok"},
		},
	}
	for index := 1; index <= agentWriterStreams; index++ {
		measured.Participants = append(measured.Participants, participant{
			Class: "supported-ktx", Role: "writer-stream", AgentID: fmt.Sprintf("writer-%d", index), SessionID: fmt.Sprintf("session-writer-%d", index),
			Outcome: "completed", Attempts: streamEvents, Completions: streamEvents,
		})
	}
	if readers != 0 {
		measured.Participants = append(measured.Participants, participant{
			Class: "supported-ktx", Role: "reader-stream", AgentID: "reader-1", SessionID: "session-reader-1",
			Outcome: "completed", Attempts: streamEvents, Completions: streamEvents,
		})
		measured.ReaderMetrics = &metrics{FailureCodes: map[string]int{}}
	}
	return measured
}

func mandatoryMeasurementRound() round {
	measured := mandatoryRound()
	measured.Participants = []participant{
		{Class: "supported-ktx", Role: "one-shot-hook-writers", Outcome: "completed"},
		{Class: "supported-ktx", Role: "read-only-query", Outcome: "ok"},
	}
	return measured
}

func mandatoryRound() round {
	return round{
		FreshOwnedDatabase: true, WriterAttempts: 1, WriterCompletions: 1,
		Participants:   []participant{{Class: "supported-ktx", Role: "writer", Outcome: "completed"}},
		Metrics:        metrics{FailureCodes: map[string]int{}},
		Reconciliation: reconciliation{Matches: true}, Outcome: "completed",
	}
}

func mandatoryScenario(name string) scenario {
	return scenario{
		Name: name, FreshOwnedDatabase: true,
		Participants:   []participant{{Class: "supported-ktx", Role: "writer", Outcome: "ok"}},
		Metrics:        metrics{FailureCodes: map[string]int{}},
		Reconciliation: reconciliation{Matches: true}, Outcome: "completed",
	}
}

func environmentMap(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		name, value, found := strings.Cut(value, "=")
		if found {
			result[name] = value
		}
	}
	return result
}
