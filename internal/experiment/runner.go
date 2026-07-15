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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jgabor/ktx/internal/db"
	_ "modernc.org/sqlite"
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
	Now                    func() time.Time
	PID                    int
	ResolvePrice           func(provider, model string) (db.Price, error)
	ResolveCacheCapability CacheCapabilityResolver
}

func NewRunner() *Runner {
	return &Runner{Executor: OSExecutor{}, Now: time.Now, PID: os.Getpid(), ResolvePrice: db.ResolvePrice}
}

func (runner *Runner) Compare(ctx context.Context, options CompareOptions) (string, map[string]any, int, error) {
	if err := runner.prepareOptions(&options); err != nil {
		return "", nil, 2, err
	}
	provider, model, err := splitModel(options.Model)
	if err != nil {
		return "", nil, 2, err
	}
	if options.BuildImage {
		result, err := runner.Executor.Run(ctx, Command{Args: []string{options.DockerBin, "build", "-f", filepath.Join(options.RepoRoot, "Dockerfile.benchmark"), "-t", options.Image, options.RepoRoot}, Stdout: os.Stdout, Stderr: os.Stderr})
		if err != nil {
			return "", nil, 2, fmt.Errorf("build benchmark image: %w", err)
		}
		if result.ExitCode != 0 {
			return "", nil, 2, fmt.Errorf("build benchmark image: exit %d", result.ExitCode)
		}
	}
	runtime, err := runner.inspectBenchmarkRuntime(ctx, options)
	if err != nil {
		return "", nil, 2, err
	}
	experimentID := options.ExperimentID
	if experimentID == "" {
		experimentID = runner.experimentID(ctx, options)
	}
	cacheControl := resolvedCacheControl{}
	if options.methodologyVersion == "v5-cache-aware" {
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
	if options.methodologyVersion == "v5-cache-aware" {
		manifest.CacheControl = &CacheControlManifest{Mode: cacheControl.mode, Source: cacheControl.source, NamespaceSHA256: cacheControl.hashes, WarmupCount: options.Warmup, IdleGapMS: 0, Model: options.Model, Provider: provider, Variant: options.Variant, RuntimeVersion: manifest.Identities["opencode"], TTFTAvailable: false, TTFTUnavailableReason: "retained OpenCode stream timestamps do not provide a reliable request start"}
	}
	if err := WriteJSON(filepath.Join(experimentDir, "manifest.json"), manifest); err != nil {
		return "", nil, 2, err
	}
	attempts := []Attempt{}
	ordinals := map[string]int{Baseline: 0, Candidate: 0}
	executionOrder := 0
	if options.methodologyVersion == "v5-cache-aware" {
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
			if options.methodologyVersion == "v5-cache-aware" {
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
	summary, err := WriteSummary(experimentDir)
	if err != nil {
		return experimentDir, nil, 2, err
	}
	valid := CountValidForMethodology(attempts, options.Task.MinimumToolCalls, options.methodologyVersion)
	if valid[Baseline] >= options.TargetValid && valid[Candidate] >= options.TargetValid {
		return experimentDir, summary, 0, nil
	}
	return experimentDir, summary, 1, nil
}

func (runner *Runner) prepareOptions(options *CompareOptions) error {
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
		return errors.New("github-copilot/* models are not permitted for this repo; use a different provider such as neuralwatt/* or openai/*")
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
	cacheAware := methodology.Version == "v5-cache-aware"
	if !cacheAware && (options.CacheControl != "" || options.WarmupSet || options.Warmup != 0) {
		return errors.New("--cache-control and --warmup require methodology v5-cache-aware")
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
	if _, err := os.Stat(options.AuthPath); err != nil {
		return fmt.Errorf("OpenCode auth file not found: %s", options.AuthPath)
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
	return os.MkdirAll(options.DataDir, 0o755)
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

func (runner *Runner) inspectBenchmarkRuntime(ctx context.Context, options CompareOptions) (benchmarkRuntime, error) {
	imageID, err := runner.output(ctx, Command{Args: []string{options.DockerBin, "image", "inspect", options.Image, "--format", "{{.Id}}"}})
	if err != nil {
		return benchmarkRuntime{}, fmt.Errorf("inspect benchmark image: %w", err)
	}
	identityOutput, err := runner.output(ctx, Command{Args: []string{options.DockerBin, "run", "--rm", "-v", options.BaselineConfig + ":/home/node/.config/ktx/ktx.toml:ro", options.Image, "--identity"}})
	if err != nil {
		return benchmarkRuntime{}, fmt.Errorf("inspect benchmark identity: %w", err)
	}
	identities := map[string]string{}
	for _, line := range strings.Split(identityOutput, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			identities[parts[0]] = parts[1]
		}
	}
	return benchmarkRuntime{ImageID: strings.TrimSpace(imageID), Identities: normalizeRuntimeIdentities(identities)}, nil
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
	return Manifest{
		SchemaVersion: 1, ExperimentID: experimentID, CreatedAt: runner.timestamp(), Task: options.Task,
		Model: options.Model, Variant: variant, TargetValidPerPipeline: options.TargetValid, MaxAttempts: options.MaxAttempts,
		AttemptTimeout: options.AttemptTimeout.String(), TerminationGrace: options.TerminationGrace.String(),
		PipelineExecutionPolicy: map[string]any{"kind": "alternating_by_cycle", "first_cycle": []string{Baseline, Candidate}, "second_cycle": []string{Candidate, Baseline}},
		Repository:              repository, Image: map[string]string{"name": options.Image, "id": runtime.ImageID},
		Identities: runtime.Identities, Pipelines: configs, Methodology: methodology,
	}, nil
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
	repositoryRoot := filepath.Join(experimentDir, manifest.Repository.Snapshot.Path)
	if _, err := prepareWorkspace(repositoryRoot, workspace, manifest.Task.WorkspaceMode, manifest.Task.CorpusFiles); err != nil {
		return Attempt{}, err
	}
	before, err := runner.listSessions(ctx, options, database)
	if err != nil {
		return Attempt{}, err
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
	startedAt := runner.timestamp()
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
	newIDs := newSessionIDs(before, after)
	metrics := ParseStream(streamPath, manifest.Task.ExpectedOutput, manifest.Task.ExpectedFacts)
	pricing, err := loadPricingSnapshot(experimentDir, manifest.Pricing)
	if err != nil {
		return Attempt{}, err
	}
	estimated := estimateAttempts([]Attempt{{Metrics: metrics}}, pricing)
	metrics.EstimatedAPICost = estimated[0].EstimatedAPICost
	status := AttemptStatus(result.ExitCode, newIDs, metrics)
	var sessionID *string
	if len(newIDs) == 1 {
		sessionID = &newIDs[0]
	}
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
		SchemaVersion: 1, ExperimentID: manifest.ExperimentID, AttemptID: attemptID, Side: side,
		Role:       role,
		PipelineID: manifest.Pipelines[side].PipelineID, Cycle: cycle, Ordinal: ordinal,
		StartedAt: startedAt, EndedAt: endedAt, Status: status, ProcessExitCode: result.ExitCode,
		SessionLink: SessionLink{State: map[bool]string{true: "linked", false: "unlinked"}[sessionID != nil], NewSessionCount: len(newIDs), NewSessionIDs: newIDs},
		SessionID:   sessionID, RecordedStreams: links, RecordedStreamID: streamID, BenchRunID: benchRunID,
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
	absolute, err := filepath.Abs(database)
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: absolute}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT id, bench_run_id FROM recorded_streams WHERE session_id = ? ORDER BY id", sessionID)
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

func newSessionIDs(before, after []map[string]any) []string {
	seen := map[string]bool{}
	for _, session := range before {
		if id, ok := session["id"].(string); ok {
			seen[id] = true
		}
	}
	result := []string{}
	for _, session := range after {
		if id, ok := session["id"].(string); ok && !seen[id] {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
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

func (runner *Runner) timestamp() string {
	return runner.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}
