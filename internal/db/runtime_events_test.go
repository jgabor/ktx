package db

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jgabor/ktx/internal/telemetry"
)

func TestRuntimeEventSchemaMigratesVersionsZeroThroughSix(t *testing.T) {
	for version := 0; version <= 6; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ktx.db")
			database, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.db.Exec(`DROP TABLE runtime_events; DROP TABLE runtime_event_intervals; PRAGMA user_version = ` + fmt.Sprint(version)); err != nil {
				t.Fatal(err)
			}
			database.Close()
			database, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			var got int
			if err := database.db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil || got != schemaVersion {
				t.Fatalf("user_version=%d err=%v", got, err)
			}
			for _, table := range []string{"runtime_event_intervals", "runtime_events"} {
				if count := tableRowCount(t, database.db, table); count != 0 {
					t.Fatalf("%s rows=%d", table, count)
				}
			}
			indexes := queryStrings(t, database.db, `SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='runtime_events' ORDER BY name`)
			if len(indexes) < 4 {
				t.Fatalf("runtime event indexes=%v", indexes)
			}
		})
	}
}

func TestRuntimeEventSchemaMigratesVersionSevenRowsAsAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ktx.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	interval, err := database.CreateBenchmarkEventInterval("attempt", "opencode", "1.18.5", "opencode-event-v1", "open")
	if err != nil {
		t.Fatal(err)
	}
	legacy := `DROP TABLE runtime_events;
		DROP TABLE runtime_event_intervals;
		CREATE TABLE runtime_event_intervals (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id), attempt_id TEXT NOT NULL UNIQUE,
			runtime TEXT NOT NULL, runtime_version TEXT NOT NULL, source TEXT NOT NULL CHECK (source = 'event_hook'),
			contract_version TEXT NOT NULL, opened_at TEXT NOT NULL, closed_at TEXT,
			close_state TEXT NOT NULL CHECK (close_state IN ('open','complete','incomplete','contradictory')),
			first_ordinal INTEGER, last_ordinal INTEGER, event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
			collector_epoch_count INTEGER NOT NULL DEFAULT 0 CHECK (collector_epoch_count >= 0), failure_reason TEXT,
			CHECK ((close_state = 'open') = (closed_at IS NULL)),
			CHECK ((event_count = 0 AND first_ordinal IS NULL AND last_ordinal IS NULL) OR (event_count > 0 AND first_ordinal = 1 AND last_ordinal = event_count)));
		CREATE TABLE runtime_events (
			interval_id TEXT NOT NULL REFERENCES runtime_event_intervals(id), ordinal INTEGER NOT NULL CHECK (ordinal > 0),
			epoch_id TEXT NOT NULL, epoch_ordinal INTEGER NOT NULL CHECK (epoch_ordinal >= 0),
			kind TEXT NOT NULL CHECK (kind IN ('collector_open','model_step','retry','compaction','interruption','other')),
			event_type TEXT NOT NULL, source_session_id TEXT, message_id TEXT, part_id TEXT, captured_at TEXT NOT NULL,
			PRIMARY KEY (interval_id, ordinal), UNIQUE (interval_id, epoch_id, epoch_ordinal));`
	if _, err := database.db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`INSERT INTO runtime_event_intervals
		(id,session_id,attempt_id,runtime,runtime_version,source,contract_version,opened_at,closed_at,close_state,first_ordinal,last_ordinal,event_count,collector_epoch_count)
		VALUES (?,?,?,?,?,'event_hook','opencode-event-v1','open','closed','complete',1,2,2,1)`, interval.ID, interval.SessionID, "attempt", "opencode", "1.18.5"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`INSERT INTO runtime_events VALUES
		(?,1,'epoch',0,'collector_open','collector.open',NULL,NULL,NULL,'open'),
		(?,2,'epoch',1,'model_step','message.part.updated','runtime','message','part','step');
		PRAGMA user_version = 7`, interval.ID, interval.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows := queryStrings(t, database.db, `SELECT kind, model_step_classification FROM runtime_events ORDER BY ordinal`)
	if got, want := fmt.Sprint(rows), "[[collector_open <nil>] [model_step accepted]]"; got != want {
		t.Fatalf("migrated classifications=%s want=%s", got, want)
	}
	var version, contradictions int
	if err := database.db.QueryRow(`SELECT contradiction_count FROM runtime_event_intervals`).Scan(&contradictions); err != nil || contradictions != 0 {
		t.Fatalf("contradictions=%d err=%v", contradictions, err)
	}
	if err := database.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
}

func TestRuntimeEventLedgerIsAtomicIdempotentAndConflictSafe(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("baseline-001", "opencode", "1.18.5", "opencode-event-v1", "open")
	if err != nil {
		t.Fatal(err)
	}
	open := RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", Kind: "collector_open", EventType: "collector.open", CapturedAt: "one"}
	first, inserted, err := database.PersistRuntimeEvent(open)
	if err != nil || !inserted || first.Ordinal != 1 {
		t.Fatalf("first=%#v inserted=%t err=%v", first, inserted, err)
	}
	open.CapturedAt = "retry-time"
	replayed, inserted, err := database.PersistRuntimeEvent(open)
	if err != nil || inserted || replayed.CapturedAt != "one" {
		t.Fatalf("replay=%#v inserted=%t err=%v", replayed, inserted, err)
	}
	conflict := open
	conflict.EventType = "different"
	if _, _, err := database.PersistRuntimeEvent(conflict); !errors.Is(err, ErrRuntimeEventConflict) {
		t.Fatalf("conflict err=%v", err)
	}

	session, message, part := "runtime-a", "message-a", "part-a"
	zero := int64(0)
	cost := float64(0)
	observation := &telemetry.Observation{Key: telemetry.Key{SessionID: session, MessageID: message, PartID: part}, Ordinal: 1, Reason: "stop", Runtime: "opencode", RuntimeVersion: "1.18.5", Source: telemetry.SourceEventHook, Cost: telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: &cost}, Tokens: telemetry.Tokens{Input: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, Output: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, Reasoning: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheRead: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheWrite: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}}}
	step := RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", EpochOrdinal: 1, Kind: "model_step", EventType: "message.part.updated", SourceSessionID: &session, MessageID: &message, PartID: &part, CapturedAt: "two", Telemetry: observation}
	if event, inserted, err := database.PersistRuntimeEvent(step); err != nil || !inserted || event.Ordinal != 2 {
		t.Fatalf("step=%#v inserted=%t err=%v", event, inserted, err)
	}
	step.CapturedAt = "retry"
	if event, inserted, err := database.PersistRuntimeEvent(step); err != nil || inserted || event.Ordinal != 2 {
		t.Fatalf("step replay=%#v inserted=%t err=%v", event, inserted, err)
	}
	conflictingStep := *observation
	conflictingStep.Reason = "different"
	step.Telemetry = &conflictingStep
	if _, _, err := database.PersistRuntimeEvent(step); !errors.Is(err, ErrRuntimeEventConflict) {
		t.Fatalf("step conflict err=%v", err)
	}
	step.Telemetry = observation
	var telemetryCount int
	if err := database.db.QueryRow(`SELECT count(*) FROM session_telemetry`).Scan(&telemetryCount); err != nil || telemetryCount != 1 {
		t.Fatalf("telemetry=%d err=%v", telemetryCount, err)
	}
	if err := database.SealRuntimeEventInterval(interval.ID, interval.SessionID, "closed", RuntimeComplete, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.LoadRuntimeEventInterval(interval.ID)
	if err != nil || loaded.CloseState != RuntimeComplete || loaded.EventCount != 2 || loaded.CollectorEpochCount != 1 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestRuntimeEventModelStepOccurrencesClassifyWithoutOverwritingAuthority(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("attempt", "opencode", "1.18.5", "opencode-event-v2", "open")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.PersistRuntimeEvent(RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch", Kind: "collector_open", EventType: "collector.open", CapturedAt: "open"}); err != nil {
		t.Fatal(err)
	}
	step := func(epochOrdinal uint64, part string, input int64) RuntimeEventInput {
		session, message, reason := "runtime", "message-"+part, "stop"
		zero := int64(0)
		cost := float64(input) / 100
		observation := &telemetry.Observation{Key: telemetry.Key{SessionID: session, MessageID: message, PartID: part}, Ordinal: 1, Reason: reason, Runtime: "opencode", RuntimeVersion: "1.18.5", Source: telemetry.SourceEventHook, Cost: telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: &cost}, Tokens: telemetry.Tokens{Input: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &input}, Output: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, Reasoning: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheRead: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheWrite: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}}}
		return RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch", EpochOrdinal: epochOrdinal, Kind: "model_step", EventType: "message.part.updated", SourceSessionID: &session, MessageID: &message, PartID: &part, CapturedAt: fmt.Sprintf("event-%d", epochOrdinal), Telemetry: observation}
	}
	inputs := []RuntimeEventInput{step(1, "w1", 10), step(2, "w2", 20), step(3, "w2", 20), step(4, "w3", 30)}
	want := []string{ModelStepAccepted, ModelStepAccepted, ModelStepExactDuplicate, ModelStepAccepted}
	for index, input := range inputs {
		event, inserted, err := database.PersistRuntimeEvent(input)
		if err != nil || !inserted || event.ModelStepClassification == nil || *event.ModelStepClassification != want[index] {
			t.Fatalf("event %d=%#v inserted=%t err=%v", index, event, inserted, err)
		}
	}
	loaded, err := database.LoadRuntimeEventInterval(interval.ID)
	if err != nil || loaded.EventCount != 5 || loaded.ContradictionCount != 0 || ValidateRuntimeEventInterval(loaded) != nil {
		t.Fatalf("interval=%#v err=%v validation=%v", loaded, err, ValidateRuntimeEventInterval(loaded))
	}
	rows := queryStrings(t, database.db, `SELECT part_id, ordinal FROM session_telemetry ORDER BY ordinal`)
	if got, wantRows := fmt.Sprint(rows), "[[w1 2] [w2 3] [w3 5]]"; got != wantRows {
		t.Fatalf("accepted telemetry=%s want=%s", got, wantRows)
	}
	if err := database.SealRuntimeEventInterval(interval.ID, interval.SessionID, "closed", RuntimeComplete, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeEventConflictingDuplicateCommitsContradiction(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("attempt", "opencode", "1.18.5", "opencode-event-v2", "open")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.PersistRuntimeEvent(RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch", Kind: "collector_open", EventType: "collector.open", CapturedAt: "open"}); err != nil {
		t.Fatal(err)
	}
	session, message, part := "runtime", "message", "part"
	zero, firstInput, changedInput := int64(0), int64(10), int64(11)
	cost := float64(0)
	observation := &telemetry.Observation{Key: telemetry.Key{SessionID: session, MessageID: message, PartID: part}, Ordinal: 1, Reason: "stop", Runtime: "opencode", RuntimeVersion: "1.18.5", Source: telemetry.SourceEventHook, Cost: telemetry.CostMeasurement{State: telemetry.MeasurementMeasured, Value: &cost}, Tokens: telemetry.Tokens{Input: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &firstInput}, Output: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, Reasoning: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheRead: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}, CacheWrite: telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &zero}}}
	input := RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch", EpochOrdinal: 1, Kind: "model_step", EventType: "message.part.updated", SourceSessionID: &session, MessageID: &message, PartID: &part, CapturedAt: "first", Telemetry: observation}
	if _, _, err := database.PersistRuntimeEvent(input); err != nil {
		t.Fatal(err)
	}
	changed := *observation
	changed.Tokens.Input.Value = &changedInput
	input.EpochOrdinal, input.CapturedAt, input.Telemetry = 2, "conflict", &changed
	event, inserted, err := database.PersistRuntimeEvent(input)
	if err != nil || !inserted || event.ModelStepClassification == nil || *event.ModelStepClassification != ModelStepConflictingDuplicate {
		t.Fatalf("conflict=%#v inserted=%t err=%v", event, inserted, err)
	}
	loaded, err := database.LoadRuntimeEventInterval(interval.ID)
	if err != nil || loaded.EventCount != 3 || loaded.ContradictionCount != 1 || loaded.CloseState != RuntimeOpen {
		t.Fatalf("interval=%#v err=%v", loaded, err)
	}
	var authoritative int64
	if err := database.db.QueryRow(`SELECT tokens_input FROM session_telemetry`).Scan(&authoritative); err != nil || authoritative != firstInput {
		t.Fatalf("authoritative input=%d err=%v", authoritative, err)
	}
	if err := database.SealRuntimeEventInterval(interval.ID, interval.SessionID, "closed", RuntimeComplete, ""); !errors.Is(err, ErrRuntimeIntervalInvalid) {
		t.Fatalf("contradiction sealed complete: %v", err)
	}
	if err := database.SealRuntimeEventInterval(interval.ID, interval.SessionID, "closed", RuntimeContradictory, "conflicting duplicate model step"); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeEventConcurrentWritersKeepOneGlobalOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ktx.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	interval, err := database.CreateBenchmarkEventInterval("attempt", "opencode", "1.18.5", "opencode-event-v1", "open")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.PersistRuntimeEvent(RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch", Kind: "collector_open", EventType: "collector.open", CapturedAt: "open"}); err != nil {
		t.Fatal(err)
	}
	database.Close()
	const writers = 8
	var wg sync.WaitGroup
	errorsCh := make(chan error, writers)
	for index := 1; index <= writers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			handle, err := Open(path)
			if err != nil {
				errorsCh <- err
				return
			}
			defer handle.Close()
			_, _, err = handle.PersistRuntimeEvent(RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: fmt.Sprintf("epoch-%d", index), Kind: "collector_open", EventType: "collector.open", CapturedAt: "time"})
			if err != nil {
				errorsCh <- err
			}
		}(index)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	loaded, err := database.LoadRuntimeEventInterval(interval.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EventCount != writers+1 {
		t.Fatalf("event_count=%d", loaded.EventCount)
	}
	for index, event := range loaded.Events {
		if event.Ordinal != uint64(index+1) {
			t.Fatalf("event[%d]=%d", index, event.Ordinal)
		}
	}
}

func TestRuntimeEventExpectedOrdinalRejectsCompetingWriterWithoutAGap(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	interval, err := database.CreateBenchmarkEventInterval("attempt", "opencode", "1.18.5", "opencode-event-v1", "open")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.PersistRuntimeEvent(RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", Kind: "collector_open", EventType: "collector.open", CapturedAt: "one"}); err != nil {
		t.Fatal(err)
	}
	after := uint64(1)
	first := RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", EpochOrdinal: 1, Kind: "other", EventType: "message.updated", CapturedAt: "two", AfterOrdinal: &after}
	if event, inserted, err := database.PersistRuntimeEvent(first); err != nil || !inserted || event.Ordinal != 2 {
		t.Fatalf("event=%#v inserted=%t err=%v", event, inserted, err)
	}
	stale := RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", EpochOrdinal: 2, Kind: "retry", EventType: "session.status", CapturedAt: "three", AfterOrdinal: &after}
	if _, _, err := database.PersistRuntimeEvent(stale); !errors.Is(err, ErrRuntimeIntervalInvalid) {
		t.Fatalf("stale writer err=%v", err)
	}
	loaded, err := database.LoadRuntimeEventInterval(interval.ID)
	if err != nil || loaded.EventCount != 2 || loaded.LastOrdinal == nil || *loaded.LastOrdinal != 2 {
		t.Fatalf("interval=%#v err=%v", loaded, err)
	}
}

func TestRuntimeEventEpochRestartMustNotOverlap(t *testing.T) {
	newInterval := func(t *testing.T) (*DB, *RuntimeEventInterval) {
		t.Helper()
		database, err := OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		interval, err := database.CreateBenchmarkEventInterval("attempt", "opencode", "1.18.5", "opencode-event-v1", "open")
		if err != nil {
			database.Close()
			t.Fatal(err)
		}
		return database, interval
	}
	persist := func(t *testing.T, database *DB, input RuntimeEventInput) {
		t.Helper()
		if _, _, err := database.PersistRuntimeEvent(input); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("sequential second epoch is a restart", func(t *testing.T) {
		database, interval := newInterval(t)
		defer database.Close()
		persist(t, database, RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", Kind: "collector_open", EventType: "collector.open", CapturedAt: "one"})
		persist(t, database, RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", EpochOrdinal: 1, Kind: "other", EventType: "message.updated", CapturedAt: "two"})
		persist(t, database, RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-b", Kind: "collector_open", EventType: "collector.open", CapturedAt: "three"})
		persist(t, database, RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-b", EpochOrdinal: 1, Kind: "retry", EventType: "session.status", CapturedAt: "four"})
		loaded, err := database.LoadRuntimeEventInterval(interval.ID)
		if err != nil || ValidateRuntimeEventInterval(loaded) != nil || loaded.CollectorEpochCount != 2 {
			t.Fatalf("sequential restart interval=%#v load_err=%v validation_err=%v", loaded, err, ValidateRuntimeEventInterval(loaded))
		}
	})

	t.Run("overlapping old epoch leaves interval incomplete", func(t *testing.T) {
		database, interval := newInterval(t)
		defer database.Close()
		persist(t, database, RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", Kind: "collector_open", EventType: "collector.open", CapturedAt: "one"})
		persist(t, database, RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-b", Kind: "collector_open", EventType: "collector.open", CapturedAt: "two"})
		persist(t, database, RuntimeEventInput{IntervalID: interval.ID, SessionID: interval.SessionID, EpochID: "epoch-a", EpochOrdinal: 1, Kind: "other", EventType: "message.updated", CapturedAt: "three"})
		loaded, err := database.LoadRuntimeEventInterval(interval.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateRuntimeEventInterval(loaded); !errors.Is(err, ErrRuntimeIntervalInvalid) {
			t.Fatalf("overlap validation err=%v", err)
		}
		if err := database.SealRuntimeEventInterval(interval.ID, interval.SessionID, "closed", RuntimeComplete, ""); !errors.Is(err, ErrRuntimeIntervalInvalid) {
			t.Fatalf("overlap sealed complete: %v", err)
		}
		if err := database.SealRuntimeEventInterval(interval.ID, interval.SessionID, "closed", RuntimeIncomplete, "collector epochs overlapped"); err != nil {
			t.Fatal(err)
		}
		loaded, err = database.LoadRuntimeEventInterval(interval.ID)
		if err != nil || loaded.CloseState != RuntimeIncomplete {
			t.Fatalf("incomplete interval=%#v err=%v", loaded, err)
		}
	})
}
