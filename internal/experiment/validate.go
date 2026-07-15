package experiment

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func ValidateExperiment(experimentDir string) (map[string]any, error) {
	manifestData, err := os.ReadFile(filepath.Join(experimentDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, err
	}
	attempts, err := LoadAttempts(experimentDir)
	if err != nil {
		return nil, err
	}
	attempts = reconcileCacheFromStream(attempts, experimentDir)
	seen := map[string]bool{}
	checks := []map[string]any{}
	valid := true
	for _, attempt := range attempts {
		attemptValid := true
		reasons := []string{}
		if attempt.AttemptID == "" || seen[attempt.AttemptID] {
			attemptValid = false
			reasons = append(reasons, "missing or duplicate attempt_id")
		}
		seen[attempt.AttemptID] = true
		if attempt.Side != Baseline && attempt.Side != Candidate {
			attemptValid = false
			reasons = append(reasons, "invalid side")
		}
		for name, relative := range attempt.Artifacts {
			path := filepath.Join(experimentDir, filepath.FromSlash(relative))
			if _, err := os.Stat(path); err != nil {
				attemptValid = false
				reasons = append(reasons, "missing artifact "+name)
			}
		}
		for name, expected := range attempt.ContentHashes {
			relative := attempt.Artifacts[name]
			if name == "workspace" {
				hash, _, err := directorySHA256(filepath.Join(experimentDir, filepath.FromSlash(relative)))
				if err != nil || hash != expected {
					attemptValid = false
					reasons = append(reasons, "content hash mismatch "+name)
				}
			} else if relative != "" {
				hash, err := fileSHA256(filepath.Join(experimentDir, filepath.FromSlash(relative)))
				if err != nil || hash != expected {
					attemptValid = false
					reasons = append(reasons, "content hash mismatch "+name)
				}
			}
		}
		if !attemptValid {
			valid = false
		}
		checks = append(checks, map[string]any{"attempt_id": attempt.AttemptID, "valid": attemptValid, "reasons": reasons})
	}
	expectedSummary, err := MarshalJSON(BuildSummary(manifest, attempts))
	if err != nil {
		return nil, err
	}
	actualSummary, err := os.ReadFile(filepath.Join(experimentDir, "summary.json"))
	if err != nil {
		return nil, err
	}
	summaryMatches := string(expectedSummary) == string(actualSummary)
	if !summaryMatches {
		valid = false
	}
	counts := CountValid(attempts, manifest.Task.MinimumToolCalls)
	targetMet := counts[Baseline] >= manifest.TargetValidPerPipeline && counts[Candidate] >= manifest.TargetValidPerPipeline
	if !targetMet {
		valid = false
	}
	return map[string]any{
		"schema_version": 1, "experiment_id": manifest.ExperimentID, "valid": valid,
		"summary_byte_match": summaryMatches, "target_met": targetMet,
		"valid_counts": counts, "attempt_checks": checks,
	}, nil
}

func RequireValidExperiment(experimentDir string) (map[string]any, error) {
	report, err := ValidateExperiment(experimentDir)
	if err != nil {
		return nil, err
	}
	if valid, _ := report["valid"].(bool); !valid {
		return report, fmt.Errorf("experiment validation failed")
	}
	return report, nil
}
