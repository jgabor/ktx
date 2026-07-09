// Table-driven unit tests for internal/db/compress.go.
//
// Acceptance coverage:
//   - InsertCompressed happy path + 6-column assertion
//   - InsertCompressed with unknown source_id → ErrSourceNotFound (FK)
//   - GetCompressed hit + miss (ErrNotFound)
//   - GetSourceOutput hit + miss (ErrNotFound) — the reversal primitive
//   - content_hash round-trip: insert with hash of output B; read back;
//     hash of stored output B equals the inserted hash
//
// All subtests use OpenMemory() (the existing in-memory seam) — no
// filesystem fixtures required.
package db

import (
	"errors"
	"strings"
	"testing"
)

// seedToolCallForCompress inserts a session + tool_call so the
// compressed.source_id FK has a valid target. Returns the tool_call id.
// Mirrors the seed pattern used by persist_test.go's seed helpers but
// keeps the compress fixture self-contained.
func seedToolCallForCompress(t *testing.T, d *DB) string {
	t.Helper()
	sessionID := mustNewID(t)
	if err := d.InsertSession(&Session{
		ID:         sessionID,
		Agent:      "test-agent",
		StartedAt:  "2026-06-24T00:00:00Z",
		LastSeenAt: "2026-06-24T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	tcID := mustNewID(t)
	if err := d.PersistToolCall(&ToolCall{
		ID:        tcID,
		SessionID: sessionID,
		Tool:      "bash",
		Input:     "echo hi",
		Output:    "hi\n",
		Timestamp: "2026-06-24T00:00:01Z",
	}); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}
	return tcID
}

// TestInsertCompressed covers the happy path + FK rejection leg of
// the acceptance contract.
func TestInsertCompressed(t *testing.T) {
	t.Run("happy path persists all 6 columns", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sourceID := seedToolCallForCompress(t, d)

		// Recompute content_hash from the COMPRESSED output (the
		// integrity rule: hash the artifact that was stored). Here
		// the compressed output happens to equal the source output
		// (passthrough pipeline), so the hash matches the source's
		// content_hash — but the contract is "hash what you stored".
		compressedOutput := "hi\n"
		hash := ContentHash(compressedOutput)
		cid := mustNewID(t)
		c := &Compressed{
			ID:          cid,
			SourceID:    sourceID,
			Output:      compressedOutput,
			ContentHash: hash,
			Pipeline:    "[]", // empty pipeline = passthrough (default)
			Timestamp:   "2026-06-24T00:00:02Z",
		}
		if err := d.InsertCompressed(c); err != nil {
			t.Fatalf("InsertCompressed: %v", err)
		}

		// Re-read via GetCompressed to confirm the row round-trips
		// with all 6 columns populated.
		got, err := d.GetCompressed(cid)
		if err != nil {
			t.Fatalf("GetCompressed: %v", err)
		}
		if got.ID != cid {
			t.Errorf("ID = %q, want %q", got.ID, cid)
		}
		if got.SourceID != sourceID {
			t.Errorf("SourceID = %q, want %q", got.SourceID, sourceID)
		}
		if got.Output != compressedOutput {
			t.Errorf("Output = %q, want %q", got.Output, compressedOutput)
		}
		if got.ContentHash != hash {
			t.Errorf("ContentHash = %q, want %q", got.ContentHash, hash)
		}
		if got.Pipeline != "[]" {
			t.Errorf("Pipeline = %q, want %q", got.Pipeline, "[]")
		}
		if got.Timestamp != "2026-06-24T00:00:02Z" {
			t.Errorf("Timestamp = %q, want 2026-06-24T00:00:02Z", got.Timestamp)
		}
	})

	t.Run("unknown source_id → ErrSourceNotFound (FK enforced)", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		cid := mustNewID(t)
		err = d.InsertCompressed(&Compressed{
			ID:          cid,
			SourceID:    "zzzzzzzzzzzzzzzz", // no tool_call with this id
			Output:      "out",
			ContentHash: "hash",
			Pipeline:    "[]",
			Timestamp:   "2026-06-24T00:00:04Z",
		})
		if !errors.Is(err, ErrSourceNotFound) {
			t.Fatalf("InsertCompressed unknown source_id: err = %v, want ErrSourceNotFound", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ErrSourceNotFound does not wrap ErrNotFound; CLI exit-code mapping would break")
		}
		// The message must name the compressed id and source so the
		// user can debug which reference was dangling.
		if !strings.Contains(err.Error(), cid) {
			t.Errorf("error message %q missing compressed id %q", err.Error(), cid)
		}
	})

	t.Run("empty id is a programmer error", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		sourceID := seedToolCallForCompress(t, d)

		if err := d.InsertCompressed(&Compressed{
			ID: "", SourceID: sourceID, Timestamp: "2026-06-24T00:00:05Z",
		}); err == nil {
			t.Fatal("InsertCompressed with empty id succeeded; want programmer error")
		}
	})

	t.Run("empty source_id is a programmer error", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		cid := mustNewID(t)
		if err := d.InsertCompressed(&Compressed{
			ID: cid, SourceID: "", Timestamp: "2026-06-24T00:00:06Z",
		}); err == nil {
			t.Fatal("InsertCompressed with empty source_id succeeded; want programmer error")
		}
	})
}

// TestGetCompressed covers the lookup leg of the acceptance contract.
func TestGetCompressed(t *testing.T) {
	t.Run("hit returns full struct", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		sourceID := seedToolCallForCompress(t, d)

		cid := mustNewID(t)
		c := &Compressed{
			ID: cid, SourceID: sourceID,
			Output: "compressed", ContentHash: "h", Pipeline: `["head:5"]`,
			Timestamp: "2026-06-24T00:00:07Z",
		}
		if err := d.InsertCompressed(c); err != nil {
			t.Fatalf("InsertCompressed: %v", err)
		}

		got, err := d.GetCompressed(cid)
		if err != nil {
			t.Fatalf("GetCompressed: %v", err)
		}
		if got.Pipeline != `["head:5"]` {
			t.Errorf("Pipeline = %q, want [\"head:5\"]", got.Pipeline)
		}
	})

	t.Run("miss → ErrNotFound", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		_, err = d.GetCompressed("zzzzzzzzzzzzzzzz")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetCompressed miss: err = %v, want ErrNotFound", err)
		}
	})
}

// TestGetSourceOutput covers the reversal primitive (the public-surface
// acceptance "a compressed output can be retrieved as the original
// bytes via the public surface").
func TestGetSourceOutput(t *testing.T) {
	t.Run("hit returns source output verbatim", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Persist a tool_call whose output we will then "decompress".
		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, StartedAt: "2026-06-24T00:00:00Z",
			LastSeenAt: "2026-06-24T00:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		sourceID := mustNewID(t)
		originalOutput := "line1\nline2\nline3\nline4\nline5\n"
		if err := d.PersistToolCall(&ToolCall{
			ID: sourceID, SessionID: sessionID, Tool: "bash",
			Output: originalOutput, Timestamp: "2026-06-24T00:00:01Z",
		}); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}

		got, err := d.GetSourceOutput(sourceID)
		if err != nil {
			t.Fatalf("GetSourceOutput: %v", err)
		}
		if got != originalOutput {
			t.Errorf("GetSourceOutput = %q, want %q (verbatim)", got, originalOutput)
		}
	})

	t.Run("miss → ErrNotFound", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		_, err = d.GetSourceOutput("zzzzzzzzzzzzzzzz")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetSourceOutput miss: err = %v, want ErrNotFound", err)
		}
	})
}

// TestCompressed_ContentHashRoundTrip covers the content_hash
// acceptance: the
// content_hash on a Compressed row, when computed by the caller via
// db.ContentHash(compressedOutput), is the SHA-256 of the COMPRESSED
// output (not the source output). Re-reading the row and re-hashing
// the stored output must yield the stored hash — the round-trip
// integrity check.
func TestCompressed_ContentHashRoundTrip(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	sourceID := seedToolCallForCompress(t, d)

	// Simulate a lossy pipeline: source output "hi\n" compressed to
	// "h" via a hypothetical head:1-byte stage. The content_hash is
	// recomputed from the COMPRESSED output "h", not from "hi\n".
	compressedOutput := "h"
	storedHash := ContentHash(compressedOutput)
	cid := mustNewID(t)
	if err := d.InsertCompressed(&Compressed{
		ID: cid, SourceID: sourceID,
		Output: compressedOutput, ContentHash: storedHash,
		Pipeline:  `["head:1"]`,
		Timestamp: "2026-06-24T00:00:08Z",
	}); err != nil {
		t.Fatalf("InsertCompressed: %v", err)
	}

	got, err := d.GetCompressed(cid)
	if err != nil {
		t.Fatalf("GetCompressed: %v", err)
	}
	// Re-hash the persisted output; it MUST equal the persisted hash.
	recomputed := ContentHash(got.Output)
	if recomputed != got.ContentHash {
		t.Errorf("content_hash mismatch: persisted %q, recomputed-from-output %q",
			got.ContentHash, recomputed)
	}
	if got.ContentHash != storedHash {
		t.Errorf("ContentHash round-trip failed: got %q, want %q", got.ContentHash, storedHash)
	}
	// And the hash of the compressed output must NOT equal the hash
	// of the source output (proving we hashed the artifact, not the
	// source) — for "hi\n" vs "h" the SHA-256 digests differ.
	sourceHash := ContentHash("hi\n")
	if got.ContentHash == sourceHash {
		t.Errorf("content_hash equals source-output hash; the integrity rule says hash the stored artifact")
	}
}
