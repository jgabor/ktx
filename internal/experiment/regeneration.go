package experiment

import (
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/jgabor/ktx/internal/db"
)

// ProjectionDivergence describes a copied attempt measurement that disagrees
// with the retained artifact that owns the field. It is diagnostic only and is
// never an input to summary aggregation.
type ProjectionDivergence struct {
	AttemptID     string `json:"attempt_id"`
	RecordOrdinal int    `json:"record_ordinal"`
	Field         string `json:"field"`
	Projected     any    `json:"projected"`
	Authoritative any    `json:"authoritative"`
}

// materializeAttempts deduplicates append-only attempt envelopes and, when an
// attempt declares the cache-aware retained-artifact contract, replaces copied
// measurements with values from their field authorities.
func materializeAttempts(experimentDir string, manifest Manifest, attempts []Attempt) ([]Attempt, []ProjectionDivergence, error) {
	first := make(map[string]Attempt, len(attempts))
	records := make(map[string][]Attempt, len(attempts))
	order := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		prior, exists := first[attempt.AttemptID]
		if !exists {
			first[attempt.AttemptID] = attempt
			order = append(order, attempt.AttemptID)
		} else if !reflect.DeepEqual(orchestrationOf(prior), orchestrationOf(attempt)) {
			return nil, nil, fmt.Errorf("conflicting authoritative duplicate attempt %q", attempt.AttemptID)
		}
		records[attempt.AttemptID] = append(records[attempt.AttemptID], attempt)
	}

	materialized := make([]Attempt, 0, len(order))
	divergences := []ProjectionDivergence{}
	for _, id := range order {
		authoritative, err := materializeAttempt(experimentDir, manifest, first[id])
		if err != nil {
			return nil, nil, err
		}
		materialized = append(materialized, authoritative)
		for ordinal, projection := range records[id] {
			divergences = append(divergences, metricDivergences(authoritative, projection, ordinal+1)...)
		}
	}
	return materialized, divergences, nil
}

func materializeAttempt(experimentDir string, manifest Manifest, attempt Attempt) (Attempt, error) {
	databaseRel, hasDatabase := attempt.Artifacts["database"]
	streamRel, hasStream := attempt.Artifacts["stream"]
	// Legacy attempt schemas did not promise these lower-level authorities.
	if attempt.SchemaVersion < 2 || !hasDatabase || databaseRel == "" || !hasStream || streamRel == "" {
		return reconcileLegacyStreamProjection(experimentDir, attempt), nil
	}
	if attempt.SessionID == nil || *attempt.SessionID == "" {
		return Attempt{}, fmt.Errorf("attempt %q requires a session identity for tool-call authority", attempt.AttemptID)
	}

	streamPath := filepath.Join(experimentDir, filepath.FromSlash(streamRel))
	if _, err := fileSHA256(streamPath); err != nil {
		return Attempt{}, fmt.Errorf("attempt %q read stream authority: %w", attempt.AttemptID, err)
	}
	metrics := ParseStream(streamPath, manifest.Task.ExpectedOutput, manifest.Task.ExpectedFacts)

	database, err := db.OpenReadOnly(filepath.Join(experimentDir, filepath.FromSlash(databaseRel)))
	if err != nil {
		return Attempt{}, fmt.Errorf("attempt %q open database authority: %w", attempt.AttemptID, err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow("SELECT count(*) FROM tool_calls WHERE session_id = ?", *attempt.SessionID).Scan(&count); err != nil {
		return Attempt{}, fmt.Errorf("attempt %q read tool-call authority: %w", attempt.AttemptID, err)
	}
	metrics.ToolCallCount = count
	metrics.EstimatedAPICost = nil
	attempt.Metrics = metrics
	return attempt, nil
}

func reconcileLegacyStreamProjection(experimentDir string, attempt Attempt) Attempt {
	hasCacheEvidence := attempt.CacheRead != nil || attempt.CacheWrite != nil ||
		attempt.UsageEvidence.CacheReadEvents > 0 || attempt.UsageEvidence.CacheWriteEvents > 0 ||
		attempt.Reasoning != nil || attempt.UsageEvidence.ReasoningEvents > 0
	streamRel := attempt.Artifacts["stream"]
	if !hasCacheEvidence || streamRel == "" {
		return attempt
	}
	metrics := ParseStream(filepath.Join(experimentDir, filepath.FromSlash(streamRel)), nil, nil)
	attempt.CacheRead = metrics.CacheRead
	attempt.CacheWrite = metrics.CacheWrite
	attempt.UsageEvidence.CacheReadEvents = metrics.UsageEvidence.CacheReadEvents
	attempt.UsageEvidence.CacheWriteEvents = metrics.UsageEvidence.CacheWriteEvents
	attempt.Reasoning = metrics.Reasoning
	attempt.UsageEvidence.ReasoningEvents = metrics.UsageEvidence.ReasoningEvents
	return attempt
}

type attemptOrchestration struct {
	SchemaVersion                                   int
	ExperimentID, AttemptID, Side, Role, PipelineID string
	CacheControl                                    *CacheControlMetadata
	Cycle, Ordinal                                  int
	StartedAt, EndedAt, Status                      string
	ProcessExitCode                                 int
	SessionLink                                     SessionLink
	SessionID                                       *string
	RecordedStreams                                 []RecordedStreamLink
	RecordedStreamID, BenchRunID                    *string
	ConfigSHA256                                    string
	Artifacts, ContentHashes                        map[string]string
}

func orchestrationOf(a Attempt) attemptOrchestration {
	return attemptOrchestration{
		SchemaVersion: a.SchemaVersion, ExperimentID: a.ExperimentID, AttemptID: a.AttemptID,
		Side: a.Side, Role: a.Role, PipelineID: a.PipelineID, CacheControl: a.CacheControl,
		Cycle: a.Cycle, Ordinal: a.Ordinal, StartedAt: a.StartedAt, EndedAt: a.EndedAt,
		Status: a.Status, ProcessExitCode: a.ProcessExitCode, SessionLink: a.SessionLink,
		SessionID: a.SessionID, RecordedStreams: a.RecordedStreams, RecordedStreamID: a.RecordedStreamID,
		BenchRunID: a.BenchRunID, ConfigSHA256: a.ConfigSHA256, Artifacts: a.Artifacts,
		ContentHashes: a.ContentHashes,
	}
}

func metricDivergences(authoritative, projection Attempt, recordOrdinal int) []ProjectionDivergence {
	fields := []struct {
		name string
		a, p any
	}{
		{"stream_complete", authoritative.StreamComplete, projection.StreamComplete},
		{"score", authoritative.Score, projection.Score},
		{"tool_call_count", authoritative.ToolCallCount, projection.ToolCallCount},
		{"tokens_in", authoritative.TokensIn, projection.TokensIn},
		{"tokens_out", authoritative.TokensOut, projection.TokensOut},
		{"cost", authoritative.Cost, projection.Cost},
		{"cache_read", authoritative.CacheRead, projection.CacheRead},
		{"cache_write", authoritative.CacheWrite, projection.CacheWrite},
		{"reasoning", authoritative.Reasoning, projection.Reasoning},
		{"answer_excerpt", authoritative.AnswerExcerpt, projection.AnswerExcerpt},
		{"usage_evidence", authoritative.UsageEvidence, projection.UsageEvidence},
		{"model_steps", authoritative.ModelSteps, projection.ModelSteps},
	}
	result := []ProjectionDivergence{}
	for _, field := range fields {
		if !reflect.DeepEqual(field.a, field.p) {
			result = append(result, ProjectionDivergence{AttemptID: authoritative.AttemptID, RecordOrdinal: recordOrdinal, Field: field.name, Projected: field.p, Authoritative: field.a})
		}
	}
	return result
}
