package db

import (
	"database/sql"
	"fmt"
)

// SessionCosts is the per-session cost-publication rollup surfaced by
// `ktx list`. Each field is a pointer so the absent-vs-zero
// distinction survives the JSON output: a session with no underlying
// call contributing to a field has a nil pointer; a session where
// the non-NULL contributions sum to zero has a non-nil pointer to 0.
type SessionCosts struct {
	Tokens *int64   `json:"tokens"`
	Cost   *float64 `json:"cost"`
	Model  *string  `json:"model"`
}

// SessionCostsRollups returns cost data keyed by session ID. Sessions with
// no non-NULL cost fields are absent from the result. Token totals include a
// call only when both token fields are present; cost sums ignore NULL values;
// MIN selects a stable model from grouped rows. Pointer fields preserve the
// distinction between absent data and a measured zero.
func (d *DB) SessionCostsRollups() (map[string]SessionCosts, error) {
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
		return nil, fmt.Errorf("db: session cost rollups: %w", err)
	}
	defer rows.Close()

	out := make(map[string]SessionCosts)
	for rows.Next() {
		var (
			sessionID string
			tokens    sql.NullInt64
			cost      sql.NullFloat64
			model     sql.NullString
		)
		if err := rows.Scan(&sessionID, &tokens, &cost, &model); err != nil {
			return nil, fmt.Errorf("db: scan cost rollup: %w", err)
		}
		roll := SessionCosts{}
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
		return nil, fmt.Errorf("db: iterate cost rollups: %w", err)
	}
	return out, nil
}
