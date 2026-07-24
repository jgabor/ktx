package cacheevidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jgabor/ktx/internal/experiment"
)

type GeminiParityEvidence struct {
	SchemaVersion   int                      `json:"schema_version"`
	Native          GeminiNativeEvidence     `json:"native"`
	Normalized      RetainedSource           `json:"normalized"`
	FieldAccounting []GeminiFieldDisposition `json:"field_accounting"`
}

type GeminiNativeEvidence struct {
	Source       string                     `json:"source"`
	Version      string                     `json:"version"`
	Raw          string                     `json:"raw"`
	Outcome      string                     `json:"outcome"`
	Unavailable  *string                    `json:"unavailable_reason,omitempty"`
	CacheObject  *GeminiCacheObjectEvidence `json:"cache_object,omitempty"`
	Observations []experiment.SourceEvent   `json:"observations"`
}

type GeminiCacheObjectEvidence struct {
	NameSHA256   string                        `json:"name_sha256"`
	Model        string                        `json:"model"`
	CreateTime   string                        `json:"create_time"`
	UpdateTime   string                        `json:"update_time"`
	ExpireTime   string                        `json:"expire_time"`
	TTLRequested string                        `json:"ttl_requested"`
	TotalTokens  experiment.IntegerMeasurement `json:"total_tokens"`
	Retry        experiment.RetryMetadata      `json:"retry"`
}

type GeminiFieldDisposition struct {
	NativeField     string `json:"native_field"`
	NormalizedField string `json:"normalized_field,omitempty"`
	Disposition     string `json:"disposition"`
	Detail          string `json:"detail"`
}

type geminiRecord struct {
	Outcome     string `json:"outcome"`
	CacheCreate struct {
		Request struct {
			Model        string `json:"model"`
			TTL          string `json:"ttl"`
			RetryAttempt int    `json:"retry_attempt"`
			MaxAttempts  int    `json:"max_attempts"`
		} `json:"request"`
		Response struct {
			NameSHA256 string `json:"name_sha256"`
			Model      string `json:"model"`
			CreateTime string `json:"create_time"`
			UpdateTime string `json:"update_time"`
			ExpireTime string `json:"expire_time"`
			Usage      struct {
				TotalTokenCount json.Number `json:"total_token_count"`
			} `json:"usage_metadata"`
		} `json:"response"`
	} `json:"cache_create"`
	Generate struct {
		Request struct {
			Model                   string `json:"model"`
			CachedContentNameSHA256 string `json:"cached_content_name_sha256"`
			MaxOutputTokens         int64  `json:"max_output_tokens"`
			RetryAttempt            int    `json:"retry_attempt"`
			MaxAttempts             int    `json:"max_attempts"`
		} `json:"request"`
		Response struct {
			ResponseID   string `json:"response_id"`
			ModelVersion string `json:"model_version"`
			Usage        struct {
				PromptTokenCount        json.Number `json:"prompt_token_count"`
				CachedContentTokenCount json.Number `json:"cached_content_token_count"`
				CandidatesTokenCount    json.Number `json:"candidates_token_count"`
				ThoughtsTokenCount      json.Number `json:"thoughts_token_count"`
				TotalTokenCount         json.Number `json:"total_token_count"`
			} `json:"usage_metadata"`
		} `json:"response"`
	} `json:"generate"`
	Error *struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NormalizeGemini(raw []byte, version string) (GeminiNativeEvidence, error) {
	var record geminiRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return GeminiNativeEvidence{}, fmt.Errorf("decode Gemini evidence: %w", err)
	}
	retained := GeminiNativeEvidence{
		Source:  "Gemini Developer API cachedContents and generateContent",
		Version: version,
		Raw:     string(raw),
		Outcome: record.Outcome,
	}
	if record.Outcome == "unavailable" {
		if record.Error == nil || strings.TrimSpace(record.Error.Status) == "" || strings.TrimSpace(record.Error.Message) == "" {
			return GeminiNativeEvidence{}, fmt.Errorf("unavailable Gemini evidence requires exact status and message")
		}
		reason := record.Error.Status + ": " + record.Error.Message
		retained.Unavailable = &reason
		return retained, nil
	}
	if record.Outcome != "observed" {
		return GeminiNativeEvidence{}, fmt.Errorf("unsupported Gemini evidence outcome %q", record.Outcome)
	}
	if record.Generate.Response.ResponseID == "" || record.Generate.Response.ModelVersion == "" {
		return GeminiNativeEvidence{}, fmt.Errorf("gemini generateContent evidence lacks response identity")
	}
	if record.CacheCreate.Response.NameSHA256 == "" || record.Generate.Request.CachedContentNameSHA256 != record.CacheCreate.Response.NameSHA256 {
		return GeminiNativeEvidence{}, fmt.Errorf("gemini cached-content identity is absent or inconsistent")
	}
	cacheTokens, _ := integer(record.CacheCreate.Response.Usage.TotalTokenCount)
	retained.CacheObject = &GeminiCacheObjectEvidence{
		NameSHA256:   record.CacheCreate.Response.NameSHA256,
		Model:        record.CacheCreate.Response.Model,
		CreateTime:   record.CacheCreate.Response.CreateTime,
		UpdateTime:   record.CacheCreate.Response.UpdateTime,
		ExpireTime:   record.CacheCreate.Response.ExpireTime,
		TTLRequested: record.CacheCreate.Request.TTL,
		TotalTokens:  cacheTokens,
		Retry: experiment.RetryMetadata{
			Attempt: &record.CacheCreate.Request.RetryAttempt, MaxAttempts: &record.CacheCreate.Request.MaxAttempts,
			Availability: text("reported_by_probe_client"),
		},
	}
	prompt, promptState := integer(record.Generate.Response.Usage.PromptTokenCount)
	read, readState := integer(record.Generate.Response.Usage.CachedContentTokenCount)
	uncached := experiment.UnknownInteger()
	if promptState == experiment.MeasurementMeasured && readState == experiment.MeasurementMeasured {
		if value := *prompt.Value - *read.Value; value >= 0 {
			uncached = experiment.MeasuredInteger(value)
		} else {
			uncached = experiment.MalformedInteger()
		}
	} else if promptState == experiment.MeasurementMalformed || readState == experiment.MeasurementMalformed {
		uncached = experiment.MalformedInteger()
	}
	output, _ := integer(record.Generate.Response.Usage.CandidatesTokenCount)
	reasoning, _ := integer(record.Generate.Response.Usage.ThoughtsTokenCount)
	provider, runtime := "google", "direct"
	retryAvailability := "reported_by_probe_client"
	mode := "explicit_cached_content"
	eligibility := "gemini-2.5-flash; explicit cache content at least 2048 tokens"
	event := experiment.SourceEvent{
		Ordinal: 1, Kind: experiment.NativeRequest,
		UncachedInput: uncached, Output: output, CacheRead: read,
		CacheWrite: experiment.UnknownInteger(), Reasoning: reasoning,
		Cost: experiment.CostMeasurement{State: experiment.MeasurementUnknown},
		Identity: experiment.EventIdentity{
			Provider: &provider, Runtime: &runtime, Model: text(record.Generate.Response.ModelVersion),
			ResponseID:        text(record.Generate.Response.ResponseID),
			ProviderRequestID: text(record.Generate.Response.ResponseID),
		},
		Control: experiment.EventControl{
			Mode: &mode, NamespaceHash: text(record.Generate.Request.CachedContentNameSHA256),
			Eligibility: &eligibility, MaxOutputTokens: &record.Generate.Request.MaxOutputTokens,
		},
		Retry: experiment.RetryMetadata{
			Attempt: &record.Generate.Request.RetryAttempt, MaxAttempts: &record.Generate.Request.MaxAttempts,
			Availability: &retryAvailability,
		},
	}
	if err := experiment.ValidateSourceEvents([]experiment.SourceEvent{event}); err != nil {
		return GeminiNativeEvidence{}, err
	}
	retained.Observations = []experiment.SourceEvent{event}
	return retained, nil
}

func BuildGeminiParityEvidence(nativeRaw, normalizedRaw []byte, geminiVersion, openCodeVersion string) (GeminiParityEvidence, error) {
	native, err := NormalizeGemini(nativeRaw, geminiVersion)
	if err != nil {
		return GeminiParityEvidence{}, err
	}
	normalized, err := NormalizeOpenCode(normalizedRaw, openCodeVersion)
	if err != nil {
		return GeminiParityEvidence{}, err
	}
	return GeminiParityEvidence{
		SchemaVersion:   SchemaVersion,
		Native:          native,
		Normalized:      normalized,
		FieldAccounting: geminiFieldAccounting(),
	}, nil
}

func geminiFieldAccounting() []GeminiFieldDisposition {
	return []GeminiFieldDisposition{
		{NativeField: "CachedContent.name", Disposition: "dropped", Detail: "OpenCode step-finish has no cache-object identity; the sanitized native fixture retains only its SHA-256"},
		{NativeField: "CachedContent.expireTime/ttl", Disposition: "unavailable", Detail: "OpenCode 1.18.4 does not wire the out-of-band explicit CachedContent API"},
		{NativeField: "CachedContent.usageMetadata.totalTokenCount", Disposition: "unavailable", Detail: "OpenCode step-finish has no explicit cache-creation token field or cache-write equivalent for Gemini"},
		{NativeField: "GenerateContentResponse.usageMetadata.promptTokenCount", NormalizedField: "tokens.input + tokens.cache.read", Disposition: "split", Detail: "OpenCode subtracts cachedContentTokenCount and reports the non-cached remainder as input"},
		{NativeField: "GenerateContentResponse.usageMetadata.cachedContentTokenCount", NormalizedField: "tokens.cache.read", Disposition: "renamed", Detail: "integer token count is preserved without rounding"},
		{NativeField: "GenerateContentResponse.usageMetadata.candidatesTokenCount", NormalizedField: "tokens.output", Disposition: "renamed", Detail: "visible output is preserved without rounding"},
		{NativeField: "GenerateContentResponse.usageMetadata.thoughtsTokenCount", NormalizedField: "tokens.reasoning", Disposition: "renamed", Detail: "measured zero remains measured zero; absence remains unavailable"},
		{NativeField: "GenerateContentResponse.usageMetadata.totalTokenCount", Disposition: "dropped", Detail: "OpenCode step-finish exposes the component fields used by this evidence path, not the provider total"},
		{NativeField: "GenerateContentResponse.responseId", Disposition: "dropped", Detail: "OpenCode step-finish exposes session, message, and part identity but not Gemini response identity"},
		{NativeField: "GenerateContentResponse.modelVersion", NormalizedField: "part.model", Disposition: "weakened", Detail: "OpenCode reports its requested model identity; a resolved Gemini model version is unavailable"},
		{NativeField: "HTTP/client retry attempt", Disposition: "unavailable", Detail: "OpenCode step-finish has no retry count or provider request identity; the direct probe records its own attempt ceiling"},
		{NativeField: "reported provider cost", Disposition: "unavailable", Detail: "Gemini generateContent usage does not report currency cost; OpenCode cost is runtime-calculated"},
	}
}
