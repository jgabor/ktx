// Stored record types and persistence methods for the ktx database.
//
// Two-table model:
//   - tool_calls: append-only. INSERT only (PersistToolCall).
//   - sessions: aggregate. INSERT on open (InsertSession), UPDATE on
//     roll-up and close (PersistToolCall's tx, CloseSession).
//
// The ToolCall struct carries the full flat JSON shape, including the
// optional cost-publication fields (tokens_in, tokens_out, cost, latency_ms,
// model, provider). The cost-publication columns live in tool_calls since
// the in-place ALTER TABLE migration in Open(). PersistToolCall writes
// them; a nil pointer maps to SQL NULL so 'field absent' survives the
// round-trip; a non-nil pointer to zero persists as 0 so 'field set to
// zero' is not conflated with 'absent' (the reconstructability requirement).
package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jgabor/ktx/internal/canon"
	gonanoid "github.com/matoous/go-nanoid/v2"
)

// ErrNotFound is returned when a referenced session or record does not exist
// in the database. The cli layer maps it to a not-found exit code.
var ErrNotFound = errors.New("db: not found")

// ErrToolCallConflict reports that a retry reused a tool-call identity with
// different evidence. The first persisted row remains authoritative.
var ErrToolCallConflict = errors.New("db: tool_call identity conflict")

const (
	LatencyStateMeasured    = "measured"
	LatencyStateUnknown     = "unknown"
	LatencyStateMalformed   = "malformed"
	LatencyUnitMilliseconds = "milliseconds"
	LatencyAuthorityRuntime = "runtime"
	LatencyAuthorityUnknown = "unknown"
)

// Producer is the bounded command label that first accepted a source or
// artifact. A nil producer means the row is historical or unattributed.
type Producer string

const (
	ProducerWrite          Producer = "write"
	ProducerHook           Producer = "hook"
	ProducerRun            Producer = "run"
	ProducerCompress       Producer = "compress"
	ProducerComponentBench Producer = "componentbench"
	ProducerLoadFixture    Producer = "loadbench-fixture"
)

// ToolCall is the flat JSON tool_call payload.
//
// Required fields are persisted to the tool_calls table. The optional
// cost-publication fields (TokensIn, TokensOut, Cost, LatencyMs, Model,
// Provider) are persisted when present; absent fields stay NULL.
//
// These fields use pointer types (*int64, *float64, *string) so the absent-vs-
// zero distinction survives both the JSON marshal round-trip (nil → null,
// &0 → 0) and the SQL round-trip (nil → NULL, &0 → 0). Without pointers,
// Go's zero-value semantics would coerce a caller-supplied 0 into "absent"
// (because the zero value for the field is indistinguishable from "not
// set"). The JSON tags deliberately omit `omitempty` so an absent cost-
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

	// Optional cost-publication fields (persisted when present;
	// NULL when absent; pointer types preserve the absent-vs-zero
	// distinction through JSON and SQL round-trips).
	TokensIn         *int64   `json:"tokens_in"`
	TokensOut        *int64   `json:"tokens_out"`
	Cost             *float64 `json:"cost"`
	LatencyMs        *int64   `json:"latency_ms"`
	LatencyState     string   `json:"latency_state"`
	LatencyUnit      string   `json:"latency_unit"`
	LatencyAuthority string   `json:"latency_authority"`
	Model            *string  `json:"model"`
	Provider         *string  `json:"provider"`

	// Optional failure-signal fields (persisted when present;
	// NULL when absent; pointer types preserve the absent-vs-zero
	// distinction through JSON and SQL round-trips). is_error is
	// canonical for the compress pre-pipeline gate; exit_code is
	// diagnostic only.
	ExitCode *int64 `json:"exit_code"`
	IsError  *bool  `json:"is_error"`

	// Optional runtime field (persisted when present; NULL when
	// absent; pointer type preserves the absent-vs-empty distinction
	// through JSON and SQL round-trips). runtime records the agent
	// runtime that executed the tool call.
	Runtime *string `json:"runtime"`

	Producer *Producer `json:"producer"`
}

func normalizeLatencyEvidence(tc *ToolCall) error {
	if tc.LatencyState == "" {
		tc.LatencyState = LatencyStateUnknown
	}
	if tc.LatencyUnit == "" {
		tc.LatencyUnit = LatencyUnitMilliseconds
	}
	if tc.LatencyAuthority == "" {
		tc.LatencyAuthority = LatencyAuthorityUnknown
	}
	if tc.LatencyUnit != LatencyUnitMilliseconds {
		return fmt.Errorf("latency unit %q is invalid", tc.LatencyUnit)
	}
	switch tc.LatencyState {
	case LatencyStateMeasured:
		if tc.LatencyMs == nil || *tc.LatencyMs < 0 || tc.LatencyAuthority != LatencyAuthorityRuntime {
			return errors.New("measured latency requires a non-negative value and runtime authority")
		}
	case LatencyStateUnknown:
		if tc.LatencyAuthority != LatencyAuthorityUnknown {
			return errors.New("unknown latency requires unknown authority")
		}
	case LatencyStateMalformed:
		if tc.LatencyMs != nil || tc.LatencyAuthority != LatencyAuthorityRuntime {
			return errors.New("malformed latency requires no value and runtime authority")
		}
	default:
		return fmt.Errorf("latency state %q is invalid", tc.LatencyState)
	}
	return nil
}

// Session is a row in the sessions table. The JSON tags mirror
// the column names so ktx read can marshal a fetched row directly. There is
// no omitempty: an empty EndedAt is a meaningful signal that the session
// is still open, and zero ToolCallCount is a real value for an unused
// session.
type Session struct {
	ID                        string `json:"id"`
	Agent                     string `json:"agent"`
	AgentAttributionAuthority string `json:"-"`
	StartedAt                 string `json:"started_at"`
	EndedAt                   string `json:"ended_at"`
	LastSeenAt                string `json:"last_seen_at"`
	ToolCallCount             int64  `json:"tool_call_count"`
}

// NewID returns a fresh 16-character lowercase alphabetic nanoid.
// Collision-safe for billions of records; no ambiguous characters.
func NewID() (string, error) {
	return gonanoid.Generate(idAlphabet, idLength)
}

// ContentHash returns the SHA-256 hex digest of output. The
// content hash is stored as a separate field on each tool_call row for
// verification without being the primary key.
func ContentHash(output string) string {
	sum := sha256.Sum256([]byte(output))
	return hex.EncodeToString(sum[:])
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

// InsertSession creates a new session row (INSERT on open). The
// caller is responsible for generating the id via NewID and for writing the
// ppid session file when this is part of the adapter/hook open path.
func (d *DB) InsertSession(s *Session) error {
	return d.insertSession(s, d.withWriteTx)
}

func (d *DB) insertSession(s *Session, write func(context.Context, func(*sql.Tx) error) error) error {
	authority := s.AgentAttributionAuthority
	if authority == "" {
		authority = AgentAttributionUnknown
	}
	if authority != AgentAttributionUnknown && authority != AgentAttributionIdentified {
		return fmt.Errorf("db: insert session %q: invalid agent attribution authority %q", s.ID, authority)
	}
	ctx, cancel := writeContext()
	defer cancel()
	err := write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (id, agent, agent_attribution_authority, started_at, ended_at, last_seen_at, tool_call_count) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.Agent, authority, s.StartedAt, s.EndedAt, s.LastSeenAt, s.ToolCallCount,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: insert session %q: %w", s.ID, err)
	}
	return nil
}

// IdentifySession records the first trusted runtime identity observed for a
// session. Later trusted observations leave that identity unchanged.
func (d *DB) IdentifySession(id, agent string) error {
	return d.identifySession(id, agent, d.withWriteTx)
}

func (d *DB) identifySession(id, agent string, write func(context.Context, func(*sql.Tx) error) error) error {
	if agent == "" {
		return errors.New("db: identify session: agent is empty")
	}
	ctx, cancel := writeContext()
	defer cancel()
	var exists bool
	err := write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions
			SET agent = ?, agent_attribution_authority = ?
			WHERE id = ? AND agent_attribution_authority = ?`,
			agent, AgentAttributionIdentified, id, AgentAttributionUnknown); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, id).Scan(&exists)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("db: identify session %q: %w", id, err)
	}
	return nil
}

// CloseSession sets ended_at on the session row (UPDATE on
// close). It returns ErrNotFound when no session matches id, so the close
// path surfaces a clear not-found rather than a silent no-op.
func (d *DB) CloseSession(id, endedAt string) error {
	return d.closeSession(id, endedAt, d.withWriteTx)
}

func (d *DB) closeSession(id, endedAt string, write func(context.Context, func(*sql.Tx) error) error) error {
	ctx, cancel := writeContext()
	defer cancel()
	var rows int64
	err := write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE sessions SET ended_at = ? WHERE id = ?`, endedAt, id)
		if err != nil {
			return err
		}
		rows, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("db: close session %q: %w", id, err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// PersistToolCall inserts the tool_call row and updates the session roll-up
// (tool_call_count + 1, last_seen_at = tc.Timestamp) in one transaction
// (tool_calls INSERT only, sessions UPDATE roll-up). The
// transaction keeps the aggregate consistent with the evidence — a
// tool_call never exists without its session counters reflecting it.
//
// The INSERT writes all columns: the 8 core fields (id, session_id, tool,
// input, output, content_hash, timestamp, event_type), the 6
// cost-publication fields (tokens_in, tokens_out, cost, latency_ms, model,
// provider), the 2 failure-signal fields (exit_code, is_error), and the
// runtime and producer fields. These fields are passed as pointer values on
// the ToolCall struct; a nil pointer round-trips to
// SQL NULL. A non-nil pointer to zero persists as 0, preserving the
// distinction between an absent field and a measured zero.
//
// Cost derivation runs at write time, before the INSERT:
// when the caller omitted `cost` (nil pointer) AND tokens_in, tokens_out,
// model, and provider are all non-NULL AND the cache has a price for the
// (provider, model) tuple, the function fills `cost` from
//
//	cost = (tokens_in * in_price + tokens_out * out_price) / 1_000_000
//
// rounded to 6 decimal places.
// The derivation runs ONCE per write; subsequent reads (GetToolCall,
// ktx read, ktx get) surface the persisted value verbatim — no
// re-derivation at read time.
// Cache miss + warm cache → cost stays NULL (no refetch from the write
// path; the on-disk cache is the source of truth between fetches).
// Cache miss + cold cache → fetch + atomic write to disk, then
// derivation. Fetch failure → cost stays NULL, no error returned.
// NULL preservation: any of tokens_in / tokens_out / model NULL
// blocks derivation even when the cache has a price.
//
// EventType defaults to "tool_call" when empty.
func (d *DB) PersistToolCall(tc *ToolCall) error {
	if err := normalizeLatencyEvidence(tc); err != nil {
		return fmt.Errorf("db: persist tool_call %q: %w", tc.ID, err)
	}
	d.deriveCost(tc) // write-time cost derivation; soft-fail, see helper
	return d.persistToolCallPrepared(tc, d.withWriteTx)
}

func (d *DB) persistToolCallPrepared(tc *ToolCall, write func(context.Context, func(*sql.Tx) error) error) error {
	eventType := tc.EventType
	if eventType == "" {
		eventType = "tool_call"
	}

	ctx, cancel := writeContext()
	defer cancel()
	err := write(ctx, func(tx *sql.Tx) error {
		columns, err := tableColumns(tx, "tool_calls")
		if err != nil {
			return err
		}
		timestampUnixNano := sortableTimestamp(tc.Timestamp)
		query := `INSERT INTO tool_calls (
			id, session_id, tool, input, output, content_hash, timestamp, timestamp_unix_nano, event_type,
			tokens_in, tokens_out, cost, latency_ms, model, provider,
			exit_code, is_error, runtime, latency_state, latency_unit, latency_authority, producer
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		args := []any{
			tc.ID, tc.SessionID, tc.Tool, tc.Input, tc.Output, tc.ContentHash, tc.Timestamp, timestampUnixNano, eventType,
			tc.TokensIn, tc.TokensOut, tc.Cost, tc.LatencyMs, tc.Model, tc.Provider,
			tc.ExitCode, isErrorSQL(tc.IsError), tc.Runtime, tc.LatencyState, tc.LatencyUnit, tc.LatencyAuthority, tc.Producer,
		}
		if _, ok := columns["latency_state"]; !ok {
			query = `INSERT INTO tool_calls (id, session_id, tool, input, output, content_hash, timestamp, event_type, tokens_in, tokens_out, cost, latency_ms, model, provider, exit_code, is_error, runtime) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			args = append(args[:7], args[8:18]...)
		}
		_, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("db: insert tool_call %q: %w", tc.ID, err)
		}

		_, err = tx.ExecContext(ctx,
			`UPDATE sessions SET tool_call_count = tool_call_count + 1, last_seen_at = ? WHERE id = ?`,
			tc.Timestamp, tc.SessionID,
		)
		if err != nil {
			return fmt.Errorf("db: update session roll-up %q: %w", tc.SessionID, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("db: persist tool_call %q: %w", tc.ID, err)
	}
	return nil
}

// PersistToolCallOnce persists tc exactly once and reports whether this call
// inserted the row. A retry with the same id, tool, canonically-equal input,
// exact output, and exact runtime succeeds without updating the row or session
// roll-up. Reusing the id with different authoritative evidence returns
// ErrToolCallConflict and preserves the first row.
//
// Authoritative equality follows the identity decision: the input field is
// compared under the strict canonical JSON comparison (canon.Equal, which
// removes only object member order and insignificant whitespace and falls back
// to exact bytes for non-normalizable input). Tool, output (exact bytes), and
// runtime are compared exactly. All other metadata is first-wins: a retry with
// different cost, token, timestamp, exit-code, or session fields neither
// conflicts nor enriches the stored row.
//
// The insert-or-observe decision and session roll-up share one transaction, so
// concurrent deliveries cannot double-count a tool call.
func (d *DB) PersistToolCallOnce(tc *ToolCall) (bool, error) {
	if err := normalizeLatencyEvidence(tc); err != nil {
		return false, fmt.Errorf("db: persist tool_call %q: %w", tc.ID, err)
	}
	d.deriveCost(tc)
	return d.persistToolCallOncePrepared(tc, d.withWriteTx)
}

func (d *DB) persistToolCallOncePrepared(tc *ToolCall, write func(context.Context, func(*sql.Tx) error) error) (bool, error) {
	eventType := tc.EventType
	if eventType == "" {
		eventType = "tool_call"
	}

	ctx, cancel := writeContext()
	defer cancel()
	var didInsert bool
	err := write(ctx, func(tx *sql.Tx) error {
		timestampUnixNano := sortableTimestamp(tc.Timestamp)
		res, err := tx.ExecContext(ctx,
			`INSERT INTO tool_calls (
			id, session_id, tool, input, output, content_hash, timestamp, timestamp_unix_nano, event_type,
			tokens_in, tokens_out, cost, latency_ms, model, provider,
			exit_code, is_error, runtime, latency_state, latency_unit, latency_authority, producer
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
			tc.ID, tc.SessionID, tc.Tool, tc.Input, tc.Output, tc.ContentHash, tc.Timestamp, timestampUnixNano, eventType,
			tc.TokensIn, tc.TokensOut, tc.Cost, tc.LatencyMs, tc.Model, tc.Provider,
			tc.ExitCode, isErrorSQL(tc.IsError), tc.Runtime, tc.LatencyState, tc.LatencyUnit, tc.LatencyAuthority, tc.Producer,
		)
		if err != nil {
			return fmt.Errorf("db: insert retry-safe tool_call %q: %w", tc.ID, err)
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("db: inspect retry-safe tool_call %q: %w", tc.ID, err)
		}
		didInsert = inserted != 0

		if !didInsert {
			var tool, input, output string
			var runtime sql.NullString
			if err := tx.QueryRowContext(ctx,
				`SELECT tool, input, output, runtime FROM tool_calls WHERE id = ?`, tc.ID,
			).Scan(&tool, &input, &output, &runtime); err != nil {
				return fmt.Errorf("db: inspect existing tool_call %q: %w", tc.ID, err)
			}
			if reason := conflictReason(tool, input, output, runtime, tc); reason != "" {
				return fmt.Errorf("%w: %q %s", ErrToolCallConflict, tc.ID, reason)
			}
			return nil
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET tool_call_count = tool_call_count + 1, last_seen_at = ? WHERE id = ?`,
			tc.Timestamp, tc.SessionID,
		); err != nil {
			return fmt.Errorf("db: update retry-safe session roll-up %q: %w", tc.SessionID, err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return didInsert, nil
}

func (w *HookWriter) write(ctx context.Context, fn func(*sql.Tx) error) error {
	return withWriteTx(ctx, w.db.db, fn)
}

func (w *HookWriter) PersistToolCall(tc *ToolCall) error {
	if err := normalizeLatencyEvidence(tc); err != nil {
		return fmt.Errorf("db: persist tool_call %q: %w", tc.ID, err)
	}
	return w.db.persistToolCallPrepared(tc, w.write)
}

func (w *HookWriter) PersistToolCallOnce(tc *ToolCall) (bool, error) {
	if err := normalizeLatencyEvidence(tc); err != nil {
		return false, fmt.Errorf("db: persist tool_call %q: %w", tc.ID, err)
	}
	return w.db.persistToolCallOncePrepared(tc, w.write)
}

func (w *HookWriter) InsertSession(s *Session) error {
	return w.db.insertSession(s, w.write)
}

func (w *HookWriter) IdentifySession(id, agent string) error {
	return w.db.identifySession(id, agent, w.write)
}

func (w *HookWriter) CloseSession(id, endedAt string) error {
	return w.db.closeSession(id, endedAt, w.write)
}

func sameOptionalString(stored sql.NullString, incoming *string) bool {
	if incoming == nil {
		return !stored.Valid
	}
	return stored.Valid && stored.String == *incoming
}

// conflictReason returns a non-empty reason string when the incoming
// ToolCall's authoritative evidence differs from the stored row, or "" when
// the retry is an exact match (including canonically-equivalent input). Tool,
// output, and runtime are compared exactly; input uses the strict canonical
// JSON comparison so differently formatted but semantically equal JSON inputs
// converge instead of conflicting.
func conflictReason(tool, input, output string, runtime sql.NullString, tc *ToolCall) string {
	switch {
	case tool != tc.Tool:
		return "tool differs"
	case output != tc.Output:
		return "output differs"
	case !sameOptionalString(runtime, tc.Runtime):
		return "runtime differs"
	case !canon.Equal([]byte(input), []byte(tc.Input)):
		return "input differs"
	default:
		return ""
	}
}

// ValidateToolCallRetry performs the read-only half of hook retry handling.
// It reports an exact retained source without starting a write transaction.
func (d *DB) ValidateToolCallRetry(tc *ToolCall) (sessionID string, exists bool, err error) {
	var tool, input, output string
	var runtime sql.NullString
	err = d.db.QueryRow(
		`SELECT session_id, tool, input, output, runtime FROM tool_calls WHERE id = ?`, tc.ID,
	).Scan(&sessionID, &tool, &input, &output, &runtime)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: inspect existing tool_call %q: %w", tc.ID, err)
	}
	if reason := conflictReason(tool, input, output, runtime, tc); reason != "" {
		return "", true, fmt.Errorf("%w: %q %s", ErrToolCallConflict, tc.ID, reason)
	}
	return sessionID, true, nil
}

// GetToolCall fetches a tool_call row by id. It returns ErrNotFound when no
// row matches so the CLI layer can map "not found" to exit code 2.
// The returned struct carries the persisted shape including the
// cost-publication fields (tokens_in, tokens_out, cost, latency_ms,
// model, provider), the failure-signal fields (exit_code, is_error), and
// the runtime and producer fields. The columns are nullable; the Scan uses
// sql.NullInt64 / sql.NullFloat64 / sql.NullString intermediates so
// NULL is converted back to a nil pointer on the ToolCall struct,
// preserving the 'field absent' distinction. A non-NULL column with
// value 0 round-trips to a non-nil pointer to 0; callers can detect
// 'absent' (nil) vs 'set to zero' (&0) without ambiguity.
func (d *DB) GetToolCall(id string) (*ToolCall, error) {
	var tc ToolCall
	var (
		tokensIn                                    sql.NullInt64
		tokensOut                                   sql.NullInt64
		cost                                        sql.NullFloat64
		latencyMs                                   sql.NullInt64
		model                                       sql.NullString
		provider                                    sql.NullString
		exitCode                                    sql.NullInt64
		isError                                     sql.NullInt64
		runtime                                     sql.NullString
		latencyState, latencyUnit, latencyAuthority string
		producer                                    sql.NullString
	)
	columns, err := tableColumns(d.db, "tool_calls")
	if err != nil {
		return nil, fmt.Errorf("db: inspect tool_calls: %w", err)
	}
	query := `SELECT id, session_id, tool, input, output, content_hash, timestamp, event_type,
		        tokens_in, tokens_out, cost, latency_ms, model, provider,
		        exit_code, is_error, runtime, latency_state, latency_unit, latency_authority, producer
		 FROM tool_calls WHERE id = ?`
	dest := []any{
		&tc.ID, &tc.SessionID, &tc.Tool, &tc.Input, &tc.Output,
		&tc.ContentHash, &tc.Timestamp, &tc.EventType,
		&tokensIn, &tokensOut, &cost, &latencyMs, &model, &provider,
		&exitCode, &isError, &runtime, &latencyState, &latencyUnit, &latencyAuthority, &producer,
	}
	if _, ok := columns["latency_state"]; !ok {
		query = `SELECT id, session_id, tool, input, output, content_hash, timestamp, event_type, tokens_in, tokens_out, cost, latency_ms, model, provider, exit_code, is_error, runtime FROM tool_calls WHERE id = ?`
		dest = dest[:17]
		latencyState, latencyUnit, latencyAuthority = LatencyStateUnknown, LatencyUnitMilliseconds, LatencyAuthorityUnknown
	}
	err = d.db.QueryRow(query, id).Scan(dest...)
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
	tc.LatencyState = latencyState
	tc.LatencyUnit = latencyUnit
	tc.LatencyAuthority = latencyAuthority
	if model.Valid {
		v := model.String
		tc.Model = &v
	}
	if provider.Valid {
		v := provider.String
		tc.Provider = &v
	}
	if exitCode.Valid {
		v := exitCode.Int64
		tc.ExitCode = &v
	}
	if isError.Valid {
		v := isError.Int64 != 0
		tc.IsError = &v
	}
	if runtime.Valid {
		v := runtime.String
		tc.Runtime = &v
	}
	if producer.Valid {
		v := Producer(producer.String)
		tc.Producer = &v
	}
	return &tc, nil
}

// isErrorSQL maps a nullable *bool to the INTEGER column value SQLite
// expects: nil → SQL NULL, &false → 0, &true → 1.
func isErrorSQL(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return int64(1)
	}
	return int64(0)
}

// GetSession fetches a session row by id. It returns ErrNotFound when no
// row matches. `ktx read` uses it for cross-table ID lookup.
//
// Nullable text columns (agent, started_at, ended_at, last_seen_at) are
// coalesced to the empty string in SQL so the Session struct can stay
// string-typed and consistent with how ToolCall represents missing values.
// An empty EndedAt in the output remains a meaningful signal that the
// session is still open.
func (d *DB) GetSession(id string) (*Session, error) {
	s, err := scanSession(d.db.QueryRow(
		`SELECT id,
		        COALESCE(agent, ''),
		        agent_attribution_authority,
		        COALESCE(started_at, ''),
		        COALESCE(ended_at, ''),
		        COALESCE(last_seen_at, ''),
		        tool_call_count
		 FROM sessions WHERE id = ?`,
		id,
	))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get session %q: %w", id, err)
	}
	return s, nil
}

// GetRecord fetches a record by id from either the tool_calls or sessions
// table. Both tables are addressed by 16-character IDs.
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
// This is the minimal list: no cost/token filters and no
// pagination. Ordered by started_at so the table view shows
// the oldest session first, matching the convention of other log-style
// tools.
func (d *DB) ListSessions() ([]Session, error) {
	rows, err := d.db.Query(
		`SELECT id,
		        COALESCE(agent, ''),
		        agent_attribution_authority,
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
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scan session: %w", err)
		}
		sessions = append(sessions, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate sessions: %w", err)
	}
	return sessions, nil
}

func scanSession(row rowScanner) (*Session, error) {
	var s Session
	if err := row.Scan(&s.ID, &s.Agent, &s.AgentAttributionAuthority, &s.StartedAt, &s.EndedAt, &s.LastSeenAt, &s.ToolCallCount); err != nil {
		return nil, err
	}
	return &s, nil
}

// deriveCost fills tc.Cost from cached provider and model prices when the
// caller omitted it.
//
// Soft-fail: Derive returns nil on every branch that does not
// derive. tc.Cost is left untouched in that case (the caller's
// original value, or nil), and the row persists with whatever cost
// the caller supplied (or NULL).
//
// The mutex-free read on tc is safe because the caller owns the
// pointer (writeToolCall / teeAndParse build a fresh ToolCall and
// hand it to PersistToolCall). The Prices cache is itself
// concurrency-safe (sync.RWMutex inside prices.go).
func (d *DB) deriveCost(tc *ToolCall) {
	if tc == nil {
		return
	}
	// A nil price cache or empty cache path means price discovery failed.
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
