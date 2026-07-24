package cacheevidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jgabor/ktx/internal/experiment"
	"github.com/jgabor/ktx/internal/usage"
)

// ParityEvidence retains each source independently. Raw is the exact input to
// normalization, while Observations is an additive interpretation of it.
type ParityEvidence struct {
	SchemaVersion int            `json:"schema_version"`
	Native        RetainedSource `json:"native"`
	Normalized    RetainedSource `json:"normalized"`
}

type RetainedSource struct {
	Source       string                   `json:"source"`
	Version      string                   `json:"version"`
	Raw          string                   `json:"raw"`
	Observations []experiment.SourceEvent `json:"observations"`
}

type openAIRecord struct {
	Request struct {
		Model                string `json:"model"`
		ReasoningEffort      string `json:"reasoning_effort"`
		MaxOutputTokens      int64  `json:"max_output_tokens"`
		PromptCacheKeySHA256 string `json:"prompt_cache_key_sha256"`
		PromptCacheMode      string `json:"prompt_cache_mode"`
		RetryAttempt         int    `json:"retry_attempt"`
		MaxAttempts          int    `json:"max_attempts"`
	} `json:"request"`
	Response struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens        json.Number `json:"input_tokens"`
			OutputTokens       json.Number `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens     json.Number `json:"cached_tokens"`
				CacheWriteTokens json.Number `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens json.Number `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
	ResponseHeaders struct {
		RequestID string `json:"x_request_id"`
	} `json:"response_headers"`
}

func NormalizeOpenAI(raw []byte, version string) (RetainedSource, error) {
	var record openAIRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return RetainedSource{}, fmt.Errorf("decode OpenAI evidence: %w", err)
	}
	input, inputState := integer(record.Response.Usage.InputTokens)
	read, readState := integer(record.Response.Usage.InputTokensDetails.CachedTokens)
	write, writeState := integer(record.Response.Usage.InputTokensDetails.CacheWriteTokens)
	uncached := experiment.UnknownInteger()
	if inputState == experiment.MeasurementMeasured && readState == experiment.MeasurementMeasured && writeState == experiment.MeasurementMeasured {
		value := *input.Value - *read.Value - *write.Value
		if value >= 0 {
			uncached = experiment.MeasuredInteger(value)
		} else {
			uncached = experiment.MalformedInteger()
		}
	} else if inputState == experiment.MeasurementMalformed || readState == experiment.MeasurementMalformed || writeState == experiment.MeasurementMalformed {
		uncached = experiment.MalformedInteger()
	}
	output, _ := integer(record.Response.Usage.OutputTokens)
	reasoning, _ := integer(record.Response.Usage.OutputTokensDetails.ReasoningTokens)
	provider, runtime := "openai", "direct"
	retryAvailability := "reported_by_probe_client"
	event := experiment.SourceEvent{
		Ordinal: 1, Kind: experiment.NativeRequest,
		UncachedInput: uncached, Output: output, CacheRead: read, CacheWrite: write, Reasoning: reasoning,
		Cost: experiment.CostMeasurement{State: experiment.MeasurementUnknown},
		Identity: experiment.EventIdentity{
			Provider: &provider, Runtime: &runtime, Model: text(record.Response.Model),
			ResponseID: text(record.Response.ID), RequestID: text(record.ResponseHeaders.RequestID),
			ProviderRequestID: text(record.ResponseHeaders.RequestID),
		},
		Control: experiment.EventControl{
			Mode: text(record.Request.PromptCacheMode), NamespaceHash: text(record.Request.PromptCacheKeySHA256),
			MaxOutputTokens: &record.Request.MaxOutputTokens,
		},
		Retry: experiment.RetryMetadata{Attempt: &record.Request.RetryAttempt, MaxAttempts: &record.Request.MaxAttempts, Availability: &retryAvailability},
	}
	if err := experiment.ValidateSourceEvents([]experiment.SourceEvent{event}); err != nil {
		return RetainedSource{}, err
	}
	return RetainedSource{Source: "OpenAI Responses API", Version: version, Raw: string(raw), Observations: []experiment.SourceEvent{event}}, nil
}

func NormalizeOpenCode(raw []byte, version string) (RetainedSource, error) {
	parsed := usage.Parse(bytes.NewReader(raw))
	if !parsed.HasData {
		return RetainedSource{}, fmt.Errorf("OpenCode evidence has no step-finish observation")
	}
	identities := openCodeIdentities(raw)
	events := make([]experiment.SourceEvent, len(parsed.ModelSteps))
	for i, step := range parsed.ModelSteps {
		runtime := "opencode"
		retryAvailability := "unavailable_in_step_finish"
		controlAvailability := "unavailable_in_step_finish"
		identity := experiment.EventIdentity{Provider: step.Provider, Runtime: &runtime, Model: step.Model}
		if i < len(identities) {
			identity.SessionID = text(identities[i].SessionID)
			identity.MessageID = text(identities[i].MessageID)
			identity.PartID = text(identities[i].PartID)
		}
		events[i] = experiment.SourceEvent{
			Ordinal: step.Ordinal, Kind: experiment.ModelStep,
			UncachedInput: convertInteger(step.UncachedInput), Output: convertInteger(step.Output),
			CacheRead: convertInteger(step.CacheRead), CacheWrite: convertInteger(step.CacheWrite),
			Reasoning: convertInteger(step.Reasoning), Cost: convertCost(step.Cost),
			Identity: identity,
			Control:  experiment.EventControl{Availability: &controlAvailability},
			Retry:    experiment.RetryMetadata{Availability: &retryAvailability},
		}
	}
	if err := experiment.ValidateSourceEvents(events); err != nil {
		return RetainedSource{}, err
	}
	return RetainedSource{Source: "OpenCode step-finish event", Version: version, Raw: string(raw), Observations: events}, nil
}

type openCodeIdentity struct{ SessionID, MessageID, PartID string }

func openCodeIdentities(raw []byte) []openCodeIdentity {
	var identities []openCodeIdentity
	usage.ReadDelimitedLines(bytes.NewReader(raw), usage.MaxRecordSize, func(line []byte) bool {
		var record struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Part      struct {
				ID        string `json:"id"`
				SessionID string `json:"sessionID"`
				MessageID string `json:"messageID"`
			} `json:"part"`
		}
		if json.Unmarshal(line, &record) == nil && record.Type == "step_finish" {
			sessionID := record.Part.SessionID
			if sessionID == "" {
				sessionID = record.SessionID
			}
			identities = append(identities, openCodeIdentity{SessionID: sessionID, MessageID: record.Part.MessageID, PartID: record.Part.ID})
		}
		return false
	})
	return identities
}

func BuildParityEvidence(nativeRaw, normalizedRaw []byte, openAIVersion, openCodeVersion string) (ParityEvidence, error) {
	native, err := NormalizeOpenAI(nativeRaw, openAIVersion)
	if err != nil {
		return ParityEvidence{}, err
	}
	normalized, err := NormalizeOpenCode(normalizedRaw, openCodeVersion)
	if err != nil {
		return ParityEvidence{}, err
	}
	return ParityEvidence{SchemaVersion: SchemaVersion, Native: native, Normalized: normalized}, nil
}

func integer(number json.Number) (experiment.IntegerMeasurement, experiment.MeasurementState) {
	if strings.TrimSpace(string(number)) == "" {
		return experiment.UnknownInteger(), experiment.MeasurementUnknown
	}
	value, err := number.Int64()
	if err != nil || value < 0 {
		return experiment.MalformedInteger(), experiment.MeasurementMalformed
	}
	return experiment.MeasuredInteger(value), experiment.MeasurementMeasured
}

func convertInteger(value usage.IntegerMeasurement) experiment.IntegerMeasurement {
	switch value.State {
	case usage.MeasurementMeasured:
		if value.Value != nil {
			return experiment.MeasuredInteger(*value.Value)
		}
	case usage.MeasurementMalformed:
		return experiment.MalformedInteger()
	}
	return experiment.UnknownInteger()
}

func convertCost(value usage.CostMeasurement) experiment.CostMeasurement {
	if value.State == usage.MeasurementMeasured && value.Value != nil {
		return experiment.MeasuredCost(*value.Value)
	}
	if value.State == usage.MeasurementMalformed {
		return experiment.CostMeasurement{State: experiment.MeasurementMalformed}
	}
	return experiment.CostMeasurement{State: experiment.MeasurementUnknown}
}

func text(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}
