package db

import (
	"sort"
	"testing"
	"time"
)

// TestStatsRows covers the raw row query: uncompressed calls
// have delivered equal to raw, compressed calls have delivered from
// the compressed output, the most recent compression is used when
// multiple exist, and an empty database returns an empty slice.
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

	t.Run("most recent compression is used when multiple exist", func(t *testing.T) {
		d := openMemoryDB(t)
		sourceID := seedSessionAndCalls(t, d, []callFixture{
			{id: "dddddddddddddddd", tool: "bash", input: `{"command": "cat file"}`, output: mustLongString(800)},
		})[0]

		seedCompressed(t, d, sourceID, "medium output here\n", "2026-07-12T00:00:01Z")
		seedCompressed(t, d, sourceID, "tiny\n", "2026-07-12T00:00:02Z")

		rows, err := d.StatsRows()
		if err != nil {
			t.Fatalf("StatsRows: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(rows))
		}
		want := len("tiny\n")
		if rows[0].DeliveredBytes != want {
			t.Errorf("delivered=%d, want %d (most recent compression)", rows[0].DeliveredBytes, want)
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
		{id: "emptyemptyemptyx", output: "", timestamp: from.Add(time.Minute).Format(time.RFC3339Nano)},
		{id: "compressedsource", output: "123456789", timestamp: from.Add(2 * time.Minute).Format(time.RFC3339Nano)},
		{id: "emptycompressedx", output: "1234", timestamp: from.Add(3 * time.Minute).Format(time.RFC3339Nano)},
		{id: "beforebeforebefx", output: "outside", timestamp: from.Add(-time.Nanosecond).Format(time.RFC3339Nano)},
		{id: "afterafterafterx", output: "outside", timestamp: to.Add(time.Nanosecond).Format(time.RFC3339Nano)},
		{id: "invalidinvalidxx", output: "outside", timestamp: "not-a-timestamp"},
	})

	// The latest timestamp wins. At an equal timestamp, the greatest ID wins.
	seedCompressedWithID(t, d, "oldoldoldoldoldx", ids[3], "12345678", from.Add(4*time.Minute).Format(time.RFC3339Nano))
	latestTimestamp := from.Add(5*time.Minute + time.Nanosecond).Format(time.RFC3339Nano)
	seedCompressedWithID(t, d, "aaaaaaaaaaaaaaax", ids[3], "1", latestTimestamp)
	seedCompressedWithID(t, d, "zzzzzzzzzzzzzzzx", ids[3], "1234", latestTimestamp)
	seedCompressedWithID(t, d, "emptyartifactxxx", ids[4], "", from.Add(5*time.Minute).Format(time.RFC3339Nano))

	if _, err := d.db.Exec(`INSERT INTO tool_calls (id, session_id, output, timestamp)
		VALUES (?, (SELECT session_id FROM tool_calls LIMIT 1), NULL, ?)`,
		"nullnullnullnull", from.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert NULL output: %v", err)
	}

	rows, err := d.StatsSummaryRows(from, to)
	if err != nil {
		t.Fatalf("StatsSummaryRows: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(rows), rows)
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
	if rawChars != 23 || deliveredChars != 14 {
		t.Errorf("character totals raw=%d delivered=%d, want 23 and 14", rawChars, deliveredChars)
	}
	if compressed != 2 {
		t.Errorf("compressed artifact count=%d, want 2", compressed)
	}
	if rows[0].RawChars != 0 {
		t.Errorf("empty non-NULL output missing: rows=%+v", rows)
	}
}

// callFixture is a minimal tool_call fixture for seeding.
type callFixture struct {
	id        string
	tool      string
	input     string
	output    string
	timestamp string
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
