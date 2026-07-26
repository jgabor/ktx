package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/experiment"
	"github.com/jgabor/ktx/internal/version"
)

func runBenchAgent(args []string, opts Options, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--identity" {
		fmt.Fprintf(stdout, "ktx\t%s\n", version.Version)
		command := exec.Command("opencode", "--version")
		output, err := command.Output()
		if err != nil {
			fmt.Fprintln(stdout, "opencode\tmissing")
		} else {
			fmt.Fprintf(stdout, "opencode\t%s\n", strings.TrimSpace(string(output)))
		}
		return ExitOK
	}
	fs := flag.NewFlagSet("ktx bench agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var model, format, variant string
	fs.StringVar(&model, "model", os.Getenv("MODEL"), "model id (provider/name)")
	fs.StringVar(&model, "m", os.Getenv("MODEL"), "model id (provider/name)")
	fs.StringVar(&variant, "variant", os.Getenv("MODEL_VARIANT"), "model variant (e.g. high)")
	fs.StringVar(&format, "format", "json", "OpenCode output format")
	if err := fs.Parse(args); err != nil || model == "" || len(fs.Args()) != 1 {
		if err == nil {
			err = errors.New("usage: ktx bench agent -m <model> <prompt>")
		}
		printError(stderr, usageError("ktx bench agent: %v", err), opts.Format)
		return ExitUsage
	}
	if strings.HasPrefix(model, "github-copilot/") {
		printError(stderr, usageError("ktx bench agent: github-copilot/* models are not permitted for this repo; use a different provider such as neuralwatt/* or openai/*"), opts.Format)
		return ExitUsage
	}
	auth := os.Getenv("OPENCODE_AUTH_PATH")
	if auth == "" {
		home, _ := os.UserHomeDir()
		auth = filepath.Join(home, ".local", "share", "opencode", "auth.json")
	}
	if _, err := os.Stat(auth); err != nil {
		printError(stderr, usageError("ktx bench agent: opencode auth missing: %s", auth), opts.Format)
		return ExitUsage
	}
	if _, exists := config.ConfigPath(); !exists {
		printError(stderr, usageError("ktx bench agent: ktx config missing from $XDG_CONFIG_HOME/ktx/ktx.toml"), opts.Format)
		return ExitUsage
	}
	cfg, err := config.Load()
	if err != nil {
		printError(stderr, usageError("ktx bench agent: %v", err), opts.Format)
		return ExitUsage
	}
	if expected := os.Getenv("KTX_EXPECTED_STAGES_JSON"); expected != "" {
		var stages []string
		if err := json.Unmarshal([]byte(expected), &stages); err != nil {
			printError(stderr, usageError("ktx bench agent: invalid KTX_EXPECTED_STAGES_JSON: %v", err), opts.Format)
			return ExitUsage
		}
		if !slices.Equal(stages, cfg.PipelineDefault) {
			actual, _ := json.Marshal(cfg.PipelineDefault)
			printError(stderr, usageError("ktx bench agent: configured stages mismatch: expected=%s actual=%s", expected, actual), opts.Format)
			return ExitUsage
		}
	}
	opencodeArgs := []string{"run", "--format", format, "-m", model}
	if variant != "" {
		opencodeArgs = append(opencodeArgs, "--variant", variant)
	}
	opencodeArgs = append(opencodeArgs, fs.Arg(0))
	command := exec.Command("opencode", opencodeArgs...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, stdout, stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return exitError.ExitCode()
		}
		printError(stderr, internalError("ktx bench agent: %v", err), opts.Format)
		return ExitInternal
	}
	return ExitOK
}

func runBenchCompare(args []string, opts Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ktx bench compare", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var model, variant, taskFile, taskID, prompt, expectedOutput, expectedFactsRaw, workspaceMode string
	var baseline, candidate, dataDir, auth, image, experimentID, format, summarize, repoRoot, methodology string
	var cacheControl string
	var target, maxAttempts, minimumToolCalls, warmup int
	var timeout, grace time.Duration
	var build bool
	fs.StringVar(&model, "model", "", "model id (provider/name, e.g. opencode/big-pickle)")
	fs.StringVar(&model, "m", "", "model id (provider/name)")
	fs.StringVar(&variant, "variant", "", "model variant (e.g. high)")
	fs.StringVar(&taskFile, "task-file", "", "benchmark task TOML")
	fs.StringVar(&taskID, "task-id", "", "task id when not using --task-file")
	fs.StringVar(&prompt, "prompt", "", "task prompt when not using --task-file")
	fs.StringVar(&prompt, "p", "", "task prompt when not using --task-file")
	fs.StringVar(&expectedOutput, "expected-output", "", "exact expected answer")
	fs.StringVar(&expectedFactsRaw, "expected-facts", "", "JSON array of required facts")
	fs.IntVar(&minimumToolCalls, "min-tool-calls", 1, "minimum tool calls for evaluation")
	fs.StringVar(&workspaceMode, "workspace-mode", "repo", "repo or corpus")
	fs.StringVar(&baseline, "baseline-config", "", "baseline ktx.toml")
	fs.StringVar(&candidate, "candidate-config", "", "candidate ktx.toml")
	fs.StringVar(&dataDir, "data-dir", "", "experiment data directory")
	fs.StringVar(&auth, "auth", "", "OpenCode auth.json")
	fs.StringVar(&image, "image", "ktx-bench", "Docker image")
	fs.StringVar(&experimentID, "experiment-id", "", "experiment id")
	fs.StringVar(&format, "format", opts.Format, "json or text")
	fs.StringVar(&summarize, "summarize", "", "regenerate an experiment summary")
	fs.StringVar(&repoRoot, "repo-root", "", "repository root")
	fs.StringVar(&methodology, "methodology", "", "methodology YAML")
	fs.StringVar(&cacheControl, "cache-control", "", "cache control: auto, isolated, or observed (methodology v5)")
	fs.IntVar(&warmup, "warmup", 0, "warm-up attempts per side (methodology v5 default 1)")
	fs.IntVar(&target, "target-valid", 0, "valid measured attempts required per side")
	fs.IntVar(&target, "rounds", 0, "valid measured attempts required per side (v4 default 3; v5 default 5)")
	fs.IntVar(&maxAttempts, "max-attempts", 0, "total attempt budget")
	fs.DurationVar(&timeout, "attempt-timeout", 10*time.Minute, "per-attempt timeout")
	fs.DurationVar(&grace, "termination-grace", 30*time.Second, "termination grace period")
	fs.BoolVar(&build, "build", false, "build the benchmark image")
	if err := fs.Parse(args); err != nil {
		printError(stderr, usageError("ktx bench compare: %v", err), opts.Format)
		return ExitUsage
	}
	warmupSet := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == "warmup" {
			warmupSet = true
		}
	})
	if summarize != "" {
		regeneration, err := experiment.RegenerateSummary(summarize)
		if err != nil {
			printError(stderr, internalError("ktx bench compare: %v", err), opts.Format)
			return ExitInternal
		}
		if len(regeneration.ProjectionDivergences) > 0 {
			fmt.Fprintf(stderr, "ktx bench compare: %d copied measurement projection divergences ignored\n", len(regeneration.ProjectionDivergences))
		}
		return renderExperimentValue(stdout, regeneration.Summary, format, experiment.RenderSummary)
	}
	if model == "" || (taskFile == "" && (taskID == "" || prompt == "")) {
		printError(stderr, usageError("ktx bench compare: --model and either --task-file or --task-id/--prompt are required"), opts.Format)
		return ExitUsage
	}
	var task experiment.Task
	if taskFile == "" {
		task = experiment.Task{ID: taskID, Prompt: prompt, MinimumToolCalls: minimumToolCalls, WorkspaceMode: workspaceMode}
		switch {
		case expectedFactsRaw != "" && expectedOutput != "":
			printError(stderr, usageError("ktx bench compare: use either --expected-output or --expected-facts"), opts.Format)
			return ExitUsage
		case expectedFactsRaw != "":
			if err := json.Unmarshal([]byte(expectedFactsRaw), &task.ExpectedFacts); err != nil || len(task.ExpectedFacts) == 0 {
				printError(stderr, usageError("ktx bench compare: --expected-facts must be a non-empty JSON string array"), opts.Format)
				return ExitUsage
			}
			task.ScoringKind = "multi_fact_fractional"
		case expectedOutput != "":
			task.ExpectedOutput = &expectedOutput
			task.ScoringKind = "exact_match"
		default:
			printError(stderr, usageError("ktx bench compare: expected output or facts are required"), opts.Format)
			return ExitUsage
		}
	}
	runner := experiment.NewRunner()
	_, summary, code, err := runner.Compare(context.Background(), experiment.CompareOptions{
		Model: model, Variant: variant, TaskFile: taskFile, Task: task, TargetValid: target, MaxAttempts: maxAttempts,
		AttemptTimeout: timeout, TerminationGrace: grace, ExperimentID: experimentID,
		DataDir: dataDir, Image: image, AuthPath: auth, BaselineConfig: baseline,
		CandidateConfig: candidate, BuildImage: build, RepoRoot: repoRoot, MethodologyPath: methodology,
		CacheControl: cacheControl, Warmup: warmup, WarmupSet: warmupSet,
		DockerBin: os.Getenv("DOCKER_BIN"), KTXBin: os.Getenv("KTX_BIN"),
	})
	if err != nil {
		printError(stderr, usageError("ktx bench compare: %v", err), opts.Format)
		return code
	}
	if renderCode := renderExperimentValue(stdout, summary, format, experiment.RenderSummary); renderCode != ExitOK {
		return renderCode
	}
	return code
}

func runBenchCorpus(args []string, opts Options, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "generate" {
		printError(stderr, usageError("ktx bench corpus: usage: ktx bench corpus generate [--output DIR]"), opts.Format)
		return ExitUsage
	}
	fs := flag.NewFlagSet("ktx bench corpus generate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output, format string
	fs.StringVar(&output, "output", filepath.Join("tools", "bench", "corpus"), "corpus output directory")
	fs.StringVar(&format, "format", opts.Format, "json or text")
	if err := fs.Parse(args[1:]); err != nil || len(fs.Args()) > 0 {
		if err == nil {
			err = fmt.Errorf("unexpected argument %q", fs.Arg(0))
		}
		printError(stderr, usageError("ktx bench corpus generate: %v", err), opts.Format)
		return ExitUsage
	}
	manifest, err := experiment.GenerateCorpus(output)
	if err != nil {
		printError(stderr, internalError("ktx bench corpus generate: %v", err), opts.Format)
		return ExitInternal
	}
	if format == FormatText {
		fmt.Fprintf(stdout, "wrote %v files, %v bytes, ~%v tokens\n", len(manifest["files"].([]any)), manifest["total_bytes"], manifest["total_bytes"].(int)/4)
		return ExitOK
	}
	return encodeExperimentJSON(stdout, manifest)
}

func runBenchValidate(args []string, opts Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ktx bench validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var directory, format string
	fs.StringVar(&directory, "experiment", "", "experiment directory")
	fs.StringVar(&format, "format", opts.Format, "json or text")
	if err := fs.Parse(args); err != nil || directory == "" || len(fs.Args()) > 0 {
		if err == nil {
			err = fmt.Errorf("--experiment is required")
		}
		printError(stderr, usageError("ktx bench validate: %v", err), opts.Format)
		return ExitUsage
	}
	report, err := experiment.ValidateExperiment(directory)
	if err != nil {
		printError(stderr, internalError("ktx bench validate: %v", err), opts.Format)
		return ExitInternal
	}
	if format == FormatText {
		fmt.Fprintf(stdout, "experiment=%v valid=%v summary_byte_match=%v target_met=%v\n", report["experiment_id"], report["valid"], report["summary_byte_match"], report["target_met"])
	} else if code := encodeExperimentJSON(stdout, report); code != ExitOK {
		return code
	}
	if valid, _ := report["valid"].(bool); !valid {
		return 1
	}
	return ExitOK
}

func runBenchRank(args []string, opts Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ktx bench rank", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var model, variant, taskFile, pipelinesRaw, baselineID, dataDir, auth, image, campaignID, format string
	var rounds, maxAttempts int
	var timeout, grace time.Duration
	fs.StringVar(&model, "model", "", "model id (provider/name, e.g. opencode/big-pickle)")
	fs.StringVar(&model, "m", "", "model id (provider/name)")
	fs.StringVar(&variant, "variant", "", "model variant (e.g. high)")
	fs.StringVar(&taskFile, "task-file", "", "benchmark task TOML")
	fs.StringVar(&pipelinesRaw, "pipelines", "sanitize,keep-ends-mid,conservative-a,conservative-a-recover,aggressive-head", "comma-separated pipeline ids")
	fs.StringVar(&baselineID, "baseline-pipeline", "passthrough", "baseline pipeline id")
	fs.StringVar(&dataDir, "data-dir", ".bench-data", "experiment data directory")
	fs.StringVar(&auth, "auth", "", "OpenCode auth.json")
	fs.StringVar(&image, "image", "ktx-bench", "Docker image")
	fs.StringVar(&campaignID, "campaign-id", "", "campaign id")
	fs.StringVar(&format, "format", opts.Format, "json or text")
	fs.IntVar(&rounds, "rounds", 5, "valid attempts per side")
	fs.IntVar(&maxAttempts, "max-attempts", 24, "attempt budget per comparison")
	fs.DurationVar(&timeout, "attempt-timeout", 15*time.Minute, "per-attempt timeout")
	fs.DurationVar(&grace, "termination-grace", 30*time.Second, "termination grace period")
	if err := fs.Parse(args); err != nil || model == "" || taskFile == "" || len(fs.Args()) > 0 {
		if err == nil {
			err = fmt.Errorf("--model and --task-file are required")
		}
		printError(stderr, usageError("ktx bench rank: %v", err), opts.Format)
		return ExitUsage
	}
	var candidates []string
	for _, item := range strings.Split(pipelinesRaw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		printError(stderr, usageError("ktx bench rank: no candidate pipelines specified"), opts.Format)
		return ExitUsage
	}
	if campaignID == "" {
		campaignID = "rank-" + time.Now().UTC().Format("20060102T150405Z")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		printError(stderr, internalError("ktx bench rank: %v", err), opts.Format)
		return ExitInternal
	}
	campaignDir := filepath.Join(dataDir, campaignID)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		printError(stderr, internalError("ktx bench rank: %v", err), opts.Format)
		return ExitInternal
	}
	if err := os.Mkdir(campaignDir, 0o755); err != nil {
		printError(stderr, internalError("ktx bench rank: %v", err), opts.Format)
		return ExitInternal
	}
	runner := experiment.NewRunner()
	pairs := make([]experiment.PairResult, 0, len(candidates))
	exitCode := ExitOK
	for _, candidateID := range candidates {
		experimentID := campaignID + "-" + candidateID
		_, summary, code, err := runner.Compare(context.Background(), experiment.CompareOptions{
			Model: model, Variant: variant, TaskFile: taskFile, TargetValid: rounds, MaxAttempts: maxAttempts,
			AttemptTimeout: timeout, TerminationGrace: grace, ExperimentID: experimentID,
			DataDir: dataDir, Image: image, AuthPath: auth,
			BaselineConfig: pipelineConfigPath(baselineID), CandidateConfig: pipelineConfigPath(candidateID),
			DockerBin: os.Getenv("DOCKER_BIN"), KTXBin: os.Getenv("KTX_BIN"),
		})
		if err != nil {
			printError(stderr, internalError("ktx bench rank: %s: %v", candidateID, err), opts.Format)
			return ExitInternal
		}
		if code != 0 {
			exitCode = 1
		}
		pair := experiment.PairResult{CandidatePipeline: candidateID, ExperimentID: experimentID, ExperimentDir: filepath.Join(dataDir, experimentID), ExitCode: code, Summary: summary}
		pairs = append(pairs, pair)
		_ = experiment.WriteJSON(filepath.Join(campaignDir, "pair-"+candidateID+".json"), pair)
	}
	ranking := experiment.BuildRanking(experiment.RankingOptions{TaskFile: taskFile, Model: model, Variant: variant, Rounds: rounds, BaselinePipeline: baselineID, Candidates: candidates}, pairs, time.Now())
	if err := experiment.WriteJSON(filepath.Join(campaignDir, "ranking.json"), ranking); err != nil {
		printError(stderr, internalError("ktx bench rank: %v", err), opts.Format)
		return ExitInternal
	}
	text := experiment.RenderRanking(ranking)
	if err := os.WriteFile(filepath.Join(campaignDir, "ranking.txt"), []byte(text+"\n"), 0o644); err != nil {
		printError(stderr, internalError("ktx bench rank: %v", err), opts.Format)
		return ExitInternal
	}
	if format == FormatText {
		fmt.Fprintln(stdout, text)
	} else if code := encodeExperimentJSON(stdout, ranking); code != ExitOK {
		return code
	}
	return exitCode
}

func pipelineConfigPath(id string) string {
	return filepath.Join("tools", "bench", "pipelines", id+".toml")
}

func renderExperimentValue(stdout io.Writer, value map[string]any, format string, textRenderer func(map[string]any) string) int {
	if format == FormatText {
		fmt.Fprintln(stdout, textRenderer(value))
		return ExitOK
	}
	return encodeExperimentJSON(stdout, value)
}

func encodeExperimentJSON(stdout io.Writer, value any) int {
	data, err := experiment.MarshalJSON(value)
	if err != nil {
		return ExitInternal
	}
	if _, err := stdout.Write(data); err != nil {
		return ExitInternal
	}
	return ExitOK
}
