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
	if _, err := d.PersistSessionTelemetry("aaaaaaaaaaaaaaaa", "2026-07-24T10:00:00Z", telemetryObservation("ses_other", "msg_2", "prt_2", 2, 1, 1)); !errors.Is(err, ErrSessionTelemetrySourceNotFound) {
		t.Fatalf("unknown session error = %v", err)
	}
	if count := tableRowCount(t, d.db, "session_telemetry"); count != 1 {
		t.Fatalf("rows after rejected association = %d, want 1", count)
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
		Cost:    0,
		Tokens: telemetry.Tokens{
			Input: 1, Output: 2, Reasoning: 0, CacheRead: read, CacheWrite: write,
		},
		Runtime:        "test-runtime",
		RuntimeVersion: "1.0.0",
	}
}
