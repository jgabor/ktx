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
//
// SessionM2 and SessionM2Rollups were carried over from rollup.go during
// the file-reorg (Task 2 of the deepening plan): the per-session M2
// cost-publication rollup is part of the record-side read surface, so
// the type and its query live here alongside the other record methods.
package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	gonanoid "github.com/matoous/go-nanoid/v2"
)

// ErrNotFound is returned when a referenced session or record does not exist
// in the database. The cli layer maps it to a not-found exit code (D4).
var ErrNotFound = errors.New("db: not found")

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

// NewID returns a fresh 16-character lowercase alphabetic nanoid (D3).
// Collision-safe for billions of records; no ambiguous characters.
func NewID() (string, error) {
	return gonanoid.Generate(idAlphabet, idLength)
}

// ContentHash returns the SHA-256 hex digest of output (Decision 3). The
// content hash is stored as a separate field on each tool_call row for
// verification without being the primary key.
func ContentHash(output string) string {
	sum := sha256.Sum256([]byte(output))
	return hex.EncodeToString(sum[:])
}

// SessionM2 is the per-session M2 cost-publication rollup surfaced by
// `ktx list`. Each field is a pointer so the absent-vs-zero
// distinction survives the JSON output: a session with no underlying
// call contributing to a field has a nil pointer; a session where
// the non-NULL contributions sum to zero has a non-nil pointer to 0.
type SessionM2 struct {
	Tokens *int64   `json:"tokens"`
	Cost   *float64 `json:"cost"`
	Model  *string  `json:"model"`
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
		return false, fmt.Errorf("db: check session %q: %w", id, err)
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
		return fmt.Errorf("db: insert session %q: %w", s.ID, err)
	}
	return nil
}

// CloseSession sets ended_at on the session row (Decision 7: UPDATE on
// close). It returns ErrNotFound when no session matches id, so the close
// path surfaces a clear not-found rather than a silent no-op.
func (d *DB) CloseSession(id, endedAt string) error {
	res, err := d.db.Exec(`UPDATE sessions SET ended_at = ? WHERE id = ?`, endedAt, id)
	if err != nil {
		return fmt.Errorf("db: close session %q: %w", id, err)
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
		return fmt.Errorf("db: begin tx: %w", err)
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
		return fmt.Errorf("db: insert tool_call %q: %w", tc.ID, err)
	}

	_, err = tx.Exec(
		`UPDATE sessions SET tool_call_count = tool_call_count + 1, last_seen_at = ? WHERE id = ?`,
		tc.Timestamp, tc.SessionID,
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("db: update session roll-up %q: %w", tc.SessionID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit tool_call %q: %w", tc.ID, err)
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
		tokensIn  sql.NullInt64
		tokensOut sql.NullInt64
		cost      sql.NullFloat64
		latencyMs sql.NullInt64
		model     sql.NullString
		provider  sql.NullString
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
		return nil, fmt.Errorf("db: get tool_call %q: %w", id, err)
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
		return nil, fmt.Errorf("db: get session %q: %w", id, err)
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
		return nil, fmt.Errorf("db: list sessions: %w", err)
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(
			&s.ID, &s.Agent, &s.StartedAt, &s.EndedAt, &s.LastSeenAt, &s.ToolCallCount,
		); err != nil {
			return nil, fmt.Errorf("db: scan session: %w", err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate sessions: %w", err)
	}
	return sessions, nil
}

// SessionM2Rollups returns the M2 cost-publication rollup for every
// session that has at least one tool_call with a non-NULL M2 field.
// Sessions without any non-NULL M2 data are absent from the result
// map — a missing key means "no M2 data", which the inspect surface
// renders as the explicit "-" marker (AC6).
//
// Implementation: a single GROUP BY over tool_calls filtered with
// a HAVING clause that excludes groups where every aggregate is
// NULL. SQLite evaluates SUM and MIN on a per-group basis; SUM
// ignores NULL operands (so a row with tokens_in=NULL contributes
// NULL to the per-call sum and is excluded), and MIN returns NULL
// only when every input is NULL. The HAVING clause is therefore a
// faithful "did this group see any non-NULL M2 data?" check.
//
// The query is read-only and uses a single index-friendly
// aggregate; for the expected session counts (thousands, not
// millions) the per-group sort is not a bottleneck. The result
// map is keyed by session_id so the CLI layer can look up the
// rollup in O(1) when rendering each session row.
//
// Tokens: SUM(tokens_in + tokens_out) over the session's tool_calls.
// SQLite's SUM ignores NULL operands, so a row with either field
// NULL contributes NULL to the per-call sum and is excluded from
// the aggregate. This matches the spec's "per-call roll-up of
// non-NULL values" — the per-call pairing of tokens_in +
// tokens_out is the unit of the rollup.
//
// Cost: SUM(cost) over the session's tool_calls. AC2's mixed-NULL
// case (1 NULL + 2 non-NULL) sums only the non-NULL values.
//
// Model: MIN(model) over the session's tool_calls. SQLite's MIN
// is deterministic and returns NULL when every value is NULL.
// "First non-NULL model" in the plan is interpreted as "any
// non-NULL model, deterministic" — the GROUP BY aggregation has
// no natural row order, so MIN/MAX are the SQL-idiomatic choices
// and both produce a stable, non-NULL model when at least one
// call has a non-NULL model.
//
// Pointer types (*int64, *float64, *string) preserve the
// absent-vs-zero distinction through to the inspect surface: a
// session with calls summing to 0 tokens reports Tokens=&0, not
// nil. A session with no non-NULL token values in any call reports
// Tokens=nil, and the inspect surface renders "-" for the column.
// The JSON tags deliberately omit omitempty so absent sub-rollups
// surface as JSON null rather than being dropped from the object
// — consistent with the pointer discipline used on ToolCall
// (Task 3).
//
// The query uses a HAVING clause to filter to sessions with at
// least one non-NULL M2 field. Sessions with no M2 data are absent
// from the result map; the inspect surface treats a missing key as
// "no data" and renders the explicit "-" marker (AC6).
func (d *DB) SessionM2Rollups() (map[string]SessionM2, error) {
	rows, err := d.db.Query(
		`SELECT session_id,
		        SUM(tokens_in + tokens_out),
		        SUM(cost),
		        MIN(model)
		 FROM tool_calls
		 GROUP BY session_id
		 HAVING SUM(tokens_in + tokens_out) IS NOT NULL
		     OR SUM(cost) IS NOT NULL
		     OR MIN(model) IS NOT NULL`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: session m2 rollups: %w", err)
	}
	defer rows.Close()

	out := make(map[string]SessionM2)
	for rows.Next() {
		var (
			sessionID string
			tokens    sql.NullInt64
			cost      sql.NullFloat64
			model     sql.NullString
		)
		if err := rows.Scan(&sessionID, &tokens, &cost, &model); err != nil {
			return nil, fmt.Errorf("db: scan m2 rollup: %w", err)
		}
		roll := SessionM2{}
		if tokens.Valid {
			v := tokens.Int64
			roll.Tokens = &v
		}
		if cost.Valid {
			v := cost.Float64
			roll.Cost = &v
		}
		if model.Valid {
			v := model.String
			roll.Model = &v
		}
		out[sessionID] = roll
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate m2 rollups: %w", err)
	}
	return out, nil
}

// deriveCost fills tc.Cost from the cached (provider, model) price
// when the caller omitted it. The function is a thin wrapper around
// the costDeriver deep module (cost.go): it builds a CostInputs from
// the ToolCall, hands it to Derive, and assigns the result back to
// tc.Cost. The math, the NULL-preservation branches, the lookup-and-
// cold-fetch policy, and the 6-decimal rounding all live in cost.go;
// this wrapper is only the struct-to-input adapter.
//
// D11 soft-fail: Derive returns nil on every branch that does not
// derive. tc.Cost is left untouched in that case (the caller's
// original value, or nil), and the row persists with whatever cost
// the caller supplied (or NULL) — same contract as the inline
// helper it replaced.
//
// The mutex-free read on tc is safe because the caller owns the
// pointer (writeToolCall / teeAndParse build a fresh ToolCall and
// hand it to PersistToolCall). The Prices cache is itself
// concurrency-safe (sync.RWMutex inside prices.go).
func (d *DB) deriveCost(tc *ToolCall) {
	if tc == nil {
		return
	}
	// LoadPrices is the process-wide singleton (sync.Once). A nil
	// *Prices with an empty path means XDG_DATA_HOME resolution
	// failed — the write path cannot derive against an empty
	// path, so the early-out mirrors the pre-refactor branch.
	prices, _ := LoadPrices()
	if prices == nil || prices.path == "" {
		return
	}
	tc.Cost = (&costDeriver{}).Derive(CostInputs{
		TokensIn:  tc.TokensIn,
		TokensOut: tc.TokensOut,
		Model:     tc.Model,
		Provider:  tc.Provider,
		Cost:      tc.Cost,
	}, prices)
}
