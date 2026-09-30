package experiment

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"github.com/jgabor/ktx/internal/benchregistry"
	"github.com/jgabor/ktx/internal/db"
)

type CompareOptions struct {
	Model                    string
	Variant                  string
	TaskFile                 string
	Task                     Task
	TargetValid              int
	MaxAttempts              int
	AttemptTimeout           time.Duration
	TerminationGrace         time.Duration
	ExperimentID             string
	DataDir                  string
	Image                    string
	AuthPath                 string
	RepoRoot                 string
	MethodologyPath          string
	BaselineConfig           string
	CandidateConfig          string
	KTXBin                   string
	DockerBin                string
	BuildImage               bool
	CacheControl             string
	PricingSnapshot          string
	PricingSHA256            string
	RuntimeConfig            string
	ReadDescriptionTreatment string
	FirstArm                 string
	EstimatedCostLimit       *float64
	Warmup                   int
	WarmupSet                bool
	RegisteredInputs         bool
	SpecID                   string
	TaskID                   string
	BaselineID               string
	CandidateID              string
	ContractID               string
	ContractPath             string
	methodologyVersion       string
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
	Model                  string   `json:"model,omitempty"`
	Variant                string   `json:"variant,omitempty"`
	KTXVersion             string   `json:"ktx_version,omitempty"`
	OpenCodeVersion        string   `json:"opencode_version,omitempty"`
	PluginSHA256           string   `json:"generated_plugin_sha256,omitempty"`
	EventCollectorContract string   `json:"event_collector_contract,omitempty"`
	PricingSHA256          string   `json:"pricing_sha256,omitempty"`
	Reasons                []string `json:"reasons"`
}

var errLocalCollectorContract = errors.New("local collector source contract mismatch")

func NewRunner() *Runner {
	return &Runner{Executor: OSExecutor{}, Diagnostics: os.Stderr, Now: time.Now, PID: os.Getpid(), ResolvePrice: db.ResolvePrice}
}

func (runner *Runner) Compare(ctx context.Context, options CompareOptions) (string, Summary, int, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, 2, err
	}
	if err := runner.prepareStructuralOptions(&options); err != nil {
		return "", nil, 2, err
	}
	experimentID := options.ExperimentID
	if experimentID == "" {
		experimentID = runner.experimentID(ctx, options)
	}
	if err := ValidateExperimentID(experimentID); err != nil {
		return "", nil, 2, err
	}
	experimentDir := filepath.Join(options.DataDir, experimentID)
	if _, err := os.Lstat(experimentDir); err == nil {
		return "", nil, 2, fmt.Errorf("experiment id already exists: %s", experimentID)
	} else if !errors.Is(err, os.ErrNotExist) {
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
	runtimeAuthDir := ""
	if options.RuntimeConfig != "" {
		authPath, err := stageRuntimeConfigAuth(options.AuthPath)
		if err != nil {
			return "", nil, 2, err
		}
		runtimeAuthDir = filepath.Dir(authPath)
		defer func() {
			if err := os.RemoveAll(runtimeAuthDir); err != nil && runner.Diagnostics != nil {
				fmt.Fprintf(runner.Diagnostics, "warning: remove temporary OpenCode auth: %v\n", err)
			}
		}()
		options.AuthPath = authPath
	}
	if err := os.MkdirAll(options.DataDir, 0o755); err != nil {
		return "", nil, 2, err
	}
	cacheControl := resolvedCacheControl{}
	if cacheAwareMethodology(options.methodologyVersion) {
		request := CacheCapabilityRequest{Runtime: "opencode", RuntimeVersion: runtime.Identities["opencode"], Provider: provider, Model: model}
		cacheControl, err = resolveCacheControl(options.CacheControl, experimentID, request, runner.ResolveCacheCapability)
		if err != nil {
			return "", nil, 2, err
		}
		if options.RuntimeConfig != "" && cacheControl.mode == "isolated" {
			return "", nil, 2, errors.New("isolated cache configuration conflicts with explicit runtime configuration; use --cache-control observed")
		}
	}
	resolvePrice := runner.ResolvePrice
	if resolvePrice == nil {
		resolvePrice = db.ResolvePrice
	}
	var price db.Price
	if runtime.Pricing == nil {
		price, err = resolvePrice(provider, model)
		if err != nil {
			return "", nil, 2, err
		}
	}
	if err := os.Mkdir(experimentDir, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", nil, 2, fmt.Errorf("experiment directory already exists: %s", experimentDir)
		}
		return "", nil, 2, err
	}
	if err := os.Mkdir(filepath.Join(experimentDir, "attempts"), 0o755); err != nil {
		return "", nil, 2, err
	}
	var pricing *PricingSnapshotMetadata
	if runtime.Pricing != nil {
		pricing, err = writePinnedPricingSnapshot(experimentDir, runtime.Pricing)
	} else {
		pricing, err = writePricingSnapshot(experimentDir, provider, model, price)
	}
	if err != nil {
		return "", nil, 2, err
	}
	repositoryDir := filepath.Join(experimentDir, "repository")
	dataRelative, _ := filepath.Rel(options.RepoRoot, options.DataDir)
	dataRelative = filepath.ToSlash(dataRelative)
	if err := copyTree(options.RepoRoot, repositoryDir, func(relative string, entry os.DirEntry) bool {
		base := filepath.Base(relative)
		insideDataDir := dataRelative != "." && dataRelative != "" && dataRelative != ".." && !strings.HasPrefix(dataRelative, "../") && (relative == dataRelative || strings.HasPrefix(relative, dataRelative+"/"))
		// TMPDIR may be inside (or symlink into) the repository. Exclude the
		// uniquely named staging directory regardless of its parent spelling.
		if runtimeAuthDir != "" && base == filepath.Base(runtimeAuthDir) {
			return true
		}
		return base == ".git" || base == ".ktx" || base == "__pycache__" || insideDataDir || (options.RuntimeConfig != "" && base == ".opencode")
	}); err != nil {
		return "", nil, 2, fmt.Errorf("snapshot repository: %w", err)
	}
	if options.RuntimeConfig != "" {
		if err := rejectProjectRuntimeConfig(repositoryDir); err != nil {
			return "", nil, 2, err
		}
	}
	repositoryHash, repositoryFiles, err := directorySHA256(repositoryDir)
	if err != nil {
		return "", nil, 2, err
	}
	configs, err := snapshotConfigs(experimentDir, options.BaselineConfig, options.CandidateConfig)
	if err != nil {
		return "", nil, 2, err
	}
	inputs, err := snapshotInputs(experimentDir, options)
	if err != nil {
		return "", nil, 2, err
	}
	runtimeConfig, err := captureRuntimeConfig(experimentDir, options.RuntimeConfig, options.Model, options.Variant)
	if err != nil {
		return "", nil, 2, err
	}
	if runtimeConfig != nil {
		inputs = append(inputs, *runtimeConfig)
	}
	manifest, err := runner.createManifest(ctx, options, experimentID, configs, SnapshotMetadata{Path: "repository", SHA256: repositoryHash, FileCount: repositoryFiles}, runtime)
	if err != nil {
		return "", nil, 2, err
	}
	manifest.Pricing = pricing
	manifest.Inputs = inputs
	manifest.RuntimeConfig = runtimeConfig
	manifest.ReadDescription, err = captureReadDescription(experimentDir, options)
	if err != nil {
		return experimentDir, nil, 2, err
	}
	if manifest.ReadDescription != nil {
		manifest.Inputs = append(manifest.Inputs, manifest.ReadDescription.Original, manifest.ReadDescription.Candidate)
	}
	if cacheAwareMethodology(options.methodologyVersion) {
		manifest.CacheControl = &CacheControlManifest{Mode: cacheControl.mode, Source: cacheControl.source, NamespaceSHA256: cacheControl.hashes, WarmupCount: options.Warmup, IdleGapMS: 0, Model: options.Model, Provider: provider, Variant: options.Variant, RuntimeVersion: manifest.Identities["opencode"], TTFTAvailable: false, TTFTUnavailableReason: "retained OpenCode stream timestamps do not provide a reliable request start"}
	}
	if err := WriteJSON(filepath.Join(experimentDir, "manifest.json"), manifest); err != nil {
		return "", nil, 2, err
	}
	if options.EstimatedCostLimit != nil {
		if _, err := runner.costCheckpoint(experimentDir, manifest, 0); err != nil {
			return experimentDir, nil, 2, err
		}
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
	var executionStop *ExecutionStop
measured:
	for cycle := 1; len(attempts) < options.MaxAttempts; cycle++ {
		valid := CountValidForMethodology(attempts, options.Task.MinimumToolCalls, options.methodologyVersion)
		if valid[Baseline] >= options.TargetValid && valid[Candidate] >= options.TargetValid {
			break
		}
		order := Sides
		if (cycle%2 == 0) != (options.FirstArm == Candidate) {
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
				if options.EstimatedCostLimit != nil {
					stopErr := retainExecutionStop(experimentDir, &ExecutionStop{Reason: "evidence_invalid", Detail: err.Error(), AfterAttemptID: fmt.Sprintf("%s-%03d", side, ordinals[side])})
					if stopErr != nil {
						return experimentDir, nil, 2, errors.Join(err, stopErr)
					}
				}
				return experimentDir, nil, 2, err
			}
			attempts = append(attempts, attempt)
			if options.EstimatedCostLimit != nil {
				executionStop, err = runner.costCheckpoint(experimentDir, manifest, len(attempts))
				if err != nil {
					return experimentDir, nil, 2, err
				}
				if executionStop != nil {
					break measured
				}
			}
			valid = CountValidForMethodology(attempts, options.Task.MinimumToolCalls, options.methodologyVersion)
		}
	}
	materialized, _, err := materializeAttempts(experimentDir, manifest, attempts)
	if err != nil {
		return experimentDir, nil, 2, err
	}
	pricingEvidence, err := preparePricingEvidence(experimentDir, manifest)
	if err != nil {
		return experimentDir, nil, 2, err
	}
	attempts = estimateAttempts(materialized, pricingEvidence)
	summary := BuildSummary(manifest, attempts)
	if err := WriteJSON(filepath.Join(experimentDir, "summary.json"), summary); err != nil {
		return experimentDir, nil, 2, err
	}
	valid := CountValidForMethodology(attempts, options.Task.MinimumToolCalls, options.methodologyVersion)
	if valid[Baseline] >= options.TargetValid && valid[Candidate] >= options.TargetValid && (executionStop == nil || executionStop.Reason == "estimated_cost_limit_reached") {
		return experimentDir, summary, 0, nil
	}
	return experimentDir, summary, 1, nil
}

func (runner *Runner) prepareStructuralOptions(options *CompareOptions) error {
	if err := validateExecutionControlOptions(*options); err != nil {
		return err
	}
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
		options.DataDir = filepath.Join(options.RepoRoot, ".ktx", "experiments")
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
	if options.RegisteredInputs {
		registry := benchregistry.Active()
		if options.MethodologyPath == "" {
			return errors.New("--spec is required; use a registered benchmark spec ID")
		}
		if options.BaselineConfig == "" {
			options.BaselineConfig = "passthrough"
		}
		if options.CandidateConfig == "" {
			options.CandidateConfig = "conservative-a"
		}
		lookups := []struct {
			kind       benchregistry.Kind
			selector   string
			path       *string
			resolvedID *string
		}{{benchregistry.Spec, options.MethodologyPath, &options.MethodologyPath, &options.SpecID}, {benchregistry.Pipeline, options.BaselineConfig, &options.BaselineConfig, &options.BaselineID}, {benchregistry.Pipeline, options.CandidateConfig, &options.CandidateConfig, &options.CandidateID}}
		if options.TaskFile != "" {
			lookups = append(lookups, struct {
				kind       benchregistry.Kind
				selector   string
				path       *string
				resolvedID *string
			}{benchregistry.Task, options.TaskFile, &options.TaskFile, &options.TaskID})
		}
		for _, lookup := range lookups {
			entry, err := registry.Lookup(lookup.kind, lookup.selector)
			if err != nil {
				return err
			}
			*lookup.resolvedID = entry.ID
			*lookup.path = filepath.Join(options.RepoRoot, filepath.FromSlash(entry.Path))
		}
		if options.SpecID == "prompt-cache-token-hit-rate" {
			contract, err := registry.Lookup(benchregistry.Contract, "cache-reuse")
			if err != nil {
				return err
			}
			options.ContractID = contract.ID
			options.ContractPath = filepath.Join(options.RepoRoot, filepath.FromSlash(contract.Path))
		}
	} else {
		if options.MethodologyPath == "" {
			options.MethodologyPath = filepath.Join(options.RepoRoot, "tools", "bench", "specs", "eval-matrix.yaml")
		}
		if options.BaselineConfig == "" {
			options.BaselineConfig = filepath.Join(options.RepoRoot, "tools", "bench", "pipelines", "passthrough.toml")
		}
		if options.CandidateConfig == "" {
			options.CandidateConfig = filepath.Join(options.RepoRoot, "tools", "bench", "pipelines", "conservative-a.toml")
		}
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
	if err := validateMethodologyVersion(methodology.Version); err != nil {
		return err
	}
	cacheAware := cacheAwareMethodology(methodology.Version)
	if !cacheAware && (options.CacheControl != "" || options.WarmupSet || options.Warmup != 0) && options.ReadDescriptionTreatment == "" {
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
	return validateReadDescriptionOptions(*options)
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
	if err := runner.prepareStructuralOptions(&options); err != nil {
		return PreflightReport{}, err
	}
	var err error
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
	report := PreflightReport{Image: options.Image, Model: options.Model, Variant: options.Variant, ModelAttempts: 0, ArtifactCreation: false, Reasons: []string{}}
	if options.RuntimeConfig != "" {
		data, err := os.ReadFile(options.RuntimeConfig)
		if err != nil || validateRuntimeConfig(data, options.Model, options.Variant) != nil {
			report.Reasons = append(report.Reasons, "runtime_config_invalid")
			return report, benchmarkRuntime{}, nil
		}
		if err := rejectProjectRuntimeConfig(options.RepoRoot); err != nil {
			report.Reasons = append(report.Reasons, "project_runtime_config_conflict")
			return report, benchmarkRuntime{}, nil
		}
	}
	var pinned *pinnedPricingSnapshot
	if options.PricingSnapshot != "" {
		for _, path := range []string{options.BaselineConfig, options.CandidateConfig} {
			var config map[string]any
			if _, err := toml.DecodeFile(path, &config); err != nil {
				report.Reasons = append(report.Reasons, "pipeline_config_invalid")
				return report, benchmarkRuntime{}, nil
			}
		}
		var err error
		pinned, err = loadPinnedPricingSnapshot(options.PricingSnapshot, options.PricingSHA256, options.Model)
		if err != nil {
			report.Reasons = append(report.Reasons, "pricing_snapshot_mismatch")
			return report, benchmarkRuntime{}, nil
		}
		report.PricingSHA256 = pinned.SHA256
	}
	if options.BuildImage {
		if err := validateLocalCollectorSource(options.RepoRoot); err != nil {
			reason := "local_source_unavailable"
			if errors.Is(err, errLocalCollectorContract) {
				reason = "local_source_contract_mismatch"
			}
			report.Reasons = append(report.Reasons, reason)
			return report, benchmarkRuntime{}, nil
		}
		diagnostics := newDiagnosticLimit(runner.diagnosticWriter())
		result, err := runner.Executor.Run(ctx, Command{Args: []string{options.DockerBin, "build", "-f", filepath.Join(options.RepoRoot, "Dockerfile.benchmark"), "-t", options.Image, options.RepoRoot}, Stdout: diagnostics, Stderr: diagnostics, Timeout: options.AttemptTimeout, Grace: options.TerminationGrace})
		if err != nil {
			return report, benchmarkRuntime{}, fmt.Errorf("build benchmark image: %w", err)
		}
		if result.ExitCode != 0 {
			return report, benchmarkRuntime{}, fmt.Errorf("build benchmark image: exit %d", result.ExitCode)
		}
	}
	runtime, capabilities, err := runner.inspectBenchmarkRuntime(ctx, options)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, errContainerCleanup) || errors.Is(err, errInspectionResponseSize) {
			return report, benchmarkRuntime{}, err
		}
		report.Reasons = append(report.Reasons, "image_capability_unavailable")
		return report, benchmarkRuntime{}, nil
	}
	report.KTXVersion, report.OpenCodeVersion, report.PluginSHA256, report.EventCollectorContract = capabilities.KTXVersion, capabilities.OpenCodeVersion, capabilities.GeneratedPluginSHA256, capabilities.EventCollectorContract
	if reason := validateBenchmarkCapabilities(capabilities); reason != "" {
		report.Reasons = append(report.Reasons, reason)
		return report, runtime, nil
	}
	if options.ReadDescriptionTreatment != "" && (capabilities.ReadDescriptionTreatment != options.ReadDescriptionTreatment || !validSHA256(capabilities.BinarySHA256)) {
		report.Reasons = append(report.Reasons, "read_description_hook_unavailable")
		return report, runtime, nil
	}
	runtime.Pricing = pinned
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
	type capture struct {
		data []byte
		mode os.FileMode
	}
	captured := map[string]capture{}
	for side, source := range map[string]string{Baseline: baseline, Candidate: candidate} {
		absolute, err := filepath.Abs(source)
		if err != nil {
			return nil, err
		}
		destination := filepath.Join(directory, "ktx.toml."+side)
		config, ok := captured[absolute]
		if !ok {
			info, err := os.Stat(absolute)
			if err != nil {
				return nil, fmt.Errorf("pipeline config for %s: %w", side, err)
			}
			config.mode = info.Mode().Perm()
			config.data, err = os.ReadFile(absolute)
			if err != nil {
				return nil, fmt.Errorf("pipeline config for %s: %w", side, err)
			}
			captured[absolute] = config
		}
		if err := os.WriteFile(destination, config.data, config.mode); err != nil {
			return nil, fmt.Errorf("pipeline config for %s: %w", side, err)
		}
		var decoded struct {
			Pipeline map[string]struct {
				Stages []string `toml:"stages"`
			} `toml:"pipeline"`
		}
		if _, err := toml.Decode(string(config.data), &decoded); err != nil {
			return nil, err
		}
		digest := sha256.Sum256(config.data)
		hash := hex.EncodeToString(digest[:])
		pipelineID := side
		if filepath.Base(filepath.Dir(absolute)) == "pipelines" {
			pipelineID = strings.TrimSuffix(filepath.Base(absolute), filepath.Ext(absolute))
		}
		configs[side] = PipelineMetadata{Path: filepath.ToSlash(filepath.Join("configs", "ktx.toml."+side)), PipelineID: pipelineID, SHA256: hash, Stages: decoded.Pipeline["default"].Stages}
	}
	return configs, nil
}

func snapshotInputs(experimentDir string, options CompareOptions) ([]InputMetadata, error) {
	if !options.RegisteredInputs {
		return nil, nil
	}
	type source struct{ kind, id, format, path string }
	// Pipeline inputs are copies of the execution capture, never a second
	// snapshot of the mutable source configuration.
	sources := []source{{"spec", options.SpecID, "yaml", options.MethodologyPath}, {"pipeline", options.BaselineID, "toml", filepath.Join(experimentDir, "configs", "ktx.toml."+Baseline)}, {"pipeline", options.CandidateID, "toml", filepath.Join(experimentDir, "configs", "ktx.toml."+Candidate)}}
	if options.TaskFile != "" {
		sources = append(sources, source{"task", options.TaskID, "toml", options.TaskFile})
	}
	if options.ContractPath != "" {
		sources = append(sources, source{"contract", options.ContractID, "json", options.ContractPath})
	}
	result := make([]InputMetadata, 0, len(sources))
	seen := make(map[string]bool)
	for _, input := range sources {
		key := input.kind + "\x00" + input.id
		if seen[key] {
			continue
		}
		seen[key] = true
		relative := filepath.ToSlash(filepath.Join("inputs", input.kind, input.id+"."+input.format))
		destination := filepath.Join(experimentDir, filepath.FromSlash(relative))
		if err := copyTreeFile(input.path, destination); err != nil {
			return nil, err
		}
		hash, err := fileSHA256(destination)
		if err != nil {
			return nil, err
		}
		result = append(result, InputMetadata{ID: input.id, Kind: input.kind, Format: input.format, Path: relative, SHA256: hash})
	}
	return result, nil
}

type benchmarkRuntime struct {
	ImageID    string
	Identities map[string]string
	Pricing    *pinnedPricingSnapshot
}

type benchmarkCapabilities struct {
	BinarySHA256                 string   `json:"ktx_binary_sha256"`
	ReadDescriptionTreatment     string   `json:"read_description_treatment"`
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
	imageCommand := Command{Args: []string{options.DockerBin, "image", "inspect", options.Image, "--format", "{{.Id}}"}, Timeout: options.AttemptTimeout, Grace: options.TerminationGrace}
	if imageCommand.Timeout <= 0 {
		imageCommand.Timeout = 10 * time.Minute
	}
	if imageCommand.Grace <= 0 {
		imageCommand.Grace = 30 * time.Second
	}
	var imageStderr bytes.Buffer
	imageCommand.Stderr = newDiagnosticLimit(&imageStderr)
	imageID, result, err := inspectResponse(ctx, imageCommand, runner.Executor.Run)
	if err != nil {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("inspect benchmark image: %w", err)
	}
	if result.ExitCode != 0 {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("inspect benchmark image: command failed (%d): %s: %s", result.ExitCode, strings.Join(imageCommand.Args, " "), strings.TrimSpace(imageStderr.String()))
	}
	identity := strings.TrimSpace(string(imageID))
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(identity) {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("inspect benchmark image: missing or invalid full immutable image identity")
	}
	var capabilityStderr bytes.Buffer
	capabilityOutput, result, err := inspectResponse(ctx, Command{Args: []string{options.DockerBin, "run", identity, "--capabilities"}, Stderr: newDiagnosticLimit(&capabilityStderr), Timeout: options.AttemptTimeout, Grace: options.TerminationGrace}, func(ctx context.Context, command Command) (CommandResult, error) {
		return runner.runContainer(ctx, "ktx-capabilities", command)
	})
	if err != nil {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("inspect benchmark capabilities: %w", err)
	}
	if result.ExitCode != 0 {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("inspect benchmark capabilities: exit %d: %s", result.ExitCode, strings.TrimSpace(capabilityStderr.String()))
	}
	var capabilities benchmarkCapabilities
	if err := json.Unmarshal(capabilityOutput, &capabilities); err != nil {
		return benchmarkRuntime{}, benchmarkCapabilities{}, fmt.Errorf("decode benchmark capabilities: %w", err)
	}
	identities := normalizeRuntimeIdentities(map[string]string{"ktx": capabilities.KTXVersion, "opencode": capabilities.OpenCodeVersion})
	if options.ReadDescriptionTreatment != "" {
		identities["generated_plugin_sha256"] = capabilities.GeneratedPluginSHA256
		identities["ktx_binary_sha256"] = capabilities.BinarySHA256
	}
	return benchmarkRuntime{ImageID: identity, Identities: identities}, capabilities, nil
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
	if options.RegisteredInputs {
		methodology.ID = options.SpecID
		methodology.Path = filepath.ToSlash(filepath.Join("inputs", "spec", options.SpecID+".yaml"))
		options.Task.TaskFile = ""
		for side, id := range map[string]string{Baseline: options.BaselineID, Candidate: options.CandidateID} {
			pipeline := configs[side]
			pipeline.PipelineID = id
			configs[side] = pipeline
		}
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
	if options.FirstArm != "" || options.EstimatedCostLimit != nil {
		manifest.FirstArm = options.FirstArm
		if manifest.FirstArm == "" {
			manifest.FirstArm = Baseline
		}
		manifest.EstimatedCostLimit = options.EstimatedCostLimit
		first, second := pairOrder(manifest.FirstArm)
		manifest.PipelineExecutionPolicy["first_cycle"] = first
		manifest.PipelineExecutionPolicy["second_cycle"] = second
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
	if err := ctx.Err(); err != nil {
		return Attempt{}, err
	}
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
	if err == nil {
		_, err = attemptDB.EnsureCollectionInterval(interval.ID, []string{"run"}, startedAt)
	}
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
	command := []string{
		options.DockerBin, "run",
		"-e", "KTX_EXPECTED_STAGES_JSON=" + string(stages),
		"-e", benchcontract.SessionEnv + "=" + interval.SessionID,
		"-e", benchcontract.IntervalEnv + "=" + interval.ID,
		"-e", benchcontract.ContractEnv + "=" + benchcontract.ContractVersion,
		"-e", benchcontract.CollectorModeEnv + "=" + benchcontract.CollectorRequired,
	}
	if manifest.ReadDescription != nil {
		mounts, err := prepareReadDescriptionAttempt(experimentDir, attemptDir, side, manifest)
		if err != nil {
			return Attempt{}, err
		}
		command = append(command, mounts...)
	}
	if manifest.RuntimeConfig != nil {
		config, err := runtimeConfigInput(manifest)
		if err != nil {
			return Attempt{}, err
		}
		path := archivePath(experimentDir, config.Path)
		hash, err := fileSHA256(path)
		if err != nil || hash != config.SHA256 {
			return Attempt{}, errors.New("retained runtime configuration changed before execution")
		}
		command = append(command, "-e", "OPENCODE_CONFIG=/home/node/.config/ktx/opencode.json", "-v", path+":/home/node/.config/ktx/opencode.json:ro")
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
	command = append(
		command,
		"-v", options.AuthPath+":/home/node/.local/share/opencode/auth.json:ro",
		"-v", archivePath(experimentDir, manifest.Pipelines[side].Path)+":/home/node/.config/ktx/ktx.toml:ro",
		"-v", database+":/home/node/.local/share/ktx/ktx.db:rw",
		"-v", workspace+":/work:rw", manifest.Image["id"], "-m", options.Model,
	)
	if options.Variant != "" {
		command = append(command, "--variant", options.Variant)
	}
	if manifest.Task.FollowupPrompt != "" {
		command = append(command, "--followup", manifest.Task.FollowupPrompt)
	}
	command = append(command, manifest.Task.Prompt)
	if manifest.ReadDescription != nil {
		if err := WriteJSON(filepath.Join(attemptDir, "launch.json"), command); err != nil {
			return Attempt{}, err
		}
	}
	result, runErr := runner.runContainer(ctx, "ktx-"+manifest.ExperimentID+"-"+attemptID, Command{Args: command, Stdout: stream, Stderr: stderr, Timeout: options.AttemptTimeout, Grace: options.TerminationGrace})
	streamErr, stderrErr := stream.Close(), stderr.Close()
	if err := ctx.Err(); err != nil {
		return Attempt{}, errors.Join(runErr, err)
	}
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
	completionErr := validateAttemptDatabase(database, attemptID, interval.SessionID, interval.ID)
	if completionErr != nil && manifest.ReadDescription == nil {
		return Attempt{}, completionErr
	}
	newIDs := []string{interval.SessionID}
	metrics := parseSchema3Stream(streamPath, manifest.Task.ExpectedOutput, manifest.Task.ExpectedFacts)
	metrics.ToolCallCount, err = attemptToolCallCount(database, interval.SessionID)
	if err != nil {
		return Attempt{}, err
	}
	pricing, err := loadPricingSnapshot(experimentDir, manifest.Pricing)
	if err != nil {
		return Attempt{}, err
	}
	estimated := estimateAttempts([]Attempt{{Metrics: metrics}}, pricing)
	metrics.EstimatedAPICost = estimated[0].EstimatedAPICost
	status := AttemptStatus(result.ExitCode, newIDs, metrics)
	if completionErr != nil && status == "completed" {
		status = "invalid"
	}
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
	attempt := Attempt{
		SchemaVersion: 3, ExperimentID: manifest.ExperimentID, AttemptID: attemptID, Side: side,
		Role:       role,
		PipelineID: manifest.Pipelines[side].PipelineID, Cycle: cycle, Ordinal: ordinal,
		StartedAt: startedAt, EndedAt: endedAt, Status: status, ProcessExitCode: result.ExitCode,
		SessionLink: SessionLink{State: map[bool]string{true: "linked", false: "unlinked"}[sessionID != nil], NewSessionCount: len(newIDs), NewSessionIDs: newIDs},
		SessionID:   sessionID, RuntimeEventIntervalID: &interval.ID, RecordedStreams: links, RecordedStreamID: streamID, BenchRunID: benchRunID,
		Metrics: metrics, ConfigSHA256: manifest.Pipelines[side].SHA256, Artifacts: artifacts,
		ContentHashes: map[string]string{"stream": streamHash, "stderr": stderrHash, "workspace": workspaceHash},
	}
	if manifest.FirstArm != "" {
		attempt.FirstArm, attempt.ExecutionOrder = manifest.FirstArm, executionOrder
	}
	if manifest.RuntimeConfig != nil {
		attempt.RuntimeConfigSHA256 = manifest.RuntimeConfig.SHA256
	}
	if manifest.ReadDescription != nil {
		attempt.ReadDescriptionTreatment = manifest.ReadDescription.Version
		for name, relative := range map[string]string{"read_description": "read-description/hooks.jsonl", "launch": "launch.json", "turn_1": "read-description/turn-1.jsonl"} {
			path := filepath.Join(attemptDir, relative)
			attempt.Artifacts[name] = relativePath(experimentDir, path)
			// Keep a failed attempt even when its required evidence is absent.
			if hash, err := fileSHA256(path); err == nil {
				attempt.ContentHashes[name] = hash
			}
		}
		if manifest.Task.FollowupPrompt != "" {
			path := filepath.Join(attemptDir, "read-description/turn-2.jsonl")
			attempt.Artifacts["turn_2"] = relativePath(experimentDir, path)
			if hash, err := fileSHA256(path); err == nil {
				attempt.ContentHashes["turn_2"] = hash
			}
		}
		if manifest.Task.ScoringKind == "read_preservation" {
			// The retained ledger is a projection; regeneration independently
			// recomputes this score from turn streams and persisted tool calls.
			scored, scoreErr := scoreLocalReadAttempt(manifest.Task, interval.SessionID, attemptDir)
			if scoreErr != nil {
				return Attempt{}, scoreErr
			}
			attempt.Score = scored
			if status != "completed" || result.ExitCode != 0 || !metrics.StreamComplete {
				attempt.FullPass, attempt.Score.Score = false, 0
			}
		}
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
	if manifest.ReadDescription != nil {
		// Retain the attempt before rejecting its evidence; never start another
		// paid arm after an unproven application or incomplete follow-up.
		if completionErr != nil {
			return Attempt{}, completionErr
		}
		materialized, err := materializeAttempt(experimentDir, manifest, attempt)
		if err != nil {
			return Attempt{}, err
		}
		if len(materialized.validationReasons) > 0 {
			return Attempt{}, fmt.Errorf("attempt %q evidence invalid: %s", attemptID, strings.Join(materialized.validationReasons, "; "))
		}
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
		digest := sha256.Sum256([]byte(value))
		return builder.String()[:55] + "-" + hex.EncodeToString(digest[:])
	}
	return builder.String()
}

var errContainerCleanup = errors.New("container cleanup failed")

// runContainer owns one Docker run and its cleanup, including capability probes.
// It deliberately omits --rm so successful auto-removal cannot be confused with
// a failed cleanup request. Cleanup never inherits the canceled run context.
func (runner *Runner) runContainer(ctx context.Context, identity string, command Command) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	// Separate concurrent executions even when archive roots reuse an identity.
	name := sanitizeContainerName(identity + "-" + rand.Text())
	args := []string{command.Args[0], "run", "--name", name}
	command.Args = append(args, command.Args[2:]...)
	result, runErr := runner.Executor.Run(ctx, command)
	var stderr bytes.Buffer
	cleanup, cleanupErr := runner.Executor.Run(context.Background(), Command{
		Args: []string{command.Args[0], "rm", "-f", name}, Stderr: newDiagnosticLimit(&stderr), Stdout: io.Discard,
		Timeout: 30 * time.Second, Grace: time.Second,
	})
	if cleanupErr != nil || cleanup.ExitCode != 0 || cleanup.TimedOut {
		return result, errors.Join(runErr, cleanupErr, fmt.Errorf("%w for %q (exit %d, timed_out=%t): %s", errContainerCleanup, name, cleanup.ExitCode, cleanup.TimedOut, strings.TrimSpace(stderr.String())))
	}
	return result, errors.Join(runErr, ctx.Err())
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}

func archivePath(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, filepath.FromSlash(path))
}

func (runner *Runner) output(ctx context.Context, command Command) (string, error) {
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, newDiagnosticLimit(&stderr)
	if command.Timeout <= 0 {
		command.Timeout = 10 * time.Minute
	}
	if command.Grace <= 0 {
		command.Grace = 30 * time.Second
	}
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
