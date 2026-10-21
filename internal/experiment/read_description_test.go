package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
)

func readDescriptionTestOptions(t *testing.T) CompareOptions {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	pipeline := filepath.Join(root, "tools/bench/pipelines/passthrough.toml")
	return CompareOptions{
		ReadDescriptionTreatment: benchcontract.ReadDescriptionTreatment, RepoRoot: root,
		Model: "openai/gpt-6-luna", Variant: "high", RuntimeConfig: "registration.json", PricingSnapshot: "pricing.json",
		CacheControl: "observed", methodologyVersion: exactMatchMethodologyVersion, TargetValid: 1, MaxAttempts: 2,
		Task: Task{WorkspaceMode: "corpus", Prompt: "synthetic first", FollowupPrompt: "synthetic second"}, BaselineConfig: pipeline, CandidateConfig: pipeline,
	}
}

func readDescriptionTestRunner(t *testing.T) (*Runner, CompareOptions, *fakeExecutor) {
	t.Helper()
	isolateExecutionIdentityTest(t)
	fixture, _, sample := readDescriptionTestArchive(t)
	hooks, err := os.ReadFile(filepath.Join(fixture, sample.Artifacts["read_description"]))
	if err != nil {
		t.Fatal(err)
	}
	originalOptions := readDescriptionTestOptions(t)
	root := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, name := range []string{"docs/reports/normalization/normalization-read-original.txt", "docs/reports/normalization/normalization-read-candidate.txt", "tools/bench/pipelines/passthrough.toml", "docs/reports/normalization/normalization-model-registration.json"} {
		data, err := os.ReadFile(filepath.Join(originalOptions.RepoRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		write(name, data)
	}
	spec := write("tools/bench/specs/exact-match.yaml", []byte("methodology_version: v8-exact-match\n"))
	write("Dockerfile.benchmark", []byte("FROM synthetic-test-image\n"))
	auth := write("synthetic-auth.json", []byte(`{"openai":{"type":"oauth","access":"synthetic","refresh":"synthetic","expires":1}}`))
	price, err := testPriceResolver("", "")
	if err != nil {
		t.Fatal(err)
	}
	pricing, err := writePricingSnapshot(root, "openai", "gpt-6-luna", price)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeExecutor{sessions: map[string]string{}, fullStepUsage: true}
	runner := &Runner{Now: time.Now, Executor: responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
		if !contains(command.Args, "run") {
			return fake.Run(ctx, command)
		}
		var output bytes.Buffer
		forward := command.Stdout
		command.Stdout = &output
		result, err := fake.Run(ctx, command)
		if err != nil {
			return result, err
		}
		if contains(command.Args, "--capabilities") {
			var c map[string]any
			if err := json.Unmarshal(output.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			c["read_description_treatment"] = benchcontract.ReadDescriptionTreatment
			c["ktx_binary_sha256"] = strings.Repeat("c", 64)
			err = json.NewEncoder(forward).Encode(c)
			return result, err
		}
		side := dockerEnvironmentValue(command.Args, benchcontract.ReadDescriptionEnv)
		mount := mountSource(command.Args, benchcontract.ReadDescriptionDir)
		if mount == "" || side == "" {
			t.Fatal("missing treatment mount/arm")
		}
		var stream bytes.Buffer
		for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
			var event map[string]any
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatal(err)
			}
			event["sessionID"] = "ses_runtime-" + side
			if err := json.NewEncoder(&stream).Encode(event); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(mount, "turn-1.jsonl"), stream.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		var events bytes.Buffer
		for _, line := range strings.Split(strings.TrimSpace(string(hooks)), "\n") {
			var r readDefinitionObservation
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				t.Fatal(err)
			}
			r.Owner = dockerEnvironmentValue(command.Args, benchcontract.SessionEnv)
			r.Interval = dockerEnvironmentValue(command.Args, benchcontract.IntervalEnv)
			r.Side = side
			if r.Tool == "read" && side == Candidate {
				b, err := os.ReadFile(filepath.Join(mount, "candidate.txt"))
				if err != nil {
					t.Fatal(err)
				}
				r.After = string(b)
				r.AfterSHA256 = benchcontract.ReadDescriptionCandidateSHA256
			}
			if err := json.NewEncoder(&events).Encode(r); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(mount, "hooks.jsonl"), events.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(forward, &stream)
		return result, err
	})}
	o := originalOptions
	o.RepoRoot = root
	o.DataDir = filepath.Join(root, ".ktx/experiments")
	o.ExperimentID = "synthetic-read-treatment"
	o.RuntimeConfig = filepath.Join(root, "docs/reports/normalization/normalization-model-registration.json")
	o.PricingSnapshot = filepath.Join(root, pricing.Path)
	o.PricingSHA256 = pricing.SHA256
	o.BaselineConfig = filepath.Join(root, "tools/bench/pipelines/passthrough.toml")
	o.CandidateConfig = o.BaselineConfig
	o.MethodologyPath = spec
	o.AuthPath = auth
	o.Task.FollowupPrompt = ""
	o.Task.ID = "synthetic"
	answer := "done"
	o.Task.ExpectedOutput = &answer
	o.DockerBin = "docker"
	o.KTXBin = "ktx"
	return runner, o, fake
}

func TestReadDescriptionRunnerAndLocalArchive(t *testing.T) {
	runner, o, fake := readDescriptionTestRunner(t)
	dir, _, code, err := runner.Compare(context.Background(), o)
	if err != nil || code != 0 {
		t.Fatalf("compare code=%d err=%v", code, err)
	}
	if _, err := RequireValidExperiment(dir); err != nil {
		t.Fatal(err)
	}
	if len(fake.runArgs) != 2 {
		t.Fatalf("attempts=%d", len(fake.runArgs))
	}
	if os.Getenv("KTX_READ_DESCRIPTION_CLI") != "" {
		invoke := func(wantSuccess bool, args ...string) { invokeReadDescriptionCLI(t, o.RepoRoot, wantSuccess, args...) }
		invoke(true, "bench", "show", o.ExperimentID)
		invoke(true, "bench", "validate", o.ExperimentID)
		invoke(true, "bench", "summarize", o.ExperimentID)
		invoke(true, "bench", "validate", o.ExperimentID)
		if err := os.Remove(filepath.Join(dir, "inputs/read-description/original.txt")); err != nil {
			t.Fatal(err)
		}
		invoke(false, "bench", "validate", o.ExperimentID)
		invoke(false, "bench", "summarize", o.ExperimentID)
	}
}

func invokeReadDescriptionCLI(t *testing.T, root string, wantSuccess bool, args ...string) []byte {
	t.Helper()
	binary := os.Getenv("KTX_READ_DESCRIPTION_CLI")
	if binary == "" {
		return nil
	}
	envRoot := t.TempDir()
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "KTX_DB" && key != "HOME" && !strings.HasPrefix(key, "XDG_") {
			env = append(env, entry)
		}
	}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		env = append(env, key+"="+filepath.Join(envRoot, key))
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir, cmd.Env = root, env
	out, err := cmd.CombinedOutput()
	if (err == nil) != wantSuccess {
		t.Fatalf("CLI %v: %v %s", args, err, out)
	}
	return out
}

func TestReadDescriptionFrozenInputs(t *testing.T) {
	o := readDescriptionTestOptions(t)
	if err := validateReadDescriptionOptions(o); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(o.RepoRoot, "docs/reports/normalization/normalization-read-original.txt"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile(filepath.Join(o.RepoRoot, "docs/reports/normalization/normalization-read-candidate.txt"))
	if err != nil {
		t.Fatal(err)
	}
	transformed := string(original)
	for _, line := range []string{"- The filePath parameter should be an absolute path.\n", "- By default, this tool returns up to 2000 lines from the start of the file.\n", "- The offset parameter is the line number to start from (1-indexed).\n"} {
		if strings.Count(transformed, line) != 1 {
			t.Fatal("removal count")
		}
		transformed = strings.Replace(transformed, line, "", 1)
	}
	if transformed != string(candidate) {
		t.Fatal("unexpected description edits")
	}
	for name, mutate := range map[string]func(*CompareOptions){
		"transform":         func(o *CompareOptions) { o.ReadDescriptionTreatment += "-changed" },
		"model":             func(o *CompareOptions) { o.Model = "openai/other" },
		"private-workspace": func(o *CompareOptions) { o.Task.WorkspaceMode = "repo" },
		"pipeline": func(o *CompareOptions) {
			o.CandidateConfig = filepath.Join(o.RepoRoot, "tools/bench/pipelines/conservative-a.toml")
		},
		"warmup":             func(o *CompareOptions) { o.Warmup = 1 },
		"unclaimed-followup": func(o *CompareOptions) { o.ReadDescriptionTreatment = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := o
			mutate(&bad)
			if validateReadDescriptionOptions(bad) == nil {
				t.Fatal("accepted changed scope")
			}
		})
	}
	for _, text := range []string{"[pipeline.default]\nstages=[]\n[plugin.x]\ncommand='bad'\n", "[pipeline.default]\nstages=['head:1']\n"} {
		if readDescriptionPipeline([]byte(text)) == nil {
			t.Fatal("accepted non-passthrough config")
		}
	}
}

// This is deterministic synthetic evidence, not a runtime/provider result.
func readDescriptionTestArchive(t *testing.T) (string, Manifest, Attempt) {
	t.Helper()
	dir := t.TempDir()
	o := readDescriptionTestOptions(t)
	metadata, err := captureReadDescription(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	variant := "high"
	manifest := Manifest{
		ReadDescription: metadata, Inputs: readDescriptionInputs(), Model: o.Model, Variant: &variant,
		RuntimeConfig: &InputMetadata{}, Pricing: &PricingSnapshotMetadata{}, Identities: map[string]string{"opencode": "1.18.5", "ktx_binary_sha256": strings.Repeat("a", 64), "generated_plugin_sha256": strings.Repeat("b", 64)}, Task: o.Task,
		Image: map[string]string{"id": testImageID}, Pipelines: map[string]PipelineMetadata{Baseline: {Path: "pipeline.toml"}, Candidate: {Path: "pipeline.toml"}},
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("pipeline.toml", []byte("[pipeline.default]\nstages=[]\n"))
	owner, interval := "owner", "interval"
	attempt := Attempt{AttemptID: "baseline-001", Side: Baseline, SessionID: &owner, RuntimeEventIntervalID: &interval, ReadDescriptionTreatment: benchcontract.ReadDescriptionTreatment, Artifacts: map[string]string{}, ContentHashes: map[string]string{}}
	retain := func(key, path string, data []byte) {
		write(path, data)
		attempt.Artifacts[key] = path
		attempt.ContentHashes[key] = sha256Bytes(data)
	}
	stream := []byte(`{"type":"text","sessionID":"ses_synthetic","part":{"text":"synthetic"}}` + "\n" + `{"type":"step_finish","sessionID":"ses_synthetic","part":{"reason":"stop"}}` + "\n")
	retain("turn_1", "attempts/baseline-001/read-description/turn-1.jsonl", stream)
	retain("turn_2", "attempts/baseline-001/read-description/turn-2.jsonl", stream)
	attempt.ContentHashes["stream"] = sha256Bytes(append(append([]byte{}, stream...), stream...))
	launch, _ := json.Marshal([]string{"docker", "run", testImageID, "-m", o.Model, "--variant", "high", "--followup", o.Task.FollowupPrompt, o.Task.Prompt})
	retain("launch", "attempts/baseline-001/launch.json", launch)
	original, err := os.ReadFile(filepath.Join(dir, metadata.Original.Path))
	if err != nil {
		t.Fatal(err)
	}
	ast, err := os.ReadFile("testdata/read-parameters-ast.json")
	if err != nil {
		t.Fatal(err)
	}
	ast = bytes.TrimSuffix(ast, []byte("\n"))
	if sha256Bytes(ast) != benchcontract.ReadParametersASTSHA256 {
		t.Fatal("AST fixture drift")
	}
	var hooks bytes.Buffer
	for i, tool := range strings.Fields("invalid question bash read glob grep task webfetch todowrite skill apply_patch") {
		r := readDefinitionObservation{
			Version: metadata.Version, Boundary: "tool.definition", Runtime: "1.18.5", Owner: owner, Interval: interval, Epoch: "epoch", Ordinal: i + 1, Side: Baseline, Tool: tool,
			BeforeSHA256: sha256Bytes([]byte(tool)), AfterSHA256: sha256Bytes([]byte(tool)), ParametersSHA256: sha256Bytes([]byte(tool)), JSONSchemaSHA256: sha256Bytes([]byte("null")), ParametersUnchanged: true, JSONSchemaUnchanged: true,
		}
		if tool == "read" {
			r.Before = string(original)
			r.After = r.Before
			r.BeforeSHA256 = metadata.Original.SHA256
			r.AfterSHA256 = r.BeforeSHA256
			r.ParametersAST = string(ast)
			r.ParametersSHA256 = benchcontract.ReadParametersASTSHA256
		}
		if err := json.NewEncoder(&hooks).Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	retain("read_description", "attempts/baseline-001/read-description/hooks.jsonl", hooks.Bytes())
	return dir, manifest, attempt
}

func TestReadDescriptionRetainedEvidence(t *testing.T) {
	for _, broken := range []string{"", "source", "missing", "metadata", "unclaimed", "hash", "parameters", "ordinal", "partial", "second-turn", "different-session", "launch"} {
		t.Run(broken, func(t *testing.T) {
			dir, m, attempt := readDescriptionTestArchive(t)
			path := filepath.Join(dir, attempt.Artifacts["read_description"])
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch broken {
			case "source":
				err = os.WriteFile(filepath.Join(dir, m.ReadDescription.Original.Path), []byte("changed"), 0o600)
			case "missing":
				err = os.Remove(path)
			case "metadata":
				m.ReadDescription.Version += "-changed"
			case "unclaimed":
				m.ReadDescription = nil
			case "hash":
				attempt.ContentHashes["read_description"] = strings.Repeat("0", 64)
			case "parameters":
				data = bytes.ReplaceAll(data, []byte(`"parameters_unchanged":true`), []byte(`"parameters_unchanged":false`))
			case "ordinal":
				data = bytes.ReplaceAll(data, []byte(`"ordinal":2`), []byte(`"ordinal":1`))
			case "partial":
				data = data[:bytes.IndexByte(data, '\n')+1]
			case "second-turn":
				delete(attempt.Artifacts, "turn_2")
			case "different-session":
				p := filepath.Join(dir, attempt.Artifacts["turn_2"])
				var b []byte
				b, err = os.ReadFile(p)
				if err == nil {
					b = bytes.ReplaceAll(b, []byte("ses_synthetic"), []byte("ses_other"))
					err = os.WriteFile(p, b, 0o600)
					attempt.ContentHashes["turn_2"] = sha256Bytes(b)
				}
			case "launch":
				delete(attempt.Artifacts, "launch")
			}
			if err != nil {
				t.Fatal(err)
			}
			if broken == "parameters" || broken == "ordinal" || broken == "partial" {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				attempt.ContentHashes["read_description"] = sha256Bytes(data)
			}
			a, err := openArchive(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			err = a.validateReadDescription(m)
			if err == nil {
				_, err = a.readDescriptionDefinitions(m, &attempt)
			}
			if (err == nil) != (broken == "") {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := validateReadDescriptionComparability([]Attempt{{readDefinitions: map[string]string{"read": "a"}}, {readDefinitions: map[string]string{"read": "b"}}}); err == nil {
		t.Fatal("accepted different arm parameters")
	}
}

func TestRuntimeTurnSession(t *testing.T) {
	complete := `{"type":"text","sessionID":"ses_one","part":{"text":"first"}}` + "\n" + `{"type":"step_finish","sessionID":"ses_one","part":{"reason":"stop"}}` + "\n"
	if id, err := RuntimeTurnSession(strings.NewReader(complete)); err != nil || id != "ses_one" {
		t.Fatalf("%s %v", id, err)
	}
	for _, bad := range []string{"", strings.ReplaceAll(complete, "stop", "tool-calls"), complete + strings.ReplaceAll(complete, "ses_one", "ses_two"), "invalid\n"} {
		if _, err := RuntimeTurnSession(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted incomplete/conflicting turn")
		}
	}
}
