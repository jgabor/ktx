package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
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

func TestTelemetryRecordRequiresEveryNumericFieldAndPreservesZeros(t *testing.T) {
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
	for _, field := range []string{"ordinal", "cost", "tokens.input", "tokens.output", "tokens.reasoning", "tokens.cache_read", "tokens.cache_write"} {
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
				if _, err := telemetryLogic([]string{"record"}, FormatJSON, bytes.NewReader(encoded), d, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
					t.Fatalf("record accepted %s %s", form, field)
				}
			})
		}
	}
	rows, err := d.ListSessionTelemetry(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("omitted fields persisted %d observations", len(rows))
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
	if _, err := telemetryLogic([]string{"record"}, FormatJSON, strings.NewReader(malformed), d, &stdout, &stderr); err == nil {
		t.Fatal("malformed observation accepted")
	}
	if _, err := d.DB().Exec(`CREATE TRIGGER fail_optional_telemetry BEFORE INSERT ON session_telemetry BEGIN SELECT RAISE(ABORT, 'optional failure'); END`); err != nil {
		t.Fatal(err)
	}
	valid := `{"ktx_session_id":"` + sessionID + `","session_id":"ses_source","message_id":"msg_1","part_id":"prt_1","ordinal":1,"reason":"stop","cost":0,"tokens":{"input":1,"output":2,"reasoning":0,"cache_read":0,"cache_write":9}}`
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
