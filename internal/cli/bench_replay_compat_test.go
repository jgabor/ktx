package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestDatabaseReplayLegacyEnvelopesAndExits(t *testing.T) {
	tests := []struct {
		name        string
		mutate      string
		consistency string
		exitCode    int
	}{
		{name: "pass", consistency: "PASS", exitCode: ExitOK},
		{name: "drift", mutate: `UPDATE bench_runs SET tokens_in = tokens_in + 1`, consistency: "DRIFT", exitCode: ExitOK},
		{name: "fail", mutate: `UPDATE recorded_streams SET content_hash = 'changed'`, consistency: "FAIL", exitCode: ExitInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "ktx.db")
			sessionID := seedFixtureExportDB(t, dbPath, true)
			d, err := dbpkg.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if tt.mutate != "" {
				if _, err := d.DB().Exec(tt.mutate); err != nil {
					t.Fatal(err)
				}
			}

			var stdout, stderr bytes.Buffer
			code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON)
			if code != tt.exitCode {
				t.Fatalf("exit code = %d, want %d; stderr=%q", code, tt.exitCode, stderr.String())
			}
			var envelope map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("decode replay envelope: %v", err)
			}
			if got := envelope["consistency"]; got != tt.consistency {
				t.Fatalf("consistency = %v, want %s", got, tt.consistency)
			}
			assertJSONKeys(t, envelope, "compression", "consistency", "cost_drift", "integrity", "original_bench_run", "parser_regression", "replay")
			assertJSONKeys(t, envelope["replay"].(map[string]any), "bench_run_id", "capture_source", "captured_at", "session_id", "stream_format")
			assertJSONKeys(t, envelope["integrity"].(map[string]any), "pass", "recomputed_hash", "stored_hash")
			assertJSONKeys(t, envelope["parser_regression"].(map[string]any), "original_tool_call_count", "pass", "replayed_tool_use_count")
			assertJSONKeys(t, envelope["compression"].(map[string]any), "baseline_bytes", "bytes_saved", "compressed_bytes", "pipeline", "ratio")
			assertJSONKeys(t, envelope["cost_drift"].(map[string]any), "delta", "detected", "original_cache_read", "original_cache_write", "original_cost", "original_tokens_in", "original_tokens_out", "replay_cost", "replay_tokens_in", "replay_tokens_out", "replayed_cache_read", "replayed_cache_write")
			assertJSONKeys(t, envelope["original_bench_run"].(map[string]any), "agent", "cache_read", "cache_write", "cost", "estimated_api_cost", "id", "methodology_version", "model", "reasoning", "score", "task", "timestamp", "tokens_in", "tokens_out")

			stdout.Reset()
			stderr.Reset()
			code = runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatText)
			if code != tt.exitCode {
				t.Fatalf("text exit code = %d, want %d; stderr=%q", code, tt.exitCode, stderr.String())
			}
			wantPrefix := fmt.Sprintf("replay: %s (integrity=", tt.consistency)
			if !strings.HasPrefix(stdout.String(), wantPrefix) || !strings.Contains(stdout.String(), "session_id: "+sessionID+"\n") || !strings.Contains(stdout.String(), "compression: baseline=") {
				t.Fatalf("text replay envelope = %q", stdout.String())
			}
		})
	}
}

func TestDatabaseReplayParentlessComparisonRemainsUnavailable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ktx.db")
	sessionID := seedFixtureExportDB(t, dbPath, false)
	d, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var stdout, stderr bytes.Buffer
	if code := runBenchReplayImpl(d, sessionID, &stdout, &stderr, FormatJSON); code != ExitOK {
		t.Fatalf("exit code = %d; stderr=%q", code, stderr.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	replay := envelope["replay"].(map[string]any)
	costDrift := envelope["cost_drift"].(map[string]any)
	_, hasFixtureVersion := replay["fixture_version"]
	if replay["capture_source"] != "ktx_run" || hasFixtureVersion || envelope["original_bench_run"] != nil ||
		costDrift["comparison"] != "unavailable" || costDrift["detected"] != false || envelope["consistency"] != "PASS" {
		t.Fatalf("parentless replay presented with the wrong semantics: %s", stdout.String())
	}
	assertJSONKeys(t, costDrift, "comparison", "detected", "reason", "replay_cost", "replay_tokens_in", "replay_tokens_out", "replayed_cache_read", "replayed_cache_write")
}

func TestDatabaseAndExportedFixtureReplayEvidenceParity(t *testing.T) {
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi", "dedup-blank", "normalize"]
`)
	dbPath := filepath.Join(t.TempDir(), "ktx.db")
	sessionID := seedFixtureExportDB(t, dbPath, true)
	t.Setenv("KTX_DB", dbPath)

	d, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var databaseJSON, databaseErr bytes.Buffer
	if code := runBenchReplayImpl(d, sessionID, &databaseJSON, &databaseErr, FormatJSON); code != ExitOK {
		t.Fatalf("database replay exit=%d stderr=%q", code, databaseErr.String())
	}
	var databaseEvidence map[string]any
	if err := json.Unmarshal(databaseJSON.Bytes(), &databaseEvidence); err != nil {
		t.Fatal(err)
	}

	var fixtureJSON, exportErr bytes.Buffer
	if code := runBenchExport([]string{sessionID}, Options{Format: FormatJSON}, &fixtureJSON, &exportErr); code != ExitOK {
		t.Fatalf("fixture export exit=%d stderr=%q", code, exportErr.String())
	}
	fixture, err := decodeReplayFixtureForEvaluation(bytes.NewReader(fixtureJSON.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	fixtureEvidence := normalizedJSONMap(t, evaluateReplayFixture(fixture))

	for _, section := range []string{"integrity", "parser_regression"} {
		if !reflect.DeepEqual(databaseEvidence[section], fixtureEvidence[section]) {
			t.Errorf("%s differs:\ndatabase=%v\nfixture=%v", section, databaseEvidence[section], fixtureEvidence[section])
		}
	}
	assertSharedValues(t, databaseEvidence["cost_drift"].(map[string]any), fixtureEvidence["cost_drift"].(map[string]any),
		"detected", "replay_tokens_in", "original_tokens_in", "replay_tokens_out", "original_tokens_out", "replay_cost", "original_cost", "replayed_cache_read", "original_cache_read", "replayed_cache_write", "original_cache_write")
	assertSharedValues(t, databaseEvidence["compression"].(map[string]any), fixtureEvidence["compression"].(map[string]any),
		"pipeline", "baseline_bytes", "compressed_bytes", "bytes_saved", "ratio")
}

func TestReplayMissingInputsRemainSelectorSpecific(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KTX_DB", filepath.Join(dir, "ktx.db"))
	missingFixture := filepath.Join(dir, "missing.json")

	var stdout, stderr bytes.Buffer
	code := runBenchRun([]string{"--replay", "missing-session"}, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitNotFound || !strings.Contains(stderr.String(), "no recorded stream for session") || strings.Contains(stderr.String(), "invalid-replay-fixture") {
		t.Fatalf("missing session: exit=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = runBenchRun([]string{"--replay-file", missingFixture}, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), `"failure_class":"invalid-replay-fixture"`) || strings.Contains(stderr.String(), "no recorded stream for session") {
		t.Fatalf("missing fixture: exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(missingFixture); !os.IsNotExist(err) {
		t.Fatalf("missing fixture was unexpectedly created: %v", err)
	}
}

func assertJSONKeys(t *testing.T, value map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON keys = %v, want %v", got, want)
	}
}

func normalizedJSONMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(data, &normalized); err != nil {
		t.Fatal(err)
	}
	return normalized
}

func assertSharedValues(t *testing.T, database, fixture map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if !reflect.DeepEqual(database[key], fixture[key]) {
			t.Errorf("%s differs: database=%v fixture=%v", key, database[key], fixture[key])
		}
	}
}
