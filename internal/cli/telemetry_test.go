package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/telemetry"
	telemetryopencode "github.com/jgabor/ktx/internal/telemetry/opencode"
)

func TestTelemetryRecordAndBoundedJSONInspection(t *testing.T) {
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID, err := dbpkg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&dbpkg.Session{ID: sessionID, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	record := `{"ktx_session_id":"` + sessionID + `","session_id":"ses_source","message_id":"msg_1","part_id":"prt_1","ordinal":1,"reason":"stop","cost":0,"tokens":{"input":1,"output":2,"reasoning":0,"cache_read":0,"cache_write":9},"runtime":"untrusted","runtime_version":"0"}`
	var stdout, stderr bytes.Buffer
	if _, err := telemetryLogic([]string{"record"}, FormatJSON, strings.NewReader(record), d, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("record output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	second := `{"ktx_session_id":"` + sessionID + `","session_id":"ses_source","message_id":"msg_2","part_id":"prt_2","ordinal":2,"reason":"stop","cost":0,"tokens":{"input":1,"output":2,"reasoning":0,"cache_read":4,"cache_write":0}}`
	if _, err := telemetryLogic([]string{"record"}, FormatJSON, strings.NewReader(second), d, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := telemetryLogic([]string{"list", "--session", sessionID, "--limit", "1"}, FormatJSON, strings.NewReader(""), d, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var result struct {
		SessionID    string                   `json:"session_id"`
		Limit        int                      `json:"limit"`
		Observations []dbpkg.SessionTelemetry `json:"observations"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID != sessionID || result.Limit != 1 || len(result.Observations) != 1 {
		t.Fatalf("inspection = %#v", result)
	}
	if got := result.Observations[0].CacheRead; got.State != dbpkg.MeasurementMeasured || got.Value == nil || *got.Value != 0 {
		t.Fatalf("cache_read = %#v, want measured zero", got)
	}
	if result.Observations[0].Provider != nil || result.Observations[0].Model != nil {
		t.Fatalf("unavailable identity was invented: %#v", result.Observations[0])
	}
	if result.Observations[0].Runtime != "opencode" || result.Observations[0].RuntimeVersion != "1.18.4" {
		t.Fatalf("runtime identity = %q/%q", result.Observations[0].Runtime, result.Observations[0].RuntimeVersion)
	}
}

func TestTelemetryInspectionOrdersRuntimeNamespacesAndShowsProvenance(t *testing.T) {
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID, err := dbpkg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&dbpkg.Session{ID: sessionID, Agent: "test"}); err != nil {
		t.Fatal(err)
	}

	for _, provenance := range []struct {
		runtime string
		version string
	}{
		{runtime: "zeta-runtime", version: "2.0.0"},
		{runtime: "alpha-runtime", version: "1.0.0"},
	} {
		observation := &telemetry.Observation{
			Key:     telemetry.Key{SessionID: "source", MessageID: "message", PartID: "part"},
			Ordinal: 7,
			Reason:  "stop",
			Cost:    telemetry.DecodeCostMeasurement(json.RawMessage("0"), telemetry.MeasurementMeasured),
			Tokens: telemetry.Tokens{
				Input: telemetry.DecodeIntegerMeasurement(json.RawMessage("0"), telemetry.MeasurementMeasured), Output: telemetry.DecodeIntegerMeasurement(json.RawMessage("0"), telemetry.MeasurementMeasured),
				Reasoning: telemetry.DecodeIntegerMeasurement(json.RawMessage("0"), telemetry.MeasurementMeasured), CacheRead: telemetry.DecodeIntegerMeasurement(json.RawMessage("0"), telemetry.MeasurementMeasured), CacheWrite: telemetry.DecodeIntegerMeasurement(json.RawMessage("0"), telemetry.MeasurementMeasured),
			},
			Runtime:        provenance.runtime,
			RuntimeVersion: provenance.version,
			Source:         telemetry.SourceEventHook,
		}
		if inserted, err := d.PersistSessionTelemetry(sessionID, "2026-07-24T10:00:00Z", observation); err != nil || !inserted {
			t.Fatalf("persist %s = %t, %v", provenance.runtime, inserted, err)
		}
	}

	var stdout bytes.Buffer
	if _, err := telemetryLogic([]string{"list", "--session", sessionID, "--limit", "2"}, FormatJSON, strings.NewReader(""), d, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var result telemetryListResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 2 || result.Observations[0].Runtime != "alpha-runtime" || result.Observations[0].RuntimeVersion != "1.0.0" ||
		result.Observations[1].Runtime != "zeta-runtime" || result.Observations[1].RuntimeVersion != "2.0.0" {
		t.Fatalf("runtime-qualified order/provenance = %#v", result.Observations)
	}

	stdout.Reset()
	if _, err := telemetryLogic([]string{"list", "--session", sessionID, "--limit", "2"}, FormatText, strings.NewReader(""), d, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "RUNTIME") || !strings.Contains(lines[0], "RUNTIME VERSION") {
		t.Fatalf("text header = %q", stdout.String())
	}
	if got := strings.Join(strings.Fields(lines[1]), "|"); got != "7|source|message|part|0|(measured)|0|(measured)|-|-|alpha-runtime|1.0.0|event_hook" {
		t.Fatalf("first text row fields = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[2]), "|"); got != "7|source|message|part|0|(measured)|0|(measured)|-|-|zeta-runtime|2.0.0|event_hook" {
		t.Fatalf("text runtime-qualified order/provenance = %q", stdout.String())
	}
}

func TestTelemetryRecordDistinguishesUnavailableFieldsAndPreservesZeros(t *testing.T) {
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID, err := dbpkg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&dbpkg.Session{ID: sessionID, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	complete := map[string]any{
		"ktx_session_id": sessionID,
		"session_id":     "ses_source",
		"message_id":     "msg_1",
		"part_id":        "prt_1",
		"ordinal":        1,
		"reason":         "stop",
		"cost":           0,
		"tokens": map[string]any{
			"input": 0, "output": 0, "reasoning": 0, "cache_read": 0, "cache_write": 0,
		},
	}
	for _, field := range []string{"cost", "tokens.input", "tokens.output", "tokens.reasoning", "tokens.cache_read", "tokens.cache_write"} {
		for _, form := range []string{"missing", "null"} {
			t.Run(form+"_"+strings.ReplaceAll(field, ".", "_"), func(t *testing.T) {
				payload := cloneTelemetryInput(t, complete)
				name := field
				target := payload
				if strings.HasPrefix(field, "tokens.") {
					name = strings.TrimPrefix(field, "tokens.")
					target = payload["tokens"].(map[string]any)
				}
				if form == "missing" {
					delete(target, name)
				} else {
					target[name] = nil
				}
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				var input telemetryRecordInput
				if err := json.Unmarshal(encoded, &input); err != nil {
					t.Fatal(err)
				}
				observation, err := input.observation()
				if err != nil {
					t.Fatalf("record input rejected %s %s: %v", form, field, err)
				}
				if state := telemetryInputState(observation, field); state != telemetry.MeasurementUnknown {
					t.Fatalf("%s %s state = %q, want unknown", form, field, state)
				}
			})
		}
	}
	rows, err := d.ListSessionTelemetry(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("input-only checks persisted %d observations", len(rows))
	}
	encoded, err := json.Marshal(complete)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := telemetryLogic([]string{"record"}, FormatJSON, bytes.NewReader(encoded), d, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	rows, err = d.ListSessionTelemetry(sessionID, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("explicit-zero rows = %d, %v", len(rows), err)
	}
	row := rows[0]
	for name, measurement := range map[string]dbpkg.IntegerMeasurement{
		"input": row.UncachedInput, "output": row.Output, "reasoning": row.Reasoning,
		"cache_read": row.CacheRead, "cache_write": row.CacheWrite,
	} {
		if measurement.State != dbpkg.MeasurementMeasured || measurement.Value == nil || *measurement.Value != 0 {
			t.Errorf("%s = %#v, want measured zero", name, measurement)
		}
	}
	if row.Cost.State != dbpkg.MeasurementMeasured || row.Cost.Value == nil || *row.Cost.Value != 0 {
		t.Errorf("cost = %#v, want measured zero", row.Cost)
	}
}

func TestTelemetryExplicitStatesPreserveCrossIngressFirstEvidence(t *testing.T) {
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID, err := dbpkg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&dbpkg.Session{ID: sessionID, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	hookPayload := `{"ktx_session_id":"` + sessionID + `","session_id":"ses_1","message_id":"msg_1","part_id":"prt_1","ordinal":1,"reason":"stop","cost":{"state":"measured","value":0},"tokens":{"input":{"state":"malformed","value":null},"output":{"state":"malformed","value":null},"reasoning":{"state":"malformed","value":null},"cache_read":{"state":"malformed","value":null},"cache_write":{"state":"malformed","value":null}}}`
	if _, err := telemetryLogic([]string{"record"}, FormatJSON, strings.NewReader(hookPayload), d, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("record explicit hook states: %v", err)
	}
	collector := telemetryopencode.NewCollector()
	wrapper, err := collector.AddStream([]byte(`{"type":"step_finish","part":{"id":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"step-finish","reason":"stop","cost":0,"tokens":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := d.PersistSessionTelemetry(sessionID, "later", wrapper); err != nil || inserted {
		t.Fatalf("equivalent wrapper duplicate = %t, %v", inserted, err)
	}
	weaker := *wrapper
	weaker.Tokens.Input = telemetry.IntegerMeasurement{State: telemetry.MeasurementUnknown}
	if inserted, err := d.PersistSessionTelemetry(sessionID, "later-still", &weaker); !errors.Is(err, dbpkg.ErrSessionTelemetryConflict) || inserted {
		t.Fatalf("weaker conflicting duplicate = %t, %v", inserted, err)
	}
	rows, err := d.ListSessionTelemetry(sessionID, 10)
	if err != nil || len(rows) != 1 || rows[0].Source != telemetry.SourceEventHook || rows[0].UncachedInput.State != dbpkg.MeasurementMalformed {
		t.Fatalf("first hook evidence changed: %#v, %v", rows, err)
	}
}

func telemetryInputState(observation *telemetry.Observation, field string) telemetry.MeasurementState {
	switch field {
	case "cost":
		return observation.Cost.State
	case "tokens.input":
		return observation.Tokens.Input.State
	case "tokens.output":
		return observation.Tokens.Output.State
	case "tokens.reasoning":
		return observation.Tokens.Reasoning.State
	case "tokens.cache_read":
		return observation.Tokens.CacheRead.State
	default:
		return observation.Tokens.CacheWrite.State
	}
}

func TestTelemetryCommandDatabaseFailuresFailOpenOnlyForRecord(t *testing.T) {
	if dispatchRegistry["telemetry"].opts.NeedsDB {
		t.Fatal("telemetry database open still runs in the dispatcher")
	}
	valid := `{"session_id":"ses_source","message_id":"msg_1","part_id":"prt_1","ordinal":1,"reason":"stop","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache_read":0,"cache_write":0}}`
	for _, failure := range []struct {
		name    string
		prepare func(t *testing.T) string
	}{
		{
			name: "open",
			prepare: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name: "migration",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "newer.db")
				d, err := dbpkg.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := d.DB().Exec(`PRAGMA user_version = 999`); err != nil {
					t.Fatal(err)
				}
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "malformed-v4-provenance",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "malformed-v4.db")
				d, err := dbpkg.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := d.InsertSession(&dbpkg.Session{ID: "abcdefghijklmnop", Agent: "opencode"}); err != nil {
					t.Fatal(err)
				}
				if _, err := d.DB().Exec(`DROP TABLE session_telemetry;
					CREATE TABLE session_telemetry (
						session_id TEXT NOT NULL REFERENCES sessions(id), source_session_id TEXT NOT NULL,
						message_id TEXT NOT NULL, part_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
						reason TEXT NOT NULL, cost REAL NOT NULL, tokens_input INTEGER NOT NULL,
						tokens_output INTEGER NOT NULL, tokens_reasoning INTEGER NOT NULL,
						cache_read INTEGER NOT NULL, cache_write INTEGER NOT NULL, provider TEXT, model TEXT,
						runtime TEXT NOT NULL, runtime_version TEXT NOT NULL, captured_at TEXT NOT NULL,
						PRIMARY KEY (source_session_id, message_id, part_id));
					INSERT INTO session_telemetry VALUES
						('abcdefghijklmnop','source','message','part',1,'stop',0,1,2,3,4,5,NULL,NULL,' ','1','captured');
					PRAGMA user_version = 4`); err != nil {
					t.Fatal(err)
				}
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	} {
		t.Run(failure.name+"/record", func(t *testing.T) {
			t.Setenv("KTX_DB", failure.prepare(t))
			var stdout, stderr bytes.Buffer
			_, err := telemetryCommandFunc(&DispatchContext{
				Args: []string{"record"}, Format: FormatJSON, Stdin: strings.NewReader(valid), Stdout: &stdout, Stderr: &stderr,
			})
			if err != nil {
				t.Fatalf("record database failure escaped: %v", err)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "telemetry not recorded") {
				t.Fatalf("record output stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
		t.Run(failure.name+"/list", func(t *testing.T) {
			t.Setenv("KTX_DB", failure.prepare(t))
			var stdout, stderr bytes.Buffer
			if code := runWith([]string{"telemetry", "list", "--session", "abcdefghijklmnop"}, &stdout, &stderr); code != ExitInternal {
				t.Fatalf("list exit = %d, want %d; stdout=%q stderr=%q", code, ExitInternal, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("list failure output stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func cloneTelemetryInput(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestTelemetryRejectsMalformedAndFailsOpenOnPersistenceError(t *testing.T) {
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID, err := dbpkg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSession(&dbpkg.Session{ID: sessionID, Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	invalid := `{"ktx_session_id":"` + sessionID + `","session_id":" ","message_id":"msg_1","part_id":"prt_1","ordinal":1,"reason":"stop","cost":0,"tokens":{"input":1,"output":2,"reasoning":0,"cache_read":0,"cache_write":9}}`
	if _, err := telemetryLogic([]string{"record"}, FormatJSON, strings.NewReader(invalid), d, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "invalid OpenCode telemetry event: session_id must be a non-blank string") {
		t.Fatalf("normalized validation error = %v", err)
	}
	malformed := `{"ktx_session_id":"` + sessionID + `","session_id":"ses_source","message_id":"msg_1","part_id":"prt_1","ordinal":1,"reason":"stop","cost":0,"tokens":{"input":1,"output":2,"reasoning":0,"cache_read":"bad","cache_write":9}}`
	if _, err := telemetryLogic([]string{"record"}, FormatJSON, strings.NewReader(malformed), d, &stdout, &stderr); err != nil {
		t.Fatalf("identifiable observation with malformed field rejected: %v", err)
	}
	rows, err := d.ListSessionTelemetry(sessionID, 10)
	if err != nil || len(rows) != 1 || rows[0].CacheRead.State != dbpkg.MeasurementMalformed || rows[0].CacheRead.Value != nil {
		t.Fatalf("malformed field persistence = %#v, %v", rows, err)
	}
	if _, err := d.DB().Exec(`CREATE TRIGGER fail_optional_telemetry BEFORE INSERT ON session_telemetry BEGIN SELECT RAISE(ABORT, 'optional failure'); END`); err != nil {
		t.Fatal(err)
	}
	valid := `{"ktx_session_id":"` + sessionID + `","session_id":"ses_source","message_id":"msg_2","part_id":"prt_2","ordinal":2,"reason":"stop","cost":0,"tokens":{"input":1,"output":2,"reasoning":0,"cache_read":0,"cache_write":9}}`
	stdout.Reset()
	stderr.Reset()
	if _, err := telemetryLogic([]string{"record"}, FormatJSON, strings.NewReader(valid), d, &stdout, &stderr); err != nil {
		t.Fatalf("optional persistence failure escaped: %v", err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "telemetry not recorded") {
		t.Fatalf("fail-open output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	session, err := d.GetSession(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.ToolCallCount != 0 {
		t.Fatalf("session evidence changed: %#v", session)
	}
}
