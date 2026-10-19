package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/experiment"
	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/pipeline/stages"
	"github.com/jgabor/ktx/internal/router"
	"github.com/jgabor/ktx/internal/version"
)

const (
	optimizeSchema      = "ktx.optimize.result.v1"
	optimizeConfigLimit = 64 << 10
	// The embedded qualification prompt asks the agent to run this command.
	optimizeQualificationCommand = "cat tools/bench/corpus/qualification.txt"
)

type optimizeArgs struct {
	config, candidate, model, variant, pricing string
	live                                       bool
	dryRun                                     bool
	maxAttempts                                int
	maxAttemptsSet                             bool
}

type optimizeConfigIdentity struct {
	SHA256   string `json:"sha256"`
	Artifact string `json:"artifact"`
}

type optimizeTarget struct {
	Path   string  `json:"path"`
	Exists *bool   `json:"exists"`
	SHA256 *string `json:"sha256"`
}

type optimizeCandidate struct {
	Name    string                 `json:"name"`
	Config  optimizeConfigIdentity `json:"config"`
	Outcome string                 `json:"outcome"`
	Reasons []string               `json:"reasons"`
	Screen  *screenResult          `json:"screen"`
}

type optimizeUnavailable struct {
	State     string   `json:"state"`
	Reason    string   `json:"reason"`
	Unit      string   `json:"unit,omitempty"`
	Baseline  *float64 `json:"baseline,omitempty"`
	Candidate *float64 `json:"candidate,omitempty"`
}

type optimizeResult struct {
	Schema         string                  `json:"schema"`
	ID             string                  `json:"result_id"`
	Outcome        string                  `json:"outcome"`
	Mode           string                  `json:"mode"`
	BinaryVersion  string                  `json:"binary_version"`
	Target         optimizeTarget          `json:"target"`
	Baseline       *optimizeConfigIdentity `json:"baseline"`
	BaselineScreen *screenResult           `json:"baseline_screen"`
	Scope          struct {
		Population       string `json:"population"`
		PopulationSHA256 string `json:"population_sha256"`
		WorkloadEvidence bool   `json:"workload_evidence"`
		Routing          string `json:"routing"`
		SupportedChanges string `json:"supported_changes"`
	} `json:"scope"`
	Bounds struct {
		MaxCandidates           int `json:"max_candidates"`
		CasesPerConfig          int `json:"cases_per_config"`
		MaxCaseBytes            int `json:"max_case_bytes"`
		MaxConfigBytes          int `json:"max_config_bytes"`
		MaxProfiles             int `json:"max_profiles"`
		MaxStagesPerProfile     int `json:"max_stages_per_profile"`
		MaxNumericStageArgument int `json:"max_numeric_stage_argument"`
		ProviderAttempts        int `json:"provider_attempts"`
	} `json:"bounds"`
	Quality           string                  `json:"quality"`
	Integrity         string                  `json:"integrity"`
	RuntimeTokens     optimizeUnavailable     `json:"runtime_tokens"`
	Cost              optimizeUnavailable     `json:"cost"`
	ApplyEligible     bool                    `json:"apply_eligible"`
	Candidates        []optimizeCandidate     `json:"candidates"`
	SelectedCandidate *optimizeConfigIdentity `json:"selected_candidate"`
	Reasons           []string                `json:"reasons"`
	MissingInputs     []string                `json:"missing_inputs"`
	NextActions       []string                `json:"next_actions"`
	NextCommands      []string                `json:"next_commands"`
	LiveRequirements  []string                `json:"live_requirements"`
	Live              *optimizeQualification  `json:"qualification,omitempty"`
	Application       *optimizeApplication    `json:"application,omitempty"`
}

func init() {
	Register("optimize", RegisterOptions{
		Description:    "Screen and qualify configs; inspect, preview, apply, or roll back results",
		DatabaseAccess: DatabaseNone, FormatDefault: FormatJSON,
		Help: printOptimizeHelp, ParseArgs: parseOptimizeArgs,
	}, optimizeCommand)
}

func parseOptimizeArgs(args []string) ([]string, any, error) {
	parsed := optimizeArgs{maxAttempts: 24}
	var rest []string
	for i, arg := range args {
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--dry-run", "-dry-run":
			parsed.dryRun = true
			if hasValue {
				var err error
				parsed.dryRun, err = strconv.ParseBool(value)
				if err != nil {
					return nil, nil, fmt.Errorf("--dry-run requires a boolean")
				}
			}
		case "--live", "-live":
			parsed.live = true
			if hasValue {
				var err error
				parsed.live, err = strconv.ParseBool(value)
				if err != nil {
					return nil, nil, fmt.Errorf("--live requires a boolean")
				}
			}
		case "--config", "-config", "--candidate", "-candidate", "--model", "-model", "--variant", "-variant", "--pricing-snapshot", "-pricing-snapshot", "--max-attempts", "-max-attempts":
			if !hasValue || value == "" {
				kind := map[string]string{"model": "provider/name", "variant": "name", "max-attempts": "number"}[strings.TrimLeft(name, "-")]
				if kind == "" {
					kind = "path"
				}
				return nil, nil, fmt.Errorf("%s requires a nonempty %s", name, kind)
			}
			switch strings.TrimLeft(name, "-") {
			case "model":
				parsed.model = value
			case "variant":
				parsed.variant = value
			case "pricing-snapshot":
				parsed.pricing = value
			case "max-attempts":
				var err error
				parsed.maxAttempts, err = strconv.Atoi(value)
				parsed.maxAttemptsSet = true
				if err != nil || parsed.maxAttempts < 14 || parsed.maxAttempts > 64 {
					return nil, nil, fmt.Errorf("--max-attempts must be 14..64 container/agent launches, not requests or USD")
				}
			case "candidate":
				parsed.candidate = value
			default:
				parsed.config = value
			}
		default:
			rest = append(rest, arg)
		}
	}
	return rest, parsed, nil
}

func optimizeCommand(ctx *DispatchContext) (any, error) {
	args := ctx.ParsedArgs.(optimizeArgs)
	if args.live && (args.model == "" || strings.HasPrefix(args.model, "github-copilot/") || !strings.Contains(args.model, "/")) {
		return nil, usageError("--live requires an explicit permitted --model provider/name")
	}
	if !args.live && (args.model != "" || args.variant != "" || args.pricing != "" || args.maxAttemptsSet) {
		return nil, usageError("model, variant, pricing snapshot and attempt limits require --live")
	}
	var result optimizeResult
	if len(ctx.Args) > 0 && (ctx.Args[0] == "apply" || ctx.Args[0] == "rollback") {
		if len(ctx.Args) != 2 || args.config != "" || args.candidate != "" || args.live || (ctx.Args[0] == "rollback" && args.dryRun) {
			return nil, usageError("use ktx optimize apply <result-id> [--dry-run] or ktx optimize rollback <result-id>; target comes only from the captured result")
		}
		report, err := mutateOptimize(ctx.Args[1], ctx.Args[0], args.dryRun, optimizeMutationHooks{})
		if err != nil {
			return nil, err
		}
		if ctx.Format == FormatText {
			err = renderOptimizeMutation(ctx.Stdout, report)
		} else {
			err = json.NewEncoder(ctx.Stdout).Encode(report)
		}
		return report, err
	}
	if args.dryRun {
		return nil, usageError("--dry-run belongs to optimize apply")
	}
	var err error
	if len(ctx.Args) > 0 {
		if len(ctx.Args) != 2 || ctx.Args[0] != "show" || args.config != "" || args.candidate != "" || args.live {
			return nil, usageError("use ktx optimize [--config path] [--candidate path] or ktx optimize show <result-id>")
		}
		result, err = loadOptimizeResult(ctx.Args[1])
	} else {
		result, err = screenOptimize(args)
		if err == nil && args.live {
			liveContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			result, err = qualifyOptimize(liveContext, result, args, experiment.NewRunner(), ctx.Stderr)
		}
	}
	if err != nil {
		return nil, err
	}
	if ctx.Format == FormatText {
		err = renderOptimizeText(ctx.Stdout, result)
	} else {
		err = json.NewEncoder(ctx.Stdout).Encode(result)
	}
	return result, err
}

func printOptimizeHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: ktx optimize [--config path] [--candidate proposal.toml] [--format json|text]")
	fmt.Fprintln(w, "       ktx optimize show <result-id> [--format json|text]")
	fmt.Fprintln(w, "       ktx optimize apply <result-id> [--dry-run] [--format json|text]")
	fmt.Fprintln(w, "       ktx optimize rollback <result-id> [--format json|text]")
	fmt.Fprintln(w, "       ktx optimize --live --model provider/name [--variant name] [--max-attempts 14..64]")
	fmt.Fprintln(w, "                    [--pricing-snapshot file] [--config path] [--candidate proposal.toml]")
	fmt.Fprintln(w, "Offline by default: two incumbent-derived proposals and at most one supplied full config.")
	fmt.Fprintln(w, "Offline tests fixed generated cases only: no providers, database access, config changes, or apply-ready claims.")
	fmt.Fprintln(w, "Explicit live consent qualifies one proposal on an independent controlled behavioral corpus, not user workload.")
	fmt.Fprintln(w, "Live requires the prebuilt pinned ktx-bench image and OpenCode auth. No image build or automatic apply.")
	fmt.Fprintln(w, "Default bound: 24 agent launches including cold observations, warmups, retries; not provider requests or a USD cap.")
	fmt.Fprintln(w, "Capability inspection has no agent/model call and is outside that attempt count.")
	fmt.Fprintln(w, "Ready requires five complete qualified paired cycles and complete runtime-token improvement.")
	fmt.Fprintln(w, "Without an explicit pricing snapshot, API estimates are unavailable; reported cost remains independent.")
	fmt.Fprintln(w, "Without --config, targets the effective user config (including original absence).")
	fmt.Fprintln(w, "External plugins, outcome routes, and unsupported stages block before execution. Command, subcommand,")
	fmt.Fprintln(w, "and other-tool routes stay unchanged and block only if a controlled or qualification call reaches them.")
	fmt.Fprintln(w, "Targets apply cannot replace exactly block during screening: symlinks or symlinked parents,")
	fmt.Fprintln(w, "extra hard links, files owned by another user, or setuid/setgid/sticky bits.")
	fmt.Fprintln(w, "Private full configs and evidence are retained in .ktx/optimizations/<result-id>.")
	fmt.Fprintln(w, "Treat these artifacts as sensitive. show verifies captured identities and does not show config contents.")
	fmt.Fprintln(w, "Outcomes: screened, ready, no_change, blocked, incomplete. Ready claims only the retained controlled scope.")
	fmt.Fprintln(w, "Only pipeline.default.stages may change. Routes, other profiles, timeout/recovery, logging and plugins must stay unchanged.")
	fmt.Fprintln(w, "Unsupported proposals block during screening, before live preflight or paid launches. Built-in proposals preserve other config bytes.")
	fmt.Fprintln(w, "Built-in edits accept a plain [pipeline.default] table and stages array (or an absent table); ambiguous syntax blocks.")
	fmt.Fprintln(w, "Apply revalidates qualification and the captured target; it never merges, forces, or chooses today's default target.")
	fmt.Fprintln(w, "Preview writes nothing. Apply retains private originals; retries are no-ops. Rollback refuses later byte or mode changes.")
	fmt.Fprintln(w, "Directory leases coordinate optimize mutations; external editors must cooperate for full lost-update protection.")
	fmt.Fprintln(w, "Rolled-back results are retired. show retains the original qualification and reports application state separately.")
	fmt.Fprintln(w, "Live prerequisites: docker on PATH, an existing ktx-bench image with pinned OpenCode 1.18.5/collector support,")
	fmt.Fprintln(w, "and OpenCode auth at $HOME/.local/share/opencode/auth.json. Missing/mismatched inputs block before model calls.")
	fmt.Fprintln(w, "Qualification task, corpus and scoring spec are embedded; no source checkout is required and optimize never builds an image.")
	fmt.Fprintln(w, "Exit 0: report produced, including blocked/incomplete/no_change; inspect outcome and apply_eligible, not exit alone.")
	fmt.Fprintln(w, "Errors: 1 usage, 2 missing result, 3 apply/rollback conflict, 4 I/O/integrity. Commands never prompt.")
	fmt.Fprintln(w, "Start: ktx optimize --format text; inspect: ktx optimize show <result-id> --format text.")
	fmt.Fprintln(w, "Only with explicit paid consent: ktx optimize --live --model provider/name. Then apply <result-id> --dry-run before apply.")
}

func optimizeHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func optimizeRead(path string, limit int) ([]byte, error) {
	f, err := openOptimizeInput(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a readable regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err == nil && len(data) > limit {
		err = fmt.Errorf("file exceeds size bound")
	}
	return data, err
}

func screenOptimize(args optimizeArgs) (result optimizeResult, resultErr error) {
	r := optimizeResult{
		Schema: optimizeSchema, Outcome: "blocked", Mode: "offline", BinaryVersion: version.Version,
		Quality: "not_run", Integrity: "captured_sha256", Candidates: []optimizeCandidate{}, Reasons: []string{},
		RuntimeTokens: optimizeUnavailable{State: "unavailable", Reason: "no_runtime_measurement_offline"}, Cost: optimizeUnavailable{State: "unavailable", Reason: "no_provider_cost_offline"},
		MissingInputs: []string{"real_workload_evidence", "independent_behavioral_qualification", "runtime_token_measurement"},
		NextActions:   []string{"inspect_result", "qualify_one_full_config_with_explicit_live_consent_before_apply"},
	}
	r.Scope.Population = "generated_preservation_v1"
	r.Scope.Routing = "controlled bash/read/view_image through default and tool-only any-outcome routes; command, subcommand and other-tool routes stay unchanged and unexercised; unknown is not success"
	r.Scope.SupportedChanges = "pipeline.default.stages only; all unrelated settings and route order unchanged"
	r.Bounds.MaxCandidates, r.Bounds.CasesPerConfig, r.Bounds.MaxCaseBytes = 3, 7, 8192
	r.Bounds.MaxConfigBytes, r.Bounds.MaxProfiles, r.Bounds.MaxStagesPerProfile = optimizeConfigLimit, 16, 32
	r.Bounds.MaxNumericStageArgument = 1 << 20
	path := args.config
	if path == "" {
		var err error
		path, err = config.ResolveUserConfigPath()
		if err != nil {
			return r, internalError("resolve configuration target")
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return r, usageError("invalid configuration path")
	}
	r.Target.Path = path
	root := filepath.Join(".ktx", "optimizations")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return r, internalError("create optimization archive: %v", err)
	}
	dir, err := os.MkdirTemp(root, ".pending-")
	if err != nil {
		return r, internalError("create private optimization archive: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil && resultErr == nil {
			resultErr = internalError("remove pending optimization archive: %v", err)
		}
	}()
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return r, internalError("create result identity")
	}
	r.ID = hex.EncodeToString(idBytes[:])
	baselineBytes, readErr := optimizeRead(path, optimizeConfigLimit)
	if errors.Is(readErr, os.ErrNotExist) {
		exists := false
		r.Target.Exists = &exists
		if args.config != "" {
			r.Reasons = append(r.Reasons, "explicit_target_missing")
			r.NextActions = []string{"supply_existing_target_config"}
			return publishOptimize(dir, root, r)
		}
		baselineBytes = []byte("[pipeline.default]\nstages = []\n[pipeline.passthrough]\nstages = []\nrecover_footer = false\n[[route]]\ntool = \"view_image\"\nexit_status = \"any\"\nprofile = \"passthrough\"\n")
	} else if readErr != nil {
		r.Outcome = "incomplete"
		r.Reasons = append(r.Reasons, "target_unreadable_or_exceeds_bound")
		r.NextActions = []string{"supply_readable_bounded_target_config"}
		return publishOptimize(dir, root, r)
	} else {
		exists := true
		r.Target.Exists = &exists
		hash := optimizeHash(baselineBytes)
		r.Target.SHA256 = &hash
	}
	baseline, cfg, err := captureOptimizeConfig(dir, "baseline.toml", baselineBytes)
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return r, internalError("capture baseline configuration: %v", err)
	}
	r.Baseline = &baseline
	if err != nil {
		r.Reasons = append(r.Reasons, "invalid_target_config")
		r.NextActions = []string{"fix_target_config"}
		return publishOptimize(dir, root, r)
	}
	if runtime.GOOS != "linux" {
		r.Reasons = append(r.Reasons, "native_linux_required")
		return publishOptimize(dir, root, r)
	}
	// Refuse before paid qualification any target that apply cannot replace.
	if err := checkOptimizeTarget(path); err != nil {
		r.Reasons = append(r.Reasons, "target_not_safely_applicable")
		r.NextActions = []string{"use_an_owned_singly_linked_regular_config_without_symlinked_path_components"}
		return publishOptimize(dir, root, r)
	}
	if reasons := optimizeSupported(cfg); len(reasons) > 0 {
		r.Reasons = append(r.Reasons, reasons...)
		r.NextActions = []string{"supply_supported_config_or_wait_for_scoped_routing_support"}
		return publishOptimize(dir, root, r)
	}
	cases, outputs, err := optimizePopulation()
	if err != nil {
		return r, internalError("generate controlled population: %v", err)
	}
	populationBytes, _ := json.Marshal(cases)
	r.Scope.PopulationSHA256 = optimizeHash(populationBytes)
	if err := os.WriteFile(filepath.Join(dir, "population.json"), populationBytes, 0o600); err != nil {
		return r, internalError("capture population: %v", err)
	}
	baseScreen := evaluateOptimizeConfiguration(baseline.SHA256, cases, outputs, cfg)
	r.BaselineScreen = &baseScreen
	r.Quality = "passed_generated_baseline_gates"
	if !baseScreen.ControlledFactsPass {
		r.Quality = "failed_generated_baseline_gates"
	}
	proposals := []struct {
		name string
		data []byte
	}{}
	for _, variant := range []struct {
		name   string
		stages []string
	}{
		{"append-json-compact", []string{"json-compact"}},
		{"append-sanitize", []string{"strip-ansi", "dedup-blank", "normalize"}},
	} {
		data, err := optimizeDerivedConfig(baselineBytes, cfg, variant.stages)
		if err != nil {
			r.Reasons = []string{"unsupported_default_stages_edit_syntax"}
			r.NextActions = []string{"use_a_plain_pipeline_default_table_and_stages_array_without_changing_other_settings"}
			return publishOptimize(dir, root, r)
		}
		if len(data) > optimizeConfigLimit {
			r.Outcome = "incomplete"
			r.Reasons = append(r.Reasons, "derived_config_exceeds_bound")
			r.NextActions = []string{"supply_smaller_target_config"}
			return publishOptimize(dir, root, r)
		}
		proposals = append(proposals, struct {
			name string
			data []byte
		}{variant.name, data})
	}
	if args.candidate != "" {
		data, err := optimizeRead(args.candidate, optimizeConfigLimit)
		if err != nil {
			r.Reasons = append(r.Reasons, "candidate_unreadable_or_exceeds_bound")
			r.NextActions = []string{"supply_readable_bounded_candidate_config"}
			return publishOptimize(dir, root, r)
		}
		proposals = append(proposals, struct {
			name string
			data []byte
		}{"supplied", data})
	}
	r.Outcome = "no_change"
	best := baseScreen.DeliveredEstimatedTokens
	for i, proposal := range proposals {
		identity, candidateCfg, err := captureOptimizeConfig(dir, fmt.Sprintf("candidate-%d.toml", i+1), proposal.data)
		if errors.As(err, &pathError) {
			return r, internalError("capture candidate configuration: %v", err)
		}
		c := optimizeCandidate{Name: proposal.name, Config: identity, Outcome: "blocked", Reasons: []string{}}
		if err != nil {
			c.Reasons = append(c.Reasons, "invalid_candidate_config")
		} else {
			c.Reasons = optimizeSupported(candidateCfg)
			if len(c.Reasons) == 0 {
				if _, _, err := optimizeStageOnlyDiff(baselineBytes, proposal.data); err != nil {
					c.Reasons = []string{"unsupported_changes_outside_pipeline_default_stages"}
				}
			}
		}
		if len(c.Reasons) == 0 {
			screen := evaluateOptimizeConfiguration(identity.SHA256, cases, outputs, candidateCfg)
			c.Screen = &screen
			switch {
			case !screen.ControlledFactsPass:
				c.Outcome = "rejected"
				c.Reasons = []string{"generated_preservation_gate_failed"}
			case screen.DeliveredEstimatedTokens >= baseScreen.DeliveredEstimatedTokens:
				c.Outcome = "no_change"
				c.Reasons = []string{"no_generated_estimated_token_improvement"}
			default:
				c.Outcome = "screened"
				c.Reasons = []string{"generated_only_not_workload_qualified"}
				if screen.DeliveredEstimatedTokens < best {
					best = screen.DeliveredEstimatedTokens
					selected := identity
					r.SelectedCandidate = &selected
				}
			}
		}
		r.Candidates = append(r.Candidates, c)
		if proposal.name == "supplied" && c.Outcome == "blocked" {
			r.Outcome = "blocked"
			r.SelectedCandidate = nil
			r.Reasons = append(r.Reasons, c.Reasons...)
			r.NextActions = []string{"fix_or_remove_supplied_candidate"}
		}
	}
	if r.Outcome != "blocked" && r.SelectedCandidate != nil {
		r.Outcome = "screened"
	}
	r.Reasons = append(r.Reasons, "generated_only_not_workload_qualified")
	if r.Outcome == "no_change" {
		r.Reasons = append(r.Reasons, "no_candidate_passed_gates_and_improved_incumbent")
	}
	return publishOptimize(dir, root, r)
}

func captureOptimizeConfig(dir, name string, data []byte) (optimizeConfigIdentity, *config.Config, error) {
	id := optimizeConfigIdentity{optimizeHash(data), name}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return id, nil, err
	}
	cfg, err := config.LoadFile(path)
	return id, cfg, err
}

func optimizeSupported(cfg *config.Config) []string {
	if len(cfg.Plugins) > 0 {
		return []string{"external_plugins_unsupported_not_executed"}
	}
	if len(cfg.Profiles) > 16 || len(cfg.Routes) > 16 {
		return []string{"configuration_complexity_exceeds_bound"}
	}
	for _, route := range cfg.Routes {
		if route.ExitStatus != "" && route.ExitStatus != "any" {
			return []string{"outcome_routing_not_qualified"}
		}
	}
	calls, err := optimizeControlledCalls()
	if err != nil {
		return []string{"controlled_population_unavailable"}
	}
	if optimizeUnqualifiedReach(calls, cfg.Routes) {
		return []string{"route_shadows_controlled_case"}
	}
	reg := pipeline.NewRegistry()
	stages.RegisterBuiltins(reg)
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := cfg.Profiles[name]
		if len(profile.Stages) > 32 {
			return []string{"stage_count_exceeds_bound"}
		}
		for _, token := range profile.Stages {
			base, arg, _ := strings.Cut(token, ":")
			switch base {
			case "json-compact", "unwrap-result", "strip-ansi", "dedup-blank", "normalize":
				if arg != "" {
					return []string{"unsupported_stage_arguments_not_executed"}
				}
			case "head", "truncate", "keep-ends", "token-budget":
				if arg != "" {
					for _, number := range strings.Split(arg, ":") {
						n, err := strconv.Atoi(number)
						if err != nil || n <= 0 || n > 1<<20 {
							return []string{"invalid_or_excessive_stage_arguments_not_executed"}
						}
					}
				}
			default:
				return []string{"unsupported_stage_not_executed"}
			}
			if _, found, err := reg.Lookup(token); !found || err != nil {
				return []string{"invalid_stage_arguments_not_executed"}
			}
		}
	}
	return []string{}
}

// optimizeControlledCalls lists every tool call the controlled populations
// route: the generated screening cases and the qualification corpus read.
func optimizeControlledCalls() ([]*dbpkg.ToolCall, error) {
	cases, _, err := optimizePopulation()
	if err != nil {
		return nil, err
	}
	calls := make([]*dbpkg.ToolCall, 0, len(cases)+1)
	for _, c := range cases {
		calls = append(calls, &dbpkg.ToolCall{Tool: c.Tool, Input: string(c.Input), IsError: c.Expected.Errors.IsError})
	}
	input := mustJSON(map[string]string{"command": optimizeQualificationCommand})
	return append(calls, &dbpkg.ToolCall{Tool: "bash", Input: string(input)}), nil
}

// optimizeRouteExercised reports whether controlled cases exercise a route
// directly: a tool-only rule over bash, read, or view_image.
func optimizeRouteExercised(route config.RouteRule) bool {
	return route.CommandPrefix == "" && route.Subcommand == "" && (route.Tool == "" || route.Tool == "bash" || route.Tool == "read" || route.Tool == "view_image")
}

// optimizeMatchedRoute returns the index of the first route that selects tc,
// or -1 for the default fallback. Rules match independently, so the first
// rule that matches alone is the router's ordered choice.
func optimizeMatchedRoute(tc *dbpkg.ToolCall, routes []config.RouteRule) int {
	for i := range routes {
		if router.Resolve(tc, routes[i:i+1], "").Route == router.RouteMatched {
			return i
		}
	}
	return -1
}

// optimizeUnqualifiedReach reports whether any call selects a command,
// subcommand, or other-tool route. Such routes are admitted only while no
// controlled call reaches them; their profiles stay byte-identical.
func optimizeUnqualifiedReach(calls []*dbpkg.ToolCall, routes []config.RouteRule) bool {
	for _, tc := range calls {
		if i := optimizeMatchedRoute(tc, routes); i >= 0 && !optimizeRouteExercised(routes[i]) {
			return true
		}
	}
	return false
}

func optimizeDerivedConfig(data []byte, cfg *config.Config, additions []string) ([]byte, error) {
	list := append([]string{}, cfg.PipelineDefault...)
	for _, stage := range additions {
		found := false
		for _, existing := range list {
			if stage == existing {
				found = true
			}
		}
		if !found {
			list = append(list, stage)
		}
	}
	if reflect.DeepEqual(list, cfg.PipelineDefault) || (len(list) == 0 && len(cfg.PipelineDefault) == 0) {
		return bytes.Clone(data), nil
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	// This is only a default-stage-array edit, not a TOML parser or merge.
	// Parsed settings are compared afterwards; ambiguous/unusual syntax refuses.
	headers := regexp.MustCompile(`(?m)^[\t ]*\[pipeline\.default\][\t ]*(?:#[^\r\n]*)?\r?$`).FindAllIndex(data, -1)
	var out []byte
	if len(headers) == 0 {
		out = append(bytes.Clone(data), []byte("\n[pipeline.default]\nstages = "+string(encoded)+"\n")...)
	} else if len(headers) == 1 {
		start, end := headers[0][1], len(data)
		if next := regexp.MustCompile(`(?m)^[\t ]*\[\[?[A-Za-z_"']`).FindIndex(data[start:]); next != nil {
			end = start + next[0]
		}
		arrays := regexp.MustCompile(`(?m)^[\t ]*stages[\t ]*=[\t ]*(\[(?:[^\]"'#]|"(?:\\.|[^"\\])*"|'[^']*'|#[^\r\n]*(?:\r?\n|$))*\])`).FindAllSubmatchIndex(data[start:end], -1)
		if len(arrays) == 1 {
			left, right := start+arrays[0][2], start+arrays[0][3]
			out = append(bytes.Clone(data[:left]), encoded...)
			out = append(out, data[right:]...)
		} else if len(arrays) == 0 && len(cfg.PipelineDefault) == 0 {
			out = append(bytes.Clone(data[:start]), []byte("\nstages = "+string(encoded))...)
			out = append(out, data[start:]...)
		} else {
			return nil, fmt.Errorf("unsupported default stages syntax")
		}
	} else {
		return nil, fmt.Errorf("ambiguous default table")
	}
	var decoded struct {
		Pipeline map[string]struct{ Stages []string }
	}
	if _, err := toml.Decode(string(out), &decoded); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(decoded.Pipeline["default"].Stages, list) {
		return nil, fmt.Errorf("default-stage edit did not match requested stages")
	}
	if _, _, err := optimizeStageOnlyDiff(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

func optimizePopulation() ([]screenCase, [][]byte, error) {
	// Only these bounded generators are accepted here. No user output or generator input is read.
	var lines screenCase
	lines.Tool = "bash"
	lines.Input = json.RawMessage(`{"command":"printf controlled"}`)
	lines.Generator.Algorithm, lines.Generator.Bytes, lines.Generator.FillByte, lines.Generator.NewlineEvery = "fixed_lines_v1", 4096, " ", 64
	for i, fact := range []string{"HEAD_FACT", "MIDDLE_FACT", "TAIL_FACT"} {
		lines.Generator.Placements = append(lines.Generator.Placements, struct {
			Fact    string `json:"fact"`
			Offsets []int  `json:"offsets"`
		}{fact, []int{[]int{0, 2048, 3968}[i]}})
		lines.Expected.RequiredFacts = append(lines.Expected.RequiredFacts, struct {
			Fact             string `json:"fact"`
			ExactOccurrences int    `json:"exact_occurrences"`
		}{fact, 1})
	}
	var structured screenCase
	structured.Tool = "read"
	structured.Input = json.RawMessage(`{"filePath":"controlled.json"}`)
	structured.Generator.Algorithm, structured.Generator.Bytes = "json_object_v1", 4096
	structured.Generator.JSON = &struct {
		RequiredFact  string `json:"required_fact"`
		RepeatedFact  string `json:"repeated_fact"`
		RepeatedCount int    `json:"repeated_count"`
		PaddingByte   string `json:"padding_byte"`
	}{"JSON_FACT", "ITEM_FACT", 8, "x"}
	structured.Expected.RequiredFacts = append(structured.Expected.RequiredFacts, struct {
		Fact             string `json:"fact"`
		ExactOccurrences int    `json:"exact_occurrences"`
	}{"JSON_FACT", 1}, struct {
		Fact             string `json:"fact"`
		ExactOccurrences int    `json:"exact_occurrences"`
	}{"ITEM_FACT", 8})
	cases := []screenCase{}
	outputs := [][]byte{}
	for _, template := range []screenCase{lines, structured} {
		for _, outcome := range []string{"success", "failure", "unknown"} {
			c := template
			c.Outcome = outcome
			if outcome != "unknown" {
				failure := outcome == "failure"
				c.Expected.Errors.IsError = &failure
				c.Expected.Passthrough = failure
			}
			output, err := generateScreenOutput(c)
			if err != nil {
				return nil, nil, err
			}
			if template.Tool == "read" {
				var indented bytes.Buffer
				if err := json.Indent(&indented, output, "", "  "); err != nil {
					return nil, nil, err
				}
				output = indented.Bytes()
			}
			if len(output) > 8192 {
				return nil, nil, fmt.Errorf("generated case exceeds bound")
			}
			cases = append(cases, c)
			outputs = append(outputs, output)
		}
	}
	image := lines
	image.Tool = "view_image"
	image.Generator.FillByte = "x"
	image.Expected.Passthrough = true
	image.Outcome = "unknown"
	output, err := generateScreenOutput(image)
	if err != nil {
		return nil, nil, err
	}
	return append(cases, image), append(outputs, output), nil
}

func evaluateOptimizeConfiguration(id string, cases []screenCase, outputs [][]byte, cfg *config.Config) screenResult {
	r := screenResult{ConfigID: id, Calls: len(cases), ControlledFactsPass: true, Dispositions: map[string]int{"transformed": 0, "anti_worsening_rejected": 0, "intentional_passthrough": 0}}
	for i, c := range cases {
		tc := &dbpkg.ToolCall{Tool: c.Tool, Input: string(c.Input), Output: string(outputs[i]), IsError: c.Expected.Errors.IsError}
		var warnings bytes.Buffer
		execution, err := compressPipelineWithConfig(tc, cfg, nil, &warnings, true)
		if err != nil || (warnings.Len() > 0 && (tc.IsError == nil || !*tc.IsError)) || (json.Valid(outputs[i]) && !json.Valid([]byte(execution.output))) || len(execution.output) > len(outputs[i]) {
			r.ControlledFactsPass = false
		}
		execution = applyRecoveryCueGate(execution, screenSourceID, tc.Output)
		delivered := execution.output
		if execution.transformed && execution.recoverFooter && !execution.structuredFallback {
			if withCue, ok := recoveryCueOutput(screenSourceID, tc.Output, delivered); ok {
				r.RecoveryFooterBytes += len(withCue) - len(delivered)
				r.RecoveryCueCalls++
				delivered = withCue
			}
		}
		switch {
		case execution.transformed:
			r.Dispositions["transformed"]++
		case execution.antiWorseningRejected:
			r.Dispositions["anti_worsening_rejected"]++
		default:
			r.Dispositions["intentional_passthrough"]++
		}
		r.RawBytes += len(outputs[i])
		r.DeliveredBytes += len(delivered)
		r.RawEstimatedTokens += estimatedTokens(len(outputs[i]))
		r.DeliveredEstimatedTokens += estimatedTokens(len(delivered))
		for _, fact := range c.Expected.RequiredFacts {
			if strings.Count(delivered, fact.Fact) != fact.ExactOccurrences {
				r.ControlledFactsPass = false
			}
		}
		if c.Expected.Passthrough && delivered != tc.Output {
			r.ControlledFactsPass = false
		}
	}
	r.NetBytesSaved = r.RawBytes - r.DeliveredBytes
	return r
}

func publishOptimize(dir, root string, r optimizeResult) (optimizeResult, error) {
	r.NextCommands = optimizeNextCommands(r)
	r.LiveRequirements = []string{"explicit --live consent and permitted --model provider/name (replace the provider/name placeholder)", "docker on PATH and a prebuilt ktx-bench image with pinned runtime/collector capabilities", "OpenCode auth at $HOME/.local/share/opencode/auth.json; no image build or provider choice is automatic"}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return r, internalError("encode optimization result")
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), data, 0o600); err != nil {
		return r, internalError("retain optimization result: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.sha256"), []byte(optimizeHash(data)), 0o600); err != nil {
		return r, internalError("retain result digest: %v", err)
	}
	// Each invocation publishes a new identity; existing results are never rewritten.
	destination := filepath.Join(root, r.ID)
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return r, internalError("result identity collision")
	}
	if err := os.Rename(dir, destination); err != nil {
		return r, internalError("publish optimization result: %v", err)
	}
	return r, nil
}

func loadOptimizeResult(id string) (optimizeResult, error) {
	var r optimizeResult
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 || strings.ToLower(id) != id {
		return r, usageError("invalid optimization result id")
	}
	dir := filepath.Join(".ktx", "optimizations", id)
	if err := optimizeNoSymlinkPath(dir); err != nil && !os.IsNotExist(err) {
		return r, internalError("unsafe optimization archive path")
	}
	read := func(name string, limit int) ([]byte, error) {
		file, err := experiment.OpenArchiveFile(dir, name)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
		if err == nil && len(data) > limit {
			err = fmt.Errorf("archive file exceeds bound")
		}
		return data, err
	}
	data, err := read("result.json", 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return r, notFoundError("optimization result not found")
	}
	if err != nil {
		return r, internalError("optimization result unreadable")
	}
	digest, err := read("result.sha256", 64)
	if err != nil || string(digest) != optimizeHash(data) {
		return r, internalError("optimization result integrity check failed")
	}
	if err := json.Unmarshal(data, &r); err != nil || (r.Schema != optimizeSchema && r.Schema != optimizeLiveSchema) || r.ID != id {
		return r, internalError("unsupported or invalid optimization result")
	}
	if r.Schema == optimizeSchema && (r.Mode != "offline" || r.ApplyEligible || r.Outcome == "ready" || r.Live != nil) {
		return r, internalError("offline optimization result cannot be promoted")
	}
	identities := []optimizeConfigIdentity{}
	if r.Baseline != nil {
		identities = append(identities, *r.Baseline)
	}
	for i, c := range r.Candidates {
		if c.Config.Artifact != fmt.Sprintf("candidate-%d.toml", i+1) {
			return r, internalError("invalid candidate artifact identity")
		}
		identities = append(identities, c.Config)
	}
	if r.Baseline != nil && r.Baseline.Artifact != "baseline.toml" {
		return r, internalError("invalid baseline artifact identity")
	}
	for _, identity := range identities {
		data, err := read(identity.Artifact, optimizeConfigLimit)
		if err != nil || optimizeHash(data) != identity.SHA256 {
			return r, internalError("captured configuration integrity check failed")
		}
	}
	if r.Scope.PopulationSHA256 != "" {
		data, err := read("population.json", optimizeConfigLimit)
		if err != nil || optimizeHash(data) != r.Scope.PopulationSHA256 {
			return r, internalError("population integrity check failed")
		}
	}
	if r.Schema == optimizeLiveSchema {
		if err := inspectOptimizeQualification(dir, r); err != nil {
			return r, internalError("live qualification integrity check failed: %v", err)
		}
	}
	application, err := readOptimizeApplication(dir)
	if err != nil {
		return r, internalError("invalid application recovery record: %v", err)
	}
	r.Application = application
	if application != nil {
		quote := "'" + strings.ReplaceAll(r.Target.Path, "'", "'\"'\"'") + "'"
		r.NextCommands = []string{"ktx optimize show " + r.ID + " --format text"}
		if application.State == "applied" || application.State == "rollback_prepared" {
			r.NextCommands = append(r.NextCommands, "ktx optimize rollback "+r.ID)
		}
		if application.State == "rolled_back" || application.State == "rollback_prepared" {
			r.ApplyEligible = false
			r.NextActions = []string{"finish_or_inspect_rollback", "start_new_cycle_using_current_incumbent"}
		}
		r.NextCommands = append(r.NextCommands, "ktx optimize --config "+quote+" --format text")
	}
	return r, nil
}

func renderOptimizeText(w io.Writer, r optimizeResult) error {
	var out strings.Builder
	fmt.Fprintf(&out, "Optimization %s: %s\nSchema: %s\nMode: %s\nBinary version: %s\n", r.ID, r.Outcome, r.Schema, r.Mode, r.BinaryVersion)
	fmt.Fprintf(&out, "Target: %s\n", r.Target.Path)
	if r.Target.Exists == nil {
		fmt.Fprintln(&out, "Target exists: unknown (unreadable target)")
	} else {
		fmt.Fprintf(&out, "Target exists: %t\n", *r.Target.Exists)
	}
	if r.Target.SHA256 != nil {
		fmt.Fprintf(&out, "Target SHA256: %s\n", *r.Target.SHA256)
	} else {
		fmt.Fprintln(&out, "Target SHA256: unavailable (original absence or unreadable target)")
	}
	if r.Baseline != nil {
		fmt.Fprintf(&out, "Baseline: %s (%s)\n", r.Baseline.SHA256, r.Baseline.Artifact)
	} else {
		fmt.Fprintln(&out, "Baseline: unavailable")
	}
	fmt.Fprintf(&out, "Scope: %s\nPopulation SHA256: %s\nReal workload evidence: %t\nRouting: %s\n", r.Scope.Population, r.Scope.PopulationSHA256, r.Scope.WorkloadEvidence, r.Scope.Routing)
	fmt.Fprintf(&out, "Supported changes: %s\n", r.Scope.SupportedChanges)
	fmt.Fprintf(&out, "Bounds: %d candidates; %d cases/config; %d bytes/case; %d bytes/config; %d profiles; %d stages/profile; numeric stage argument <= %d; %d provider attempts\n", r.Bounds.MaxCandidates, r.Bounds.CasesPerConfig, r.Bounds.MaxCaseBytes, r.Bounds.MaxConfigBytes, r.Bounds.MaxProfiles, r.Bounds.MaxStagesPerProfile, r.Bounds.MaxNumericStageArgument, r.Bounds.ProviderAttempts)
	fmt.Fprintf(&out, "Baseline quality: %s\nIntegrity: %s\nRuntime tokens: %s (%s)\nCost: %s (%s)\nApply eligible: %t\n", r.Quality, r.Integrity, r.RuntimeTokens.State, r.RuntimeTokens.Reason, r.Cost.State, r.Cost.Reason, r.ApplyEligible)
	measure := func(label string, s *screenResult) {
		if s == nil {
			fmt.Fprintf(&out, "%s measurements: not run\n", label)
			return
		}
		fmt.Fprintf(&out, "%s generated measurements: %d calls; bytes %d -> %d (saved %d); estimated tokens %d -> %d (bytes/4, not runtime tokens); preservation pass %t; recovery cues %d (%d bytes); dispositions %v\n", label, s.Calls, s.RawBytes, s.DeliveredBytes, s.NetBytesSaved, s.RawEstimatedTokens, s.DeliveredEstimatedTokens, s.ControlledFactsPass, s.RecoveryCueCalls, s.RecoveryFooterBytes, s.Dispositions)
	}
	measure("Baseline", r.BaselineScreen)
	for _, c := range r.Candidates {
		fmt.Fprintf(&out, "Candidate %s: %s\n  Config: %s (%s)\n  Reasons: %s\n", c.Name, c.Outcome, c.Config.SHA256, c.Config.Artifact, strings.Join(c.Reasons, ", "))
		measure("  Candidate", c.Screen)
	}
	if r.SelectedCandidate != nil {
		fmt.Fprintf(&out, "Selected for qualification only: %s (%s)\n", r.SelectedCandidate.SHA256, r.SelectedCandidate.Artifact)
	} else {
		fmt.Fprintln(&out, "Selected for qualification only: none")
	}
	fmt.Fprintf(&out, "Reasons: %s\nMissing inputs: %s\nNext actions: %s\n", strings.Join(r.Reasons, ", "), strings.Join(r.MissingInputs, ", "), strings.Join(r.NextActions, ", "))
	fmt.Fprintf(&out, "Live requirements (only if authorizing live evaluation): %s\n", strings.Join(r.LiveRequirements, "; "))
	for _, command := range r.NextCommands {
		fmt.Fprintf(&out, "Next command: %s\n", command)
	}
	if r.Live != nil {
		data, err := json.MarshalIndent(r.Live, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(&out, "Controlled live qualification (not representative user workload):\n%s\nRuntime tokens per correct task: baseline=%v candidate=%v\n", data, optimizeMeasurementText(r.RuntimeTokens.Baseline), optimizeMeasurementText(r.RuntimeTokens.Candidate))
	}
	_, err := io.WriteString(w, out.String())
	if err == nil && r.Application != nil {
		_, err = fmt.Fprintf(w, "Application state: %s (captured target %s)\n", r.Application.State, r.Application.Target)
	}
	return err
}

func optimizeNextCommands(r optimizeResult) []string {
	commands := []string{"ktx optimize show " + r.ID + " --format text"}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if r.Outcome == "screened" && r.Mode == "offline" {
		command := "ktx optimize --live --model provider/name"
		// A screened absent target can only be the effective default. Passing it
		// explicitly would turn absence into an explicit_target_missing refusal.
		if r.Target.Exists == nil || *r.Target.Exists {
			command += " --config " + quote(r.Target.Path)
		}
		for _, c := range r.Candidates {
			if r.SelectedCandidate != nil && c.Config == *r.SelectedCandidate && c.Name == "supplied" {
				command += " --candidate " + quote(filepath.Join(".ktx", "optimizations", r.ID, c.Config.Artifact))
			}
		}
		commands = append(commands, command)
	}
	if r.Outcome == "ready" {
		commands = append(commands, "ktx optimize apply "+r.ID+" --dry-run", "ktx optimize apply "+r.ID)
	}
	return commands
}
