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
	actualSummary, err := os.ReadFile(filepath.Join(experimentDir, "summary.json"))
	if err != nil {
		return nil, err
	}
	pricing, err := preparePricingEvidence(experimentDir, manifest, attempts, actualSummary)
	if err != nil {
		return nil, err
	}
	attempts = estimateAttempts(attempts, pricing)
	methodologyVersion := manifest.Methodology.Version
	cacheAware := methodologyVersion == "v5-cache-aware"
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
		if len(reasons) > 0 {
			attemptValid = false
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
	summaryMatches := string(expectedSummary) == string(actualSummary)
	if !summaryMatches {
		valid = false
	}
	counts := CountValidForMethodology(attempts, manifest.Task.MinimumToolCalls, methodologyVersion)
	targetMet := counts[Baseline] >= manifest.TargetValidPerPipeline && counts[Candidate] >= manifest.TargetValidPerPipeline
	if !targetMet {
		valid = false
	}
	methodologyReasons := []string{}
	if cacheAware {
		methodologyReasons = validateV5Evidence(manifest, attempts, pricing, actualSummary, counts)
		if len(methodologyReasons) > 0 {
			valid = false
		}
	}
	report := map[string]any{
		"schema_version": 1, "experiment_id": manifest.ExperimentID, "valid": valid,
		"summary_byte_match": summaryMatches, "target_met": targetMet,
		"valid_counts": counts, "attempt_checks": checks,
	}
	if cacheAware {
		report["methodology_reasons"] = methodologyReasons
	}
	return report, nil
}

func validateV5Evidence(manifest Manifest, attempts []Attempt, pricing *pricingSnapshot, summaryData []byte, counts map[string]int) []string {
	reasons := []string{}
	var summary map[string]any
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		return append(reasons, "summary is not valid JSON")
	}
	if schema, ok := summary["schema_version"].(float64); !ok || schema != 3 {
		reasons = append(reasons, "v5 summary schema_version must be 3")
	}
	if manifest.Pricing == nil || pricing == nil {
		reasons = append(reasons, "v5 pricing snapshot is required")
	}
	provider, model, modelErr := splitModel(manifest.Model)
	if modelErr != nil {
		reasons = append(reasons, "manifest model must be provider/name")
	}
	if manifest.Pricing != nil && modelErr == nil && (manifest.Pricing.Provider != provider || manifest.Pricing.Model != model) {
		reasons = append(reasons, "pricing metadata does not match manifest model")
	}
	if pricing != nil && modelErr == nil && (pricing.Provider != provider || pricing.Model != model) {
		reasons = append(reasons, "pricing snapshot does not match manifest model")
	}
	control := manifest.CacheControl
	if control == nil {
		return append(reasons, "v5 cache_control manifest metadata is required")
	}
	if control.Mode != "isolated" && control.Mode != "observed" {
		reasons = append(reasons, "invalid cache control mode")
	}
	if control.Source != "runtime_adapter" && control.Source != "provider_adapter" && control.Source != "unavailable" {
		reasons = append(reasons, "invalid cache control source")
	}
	if control.Mode == "observed" && control.Source != "unavailable" {
		reasons = append(reasons, "observed cache control source must be unavailable")
	}
	if control.Mode == "isolated" && control.Source == "unavailable" {
		reasons = append(reasons, "isolated cache control requires an adapter source")
	}
	if control.WarmupCount < 0 {
		reasons = append(reasons, "warmup count must be non-negative")
	}
	if manifest.TargetValidPerPipeline <= 0 {
		reasons = append(reasons, "v5 target valid per pipeline must be positive")
	}
	if modelErr == nil && (control.Model != manifest.Model || control.Provider != provider) {
		reasons = append(reasons, "cache control model/provider does not match manifest")
	}
	variant := ""
	if manifest.Variant != nil {
		variant = *manifest.Variant
	}
	if control.Variant != variant {
		reasons = append(reasons, "cache control variant does not match manifest")
	}
	if manifest.Identities["opencode"] == "" {
		reasons = append(reasons, "manifest OpenCode runtime identity is required")
	} else if control.RuntimeVersion != manifest.Identities["opencode"] {
		reasons = append(reasons, "cache control runtime version does not match manifest identity")
	}
	if control.Mode == "isolated" && control.Source == "runtime_adapter" && control.RuntimeVersion != "1.18.0" {
		reasons = append(reasons, "runtime adapter isolation requires OpenCode 1.18.0")
	}
	if control.Mode == "observed" && len(control.NamespaceSHA256) != 0 {
		reasons = append(reasons, "observed cache control must not retain namespace hashes")
	}
	if control.Mode == "isolated" {
		if len(control.NamespaceSHA256) != len(Sides) {
			reasons = append(reasons, "isolated cache control requires exactly one namespace hash per side")
		}
		for _, side := range Sides {
			if !isLowerSHA256(control.NamespaceSHA256[side]) {
				reasons = append(reasons, fmt.Sprintf("isolated %s manifest namespace hash must be 64 lowercase hexadecimal characters", side))
			}
		}
	}
	phaseCounts := map[string]map[string]int{ColdObservation: {Baseline: 0, Candidate: 0}, Warmup: {Baseline: 0, Candidate: 0}, MeasuredWarm: {Baseline: 0, Candidate: 0}}
	executionOrders := map[int]bool{}
	for _, attempt := range attempts {
		if attempt.ExperimentID != manifest.ExperimentID {
			reasons = append(reasons, fmt.Sprintf("attempt %s experiment id does not match manifest", attempt.AttemptID))
		}
		if pipeline, ok := manifest.Pipelines[attempt.Side]; ok && (attempt.PipelineID != pipeline.PipelineID || attempt.ConfigSHA256 != pipeline.SHA256) {
			reasons = append(reasons, fmt.Sprintf("attempt %s pipeline metadata does not match manifest", attempt.AttemptID))
		}
		if _, ok := phaseCounts[attempt.Role]; !ok {
			reasons = append(reasons, fmt.Sprintf("attempt %s has missing or invalid role", attempt.AttemptID))
			continue
		}
		phaseCounts[attempt.Role][attempt.Side]++
		metadata := attempt.CacheControl
		if metadata == nil {
			reasons = append(reasons, fmt.Sprintf("attempt %s is missing cache metadata", attempt.AttemptID))
			continue
		}
		if metadata.Mode != control.Mode || metadata.Source != control.Source || metadata.Phase != attempt.Role {
			reasons = append(reasons, fmt.Sprintf("attempt %s cache mode/source/phase does not match manifest", attempt.AttemptID))
		}
		if metadata.Model != control.Model || metadata.Provider != control.Provider || metadata.Variant != control.Variant || metadata.RuntimeVersion != control.RuntimeVersion || metadata.IdleGapMS != control.IdleGapMS {
			reasons = append(reasons, fmt.Sprintf("attempt %s cache identity metadata does not match manifest", attempt.AttemptID))
		}
		if metadata.ExecutionOrder <= 0 || executionOrders[metadata.ExecutionOrder] {
			reasons = append(reasons, fmt.Sprintf("attempt %s has invalid execution order", attempt.AttemptID))
		} else {
			executionOrders[metadata.ExecutionOrder] = true
		}
		if control.Mode == "isolated" {
			expected := control.NamespaceSHA256[attempt.Side]
			if !isLowerSHA256(metadata.NamespaceSHA256) {
				reasons = append(reasons, fmt.Sprintf("attempt %s namespace hash must be 64 lowercase hexadecimal characters", attempt.AttemptID))
			}
			if metadata.NamespaceSHA256 != expected {
				reasons = append(reasons, fmt.Sprintf("attempt %s namespace hash does not match its side", attempt.AttemptID))
			}
		} else if control.Mode == "observed" && metadata.NamespaceSHA256 != "" {
			reasons = append(reasons, fmt.Sprintf("observed attempt %s must not retain a namespace hash", attempt.AttemptID))
		}
	}
	for _, side := range Sides {
		if phaseCounts[ColdObservation][side] != 1 {
			reasons = append(reasons, fmt.Sprintf("%s must have one cold_observation", side))
		}
		if phaseCounts[Warmup][side] != control.WarmupCount {
			reasons = append(reasons, fmt.Sprintf("%s warmup count does not match manifest", side))
		}
		if phaseCounts[MeasuredWarm][side] == 0 {
			reasons = append(reasons, fmt.Sprintf("%s must have measured_warm attempts", side))
		}
	}
	if control.Mode == "isolated" {
		baselineHash, candidateHash := control.NamespaceSHA256[Baseline], control.NamespaceSHA256[Candidate]
		if baselineHash == "" || candidateHash == "" {
			reasons = append(reasons, "isolated namespace hashes are required for both sides")
		} else if baselineHash == candidateHash {
			reasons = append(reasons, "isolated namespace hashes must differ across sides")
		}
	}
	if pipelines, ok := summary["pipelines"].(map[string]any); ok {
		for _, side := range Sides {
			pipeline, _ := pipelines[side].(map[string]any)
			if value, ok := pipeline["valid_count"].(float64); !ok || int(value) != counts[side] {
				reasons = append(reasons, fmt.Sprintf("%s summary valid_count does not match measured target predicate", side))
			}
		}
	} else {
		reasons = append(reasons, "summary pipelines are missing")
	}
	return reasons
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
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
