package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/experiment"
)

type optimizeApplication struct {
	Schema         string `json:"schema"`
	ResultID       string `json:"result_id"`
	Target         string `json:"target"`
	State          string `json:"state"`
	OriginalExists bool   `json:"original_exists"`
	OriginalSHA256 string `json:"original_sha256"`
	OriginalMode   uint32 `json:"original_mode"`
	OriginalGroup  int    `json:"original_group"`
	AppliedSHA256  string `json:"applied_sha256"`
	AppliedMode    uint32 `json:"applied_mode"`
	AppliedGroup   int    `json:"applied_group"`
}

type optimizeMutationReport struct {
	Schema   string `json:"schema"`
	ResultID string `json:"result_id"`
	Action   string `json:"action"`
	Outcome  string `json:"outcome"`
	Target   string `json:"target"`
	DryRun   bool   `json:"dry_run"`
	Diff     struct {
		Field  string   `json:"field"`
		Before []string `json:"before"`
		After  []string `json:"after"`
	} `json:"diff"`
	OriginalExists bool   `json:"original_exists"`
	OriginalMode   uint32 `json:"original_mode"`
	AppliedMode    uint32 `json:"applied_mode"`
}

type optimizeMutationHooks struct{ beforePublish, afterPublish func() error }

// Drift checks are not authenticity against an owner who can rewrite an entire
// archive. Eligibility is recomputed from retained field authorities, not flags.
func validateOptimizeApplication(r optimizeResult, dir string) ([]byte, []byte, error) {
	refuse := func() ([]byte, []byte, error) {
		return nil, nil, conflictError("result is not a validated, scoped ready configuration")
	}
	if r.Schema != optimizeLiveSchema || r.Mode != "live" || r.Outcome != "ready" || r.Live == nil || r.Baseline == nil || r.SelectedCandidate == nil || r.Target.Exists == nil {
		return refuse()
	}
	d, err := loadOptimizeResult(r.Live.DevelopmentResultID)
	if err != nil || d.Schema != optimizeSchema || d.Outcome != "screened" || d.Baseline == nil || d.SelectedCandidate == nil || d.BaselineScreen == nil || !d.BaselineScreen.ControlledFactsPass || *d.Baseline != *r.Baseline || *d.SelectedCandidate != *r.SelectedCandidate || !reflect.DeepEqual(d.Target, r.Target) {
		return refuse()
	}
	selected := false
	for _, c := range r.Candidates {
		if c.Config == *r.SelectedCandidate && c.Outcome == "screened" && c.Screen != nil && c.Screen.ControlledFactsPass {
			selected = true
		}
	}
	if !selected || !filepath.IsAbs(r.Target.Path) || filepath.Clean(r.Target.Path) != r.Target.Path {
		return refuse()
	}
	baseline, err := readOptimizeArtifact(dir, r.Baseline.Artifact, optimizeConfigLimit)
	if err != nil {
		return refuse()
	}
	candidate, err := readOptimizeArtifact(dir, r.SelectedCandidate.Artifact, optimizeConfigLimit)
	if err != nil {
		return refuse()
	}
	if optimizeHash(baseline) != r.Baseline.SHA256 || optimizeHash(candidate) != r.SelectedCandidate.SHA256 || (*r.Target.Exists && (r.Target.SHA256 == nil || *r.Target.SHA256 != r.Baseline.SHA256)) || (!*r.Target.Exists && r.Target.SHA256 != nil) {
		return refuse()
	}
	experimentDir := filepath.Join(dir, "experiments/qualification")
	if r.Live.Experiment != "experiments/qualification" {
		return refuse()
	}
	if _, err := experiment.RequireValidExperiment(experimentDir); err != nil {
		return refuse()
	}
	data, err := readOptimizeArtifact(experimentDir, "manifest.json", 1<<20)
	if err != nil {
		return refuse()
	}
	var manifest experiment.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return refuse()
	}
	if manifest.Pipelines[experiment.Baseline].SHA256 != r.Baseline.SHA256 || manifest.Pipelines[experiment.Candidate].SHA256 != r.SelectedCandidate.SHA256 || manifest.Methodology.Version != "v7-recovery-aware-comparison" || manifest.TargetValidPerPipeline != 5 || manifest.Model != r.Live.Model {
		return refuse()
	}
	for side, expected := range map[string][]byte{experiment.Baseline: baseline, experiment.Candidate: candidate} {
		captured, err := readOptimizeArtifact(experimentDir, manifest.Pipelines[side].Path, optimizeConfigLimit)
		if err != nil || !bytes.Equal(captured, expected) {
			return refuse()
		}
	}
	data, err = readOptimizeArtifact(experimentDir, "summary.json", 1<<20)
	if err != nil {
		return refuse()
	}
	summary, err := experiment.DecodeSummary(data)
	if err != nil {
		return refuse()
	}
	checked := optimizeResult{Outcome: "incomplete", Live: &optimizeQualification{MinimumCompletePairs: 5, ValidationPassed: true}}
	assessOptimizeQualification(&checked, summary)
	if checked.Outcome != "ready" || !checked.ApplyEligible {
		return refuse()
	}
	bCfg, err := config.LoadFile(filepath.Join(dir, r.Baseline.Artifact))
	if err != nil || len(optimizeSupported(bCfg)) > 0 {
		return refuse()
	}
	if err := optimizeRetainedRouting(experimentDir, bCfg.Routes); err != nil {
		return refuse()
	}
	cCfg, err := config.LoadFile(filepath.Join(dir, r.SelectedCandidate.Artifact))
	if err != nil || len(optimizeSupported(cCfg)) > 0 {
		return refuse()
	}
	cases, outputs, err := optimizePopulation()
	if err != nil {
		return refuse()
	}
	pop, _ := json.Marshal(cases)
	if optimizeHash(pop) != d.Scope.PopulationSHA256 {
		return refuse()
	}
	b := evaluateOptimizeConfiguration(r.Baseline.SHA256, cases, outputs, bCfg)
	c := evaluateOptimizeConfiguration(r.SelectedCandidate.SHA256, cases, outputs, cCfg)
	if !b.ControlledFactsPass || !c.ControlledFactsPass || c.DeliveredEstimatedTokens >= b.DeliveredEstimatedTokens {
		return refuse()
	}
	return baseline, candidate, nil
}

func optimizeStageOnlyDiff(baseline, candidate []byte) ([]string, []string, error) {
	decode := func(data []byte) (map[string]any, []string, error) {
		var doc map[string]any
		if _, err := toml.Decode(string(data), &doc); err != nil {
			return nil, nil, err
		}
		list := []string{}
		pipelines, _ := doc["pipeline"].(map[string]any)
		def, _ := pipelines["default"].(map[string]any)
		if values, ok := def["stages"].([]any); ok {
			for _, value := range values {
				stage, ok := value.(string)
				if !ok {
					return nil, nil, fmt.Errorf("invalid stages")
				}
				list = append(list, stage)
			}
		}
		delete(def, "stages")
		if len(def) == 0 {
			delete(pipelines, "default")
		}
		if len(pipelines) == 0 {
			delete(doc, "pipeline")
		}
		return doc, list, nil
	}
	b, before, err := decode(baseline)
	if err != nil {
		return nil, nil, err
	}
	c, after, err := decode(candidate)
	if err != nil {
		return nil, nil, err
	}
	if !reflect.DeepEqual(b, c) {
		return nil, nil, conflictError("proposal changes unrelated settings; application supports only pipeline.default.stages, without merging or force")
	}
	return before, after, nil
}

func mutateOptimize(id, action string, dryRun bool, hooks optimizeMutationHooks) (optimizeMutationReport, error) {
	report := optimizeMutationReport{Schema: "ktx.optimize.mutation.v1", ResultID: id, Action: action, DryRun: dryRun}
	if runtime.GOOS != "linux" {
		return report, conflictError("configuration mutation requires supported native Linux")
	}
	r, err := loadOptimizeResult(id)
	if err != nil {
		return report, err
	}
	dir, err := filepath.Abs(filepath.Join(".ktx", "optimizations", id))
	if err != nil {
		return report, err
	}
	baseline, candidate, err := validateOptimizeApplication(r, dir)
	if err != nil {
		return report, err
	}
	before, after, err := optimizeStageOnlyDiff(baseline, candidate)
	if err != nil {
		return report, err
	}
	report.Target, report.OriginalExists = r.Target.Path, *r.Target.Exists
	report.Diff.Field, report.Diff.Before, report.Diff.After = "pipeline.default.stages", before, after
	target, err := beginOptimizeTarget(r.Target.Path, !dryRun && action == "apply")
	if err != nil {
		return report, conflictError("unsafe or unavailable captured target: %v", err)
	}
	defer target.close()
	receipt, err := readOptimizeApplication(dir)
	if err != nil {
		return report, conflictError("application recovery record is invalid: %v", err)
	}
	if receipt != nil && (receipt.ResultID != id || receipt.Target != r.Target.Path || receipt.OriginalExists != *r.Target.Exists || receipt.AppliedSHA256 != r.SelectedCandidate.SHA256 || (receipt.OriginalExists && receipt.OriginalSHA256 != r.Baseline.SHA256)) {
		return report, conflictError("application recovery identities do not match the qualified result")
	}
	if receipt != nil && ((receipt.OriginalExists && (receipt.OriginalMode != receipt.AppliedMode || receipt.OriginalGroup != receipt.AppliedGroup)) || (!receipt.OriginalExists && (receipt.OriginalMode != 0 || receipt.OriginalSHA256 != "" || receipt.AppliedMode != 0o600))) {
		return report, conflictError("application recovery metadata is inconsistent")
	}
	current, info, err := target.read()
	if err != nil {
		return report, conflictError("captured target cannot be safely read: %v", err)
	}
	if receipt == nil {
		if action == "rollback" {
			return report, conflictError("result has no application to roll back")
		}
		if (info != nil) != *r.Target.Exists || (info != nil && !bytes.Equal(current, baseline)) {
			return report, conflictError("captured target changed since evaluation; no overwrite")
		}
		receipt = &optimizeApplication{Schema: "ktx.optimize.application.v1", ResultID: id, Target: r.Target.Path, State: "prepared", OriginalExists: info != nil, AppliedSHA256: r.SelectedCandidate.SHA256, AppliedMode: 0o600, AppliedGroup: optimizeNewGroup(target)}
		if info != nil {
			receipt.OriginalSHA256 = optimizeHash(current)
			receipt.OriginalMode = uint32(info.Mode().Perm())
			receipt.OriginalGroup = optimizeFileGroup(info)
			receipt.AppliedMode, receipt.AppliedGroup = receipt.OriginalMode, receipt.OriginalGroup
		}
	} else if receipt.OriginalExists {
		backup, err := readOptimizeArtifact(dir, "application/original.toml", optimizeConfigLimit)
		if err != nil || optimizeHash(backup) != receipt.OriginalSHA256 || !bytes.Equal(backup, baseline) {
			return report, conflictError("private original backup is missing or changed")
		}
	}
	report.OriginalMode, report.AppliedMode = receipt.OriginalMode, receipt.AppliedMode
	matches := func(applied bool) bool {
		if !applied && !receipt.OriginalExists {
			return info == nil
		}
		if info == nil {
			return false
		}
		hash, mode, group := receipt.OriginalSHA256, receipt.OriginalMode, receipt.OriginalGroup
		if applied {
			hash, mode, group = receipt.AppliedSHA256, receipt.AppliedMode, receipt.AppliedGroup
		}
		return optimizeHash(current) == hash && uint32(info.Mode().Perm()) == mode && optimizeFileGroup(info) == group
	}
	if action == "apply" {
		if receipt.State == "rolled_back" || receipt.State == "rollback_prepared" {
			return report, conflictError("result was rolled back; start a new optimization cycle")
		}
		if matches(true) {
			report.Outcome = "noop"
			if !dryRun && receipt.State != "applied" {
				receipt.State = "applied"
				err = writeOptimizeApplication(dir, receipt, nil)
			}
			return report, err
		}
		if receipt.State == "applied" || !matches(false) {
			return report, conflictError("captured target bytes or mode changed; no overwrite")
		}
		if dryRun {
			report.Outcome = "preview"
			return report, nil
		}
		if err := writeOptimizeApplication(dir, receipt, baseline); err != nil {
			return report, internalError("retain recoverable original before apply: %v", err)
		}
		if err := target.publish(candidate, os.FileMode(receipt.AppliedMode), receipt.AppliedGroup, hooks); err != nil {
			return report, err
		}
		receipt.State, report.Outcome = "applied", "applied"
	} else {
		if matches(false) {
			if receipt.State == "applied" {
				return report, conflictError("target was changed outside rollback; no overwrite")
			}
			receipt.State, report.Outcome = "rolled_back", "noop"
			return report, writeOptimizeApplication(dir, receipt, nil)
		}
		if receipt.State == "rolled_back" || !matches(true) {
			return report, conflictError("target has later byte or mode edits; rollback refuses")
		}
		receipt.State = "rollback_prepared"
		if err := writeOptimizeApplication(dir, receipt, nil); err != nil {
			return report, internalError("prepare rollback recovery: %v", err)
		}
		if receipt.OriginalExists {
			err = target.publish(baseline, os.FileMode(receipt.OriginalMode), receipt.OriginalGroup, hooks)
		} else {
			err = target.remove(hooks)
		}
		if err != nil {
			return report, err
		}
		receipt.State, report.Outcome = "rolled_back", "rolled_back"
	}
	if err := writeOptimizeApplication(dir, receipt, nil); err != nil {
		return report, internalError("configuration publication finished but recovery finalization failed; retry safely: %v", err)
	}
	return report, nil
}

func readOptimizeArtifact(dir, name string, limit int) ([]byte, error) {
	if !filepath.IsLocal(name) {
		return nil, fmt.Errorf("artifact path is not local")
	}
	if err := optimizeNoSymlinkPath(filepath.Join(dir, name)); err != nil {
		return nil, err
	}
	f, err := experiment.OpenArchiveFile(dir, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err == nil && len(data) > limit {
		err = fmt.Errorf("artifact exceeds bound")
	}
	return data, err
}

func readOptimizeApplication(dir string) (*optimizeApplication, error) {
	path := filepath.Join(dir, "application")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	}
	if err := optimizeNoSymlinkPath(path); err != nil {
		return nil, err
	}
	data, err := readOptimizeArtifact(dir, "application/receipt.json", optimizeConfigLimit)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt optimizeApplication
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, err
	}
	if receipt.Schema != "ktx.optimize.application.v1" || receipt.OriginalMode > 0o777 || receipt.AppliedMode > 0o777 {
		return nil, fmt.Errorf("invalid recovery schema or mode")
	}
	switch receipt.State {
	case "prepared", "applied", "rollback_prepared", "rolled_back":
	default:
		return nil, fmt.Errorf("invalid application state")
	}
	return &receipt, nil
}

func writeOptimizeApplication(dir string, receipt *optimizeApplication, original []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir("application", 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := root.Stat("application")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("application recovery directory must be private")
	}
	if err := optimizeSyncRoot(root); err != nil {
		return err
	}
	if err := optimizeNoSymlinkPath(filepath.Join(dir, "application")); err != nil {
		return err
	}
	application, err := root.OpenRoot("application")
	if err != nil {
		return err
	}
	defer application.Close()
	if receipt.OriginalExists && original != nil {
		backup, err := readOptimizeArtifact(dir, "application/original.toml", optimizeConfigLimit)
		if os.IsNotExist(err) {
			err = optimizeAtomicRootWrite(application, "original.toml", original, 0o600, -1, nil, optimizeMutationHooks{})
		} else if err == nil && !bytes.Equal(backup, original) {
			err = fmt.Errorf("existing backup differs")
		}
		if err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return optimizeAtomicRootWrite(application, "receipt.json", data, 0o600, -1, nil, optimizeMutationHooks{})
}

// Use the local durable-file pattern, pinned to an opened parent directory.
func optimizeAtomicRootWrite(root *os.Root, name string, data []byte, mode os.FileMode, group int, check func() error, hooks optimizeMutationHooks) (returnedErr error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temporary := ".ktx-optimize-" + hex.EncodeToString(random[:]) + ".tmp"
	f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if err := root.Remove(temporary); err != nil && !os.IsNotExist(err) && returnedErr == nil {
			returnedErr = err
		}
	}()
	if group >= 0 {
		if err := f.Chown(-1, group); err != nil {
			return err
		}
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if hooks.beforePublish != nil {
		if err := hooks.beforePublish(); err != nil {
			return err
		}
	}
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	if err := optimizeSyncRoot(root); err != nil {
		return err
	}
	if hooks.afterPublish != nil {
		return hooks.afterPublish()
	}
	return nil
}

// Component checks complement rooted operations, not an uncooperative-editor CAS.
func optimizeNoSymlinkPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(abs, current), string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) {
			return fmt.Errorf("symlink or non-directory path component")
		}
	}
	return nil
}

func optimizeSyncRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func renderOptimizeMutation(w io.Writer, r optimizeMutationReport) error {
	_, err := fmt.Fprintf(w, "Optimization %s %s: %s\nTarget: %s\nDry run: %t\nScoped diff: %s\n  Before: %v\n  After: %v\nOriginal exists: %t\nOriginal mode: %04o\nApplied mode: %04o\n", r.ResultID, r.Action, r.Outcome, r.Target, r.DryRun, r.Diff.Field, r.Diff.Before, r.Diff.After, r.OriginalExists, r.OriginalMode, r.AppliedMode)
	return err
}
