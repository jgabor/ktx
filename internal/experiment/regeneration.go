package experiment

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/jgabor/ktx/internal/benchcontract"
	cacheevidence "github.com/jgabor/ktx/internal/cacheevidence/sequence"
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
	if attempt.SchemaVersion < 3 && (attempt.SchemaVersion < 2 || !hasDatabase || databaseRel == "" || !hasStream || streamRel == "") {
		return reconcileLegacyStreamProjection(experimentDir, attempt), nil
	}
	if attempt.SchemaVersion >= 3 && (!hasDatabase || databaseRel == "" || !hasStream || streamRel == "") {
		return invalidSchema3Attempt(attempt, Metrics{}, "schema 3 attempt is missing its required database or stream artifact"), nil
	}
	streamPath := filepath.Join(experimentDir, filepath.FromSlash(streamRel))
	if _, err := fileSHA256(streamPath); err != nil {
		if attempt.SchemaVersion >= 3 {
			return invalidSchema3Attempt(attempt, Metrics{}, "schema 3 attempt stream artifact is unavailable"), nil
		}
		return Attempt{}, fmt.Errorf("attempt %q read stream authority: %w", attempt.AttemptID, err)
	}
	metrics := ParseStream(streamPath, manifest.Task.ExpectedOutput, manifest.Task.ExpectedFacts)
	if attempt.SchemaVersion >= 3 {
		metrics = parseSchema3Stream(streamPath, manifest.Task.ExpectedOutput, manifest.Task.ExpectedFacts)
	}
	metrics.EstimatedAPICost = nil
	if attempt.SessionID == nil || *attempt.SessionID == "" {
		if attempt.SchemaVersion >= 3 {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 attempt is missing its explicit session owner"), nil
		}
		if attempt.Status == "completed" {
			return Attempt{}, fmt.Errorf("attempt %q requires a session identity for tool-call authority", attempt.AttemptID)
		}
		// A non-completed attempt cannot contribute to evaluated aggregates.
		// With no session identity there is no authoritative tool-call set, so
		// materialize an explicit zero rather than retaining its copied count.
		metrics.ToolCallCount = 0
		attempt.Metrics = metrics
		return attempt, nil
	}

	database, err := db.OpenReadOnly(filepath.Join(experimentDir, filepath.FromSlash(databaseRel)))
	if err != nil {
		if attempt.SchemaVersion >= 3 {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 attempt database is unavailable"), nil
		}
		return Attempt{}, fmt.Errorf("attempt %q open database authority: %w", attempt.AttemptID, err)
	}
	defer database.Close()
	if attempt.SchemaVersion >= 3 {
		if attempt.SessionLink.State != "linked" || attempt.SessionLink.NewSessionCount != 1 || len(attempt.SessionLink.NewSessionIDs) != 1 || attempt.SessionLink.NewSessionIDs[0] != *attempt.SessionID {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 explicit session link does not match its owner"), nil
		}
		var total int
		if err := database.QueryRow("SELECT count(*) FROM sessions").Scan(&total); err != nil || total != 1 {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 database does not contain exactly one session owner"), nil
		}
		var endedAt sql.NullString
		var agent string
		if err := database.QueryRow("SELECT agent, ended_at FROM sessions WHERE id = ?", *attempt.SessionID).Scan(&agent, &endedAt); err != nil || agent != "opencode" || !endedAt.Valid || endedAt.String == "" {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 explicit session owner is not closed"), nil
		}
	}
	var sessions int
	if err := database.QueryRow("SELECT count(*) FROM sessions WHERE id = ?", *attempt.SessionID).Scan(&sessions); err != nil {
		return Attempt{}, fmt.Errorf("attempt %q read session authority: %w", attempt.AttemptID, err)
	}
	if sessions != 1 {
		if attempt.SchemaVersion >= 3 {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 explicit session owner is missing or replaced"), nil
		}
		return Attempt{}, fmt.Errorf("attempt %q session authority is unavailable for %q", attempt.AttemptID, *attempt.SessionID)
	}
	var count int
	if err := database.QueryRow("SELECT count(*) FROM tool_calls WHERE session_id = ?", *attempt.SessionID).Scan(&count); err != nil {
		return Attempt{}, fmt.Errorf("attempt %q read tool-call authority: %w", attempt.AttemptID, err)
	}
	metrics.ToolCallCount = count
	if attempt.SchemaVersion >= 3 {
		var reason string
		metrics.sequence, reason = materializeEventHookSequence(database, attempt, metrics)
		if reason != "" {
			return invalidSchema3Attempt(attempt, metrics, reason), nil
		}
	}
	attempt.Metrics = metrics
	deliveryRows, err := db.QueryDeliveryObservations(database, *attempt.SessionID)
	if err != nil {
		return Attempt{}, fmt.Errorf("attempt %q read delivery authority: %w", attempt.AttemptID, err)
	}
	attempt.delivery = newDeliveryEvidence(databaseRel, deliveryRows)
	return attempt, nil
}

func invalidSchema3Attempt(attempt Attempt, metrics Metrics, reason string) Attempt {
	metrics.sequence = &attemptSequence{}
	attempt.Metrics = metrics
	attempt.validationReasons = append(attempt.validationReasons, reason)
	return attempt
}

func materializeEventHookSequence(database *sql.DB, attempt Attempt, metrics Metrics) (*attemptSequence, string) {
	if attempt.RuntimeEventIntervalID == nil || *attempt.RuntimeEventIntervalID == "" {
		return nil, "schema 3 attempt is missing its runtime event interval link"
	}
	var intervalCount int
	if err := database.QueryRow(`SELECT count(*) FROM runtime_event_intervals`).Scan(&intervalCount); err != nil || intervalCount != 1 {
		return nil, "schema 3 database does not contain exactly one runtime event interval"
	}
	var intervalID, owner, attemptID, runtime, runtimeVersion, source, contract, state string
	var first, last sql.NullInt64
	var closedAt, failureReason sql.NullString
	classifiedEvents := sqliteColumnExists(database, "runtime_events", "model_step_classification")
	intervalContradictions := sqliteColumnExists(database, "runtime_event_intervals", "contradiction_count")
	contradictionColumn := "0"
	if intervalContradictions {
		contradictionColumn = "contradiction_count"
	}
	var eventCount, epochs, contradictions int
	err := database.QueryRow(`SELECT id, session_id, attempt_id, runtime, runtime_version, source, contract_version, close_state, closed_at, first_ordinal, last_ordinal, event_count, collector_epoch_count, `+contradictionColumn+`, failure_reason FROM runtime_event_intervals`).Scan(&intervalID, &owner, &attemptID, &runtime, &runtimeVersion, &source, &contract, &state, &closedAt, &first, &last, &eventCount, &epochs, &contradictions, &failureReason)
	if err != nil {
		return nil, "schema 3 runtime event interval is unreadable"
	}
	if intervalID != *attempt.RuntimeEventIntervalID || owner != *attempt.SessionID || attemptID != attempt.AttemptID {
		return nil, "schema 3 runtime event interval ownership does not match the attempt"
	}
	if state != db.RuntimeComplete || !closedAt.Valid || failureReason.Valid {
		return nil, "schema 3 runtime event interval is not complete"
	}
	if contradictions != 0 {
		return nil, "schema 3 runtime event interval is contradictory"
	}
	var ownerEndedAt sql.NullString
	if err := database.QueryRow(`SELECT ended_at FROM sessions WHERE id = ?`, owner).Scan(&ownerEndedAt); err != nil || !ownerEndedAt.Valid || ownerEndedAt.String != closedAt.String {
		return nil, "schema 3 owner closure does not match the runtime event interval"
	}
	if runtime != cacheevidence.RequiredRuntime || runtimeVersion != cacheevidence.RequiredRuntimeVersion || source != "event_hook" || contract != benchcontract.ContractVersion {
		return nil, "schema 3 runtime event interval provenance does not match the required contract"
	}
	if !first.Valid || first.Int64 != 1 || !last.Valid || last.Int64 != int64(eventCount) || eventCount < 1 || epochs < 1 {
		return nil, "schema 3 runtime event interval counters are invalid"
	}
	classificationColumn := `CASE WHEN kind = 'model_step' THEN 'accepted' ELSE NULL END`
	if classifiedEvents {
		classificationColumn = "model_step_classification"
	}
	rows, err := database.Query(`SELECT ordinal, epoch_id, epoch_ordinal, kind, source_session_id, message_id, part_id, ` + classificationColumn + ` FROM runtime_events ORDER BY ordinal`)
	if err != nil {
		return nil, "schema 3 runtime events are unreadable"
	}
	defer rows.Close()
	result := &attemptSequence{}
	modelOrdinal := uint64(0)
	lastModelOrdinal := uint64(0)
	epochSeen := map[string]bool{}
	epochNext := map[string]uint64{}
	contiguous := true
	structureValid := true
	for rows.Next() {
		var ordinal, epochOrdinal uint64
		var epoch, kind string
		var session, message, part, classification sql.NullString
		if err := rows.Scan(&ordinal, &epoch, &epochOrdinal, &kind, &session, &message, &part, &classification); err != nil {
			return nil, "schema 3 runtime events are unreadable"
		}
		if ordinal != uint64(len(result.Events)+1) || epochOrdinal != epochNext[epoch] {
			contiguous = false
		}
		epochNext[epoch] = epochOrdinal + 1
		if (epochOrdinal == 0) != (kind == "collector_open") {
			structureValid = false
		}
		fact := cacheevidence.SequenceFact{Ordinal: ordinal, Provenance: cacheevidence.SequenceProvenance{Runtime: runtime, RuntimeVersion: runtimeVersion, Source: source, StartOrdinal: ordinal, EndOrdinal: ordinal}}
		if session.Valid {
			fact.SessionID = session.String
		}
		switch kind {
		case "model_step":
			if !session.Valid || session.String == "" || !message.Valid || message.String == "" || !part.Valid || part.String == "" || !classification.Valid {
				structureValid = false
			}
			switch classification.String {
			case db.ModelStepAccepted:
				modelOrdinal++
				fact.ModelStepOrdinal, lastModelOrdinal = modelOrdinal, ordinal
			case db.ModelStepExactDuplicate:
			case db.ModelStepConflictingDuplicate:
				structureValid = false
			default:
				structureValid = false
			}
		case "retry":
			fact.Boundary = cacheevidence.BoundaryRetry
		case "compaction":
			fact.Boundary = cacheevidence.BoundaryCompaction
		case "interruption":
			fact.Boundary = cacheevidence.BoundaryInterruption
		case "collector_open":
			if len(epochSeen) > 0 {
				fact.Boundary = cacheevidence.BoundaryRestart
			}
			epochSeen[epoch] = true
		case "other":
		default:
			structureValid = false
		}
		result.Events = append(result.Events, fact)
		if fact.ModelStepOrdinal > 0 {
			result.Facts = append(result.Facts, fact)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "schema 3 runtime events are unreadable"
	}
	if len(result.Events) != eventCount {
		return nil, "schema 3 runtime event count does not match its interval"
	}
	if !contiguous || len(epochSeen) != epochs || len(epochNext) != epochs {
		return result, "schema 3 runtime event ordinals are not contiguous"
	}
	if !structureValid {
		return result, "schema 3 runtime event rows violate the required event contract"
	}
	if len(result.Facts) == 0 {
		return result, "schema 3 runtime event interval has no model steps"
	}
	if len(result.Facts) != len(metrics.ModelSteps) || len(metrics.wrapperModelIdentities) != len(result.Facts) {
		return result, "schema 3 wrapper and event-hook model-step counts do not match"
	}
	modelIndex := 0
	acceptedPredicate := "kind = 'model_step'"
	if classifiedEvents {
		acceptedPredicate += " AND model_step_classification = 'accepted'"
	}
	rows, err = database.Query(`SELECT source_session_id, message_id, part_id FROM runtime_events WHERE ` + acceptedPredicate + ` ORDER BY ordinal`)
	if err != nil {
		return result, "schema 3 event-hook model-step identities are unreadable"
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID, messageID, partID sql.NullString
		if err := rows.Scan(&sessionID, &messageID, &partID); err != nil {
			return result, "schema 3 event-hook model-step identities are unreadable"
		}
		wrapper := metrics.wrapperModelIdentities[modelIndex]
		if wrapper.SessionID == "" || wrapper.MessageID == "" || wrapper.PartID == "" {
			return result, "schema 3 wrapper model-step identity is missing or partial"
		}
		if !sessionID.Valid || !messageID.Valid || !partID.Valid || sessionID.String != wrapper.SessionID || messageID.String != wrapper.MessageID || partID.String != wrapper.PartID {
			return result, "schema 3 wrapper and event-hook model-step identity order does not match"
		}
		modelIndex++
	}
	if err := rows.Err(); err != nil || modelIndex != len(result.Facts) {
		return result, "schema 3 event-hook model-step identities are unreadable"
	}
	if lastModelOrdinal == 0 {
		return result, "schema 3 runtime event interval has no model steps"
	}
	filtered := result.Events[:0]
	for _, fact := range result.Events {
		if fact.Ordinal <= lastModelOrdinal {
			filtered = append(filtered, fact)
		}
	}
	result.Events = filtered
	provenance := cacheevidence.SequenceProvenance{Runtime: runtime, RuntimeVersion: runtimeVersion, Source: source, StartOrdinal: 1, EndOrdinal: lastModelOrdinal}
	for _, class := range []string{"order", "retry", "reset", "segment", "concurrency", "identity"} {
		result.Coverage = append(result.Coverage, cacheevidence.CoverageFact{Class: class, State: cacheevidence.CoverageCovered, Provenance: provenance})
	}
	return result, ""
}

func sqliteColumnExists(database *sql.DB, table, column string) bool {
	var count int
	return database.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count) == nil && count == 1
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
	RuntimeEventIntervalID                          *string
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
		SessionID: a.SessionID, RuntimeEventIntervalID: a.RuntimeEventIntervalID, RecordedStreams: a.RecordedStreams, RecordedStreamID: a.RecordedStreamID,
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
