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
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
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

func TestBenchRunReasoningAndEstimatedCostRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	reasoning := int64(42)
	estimated := 0.001234
	reported := 0.0
	run := &BenchRun{ID: "estimated-roundtrip", Agent: "openai/gpt", Model: "gpt", Cost: &reported, Reasoning: &reasoning, EstimatedAPICost: &estimated}
	if err := database.InsertBenchRun(run); err != nil {
		t.Fatal(err)
	}
	got, err := database.QueryBenchRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cost == nil || *got.Cost != 0 || got.Reasoning == nil || *got.Reasoning != 42 || got.EstimatedAPICost == nil || *got.EstimatedAPICost != estimated {
		t.Fatalf("round trip = %#v", got)
	}
}

func TestBenchRunReportedZeroKeepsSeparateNonzeroEstimate(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	resetPricesForTest()
	t.Cleanup(resetPricesForTest)
	path, err := PricesPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	prices := `{"openai":{"gpt":{"input":2.5,"output":15,"cache_read":0.25,"cache_write":3.125}}}`
	if err := os.WriteFile(path, []byte(prices), 0o644); err != nil {
		t.Fatal(err)
	}

	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	reported := 0.0
	reasoning, cacheRead, cacheWrite := int64(20), int64(100), int64(10)
	run := &BenchRun{ID: "reported-zero", Agent: "openai/gpt", Model: "gpt", TokensIn: 1000, TokensOut: 50, Cost: &reported, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite}
	if err := database.InsertBenchRun(run); err != nil {
		t.Fatal(err)
	}
	if run.Cost == nil || *run.Cost != 0 || run.EstimatedAPICost == nil || *run.EstimatedAPICost != 0.003606 {
		t.Fatalf("run after insert = %#v", run)
	}
	partial := &BenchRun{ID: "partial-usage", Agent: "openai/gpt", Model: "gpt", TokensIn: 1000, TokensOut: 50, Cost: &reported, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite, SkipEstimatedAPICost: true}
	if err := database.InsertBenchRun(partial); err != nil {
		t.Fatal(err)
	}
	if partial.EstimatedAPICost != nil {
		t.Fatalf("partial usage estimate = %v, want nil", *partial.EstimatedAPICost)
	}
	missingReported := &BenchRun{ID: "missing-reported", Agent: "openai/gpt", Model: "gpt", TokensIn: 1000, TokensOut: 50, Reasoning: &reasoning, CacheRead: &cacheRead, CacheWrite: &cacheWrite}
	if err := database.InsertBenchRun(missingReported); err != nil {
		t.Fatal(err)
	}
	if missingReported.Cost != nil || missingReported.EstimatedAPICost == nil || *missingReported.EstimatedAPICost != 0.003606 {
		t.Fatalf("missing reported cost run = %#v", missingReported)
	}
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

// TestInsertBenchRun_CacheTelemetryRoundTrip covers the absent-vs-zero
// contract for the additive cache telemetry columns on bench_runs.
// CacheRead / CacheWrite are *int64 nullable pointers — a runtime that
// emits cache telemetry on step_finish produces non-nil pointers on the
// bench_runs row; a runtime that does not (or a pre-cache bench run)
// produces SQL NULL that round-trips to nil pointers.
func TestInsertBenchRun_CacheTelemetryRoundTrip(t *testing.T) {
	t.Run("populated cache telemetry round-trips verbatim", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		id := mustNewID(t)
		cacheRead := int64(15232)
		cacheWrite := int64(1500)
		b := &BenchRun{
			ID: id, Agent: "opencode", Model: "m", Task: "t", Score: 1,
			MethodologyVersion: "v1", Timestamp: 1718928000,
			CacheRead:  &cacheRead,
			CacheWrite: &cacheWrite,
		}
		if err := d.InsertBenchRun(b); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}
		got, err := d.QueryBenchRun(id)
		if err != nil {
			t.Fatalf("QueryBenchRun: %v", err)
		}
		if got.CacheRead == nil || *got.CacheRead != cacheRead {
			t.Errorf("CacheRead = %v, want pointer to %d", got.CacheRead, cacheRead)
		}
		if got.CacheWrite == nil || *got.CacheWrite != cacheWrite {
			t.Errorf("CacheWrite = %v, want pointer to %d", got.CacheWrite, cacheWrite)
		}
	})

	t.Run("emitted zero cache round-trips as 0 (not NULL)", func(t *testing.T) {
		// An explicitly emitted zero cache_read is a real
		// measurement (no cache hits on a cold prompt). The SQL
		// column is NULL only when the runtime did not emit
		// cache telemetry at all.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		id := mustNewID(t)
		zero := int64(0)
		b := &BenchRun{
			ID: id, Agent: "opencode", Model: "m", Task: "t", Score: 1,
			MethodologyVersion: "v1", Timestamp: 1718928000,
			CacheRead:  &zero,
			CacheWrite: &zero,
		}
		if err := d.InsertBenchRun(b); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}
		got, err := d.QueryBenchRun(id)
		if err != nil {
			t.Fatalf("QueryBenchRun: %v", err)
		}
		if got.CacheRead == nil || *got.CacheRead != 0 {
			t.Errorf("CacheRead = %v, want pointer to 0 (emitted zero preserved)", got.CacheRead)
		}
		if got.CacheWrite == nil || *got.CacheWrite != 0 {
			t.Errorf("CacheWrite = %v, want pointer to 0 (emitted zero preserved)", got.CacheWrite)
		}
	})

	t.Run("nil cache telemetry round-trips as NULL", func(t *testing.T) {
		// Pre-cache or cache-less run: CacheRead and CacheWrite
		// stay nil, the columns are NULL. The absent-vs-zero
		// distinction survives the round-trip.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		id := mustNewID(t)
		b := &BenchRun{
			ID: id, Agent: "opencode", Model: "m", Task: "t", Score: 1,
			MethodologyVersion: "v1", Timestamp: 1718928000,
		}
		if err := d.InsertBenchRun(b); err != nil {
			t.Fatalf("InsertBenchRun: %v", err)
		}
		got, err := d.QueryBenchRun(id)
		if err != nil {
			t.Fatalf("QueryBenchRun: %v", err)
		}
		if got.CacheRead != nil {
			t.Errorf("CacheRead = %v, want nil (cache telemetry absent)", *got.CacheRead)
		}
		if got.CacheWrite != nil {
			t.Errorf("CacheWrite = %v, want nil (cache telemetry absent)", *got.CacheWrite)
		}
	})

	t.Run("QueryBenchRuns surfaces cache telemetry on every row", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Two rows: one with cache telemetry, one without.
		zero := int64(0)
		populated := int64(7777)
		rows := []*BenchRun{
			{ID: "aaaaaaaaaaaaaaaa", Agent: "a", Model: "m", Task: "t", Score: 1, MethodologyVersion: "v1", Timestamp: 100, CacheRead: &populated, CacheWrite: &populated},
			{ID: "bbbbbbbbbbbbbbbb", Agent: "a", Model: "m", Task: "t", Score: 1, MethodologyVersion: "v1", Timestamp: 200, CacheRead: &zero, CacheWrite: &zero},
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
		if len(got) != 2 {
			t.Fatalf("got %d rows, want 2", len(got))
		}
		// Order: timestamp ASC. Row 0 = 100, cache = 7777. Row 1 = 200, cache = 0.
		if got[0].CacheRead == nil || *got[0].CacheRead != populated {
			t.Errorf("row[0].CacheRead = %v, want %d", got[0].CacheRead, populated)
		}
		if got[1].CacheRead == nil || *got[1].CacheRead != 0 {
			t.Errorf("row[1].CacheRead = %v, want 0 (emitted-zero preserved)", got[1].CacheRead)
		}
	})
}

// TestOpen_BenchRunsCacheMigration covers the contract: an older
// database at user_version=1 without the cache_read / cache_write
// columns picks them up on open (additive ALTER TABLE ADD COLUMN),
// existing rows have NULL for the cache columns, and a second migrate
// is a no-op. Mirrors TestOpen_RuntimeMigration's structure.
func TestOpen_BenchRunsCacheMigration(t *testing.T) {
	t.Run("user_version=1 db gains cache columns idempotently", func(t *testing.T) {
		path := t.TempDir() + "/pre-cache.db"
		raw, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw sqlite: %v", err)
		}
		t.Cleanup(func() { _ = raw.Close() })

		// Build a pre-cache bench_runs schema (the 10-column legacy shape)
		// and explicitly stamp user_version=1 so it simulates a real v1
		// database that must trigger the v1→v2 migration path.
		if _, err := raw.Exec(`CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			agent TEXT,
			started_at TEXT,
			ended_at TEXT,
			last_seen_at TEXT,
			tool_call_count INTEGER DEFAULT 0
		)`); err != nil {
			t.Fatalf("create sessions: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE bench_runs (
			id TEXT PRIMARY KEY,
			agent TEXT,
			model TEXT,
			task TEXT,
			score REAL,
			tokens_in INTEGER,
			tokens_out INTEGER,
			cost REAL,
			methodology_version TEXT,
			timestamp INTEGER
		)`); err != nil {
			t.Fatalf("create legacy bench_runs: %v", err)
		}
		legacyID := "cccccccccccccccc"
		if _, err := raw.Exec(
			`INSERT INTO bench_runs (id, agent, model, task, score, tokens_in, tokens_out, methodology_version, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			legacyID, "legacy-agent", "m", "t", 1.0, 0, 0, "v1", 100,
		); err != nil {
			t.Fatalf("seed legacy bench_run: %v", err)
		}
		if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
			t.Fatalf("set user_version=1: %v", err)
		}
		_ = raw.Close()

		d, err := Open(path)
		if err != nil {
			t.Fatalf("Open v1 db: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })

		// Verify schema version bumped to the current additive schema.
		var gotVersion int
		if err := d.db.QueryRow(`PRAGMA user_version`).Scan(&gotVersion); err != nil {
			t.Fatalf("read user_version: %v", err)
		}
		if gotVersion != 3 {
			t.Fatalf("user_version = %d, want 3 after migration", gotVersion)
		}

		for _, col := range []string{"cache_read", "cache_write", "reasoning", "estimated_api_cost"} {
			var present int
			if err := d.db.QueryRow(
				"SELECT COUNT(*) FROM pragma_table_info('bench_runs') WHERE name = ?",
				col,
			).Scan(&present); err != nil {
				t.Fatalf("pragma_table_info bench_runs.%s: %v", col, err)
			}
			if present != 1 {
				t.Fatalf("bench_runs column %q missing after cache migration", col)
			}
		}

		// Pre-cache rows carry NULL for the new columns and are queryable.
		got, err := d.QueryBenchRun(legacyID)
		if err != nil {
			t.Fatalf("QueryBenchRun legacy: %v", err)
		}
		if got.CacheRead != nil {
			t.Errorf("legacy CacheRead = %v, want nil (NULL preserved on existing row)", *got.CacheRead)
		}
		if got.CacheWrite != nil {
			t.Errorf("legacy CacheWrite = %v, want nil (NULL preserved on existing row)", *got.CacheWrite)
		}
		if got.Reasoning != nil || got.EstimatedAPICost != nil {
			t.Fatalf("legacy estimated fields = reasoning:%v estimated:%v, want nil", got.Reasoning, got.EstimatedAPICost)
		}

		// New rows with cache telemetry round-trip through the migrated table.
		cacheRead, cacheWrite := int64(15232), int64(1500)
		cost := 0.001
		cacheRun := &BenchRun{
			ID: mustNewID(t), Agent: "test-agent", Model: "m", Task: "t", Score: 0.5,
			TokensIn: 100, TokensOut: 10, Cost: &cost, MethodologyVersion: "v4-eval-matrix",
			Timestamp: 200, CacheRead: &cacheRead, CacheWrite: &cacheWrite,
		}
		if err := d.InsertBenchRun(cacheRun); err != nil {
			t.Fatalf("InsertBenchRun with cache: %v", err)
		}
		gotCache, err := d.QueryBenchRun(cacheRun.ID)
		if err != nil {
			t.Fatalf("QueryBenchRun cache: %v", err)
		}
		if gotCache.CacheRead == nil || *gotCache.CacheRead != cacheRead {
			t.Errorf("cache CacheRead = %v, want %d", gotCache.CacheRead, cacheRead)
		}
		if gotCache.CacheWrite == nil || *gotCache.CacheWrite != cacheWrite {
			t.Errorf("cache CacheWrite = %v, want %d", gotCache.CacheWrite, cacheWrite)
		}

		// QueryBenchRuns returns both rows (bench report path works).
		allRows, err := d.QueryBenchRuns()
		if err != nil {
			t.Fatalf("QueryBenchRuns: %v", err)
		}
		if len(allRows) != 2 {
			t.Fatalf("QueryBenchRuns returned %d rows, want 2", len(allRows))
		}

		// Re-running migrate is idempotent.
		if err := migrate(d.db); err != nil {
			t.Fatalf("second migrate: %v", err)
		}
		for _, col := range []string{"cache_read", "cache_write"} {
			var present int
			if err := d.db.QueryRow(
				"SELECT COUNT(*) FROM pragma_table_info('bench_runs') WHERE name = ?",
				col,
			).Scan(&present); err != nil {
				t.Fatalf("pragma_table_info bench_runs.%s after re-migrate: %v", col, err)
			}
			if present != 1 {
				t.Fatalf("column %q duplicated or missing after idempotent migrate", col)
			}
		}
	})
}

func TestOpen_BenchRunsUserVersion2To3Migration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	raw, err := sql.Open(sqliteDriver, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE bench_runs (
		id TEXT PRIMARY KEY, agent TEXT, model TEXT, task TEXT, score REAL,
		tokens_in INTEGER, tokens_out INTEGER, cost REAL,
		methodology_version TEXT, timestamp INTEGER,
		cache_read INTEGER, cache_write INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS migration_marker (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO migration_marker VALUES ('preserved')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version int
	if err := database.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var marker string
	if err := database.db.QueryRow(`SELECT value FROM migration_marker`).Scan(&marker); err != nil || marker != "preserved" {
		t.Fatalf("marker=%q err=%v", marker, err)
	}
}
