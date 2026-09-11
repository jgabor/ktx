package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/benchregistry"
	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/experiment"
	"github.com/jgabor/ktx/internal/version"
)

func runBenchAgent(args []string, opts Options, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--capabilities" {
		return printBenchAgentCapabilities(opts.Format, stdout, stderr)
	}
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
		printError(stderr, usageError("ktx bench agent: github-copilot/* models are not permitted for this repo; use --model openai/gpt-5.6-luna --variant high"), opts.Format)
		return ExitUsage
	}
	collectorEnv, collectorPresent, err := benchcontract.ReadEnvironment()
	if err != nil {
		printError(stderr, usageError("ktx bench agent: %v", err), opts.Format)
		return ExitUsage
	}
	var collectorDB *dbpkg.DB
	if collectorPresent {
		target, pathErr := dbpkg.ResolveTarget("")
		if pathErr != nil {
			printError(stderr, usageError("ktx bench agent: resolve collector database: %v", pathErr), opts.Format)
			return ExitUsage
		}
		collectorDB, err = openDatabase(target, "bench")
		if err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				printError(stderr, typed, opts.Format)
				return exitCodeOf(typed)
			}
			printError(stderr, usageError("ktx bench agent: open collector database: %v", err), opts.Format)
			return ExitUsage
		}
		defer collectorDB.Close()
		if err := validateBenchAgentCollector(collectorDB, collectorEnv); err != nil {
			printError(stderr, usageError("ktx bench agent: %v", err), opts.Format)
			return ExitUsage
		}
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
			if collectorDB != nil {
				state := dbpkg.RuntimeIncomplete
				if interval, loadErr := collectorDB.LoadRuntimeEventInterval(collectorEnv.IntervalID); loadErr == nil && interval.ContradictionCount > 0 {
					state = dbpkg.RuntimeContradictory
				}
				if closeErr := collectorDB.SealRuntimeEventInterval(collectorEnv.IntervalID, collectorEnv.SessionID, time.Now().UTC().Format(time.RFC3339Nano), state, fmt.Sprintf("opencode exit %d", exitError.ExitCode())); closeErr != nil {
					printError(stderr, internalError("ktx bench agent: seal incomplete collector interval: %v", closeErr), opts.Format)
					return ExitInternal
				}
			}
			return exitError.ExitCode()
		}
		if collectorDB != nil {
			_ = collectorDB.SealRuntimeEventInterval(collectorEnv.IntervalID, collectorEnv.SessionID, time.Now().UTC().Format(time.RFC3339Nano), dbpkg.RuntimeIncomplete, "opencode startup failure")
		}
		printError(stderr, internalError("ktx bench agent: %v", err), opts.Format)
		return ExitInternal
	}
	if collectorDB != nil {
		interval, loadErr := collectorDB.LoadRuntimeEventInterval(collectorEnv.IntervalID)
		if loadErr == nil {
			loadErr = validateCompleteCollectorInterval(interval, collectorEnv)
		}
		state, reason := dbpkg.RuntimeComplete, ""
		if loadErr != nil {
			state, reason = dbpkg.RuntimeIncomplete, loadErr.Error()
			if interval != nil && interval.ContradictionCount > 0 {
				state = dbpkg.RuntimeContradictory
			}
		}
		if closeErr := collectorDB.SealRuntimeEventInterval(collectorEnv.IntervalID, collectorEnv.SessionID, time.Now().UTC().Format(time.RFC3339Nano), state, reason); closeErr != nil {
			printError(stderr, internalError("ktx bench agent: seal collector interval: %v", closeErr), opts.Format)
			return ExitInternal
		}
		if loadErr != nil {
			printError(stderr, internalError("ktx bench agent: collector interval incomplete: %v", loadErr), opts.Format)
			return ExitInternal
		}
	}
	return ExitOK
}

func printBenchAgentCapabilities(format string, stdout, stderr io.Writer) int {
	output, err := exec.Command("opencode", "--version").Output()
	if err != nil {
		printError(stderr, internalError("ktx bench agent capabilities: opencode --version: %v", err), format)
		return ExitInternal
	}
	opencodeVersion := strings.TrimSpace(string(output))
	pluginPath, pathErr := opencodeGlobalPluginPath()
	pluginBytes, readErr := os.ReadFile(pluginPath)
	if pathErr != nil || readErr != nil {
		printError(stderr, internalError("ktx bench agent capabilities: read generated plugin: %v", errors.Join(pathErr, readErr)), format)
		return ExitInternal
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(pluginBytes))
	expectedDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(ktxOpencodePluginTemplate)))
	capabilities := map[string]any{
		"ktx_version": version.Version, "opencode_version": opencodeVersion, "generated_plugin_sha256": digest,
		"generated_plugin_matches_binary": digest == expectedDigest,
		"event_collector_contract":        benchcontract.ContractVersion, "explicit_session_ownership": true,
		"serialized_callbacks": true, "complete_interval_lifecycle": true, "epoch_tracking": true,
		"strict_acknowledgements": true, "exact_model_step_identity": true,
		"retained_duplicate_callbacks": true, "accepted_model_step_projection": true, "callback_queue_continuation": true,
		"fail_closed_interval_validation": true, "preflight_before_auth": true,
		"normalized_kinds": []string{"collector_open", "model_step", "retry", "compaction", "interruption", "other"},
	}
	if format == FormatText {
		fmt.Fprintf(stdout, "ktx=%s opencode=%s contract=%s plugin_sha256=%s generated_plugin_matches_binary=%t explicit_session_ownership=true serialized_callbacks=true complete_interval_lifecycle=true epoch_tracking=true strict_acknowledgements=true exact_model_step_identity=true retained_duplicate_callbacks=true accepted_model_step_projection=true callback_queue_continuation=true fail_closed_interval_validation=true preflight_before_auth=true\n", version.Version, opencodeVersion, benchcontract.ContractVersion, digest, digest == expectedDigest)
		return ExitOK
	}
	if err := json.NewEncoder(stdout).Encode(capabilities); err != nil {
		return ExitInternal
	}
	return ExitOK
}

func validateBenchAgentCollector(database *dbpkg.DB, env benchcontract.Environment) error {
	interval, err := database.LoadRuntimeEventInterval(env.IntervalID)
	if err != nil {
		return fmt.Errorf("load collector interval: %w", err)
	}
	if interval.SessionID != env.SessionID || interval.Runtime != benchcontract.Runtime || interval.RuntimeVersion != benchcontract.RuntimeVersion || interval.Source != dbpkg.RuntimeEventSource || interval.ContractVersion != env.Contract || interval.CloseState != dbpkg.RuntimeOpen {
		return errors.New("collector owner or interval contract mismatch")
	}
	var count int
	if err := database.DB().QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("collector database contains %d sessions; want exactly one", count)
	}
	var intervalCount int
	if err := database.DB().QueryRow(`SELECT count(*) FROM runtime_event_intervals`).Scan(&intervalCount); err != nil {
		return err
	}
	if intervalCount != 1 {
		return fmt.Errorf("collector database contains %d intervals; want exactly one", intervalCount)
	}
	return nil
}

func validateCompleteCollectorInterval(interval *dbpkg.RuntimeEventInterval, env benchcontract.Environment) error {
	if interval.SessionID != env.SessionID || interval.ID != env.IntervalID || interval.Runtime != benchcontract.Runtime || interval.RuntimeVersion != benchcontract.RuntimeVersion || interval.Source != dbpkg.RuntimeEventSource || interval.ContractVersion != env.Contract {
		return errors.New("collector provenance mismatch")
	}
	if err := dbpkg.ValidateRuntimeEventInterval(interval); err != nil {
		return err
	}
	steps := 0
	for _, event := range interval.Events {
		if event.Kind == "model_step" {
			steps++
		}
	}
	if steps == 0 {
		return errors.New("collector retained no model steps")
	}
	return nil
}

func runBenchCompare(args []string, opts Options, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet("ktx bench compare", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var model, variant, taskID string
	var baseline, candidate, auth, image, experimentID, format, repoRoot, specID string
	var pricingSnapshot, pricingSHA256 string
	var cacheControl string
	var target, maxAttempts, warmup int
	var timeout, grace time.Duration
	var build, preflight bool
	fs.StringVar(&model, "model", "", "model id (provider/name, e.g. openai/gpt-5.6-luna with --variant high)")
	fs.StringVar(&model, "m", "", "model id (provider/name)")
	fs.StringVar(&variant, "variant", "", "model variant (e.g. high)")
	fs.StringVar(&taskID, "task", "", "registered task ID")
	fs.StringVar(&baseline, "baseline", "", "registered baseline pipeline ID")
	fs.StringVar(&candidate, "candidate", "", "registered candidate pipeline ID")
	fs.StringVar(&auth, "auth", "", "OpenCode auth.json")
	fs.StringVar(&image, "image", "ktx-bench", "Docker image")
	fs.StringVar(&experimentID, "experiment-id", "", "experiment id")
	fs.StringVar(&format, "format", opts.Format, "json or text")
	fs.StringVar(&repoRoot, "repo-root", "", "repository root")
	fs.StringVar(&specID, "spec", "", "registered benchmark spec ID")
	fs.StringVar(&pricingSnapshot, "pricing-snapshot", "", "retained pricing.json to pin before model calls")
	fs.StringVar(&pricingSHA256, "pricing-sha256", "", "required SHA-256 identity for --pricing-snapshot")
	fs.StringVar(&cacheControl, "cache-control", "", "cache control: auto, isolated, or observed (cache-aware methodologies v5 and v6)")
	fs.IntVar(&warmup, "warmup", 0, "warm-up attempts per side (cache-aware methodologies v5 and v6 default 1)")
	fs.IntVar(&target, "target-valid", 0, "valid measured attempts required per side")
	fs.IntVar(&target, "rounds", 0, "valid measured attempts required per side (v4 default 3; cache-aware v5 and v6 default 5)")
	fs.IntVar(&maxAttempts, "max-attempts", 0, "total attempt budget")
	fs.DurationVar(&timeout, "attempt-timeout", 10*time.Minute, "per-attempt timeout")
	fs.DurationVar(&grace, "termination-grace", 30*time.Second, "termination grace period")
	fs.BoolVar(&build, "build", false, "build the benchmark image")
	fs.BoolVar(&preflight, "preflight", false, "attest benchmark evidence capability without a model call")
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
	if preflight {
		if pricingSHA256 != "" && pricingSnapshot == "" {
			printError(stderr, usageError("ktx bench compare: --pricing-sha256 requires --pricing-snapshot"), opts.Format)
			return ExitUsage
		}
		runner := experiment.NewRunner()
		runner.Diagnostics = stderr
		report, err := runner.Preflight(ctx, experiment.CompareOptions{
			Model: model, Variant: variant, TaskFile: taskID, TargetValid: target, MaxAttempts: maxAttempts,
			AttemptTimeout: timeout, TerminationGrace: grace, Image: image, AuthPath: auth,
			BaselineConfig: baseline, CandidateConfig: candidate, BuildImage: build, RepoRoot: repoRoot, MethodologyPath: specID,
			CacheControl: cacheControl, Warmup: warmup, WarmupSet: warmupSet, PricingSnapshot: pricingSnapshot, PricingSHA256: pricingSHA256,
			DockerBin: os.Getenv("DOCKER_BIN"), KTXBin: os.Getenv("KTX_BIN"),
			RegisteredInputs: true,
		})
		if err != nil {
			printError(stderr, usageError("ktx bench compare preflight: %v", err), opts.Format)
			return ExitUsage
		}
		if format == FormatText {
			fmt.Fprintf(stdout, "eligible=%t direct=%t controlled=%t secondary=%t model_attempts=%d artifact_creation=%t reasons=%s\n", report.Eligible, report.DirectEvidence, report.ControlledComparison, report.SecondaryEvidence, report.ModelAttempts, report.ArtifactCreation, strings.Join(report.Reasons, ","))
		} else if code := encodeExperimentJSON(stdout, report); code != ExitOK {
			return code
		}
		if !report.Eligible {
			return 1
		}
		return ExitOK
	}
	if specID == "" || model == "" || taskID == "" {
		printError(stderr, usageError("ktx bench compare: --spec, --model, and --task are required; use registered IDs with --spec, --task, --baseline, and --candidate"), opts.Format)
		return ExitUsage
	}
	if pricingSHA256 != "" && pricingSnapshot == "" {
		printError(stderr, usageError("ktx bench compare: --pricing-sha256 requires --pricing-snapshot"), opts.Format)
		return ExitUsage
	}
	runner := experiment.NewRunner()
	_, summary, code, err := runner.Compare(ctx, experiment.CompareOptions{
		Model: model, Variant: variant, TaskFile: taskID, TargetValid: target, MaxAttempts: maxAttempts,
		AttemptTimeout: timeout, TerminationGrace: grace, ExperimentID: experimentID,
		Image: image, AuthPath: auth, BaselineConfig: baseline,
		CandidateConfig: candidate, BuildImage: build, RepoRoot: repoRoot, MethodologyPath: specID,
		CacheControl: cacheControl, Warmup: warmup, WarmupSet: warmupSet,
		PricingSnapshot: pricingSnapshot, PricingSHA256: pricingSHA256,
		DockerBin: os.Getenv("DOCKER_BIN"), KTXBin: os.Getenv("KTX_BIN"),
		RegisteredInputs: true,
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
	var format string
	fs.StringVar(&format, "format", opts.Format, "json or text")
	if err := fs.Parse(args); err != nil || len(fs.Args()) != 1 {
		if err == nil {
			err = fmt.Errorf("usage: ktx bench validate <id>")
		}
		printError(stderr, usageError("ktx bench validate: %v", err), opts.Format)
		return ExitUsage
	}
	directory, err := resolveExperiment(fs.Arg(0))
	if err != nil {
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet("ktx bench rank", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var model, variant, specID, taskID, candidatesRaw, baselineID, auth, image, campaignID, format string
	var rounds, maxAttempts int
	var timeout, grace time.Duration
	fs.StringVar(&model, "model", "", "model id (provider/name, e.g. openai/gpt-5.6-luna with --variant high)")
	fs.StringVar(&model, "m", "", "model id (provider/name)")
	fs.StringVar(&variant, "variant", "", "model variant (e.g. high)")
	fs.StringVar(&specID, "spec", "", "registered benchmark spec ID")
	fs.StringVar(&taskID, "task", "", "registered task ID")
	fs.StringVar(&candidatesRaw, "candidate", "sanitize,keep-ends-mid,conservative-a,conservative-a-recover,aggressive-head", "comma-separated candidate pipeline IDs")
	fs.StringVar(&baselineID, "baseline", "passthrough", "registered baseline pipeline ID")
	fs.StringVar(&auth, "auth", "", "OpenCode auth.json")
	fs.StringVar(&image, "image", "ktx-bench", "Docker image")
	fs.StringVar(&campaignID, "campaign-id", "", "campaign id")
	fs.StringVar(&format, "format", opts.Format, "json or text")
	fs.IntVar(&rounds, "rounds", 5, "valid attempts per side")
	fs.IntVar(&maxAttempts, "max-attempts", 24, "attempt budget per comparison")
	fs.DurationVar(&timeout, "attempt-timeout", 15*time.Minute, "per-attempt timeout")
	fs.DurationVar(&grace, "termination-grace", 30*time.Second, "termination grace period")
	if err := fs.Parse(args); err != nil || specID == "" || model == "" || taskID == "" || len(fs.Args()) > 0 {
		if err == nil {
			err = fmt.Errorf("--spec, --model, and --task are required; selectors accept registered IDs")
		}
		printError(stderr, usageError("ktx bench rank: %v", err), opts.Format)
		return ExitUsage
	}
	var candidates []string
	for _, item := range strings.Split(candidatesRaw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		printError(stderr, usageError("ktx bench rank: no candidate pipelines specified"), opts.Format)
		return ExitUsage
	}
	registry := benchregistry.Active()
	for _, selector := range []struct {
		kind benchregistry.Kind
		id   string
	}{{benchregistry.Spec, specID}, {benchregistry.Task, taskID}} {
		if _, err := registry.Lookup(selector.kind, selector.id); err != nil {
			printError(stderr, usageError("ktx bench rank: %v", err), opts.Format)
			return ExitUsage
		}
	}
	for _, id := range append([]string{baselineID}, candidates...) {
		if _, err := registry.Lookup(benchregistry.Pipeline, id); err != nil {
			printError(stderr, usageError("ktx bench rank: %v", err), opts.Format)
			return ExitUsage
		}
	}
	if campaignID == "" {
		campaignID = "rank-" + time.Now().UTC().Format("20060102T150405Z")
	}
	if err := experiment.ValidateExperimentID(campaignID); err != nil {
		printError(stderr, usageError("ktx bench rank: %v", err), opts.Format)
		return ExitUsage
	}
	for _, candidateID := range candidates {
		if err := experiment.ValidateExperimentID(campaignID + "-" + candidateID); err != nil {
			printError(stderr, usageError("ktx bench rank: %v", err), opts.Format)
			return ExitUsage
		}
	}
	dataDir, err := experimentRoot()
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
		_, summary, code, err := runner.Compare(ctx, experiment.CompareOptions{
			Model: model, Variant: variant, TaskFile: taskID, TargetValid: rounds, MaxAttempts: maxAttempts,
			AttemptTimeout: timeout, TerminationGrace: grace, ExperimentID: experimentID,
			DataDir: dataDir, Image: image, AuthPath: auth,
			MethodologyPath: specID, BaselineConfig: baselineID, CandidateConfig: candidateID,
			DockerBin: os.Getenv("DOCKER_BIN"), KTXBin: os.Getenv("KTX_BIN"),
			RegisteredInputs: true,
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
	ranking := experiment.BuildRanking(experiment.RankingOptions{TaskFile: taskID, Model: model, Variant: variant, Rounds: rounds, BaselinePipeline: baselineID, Candidates: candidates}, pairs, time.Now())
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

func renderExperimentValue[T any](stdout io.Writer, value T, format string, textRenderer func(T) string) int {
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
