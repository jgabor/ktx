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

	"github.com/jgabor/ktx/internal/benchcontract"
)

var testCommit = strings.Repeat("a", 40)

// writeTestPluginSource gives a fixture checkout the OpenCode package files
// the benchmark image installs and returns their content hash.
func writeTestPluginSource(root string) (string, error) {
	directory := filepath.Join(root, filepath.FromSlash(benchcontract.PluginSourceDir))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	files := map[string]string{"package.json": `{"name":"` + benchcontract.PluginPackage + `"}` + "\n", "server.ts": "export default async () => ({})\n"}
	for name, content := range files {
		path := filepath.Join(directory, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return benchcontract.PluginFilesSHA256(directory, benchcontract.PluginFiles)
}

// testPluginCapabilities renders the plugin identity fields of an image's
// capability JSON for one commit and package hash.
func testPluginCapabilities(commit, digest string) string {
	return fmt.Sprintf(`"ktx_commit":%q,"ktx_commit_modified":false,"opencode_plugin_package":%q,"opencode_plugin_commit":%q,"opencode_plugin_sha256":%q,"opencode_plugins":[%q]`, commit, benchcontract.PluginPackage, commit, digest, benchcontract.PluginSpec)
}

func TestValidationLabelsPluginIdentity(t *testing.T) {
	control := produceEntryArchive(t, "exact-match", false, false)
	report, err := ValidateExperiment(control)
	if err != nil {
		t.Fatal(err)
	}
	if report["valid"] != true || report["plugin_identity"] != pluginIdentityVerified {
		t.Fatalf("current archive report = %v", report)
	}

	// A historical archive has no plugin source identity and keeps validating.
	historical := filepath.Join(t.TempDir(), "historical")
	if err := copyTree(control, historical, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(historical, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["schema_version"] = historicalManifestSchemaVersion
	delete(manifest, "opencode_plugin")
	if err := WriteJSON(filepath.Join(historical, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(historical, "opencode-plugin")); err != nil {
		t.Fatal(err)
	}
	report, err = ValidateExperiment(historical)
	if err != nil {
		t.Fatal(err)
	}
	if report["valid"] != true || report["plugin_identity"] != pluginIdentityHistorical {
		t.Fatalf("historical archive report = %v", report)
	}
}

func TestPreflightPluginSourceIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configure  func(*fakeExecutor)
		wantReason string
	}{
		{"same commit and hash", func(*fakeExecutor) {}, ""},
		{"other commit", func(f *fakeExecutor) { f.imageCommit = strings.Repeat("b", 40) }, "plugin_commit_mismatch"},
		{"other hash", func(f *fakeExecutor) { f.imagePluginSHA256 = strings.Repeat("b", 64) }, "plugin_hash_mismatch"},
		{"other commit and hash", func(f *fakeExecutor) {
			f.imageCommit, f.imagePluginSHA256 = strings.Repeat("b", 40), strings.Repeat("b", 64)
		}, "plugin_commit_mismatch,plugin_hash_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeExecutor{sessions: map[string]string{}}
			tc.configure(fake)
			runner := &Runner{Executor: fake}
			report, _, err := runner.preflightPrepared(context.Background(), CompareOptions{RepoRoot: t.TempDir(), Image: "image", DockerBin: "docker"})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(report.Reasons, ","); got != tc.wantReason || report.Eligible != (tc.wantReason == "") {
				t.Fatalf("eligible=%v reasons=%q, want %q", report.Eligible, got, tc.wantReason)
			}
		})
	}
}

func TestPreflightBuildPassesTheCheckoutCommit(t *testing.T) {
	for _, tc := range []struct {
		name, status, wantModified string
	}{
		{"clean tree", "", "false"},
		{"tree with changes", " M plugins/opencode/server.ts\n", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeExecutor{sessions: map[string]string{}}
			var build []string
			runner := &Runner{Executor: responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
				if command.Args[0] == "git" && command.Args[len(command.Args)-1] == "--porcelain" {
					_, err := io.WriteString(command.Stdout, tc.status)
					return CommandResult{}, err
				}
				if command.Args[0] == "docker" && command.Args[1] == "build" {
					build = command.Args
					return CommandResult{}, nil
				}
				return fake.Run(ctx, command)
			})}
			report, _, err := runner.preflightPrepared(context.Background(), CompareOptions{RepoRoot: filepath.Join("..", ".."), Image: "image", DockerBin: "docker", BuildImage: true})
			if err != nil {
				t.Fatal(err)
			}
			if !report.Eligible {
				t.Fatalf("reasons = %v", report.Reasons)
			}
			got := strings.Join(build, " ")
			for _, want := range []string{"--build-arg KTX_COMMIT=" + testCommit, "--build-arg KTX_COMMIT_MODIFIED=" + tc.wantModified} {
				if !strings.Contains(got, want) {
					t.Fatalf("docker build %q lacks %q", got, want)
				}
			}
		})
	}
}

func TestPreflightPluginSourceUnavailable(t *testing.T) {
	runner := &Runner{Executor: responseExecutor(func(_ context.Context, command Command) (CommandResult, error) {
		if command.Args[0] == "git" {
			_, err := io.WriteString(command.Stdout, testCommit+"\n")
			return CommandResult{}, err
		}
		t.Fatalf("preflight ran %v without a plugin source", command.Args)
		return CommandResult{}, nil
	})}
	report, _, err := runner.preflightPrepared(context.Background(), CompareOptions{RepoRoot: t.TempDir(), Image: "image", DockerBin: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(report.Reasons, ","); got != "plugin_source_unavailable" {
		t.Fatalf("reasons = %q", got)
	}
}

func TestValidateBenchmarkCapabilitiesPluginIdentity(t *testing.T) {
	valid := benchmarkCapabilities{
		KTXVersion: "test", OpenCodeVersion: benchcontract.RuntimeVersion, EventCollectorContract: benchcontract.ContractVersion,
		KTXCommit: testCommit, PluginCommit: testCommit, PluginPackage: benchcontract.PluginPackage, PluginSHA256: strings.Repeat("c", 64),
		Plugins: []string{benchcontract.PluginSpec}, ExplicitSessionOwnership: true, SerializedCallbacks: true, CompleteIntervalLifecycle: true,
		EpochTracking: true, StrictAcknowledgements: true, ExactModelStepIdentity: true, RetainedDuplicateCallbacks: true,
		AcceptedModelStepProjection: true, CallbackQueueContinuation: true, FailClosedIntervalValidation: true, PreflightBeforeAuth: true,
		NormalizedKinds: []string{"collector_open", "model_step", "retry", "compaction", "interruption", "other"},
	}
	for _, tc := range []struct {
		name   string
		mutate func(*benchmarkCapabilities)
		want   string
	}{
		{"one plugin from the binary's commit", func(*benchmarkCapabilities) {}, ""},
		{"image with a generated plugin only", func(c *benchmarkCapabilities) { c.KTXCommit, c.PluginCommit, c.PluginSHA256 = "", "", "" }, "binary_or_plugin_identity_missing"},
		{"other package", func(c *benchmarkCapabilities) { c.PluginPackage = "other" }, "binary_or_plugin_identity_missing"},
		{"second plugin", func(c *benchmarkCapabilities) {
			c.Plugins = []string{"file:///home/node/.config/opencode/plugins/ktx.ts", benchcontract.PluginSpec}
		}, "plugin_not_unique"},
		{"registry plugin", func(c *benchmarkCapabilities) { c.Plugins = []string{benchcontract.PluginPackage} }, "plugin_not_unique"},
		{"package from another commit", func(c *benchmarkCapabilities) { c.PluginCommit = strings.Repeat("b", 40) }, "plugin_binary_commit_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capabilities := valid
			tc.mutate(&capabilities)
			if got := validateBenchmarkCapabilities(capabilities); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}
