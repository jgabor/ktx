package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

const durableStep = `{"type":"step_finish","part":{"type":"step-finish","sessionID":"runtime","messageID":"message","id":"part","reason":"stop","cost":0.25,"tokens":{"input":1,"output":2,"reasoning":0,"cache":{"read":0,"write":0}}}}`

type recoveryReader struct {
	recover func()
	reader  io.Reader
}

func (r *recoveryReader) Read(p []byte) (int, error) {
	if r.recover != nil {
		r.recover()
		r.recover = nil
	}
	return r.reader.Read(p)
}

func TestWrapperTelemetryDurableRedelivery(t *testing.T) {
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.InsertSession(&dbpkg.Session{ID: "owner", Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB().Exec(`CREATE TRIGGER fail_telemetry BEFORE INSERT ON session_telemetry BEGIN SELECT RAISE(FAIL, 'controlled persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	var recoveredAt time.Time
	r := io.MultiReader(strings.NewReader(durableStep+"\n"), &recoveryReader{
		recover: func() {
			recoveredAt = time.Now().UTC()
			if _, err := d.DB().Exec(`DROP TRIGGER fail_telemetry`); err != nil {
				t.Fatal(err)
			}
		},
		reader: strings.NewReader(strings.Replace(durableStep, `"cost":0.25`, `"cost":9`, 1) + "\n" + durableStep + "\n" + durableStep + "\n"),
	})
	var stdout, stderr bytes.Buffer
	if _, err := teeAndParseRuntimeCollection(r, &stdout, d, "owner", &stderr, "opencode", ""); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListSessionTelemetry("owner", 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("durable rows=%+v err=%v warnings=%s", rows, err, &stderr)
	}
	row := rows[0]
	when, err := time.Parse(time.RFC3339Nano, row.CapturedAt)
	if err != nil || !when.Before(recoveredAt) || row.Ordinal != 1 || row.Cost.Value == nil || *row.Cost.Value != 0.25 {
		t.Fatalf("first observation not retained: %+v recovered=%s err=%v", row, recoveredAt, err)
	}
	if !strings.Contains(stderr.String(), "controlled persistence failure") {
		t.Fatalf("missing warning: %s", &stderr)
	}
}

func TestTelemetryDurableMachineACK(t *testing.T) {
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.InsertSession(&dbpkg.Session{ID: "owner", Agent: "opencode"}); err != nil {
		t.Fatal(err)
	}
	payload := `{"ktx_session_id":"owner","session_id":"runtime","message_id":"message","part_id":"part","ordinal":1,"reason":"stop","captured_at":"2026-09-01T00:00:00Z"}`
	if _, err := d.DB().Exec(`CREATE TRIGGER fail_telemetry BEFORE INSERT ON session_telemetry BEGIN SELECT RAISE(FAIL, 'controlled persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{true, false, false} {
		var stdout, stderr bytes.Buffer
		_, err := telemetryLogic([]string{"record", "--ack"}, FormatJSON, strings.NewReader(payload), d, &stdout, &stderr)
		if err != nil {
			t.Fatalf("optional command failed: %v", err)
		}
		var ack struct {
			OK          bool            `json:"ok"`
			Durable     bool            `json:"durable"`
			Observation json.RawMessage `json:"observation"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &ack); err != nil || ack.OK == fail || ack.Durable == fail {
			t.Fatalf("ack=%s err=%v fail=%t", &stdout, err, fail)
		}
		if fail {
			if _, err := d.DB().Exec(`DROP TRIGGER fail_telemetry`); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows, err := d.ListSessionTelemetry("owner", 100)
	if err != nil || len(rows) != 1 || rows[0].CapturedAt != "2026-09-01T00:00:00Z" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
