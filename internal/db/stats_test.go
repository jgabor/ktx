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
		CREATE TABLE tool_calls (id TEXT PRIMARY KEY, tool TEXT, input TEXT, output TEXT, timestamp TEXT, is_error);
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
		if _, err := database.Exec(`INSERT INTO tool_calls (id, tool, input, output, timestamp, is_error) VALUES (?, 'bash', '', ?, ?, ?)`,
			value.id, strings.Repeat("x", index+1), from.Format(time.RFC3339Nano), value.outcome); err != nil {
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

	rows, err := d.StatsSummaryCacheRows(from, to)
	if err != nil {
		t.Fatalf("StatsSummaryCacheRows: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(rows), rows)
	}
	var measuredInput, unknownInput, malformedWrite, measuredZeroWrite int
	var measuredOutput, measuredReasoning, measuredCostZero int
	var inputTotal, readTotal, writeTotal int64
	var captures []string
	sessions := make(map[string]struct{})
	for _, row := range rows {
		captures = append(captures, row.CapturedAt.Format(time.RFC3339Nano))
		sessions[row.SessionID] = struct{}{}
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
		if row.Output.State == MeasurementMeasured && row.Output.Value != nil && *row.Output.Value == 2 {
			measuredOutput++
		}
		if row.Reasoning.State == MeasurementMeasured && row.Reasoning.Value != nil && *row.Reasoning.Value == 0 {
			measuredReasoning++
		}
		if row.Cost.State == MeasurementMeasured && row.Cost.Value != nil && *row.Cost.Value == 0 {
			measuredCostZero++
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
	if measuredInput != 4 || unknownInput != 1 || malformedWrite != 1 || measuredZeroWrite != 1 {
		t.Errorf("states measured_input=%d unknown_input=%d malformed_write=%d measured_zero_write=%d", measuredInput, unknownInput, malformedWrite, measuredZeroWrite)
	}
	if measuredOutput != 5 || measuredReasoning != 5 || measuredCostZero != 5 {
		t.Errorf("projected output=%d reasoning=%d cost_zero=%d, want 5/5/5", measuredOutput, measuredReasoning, measuredCostZero)
	}
	if inputTotal != 52 || readTotal != 102 || writeTotal != 62 {
		t.Errorf("measured totals input=%d read=%d write=%d, want 52/102/62", inputTotal, readTotal, writeTotal)
	}
	sort.Strings(captures)
	wantCaptures := []string{from.Format(time.RFC3339Nano), from.Format(time.RFC3339Nano), from.Format(time.RFC3339Nano), from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano)}
	if !reflect.DeepEqual(captures, wantCaptures) {
		t.Errorf("captures = %v, want %v", captures, wantCaptures)
	}
	if len(sessions) != 2 {
		t.Errorf("contributing sessions = %d, want 2", len(sessions))
	}
}

func TestStatsSummaryCacheRowsPreservesSourceIdentity(t *testing.T) {
	const runtime, runtimeVersion = "opencode", "1.18.5"
	d := openMemoryDB(t)
	from := time.Date(2026, 7, 21, 11, 30, 0, 0, time.UTC)
	to := from.Add(time.Minute)
	withIdentity, withoutIdentity := mustNewID(t), mustNewID(t)
	for _, sessionID := range []string{withIdentity, withoutIdentity} {
		if err := d.InsertSession(&Session{ID: sessionID, Agent: runtime}); err != nil {
			t.Fatal(err)
		}
	}
	provider, model := "openai", "gpt-5"
	persist := func(sessionID, source string, capturedAt string, provider, model *string) {
		t.Helper()
		observation := telemetryObservation(source, source, source, 1, 0, 0)
		observation.Runtime, observation.RuntimeVersion = runtime, runtimeVersion
		observation.Provider, observation.Model = provider, model
		if _, err := d.PersistSessionTelemetry(sessionID, capturedAt, observation); err != nil {
			t.Fatalf("PersistSessionTelemetry(%q): %v", source, err)
		}
	}
	persist(withIdentity, "source-with-identity", from.Format(time.RFC3339Nano), &provider, &model)
	persist(withoutIdentity, "source-without-identity", to.Format(time.RFC3339Nano), nil, nil)

	rows, err := d.StatsSummaryCacheRows(from, to)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]StatsSummaryCacheRow, len(rows))
	for _, row := range rows {
		got[row.SessionID] = row
	}
	if row := got[withIdentity]; row.Provider == nil || *row.Provider != provider || row.Model == nil || *row.Model != model {
		t.Fatalf("source identity = %#v/%#v", row.Provider, row.Model)
	}
	if row := got[withoutIdentity]; row.Provider != nil || row.Model != nil {
		t.Fatalf("missing source identity = %#v/%#v", row.Provider, row.Model)
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
