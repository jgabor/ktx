package reuse

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/jgabor/ktx/internal/experiment"
)

type anthropicRecord struct {
	Request struct {
		Model           string `json:"model"`
		APIVersion      string `json:"anthropic_version"`
		MaxOutputTokens int64  `json:"max_tokens"`
		CacheControl    struct {
			Type string `json:"type"`
			TTL  string `json:"ttl"`
		} `json:"cache_control"`
		Breakpoint   string `json:"breakpoint"`
		Eligibility  string `json:"eligibility"`
		RetryAttempt int    `json:"retry_attempt"`
		MaxAttempts  int    `json:"max_attempts"`
	} `json:"request"`
	Response struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              json.Number `json:"input_tokens"`
			CacheCreationInputTokens json.Number `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     json.Number `json:"cache_read_input_tokens"`
			OutputTokens             json.Number `json:"output_tokens"`
		} `json:"usage"`
	} `json:"response"`
	ResponseHeaders struct {
		RequestID string `json:"request-id"`
	} `json:"response_headers"`
}

func NormalizeAnthropic(raw []byte, version string) (RetainedSource, error) {
	var record anthropicRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return RetainedSource{}, fmt.Errorf("decode Anthropic evidence: %w", err)
	}
	input, _ := integer(record.Response.Usage.InputTokens)
	creation, _ := integer(record.Response.Usage.CacheCreationInputTokens)
	read, _ := integer(record.Response.Usage.CacheReadInputTokens)
	output, _ := integer(record.Response.Usage.OutputTokens)
	provider, runtime := "anthropic", "direct"
	mode := record.CacheMode()
	retryAvailability := "reported_by_probe_client"
	event := experiment.SourceEvent{
		Ordinal: 1, Kind: experiment.NativeRequest,
		UncachedInput: input, CacheRead: read, CacheWrite: creation, Output: output,
		Reasoning: experiment.UnknownInteger(), Cost: experiment.CostMeasurement{State: experiment.MeasurementUnknown},
		Identity: experiment.EventIdentity{
			Provider: &provider, Runtime: &runtime, Model: text(record.Response.Model),
			ResponseID: text(record.Response.ID), RequestID: text(record.ResponseHeaders.RequestID),
			ProviderRequestID: text(record.ResponseHeaders.RequestID),
		},
		Control: experiment.EventControl{
			Mode: text(mode), Breakpoint: text(record.Request.Breakpoint), Eligibility: text(record.Request.Eligibility),
			MaxOutputTokens: &record.Request.MaxOutputTokens,
		},
		Retry: experiment.RetryMetadata{Attempt: &record.Request.RetryAttempt, MaxAttempts: &record.Request.MaxAttempts, Availability: &retryAvailability},
	}
	if err := experiment.ValidateSourceEvents([]experiment.SourceEvent{event}); err != nil {
		return RetainedSource{}, err
	}
	return RetainedSource{Source: "Anthropic Messages API", Version: version, Raw: string(raw), Observations: []experiment.SourceEvent{event}}, nil
}

func (r anthropicRecord) CacheMode() string {
	if r.Request.CacheControl.Type == "" {
		return ""
	}
	if r.Request.CacheControl.TTL == "" {
		return r.Request.CacheControl.Type
	}
	return r.Request.CacheControl.Type + "; ttl=" + r.Request.CacheControl.TTL
}

func BuildAnthropicParityEvidence(nativeRaw, normalizedRaw []byte, anthropicVersion, openCodeVersion string) (ParityEvidence, error) {
	native, err := NormalizeAnthropic(nativeRaw, anthropicVersion)
	if err != nil {
		return ParityEvidence{}, err
	}
	normalized, err := NormalizeOpenCode(normalizedRaw, openCodeVersion)
	if err != nil {
		return ParityEvidence{}, err
	}
	return ParityEvidence{SchemaVersion: SchemaVersion, Native: native, Normalized: normalized}, nil
}
