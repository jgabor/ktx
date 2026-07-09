// Table-driven unit tests for internal/db/rollup.go (SessionCostsRollups).
//
// Acceptance coverage:
//
//   - pointer discipline (nil for absent aggregates, non-nil pointer
//     to zero for present-but-zero aggregates) is preserved through
//     every edge case the inspect surface relies on:
//
//   - empty DB: no sessions, no tool_calls → empty result map
//
//   - mixed-NULL aggregates: a session whose tool_calls have a mix of
//     NULL and non-NULL cost-publication fields; SUM ignores NULL row contributions
//     so the aggregate is the sum of the non-NULL contributions
//
//   - zero aggregates: a session whose every contribution is non-NULL
//     but sums to 0 (e.g. tokens_in=0 + tokens_out=0). The pointer
//     must be non-nil AND dereference to 0 — the absent-vs-zero
//     distinction is the entire reason SessionCosts fields are pointers
//
//   - single partial call: a session with exactly one tool_call
//     carrying a non-NULL model but NULL tokens/cost. MIN(model) is
//     the only aggregate that is non-NULL; the session IS in the
//     result map (HAVING clause) with Tokens=nil, Cost=nil,
//     Model=&value
//
// The rollup query is:
//
//	SELECT session_id, SUM(tokens_in + tokens_out), SUM(cost), MIN(model)
//	FROM tool_calls GROUP BY session_id
//	HAVING SUM(tokens_in + tokens_out) IS NOT NULL
//	    OR SUM(cost) IS NOT NULL
//	    OR MIN(model) IS NOT NULL
//
// Per-row NULL arithmetic: tokens_in + tokens_out returns NULL when
// either operand is NULL, so a row with one NULL token contributes
// NULL to the per-row sum. SUM() ignores NULL operands in the
// aggregate. MIN(model) returns NULL only when every input is NULL;
// one non-NULL model yields a non-NULL MIN.
//
// The test uses OpenMemory + InsertSession + PersistToolCall to
// build the scenarios. PersistToolCall increments tool_call_count
// on the session and writes all 14 columns; the rollup query reads
// only the cost-publication columns, so the count column is
// irrelevant to the assertions.
package db

import "testing"

// TestSessionCostsRollups_PointerDiscipline covers the four edge cases
// that pin down the absent-vs-zero pointer contract on SessionCosts.
// Each subtest builds a session with controlled tool_call
// cost-publication field presence and asserts the resulting map's
// pointer discipline.
//
// The map is keyed by session_id; sessions absent from the map
// represent "no cost-publication data" per the rollup's HAVING
// clause. The
// pointer discipline on a present entry is: nil for "every
// contribution to this aggregate was NULL", non-nil pointer to 0
// for "contributions existed and summed to 0", non-nil pointer to
// a value otherwise.
func TestSessionCostsRollups_PointerDiscipline(t *testing.T) {
	t.Run("empty DB: no sessions → empty map", func(t *testing.T) {
		// No InsertSession, no PersistToolCall. The rollup query
		// runs against an empty tool_calls table; the result map
		// must be empty (zero rows in → zero rows out, never nil).
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		rolls, err := d.SessionCostsRollups()
		if err != nil {
			t.Fatalf("SessionCostsRollups: %v", err)
		}
		if len(rolls) != 0 {
			t.Errorf("rollup map size = %d, want 0 (empty DB)", len(rolls))
		}
	})

	t.Run("mixed-NULL aggregates: non-NULL contributions sum", func(t *testing.T) {
		// Three tool_calls in one session:
		//   row A: tokens_in=10, tokens_out=5,  cost=0.10, model="m1"
		//   row B: all cost-publication fields NULL  (absent)
		//   row C: tokens_in=20, tokens_out=NULL, cost=0.20, model=NULL
		//
		// Per-row tokens_in + tokens_out: A=15, B=NULL, C=NULL.
		// SUM ignores NULL operands in the aggregate → SUM = 15.
		// Per-row cost: A=0.10, B=NULL, C=0.20 → SUM = 0.30.
		// Per-row model: A="m1", B=NULL, C=NULL → MIN = "m1".
		//
		// Result: Tokens=&15, Cost=&0.30, Model=&"m1".
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID:         sessionID,
			Agent:      "test",
			StartedAt:  "2026-06-21T00:00:00Z",
			LastSeenAt: "2026-06-21T00:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		// PersistToolCall requires the FK to sessions; the session
		// row above satisfies it. Each call writes one tool_call.
		if err := d.PersistToolCall(&ToolCall{
			ID:        mustNewID(t),
			SessionID: sessionID,
			Tool:      "Bash", Input: "a", Output: "o",
			Timestamp: "2026-06-21T00:00:01Z",
			TokensIn:  ptrInt64(10), TokensOut: ptrInt64(5),
			Cost: ptrFloat64(0.10), Model: ptrString("m1"),
		}); err != nil {
			t.Fatalf("PersistToolCall A: %v", err)
		}
		if err := d.PersistToolCall(&ToolCall{
			ID:        mustNewID(t),
			SessionID: sessionID,
			Tool:      "Bash", Input: "b", Output: "o",
			Timestamp: "2026-06-21T00:00:02Z",
			// All cost-publication fields NULL.
		}); err != nil {
			t.Fatalf("PersistToolCall B: %v", err)
		}
		if err := d.PersistToolCall(&ToolCall{
			ID:        mustNewID(t),
			SessionID: sessionID,
			Tool:      "Bash", Input: "c", Output: "o",
			Timestamp: "2026-06-21T00:00:03Z",
			TokensIn:  ptrInt64(20),
			Cost:      ptrFloat64(0.20),
		}); err != nil {
			t.Fatalf("PersistToolCall C: %v", err)
		}

		rolls, err := d.SessionCostsRollups()
		if err != nil {
			t.Fatalf("SessionCostsRollups: %v", err)
		}
		roll, ok := rolls[sessionID]
		if !ok {
			t.Fatalf("rollup map missing session %q (HAVING clause should keep it)", sessionID)
		}
		if roll.Tokens == nil {
			t.Errorf("Tokens: nil, want pointer to 15 (SUM of non-NULL pairs)")
		} else if *roll.Tokens != 15 {
			t.Errorf("Tokens = %d, want 15", *roll.Tokens)
		}
		if roll.Cost == nil {
			t.Errorf("Cost: nil, want pointer to 0.30")
		} else if *roll.Cost < 0.299999 || *roll.Cost > 0.300001 {
			// Float tolerance: 0.10 + 0.20 in IEEE-754 is
			// 0.30000000000000004. The rollup does not apply
			// 6-decimal rounding (only PersistToolCall's
			// deriveCost does), so the raw aggregate is what
			// SQLite computes.
			t.Errorf("Cost = %v, want ~0.30", *roll.Cost)
		}
		if roll.Model == nil {
			t.Errorf("Model: nil, want pointer to %q", "m1")
		} else if *roll.Model != "m1" {
			t.Errorf("Model = %q, want %q", *roll.Model, "m1")
		}
	})

	t.Run("zero aggregates: non-nil pointer to zero, not nil", func(t *testing.T) {
		// One tool_call with tokens_in=0, tokens_out=0, cost=0, model="" (empty).
		//
		// Per-row tokens_in + tokens_out = 0 (NOT NULL).
		// SUM = 0 (NOT NULL).
		// Per-row cost = 0; SUM = 0.
		// Per-row model = "" (empty string, NOT NULL); MIN = "".
		//
		// Result: Tokens=&0 (NOT nil), Cost=&0 (NOT nil), Model=&"" (NOT nil).
		// The "absent-vs-zero" contract demands the pointer is non-nil;
		// a nil pointer would conflate "every contribution was NULL"
		// with "every contribution was 0".
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID:         sessionID,
			Agent:      "test",
			StartedAt:  "2026-06-21T00:00:00Z",
			LastSeenAt: "2026-06-21T00:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		if err := d.PersistToolCall(&ToolCall{
			ID:        mustNewID(t),
			SessionID: sessionID,
			Tool:      "Bash", Input: "in", Output: "out",
			Timestamp: "2026-06-21T00:00:01Z",
			TokensIn:  ptrInt64(0), TokensOut: ptrInt64(0),
			Cost:  ptrFloat64(0),
			Model: ptrString(""),
		}); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}

		rolls, err := d.SessionCostsRollups()
		if err != nil {
			t.Fatalf("SessionCostsRollups: %v", err)
		}
		roll, ok := rolls[sessionID]
		if !ok {
			t.Fatalf("rollup map missing session %q (HAVING should keep a zero-aggregate session)", sessionID)
		}
		if roll.Tokens == nil {
			t.Error("Tokens: nil, want pointer to 0 (present-but-zero)")
		} else if *roll.Tokens != 0 {
			t.Errorf("Tokens = %d, want 0", *roll.Tokens)
		}
		if roll.Cost == nil {
			t.Error("Cost: nil, want pointer to 0 (present-but-zero)")
		} else if *roll.Cost != 0 {
			t.Errorf("Cost = %v, want 0", *roll.Cost)
		}
		if roll.Model == nil {
			t.Error("Model: nil, want pointer to empty string (present-but-empty)")
		} else if *roll.Model != "" {
			t.Errorf("Model = %q, want \"\"", *roll.Model)
		}
	})

	t.Run("single partial call: model only, tokens/cost NULL", func(t *testing.T) {
		// One tool_call with model="gpt-4o" and every other
		// cost-publication field NULL. The aggregate values:
		//   SUM(tokens_in + tokens_out) = NULL
		//   SUM(cost) = NULL
		//   MIN(model) = "gpt-4o"
		// HAVING: MIN(model) IS NOT NULL → TRUE.
		// The session IS in the result map; the inspect surface
		// renders Tokens/Cost as "-" (nil → marker) and Model as
		// "gpt-4o".
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID:         sessionID,
			Agent:      "test",
			StartedAt:  "2026-06-21T00:00:00Z",
			LastSeenAt: "2026-06-21T00:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		if err := d.PersistToolCall(&ToolCall{
			ID:        mustNewID(t),
			SessionID: sessionID,
			Tool:      "Bash", Input: "in", Output: "out",
			Timestamp: "2026-06-21T00:00:01Z",
			// Only Model is set; TokensIn/TokensOut/Cost stay nil.
			// Provider is also nil so deriveCost is a no-op even
			// if a price is loaded — NULL preservation.
			Model: ptrString("gpt-4o"),
		}); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}

		rolls, err := d.SessionCostsRollups()
		if err != nil {
			t.Fatalf("SessionCostsRollups: %v", err)
		}
		roll, ok := rolls[sessionID]
		if !ok {
			t.Fatalf("rollup map missing session %q (MIN(model) should keep it)", sessionID)
		}
		if roll.Tokens != nil {
			t.Errorf("Tokens: non-nil (%v), want nil (no non-NULL token pair)", *roll.Tokens)
		}
		if roll.Cost != nil {
			t.Errorf("Cost: non-nil (%v), want nil (no non-NULL cost)", *roll.Cost)
		}
		if roll.Model == nil {
			t.Error("Model: nil, want pointer to \"gpt-4o\"")
		} else if *roll.Model != "gpt-4o" {
			t.Errorf("Model = %q, want \"gpt-4o\"", *roll.Model)
		}
	})

	t.Run("session with all-NULL cost-publication fields across every call is absent from map", func(t *testing.T) {
		// The negative side of the HAVING clause: a session whose
		// every tool_call has all cost-publication fields NULL must NOT appear
		// in the rollup map. The inspect surface treats a missing
		// key as "no cost-publication data" and renders every column as "-".
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID:         sessionID,
			Agent:      "test",
			StartedAt:  "2026-06-21T00:00:00Z",
			LastSeenAt: "2026-06-21T00:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		// Two tool_calls; both with every cost-publication field nil.
		for i := 0; i < 2; i++ {
			if err := d.PersistToolCall(&ToolCall{
				ID: mustNewID(t), SessionID: sessionID,
				Tool: "Bash", Input: "in", Output: "out",
				Timestamp: "2026-06-21T00:00:01Z",
			}); err != nil {
				t.Fatalf("PersistToolCall[%d]: %v", i, err)
			}
		}

		rolls, err := d.SessionCostsRollups()
		if err != nil {
			t.Fatalf("SessionCostsRollups: %v", err)
		}
		if _, ok := rolls[sessionID]; ok {
			t.Errorf("rollup map contains session %q; want absent (all-NULL cost-publication fields → HAVING excludes)", sessionID)
		}
	})
}
