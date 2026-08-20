package db

import (
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/telemetry"
)

// TestStatsRows covers the raw row query: uncompressed calls
// have delivered equal to raw, compressed calls have delivered from
// the compressed output, the first compression by timestamp and ID is used
// when multiple exist, and an empty database returns an empty slice.
func TestStatsRows(t *testing.T) {
	t.Run("uncompressed calls have delivered equal to raw", func(t *testing.T) {
		d := openMemoryDB(t)
		seedSessionAndCalls(t, d, []callFixture{
			{id: "aaaaaaaaaaaaaaaa", tool: "bash", input: `{"command": "ls -la"}`, output: "drwxr-xr-x  2 user user 4096 Jan 1 00:00 .\n"},
			{id: "bbbbbbbbbbbbbbbb", tool: "bash", input: `{"command": "ls -la"}`, output: "drwxr-xr-x  2 user user 4096 Jan 1 00:00 ..\n"},
		})

		rows, err := d.StatsRows()
		if err != nil {
			t.Fatalf("StatsRows: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("got %d rows, want 2", len(rows))
		}
		for _, r := range rows {
			if r.RawBytes != r.DeliveredBytes {
				t.Errorf("raw=%d != delivered=%d for tool %q", r.RawBytes, r.DeliveredBytes, r.Tool)
			}
		}
	})

	t.Run("compressed calls have delivered from compressed output", func(t *testing.T) {
		d := openMemoryDB(t)
		sourceID := seedSessionAndCalls(t, d, []callFixture{
			{id: "cccccccccccccccc", tool: "bash", input: `{"command": "cat large.txt"}`, output: mustLongString(800)},
		})[0]

		// Insert a compressed row with smaller output.
		compressedOutput := "short\n"
		seedCompressed(t, d, sourceID, compressedOutput, "2026-07-12T00:00:01Z")

		rows, err := d.StatsRows()
		if err != nil {
			t.Fatalf("StatsRows: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		if rows[0].DeliveredBytes != len(compressedOutput) {
			t.Errorf("delivered=%d, want %d (compressed output length)", rows[0].DeliveredBytes, len(compressedOutput))
		}
		if rows[0].RawBytes != 800 {
			t.Errorf("raw=%d, want 800", rows[0].RawBytes)
		}
	})

	t.Run("first compression remains authoritative when multiple exist", func(t *testing.T) {
		d := openMemoryDB(t)
		sourceID := seedSessionAndCalls(t, d, []callFixture{
			{id: "dddddddddddddddd", tool: "bash", input: `{"command": "cat file"}`, output: mustLongString(800)},
		})[0]

		firstTimestamp := "2026-07-12T00:00:01Z"
		seedCompressedWithID(t, d, "bbbbbbbbbbbbbbbb", sourceID, "same-time larger ID\n", firstTimestamp)
		seedCompressedWithID(t, d, "aaaaaaaaaaaaaaaa", sourceID, "first output\n", firstTimestamp)
		seedCompressedWithID(t, d, "cccccccccccccccc", sourceID, "later\n", "2026-07-12T00:00:02Z")

		rows, err := d.StatsRows()
		if err != nil {
			t.Fatalf("StatsRows: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		want := len("first output\n")
		if rows[0].DeliveredBytes != want {
			t.Errorf("delivered=%d, want %d (first compression)", rows[0].DeliveredBytes, want)
		}
	})

	t.Run("empty database returns empty slice", func(t *testing.T) {
		d := openMemoryDB(t)
		rows, err := d.StatsRows()
		if err != nil {
			t.Fatalf("StatsRows: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("got %d rows, want 0", len(rows))
		}
	})
}

func TestStatsSummaryRows(t *testing.T) {
	d := openMemoryDB(t)
	from := time.Date(2026, 7, 21, 11, 30, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	ids := seedSessionAndCalls(t, d, []callFixture{
		{id: "fromfromfromfrom", output: "12345", timestamp: from.Format(time.RFC3339Nano)},
		{id: "totototototototo", output: "界界界界界", timestamp: to.Format(time.RFC3339Nano)},
		{id: "offsetoffsetoffx", output: "offset", timestamp: "2026-07-21T13:30:00+02:00"},
		{id: "emptyemptyemptyx", output: "", timestamp: from.Add(time.Minute).Format(time.RFC3339Nano)},
		{id: "compressedsource", output: "123456789", timestamp: from.Add(2 * time.Minute).Format(time.RFC3339Nano)},
		{id: "emptycompressedx", output: "1234", timestamp: from.Add(3 * time.Minute).Format(time.RFC3339Nano)},
		{id: "nullartifactsrcx", output: "abc", timestamp: from.Add(4 * time.Minute).Format(time.RFC3339Nano)},
		{id: "beforebeforebefx", output: "outside", timestamp: from.Add(-time.Nanosecond).Format(time.RFC3339Nano)},
		{id: "afterafterafterx", output: "outside", timestamp: to.Add(time.Nanosecond).Format(time.RFC3339Nano)},
		{id: "invalidinvalidxx", output: "outside", timestamp: "not-a-timestamp"},
	})

	// The first timestamp wins. At an equal timestamp, the smallest ID wins.
	firstTimestamp := from.Add(4 * time.Minute).Format(time.RFC3339Nano)
	seedCompressedWithID(t, d, "oldoldoldoldoldx", ids[4], "12345678", firstTimestamp)
	seedCompressedWithID(t, d, "zzzzzzzzzzzzzzzy", ids[4], "12", firstTimestamp)
	latestTimestamp := from.Add(5*time.Minute + time.Nanosecond).Format(time.RFC3339Nano)
	seedCompressedWithID(t, d, "aaaaaaaaaaaaaaax", ids[4], "1", latestTimestamp)
	seedCompressedWithID(t, d, "zzzzzzzzzzzzzzzx", ids[4], "1234", latestTimestamp)
	seedCompressedWithID(t, d, "emptyartifactxxx", ids[5], "", from.Add(5*time.Minute).Format(time.RFC3339Nano))
	if _, err := d.db.Exec(`INSERT INTO compressed (id, source_id, output, timestamp) VALUES (?, ?, NULL, ?)`,
		"nullartifactxxxx", ids[6], from.Add(5*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert NULL artifact output: %v", err)
	}

	if _, err := d.db.Exec(`INSERT INTO tool_calls (id, session_id, output, timestamp)
		VALUES (?, (SELECT session_id FROM tool_calls LIMIT 1), NULL, ?)`,
		"nullnullnullnull", from.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert NULL output: %v", err)
	}

	rows, err := d.StatsSummaryRows(from, to)
	if err != nil {
		t.Fatalf("StatsSummaryRows: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("got %d rows, want 7: %+v", len(rows), rows)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].RawChars < rows[j].RawChars })
	var rawChars, deliveredChars, compressed int
	for _, row := range rows {
		rawChars += row.RawChars
		deliveredChars += row.DeliveredChars
		if row.HasCompressedArtifact {
			compressed++
		}
	}
	if rawChars != 32 || deliveredChars != 27 {
		t.Errorf("character totals raw=%d delivered=%d, want 32 and 27", rawChars, deliveredChars)
	}
	if compressed != 3 {
		t.Errorf("compressed artifact count=%d, want 3", compressed)
	}
	if rows[0].RawChars != 0 {
		t.Errorf("empty non-NULL output missing: rows=%+v", rows)
	}
	for _, row := range rows {
		if row.RawChars == 9 && row.DeliveredChars != 8 {
			t.Errorf("first artifact should remain authoritative: %+v", row)
		}
		if row.RawChars == 3 && (!row.HasCompressedArtifact || row.DeliveredChars != row.RawChars) {
			t.Errorf("NULL artifact output should use raw output: %+v", row)
		}
	}
}

func TestStatsSummaryRowsClassifiesCanonicalOutcome(t *testing.T) {
	d := openMemoryDB(t)
	from := time.Date(2026, 7, 21, 11, 30, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	success, failure := false, true
	ids := seedSessionAndCalls(t, d, []callFixture{
		{id: "successsuccessxx", output: "s", timestamp: from.Format(time.RFC3339Nano), isError: &success},
		{id: "failurefailurexx", output: "ff", timestamp: from.Format(time.RFC3339Nano), isError: &failure},
		{id: "unknownunknownxx", output: "uuu", timestamp: from.Format(time.RFC3339Nano)},
		{id: "malformedstatexx", output: "mmmm", timestamp: from.Format(time.RFC3339Nano)},
	})
	if _, err := d.db.Exec(`UPDATE tool_calls SET is_error = 7, exit_code = 0 WHERE id = ?`, ids[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE tool_calls SET exit_code = 1 WHERE id = ?`, ids[2]); err != nil {
		t.Fatal(err)
	}

	rows, err := d.StatsSummaryRows(from, to)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[int]*bool, len(rows))
	for _, row := range rows {
		got[row.RawChars] = row.IsError
	}
	if got[1] == nil || *got[1] || got[2] == nil || !*got[2] {
		t.Errorf("boolean outcomes = %#v", got)
	}
	if got[3] != nil || got[4] != nil {
		t.Errorf("unknown outcomes = %#v; NULL, contradictory exit code, and malformed integer must remain unknown", got)
	}
}

func TestStatsSummaryRowsNormalizesOutcomeStorageClasses(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`
		CREATE TABLE sessions (id TEXT PRIMARY KEY, agent_attribution_authority TEXT NOT NULL DEFAULT 'unknown');
		CREATE TABLE tool_calls (id TEXT PRIMARY KEY, session_id TEXT, tool TEXT, input TEXT, output TEXT, timestamp TEXT, timestamp_unix_nano INTEGER, is_error, latency_ms INTEGER, latency_state TEXT NOT NULL DEFAULT 'unknown', latency_authority TEXT NOT NULL DEFAULT 'unknown');
		CREATE TABLE compression_dispositions (source_id TEXT PRIMARY KEY, profile TEXT, route TEXT, disposition TEXT);
		CREATE TABLE compressed (id TEXT PRIMARY KEY, source_id TEXT, output TEXT, timestamp TEXT);
	`); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 21, 11, 30, 0, 0, time.UTC)
	success, failure := false, true
	values := []struct {
		id      string
		outcome any
		known   *bool
	}{
		{id: "integerzeroaaaa", outcome: int64(0), known: &success},
		{id: "integeroneaaaaa", outcome: int64(1), known: &failure},
		{id: "integerbadaaaaa", outcome: int64(7)},
		{id: "realbadbadbaaaa", outcome: float64(0.5)},
		{id: "textbadbadbaaaa", outcome: "malformed"},
		{id: "textzeroaaaaaaaa", outcome: "0"},
		{id: "textoneaaaaaaaaa", outcome: "1"},
		{id: "blobbadbadbaaaa", outcome: []byte{0}},
		{id: "nullbadbadbaaaa", outcome: nil},
	}
	for index, value := range values {
		if _, err := database.Exec(`INSERT INTO tool_calls (id, tool, input, output, timestamp, timestamp_unix_nano, is_error) VALUES (?, 'bash', '', ?, ?, ?, ?)`,
			value.id, strings.Repeat("x", index+1), from.Format(time.RFC3339Nano), from.UnixNano(), value.outcome); err != nil {
			t.Fatalf("insert %s: %v", value.id, err)
		}
	}

	rows, err := statsSummaryRowsFor(database, from, from, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(values) {
		t.Fatalf("rows = %d, want %d", len(rows), len(values))
	}
	want := make(map[int]*bool, len(values))
	for index, value := range values {
		want[index+1] = value.known
	}
	for _, row := range rows {
		if expected := want[row.RawChars]; (row.IsError == nil) != (expected == nil) || row.IsError != nil && *row.IsError != *expected {
			t.Errorf("raw chars %d is_error = %v, want %v", row.RawChars, row.IsError, expected)
		}
	}
}

func TestStatsSummaryCacheRows(t *testing.T) {
	const runtime, runtimeVersion = "opencode", "1.18.5"
	d := openMemoryDB(t)
	from := time.Date(2026, 7, 21, 11, 30, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	sessionID := mustNewID(t)
	otherSessionID := mustNewID(t)
	for _, id := range []string{sessionID, otherSessionID} {
		if err := d.InsertSession(&Session{ID: id, Agent: runtime}); err != nil {
			t.Fatal(err)
		}
	}

	persist := func(name, sessionID, capturedAt, runtime, runtimeVersion string, input, read, write telemetry.IntegerMeasurement) {
		t.Helper()
		observation := telemetryObservation("source_"+name, "message_"+name, "part_"+name, 1, 0, 0)
		observation.Runtime = runtime
		observation.RuntimeVersion = runtimeVersion
		observation.Tokens.Input = input
		observation.Tokens.CacheRead = read
		observation.Tokens.CacheWrite = write
		if _, err := d.PersistSessionTelemetry(sessionID, capturedAt, observation); err != nil {
			t.Fatalf("PersistSessionTelemetry(%s): %v", name, err)
		}
	}
	measured := func(value int64) telemetry.IntegerMeasurement {
		return telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &value}
	}

	persist("from", sessionID, from.Format(time.RFC3339Nano), runtime, runtimeVersion, measured(10), measured(20), measured(0))
	persist("to", otherSessionID, to.Format(time.RFC3339Nano), runtime, runtimeVersion,
		telemetry.IntegerMeasurement{State: telemetry.MeasurementUnknown}, measured(30), telemetry.IntegerMeasurement{State: telemetry.MeasurementMalformed})
	persist("offset", otherSessionID, "2026-07-21T13:30:00+02:00", runtime, runtimeVersion, measured(40), measured(50), measured(60))
	persist("before", sessionID, from.Add(-time.Nanosecond).Format(time.RFC3339Nano), runtime, runtimeVersion, measured(1), measured(1), measured(1))
	persist("after", sessionID, to.Add(time.Nanosecond).Format(time.RFC3339Nano), runtime, runtimeVersion, measured(1), measured(1), measured(1))
	persist("invalid", sessionID, "not-a-timestamp", runtime, runtimeVersion, measured(1), measured(1), measured(1))
	persist("runtime", sessionID, from.Format(time.RFC3339Nano), "other-runtime", runtimeVersion, measured(1), measured(1), measured(1))
	persist("version", sessionID, from.Format(time.RFC3339Nano), runtime, "1.18.4", measured(1), measured(1), measured(1))

	rows, err := d.StatsSummaryCacheRows(from, to, runtime, runtimeVersion)
	if err != nil {
		t.Fatalf("StatsSummaryCacheRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	var measuredInput, unknownInput, malformedWrite, measuredZeroWrite int
	var inputTotal, readTotal, writeTotal int64
	for _, row := range rows {
		switch row.UncachedInput.State {
		case MeasurementMeasured:
			measuredInput++
			inputTotal += *row.UncachedInput.Value
		case MeasurementUnknown:
			unknownInput++
		}
		if row.CacheRead.State == MeasurementMeasured {
			readTotal += *row.CacheRead.Value
		}
		switch row.CacheWrite.State {
		case MeasurementMeasured:
			writeTotal += *row.CacheWrite.Value
			if *row.CacheWrite.Value == 0 {
				measuredZeroWrite++
			}
		case MeasurementMalformed:
			malformedWrite++
		}
	}
	if measuredInput != 2 || unknownInput != 1 || malformedWrite != 1 || measuredZeroWrite != 1 {
		t.Errorf("states measured_input=%d unknown_input=%d malformed_write=%d measured_zero_write=%d", measuredInput, unknownInput, malformedWrite, measuredZeroWrite)
	}
	if inputTotal != 50 || readTotal != 100 || writeTotal != 60 {
		t.Errorf("measured totals input=%d read=%d write=%d, want 50/100/60", inputTotal, readTotal, writeTotal)
	}
}

func TestForEachStatsSummaryBatch(t *testing.T) {
	for _, test := range []struct {
		name        string
		sources     int
		wantBatches []int
	}{
		{name: "zero", sources: 0},
		{name: "one", sources: 1, wantBatches: []int{1}},
		{name: "one batch", sources: statsSummaryBatchSize, wantBatches: []int{statsSummaryBatchSize}},
		{name: "two batches", sources: statsSummaryBatchSize + 1, wantBatches: []int{statsSummaryBatchSize, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ids := make([]string, test.sources)
			for i := range ids {
				ids[i] = fmt.Sprintf("batch%011d", i)
			}

			var batchSizes []int
			err := forEachStatsSummaryBatch(ids, func(batch []string) error {
				batchSizes = append(batchSizes, len(batch))
				return nil
			})
			if err != nil {
				t.Fatalf("forEachStatsSummaryBatch: %v", err)
			}
			if !reflect.DeepEqual(batchSizes, test.wantBatches) {
				t.Errorf("batch sizes = %v, want %v", batchSizes, test.wantBatches)
			}
			for _, size := range batchSizes {
				if size > statsSummaryBatchSize {
					t.Errorf("batch size = %d, want at most %d", size, statsSummaryBatchSize)
				}
			}
		})
	}
}

// callFixture is a minimal tool_call fixture for seeding.
type callFixture struct {
	id        string
	tool      string
	input     string
	output    string
	timestamp string
	isError   *bool
}

// seedSessionAndCalls inserts a session and the given tool_calls.
func seedSessionAndCalls(t *testing.T, d *DB, calls []callFixture) []string {
	t.Helper()
	sid := mustNewID(t)
	if err := d.InsertSession(&Session{
		ID:         sid,
		Agent:      "test-agent",
		StartedAt:  "2026-07-12T00:00:00Z",
		LastSeenAt: "2026-07-12T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	var ids []string
	for _, c := range calls {
		timestamp := c.timestamp
		if timestamp == "" {
			timestamp = "2026-07-12T00:00:01Z"
		}
		if err := d.PersistToolCall(&ToolCall{
			ID:        c.id,
			SessionID: sid,
			Tool:      c.tool,
			Input:     c.input,
			Output:    c.output,
			Timestamp: timestamp,
			IsError:   c.isError,
		}); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}
		ids = append(ids, c.id)
	}
	return ids
}

func seedCompressedWithID(t *testing.T, d *DB, id, sourceID, output, timestamp string) {
	t.Helper()
	if err := d.InsertCompressed(&Compressed{
		ID: id, SourceID: sourceID, Output: output,
		ContentHash: ContentHash(output), Pipeline: "[]", Timestamp: timestamp,
	}); err != nil {
		t.Fatalf("InsertCompressed: %v", err)
	}
}

// seedCompressed inserts one compressed row.
func seedCompressed(t *testing.T, d *DB, sourceID, output, timestamp string) {
	t.Helper()
	cid := mustNewID(t)
	if err := d.InsertCompressed(&Compressed{
		ID:          cid,
		SourceID:    sourceID,
		Output:      output,
		ContentHash: ContentHash(output),
		Pipeline:    "[]",
		Timestamp:   timestamp,
	}); err != nil {
		t.Fatalf("InsertCompressed: %v", err)
	}
}

// openMemoryDB opens an in-memory database for testing.
func openMemoryDB(t *testing.T) *DB {
	t.Helper()
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// mustLongString returns a string of approximately n bytes.
func mustLongString(n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = 'x'
	}
	s[n-1] = '\n'
	return string(s)
}
