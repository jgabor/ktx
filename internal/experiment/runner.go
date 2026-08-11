package experiment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/db"
)

type CompareOptions struct {
	Model              string
	Variant            string
	TaskFile           string
	Task               Task
	TargetValid        int
	MaxAttempts        int
	AttemptTimeout     time.Duration
	TerminationGrace   time.Duration
	ExperimentID       string
	DataDir            string
	Image              string
	AuthPath           string
	RepoRoot           string
	MethodologyPath    string
	BaselineConfig     string
	CandidateConfig    string
	KTXBin             string
	DockerBin          string
	BuildImage         bool
	CacheControl       string
	Warmup             int
	WarmupSet          bool
	methodologyVersion string
}

type Runner struct {
	Executor               CommandExecutor
	Diagnostics            io.Writer
	Now                    func() time.Time
	PID                    int
	ResolvePrice           func(provider, model string) (db.Price, error)
	ResolveCacheCapability CacheCapabilityResolver
}

type PreflightReport struct {
	Eligible               bool     `json:"eligible"`
	DirectEvidence         bool     `json:"direct_evidence"`
	ControlledComparison   bool     `json:"controlled_comparison"`
	SecondaryEvidence      bool     `json:"secondary_evidence"`
	ModelAttempts          int      `json:"model_attempts"`
	ArtifactCreation       bool     `json:"artifact_creation"`
	Image                  string   `json:"image"`
	KTXVersion             string   `json:"ktx_version,omitempty"`
	OpenCodeVersion        string   `json:"opencode_version,omitempty"`
	PluginSHA256           string   `json:"generated_plugin_sha256,omitempty"`
	EventCollectorContract string   `json:"event_collector_contract,omitempty"`
	Reasons                []string `json:"reasons"`
}

var errLocalCollectorContract = errors.New("local collector source contract mismatch")

func NewRunner() *Runner {
	return &Runner{Executor: OSExecutor{}, Diagnostics: os.Stderr, Now: time.Now, PID: os.Getpid(), ResolvePrice: db.ResolvePrice}
}

func (runner *Runner) Compare(ctx context.Context, options CompareOptions) (string, map[string]any, int, error) {
	if err := runner.prepareStructuralOptions(&options); err != nil {
		return "", nil, 2, err
	}
	provider, model, err := splitModel(options.Model)
	if err != nil {
		return "", nil, 2, err
	}
	preflight, runtime, err := runner.preflightPrepared(ctx, options)
	if err != nil {
		return "", nil, 2, err
	}
	if !preflight.Eligible {
		return "", nil, 2, fmt.Errorf("benchmark preflight failed: %s", strings.Join(preflight.Reasons, "; "))
	}
	if err := runner.preparePaidRun(&options); err != nil {
		return "", nil, 2, err
	}
	if err := os.MkdirAll(options.DataDir, 0o755); err != nil {
		return "", nil, 2, err
	}
	experimentID := options.ExperimentID
	if experimentID == "" {
		experimentID = runner.experimentID(ctx, options)
	}
	cacheControl := resolvedCacheControl{}
	if cacheAwareMethodology(options.methodologyVersion) {
		request := CacheCapabilityRequest{Runtime: "opencode", RuntimeVersion: runtime.Identities["opencode"], Provider: provider, Model: model}
		cacheControl, err = resolveCacheControl(options.CacheControl, experimentID, request, runner.ResolveCacheCapability)
		if err != nil {
			return "", nil, 2, err
		}
	}
	resolvePrice := runner.ResolvePrice
	if resolvePrice == nil {
		resolvePrice = db.ResolvePrice
	}
	price, err := resolvePrice(provider, model)
	if err != nil {
		return "", nil, 2, err
	}
	experimentDir := filepath.Join(options.DataDir, experimentID)
	if err := os.Mkdir(experimentDir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", nil, 2, fmt.Errorf("experiment directory already exists: %s", experimentDir)
		}
		return "", nil, 2, err
	}
	if err := os.Mkdir(filepath.Join(experimentDir, "attempts"), 0o755); err != nil {
		return "", nil, 2, err
	}
	pricing, err := writePricingSnapshot(experimentDir, provider, model, price)
	if err != nil {
		return "", nil, 2, err
	}
	repositoryDir := filepath.Join(experimentDir, "repository")
	dataRelative, _ := filepath.Rel(options.RepoRoot, options.DataDir)
	dataRelative = filepath.ToSlash(dataRelative)
	if err := copyTree(options.RepoRoot, repositoryDir, func(relative string, entry os.DirEntry) bool {
		base := filepath.Base(relative)
		insideDataDir := dataRelative != "." && dataRelative != "" && dataRelative != ".." && !strings.HasPrefix(dataRelative, "../") && (relative == dataRelative || strings.HasPrefix(relative, dataRelative+"/"))
		return base == ".git" || base == ".bench-data" || base == "__pycache__" || insideDataDir
	}); err != nil {
		return "", nil, 2, fmt.Errorf("snapshot repository: %w", err)
	}
	repositoryHash, repositoryFiles, err := directorySHA256(repositoryDir)
	if err != nil {
		return "", nil, 2, err
	}
	configs, err := snapshotConfigs(experimentDir, options.BaselineConfig, options.CandidateConfig)
	if err != nil {
		return "", nil, 2, err
	}
	manifest, err := runner.createManifest(ctx, options, experimentID, configs, SnapshotMetadata{Path: "repository", SHA256: repositoryHash, FileCount: repositoryFiles}, runtime)
	if err != nil {
		return "", nil, 2, err
	}
	manifest.Pricing = pricing
	if cacheAwareMethodology(options.methodologyVersion) {
		manifest.CacheControl = &CacheControlManifest{Mode: cacheControl.mode, Source: cacheControl.source, NamespaceSHA256: cacheControl.hashes, WarmupCount: options.Warmup, IdleGapMS: 0, Model: options.Model, Provider: provider, Variant: options.Variant, RuntimeVersion: manifest.Identities["opencode"], TTFTAvailable: false, TTFTUnavailableReason: "retained OpenCode stream timestamps do not provide a reliable request start"}
	}
	if err := WriteJSON(filepath.Join(experimentDir, "manifest.json"), manifest); err != nil {
		return "", nil, 2, err
	}
	attempts := []Attempt{}
	ordinals := map[string]int{Baseline: 0, Candidate: 0}
	executionOrder := 0
	if cacheAwareMethodology(options.methodologyVersion) {
		setup := setupSchedule(experimentID, options.Warmup)
		for _, item := range setup {
			if len(attempts) >= options.MaxAttempts {
				break
			}
			ordinals[item.side]++
			executionOrder++
			attempt, err := runner.runAttempt(ctx, options, manifest, experimentDir, item.side, item.role, 0, ordinals[item.side], executionOrder, cacheControl)
			if err != nil {
				return experimentDir, nil, 2, err
			}
			attempts = append(attempts, attempt)
		}
	}
	for cycle := 1; len(attempts) < options.MaxAttempts; cycle++ {
		valid := CountValidForMethodology(attempts, options.Task.MinimumToolCalls, options.methodologyVersion)
		if valid[Baseline] >= options.TargetValid && valid[Candidate] >= options.TargetValid {
			break
		}
		order := Sides
		if cycle%2 == 0 {
			order = []string{Candidate, Baseline}
		}
		for _, side := range order {
			if len(attempts) >= options.MaxAttempts {
				break
			}
			if valid[side] >= options.TargetValid {
				continue
			}
			ordinals[side]++
			executionOrder++
			role := ""
			if cacheAwareMethodology(options.methodologyVersion) {
				role = MeasuredWarm
			}
			attempt, err := runner.runAttempt(ctx, options, manifest, experimentDir, side, role, cycle, ordinals[side], executionOrder, cacheControl)
			if err != nil {
				return experimentDir, nil, 2, err
			}
			attempts = append(attempts, attempt)
			valid = CountValidForMethodology(attempts, options.Task.MinimumToolCalls, options.methodologyVersion)
		}
	}
	materialized, _, err := materializeAttempts(experimentDir, manifest, attempts)
	if err != nil {
		return experimentDir, nil, 2, err
	}
	pricingEvidence, err := preparePricingEvidence(experimentDir, manifest, materialized, nil)
	if err != nil {
		return experimentDir, nil, 2, err
	}
	attempts = estimateAttempts(materialized, pricingEvidence)
	summary := BuildSummary(manifest, attempts)
	if err := WriteJSON(filepath.Join(experimentDir, "summary.json"), summary); err != nil {
		return experimentDir, nil, 2, err
	}
	valid := CountValidForMethodology(attempts, options.Task.MinimumToolCalls, options.methodologyVersion)
	if valid[Baseline] >= options.TargetValid && valid[Candidate] >= options.TargetValid {
		return experimentDir, summary, 0, nil
	}
	return experimentDir, summary, 1, nil
}

func (runner *Runner) prepareStructuralOptions(options *CompareOptions) error {
	if options.RepoRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		options.RepoRoot = cwd
	}
	var err error
	options.RepoRoot, err = filepath.Abs(options.RepoRoot)
	if err != nil {
		return err
	}
	if options.TaskFile != "" {
		options.Task, err = LoadTask(options.TaskFile)
		if err != nil {
			return err
		}
	}
	if options.Task.ID == "" || options.Task.Prompt == "" {
		return errors.New("task id and prompt are required")
	}
	if options.Task.ExpectedOutput == nil && options.Task.ExpectedFacts == nil {
		return errors.New("task expected output or facts are required")
	}
	if strings.HasPrefix(options.Model, "github-copilot/") {
		return errors.New("github-copilot/* models are not permitted for this repo; use --model openai/gpt-5.6-luna --variant high")
	}
	if options.AttemptTimeout <= 0 {
		options.AttemptTimeout = 10 * time.Minute
	}
	if options.TerminationGrace <= 0 {
		options.TerminationGrace = 30 * time.Second
	}
	if options.DataDir == "" {
		options.DataDir = filepath.Join(options.RepoRoot, ".bench-data")
	}
	options.DataDir, err = filepath.Abs(options.DataDir)
	if err != nil {
		return err
	}
	if options.DataDir == options.RepoRoot {
		return errors.New("experiment data directory must not be the repository root")
	}
	if options.Image == "" {
		options.Image = "ktx-bench"
	}
	if options.MethodologyPath == "" {
		options.MethodologyPath = filepath.Join(options.RepoRoot, "tools", "bench", "methodology-v4-eval-matrix.yaml")
	}
	if options.BaselineConfig == "" {
		options.BaselineConfig = filepath.Join(options.RepoRoot, "tools", "bench", "pipelines", "passthrough.toml")
	}
	if options.CandidateConfig == "" {
		options.CandidateConfig = filepath.Join(options.RepoRoot, "tools", "bench", "pipelines", "conservative-a.toml")
	}
	if options.AuthPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		options.AuthPath = filepath.Join(home, ".local", "share", "opencode", "auth.json")
	}
	for _, target := range []*string{&options.MethodologyPath, &options.BaselineConfig, &options.CandidateConfig, &options.AuthPath} {
		absolute, err := filepath.Abs(*target)
		if err != nil {
			return err
		}
		*target = absolute
	}
	methodology, err := methodologyMetadata(options.MethodologyPath)
	if err != nil {
		return err
	}
	options.methodologyVersion = methodology.Version
	cacheAware := cacheAwareMethodology(methodology.Version)
	if !cacheAware && (options.CacheControl != "" || options.WarmupSet || options.Warmup != 0) {
		return errors.New("--cache-control and --warmup require a cache-aware methodology")
	}
	if options.TargetValid <= 0 {
		if cacheAware {
			options.TargetValid = 5
		} else {
			options.TargetValid = 3
		}
	}
	if cacheAware && !options.WarmupSet && options.Warmup == 0 {
		options.Warmup = 1
	}
	if options.Warmup < 0 {
		return errors.New("warmup must be non-negative")
	}
	setupAttempts := 0
	if cacheAware {
		setupAttempts = 2 + 2*options.Warmup
	}
	if options.MaxAttempts <= 0 {
		if cacheAware {
			options.MaxAttempts = setupAttempts + 4*options.TargetValid
		} else {
			options.MaxAttempts = 12
		}
	}
	if setupAttempts+options.TargetValid*2 > options.MaxAttempts {
		return errors.New("max attempts must allow setup and requested measured completions")
	}
	if options.DockerBin == "" {
		options.DockerBin, err = exec.LookPath("docker")
		if err != nil {
			return errors.New("required executable not found: docker")
		}
	}
	if options.KTXBin == "" {
		options.KTXBin, err = os.Executable()
		if err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) preparePaidRun(options *CompareOptions) error {
	if _, err := os.Stat(options.AuthPath); err != nil {
		return fmt.Errorf("OpenCode auth file not found: %s", options.AuthPath)
	}
	return nil
}

func (runner *Runner) prepareOptions(options *CompareOptions) error {
	if err := runner.prepareStructuralOptions(options); err != nil {
		return err
	}
	return runner.preparePaidRun(options)
}

func (runner *Runner) Preflight(ctx context.Context, options CompareOptions) (PreflightReport, error) {
	if options.RepoRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return PreflightReport{}, err
		}
		options.RepoRoot = cwd
	}
	var err error
	options.RepoRoot, err = filepath.Abs(options.RepoRoot)
	if err != nil {
		return PreflightReport{}, err
	}
	if options.Image == "" {
		options.Image = "ktx-bench"
	}
	if options.DockerBin == "" {
		options.DockerBin, err = exec.LookPath("docker")
		if err != nil {
			return PreflightReport{}, errors.New("required executable not found: docker")
		}
	}
	report, _, err := runner.preflightPrepared(ctx, options)
	return report, err
}

func (runner *Runner) preflightPrepared(ctx context.Context, options CompareOptions) (PreflightReport, benchmarkRuntime, error) {
	report := PreflightReport{Image: options.Image, ModelAttempts: 0, ArtifactCreation: false, Reasons: []string{}}
	if options.BuildImage {
		if err := validateLocalCollectorSource(options.RepoRoot); err != nil {
			reason := "local_source_unavailable"
			if errors.Is(err, errLocalCollectorContract) {
				reason = "local_source_contract_mismatch"
			}
			report.Reasons = append(report.Reasons, reason)
			return report, benchmarkRuntime{}, nil
		}
		diagnostics := runner.diagnosticWriter()
		result, err := runner.Executor.Run(ctx, Command{Args: []string{options.DockerBin, "build", "-f", filepath.Join(options.RepoRoot, "Dockerfile.benchmark"), "-t", options.Image, options.RepoRoot}, Stdout: diagnostics, Stderr: diagnostics})
		if err != nil {
			return report, benchmarkRuntime{}, fmt.Errorf("build benchmark image: %w", err)
		}
		if result.ExitCode != 0 {
			return report, benchmarkRuntime{}, fmt.Errorf("build benchmark image: exit %d", result.ExitCode)
		}
	}
	runtime, capabilities, err := runner.inspectBenchmarkRuntime(ctx, options)
	if err != nil {
		report.Reasons = append(report.Reasons, "image_capability_unavailable")
		return report, benchmarkRuntime{}, nil
	}
	report.KTXVersion, report.OpenCodeVersion, report.PluginSHA256, report.EventCollectorContract = capabilities.KTXVersion, capabilities.OpenCodeVersion, capabilities.GeneratedPluginSHA256, capabilities.EventCollectorContract
	if reason := validateBenchmarkCapabilities(capabilities); reason != "" {
		report.Reasons = append(report.Reasons, reason)
		return report, runtime, nil
	}
	report.Eligible, report.DirectEvidence, report.ControlledComparison, report.SecondaryEvidence = true, true, true, true
	return report, runtime, nil
}

func validateLocalCollectorSource(root string) error {
	checks := map[string][]string{
		"Dockerfile.benchmark":                       {"ARG OPENCODE_VERSION=" + benchcontract.RuntimeVersion, "RUN ktx init opencode", `ENTRYPOINT ["/usr/local/bin/ktx", "bench", "agent"]`},
		filepath.Join("internal", "cli", "init.go"):  {`["telemetry", "collector-open"]`, `["telemetry", "event-record"]`, "eventQueue.then", "next.catch(() => undefined)"},
		filepath.Join("internal", "db", "schema.go"): {"runtime_event_intervals", "runtime_events", "model_step_classification", "contradiction_count"},
	}
	for relative, markers := range checks {
		data, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			return err
		}
		for _, marker := range markers {
			if !strings.Contains(string(data), marker) {
				return fmt.Errorf("%w: %s", errLocalCollectorContract, relative)
			}
		}
	}
	contractPath := filepath.Join("internal", "benchcontract", "contract.go")
	contractSource, err := os.ReadFile(filepath.Join(root, contractPath))
	if err != nil {
		return err
	}
	for _, declaration := range []struct{ identifier, value string }{
		{"ContractVersion", benchcontract.ContractVersion},
		{"CollectorRequired", benchcontract.CollectorRequired},
	} {
		if !hasStringDeclaration(contractSource, declaration.identifier, declaration.value) {
			return fmt.Errorf("%w: %s", errLocalCollectorContract, contractPath)
		}
	}
	return nil
}

func hasStringDeclaration(source []byte, identifier, value string) bool {
	pattern := `(?m)^[\t ]*` + regexp.QuoteMeta(identifier) + `[\t ]*=[\t ]*"` + regexp.QuoteMeta(value) + `"[\t ]*(?://[^\r\n]*)?\r?$`
	return regexp.MustCompile(pattern).Match(source)
}

func snapshotConfigs(experimentDir, baseline, candidate string) (map[string]PipelineMetadata, error) {
	directory := filepath.Join(experimentDir, "configs")
	if err := os.Mkdir(directory, 0o755); err != nil {
		return nil, err
	}
	configs := map[string]PipelineMetadata{}
	for side, source := range map[string]string{Baseline: baseline, Candidate: candidate} {
		absolute, err := filepath.Abs(source)
		if err != nil {
			return nil, err
		}
		destination := filepath.Join(directory, "ktx.toml."+side)
		if err := copyTreeFile(absolute, destination); err != nil {
			return nil, fmt.Errorf("pipeline config for %s: %w", side, err)
		}
		var decoded struct {
			Pipeline map[string]struct {
				Stages []string `toml:"stages"`
			} `toml:"pipeline"`
		}
		if _, err := toml.DecodeFile(destination, &decoded); err != nil {
			return nil, err
		}
		hash, err := fileSHA256(destination)
		if err != nil {
			return nil, err
		}
		pipelineID := side
		if filepath.Base(filepath.Dir(absolute)) == "pipelines" {
			pipelineID = strings.TrimSuffix(filepath.Base(absolute), filepath.Ext(absolute))
		}
		configs[side] = PipelineMetadata{Path: destination, SourcePath: absolute, PipelineID: pipelineID, SHA256: hash, Stages: decoded.Pipeline["default"].Stages}
	}
	return configs, nil
}

type benchmarkRuntime struct {
	ImageID    string
	Identities map[string]string
}

type benchmarkCapabilities struct {
	KTXVersion                   string   `json:"ktx_version"`
	OpenCodeVersion              string   `json:"opencode_version"`
	GeneratedPluginSHA256        string   `json:"generated_plugin_sha256"`
	GeneratedPluginMatchesBinary bool     `json:"generated_plugin_matches_binary"`
	EventCollectorContract       string   `json:"event_collector_contract"`
	ExplicitSessionOwnership     bool     `json:"explicit_session_ownership"`
	SerializedCallbacks          bool     `json:"serialized_callbacks"`
	CompleteIntervalLifecycle    bool     `json:"complete_interval_lifecycle"`
	EpochTracking                bool     `json:"epoch_tracking"`
	StrictAcknowledgements       bool     `json:"strict_acknowledgements"`
	ExactModelStepIdentity       bool     `json:"exact_model_step_identity"`
	RetainedDuplicateCallbacks   bool     `json:"retained_duplicate_callbacks"`
	AcceptedModelStepProjection  bool     `json:"accepted_model_step_projection"`
	CallbackQueueContinuation    bool     `json:"callback_queue_continuation"`
	FailClosedIntervalValidation bool     `json:"fail_closed_interval_validation"`
	PreflightBeforeAuth          bool     `json:"preflight_before_auth"`
	NormalizedKinds              []string `json:"normalized_kinds"`
}

func (runner *Runner) inspectBenchmarkRuntime(ctx context.Context, options CompareOptions) (benchmarkRuntime, benchmarkCapabilities, error) {
	imageID, err := runner.output(ctx, Command{Args: []string{options.DockerBin, "image", "inspect", options.Image, "--format", "{{.Id}}"}})
	if err != nil {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("inspect benchmark image: %w", err)
	}
	capabilityOutput, err := runner.output(ctx, Command{Args: []string{options.DockerBin, "run", "--rm", options.Image, "--capabilities"}})
	if err != nil {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("inspect benchmark capabilities: %w", err)
	}
	var capabilities benchmarkCapabilities
	if err := json.Unmarshal([]byte(capabilityOutput), &capabilities); err != nil {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("decode benchmark capabilities: %w", err)
	}
	identities := normalizeRuntimeIdentities(map[string]string{"ktx": capabilities.KTXVersion, "opencode": capabilities.OpenCodeVersion})
	return benchmarkRuntime{ImageID: strings.TrimSpace(imageID), Identities: identities}, capabilities, nil
}

func validateBenchmarkCapabilities(capabilities benchmarkCapabilities) string {
	if capabilities.OpenCodeVersion != benchcontract.RuntimeVersion || capabilities.EventCollectorContract != benchcontract.ContractVersion {
		return "runtime_or_event_contract_mismatch"
	}
	if capabilities.KTXVersion == "" || len(capabilities.GeneratedPluginSHA256) != 64 || !capabilities.GeneratedPluginMatchesBinary {
		return "binary_or_plugin_identity_missing"
	}
	if !capabilities.ExplicitSessionOwnership || !capabilities.SerializedCallbacks || !capabilities.CompleteIntervalLifecycle || !capabilities.EpochTracking ||
		!capabilities.StrictAcknowledgements || !capabilities.ExactModelStepIdentity || !capabilities.RetainedDuplicateCallbacks ||
		!capabilities.AcceptedModelStepProjection || !capabilities.CallbackQueueContinuation || !capabilities.FailClosedIntervalValidation || !capabilities.PreflightBeforeAuth {
		return "collector_lifecycle_capability_missing"
	}
	want := []string{"collector_open", "model_step", "retry", "compaction", "interruption", "other"}
	if !slices.Equal(capabilities.NormalizedKinds, want) {
		return "normalized_event_kinds_mismatch"
	}
	return ""
}

func (runner *Runner) createManifest(ctx context.Context, options CompareOptions, experimentID string, configs map[string]PipelineMetadata, snapshot SnapshotMetadata, runtime benchmarkRuntime) (Manifest, error) {
	methodology, err := methodologyMetadata(options.MethodologyPath)
	if err != nil {
		return Manifest{}, err
	}
	repository := runner.repositoryMetadata(ctx, options.RepoRoot)
	repository.Snapshot = snapshot
	var variant *string
	if options.Variant != "" {
		v := options.Variant
		variant = &v
	}
	manifest := Manifest{
		SchemaVersion: 1, ExperimentID: experimentID, CreatedAt: runner.timestamp(), Task: options.Task,
		Model: options.Model, Variant: variant, TargetValidPerPipeline: options.TargetValid, MaxAttempts: options.MaxAttempts,
		AttemptTimeout: options.AttemptTimeout.String(), TerminationGrace: options.TerminationGrace.String(),
		PipelineExecutionPolicy: map[string]any{"kind": "alternating_by_cycle", "first_cycle": []string{Baseline, Candidate}, "second_cycle": []string{Candidate, Baseline}},
		Repository:              repository, Image: map[string]string{"name": options.Image, "id": runtime.ImageID},
		Identities: runtime.Identities, Pipelines: configs, Methodology: methodology,
	}
	return manifest, nil
}

func methodologyMetadata(path string) (MethodologyMetadata, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return MethodologyMetadata{}, err
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return MethodologyMetadata{}, err
	}
	version := ""
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "methodology_version" {
			version = strings.Trim(strings.TrimSpace(value), "\"'")
			break
		}
	}
	if version == "" {
		return MethodologyMetadata{}, fmt.Errorf("methodology_version missing from %s", absolute)
	}
	hash := sha256Bytes(data)
	return MethodologyMetadata{Version: version, Path: absolute, SHA256: hash}, nil
}

func sha256Bytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (runner *Runner) repositoryMetadata(ctx context.Context, root string) RepositoryMetadata {
	commit, err := runner.output(ctx, Command{Args: []string{"git", "-C", root, "rev-parse", "HEAD"}})
	if err != nil {
		return RepositoryMetadata{}
	}
	status, err := runner.output(ctx, Command{Args: []string{"git", "-C", root, "status", "--porcelain"}})
	if err != nil {
		return RepositoryMetadata{}
	}
	commit = strings.TrimSpace(commit)
	dirty := strings.TrimSpace(status) != ""
	hash := sha256Bytes([]byte(status))
	return RepositoryMetadata{Commit: &commit, Dirty: &dirty, StatusSHA256: &hash}
}

func (runner *Runner) experimentID(ctx context.Context, options CompareOptions) string {
	suffix := "local"
	if output, err := runner.output(ctx, Command{Args: []string{"git", "-C", options.RepoRoot, "rev-parse", "--short", "HEAD"}}); err == nil {
		suffix = strings.TrimSpace(output)
	}
	return fmt.Sprintf("%s-%s-%d", runner.Now().UTC().Format("20060102T150405Z"), suffix, runner.PID)
}

func (runner *Runner) runAttempt(ctx context.Context, options CompareOptions, manifest Manifest, experimentDir, side, role string, cycle, ordinal, executionOrder int, cacheControl resolvedCacheControl) (Attempt, error) {
	attemptID := fmt.Sprintf("%s-%03d", side, ordinal)
	attemptDir := filepath.Join(experimentDir, "attempts", attemptID)
	if err := os.Mkdir(attemptDir, 0o755); err != nil {
		return Attempt{}, err
	}
	database := filepath.Join(attemptDir, "ktx.db")
	streamPath, stderrPath := filepath.Join(attemptDir, "stream.jsonl"), filepath.Join(attemptDir, "stderr.log")
	workspace := filepath.Join(attemptDir, "workspace")
	if err := os.WriteFile(database, nil, 0o644); err != nil {
		return Attempt{}, err
	}
	startedAt := runner.timestamp()
	attemptDB, err := db.Open(database)
	if err != nil {
		return Attempt{}, fmt.Errorf("initialize attempt database: %w", err)
	}
	interval, err := attemptDB.CreateBenchmarkEventInterval(attemptID, benchcontract.Runtime, benchcontract.RuntimeVersion, benchcontract.ContractVersion, startedAt)
	closeErr := attemptDB.Close()
	if err != nil {
		return Attempt{}, err
	}
	if closeErr != nil {
		return Attempt{}, closeErr
	}
	repositoryRoot := filepath.Join(experimentDir, manifest.Repository.Snapshot.Path)
	if _, err := prepareWorkspace(repositoryRoot, workspace, manifest.Task.WorkspaceMode, manifest.Task.CorpusFiles); err != nil {
		return Attempt{}, err
	}
	before, err := runner.listSessions(ctx, options, database)
	if err != nil {
		return Attempt{}, err
	}
	if len(before) != 1 || before[0]["id"] != interval.SessionID {
		return Attempt{}, fmt.Errorf("attempt %q precreated owner is not the database singleton", attemptID)
	}
	beforePath, afterPath := filepath.Join(attemptDir, "sessions-before.json"), filepath.Join(attemptDir, "sessions-after.json")
	if err := WriteJSON(beforePath, before); err != nil {
		return Attempt{}, err
	}
	stream, err := os.Create(streamPath)
	if err != nil {
		return Attempt{}, err
	}
	stderr, err := os.Create(stderrPath)
	if err != nil {
		stream.Close()
		return Attempt{}, err
	}
	stages, _ := json.Marshal(manifest.Pipelines[side].Stages)
	containerName := sanitizeContainerName("ktx-" + manifest.ExperimentID + "-" + attemptID)
	command := []string{
		options.DockerBin, "run", "--rm", "--name", containerName,
		"-e", "KTX_EXPECTED_STAGES_JSON=" + string(stages),
		"-e", benchcontract.SessionEnv + "=" + interval.SessionID,
		"-e", benchcontract.IntervalEnv + "=" + interval.ID,
		"-e", benchcontract.ContractEnv + "=" + benchcontract.ContractVersion,
		"-e", benchcontract.CollectorModeEnv + "=" + benchcontract.CollectorRequired,
	}
	if role != "" {
		command = append(command, "-e", "KTX_BENCH_CACHE_PHASE="+role)
	}
	if cacheControl.mode == "isolated" {
		provider, model, _ := splitModel(options.Model)
		content, err := opencodeConfigContent(provider, model, cacheControl.raw[side])
		if err != nil {
			return Attempt{}, err
		}
		command = append(command, "-e", "OPENCODE_CONFIG_CONTENT="+content)
	}
	command = append(command,
		"-v", options.AuthPath+":/home/node/.local/share/opencode/auth.json:ro",
		"-v", manifest.Pipelines[side].Path+":/home/node/.config/ktx/ktx.toml:ro",
		"-v", database+":/home/node/.local/share/ktx/ktx.db:rw",
		"-v", workspace+":/work:rw", options.Image, "-m", options.Model,
	)
	if options.Variant != "" {
		command = append(command, "--variant", options.Variant)
	}
	command = append(command, manifest.Task.Prompt)
	result, runErr := runner.Executor.Run(ctx, Command{Args: command, Stdout: stream, Stderr: stderr, Timeout: options.AttemptTimeout, Grace: options.TerminationGrace})
	streamErr, stderrErr := stream.Close(), stderr.Close()
	// A killed Docker client can leave the container alive. Cleanup is
	// idempotent and runs after every attempt.
	_, _ = runner.Executor.Run(context.Background(), Command{Args: []string{options.DockerBin, "rm", "-f", containerName}, Stdout: io.Discard, Stderr: io.Discard, Timeout: 30 * time.Second, Grace: time.Second})
	if runErr != nil {
		return Attempt{}, runErr
	}
	if streamErr != nil {
		return Attempt{}, streamErr
	}
	if stderrErr != nil {
		return Attempt{}, stderrErr
	}
	endedAt := runner.timestamp()
	after, err := runner.listSessions(ctx, options, database)
	if err != nil {
		return Attempt{}, err
	}
	if err := WriteJSON(afterPath, after); err != nil {
		return Attempt{}, err
	}
	if err := validateAttemptDatabase(database, attemptID, interval.SessionID, interval.ID); err != nil {
		return Attempt{}, err
	}
	newIDs := []string{interval.SessionID}
	metrics := ParseStream(streamPath, manifest.Task.ExpectedOutput, manifest.Task.ExpectedFacts)
	if cacheAwareMethodology(options.methodologyVersion) {
		metrics = parseSchema3Stream(streamPath, manifest.Task.ExpectedOutput, manifest.Task.ExpectedFacts)
	}
	metrics.ToolCallCount, err = attemptToolCallCount(database, interval.SessionID)
	if err != nil {
		return Attempt{}, err
	}
	// Ordered model-step evidence is an additive cache-aware attempt contract.
	// Keep cache-less attempts on their existing serialized schema.
	if !cacheAwareMethodology(options.methodologyVersion) {
		metrics.ModelSteps = nil
	}
	pricing, err := loadPricingSnapshot(experimentDir, manifest.Pricing)
	if err != nil {
		return Attempt{}, err
	}
	estimated := estimateAttempts([]Attempt{{Metrics: metrics}}, pricing)
	metrics.EstimatedAPICost = estimated[0].EstimatedAPICost
	status := AttemptStatus(result.ExitCode, newIDs, metrics)
	sessionID := &interval.SessionID
	links := []RecordedStreamLink{}
	if sessionID != nil {
		links, _ = recordedStreamLinks(database, *sessionID)
	}
	var streamID, benchRunID *string
	if len(links) == 1 {
		streamID = &links[0].ID
		benchRunID = links[0].BenchRunID
	}
	workspaceHash, _, err := directorySHA256(workspace)
	if err != nil {
		return Attempt{}, err
	}
	streamHash, err := fileSHA256(streamPath)
	if err != nil {
		return Attempt{}, err
	}
	stderrHash, err := fileSHA256(stderrPath)
	if err != nil {
		return Attempt{}, err
	}
	artifacts := map[string]string{
		"database": relativePath(experimentDir, database), "stream": relativePath(experimentDir, streamPath),
		"stderr": relativePath(experimentDir, stderrPath), "workspace": relativePath(experimentDir, workspace),
		"sessions_before": relativePath(experimentDir, beforePath), "sessions_after": relativePath(experimentDir, afterPath),
	}
	schemaVersion := 1
	if cacheAwareMethodology(options.methodologyVersion) {
		schemaVersion = 3
	}
	attempt := Attempt{
		SchemaVersion: schemaVersion, ExperimentID: manifest.ExperimentID, AttemptID: attemptID, Side: side,
		Role:       role,
		PipelineID: manifest.Pipelines[side].PipelineID, Cycle: cycle, Ordinal: ordinal,
		StartedAt: startedAt, EndedAt: endedAt, Status: status, ProcessExitCode: result.ExitCode,
		SessionLink: SessionLink{State: map[bool]string{true: "linked", false: "unlinked"}[sessionID != nil], NewSessionCount: len(newIDs), NewSessionIDs: newIDs},
		SessionID:   sessionID, RuntimeEventIntervalID: &interval.ID, RecordedStreams: links, RecordedStreamID: streamID, BenchRunID: benchRunID,
		Metrics: metrics, ConfigSHA256: manifest.Pipelines[side].SHA256, Artifacts: artifacts,
		ContentHashes: map[string]string{"stream": streamHash, "stderr": stderrHash, "workspace": workspaceHash},
	}
	if role != "" {
		provider, _, _ := splitModel(options.Model)
		attempt.CacheControl = &CacheControlMetadata{Mode: cacheControl.mode, Source: cacheControl.source, Phase: role, ExecutionOrder: executionOrder, Model: options.Model, Provider: provider, Variant: options.Variant, RuntimeVersion: manifest.Identities["opencode"]}
		if cacheControl.mode == "isolated" {
			attempt.CacheControl.NamespaceSHA256 = cacheControl.hashes[side]
		}
	}
	if err := AppendAttempt(experimentDir, attempt); err != nil {
		return Attempt{}, err
	}
	return attempt, nil
}

type setupAttempt struct{ side, role string }

func setupSchedule(experimentID string, warmups int) []setupAttempt {
	digest := sha256.Sum256([]byte(experimentID))
	cold := []string{Baseline, Candidate}
	if digest[0]&1 == 1 {
		cold = []string{Candidate, Baseline}
	}
	result := []setupAttempt{{cold[0], ColdObservation}, {cold[1], ColdObservation}}
	order := []string{cold[1], cold[0]}
	for i := 0; i < warmups; i++ {
		result = append(result, setupAttempt{order[0], Warmup}, setupAttempt{order[1], Warmup})
		order[0], order[1] = order[1], order[0]
	}
	return result
}

func (runner *Runner) listSessions(ctx context.Context, options CompareOptions, database string) ([]map[string]any, error) {
	output, err := runner.output(ctx, Command{Args: []string{options.KTXBin, "list", "--format", "json"}, Env: commandEnvironment(map[string]string{"KTX_DB": database})})
	if err != nil {
		return nil, err
	}
	var sessions []map[string]any
	if err := json.Unmarshal([]byte(output), &sessions); err != nil {
		return nil, fmt.Errorf("ktx list did not return a JSON array: %w", err)
	}
	return sessions, nil
}

func recordedStreamLinks(database, sessionID string) ([]RecordedStreamLink, error) {
	databaseHandle, err := db.OpenReadOnly(database)
	if err != nil {
		return nil, err
	}
	defer databaseHandle.Close()
	rows, err := databaseHandle.DB().Query("SELECT id, bench_run_id FROM recorded_streams WHERE session_id = ? ORDER BY id", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []RecordedStreamLink
	for rows.Next() {
		var id string
		var bench sql.NullString
		if err := rows.Scan(&id, &bench); err != nil {
			return nil, err
		}
		link := RecordedStreamLink{ID: id}
		if bench.Valid {
			link.BenchRunID = &bench.String
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func validateAttemptDatabase(path, attemptID, sessionID, intervalID string) error {
	database, err := db.OpenReadOnly(path)
	if err != nil {
		return fmt.Errorf("validate attempt database: %w", err)
	}
	defer database.Close()
	var count int
	if err := database.DB().QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("attempt %q database contains %d sessions; want the explicit singleton owner", attemptID, count)
	}
	var owner string
	var ended sql.NullString
	if err := database.DB().QueryRow(`SELECT id, ended_at FROM sessions`).Scan(&owner, &ended); err != nil {
		return err
	}
	if owner != sessionID || !ended.Valid || ended.String == "" {
		return fmt.Errorf("attempt %q explicit owner was replaced or left open", attemptID)
	}
	var gotInterval, gotAttempt, gotOwner, state string
	if err := database.DB().QueryRow(`SELECT id, attempt_id, session_id, close_state FROM runtime_event_intervals`).Scan(&gotInterval, &gotAttempt, &gotOwner, &state); err != nil {
		return fmt.Errorf("attempt %q interval unavailable: %w", attemptID, err)
	}
	if gotInterval != intervalID || gotAttempt != attemptID || gotOwner != sessionID || state != db.RuntimeComplete {
		return fmt.Errorf("attempt %q interval ownership or completion mismatch", attemptID)
	}
	return nil
}

func attemptToolCallCount(path, sessionID string) (int, error) {
	database, err := db.OpenReadOnly(path)
	if err != nil {
		return 0, err
	}
	defer database.Close()
	var count int
	if err := database.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func sanitizeContainerName(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("_.-", char) {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	if builder.Len() > 120 {
		return builder.String()[:120]
	}
	return builder.String()
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}

func (runner *Runner) output(ctx context.Context, command Command) (string, error) {
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	result, err := runner.Executor.Run(ctx, command)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("command failed (%d): %s: %s", result.ExitCode, strings.Join(command.Args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (runner *Runner) diagnosticWriter() io.Writer {
	if runner.Diagnostics == nil {
		return io.Discard
	}
	return runner.Diagnostics
}

func (runner *Runner) timestamp() string {
	return runner.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}
