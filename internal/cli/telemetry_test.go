package cli

import (
	"bytes"
	"encoding/json"
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
	record := `{"ktx_session_id":"` + sessionID + `","session_id":"ses_source","message_id":"msg_1","part_id":"prt_1","ordinal":1,"reason":"stop","cost":0,"tokens":{"input":1,"output":2,"reasoning":0,"cache_read":0,"cache_write":9}}`
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
