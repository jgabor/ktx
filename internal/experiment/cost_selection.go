package experiment

import (
	"math"
)

// CostObservation holds source facts, not an aggregate or a copied estimate.
// Constructors require materialized source values; they are not attestation.
type CostObservation struct {
	id, experimentID, source, estimateBasis string
	reported, estimated                     *float64
	reportedReasons                         []string
	estimatedReasons                        []string
	pricing                                 *PricingSnapshotMetadata
}

type SelectedCostObservation struct {
	ID               string                   `json:"id"`
	ExperimentID     string                   `json:"experiment_id,omitempty"`
	Value            *float64                 `json:"value"`
	Source           string                   `json:"source"`
	Pricing          *PricingSnapshotMetadata `json:"pricing,omitempty"`
	ReportedReasons  []string                 `json:"reported_reasons"`
	EstimatedReasons []string                 `json:"estimated_reasons"`
}

// CostSelection uses one basis for every supplied observation. Values remain
// observation-local so consumers can retain their own aggregation contracts.
type CostSelection struct {
	PolicyVersion     int                       `json:"policy_version"`
	Basis             string                    `json:"basis"`
	Reason            string                    `json:"reason"`
	Unit              string                    `json:"unit"`
	RequiredCount     int                       `json:"required_count"`
	ReportedEligible  int                       `json:"reported_eligible_count"`
	EstimatedEligible int                       `json:"estimated_eligible_count"`
	EstimateReason    string                    `json:"estimate_reason,omitempty"`
	Observations      []SelectedCostObservation `json:"observations"`
}

// ReportedCostObservation adapts a persisted USD observation without inventing
// usage or pricing that older database rows do not retain.
func ReportedCostObservation(id string, value *float64, source string) CostObservation {
	return CostObservation{
		id: id, source: source, reported: copyCost(value),
		reportedReasons:  costValueReasons(value, "reported"),
		estimatedReasons: []string{"retained_usage_and_pricing_unavailable"},
	}
}

// AttemptCostObservations adapts an already materialized coverage population.
// Callers must load retained field authorities before passing attempts. This
// function neither filters attempts nor trusts their EstimatedAPICost fields.
// Claimed pricing corruption remains an error even when reports are usable.
func AttemptCostObservations(dir string, manifest Manifest, attempts []Attempt) ([]CostObservation, error) {
	snapshot, err := preparePricingEvidence(dir, manifest)
	if err != nil {
		return nil, err
	}
	basis := ""
	if snapshot != nil {
		// Compare pricing semantics, not retrieval time or source URL. The
		// original snapshot hash remains attached to each observation.
		price := snapshot.Price
		price.SourceURL, price.RetrievedAt = "", ""
		data, err := MarshalJSON(pricingSnapshot{Provider: snapshot.Provider, Model: snapshot.Model, Price: price})
		if err != nil {
			return nil, err
		}
		basis = sha256Bytes(data)
	}
	observations := make([]CostObservation, 0, len(attempts))
	for _, attempt := range attempts {
		o := ReportedCostObservation(attempt.AttemptID, attempt.Cost, attempt.Artifacts["stream"])
		o.experimentID = attempt.ExperimentID
		o.pricing, o.estimateBasis = manifest.Pricing, basis
		e := attempt.UsageEvidence
		if e.StepFinishEvents == 0 || e.CostEvents != e.StepFinishEvents || len(attempt.ModelSteps) != e.StepFinishEvents {
			o.reportedReasons = appendUnique(o.reportedReasons, "reported_incomplete")
		}
		estimate := estimateAttemptAPICost(attempt, snapshot)
		o.estimated, o.estimatedReasons = copyCost(estimate.Value), append([]string{}, estimate.AvailabilityReasons...)
		if e.StepFinishEvents == 0 || len(attempt.ModelSteps) != e.StepFinishEvents ||
			e.TokensInEvents != e.StepFinishEvents || e.TokensOutEvents != e.StepFinishEvents ||
			e.ReasoningEvents != e.StepFinishEvents || e.CacheReadEvents != e.StepFinishEvents || e.CacheWriteEvents != e.StepFinishEvents {
			o.estimatedReasons = appendUnique(o.estimatedReasons, "estimated_usage_incomplete")
		}
		for _, step := range attempt.ModelSteps {
			if snapshot != nil {
				if step.Identity.Provider != nil && *step.Identity.Provider != snapshot.Provider {
					o.estimatedReasons = appendUnique(o.estimatedReasons, "estimated_step_provider_conflict")
				}
				if step.Identity.Model != nil && *step.Identity.Model != snapshot.Model {
					o.estimatedReasons = appendUnique(o.estimatedReasons, "estimated_step_model_conflict")
				}
			}
			if step.Cost.State != MeasurementMeasured || step.Cost.Value == nil {
				o.reportedReasons = appendUnique(o.reportedReasons, "reported_incomplete")
			} else {
				o.reportedReasons = appendUnique(o.reportedReasons, costValueReasons(step.Cost.Value, "reported")...)
			}
			for _, field := range []IntegerMeasurement{step.UncachedInput, step.Output, step.Reasoning, step.CacheRead, step.CacheWrite} {
				if field.State != MeasurementMeasured || field.Value == nil || *field.Value < 0 {
					o.estimatedReasons = appendUnique(o.estimatedReasons, "estimated_step_usage_invalid")
				}
			}
		}
		o.estimatedReasons = appendUnique(o.estimatedReasons, costValueReasons(o.estimated, "estimated")...)
		observations = append(observations, o)
	}
	return observations, nil
}

// SelectCostCohort applies reported-first selection to the entire caller-declared
// coverage set. Unknown billing derivation is not a comparability failure.
// Empty or unavailable cost never supplies a numeric winner or recommendation.
func SelectCostCohort(observations []CostObservation) CostSelection {
	selection := CostSelection{
		PolicyVersion: 1, Basis: "unavailable", Reason: "empty_cohort", Unit: "USD",
		RequiredCount: len(observations), Observations: make([]SelectedCostObservation, 0, len(observations)),
	}
	positive := false
	estimateBasis := ""
	estimatesComparable := true
	for _, o := range observations {
		o.reportedReasons = appendUnique(append([]string{}, o.reportedReasons...), costValueReasons(o.reported, "reported")...)
		o.estimatedReasons = appendUnique(append([]string{}, o.estimatedReasons...), costValueReasons(o.estimated, "estimated")...)
		if o.estimateBasis == "" {
			o.estimatedReasons = appendUnique(o.estimatedReasons, "pricing_snapshot_unavailable")
		}
		if len(o.reportedReasons) == 0 {
			selection.ReportedEligible++
			positive = positive || *o.reported > 0
		}
		if len(o.estimatedReasons) == 0 {
			selection.EstimatedEligible++
			if estimateBasis != "" && estimateBasis != o.estimateBasis {
				estimatesComparable = false
			}
			estimateBasis = o.estimateBasis
		}
		selection.Observations = append(selection.Observations, SelectedCostObservation{
			ID: o.id, ExperimentID: o.experimentID, Source: o.source, Pricing: o.pricing,
			ReportedReasons: append([]string{}, o.reportedReasons...), EstimatedReasons: append([]string{}, o.estimatedReasons...),
		})
	}
	if len(observations) == 0 {
		return selection
	}
	switch {
	case selection.ReportedEligible == len(observations) && positive:
		selection.Basis, selection.Reason = "reported", "reported_complete_positive"
	case selection.ReportedEligible == len(observations):
		selection.Reason = "reported_uniform_zero"
	default:
		selection.Reason = "reported_incomplete"
		for _, o := range observations {
			if containsString(o.reportedReasons, "reported_invalid") {
				selection.Reason = "reported_invalid"
				break
			}
		}
	}
	if selection.Basis != "reported" {
		switch {
		case selection.EstimatedEligible != len(observations):
			selection.EstimateReason = "estimated_incomplete"
		case !estimatesComparable:
			selection.EstimateReason = "estimated_incomparable"
		default:
			selection.Basis = "estimated_api"
		}
	}
	for i, o := range observations {
		switch selection.Basis {
		case "reported":
			selection.Observations[i].Value = copyCost(o.reported)
		case "estimated_api":
			selection.Observations[i].Value = copyCost(o.estimated)
		}
	}
	return selection
}

func costValueReasons(value *float64, source string) []string {
	if value == nil {
		return []string{source + "_unavailable"}
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return []string{source + "_invalid"}
	}
	return []string{}
}

func copyCost(value *float64) *float64 {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}
