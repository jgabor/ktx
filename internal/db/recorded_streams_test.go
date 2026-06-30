// Table-driven unit tests for internal/db/recorded_streams.go (D20 plan Task 2).
//
// Acceptance coverage (per the plan's acceptance bullets):
//   - InsertRecordedStream happy path + 7-column assertion
//   - InsertRecordedStream with unknown session_id → ErrRecordedStreamSourceNotFound (FK)
//   - InsertRecordedStream with unknown bench_run_id → ErrRecordedStreamSourceNotFound (FK)
//   - GetRecordedStreamBySession hit + miss (ErrNotFound)
//   - GetRecordedStreamByBenchRun hit + miss (ErrNotFound)
//   - content_hash round-trip: insert with hash of stream_text; read back;
//     hash of stored stream_text equals the inserted hash
//   - empty stream_text round-trip: a session that emitted no events still
//     gets captured; content_hash = ContentHash('') (SHA-256 of empty string)
//
// All subtests use OpenMemory() (the existing in-memory seam) per the
// plan's test_approach — no filesystem fixtures required.
package db

import (
	"errors"
	"testing"
)

// seedSessionAndBenchRun inserts a session + bench_run so the
// recorded_streams FKs have valid targets. Returns the session_id and
// bench_run_id. Mirrors seedToolCallForCompress's self-contained pattern.
func seedSessionAndBenchRun(t *testing.T, d *DB) (sessionID, benchRunID string) {
	t.Helper()
	sessionID = mustNewID(t)
	if err := d.InsertSession(&Session{
		ID:         sessionID,
		Agent:      "opencode",
		StartedAt:  "2026-06-30T00:00:00Z",
		LastSeenAt: "2026-06-30T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	benchRunID = mustNewID(t)
	if err := d.InsertBenchRun(&BenchRun{
		ID:                 benchRunID,
		Agent:              "opencode",
		Model:              "glm-5.2-short-fast-flex",
		Task:               "addition_v1",
		Score:              1.0,
		MethodologyVersion: "v1",
		Timestamp:          1782834992,
	}); err != nil {
		t.Fatalf("InsertBenchRun: %v", err)
	}
	return sessionID, benchRunID
}

// TestInsertRecordedStream covers the happy path + FK rejection legs of
// plan Task 2's acceptance.
func TestInsertRecordedStream(t *testing.T) {
	t.Run("happy path persists all 7 columns", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID, benchRunID := seedSessionAndBenchRun(t, d)

		streamText := `{"type":"text","part":{"text":"4"}}`
		hash := ContentHash(streamText)
		rsID := mustNewID(t)
		rs := &RecordedStream{
			ID:           rsID,
			SessionID:    sessionID,
			BenchRunID:   benchRunID,
			StreamText:   streamText,
			ContentHash:  hash,
			StreamFormat: "opencode-jsonl-1",
			CapturedAt:   "2026-06-30T12:00:00Z",
		}
		if err := d.InsertRecordedStream(rs); err != nil {
			t.Fatalf("InsertRecordedStream: %v", err)
		}

		// Re-read via GetRecordedStreamBySession to confirm the row
		// round-trips with all 7 columns populated.
		got, err := d.GetRecordedStreamBySession(sessionID)
		if err != nil {
			t.Fatalf("GetRecordedStreamBySession: %v", err)
		}
		if got.ID != rsID {
			t.Errorf("ID = %q, want %q", got.ID, rsID)
		}
		if got.SessionID != sessionID {
			t.Errorf("SessionID = %q, want %q", got.SessionID, sessionID)
		}
		if got.BenchRunID != benchRunID {
			t.Errorf("BenchRunID = %q, want %q", got.BenchRunID, benchRunID)
		}
		if got.StreamText != streamText {
			t.Errorf("StreamText = %q, want %q", got.StreamText, streamText)
		}
		if got.ContentHash != hash {
			t.Errorf("ContentHash = %q, want %q", got.ContentHash, hash)
		}
		if got.StreamFormat != "opencode-jsonl-1" {
			t.Errorf("StreamFormat = %q, want %q", got.StreamFormat, "opencode-jsonl-1")
		}
		if got.CapturedAt != "2026-06-30T12:00:00Z" {
			t.Errorf("CapturedAt = %q, want %q", got.CapturedAt, "2026-06-30T12:00:00Z")
		}
	})

	t.Run("unknown session_id → ErrRecordedStreamSourceNotFound (FK enforced)", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		_, benchRunID := seedSessionAndBenchRun(t, d)

		rsID := mustNewID(t)
		err = d.InsertRecordedStream(&RecordedStream{
			ID:           rsID,
			SessionID:    "zzzzzzzzzzzzzzzz",
			BenchRunID:   benchRunID,
			StreamText:   "",
			ContentHash:  "",
			StreamFormat: "opencode-jsonl-1",
			CapturedAt:   "2026-06-30T12:00:00Z",
		})
		if !errors.Is(err, ErrRecordedStreamSourceNotFound) {
			t.Fatalf("InsertRecordedStream unknown session_id: err = %v, want ErrRecordedStreamSourceNotFound", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ErrRecordedStreamSourceNotFound does not wrap ErrNotFound; CLI exit-code mapping would break")
		}
	})

	t.Run("unknown bench_run_id → ErrRecordedStreamSourceNotFound (FK enforced)", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID, _ := seedSessionAndBenchRun(t, d)

		rsID := mustNewID(t)
		err = d.InsertRecordedStream(&RecordedStream{
			ID:           rsID,
			SessionID:    sessionID,
			BenchRunID:   "zzzzzzzzzzzzzzzz",
			StreamText:   "",
			ContentHash:  "",
			StreamFormat: "opencode-jsonl-1",
			CapturedAt:   "2026-06-30T12:00:00Z",
		})
		if !errors.Is(err, ErrRecordedStreamSourceNotFound) {
			t.Fatalf("InsertRecordedStream unknown bench_run_id: err = %v, want ErrRecordedStreamSourceNotFound", err)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ErrRecordedStreamSourceNotFound does not wrap ErrNotFound; CLI exit-code mapping would break")
		}
	})

	t.Run("empty id is a programmer error", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		sessionID, benchRunID := seedSessionAndBenchRun(t, d)

		if err := d.InsertRecordedStream(&RecordedStream{
			ID: "", SessionID: sessionID, BenchRunID: benchRunID,
			CapturedAt: "2026-06-30T12:00:00Z",
		}); err == nil {
			t.Fatal("InsertRecordedStream with empty id succeeded; want programmer error")
		}
	})

	t.Run("empty session_id is a programmer error", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		_, benchRunID := seedSessionAndBenchRun(t, d)

		rsID := mustNewID(t)
		if err := d.InsertRecordedStream(&RecordedStream{
			ID: rsID, SessionID: "", BenchRunID: benchRunID,
			CapturedAt: "2026-06-30T12:00:00Z",
		}); err == nil {
			t.Fatal("InsertRecordedStream with empty session_id succeeded; want programmer error")
		}
	})

	t.Run("empty bench_run_id is a programmer error", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		sessionID, _ := seedSessionAndBenchRun(t, d)

		rsID := mustNewID(t)
		if err := d.InsertRecordedStream(&RecordedStream{
			ID: rsID, SessionID: sessionID, BenchRunID: "",
			CapturedAt: "2026-06-30T12:00:00Z",
		}); err == nil {
			t.Fatal("InsertRecordedStream with empty bench_run_id succeeded; want programmer error")
		}
	})
}

// TestGetRecordedStreamBySession covers the session-id lookup leg of
// plan Task 2's acceptance (the public --replay arg per D13/D20).
func TestGetRecordedStreamBySession(t *testing.T) {
	t.Run("hit returns full struct", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		sessionID, benchRunID := seedSessionAndBenchRun(t, d)

		streamText := `{"type":"step_finish","part":{"tokens":{"input":41,"output":2},"cost":0.002597825}}`
		rsID := mustNewID(t)
		if err := d.InsertRecordedStream(&RecordedStream{
			ID: rsID, SessionID: sessionID, BenchRunID: benchRunID,
			StreamText: streamText, ContentHash: ContentHash(streamText),
			StreamFormat: "opencode-jsonl-1", CapturedAt: "2026-06-30T12:00:00Z",
		}); err != nil {
			t.Fatalf("InsertRecordedStream: %v", err)
		}

		got, err := d.GetRecordedStreamBySession(sessionID)
		if err != nil {
			t.Fatalf("GetRecordedStreamBySession: %v", err)
		}
		if got.ID != rsID {
			t.Errorf("ID = %q, want %q", got.ID, rsID)
		}
		if got.StreamText != streamText {
			t.Errorf("StreamText = %q, want %q", got.StreamText, streamText)
		}
	})

	t.Run("miss → ErrNotFound", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		_, err = d.GetRecordedStreamBySession("zzzzzzzzzzzzzzzz")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetRecordedStreamBySession miss: err = %v, want ErrNotFound", err)
		}
	})
}

// TestGetRecordedStreamByBenchRun covers the bench_run_id lookup leg
// (internal FK lookup; bench_run_id is NOT a public --replay arg).
func TestGetRecordedStreamByBenchRun(t *testing.T) {
	t.Run("hit returns full struct", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		sessionID, benchRunID := seedSessionAndBenchRun(t, d)

		streamText := `{"type":"text"}`
		rsID := mustNewID(t)
		if err := d.InsertRecordedStream(&RecordedStream{
			ID: rsID, SessionID: sessionID, BenchRunID: benchRunID,
			StreamText: streamText, ContentHash: ContentHash(streamText),
			StreamFormat: "opencode-jsonl-1", CapturedAt: "2026-06-30T12:00:00Z",
		}); err != nil {
			t.Fatalf("InsertRecordedStream: %v", err)
		}

		got, err := d.GetRecordedStreamByBenchRun(benchRunID)
		if err != nil {
			t.Fatalf("GetRecordedStreamByBenchRun: %v", err)
		}
		if got.ID != rsID {
			t.Errorf("ID = %q, want %q", got.ID, rsID)
		}
	})

	t.Run("miss → ErrNotFound", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		_, err = d.GetRecordedStreamByBenchRun("zzzzzzzzzzzzzzzz")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetRecordedStreamByBenchRun miss: err = %v, want ErrNotFound", err)
		}
	})
}

// TestRecordedStream_ContentHashRoundTrip covers plan Task 2 AC5: the
// content_hash on a RecordedStream row, when computed by the caller via
// db.ContentHash(streamText), is the SHA-256 of the stream_text.
// Re-reading the row and re-hashing the stored stream_text must yield
// the stored hash — the round-trip integrity check that --replay's
// stream-integrity check exercises.
func TestRecordedStream_ContentHashRoundTrip(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID, benchRunID := seedSessionAndBenchRun(t, d)

	streamText := `{"type":"tool_use","part":{"tool":"bash","input":"echo hi"}}
{"type":"text","part":{"text":"hi"}}
{"type":"step_finish","part":{"tokens":{"input":10,"output":1},"cost":0.001}}`
	storedHash := ContentHash(streamText)
	rsID := mustNewID(t)
	if err := d.InsertRecordedStream(&RecordedStream{
		ID: rsID, SessionID: sessionID, BenchRunID: benchRunID,
		StreamText: streamText, ContentHash: storedHash,
		StreamFormat: "opencode-jsonl-1", CapturedAt: "2026-06-30T12:00:00Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream: %v", err)
	}

	got, err := d.GetRecordedStreamBySession(sessionID)
	if err != nil {
		t.Fatalf("GetRecordedStreamBySession: %v", err)
	}
	recomputed := ContentHash(got.StreamText)
	if recomputed != got.ContentHash {
		t.Errorf("content_hash mismatch: persisted %q, recomputed-from-stream_text %q",
			got.ContentHash, recomputed)
	}
	if got.ContentHash != storedHash {
		t.Errorf("ContentHash round-trip failed: got %q, want %q", got.ContentHash, storedHash)
	}
}

// TestRecordedStream_EmptyStreamText covers plan Task 2 AC6: empty
// stream_text is a valid stream (a session that emitted no events still
// gets captured). content_hash = ContentHash('') — SHA-256 of the
// empty string, a real value. The absence-vs-empty distinction is
// preserved; no silent skip on empty stream.
func TestRecordedStream_EmptyStreamText(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	sessionID, benchRunID := seedSessionAndBenchRun(t, d)

	emptyStreamText := ""
	emptyHash := ContentHash(emptyStreamText)
	rsID := mustNewID(t)
	if err := d.InsertRecordedStream(&RecordedStream{
		ID: rsID, SessionID: sessionID, BenchRunID: benchRunID,
		StreamText: emptyStreamText, ContentHash: emptyHash,
		StreamFormat: "opencode-jsonl-1", CapturedAt: "2026-06-30T12:00:00Z",
	}); err != nil {
		t.Fatalf("InsertRecordedStream with empty stream_text: %v", err)
	}

	got, err := d.GetRecordedStreamBySession(sessionID)
	if err != nil {
		t.Fatalf("GetRecordedStreamBySession: %v", err)
	}
	if got.StreamText != "" {
		t.Errorf("StreamText = %q, want empty string", got.StreamText)
	}
	recomputed := ContentHash(got.StreamText)
	if recomputed != got.ContentHash {
		t.Errorf("content_hash mismatch for empty stream: persisted %q, recomputed %q",
			got.ContentHash, recomputed)
	}
	// SHA-256 of empty string is a known value — verify it's not the
	// zero value or a nil hash.
	if got.ContentHash == "" {
		t.Error("content_hash for empty stream_text is empty; want SHA-256 of empty string")
	}
	if got.ContentHash != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("content_hash for empty stream_text = %q, want known SHA-256 of empty string", got.ContentHash)
	}
}
