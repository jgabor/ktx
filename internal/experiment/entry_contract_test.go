package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchregistry"
	"github.com/jgabor/ktx/internal/db"
)

// The executor supplies offline runtime evidence; the real producer writes the
// manifest, retained inputs, attempt ledger, owner databases, and summary.
func produceEntryArchive(t *testing.T, spec string, priced, cache bool, executors ...CommandExecutor) string {
	return produceEntryArchiveID(t, "entry-control", spec, priced, cache, executors...)
}

func produceEntryArchiveID(t *testing.T, id, spec string, priced, cache bool, executors ...CommandExecutor) string {
	t.Helper()
	return produceEntryArchiveIDExit(t, id, spec, priced, cache, 0, executors...)
}

func produceEntryArchiveIDExit(t *testing.T, id, spec string, priced, cache bool, expectedExit int, executors ...CommandExecutor) string {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"tools/bench/specs/" + spec + ".yaml", "tools/bench/pipelines/passthrough.toml", "tools/bench/pipelines/conservative-a.toml", "tools/bench/contracts/cache-reuse.json"} {
		if err := copyTreeFile(filepath.Join(repo, name), filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"README.md": "offline fixture\n", "Dockerfile.benchmark": "FROM scratch\n", "auth.json": "{}\n",
		"tools/bench/tasks/crossfile-nav.toml": "id = 'crossfile-nav'\nprompt = 'do work'\nminimum_tool_calls = 1\nworkspace_mode = 'repo'\n[scoring]\nkind = 'exact_match'\nexpected_output = 'done'\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &Runner{
		Executor:     &fakeExecutor{sessions: map[string]string{}, fullStepUsage: cache},
		Now:          func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) },
		PID:          56,
		ResolvePrice: func(string, string) (db.Price, error) { return db.Price{}, nil },
	}
	if priced {
		runner.ResolvePrice = testPriceResolver
	}
	if len(executors) != 0 {
		runner.Executor = executors[0]
	}
	dir, _, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "openai/test", TaskFile: "crossfile-nav", TargetValid: 1, MaxAttempts: 6,
		AttemptTimeout: time.Minute, TerminationGrace: time.Second, ExperimentID: id,
		Image: "image", AuthPath: filepath.Join(root, "auth.json"), RepoRoot: root,
		MethodologyPath: spec, BaselineConfig: "passthrough", CandidateConfig: "conservative-a",
		KTXBin: "ktx", DockerBin: "docker", RegisteredInputs: true,
	})
	if err != nil || code != expectedExit {
		t.Fatalf("Compare code=%d err=%v", code, err)
	}
	return dir
}

func entryCLI(t *testing.T, dir, verb string, extra ...string) ([]byte, error) {
	t.Helper()
	binary := os.Getenv("KTX_ENTRY_CONTRACT_BINARY")
	if binary == "" {
		t.Fatal("KTX_ENTRY_CONTRACT_BINARY must name the local mage build")
	}
	root := t.TempDir()
	env := []string{}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key != "KTX_DB" && key != "HOME" && key != "TMPDIR" && key != "TMP" && key != "TEMP" && !strings.HasPrefix(key, "XDG_") {
			env = append(env, item)
		}
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		env = append(env, key+"="+filepath.Join(root, key))
	}
	tmp := filepath.Join(root, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		env = append(env, key+"="+tmp)
	}
	args := []string{"bench", verb, "--format", "json"}
	args = append(args, extra...)
	if verb != "list" {
		args = append(args, filepath.Base(dir))
	}
	cmd := exec.Command(binary, args...)
	cmd.Env, cmd.Dir = env, filepath.Dir(filepath.Dir(filepath.Dir(dir)))
	output, err := cmd.CombinedOutput()
	if _, statErr := os.Stat(filepath.Join(root, "XDG_DATA_HOME", "ktx", "ktx.db")); !os.IsNotExist(statErr) {
		t.Fatalf("archive command touched default database: %v", statErr)
	}
	return output, err
}

func TestEntryProducerRegenerationMatrix(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range []struct {
		name, spec    string
		priced, cache bool
		schema        int
	}{
		{"exact-unknown-rates", "exact-match", false, false, 8},
		{"exact-cache", "exact-match", false, true, 8},
		{"exact-priced", "exact-match", true, true, 8},
		{"prompt-cache", "prompt-cache-token-hit-rate", true, true, 5},
		{"recovery", "recovery-aware-comparison", true, true, 7},
	} {
		covered[tc.spec] = true
		t.Run(tc.name, func(t *testing.T) {
			dir := produceEntryArchive(t, tc.spec, tc.priced, tc.cache)
			attempts, err := LoadAttempts(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, attempt := range attempts {
				if attempt.SchemaVersion != 3 || attempt.RuntimeEventIntervalID == nil {
					t.Fatalf("producer did not retain current authority: %#v", attempt)
				}
			}
			original, err := os.ReadFile(filepath.Join(dir, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				summary, err := WriteSummary(dir)
				if err != nil || summary.SchemaVersion() != tc.schema {
					t.Fatalf("schema=%d err=%v", summary.SchemaVersion(), err)
				}
				report, err := ValidateExperiment(dir)
				if err != nil || report["valid"] != true || report["target_met"] != true {
					t.Fatalf("report=%v err=%v", report, err)
				}
				if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
					for _, verb := range []string{"list", "show", "summarize", "validate"} {
						if out, err := entryCLI(t, dir, verb); err != nil {
							t.Fatalf("%s: %v %s", verb, err, out)
						}
					}
				}
			}
			regenerated, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
			if !bytes.Equal(original, regenerated) {
				t.Fatal("producer/regenerator summary bytes differ")
			}
			// Copied ledger and session counters cannot become measurement authority.
			for i := range attempts {
				attempts[i].Metrics = Metrics{ToolCallCount: 999, TokensIn: float64Pointer(999), Cost: float64Pointer(999)}
				database, err := db.Open(filepath.Join(dir, attempts[i].Artifacts["database"]))
				if err != nil {
					t.Fatal(err)
				}
				_, updateErr := database.DB().Exec("UPDATE sessions SET tool_call_count=999")
				closeErr := database.Close()
				if updateErr != nil || closeErr != nil {
					t.Fatalf("session projection drift: %v %v", updateErr, closeErr)
				}
			}
			writeAttemptRecords(t, dir, attempts...)
			result, err := RegenerateSummary(dir)
			if err != nil || len(result.ProjectionDivergences) == 0 {
				t.Fatalf("drift regeneration=%v err=%v", result, err)
			}
			regenerated, _ = os.ReadFile(filepath.Join(dir, "summary.json"))
			if !bytes.Equal(original, regenerated) {
				t.Fatal("copied attempt/session counters changed the current summary")
			}
			report, err := ValidateExperiment(dir)
			if err != nil || report["valid"] != true {
				t.Fatalf("drift validation=%v err=%v", report, err)
			}
		})
	}
	for _, spec := range benchregistry.Active().Entries(benchregistry.Spec) {
		if !covered[spec.ID] {
			t.Errorf("registered spec %q has no current producer/regenerator control", spec.ID)
		}
	}
}

func TestEntryContracts(t *testing.T) {
	control := produceEntryArchive(t, "exact-match", false, false)
	tests := []struct {
		name   string
		mutate func(string, map[string]any)
	}{
		{"manifest-only", func(dir string, m map[string]any) {
			clear(m)
			m["experiment_id"] = "entry-control"
			if err := os.Remove(filepath.Join(dir, "attempts.jsonl")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}},
		{"inputs-empty", func(_ string, m map[string]any) { m["inputs"] = []any{} }},
		{"methodology-version-missing", func(_ string, m map[string]any) { delete(m["methodology"].(map[string]any), "version") }},
	}
	for _, kind := range []string{"spec", "pipeline"} {
		tests = append(tests, struct {
			name   string
			mutate func(string, map[string]any)
		}{"required-input-" + kind, func(_ string, m map[string]any) {
			inputs := []any{}
			for _, input := range m["inputs"].([]any) {
				if input.(map[string]any)["kind"] != kind {
					inputs = append(inputs, input)
				}
			}
			m["inputs"] = inputs
		}})
	}
	for _, field := range []string{"schema_version", "methodology", "target_valid_per_pipeline", "task", "inputs"} {
		for _, value := range []string{"missing", "null"} {
			tests = append(tests, struct {
				name   string
				mutate func(string, map[string]any)
			}{field + "-" + value, func(_ string, m map[string]any) {
				if value == "missing" {
					delete(m, field)
				} else {
					m[field] = nil
				}
			}})
		}
	}
	for _, value := range []any{0, -1, 999} {
		tests = append(tests, struct {
			name   string
			mutate func(string, map[string]any)
		}{"manifest-version-" + stringJSON(value), func(_ string, m map[string]any) { m["schema_version"] = value }})
	}
	for _, value := range []any{nil, "", "unknown", "v99", "v1", "v4", "v4-eval-matrix", "v5-cache-aware"} {
		tests = append(tests, struct {
			name   string
			mutate func(string, map[string]any)
		}{"methodology-version-" + stringJSON(value), func(_ string, m map[string]any) { m["methodology"].(map[string]any)["version"] = value }})
	}
	for _, value := range []any{0, -1} {
		tests = append(tests, struct {
			name   string
			mutate func(string, map[string]any)
		}{"target-" + stringJSON(value), func(_ string, m map[string]any) { m["target_valid_per_pipeline"] = value }})
	}
	for _, value := range []string{"missing", "empty", "null", "unknown-version", "missing-version", "null-version", "retired-one-version", "retired-two-version"} {
		tests = append(tests, struct {
			name   string
			mutate func(string, map[string]any)
		}{"ledger-" + value, func(dir string, _ map[string]any) {
			path := filepath.Join(dir, "attempts.jsonl")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if value == "missing" {
				return
			}
			data := []byte{}
			if value == "null" {
				data = []byte("null\n")
			}
			if strings.HasSuffix(value, "version") {
				records, err := LoadAttempts(control)
				if err != nil {
					t.Fatal(err)
				}
				for _, record := range records {
					raw, _ := json.Marshal(record)
					var m map[string]any
					if err := json.Unmarshal(raw, &m); err != nil {
						t.Fatal(err)
					}
					switch value {
					case "retired-one-version":
						m["schema_version"] = 1
					case "retired-two-version":
						m["schema_version"] = 2
					case "unknown-version":
						m["schema_version"] = 999
					case "missing-version":
						delete(m, "schema_version")
					case "null-version":
						m["schema_version"] = nil
					}
					raw, _ = json.Marshal(m)
					data = append(append(data, raw...), '\n')
				}
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), ".ktx", "experiments", "entry-control")
			if tc.name == "manifest-only" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := copyTree(control, dir, nil); err != nil {
					t.Fatal(err)
				}
			}
			data, _ := os.ReadFile(filepath.Join(control, "manifest.json"))
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			tc.mutate(dir, m)
			if err := WriteJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				for _, verb := range []string{"list", "show"} {
					if out, err := entryCLI(t, dir, verb); err != nil {
						t.Errorf("permissive %s: %v %s", verb, err, out)
					}
				}
				for _, verb := range []string{"summarize", "validate"} {
					if _, err := entryCLI(t, dir, verb); err == nil {
						t.Errorf("%s accepted invalid entry", verb)
					}
				}
			}
			if _, err := WriteSummary(dir); err == nil {
				t.Error("WriteSummary accepted invalid entry")
			} else if strings.Contains(tc.name, "version") && !strings.Contains(err.Error(), "unsupported") {
				t.Errorf("version rejection is not explicit: %v", err)
			}
			if report, err := ValidateExperiment(dir); err == nil {
				t.Errorf("ValidateExperiment accepted invalid entry: %v", report)
			}
			after, _ := os.ReadFile(filepath.Join(dir, "summary.json"))
			if !bytes.Equal(before, after) {
				t.Error("rejected regeneration changed summary")
			}
		})
	}
}

func TestCurrentArchiveRejectsUnsupportedSummaryVersions(t *testing.T) {
	dir := produceEntryArchive(t, "exact-match", true, true)
	path := filepath.Join(dir, "summary.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []any{nil, 0, -1, 1, 2, 3, 4, 6, 999} {
		t.Run(stringJSON(version), func(t *testing.T) {
			var summary map[string]any
			if err := json.Unmarshal(original, &summary); err != nil {
				t.Fatal(err)
			}
			summary["schema_version"] = version
			if err := WriteJSON(path, summary); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateExperiment(dir); err == nil || !strings.Contains(err.Error(), "unsupported summary schema_version") {
				t.Fatalf("version rejection = %v", err)
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				if out, err := entryCLI(t, dir, "validate"); err == nil || !bytes.Contains(out, []byte("unsupported summary schema_version")) {
					t.Fatalf("CLI version rejection = %s %v", out, err)
				}
			}
			// These are current retained sources with a corrupt derived summary,
			// not a historical archive. Rebuilding never interprets that summary.
			if _, err := WriteSummary(dir); err != nil {
				t.Fatal(err)
			}
			regenerated, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, regenerated) {
				t.Fatalf("current-source regeneration changed bytes: %v", err)
			}
		})
	}
}

func TestEntryIncompleteCurrentEvidence(t *testing.T) {
	for _, spec := range []string{"exact-match", "prompt-cache-token-hit-rate", "recovery-aware-comparison"} {
		t.Run(spec, func(t *testing.T) {
			control := produceEntryArchive(t, spec, true, true)
			for _, fault := range []string{"missing-side", "missing-stream", "stream-hash-mismatch"} {
				t.Run(fault, func(t *testing.T) {
					dir := filepath.Join(t.TempDir(), ".ktx", "experiments", "entry-control")
					if err := copyTree(control, dir, nil); err != nil {
						t.Fatal(err)
					}
					attempts, err := LoadAttempts(dir)
					if err != nil {
						t.Fatal(err)
					}
					if fault == "missing-side" {
						kept := []Attempt{}
						for _, attempt := range attempts {
							if attempt.Side == Baseline {
								kept = append(kept, attempt)
							}
						}
						writeAttemptRecords(t, dir, kept...)
					} else {
						for _, attempt := range attempts {
							if attempt.Side == Candidate && (attempt.Role == "" || attempt.Role == MeasuredWarm) {
								path := filepath.Join(dir, attempt.Artifacts["stream"])
								if fault == "missing-stream" {
									if err := os.Remove(path); err != nil {
										t.Fatal(err)
									}
								} else {
									data, err := os.ReadFile(path)
									if err != nil {
										t.Fatal(err)
									}
									if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
										t.Fatal(err)
									}
								}
							}
						}
					}
					if _, err := WriteSummary(dir); err != nil {
						t.Fatal(err)
					}
					report, err := ValidateExperiment(dir)
					if err != nil || report["valid"] != false || report["target_met"] != false {
						t.Fatalf("incomplete evidence: report=%v err=%v", report, err)
					}
					if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
						if out, err := entryCLI(t, dir, "summarize"); err != nil {
							t.Fatalf("summarize: %v %s", err, out)
						}
						out, err := entryCLI(t, dir, "validate")
						if err == nil || !bytes.Contains(out, []byte(`"target_met": false`)) {
							t.Fatalf("validate: %v %s", err, out)
						}
					}
				})
			}
		})
	}
}

func TestEntryRequiredCacheContract(t *testing.T) {
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	inputs := []InputMetadata{}
	for _, input := range manifest.Inputs {
		if input.Kind != "contract" {
			inputs = append(inputs, input)
		}
	}
	manifest.Inputs = inputs
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSummary(dir); err == nil {
		t.Fatal("missing contract accepted")
	}
	if _, err := ValidateExperiment(dir); err == nil {
		t.Fatal("missing contract validated")
	}
	if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
		for _, verb := range []string{"summarize", "validate"} {
			if out, err := entryCLI(t, dir, verb); err == nil || !bytes.Contains(out, []byte("requires retained contract")) {
				t.Fatalf("%s: %v %s", verb, err, out)
			}
		}
	}
}

func stringJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
