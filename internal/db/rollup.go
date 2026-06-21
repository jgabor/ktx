// Per-session M2 cost-publication rollups for `ktx list` (Task 5).
//
// ktx list is the session-level summary command (Decision 7). M1 surfaced
// tool_call_count per session via the sessions.tool_call_count column;
// M2 adds a per-session rollup of the cost-publication fields. The
// rollup is a single GROUP BY query that aggregates the M2 columns
// across every tool_call in a session:
//
//   - Tokens: SUM(tokens_in + tokens_out) over the session's tool_calls.
//     SQLite's SUM ignores NULL operands, so a row with either field
//     NULL contributes NULL to the per-call sum and is excluded from
//     the aggregate. This matches the spec's "per-call roll-up of
//     non-NULL values" — the per-call pairing of tokens_in +
//     tokens_out is the unit of the rollup.
//   - Cost: SUM(cost) over the session's tool_calls. AC2's mixed-NULL
//     case (1 NULL + 2 non-NULL) sums only the non-NULL values.
//   - Model: MIN(model) over the session's tool_calls. SQLite's MIN
//     is deterministic and returns NULL when every value is NULL.
//     "First non-NULL model" in the plan is interpreted as "any
//     non-NULL model, deterministic" — the GROUP BY aggregation has
//     no natural row order, so MIN/MAX are the SQL-idiomatic choices
//     and both produce a stable, non-NULL model when at least one
//     call has a non-NULL model.
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
package db

import (
	"database/sql"
	"fmt"
)

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
