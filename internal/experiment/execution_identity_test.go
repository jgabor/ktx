package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testImageID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func isolateExecutionIdentityTest(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	t.Setenv("KTX_DB", "")
	if err := os.Unsetenv("KTX_DB"); err != nil {
		t.Fatal(err)
	}
}

// Synthetic evidence fixtures still need real retained execution inputs.
func retainFixtureConfigs(t *testing.T, dir string, manifest *Manifest, attempts ...*Attempt) {
	t.Helper()
	if manifest.Pipelines == nil {
		manifest.Pipelines = map[string]PipelineMetadata{}
	}
	for _, side := range Sides {
		pipeline := manifest.Pipelines[side]
		oldHash := pipeline.SHA256
		data := []byte("# " + side + "\n[pipeline.default]\nstages = []\n")
		pipeline.Path = "configs/ktx.toml." + side
		pipeline.SHA256 = sha256Bytes(data)
		path := filepath.Join(dir, pipeline.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		manifest.Pipelines[side] = pipeline
		for _, attempt := range attempts {
			if attempt.Side == side && attempt.ConfigSHA256 == oldHash {
				attempt.ConfigSHA256 = pipeline.SHA256
			}
		}
	}
}

func TestExecutionConfigIdentity(t *testing.T) {
	for _, mutation := range []string{"change", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			isolateExecutionIdentityTest(t)
			mounted := ""
			fake := &fakeExecutor{sessions: map[string]string{}, fullStepUsage: true}
			dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true, responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
				for _, arg := range command.Args {
					if strings.HasSuffix(arg, "ktx.toml.candidate:/home/node/.config/ktx/ktx.toml:ro") {
						mounted = strings.TrimSuffix(arg, ":/home/node/.config/ktx/ktx.toml:ro")
					}
				}
				return fake.Run(ctx, command)
			}))
			var manifest Manifest
			data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			config := manifest.Pipelines[Candidate]
			path := filepath.Join(dir, config.Path)
			if mounted != path {
				t.Fatalf("mounted config %q differs from retained identity %q", mounted, path)
			}
			if mutation == "delete" {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte("[pipeline.default]\nstages = []\n"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range manifest.Inputs {
				if input.Kind == "pipeline" && input.ID == config.PipelineID {
					hash, err := fileSHA256(filepath.Join(dir, input.Path))
					if err != nil || hash != input.SHA256 {
						t.Fatalf("other copy changed: %s %v", hash, err)
					}
				}
			}
			if _, err := ValidateExperiment(dir); err == nil || !strings.Contains(err.Error(), "execution config") {
				t.Errorf("ValidateExperiment error = %v", err)
			}
			if _, err := RegenerateSummary(dir); err == nil || !strings.Contains(err.Error(), "execution config") {
				t.Errorf("RegenerateSummary error = %v", err)
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
				for _, verb := range []string{"validate", "summarize"} {
					out, err := entryCLI(t, dir, verb)
					if err == nil || !strings.Contains(string(out), "execution config") {
						t.Errorf("%s: %v %s", verb, err, out)
					}
				}
			}
		})
	}
}

func TestExecutionConfigCaptureIgnoresLaterSourceEdits(t *testing.T) {
	isolateExecutionIdentityTest(t)
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.toml")
	if err := os.WriteFile(source, []byte("[pipeline.default]\nstages = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configs, err := snapshotConfigs(dir, source, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("[pipeline.default]\nstages = ['head:1']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inputs, err := snapshotInputs(dir, CompareOptions{RegisteredInputs: true, SpecID: "spec", MethodologyPath: source, BaselineID: "baseline", CandidateID: "candidate", BaselineConfig: source, CandidateConfig: source})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	for _, input := range inputs {
		if input.Kind == "pipeline" && input.SHA256 != configs[input.ID].SHA256 {
			t.Errorf("captured %s identity changed after source edit", input.ID)
		}
	}
	for _, config := range configs {
		hash, err := fileSHA256(filepath.Join(dir, config.Path))
		if err != nil || hash != config.SHA256 {
			t.Fatalf("execution capture changed: %s %v", hash, err)
		}
		info, err := os.Stat(filepath.Join(dir, config.Path))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("capture lost source permissions: %v %v", info, err)
		}
	}
}

func TestExecutionConfigCopiesMustAgree(t *testing.T) {
	isolateExecutionIdentityTest(t)
	dir := t.TempDir()
	manifest := Manifest{Pipelines: map[string]PipelineMetadata{Baseline: {PipelineID: Baseline}, Candidate: {PipelineID: Candidate}}}
	retainFixtureConfigs(t, dir, &manifest)
	data := []byte("[pipeline.default]\nstages = ['head:1']\n")
	if err := os.WriteFile(filepath.Join(dir, "input.toml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest.Inputs = []InputMetadata{{ID: Baseline, Kind: "pipeline", Format: "toml", Path: "input.toml", SHA256: sha256Bytes(data)}}
	a, err := openArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.validateInputs(manifest.Inputs); err != nil {
		t.Fatal(err)
	}
	if err := a.validateExecutionConfigs(manifest); err == nil || !strings.Contains(err.Error(), "does not match retained pipeline input") {
		t.Fatalf("mismatched copy error = %v", err)
	}
}

func TestExecutionUsesResolvedImageAfterRetag(t *testing.T) {
	isolateExecutionIdentityTest(t)
	fake := &fakeExecutor{sessions: map[string]string{}, fullStepUsage: true}
	tagID, inspections, capabilities, attempts := testImageID, 0, 0, 0
	executor := responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
		args := command.Args
		if args[0] == "docker" && args[1] == "image" {
			inspections++
			_, err := fmt.Fprintln(command.Stdout, tagID)
			tagID = "sha256:" + strings.Repeat("f", 64)
			return CommandResult{}, err
		}
		if args[0] == "docker" && args[1] == "run" {
			if contains(args, "--capabilities") {
				capabilities++
			} else {
				attempts++
			}
			if !contains(args, testImageID) || contains(args, "image") || contains(args, tagID) {
				t.Errorf("execution did not use resolved full image ID: %v", args)
			}
			tagID = "sha256:" + strings.Repeat("e", 64)
		}
		return fake.Run(ctx, command)
	})
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true, executor)
	if inspections != 1 || capabilities != 1 || attempts < 2 {
		t.Fatalf("inspect=%d capabilities=%d attempts=%d", inspections, capabilities, attempts)
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Image["name"] != "image" || manifest.Image["id"] != testImageID {
		t.Fatalf("image metadata = %v", manifest.Image)
	}
}

func TestExecutionRejectsUnavailableOrInvalidImageIdentity(t *testing.T) {
	for _, identity := range []string{"", "sha256:short", "image:latest", "sha256:" + strings.Repeat("g", 64), testImageID + "\n" + testImageID, "unavailable"} {
		t.Run(identity, func(t *testing.T) {
			isolateExecutionIdentityTest(t)
			calls := 0
			runner := Runner{Executor: responseExecutor(func(_ context.Context, command Command) (CommandResult, error) {
				calls++
				if calls != 1 {
					t.Error("execution continued after invalid image identity")
				}
				if identity == "unavailable" {
					return CommandResult{ExitCode: 1}, nil
				}
				_, err := io.WriteString(command.Stdout, identity)
				return CommandResult{}, err
			})}
			repo, err := filepath.Abs(filepath.Join("..", ".."))
			if err != nil {
				t.Fatal(err)
			}
			dataDir := filepath.Join(t.TempDir(), "experiments")
			_, _, code, err := runner.Compare(context.Background(), CompareOptions{DockerBin: "docker", Image: "image", RepoRoot: repo, DataDir: dataDir, ExperimentID: "invalid-image", Model: "openai/test", RegisteredInputs: true, MethodologyPath: "prompt-cache-token-hit-rate", Task: Task{ID: "test", Prompt: "test", ExpectedOutput: stringPointer("done")}})
			if err == nil || code != 2 || calls != 1 || !strings.Contains(err.Error(), "image_capability_unavailable") {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
				t.Fatalf("invalid image created experiment data: %v", err)
			}
		})
	}
}
