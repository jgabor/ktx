package experiment

import "fmt"

// ValidateCollectedAttempts checks retained field authorities without requiring
// the final target count or rewriting a derived summary. It is an admission
// check for callers that must stop on invalid evidence before another launch.
func ValidateCollectedAttempts(directory string) error {
	a, err := openArchive(directory)
	if err != nil {
		return err
	}
	defer a.Close()
	m, attempts, _, err := loadExperimentEvidence(a)
	if err != nil {
		return err
	}
	if _, err := a.preparePricingEvidence(m); err != nil {
		return err
	}
	for _, attempt := range attempts {
		if len(attempt.validationReasons) > 0 {
			return fmt.Errorf("attempt %s has invalid retained evidence: %v", attempt.AttemptID, attempt.validationReasons)
		}
	}
	return nil
}

func completeAttemptPairs(attempts []Attempt, options CompareOptions) int {
	cycles := map[int]map[string]bool{}
	for _, attempt := range attempts {
		if attempt.Role != MeasuredWarm || !AttemptIsTargetValid(attempt, options.Task.MinimumToolCalls, options.methodologyVersion) || attempt.TokensIn == nil || attempt.TokensOut == nil || attempt.Reasoning == nil {
			continue
		}
		if cycles[attempt.Cycle] == nil {
			cycles[attempt.Cycle] = map[string]bool{}
		}
		cycles[attempt.Cycle][attempt.Side] = true
	}
	count := 0
	for _, sides := range cycles {
		if sides[Baseline] && sides[Candidate] {
			count++
		}
	}
	return count
}
