package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jgabor/ktx/internal/telemetry"
)

func TestOpenMigratesExistingDatabaseForSessionTelemetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	raw, err := sql.Open(sqliteDriver, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY, agent TEXT, started_at TEXT, ended_at TEXT,
		last_seen_at TEXT, tool_call_count INTEGER DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO sessions (id, agent, tool_call_count) VALUES ('abcdefghijklmnop', 'opencode', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if count := tableRowCount(t, d.db, "session_telemetry"); count != 0 {
		t.Fatalf("new telemetry table rows = %d, want 0", count)
	}
	session, err := d.GetSession("abcdefghijklmnop")
	if err != nil || session.ToolCallCount != 3 {
		t.Fatalf("existing session = %#v, %v", session, err)
	}
}

func TestSessionTelemetryPersistsSeparatelyAndListsMeasurements(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID := mustNewID(t)
	if err := d.InsertSession(&Session{ID: sessionID, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	provider := "openai"
	model := "gpt-5"
	observation := telemetryObservation("ses_source", "msg_1", "prt_1", 1, 0, 7)
	observation.Provider = &provider
	observation.Model = &model

	inserted, err := d.PersistSessionTelemetry(sessionID, "2026-07-24T10:00:00Z", observation)
	if err != nil || !inserted {
		t.Fatalf("PersistSessionTelemetry() = %t, %v", inserted, err)
	}
	rows, err := d.ListSessionTelemetry(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.SessionID != sessionID || got.SourceSessionID != "ses_source" || got.MessageID != "msg_1" || got.PartID != "prt_1" || got.Ordinal != 1 {
		t.Fatalf("identity/order = %#v", got)
	}
	if got.Provider == nil || *got.Provider != provider || got.Model == nil || *got.Model != model {
		t.Fatalf("provider/model = %#v/%#v", got.Provider, got.Model)
	}
	if got.Runtime != "test-runtime" || got.RuntimeVersion != "1.0.0" {
		t.Fatalf("runtime identity = %q/%q", got.Runtime, got.RuntimeVersion)
	}
	if got.Source != telemetry.SourceEventHook {
		t.Fatalf("source = %q, want %q", got.Source, telemetry.SourceEventHook)
	}
	if got.CacheRead.State != MeasurementMeasured || got.CacheRead.Value == nil || *got.CacheRead.Value != 0 {
		t.Fatalf("cache_read = %#v, want measured zero", got.CacheRead)
	}
	if got.CacheWrite.State != MeasurementMeasured || got.CacheWrite.Value == nil || *got.CacheWrite.Value != 7 {
		t.Fatalf("cache_write = %#v, want measured 7", got.CacheWrite)
	}
	for _, table := range []string{"tool_calls", "bench_runs", "recorded_streams"} {
		if count := tableRowCount(t, d.db, table); count != 0 {
			t.Errorf("%s rows = %d, want 0", table, count)
		}
	}
}

func TestSessionTelemetryPersistsUnknownAndMalformedFields(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID := mustNewID(t)
	if err := d.InsertSession(&Session{ID: sessionID, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	observation := telemetryObservation("ses_source", "msg_1", "prt_1", 1, 0, 7)
	observation.Tokens.Output = telemetry.IntegerMeasurement{State: telemetry.MeasurementUnknown}
	observation.Tokens.Reasoning = telemetry.IntegerMeasurement{State: telemetry.MeasurementMalformed}
	observation.Cost = telemetry.CostMeasurement{State: telemetry.MeasurementUnknown}
	observation.Source = telemetry.SourceWrapperStream
	if inserted, err := d.PersistSessionTelemetry(sessionID, "2026-07-24T10:00:00Z", observation); err != nil || !inserted {
		t.Fatalf("PersistSessionTelemetry() = %t, %v", inserted, err)
	}
	rows, err := d.ListSessionTelemetry(sessionID, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListSessionTelemetry() = %#v, %v", rows, err)
	}
	got := rows[0]
	if got.Output.State != MeasurementUnknown || got.Output.Value != nil || got.Reasoning.State != MeasurementMalformed || got.Reasoning.Value != nil || got.Cost.State != MeasurementUnknown || got.Cost.Value != nil {
		t.Fatalf("incomplete measurements changed: %#v", got)
	}
	if got.UncachedInput.Value == nil || *got.UncachedInput.Value != 1 || got.Source != telemetry.SourceWrapperStream {
		t.Fatalf("measured sibling/provenance changed: %#v", got)
	}
}

func TestSessionTelemetryDuplicateConcurrentAndAtomicAssociation(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "telemetry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID := mustNewID(t)
	if err := d.InsertSession(&Session{ID: sessionID, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	observation := telemetryObservation("ses_source", "msg_1", "prt_1", 1, 3, 4)

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.PersistSessionTelemetry(sessionID, "2026-07-24T10:00:00Z", observation)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.ListSessionTelemetry(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows after concurrent retries = %d, want 1", len(rows))
	}
	if rows[0].Provider != nil || rows[0].Model != nil {
		t.Fatalf("unavailable identity was invented: %#v", rows[0])
	}
	crossIngress := *observation
	crossIngress.Source = telemetry.SourceWrapperStream
	if inserted, err := d.PersistSessionTelemetry(sessionID, "2026-07-24T11:00:00Z", &crossIngress); err != nil || inserted {
		t.Fatalf("cross-ingress exact duplicate = %t, %v", inserted, err)
	}
	if _, err := d.PersistSessionTelemetry("aaaaaaaaaaaaaaaa", "2026-07-24T10:00:00Z", telemetryObservation("ses_other", "msg_2", "prt_2", 2, 1, 1)); !errors.Is(err, ErrSessionTelemetrySourceNotFound) {
		t.Fatalf("unknown session error = %v", err)
	}
	if count := tableRowCount(t, d.db, "session_telemetry"); count != 1 {
		t.Fatalf("rows after rejected association = %d, want 1", count)
	}
}

func TestSessionTelemetryRuntimeQualifiedIdentityAndFirstEvidence(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	firstOwner := mustNewID(t)
	secondOwner := mustNewID(t)
	for _, sessionID := range []string{firstOwner, secondOwner} {
		if err := d.InsertSession(&Session{ID: sessionID, Agent: "opencode"}); err != nil {
			t.Fatal(err)
		}
	}

	first := telemetryObservation("ses_source", "msg_1", "prt_1", 1, 3, 4)
	provider, model := "provider-first", "model-first"
	first.Provider, first.Model = &provider, &model
	if inserted, err := d.PersistSessionTelemetry(firstOwner, "2026-07-24T10:00:00Z", first); err != nil || !inserted {
		t.Fatalf("first insert = %t, %v", inserted, err)
	}

	conflict := telemetryObservation("ses_source", "msg_1", "prt_1", 99, 30, 40)
	conflict.Reason = "conflicting"
	conflict.Cost = telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: float64Pointer(9.5)}
	conflict.Tokens.Input = telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: int64Pointer(10)}
	conflict.Tokens.Output = telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: int64Pointer(20)}
	conflict.Tokens.Reasoning = telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: int64Pointer(30)}
	conflict.RuntimeVersion = "2.0.0"
	conflict.Source = telemetry.SourceWrapperStream
	otherProvider, otherModel := "provider-second", "model-second"
	conflict.Provider, conflict.Model = &otherProvider, &otherModel
	if inserted, err := d.PersistSessionTelemetry(secondOwner, "2026-07-24T11:00:00Z", conflict); !errors.Is(err, ErrSessionTelemetryConflict) || inserted {
		t.Fatalf("conflicting retry = %t, %v; want conflict", inserted, err)
	}

	otherRuntime := *conflict
	otherRuntime.Runtime = "other-runtime"
	if inserted, err := d.PersistSessionTelemetry(secondOwner, "2026-07-24T12:00:00Z", &otherRuntime); err != nil || !inserted {
		t.Fatalf("other runtime insert = %t, %v", inserted, err)
	}

	rows, err := d.ListSessionTelemetry(firstOwner, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("first owner rows = %#v, %v", rows, err)
	}
	got := rows[0]
	if got.SessionID != firstOwner || got.Ordinal != 1 || got.Reason != "stop" || got.RuntimeVersion != "1.0.0" ||
		got.Source != telemetry.SourceEventHook ||
		got.Provider == nil || *got.Provider != provider || got.Model == nil || *got.Model != model ||
		got.Cost.Value == nil || *got.Cost.Value != 0 || got.CacheRead.Value == nil || *got.CacheRead.Value != 3 ||
		got.CacheWrite.Value == nil || *got.CacheWrite.Value != 4 || got.CapturedAt != "2026-07-24T10:00:00Z" {
		t.Fatalf("first evidence changed: %#v", got)
	}
	rows, err = d.ListSessionTelemetry(secondOwner, 10)
	if err != nil || len(rows) != 1 || rows[0].Runtime != "other-runtime" {
		t.Fatalf("second runtime rows = %#v, %v", rows, err)
	}
}

func TestSessionTelemetryConcurrentConflictHasOneCompleteWinner(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "telemetry-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	owners := []string{mustNewID(t), mustNewID(t)}
	for _, owner := range owners {
		if err := d.InsertSession(&Session{ID: owner, Agent: "opencode"}); err != nil {
			t.Fatal(err)
		}
	}
	observations := []*telemetry.Observation{
		telemetryObservation("ses_source", "msg_1", "prt_1", 1, 11, 12),
		telemetryObservation("ses_source", "msg_1", "prt_1", 2, 21, 22),
	}
	for i, observation := range observations {
		observation.Reason = []string{"winner-a", "winner-b"}[i]
		observation.Cost = telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: float64Pointer([]float64{1.25, 2.5}[i])}
		observation.Tokens.Input = telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: int64Pointer(int64(100 + i))}
		observation.RuntimeVersion = []string{"1.0.0", "2.0.0"}[i]
		observation.Source = []string{telemetry.SourceEventHook, telemetry.SourceWrapperStream}[i]
		provider, model := []string{"provider-a", "provider-b"}[i], []string{"model-a", "model-b"}[i]
		observation.Provider, observation.Model = &provider, &model
	}

	start := make(chan struct{})
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range observations {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			inserted, err := d.PersistSessionTelemetry(owners[i], []string{"captured-a", "captured-b"}[i], observations[i])
			results <- inserted
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	inserted := 0
	for result := range results {
		if result {
			inserted++
		}
	}
	conflicts := 0
	for err := range errs {
		if errors.Is(err, ErrSessionTelemetryConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if inserted != 1 || conflicts != 1 {
		t.Fatalf("successful inserts/conflicts = %d/%d, want 1/1", inserted, conflicts)
	}

	var owner, sourceSessionID, messageID, partID, reason, provider, model, runtime, runtimeVersion, source, capturedAt string
	var ordinal, input, output, reasoning, cacheRead, cacheWrite int64
	var cost float64
	if err := d.db.QueryRow(`SELECT session_id, source_session_id, message_id, part_id, ordinal, reason, cost,
		tokens_input, tokens_output, tokens_reasoning, cache_read, cache_write,
		provider, model, runtime, runtime_version, source, captured_at FROM session_telemetry`).Scan(
		&owner, &sourceSessionID, &messageID, &partID, &ordinal, &reason, &cost,
		&input, &output, &reasoning, &cacheRead, &cacheWrite,
		&provider, &model, &runtime, &runtimeVersion, &source, &capturedAt,
	); err != nil {
		t.Fatal(err)
	}
	winner := 0
	if owner == owners[1] {
		winner = 1
	} else if owner != owners[0] {
		t.Fatalf("unexpected owner %q", owner)
	}
	if sourceSessionID != "ses_source" || messageID != "msg_1" || partID != "prt_1" || runtime != "test-runtime" ||
		ordinal != int64(winner+1) || reason != []string{"winner-a", "winner-b"}[winner] ||
		cost != []float64{1.25, 2.5}[winner] || input != int64(100+winner) || output != 2 || reasoning != 0 ||
		cacheRead != []int64{11, 21}[winner] || cacheWrite != []int64{12, 22}[winner] ||
		provider != []string{"provider-a", "provider-b"}[winner] || model != []string{"model-a", "model-b"}[winner] ||
		runtimeVersion != []string{"1.0.0", "2.0.0"}[winner] || source != []string{telemetry.SourceEventHook, telemetry.SourceWrapperStream}[winner] || capturedAt != []string{"captured-a", "captured-b"}[winner] {
		t.Fatalf("mixed concurrent winner: owner=%q source=%q message=%q part=%q ordinal=%d reason=%q cost=%v input=%d output=%d reasoning=%d read=%d write=%d provider=%q model=%q runtime=%q version=%q captured=%q",
			owner, sourceSessionID, messageID, partID, ordinal, reason, cost, input, output, reasoning, cacheRead, cacheWrite, provider, model, runtime, runtimeVersion, capturedAt)
	}
}

func TestSessionTelemetryFailurePreservesExistingEvidence(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID := mustNewID(t)
	if err := d.InsertSession(&Session{ID: sessionID, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	toolCallID := mustNewID(t)
	if err := d.PersistToolCall(&ToolCall{ID: toolCallID, SessionID: sessionID, Tool: "bash", Output: "original", Timestamp: "2026-07-24T09:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`CREATE TRIGGER fail_optional_telemetry BEFORE INSERT ON session_telemetry BEGIN SELECT RAISE(ABORT, 'optional failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PersistSessionTelemetry(sessionID, "2026-07-24T10:00:00Z", telemetryObservation("ses_source", "msg_1", "prt_1", 1, 1, 1)); err == nil {
		t.Fatal("PersistSessionTelemetry() succeeded with failing trigger")
	}
	session, err := d.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	toolCall, err := d.GetToolCall(toolCallID)
	if err != nil {
		t.Fatal(err)
	}
	if session.ToolCallCount != 1 || toolCall.Output != "original" || tableRowCount(t, d.db, "session_telemetry") != 0 {
		t.Fatalf("existing evidence changed after optional failure: %#v", session)
	}
}

func telemetryObservation(sessionID, messageID, partID string, ordinal uint64, read, write int64) *telemetry.Observation {
	return &telemetry.Observation{
		Key:     telemetry.Key{SessionID: sessionID, MessageID: messageID, PartID: partID},
		Ordinal: ordinal,
		Reason:  "stop",
		Cost:    telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: float64Pointer(0)},
		Tokens: telemetry.Tokens{
			Input:      telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: int64Pointer(1)},
			Output:     telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: int64Pointer(2)},
			Reasoning:  telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: int64Pointer(0)},
			CacheRead:  telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &read},
			CacheWrite: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &write},
		},
		Runtime:        "test-runtime",
		RuntimeVersion: "1.0.0",
		Source:         telemetry.SourceEventHook,
	}
}

func int64Pointer(value int64) *int64       { return &value }
func float64Pointer(value float64) *float64 { return &value }
