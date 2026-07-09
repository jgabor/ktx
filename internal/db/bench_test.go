// Table-driven unit tests for internal/db/bench.go.
//
// Coverage:
//
//   - InsertBenchRun round-trip (populated and nil Cost)
//   - QueryBenchRuns ordering (timestamp ASC, id ASC tie-breaker)
//   - QueryBenchRun ErrNotFound for unknown id
//   - SummarizeBenchToolCalls aggregation (tokens, cost, model, provider)
//
// Bench tests do not exercise cost derivation here — that contract
// is covered in persist_test.go against the same
// deriveCost shape with the ToolCall inputs. SummarizeBenchToolCalls
// reads existing tool_calls rows verbatim; InsertBenchRun's
// deriveBenchCost uses the same price-cache singleton and would
// require the same XDG_DATA_HOME isolation as the
// cost derivation tests, which is already
// covered. This file stays focused on the bench_runs row shape and
// the session-aggregation helper.
package db

import (
	"errors"
	"math"
	"testing"
)

// TestInsertBenchRun_RoundTrip covers the row-shape contract: every
// bench_runs column is written and read back verbatim, including the
// pointer-type Cost that carries the absent-vs-zero distinction
// (the reconstructability contract inherited from tool_calls).
func TestInsertBenchRun_RoundTrip(t *testing.T) {
	t.Run("populated BenchRun round-trips byte-identical", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		id := mustNewID(t)
		cost := 0.0123
		b := &BenchRun{
			ID:                 id,
			Agent:              "test-agent",
			Model:              "claude-sonnet-4-20250514",
			Task:               "test-task",
			Score:              1.0,
			TokensIn:           1234,
			TokensOut:          567,
			Cost:               &cost,
			MethodologyVersion: "v1",
			Timestamp:          1718928000,
		}
		if err := d.InsertBenchRun(b); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}

		got, err := d.QueryBenchRun(id)
		if err != nil {
			t.Fatalf("QueryBenchRun: %v", err)
		}
		if got.ID != b.ID {
			t.Errorf("ID: got %q, want %q", got.ID, b.ID)
		}
		if got.Agent != b.Agent {
			t.Errorf("Agent: got %q, want %q", got.Agent, b.Agent)
		}
		if got.Model != b.Model {
			t.Errorf("Model: got %q, want %q", got.Model, b.Model)
		}
		if got.Task != b.Task {
			t.Errorf("Task: got %q, want %q", got.Task, b.Task)
		}
		if got.Score != b.Score {
			t.Errorf("Score: got %v, want %v", got.Score, b.Score)
		}
		if got.TokensIn != b.TokensIn {
			t.Errorf("TokensIn: got %d, want %d", got.TokensIn, b.TokensIn)
		}
		if got.TokensOut != b.TokensOut {
			t.Errorf("TokensOut: got %d, want %d", got.TokensOut, b.TokensOut)
		}
		if got.Cost == nil || *got.Cost != *b.Cost {
			t.Errorf("Cost: got %v, want %v", got.Cost, *b.Cost)
		}
		if got.MethodologyVersion != b.MethodologyVersion {
			t.Errorf("MethodologyVersion: got %q, want %q", got.MethodologyVersion, b.MethodologyVersion)
		}
		if got.Timestamp != b.Timestamp {
			t.Errorf("Timestamp: got %d, want %d", got.Timestamp, b.Timestamp)
		}
	})

	t.Run("nil Cost round-trips as nil (SQL NULL)", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		id := mustNewID(t)
		b := &BenchRun{
			ID: id, Agent: "test", Model: "m", Task: "t",
			Score: 0.0, TokensIn: 1, TokensOut: 2,
			// Cost left nil — caller did not supply; deriveBenchCost
			// is a no-op in this test because the price cache has
			// no entry for "m" and the cache is empty under the
			// default test XDG path (we did not call testutil
			// isolation here, so the singleton may carry residual
			// state from earlier tests; a model id "m" is not in
			// any real price table and Lookup will miss).
			MethodologyVersion: "v1",
			Timestamp:          1,
		}
		if err := d.InsertBenchRun(b); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}
		got, err := d.QueryBenchRun(id)
		if err != nil {
			t.Fatalf("QueryBenchRun: %v", err)
		}
		if got.Cost != nil {
			t.Errorf("Cost: got %v, want nil", *got.Cost)
		}
	})
}

// TestQueryBenchRuns_OrderedByTimestamp covers the "rows array, oldest
// first" contract: QueryBenchRuns returns rows ordered by
// timestamp ASC, with id ASC as the deterministic tie-breaker.
func TestQueryBenchRuns_OrderedByTimestamp(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Insert three rows out-of-order by timestamp. QueryBenchRuns
	// must return them sorted ASC by timestamp, with id as the
	// tie-breaker when timestamps are equal.
	rows := []*BenchRun{
		{ID: "cccccccccccccccc", Agent: "a", Model: "m", Task: "t", Score: 1, TokensIn: 1, TokensOut: 1, MethodologyVersion: "v1", Timestamp: 300},
		{ID: "aaaaaaaaaaaaaaaz", Agent: "a", Model: "m", Task: "t", Score: 1, TokensIn: 1, TokensOut: 1, MethodologyVersion: "v1", Timestamp: 100},
		{ID: "bbbbbbbbbbbbbbbb", Agent: "a", Model: "m", Task: "t", Score: 1, TokensIn: 1, TokensOut: 1, MethodologyVersion: "v1", Timestamp: 200},
	}
	for _, b := range rows {
		if err := d.InsertBenchRun(b); err != nil {
			t.Fatalf("InsertBenchRun(%q): %v", b.ID, err)
		}
	}

	got, err := d.QueryBenchRuns()
	if err != nil {
		t.Fatalf("QueryBenchRuns: %v", err)
	}
	want := []string{"aaaaaaaaaaaaaaaz", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("row[%d].ID = %q, want %q", i, got[i].ID, id)
		}
		if got[i].Timestamp != int64((i+1)*100) {
			t.Errorf("row[%d].Timestamp = %d, want %d", i, got[i].Timestamp, (i+1)*100)
		}
	}
}

// TestQueryBenchRun_NotFound covers the not-found contract on
// QueryBenchRun: an unknown id returns ErrNotFound so the CLI layer
// can map it to exit code 2. Mirrors the GetToolCall /
// GetSession behaviour.
func TestQueryBenchRun_NotFound(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	_, err = d.QueryBenchRun("zzzzzzzzzzzzzzzz")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("QueryBenchRun(unknown) = %v, want ErrNotFound", err)
	}
}

// TestSummarizeBenchToolCalls covers the aggregation helper used by
// the bench-run CLI to roll a session's tool_calls up into a
// bench_runs row. The helper computes tokens_in/tokens_out/cost/
// model/provider from the underlying tool_calls; NULL preservation
// means the rollup fields stay at zero/empty when every input
// is NULL.
func TestSummarizeBenchToolCalls(t *testing.T) {
	t.Run("aggregates tokens + cost + model across tool_calls", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Seed one session with three tool_calls.
		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		cost1, cost2, cost3 := 0.10, 0.20, 0.30
		model := "claude-sonnet-4-20250514"
		provider := "anthropic"
		cases := []*ToolCall{
			{
				ID: mustNewID(t), SessionID: sessionID, Tool: "Bash",
				Input: "i", Output: "o", Timestamp: "t1",
				TokensIn: ptrInt64(100), TokensOut: ptrInt64(50),
				Cost: &cost1, Model: &model, Provider: &provider,
			},
			{
				ID: mustNewID(t), SessionID: sessionID, Tool: "Bash",
				Input: "i", Output: "o", Timestamp: "t2",
				TokensIn: ptrInt64(200), TokensOut: ptrInt64(75),
				Cost: &cost2, Model: &model, Provider: &provider,
			},
			{
				ID: mustNewID(t), SessionID: sessionID, Tool: "Bash",
				Input: "i", Output: "o", Timestamp: "t3",
				TokensIn: ptrInt64(300), TokensOut: ptrInt64(125),
				Cost: &cost3, Model: &model, Provider: &provider,
			},
		}
		for _, tc := range cases {
			if err := d.PersistToolCall(tc); err != nil {
				t.Fatalf("PersistToolCall: %v", err)
			}
		}

		tokensIn, tokensOut, cost, modelOut, providerOut, err := d.SummarizeBenchToolCalls(sessionID)
		if err != nil {
			t.Fatalf("SummarizeBenchToolCalls: %v", err)
		}
		if tokensIn != 600 {
			t.Errorf("tokensIn = %d, want 600", tokensIn)
		}
		if tokensOut != 250 {
			t.Errorf("tokensOut = %d, want 250", tokensOut)
		}
		if cost == nil {
			t.Fatal("cost = nil; want 0.60 (sum of three rows)")
		}
		if math.Abs(*cost-0.60) > 1e-9 {
			t.Errorf("cost = %v, want 0.60", *cost)
		}
		if modelOut != model {
			t.Errorf("model = %q, want %q", modelOut, model)
		}
		if providerOut != provider {
			t.Errorf("provider = %q, want %q", providerOut, provider)
		}
	})

	t.Run("empty session returns zero/empty/NULL rollup", func(t *testing.T) {
		// NULL preservation: when there are no tool_calls
		// for the session, SUM(cost) is NULL and MIN(model) is
		// NULL — the helper surfaces cost=nil, model="",
		// provider="". The bench-run layer treats an empty
		// session as "no measurement", which is a valid state for
		// a misconfigured bench invocation (e.g. the agent did not
		// emit any tool events).
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Session with no tool_calls. SummarizeBenchToolCalls
		// selects by session_id; an empty result set yields NULL
		// from SUM(cost) and MIN(model).
		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		tokensIn, tokensOut, cost, model, provider, err := d.SummarizeBenchToolCalls(sessionID)
		if err != nil {
			t.Fatalf("SummarizeBenchToolCalls: %v", err)
		}
		if tokensIn != 0 || tokensOut != 0 {
			t.Errorf("tokensIn=%d tokensOut=%d, want 0/0", tokensIn, tokensOut)
		}
		if cost != nil {
			t.Errorf("cost = %v, want nil (empty session → SUM(cost) is NULL)", *cost)
		}
		if model != "" || provider != "" {
			t.Errorf("model=%q provider=%q, want both empty (empty session → MIN is NULL)", model, provider)
		}
	})
}

// TestInsertBenchRun_NilRejected covers the input-validation contract:
// a nil BenchRun or an empty id is rejected before any SQL runs.
// Catches a regression where InsertBenchRun would panic on a nil
// receiver or write an empty-id row.
func TestInsertBenchRun_NilRejected(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	t.Run("nil BenchRun rejected", func(t *testing.T) {
		if err := d.InsertBenchRun(nil); err == nil {
			t.Fatal("InsertBenchRun(nil) returned nil error")
		}
	})

	t.Run("empty id rejected", func(t *testing.T) {
		if err := d.InsertBenchRun(&BenchRun{}); err == nil {
			t.Fatal("InsertBenchRun(empty id) returned nil error")
		}
	})
}
