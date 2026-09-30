package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
)

// loadExperimentEvidence is the interpretation boundary, not the discovery
// loader: incomplete archives must remain listable and inspectable.
func loadExperimentEvidence(a *archive) (Manifest, []Attempt, []ProjectionDivergence, error) {
	var manifest Manifest
	data, err := a.readFile("manifest.json")
	if err != nil {
		return manifest, nil, nil, fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, nil, nil, fmt.Errorf("decode manifest: %w", err)
	}
	if err := validateManifestContract(manifest); err != nil {
		return manifest, nil, nil, err
	}
	if err := a.validateInputs(manifest.Inputs); err != nil {
		return manifest, nil, nil, err
	}
	if err := a.validateExecutionConfigs(manifest); err != nil {
		return manifest, nil, nil, err
	}
	if err := a.validateRuntimeConfig(manifest); err != nil {
		return manifest, nil, nil, err
	}
	if err := a.validateReadDescription(manifest); err != nil {
		return manifest, nil, nil, err
	}
	ledger, err := a.open("attempts.jsonl")
	if os.IsNotExist(err) {
		return manifest, nil, nil, fmt.Errorf("attempts.jsonl must contain experiment evidence")
	}
	if err != nil {
		return manifest, nil, nil, err
	}
	defer ledger.Close()
	// Keep unique materialized attempts and output-sized diagnostics, not an
	// input-sized collection of duplicate ledger projections. Every record is
	// still decoded under the same scanner limit before it can receive credit.
	first := make(map[string]int)
	var attempts []Attempt
	var ordinals []int
	var grouped [][]ProjectionDivergence
	err = visitJSONLines(ledger, func(attempt Attempt) error {
		if attempt.SchemaVersion != 3 {
			return fmt.Errorf("attempt %q has missing or unsupported schema_version %d", attempt.AttemptID, attempt.SchemaVersion)
		}
		if attempt.AttemptID == "" || (attempt.Side != Baseline && attempt.Side != Candidate) {
			return fmt.Errorf("attempt must have an attempt_id and baseline or candidate side")
		}
		if manifest.RuntimeConfig != nil {
			if attempt.RuntimeConfigSHA256 != manifest.RuntimeConfig.SHA256 {
				return fmt.Errorf("attempt %q runtime configuration identity mismatch", attempt.AttemptID)
			}
		} else if attempt.RuntimeConfigSHA256 != "" {
			return fmt.Errorf("attempt %q has unclaimed runtime configuration", attempt.AttemptID)
		}
		if index, exists := first[attempt.AttemptID]; exists {
			prior := attempts[index]
			if !reflect.DeepEqual(orchestrationOf(prior), orchestrationOf(attempt)) {
				return fmt.Errorf("conflicting authoritative duplicate attempt %q", attempt.AttemptID)
			}
			ordinals[index]++
			grouped[index] = append(grouped[index], metricDivergences(prior, attempt, ordinals[index])...)
			return nil
		}
		reasons, err := a.attemptArtifactReasons(&attempt)
		if err != nil {
			return err
		}
		attempt.validationReasons = append(attempt.validationReasons, reasons...)
		authoritative, err := a.materializeAttempt(manifest, attempt)
		if err != nil {
			return err
		}
		first[attempt.AttemptID] = len(attempts)
		attempts = append(attempts, authoritative)
		ordinals = append(ordinals, 1)
		grouped = append(grouped, metricDivergences(authoritative, attempt, 1))
		return nil
	})
	if err != nil {
		return manifest, nil, nil, err
	}
	if len(attempts) == 0 {
		return manifest, nil, nil, fmt.Errorf("attempts.jsonl must contain experiment evidence")
	}
	if err := validateReadDescriptionComparability(attempts); err != nil {
		return manifest, nil, nil, err
	}
	if err := validatePairOrder(manifest, attempts); err != nil {
		return manifest, nil, nil, err
	}
	divergences := []ProjectionDivergence{}
	for _, group := range grouped {
		divergences = append(divergences, group...)
	}
	attempts, err = a.applyCostControls(manifest, attempts, divergences)
	if err != nil {
		return manifest, nil, nil, err
	}
	return manifest, attempts, divergences, nil
}

func (a *archive) validateExecutionConfigs(manifest Manifest) error {
	for _, side := range Sides {
		pipeline := manifest.Pipelines[side]
		if pipeline.Path == "" || pipeline.SHA256 == "" {
			return fmt.Errorf("execution config identity missing %s", side)
		}
		hash, err := a.hash(pipeline.Path)
		if unsafeArchiveAccess(err) {
			return err
		}
		if err != nil || hash != pipeline.SHA256 {
			return fmt.Errorf("execution config hash mismatch %s", side)
		}
		// Older producers retained independent copies. Both must match the
		// execution identity; do not rewrite those archives on read.
		for _, input := range manifest.Inputs {
			if input.Kind == "pipeline" && input.ID == pipeline.PipelineID && input.SHA256 != hash {
				return fmt.Errorf("execution config does not match retained pipeline input %s", side)
			}
		}
	}
	return nil
}

func (a *archive) attemptArtifactReasons(attempt *Attempt) ([]string, error) {
	var reasons []string
	for name, relative := range attempt.Artifacts {
		if _, err := a.stat(relative); err != nil {
			if unsafeArchiveAccess(err) {
				return nil, err
			}
			reasons = append(reasons, "missing artifact "+name)
		}
	}
	for name, expected := range attempt.ContentHashes {
		relative := attempt.Artifacts[name]
		if name == "workspace" {
			hash, err := a.directoryHash(relative)
			if unsafeArchiveAccess(err) {
				return nil, err
			}
			if err != nil || hash != expected {
				reasons = append(reasons, "content hash mismatch "+name)
			}
		} else if relative != "" {
			var hash string
			var err error
			if name == "stream" && attempt.SchemaVersion >= 2 && attempt.Artifacts["database"] != "" {
				var calls callAuthority
				hash, calls, err = a.hashStreamCalls(relative)
				if err == nil {
					attempt.calls = &callAccounting{Stream: calls, Persistence: unavailableCallAuthority("persistence_authority_unavailable")}
				}
			} else {
				hash, err = a.hash(relative)
			}
			if unsafeArchiveAccess(err) {
				return nil, err
			}
			if err != nil || hash != expected {
				reasons = append(reasons, "content hash mismatch "+name)
			}
		}
	}
	return reasons, nil
}

// Hash every byte even when call accounting stops at a malformed identity or
// its existing scanner limit. Usage/scoring keep their separate skip-and-
// continue record contract; merging those scanners would change accepted data.
func (a *archive) hashStreamCalls(name string) (string, callAuthority, error) {
	f, err := a.open(name)
	if err != nil {
		return "", callAuthority{}, err
	}
	defer f.Close()
	return hashStreamCalls(f)
}

func hashStreamCalls(r io.Reader) (string, callAuthority, error) {
	checked := &streamReadError{Reader: r}
	digest := sha256.New()
	reader := io.TeeReader(checked, digest)
	calls := readStreamCallAuthority(reader)
	_, err := io.Copy(digest, checked)
	if checked.err != nil {
		err = checked.err
	}
	if err != nil {
		return "", calls, err
	}
	return hex.EncodeToString(digest.Sum(nil)), calls, nil
}

type streamReadError struct {
	io.Reader
	err error
}

func (r *streamReadError) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

func validateManifestContract(manifest Manifest) error {
	if err := validateExecutionControlManifest(manifest); err != nil {
		return err
	}
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("manifest has missing or unsupported schema_version %d", manifest.SchemaVersion)
	}
	if err := validateMethodologyVersion(manifest.Methodology.Version); err != nil {
		return err
	}
	if manifest.ExperimentID == "" {
		return fmt.Errorf("manifest experiment_id is required")
	}
	if manifest.TargetValidPerPipeline <= 0 {
		return fmt.Errorf("manifest target_valid_per_pipeline must be positive")
	}
	if manifest.Task.MinimumToolCalls < 0 || (manifest.Task.ExpectedOutput == nil && len(manifest.Task.ExpectedFacts) == 0) {
		return fmt.Errorf("manifest task requires expected output or facts and non-negative minimum_tool_calls")
	}
	// Registered producers retain a spec and each selected pipeline. Inline
	// tasks remain supported; a task snapshot is optional for that producer.
	// Explicitly versioned unregistered producers predate this input inventory.
	if manifest.Methodology.ID != "" {
		required := []InputMetadata{{Kind: "spec", ID: manifest.Methodology.ID, Format: "yaml"}}
		for _, side := range Sides {
			required = append(required, InputMetadata{Kind: "pipeline", ID: manifest.Pipelines[side].PipelineID, Format: "toml"})
		}
		if manifest.Methodology.Version == promptCacheTokenHitRateMethodologyVersion {
			required = append(required, InputMetadata{Kind: "contract", ID: "cache-reuse", Format: "json"})
		}
		for _, want := range required {
			found := false
			for _, input := range manifest.Inputs {
				found = found || (want.ID != "" && input.Kind == want.Kind && input.ID == want.ID && input.Format == want.Format)
			}
			if !found {
				return fmt.Errorf("manifest requires retained %s input %q (%s)", want.Kind, want.ID, want.Format)
			}
		}
	}
	return nil
}

func validateMethodologyVersion(version string) error {
	switch version {
	case exactMatchMethodologyVersion, promptCacheTokenHitRateMethodologyVersion, recoveryAwareMethodologyVersion:
		return nil
	default:
		return fmt.Errorf("missing or unsupported methodology version %q", version)
	}
}
