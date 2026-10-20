package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/experiment"
	benchdata "github.com/jgabor/ktx/tools/bench"
)

const optimizeLiveSchema = "ktx.optimize.result.v2"

type optimizeQualification struct {
	DevelopmentResultID  string                    `json:"development_result_id"`
	Scope                string                    `json:"scope"`
	Model                string                    `json:"model"`
	Variant              string                    `json:"variant,omitempty"`
	Experiment           string                    `json:"experiment"`
	AttemptLimit         int                       `json:"attempt_limit"`
	AttemptUnit          string                    `json:"attempt_unit"`
	AttemptsAdmitted     int                       `json:"attempts_admitted"`
	MinimumCompletePairs int                       `json:"minimum_complete_pairs"`
	CompletePairs        int                       `json:"complete_pairs"`
	WarmupsPerArm        int                       `json:"warmups_per_arm"`
	AttemptTimeout       string                    `json:"attempt_timeout"`
	TerminationGrace     string                    `json:"termination_grace"`
	PricingAuthority     string                    `json:"pricing_authority"`
	LocalBinarySHA256    string                    `json:"local_binary_sha256"`
	QualityPolicy        string                    `json:"quality_policy"`
	Freeze               map[string]string         `json:"freeze"`
	EvidenceHashes       map[string]string         `json:"evidence_hashes"`
	ValidationPassed     bool                      `json:"validation_passed"`
	CostSelection        *experiment.CostSelection `json:"cost_selection,omitempty"`
	SelectedCost         map[string]any            `json:"selected_cost,omitempty"`
}

type optimizeStop struct{ reason string }

func (s *optimizeStop) Error() string { return s.reason }

func optimizeMeasurementText(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%g", *value)
}

func qualifyOptimize(ctx context.Context, development optimizeResult, args optimizeArgs, runner *experiment.Runner, diagnostics io.Writer) (result optimizeResult, resultErr error) {
	r := development
	r.Schema, r.Mode, r.ApplyEligible = optimizeLiveSchema, "live", false
	r.Integrity = "captured_inputs_live_evidence_not_validated"
	r.Live = &optimizeQualification{
		DevelopmentResultID: development.ID, Scope: "independent controlled release-control behavioral cohort; not representative user workload",
		Model: args.model, Variant: args.variant, Experiment: "experiments/qualification", AttemptLimit: args.maxAttempts,
		MinimumCompletePairs: 5, WarmupsPerArm: 1, AttemptTimeout: "10m0s", TerminationGrace: "30s", PricingAuthority: "unavailable rates; runtime-reported cost may remain usable", Freeze: map[string]string{}, EvidenceHashes: map[string]string{},
	}
	r.Live.QualityPolicy = "both arm quality gates qualified; candidate measured full-pass rate must not be lower than baseline"
	r.Live.AttemptUnit = "model-bearing benchmark container/agent launch, including cold observations, warmups and retries; capability inspection has no agent/model call and is excluded; not provider requests or USD"
	r.Bounds.ProviderAttempts = args.maxAttempts
	r.RuntimeTokens = optimizeUnavailable{State: "unavailable", Reason: "live_qualification_not_completed"}
	r.Cost = optimizeUnavailable{State: "unavailable", Reason: "live_cost_not_measured"}
	r.MissingInputs = []string{"real_workload_evidence"}
	root, err := filepath.Abs(filepath.Join(".ktx", "optimizations"))
	if err != nil {
		return r, err
	}
	dir, err := os.MkdirTemp(root, ".live-pending-")
	if err != nil {
		return r, internalError("create private live result")
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil && resultErr == nil {
			resultErr = internalError("remove pending live result: %v", err)
		}
	}()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return r, err
	}
	r.ID = hex.EncodeToString(id[:])
	// Fork a new result identity. The completed development result stays immutable.
	for _, name := range append([]string{"baseline.toml", "population.json"}, optimizeCandidateFiles(development)...) {
		data, err := optimizeRead(filepath.Join(root, development.ID, name), optimizeConfigLimit)
		if err != nil {
			if development.Baseline == nil && (name == "baseline.toml" || name == "population.json") {
				continue
			}
			if name == "population.json" && development.Scope.PopulationSHA256 == "" {
				continue
			}
			return r, internalError("copy captured development input")
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return r, err
		}
	}
	if development.Outcome != "screened" || development.Baseline == nil || development.SelectedCandidate == nil || development.BaselineScreen == nil || !development.BaselineScreen.ControlledFactsPass {
		r.Outcome = "blocked"
		r.Reasons = append(r.Reasons, "development_screen_not_qualified")
		r.NextActions = []string{"fix_configuration_and_pass_development_screen"}
		return publishOptimize(dir, root, r)
	}
	baseline, err := readOptimizeArtifact(dir, development.Baseline.Artifact, optimizeConfigLimit)
	if err != nil {
		return r, err
	}
	selected, err := readOptimizeArtifact(dir, development.SelectedCandidate.Artifact, optimizeConfigLimit)
	if err != nil {
		return r, err
	}
	if _, _, err := optimizeStageOnlyDiff(baseline, selected); err != nil {
		r.Outcome, r.ApplyEligible = "blocked", false
		r.Reasons = []string{"unsupported_changes_outside_pipeline_default_stages"}
		r.NextActions = []string{"change_only_pipeline_default_stages_and_start_a_new_cycle"}
		return publishOptimize(dir, root, r)
	}
	baselineCfg, err := config.LoadFile(filepath.Join(dir, development.Baseline.Artifact))
	if err != nil {
		return r, err
	}
	inputs := filepath.Join(dir, "qualification-inputs")
	assets, err := benchdata.OptimizationAssets()
	if err != nil {
		return r, err
	}
	methodology, _, err := benchdata.ReadMethodology("recovery-aware-comparison")
	if err != nil {
		return r, err
	}
	paths := map[string][]byte{
		"tools/bench/specs/recovery-aware-comparison.yaml": methodology,
		"tools/bench/tasks/qualification.toml":             assets["qualification.toml"],
		"tools/bench/corpus/qualification.txt":             assets["qualification.txt"],
	}
	for path, data := range paths {
		destination := filepath.Join(inputs, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return r, err
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			return r, err
		}
		r.Live.Freeze["qualification-inputs/"+path] = optimizeHash(data)
	}
	provider, model, ok := strings.Cut(args.model, "/")
	if !ok || provider == "" || model == "" {
		return r, usageError("live qualification requires a provider/name model")
	}
	// Freeze explicit price inputs, including explicit absence of usable rates.
	// Do not turn pricing-service availability into a token-qualification gate.
	pricing, err := json.Marshal(map[string]any{"provider": provider, "model": model, "price": map[string]any{"input": nil, "output": nil, "source_url": "unavailable"}})
	if err != nil {
		return r, err
	}
	if args.pricing != "" {
		pricing, err = optimizeRead(args.pricing, optimizeConfigLimit)
		if err != nil {
			r.Outcome = "blocked"
			r.Reasons = []string{"pricing_snapshot_unreadable"}
			return publishOptimize(dir, root, r)
		}
		r.Live.PricingAuthority = "explicit frozen pricing snapshot; whole-cohort reported-first selection"
	}
	pricePath := filepath.Join(dir, "pricing-input.json")
	if err := os.WriteFile(pricePath, pricing, 0o600); err != nil {
		return r, err
	}
	r.Live.Freeze["pricing-input.json"] = optimizeHash(pricing)
	r.Live.Freeze["baseline.toml"] = development.Baseline.SHA256
	r.Live.Freeze[development.SelectedCandidate.Artifact] = development.SelectedCandidate.SHA256
	options := experiment.CompareOptions{
		Model: args.model, Variant: args.variant, TaskFile: filepath.Join(inputs, "tools/bench/tasks/qualification.toml"),
		TargetValid: 5, MaxAttempts: args.maxAttempts, MinimumCompletePairs: 5, Warmup: 1, WarmupSet: true, CacheControl: "observed",
		AttemptTimeout: 10 * time.Minute, TerminationGrace: 30 * time.Second, ExperimentID: "qualification", DataDir: filepath.Join(dir, "experiments"),
		Image: "ktx-bench", RepoRoot: inputs, MethodologyPath: filepath.Join(inputs, "tools/bench/specs/recovery-aware-comparison.yaml"),
		BaselineConfig: filepath.Join(dir, "baseline.toml"), CandidateConfig: filepath.Join(dir, development.SelectedCandidate.Artifact),
		PricingSnapshot: pricePath, PricingSHA256: optimizeHash(pricing), RegisteredInputs: false, RetainFailedAttempts: true,
	}
	task, err := experiment.LoadTask(options.TaskFile)
	if err != nil {
		return r, err
	}
	options.Task = task
	guard := &optimizeAdmission{dir: dir, development: development, result: &r, diagnostics: diagnostics, task: task, frozen: map[string]string{}, routes: baselineCfg.Routes}
	binary, err := os.Executable()
	if err != nil {
		return r, err
	}
	binaryHash, err := optimizeFileHash(binary)
	if err != nil {
		return r, err
	}
	r.Live.LocalBinarySHA256 = binaryHash
	guard.frozen[binary] = binaryHash
	options.KTXBin = binary
	options.AdmitAttempt = guard.admit
	runner.Diagnostics = diagnostics
	fmt.Fprintf(diagnostics, "ktx optimize: live attempt bound %d container/agent launches for the whole cycle, including cold observations, warmups and retries; not provider requests or a USD cap. Controlled qualification only.\n", args.maxAttempts)
	experimentDir, _, code, compareErr := runner.Compare(ctx, options)
	r.Outcome, r.ApplyEligible = "incomplete", false
	r.Reasons = []string{"controlled_qualification_incomplete"}
	r.NextActions = []string{"inspect_retained_partial_evidence", "start_a_new_explicit_cycle_if_inputs_are_correct"}
	if ctx.Err() != nil {
		r.Reasons = []string{"cancelled"}
	} else if compareErr != nil {
		var stop *optimizeStop
		if errors.As(compareErr, &stop) {
			r.Outcome = "blocked"
			r.Reasons = []string{stop.reason}
			r.Integrity = "live_admission_evidence_validation_failed"
		} else {
			r.Outcome = "blocked"
			r.Reasons = []string{"benchmark_preflight_or_evidence_failed"}
		}
	} else if code != 0 || r.Live.AttemptsAdmitted >= args.maxAttempts {
		r.Reasons = []string{"attempt_bound_exhausted_or_target_unmet"}
	}
	if experimentDir != "" {
		if compareErr == nil {
			if err := guard.verify(); err != nil {
				r.Outcome = "blocked"
				r.Reasons = []string{"frozen_evidence_changed"}
				r.Integrity = "frozen_evidence_validation_failed"
			} else if err := optimizeRetainedRouting(experimentDir, guard.routes); err != nil {
				var stop *optimizeStop
				r.Outcome = "blocked"
				r.Reasons = []string{"retained_evidence_invalid"}
				if errors.As(err, &stop) {
					r.Reasons = []string{stop.reason}
				}
				r.Integrity = "retained_routing_validation_failed"
			} else {
				regenerated, err := experiment.RegenerateSummary(experimentDir)
				if err != nil {
					r.Outcome = "blocked"
					r.Reasons = []string{"retained_evidence_invalid"}
					r.Integrity = "retained_evidence_validation_failed"
				} else {
					view, costErr := experiment.LoadCurrentCostView(experimentDir)
					if costErr != nil {
						r.Outcome = "blocked"
						r.Reasons = []string{"cost_provenance_or_integrity_failed"}
						r.Integrity = "cost_evidence_validation_failed"
					} else {
						costSelection := view.CostSelection
						r.Live.CostSelection = &costSelection
						r.Live.SelectedCost = view.SelectedCost
						r.Cost = optimizeUnavailable{State: costSelection.Basis, Reason: costSelection.Reason, Unit: costSelection.Unit}
						if _, err := experiment.RequireValidExperiment(experimentDir); err == nil && ctx.Err() == nil && code == 0 {
							r.Live.ValidationPassed = true
							r.Integrity = "validated_retained_experiment_and_frozen_inputs"
						}
						assessOptimizeQualification(&r, regenerated.Summary)
						if code != 0 {
							r.Reasons = append(r.Reasons, "attempt_bound_exhausted_or_target_unmet")
						}
					}
				}
			}
		}
		// Seal retained successful and unsuccessful artifacts for read-only show.
		hashes, err := optimizeTreeHashes(dir, "experiments")
		if err != nil {
			return r, internalError("seal retained qualification evidence: %v", err)
		}
		r.Live.EvidenceHashes = hashes
	}
	return publishOptimize(dir, root, r)
}

func optimizeCandidateFiles(r optimizeResult) []string {
	files := []string{}
	for _, c := range r.Candidates {
		files = append(files, c.Config.Artifact)
	}
	return files
}

type optimizeAdmission struct {
	dir         string
	development optimizeResult
	result      *optimizeResult
	diagnostics io.Writer
	task        experiment.Task
	frozen      map[string]string
	routes      []config.RouteRule
}

func (g *optimizeAdmission) verify() error {
	for relative, expected := range g.result.Live.Freeze {
		actual, err := optimizeFileHash(filepath.Join(g.dir, filepath.FromSlash(relative)))
		if err != nil || actual != expected {
			return &optimizeStop{"frozen_inputs_changed"}
		}
	}
	for path, expected := range g.frozen {
		actual, err := optimizeFileHash(path)
		if err != nil || actual != expected {
			return &optimizeStop{"retained_evidence_changed"}
		}
	}
	return nil
}

func (g *optimizeAdmission) admit(ctx context.Context, launch experiment.AttemptAdmission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.result.Live.AttemptsAdmitted >= g.result.Live.AttemptLimit {
		return &optimizeStop{"attempt_bound_exhausted"}
	}
	if _, err := loadOptimizeResult(g.development.ID); err != nil {
		return &optimizeStop{"development_evidence_changed"}
	}
	if err := g.verify(); err != nil {
		return err
	}
	m := launch.Manifest
	if m.Model != g.result.Live.Model || m.Methodology.Version != "v7-recovery-aware-comparison" || !reflect.DeepEqual(m.Task, g.task) || m.MaxAttempts != g.result.Live.AttemptLimit || m.TargetValidPerPipeline != 5 || m.CacheControl == nil || m.CacheControl.Mode != "observed" || m.CacheControl.WarmupCount != 1 {
		return &optimizeStop{"frozen_execution_contract_mismatch"}
	}
	variant := ""
	if m.Variant != nil {
		variant = *m.Variant
	}
	if variant != g.result.Live.Variant || m.AttemptTimeout != g.result.Live.AttemptTimeout || m.TerminationGrace != g.result.Live.TerminationGrace || m.Pricing == nil || m.Pricing.SHA256 != g.result.Live.Freeze["pricing-input.json"] || m.Methodology.SHA256 != g.result.Live.Freeze["qualification-inputs/tools/bench/specs/recovery-aware-comparison.yaml"] {
		return &optimizeStop{"frozen_runtime_scoring_or_pricing_mismatch"}
	}
	for side, expected := range map[string]*optimizeConfigIdentity{experiment.Baseline: g.development.Baseline, experiment.Candidate: g.development.SelectedCandidate} {
		captured := m.Pipelines[side]
		if captured.SHA256 != expected.SHA256 {
			return &optimizeStop{"captured_full_config_mismatch"}
		}
		path := filepath.Join(launch.Directory, filepath.FromSlash(captured.Path))
		actual, err := optimizeFileHash(path)
		if err != nil || actual != expected.SHA256 {
			return &optimizeStop{"captured_full_config_changed"}
		}
		cfg, err := config.LoadFile(path)
		if err != nil || len(optimizeSupported(cfg)) > 0 {
			return &optimizeStop{"pre_delivery_routing_authority_missing"}
		}
	}
	workspaceInput := filepath.Join(launch.Directory, "attempts", launch.AttemptID, "workspace/tools/bench/corpus/qualification.txt")
	workspaceHash, err := optimizeFileHash(workspaceInput)
	if err != nil || workspaceHash != g.result.Live.Freeze["qualification-inputs/tools/bench/corpus/qualification.txt"] {
		return &optimizeStop{"qualification_delivery_input_changed"}
	}
	if g.result.Live.AttemptsAdmitted == 0 {
		g.result.Live.Freeze["experiments/qualification/manifest.json"] = mustManifestHash(m)
		for path, expected := range g.result.Live.Freeze {
			if strings.HasPrefix(path, "qualification-inputs/") {
				g.result.Live.Freeze["experiments/qualification/repository/"+strings.TrimPrefix(path, "qualification-inputs/")] = expected
			}
		}
		for _, relative := range []string{m.Pricing.Path, m.Pipelines[experiment.Baseline].Path, m.Pipelines[experiment.Candidate].Path} {
			hash, err := optimizeFileHash(filepath.Join(launch.Directory, relative))
			if err != nil {
				return err
			}
			g.result.Live.Freeze["experiments/qualification/"+relative] = hash
		}
		// Manifest image ID freezes the container binary, runtime and generated adapter.
		freezeBytes, _ := json.Marshal(map[string]any{"image": m.Image, "runtime": m.Identities, "local_binary_sha256": g.result.Live.LocalBinarySHA256, "configs": map[string]string{"baseline": m.Pipelines[experiment.Baseline].SHA256, "candidate": m.Pipelines[experiment.Candidate].SHA256}, "methodology": m.Methodology, "task_sha256": optimizeHash(mustJSON(m.Task)), "pricing_sha256": m.Pricing.SHA256, "max_attempts": m.MaxAttempts})
		fmt.Fprintf(g.diagnostics, "ktx optimize: frozen before first model launch: %s\n", freezeBytes)
		if err := g.verify(); err != nil {
			return err
		}
	} else {
		if err := experiment.ValidateCollectedAttempts(launch.Directory); err != nil {
			return &optimizeStop{"prior_attempt_evidence_invalid"}
		}
		// Stop paid launches as soon as an agent call reaches an unexercised route.
		if err := optimizeRetainedRouting(launch.Directory, g.routes); err != nil {
			var stop *optimizeStop
			if errors.As(err, &stop) {
				return err
			}
			return &optimizeStop{"prior_attempt_evidence_invalid"}
		}
		attempts, err := experiment.LoadAttempts(launch.Directory)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			hashes, err := optimizeTreeHashes(g.dir, "experiments/qualification/attempts/"+attempt.AttemptID)
			if err != nil {
				return err
			}
			for relative, hash := range hashes {
				g.frozen[filepath.Join(g.dir, relative)] = hash
			}
		}
	}
	g.result.Live.AttemptsAdmitted++ // Never refund a launched attempt after collection failure.
	return nil
}

// optimizeRetainedRouting refuses qualification evidence in which a persisted
// tool call selected a route that screening admitted only as unreached.
func optimizeRetainedRouting(experimentDir string, routes []config.RouteRule) error {
	retained, err := experiment.RetainedToolCalls(experimentDir)
	if err != nil {
		return err
	}
	calls := make([]*dbpkg.ToolCall, 0, len(retained))
	for _, call := range retained {
		calls = append(calls, &dbpkg.ToolCall{Tool: call.Tool, Input: call.Input, IsError: call.IsError})
	}
	if optimizeUnqualifiedReach(calls, routes) {
		return &optimizeStop{"qualification_reached_unqualified_route"}
	}
	return nil
}

func mustJSON(value any) []byte { data, _ := json.Marshal(value); return data }
func mustManifestHash(m experiment.Manifest) string {
	data, _ := experiment.MarshalJSON(m)
	return optimizeHash(data)
}

func optimizeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular evidence file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func optimizeTreeHashes(root, relative string) (map[string]string, error) {
	hashes := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(relative)), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in controlled evidence")
		}
		if entry.IsDir() {
			return nil
		}
		hash, err := optimizeFileHash(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hashes[filepath.ToSlash(rel)] = hash
		return nil
	})
	return hashes, err
}

func assessOptimizeQualification(r *optimizeResult, summary experiment.Summary) {
	// Decode only the existing primary authorities, never copied ledger metrics.
	var comparison struct {
		Arms map[string]struct {
			Quality struct {
				Qualified bool `json:"qualified"`
				Correct   int  `json:"correct_task_count"`
				Evaluated int  `json:"evaluated_task_count"`
			} `json:"quality_gate"`
			Tokens struct {
				State string   `json:"state"`
				Value *float64 `json:"value"`
			} `json:"runtime_total_tokens_per_correct_task"`
		} `json:"arms"`
		Pairs []struct {
			Baseline struct {
				Correct bool     `json:"correct"`
				Tokens  *float64 `json:"runtime_total_tokens"`
			} `json:"baseline"`
			Candidate struct {
				Correct bool     `json:"correct"`
				Tokens  *float64 `json:"runtime_total_tokens"`
			} `json:"candidate"`
		} `json:"paired_cycles"`
	}
	if err := json.Unmarshal(mustJSON(summary["recovery_aware_comparison"]), &comparison); err != nil {
		r.Reasons = []string{"qualification_primary_authority_missing"}
		return
	}
	baseline, candidate := comparison.Arms[experiment.Baseline], comparison.Arms[experiment.Candidate]
	for _, pair := range comparison.Pairs {
		if pair.Baseline.Correct && pair.Candidate.Correct && pair.Baseline.Tokens != nil && pair.Candidate.Tokens != nil {
			r.Live.CompletePairs++
		}
	}
	if !baseline.Quality.Qualified || !candidate.Quality.Qualified {
		r.Reasons = []string{"behavioral_quality_gate_failed"}
		return
	}
	if baseline.Quality.Evaluated == 0 || candidate.Quality.Evaluated == 0 || candidate.Quality.Correct*baseline.Quality.Evaluated < baseline.Quality.Correct*candidate.Quality.Evaluated {
		r.Reasons = []string{"behavioral_quality_not_preserved"}
		return
	}
	if baseline.Tokens.State != "available" || candidate.Tokens.State != "available" || baseline.Tokens.Value == nil || candidate.Tokens.Value == nil {
		r.Reasons = []string{"complete_runtime_tokens_unavailable"}
		return
	}
	r.RuntimeTokens = optimizeUnavailable{State: "available", Reason: "validated_runtime_input_output_reasoning_per_correct_controlled_task", Unit: "tokens_per_correct_task", Baseline: baseline.Tokens.Value, Candidate: candidate.Tokens.Value}
	if r.Live.CompletePairs < r.Live.MinimumCompletePairs {
		r.Reasons = []string{"insufficient_complete_qualified_pairs"}
		return
	}
	if !r.Live.ValidationPassed {
		r.Reasons = []string{"retained_qualification_not_validated"}
		return
	}
	if *candidate.Tokens.Value >= *baseline.Tokens.Value {
		r.Outcome = "no_change"
		r.Reasons = []string{"no_quality_preserving_runtime_token_improvement"}
		return
	}
	r.Outcome, r.ApplyEligible = "ready", true
	r.Quality = "development_and_controlled_behavioral_gates_passed"
	r.Integrity = "validated_retained_experiment_and_frozen_inputs"
	r.Reasons = []string{"qualified_controlled_scope_only_not_user_workload"}
	r.NextActions = []string{"inspect_result", "preview_with_optimize_apply_dry_run", "explicit_apply_requires_unchanged_captured_target"}
}

func inspectOptimizeQualification(dir string, r optimizeResult) error {
	if r.Mode != "live" || r.Live == nil || r.Live.AttemptsAdmitted > r.Live.AttemptLimit || (r.Outcome != "ready" && r.ApplyEligible) {
		return fmt.Errorf("invalid live result contract")
	}
	for relative, expected := range r.Live.Freeze {
		if !filepath.IsLocal(relative) {
			return fmt.Errorf("nonlocal frozen input")
		}
		file, err := experiment.OpenArchiveFile(dir, relative)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, readErr := io.Copy(h, file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != expected {
			return fmt.Errorf("frozen input changed")
		}
	}
	for relative, expected := range r.Live.EvidenceHashes {
		if !filepath.IsLocal(relative) {
			return fmt.Errorf("nonlocal retained evidence")
		}
		file, err := experiment.OpenArchiveFile(dir, relative)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, readErr := io.Copy(h, file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != expected {
			return fmt.Errorf("retained evidence changed")
		}
	}
	if r.Outcome == "ready" {
		if !r.ApplyEligible || !r.Live.ValidationPassed || r.Live.CompletePairs < 5 || r.RuntimeTokens.State != "available" || r.RuntimeTokens.Baseline == nil || r.RuntimeTokens.Candidate == nil || *r.RuntimeTokens.Candidate >= *r.RuntimeTokens.Baseline {
			return fmt.Errorf("ready evidence incomplete")
		}
		if r.Live.Experiment != "experiments/qualification" {
			return fmt.Errorf("invalid qualification archive")
		}
		_, err := experiment.RequireValidExperiment(filepath.Join(dir, r.Live.Experiment))
		return err
	}
	return nil
}
