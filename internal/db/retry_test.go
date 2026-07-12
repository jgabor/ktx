package db

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestPersistToolCallOnceConcurrentRetry(t *testing.T) {
	d, err := Open(t.TempDir() + "/ktx.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if err := d.InsertSession(&Session{ID: "session", Agent: "codex", StartedAt: "2026-07-12T10:00:00Z", LastSeenAt: "2026-07-12T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	const deliveries = 12
	start := make(chan struct{})
	results := make(chan bool, deliveries)
	errs := make(chan error, deliveries)
	var wg sync.WaitGroup
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			runtime := "codex"
			tc := &ToolCall{
				ID: "abcdefghijklmnop", SessionID: "session", Tool: "exec",
				Input: `{"cmd":"go test ./..."}`, Output: "ok\n", Timestamp: "2026-07-12T10:00:01Z",
				ContentHash: ContentHash("ok\n"), Runtime: &runtime,
			}
			inserted, err := d.PersistToolCallOnce(tc)
			results <- inserted
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	inserted := 0
	for result := range results {
		if result {
			inserted++
		}
	}
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent retry: %v", err)
		}
	}
	if inserted != 1 {
		t.Fatalf("inserted deliveries = %d, want 1", inserted)
	}
	session, err := d.GetSession("session")
	if err != nil {
		t.Fatal(err)
	}
	if session.ToolCallCount != 1 {
		t.Errorf("tool_call_count = %d, want 1", session.ToolCallCount)
	}
}

func TestPersistToolCallOnceConflictPreservesFirstEvidence(t *testing.T) {
	d, err := Open(t.TempDir() + "/ktx.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.InsertSession(&Session{ID: "session", Agent: "codex", StartedAt: "2026-07-12T10:00:00Z", LastSeenAt: "2026-07-12T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	runtime := "codex"
	first := &ToolCall{ID: "abcdefghijklmnop", SessionID: "session", Tool: "exec", Input: "first input", Output: "first output", Timestamp: "2026-07-12T10:00:01Z", Runtime: &runtime}
	if inserted, err := d.PersistToolCallOnce(first); err != nil || !inserted {
		t.Fatalf("first insert = %v, %v", inserted, err)
	}
	changed := *first
	changed.Input = "changed input"
	changed.Output = "changed output"
	if _, err := d.PersistToolCallOnce(&changed); !errors.Is(err, ErrToolCallConflict) {
		t.Fatalf("changed retry error = %v, want ErrToolCallConflict", err)
	}
	got, err := d.GetToolCall(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Input != first.Input || got.Output != first.Output {
		t.Errorf("persisted evidence = (%q, %q), want first (%q, %q)", got.Input, got.Output, first.Input, first.Output)
	}
}

func TestInsertCompressedOnceConcurrentRetry(t *testing.T) {
	d, err := Open(t.TempDir() + "/ktx.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.InsertSession(&Session{ID: "session", Agent: "codex", StartedAt: "2026-07-12T10:00:00Z", LastSeenAt: "2026-07-12T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	runtime := "codex"
	tc := &ToolCall{ID: "abcdefghijklmnop", SessionID: "session", Tool: "exec", Input: "input", Output: "long output", Timestamp: "2026-07-12T10:00:01Z", Runtime: &runtime}
	if _, err := d.PersistToolCallOnce(tc); err != nil {
		t.Fatal(err)
	}

	const deliveries = 12
	start := make(chan struct{})
	ids := make(chan string, deliveries)
	errs := make(chan error, deliveries)
	var wg sync.WaitGroup
	for i := 0; i < deliveries; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			candidate := &Compressed{ID: fmt.Sprintf("artifact%08d", i), SourceID: tc.ID, Output: "short", ContentHash: ContentHash("short"), Pipeline: `["head:1"]`, Timestamp: "2026-07-12T10:00:02Z"}
			stored, _, err := d.InsertCompressedOnce(candidate)
			if stored != nil {
				ids <- stored.ID
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)

	var winner string
	for id := range ids {
		if winner == "" {
			winner = id
		} else if id != winner {
			t.Errorf("returned compressed id = %q, want winner %q", id, winner)
		}
	}
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent compressed retry: %v", err)
		}
	}
	var count int
	if err := d.DB().QueryRow(`SELECT count(*) FROM compressed WHERE source_id = ?`, tc.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("compressed rows = %d, want 1", count)
	}
}
