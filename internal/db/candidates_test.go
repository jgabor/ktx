package db

import "testing"

// TestCandidateRows covers the raw row query: uncompressed calls
// have delivered equal to raw, compressed calls have delivered from
// the compressed output, the most recent compression is used when
// multiple exist, and an empty database returns an empty slice.
func TestCandidateRows(t *testing.T) {
	t.Run("uncompressed calls have delivered equal to raw", func(t *testing.T) {
		d := openMemoryDB(t)
		seedSessionAndCalls(t, d, []callFixture{
			{id: "aaaaaaaaaaaaaaaa", tool: "bash", input: `{"command": "ls -la"}`, output: "drwxr-xr-x  2 user user 4096 Jan 1 00:00 .\n"},
			{id: "bbbbbbbbbbbbbbbb", tool: "bash", input: `{"command": "ls -la"}`, output: "drwxr-xr-x  2 user user 4096 Jan 1 00:00 ..\n"},
		})

		rows, err := d.CandidateRows()
		if err != nil {
			t.Fatalf("CandidateRows: %v", err)
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

		rows, err := d.CandidateRows()
		if err != nil {
			t.Fatalf("CandidateRows: %v", err)
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

		rows, err := d.CandidateRows()
		if err != nil {
			t.Fatalf("CandidateRows: %v", err)
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
		rows, err := d.CandidateRows()
		if err != nil {
			t.Fatalf("CandidateRows: %v", err)
		}
		if len(rows) != 0 {
			t.Fatalf("got %d rows, want 0", len(rows))
		}
	})
}

// callFixture is a minimal tool_call fixture for seeding.
type callFixture struct {
	id     string
	tool   string
	input  string
	output string
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
		if err := d.PersistToolCall(&ToolCall{
			ID:        c.id,
			SessionID: sid,
			Tool:      c.tool,
			Input:     c.input,
			Output:    c.output,
			Timestamp: "2026-07-12T00:00:01Z",
		}); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}
		ids = append(ids, c.id)
	}
	return ids
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
