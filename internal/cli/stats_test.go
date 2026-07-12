package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// TestStatsLogic exercises the aggregation, filtering,
// sorting, and rendering of `ktx stats`.
func TestStatsLogic(t *testing.T) {
	t.Run("groups by tool and rank by delivered tokens descending", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls -la", output: strings.Repeat("x", 400) + "\n"},
			{tool: "bash", command: "ls -la", output: strings.Repeat("y", 400) + "\n"},
			{tool: "bash", command: "cat big.txt", output: strings.Repeat("z", 800) + "\n"},
			{tool: "bash", command: "cat big.txt", output: strings.Repeat("w", 800) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v\noutput: %s", err, buf.String())
		}
		if len(groups) != 2 {
			t.Fatalf("got %d groups, want 2", len(groups))
		}

		// cat big.txt has 800-byte outputs, ls -la has 400-byte.
		if groups[0].Command != "cat" {
			t.Errorf("first group command=%q, want %q (higher delivered tokens)", groups[0].Command, "cat")
		}
		if groups[0].TotalDeliveredTokens <= groups[1].TotalDeliveredTokens {
			t.Errorf("groups not sorted desc: first=%d, second=%d",
				groups[0].TotalDeliveredTokens, groups[1].TotalDeliveredTokens)
		}
		if groups[0].Tool != "bash" {
			t.Errorf("first group tool=%q, want %q", groups[0].Tool, "bash")
		}
		if groups[0].Calls != 2 {
			t.Errorf("first group calls=%d, want 2", groups[0].Calls)
		}
	})

	t.Run("uncompressed calls have delivered equal to raw", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: strings.Repeat("a", 400) + "\n"},
			{tool: "bash", command: "ls", output: strings.Repeat("b", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1", len(groups))
		}
		if groups[0].TotalRawTokens != groups[0].TotalDeliveredTokens {
			t.Errorf("raw=%d != delivered=%d (should be equal for uncompressed)",
				groups[0].TotalRawTokens, groups[0].TotalDeliveredTokens)
		}
	})

	t.Run("compressed calls have delivered less than raw", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		ids := seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "cat big", output: strings.Repeat("x", 800) + "\n"},
			{tool: "bash", command: "cat big", output: strings.Repeat("y", 800) + "\n"},
		})
		seedCompressedRow(t, db, ids[0], strings.Repeat("s", 200)+"\n", "2026-07-12T00:00:01Z")
		seedCompressedRow(t, db, ids[1], strings.Repeat("s", 200)+"\n", "2026-07-12T00:00:01Z")

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1", len(groups))
		}
		if groups[0].TotalDeliveredTokens >= groups[0].TotalRawTokens {
			t.Errorf("delivered=%d should be < raw=%d (compressed)",
				groups[0].TotalDeliveredTokens, groups[0].TotalRawTokens)
		}
	})

	t.Run("groups with fewer than 2 calls excluded", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: strings.Repeat("a", 400) + "\n"},
			{tool: "read", command: "cat", output: strings.Repeat("b", 400) + "\n"},
			{tool: "read", command: "cat", output: strings.Repeat("c", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		for _, g := range groups {
			if g.Calls < 2 {
				t.Errorf("group with calls=%d should have been excluded", g.Calls)
			}
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1 (only the 2-call group)", len(groups))
		}
	})

	t.Run("groups with fewer than 50 delivered tokens excluded", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: "short\n"}, // 6 bytes → ~1 token
			{tool: "bash", command: "ls", output: "short\n"}, // 6 bytes → ~1 token
			{tool: "bash", command: "cat", output: strings.Repeat("z", 400) + "\n"},
			{tool: "bash", command: "cat", output: strings.Repeat("z", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		for _, g := range groups {
			if g.TotalDeliveredTokens < 50 {
				t.Errorf("group with delivered=%d should have been excluded", g.TotalDeliveredTokens)
			}
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1 (only the 50+ token group)", len(groups))
		}
	})

	t.Run("--limit caps the number of groups", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: strings.Repeat("a", 400) + "\n"},
			{tool: "bash", command: "ls", output: strings.Repeat("b", 400) + "\n"},
			{tool: "read", command: "cat", output: strings.Repeat("c", 400) + "\n"},
			{tool: "read", command: "cat", output: strings.Repeat("d", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, 1, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups with --limit 1, want 1", len(groups))
		}
	})

	t.Run("text output is a table", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: strings.Repeat("a", 400) + "\n"},
			{tool: "bash", command: "ls", output: strings.Repeat("b", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatText, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "TOOL") {
			t.Errorf("text output missing header: %q", out)
		}
		if !strings.Contains(out, "bash") {
			t.Errorf("text output missing tool name: %q", out)
		}
		if !strings.Contains(out, "ls") {
			t.Errorf("text output missing command: %q", out)
		}
	})

	t.Run("empty database returns empty output", func(t *testing.T) {
		db := openMemoryDB(t)

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if len(groups) != 0 {
			t.Fatalf("got %d groups, want 0", len(groups))
		}

		var textBuf bytes.Buffer
		_, err = statsLogic(nil, FormatText, statsDefaultLimit, db, &textBuf)
		if err != nil {
			t.Fatalf("statsLogic text: %v", err)
		}
		if !strings.Contains(textBuf.String(), "no stats") {
			t.Errorf("text output should say 'no stats': %q", textBuf.String())
		}
	})

	t.Run("invalid --limit returns usage error", func(t *testing.T) {
		db := openMemoryDB(t)
		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, 0, db, &buf)
		if err == nil {
			t.Error("expected usage error for --limit 0")
		}
	})

	t.Run("--help renders help text", func(t *testing.T) {
		db := openMemoryDB(t)
		var buf bytes.Buffer
		_, err := statsLogic([]string{"--help"}, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}
		if !strings.Contains(buf.String(), "ktx stats") {
			t.Errorf("help output missing command name: %q", buf.String())
		}
	})
}

// openMemoryDB is a helper that opens an in-memory database.
func openMemoryDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// callSeed is a minimal tool_call fixture for the stats tests.
type callSeed struct {
	tool    string
	command string
	output  string
}

// seedSession inserts a session and returns its ID.
func seedSession(t *testing.T, db *dbpkg.DB) string {
	t.Helper()
	sid := "aaaaaaaaaaaaaaaa"
	if err := db.InsertSession(&dbpkg.Session{
		ID:         sid,
		Agent:      "test-agent",
		StartedAt:  "2026-07-12T00:00:00Z",
		LastSeenAt: "2026-07-12T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	return sid
}

// seedCalls inserts tool_call rows and returns their IDs.
func seedCalls(t *testing.T, db *dbpkg.DB, sessionID string, calls []callSeed) []string {
	t.Helper()
	var ids []string
	for i, c := range calls {
		id := string(rune('a'+i)) + string(rune('b'+i)) + "xxxxxxxxxxxxxx"
		if len(id) != 16 {
			id = strings.Repeat("a", 16)
		}
		if err := db.PersistToolCall(&dbpkg.ToolCall{
			ID:        id,
			SessionID: sessionID,
			Tool:      c.tool,
			Input:     `{"command": "` + c.command + `"}`,
			Output:    c.output,
			Timestamp: "2026-07-12T00:00:01Z",
		}); err != nil {
			t.Fatalf("PersistToolCall[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// seedCompressedRow inserts a compressed row for a source tool_call.
func seedCompressedRow(t *testing.T, db *dbpkg.DB, sourceID, output, timestamp string) {
	t.Helper()
	cid := "c" + sourceID[1:]
	if err := db.InsertCompressed(&dbpkg.Compressed{
		ID:          cid,
		SourceID:    sourceID,
		Output:      output,
		ContentHash: dbpkg.ContentHash(output),
		Pipeline:    "[]",
		Timestamp:   timestamp,
	}); err != nil {
		t.Fatalf("InsertCompressed: %v", err)
	}
}
