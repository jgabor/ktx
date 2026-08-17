package db

import (
	"errors"
	"fmt"
	"strings"
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
	winners := make(chan Producer, deliveries)
	errs := make(chan error, deliveries)
	var wg sync.WaitGroup
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			runtime := "codex"
			producer := ProducerHook
			if i%2 != 0 {
				producer = ProducerRun
			}
			tc := &ToolCall{
				ID: "abcdefghijklmnop", SessionID: "session", Tool: "exec",
				Input: `{"cmd":"go test ./..."}`, Output: "ok\n", Timestamp: "2026-07-12T10:00:01Z",
				ContentHash: ContentHash("ok\n"), Runtime: &runtime,
				Producer: &producer,
			}
			inserted, err := d.PersistToolCallOnce(tc)
			results <- inserted
			if inserted {
				winners <- producer
			}
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(winners)
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
	winner := <-winners
	stored, err := d.GetToolCall("abcdefghijklmnop")
	if err != nil || stored.Producer == nil || *stored.Producer != winner {
		t.Fatalf("stored producer = %v, %v; want %q", stored.Producer, err, winner)
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

func TestPersistToolCallOnceDoesNotBackfillProducer(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.InsertSession(&Session{ID: "session", Agent: "test", StartedAt: "2026-08-17T10:00:00Z", LastSeenAt: "2026-08-17T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	tc := &ToolCall{ID: "abcdefghijklmnop", SessionID: "session", Tool: "exec", Input: "same", Output: "same", Timestamp: "2026-08-17T10:00:01Z"}
	if inserted, err := d.PersistToolCallOnce(tc); err != nil || !inserted {
		t.Fatalf("first insert = %t, %v", inserted, err)
	}
	producer := ProducerHook
	tc.Producer = &producer
	if inserted, err := d.PersistToolCallOnce(tc); err != nil || inserted {
		t.Fatalf("retry insert = %t, %v", inserted, err)
	}
	stored, err := d.GetToolCall(tc.ID)
	if err != nil || stored.Producer != nil {
		t.Fatalf("stored producer = %v, %v; want nil", stored.Producer, err)
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
			producer := ProducerHook
			if i%2 != 0 {
				producer = ProducerCompress
			}
			candidate := &Compressed{ID: fmt.Sprintf("artifact%08d", i), SourceID: tc.ID, Output: "short", ContentHash: ContentHash("short"), Pipeline: `["head:1"]`, Timestamp: "2026-07-12T10:00:02Z", Producer: &producer}
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
	stored, err := d.GetCompressed(winner)
	if err != nil || stored.Producer == nil {
		t.Fatalf("stored producer = %v, %v", stored, err)
	}
	winnerIndex := strings.TrimLeft(strings.TrimPrefix(winner, "artifact"), "0")
	var index int
	if winnerIndex != "" {
		if _, err := fmt.Sscan(winnerIndex, &index); err != nil {
			t.Fatal(err)
		}
	}
	wantProducer := ProducerHook
	if index%2 != 0 {
		wantProducer = ProducerCompress
	}
	if *stored.Producer != wantProducer {
		t.Errorf("stored producer = %q, want %q", *stored.Producer, wantProducer)
	}
}

func TestInsertCompressedOnceDoesNotBackfillProducer(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.InsertSession(&Session{ID: "session", Agent: "test", StartedAt: "2026-08-17T10:00:00Z", LastSeenAt: "2026-08-17T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	tc := &ToolCall{ID: "abcdefghijklmnop", SessionID: "session", Tool: "exec", Output: "original", Timestamp: "2026-08-17T10:00:01Z"}
	if err := d.PersistToolCall(tc); err != nil {
		t.Fatal(err)
	}
	first := &Compressed{ID: "firstartifactxxx", SourceID: tc.ID, Output: "short", Timestamp: "2026-08-17T10:00:02Z"}
	if stored, inserted, err := d.InsertCompressedOnce(first); err != nil || !inserted || stored.ID != first.ID {
		t.Fatalf("first artifact = %+v, %t, %v", stored, inserted, err)
	}
	producer := ProducerCompress
	retry := &Compressed{ID: "retryartifactxxx", SourceID: tc.ID, Output: "short", Timestamp: "2026-08-17T10:00:03Z", Producer: &producer}
	stored, inserted, err := d.InsertCompressedOnce(retry)
	if err != nil || inserted || stored.ID != first.ID || stored.Producer != nil {
		t.Fatalf("retry artifact = %+v, %t, %v; want first unknown producer", stored, inserted, err)
	}
}
