// Persistence methods for the ktx database (Decision 7).
//
// Two-table model:
//   - tool_calls: append-only. INSERT only (PersistToolCall).
//   - sessions: aggregate. INSERT on open (InsertSession), UPDATE on
//     roll-up and close (PersistToolCall's tx, CloseSession).
//
// The ToolCall struct carries the full D9 flat JSON shape, including the
// optional M2 fields (tokens_in, tokens_out, cost, latency_ms, model,
// provider). The M2 columns live in tool_calls since the in-place ALTER
// TABLE migration in Open() (Task 2). PersistToolCall writes them; a nil
// pointer maps to SQL NULL so 'field absent' survives the round-trip;
// a non-nil pointer to zero persists as 0 so 'field set to zero' is not
// conflated with 'absent' (Decision 9 reconstructability requirement).
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// ErrNotFound is returned when a referenced session or record does not exist
// in the database. The cli layer maps it to a not-found exit code (D4).
var ErrNotFound = errors.New("store: not found")

// ToolCall is the flat JSON tool_call payload (Decision 9).
//
// Required fields are persisted to the tool_calls table. The optional M2
// fields (TokensIn, TokensOut, Cost, LatencyMs, Model, Provider) are
// persisted when present; absent fields stay NULL.
//
// M2 fields use pointer types (*int64, *float64, *string) so the absent-vs-
// zero distinction survives both the JSON marshal round-trip (nil → null,
// &0 → 0) and the SQL round-trip (nil → NULL, &0 → 0). Without pointers,
// Go's zero-value semantics would coerce a caller-supplied 0 into "absent"
// (because the zero value for the field is indistinguishable from "not
// set"). The JSON tags deliberately omit `omitempty` so an absent M2
// field round-trips as JSON `null` rather than being dropped from the
// output object — ktx read / ktx get (JSON) must surface the absence
// explicitly so consumers can detect 'absent' from '0'.
//
// EventType defaults to "tool_call" when empty; "session_close" selects the
// close payload shape (just SessionID + Timestamp).
type ToolCall struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	Tool        string `json:"tool"`
	Input       string `json:"input"`
	Output      string `json:"output"`
	ContentHash string `json:"content_hash"`
	Timestamp   string `json:"timestamp"`
	EventType   string `json:"event_type"`

	// Optional M2 cost-publication fields (persisted when present;
	// NULL when absent; pointer types preserve the absent-vs-zero
	// distinction through JSON and SQL round-trips).
	TokensIn  *int64   `json:"tokens_in"`
	TokensOut *int64   `json:"tokens_out"`
	Cost      *float64 `json:"cost"`
	LatencyMs *int64   `json:"latency_ms"`
	Model     *string  `json:"model"`
	Provider  *string  `json:"provider"`
}

// Session is a row in the sessions table (Decision 7). The JSON tags mirror
// the column names so ktx read can marshal a fetched row directly. There is
// no omitempty: an empty EndedAt is a meaningful signal that the session
// is still open, and zero ToolCallCount is a real value for an unused
// session.
type Session struct {
	ID            string `json:"id"`
	Agent         string `json:"agent"`
	StartedAt     string `json:"started_at"`
	EndedAt       string `json:"ended_at"`
	LastSeenAt    string `json:"last_seen_at"`
	ToolCallCount int64  `json:"tool_call_count"`
}

// SessionExists reports whether a session with the given id exists. An empty
// id is treated as "does not exist" so the caller-provided hint path can
// distinguish "absent" from "present" without a separate guard.
func (d *DB) SessionExists(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var one int
	err := d.db.QueryRow(`SELECT 1 FROM sessions WHERE id = ? LIMIT 1`, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: check session %q: %w", id, err)
	}
	return true, nil
}

// InsertSession creates a new session row (Decision 7: INSERT on open). The
// caller is responsible for generating the id via NewID and for writing the
// ppid session file when this is part of the adapter/hook open path.
func (d *DB) InsertSession(s *Session) error {
	_, err := d.db.Exec(
		`INSERT INTO sessions (id, agent, started_at, ended_at, last_seen_at, tool_call_count) VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.Agent, s.StartedAt, s.EndedAt, s.LastSeenAt, s.ToolCallCount,
	)
	if err != nil {
		return fmt.Errorf("store: insert session %q: %w", s.ID, err)
	}
	return nil
}

// CloseSession sets ended_at on the session row (Decision 7: UPDATE on
// close). It returns ErrNotFound when no session matches id, so the close
// path surfaces a clear not-found rather than a silent no-op.
func (d *DB) CloseSession(id, endedAt string) error {
	res, err := d.db.Exec(`UPDATE sessions SET ended_at = ? WHERE id = ?`, endedAt, id)
	if err != nil {
		return fmt.Errorf("store: close session %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PersistToolCall inserts the tool_call row and updates the session roll-up
// (tool_call_count + 1, last_seen_at = tc.Timestamp) in one transaction
// (Decision 7: tool_calls INSERT only, sessions UPDATE roll-up). The
// transaction keeps the aggregate consistent with the evidence — a
// tool_call never exists without its session counters reflecting it.
//
// The INSERT writes all 14 columns: the 8 M1 fields (id, session_id, tool,
// input, output, content_hash, timestamp, event_type) and the 6 M2
// cost-publication fields (tokens_in, tokens_out, cost, latency_ms, model,
// provider). M2 fields are passed as the *int64 / *float64 / *string
// pointer values on the ToolCall struct; a nil pointer round-trips to
// SQL NULL (database/sql passes a typed nil pointer as NULL), so 'field
// absent' is preserved. A non-nil pointer to zero persists as 0, so
// 'field set to zero' is preserved. This is the documented plan choice
// (Task 3) and the only round-trip-safe shape: Go's zero-value semantics
// would otherwise coerce 0 → NULL via the field's default.
//
// Cost derivation (Decision 11) runs at write time, before the INSERT:
// when the caller omitted `cost` (nil pointer) AND tokens_in, tokens_out,
// model, and provider are all non-NULL AND the cache has a price for the
// (provider, model) tuple, the function fills `cost` from
//
//	cost = (tokens_in * in_price + tokens_out * out_price) / 1_000_000
//
// rounded to 6 decimal places (the methodology-v1 headline precision).
// The derivation runs ONCE per write; subsequent reads (GetToolCall,
// ktx read, ktx get) surface the persisted value verbatim — no
// re-derivation at read time (the reconstructability contract, AC5).
// Cache miss + warm cache → cost stays NULL (no refetch from the write
// path; the on-disk cache is the source of truth between fetches,
// AC2). Cache miss + cold cache → fetch + atomic write to disk, then
// derivation. Fetch failure → cost stays NULL, no error returned (AC3,
// AC6). NULL preservation: any of tokens_in / tokens_out / model NULL
// blocks derivation even when the cache has a price (AC7).
//
// EventType defaults to "tool_call" when empty.
func (d *DB) PersistToolCall(tc *ToolCall) error {
	eventType := tc.EventType
	if eventType == "" {
		eventType = "tool_call"
	}

	d.deriveCost(tc) // write-time D11 derivation; soft-fail, see helper

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	defer tx.Rollback() // Safe, low-overhead no-op if tx.Commit() is called successfully

	_, err = tx.Exec(
		`INSERT INTO tool_calls (
			id, session_id, tool, input, output, content_hash, timestamp, event_type,
			tokens_in, tokens_out, cost, latency_ms, model, provider
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tc.ID, tc.SessionID, tc.Tool, tc.Input, tc.Output, tc.ContentHash, tc.Timestamp, eventType,
		tc.TokensIn, tc.TokensOut, tc.Cost, tc.LatencyMs, tc.Model, tc.Provider,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: insert tool_call %q: %w", tc.ID, err)
	}

	_, err = tx.Exec(
		`UPDATE sessions SET tool_call_count = tool_call_count + 1, last_seen_at = ? WHERE id = ?`,
		tc.Timestamp, tc.SessionID,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: update session roll-up %q: %w", tc.SessionID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tool_call %q: %w", tc.ID, err)
	}
	return nil
}

// GetToolCall fetches a tool_call row by id. It returns ErrNotFound when no
// row matches so the CLI layer can map "not found" to exit code 2 (D4).
// The returned struct carries the persisted shape including the M2
// cost-publication fields (tokens_in, tokens_out, cost, latency_ms,
// model, provider). The M2 columns are nullable; the Scan uses
// sql.NullInt64 / sql.NullFloat64 / sql.NullString intermediates so
// NULL is converted back to a nil pointer on the ToolCall struct,
// preserving the 'field absent' distinction. A non-NULL column with
// value 0 round-trips to a non-nil pointer to 0; callers can detect
// 'absent' (nil) vs 'set to zero' (&0) without ambiguity.
func (d *DB) GetToolCall(id string) (*ToolCall, error) {
	var tc ToolCall
	var (
		tokensIn   sql.NullInt64
		tokensOut  sql.NullInt64
		cost       sql.NullFloat64
		latencyMs  sql.NullInt64
		model      sql.NullString
		provider   sql.NullString
	)
	err := d.db.QueryRow(
		`SELECT id, session_id, tool, input, output, content_hash, timestamp, event_type,
		        tokens_in, tokens_out, cost, latency_ms, model, provider
		 FROM tool_calls WHERE id = ?`,
		id,
	).Scan(
		&tc.ID, &tc.SessionID, &tc.Tool, &tc.Input, &tc.Output,
		&tc.ContentHash, &tc.Timestamp, &tc.EventType,
		&tokensIn, &tokensOut, &cost, &latencyMs, &model, &provider,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get tool_call %q: %w", id, err)
	}
	if tokensIn.Valid {
		v := tokensIn.Int64
		tc.TokensIn = &v
	}
	if tokensOut.Valid {
		v := tokensOut.Int64
		tc.TokensOut = &v
	}
	if cost.Valid {
		v := cost.Float64
		tc.Cost = &v
	}
	if latencyMs.Valid {
		v := latencyMs.Int64
		tc.LatencyMs = &v
	}
	if model.Valid {
		v := model.String
		tc.Model = &v
	}
	if provider.Valid {
		v := provider.String
		tc.Provider = &v
	}
	return &tc, nil
}

// GetSession fetches a session row by id. It returns ErrNotFound when no
// row matches. Used by `ktx read` to back the cross-table ID lookup; also
// useful for future query commands (Task 7) and for session inspection in
// tests.
//
// Nullable text columns (agent, started_at, ended_at, last_seen_at) are
// coalesced to the empty string in SQL so the Session struct can stay
// string-typed and consistent with how ToolCall represents missing values.
// An empty EndedAt in the output remains a meaningful signal that the
// session is still open.
func (d *DB) GetSession(id string) (*Session, error) {
	var s Session
	err := d.db.QueryRow(
		`SELECT id,
		        COALESCE(agent, ''),
		        COALESCE(started_at, ''),
		        COALESCE(ended_at, ''),
		        COALESCE(last_seen_at, ''),
		        tool_call_count
		 FROM sessions WHERE id = ?`,
		id,
	).Scan(
		&s.ID, &s.Agent, &s.StartedAt, &s.EndedAt, &s.LastSeenAt, &s.ToolCallCount,
	)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get session %q: %w", id, err)
	}
	return &s, nil
}

// GetRecord fetches a record by id from either the tool_calls or sessions
// table (Decision 7: both tables are addressed by 16-char nanoid ID).
// tool_calls is checked first because it is the higher-volume table; the
// CLI layer treats both as records and prints whichever was found.
//
// The returned value is a *ToolCall or *Session, both already JSON-tagged,
// so the CLI can pass it directly to json.NewEncoder. Any error other than
// ErrNotFound from the underlying table reads is propagated unchanged.
func (d *DB) GetRecord(id string) (any, error) {
	if tc, err := d.GetToolCall(id); err == nil {
		return tc, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if s, err := d.GetSession(id); err == nil {
		return s, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return nil, ErrNotFound
}

// ListSessions returns all session rows ordered by started_at ascending,
// then by id ascending as a stable tie-breaker (so two sessions that
// started at the same instant keep a deterministic display order across
// invocations). The slice is non-nil but may be empty when no sessions
// have been recorded — the CLI can json-encode an empty slice as "[]"
// without a nil check.
//
// This is the M1 minimal list: no cost/token filters (M2) and no
// pagination (post-M1). Ordered by started_at so the table view shows
// the oldest session first, matching the convention of other log-style
// tools.
func (d *DB) ListSessions() ([]Session, error) {
	rows, err := d.db.Query(
		`SELECT id,
		        COALESCE(agent, ''),
		        COALESCE(started_at, ''),
		        COALESCE(ended_at, ''),
		        COALESCE(last_seen_at, ''),
		        tool_call_count
		 FROM sessions
		 ORDER BY started_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(
			&s.ID, &s.Agent, &s.StartedAt, &s.EndedAt, &s.LastSeenAt, &s.ToolCallCount,
		); err != nil {
			return nil, fmt.Errorf("store: scan session: %w", err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate sessions: %w", err)
	}
	return sessions, nil
}

// costRoundDecimals is the precision for the D11 derivation. The
// methodology-v1 headline number is six decimals
// (`.agentera/bench/methodology-v1.yaml`'s `cost_per_correct = cost /
// max(score, 1e-9)` plus the D12 `six decimal places` rule); the
// persisted cost uses the same precision so a future reader does not
// have to round before dividing.
const costRoundDecimals = 6

// deriveCost fills tc.Cost from the cached (provider, model) price
// when the caller omitted it and all four price-derivation inputs are
// non-NULL (tokens_in, tokens_out, model, provider). The function is
// silent on every branch that does not derive — there is no error
// return because the D11 soft-fail contract requires the write path to
// persist the row with whatever cost the caller supplied (or NULL) on
// any failure to derive.
//
// Derivation formula:
//
//	cost_USD = (tokens_in * in_price + tokens_out * out_price) / 1_000_000
//
// rounded to 6 decimals. in_price and out_price are USD per million
// tokens, the units models.dev uses for its cost.input and
// cost.output fields; the division by 1_000_000 converts to "USD per
// token" multiplied by token count.
//
// Lookup behaviour (D11 reasoning, AC1-AC7):
//
//   - Caller supplied cost → no-op. A non-nil pointer (including a
//     pointer to 0) round-trips verbatim per Task 3's contract; the
//     caller owns the cost evidence.
//   - Any of tokens_in / tokens_out / model NULL → no-op. NULL
//     preservation: the D11 reconstructability contract requires all
//     three; a partial set would force ktx to invent the missing
//     value, which violates "conservative failure" (ARCHITECTURE.md
//     Quality attributes).
//   - Provider NULL → no-op. The cache is keyed by (provider, model);
//     without a provider, the lookup cannot resolve and silently
//     failing is the safe outcome. The OpenCode wrapper path defaults
//     provider to "opencode" (D10), so this branch only fires on
//     sidecar payloads that omit the field.
//   - Cache hit → derive.
//   - Cache miss + cold cache → fetch from models.dev. On success,
//     atomic-write the cache to disk (AC1, AC4) and re-lookup. On
//     failure, cost stays NULL (AC3, AC6 — soft fail).
//   - Cache miss + warm cache → no-op. The on-disk cache is the source
//     of truth between fetches; the user must run `ktx prices update`
//     to refresh (AC2 — idempotent write path preserves mtime).
//
// The mutex-free read on tc is safe because the caller owns the
// pointer (writeToolCall / teeAndParse build a fresh ToolCall and
// hand it to PersistToolCall). The Prices cache is itself
// concurrency-safe (sync.RWMutex inside prices.go).
func (d *DB) deriveCost(tc *ToolCall) {
	if tc == nil {
		return
	}
	// Caller-supplied cost wins. A non-nil pointer (including a
	// pointer to zero) round-trips verbatim — D11 is about filling
	// in what the caller omitted, not overwriting what they
	// supplied.
	if tc.Cost != nil {
		return
	}
	// NULL preservation: all four derivation inputs must be non-NULL.
	// Any NULL field short-circuits derivation (AC7).
	if tc.TokensIn == nil || tc.TokensOut == nil ||
		tc.Model == nil || tc.Provider == nil {
		return
	}

	prices, _ := LoadPrices() // soft-fail: a load error means empty cache
	if prices == nil || prices.path == "" {
		return
	}

	in, out, ok := prices.Lookup(*tc.Provider, *tc.Model)
	if !ok && prices.Cold() {
		// Cold cache: try fetching from models.dev and persisting
		// atomically. Refresh is bounded to 5s and surfaces
		// failures as a non-nil error; the soft-fail contract
		// drops the error and leaves cost NULL (AC3, AC6).
		if err := prices.Refresh(); err == nil {
			in, out, ok = prices.Lookup(*tc.Provider, *tc.Model)
			if ok {
				// Best-effort write to disk. A second-order
				// failure here (disk full, permission denied)
				// does not block the in-memory derivation —
				// the row persists with the derived cost
				// regardless, and the next cold-cache
				// invocation will retry the write.
				_ = prices.WriteToDisk()
			}
		}
	}
	if !ok {
		return
	}

	raw := float64(*tc.TokensIn)*in + float64(*tc.TokensOut)*out
	raw = raw / 1_000_000
	rounded := math.Round(raw*math.Pow(10, costRoundDecimals)) / math.Pow(10, costRoundDecimals)
	tc.Cost = &rounded
}
