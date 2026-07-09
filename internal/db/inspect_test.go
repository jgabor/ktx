// Table-driven unit tests for internal/db/inspect.go.
//
// Coverage:
//
//   - CheckSchema: passes on a fresh migrated DB; missing column
//     surfaced as an error
//   - HasCostColumns: passes on a fresh DB; the missing-column branch
//     lists the absent column by name
//   - HasBenchRunsTable + HasBenchRunsColumns: passes on a fresh DB
//   - SampleIDs: returns canonical IDs; empty DB returns empty
//     slices without error
//
// The inspect helpers are read-only — `ktx doctor` relies on them
// for a structured health report without touching the database.
// Each probe is exercised independently so a regression in one
// surfaces as a single failing subtest, not a cascade.
package db

import (
	"strings"
	"testing"
)

// TestCheckSchema covers the core hard schema check: a freshly
// migrated DB has every expected column on sessions and tool_calls,
// and CheckSchema returns nil. The negative branch is also exercised:
// dropping a column surfaces a clear error naming the missing column.
func TestCheckSchema(t *testing.T) {
	t.Run("fresh DB: CheckSchema returns nil", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		if err := d.CheckSchema(); err != nil {
			t.Errorf("CheckSchema: %v (fresh migrated DB must satisfy the core schema)", err)
		}
	})

	t.Run("missing column surfaces a clear error", func(t *testing.T) {
		// Construct a degraded DB by dropping a known core column.
		// CheckSchema must report the missing column by name; the
		// doctor uses this signal to produce a degraded-status
		// line item.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		if _, err := d.db.Exec(`ALTER TABLE tool_calls DROP COLUMN tool`); err != nil {
			t.Fatalf("ALTER TABLE DROP COLUMN: %v", err)
		}

		err = d.CheckSchema()
		if err == nil {
			t.Fatal("CheckSchema returned nil after dropping tool_calls.tool")
		}
		if !strings.Contains(err.Error(), "tool") {
			t.Errorf("CheckSchema error %q does not name the dropped column 'tool'", err.Error())
		}
	})
}

// TestHasCostColumns covers the cost-publication soft check: a fresh DB reports
// (true, nil, nil); a DB missing one cost-publication column reports
// (false, [missing], nil) with the missing column named.
func TestHasCostColumns(t *testing.T) {
	t.Run("fresh DB: HasCostColumns reports true with no missing", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		ok, missing, err := d.HasCostColumns()
		if err != nil {
			t.Fatalf("HasCostColumns: %v", err)
		}
		if !ok {
			t.Errorf("HasCostColumns returned ok=false; want true (fresh DB has every cost-publication column)")
		}
		if len(missing) != 0 {
			t.Errorf("HasCostColumns returned missing=%v; want []", missing)
		}
	})

	t.Run("missing cost-publication column surfaces in the missing slice", func(t *testing.T) {
		// Drop one cost-publication column to simulate an unmigrated DB that was not
		// migrated (or a partial migration). HasCostColumns must
		// report the absent column so the doctor can name it in
		// the degraded-status detail.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Drop one cost-publication column. SQLite does not allow DROP COLUMN on
		// every column (it refuses indexes / primary keys), but
		// `tokens_in` is a plain nullable column with no index.
		if _, err := d.db.Exec(`ALTER TABLE tool_calls DROP COLUMN tokens_in`); err != nil {
			t.Skipf("DROP COLUMN tokens_in not supported by this SQLite build: %v", err)
		}

		ok, missing, err := d.HasCostColumns()
		if err != nil {
			t.Fatalf("HasCostColumns: %v", err)
		}
		if ok {
			t.Error("HasCostColumns returned ok=true after dropping tokens_in; want false")
		}
		// "tokens_in" must be in the missing slice; other columns
		// may or may not be (the test does not assert a complete
		// set — only that the dropped one is reported).
		found := false
		for _, m := range missing {
			if m == "tokens_in" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("HasCostColumns missing slice %v does not contain tokens_in", missing)
		}
	})
}

// TestHasBenchRunsTable covers the table-level probe. A fresh DB
// has the bench_runs table (migrate runs CREATE TABLE IF NOT EXISTS
// as part of OpenMemory); an unmigrated DB would not.
func TestHasBenchRunsTable(t *testing.T) {
	t.Run("fresh DB: HasBenchRunsTable reports true", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		ok, err := d.HasBenchRunsTable()
		if err != nil {
			t.Fatalf("HasBenchRunsTable: %v", err)
		}
		if !ok {
			t.Error("HasBenchRunsTable returned false; want true (fresh DB has bench_runs)")
		}
	})
}

// TestHasBenchRunsColumns covers the column-level probe on a fresh
// DB: every expected bench_runs column is present, missing slice
// is empty.
func TestHasBenchRunsColumns(t *testing.T) {
	t.Run("fresh DB: HasBenchRunsColumns reports no missing", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		missing, err := d.HasBenchRunsColumns()
		if err != nil {
			t.Fatalf("HasBenchRunsColumns: %v", err)
		}
		if len(missing) != 0 {
			t.Errorf("HasBenchRunsColumns missing=%v; want []", missing)
		}
	})
}

// TestSampleIDs covers the doctor ID-format probe: a populated DB
// returns IDs that match Decision 3 (^[a-z]{16}$); an empty DB
// returns empty slices without error so the doctor does not crash
// on a fresh install.
func TestSampleIDs(t *testing.T) {
	t.Run("populated DB: sampled IDs match ^[a-z]{16}$", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Seed one session and one tool_call; NewID produces
		// 16-char lowercase alpha by construction.
		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}
		tcID := mustNewID(t)
		if err := d.PersistToolCall(&ToolCall{
			ID: tcID, SessionID: sessionID, Tool: "Bash",
			Input: "in", Output: "out", Timestamp: "t",
		}); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}

		sessions, toolCalls, err := d.SampleIDs(10)
		if err != nil {
			t.Fatalf("SampleIDs: %v", err)
		}
		if len(sessions) != 1 || sessions[0] != sessionID {
			t.Errorf("sessions = %v, want [%q]", sessions, sessionID)
		}
		if len(toolCalls) != 1 || toolCalls[0] != tcID {
			t.Errorf("toolCalls = %v, want [%q]", toolCalls, tcID)
		}
		for _, id := range append(sessions, toolCalls...) {
			if !IDPattern.MatchString(id) {
				t.Errorf("sampled ID %q does not match %s", id, IDPattern)
			}
		}
	})

	t.Run("empty DB: SampleIDs returns empty slices, no error", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessions, toolCalls, err := d.SampleIDs(10)
		if err != nil {
			t.Fatalf("SampleIDs(empty DB): %v", err)
		}
		if len(sessions) != 0 {
			t.Errorf("sessions = %v; want []", sessions)
		}
		if len(toolCalls) != 0 {
			t.Errorf("toolCalls = %v; want []", toolCalls)
		}
	})

	t.Run("limit <= 0 returns empty slices", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Even with a populated DB, limit<=0 is a "no sampling"
		// request, not an error.
		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		for _, limit := range []int{0, -1, -100} {
			sessions, toolCalls, err := d.SampleIDs(limit)
			if err != nil {
				t.Errorf("SampleIDs(%d): %v", limit, err)
			}
			if len(sessions) != 0 || len(toolCalls) != 0 {
				t.Errorf("SampleIDs(%d) returned sessions=%v toolCalls=%v; want empty", limit, sessions, toolCalls)
			}
		}
	})
}

// TestIDPattern_Locked covers the IDPattern regex's lock-down: the
// regex must be exactly ^[a-z]{16}$ (anchored, lowercase alpha only,
// 16 chars). A regression that loosened the alphabet or length
// would compromise Decision 3's collision-safety claim.
func TestIDPattern_Locked(t *testing.T) {
	t.Run("positive cases match", func(t *testing.T) {
		for _, id := range []string{
			"abcdefghijklmnop",
			"zzzzzzzzzzzzzzzz",
			"qwertyuiopasdfgh",
		} {
			if !IDPattern.MatchString(id) {
				t.Errorf("IDPattern rejected %q; want match", id)
			}
		}
	})

	t.Run("negative cases rejected (anchored, exact length)", func(t *testing.T) {
		for _, id := range []string{
			"",                  // empty
			"abc",               // too short
			"abcdefghijklmnopq", // too long
			"ABCDEFGHIJKLMNOP",  // uppercase
			"abcdefghijklmno0",  // digit
			"abcdef ghijklmnop", // space
		} {
			if IDPattern.MatchString(id) {
				t.Errorf("IDPattern matched %q; want no match", id)
			}
		}
	})
}
