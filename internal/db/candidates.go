// Residual-waste discovery: raw row query for the candidates command.
//
// The query uses a LEFT JOIN from tool_calls to compressed so
// uncompressed calls surface with delivered bytes equal to raw
// bytes. When multiple compressed rows exist for the same source,
// the most recent (by timestamp) is used. The binary name is
// extracted in the CLI layer (reusing router.BinaryFromInput) so
// the extraction logic stays in one place.
package db

import "fmt"

// CandidateRow is one tool_call row from the candidates query. The
// CLI layer groups these by (Tool, extracted binary name) to
// produce the final ranked output.
type CandidateRow struct {
	Tool           string
	Input          string
	RawBytes       int
	DeliveredBytes int
}

// CandidateRows returns one row per tool_call with the raw output
// length and the delivered (compressed) output length. When no
// compressed row exists, delivered equals raw. When multiple
// compressed rows exist for the same source, the most recent by
// timestamp is used.
func (d *DB) CandidateRows() ([]CandidateRow, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("db: candidates: database is nil")
	}

	query := `
		SELECT tc.tool,
		       tc.input,
		       LENGTH(tc.output),
		       COALESCE(
			(SELECT LENGTH(c.output) FROM compressed c
			 WHERE c.source_id = tc.id
			 ORDER BY c.timestamp DESC LIMIT 1),
			LENGTH(tc.output)
		       )
		FROM tool_calls tc
		WHERE tc.output IS NOT NULL
	`
	rows, err := d.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("db: candidates query: %w", err)
	}
	defer rows.Close()

	var result []CandidateRow
	for rows.Next() {
		var r CandidateRow
		if err := rows.Scan(&r.Tool, &r.Input, &r.RawBytes, &r.DeliveredBytes); err != nil {
			return nil, fmt.Errorf("db: candidates scan: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: candidates iterate: %w", err)
	}
	return result, nil
}
