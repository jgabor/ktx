package experiment

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/jgabor/ktx/internal/db"
)

// ExecutionStop is an operational receipt, not a cost metric or a complete pair.
// Partial usage remains in the attempt ledger and its retained stream.
type ExecutionStop struct {
	Reason           string   `json:"reason"`
	Detail           string   `json:"detail,omitempty"`
	AfterAttemptID   string   `json:"after_attempt_id,omitempty"`
	EstimatedAPICost *float64 `json:"estimated_api_cost,omitempty"`
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateExecutionControlOptions(o CompareOptions) error {
	if o.FirstArm != "" && o.FirstArm != Baseline && o.FirstArm != Candidate {
		return fmt.Errorf("--first-arm must be baseline or candidate")
	}
	if o.EstimatedCostLimit != nil && !finitePositive(*o.EstimatedCostLimit) {
		return fmt.Errorf("--estimated-cost-limit must be finite and positive (USD)")
	}
	if (o.FirstArm != "" || o.EstimatedCostLimit != nil) && o.ReadDescriptionTreatment == "" {
		return fmt.Errorf("--first-arm and --estimated-cost-limit require the read-description treatment; compare only, not rank")
	}
	if o.EstimatedCostLimit != nil && (o.PricingSnapshot == "" || o.PricingSHA256 == "" || o.RuntimeConfig == "") {
		return fmt.Errorf("--estimated-cost-limit requires --pricing-snapshot, --pricing-sha256 and --runtime-config")
	}
	return nil
}

func pairOrder(first string) ([]string, []string) {
	if first == Candidate {
		return []string{Candidate, Baseline}, []string{Baseline, Candidate}
	}
	return []string{Baseline, Candidate}, []string{Candidate, Baseline}
}

func validateExecutionControlManifest(m Manifest) error {
	if m.FirstArm == "" && m.EstimatedCostLimit == nil {
		return nil // Preserve archives produced without these controls.
	}
	if (m.FirstArm != Baseline && m.FirstArm != Candidate) || m.ReadDescription == nil || m.TargetValidPerPipeline != 1 || m.MaxAttempts != 2 || m.CacheControl != nil {
		return fmt.Errorf("execution controls require a baseline/candidate first arm and a two-attempt read-description pair")
	}
	if m.EstimatedCostLimit != nil && (!finitePositive(*m.EstimatedCostLimit) || m.Pricing == nil || m.RuntimeConfig == nil) {
		return fmt.Errorf("estimated cost limit requires a finite positive USD value, retained pricing and runtime configuration")
	}
	first, second := pairOrder(m.FirstArm)
	want := map[string]any{"kind": "alternating_by_cycle", "first_cycle": first, "second_cycle": second}
	// Normalize the slice types used by JSON decoding without loosening the policy.
	a, err := MarshalJSON(m.PipelineExecutionPolicy)
	if err != nil {
		return err
	}
	b, _ := MarshalJSON(want)
	if string(a) != string(b) {
		return fmt.Errorf("pipeline execution policy does not match requested first arm")
	}
	return nil
}

func validatePairOrder(m Manifest, attempts []Attempt) error {
	if err := validateExecutionControlManifest(m); err != nil {
		return err
	}
	first, _ := pairOrder(m.FirstArm)
	for i, a := range attempts {
		if m.FirstArm == "" {
			if a.FirstArm != "" || a.ExecutionOrder != 0 {
				return fmt.Errorf("attempt %q has unclaimed pair order", a.AttemptID)
			}
			continue
		}
		if i >= len(first) || a.FirstArm != m.FirstArm || a.ExecutionOrder != i+1 || a.Side != first[i] || a.Cycle != 1 || a.Ordinal != 1 || a.Role != "" {
			return fmt.Errorf("attempt %q executed order does not match requested first arm", a.AttemptID)
		}
	}
	return nil
}

func retainExecutionStop(dir string, stop *ExecutionStop) error {
	return WriteJSON(filepath.Join(dir, "execution-stop.json"), stop)
}

func (a *archive) applyCostControls(m Manifest, attempts []Attempt, divergences []ProjectionDivergence) ([]Attempt, error) {
	if m.EstimatedCostLimit == nil {
		return attempts, nil
	}
	pricing, err := a.preparePricingEvidence(m)
	if err != nil {
		return nil, err
	}
	attempts = estimateAttempts(attempts, pricing)
	for i := range attempts {
		total, reason := completeEstimatedCost(attempts[:i])
		if reason != "" || total >= *m.EstimatedCostLimit {
			return nil, fmt.Errorf("attempt %q launched after estimated-cost stop", attempts[i].AttemptID)
		}
		for _, d := range divergences {
			if d.AttemptID == attempts[i].AttemptID {
				attempts[i].validationReasons = append(attempts[i].validationReasons, "attempt projections cannot be reconciled")
				break
			}
		}
		if _, reason := completeEstimatedCost(attempts[i : i+1]); reason != "" {
			attempts[i].validationReasons = append(attempts[i].validationReasons, reason)
		}
	}
	return attempts, nil
}

// completeEstimatedCost requires completed, reconciled authority for every model
// step of every started attempt. A known subtotal on a failed turn is not complete
// coverage. Answer correctness is independent of this operational gate.
func completeEstimatedCost(attempts []Attempt) (float64, string) {
	total := 0.0
	for _, a := range attempts {
		e := a.UsageEvidence
		if a.Status != "completed" || a.ProcessExitCode != 0 || !a.StreamComplete || len(a.validationReasons) != 0 || e.StepFinishEvents == 0 || len(a.ModelSteps) != e.StepFinishEvents ||
			e.TokensInEvents != e.StepFinishEvents || e.TokensOutEvents != e.StepFinishEvents || e.ReasoningEvents != e.StepFinishEvents || e.CacheReadEvents != e.StepFinishEvents || e.CacheWriteEvents != e.StepFinishEvents ||
			a.EstimatedAPICost == nil || len(a.estimatedAPICostReasons) != 0 || *a.EstimatedAPICost < 0 || math.IsNaN(*a.EstimatedAPICost) || math.IsInf(*a.EstimatedAPICost, 0) {
			return 0, fmt.Sprintf("attempt %s has incomplete estimated-cost coverage", a.AttemptID)
		}
		// Valid totals cannot establish coverage: signed per-step values can
		// cancel to a nonnegative sum in the shared usage parser.
		for i, step := range a.ModelSteps {
			for _, field := range []struct {
				name  string
				value IntegerMeasurement
			}{
				{"uncached_input", step.UncachedInput},
				{"output", step.Output},
				{"reasoning", step.Reasoning},
				{"cache_read", step.CacheRead},
				{"cache_write", step.CacheWrite},
			} {
				if field.value.State != MeasurementMeasured || field.value.Value == nil || *field.value.Value < 0 {
					return 0, fmt.Sprintf("attempt %s model step %d has invalid %s estimated-cost usage", a.AttemptID, i+1, field.name)
				}
			}
		}
		total += *a.EstimatedAPICost
		if math.IsInf(total, 0) || math.IsNaN(total) {
			return 0, "cumulative estimated cost is nonfinite"
		}
	}
	return total, ""
}

// costCheckpoint runs only after the prior attempt is retained, before the next
// arm is scheduled. It reloads authorities rather than trusting copied estimates.
func (runner *Runner) costCheckpoint(dir string, expected Manifest, startedAttempts int) (stop *ExecutionStop, err error) {
	defer func() {
		if err != nil {
			stop = &ExecutionStop{Reason: "evidence_invalid", Detail: err.Error()}
			if receiptErr := retainExecutionStop(dir, stop); receiptErr != nil {
				err = fmt.Errorf("%w; retain stop receipt: %v", err, receiptErr)
			}
		}
	}()
	a, err := openArchive(dir)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	m := expected
	var attempts []Attempt
	var divergences []ProjectionDivergence
	if _, statErr := a.stat("attempts.jsonl"); statErr == nil {
		m, attempts, divergences, err = loadExperimentEvidence(a)
		if err != nil {
			stop := &ExecutionStop{Reason: "evidence_invalid", Detail: err.Error()}
			return stop, retainExecutionStop(dir, stop)
		}
		x, _ := MarshalJSON(m)
		y, _ := MarshalJSON(expected)
		if string(x) != string(y) {
			return nil, fmt.Errorf("retained manifest changed during execution")
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if len(attempts) != startedAttempts {
		return nil, fmt.Errorf("retained attempt count %d does not cover %d started attempts", len(attempts), startedAttempts)
	}
	pricing, err := a.preparePricingEvidence(m)
	if err != nil {
		return nil, err
	}
	if pricing == nil {
		return nil, fmt.Errorf("estimated cost checkpoint requires retained pricing")
	}
	// Validate frozen pricing before the first model call, including the usage
	// fields that this controlled treatment requires. Do not use reported USD.
	one := int64(1)
	probe := db.EstimateAPICostDetails(db.EstimatedCostInputs{UncachedInput: 1, Output: 1, Reasoning: &one, CacheRead: &one, CacheWrite: &one}, pricing.Price)
	if probe.Value == nil {
		return nil, fmt.Errorf("incompatible pinned pricing: %s", strings.Join(probe.AvailabilityReasons, ", "))
	}
	attempts = estimateAttempts(attempts, pricing)
	total, reason := completeEstimatedCost(attempts)
	stop = &ExecutionStop{}
	if len(attempts) > 0 {
		stop.AfterAttemptID = attempts[len(attempts)-1].AttemptID
	}
	switch {
	case len(divergences) != 0:
		stop.Reason, stop.Detail = "evidence_invalid", "attempt projections cannot be reconciled"
	case reason != "":
		stop.Reason, stop.Detail = "estimated_cost_coverage_unknown", reason
	case total >= *m.EstimatedCostLimit:
		stop.Reason, stop.EstimatedAPICost = "estimated_cost_limit_reached", &total
	default:
		return nil, nil
	}
	return stop, retainExecutionStop(dir, stop)
}
