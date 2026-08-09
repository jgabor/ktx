package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	if len(first) != 2 || len(second) != len(first) {
		t.Fatalf("fixture counts = %d and %d, want 2", len(first), len(second))
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
		"ktx bench",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("documentation is missing %q", required)
		}
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
