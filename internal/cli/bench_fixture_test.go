package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func validReplayFixtureJSON(t *testing.T, usage map[string]any) []byte {
	t.Helper()
	stream := "{\"type\":\"step_start\",\"part\":{}}\n"
	fixture := map[string]any{
		"version": 1,
		"benchmark": map[string]any{
			"methodology_version": "v1", "agent": "opencode", "model": "model", "task": "task", "score": 1,
		},
		"stream": map[string]any{
			"format": "opencode-jsonl-1", "content": stream, "sha256": dbpkg.ContentHash(stream),
		},
		"expected": map[string]any{
			"parser": map[string]any{"tool_use_count": 0},
			"usage":  usage,
			"compression": map[string]any{
				"pipeline": []string{"strip-ansi"}, "baseline_bytes": 0, "compressed_bytes": 0, "bytes_saved": 0, "ratio": 1,
			},
		},
	}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func completeFixtureUsage() map[string]any {
	return map[string]any{
		"tokens_in": nil, "tokens_out": int64(0), "cost": nil, "cache_read": int64(0), "cache_write": nil,
	}
}

func TestDecodeReplayFixtureV1PreservesNullAndZero(t *testing.T) {
	fixture, err := decodeReplayFixture(bytes.NewReader(validReplayFixtureJSON(t, completeFixtureUsage())))
	if err != nil {
		t.Fatalf("decodeReplayFixture: %v", err)
	}
	u := fixture.Expected.Usage
	if !u.TokensIn.Present || u.TokensIn.Value != nil {
		t.Fatalf("tokens_in = %#v, want present null", u.TokensIn)
	}
	if !u.TokensOut.Present || u.TokensOut.Value == nil || *u.TokensOut.Value != 0 {
		t.Fatalf("tokens_out = %#v, want explicit zero", u.TokensOut)
	}
	if !u.CacheRead.Present || u.CacheRead.Value == nil || *u.CacheRead.Value != 0 {
		t.Fatalf("cache_read = %#v, want explicit zero", u.CacheRead)
	}
	if !u.CacheWrite.Present || u.CacheWrite.Value != nil {
		t.Fatalf("cache_write = %#v, want present null", u.CacheWrite)
	}
	roundTrip, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if !bytes.Contains(roundTrip, []byte(`"tokens_in":null`)) || !bytes.Contains(roundTrip, []byte(`"tokens_out":0`)) {
		t.Fatalf("nullable round trip lost null/zero distinction: %s", roundTrip)
	}
}

func TestDecodeReplayFixtureRejectsVersionAndRequiredFields(t *testing.T) {
	base := validReplayFixtureJSON(t, completeFixtureUsage())
	tests := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"missing version", func(f map[string]any) { delete(f, "version") }, "missing required field version"},
		{"unsupported version", func(f map[string]any) { f["version"] = 2 }, "unsupported version 2"},
		{"missing benchmark", func(f map[string]any) { delete(f, "benchmark") }, "missing required benchmark parent"},
		{"missing nullable evidence", func(f map[string]any) {
			u := f["expected"].(map[string]any)["usage"].(map[string]any)
			delete(u, "cache_write")
		}, "expected.usage requires"},
		{"missing compression identity", func(f map[string]any) {
			c := f["expected"].(map[string]any)["compression"].(map[string]any)
			delete(c, "pipeline")
		}, "expected.compression requires"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var candidate map[string]any
			if err := json.Unmarshal(base, &candidate); err != nil {
				t.Fatal(err)
			}
			tt.edit(candidate)
			b, _ := json.Marshal(candidate)
			_, err := decodeReplayFixture(bytes.NewReader(b))
			var ktxErr *Error
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			ktxErr = toKtxError(err)
			if ktxErr.Code != CodeUsage || ktxErr.Extra["failure_class"] != "invalid-replay-fixture" || ktxErr.Extra["correction"] == "" {
				t.Fatalf("structured error = %#v", ktxErr)
			}
		})
	}
}

func TestDecodeReplayFixtureDetectsIntegrityMismatch(t *testing.T) {
	b := validReplayFixtureJSON(t, completeFixtureUsage())
	b = bytes.Replace(b, []byte("step_start"), []byte("step_stop"), 1)
	_, err := decodeReplayFixture(bytes.NewReader(b))
	if err == nil || !strings.Contains(err.Error(), "stream integrity mismatch") {
		t.Fatalf("error = %v, want integrity mismatch", err)
	}
}

func TestDecodeReplayFixtureAcceptsReviewedContentAndHashChange(t *testing.T) {
	b := validReplayFixtureJSON(t, completeFixtureUsage())
	var fixture map[string]any
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	stream := fixture["stream"].(map[string]any)
	stream["content"] = "{\"type\":\"step_stop\",\"part\":{}}\n"
	stream["sha256"] = dbpkg.ContentHash(stream["content"].(string))
	b, _ = json.Marshal(fixture)
	if _, err := decodeReplayFixture(bytes.NewReader(b)); err != nil {
		t.Fatalf("decodeReplayFixture after synchronized review edit: %v", err)
	}
}

func TestRunBenchRunReplaySelectorsAreUnambiguous(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "session-looking.json")
	if err := os.WriteFile(fixturePath, []byte("not a fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_DB", filepath.Join(dir, "ktx.db"))

	t.Run("session selector never reads matching filename", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchRun([]string{"--replay", fixturePath}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound || strings.Contains(stderr.String(), "invalid-replay-fixture") {
			t.Fatalf("code=%d stderr=%q, want session not-found", code, stderr.String())
		}
	})

	t.Run("file selector validates file", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchRun([]string{"--replay-file", fixturePath}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage || !strings.Contains(stderr.String(), `"failure_class":"invalid-replay-fixture"`) {
			t.Fatalf("code=%d stderr=%q, want fixture validation failure", code, stderr.String())
		}
	})

	t.Run("selectors conflict", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runBenchRun([]string{"--replay", "session", "--replay-file", fixturePath}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage || !strings.Contains(stderr.String(), "mutually exclusive") {
			t.Fatalf("code=%d stderr=%q, want selector conflict", code, stderr.String())
		}
	})
}

func TestRunBenchReplayFileValidV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, evaluableReplayFixtureJSON(t), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runBenchReplayFile(path, &stdout, &stderr, FormatJSON); code != ExitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"consistency":"PASS"`) ||
		!strings.Contains(stdout.String(), `"capture_source":"fixture"`) ||
		!strings.Contains(stdout.String(), `"original_bench_run"`) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func evaluableReplayFixtureJSON(t *testing.T) []byte {
	t.Helper()
	toolEvent, err := json.Marshal(map[string]any{
		"type": "tool_use", "timestamp": "2026-07-17T12:00:30Z",
		"part": map[string]any{"type": "tool", "tool": "bash", "callID": "call1", "state": map[string]any{
			"input": "printf output", "output": "\x1b[31mline\x1b[0m\n\n\nline\n",
			"time": map[string]any{"start": 1, "end": 2}, "metadata": map[string]any{"exit": 0},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := string(toolEvent) + "\n" + `{"type":"step_finish","part":{"tokens":{"input":12,"output":3,"cache":{"read":0}},"cost":0.25,"model":"test-model","provider":"opencode"}}` + "\n"
	metrics := extractStreamMetrics([]byte(stream))
	toolUseCount := int64(len(parseReplayToolCalls(stream)))
	version, score := 1, 1.0
	fixture := replayFixture{
		Version:   &version,
		Benchmark: &replayFixtureBenchmark{MethodologyVersion: "v1", Agent: "opencode", Model: "test-model", Task: "portable_fixture", Score: &score},
		Stream:    &replayFixtureStream{Format: "opencode-jsonl-1", Content: &stream, SHA256: dbpkg.ContentHash(stream)},
		Expected: &replayFixtureExpectations{
			Parser: &replayFixtureParser{ToolUseCount: &toolUseCount},
			Usage: &replayFixtureUsage{
				TokensIn: optionalNullable[int64]{Present: true, Value: cloneInt64(metrics.tokensIn)}, TokensOut: optionalNullable[int64]{Present: true, Value: cloneInt64(metrics.tokensOut)},
				Cost: optionalNullable[float64]{Present: true, Value: cloneFloat64(metrics.cost)}, CacheRead: optionalNullable[int64]{Present: true, Value: cloneInt64(metrics.cacheRead)},
				CacheWrite: optionalNullable[int64]{Present: true, Value: cloneInt64(metrics.cacheWrite)},
			},
			Compression: measureReplayFixtureCompression(parseReplayToolCalls(stream)),
		},
	}
	b, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunBenchReplayFileIsDeterministicOfflineAndIgnoresConfiguration(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixturePath, evaluableReplayFixtureJSON(t), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_DB", filepath.Join(dir, "not-a-database"))
	if err := os.Mkdir(os.Getenv("KTX_DB"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	if err := os.MkdirAll(filepath.Join(dir, "config", "ktx"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "ktx", "ktx.toml"), []byte("malformed = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "plugin-ran")
	config := "[pipeline.default]\nstages = [\"strip-ansi\", \"hostile\"]\n[[plugin]]\nname = \"hostile\"\ncommand = \"/bin/sh\"\nargs = [\"-c\", \"touch " + marker + "\"]\n"
	if err := os.WriteFile(filepath.Join(work, "ktx.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	var first, second, firstErr, secondErr bytes.Buffer
	if code := runBenchReplayFile(fixturePath, &first, &firstErr, FormatJSON); code != ExitOK {
		t.Fatalf("first code=%d stderr=%q", code, firstErr.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "ktx", "ktx.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runBenchReplayFile(fixturePath, &second, &secondErr, FormatJSON); code != ExitOK {
		t.Fatalf("second code=%d stderr=%q", code, secondErr.String())
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("replay evidence is nondeterministic:\nfirst=%s\nsecond=%s", first.Bytes(), second.Bytes())
	}
	if firstErr.Len() != 0 || secondErr.Len() != 0 {
		t.Fatalf("unexpected diagnostics: first=%q second=%q", firstErr.String(), secondErr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("external plugin affected replay: stat error=%v", err)
	}
}

func TestRunBenchReplayFileStrictVerificationEmitsEveryDivergence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		field  string
	}{
		{"integrity", func(f map[string]any) { f["stream"].(map[string]any)["content"] = "{}\n" }, `"integrity":{"pass":false`},
		{"parser", func(f map[string]any) {
			f["expected"].(map[string]any)["parser"].(map[string]any)["tool_use_count"] = 2
		}, `"parser_regression":{"original_tool_call_count":2,"pass":false`},
		{"tokens in", func(f map[string]any) { f["expected"].(map[string]any)["usage"].(map[string]any)["tokens_in"] = 13 }, `"original_tokens_in":13`},
		{"tokens out", func(f map[string]any) { f["expected"].(map[string]any)["usage"].(map[string]any)["tokens_out"] = 4 }, `"original_tokens_out":4`},
		{"cost", func(f map[string]any) { f["expected"].(map[string]any)["usage"].(map[string]any)["cost"] = 0.5 }, `"original_cost":0.5`},
		{"cache read", func(f map[string]any) { f["expected"].(map[string]any)["usage"].(map[string]any)["cache_read"] = nil }, `"original_cache_read":null`},
		{"cache write", func(f map[string]any) { f["expected"].(map[string]any)["usage"].(map[string]any)["cache_write"] = 0 }, `"original_cache_write":0`},
		{"compression pipeline", func(f map[string]any) {
			f["expected"].(map[string]any)["compression"].(map[string]any)["pipeline"] = []any{"hostile-plugin"}
		}, `"pass":false`},
		{"compression measurements", func(f map[string]any) {
			c := f["expected"].(map[string]any)["compression"].(map[string]any)
			baseline := int64(c["baseline_bytes"].(float64)) + 1
			compressed := int64(c["compressed_bytes"].(float64))
			c["baseline_bytes"], c["bytes_saved"], c["ratio"] = baseline, baseline-compressed, float64(compressed)/float64(baseline)
		}, `"pass":false`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fixture map[string]any
			if err := json.Unmarshal(evaluableReplayFixtureJSON(t), &fixture); err != nil {
				t.Fatal(err)
			}
			tt.mutate(fixture)
			data, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "fixture.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := runBenchReplayFile(path, &stdout, &stderr, FormatJSON)
			if code == ExitOK || !strings.Contains(stdout.String(), `"consistency":"FAIL"`) || !strings.Contains(stdout.String(), tt.field) ||
				!strings.Contains(stderr.String(), `"failure_class":"replay-fixture-verification-failed"`) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRetainedReplayFixtureGate(t *testing.T) {
	fixturePath := filepath.Join("testdata", "portable-replay-v1.json")
	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}

	runFixture := func(t *testing.T, path string) (int, []byte, []byte) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := runWith([]string{"bench", "run", "--replay-file", path}, &stdout, &stderr)
		return code, stdout.Bytes(), stderr.Bytes()
	}

	firstCode, firstEvidence, firstDiagnostics := runFixture(t, fixturePath)
	secondCode, secondEvidence, secondDiagnostics := runFixture(t, fixturePath)
	if firstCode != ExitOK || secondCode != ExitOK {
		t.Fatalf("retained fixture failed: first code=%d stderr=%s; second code=%d stderr=%s", firstCode, firstDiagnostics, secondCode, secondDiagnostics)
	}
	if !bytes.Equal(firstEvidence, secondEvidence) || !bytes.Equal(firstDiagnostics, secondDiagnostics) {
		t.Fatalf("repeated fixture evidence differs:\nfirst=%s\nsecond=%s\nfirst stderr=%s\nsecond stderr=%s", firstEvidence, secondEvidence, firstDiagnostics, secondDiagnostics)
	}
	var baseline map[string]any
	if err := json.Unmarshal(firstEvidence, &baseline); err != nil {
		t.Fatalf("decode retained fixture evidence: %v", err)
	}
	if baseline["consistency"] != "PASS" {
		t.Fatalf("retained fixture consistency = %v, want PASS", baseline["consistency"])
	}

	type divergence struct {
		name    string
		section string
		field   string
		want    bool
		mutate  func(map[string]any)
	}
	divergences := []divergence{
		{name: "integrity", section: "integrity", field: "pass", want: false, mutate: func(f map[string]any) {
			f["stream"].(map[string]any)["content"] = f["stream"].(map[string]any)["content"].(string) + "{}\n"
		}},
		{name: "parser", section: "parser_regression", field: "pass", want: false, mutate: func(f map[string]any) {
			f["expected"].(map[string]any)["parser"].(map[string]any)["tool_use_count"] = float64(3)
		}},
		{name: "usage", section: "cost_drift", field: "detected", want: true, mutate: func(f map[string]any) {
			f["expected"].(map[string]any)["usage"].(map[string]any)["tokens_in"] = float64(161)
		}},
		{name: "cost", section: "cost_drift", field: "detected", want: true, mutate: func(f map[string]any) {
			f["expected"].(map[string]any)["usage"].(map[string]any)["cost"] = 0.003
		}},
		{name: "cache absent versus zero", section: "cost_drift", field: "detected", want: true, mutate: func(f map[string]any) {
			f["expected"].(map[string]any)["usage"].(map[string]any)["cache_read"] = nil
		}},
		{name: "compression pipeline", section: "compression", field: "pass", want: false, mutate: func(f map[string]any) {
			f["expected"].(map[string]any)["compression"].(map[string]any)["pipeline"] = []any{"normalize"}
		}},
		{name: "compression measurement", section: "compression", field: "pass", want: false, mutate: func(f map[string]any) {
			compression := f["expected"].(map[string]any)["compression"].(map[string]any)
			compression["baseline_bytes"] = float64(71)
			compression["bytes_saved"] = float64(15)
			compression["ratio"] = float64(56) / float64(71)
		}},
	}

	for _, tt := range divergences {
		t.Run(tt.name, func(t *testing.T) {
			var candidate map[string]any
			if err := json.Unmarshal(fixtureBytes, &candidate); err != nil {
				t.Fatal(err)
			}
			tt.mutate(candidate)
			data, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "portable-replay-v1.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			code, evidence, diagnostics := runFixture(t, path)
			if code == ExitOK {
				t.Fatalf("strict fixture verification accepted %s divergence: %s", tt.name, evidence)
			}
			var envelope map[string]any
			if err := json.Unmarshal(evidence, &envelope); err != nil {
				t.Fatalf("decode %s evidence: %v; stdout=%s stderr=%s", tt.name, err, evidence, diagnostics)
			}
			section, ok := envelope[tt.section].(map[string]any)
			if envelope["consistency"] != "FAIL" || !ok || section[tt.field] != tt.want {
				t.Fatalf("%s divergence evidence did not identify strict failure: stdout=%s stderr=%s", tt.name, evidence, diagnostics)
			}
			if !bytes.Contains(diagnostics, []byte(`"failure_class":"replay-fixture-verification-failed"`)) {
				t.Fatalf("%s divergence diagnostics lack failure class: %s", tt.name, diagnostics)
			}
		})
	}

	t.Run("informational session drift is strict fixture failure", func(t *testing.T) {
		fixture, err := decodeReplayFixture(bytes.NewReader(fixtureBytes))
		if err != nil {
			t.Fatal(err)
		}
		database, err := dbpkg.Open(filepath.Join(t.TempDir(), "replay.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		sessionID, benchID := mustNewIDBench(t), mustNewIDBench(t)
		if err := database.InsertSession(&dbpkg.Session{
			ID: sessionID, Agent: fixture.Benchmark.Agent, StartedAt: "2026-07-17T12:00:00Z",
			LastSeenAt: "2026-07-17T12:01:00Z", ToolCallCount: *fixture.Expected.Parser.ToolUseCount,
		}); err != nil {
			t.Fatal(err)
		}
		if err := database.InsertBenchRun(&dbpkg.BenchRun{
			ID: benchID, Agent: fixture.Benchmark.Agent, Model: fixture.Benchmark.Model, Task: fixture.Benchmark.Task,
			Score: *fixture.Benchmark.Score, TokensIn: *fixture.Expected.Usage.TokensIn.Value,
			TokensOut: *fixture.Expected.Usage.TokensOut.Value, Cost: cloneFloat64(fixture.Expected.Usage.Cost.Value),
			MethodologyVersion: fixture.Benchmark.MethodologyVersion, Timestamp: 1784289600,
			CacheRead: cloneInt64(fixture.Expected.Usage.CacheRead.Value), CacheWrite: cloneInt64(fixture.Expected.Usage.CacheWrite.Value),
		}); err != nil {
			t.Fatal(err)
		}
		if err := database.InsertRecordedStream(&dbpkg.RecordedStream{
			ID: mustNewIDBench(t), SessionID: sessionID, BenchRunID: benchID, StreamText: *fixture.Stream.Content,
			ContentHash: fixture.Stream.SHA256, StreamFormat: fixture.Stream.Format, CapturedAt: "2026-07-17T12:01:00Z",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := database.DB().Exec(`UPDATE bench_runs SET cost = 0.003 WHERE id = ?`, benchID); err != nil {
			t.Fatal(err)
		}
		var sessionEvidence, sessionDiagnostics bytes.Buffer
		if code := runBenchReplayImpl(database, sessionID, &sessionEvidence, &sessionDiagnostics, FormatJSON); code != ExitOK ||
			!bytes.Contains(sessionEvidence.Bytes(), []byte(`"consistency":"DRIFT"`)) {
			t.Fatalf("database replay did not preserve informational DRIFT success: code=%d stdout=%s stderr=%s", code, sessionEvidence.Bytes(), sessionDiagnostics.Bytes())
		}

		var candidate map[string]any
		if err := json.Unmarshal(fixtureBytes, &candidate); err != nil {
			t.Fatal(err)
		}
		candidate["expected"].(map[string]any)["usage"].(map[string]any)["cost"] = 0.003
		data, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "informational-drift.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		code, evidence, _ := runFixture(t, path)
		if code != ExitInternal || !bytes.Contains(evidence, []byte(`"consistency":"FAIL"`)) {
			t.Fatalf("portable gate accepted drift that database replay treats informationally: code=%d evidence=%s", code, evidence)
		}
	})
}

func seedFixtureExportDB(t *testing.T, path string, parented bool) string {
	t.Helper()
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("open fixture export database: %v", err)
	}
	sessionID := mustNewIDBench(t)
	if err := d.InsertSession(&dbpkg.Session{
		ID: sessionID, Agent: "opencode", StartedAt: "2026-07-17T12:00:00Z",
		LastSeenAt: "2026-07-17T12:01:00Z", ToolCallCount: 1,
	}); err != nil {
		d.Close()
		t.Fatalf("insert session: %v", err)
	}

	benchID := ""
	if parented {
		benchID = mustNewIDBench(t)
		cost, cacheRead := 0.25, int64(0)
		if err := d.InsertBenchRun(&dbpkg.BenchRun{
			ID: benchID, Agent: "opencode", Model: "test-model", Task: "portable_fixture",
			Score: 1, TokensIn: 12, TokensOut: 3, Cost: &cost,
			MethodologyVersion: "v1", Timestamp: 1784289600, CacheRead: &cacheRead,
		}); err != nil {
			d.Close()
			t.Fatalf("insert bench run: %v", err)
		}
	}

	toolEvent, err := json.Marshal(map[string]any{
		"type": "tool_use",
		"part": map[string]any{
			"type": "tool", "tool": "bash", "callID": "call1",
			"state": map[string]any{
				"input": "printf output", "output": "\x1b[31mline\x1b[0m\n\n\nline\n",
				"time": map[string]any{"start": 1, "end": 2}, "metadata": map[string]any{"exit": 0},
			},
		},
		"timestamp": "2026-07-17T12:00:30Z",
	})
	if err != nil {
		d.Close()
		t.Fatalf("marshal tool event: %v", err)
	}
	finishEvent := `{"type":"step_finish","part":{"tokens":{"input":12,"output":3,"cache":{"read":0}},"cost":0.25,"model":"test-model","provider":"opencode"}}`
	stream := string(toolEvent) + "\n" + finishEvent + "\n"
	if err := d.InsertRecordedStream(&dbpkg.RecordedStream{
		ID: mustNewIDBench(t), SessionID: sessionID, BenchRunID: benchID,
		StreamText: stream, ContentHash: dbpkg.ContentHash(stream),
		StreamFormat: "opencode-jsonl-1", CapturedAt: "2026-07-17T12:01:00Z",
	}); err != nil {
		d.Close()
		t.Fatalf("insert recorded stream: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close fixture export database: %v", err)
	}
	return sessionID
}

func TestRunBenchExportCanonicalStdoutAndFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ktx.db")
	sessionID := seedFixtureExportDB(t, dbPath, true)
	t.Setenv("KTX_DB", dbPath)

	var first, second, stderr bytes.Buffer
	if code := runWith([]string{"bench", "export", sessionID}, &first, &stderr); code != ExitOK {
		t.Fatalf("first export code=%d stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stdout export diagnostics = %q, want none", stderr.String())
	}
	configHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configHome, "ktx"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "ktx", "ktx.toml"), []byte("malformed = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if code := runWith([]string{"bench", "export", sessionID}, &second, &stderr); code != ExitOK {
		t.Fatalf("second export code=%d stderr=%q", code, stderr.String())
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("repeated exports differ:\nfirst=%s\nsecond=%s", first.Bytes(), second.Bytes())
	}
	for _, forbidden := range [][]byte{[]byte(`"captured_at"`), []byte(`"timestamp"`), []byte(`"session_id"`), []byte(`"bench_run_id"`)} {
		if bytes.Contains(first.Bytes(), forbidden) {
			t.Fatalf("canonical fixture contains invocation or machine-local metadata %s: %s", forbidden, first.Bytes())
		}
	}
	fixture, err := decodeReplayFixture(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatalf("decode exported fixture: %v\n%s", err, first.Bytes())
	}
	if *fixture.Expected.Parser.ToolUseCount != 1 || fixture.Expected.Usage.CacheRead.Value == nil || *fixture.Expected.Usage.CacheRead.Value != 0 || fixture.Expected.Usage.CacheWrite.Value != nil {
		t.Fatalf("exported parser/cache semantics = %#v %#v", fixture.Expected.Parser, fixture.Expected.Usage)
	}
	if got := fixture.Expected.Compression.Pipeline; len(got) != 3 || got[0] != "strip-ansi" || got[1] != "dedup-blank" || got[2] != "normalize" {
		t.Fatalf("controlled pipeline = %q", got)
	}

	destination := filepath.Join(dir, "portable.json")
	var fileStdout, fileStderr bytes.Buffer
	if code := runWith([]string{"bench", "export", "--output", destination, sessionID}, &fileStdout, &fileStderr); code != ExitOK {
		t.Fatalf("file export code=%d stderr=%q", code, fileStderr.String())
	}
	if fileStdout.Len() != 0 {
		t.Fatalf("file export stdout=%q, want empty", fileStdout.String())
	}
	if !strings.Contains(fileStderr.String(), destination) || strings.Contains(fileStderr.String(), `"version"`) {
		t.Fatalf("file export diagnostics=%q, want destination only and no result data", fileStderr.String())
	}
	fileBytes, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fileBytes, first.Bytes()) {
		t.Fatalf("file and stdout fixtures differ:\nfile=%s\nstdout=%s", fileBytes, first.Bytes())
	}
}

func TestRunBenchExportPreservesSourceDatabaseBytes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ktx.db")
	sessionID := seedFixtureExportDB(t, dbPath, true)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_DB", dbPath)
	var stdout, stderr bytes.Buffer
	if code := runWith([]string{"bench", "export", sessionID}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("export code=%d stderr=%q", code, stderr.String())
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source database bytes changed during export")
	}
}

func assertFixtureDatabaseUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read source database after rejected export: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source database bytes changed after rejected export")
	}
	database, err := dbpkg.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("reopen source database after rejected export: %v", err)
	}
	defer database.Close()
	var integrity string
	if err := database.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("check source database integrity: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("source database integrity = %q, want ok", integrity)
	}
}

func TestRunBenchExportRejectsSourceDatabaseDestinationAliases(t *testing.T) {
	tests := []struct {
		name        string
		destination func(*testing.T, string) string
	}{
		{"direct path", func(_ *testing.T, path string) string { return path }},
		{"relative equivalent", func(_ *testing.T, path string) string {
			return filepath.Dir(path) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(path)
		}},
		{"symlink alias", func(t *testing.T, path string) string {
			alias := filepath.Join(filepath.Dir(path), "source-link.db")
			if err := os.Symlink(path, alias); err != nil {
				t.Fatalf("create source database symlink: %v", err)
			}
			return alias
		}},
		{"hard-link alias", func(t *testing.T, path string) string {
			alias := filepath.Join(filepath.Dir(path), "source-hardlink.db")
			if err := os.Link(path, alias); err != nil {
				t.Fatalf("create source database hard link: %v", err)
			}
			return alias
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "ktx.db")
			sessionID := seedFixtureExportDB(t, dbPath, true)
			before, err := os.ReadFile(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			destination := tt.destination(t, dbPath)
			t.Setenv("KTX_DB", dbPath)

			var stdout, stderr bytes.Buffer
			code := runWith([]string{"bench", "export", "--output", destination, sessionID}, &stdout, &stderr)
			if code != ExitUsage || stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q, want usage rejection without output", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "source database") || !strings.Contains(stderr.String(), "--output -") {
				t.Fatalf("stderr=%q, want source alias explanation and safe stdout correction", stderr.String())
			}
			assertFixtureDatabaseUnchanged(t, dbPath, before)
		})
	}
}

func TestRunBenchExportRejectsInvalidSourcesBeforePublication(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{"parentless", nil, "benchmark-parented"},
		{"bad hash", func(t *testing.T, path string) {
			d, err := dbpkg.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB().Exec(`UPDATE recorded_streams SET content_hash = 'bad'`); err != nil {
				t.Fatal(err)
			}
			d.Close()
		}, "content hash"},
		{"broken benchmark link", func(t *testing.T, path string) {
			d, err := dbpkg.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			d.DB().SetMaxOpenConns(1)
			if _, err := d.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB().Exec(`UPDATE recorded_streams SET bench_run_id = 'missing-parent'`); err != nil {
				t.Fatal(err)
			}
			d.Close()
		}, "benchmark link"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "ktx.db")
			sessionID := seedFixtureExportDB(t, dbPath, tt.name != "parentless")
			if tt.mutate != nil {
				tt.mutate(t, dbPath)
			}
			t.Setenv("KTX_DB", dbPath)
			destination := filepath.Join(dir, "fixture.json")
			sentinel := []byte("existing destination")
			if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := runWith([]string{"bench", "export", "--output", destination, sessionID}, &stdout, &stderr)
			if code != ExitUsage || stdout.Len() != 0 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q, want rejection containing %q", code, stdout.String(), stderr.String(), tt.want)
			}
			got, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, sentinel) {
				t.Fatalf("destination changed on rejected export: %q", got)
			}
		})
	}
}
