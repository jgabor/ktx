package experiment

import (
	"strings"
	"testing"
)

func TestValidateExperimentID(t *testing.T) {
	for _, id := range []string{"experiment-1", "20260828T120000Z-local-1", strings.Repeat("a", 128)} {
		if err := ValidateExperimentID(id); err != nil {
			t.Fatalf("ValidateExperimentID(%q): %v", id, err)
		}
	}
	for _, id := range []string{"", ".", "../outside", "nested/id", "under_score", strings.Repeat("a", 129)} {
		if err := ValidateExperimentID(id); err == nil {
			t.Fatalf("ValidateExperimentID(%q) succeeded", id)
		}
	}
}
