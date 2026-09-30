package experiment

import (
	"database/sql"
	"fmt"
	"io"
	"reflect"

	"github.com/jgabor/ktx/internal/benchcontract"
	"github.com/jgabor/ktx/internal/cache/reuse/sequence"
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

// materializeAttempts deduplicates append-only attempt envelopes and replaces
// copied measurements with values from their retained field authorities.
func materializeAttempts(experimentDir string, manifest Manifest, attempts []Attempt) ([]Attempt, []ProjectionDivergence, error) {
	a, err := openArchive(experimentDir)
	if err != nil {
		return nil, nil, err
	}
	defer a.Close()
	return a.materializeAttempts(manifest, attempts)
}

func (a *archive) materializeAttempts(manifest Manifest, attempts []Attempt) ([]Attempt, []ProjectionDivergence, error) {
	if err := a.validateReadDescription(manifest); err != nil {
		return nil, nil, err
	}
	first := make(map[string]int)
	records := make(map[string][]int)
	order := make([]string, 0)
	for i, attempt := range attempts {
		prior, exists := first[attempt.AttemptID]
		if !exists {
			first[attempt.AttemptID] = i
			order = append(order, attempt.AttemptID)
		} else if !reflect.DeepEqual(orchestrationOf(attempts[prior]), orchestrationOf(attempt)) {
			return nil, nil, fmt.Errorf("conflicting authoritative duplicate attempt %q", attempt.AttemptID)
		}
		records[attempt.AttemptID] = append(records[attempt.AttemptID], i)
	}

	materialized := make([]Attempt, 0, len(order))
	divergences := []ProjectionDivergence{}
	for _, id := range order {
		authoritative, err := a.materializeAttempt(manifest, attempts[first[id]])
		if err != nil {
			return nil, nil, err
		}
		materialized = append(materialized, authoritative)
		for ordinal, index := range records[id] {
			divergences = append(divergences, metricDivergences(authoritative, attempts[index], ordinal+1)...)
		}
	}
	if err := validateReadDescriptionComparability(materialized); err != nil {
		return nil, nil, err
	}
	if err := validatePairOrder(manifest, materialized); err != nil {
		return nil, nil, err
	}
	materialized, err := a.applyCostControls(manifest, materialized, divergences)
	if err != nil {
		return nil, nil, err
	}
	return materialized, divergences, nil
}

func materializeAttempt(experimentDir string, manifest Manifest, attempt Attempt) (Attempt, error) {
	a, err := openArchive(experimentDir)
	if err != nil {
		return Attempt{}, err
	}
	defer a.Close()
	return a.materializeAttempt(manifest, attempt)
}

func (a *archive) materializeAttempt(manifest Manifest, attempt Attempt) (Attempt, error) {
	definitions, definitionErr := a.readDescriptionDefinitions(manifest, &attempt)
	if definitionErr != nil && (manifest.ReadDescription == nil || unsafeArchiveAccess(definitionErr)) {
		return Attempt{}, fmt.Errorf("attempt %q: %w", attempt.AttemptID, definitionErr)
	}
	attempt.readDefinitions = definitions
	databaseRel, hasDatabase := attempt.Artifacts["database"]
	streamRel, hasStream := attempt.Artifacts["stream"]
	if attempt.SchemaVersion != 3 {
		return Attempt{}, fmt.Errorf("attempt %q has missing or unsupported schema_version %d", attempt.AttemptID, attempt.SchemaVersion)
	}
	if !hasDatabase || databaseRel == "" || !hasStream || streamRel == "" {
		return invalidSchema3Attempt(attempt, Metrics{}, "schema 3 attempt is missing its required database or stream artifact"), nil
	}
	stream, err := a.open(streamRel)
	if err != nil {
		if unsafeArchiveAccess(err) {
			return Attempt{}, err
		}
		attempt.calls = &callAccounting{Stream: unavailableCallAuthority("stream_authority_unavailable"), Persistence: unavailableCallAuthority("persistence_authority_unavailable")}
		return invalidSchema3Attempt(attempt, Metrics{}, "schema 3 attempt stream artifact is unavailable"), nil
	}
	defer stream.Close()
	metrics := parseStreamReader(stream, manifest.Task.ExpectedOutput, manifest.Task.ExpectedFacts, true)
	if attempt.calls == nil {
		if _, err := stream.Seek(0, io.SeekStart); err != nil {
			return Attempt{}, err
		}
		attempt.calls = &callAccounting{Stream: readStreamCallAuthority(stream), Persistence: unavailableCallAuthority("persistence_authority_unavailable")}
	} else {
		// Each materialized attempt owns its persistence result even if ledger
		// projections shared the stream observation during entry validation.
		attempt.calls = &callAccounting{Stream: attempt.calls.Stream, Persistence: unavailableCallAuthority("persistence_authority_unavailable")}
	}
	metrics.EstimatedAPICost = nil
	if definitionErr != nil {
		// A failed follow-up can leave valid first-turn usage but incomplete
		// application/response evidence. Preserve those observations for cost
		// accounting without granting the attempt completion or pass eligibility.
		return invalidSchema3Attempt(attempt, metrics, fmt.Sprintf("read-description evidence: %v", definitionErr)), nil
	}
	if attempt.SessionID == nil || *attempt.SessionID == "" {
		return invalidSchema3Attempt(attempt, metrics, "schema 3 attempt is missing its explicit session owner"), nil
	}

	database, cleanup, err := a.openDatabase(databaseRel)
	if err != nil {
		if unsafeArchiveAccess(err) {
			return Attempt{}, err
		}
		return invalidSchema3Attempt(attempt, metrics, "schema 3 attempt database is unavailable"), nil
	}
	defer cleanup()
	defer database.Close()
	if manifest.ReadDescription != nil {
		interval, err := database.LoadRuntimeEventInterval(*attempt.RuntimeEventIntervalID)
		if err != nil {
			return Attempt{}, err
		}
		epochs := map[string]bool{}
		for _, event := range interval.Events {
			if event.Kind == "collector_open" {
				epochs[event.EpochID] = true
			}
		}
		if !reflect.DeepEqual(epochs, attempt.readEpochs) {
			return Attempt{}, fmt.Errorf("attempt %q read-description epochs differ from collector", attempt.AttemptID)
		}
	}
	{
		if attempt.SessionLink.State != "linked" || attempt.SessionLink.NewSessionCount != 1 || len(attempt.SessionLink.NewSessionIDs) != 1 || attempt.SessionLink.NewSessionIDs[0] != *attempt.SessionID {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 explicit session link does not match its owner"), nil
		}
		var total int
		if err := database.DB().QueryRow("SELECT count(*) FROM sessions").Scan(&total); err != nil || total != 1 {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 database does not contain exactly one session owner"), nil
		}
		var endedAt sql.NullString
		var agent string
		if err := database.DB().QueryRow("SELECT agent, ended_at FROM sessions WHERE id = ?", *attempt.SessionID).Scan(&agent, &endedAt); err != nil || agent != "opencode" || !endedAt.Valid || endedAt.String == "" {
			return invalidSchema3Attempt(attempt, metrics, "schema 3 explicit session owner is not closed"), nil
		}
	}
	attempt.calls.Persistence = persistedCallAuthority(database.DB(), *attempt.SessionID)
	var sessions int
	if err := database.DB().QueryRow("SELECT count(*) FROM sessions WHERE id = ?", *attempt.SessionID).Scan(&sessions); err != nil {
		return Attempt{}, fmt.Errorf("attempt %q read session authority: %w", attempt.AttemptID, err)
	}
	if sessions != 1 {
		return invalidSchema3Attempt(attempt, metrics, "schema 3 explicit session owner is missing or replaced"), nil
	}
	var count int
	if err := database.DB().QueryRow("SELECT count(*) FROM tool_calls WHERE session_id = ?", *attempt.SessionID).Scan(&count); err != nil {
		return Attempt{}, fmt.Errorf("attempt %q read tool-call authority: %w", attempt.AttemptID, err)
	}
	metrics.ToolCallCount = count
	{
		var reason string
		metrics.sequence, reason = materializeEventHookSequence(database.DB(), attempt, metrics)
		if reason != "" {
			return invalidSchema3Attempt(attempt, metrics, reason), nil
		}
	}
	attempt.Metrics = metrics
	deliveryRows, err := db.QueryDeliveryObservations(database.DB(), *attempt.SessionID)
	if err != nil {
		return Attempt{}, fmt.Errorf("attempt %q read delivery authority: %w", attempt.AttemptID, err)
	}
	attempt.delivery = newDeliveryEvidence(databaseRel, deliveryRows)
	attempt.outcomes = materializeAttemptOutcomes(database.DB(), *attempt.SessionID)
	if manifest.ReadDescription != nil && manifest.Task.ScoringKind == "read_preservation" {
		if err := checkReadWorkspace(a, attempt, manifest.Task); err != nil {
			if unsafeArchiveAccess(err) {
				return Attempt{}, err
			}
			return invalidSchema3Attempt(attempt, metrics, fmt.Sprintf("read-preservation input: %v", err)), nil
		}
		attempt.Score = scoreReadPreservation(manifest.Task, *attempt.SessionID, database.DB(), func(turn int) (io.ReadCloser, error) {
			return a.open(attempt.Artifacts[fmt.Sprintf("turn_%d", turn)])
		})
		if attempt.Status != "completed" || attempt.ProcessExitCode != 0 || !metrics.StreamComplete {
			attempt.FullPass, attempt.Score.Score = false, 0
		}
	}
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
	var eventCount, epochs, contradictions int
	err := database.QueryRow(`SELECT id, session_id, attempt_id, runtime, runtime_version, source, contract_version, close_state, closed_at, first_ordinal, last_ordinal, event_count, collector_epoch_count, contradiction_count, failure_reason FROM runtime_event_intervals`).Scan(&intervalID, &owner, &attemptID, &runtime, &runtimeVersion, &source, &contract, &state, &closedAt, &first, &last, &eventCount, &epochs, &contradictions, &failureReason)
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
	if runtime != sequence.RequiredRuntime || runtimeVersion != sequence.RequiredRuntimeVersion || source != "event_hook" {
		return nil, "schema 3 runtime event interval provenance does not match the required contract"
	}
	if contract != benchcontract.ContractVersion {
		return nil, "schema 3 runtime event interval has unsupported contract version"
	}
	if !first.Valid || first.Int64 != 1 || !last.Valid || last.Int64 != int64(eventCount) || eventCount < 1 || epochs < 1 {
		return nil, "schema 3 runtime event interval counters are invalid"
	}
	rows, err := database.Query(`SELECT ordinal, epoch_id, epoch_ordinal, kind, source_session_id, message_id, part_id, model_step_classification FROM runtime_events ORDER BY ordinal`)
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
		fact := sequence.SequenceFact{Ordinal: ordinal, Provenance: sequence.SequenceProvenance{Runtime: runtime, RuntimeVersion: runtimeVersion, Source: source, StartOrdinal: ordinal, EndOrdinal: ordinal}}
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
			fact.Boundary = sequence.BoundaryRetry
		case "compaction":
			fact.Boundary = sequence.BoundaryCompaction
		case "interruption":
			fact.Boundary = sequence.BoundaryInterruption
		case "collector_open":
			if len(epochSeen) > 0 {
				fact.Boundary = sequence.BoundaryRestart
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
	rows, err = database.Query(`SELECT source_session_id, message_id, part_id FROM runtime_events WHERE kind = 'model_step' AND model_step_classification = 'accepted' ORDER BY ordinal`)
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
	provenance := sequence.SequenceProvenance{Runtime: runtime, RuntimeVersion: runtimeVersion, Source: source, StartOrdinal: 1, EndOrdinal: lastModelOrdinal}
	for _, class := range []string{"order", "retry", "reset", "segment", "concurrency", "identity"} {
		result.Coverage = append(result.Coverage, sequence.CoverageFact{Class: class, State: sequence.CoverageCovered, Provenance: provenance})
	}
	return result, ""
}

type attemptOrchestration struct {
	SchemaVersion                                   int
	ExperimentID, AttemptID, Side, Role, PipelineID string
	CacheControl                                    *CacheControlMetadata
	Cycle, Ordinal                                  int
	FirstArm                                        string
	ExecutionOrder                                  int
	StartedAt, EndedAt, Status                      string
	ProcessExitCode                                 int
	SessionLink                                     SessionLink
	SessionID                                       *string
	RuntimeEventIntervalID                          *string
	RecordedStreams                                 []RecordedStreamLink
	RecordedStreamID, BenchRunID                    *string
	ConfigSHA256                                    string
	RuntimeConfigSHA256                             string
	ReadDescriptionTreatment                        string
	Artifacts, ContentHashes                        map[string]string
}

func orchestrationOf(a Attempt) attemptOrchestration {
	return attemptOrchestration{
		SchemaVersion: a.SchemaVersion, ExperimentID: a.ExperimentID, AttemptID: a.AttemptID,
		Side: a.Side, Role: a.Role, PipelineID: a.PipelineID, CacheControl: a.CacheControl,
		Cycle: a.Cycle, Ordinal: a.Ordinal, StartedAt: a.StartedAt, EndedAt: a.EndedAt,
		Status: a.Status, ProcessExitCode: a.ProcessExitCode, SessionLink: a.SessionLink,
		SessionID: a.SessionID, RuntimeEventIntervalID: a.RuntimeEventIntervalID, RecordedStreams: a.RecordedStreams, RecordedStreamID: a.RecordedStreamID,
		BenchRunID: a.BenchRunID, ConfigSHA256: a.ConfigSHA256, RuntimeConfigSHA256: a.RuntimeConfigSHA256, Artifacts: a.Artifacts,
		ReadDescriptionTreatment: a.ReadDescriptionTreatment,
		FirstArm:                 a.FirstArm, ExecutionOrder: a.ExecutionOrder,
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
