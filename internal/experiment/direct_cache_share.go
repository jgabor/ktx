package experiment

import "math/big"

const (
	directCacheContractVersion                = "cache-reuse-v1"
	promptCacheTokenHitRateContractVersion    = "cache-reuse-v2"
	promptCacheTokenHitRateMethodologyVersion = "v6-prompt-cache-token-hit-rate"
	recoveryAwareMethodologyVersion           = "v7-recovery-aware-comparison"
	directCacheRuntime                        = "opencode"
	directCacheRuntimeVersion                 = "1.18.5"
)

func cacheAwareMethodology(version string) bool {
	return version == "v5-cache-aware" || version == promptCacheTokenHitRateMethodologyVersion || version == recoveryAwareMethodologyVersion
}

func supportsDirectCachedInputShare(manifest Manifest) bool {
	return manifest.Methodology.Version == "v5-cache-aware" &&
		manifest.CacheControl != nil &&
		manifest.CacheControl.RuntimeVersion == directCacheRuntimeVersion &&
		manifest.Identities[directCacheRuntime] == directCacheRuntimeVersion
}

func supportsPromptCacheTokenHitRate(manifest Manifest) bool {
	return manifest.Methodology.Version == promptCacheTokenHitRateMethodologyVersion &&
		manifest.CacheControl != nil &&
		manifest.CacheControl.RuntimeVersion == directCacheRuntimeVersion &&
		manifest.Identities[directCacheRuntime] == directCacheRuntimeVersion
}

func supportsRecoveryAwareComparison(manifest Manifest) bool {
	return manifest.Methodology.Version == recoveryAwareMethodologyVersion
}

func supportsCacheTokenMetric(manifest Manifest) bool {
	return supportsDirectCachedInputShare(manifest) || supportsPromptCacheTokenHitRate(manifest)
}

func buildDirectCachedInputShare(manifest Manifest, attempts []Attempt) map[string]any {
	rows := make([]any, 0, len(attempts))
	for _, attempt := range attempts {
		rows = append(rows, directCachedInputShareAttempt(manifest, attempt))
	}
	return map[string]any{
		"contract_version": directCacheContractVersion,
		"description":      "OpenCode-reported cached-input share",
		"purpose":          "descriptive_non_ranking",
		"attempts":         rows,
	}
}

func directCachedInputShareAttempt(manifest Manifest, attempt Attempt) map[string]any {
	steps := make([]SourceEvent, 0, len(attempt.ModelSteps))
	for _, event := range attempt.ModelSteps {
		if event.Kind == ModelStep {
			steps = append(steps, event)
		}
	}
	fields := []struct {
		name        string
		measurement func(SourceEvent) IntegerMeasurement
	}{
		{"uncached_input", func(event SourceEvent) IntegerMeasurement { return event.UncachedInput }},
		{"cache_read", func(event SourceEvent) IntegerMeasurement { return event.CacheRead }},
		{"cache_write", func(event SourceEvent) IntegerMeasurement { return event.CacheWrite }},
	}
	measurements := map[string]map[string]any{}
	allGaps := []any{}
	for _, field := range fields {
		measurement, gaps := sumDirectMeasurements(steps, field.name, field.measurement)
		measurements[field.name] = measurement
		allGaps = append(allGaps, gaps...)
	}

	logicalInput := unavailableDirectMeasurement(allGaps)
	share := unavailableDirectMeasurement(allGaps)
	if len(allGaps) == 0 {
		uncached := measurements["uncached_input"]["value"].(*big.Int)
		read := measurements["cache_read"]["value"].(*big.Int)
		write := measurements["cache_write"]["value"].(*big.Int)
		logical := new(big.Int).Add(uncached, read)
		logical.Add(logical, write)
		logicalInput = measuredDirectInteger(logical)
		if logical.Sign() > 0 {
			ratio := new(big.Rat).SetFrac(read, logical)
			value, _ := ratio.Float64()
			share = map[string]any{"state": "measured", "value": value, "ratio": ratio.RatString()}
		} else {
			share = map[string]any{"state": "not_applicable", "value": nil, "reason": "logical_input_not_positive"}
		}
	}

	return map[string]any{
		"attempt_id":     attempt.AttemptID,
		"side":           attempt.Side,
		"phase":          attempt.Role,
		"cycle":          attempt.Cycle,
		"uncached_input": measurements["uncached_input"],
		"cache_read":     measurements["cache_read"],
		"cache_write":    measurements["cache_write"],
		"logical_input":  logicalInput,
		"share":          share,
		"formula":        "cache_read / logical_input",
		"unit":           "token_fraction",
		"source": map[string]any{
			"authority":            "retained raw runtime step-finish events",
			"artifact":             attempt.Artifacts["stream"],
			"accepted_model_steps": len(steps),
		},
		"identity_provenance": directIdentityProvenance(manifest, attempt, steps),
	}
}

func sumDirectMeasurements(events []SourceEvent, field string, measurement func(SourceEvent) IntegerMeasurement) (map[string]any, []any) {
	gaps := []any{}
	sum := new(big.Int)
	if len(events) == 0 {
		gaps = append(gaps, map[string]any{"field": field, "source_event_ordinal": nil, "state": "missing"})
	}
	for _, event := range events {
		value := measurement(event)
		if value.State != MeasurementMeasured || value.Value == nil || *value.Value < 0 {
			state := "missing"
			if value.State == MeasurementMalformed || (value.Value != nil && *value.Value < 0) {
				state = "malformed"
			}
			gaps = append(gaps, map[string]any{"field": field, "source_event_ordinal": event.Ordinal, "state": state})
			continue
		}
		sum.Add(sum, big.NewInt(*value.Value))
	}
	if len(gaps) > 0 {
		return unavailableDirectMeasurement(gaps), gaps
	}
	return measuredDirectInteger(sum), gaps
}

func measuredDirectInteger(value *big.Int) map[string]any {
	return map[string]any{"state": "measured", "value": value}
}

func unavailableDirectMeasurement(gaps []any) map[string]any {
	state := "missing"
	for _, raw := range gaps {
		if raw.(map[string]any)["state"] == "malformed" {
			state = "malformed"
			break
		}
	}
	return map[string]any{"state": state, "value": nil, "evidence_gaps": gaps}
}

func directIdentityProvenance(manifest Manifest, attempt Attempt, steps []SourceEvent) map[string]any {
	configuredAttempt := map[string]any{
		"model": nil, "provider": nil, "variant": nil, "configuration_sha256": nullableText(attempt.ConfigSHA256),
		"source": "attempt.cache_control", "scope": "attempt_context",
	}
	if attempt.CacheControl != nil {
		configuredAttempt["model"] = nullableText(attempt.CacheControl.Model)
		configuredAttempt["provider"] = nullableText(attempt.CacheControl.Provider)
		configuredAttempt["variant"] = nullableText(attempt.CacheControl.Variant)
	}
	configuredManifest := map[string]any{
		"model": nullableText(manifest.Model), "provider": nil, "variant": nil, "pipeline_sha256": nil,
		"source": "manifest", "scope": "experiment_context",
	}
	if manifest.CacheControl != nil {
		configuredManifest["provider"] = nullableText(manifest.CacheControl.Provider)
		configuredManifest["variant"] = nullableText(manifest.CacheControl.Variant)
	}
	if pipeline, ok := manifest.Pipelines[attempt.Side]; ok {
		configuredManifest["pipeline_sha256"] = nullableText(pipeline.SHA256)
	}
	sourceEvents := make([]any, 0, len(steps))
	for _, event := range steps {
		sourceEvents = append(sourceEvents, map[string]any{
			"source_event_ordinal": event.Ordinal,
			"runtime":              nullablePointer(event.Identity.Runtime),
			"provider":             nullablePointer(event.Identity.Provider),
			"model":                nullablePointer(event.Identity.Model),
			"session_id":           nullablePointer(event.Identity.SessionID),
			"message_id":           nullablePointer(event.Identity.MessageID),
			"part_id":              nullablePointer(event.Identity.PartID),
		})
	}
	return map[string]any{
		"runtime": map[string]any{
			"name": directCacheRuntime, "version": manifest.Identities[directCacheRuntime],
			"cache_control_version":    manifest.CacheControl.RuntimeVersion,
			"required_runtime_version": directCacheRuntimeVersion,
			"source":                   "manifest.identities.opencode and cache_control.runtime_version",
		},
		"configured_attempt":                          configuredAttempt,
		"configured_manifest":                         configuredManifest,
		"source_events":                               sourceEvents,
		"configured_identity_backfills_source_events": false,
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePointer(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
