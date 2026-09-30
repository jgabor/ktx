package experiment

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const registrationFixture = `{"provider":{"openai":{"npm":"@ai-sdk/openai","models":{"test":{"id":"test","name":"Test","attachment":true,"reasoning":true,"temperature":false,"tool_call":true,"modalities":{"input":["text"],"output":["text"]},"limit":{"context":1000,"input":800,"output":200},"variants":{"high":{"reasoningEffort":"high"}}}}}}}`

func TestRuntimeConfigurationExactKeys(t *testing.T) {
	for _, key := range []string{"provider", "npm", "models", "id", "name", "attachment", "reasoning", "temperature", "tool_call", "modalities", "input", "output", "limit", "context", "variants", "reasoningEffort"} {
		t.Run("alias/"+key, func(t *testing.T) {
			data := strings.ReplaceAll(registrationFixture, `"`+key+`":`, `"`+strings.ToUpper(key)+`":`)
			if err := validateRuntimeConfig([]byte(data), "openai/test", "high"); err == nil {
				t.Fatal("accepted case-insensitive key alias")
			}
		})
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"package alias", `"npm":"@ai-sdk/openai"`, `"npm":"file:///work/provider.ts","NPM":"@ai-sdk/openai"`},
		{"model alias", `"id":"test"`, `"id":"other","ID":"test"`},
		{"duplicate package", `"npm":"@ai-sdk/openai"`, `"npm":"file:///work/provider.ts","npm":"@ai-sdk/openai"`},
		{"duplicate model id", `"id":"test"`, `"id":"other","id":"test"`},
		{"duplicate escaped id", `"id":"test"`, `"id":"other","\u0069d":"test"`},
		{"duplicate provider field", `"provider":`, `"provider":{},"provider":`},
		{"duplicate provider name", `"openai":`, `"openai":{},"openai":`},
		{"duplicate model name", `"test":`, `"test":{},"test":`},
		{"duplicate variant name", `"high":`, `"high":{},"high":`},
		{"duplicate limit", `"context":1000`, `"context":1,"context":1000`},
		{"duplicate modality", `"input":["text"]`, `"input":["image"],"input":["text"]`},
		{"duplicate variant option", `"reasoningEffort":"high"`, `"reasoningEffort":"low","reasoningEffort":"high"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(registrationFixture, tc.old, tc.replacement, 1)
			if err := validateRuntimeConfig([]byte(data), "openai/test", "high"); err == nil {
				t.Fatal("accepted ambiguous registration")
			}
		})
	}
	for _, data := range []string{registrationFixture, strings.Replace(registrationFixture, `"id":`, `"\u0069d":`, 1)} {
		if err := validateRuntimeConfig([]byte(data), "openai/test", "high"); err != nil {
			t.Fatalf("rejected exact decoded keys: %v", err)
		}
	}
}

func TestRuntimeConfigurationRawReferences(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		valid                  bool
	}{
		{"literal registration", `"Test"`, `"Test"`, true},
		{"file name", `"Test"`, `"{file:/work/secret}"`, false},
		{"env name", `"Test"`, `"{env:MODEL_NAME}"`, false},
		{"file variant key", `"high":`, `"{file:/work/variant}":`, false},
		{"env variant key", `"high":`, `"{env:VARIANT}":`, false},
		{"discarded file value", `"name":"Test"`, `"name":"{file:/work/secret}","name":"Test"`, false},
		{"discarded env value", `"name":"Test"`, `"name":"{env:MODEL_NAME}","name":"Test"`, false},
		{"escaped file path", `"Test"`, `"{file:\u002fwork/secret}"`, false},
		{"raw nested token", `"Test"`, `"{env:{file:/work/secret}}"`, false},
		// A raw token can end at a later object brace, beyond the string value.
		{"escaped closing brace", `"Test"`, `"{file:/work/secret\u007d"`, false},
		// These decode to token-looking strings only AFTER raw substitution.
		{"escaped opening brace", `"Test"`, `"\u007bfile:/work/secret}"`, true},
		{"escaped env spelling", `"Test"`, `"{\u0065nv:MODEL_NAME}"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(registrationFixture, tc.old, tc.replacement, 1)
			err := validateRuntimeConfig([]byte(data), "openai/test", "")
			if (err == nil) != tc.valid || (!tc.valid && !strings.Contains(err.Error(), "env or file references")) {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestRuntimeConfigurationDocumentedRegistration(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "normalization-model-registration.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Provider map[string]struct {
			Models map[string]modelRegistration `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Provider) != 1 || len(config.Provider["openai"].Models) != 1 {
		t.Fatal("fixture must register one OpenAI model")
	}
	for model, registration := range config.Provider["openai"].Models {
		if err := validateRuntimeConfig(data, "openai/"+model, ""); err != nil {
			t.Fatal(err)
		}
		for variant := range registration.Variants {
			if err := validateRuntimeConfig(data, "openai/"+model, variant); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRuntimeConfigurationInputScope(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"registration", registrationFixture, true},
		{"offline transport options", strings.Replace(registrationFixture, `"npm":"@ai-sdk/openai"`, `"npm":"@ai-sdk/openai","options":{"baseURL":"http://127.0.0.1:4101/v1","apiKey":"offline-not-a-credential"}`, 1), false},
		{"plugin", strings.Replace(registrationFixture, `"provider":`, `"plugin":["file:///tmp/plugin.js"],"provider":`, 1), false},
		{"model options", strings.Replace(registrationFixture, `"id":"test"`, `"options":{"plugin":"file:///tmp/plugin.js"},"id":"test"`, 1), false},
		{"model change", strings.Replace(registrationFixture, `"id":"test"`, `"id":"other"`, 1), false},
		{"provider package", strings.Replace(registrationFixture, `@ai-sdk/openai`, `file:///tmp/provider.js`, 1), false},
		{"extra model", strings.Replace(registrationFixture, `"models":{"test":`, `"models":{"other":{},"test":`, 1), false},
		{"malformed", `{`, false},
		{"other model", registrationFixture, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := "openai/test"
			if tc.name == "other model" {
				model = "openai/other"
			}
			if got := validateRuntimeConfig([]byte(tc.body), model, "high"); (got == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, got)
			}
		})
	}
	if err := validateRuntimeConfig([]byte(registrationFixture), "openai/test", "unregistered"); err == nil {
		t.Fatal("accepted unregistered variant")
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := rejectProjectRuntimeConfig(dir); err == nil {
			t.Fatalf("accepted project config %s", name)
		}
	}
}

func TestRuntimeConfigurationRetainedExecutionAndValidation(t *testing.T) {
	isolateExecutionIdentityTest(t)
	for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_AUTH_CONTENT"} {
		t.Setenv(key, "unmounted-host-input")
	}
	root := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("README.md", "fixture")
	write(".opencode/plugins/host-plugin.js", "throw new Error('host plugin leaked')")
	write("Dockerfile.benchmark", "FROM scratch")
	spec := write("tools/bench/specs/exact-match.yaml", "methodology_version: v8-exact-match\n")
	baseline := write("tools/bench/pipelines/passthrough.toml", "[pipeline.default]\nstages = []\n")
	candidate := write("tools/bench/pipelines/candidate.toml", "[pipeline.default]\nstages = ['head:1']\n")
	task := write("tools/bench/tasks/task.toml", "id = 'task'\nprompt = 'do work'\nminimum_tool_calls = 1\n[scoring]\nkind = 'exact_match'\nexpected_output = 'done'\n")
	const authBody = `{"openai":{"type":"oauth","refresh":"synthetic-refresh","access":"synthetic-access","expires":100},"other":{"type":"api","key":"synthetic-key"}}`
	auth := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(auth, []byte(authBody), 0o600); err != nil {
		t.Fatal(err)
	}
	source := write("runtime.json", registrationFixture)
	fake := &fakeExecutor{sessions: map[string]string{}}
	mounted := []string{}
	authMounts := []string{}
	runner := &Runner{Executor: responseExecutor(func(ctx context.Context, command Command) (CommandResult, error) {
		if len(command.Args) > 1 && command.Args[0] == "docker" && command.Args[1] == "run" && !contains(command.Args, "--capabilities") {
			if env := dockerEnvironmentValue(command.Args, "OPENCODE_CONFIG"); env != "/home/node/.config/ktx/opencode.json" {
				t.Errorf("runtime config env = %q", env)
			}
			path := mountSource(command.Args, "/home/node/.config/ktx/opencode.json")
			if !contains(command.Args, path+":/home/node/.config/ktx/opencode.json:ro") || !contains(command.Args, "-m") || !contains(command.Args, "openai/test") || !contains(command.Args, "--variant") || !contains(command.Args, "high") {
				t.Errorf("runtime configuration changed mount, model, or variant")
			}
			mounted = append(mounted, path)
			authPath := mountSource(command.Args, "/home/node/.local/share/opencode/auth.json")
			authMounts = append(authMounts, authPath)
			authData, err := os.ReadFile(authPath)
			if err != nil || string(authData) != authBody || authPath == auth || strings.HasPrefix(authPath, root+string(os.PathSeparator)) || !contains(command.Args, authPath+":/home/node/.local/share/opencode/auth.json:ro") {
				t.Fatal("auth mount is not the checked, disposable read-only input")
			}
			parent, err := os.Stat(filepath.Dir(authPath))
			if err != nil || parent.Mode().Perm() != 0o700 {
				t.Fatal("temporary auth directory is not private")
			}
			for _, key := range []string{"OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_AUTH_CONTENT"} {
				if dockerEnvironmentValue(command.Args, key) != "" || contains(command.Args, key) {
					t.Fatalf("host %s leaked into container", key)
				}
			}
			mounts := 0
			for _, arg := range command.Args {
				if arg == "-v" {
					mounts++
				}
			}
			if mounts != 5 || contains(command.Args, "--env-file") || contains(command.Args, "--volume") {
				t.Fatal("unexpected host inputs mounted")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != registrationFixture {
				t.Errorf("mounted bytes changed: %v", err)
			}
			if len(mounted) == 1 {
				// A later edit to the original must not affect the next arm.
				if err := os.WriteFile(source, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(auth, []byte(`{"https://example.invalid":{"type":"wellknown","key":"CONFIG_TOKEN","token":"synthetic-token"}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		return fake.Run(ctx, command)
	}), Now: func() time.Time { return time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC) }, ResolvePrice: testPriceResolver}
	dir, _, code, err := runner.Compare(context.Background(), CompareOptions{
		Model: "openai/test", Variant: "high", TaskFile: task, TargetValid: 1, MaxAttempts: 2,
		ExperimentID: "config-retained", DataDir: filepath.Join(root, ".ktx", "experiments"), RepoRoot: root, Image: "image", AuthPath: auth,
		MethodologyPath: spec, BaselineConfig: baseline, CandidateConfig: candidate, RuntimeConfig: source, DockerBin: "docker", KTXBin: "ktx",
	})
	if err != nil || code != 0 {
		t.Fatalf("compare code=%d err=%v", code, err)
	}
	if len(authMounts) != 2 || authMounts[0] != authMounts[1] {
		t.Fatal("arms did not share checked auth")
	}
	if _, err := os.Stat(filepath.Dir(authMounts[0])); !os.IsNotExist(err) {
		t.Fatalf("temporary auth directory retained: %v", err)
	}
	var manifest Manifest
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		t.Fatalf("manifest: %v", err)
	}
	config := manifest.RuntimeConfig
	if config == nil || config.SHA256 != sha256Bytes([]byte(registrationFixture)) || len(mounted) != 2 || mounted[0] != mounted[1] || mounted[0] != filepath.Join(dir, config.Path) {
		t.Fatalf("retained identity=%+v mounts=%v", config, mounted)
	}
	parent, err := os.Stat(filepath.Dir(mounted[0]))
	if err != nil || parent.Mode().Perm() != 0o700 {
		t.Fatalf("retained config directory permissions: %v %v", parent, err)
	}
	if _, err := os.Stat(filepath.Join(dir, manifest.Repository.Snapshot.Path, ".opencode")); !os.IsNotExist(err) {
		t.Fatalf("host project plugins leaked into workspace: %v", err)
	}
	if _, err := ValidateExperiment(dir); err != nil {
		t.Fatalf("retained config validation: %v", err)
	}
	if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
		if out, err := entryCLI(t, dir, "validate"); err != nil {
			t.Fatalf("local binary rejected config archive: %v %s", err, out)
		}
	}
	for _, mutation := range []string{"delete", "alter", "missing manifest", "missing inventory", "different inventory"} {
		t.Run(mutation, func(t *testing.T) {
			local := manifest
			local.Inputs = append([]InputMetadata(nil), manifest.Inputs...)
			path := mounted[0]
			switch mutation {
			case "delete":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.WriteFile(path, []byte(registrationFixture), 0o600); err != nil {
						t.Error(err)
					}
				}()
			case "alter":
				if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.WriteFile(path, []byte(registrationFixture), 0o600); err != nil {
						t.Error(err)
					}
				}()
			case "missing manifest":
				local.RuntimeConfig = nil
			case "missing inventory":
				local.Inputs = nil
			case "different inventory":
				local.Inputs[len(local.Inputs)-1].SHA256 = strings.Repeat("a", 64)
			}
			if mutation != "delete" && mutation != "alter" {
				if err := WriteJSON(filepath.Join(dir, "manifest.json"), local); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
						t.Error(err)
					}
				}()
			}
			if _, err := ValidateExperiment(dir); err == nil {
				t.Fatal("accepted missing or altered required configuration")
			}
			if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" && (mutation == "alter" || mutation == "missing inventory") {
				if out, err := entryCLI(t, dir, "validate"); err == nil {
					t.Fatalf("local binary accepted tampered archive: %s", out)
				}
			}
		})
	}
	ledgerPath := filepath.Join(dir, "attempts.jsonl")
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledgerPath, []byte(strings.Replace(string(ledger), `"runtime_config_sha256":"`+config.SHA256+`"`, `"runtime_config_sha256":"wrong"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateExperiment(dir); err == nil || !strings.Contains(err.Error(), "runtime configuration identity mismatch") {
		t.Fatalf("accepted altered attempt identity: %v", err)
	}
	if err := os.WriteFile(ledgerPath, ledger, 0o600); err != nil {
		t.Fatal(err)
	}
	projectPlugins := filepath.Join(dir, manifest.Repository.Snapshot.Path, ".opencode")
	if err := os.Mkdir(projectPlugins, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateExperiment(dir); err == nil {
		t.Fatal("accepted project plugins added to retained workspace")
	}
	if err := os.Remove(projectPlugins); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.Repository.Snapshot.Path, "opencode.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateExperiment(dir); err == nil {
		t.Fatal("accepted conflicting project config added to retained workspace")
	}
}

func TestRuntimeConfigurationPreflightRejectsInvalidBeforeDocker(t *testing.T) {
	isolateExecutionIdentityTest(t)
	root := t.TempDir()
	source := filepath.Join(root, "runtime.json")
	if err := os.WriteFile(source, []byte(`{"plugin":["file:///tmp/unretained.js"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExecutor{sessions: map[string]string{}}
	runner := &Runner{Executor: fake}
	// This is the prepared preflight boundary, before any image inspection.
	report, _, err := runner.preflightPrepared(context.Background(), CompareOptions{RepoRoot: root, Model: "openai/test", RuntimeConfig: source, DockerBin: "docker"})
	if err != nil || report.Eligible || len(report.Reasons) != 1 || report.Reasons[0] != "runtime_config_invalid" || fake.capabilityRuns != 0 {
		t.Fatalf("report=%+v err=%v docker=%d", report, err, fake.capabilityRuns)
	}
}

func TestRuntimeConfigurationAuthBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, auth    string
		withoutConfig bool
		wantError     string
	}{
		{name: "API", auth: `{"openai":{"type":"api","key":"synthetic-secret"}}`},
		{name: "OAuth", auth: `{"openai":{"type":"oauth","refresh":"synthetic-secret","access":"synthetic-secret","expires":100}}`},
		{name: "wellknown", auth: `{"https://example.invalid":{"type":"wellknown","key":"CONFIG_TOKEN","token":"synthetic-secret"}}`, wantError: "cannot use wellknown"},
		{name: "mixed auth", auth: `{"openai":{"type":"api","key":"synthetic-secret"},"https://example.invalid":{"type":"wellknown","key":"CONFIG_TOKEN","token":"synthetic-secret"}}`, wantError: "cannot use wellknown"},
		{name: "case alias cannot hide type", auth: `{"https://example.invalid":{"type":"wellknown","TYPE":"api","key":"CONFIG_TOKEN","token":"synthetic-secret"}}`, wantError: "cannot use wellknown"},
		{name: "escaped type", auth: `{"https://example.invalid":{"\u0074ype":"well\u006bnown","key":"CONFIG_TOKEN","token":"synthetic-secret"}}`, wantError: "cannot use wellknown"},
		{name: "last duplicate type", auth: `{"https://example.invalid":{"type":"api","type":"wellknown","key":"CONFIG_TOKEN","token":"synthetic-secret"}}`, wantError: "cannot use wellknown"},
		{name: "malformed", auth: `{"synthetic-secret":`, wantError: "requires an OpenCode auth object"},
		{name: "no config preserves auth", auth: `{"https://example.invalid":{"type":"wellknown","key":"CONFIG_TOKEN","token":"synthetic-secret"}}`, withoutConfig: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateExecutionIdentityTest(t)
			root := t.TempDir()
			// Even a repository-local TMPDIR must not put auth into the archive.
			tmp := filepath.Join(root, "tmp")
			if err := os.Mkdir(tmp, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", tmp)
			for name, body := range map[string]string{
				"README.md": "fixture", "Dockerfile.benchmark": "FROM scratch",
				"spec.yaml":     "methodology_version: v8-exact-match\n",
				"pipeline.toml": "[pipeline.default]\nstages = []\n",
				"task.toml":     "id = 'task'\nprompt = 'do work'\nminimum_tool_calls = 1\n[scoring]\nkind = 'exact_match'\nexpected_output = 'done'\n",
				"runtime.json":  registrationFixture,
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			auth := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(auth, []byte(tc.auth), 0o600); err != nil {
				t.Fatal(err)
			}
			options := CompareOptions{
				Model: "openai/test", Variant: "high", RepoRoot: root, DataDir: filepath.Join(root, ".ktx", "experiments"), ExperimentID: "auth-boundary",
				TaskFile: filepath.Join(root, "task.toml"), TargetValid: 1, MaxAttempts: 2,
				MethodologyPath: filepath.Join(root, "spec.yaml"), BaselineConfig: filepath.Join(root, "pipeline.toml"), CandidateConfig: filepath.Join(root, "pipeline.toml"),
				RuntimeConfig: filepath.Join(root, "runtime.json"), AuthPath: filepath.Join(root, "missing-auth.json"), DockerBin: "docker", KTXBin: "ktx",
			}
			if tc.withoutConfig {
				options.RuntimeConfig = ""
			}
			fake := &fakeExecutor{sessions: map[string]string{}}
			runner := &Runner{Executor: fake, Now: time.Now, ResolvePrice: testPriceResolver}
			report, err := runner.Preflight(context.Background(), options)
			if err != nil || !report.Eligible || fake.attemptRuns != 0 {
				t.Fatalf("credential-free preflight: report=%+v err=%v attempts=%d", report, err, fake.attemptRuns)
			}
			options.AuthPath = auth
			dir, _, code, err := runner.Compare(context.Background(), options)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || code != 2 || dir != "" || fake.attemptRuns != 0 {
					t.Fatalf("config-bearing auth reached execution: code=%d error=%v attempts=%d", code, err, fake.attemptRuns)
				}
				for _, secret := range []string{"synthetic-secret", "example.invalid", "CONFIG_TOKEN"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatal("auth details leaked into error")
					}
				}
				if _, err := os.Stat(options.DataDir); !os.IsNotExist(err) {
					t.Fatal("rejected auth created an archive")
				}
				return
			}
			if err != nil || code != 0 || fake.attemptRuns != 2 {
				t.Fatalf("ordinary auth rejected: code=%d error=%v attempts=%d", code, err, fake.attemptRuns)
			}
			for _, args := range fake.runArgs {
				path := mountSource(args, "/home/node/.local/share/opencode/auth.json")
				if tc.withoutConfig {
					if path != auth || dockerEnvironmentValue(args, "OPENCODE_CONFIG") != "" {
						t.Fatal("no-config execution changed")
					}
				} else if path == auth {
					t.Fatal("explicit config mounted mutable source auth")
				} else if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatal("checked auth copy was retained")
				}
				if !tc.withoutConfig {
					relative, err := filepath.Rel(root, path)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(filepath.Join(dir, "repository", relative)); !os.IsNotExist(err) {
						t.Fatal("checked auth was archived via repository-local TMPDIR")
					}
				}
			}
		})
	}
}

func TestRuntimeConfigurationFreeArchiveCompatibility(t *testing.T) {
	isolateExecutionIdentityTest(t)
	dir := produceEntryArchive(t, "exact-match", false, false)
	if _, err := ValidateExperiment(dir); err != nil {
		t.Fatalf("config-free archive rejected: %v", err)
	}
}
