// Table-driven unit tests for internal/db/persist.go (Task 2).
//
// Acceptance coverage:
//
//   - AC 1 (NewID format + collision-free): TestNewID_MatchesAndUnique
//   - AC 2 (M2 pointer round-trip when set): TestPersistToolCall_M2RoundTrip
//   - AC 3 (nil M2 pointers → SQL NULL):    TestPersistToolCall_NilM2RoundTrip
//   - AC 6 (cost derivation at write time):  TestPersistToolCall_CostDerivation
//
// Test proportionality (plan Task 2): one pass + one fail per testable unit;
// ~20 test cases total. All subtests use stdlib testing only.
package db

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli/testutil"
)

// TestNewID_MatchesAndUnique covers AC 1: 1000 calls to NewID must each
// match ^[a-z]{16}$ (Decision 3 — 16-char lowercase alpha nanoid) and the
// resulting set must be collision-free. The entropy of a 26-letter
// alphabet at length 16 is ~75 bits; the collision probability over
// 1000 IDs is negligible (~1e-20), so a collision would indicate a
// broken implementation rather than chance.
func TestNewID_MatchesAndUnique(t *testing.T) {
	t.Run("1000 IDs match ^[a-z]{16}$ and are collision-free", func(t *testing.T) {
		const n = 1000
		seen := make(map[string]struct{}, n)
		for i := 0; i < n; i++ {
			id, err := NewID()
			if err != nil {
				t.Fatalf("NewID[%d]: %v", i, err)
			}
			if !IDPattern.MatchString(id) {
				t.Fatalf("NewID[%d] = %q does not match %s", i, id, IDPattern)
			}
			if _, dup := seen[id]; dup {
				t.Fatalf("NewID collision at iteration %d: %q already seen", i, id)
			}
			seen[id] = struct{}{}
		}
		if got := len(seen); got != n {
			t.Fatalf("unique ID count = %d, want %d", got, n)
		}
	})

	t.Run("pattern is anchored and rejects out-of-set characters", func(t *testing.T) {
		// Reject a small set of negative cases against IDPattern itself.
		// A non-anchored regex (e.g. [a-z]{16}) would also match "x"; an
		// unanchored ^...$ pair would still match if the regex were
		// mis-stripped. We assert the negative cases here to lock the
		// pattern down.
		for _, bad := range []string{
			"",                  // empty
			"abc",               // too short
			"abcdefghijklmnopq", // too long (17 chars)
			"ABCDEFGHIJKLMNOP",  // uppercase rejected
			"abcdefghijklmno0",  // digit rejected
			"abcdefghijklmno_",  // underscore rejected
			"abcdefghijklmn-o",  // hyphen rejected
			"abcdefghijklmnop ", // trailing space rejected (anchored)
			" abcdefghijklmnop", // leading space rejected (anchored)
		} {
			if IDPattern.MatchString(bad) {
				t.Errorf("IDPattern matched %q, want no match", bad)
			}
		}
	})
}

// TestPersistToolCall_M2RoundTrip covers AC 2: every M2 pointer field set
// on the input round-trips byte-identical through PersistToolCall +
// GetToolCall. "Byte-identical" means the re-fetched ToolCall carries
// non-nil pointers to the same values; the JSON marshaling is also
// compared to confirm the on-wire shape survives.
//
// The M2 fields use pointer types precisely to preserve the absent-vs-
// zero distinction (Decision 9 reconstructability contract). A non-nil
// pointer to 0 must persist as 0 (not NULL); a non-nil pointer to a
// non-zero value must persist verbatim.
func TestPersistToolCall_M2RoundTrip(t *testing.T) {
	t.Run("populated M2 pointers round-trip byte-identical", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		// Insert the parent session row so the FK on tool_calls is
		// satisfied (foreign_keys=1 is enforced per AC 1 of Task 1).
		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID:         sessionID,
			Agent:      "test-agent",
			StartedAt:  "2026-06-21T00:00:00Z",
			LastSeenAt: "2026-06-21T00:00:00Z",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		tcID := mustNewID(t)
		model := "claude-sonnet-4-20250514"
		provider := "anthropic"
		tc := &ToolCall{
			ID:          tcID,
			SessionID:   sessionID,
			Tool:        "Bash",
			Input:       `{"command":"ls"}`,
			Output:      "file1\nfile2\n",
			ContentHash: ContentHash("file1\nfile2\n"),
			Timestamp:   "2026-06-21T00:00:01Z",
			EventType:   "tool_call",
			// Non-nil pointers to non-zero values across the board.
			// Cost is set to a known value so the test also confirms
			// the caller-supplied cost wins (D11: deriveCost is a
			// no-op when tc.Cost != nil) and the cost cache is not
			// consulted for this row.
			TokensIn:  ptrInt64(1234),
			TokensOut: ptrInt64(567),
			Cost:      ptrFloat64(0.0123),
			LatencyMs: ptrInt64(890),
			Model:     &model,
			Provider:  &provider,
		}

		if err := d.PersistToolCall(tc); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}

		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}

		// Pointer-equality checks: every M2 pointer on got must be
		// non-nil AND equal to the value set on tc.
		if got.TokensIn == nil || *got.TokensIn != *tc.TokensIn {
			t.Errorf("TokensIn: got %v, want %v", got.TokensIn, *tc.TokensIn)
		}
		if got.TokensOut == nil || *got.TokensOut != *tc.TokensOut {
			t.Errorf("TokensOut: got %v, want %v", got.TokensOut, *tc.TokensOut)
		}
		if got.Cost == nil || *got.Cost != *tc.Cost {
			t.Errorf("Cost: got %v, want %v", got.Cost, *tc.Cost)
		}
		if got.LatencyMs == nil || *got.LatencyMs != *tc.LatencyMs {
			t.Errorf("LatencyMs: got %v, want %v", got.LatencyMs, *tc.LatencyMs)
		}
		if got.Model == nil || *got.Model != *tc.Model {
			t.Errorf("Model: got %v, want %v", got.Model, *tc.Model)
		}
		if got.Provider == nil || *got.Provider != *tc.Provider {
			t.Errorf("Provider: got %v, want %v", got.Provider, *tc.Provider)
		}

		// Byte-identical JSON: marshal both, compare strings. The
		// JSON shape is what ktx read / ktx get emit, so byte-equality
		// here is the user-visible round-trip.
		wantJSON, err := json.Marshal(tc)
		if err != nil {
			t.Fatalf("marshal input: %v", err)
		}
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal output: %v", err)
		}
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("JSON mismatch:\n want %s\n  got %s", wantJSON, gotJSON)
		}
	})

	t.Run("non-nil pointer to zero persists as 0 (not NULL)", func(t *testing.T) {
		// The absent-vs-zero distinction (Decision 9): a caller that
		// explicitly sets tokens_in=0 must observe 0 on read, not NULL.
		// Without pointer types, the zero value of int64 would coerce
		// 0 → NULL via the field default — the contract is precisely
		// that the pointer shape preserves the distinction.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		tcID := mustNewID(t)
		tc := &ToolCall{
			ID:        tcID,
			SessionID: sessionID,
			Tool:      "Bash",
			Input:     "in",
			Output:    "out",
			Timestamp: "2026-06-21T00:00:00Z",
			// Non-nil pointers to zero across the board.
			TokensIn:  ptrInt64(0),
			TokensOut: ptrInt64(0),
			Cost:      ptrFloat64(0),
			LatencyMs: ptrInt64(0),
		}
		if err := d.PersistToolCall(tc); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}
		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		for name, ptr := range map[string]*int64{
			"TokensIn":  got.TokensIn,
			"TokensOut": got.TokensOut,
			"LatencyMs": got.LatencyMs,
		} {
			if ptr == nil {
				t.Errorf("%s: nil on read, want pointer to 0", name)
				continue
			}
			if *ptr != 0 {
				t.Errorf("%s: got %d, want 0", name, *ptr)
			}
		}
		if got.Cost == nil || *got.Cost != 0 {
			t.Errorf("Cost: got %v, want pointer to 0", got.Cost)
		}
	})
}

// TestPersistToolCall_NilM2RoundTrip covers AC 3: when the M2 pointers
// on the input are nil, the persisted columns read back as SQL NULL
// (the re-fetched ToolCall has nil pointers for those fields).
//
// The absent-vs-zero distinction is the same contract from AC 2 but
// seen from the opposite direction: nil pointers must NOT be coerced
// to zero on read. A test that round-trips nil and observes 0 would
// be indistinguishable from "caller supplied zero" — the pointer
// shape is what makes the two cases observable downstream.
func TestPersistToolCall_NilM2RoundTrip(t *testing.T) {
	t.Run("nil M2 pointers round-trip as nil (SQL NULL)", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		tcID := mustNewID(t)
		tc := &ToolCall{
			ID:        tcID,
			SessionID: sessionID,
			Tool:      "Bash",
			Input:     "in",
			Output:    "out",
			Timestamp: "2026-06-21T00:00:00Z",
			// All M2 pointers left nil — caller did not supply.
		}
		if err := d.PersistToolCall(tc); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}

		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		if got.TokensIn != nil {
			t.Errorf("TokensIn: got %v, want nil", *got.TokensIn)
		}
		if got.TokensOut != nil {
			t.Errorf("TokensOut: got %v, want nil", *got.TokensOut)
		}
		if got.Cost != nil {
			t.Errorf("Cost: got %v, want nil", *got.Cost)
		}
		if got.LatencyMs != nil {
			t.Errorf("LatencyMs: got %v, want nil", *got.LatencyMs)
		}
		if got.Model != nil {
			t.Errorf("Model: got %v, want nil", *got.Model)
		}
		if got.Provider != nil {
			t.Errorf("Provider: got %v, want nil", *got.Provider)
		}
	})

	t.Run("nil M2 fields marshal to JSON null (no omitempty)", func(t *testing.T) {
		// The JSON tags deliberately omit `omitempty` (Decision 9
		// round-trip contract) so a nil M2 field appears as JSON
		// `null` rather than being dropped. A reader must be able
		// to detect 'absent' from 'set to zero' without
		// inspecting the schema — `null` vs `0` is the wire signal.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		tcID := mustNewID(t)
		tc := &ToolCall{
			ID: tcID, SessionID: sessionID, Tool: "Bash",
			Input: "in", Output: "out", Timestamp: "t",
		}
		if err := d.PersistToolCall(tc); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}
		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// Each M2 key must appear with literal `null`. A missing
		// key (omitempty) would fail this check.
		for _, key := range []string{
			`"tokens_in":null`,
			`"tokens_out":null`,
			`"cost":null`,
			`"latency_ms":null`,
			`"model":null`,
			`"provider":null`,
		} {
			if !strings.Contains(string(data), key) {
				t.Errorf("JSON missing %s; full output: %s", key, data)
			}
		}
	})
}

// TestPersistToolCall_CostDerivation covers AC 6: when Cost is nil
// and tokens+model+provider are all supplied AND the price cache has
// a price for (provider, model), PersistToolCall fills Cost from the
// D11 formula:
//
//	cost = (tokens_in * in_price + tokens_out * out_price) / 1_000_000
//
// rounded to 6 decimal places. With TokensIn=TokensOut=1_000_000 and
// (anthropic, claude-sonnet-4-20250514) priced at 3.0/15.0 USD per
// million tokens, the persisted Cost must be $18.000000.
//
// The cache is loaded via the singleton LoadPrices (PersistToolCall
// uses LoadPrices internally), with WithHTTPClient injected to point
// at an httptest.NewServer. The seam is the Task 1 hook; this test
// is the AC 6 verification that the seam hooks into the write path.
func TestPersistToolCall_CostDerivation(t *testing.T) {
	t.Run("Cost=nil + tokens+model+provider → derived 18.000000", func(t *testing.T) {
		// Isolate the Prices singleton: resetPricesForTest ensures
		// LoadPrices re-runs against the per-test XDG_DATA_HOME set
		// below, and t.Cleanup resets the singleton after the test so
		// subsequent tests do not see the injected client.
		resetPricesForTest()
		t.Cleanup(resetPricesForTest)

		tmp := testutil.WithTempXDG(t)

		// models.dev-shaped response: the schema is
		// map[provider]modelsDevProvider (which carries a "models"
		// map of modelsDevModel (which carries an optional "cost"
		// struct with input/output). The shape is what Refresh's
		// parser expects; the AC 6 description shows a simpler
		// projection for human consumption, but the wire format
		// matches models.dev.
		const body = `{"anthropic":{"models":{"claude-sonnet-4-20250514":{"cost":{"input":3.0,"output":15.0}}}}}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)

		// Prime the singleton with a Prices bound to the test XDG path
		// (file does not exist → cold=true). Inject the test server as
		// the httpClient before PersistToolCall triggers Refresh().
		p, err := LoadPrices()
		if err != nil {
			t.Fatalf("LoadPrices: %v", err)
		}
		if p == nil {
			t.Fatal("LoadPrices returned nil *Prices")
		}
		if !p.Cold() {
			t.Fatalf("Cold() = false after first LoadPrices against empty XDG cache file")
		}
		// Sanity: the cache file path must live under the test XDG_DATA_HOME.
		wantPrefix := filepath.Join(tmp, "share") + string(filepath.Separator)
		if !strings.HasPrefix(p.Path(), wantPrefix) {
			t.Errorf("Prices path %q does not live under XDG_DATA_HOME %q", p.Path(), wantPrefix)
		}
		p.WithHTTPClient(&http.Client{Transport: redirectTransport{target: srv.URL}})

		// Open an in-memory ktx DB. OpenMemory is the Task 1 seam.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		tcID := mustNewID(t)
		model := "claude-sonnet-4-20250514"
		provider := "anthropic"
		tc := &ToolCall{
			ID:        tcID,
			SessionID: sessionID,
			Tool:      "Bash",
			Input:     "in",
			Output:    "out",
			Timestamp: "2026-06-21T00:00:00Z",
			// Cost left nil on purpose — D11 derives.
			TokensIn:  ptrInt64(1_000_000),
			TokensOut: ptrInt64(1_000_000),
			Model:     &model,
			Provider:  &provider,
		}
		if err := d.PersistToolCall(tc); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}

		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		if got.Cost == nil {
			t.Fatal("Cost: nil after PersistToolCall; expected derived value")
		}
		// AC 6: expected 18.000000 USD, rounded to 6 decimals.
		const want = 18.0
		if math.Abs(*got.Cost-want) > 1e-9 {
			t.Errorf("Cost = %v, want %v (6-decimal precision)", *got.Cost, want)
		}
		// Round-trip the 6-decimal precision explicitly: any value
		// below 1e-6 difference from a 6-decimal-rounded value is OK.
		rounded := math.Round(*got.Cost*1e6) / 1e6
		if rounded != *got.Cost {
			t.Errorf("Cost = %v is not 6-decimal precise (would round to %v)", *got.Cost, rounded)
		}
	})

	t.Run("Cost derivation is a no-op when Cost is supplied", func(t *testing.T) {
		// D11 contract: caller-supplied cost wins. The cache is not
		// consulted; the persisted value is exactly what the caller
		// passed. The test seeds a cold cache with an httptest server
		// returning a price, then persists a ToolCall with a non-nil
		// Cost pointer to a known value. The persisted Cost must equal
		// the caller-supplied value, not the derived 18.0.
		resetPricesForTest()
		t.Cleanup(resetPricesForTest)

		testutil.WithTempXDG(t)

		const body = `{"anthropic":{"models":{"claude-sonnet-4-20250514":{"cost":{"input":3.0,"output":15.0}}}}}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)

		p, _ := LoadPrices()
		p.WithHTTPClient(&http.Client{Transport: redirectTransport{target: srv.URL}})

		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		tcID := mustNewID(t)
		model := "claude-sonnet-4-20250514"
		provider := "anthropic"
		const callerCost = 0.0001
		tc := &ToolCall{
			ID: tcID, SessionID: sessionID, Tool: "Bash",
			Input: "in", Output: "out", Timestamp: "t",
			TokensIn:  ptrInt64(1_000_000),
			TokensOut: ptrInt64(1_000_000),
			Cost:      ptrFloat64(callerCost),
			Model:     &model,
			Provider:  &provider,
		}
		if err := d.PersistToolCall(tc); err != nil {
			t.Fatalf("PersistToolCall: %v", err)
		}
		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		if got.Cost == nil {
			t.Fatal("Cost: nil; caller supplied a value")
		}
		if *got.Cost != callerCost {
			t.Errorf("Cost = %v, want caller-supplied %v", *got.Cost, callerCost)
		}
	})

	t.Run("Cost derivation is a no-op when ANY of tokens/model/provider is nil", func(t *testing.T) {
		// D11 NULL preservation (AC7 of D11): any of tokens_in /
		// tokens_out / model NULL blocks derivation even when the
		// cache has a price. The contract is conservative failure —
		// ktx does not invent the missing value.
		resetPricesForTest()
		t.Cleanup(resetPricesForTest)

		testutil.WithTempXDG(t)

		const body = `{"anthropic":{"models":{"claude-sonnet-4-20250514":{"cost":{"input":3.0,"output":15.0}}}}}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)

		p, _ := LoadPrices()
		p.WithHTTPClient(&http.Client{Transport: redirectTransport{target: srv.URL}})

		// Three rows, each missing one derivation input. Cost stays
		// NULL on every row; the cache price is never consulted
		// (the cache has the price, but D11 short-circuits on NULL).
		cases := []struct {
			name string
			tc   *ToolCall
		}{
			{
				name: "tokens_in nil",
				tc: &ToolCall{
					Tool: "Bash", Input: "in", Output: "out", Timestamp: "t",
					TokensOut: ptrInt64(1_000_000),
					Model:     ptrString("claude-sonnet-4-20250514"),
					Provider:  ptrString("anthropic"),
				},
			},
			{
				name: "tokens_out nil",
				tc: &ToolCall{
					Tool: "Bash", Input: "in", Output: "out", Timestamp: "t",
					TokensIn: ptrInt64(1_000_000),
					Model:    ptrString("claude-sonnet-4-20250514"),
					Provider: ptrString("anthropic"),
				},
			},
			{
				name: "model nil",
				tc: &ToolCall{
					Tool: "Bash", Input: "in", Output: "out", Timestamp: "t",
					TokensIn:  ptrInt64(1_000_000),
					TokensOut: ptrInt64(1_000_000),
					Provider:  ptrString("anthropic"),
				},
			},
			{
				name: "provider nil",
				tc: &ToolCall{
					Tool: "Bash", Input: "in", Output: "out", Timestamp: "t",
					TokensIn:  ptrInt64(1_000_000),
					TokensOut: ptrInt64(1_000_000),
					Model:     ptrString("claude-sonnet-4-20250514"),
				},
			},
		}

		for _, c := range cases {
			c := c
			t.Run(c.name, func(t *testing.T) {
				d, err := OpenMemory()
				if err != nil {
					t.Fatalf("OpenMemory: %v", err)
				}
				t.Cleanup(func() { d.Close() })

				sessionID := mustNewID(t)
				if err := d.InsertSession(&Session{
					ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
				}); err != nil {
					t.Fatalf("InsertSession: %v", err)
				}
				c.tc.ID = mustNewID(t)
				c.tc.SessionID = sessionID

				if err := d.PersistToolCall(c.tc); err != nil {
					t.Fatalf("PersistToolCall: %v", err)
				}
				got, err := d.GetToolCall(c.tc.ID)
				if err != nil {
					t.Fatalf("GetToolCall: %v", err)
				}
				if got.Cost != nil {
					t.Errorf("Cost = %v, want nil (NULL preservation)", *got.Cost)
				}
			})
		}
	})

	t.Run("Cost derivation soft-fails on cache miss + fetch failure", func(t *testing.T) {
		// D11 AC3 + AC6: when the cache cannot supply a price AND the
		// fetch fails, Cost stays NULL and no error is returned from
		// PersistToolCall. The httptest server returns 500; Refresh
		// errors; deriveCost short-circuits.
		resetPricesForTest()
		t.Cleanup(resetPricesForTest)

		testutil.WithTempXDG(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		p, _ := LoadPrices()
		p.WithHTTPClient(&http.Client{Transport: redirectTransport{target: srv.URL}})

		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		sessionID := mustNewID(t)
		if err := d.InsertSession(&Session{
			ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
		}); err != nil {
			t.Fatalf("InsertSession: %v", err)
		}

		tcID := mustNewID(t)
		tc := &ToolCall{
			ID: tcID, SessionID: sessionID, Tool: "Bash",
			Input: "in", Output: "out", Timestamp: "t",
			TokensIn:  ptrInt64(1_000_000),
			TokensOut: ptrInt64(1_000_000),
			Model:     ptrString("claude-sonnet-4-20250514"),
			Provider:  ptrString("anthropic"),
		}
		// Soft-fail: PersistToolCall returns nil despite the price
		// cache being empty. The row is still written.
		if err := d.PersistToolCall(tc); err != nil {
			t.Fatalf("PersistToolCall: %v (should soft-fail)", err)
		}
		got, err := d.GetToolCall(tcID)
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		if got.Cost != nil {
			t.Errorf("Cost = %v, want nil (fetch failure → soft-fail)", *got.Cost)
		}
	})
}

// TestGetRecord_ToolCallVsSession is a sanity check that GetRecord
// covers both D7 tables: a tool_call id resolves to a *ToolCall,
// a session id resolves to a *Session, and an unknown id returns
// ErrNotFound. This is not in the AC list but is a natural unit
// to exercise alongside the round-trip tests.
func TestGetRecord_ToolCallVsSession(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	sessionID := mustNewID(t)
	if err := d.InsertSession(&Session{
		ID: sessionID, Agent: "test", StartedAt: "t", LastSeenAt: "t",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	tcID := mustNewID(t)
	tc := &ToolCall{
		ID: tcID, SessionID: sessionID, Tool: "Bash",
		Input: "in", Output: "out", Timestamp: "t",
	}
	if err := d.PersistToolCall(tc); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}

	t.Run("tool_call id resolves to *ToolCall", func(t *testing.T) {
		rec, err := d.GetRecord(tcID)
		if err != nil {
			t.Fatalf("GetRecord: %v", err)
		}
		if _, ok := rec.(*ToolCall); !ok {
			t.Fatalf("GetRecord(%q) returned %T, want *ToolCall", tcID, rec)
		}
	})

	t.Run("session id resolves to *Session", func(t *testing.T) {
		rec, err := d.GetRecord(sessionID)
		if err != nil {
			t.Fatalf("GetRecord: %v", err)
		}
		if _, ok := rec.(*Session); !ok {
			t.Fatalf("GetRecord(%q) returned %T, want *Session", sessionID, rec)
		}
	})

	t.Run("unknown id returns ErrNotFound", func(t *testing.T) {
		_, err := d.GetRecord("zzzzzzzzzzzzzzzz")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("GetRecord(unknown) = %v, want ErrNotFound", err)
		}
	})
}

// TestPersistMethods_EdgeCases covers AC4 of the cleanup plan: five
// method-level tests on the public *DB surface, each pinning down
// the documented contract on a single edge case. The tests are
// deliberately minimal (one assertion per case) so a regression in
// any one method surfaces as a single failing subtest rather than a
// cascade.
//
//   - SessionExists(""): the empty-string id is treated as "absent"
//     so the caller's hint-path branch can short-circuit on empty
//     input without a separate guard (persist.go:84-87).
//   - InsertSession duplicate primary key: PRIMARY KEY collision
//     surfaces as an error from the underlying SQL driver; the
//     caller relies on this to detect double-open on the same
//     session id.
//   - GetToolCall("missing"): returns (nil, ErrNotFound) so the
//     CLI layer can map "not found" to the documented exit code 2.
//   - GetSession("missing"): same contract as GetToolCall.
//   - ListSessions() on empty DB: returns a non-nil empty slice so
//     the CLI can json-encode "[]" without a nil check.
//
// The five cases are pinned in the plan (overall_acceptance, AC4);
// no fixtures are shared between them — each is a fresh OpenMemory
// so the test order does not matter.
func TestPersistMethods_EdgeCases(t *testing.T) {
	t.Run("SessionExists(\"\") returns false, nil", func(t *testing.T) {
		// The empty-id branch is documented at persist.go:84-87:
		// an empty id is treated as "absent" so the caller-provided
		// hint path can distinguish "absent" from "present" without
		// a separate guard. The contract is (false, nil); a nil
		// error distinguishes "empty input, no DB hit" from a
		// genuine query failure (which would be a non-nil error).
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		ok, err := d.SessionExists("")
		if err != nil {
			t.Errorf("SessionExists(\"\"): err = %v, want nil", err)
		}
		if ok {
			t.Error("SessionExists(\"\"): ok = true, want false")
		}
	})

	t.Run("InsertSession with duplicate primary key returns a constraint error", func(t *testing.T) {
		// The PRIMARY KEY collision must surface as an error from
		// the underlying SQL driver (not silently overwrite). The
		// exact error string varies between SQLite builds; the
		// contract is "non-nil error". A regression that swallowed
		// the error would let a double-open on the same session id
		// silently create a second row, corrupting the aggregate.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		s := &Session{
			ID:         "duplicate-id-test",
			Agent:      "test",
			StartedAt:  "t",
			LastSeenAt: "t",
		}
		if err := d.InsertSession(s); err != nil {
			t.Fatalf("first InsertSession: %v", err)
		}
		// Second insert with the same id — must fail.
		if err := d.InsertSession(s); err == nil {
			t.Fatal("second InsertSession with duplicate id: nil error, want constraint violation")
		}
	})

	t.Run("GetToolCall(\"missing\") returns nil, ErrNotFound", func(t *testing.T) {
		// The CLI layer (ktx get / ktx read) maps ErrNotFound to
		// exit code 2 (D4). The contract is (nil, ErrNotFound); a
		// different error would force the CLI layer to special-case
		// "not found" off the SQL driver's ErrNoRows — a layering
		// violation that hides the contract from the CLI.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		got, err := d.GetToolCall("missing")
		if got != nil {
			t.Errorf("GetToolCall(\"missing\"): tc = %v, want nil", got)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("GetToolCall(\"missing\"): err = %v, want ErrNotFound", err)
		}
	})

	t.Run("GetSession(\"missing\") returns nil, ErrNotFound", func(t *testing.T) {
		// Same contract as GetToolCall: (nil, ErrNotFound). The
		// CLI layer's GetRecord composes both methods and relies on
		// ErrNotFound propagating through the composition so the
		// "no such record" envelope maps to the right exit code.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		got, err := d.GetSession("missing")
		if got != nil {
			t.Errorf("GetSession(\"missing\"): s = %v, want nil", got)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSession(\"missing\"): err = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListSessions() on empty DB returns non-nil empty slice, nil error", func(t *testing.T) {
		// The contract (persist.go:324-329): a non-nil empty slice
		// so the CLI can json-encode "[]" without a nil check
		// (json.Marshal on a nil slice emits "null"; a non-nil
		// empty slice emits "[]"). A regression that returned
		// (nil, nil) would surface as `"sessions": null` in the
		// CLI output instead of `"sessions": []`, breaking the
		// array-vs-null contract downstream consumers expect.
		d, err := OpenMemory()
		if err != nil {
			t.Fatalf("OpenMemory: %v", err)
		}
		t.Cleanup(func() { d.Close() })

		got, err := d.ListSessions()
		if err != nil {
			t.Errorf("ListSessions: err = %v, want nil", err)
		}
		if got == nil {
			t.Error("ListSessions: slice = nil, want non-nil empty slice")
		}
		if len(got) != 0 {
			t.Errorf("ListSessions: len = %d, want 0", len(got))
		}
		// json.Marshal on the slice must produce "[]" not "null".
		// This is the user-visible contract the empty-slice return
		// exists to preserve.
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(data) != "[]" {
			t.Errorf("ListSessions: JSON = %s, want \"[]\"", data)
		}
	})
}

// --- test helpers ---

// mustNewID is a t.Helper wrapper around NewID that calls t.Fatal on
// error. Keeps test bodies free of `if err != nil { t.Fatalf(...) }`
// for the trivial "give me a fresh ID" case.
func mustNewID(t *testing.T) string {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}

// ptrInt64 returns a pointer to v. Used for filling the M2 pointer
// fields concisely in test inputs.
func ptrInt64(v int64) *int64 { return &v }

// ptrFloat64 returns a pointer to v.
func ptrFloat64(v float64) *float64 { return &v }

// ptrString returns a pointer to v.
func ptrString(v string) *string { return &v }
